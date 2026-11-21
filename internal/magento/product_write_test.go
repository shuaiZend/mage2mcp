// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// recordedWrite is one request captured by a fake Magento REST API.
type recordedWrite struct {
	Method string
	Path   string
	Query  url.Values
	Body   []byte
	Auth   string
}

// writeRecorder records requests without consuming their bodies.
type writeRecorder struct {
	mu       sync.Mutex
	requests []recordedWrite
}

func (rec *writeRecorder) add(r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	r.Body = io.NopCloser(bytes.NewReader(body))
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.requests = append(rec.requests, recordedWrite{
		Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.Query(), Body: body,
		Auth: r.Header.Get("Authorization"),
	})
}

func (rec *writeRecorder) all() []recordedWrite {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]recordedWrite(nil), rec.requests...)
}

func (rec *writeRecorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.requests)
}

// newWriteService starts a fake Magento REST API, records every request, and
// returns a service pointed at it. A nil respond answers 200 with "{}".
func newWriteService(t *testing.T, respond func(http.ResponseWriter, *http.Request)) (*Service, *writeRecorder) {
	t.Helper()
	rec := &writeRecorder{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.Header().Set("Content-Type", "application/json")
		if respond != nil {
			respond(w, r)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(ts.Close)
	return testService(t, ts.URL), rec
}

// assertJSONBody compares two JSON documents structurally.
func assertJSONBody(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("request body is not JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("expected body is not JSON: %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("request body = %s, want %s", got, want)
	}
}

// assertOAuthHeader checks that the request carried a signed OAuth 1.0a header.
func assertOAuthHeader(t *testing.T, header string) {
	t.Helper()
	fields := parseOAuthHeader(t, header)
	if fields["oauth_consumer_key"] != "test-key" || fields["oauth_token"] != "test-token" || fields["oauth_signature"] == "" {
		t.Fatalf("unexpected OAuth header: %v", fields)
	}
}

// productSKUFromBody reads product.sku from a recorded write body.
func productSKUFromBody(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Product struct {
			SKU string `json:"sku"`
		} `json:"product"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("product body is not JSON: %v (%s)", err, body)
	}
	return payload.Product.SKU
}

func validCreateProductInput() CreateProductInput {
	return CreateProductInput{
		SKU: "SKU-1", Name: "Test Shirt", AttributeSetID: 4, TypeID: "simple", Price: "19.99",
	}
}

func TestCreateProductRequestAndResponse(t *testing.T) {
	service, rec := newWriteService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":11,"sku":"SKU-1","name":"Test Shirt","type_id":"simple",`+
			`"status":1,"visibility":4,"price":19.99,"attribute_set_id":4}`)
	})
	in := validCreateProductInput()
	in.Status = 1
	in.Visibility = 4
	in.WebsiteIDs = []int{1}
	in.CategoryIDs = []int{3, 4}
	in.Attributes = map[string]string{"color": "12"}

	product, err := service.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if product.ID != 11 || product.SKU != "SKU-1" || product.Name != "Test Shirt" || product.Price != 19.99 {
		t.Fatalf("unexpected product: %+v", product)
	}
	requests := rec.all()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	got := requests[0]
	if got.Method != http.MethodPost || got.Path != "/rest/default/V1/products" {
		t.Fatalf("request = %s %s, want POST /rest/default/V1/products", got.Method, got.Path)
	}
	assertOAuthHeader(t, got.Auth)
	assertJSONBody(t, got.Body, `{"product":{
		"sku":"SKU-1","name":"Test Shirt","attribute_set_id":4,"type_id":"simple",
		"price":"19.99","status":1,"visibility":4,
		"custom_attributes":[{"attribute_code":"color","value":"12"}],
		"extension_attributes":{"website_ids":[1],"category_links":[
			{"category_id":"3","position":0},{"category_id":"4","position":1}]}}}`)
}

