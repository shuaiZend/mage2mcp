// SPDX-License-Identifier: Apache-2.0

package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// graphqlRecorder records the GraphQL requests a fake server receives.
type graphqlRecorder struct {
	mu    sync.Mutex
	calls []graphqlCall
}

func (r *graphqlRecorder) record(call graphqlCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *graphqlRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *graphqlRecorder) last(t *testing.T) graphqlCall {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		t.Fatal("no GraphQL request was recorded")
	}
	return r.calls[len(r.calls)-1]
}

// newFakeGraphQL starts a GraphQL endpoint that records requests and replies
// with the JSON body produced by reply.
func newFakeGraphQL(t *testing.T, reply func(graphqlCall) string) (*httptest.Server, *graphqlRecorder) {
	t.Helper()
	recorder := &graphqlRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/graphql" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		var call graphqlCall
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		recorder.record(call)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply(call))
	}))
	t.Cleanup(server.Close)
	return server, recorder
}

func TestTrafficMapsRows(t *testing.T) {
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	until := since.Add(24 * time.Hour)
	tests := []struct {
		groupBy   string
		alias     string
		valueJSON string
		want      string
	}{
		{"hostname", "hostname: clientRequestHTTPHost", `"www.example.com"`, "www.example.com"},
		{"country", "country: clientCountryName", `"US"`, "US"},
		{"status", "status: edgeResponseStatus", `403`, "403"},
	}
	for _, tc := range tests {
		t.Run(tc.groupBy, func(t *testing.T) {
			server, recorder := newFakeGraphQL(t, func(graphqlCall) string {
				return fmt.Sprintf(`{"data":{"viewer":{"zones":[{"httpRequests1hGroups":[{"dimensions":{%q:%s},"sum":{"requests":120,"bytes":4096,"cachedRequests":100,"cachedBytes":3000}}]}]}}}`,
					tc.groupBy, tc.valueJSON)
			})
			service := NewService(mustClient(t, testConfig(server.URL)))
			rows, err := service.Traffic(context.Background(), TrafficQuery{
				Since: since, Until: until, GroupBy: tc.groupBy, Limit: 10,
			})
			if err != nil {
				t.Fatalf("Traffic: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("rows = %d", len(rows))
			}
			row := rows[0]
			if row.Dimension != tc.want {
				t.Fatalf("dimension = %q, want %q", row.Dimension, tc.want)
			}
			if row.Requests != 120 || row.Bytes != 4096 || row.CachedRequests != 100 || row.CachedBytes != 3000 {
				t.Fatalf("unexpected row: %+v", row)
			}

			call := recorder.last(t)
			for _, want := range []string{
				"httpRequests1hGroups",
				tc.alias,
				"datetime_geq: $since",
				"datetime_lt: $until",
				"limit: $limit",
				"orderBy: [sum_requests_DESC]",
			} {
				if !strings.Contains(call.Query, want) {
					t.Fatalf("query missing %q:\n%s", want, call.Query)
				}
			}
			if got := call.Variables["zoneTag"]; got != "zone-test" {
				t.Fatalf("zoneTag = %#v", got)
			}
			if got := call.Variables["since"]; got != "2026-11-01T00:00:00Z" {
				t.Fatalf("since = %#v", got)
			}
			if got := call.Variables["until"]; got != "2026-11-02T00:00:00Z" {
				t.Fatalf("until = %#v", got)
			}
			if got, ok := call.Variables["limit"].(float64); !ok || got != 10 {
				t.Fatalf("limit = %#v", call.Variables["limit"])
			}
		})
	}
}

func TestTrafficValidation(t *testing.T) {
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	server, recorder := newFakeGraphQL(t, func(graphqlCall) string {
		return `{"errors":[{"message":"must not be reached"}]}`
	})
	service := NewService(mustClient(t, testConfig(server.URL)))
	tests := []struct {
		name  string
		query TrafficQuery
	}{
		{"bad group_by", TrafficQuery{Since: since, Until: since.Add(time.Hour), GroupBy: "path", Limit: 10}},
		{"zero limit", TrafficQuery{Since: since, Until: since.Add(time.Hour), GroupBy: "hostname", Limit: 0}},
		{"limit too large", TrafficQuery{Since: since, Until: since.Add(time.Hour), GroupBy: "hostname", Limit: 1001}},
		{"window too long", TrafficQuery{Since: since, Until: since.Add(32 * 24 * time.Hour), GroupBy: "hostname", Limit: 10}},
		{"until before since", TrafficQuery{Since: since, Until: since.Add(-time.Hour), GroupBy: "hostname", Limit: 10}},
		{"zero times", TrafficQuery{GroupBy: "hostname", Limit: 10}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.Traffic(context.Background(), tc.query); errs.CodeOf(err) != errs.CodeInvalidArgument {
				t.Fatalf("code = %s, want %s (err=%v)", errs.CodeOf(err), errs.CodeInvalidArgument, err)
			}
		})
	}
	if recorder.count() != 0 {
		t.Fatalf("invalid queries reached upstream: %d", recorder.count())
	}
}

