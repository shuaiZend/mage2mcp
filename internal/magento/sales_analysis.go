// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	defaultAnalysisOrders = 10000
	maxAnalysisOrders     = 20000
	analysisPageSize      = 500
	topSKULimit           = 50
	topCouponLimit        = 10
	topRefundSKULimit     = 10
	shipmentLookaheadDays = 7

	// Paging is described to the caller, not hidden: the scan uses Magento's
	// currentPage (SQL OFFSET) paging, which is not a database snapshot.
	analysisPagingMethod = "offset"
	analysisPagingOrder  = "created_at asc, entity_id asc"
	analysisPagingCaveat = "offset paging is not snapshot-consistent: an order inserted or deleted while the scan runs can shift a page boundary; duplicate entity ids are dropped and any shortfall is reported in excluded_orders"
)

// PagingInfo states how an order window was scanned so a caller can judge
// completeness without guessing.
type PagingInfo struct {
	// Method is the paging mechanism ("offset").
	Method string `json:"method" jsonschema:"How the scan was paged: offset or cursor."`
	// OrderBy is the sort key the scan requested.
	OrderBy string `json:"order_by" jsonschema:"Field the scan was ordered by."`
	// SnapshotConsistent reports whether the scan observed a fixed snapshot.
	// It is false for offset paging.
	SnapshotConsistent bool `json:"snapshot_consistent" jsonschema:"True when the pages came from one consistent snapshot."`
	// DuplicatesSkipped counts orders returned on more than one page and
	// dropped, so an unstable page boundary cannot double-count revenue.
	DuplicatesSkipped int `json:"duplicates_skipped" jsonschema:"Duplicate rows that were skipped rather than counted twice."`
	// Caveat explains how a caller must read the completeness fields.
	Caveat string `json:"caveat,omitempty" jsonschema:"Known limitation of how the scan was paged."`
}

// DimensionStat is one grouped measure (status, country, payment, store).
type DimensionStat struct {
	Key     string  `json:"key"`
	Orders  int     `json:"orders" jsonschema:"Orders that matched."`
	Revenue float64 `json:"revenue" jsonschema:"Revenue for this window."`
}

// ShippingStat is one shipping method's volume and cost.
type ShippingStat struct {
	Method       string  `json:"method" jsonschema:"How the scan was paged: offset or cursor."`
	Orders       int     `json:"orders" jsonschema:"Orders that matched."`
	Revenue      float64 `json:"revenue" jsonschema:"Revenue for this window."`
	ShippingCost float64 `json:"shipping_cost"`
}

// CurrencyStat is one currency's orders, revenue, and AOV.
type CurrencyStat struct {
	Code    string  `json:"code" jsonschema:"Machine code of the entity."`
	Orders  int     `json:"orders" jsonschema:"Orders that matched."`
	Revenue float64 `json:"revenue" jsonschema:"Revenue for this window."`
	AOV     float64 `json:"aov" jsonschema:"Average order value in the window."`
}

// BandStat is one order-value band.
type BandStat struct {
	Label   string  `json:"label" jsonschema:"Label shown to the shopper."`
	Orders  int     `json:"orders" jsonschema:"Orders that matched."`
	Revenue float64 `json:"revenue" jsonschema:"Revenue for this window."`
}

// CouponStat is one normalized coupon code.
type CouponStat struct {
	Code   string `json:"code" jsonschema:"Machine code of the entity."`
	Orders int    `json:"orders" jsonschema:"Orders that matched."`
}

// SKURefundStat is one SKU's refunded quantity and amount.
type SKURefundStat struct {
	SKU    string  `json:"sku" jsonschema:"Product SKU."`
	Qty    float64 `json:"qty" jsonschema:"Quantity at this source."`
	Amount float64 `json:"amount"`
}

// ShipLatency is the order-to-shipment latency distribution in days.
type ShipLatency struct {
	Avg    float64 `json:"avg" jsonschema:"Average value."`
	Median float64 `json:"median" jsonschema:"Median value."`
	P90    float64 `json:"p90" jsonschema:"90th percentile."`
	Max    float64 `json:"max" jsonschema:"Upper bound of the band."`
}

// DayStat is one day's order count and revenue.
type DayStat struct {
	Date    string  `json:"date" jsonschema:"Calendar day, YYYY-MM-DD."`
	Orders  int     `json:"orders" jsonschema:"Orders that matched."`
	Revenue float64 `json:"revenue" jsonschema:"Revenue for this window."`
}

