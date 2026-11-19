// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestBulkChangeLinksPartialSuccess(t *testing.T) {
	var calls []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if len(calls) == 2 {
			http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`true`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	result, err := svc.BulkChangeLinks(context.Background(), "SKU-1", "related", "add", []string{"SKU-2", "FAIL-1", "SKU-2"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 3 || result.Changed != 1 || result.Failed != 1 || len(result.Rows) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if len(calls) != 2 || calls[0] != "POST /rest/default/V1/products/SKU-1/links" {
		t.Fatalf("calls = %v", calls)
	}
	removed, err := svc.BulkChangeLinks(context.Background(), "SKU-1", "crosssell", "remove", []string{"SKU-3"})
	if err != nil {
		t.Fatal(err)
	}
	if removed.Changed != 1 || calls[len(calls)-1] != "DELETE /rest/default/V1/products/SKU-1/links/crosssell/SKU-3" {
		t.Fatalf("remove result = %+v calls = %v", removed, calls)
	}
}

func TestBulkChangeLinksValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	cases := []struct {
		sku, linkType, action string
		targets               []string
	}{
		{"bad sku!", "related", "add", []string{"SKU-2"}},
		{"SKU-1", "bogus", "add", []string{"SKU-2"}},
		{"SKU-1", "related", "bogus", []string{"SKU-2"}},
		{"SKU-1", "related", "add", nil},
		{"SKU-1", "related", "add", []string{"bad sku!"}},
	}
	for i, tc := range cases {
		if _, err := svc.BulkChangeLinks(ctx, tc.sku, tc.linkType, tc.action, tc.targets); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
	tooMany := make([]string, maxBulkLinks+1)
	for i := range tooMany {
		tooMany[i] = "SKU"
	}
	if _, err := svc.BulkChangeLinks(ctx, "SKU-1", "related", "add", tooMany); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("ceiling err = %v", err)
	}
}

func TestProductLinksFallbackToProduct(t *testing.T) {
	var calls []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/products/SKU-1/links":
			http.NotFound(w, r)
		case "/rest/default/V1/products/SKU-1":
			_, _ = w.Write([]byte(`{"id":1,"sku":"SKU-1","name":"Widget","type_id":"simple","price":9.99,"attribute_set_id":4,
				"product_links":[{"sku":"SKU-1","link_type":"related","linked_product_sku":"SKU-2","position":1},
					{"sku":"SKU-1","link_type":"upsell","linked_product_sku":"SKU-3","position":0}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	links, err := svc.ProductLinks(context.Background(), "SKU-1", "related")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || len(links) != 1 || links[0].LinkedProductSKU != "SKU-2" {
		t.Fatalf("calls = %v links = %+v", calls, links)
	}
}
