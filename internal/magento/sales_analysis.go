// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	defaultAnalysisOrders = 10000
	maxAnalysisOrders     = 20000
	analysisPageSize      = 500
	topSKULimit           = 30
)

// DimensionStat is one grouped measure (status, country, payment, store).
type DimensionStat struct {
	Key     string  `json:"key"`
	Orders  int     `json:"orders"`
	Revenue float64 `json:"revenue"`
}

// DayStat is one day's order count and revenue.
type DayStat struct {
	Date    string  `json:"date"`
	Orders  int     `json:"orders"`
	Revenue float64 `json:"revenue"`
}

// SKUStat is one product's sales inside the window.
type SKUStat struct {
	SKU     string  `json:"sku"`
	Name    string  `json:"name"`
	Qty     float64 `json:"qty"`
	Revenue float64 `json:"revenue"`
	Orders  int     `json:"orders"`
}

// SalesAnalysis is a PII-free business summary of an order window.
type SalesAnalysis struct {
	From           string          `json:"from"`
	To             string          `json:"to"`
	Orders         int             `json:"orders"`
	TotalAvailable int             `json:"total_available"`
	Truncated      bool            `json:"truncated"`
	Revenue        float64         `json:"revenue"`
	Subtotal       float64         `json:"subtotal"`
	Discount       float64         `json:"discount"`
	Shipping       float64         `json:"shipping"`
	Tax            float64         `json:"tax"`
	ItemsQty       float64         `json:"items_qty"`
	AOV            float64         `json:"aov"`
	GuestOrders    int             `json:"guest_orders"`
	RefundedAmount float64         `json:"refunded_amount"`
	RefundedQty    float64         `json:"refunded_qty"`
	Currencies     []DimensionStat `json:"currencies"`
	ByDay          []DayStat       `json:"by_day"`
	ByStatus       []DimensionStat `json:"by_status"`
	ByCountry      []DimensionStat `json:"by_country"`
	ByPayment      []DimensionStat `json:"by_payment"`
	ByStore        []DimensionStat `json:"by_store"`
	TopSKUs        []SKUStat       `json:"top_skus"`
}

type analysisOrderItem struct {
	SKU            string  `json:"sku"`
	Name           string  `json:"name"`
	ProductType    string  `json:"product_type"`
	ParentItemID   *int    `json:"parent_item_id"`
	QtyOrdered     float64 `json:"qty_ordered"`
	QtyRefunded    float64 `json:"qty_refunded"`
	RowTotal       float64 `json:"row_total"`
	AmountRefunded float64 `json:"amount_refunded"`
}

type analysisOrder struct {
	EntityID       int     `json:"entity_id"`
	CreatedAt      string  `json:"created_at"`
	Status         string  `json:"status"`
	GrandTotal     float64 `json:"grand_total"`
	Subtotal       float64 `json:"subtotal"`
	DiscountAmount float64 `json:"discount_amount"`
	ShippingAmount float64 `json:"shipping_amount"`
	TaxAmount      float64 `json:"tax_amount"`
	TotalQty       float64 `json:"total_qty_ordered"`
	Currency       string  `json:"order_currency_code"`
	StoreName      string  `json:"store_name"`
	IsGuest        int     `json:"customer_is_guest"`
	Payment        struct {
		Method string `json:"method"`
	} `json:"payment"`
	ExtensionAttributes struct {
		ShippingAssignments []struct {
			Shipping struct {
				Address struct {
					CountryID string `json:"country_id"`
				} `json:"address"`
			} `json:"shipping"`
		} `json:"shipping_assignments"`
	} `json:"extension_attributes"`
	Items []analysisOrderItem `json:"items"`
}