// SKUStat is one product's sales inside the window.
type SKUStat struct {
	SKU     string  `json:"sku" jsonschema:"Product SKU."`
	Name    string  `json:"name" jsonschema:"Display name."`
	Qty     float64 `json:"qty" jsonschema:"Quantity at this source."`
	Revenue float64 `json:"revenue" jsonschema:"Revenue for this window."`
	Orders  int     `json:"orders" jsonschema:"Orders that matched."`
}

// SalesAnalysis is a PII-free business summary of an order window.
//
// Orders is the number of orders aggregated, TotalAvailable is what Magento
// reported for the window, and ExcludedOrders is the difference. Paging states
// how the window was scanned and whether the result can be treated as complete.
type SalesAnalysis struct {
	From            string          `json:"from" jsonschema:"Start of the comparison window."`
	To              string          `json:"to" jsonschema:"End of the comparison window."`
	Timezone        string          `json:"timezone" jsonschema:"IANA time zone the report was computed in."`
	Orders          int             `json:"orders" jsonschema:"Orders that matched."`
	TotalAvailable  int             `json:"total_available" jsonschema:"Orders the store reports for the window."`
	ExcludedOrders  int             `json:"excluded_orders" jsonschema:"Orders that exist but were not read."`
	Truncated       bool            `json:"truncated" jsonschema:"True when more records exist than were returned."`
	Paging          PagingInfo      `json:"paging" jsonschema:"Paging state of the scan."`
	Revenue         float64         `json:"revenue" jsonschema:"Revenue for this window."`
	Subtotal        float64         `json:"subtotal" jsonschema:"Line subtotal before shipping, tax and discount."`
	Discount        float64         `json:"discount" jsonschema:"Total discount across the window."`
	Shipping        float64         `json:"shipping" jsonschema:"Total shipping charged across the window."`
	Tax             float64         `json:"tax" jsonschema:"Total tax charged across the window."`
	ItemsQty        float64         `json:"items_qty" jsonschema:"Units sold in the window."`
	AOV             float64         `json:"aov" jsonschema:"Average order value in the window."`
	MixedCurrency   bool            `json:"mixed_currency" jsonschema:"True when the window contains more than one currency."`
	GuestOrders     int             `json:"guest_orders" jsonschema:"Orders placed without an account."`
	Customers       int             `json:"customers" jsonschema:"Customers that matched."`
	RepeatCustomers int             `json:"repeat_customers" jsonschema:"Customers with more than one order in the window."`
	RepeatOrders    int             `json:"repeat_orders" jsonschema:"Orders from returning customers."`
	RepeatRevenue   float64         `json:"repeat_revenue" jsonschema:"Revenue from returning customers."`
	NewOrders       int             `json:"new_orders" jsonschema:"Orders from first-time customers."`
	NewRevenue      float64         `json:"new_revenue" jsonschema:"Revenue from first-time customers."`
	CouponOrders    int             `json:"coupon_orders" jsonschema:"Orders that used a coupon."`
	CouponRevenue   float64         `json:"coupon_revenue" jsonschema:"Revenue from coupon orders."`
	CouponAOV       float64         `json:"coupon_aov" jsonschema:"Average order value for coupon orders."`
	NonCouponAOV    float64         `json:"non_coupon_aov" jsonschema:"Average order value excluding coupon discounts."`
	RefundedAmount  float64         `json:"refunded_amount" jsonschema:"Amount refunded across the window."`
	RefundedQty     float64         `json:"refunded_qty" jsonschema:"Units refunded across the window."`
	Currencies      []CurrencyStat  `json:"currencies" jsonschema:"Currencies present in the window."`
	ByDay           []DayStat       `json:"by_day" jsonschema:"Per-day breakdown, oldest first."`
	ByStatus        []DimensionStat `json:"by_status" jsonschema:"Breakdown by order status."`
	ByCountry       []DimensionStat `json:"by_country" jsonschema:"Breakdown by country."`
	ByPayment       []DimensionStat `json:"by_payment" jsonschema:"Breakdown by payment method."`
	ByStore         []DimensionStat `json:"by_store" jsonschema:"Breakdown by store view."`
	ShippingMethods []ShippingStat  `json:"shipping_methods" jsonschema:"Breakdown by shipping method."`
	AOVBands        []BandStat      `json:"aov_bands" jsonschema:"Distribution of order values by band."`
	Weekday         []int           `json:"weekday" jsonschema:"Breakdown by day of week."`
	Hour            []int           `json:"hour" jsonschema:"Breakdown by hour of day, 0-23."`
	TopCoupons      []CouponStat    `json:"top_coupons" jsonschema:"Coupons by revenue, highest first."`
	RefundedSKUs    []SKURefundStat `json:"refunded_skus" jsonschema:"SKUs by refunded amount, highest first."`
	TopSKUs         []SKUStat       `json:"top_skus" jsonschema:"SKUs by revenue, highest first."`
	Shipments       int             `json:"shipments" jsonschema:"Shipments of the order."`
	UnshippedOrders int             `json:"unshipped_orders" jsonschema:"Orders that are not shipped yet."`
	ShipLatency     ShipLatency     `json:"ship_latency" jsonschema:"Time from order to shipment."`
}