func TestCreateProductOmitsEmptyOptionalBlocks(t *testing.T) {
	service, rec := newWriteService(t, nil)
	if _, err := service.Create(context.Background(), validCreateProductInput()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	requests := rec.all()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	// Status and visibility default, and no extension attributes are sent.
	assertJSONBody(t, requests[0].Body, `{"product":{
		"sku":"SKU-1","name":"Test Shirt","attribute_set_id":4,"type_id":"simple",
		"price":"19.99","status":1,"visibility":4}}`)
}

func TestCreateProductAssignsDefaultSourceQuantity(t *testing.T) {
	tests := []struct {
		name   string
		qty    int
		status int
	}{
		{name: "in stock", qty: 5, status: 1},
		{name: "out of stock", qty: 0, status: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service, rec := newWriteService(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/rest/default/V1/products":
					_, _ = io.WriteString(w, `{"id":11,"sku":"SKU-1"}`)
				case "/rest/default/V1/inventory/source-items":
					_, _ = io.WriteString(w, `[]`)
				default:
					http.NotFound(w, r)
				}
			})
			qty := tc.qty
			in := validCreateProductInput()
			in.Quantity = &qty
			if _, err := service.Create(context.Background(), in); err != nil {
				t.Fatalf("Create: %v", err)
			}
			requests := rec.all()
			if len(requests) != 2 {
				t.Fatalf("requests = %d, want 2", len(requests))
			}
			if requests[1].Path != "/rest/default/V1/inventory/source-items" {
				t.Fatalf("second path = %s", requests[1].Path)
			}
			assertJSONBody(t, requests[1].Body, `{"sourceItems":[{"sku":"SKU-1",`+
				`"source_code":"default","quantity":`+strconv.Itoa(tc.qty)+`,"status":`+strconv.Itoa(tc.status)+`}]}`)
		})
	}
}

func TestCreateProductInventoryFailureReportsPartialSuccess(t *testing.T) {
	service, _ := newWriteService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/default/V1/products" {
			_, _ = io.WriteString(w, `{"id":11,"sku":"SKU-1"}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"SECRET-UPSTREAM"}`)
	})
	qty := 3
	in := validCreateProductInput()
	in.Quantity = &qty
	product, err := service.Create(context.Background(), in)
	if product != nil {
		t.Fatalf("Create must not return a product on inventory failure: %+v", product)
	}
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("error is not *errs.Error: %v", err)
	}
	// A partial success must never be marked retryable: a blind retry would
	// create a duplicate product.
	if e.Code != errs.CodeUpstreamUnavailable || e.Retryable {
		t.Fatalf("code = %s retryable = %v, want upstream_unavailable/not retryable", e.Code, e.Retryable)
	}
	if e.Details["sku"] != "SKU-1" || e.Details["stage"] != "inventory" || e.Details["product_created"] != "true" {
		t.Fatalf("details = %v", e.Details)
	}
	if !strings.Contains(err.Error(), "created") {
		t.Fatalf("error must state the product was created: %v", err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error leaked the upstream body: %v", err)
	}
}

