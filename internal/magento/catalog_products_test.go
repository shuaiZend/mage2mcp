// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestConfigurableChildren(t *testing.T) {
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":2,"sku":"PHONE-01-BLK","name":"Phone Black","type_id":"simple","price":699,"attribute_set_id":4}]`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	children, err := svc.Children(context.Background(), "PHONE-01")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rest/default/V1/configurable-products/PHONE-01/children" {
		t.Fatalf("path = %q", path)
	}
	if len(children) != 1 || children[0].SKU != "PHONE-01-BLK" {
		t.Fatalf("children = %+v", children)
	}
}

func TestAttributeOptions(t *testing.T) {
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"label":"Red","value":"12"},{"label":"Blue","value":"13"}]`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	options, err := svc.AttributesOptions(context.Background(), "color")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rest/default/V1/products/attributes/color/options" {
		t.Fatalf("path = %q", path)
	}
	if len(options) != 2 || options[0].Label != "Red" || options[0].Value != "12" {
		t.Fatalf("options = %+v", options)
	}
	if _, err := svc.AttributesOptions(context.Background(), "Bad Code!"); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("validation err = %v", err)
	}
}

func TestCategoryProducts(t *testing.T) {
	var query url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":1,"sku":"SKU-1","name":"Widget","type_id":"simple","price":9.99,"attribute_set_id":4}],"total_count":7}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	products, total, err := svc.CategoryProducts(context.Background(), 12, 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	if total != 7 || len(products) != 1 || products[0].SKU != "SKU-1" {
		t.Fatalf("products = %+v total=%d", products, total)
	}
	if query.Get("searchCriteria[pageSize]") != "10" || query.Get("searchCriteria[currentPage]") != "3" {
		t.Fatalf("pagination = %v", query)
	}
}

func TestCategoryProductsBareArray(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1,"sku":"SKU-1"}]`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	products, total, err := svc.CategoryProducts(context.Background(), 12, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(products) != 1 {
		t.Fatalf("products = %+v total=%d", products, total)
	}
}

func TestCategoryProductsValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if _, _, err := svc.CategoryProducts(ctx, 0, 10, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("category err = %v", err)
	}
	if _, _, err := svc.CategoryProducts(ctx, 1, 500, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("limit err = %v", err)
	}
	if _, _, err := svc.CategoryProducts(ctx, 1, 10, 3); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("offset err = %v", err)
	}
}
