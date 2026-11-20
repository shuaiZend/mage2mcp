// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
			 "discount_amount":10,"shipping_amount":15,"tax_amount":5,"total_qty_ordered":1,"order_currency_code":"USD",
			 "store_name":"Giztop","customer_is_guest":0,"payment":{"method":"paypal_express"},
			 "extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"US"}}}]},
			 "items":[{"sku":"A","name":"Alpha","product_type":"configurable","qty_ordered":1,"row_total":90,"amount_refunded":0,"qty_refunded":0},
			          {"sku":"A","name":"Alpha","product_type":"simple","parent_item_id":9,"qty_ordered":1,"row_total":0}]},
			{"entity_id":2,"created_at":"2026-11-19 12:00:00","status":"complete","grand_total":50,"subtotal":40,
			 "discount_amount":0,"shipping_amount":10,"tax_amount":0,"total_qty_ordered":2,"order_currency_code":"USD",
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
