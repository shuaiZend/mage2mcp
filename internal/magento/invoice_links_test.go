// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestProductLinks(t *testing.T) {
	var query url.Values
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"sku":"SKU-1","link_type":"related","linked_product_sku":"SKU-2","position":1}]`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	links, err := svc.ProductLinks(context.Background(), "SKU-1", "related")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rest/default/V1/products/SKU-1/links" || query.Get("linkType") != "related" {
		t.Fatalf("request = %s %v", path, query)
	}
	if len(links) != 1 || links[0].LinkedProductSKU != "SKU-2" || links[0].Position != 1 {
		t.Fatalf("links = %+v", links)
	}
	if _, err := svc.ProductLinks(context.Background(), "SKU-1", "bogus"); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("link type err = %v", err)
	}
	if _, err := svc.ProductLinks(context.Background(), "bad sku!", "related"); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("sku err = %v", err)
	}
}

func TestCreateInvoice(t *testing.T) {
	var method, path string
	var body invoiceCreateBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entity_id":9,"increment_id":"000000009","order_id":42,"grand_total":129.99,"total_qty":2,"order_currency_code":"USD"}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	invoice, err := svc.CreateInvoice(context.Background(), InvoiceCreateInput{
		OrderID: 42, Capture: false,
		Items: []InvoiceLine{{OrderItemID: 1, Qty: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/rest/default/V1/invoices" {
		t.Fatalf("request = %s %s", method, path)
	}
	if body.Entity.OrderID != 42 || body.Entity.Capture || len(body.Entity.Items) != 1 {
		t.Fatalf("body = %+v", body)
	}
	if body.Entity.Items[0].OrderItemID != 1 || body.Entity.Items[0].Qty != 2 {
		t.Fatalf("items = %+v", body.Entity.Items)
	}
	if invoice.EntityID != 9 || invoice.IncrementID != "000000009" || invoice.GrandTotal != 129.99 {
		t.Fatalf("invoice = %+v", invoice)
	}
}

func TestCreateInvoiceValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	cases := []InvoiceCreateInput{
		{OrderID: 0, Items: []InvoiceLine{{OrderItemID: 1, Qty: 1}}},
		{OrderID: 42},
		{OrderID: 42, Items: []InvoiceLine{{OrderItemID: 0, Qty: 1}}},
		{OrderID: 42, Items: []InvoiceLine{{OrderItemID: 1, Qty: 0}}},
		{OrderID: 42, Items: []InvoiceLine{{OrderItemID: 1, Qty: 1}, {OrderItemID: 1, Qty: 1}}},
	}
	for i, in := range cases {
		if _, err := svc.CreateInvoice(ctx, in); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
	tooMany := make([]InvoiceLine, maxInvoiceItems+1)
	for i := range tooMany {
		tooMany[i] = InvoiceLine{OrderItemID: i + 1, Qty: 1}
	}
	if _, err := svc.CreateInvoice(ctx, InvoiceCreateInput{OrderID: 42, Items: tooMany}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("too many err = %v", err)
	}
}
