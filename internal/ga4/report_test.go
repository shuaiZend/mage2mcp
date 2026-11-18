// SPDX-License-Identifier: Apache-2.0

package ga4

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const cannedMetadata = `{
  "dimensions": [{"apiName": "date"}, {"apiName": "itemId"}, {"apiName": ""}],
  "metrics": [{"apiName": "sessions"}, {"apiName": "totalUsers"}]
}`

// trafficRequestBody mirrors the Data API wire shape asserted by report tests.
type trafficRequestBody struct {
	Dimensions []struct {
		Name string `json:"name"`
	} `json:"dimensions"`
	Metrics []struct {
		Name string `json:"name"`
	} `json:"metrics"`
	DateRanges []DateRange `json:"dateRanges"`
	Limit      int64       `json:"limit"`
	OrderBys   []struct {
		Dimension struct {
			DimensionName string `json:"dimensionName"`
		} `json:"dimension"`
	} `json:"orderBys"`
	MetricAggregations []string `json:"metricAggregations"`
}

func TestTrafficReport(t *testing.T) {
	var received trafficRequestBody
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/properties/"+testPropertyID+":runReport" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, cannedReport)
	})

	service := newTestService(t, handler)
	result, err := service.Traffic(context.Background(), TrafficQuery{Metrics: []string{"sessions", "totalUsers"}})
	if err != nil {
		t.Fatalf("Traffic: %v", err)
	}
	if len(received.Dimensions) != 1 || received.Dimensions[0].Name != "date" {
		t.Fatalf("dimensions = %+v", received.Dimensions)
	}
	if len(received.Metrics) != 2 || received.Metrics[0].Name != "sessions" || received.Metrics[1].Name != "totalUsers" {
		t.Fatalf("metrics = %+v", received.Metrics)
	}
	if len(received.DateRanges) != 1 || received.DateRanges[0] != (DateRange{StartDate: "7daysAgo", EndDate: "yesterday"}) {
		t.Fatalf("dateRanges = %+v", received.DateRanges)
	}
	if received.Limit != trafficRowLimit {
		t.Fatalf("limit = %d", received.Limit)
	}
	if len(received.OrderBys) != 1 || received.OrderBys[0].Dimension.DimensionName != "date" {
		t.Fatalf("orderBys = %+v", received.OrderBys)
	}
	if len(received.MetricAggregations) != 1 || received.MetricAggregations[0] != "TOTAL" {
		t.Fatalf("metricAggregations = %+v", received.MetricAggregations)
	}

	if len(result.Rows) != 2 {
		t.Fatalf("rows = %d", len(result.Rows))
	}
	if result.Rows[0].Date != "2026-11-12" || result.Rows[0].Metrics["sessions"] != 120 || result.Rows[0].Metrics["totalUsers"] != 90 {
		t.Fatalf("first row = %+v", result.Rows[0])
	}
	if result.Rows[1].Date != "2026-11-13" || result.Rows[1].Metrics["sessions"] != 130 {
		t.Fatalf("second row = %+v", result.Rows[1])
	}
	if result.Totals["sessions"] != 250 || result.Totals["totalUsers"] != 185 {
		t.Fatalf("totals = %+v", result.Totals)
	}
	if result.TimeZone != "America/Los_Angeles" || result.Currency != "USD" || !result.Sampled {
		t.Fatalf("envelope = %+v", result)
	}
}

func TestTrafficCustomRange(t *testing.T) {
	var received trafficRequestBody
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, cannedReport)
	})
	service := newTestService(t, handler)
	if _, err := service.Traffic(context.Background(), TrafficQuery{
		StartDate: "2026-11-01",
		EndDate:   "2026-11-07",
		Metrics:   []string{"sessions"},
	}); err != nil {
		t.Fatalf("Traffic: %v", err)
	}
	if len(received.DateRanges) != 1 || received.DateRanges[0] != (DateRange{StartDate: "2026-11-01", EndDate: "2026-11-07"}) {
		t.Fatalf("dateRanges = %+v", received.DateRanges)
	}
}

func TestTrafficRejectsInvalidQuery(t *testing.T) {
	cases := []struct {
		name   string
		query  TrafficQuery
		reason string
	}{
		{"unknown metric", TrafficQuery{Metrics: []string{"purchaseRevenue"}}, "unsupported_metric"},
		{"missing metrics", TrafficQuery{}, "missing_metric"},
		{"half range", TrafficQuery{StartDate: "2026-11-01", Metrics: []string{"sessions"}}, "invalid_range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("the Data API must not be called for an invalid query")
			})
			service := newTestService(t, handler)
			_, err := service.Traffic(context.Background(), tc.query)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := errs.CodeOf(err); got != errs.CodeValidationFailed {
				t.Fatalf("code = %s, want %s (%v)", got, errs.CodeValidationFailed, err)
			}
			if got := errorDetail(t, err, "reason"); got != tc.reason {
				t.Fatalf("reason = %q, want %q", got, tc.reason)
			}
		})
	}
}

func TestTrafficDeduplicatesMetrics(t *testing.T) {
	var received trafficRequestBody
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, cannedReport)
	})
	service := newTestService(t, handler)
	if _, err := service.Traffic(context.Background(), TrafficQuery{Metrics: []string{"sessions", "sessions"}}); err != nil {
		t.Fatalf("Traffic: %v", err)
	}
	if len(received.Metrics) != 1 {
		t.Fatalf("metrics = %+v", received.Metrics)
	}
}

func TestMetadata(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q", r.Method)
		}
		if r.URL.Path != "/v1beta/properties/"+testPropertyID+"/metadata" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, cannedMetadata)
	})
	service := newTestService(t, handler)
	metadata, err := service.Metadata(context.Background())
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if len(metadata.Dimensions) != 2 || metadata.Dimensions[0] != "date" || metadata.Dimensions[1] != "itemId" {
		t.Fatalf("dimensions = %+v", metadata.Dimensions)
	}
	if len(metadata.Metrics) != 2 || metadata.Metrics[0] != "sessions" || metadata.Metrics[1] != "totalUsers" {
		t.Fatalf("metrics = %+v", metadata.Metrics)
	}
}

func TestMetadataErrorMapping(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"User does not have sufficient permissions."}}`)
	})
	service := newTestService(t, handler)
	_, err := service.Metadata(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := errs.CodeOf(err); got != errs.CodePermissionDenied {
		t.Fatalf("code = %s, want %s", got, errs.CodePermissionDenied)
	}
}