type analysisOrderItem struct {
	SKU            string  `json:"sku" jsonschema:"Product SKU."`
	Name           string  `json:"name" jsonschema:"Display name."`
	ProductType    string  `json:"product_type" jsonschema:"Magento product type."`
	ParentItemID   *int    `json:"parent_item_id" jsonschema:"Parent line id for a child item, absent for a top-level line."`
	QtyOrdered     float64 `json:"qty_ordered" jsonschema:"Units ordered on this line."`
	QtyRefunded    float64 `json:"qty_refunded" jsonschema:"Units refunded on this line."`
	RowTotal       float64 `json:"row_total" jsonschema:"Line total before tax and shipping."`
	AmountRefunded float64 `json:"amount_refunded" jsonschema:"Amount refunded on this line."`
}

type analysisOrder struct {
	EntityID       int     `json:"entity_id" jsonschema:"Magento entity id."`
	CreatedAt      string  `json:"created_at" jsonschema:"Store time, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
	Status         string  `json:"status" jsonschema:"Current status."`
	GrandTotal     float64 `json:"grand_total" jsonschema:"Document total including shipping and tax."`
	Subtotal       float64 `json:"subtotal" jsonschema:"Line subtotal before shipping, tax and discount."`
	DiscountAmount float64 `json:"discount_amount" jsonschema:"Discount applied, as a positive number."`
	ShippingAmount float64 `json:"shipping_amount" jsonschema:"Shipping charged."`
	TaxAmount      float64 `json:"tax_amount" jsonschema:"Tax charged."`
	TotalQty       float64 `json:"total_qty_ordered" jsonschema:"Sum of line quantities; a number because Magento allows fractional quantities."`
	Currency       string  `json:"order_currency_code" jsonschema:"ISO 4217 code the amounts are expressed in."`
	StoreName      string  `json:"store_name"`
	CustomerID     *int    `json:"customer_id"`
	IsGuest        int     `json:"customer_is_guest"`
	CouponCode     string  `json:"coupon_code"`
	ShippingDesc   string  `json:"shipping_description"`
	Payment        struct {
		Method string `json:"method" jsonschema:"How the scan was paged: offset or cursor."`
	} `json:"payment"`
	ExtensionAttributes struct {
		ShippingAssignments []struct {
			Shipping struct {
				Address struct {
					CountryID string `json:"country_id" jsonschema:"ISO 3166-1 alpha-2 country code, uppercase."`
				} `json:"address" jsonschema:"The customer address that was written."`
			} `json:"shipping"`
		} `json:"shipping_assignments"`
	} `json:"extension_attributes"`
	Items []analysisOrderItem `json:"items" jsonschema:"Entries returned by this call."`
}

type customerAgg struct {
	orders  int
	revenue float64
}

// AnalyzeSales aggregates a window of orders into business metrics. Only
// aggregate counts and amounts are returned; customer PII never enters the
// result.
func (s *Service) AnalyzeSales(ctx context.Context, from, to time.Time, maxOrders int, timezone string) (*SalesAnalysis, error) {
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
	loc, zoneName, err := s.resolveTimezone(ctx, timezone)
	if err != nil {
		return nil, err
	}
	result := &SalesAnalysis{
		From: from.Format(time.RFC3339), To: to.Format(time.RFC3339), Timezone: zoneName,
		Weekday: make([]int, 7), Hour: make([]int, 24),
	}
	days := map[string]*DayStat{}
	statuses := map[string]*DimensionStat{}
	countries := map[string]*DimensionStat{}
	payments := map[string]*DimensionStat{}
	stores := map[string]*DimensionStat{}
	currencies := map[string]*CurrencyStat{}
	shippingMethods := map[string]*ShippingStat{}
	skus := map[string]*SKUStat{}
	skuOrders := map[string]map[int]bool{}
	refunds := map[string]*SKURefundStat{}
	coupons := map[string]*CouponStat{}
	customers := map[int]*customerAgg{}
	orderTimes := map[int]time.Time{}
	seenOrders := make(map[int]struct{}, 1024)
	duplicatesSkipped := 0
	var orderValues []float64
	var couponRevenue, nonCouponRevenue float64
	var couponCount, nonCouponCount int

	for page := 1; result.Orders < maxOrders; page++ {
		raw, err := s.client.do(ctx, http.MethodGet, "/orders", analysisQuery(from, to, page), nil)
		if err != nil {
			return nil, err
		}
		var payload struct {
			TotalCount int             `json:"total_count"`
			Items      []analysisOrder `json:"items" jsonschema:"Entries returned by this call."`
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
			// Offset paging can hand the same order out twice when a page
			// boundary shifts mid-scan. Drop it so revenue is never double
			// counted, and record that it happened.
			if order.EntityID > 0 {
				if _, duplicate := seenOrders[order.EntityID]; duplicate {
					duplicatesSkipped++
					continue
				}
				seenOrders[order.EntityID] = struct{}{}
			}
			result.Orders++
			result.Revenue += order.GrandTotal
			result.Subtotal += order.Subtotal
			result.Discount += order.DiscountAmount
			result.Shipping += order.ShippingAmount
			result.Tax += order.TaxAmount
			result.ItemsQty += order.TotalQty
			orderValues = append(orderValues, order.GrandTotal)
			if order.IsGuest == 1 {
				result.GuestOrders++
			} else if order.CustomerID != nil && *order.CustomerID > 0 {
				agg := customers[*order.CustomerID]
				if agg == nil {
					agg = &customerAgg{}
					customers[*order.CustomerID] = agg
				}
				agg.orders++
				agg.revenue += order.GrandTotal
			}
			local := orderLocalTime(order.CreatedAt, loc)
			orderTimes[order.EntityID] = orderUTCTime(order.CreatedAt)
			addDay(days, local.Format("2006-01-02"), order.GrandTotal)
			result.Weekday[(int(local.Weekday())+6)%7]++
			result.Hour[local.Hour()]++
			coupon := strings.ToUpper(strings.TrimSpace(order.CouponCode))
			if coupon != "" {
				result.CouponOrders++
				result.CouponRevenue += order.GrandTotal
				couponRevenue += order.GrandTotal
				couponCount++
				stat := coupons[coupon]
				if stat == nil {
					stat = &CouponStat{Code: coupon}
					coupons[coupon] = stat
				}
				stat.Orders++
			} else {
				nonCouponRevenue += order.GrandTotal
				nonCouponCount++
			}
			addDimension(statuses, order.Status, order.GrandTotal)
			country := "UNKNOWN"
			if len(order.ExtensionAttributes.ShippingAssignments) > 0 {
				if code := order.ExtensionAttributes.ShippingAssignments[0].Shipping.Address.CountryID; code != "" {
					country = code
				}
			}
			addDimension(countries, country, order.GrandTotal)
			addDimension(payments, order.Payment.Method, order.GrandTotal)
			addDimension(stores, normalizeStore(order.StoreName), order.GrandTotal)
			currency := order.Currency
			if currency == "" {
				currency = "unknown"
			}
			cstat := currencies[currency]
			if cstat == nil {
				cstat = &CurrencyStat{Code: currency}
				currencies[currency] = cstat
			}
			cstat.Orders++
			cstat.Revenue += order.GrandTotal
			method := normalizeShipping(order.ShippingDesc)
			sstat := shippingMethods[method]
			if sstat == nil {
				sstat = &ShippingStat{Method: method}
				shippingMethods[method] = sstat
			}
			sstat.Orders++
			sstat.Revenue += order.GrandTotal
			sstat.ShippingCost += order.ShippingAmount
			for _, item := range order.Items {
				if item.ParentItemID != nil {
					continue // configurable child rows duplicate the parent's amount
				}
				result.RefundedAmount += item.AmountRefunded
				result.RefundedQty += item.QtyRefunded
				if item.AmountRefunded > 0 || item.QtyRefunded > 0 {
					rstat := refunds[item.SKU]
					if rstat == nil {
						rstat = &SKURefundStat{SKU: item.SKU}
						refunds[item.SKU] = rstat
					}
					rstat.Qty += item.QtyRefunded
					rstat.Amount += item.AmountRefunded
				}
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
	if result.TotalAvailable < result.Orders {
		// A page boundary moved backwards (an order was deleted mid-scan):
		// report what was seen rather than a negative remainder.
		result.TotalAvailable = result.Orders
	}
	result.ExcludedOrders = result.TotalAvailable - result.Orders
	result.Paging = PagingInfo{
		Method:             analysisPagingMethod,
		OrderBy:            analysisPagingOrder,
		SnapshotConsistent: false,
		DuplicatesSkipped:  duplicatesSkipped,
		Caveat:             analysisPagingCaveat,
	}
	if result.Orders > 0 {
		result.AOV = result.Revenue / float64(result.Orders)
	}
	if couponCount > 0 {
		result.CouponAOV = couponRevenue / float64(couponCount)
	}
	if nonCouponCount > 0 {
		result.NonCouponAOV = nonCouponRevenue / float64(nonCouponCount)
	}
	for _, agg := range customers {
		result.Customers++
		if agg.orders > 1 {
			result.RepeatCustomers++
			result.RepeatOrders += agg.orders
			result.RepeatRevenue += agg.revenue
		} else {
			result.NewOrders++
			result.NewRevenue += agg.revenue
		}
	}
	result.Currencies = sortedCurrencies(currencies)
	result.MixedCurrency = len(result.Currencies) > 1
	result.ByDay = sortedDays(days)
	result.ByStatus = sortedDimensions(statuses)
	result.ByCountry = sortedDimensions(countries)
	result.ByPayment = sortedDimensions(payments)
	result.ByStore = sortedDimensions(stores)
	result.ShippingMethods = sortedShipping(shippingMethods)
	result.AOVBands = aovBands(orderValues)
	result.TopCoupons = topCoupons(coupons)
	result.RefundedSKUs = topRefundSKUs(refunds)
	result.TopSKUs = topSKUs(skus)
	result.Shipments, result.UnshippedOrders, result.ShipLatency = s.shipmentStats(ctx, from, to, orderTimes)
	return result, nil
}

func (s *Service) resolveTimezone(ctx context.Context, timezone string) (*time.Location, string, error) {
	name := strings.TrimSpace(timezone)
	if name == "" {
		if stores, err := s.StoreConfigs(ctx); err == nil {
			for _, store := range stores {
				if store.Timezone != "" {
					name = store.Timezone
					break
				}
			}
		}
	}
	if name == "" {
		name = "UTC"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, "", invalidf("timezone %q is not a valid IANA zone", name)
	}
	return loc, name, nil
}

func orderUTCTime(raw string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", raw, time.UTC)
	if err != nil {
		return time.Now().UTC()
	}
	return parsed
}

func orderLocalTime(raw string, loc *time.Location) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", raw, time.UTC)
	if err != nil {
		return time.Now().UTC().In(loc)
	}
	return parsed.In(loc)
}

func normalizeStore(name string) string {
	parts := strings.Split(name, "\n")
	for i := len(parts) - 1; i >= 0; i-- {
		if trimmed := strings.TrimSpace(parts[i]); trimmed != "" {
			return trimmed
		}
	}
	return "unknown"
}

func normalizeShipping(name string) string {
	trimmed := strings.Join(strings.Fields(strings.TrimSpace(name)), " ")
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
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
	// created_at is not unique (many orders share a second), so OFFSET paging
	// over it alone is not a total order: rows could repeat or vanish across
	// pages even without concurrent writes. entity_id makes the order
	// deterministic.
	query.Set("searchCriteria[sortOrders][1][field]", "entity_id")
	query.Set("searchCriteria[sortOrders][1][direction]", "asc")
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

func sortedCurrencies(cur map[string]*CurrencyStat) []CurrencyStat {
	out := make([]CurrencyStat, 0, len(cur))
	for _, stat := range cur {
		if stat.Orders > 0 {
			stat.AOV = stat.Revenue / float64(stat.Orders)
		}
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Orders != out[j].Orders {
			return out[i].Orders > out[j].Orders
		}
		return out[i].Code < out[j].Code
	})
	return out
}

func sortedShipping(methods map[string]*ShippingStat) []ShippingStat {
	out := make([]ShippingStat, 0, len(methods))
	for _, stat := range methods {
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Orders != out[j].Orders {
			return out[i].Orders > out[j].Orders
		}
		return out[i].Method < out[j].Method
	})
	return out
}

func aovBands(values []float64) []BandStat {
	bands := []struct {
		label string
		hi    float64
	}{
		{"<$100", 100}, {"$100-300", 300}, {"$300-600", 600},
		{"$600-1k", 1000}, {"$1k-2k", 2000}, {">$2k", 0},
	}
	out := make([]BandStat, len(bands))
	for i, band := range bands {
		out[i] = BandStat{Label: band.label}
	}
	for _, value := range values {
		for i, band := range bands {
			if band.hi == 0 || value < band.hi {
				out[i].Orders++
				out[i].Revenue += value
				break
			}
		}
	}
	return out
}

func topCoupons(coupons map[string]*CouponStat) []CouponStat {
	out := make([]CouponStat, 0, len(coupons))
	for _, stat := range coupons {
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Orders != out[j].Orders {
			return out[i].Orders > out[j].Orders
		}
		return out[i].Code < out[j].Code
	})
	if len(out) > topCouponLimit {
		out = out[:topCouponLimit]
	}
	return out
}

func topRefundSKUs(refunds map[string]*SKURefundStat) []SKURefundStat {
	out := make([]SKURefundStat, 0, len(refunds))
	for _, stat := range refunds {
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Amount != out[j].Amount {
			return out[i].Amount > out[j].Amount
		}
		return out[i].SKU < out[j].SKU
	})
	if len(out) > topRefundSKULimit {
		out = out[:topRefundSKULimit]
	}
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

// shipmentStats reads shipments created up to seven days after the window and
// joins them with the orders to compute latency and unshipped counts. A failed
// shipment read does not fail the order analysis.
func (s *Service) shipmentStats(ctx context.Context, from, to time.Time, orderTimes map[int]time.Time) (int, int, ShipLatency) {
	var latency []float64
	shipments := 0
	shipped := map[int]bool{}
	queryFrom := from.UTC().Format("2006-01-02 15:04:05")
	queryTo := to.AddDate(0, 0, shipmentLookaheadDays).UTC().Format("2006-01-02 15:04:05")
	for page := 1; ; page++ {
		query := url.Values{}
		query.Set("searchCriteria[pageSize]", strconv.Itoa(analysisPageSize))
		query.Set("searchCriteria[currentPage]", strconv.Itoa(page))
		query.Set("searchCriteria[filter_groups][0][filters][0][field]", "created_at")
		query.Set("searchCriteria[filter_groups][0][filters][0][value]", queryFrom)
		query.Set("searchCriteria[filter_groups][0][filters][0][condition_type]", "gteq")
		query.Set("searchCriteria[filter_groups][1][filters][0][field]", "created_at")
		query.Set("searchCriteria[filter_groups][1][filters][0][value]", queryTo)
		query.Set("searchCriteria[filter_groups][1][filters][0][condition_type]", "lt")
		raw, err := s.client.do(ctx, http.MethodGet, "/shipments", query, nil)
		if err != nil {
			return 0, 0, ShipLatency{}
		}
		var payload struct {
			Items []struct {
				OrderID   int    `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
				CreatedAt string `json:"created_at" jsonschema:"Store time, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
			} `json:"items" jsonschema:"Entries returned by this call."`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return 0, 0, ShipLatency{}
		}
		for _, shipment := range payload.Items {
			shipments++
			shipped[shipment.OrderID] = true
			orderTime, ok := orderTimes[shipment.OrderID]
			if !ok {
				continue
			}
			created, err := time.ParseInLocation("2006-01-02 15:04:05", shipment.CreatedAt, time.UTC)
			if err != nil {
				continue
			}
			latency = append(latency, created.Sub(orderTime).Hours()/24)
		}
		if len(payload.Items) < analysisPageSize {
			break
		}
	}
	unshipped := 0
	for id := range orderTimes {
		if !shipped[id] {
			unshipped++
		}
	}
	stats := ShipLatency{}
	if len(latency) > 0 {
		sort.Float64s(latency)
		sum := 0.0
		for _, value := range latency {
			sum += value
		}
		stats.Avg = sum / float64(len(latency))
		stats.Median = latency[len(latency)/2]
		stats.P90 = latency[min(len(latency)-1, int(float64(len(latency))*0.9))]
		stats.Max = latency[len(latency)-1]
	}
	return shipments, unshipped, stats
}
