// SPDX-License-Identifier: Apache-2.0

package ga4

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	capabilityPurchasesAbsent  = `{"rows":[],"rowCount":0,"metadata":{"currencyCode":"USD","timeZone":"America/Los_Angeles"}}`
	capabilityPurchasesPresent = `{"rows":[{"dimensionValues":[{"value":"2026-11-12"}],"metricValues":[{"value":"9"}]}],` +
		`"rowCount":1,"metadata":{"currencyCode":"USD","timeZone":"America/Los_Angeles"}}`
	capabilityEventsAbsent  = `{"rows":[],"rowCount":0,"metadata":{"currencyCode":"USD","timeZone":"America/Los_Angeles"}}`
	capabilityEventsPresent = `{"rows":[` +
		`{"dimensionValues":[{"value":"view_item"}],"metricValues":[{"value":"420"}]},` +
		`{"dimensionValues":[{"value":"purchase"}],"metricValues":[{"value":"7"}]},` +
		`{"dimensionValues":[{"value":"view_item_list"}],"metricValues":[{"value":"0"}]}],` +
		`"rowCount":3,"metadata":{"currencyCode":"USD","timeZone":"America/Los_Angeles"}}`
)

// capabilityProbeRequest mirrors the report wire shape the probe sends.
type capabilityProbeRequest struct {
	Dimensions []struct {
		Name string `json:"name"`
	} `json:"dimensions"`
	Metrics []struct {
		Name string `json:"name"`
	} `json:"metrics"`
	DateRanges      []DateRange    `json:"dateRanges"`
	DimensionFilter map[string]any `json:"dimensionFilter"`
	Limit           int64          `json:"limit"`
}

// capabilityAPI serves the two capability probes and records the requests.
func capabilityAPI(t *testing.T, purchases, events string, seen *[]capabilityProbeRequest) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/properties/"+testPropertyID+":runReport" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var request capabilityProbeRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode probe body: %v", err)
			return
		}
		if seen != nil {
			*seen = append(*seen, request)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case len(request.Metrics) == 1 && request.Metrics[0].Name == "ecommercePurchases":
			_, _ = io.WriteString(w, purchases)
		case len(request.Metrics) == 1 && request.Metrics[0].Name == "eventCount":
			_, _ = io.WriteString(w, events)
		default:
			t.Errorf("unexpected probe metrics: %+v", request.Metrics)
			http.Error(w, "unexpected probe", http.StatusBadRequest)
		}
	})
}

// inListEventNames extracts the eventName in-list filter values.
func inListEventNames(t *testing.T, filter map[string]any) []string {
	t.Helper()
	outer, ok := filter["filter"].(map[string]any)
	if !ok {
		t.Fatalf("dimension filter = %+v", filter)
	}
	if got := outer["fieldName"]; got != "eventName" {
		t.Fatalf("filter fieldName = %v", got)
	}
	inList, ok := outer["inListFilter"].(map[string]any)
	if !ok {
		t.Fatalf("inListFilter = %+v", outer)
	}
	rawValues, ok := inList["values"].([]any)
	if !ok {
		t.Fatalf("filter values = %+v", inList)
	}
	values := make([]string, 0, len(rawValues))
	for _, raw := range rawValues {
		value, ok := raw.(string)
		if !ok {
			t.Fatalf("filter value = %v", raw)
		}
		values = append(values, value)
	}
	return values
}