func TestTrafficGraphQLError(t *testing.T) {
	server, _ := newFakeGraphQL(t, func(graphqlCall) string {
		return `{"errors":[{"message":"query time range is too large"}],"data":null}`
	})
	service := NewService(mustClient(t, testConfig(server.URL)))
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	_, err := service.Traffic(context.Background(), TrafficQuery{
		Since: since, Until: since.Add(time.Hour), GroupBy: "hostname", Limit: 10,
	})
	if errs.CodeOf(err) != errs.CodeUpstreamUnavailable {
		t.Fatalf("code = %s, want %s", errs.CodeOf(err), errs.CodeUpstreamUnavailable)
	}
}

func TestTrafficEmptyZones(t *testing.T) {
	server, _ := newFakeGraphQL(t, func(graphqlCall) string {
		return `{"data":{"viewer":{"zones":[]}}}`
	})
	service := NewService(mustClient(t, testConfig(server.URL)))
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	rows, err := service.Traffic(context.Background(), TrafficQuery{
		Since: since, Until: since.Add(time.Hour), GroupBy: "hostname", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Traffic: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %d", len(rows))
	}
}

func TestSummaryAggregatesByAction(t *testing.T) {
	server, recorder := newFakeGraphQL(t, func(graphqlCall) string {
		return `{"data":{"viewer":{"zones":[{"firewallEventsAdaptiveGroups":[
			{"count":7,"dimensions":{"action":"block"}},
			{"count":3,"dimensions":{"action":"managed_challenge"}},
			{"count":2,"dimensions":{"action":"block"}},
			{"count":1,"dimensions":{}}
		]}]}}}`
	})
	service := NewService(mustClient(t, testConfig(server.URL)))
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	until := since.Add(24 * time.Hour)
	summary, err := service.Summary(context.Background(), since, until)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.Actions["block"] != 9 {
		t.Fatalf("block = %d", summary.Actions["block"])
	}
	if summary.Actions["managed_challenge"] != 3 {
		t.Fatalf("managed_challenge = %d", summary.Actions["managed_challenge"])
	}
	if summary.Total != 12 {
		t.Fatalf("total = %d", summary.Total)
	}
	if !summary.Sampled {
		t.Fatal("security summary must be marked sampled")
	}
	if summary.RetentionDays != 31 {
		t.Fatalf("retention = %d", summary.RetentionDays)
	}
	if summary.Window != "2026-11-01T00:00:00Z/2026-11-02T00:00:00Z" {
		t.Fatalf("window = %q", summary.Window)
	}

	call := recorder.last(t)
	for _, want := range []string{
		"firewallEventsAdaptiveGroups",
		"limit: 100",
		"datetime_geq: $since",
		"datetime_lt: $until",
		"dimensions { action }",
	} {
		if !strings.Contains(call.Query, want) {
			t.Fatalf("query missing %q:\n%s", want, call.Query)
		}
	}
	if got := call.Variables["zoneTag"]; got != "zone-test" {
		t.Fatalf("zoneTag = %#v", got)
	}
}

func TestSummaryValidation(t *testing.T) {
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	server, recorder := newFakeGraphQL(t, func(graphqlCall) string {
		return `{"errors":[{"message":"must not be reached"}]}`
	})
	service := NewService(mustClient(t, testConfig(server.URL)))
	tests := []struct {
		name         string
		since, until time.Time
	}{
		{"window too long", since, since.Add(31 * 24 * time.Hour)},
		{"until before since", since, since.Add(-time.Hour)},
		{"zero times", time.Time{}, time.Time{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.Summary(context.Background(), tc.since, tc.until); errs.CodeOf(err) != errs.CodeInvalidArgument {
				t.Fatalf("code = %s, want %s (err=%v)", errs.CodeOf(err), errs.CodeInvalidArgument, err)
			}
		})
	}
	if recorder.count() != 0 {
		t.Fatalf("invalid windows reached upstream: %d", recorder.count())
	}
}

func TestSummaryEmptyZones(t *testing.T) {
	server, _ := newFakeGraphQL(t, func(graphqlCall) string {
		return `{"data":{"viewer":{"zones":[]}}}`
	})
	service := NewService(mustClient(t, testConfig(server.URL)))
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	summary, err := service.Summary(context.Background(), since, since.Add(time.Hour))
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.Total != 0 || len(summary.Actions) != 0 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if summary.Actions == nil {
		t.Fatal("actions map must not be nil")
	}
}
