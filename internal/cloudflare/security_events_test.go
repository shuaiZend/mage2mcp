// SPDX-License-Identifier: Apache-2.0

package cloudflare

import (
	"context"
	"strings"
	"testing"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestEventsMapsRows(t *testing.T) {
	server, recorder := newFakeGraphQL(t, func(graphqlCall) string {
		return `{"data":{"viewer":{"zones":[{"firewallEventsAdaptive":[
			{"datetime":"2026-11-17T10:00:00Z","action":"block","ruleId":"r1","clientCountryName":"CN","clientRequestPath":"/admin","userAgent":"curl"},
			{"datetime":"2026-11-17T09:00:00Z","action":"challenge","ruleId":"","clientCountryName":"US","clientRequestPath":"/","userAgent":"Mozilla"}
		]}]}}}`
	})
	svc := NewService(mustClient(t, testConfig(server.URL)))
	since := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	page, err := svc.Events(context.Background(), since, since.Add(24*time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[0].Action != "block" || page.Events[0].Country != "CN" {
		t.Fatalf("events = %+v", page.Events)
	}
	if !page.Sampled || page.RetentionDays != 31 || page.Truncated {
		t.Fatalf("metadata = %+v", page)
	}
	call := recorder.last(t)
	if !strings.Contains(call.Query, "firewallEventsAdaptive") || call.Variables["limit"] != float64(100) {
		t.Fatalf("unexpected query/variables: %+v", call)
	}
}

func TestEventsValidation(t *testing.T) {
	server, _ := newFakeGraphQL(t, func(graphqlCall) string { return `{"data":{"viewer":{"zones":[]}}}` })
	svc := NewService(mustClient(t, testConfig(server.URL)))
	since := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	if _, err := svc.Events(context.Background(), since, since.Add(24*time.Hour), 5000); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("limit error = %v", err)
	}
	if _, err := svc.Events(context.Background(), since, since.Add(40*24*time.Hour), 10); err == nil {
		t.Fatal("oversized window accepted")
	}
}
