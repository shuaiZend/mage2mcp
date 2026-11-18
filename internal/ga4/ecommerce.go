// SPDX-License-Identifier: Apache-2.0

package ga4

import (
	"context"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// EcommerceOverview is a date-grouped revenue report. It is only produced when
// the property is known to track ecommerce events; otherwise the gateway
// returns ecommerce_not_tracked instead of a table of zeros.
type EcommerceOverview struct {
	Rows            []TrafficRow          `json:"rows"`
	Totals          map[string]float64    `json:"totals"`
	TimeZone        string                `json:"time_zone"`
	Currency        string                `json:"currency"`
	Sampled         bool                  `json:"sampled"`
	CapabilityCheck *PropertyCapabilities `json:"capability_check,omitempty"`
}

const (
	defaultEcommerceStart = "7daysAgo"
	defaultEcommerceEnd   = "yesterday"
	ecommerceRowLimit     = 1000
)

// EcommerceOverview reports purchases and revenue per day.
//
// The property capability probe runs first: a property without ecommerce
// tracking returns ecommerce_not_tracked rather than zeros that look like a
// real business result.
func (s *Service) EcommerceOverview(ctx context.Context, start, end string) (*EcommerceOverview, error) {
	const op = "ga4.ecommerce.overview"
	capabilities, err := s.Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	if !capabilities.EcommerceTracked {
		return nil, errs.New(errs.CodeEcommerceNotTracked,
			"this GA4 property does not track ecommerce events; revenue metrics would be zeros, not data").
			WithOp(op).
			WithDetail("property_evidence", capabilities.Evidence[0])
	}
	if start == "" {
		start = defaultEcommerceStart
	}
	if end == "" {
		end = defaultEcommerceEnd
	}
	metrics := []string{"ecommercePurchases", "purchaseRevenue", "averagePurchaseRevenue"}
	if err := Validate([]string{"date"}, metrics); err != nil {
		return nil, err
	}
	response, err := s.client.runReport(ctx, ReportRequest{
		Dimensions:         []string{"date"},
		Metrics:            metrics,
		DateRanges:         []DateRange{{StartDate: start, EndDate: end}},
		Limit:              ecommerceRowLimit,
		MetricAggregations: []string{"TOTAL"},
	})
	if err != nil {
		return nil, err
	}
	result := newReportResult(response, metrics)
	return &EcommerceOverview{
		Rows: result.Rows, Totals: result.Totals, TimeZone: result.TimeZone,
		Currency: result.Currency, Sampled: result.Sampled, CapabilityCheck: capabilities,
	}, nil
}
