// SPDX-License-Identifier: Apache-2.0

package ga4

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// standardEcommerceEvents is the fixed set of standard GA4 ecommerce events the
// capability probe filters on.
var standardEcommerceEvents = []string{
	"view_item", "add_to_cart", "begin_checkout", "purchase", "refund",
	"add_payment_info", "add_shipping_info", "view_cart", "remove_from_cart",
	"select_item", "view_item_list",
}

const (
	capabilityStartDate = "7daysAgo"
	capabilityEndDate   = "yesterday"
	capabilityRowLimit  = 100
)

// PropertyCapabilities is the ecommerce capability profile of one property.
// EcommerceTracked is true only when at least one standard ecommerce event has
// a positive count in the probe window; Evidence records what the probes saw.
type PropertyCapabilities struct {
	EcommerceTracked bool      `json:"ecommerce_tracked" jsonschema:"True when the property has ecommerce tracking enabled."`
	Currency         string    `json:"currency" jsonschema:"ISO 4217 code the amounts are expressed in."`
	TimeZone         string    `json:"time_zone" jsonschema:"IANA time zone the report was computed in."`
	CheckedAt        time.Time `json:"checked_at" jsonschema:"When this capability snapshot was taken."`
	Evidence         []string  `json:"evidence" jsonschema:"How the capability was determined."`
}

// Capabilities probes the property for ecommerce tracking over the last seven
// complete days. It runs two aggregate-only reports: ecommercePurchases by date
// and eventCount by eventName filtered to the eleven standard ecommerce events.
// A property with traffic but no ecommerce events returns EcommerceTracked=false
// and no error; the currency and time zone always come from report metadata.
func (s *Service) Capabilities(ctx context.Context) (*PropertyCapabilities, error) {
	purchases, err := s.client.runReport(ctx, ReportRequest{
		Dimensions: []string{"date"},
		Metrics:    []string{"ecommercePurchases"},
		DateRanges: []DateRange{{StartDate: capabilityStartDate, EndDate: capabilityEndDate}},
		Limit:      capabilityRowLimit,
	})
	if err != nil {
		return nil, err
	}
	events, err := s.client.runReport(ctx, ReportRequest{
		Dimensions:      []string{"eventName"},
		Metrics:         []string{"eventCount"},
		DateRanges:      []DateRange{{StartDate: capabilityStartDate, EndDate: capabilityEndDate}},
		Limit:           capabilityRowLimit,
		DimensionFilter: ecommerceEventFilter(),
	})
	if err != nil {
		return nil, err
	}
	tracked, fired := positiveEvents(events.Rows)
	evidence := []string{
		eventProbeEvidence(fired),
		purchasesProbeEvidence(purchases.Rows),
	}
	if tracked {
		evidence = append(evidence, "ecommerce tracking is configured: at least one standard ecommerce event has a positive count")
	} else {
		evidence = append(evidence, "ecommerce tracking is absent: no standard ecommerce event has a positive count over the last 7 days")
	}
	return &PropertyCapabilities{
		EcommerceTracked: tracked,
		Currency:         firstNonEmpty(events.Metadata.CurrencyCode, purchases.Metadata.CurrencyCode),
		TimeZone:         firstNonEmpty(events.Metadata.TimeZone, purchases.Metadata.TimeZone),
		CheckedAt:        time.Now().UTC(),
		Evidence:         evidence,
	}, nil
}

func ecommerceEventFilter() map[string]any {
	return map[string]any{
		"filter": map[string]any{
			"fieldName":    "eventName",
			"inListFilter": map[string]any{"values": slices.Clone(standardEcommerceEvents), "caseSensitive": true},
		},
	}
}

// positiveEvents returns whether any probe row has a positive event count and
// the names of the events that fired.
func positiveEvents(rows []Row) (bool, []string) {
	fired := make([]string, 0, len(rows))
	for _, row := range rows {
		if len(row.Metrics) == 0 || row.Metrics[0] <= 0 {
			continue
		}
		name := "(unnamed event)"
		if len(row.Dimensions) > 0 && row.Dimensions[0] != "" {
			name = row.Dimensions[0]
		}
		fired = append(fired, name)
	}
	return len(fired) > 0, fired
}

// eventProbeEvidence describes what the eventName probe observed.
func eventProbeEvidence(fired []string) string {
	if len(fired) == 0 {
		return fmt.Sprintf("eventName probe returned 0 rows with a positive count for the %d standard ecommerce events over the last 7 days", len(standardEcommerceEvents))
	}
	shown := fired
	if len(shown) > 5 {
		shown = shown[:5]
	}
	names := strings.Join(shown, ", ")
	if len(fired) > len(shown) {
		names += ", ..."
	}
	return fmt.Sprintf("eventName probe found %d standard ecommerce event(s) with positive counts over the last 7 days: %s", len(fired), names)
}

// purchasesProbeEvidence describes what the ecommercePurchases probe observed.
func purchasesProbeEvidence(rows []Row) string {
	if len(rows) == 0 {
		return "ecommercePurchases probe returned 0 rows over the last 7 days"
	}
	total := 0.0
	for _, row := range rows {
		if len(row.Metrics) > 0 {
			total += row.Metrics[0]
		}
	}
	if total > 0 {
		return fmt.Sprintf("ecommercePurchases probe reported %.0f purchases over the last 7 days", total)
	}
	return "ecommercePurchases probe returned rows but reported zero purchases over the last 7 days"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