// AnalyzeSales aggregates a window of orders into business metrics. Only
// aggregate counts and amounts are returned; customer PII never enters the
// result.
func (s *Service) AnalyzeSales(ctx context.Context, from, to time.Time, maxOrders int) (*SalesAnalysis, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento order service is not configured")
	}
	if !to.After(from) {
		return nil, invalidf("to must be after from")
	}
	if maxOrders <= 0 {
		maxOrders = defaultAnalysisOrders
	}
	if maxOrders > maxAnalysisOrders {
		return nil, invalidf("max_orders must be at most %d", maxAnalysisOrders)
	}
	result := &SalesAnalysis{From: from.Format(time.RFC3339), To: to.Format(time.RFC3339)}
	days := map[string]*DayStat{}
	statuses := map[string]*DimensionStat{}
	countries := map[string]*DimensionStat{}
	payments := map[string]*DimensionStat{}
	stores := map[string]*DimensionStat{}
	currencies := map[string]*DimensionStat{}
	skus := map[string]*SKUStat{}
	skuOrders := map[string]map[int]bool{}

	for page := 1; result.Orders < maxOrders; page++ {
		query := analysisQuery(from, to, page)
		raw, err := s.client.do(ctx, http.MethodGet, "/orders", query, nil)
		if err != nil {
			return nil, err
		}
		var payload struct {
			TotalCount int             `json:"total_count"`
			Items      []analysisOrder `json:"items"`
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
			if result.Orders >= maxOrders {
				result.Truncated = true
				break
			}
			result.Orders++
			result.Revenue += order.GrandTotal
			result.Subtotal += order.Subtotal
			result.Discount += order.DiscountAmount
			result.Shipping += order.ShippingAmount
			result.Tax += order.TaxAmount
			result.ItemsQty += order.TotalQty
			if order.IsGuest == 1 {
				result.GuestOrders++
			}
			day := order.CreatedAt
			if len(day) >= 10 {
				day = day[:10]
			}
			addDay(days, day, order.GrandTotal)
			addDimension(statuses, order.Status, order.GrandTotal)
			country := "UNKNOWN"
			if len(order.ExtensionAttributes.ShippingAssignments) > 0 {
				if code := order.ExtensionAttributes.ShippingAssignments[0].Shipping.Address.CountryID; code != "" {
					country = code
				}
			}
			addDimension(countries, country, order.GrandTotal)
			addDimension(payments, order.Payment.Method, order.GrandTotal)
			store := order.StoreName
			if store == "" {
				store = "unknown"
			}
			addDimension(stores, store, order.GrandTotal)
			addDimension(currencies, order.Currency, order.GrandTotal)
			for _, item := range order.Items {
				if item.ParentItemID != nil {
					continue // configurable child rows duplicate the parent's amount
				}
				result.RefundedAmount += item.AmountRefunded
				result.RefundedQty += item.QtyRefunded
				stat := skus[item.SKU]
				if stat == nil {
					stat = &SKUStat{SKU: item.SKU, Name: item.Name}
					skus[item.SKU] = stat
					skuOrders[item.SKU] = map[int]bool{}
				}
				stat.Qty += item.QtyOrdered
				stat.Revenue += item.RowTotal
				if !skuOrders[item.SKU][order.EntityID] {
					skuOrders[item.SKU][order.EntityID] = true
					stat.Orders++
				}
			}
		}
		if len(payload.Items) < analysisPageSize {
			break
		}
	}
	result.Truncated = result.Truncated || result.TotalAvailable > result.Orders
	if result.Orders > 0 {
		result.AOV = result.Revenue / float64(result.Orders)
	}
	result.ByDay = sortedDays(days)
	result.ByStatus = sortedDimensions(statuses)
	result.ByCountry = sortedDimensions(countries)
	result.ByPayment = sortedDimensions(payments)
	result.ByStore = sortedDimensions(stores)
	result.Currencies = sortedDimensions(currencies)
	result.TopSKUs = topSKUs(skus)
	return result, nil
}

func analysisQuery(from, to time.Time, page int) url.Values {
	query := url.Values{}
	query.Set("searchCriteria[pageSize]", strconv.Itoa(analysisPageSize))
	query.Set("searchCriteria[currentPage]", strconv.Itoa(page))
	query.Set("searchCriteria[filter_groups][0][filters][0][field]", "created_at")
	query.Set("searchCriteria[filter_groups][0][filters][0][value]", from.UTC().Format("2006-01-02 15:04:05"))
	query.Set("searchCriteria[filter_groups][0][filters][0][condition_type]", "gteq")
	query.Set("searchCriteria[filter_groups][1][filters][0][field]", "created_at")
	query.Set("searchCriteria[filter_groups][1][filters][0][value]", to.UTC().Format("2006-01-02 15:04:05"))
	query.Set("searchCriteria[filter_groups][1][filters][0][condition_type]", "lt")
	query.Set("searchCriteria[sortOrders][0][field]", "created_at")
	query.Set("searchCriteria[sortOrders][0][direction]", "asc")
	return query
}

func addDay(days map[string]*DayStat, date string, revenue float64) {
	stat := days[date]
	if stat == nil {
		stat = &DayStat{Date: date}
		days[date] = stat
	}
	stat.Orders++
	stat.Revenue += revenue
}

func addDimension(dims map[string]*DimensionStat, key string, revenue float64) {
	if key == "" {
		key = "unknown"
	}
	stat := dims[key]
	if stat == nil {
		stat = &DimensionStat{Key: key}
		dims[key] = stat
	}
	stat.Orders++
	stat.Revenue += revenue
}

func sortedDays(days map[string]*DayStat) []DayStat {
	out := make([]DayStat, 0, len(days))
	for _, stat := range days {
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

func sortedDimensions(dims map[string]*DimensionStat) []DimensionStat {
	out := make([]DimensionStat, 0, len(dims))
	for _, stat := range dims {
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Orders != out[j].Orders {
			return out[i].Orders > out[j].Orders
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func topSKUs(skus map[string]*SKUStat) []SKUStat {
	out := make([]SKUStat, 0, len(skus))
	for _, stat := range skus {
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Revenue != out[j].Revenue {
			return out[i].Revenue > out[j].Revenue
		}
		return out[i].SKU < out[j].SKU
	})
	if len(out) > topSKULimit {
		out = out[:topSKULimit]
	}
	return out
}
