// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestAnalyzeSales(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":2,"items":[
			{"entity_id":1,"created_at":"2026-11-19 10:00:00","status":"processing","grand_total":100,"subtotal":90,
			 "discount_amount":-10,"shipping_amount":15,"tax_amount":5,"total_qty_ordered":1,"total_refunded":0,"order_currency_code":"USD",
			 "store_name":"Giztop","customer_is_guest":0,"payment":{"method":"paypal_express"},
			 "extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"US"}}}]},
			 "items":[{"sku":"A","name":"Alpha","product_type":"configurable","qty_ordered":1,"row_total":90,"amount_refunded":0,"qty_refunded":0},
			          {"sku":"A","name":"Alpha","product_type":"simple","parent_item_id":9,"qty_ordered":1,"row_total":0}]},
			{"entity_id":2,"created_at":"2026-11-19 12:00:00","status":"complete","grand_total":50,"subtotal":40,
			 "discount_amount":0,"shipping_amount":10,"tax_amount":0,"total_qty_ordered":2,"total_refunded":5,"order_currency_code":"USD",
			 "store_name":"Giztop","customer_is_guest":1,"payment":{"method":"checkmo"},
			 "extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"DE"}}}]},
			 "items":[{"sku":"B","name":"Beta","product_type":"simple","qty_ordered":2,"row_total":40,"amount_refunded":5,"qty_refunded":1}]}
		]}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	from, _ := time.Parse("2006-01-02", "2026-11-19")
	to, _ := time.Parse("2006-01-02", "2026-11-20")
	a, err := svc.AnalyzeSales(context.Background(), from, to, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Orders != 2 || a.Revenue != 150 || a.AOV != 75 || a.GuestOrders != 1 {
		t.Fatalf("totals = %+v", a)
	}
	if a.Subtotal != 130 || a.Discount != 10 || a.Shipping != 25 || a.Tax != 5 || a.ItemsQty != 3 {
		t.Fatalf("amounts = %+v", a)
	}
	if a.RefundedAmount != 5 || a.RefundedQty != 1 {
		t.Fatalf("refunds = %+v", a)
	}
	if a.RefundedItemsAmount != 5 {
		t.Fatalf("refunded_items_amount = %v, want 5", a.RefundedItemsAmount)
	}
	if len(a.ByDay) != 1 || a.ByDay[0].Orders != 2 || a.ByDay[0].Revenue != 150 {
		t.Fatalf("days = %+v", a.ByDay)
	}
	if len(a.ByStatus) != 2 || a.ByCountry[0].Key != "DE" || a.ByCountry[1].Key != "US" {
		t.Fatalf("dims = %+v %+v", a.ByStatus, a.ByCountry)
	}
	if len(a.TopSKUs) != 2 || a.TopSKUs[0].SKU != "A" || a.TopSKUs[0].Qty != 1 || a.TopSKUs[0].Revenue != 90 || a.TopSKUs[0].Orders != 1 {
		t.Fatalf("skus = %+v", a.TopSKUs)
	}
	if len(a.Currencies) != 1 || a.Currencies[0].Code != "USD" || a.Currencies[0].AOV != 75 {
		t.Fatalf("currencies = %+v", a.Currencies)
	}
}

func TestAnalyzeSalesValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	now := time.Now()
	if _, err := svc.AnalyzeSales(ctx, now, now.Add(-time.Hour), 10, ""); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("window err = %v", err)
	}
	if _, err := svc.AnalyzeSales(ctx, now, now.Add(time.Hour), 30000, ""); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("max err = %v", err)
	}
}

