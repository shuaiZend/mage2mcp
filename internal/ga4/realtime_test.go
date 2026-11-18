// SPDX-License-Identifier: Apache-2.0

package ga4

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const cannedRealtimeReport = `{
  "rows": [
    {"dimensionValues": [{"value": "page_view"}], "metricValues": [{"value": "12"}]},
    {"dimensionValues": [{"value": "(other)"}], "metricValues": [{"value": "3"}]}
  ],
  "rowCount": 2,
  "metadata": {"currencyCode": "USD", "timeZone": "America/Los_Angeles"}
}`

// realtimeProbeRequest mirrors the realtime report wire shape.
type realtimeProbeRequest struct {
	Dimensions []struct {
		Name string `json:"name"`
	} `json:"dimensions"`
	Metrics []struct {
		Name string `json:"name"`
	} `json:"metrics"`
	Limit int64 `json:"limit"`
}

// realtimeAPI serves a canned realtime report and records the request.
func realtimeAPI(t *testing.T, body string, received *realtimeProbeRequest) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q", r.Method)
		}
		if r.URL.Path != "/v1beta/properties/"+testPropertyID+":runRealtimeReport" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(received); err != nil {
			t.Errorf("decode realtime body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
}

func TestRealtimeReport(t *testing.T) {
	var received realtimeProbeRequest
	service := newTestService(t, realtimeAPI(t, cannedRealtimeReport, &received))
	result, err := service.Realtime(context.Background(), RealtimeQuery{
		Dimensions: []string{"eventName"},
		Metrics:    []string{"eventCount"},
		Limit:      50,
	})
	if err != nil {
		t.Fatalf("Realtime: %v", err)
	}
	if len(received.Dimensions) != 1 || received.Dimensions[0].Name != "eventName" {
		t.Fatalf("dimensions = %+v", received.Dimensions)
	}
	if len(received.Metrics) != 1 || received.Metrics[0].Name != "eventCount" {
		t.Fatalf("metrics = %+v", received.Metrics)
	}
	if received.Limit != 50 {
		t.Fatalf("limit = %d", received.Limit)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("rows = %+v", result.Rows)
	}
	if result.Rows[0].Date != "page_view" || result.Rows[0].Metrics["eventCount"] != 12 {
		t.Fatalf("first row = %+v", result.Rows[0])
	}
	if result.Rows[1].Date != "(other)" || result.Rows[1].Metrics["eventCount"] != 3 {
		t.Fatalf("second row = %+v", result.Rows[1])
	}
	if result.TimeZone != "America/Los_Angeles" || result.Currency != "USD" {
		t.Fatalf("metadata = %+v", result)
	}
	if result.Totals == nil {
		t.Fatal("totals must be an empty map, not nil")
	}
}

func TestRealtimeMultipleDimensions(t *testing.T) {
	body := `{"rows":[{"dimensionValues":[{"value":"page_view"},{"value":"US"}],"metricValues":[{"value":"5"}]}],` +
		`"rowCount":1,"metadata":{"currencyCode":"USD","timeZone":"UTC"}}`
	var received realtimeProbeRequest
	service := newTestService(t, realtimeAPI(t, body, &received))
	result, err := service.Realtime(context.Background(), RealtimeQuery{
		Dimensions: []string{"eventName", "country"},
		Metrics:    []string{"eventCount"},
	})
	if err != nil {
		t.Fatalf("Realtime: %v", err)
	}
	if len(received.Dimensions) != 2 || received.Dimensions[1].Name != "country" {
		t.Fatalf("dimensions = %+v", received.Dimensions)
	}
	if len(result.Rows) != 1 || result.Rows[0].Date != "page_view | US" {
		t.Fatalf("rows = %+v", result.Rows)
	}
}

func TestRealtimeRowCap(t *testing.T) {
	var received realtimeProbeRequest
	service := newTestService(t, realtimeAPI(t, cannedRealtimeReport, &received))
	if _, err := service.Realtime(context.Background(), RealtimeQuery{
		Metrics: []string{"activeUsers"},
		Limit:   500,
	}); err != nil {
		t.Fatalf("Realtime: %v", err)
	}
	if received.Limit != maxRealtimeLimit {
		t.Fatalf("limit = %d, want %d", received.Limit, maxRealtimeLimit)
	}
	// A missing or non-positive limit falls back to the default cap.
	if _, err := service.Realtime(context.Background(), RealtimeQuery{
		Metrics: []string{"activeUsers"},
	}); err != nil {
		t.Fatalf("Realtime: %v", err)
	}
	if received.Limit != defaultRealtimeLimit {
		t.Fatalf("default limit = %d, want %d", received.Limit, defaultRealtimeLimit)
	}
}

func TestRealtimeRejectsInvalidCombination(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the Data API must not be called for an invalid realtime combination")
	})
	service := newTestService(t, handler)
	_, err := service.Realtime(context.Background(), RealtimeQuery{
		Dimensions: []string{"eventName"},
		Metrics:    []string{"activeUsers"},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := errs.CodeOf(err); got != errs.CodeValidationFailed {
		t.Fatalf("code = %s, want %s (%v)", got, errs.CodeValidationFailed, err)
	}
	if got := errorDetail(t, err, "reason"); got != "incompatible" {
		t.Fatalf("reason = %q", got)
	}
	if got := errorDetail(t, err, "offending_field"); got != "activeUsers" {
		t.Fatalf("offending_field = %q", got)
	}
}

func TestRealtimeRejectsUnsupportedFields(t *testing.T) {
	cases := []struct {
		name       string
		query      RealtimeQuery
		wantReason string
	}{
		{"unsupported dimension", RealtimeQuery{Dimensions: []string{"pagePath"}, Metrics: []string{"activeUsers"}}, "unsupported_dimension"},
		{"unsupported metric", RealtimeQuery{Metrics: []string{"purchaseRevenue"}}, "unsupported_metric"},
		{"missing metric", RealtimeQuery{Dimensions: []string{"country"}}, "missing_metric"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("the Data API must not be called for an invalid realtime query")
			})
			service := newTestService(t, handler)
			_, err := service.Realtime(context.Background(), tc.query)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := errs.CodeOf(err); got != errs.CodeValidationFailed {
				t.Fatalf("code = %s, want %s (%v)", got, errs.CodeValidationFailed, err)
			}
			if got := errorDetail(t, err, "reason"); got != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got, tc.wantReason)
			}
		})
	}
}

