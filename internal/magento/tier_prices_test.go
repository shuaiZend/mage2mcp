// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