func TestAnalyzeSalesDrilldown(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/store/storeConfigs":
			_, _ = w.Write([]byte(`[{"id":1,"code":"default","timezone":"Asia/Shanghai","base_currency_code":"USD"}]`))
		case "/rest/default/V1/orders":
			_, _ = w.Write([]byte(`{"total_count":3,"items":[
				{"entity_id":1,"created_at":"2026-11-19 20:00:00","status":"complete","grand_total":100,"subtotal":90,
				 "discount_amount":0,"shipping_amount":10,"tax_amount":0,"total_qty_ordered":1,"order_currency_code":"USD",
				 "store_name":"Main Website\nDefault Store View","customer_id":1,"customer_is_guest":0,
				 "coupon_code":"bf2026","shipping_description":"Shipping Rates - Express (6-10 days)",
				 "payment":{"method":"paypal_express"},
				 "extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"US"}}}]},
				 "items":[{"sku":"A","name":"Alpha","qty_ordered":1,"row_total":90,"amount_refunded":0,"qty_refunded":0}]},
				{"entity_id":2,"created_at":"2026-11-19 21:00:00","status":"complete","grand_total":200,"subtotal":190,
				 "discount_amount":0,"shipping_amount":10,"tax_amount":0,"total_qty_ordered":1,"order_currency_code":"USD",
				 "store_name":"Main Website\nDefault Store View","customer_id":1,"customer_is_guest":0,
				 "shipping_description":"Shipping Rates - Express (6-10 days)","payment":{"method":"paypal_express"},
				 "extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"US"}}}]},
				 "items":[{"sku":"A","name":"Alpha","qty_ordered":1,"row_total":190,"amount_refunded":0,"qty_refunded":0}]},
				{"entity_id":3,"created_at":"2026-11-19 22:00:00","status":"complete","grand_total":50,"subtotal":40,
				 "discount_amount":0,"shipping_amount":10,"tax_amount":0,"total_qty_ordered":1,"order_currency_code":"USD",
				 "store_name":"Main Website\nDefault Store View","customer_id":2,"customer_is_guest":0,
				 "coupon_code":"BF2026","shipping_description":"Shipping Rates - Flat Air Mail (No tracking, 15-30 days)",
				 "payment":{"method":"paypal_express"},
				 "extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"DE"}}}]},
				 "items":[{"sku":"B","name":"Beta","qty_ordered":1,"row_total":40,"amount_refunded":0,"qty_refunded":0}]}
			]}`))
		case "/rest/default/V1/shipments":
			_, _ = w.Write([]byte(`{"items":[{"order_id":1,"created_at":"2026-11-22 20:00:00"},{"order_id":2,"created_at":"2026-11-21 20:00:00"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	from, _ := time.Parse("2006-01-02", "2026-11-19")
	to, _ := time.Parse("2006-01-02", "2026-11-20")
	a, err := svc.AnalyzeSales(context.Background(), from, to, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Timezone != "Asia/Shanghai" {
		t.Fatalf("timezone = %q", a.Timezone)
	}
	if a.Customers != 2 || a.RepeatCustomers != 1 || a.RepeatOrders != 2 || a.RepeatRevenue != 300 || a.NewOrders != 1 || a.NewRevenue != 50 {
		t.Fatalf("customers = %+v", a)
	}
	if a.CouponOrders != 2 || a.CouponRevenue != 150 || len(a.TopCoupons) != 1 || a.TopCoupons[0].Code != "BF2026" || a.TopCoupons[0].Orders != 2 {
		t.Fatalf("coupons = %+v", a)
	}
	if len(a.AOVBands) != 6 || a.AOVBands[0].Orders != 1 || a.AOVBands[1].Orders != 2 || a.AOVBands[2].Orders != 0 {
		t.Fatalf("bands = %+v", a.AOVBands)
	}
	if len(a.ShippingMethods) != 2 || a.ShippingMethods[0].Orders != 2 {
		t.Fatalf("shipping = %+v", a.ShippingMethods)
	}
	if a.Shipments != 2 || a.UnshippedOrders != 1 || a.ShipLatency.Median != 3 || a.ShipLatency.Max != 3 {
		t.Fatalf("latency = %+v unshipped=%d", a.ShipLatency, a.UnshippedOrders)
	}
	if a.ByStore[0].Key != "Default Store View" {
		t.Fatalf("store key = %q", a.ByStore[0].Key)
	}
}

// analysisOrderPage renders one page of synthetic orders for the /orders
// endpoint. created_at is inside November 2026 so the window is realistic.
func analysisOrderPage(firstEntityID, count int) string {
	items := make([]string, 0, count)
	for i := 0; i < count; i++ {
		entityID := firstEntityID + i
		items = append(items, fmt.Sprintf(
			`{"entity_id":%d,"created_at":"2026-11-%02d 10:00:00","status":"processing","grand_total":10,`+
				`"subtotal":9,"discount_amount":0,"shipping_amount":1,"tax_amount":0,"total_qty_ordered":1,`+
				`"order_currency_code":"USD","store_name":"Main","customer_is_guest":0,"payment":{"method":"checkmo"},`+
				`"items":[{"sku":"SKU-%d","name":"Item","product_type":"simple","qty_ordered":1,"row_total":9}]}`,
			entityID, 1+entityID%28, entityID%50))
	}
	return strings.Join(items, ",")
}

// TestAnalyzeSalesPagingIsExplicit covers the offset-paging caveat: the response
// must state how the window was scanned, and the query must impose a total order
// so pages cannot repeat or skip an order that shares a created_at second.
func TestAnalyzeSalesPagingIsExplicit(t *testing.T) {
	const total = 924
	var mu sync.Mutex
	var secondarySorts []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/rest/default/V1/orders" {
			_, _ = w.Write([]byte(`{"items":[],"total_count":0}`))
			return
		}
		mu.Lock()
		secondarySorts = append(secondarySorts, r.URL.Query().Get("searchCriteria[sortOrders][1][field]"))
		mu.Unlock()
		page, _ := strconv.Atoi(r.URL.Query().Get("searchCriteria[currentPage]"))
		if page == 0 {
			page = 1
		}
		from := (page - 1) * analysisPageSize
		count := analysisPageSize
		if from+count > total {
			count = total - from
		}
		if count < 0 {
			count = 0
		}
		_, _ = fmt.Fprintf(w, `{"total_count":%d,"items":[%s]}`, total, analysisOrderPage(from+1, count))
	}))
	defer ts.Close()

	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	analysis, err := testService(t, ts.URL).AnalyzeSales(context.Background(), from, to, 20000, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Orders != total || analysis.TotalAvailable != total {
		t.Fatalf("orders = %d total_available = %d, want %d", analysis.Orders, analysis.TotalAvailable, total)
	}
	if analysis.ExcludedOrders != 0 || analysis.Truncated {
		t.Fatalf("excluded = %d truncated = %v, want a complete scan", analysis.ExcludedOrders, analysis.Truncated)
	}
	if analysis.Paging.Method != analysisPagingMethod {
		t.Fatalf("paging.method = %q, want %q", analysis.Paging.Method, analysisPagingMethod)
	}
	if analysis.Paging.SnapshotConsistent {
		t.Fatal("offset paging must not claim snapshot consistency")
	}
	if analysis.Paging.OrderBy != analysisPagingOrder {
		t.Fatalf("paging.order_by = %q, want %q", analysis.Paging.OrderBy, analysisPagingOrder)
	}
	if analysis.Paging.Caveat == "" {
		t.Fatal("paging.caveat is empty: a caller cannot judge completeness")
	}
	if analysis.Paging.DuplicatesSkipped != 0 {
		t.Fatalf("duplicates_skipped = %d, want 0", analysis.Paging.DuplicatesSkipped)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(secondarySorts) < 2 {
		t.Fatalf("order pages requested = %d, want at least 2", len(secondarySorts))
	}
	for i, field := range secondarySorts {
		if field != "entity_id" {
			t.Fatalf("page %d secondary sort = %q, want entity_id", i+1, field)
		}
	}
}

// TestAnalyzeSalesDropsDuplicateOrders proves a page boundary that repeats
// orders cannot double-count revenue, and that the repeat is reported.
func TestAnalyzeSalesDropsDuplicateOrders(t *testing.T) {
	const perPage = analysisPageSize
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/rest/default/V1/orders" {
			_, _ = w.Write([]byte(`{"items":[],"total_count":0}`))
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("searchCriteria[currentPage]"))
		switch page {
		case 1, 2:
			// Page 2 repeats page 1 exactly: the shifted-boundary case.
			_, _ = fmt.Fprintf(w, `{"total_count":%d,"items":[%s]}`, perPage, analysisOrderPage(1, perPage))
		default:
			_, _ = fmt.Fprintf(w, `{"total_count":%d,"items":[]}`, perPage)
		}
	}))
	defer ts.Close()

	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	analysis, err := testService(t, ts.URL).AnalyzeSales(context.Background(), from, to, 20000, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Orders != perPage {
		t.Fatalf("orders = %d, want %d (the duplicate page must not be counted)", analysis.Orders, perPage)
	}
	if want := float64(perPage) * 10; analysis.Revenue != want {
		t.Fatalf("revenue = %v, want %v (no double counting)", analysis.Revenue, want)
	}
	if analysis.Paging.DuplicatesSkipped != perPage {
		t.Fatalf("duplicates_skipped = %d, want %d", analysis.Paging.DuplicatesSkipped, perPage)
	}
	if analysis.Truncated || analysis.ExcludedOrders != 0 {
		t.Fatalf("excluded = %d truncated = %v, want a complete scan", analysis.ExcludedOrders, analysis.Truncated)
	}
}

// --- report-caliber fixes (t11 / G6) -------------------------------------
//
// Each test below pins one caliber decision. Reverting the decision makes the
// test fail, so a later refactor cannot quietly reintroduce the inconsistency.

// TestParseReportWindowUsesReportTimezone pins the window parsing rule: a bare
// date is a calendar day in the report timezone, an RFC 3339 value is an
// absolute instant.
func TestParseReportWindowUsesReportTimezone(t *testing.T) {
	warsaw, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Fatal(err)
	}
	from, to, err := parseReportWindow("2026-11-21", "2026-11-22", warsaw)
	if err != nil {
		t.Fatal(err)
	}
	// Warsaw is UTC+1 in November: the local day starts at 23:00Z the day before.
	if got := from.UTC().Format(time.RFC3339); got != "2026-11-20T23:00:00Z" {
		t.Fatalf("from = %s, want 2026-11-20T23:00:00Z", got)
	}
	if got := to.UTC().Format(time.RFC3339); got != "2026-11-21T23:00:00Z" {
		t.Fatalf("to = %s, want 2026-11-21T23:00:00Z", got)
	}
	// An RFC 3339 value is an absolute instant and must not be reinterpreted.
	from, _, err = parseReportWindow("2026-11-21T00:00:00Z", "2026-11-22T00:00:00Z", warsaw)
	if err != nil {
		t.Fatal(err)
	}
	if got := from.UTC().Format(time.RFC3339); got != "2026-11-21T00:00:00Z" {
		t.Fatalf("rfc3339 from = %s, want it used as given", got)
	}
	if _, _, err := parseReportWindow("2026-11-21", "not-a-date", warsaw); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("bad date err = %v", err)
	}
}

// TestReportDayUsesReportTimezone pins the shared day-bucketing rule that
// sales.summary must reuse: an order at 23:30Z belongs to the store's next day.
func TestReportDayUsesReportTimezone(t *testing.T) {
	warsaw, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Fatal(err)
	}
	if got := reportDay("2026-11-21 23:30:00", warsaw); got != "2026-11-22" {
		t.Fatalf("warsaw day = %s, want 2026-11-22 (UTC slicing would say 2026-11-21)", got)
	}
	if got := reportDay("2026-11-21 23:30:00", time.UTC); got != "2026-11-21" {
		t.Fatalf("utc day = %s, want 2026-11-21", got)
	}
}

// TestAnalyzeSalesWindowKeepsBucketsInsideWindow proves a date-only window and
// its day buckets share one calendar: the query is sent as UTC instants of the
// local day, and every reported bucket is dated inside window_local.
func TestAnalyzeSalesWindowKeepsBucketsInsideWindow(t *testing.T) {
	var filters []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/store/storeConfigs":
			_, _ = w.Write([]byte(`[{"id":1,"code":"pl","timezone":"Europe/Warsaw","base_currency_code":"PLN"}]`))
		case "/rest/default/V1/orders":
			filters = append(filters, r.URL.Query().Get("searchCriteria[filter_groups][0][filters][0][value]"),
				r.URL.Query().Get("searchCriteria[filter_groups][1][filters][0][value]"))
			// Both orders fall inside the local day 2026-11-21 (00:30 and 23:30 local).
			_, _ = w.Write([]byte(`{"total_count":2,"items":[
				{"entity_id":1,"created_at":"2026-11-20 23:30:00","status":"complete","grand_total":10,"subtotal":9,
				 "discount_amount":0,"shipping_amount":1,"tax_amount":0,"total_qty_ordered":1,"order_currency_code":"PLN",
				 "store_name":"PL","customer_is_guest":0,"payment":{"method":"checkmo"},
				 "items":[{"sku":"A","name":"Alpha","qty_ordered":1,"row_total":9}]},
				{"entity_id":2,"created_at":"2026-11-21 22:30:00","status":"complete","grand_total":20,"subtotal":19,
				 "discount_amount":0,"shipping_amount":1,"tax_amount":0,"total_qty_ordered":1,"order_currency_code":"PLN",
				 "store_name":"PL","customer_is_guest":0,"payment":{"method":"checkmo"},
				 "items":[{"sku":"B","name":"Beta","qty_ordered":1,"row_total":19}]}
			]}`))
		default:
			_, _ = w.Write([]byte(`{"items":[],"total_count":0}`))
		}
	}))
	defer ts.Close()

	a, err := testService(t, ts.URL).AnalyzeSalesWindow(context.Background(), "2026-11-21", "2026-11-22", 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 2 || filters[0] != "2026-11-20 23:00:00" || filters[1] != "2026-11-21 23:00:00" {
		t.Fatalf("query window = %v, want the UTC instants of the Warsaw day", filters)
	}
	if a.Timezone != "Europe/Warsaw" {
		t.Fatalf("timezone = %q", a.Timezone)
	}
	if a.WindowLocal.FirstDate != "2026-11-21" || a.WindowLocal.LastDate != "2026-11-21" {
		t.Fatalf("window_local = %+v, want the single day 2026-11-21", a.WindowLocal)
	}
	if len(a.ByDay) != 1 || a.ByDay[0].Date != "2026-11-21" || a.ByDay[0].Orders != 2 {
		t.Fatalf("by_day = %+v, want one bucket on 2026-11-21", a.ByDay)
	}
}

// TestAnalyzeSalesDayBucketsStayWithinWindowLocal is the other half of the same
// contract: even for an absolute (RFC 3339) window, every bucket date lies
// within window_local.first_date..last_date, so a bucket can never look like it
// escaped the requested range.
func TestAnalyzeSalesDayBucketsStayWithinWindowLocal(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":1,"items":[
			{"entity_id":1,"created_at":"2026-11-21 23:30:00","status":"complete","grand_total":10,"subtotal":9,
			 "discount_amount":0,"shipping_amount":1,"tax_amount":0,"total_qty_ordered":1,"order_currency_code":"PLN",
			 "store_name":"PL","customer_is_guest":0,"payment":{"method":"checkmo"},
			 "items":[{"sku":"A","name":"Alpha","qty_ordered":1,"row_total":9}]}
		]}`))
	}))
	defer ts.Close()
	from := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	a, err := testService(t, ts.URL).AnalyzeSales(context.Background(), from, to, 100, "Europe/Warsaw")
	if err != nil {
		t.Fatal(err)
	}
	// 23:30Z is 00:30 local on the 22nd, so the bucket is the 22nd; the window in
	// local time runs 11-21 01:00 .. 11-22 01:00, so the 22nd is inside it.
	if len(a.ByDay) != 1 || a.ByDay[0].Date != "2026-11-22" {
		t.Fatalf("by_day = %+v, want the local day 2026-11-22", a.ByDay)
	}
	if a.WindowLocal.FirstDate != "2026-11-21" || a.WindowLocal.LastDate != "2026-11-22" {
		t.Fatalf("window_local = %+v", a.WindowLocal)
	}
	for _, day := range a.ByDay {
		if day.Date < a.WindowLocal.FirstDate || day.Date > a.WindowLocal.LastDate {
			t.Fatalf("bucket %s outside window %s..%s", day.Date, a.WindowLocal.FirstDate, a.WindowLocal.LastDate)
		}
	}
}