func TestCapabilitiesTracked(t *testing.T) {
	var seen []capabilityProbeRequest
	service := newTestService(t, capabilityAPI(t, capabilityPurchasesPresent, capabilityEventsPresent, &seen))
	capabilities, err := service.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if !capabilities.EcommerceTracked {
		t.Fatalf("EcommerceTracked = false, evidence: %v", capabilities.Evidence)
	}
	if capabilities.Currency != "USD" || capabilities.TimeZone != "America/Los_Angeles" {
		t.Fatalf("metadata = %+v", capabilities)
	}
	if capabilities.CheckedAt.IsZero() {
		t.Fatal("CheckedAt must be set")
	}
	if len(capabilities.Evidence) < 3 {
		t.Fatalf("evidence = %v", capabilities.Evidence)
	}
	joined := ""
	for _, line := range capabilities.Evidence {
		joined += line + "\n"
	}
	for _, want := range []string{"view_item", "purchase", "9 purchases", "configured"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("evidence missing %q: %s", want, joined)
		}
	}

	if len(seen) != 2 {
		t.Fatalf("probe requests = %d, want 2", len(seen))
	}
	purchasesProbe, eventsProbe := seen[0], seen[1]
	if len(purchasesProbe.Dimensions) != 1 || purchasesProbe.Dimensions[0].Name != "date" {
		t.Fatalf("purchases probe dimensions = %+v", purchasesProbe.Dimensions)
	}
	if len(purchasesProbe.Metrics) != 1 || purchasesProbe.Metrics[0].Name != "ecommercePurchases" {
		t.Fatalf("purchases probe metrics = %+v", purchasesProbe.Metrics)
	}
	if len(eventsProbe.Dimensions) != 1 || eventsProbe.Dimensions[0].Name != "eventName" {
		t.Fatalf("events probe dimensions = %+v", eventsProbe.Dimensions)
	}
	for _, probe := range seen {
		if len(probe.DateRanges) != 1 || probe.DateRanges[0] != (DateRange{StartDate: capabilityStartDate, EndDate: capabilityEndDate}) {
			t.Fatalf("probe date range = %+v", probe.DateRanges)
		}
		if probe.Limit != capabilityRowLimit {
			t.Fatalf("probe limit = %d", probe.Limit)
		}
	}
	values := inListEventNames(t, eventsProbe.DimensionFilter)
	if len(values) != len(standardEcommerceEvents) {
		t.Fatalf("filter values = %v", values)
	}
	for _, event := range standardEcommerceEvents {
		if !slices.Contains(values, event) {
			t.Fatalf("filter is missing %q: %v", event, values)
		}
	}
}

func TestCapabilitiesAbsent(t *testing.T) {
	service := newTestService(t, capabilityAPI(t, capabilityPurchasesAbsent, capabilityEventsAbsent, nil))
	capabilities, err := service.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if capabilities.EcommerceTracked {
		t.Fatalf("EcommerceTracked = true, evidence: %v", capabilities.Evidence)
	}
	if capabilities.Currency != "USD" || capabilities.TimeZone != "America/Los_Angeles" {
		t.Fatalf("metadata = %+v", capabilities)
	}
	joined := ""
	for _, line := range capabilities.Evidence {
		joined += line + "\n"
	}
	for _, want := range []string{"0 rows", "absent"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("evidence missing %q: %s", want, joined)
		}
	}
}

func TestCapabilitiesPurchasesWithoutEvents(t *testing.T) {
	// Purchases alone do not make a property ecommerce-tracked: the decision
	// requires a standard ecommerce event with a positive count.
	service := newTestService(t, capabilityAPI(t, capabilityPurchasesPresent, capabilityEventsAbsent, nil))
	capabilities, err := service.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if capabilities.EcommerceTracked {
		t.Fatal("EcommerceTracked must be false when no ecommerce event fired")
	}
	found := false
	for _, line := range capabilities.Evidence {
		if strings.Contains(line, "9 purchases") {
			found = true
		}
	}
	if !found {
		t.Fatalf("evidence must report the purchases probe: %v", capabilities.Evidence)
	}
}

func TestCapabilitiesMetadataFallback(t *testing.T) {
	eventsWithoutMetadata := `{"rows":[],"rowCount":0}`
	purchasesWithMetadata := `{"rows":[],"rowCount":0,"metadata":{"currencyCode":"EUR","timeZone":"Europe/Berlin"}}`
	service := newTestService(t, capabilityAPI(t, purchasesWithMetadata, eventsWithoutMetadata, nil))
	capabilities, err := service.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if capabilities.Currency != "EUR" || capabilities.TimeZone != "Europe/Berlin" {
		t.Fatalf("metadata fallback = %+v", capabilities)
	}
}

func TestCapabilitiesProbeError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capabilityProbeRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		if len(request.Metrics) == 1 && request.Metrics[0].Name == "ecommercePurchases" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, capabilityPurchasesAbsent)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"User does not have sufficient permissions."}}`)
	})
	service := newTestService(t, handler)
	_, err := service.Capabilities(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := errs.CodeOf(err); got != errs.CodePermissionDenied {
		t.Fatalf("code = %s, want %s", got, errs.CodePermissionDenied)
	}
}

// containsString is a tiny readability helper for evidence assertions.
// indexOf reports the first index of needle in haystack, or -1.
