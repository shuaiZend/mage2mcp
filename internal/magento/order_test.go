// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestGetOrderReturnsProjection(t *testing.T) {
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"entity_id": 42, "increment_id": "000000042", "status": "processing", "state": "processing",
			"store_id": 1, "customer_id": 7, "customer_email": "buyer@example.com",
			"created_at": "2026-11-17 10:00:00", "grand_total": 129.99, "total_qty_ordered": 2,
			"order_currency_code": "USD",
			"items": [{"item_id": 1, "sku": "SKU-1", "name": "Widget", "product_type": "simple",
				"qty_ordered": 2, "price": 64.995, "row_total": 129.99}],
			"billing_address": {"city": "Shanghai"}
		}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	order, err := svc.GetOrder(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rest/default/V1/orders/42" {
		t.Fatalf("path = %q", path)
	}
	if order.EntityID != 42 || order.Status != "processing" || order.GrandTotal != 129.99 || order.Currency != "USD" {
		t.Fatalf("order = %+v", order)
	}
	if len(order.Items) != 1 || order.Items[0].SKU != "SKU-1" || order.Items[0].QtyOrdered != 2 {
		t.Fatalf("items = %+v", order.Items)
	}
}

func TestGetOrderRejectsInvalidID(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	if _, err := svc.GetOrder(context.Background(), 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("err = %v", err)
	}
}

func TestSearchOrdersBuildsCriteria(t *testing.T) {
	var query url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"entity_id":1,"increment_id":"000000001","status":"complete","grand_total":10}],"total_count":1}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	orders, total, err := svc.SearchOrders(context.Background(), OrderQuery{
		Status: "complete", IncrementID: "000000001", CreatedFrom: "2026-11-01", CreatedTo: "2026-11-30",
		Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(orders) != 1 || orders[0].IncrementID != "000000001" {
		t.Fatalf("orders = %+v total=%d", orders, total)
	}
	if query.Get("searchCriteria[pageSize]") != "10" || query.Get("searchCriteria[currentPage]") != "1" {
		t.Fatalf("pagination = %v", query)
	}
	checks := map[string]string{
		"searchCriteria[filter_groups][0][filters][0][field]":          "increment_id",
		"searchCriteria[filter_groups][0][filters][0][value]":          "000000001",
		"searchCriteria[filter_groups][1][filters][0][field]":          "status",
		"searchCriteria[filter_groups][1][filters][0][value]":          "complete",
		"searchCriteria[filter_groups][2][filters][0][value]":          "2026-11-01 00:00:00",
		"searchCriteria[filter_groups][3][filters][0][condition_type]": "lt",
	}
	for key, want := range checks {
		if got := query.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	fields := query.Get("fields")
	if !strings.HasPrefix(fields, "items[") {
		t.Fatalf("fields = %q", fields)
	}
	if strings.Contains(fields, "customer_email") {
		t.Fatal("search must not request customer email")
	}
}

func TestSearchOrdersValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	cases := []OrderQuery{
		{Status: "Not A Status"},
		{State: "../etc"},
		{IncrementID: "bad id!"},
		{Limit: 500},
		{CursorOffset: 3, Limit: 10},
		{CreatedFrom: "yesterday"},
		{CreatedFrom: "2026-11-30", CreatedTo: "2026-11-01"},
	}
	for i, q := range cases {
		if _, _, err := svc.SearchOrders(ctx, q); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
}

func TestSalesSummaryAggregates(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"created_at":"2026-11-17 10:00:00","grand_total":100,"total_qty_ordered":2,"order_currency_code":"USD"},
			{"created_at":"2026-11-18 11:00:00","grand_total":50,"total_qty_ordered":1,"order_currency_code":"USD"},
			{"created_at":"2026-11-18 12:00:00","grand_total":25,"total_qty_ordered":1,"order_currency_code":"USD"}
		],"total_count":3}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	from := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	summary, err := svc.SalesSummary(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalOrders != 3 || summary.TotalRevenue != 175 || summary.TotalItems != 4 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(summary.Days) != 2 || summary.Days[0].Date != "2026-11-17" || summary.Days[1].Orders != 2 {
		t.Fatalf("days = %+v", summary.Days)
	}
	if summary.Truncated || summary.MixedCurrency || summary.Currency != "USD" {
		t.Fatalf("flags = %+v", summary)
	}
}

func TestSalesSummaryTruncatedAndMixedCurrency(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"created_at":"2026-11-17 10:00:00","grand_total":10,"order_currency_code":"USD"},
			{"created_at":"2026-11-17 11:00:00","grand_total":10,"order_currency_code":"EUR"}
		],"total_count":50}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	from := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	summary, err := svc.SalesSummary(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Truncated {
		t.Fatalf("truncated = false, want true: %+v", summary)
	}
	if !summary.MixedCurrency {
		t.Fatalf("mixed currency not flagged: %+v", summary)
	}
}

func TestSalesSummaryValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	from := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	if _, err := svc.SalesSummary(ctx, from, from); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty window err = %v", err)
	}
	if _, err := svc.SalesSummary(ctx, from, from.Add(400*24*time.Hour)); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("oversized window err = %v", err)
	}
}