// analysisFixtureServer serves one fixed order payload for every window query.
func analysisFixtureServer(t *testing.T, orders string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/rest/default/V1/orders" {
			_, _ = fmt.Fprintf(w, `{"total_count":%d,"items":[%s]}`, strings.Count(orders, `"entity_id"`), orders)
			return
		}
		_, _ = w.Write([]byte(`{"items":[],"total_count":0}`))
	}))
}

// TestAnalyzeSalesSKURevenueIsLineSubtotal pins the money basis of
// top_skus.revenue: it sums item row_total, so the list equals subtotal and is
// short of revenue by the shipping (and tax, and order-level discounts).
func TestAnalyzeSalesSKURevenueIsLineSubtotal(t *testing.T) {
	ts := analysisFixtureServer(t, `
		{"entity_id":1,"created_at":"2026-11-19 10:00:00","status":"complete","grand_total":105,"subtotal":90,
		 "discount_amount":0,"shipping_amount":15,"tax_amount":0,"total_qty_ordered":1,"order_currency_code":"PLN",
		 "store_name":"PL","customer_is_guest":0,"payment":{"method":"checkmo"},
		 "items":[{"sku":"A","name":"Alpha","qty_ordered":1,"row_total":90}]}`)
	defer ts.Close()
	from := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	a, err := testService(t, ts.URL).AnalyzeSales(context.Background(), from, to, 100, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if a.SKURevenueBasis != skuRevenueBasisLineSubtotal {
		t.Fatalf("sku_revenue_basis = %q, want %q", a.SKURevenueBasis, skuRevenueBasisLineSubtotal)
	}
	var sum float64
	for _, sku := range a.TopSKUs {
		sum += sku.Revenue
	}
	if sum != a.Subtotal {
		t.Fatalf("sum(top_skus.revenue) = %v, want subtotal %v", sum, a.Subtotal)
	}
	if sum == a.Revenue {
		t.Fatalf("sum(top_skus.revenue) = revenue (%v): the list is a line-subtotal basis and must differ from grand total", sum)
	}
	if a.Revenue-a.Subtotal != 15 {
		t.Fatalf("revenue - subtotal = %v, want the 15 shipping that top_skus does not carry", a.Revenue-a.Subtotal)
	}
}

// TestAnalyzeSalesRefundedAmountIsDocumentLevel pins the refund caliber:
// refunded_amount is the sum of order total_refunded (creditmemo grand totals,
// which include refunded shipping and adjustments), while
// refunded_items_amount keeps the product-line-only figure.
func TestAnalyzeSalesRefundedAmountIsDocumentLevel(t *testing.T) {
	ts := analysisFixtureServer(t, `
		{"entity_id":1,"created_at":"2026-11-19 10:00:00","status":"closed","grand_total":39,"subtotal":34,
		 "discount_amount":0,"shipping_amount":5,"tax_amount":0,"total_qty_ordered":1,"total_refunded":22,
		 "order_currency_code":"PLN","store_name":"PL","customer_is_guest":0,"payment":{"method":"checkmo"},
		 "items":[{"sku":"A","name":"Alpha","qty_ordered":1,"row_total":34,"amount_refunded":17,"qty_refunded":1}]}`)
	defer ts.Close()
	from := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	a, err := testService(t, ts.URL).AnalyzeSales(context.Background(), from, to, 100, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if a.RefundedAmount != 22 {
		t.Fatalf("refunded_amount = %v, want 22 (document level, includes the refunded 5.00 shipping)", a.RefundedAmount)
	}
	if a.RefundedItemsAmount != 17 {
		t.Fatalf("refunded_items_amount = %v, want 17 (product lines only)", a.RefundedItemsAmount)
	}
	if a.RefundedAmount-a.RefundedItemsAmount != 5 {
		t.Fatalf("gap = %v, want the 5.00 shipping refund that only the document total carries",
			a.RefundedAmount-a.RefundedItemsAmount)
	}
}

// TestAnalyzeSalesDiscountIsReportedPositive pins the discount sign: Magento
// sends discount_amount negative, the report field is documented as positive.
func TestAnalyzeSalesDiscountIsReportedPositive(t *testing.T) {
	ts := analysisFixtureServer(t, `
		{"entity_id":1,"created_at":"2026-11-19 10:00:00","status":"complete","grand_total":90,"subtotal":100,
		 "discount_amount":-10,"shipping_amount":0,"tax_amount":0,"total_qty_ordered":1,"order_currency_code":"PLN",
		 "store_name":"PL","customer_is_guest":0,"payment":{"method":"checkmo"},
		 "items":[{"sku":"A","name":"Alpha","qty_ordered":1,"row_total":100}]}`)
	defer ts.Close()
	from := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	a, err := testService(t, ts.URL).AnalyzeSales(context.Background(), from, to, 100, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if a.Discount != 10 {
		t.Fatalf("discount = %v, want 10 (Magento sends -10)", a.Discount)
	}
}

// TestAnalyzeSalesTopCouponsRankByRevenue pins the ranking the field documents.
// Ranking by order count used to put a two-order 20.00 coupon above a one-order
// 100.00 coupon.
func TestAnalyzeSalesTopCouponsRankByRevenue(t *testing.T) {
	ts := analysisFixtureServer(t, `
		{"entity_id":1,"created_at":"2026-11-19 10:00:00","status":"complete","grand_total":10,"subtotal":10,
		 "discount_amount":0,"shipping_amount":0,"tax_amount":0,"total_qty_ordered":1,"order_currency_code":"PLN",
		 "store_name":"PL","customer_is_guest":0,"coupon_code":"MANY","payment":{"method":"checkmo"},
		 "items":[{"sku":"A","name":"Alpha","qty_ordered":1,"row_total":10}]},
		{"entity_id":2,"created_at":"2026-11-19 11:00:00","status":"complete","grand_total":10,"subtotal":10,
		 "discount_amount":0,"shipping_amount":0,"tax_amount":0,"total_qty_ordered":1,"order_currency_code":"PLN",
		 "store_name":"PL","customer_is_guest":0,"coupon_code":"MANY","payment":{"method":"checkmo"},
		 "items":[{"sku":"A","name":"Alpha","qty_ordered":1,"row_total":10}]},
		{"entity_id":3,"created_at":"2026-11-19 12:00:00","status":"complete","grand_total":100,"subtotal":100,
		 "discount_amount":0,"shipping_amount":0,"tax_amount":0,"total_qty_ordered":1,"order_currency_code":"PLN",
		 "store_name":"PL","customer_is_guest":0,"coupon_code":"BIG","payment":{"method":"checkmo"},
		 "items":[{"sku":"B","name":"Beta","qty_ordered":1,"row_total":100}]}`)
	defer ts.Close()
	from := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	a, err := testService(t, ts.URL).AnalyzeSales(context.Background(), from, to, 100, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.TopCoupons) != 2 {
		t.Fatalf("coupons = %+v", a.TopCoupons)
	}
	if a.TopCoupons[0].Code != "BIG" || a.TopCoupons[0].Revenue != 100 {
		t.Fatalf("top coupon = %+v, want BIG with revenue 100 (one order beats two)", a.TopCoupons[0])
	}
	if a.TopCoupons[1].Code != "MANY" || a.TopCoupons[1].Orders != 2 || a.TopCoupons[1].Revenue != 20 {
		t.Fatalf("second coupon = %+v", a.TopCoupons[1])
	}
}

// TestAnalyzeSalesAOVBandsUseNumericBounds pins the currency-free bands: a PLN
// store must not get "$100-300" labels, and the numeric bounds are exposed so a
// client can format them in the report currency.
func TestAnalyzeSalesAOVBandsUseNumericBounds(t *testing.T) {
	ts := analysisFixtureServer(t, `
		{"entity_id":1,"created_at":"2026-11-19 10:00:00","status":"complete","grand_total":50,"subtotal":50,
		 "discount_amount":0,"shipping_amount":0,"tax_amount":0,"total_qty_ordered":1,"order_currency_code":"PLN",
		 "store_name":"PL","customer_is_guest":0,"payment":{"method":"checkmo"},
		 "items":[{"sku":"A","name":"Alpha","qty_ordered":1,"row_total":50}]}`)
	defer ts.Close()
	from := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	a, err := testService(t, ts.URL).AnalyzeSales(context.Background(), from, to, 100, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if a.Currency != "PLN" {
		t.Fatalf("currency = %q, want PLN", a.Currency)
	}
	if len(a.AOVBands) != 6 {
		t.Fatalf("bands = %+v", a.AOVBands)
	}
	for _, band := range a.AOVBands {
		if strings.Contains(band.Label, "$") {
			t.Fatalf("band label %q hardcodes a currency symbol", band.Label)
		}
	}
	if a.AOVBands[0].Label != "<100" || a.AOVBands[0].Lower != 0 || a.AOVBands[0].Upper != 100 || a.AOVBands[0].Orders != 1 {
		t.Fatalf("first band = %+v", a.AOVBands[0])
	}
	if a.AOVBands[5].Upper != 0 || a.AOVBands[5].Lower != 2000 {
		t.Fatalf("open-ended top band = %+v, want upper 0 and lower 2000", a.AOVBands[5])
	}
}

// TestAnalyzeSalesReportsListTotals pins the anti-silent-truncation counters: a
// capped list must say how many rows exist in total.
func TestAnalyzeSalesReportsListTotals(t *testing.T) {
	// 12 orders, each with 5 distinct SKUs (60 SKUs) and its own coupon code, and
	// every SKU refunded: all three capped lists truncate.
	var orders []string
	for i := 1; i <= 12; i++ {
		var items []string
		for j := 0; j < 5; j++ {
			items = append(items, fmt.Sprintf(
				`{"sku":"S%d-%d","name":"Item","qty_ordered":1,"row_total":10,"amount_refunded":1,"qty_refunded":1}`, i, j))
		}
		orders = append(orders, fmt.Sprintf(
			`{"entity_id":%d,"created_at":"2026-11-%02d 10:00:00","status":"complete","grand_total":50,"subtotal":50,`+
				`"discount_amount":0,"shipping_amount":0,"tax_amount":0,"total_qty_ordered":5,"order_currency_code":"PLN",`+
				`"store_name":"PL","customer_is_guest":0,"coupon_code":"C%d","payment":{"method":"checkmo"},"items":[%s]}`,
			i, i, i, strings.Join(items, ",")))
	}
	ts := analysisFixtureServer(t, strings.Join(orders, ","))
	defer ts.Close()
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	a, err := testService(t, ts.URL).AnalyzeSales(context.Background(), from, to, 100, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if a.SKUsTotal != 60 || len(a.TopSKUs) != topSKULimit {
		t.Fatalf("skus_total = %d len(top_skus) = %d, want 60 and %d", a.SKUsTotal, len(a.TopSKUs), topSKULimit)
	}
	if a.CouponsTotal != 12 || len(a.TopCoupons) != topCouponLimit {
		t.Fatalf("coupons_total = %d len(top_coupons) = %d, want 12 and %d", a.CouponsTotal, len(a.TopCoupons), topCouponLimit)
	}
	if a.RefundedSKUsTotal != 60 || len(a.RefundedSKUs) != topRefundSKULimit {
		t.Fatalf("refunded_skus_total = %d len(refunded_skus) = %d, want 60 and %d",
			a.RefundedSKUsTotal, len(a.RefundedSKUs), topRefundSKULimit)
	}
}

// TestAnalyzeSalesRoundsMoneyToCents pins the money serialization: aggregating
// decimals in binary floating point must not leak tails like 566.6800000000001.
func TestAnalyzeSalesRoundsMoneyToCents(t *testing.T) {
	ts := analysisFixtureServer(t, `
		{"entity_id":1,"created_at":"2026-11-19 10:00:00","status":"complete","grand_total":0.1,"subtotal":0.1,
		 "discount_amount":0,"shipping_amount":0,"tax_amount":0,"total_qty_ordered":1,"order_currency_code":"PLN",
		 "store_name":"PL","customer_is_guest":0,"payment":{"method":"checkmo"},
		 "items":[{"sku":"A","name":"Alpha","qty_ordered":1,"row_total":0.1}]},
		{"entity_id":2,"created_at":"2026-11-19 11:00:00","status":"complete","grand_total":0.2,"subtotal":0.2,
		 "discount_amount":0,"shipping_amount":0,"tax_amount":0,"total_qty_ordered":1,"order_currency_code":"PLN",
		 "store_name":"PL","customer_is_guest":0,"payment":{"method":"checkmo"},
		 "items":[{"sku":"B","name":"Beta","qty_ordered":1,"row_total":0.2}]},
		{"entity_id":3,"created_at":"2026-11-19 12:00:00","status":"complete","grand_total":0.07,"subtotal":0.07,
		 "discount_amount":0,"shipping_amount":0,"tax_amount":0,"total_qty_ordered":1,"order_currency_code":"PLN",
		 "store_name":"PL","customer_is_guest":0,"payment":{"method":"checkmo"},
		 "items":[{"sku":"C","name":"Gamma","qty_ordered":1,"row_total":0.07}]}`)
	defer ts.Close()
	from := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	a, err := testService(t, ts.URL).AnalyzeSales(context.Background(), from, to, 100, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	// 0.1+0.2+0.07 is 0.37000000000000005 in binary floating point.
	if a.Revenue != 0.37 {
		t.Fatalf("revenue = %v, want 0.37 with no floating-point tail", a.Revenue)
	}
	for _, day := range a.ByDay {
		if day.Revenue != 0.37 {
			t.Fatalf("by_day revenue = %v, want 0.37", day.Revenue)
		}
	}
	for _, sku := range a.TopSKUs {
		if sku.Revenue != 0.1 && sku.Revenue != 0.2 && sku.Revenue != 0.07 {
			t.Fatalf("sku revenue = %v, want a clean 2-decimal amount", sku.Revenue)
		}
	}
}

// jsonStringField reads one string field out of a JSON object literal.
func jsonStringField(row, field string) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(row), &fields); err != nil {
		return ""
	}
	var value string
	_ = json.Unmarshal(fields[field], &value)
	return value
}

// analysisWindowServer serves order rows and honours the created_at window
// filters the way Magento does, so a narrow window really returns fewer orders.
func analysisWindowServer(t *testing.T, rows ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/rest/default/V1/orders" {
			_, _ = w.Write([]byte(`{"items":[],"total_count":0}`))
			return
		}
		from := r.URL.Query().Get("searchCriteria[filter_groups][0][filters][0][value]")
		to := r.URL.Query().Get("searchCriteria[filter_groups][1][filters][0][value]")
		var kept []string
		for _, row := range rows {
			created := jsonStringField(row, "created_at")
			if created < from || created >= to {
				continue
			}
			kept = append(kept, row)
		}
		_, _ = fmt.Fprintf(w, `{"total_count":%d,"items":[%s]}`, len(kept), strings.Join(kept, ","))
	}))
}

