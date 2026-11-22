// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	mcpserver "github.com/shuaiZend/mage2mcp/internal/mcp/server"
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

func TestProductUnmarshalCategories(t *testing.T) {
	var product Product
	raw := []byte(`{"id":1,"sku":"SKU-1","name":"Widget","type_id":"simple","price":9.99,"attribute_set_id":4,
		"extension_attributes":{"category_links":[{"category_id":"10","position":0},{"category_id":11,"position":1}],"website_ids":[1]}}`)
	if err := json.Unmarshal(raw, &product); err != nil {
		t.Fatal(err)
	}
	if len(product.CategoryIDs) != 2 || product.CategoryIDs[0] != 10 || product.CategoryIDs[1] != 11 {
		t.Fatalf("category ids = %v", product.CategoryIDs)
	}
	if len(product.WebsiteIDs) != 1 || product.WebsiteIDs[0] != 1 {
		t.Fatalf("website ids = %v", product.WebsiteIDs)
	}
}

func TestProductUnmarshalDetailFields(t *testing.T) {
	var product Product
	raw := []byte(`{"id":7,"sku":"SKU-7","name":"Detail","type_id":"simple","price":9.99,"weight":0.25,
		"description":"<p>Rich copy</p>","short_description":"Short","url_key":"detail",
		"meta_title":"Meta","meta_description":"Meta description",
		"custom_attributes":[{"attribute_code":"color","value":"49"},{"attribute_code":"special_price","value":"7.99"}]}`)
	if err := json.Unmarshal(raw, &product); err != nil {
		t.Fatal(err)
	}
	if product.Description != "<p>Rich copy</p>" || product.URLKey != "detail" || product.Weight != 0.25 {
		t.Fatalf("product = %+v", product)
	}
	if product.Attributes["color"] != "49" || product.Attributes["special_price"] != "7.99" {
		t.Fatalf("attributes = %+v", product.Attributes)
	}
}

// instructionsForTest returns the server instructions as a client receives them at
// initialize, so a description can be checked against the same text an agent reads.
func instructionsForTest(t *testing.T) string {
	t.Helper()
	reg := registry.New()
	if err := Register(reg, testService(t, "https://shop.example.com")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: magentoConfirmService(t)})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session.InitializeResult().Instructions
}

// GG-20: Product.Price is the global base price. Reporting it as "what the
// shopper pays" made an agent quote a price the storefront never showed, because a
// store-scope price or special_price overrides it. The field description has to
// say so, and it has to match the reads section of the server instructions.
func TestProductPriceDescriptionStatesItIsTheBasePrice(t *testing.T) {
	field, ok := reflect.TypeOf(Product{}).FieldByName("Price")
	if !ok {
		t.Fatal("Product has no Price field")
	}
	description := field.Tag.Get("jsonschema")
	for _, want := range []string{"Global base price", "not what a shopper pays", "store-scope price", "special_price", "order line"} {
		if !strings.Contains(description, want) {
			t.Fatalf("Product.Price description does not mention %q: %q", want, description)
		}
	}
	// The instructions say the same thing; drift between the two is how an agent
	// ends up trusting one and quoting the other.
	instructions := instructionsForTest(t)
	for _, want := range []string{"product price is the global base price", "read the order line"} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("server instructions no longer say %q", want)
		}
	}
}