func TestUpdateProductSendsOnlyProvidedFields(t *testing.T) {
	service, rec := newWriteService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":11,"sku":"SKU-1","name":"New name","status":2,"price":21.99}`)
	})
	name, price, status := "New name", "21.99", 2
	product, err := service.Update(context.Background(), "SKU-1", UpdateProductInput{
		Name: &name, Price: &price, Status: &status, Attributes: map[string]string{"color": "13"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if product.Name != "New name" || product.Status != 2 || product.Price != 21.99 {
		t.Fatalf("unexpected product: %+v", product)
	}
	requests := rec.all()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	got := requests[0]
	if got.Method != http.MethodPut || got.Path != "/rest/default/V1/products/SKU-1" {
		t.Fatalf("request = %s %s, want PUT /rest/default/V1/products/SKU-1", got.Method, got.Path)
	}
	assertOAuthHeader(t, got.Auth)
	assertJSONBody(t, got.Body, `{"product":{"sku":"SKU-1","name":"New name","price":"21.99",`+
		`"status":2,"custom_attributes":[{"attribute_code":"color","value":"13"}]}}`)
}

func TestProductWriteValidation(t *testing.T) {
	service, rec := newWriteService(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid input reached the upstream: %s %s", r.Method, r.URL.Path)
	})
	name := "New name"
	badPrice, badStatus, badVisibility := "19.99999", 3, 5
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "bad sku", run: func() error {
			in := validCreateProductInput()
			in.SKU = "bad sku"
			_, err := service.Create(context.Background(), in)
			return err
		}},
		{name: "bad price", run: func() error {
			in := validCreateProductInput()
			in.Price = "19.99999"
			_, err := service.Create(context.Background(), in)
			return err
		}},
		{name: "bad attribute code", run: func() error {
			in := validCreateProductInput()
			in.Attributes = map[string]string{"Color": "12"}
			_, err := service.Create(context.Background(), in)
			return err
		}},
		{name: "empty name", run: func() error {
			in := validCreateProductInput()
			in.Name = "   "
			_, err := service.Create(context.Background(), in)
			return err
		}},
		{name: "bad type", run: func() error {
			in := validCreateProductInput()
			in.TypeID = "grouped"
			_, err := service.Create(context.Background(), in)
			return err
		}},
		{name: "zero attribute set", run: func() error {
			in := validCreateProductInput()
			in.AttributeSetID = 0
			_, err := service.Create(context.Background(), in)
			return err
		}},
		{name: "bad status", run: func() error {
			in := validCreateProductInput()
			in.Status = 3
			_, err := service.Create(context.Background(), in)
			return err
		}},
		{name: "bad visibility", run: func() error {
			in := validCreateProductInput()
			in.Visibility = 5
			_, err := service.Create(context.Background(), in)
			return err
		}},
		{name: "bad website id", run: func() error {
			in := validCreateProductInput()
			in.WebsiteIDs = []int{0}
			_, err := service.Create(context.Background(), in)
			return err
		}},
		{name: "negative quantity", run: func() error {
			in := validCreateProductInput()
			qty := -1
			in.Quantity = &qty
			_, err := service.Create(context.Background(), in)
			return err
		}},
		{name: "update bad sku", run: func() error {
			_, err := service.Update(context.Background(), "bad sku", UpdateProductInput{Name: &name})
			return err
		}},
		{name: "update with no fields", run: func() error {
			_, err := service.Update(context.Background(), "SKU-1", UpdateProductInput{})
			return err
		}},
		{name: "update bad price", run: func() error {
			_, err := service.Update(context.Background(), "SKU-1", UpdateProductInput{Price: &badPrice})
			return err
		}},
		{name: "update bad status", run: func() error {
			_, err := service.Update(context.Background(), "SKU-1", UpdateProductInput{Status: &badStatus})
			return err
		}},
		{name: "update bad visibility", run: func() error {
			_, err := service.Update(context.Background(), "SKU-1", UpdateProductInput{Visibility: &badVisibility})
			return err
		}},
		{name: "update bad attribute code", run: func() error {
			_, err := service.Update(context.Background(), "SKU-1", UpdateProductInput{
				Attributes: map[string]string{"Bad-Code": "1"},
			})
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if code := errs.CodeOf(err); code != errs.CodeInvalidArgument {
				t.Fatalf("code = %s, want invalid_argument (%v)", code, err)
			}
		})
	}
	if rec.count() != 0 {
		t.Fatalf("invalid input reached the upstream %d times", rec.count())
	}
}

