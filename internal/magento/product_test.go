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

func TestProductOptions(t *testing.T) {
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"option_id":1,"title":"Gift wrap","type":"checkbox","is_require":false,"sort_order":1,
			"values":[{"option_type_id":2,"title":"Yes","price":2.5,"price_type":"fixed","sku":"GIFT","sort_order":1}]}]`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	options, err := svc.ProductOptions(context.Background(), "SKU-1")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rest/default/V1/products/SKU-1/options" {
		t.Fatalf("path = %s", path)
	}
	if len(options) != 1 || options[0].Title != "Gift wrap" || len(options[0].Values) != 1 || options[0].Values[0].Price != 2.5 {
		t.Fatalf("options = %+v", options)
	}
	if _, err := svc.ProductOptions(context.Background(), "bad sku!"); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("validation err = %v", err)
	}
}

func TestCreateProductOption(t *testing.T) {
	var path string
	var body customOptionBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"option_id":5,"title":"Gift wrap","type":"drop_down","is_require":false,"sort_order":0,
			"values":[{"option_type_id":6,"title":"Yes","price":2.5,"price_type":"fixed","sort_order":0}]}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	option, err := svc.CreateProductOption(context.Background(), "SKU-1", CustomOptionInput{
		Title: "Gift wrap", Type: "drop_down",
		Values: []CustomOptionValueInput{{Title: "Yes", Price: 2.5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rest/default/V1/products/SKU-1/options" {
		t.Fatalf("path = %s", path)
	}
	if body.Option.Type != "drop_down" || len(body.Option.Values) != 1 || body.Option.Values[0].PriceType != "fixed" {
		t.Fatalf("body = %+v", body.Option)
	}
	if option.OptionID != 5 || option.Values[0].Price != 2.5 {
		t.Fatalf("option = %+v", option)
	}
}

func TestCreateProductOptionValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	valid := CustomOptionInput{Title: "Gift wrap", Type: "drop_down",
		Values: []CustomOptionValueInput{{Title: "Yes", Price: 1}}}
	cases := []struct {
		sku string
		in  CustomOptionInput
	}{
		{"bad sku!", valid},
		{"SKU-1", CustomOptionInput{Title: "", Type: "field"}},
		{"SKU-1", CustomOptionInput{Title: "X", Type: "bogus"}},
		{"SKU-1", CustomOptionInput{Title: "X", Type: "drop_down"}},
		{"SKU-1", CustomOptionInput{Title: "X", Type: "field", Values: []CustomOptionValueInput{{Title: "Y"}}}},
		{"SKU-1", CustomOptionInput{Title: "X", Type: "drop_down", Values: []CustomOptionValueInput{{Title: "", Price: 1}}}},
		{"SKU-1", CustomOptionInput{Title: "X", Type: "drop_down", Values: []CustomOptionValueInput{{Title: "Y", Price: -1}}}},
		{"SKU-1", CustomOptionInput{Title: "X", Type: "drop_down", Values: []CustomOptionValueInput{{Title: "Y", Price: 1, PriceType: "weird"}}}},
	}
	for i, tc := range cases {
		if _, err := svc.CreateProductOption(ctx, tc.sku, tc.in); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
}

func TestUpdateAndDeleteProductOption(t *testing.T) {
	var method, path string
	var body customOptionBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			_, _ = w.Write([]byte(`true`))
			return
		}
		_, _ = w.Write([]byte(`{"option_id":5,"title":"Gift wrap","type":"drop_down","values":[]}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	option, err := svc.UpdateProductOption(context.Background(), "SKU-1", 5, CustomOptionInput{
		Title: "Gift wrap", Type: "drop_down", Values: []CustomOptionValueInput{{Title: "Yes", Price: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut || path != "/rest/default/V1/products/SKU-1/options/5" {
		t.Fatalf("request = %s %s", method, path)
	}
	if body.Option.OptionID != 5 || body.Option.Title != "Gift wrap" {
		t.Fatalf("body = %+v", body.Option)
	}
	if option.OptionID != 5 {
		t.Fatalf("option = %+v", option)
	}
	if err := svc.DeleteProductOption(context.Background(), "SKU-1", 5); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || path != "/rest/default/V1/products/SKU-1/options/5" {
		t.Fatalf("delete = %s %s", method, path)
	}
	if _, err := svc.UpdateProductOption(context.Background(), "SKU-1", 0, CustomOptionInput{Title: "X", Type: "field"}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("update id err = %v", err)
	}
	if err := svc.DeleteProductOption(context.Background(), "SKU-1", 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("delete id err = %v", err)
	}
}
