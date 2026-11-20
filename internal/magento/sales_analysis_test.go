// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"net/http"
	"net/http/httptest"
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
