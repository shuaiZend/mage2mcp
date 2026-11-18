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

// ProductRow is one item-level ecommerce row.
type ProductRow struct {
	ItemID           string  `json:"item_id"`
	ItemsViewed      float64 `json:"items_viewed"`
	ItemsAddedToCart float64 `json:"items_added_to_cart"`
	ItemsPurchased   float64 `json:"items_purchased"`
	ItemRevenue      float64 `json:"item_revenue"`
}

// ProductPerformance is the item-level report envelope.
type ProductPerformance struct {
	Products        []ProductRow          `json:"products"`
	Currency        string                `json:"currency"`
	TimeZone        string                `json:"time_zone"`
	Sampled         bool                  `json:"sampled"`
	CapabilityCheck *PropertyCapabilities `json:"capability_check,omitempty"`
}

// TopProducts reports item-level views, carts, purchases, and revenue.
//
// Like EcommerceOverview it probes capability first: a property without
// ecommerce tracking returns ecommerce_not_tracked instead of zeros.
func (s *Service) TopProducts(ctx context.Context, start, end string, limit int64) (*ProductPerformance, error) {
	const op = "ga4.ecommerce.products"
	capabilities, err := s.Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	if !capabilities.EcommerceTracked {
		return nil, errs.New(errs.CodeEcommerceNotTracked,
			"this GA4 property does not track ecommerce events; item metrics would be zeros, not data").
			WithOp(op)
	}
	if start == "" {
		start = defaultEcommerceStart
	}
	if end == "" {
		end = defaultEcommerceEnd
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	metrics := []string{"itemsViewed", "itemsAddedToCart", "itemsPurchased", "itemRevenue"}
	if err := Validate([]string{"itemId"}, metrics); err != nil {
		return nil, err
	}
	response, err := s.client.runReport(ctx, ReportRequest{
		Dimensions: []string{"itemId"},
		Metrics:    metrics,
		DateRanges: []DateRange{{StartDate: start, EndDate: end}},
		Limit:      limit,
		OrderBys:   []OrderBy{{Metric: "itemRevenue", Desc: true}},
	})
	if err != nil {
		return nil, err
	}
	out := &ProductPerformance{
		Currency: response.Metadata.CurrencyCode, TimeZone: response.Metadata.TimeZone,
		Sampled: response.Metadata.Sampled, CapabilityCheck: capabilities,
	}
	for _, row := range response.Rows {
		product := ProductRow{}
		if len(row.Dimensions) > 0 {
			product.ItemID = row.Dimensions[0]
		}
		if len(row.Metrics) >= 4 {
			product.ItemsViewed, product.ItemsAddedToCart = row.Metrics[0], row.Metrics[1]
			product.ItemsPurchased, product.ItemRevenue = row.Metrics[2], row.Metrics[3]
		}
		out.Products = append(out.Products, product)
	}
	return out, nil
}
