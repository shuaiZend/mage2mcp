// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	defaultGeographyOrders = 5000
	maxGeographyOrders     = 10000
	geographyPageSize      = 200
)

// CountryStat is the PII-free per-country aggregate of an order window.
type CountryStat struct {
	CountryID string  `json:"country_id"`
	Orders    int     `json:"orders"`
	Revenue   float64 `json:"revenue"`
}

// OrderGeography aggregates orders by shipping country over a bounded window.
// Only country codes, counts, and revenue are returned; names, streets, cities,
// postcodes, and phone numbers are never read into the result.
type OrderGeography struct {
	From           string        `json:"from"`
	To             string        `json:"to"`
	OrdersScanned  int           `json:"orders_scanned"`
	TotalAvailable int           `json:"total_available"`
	Truncated      bool          `json:"truncated"`
	TotalOrders    int           `json:"total_orders"`
	TotalRevenue   float64       `json:"total_revenue"`
	Countries      []CountryStat `json:"countries"`
}

// OrderGeography aggregates orders created in [from, to) by the country of their
// shipping address.
func (s *Service) OrderGeography(ctx context.Context, from, to time.Time, maxOrders int) (*OrderGeography, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento order service is not configured")
	}
	if !to.After(from) {
		return nil, invalidf("to must be after from")
	}
	if maxOrders <= 0 {
		maxOrders = defaultGeographyOrders
	}
	if maxOrders > maxGeographyOrders {
		return nil, invalidf("max_orders must be at most %d", maxGeographyOrders)
	}
	result := &OrderGeography{From: from.Format(time.RFC3339), To: to.Format(time.RFC3339)}
	counts := make(map[string]*CountryStat)
	for page := 1; result.OrdersScanned < maxOrders; page++ {
		query := url.Values{}
		query.Set("searchCriteria[pageSize]", strconv.Itoa(geographyPageSize))
		query.Set("searchCriteria[currentPage]", strconv.Itoa(page))
		query.Set("searchCriteria[filter_groups][0][filters][0][field]", "created_at")
		query.Set("searchCriteria[filter_groups][0][filters][0][value]", from.UTC().Format("2006-01-02 15:04:05"))
		query.Set("searchCriteria[filter_groups][0][filters][0][condition_type]", "gteq")
		query.Set("searchCriteria[filter_groups][0][filters][1][field]", "created_at")
		query.Set("searchCriteria[filter_groups][0][filters][1][value]", to.UTC().Format("2006-01-02 15:04:05"))
		query.Set("searchCriteria[filter_groups][0][filters][1][condition_type]", "lt")
		query.Set("searchCriteria[sortOrders][0][field]", "created_at")
		query.Set("searchCriteria[sortOrders][0][direction]", "desc")
		raw, err := s.client.do(ctx, http.MethodGet, "/orders", query, nil)
		if err != nil {
			return nil, err
		}
		var payload struct {
			TotalCount int `json:"total_count"`
			Items      []struct {
				GrandTotal          float64 `json:"grand_total"`
				ExtensionAttributes struct {
					ShippingAssignments []struct {
						Shipping struct {
							Address struct {
								CountryID string `json:"country_id"`
							} `json:"address"`
						} `json:"shipping"`
					} `json:"shipping_assignments"`
				} `json:"extension_attributes"`
			} `json:"items"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable order payload").WithCause(err)
		}
		if page == 1 {
			result.TotalAvailable = payload.TotalCount
		}
		if len(payload.Items) == 0 {
			break
		}
		for _, order := range payload.Items {
			if result.OrdersScanned >= maxOrders {
				result.Truncated = true
				break
			}
			country := "UNKNOWN"
			if len(order.ExtensionAttributes.ShippingAssignments) > 0 {
				if code := order.ExtensionAttributes.ShippingAssignments[0].Shipping.Address.CountryID; code != "" {
					country = code
				}
			}
			stat := counts[country]
			if stat == nil {
				stat = &CountryStat{CountryID: country}
				counts[country] = stat
			}
			stat.Orders++
			stat.Revenue += order.GrandTotal
			result.TotalOrders++
			result.TotalRevenue += order.GrandTotal
			result.OrdersScanned++
		}
		if len(payload.Items) < geographyPageSize {
			break
		}
	}
	result.Truncated = result.TotalAvailable > result.OrdersScanned
	result.Countries = make([]CountryStat, 0, len(counts))
	for _, stat := range counts {
		result.Countries = append(result.Countries, *stat)
	}
	sortCountryStats(result.Countries)
	return result, nil
}

func sortCountryStats(stats []CountryStat) {
	for i := 1; i < len(stats); i++ {
		for j := i; j > 0 && stats[j].Orders > stats[j-1].Orders; j-- {
			stats[j], stats[j-1] = stats[j-1], stats[j]
		}
	}
}
