// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestTierPricesRead(t *testing.T) {
	var query string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query().Get("skus")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"sku":"SKU-1","price":8.5,"price_type":"fixed","website_id":0,"customer_group":"ALL GROUPS","quantity":10}]`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	prices, err := svc.TierPrices(context.Background(), "SKU-1")
	if err != nil {
		t.Fatal(err)
	}
	if query != "SKU-1" {
		t.Fatalf("skus query = %q", query)
	}
	if len(prices) != 1 || prices[0].Price != 8.5 || prices[0].Quantity != 10 {
		t.Fatalf("prices = %+v", prices)
	}
}

func TestSetTierPrices(t *testing.T) {
	var method, path string
	var body tierPriceSetBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	err := svc.SetTierPrices(context.Background(), "SKU-1", []TierPrice{
		{Price: 8.5, WebsiteID: 0, CustomerGroup: "ALL GROUPS", Quantity: 10},
		{Price: 7.5, WebsiteID: 0, CustomerGroup: "ALL GROUPS", Quantity: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/rest/default/V1/products/tier-prices" {
		t.Fatalf("request = %s %s", method, path)
	}
	if len(body.Prices) != 2 || body.Prices[0].SKU != "SKU-1" || body.Prices[0].PriceType != "fixed" {
		t.Fatalf("body = %+v", body)
	}
	if body.Prices[1].Quantity != 20 {
		t.Fatalf("body = %+v", body)
	}
}

func TestSetTierPricesValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	valid := TierPrice{Price: 8.5, CustomerGroup: "ALL GROUPS", Quantity: 10}
	cases := [][]TierPrice{
		nil,
		{{Price: 0, CustomerGroup: "ALL GROUPS", Quantity: 1}},
		{{Price: 1, CustomerGroup: "ALL GROUPS", Quantity: 0}},
		{{Price: 1, CustomerGroup: "", Quantity: 1}},
		{{Price: 1, CustomerGroup: "bad*group", Quantity: 1}},
		{{Price: 1, WebsiteID: -1, CustomerGroup: "ALL GROUPS", Quantity: 1}},
		{valid, valid},
		{{Price: 1, SKU: "OTHER", CustomerGroup: "ALL GROUPS", Quantity: 1}},
	}
	for i, prices := range cases {
		if err := svc.SetTierPrices(ctx, "SKU-1", prices); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
	tooMany := make([]TierPrice, maxTierPrices+1)
	for i := range tooMany {
		tooMany[i] = TierPrice{Price: 1, CustomerGroup: "ALL GROUPS", Quantity: float64(i + 1)}
	}
	if err := svc.SetTierPrices(ctx, "SKU-1", tooMany); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("ceiling err = %v", err)
	}
}

func TestTierPricesFallbackToProduct(t *testing.T) {
	var calls []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/products/tier-prices":
			http.NotFound(w, r)
		case "/rest/default/V1/products/SKU-1":
			_, _ = w.Write([]byte(`{"id":1,"sku":"SKU-1","name":"Widget","type_id":"simple","price":9.99,"attribute_set_id":4,
				"tier_prices":[{"sku":"SKU-1","price":8.5,"price_type":"fixed","website_id":0,"customer_group_id":0,"qty":10}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	prices, err := svc.TierPrices(context.Background(), "SKU-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "/rest/default/V1/products/tier-prices" || calls[1] != "/rest/default/V1/products/SKU-1" {
		t.Fatalf("calls = %v", calls)
	}
	if len(prices) != 1 || prices[0].Price != 8.5 || prices[0].Quantity != 10 || prices[0].CustomerGroup != "0" {
		t.Fatalf("prices = %+v", prices)
	}
}

// U-08: the product resource embeds tier prices as {customer_group_id, qty,
// value}. Reading them with the write interface's field names (price/quantity)
// reported price 0 and an empty sku for every row. Reverting the normalisation
// makes this test fail.
func TestTierPricesReadsProductResourceFieldNames(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/products/tier-prices":
			http.NotFound(w, r)
		case "/rest/default/V1/products/SKU-1":
			_, _ = w.Write([]byte(`{"id":1,"sku":"SKU-1","tier_prices":[
				{"customer_group_id":0,"qty":3,"value":30},
				{"customer_group_id":2,"qty":5,"value":28.5}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	prices, err := testService(t, ts.URL).TierPrices(context.Background(), "SKU-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(prices) != 2 {
		t.Fatalf("prices = %+v", prices)
	}
	if prices[0].Price != 30 || prices[0].Quantity != 3 || prices[0].CustomerGroup != "0" {
		t.Fatalf("first row = %+v, want price 30 qty 3 group 0", prices[0])
	}
	if prices[1].Price != 28.5 || prices[1].Quantity != 5 || prices[1].CustomerGroup != "2" {
		t.Fatalf("second row = %+v, want price 28.5 qty 5 group 2", prices[1])
	}
}

// U-07: Magento answers POST /products/tier-prices with HTTP 200 even when the
// write failed, carrying the reason in a JSON array. Reporting set:true with zero
// rows written is the defect; reverting the decode makes this test fail.
func TestSetTierPricesRejectsBusinessErrorIn200(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"message":"The product that was requested doesn't exist. Verify the product and try again."}]`))
	}))
	defer ts.Close()
	err := testService(t, ts.URL).SetTierPrices(context.Background(), "SKU-1", []TierPrice{
		{Price: 8.5, WebsiteID: 0, CustomerGroup: "0", Quantity: 10},
	})
	if err == nil {
		t.Fatal("a 200 business error array must not be reported as success")
	}
	if got := errs.CodeOf(err); got != errs.CodeValidationFailed {
		t.Fatalf("code = %s, want validation_failed", got)
	}
	var e *errs.Error
	if errors.As(err, &e) && !strings.Contains(e.Details["upstream_message"], "doesn't exist") {
		t.Fatalf("details = %v, want the upstream reason", e.Details)
	}
}

// A successful write (Magento answers true) must still succeed.
func TestSetTierPricesAcceptsBooleanSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer ts.Close()
	if err := testService(t, ts.URL).SetTierPrices(context.Background(), "SKU-1", []TierPrice{
		{Price: 8.5, WebsiteID: 0, CustomerGroup: "0", Quantity: 10},
	}); err != nil {
		t.Fatalf("a boolean success body must not fail: %v", err)
	}
}