func TestProductWriteErrorMapping(t *testing.T) {
	tests := []struct {
		status    int
		code      errs.Code
		retryable bool
	}{
		{status: http.StatusBadRequest, code: errs.CodeValidationFailed},
		{status: http.StatusUnauthorized, code: errs.CodeUnauthenticated},
		{status: http.StatusForbidden, code: errs.CodePermissionDenied},
		{status: http.StatusNotFound, code: errs.CodeNotFound},
		{status: http.StatusConflict, code: errs.CodeConflict},
		{status: http.StatusTooManyRequests, code: errs.CodeRateLimited, retryable: true},
		{status: http.StatusInternalServerError, code: errs.CodeUpstreamUnavailable, retryable: true},
	}
	for _, tc := range tests {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			service, _ := newWriteService(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"message":"SECRET-UPSTREAM-BODY"}`)
			})
			_, err := service.Create(context.Background(), validCreateProductInput())
			var e *errs.Error
			if !errors.As(err, &e) {
				t.Fatalf("error is not *errs.Error: %v", err)
			}
			if e.Code != tc.code || e.Retryable != tc.retryable {
				t.Fatalf("code = %s retryable = %v, want %s/%v", e.Code, e.Retryable, tc.code, tc.retryable)
			}
			if e.Details["upstream_status"] == "" {
				t.Fatalf("upstream_status detail is missing: %v", e.Details)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("error leaked the upstream body: %v", err)
			}
		})
	}
}

func TestCreateProductToleratesEmptyResponseBody(t *testing.T) {
	service, _ := newWriteService(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	product, err := service.Create(context.Background(), validCreateProductInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if product.SKU != "SKU-1" {
		t.Fatalf("product = %+v, want the input SKU", product)
	}
}

func TestDeleteProduct(t *testing.T) {
	var method, path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	if err := svc.DeleteProduct(context.Background(), "SKU-1"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || path != "/rest/default/V1/products/SKU-1" {
		t.Fatalf("request = %s %s", method, path)
	}
	if err := svc.DeleteProduct(context.Background(), "bad sku!"); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("validation err = %v", err)
	}
}

// U-01: buildUpdatePayload must not shift pricing attributes by one slot. A
// weight-only update used to emit special_price=weight, special_from_date=
// special_price, cost=special_to_date and tax_class_id=cost, silently
// repricing the product. Reverting the fix makes this test fail.
func TestBuildUpdatePayloadWeightDoesNotBecomeSpecialPrice(t *testing.T) {
	weight := "2.5"
	payload, err := buildUpdatePayload("SKU-1", UpdateProductInput{Weight: &weight})
	if err != nil {
		t.Fatalf("buildUpdatePayload: %v", err)
	}
	if payload.Weight != "2.5" {
		t.Fatalf("payload.Weight = %q, want 2.5", payload.Weight)
	}
	if len(payload.CustomAttributes) != 0 {
		t.Fatalf("weight-only update emitted pricing custom attributes: %#v", payload.CustomAttributes)
	}
	for _, attr := range payload.CustomAttributes {
		switch attr.AttributeCode {
		case "special_price", "special_from_date", "special_to_date", "cost", "tax_class_id":
			t.Fatalf("weight-only update wrote %s=%q", attr.AttributeCode, attr.Value)
		}
	}
}

// U-01: each pricing field must land on its own attribute code.
func TestBuildUpdatePayloadPricingFieldsKeepTheirCodes(t *testing.T) {
	specialPrice := "40.00"
	from := "2026-01-01"
	to := "2026-02-01"
	cost := "20.00"
	taxClass := 2
	payload, err := buildUpdatePayload("SKU-1", UpdateProductInput{
		SpecialPrice: &specialPrice, SpecialFromDate: &from, SpecialToDate: &to,
		Cost: &cost, TaxClassID: &taxClass,
	})
	if err != nil {
		t.Fatalf("buildUpdatePayload: %v", err)
	}
	want := map[string]string{
		"special_price":     "40.00",
		"special_from_date": "2026-01-01",
		"special_to_date":   "2026-02-01",
		"cost":              "20.00",
		"tax_class_id":      "2",
	}
	got := map[string]string{}
	for _, attr := range payload.CustomAttributes {
		got[attr.AttributeCode] = attr.Value
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("custom attributes = %#v, want %#v", got, want)
	}
	if payload.Weight != "" {
		t.Fatalf("payload.Weight = %q, want empty", payload.Weight)
	}
}