func TestRealtimeDeduplicatesFields(t *testing.T) {
	var received realtimeProbeRequest
	service := newTestService(t, realtimeAPI(t, cannedRealtimeReport, &received))
	if _, err := service.Realtime(context.Background(), RealtimeQuery{
		Dimensions: []string{"country", "country"},
		Metrics:    []string{"eventCount", "eventCount"},
	}); err != nil {
		t.Fatalf("Realtime: %v", err)
	}
	if len(received.Dimensions) != 1 || len(received.Metrics) != 1 {
		t.Fatalf("dimensions/metrics = %+v / %+v", received.Dimensions, received.Metrics)
	}
}

func TestRealtimeErrorMapping(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantCode  errs.Code
		retryable bool
		reason    string
	}{
		{
			name:      "429",
			status:    http.StatusTooManyRequests,
			body:      `{"error":{"message":"Quota exceeded for quota metric 'Tokens per day'."}}`,
			wantCode:  errs.CodeRateLimited,
			retryable: true,
		},
		{
			name:     "403",
			status:   http.StatusForbidden,
			body:     `{"error":{"message":"User does not have sufficient permissions for this property."}}`,
			wantCode: errs.CodePermissionDenied,
		},
		{
			name:     "400 incompatible",
			status:   http.StatusBadRequest,
			body:     `{"error":{"message":"Please remove eventCount to make the request compatible for example."}}`,
			wantCode: errs.CodeValidationFailed,
			reason:   "incompatible",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			service := newTestService(t, handler)
			_, err := service.Realtime(context.Background(), RealtimeQuery{Metrics: []string{"activeUsers"}})
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := errs.CodeOf(err); got != tc.wantCode {
				t.Fatalf("code = %s, want %s (%v)", got, tc.wantCode, err)
			}
			if got := errs.RetryableError(err); got != tc.retryable {
				t.Fatalf("retryable = %v, want %v", got, tc.retryable)
			}
			if tc.reason != "" && errorDetail(t, err, "reason") != tc.reason {
				t.Fatalf("reason = %q", errorDetail(t, err, "reason"))
			}
		})
	}
}

func TestRealtimeUnreadableResponse(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{`)
	})
	service := newTestService(t, handler)
	_, err := service.Realtime(context.Background(), RealtimeQuery{Metrics: []string{"activeUsers"}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := errs.CodeOf(err); got != errs.CodeUpstreamUnavailable {
		t.Fatalf("code = %s, want %s", got, errs.CodeUpstreamUnavailable)
	}
	if !strings.Contains(err.Error(), "unreadable realtime") {
		t.Fatalf("message = %v", err)
	}
}