// TestAnalyzeSalesCustomerSplitIsWindowScoped pins what new_orders/repeat_orders
// actually measure: orders per registered customer inside the window. The same
// customer is "repeat" in a window holding two of their orders and "new" in a
// window holding one, which is exactly why the fields must not be described as
// first-time/returning customers.
func TestAnalyzeSalesCustomerSplitIsWindowScoped(t *testing.T) {
	ts := analysisWindowServer(t,
		`{"entity_id":1,"created_at":"2026-11-19 10:00:00","status":"complete","grand_total":10,"subtotal":10,
		 "discount_amount":0,"shipping_amount":0,"tax_amount":0,"total_qty_ordered":1,"order_currency_code":"PLN",
		 "store_name":"PL","customer_id":7,"customer_is_guest":0,"payment":{"method":"checkmo"},
		 "items":[{"sku":"A","name":"Alpha","qty_ordered":1,"row_total":10}]}`,
		`{"entity_id":2,"created_at":"2026-11-19 11:00:00","status":"complete","grand_total":20,"subtotal":20,
		 "discount_amount":0,"shipping_amount":0,"tax_amount":0,"total_qty_ordered":1,"order_currency_code":"PLN",
		 "store_name":"PL","customer_id":7,"customer_is_guest":0,"payment":{"method":"checkmo"},
		 "items":[{"sku":"A","name":"Alpha","qty_ordered":1,"row_total":20}]}`)
	defer ts.Close()
	svc := testService(t, ts.URL)
	from := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	wide, err := svc.AnalyzeSales(context.Background(), from, to, 100, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if wide.CustomerSplitBasis != customerSplitBasisWindow {
		t.Fatalf("customer_split_basis = %q", wide.CustomerSplitBasis)
	}
	if wide.RepeatOrders != 2 || wide.NewOrders != 0 || wide.RepeatCustomers != 1 {
		t.Fatalf("wide window split = %+v, want both orders counted as repeat", wide)
	}
	// The same customer with a single order in the window is reported as "new",
	// because only this window is read.
	narrowFrom := from.Add(10 * time.Hour) // holds only the 10:00 order
	narrow, err := svc.AnalyzeSales(context.Background(), narrowFrom, narrowFrom.Add(30*time.Minute), 100, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if narrow.Orders != 1 || narrow.NewOrders != 1 || narrow.RepeatOrders != 0 {
		t.Fatalf("narrow window split = %+v, want the single order counted as new", narrow)
	}
}

// TestReportFieldDescriptionsStateTheirBasis pins the documentation half of the
// caliber fixes. A description is part of the contract: if it drifts back to
// "Revenue for this window" or "first-time customers", callers are misled even
// though the numbers are unchanged.
func TestReportFieldDescriptionsStateTheirBasis(t *testing.T) {
	tagOf := func(value any, field string) string {
		t.Helper()
		structField, ok := reflect.TypeOf(value).FieldByName(field)
		if !ok {
			t.Fatalf("%T has no field %s", value, field)
		}
		return structField.Tag.Get("jsonschema")
	}
	cases := []struct {
		name    string
		desc    string
		mustSay []string
	}{
		{"top_skus.revenue", tagOf(SKUStat{}, "Revenue"), []string{"row_total", "subtotal, not revenue"}},
		{"new_orders", tagOf(SalesAnalysis{}, "NewOrders"), []string{"only order in this window", "not first-time-ever"}},
		{"repeat_orders", tagOf(SalesAnalysis{}, "RepeatOrders"), []string{"same window", "not a cross-window"}},
		{"customer_split_basis", tagOf(SalesAnalysis{}, "CustomerSplitBasis"), []string{customerSplitBasisWindow}},
		{"sku_revenue_basis", tagOf(SalesAnalysis{}, "SKURevenueBasis"), []string{skuRevenueBasisLineSubtotal}},
		{"refunded_amount", tagOf(SalesAnalysis{}, "RefundedAmount"), []string{"total_refunded", "shipping"}},
		{"refunded_items_amount", tagOf(SalesAnalysis{}, "RefundedItemsAmount"), []string{"amount_refunded"}},
		{"ship_latency.avg", tagOf(ShipLatency{}, "Avg"), []string{"in days"}},
		{"aov_bands.label", tagOf(BandStat{}, "Label"), []string{"without a currency symbol"}},
		{"country_id", tagOf(CountryStat{}, "CountryID"), []string{"ISO 3166-1", "UNKNOWN"}},
		{"top_skus", tagOf(SalesAnalysis{}, "TopSKUs"), []string{"capped at 50", "skus_total"}},
		{"top_coupons", tagOf(SalesAnalysis{}, "TopCoupons"), []string{"by revenue", "capped at 10"}},
		{"refunded_skus", tagOf(SalesAnalysis{}, "RefundedSKUs"), []string{"capped at 10"}},
		{"by_day", tagOf(SalesAnalysis{}, "ByDay"), []string{"report timezone"}},
		{"by_country", tagOf(SalesAnalysis{}, "ByCountry"), []string{"UNKNOWN"}},
		{"revenue", tagOf(SalesAnalysis{}, "Revenue"), []string{"includes every order status"}},
	}
	for _, tc := range cases {
		for _, want := range tc.mustSay {
			// Descriptions are prose: compare case-insensitively so a capitalized
			// sentence start does not fail the contract check.
			if !strings.Contains(strings.ToLower(tc.desc), strings.ToLower(want)) {
				t.Fatalf("%s description %q must mention %q", tc.name, tc.desc, want)
			}
		}
	}
	if strings.Contains(tagOf(BandStat{}, "Label"), "$") {
		t.Fatal("aov_bands label description must not hardcode a currency symbol")
	}
}

// TestAnalyzeSalesRefundedAmountFallsBackWhenTotalIsAbsent proves the refund
// caliber does not silently read an omitted total_refunded as "no refunds":
// Magento omits the field on orders without refunds, so an absent field on an
// order that does carry refunds must be reconstructed from the order's parts.
func TestAnalyzeSalesRefundedAmountFallsBackWhenTotalIsAbsent(t *testing.T) {
	ts := analysisFixtureServer(t, `
		{"entity_id":1,"created_at":"2026-11-19 10:00:00","status":"closed","grand_total":39,"subtotal":34,
		 "discount_amount":0,"shipping_amount":5,"shipping_refunded":5,"tax_amount":0,"total_qty_ordered":1,
		 "order_currency_code":"PLN","store_name":"PL","customer_is_guest":0,"payment":{"method":"checkmo"},
		 "items":[{"sku":"A","name":"Alpha","qty_ordered":1,"row_total":34,"amount_refunded":17,"qty_refunded":1}]}`)
	defer ts.Close()
	from := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	a, err := testService(t, ts.URL).AnalyzeSales(context.Background(), from, to, 100, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if a.RefundedItemsAmount != 17 {
		t.Fatalf("refunded_items_amount = %v, want 17", a.RefundedItemsAmount)
	}
	if a.RefundedAmount != 22 {
		t.Fatalf("refunded_amount = %v, want 22 (17 item refunds + 5 shipping, reconstructed because total_refunded was absent)", a.RefundedAmount)
	}
}
