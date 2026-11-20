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

func TestAddProductLink(t *testing.T) {
	var method, path string
	var body productLinkBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	if err := svc.AddProductLink(context.Background(), ProductLinkInput{
		SKU: "SKU-1", LinkType: "related", LinkedProductSKU: "SKU-2", Position: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/rest/default/V1/products/SKU-1/links" {
		t.Fatalf("request = %s %s", method, path)
	}
	// The endpoint takes an items array. Asserting the wire shape is the point of
	// this test: the previous version asserted an `entity` object, which is what
	// the gateway sent and what a real Magento rejects with
	// `"%fieldName" is required` naming `items`.
	if len(body.Items) != 1 {
		t.Fatalf("body = %+v, want one element in items", body)
	}
	item := body.Items[0]
	if item.SKU != "SKU-1" || item.LinkType != "related" ||
		item.LinkedProductSKU != "SKU-2" || item.Position != 3 {
		t.Fatalf("items[0] = %+v", item)
	}
}

func TestRemoveProductLink(t *testing.T) {
	var method, path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	if err := svc.RemoveProductLink(context.Background(), "SKU-1", "upsell", "SKU-2"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || path != "/rest/default/V1/products/SKU-1/links/upsell/SKU-2" {
		t.Fatalf("request = %s %s", method, path)
	}
}

func TestProductLinkWriteValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	cases := []ProductLinkInput{
		{SKU: "bad sku!", LinkType: "related", LinkedProductSKU: "SKU-2"},
		{SKU: "SKU-1", LinkType: "related", LinkedProductSKU: "bad sku!"},
		{SKU: "SKU-1", LinkType: "related", LinkedProductSKU: "SKU-1"},
		{SKU: "SKU-1", LinkType: "bogus", LinkedProductSKU: "SKU-2"},
		{SKU: "SKU-1", LinkType: "related", LinkedProductSKU: "SKU-2", Position: 10001},
	}
	for i, in := range cases {
		if err := svc.AddProductLink(ctx, in); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
	if err := svc.RemoveProductLink(ctx, "SKU-1", "bogus", "SKU-2"); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("remove type err = %v", err)
	}
	if err := svc.RemoveProductLink(ctx, "SKU-1", "related", "SKU-1"); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("remove self err = %v", err)
	}
}
