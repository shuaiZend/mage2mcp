// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// validConfigurableInput returns a fully valid configurable create request.
func validConfigurableInput() CreateConfigurableInput {
	return CreateConfigurableInput{
		Parent: CreateProductInput{
			SKU: "PHONE-01", Name: "Phone 01", AttributeSetID: 4, TypeID: "configurable",
			Price: "699", WebsiteIDs: []int{1},
		},
		Variants: []VariantAttribute{{
			AttributeID: 93, Code: "color",
			Values: []VariantValue{{ValueIndex: 12, Label: "Black"}, {ValueIndex: 13, Label: "White"}},
		}},
		Children: []ChildProduct{
			{SKU: "PHONE-01-BLK", Attributes: map[string]string{"color": "12"}, Price: "699"},
			{SKU: "PHONE-01-WHT", Attributes: map[string]string{"color": "13"}},
		},
	}
}

// linkChildSKU decodes the childSku of a POST .../child body.
func linkChildSKU(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		ChildSKU string `json:"childSku"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("child link body is not JSON: %v (%s)", err, body)
	}
	return payload.ChildSKU
}

// configurableResponder answers the Magento routes used by the configurable
// lifecycle; fail reports a status for a stage kind ("product" or "link").
func configurableResponder(t *testing.T, fail func(kind, sku string) int) func(http.ResponseWriter, *http.Request) {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products" {
			body, _ := io.ReadAll(r.Body)
			sku := productSKUFromBody(t, body)
			if status := fail("product", sku); status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"message":"SECRET-UPSTREAM"}`)
				return
			}
			if sku == "PHONE-01" {
				_, _ = io.WriteString(w, `{"id":100,"sku":"PHONE-01","name":"Phone 01","type_id":"configurable",`+
					`"status":1,"visibility":4,"price":699,"attribute_set_id":4}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":1,"sku":%q,"name":"Child","type_id":"simple",`+
				`"status":1,"visibility":1,"price":699,"attribute_set_id":4}`, sku)
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/child") {
			body, _ := io.ReadAll(r.Body)
			child := linkChildSKU(t, body)
			if status := fail("link", child); status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"message":"SECRET-UPSTREAM"}`)
				return
			}
			_, _ = io.WriteString(w, `true`)
			return
		}
		http.NotFound(w, r)
	}
}

func TestCreateConfigurableRequestSequence(t *testing.T) {
	service, rec := newWriteService(t, configurableResponder(t, func(string, string) int { return 0 }))
	result, err := service.CreateConfigurable(context.Background(), validConfigurableInput())
	if err != nil {
		t.Fatalf("CreateConfigurable: %v", err)
	}
	if result.Parent.SKU != "PHONE-01" || result.Parent.TypeID != "configurable" {
		t.Fatalf("unexpected parent: %+v", result.Parent)
	}
	if len(result.Children) != 2 || result.Children[0].SKU != "PHONE-01-BLK" || result.Children[1].SKU != "PHONE-01-WHT" {
		t.Fatalf("unexpected children: %+v", result.Children)
	}

	requests := rec.all()
	if len(requests) != 5 {
		t.Fatalf("requests = %d, want 5 (2 children, 1 parent, 2 links)", len(requests))
	}
	for _, request := range requests {
		assertOAuthHeader(t, request.Auth)
	}
	// Children are created before the parent, and links come after the parent
	// create that carries the options.
	childIndex, parentIndex := 0, 2
	if got := productSKUFromBody(t, requests[childIndex].Body); got != "PHONE-01-BLK" {
		t.Fatalf("request %d sku = %q", childIndex, got)
	}
	if got := productSKUFromBody(t, requests[childIndex+1].Body); got != "PHONE-01-WHT" {
		t.Fatalf("request %d sku = %q", childIndex+1, got)
	}
	if got := productSKUFromBody(t, requests[parentIndex].Body); got != "PHONE-01" {
		t.Fatalf("request %d sku = %q, want the parent", parentIndex, got)
	}
	assertJSONBody(t, requests[0].Body, `{"product":{
		"sku":"PHONE-01-BLK","name":"Phone 01 PHONE-01-BLK","attribute_set_id":4,"type_id":"simple",
		"price":"699","status":1,"visibility":1,
		"custom_attributes":[{"attribute_code":"color","value":"12"}],
		"extension_attributes":{"website_ids":[1]}}}`)
	assertJSONBody(t, requests[1].Body, `{"product":{
		"sku":"PHONE-01-WHT","name":"Phone 01 PHONE-01-WHT","attribute_set_id":4,"type_id":"simple",
		"price":"699","status":1,"visibility":1,
		"custom_attributes":[{"attribute_code":"color","value":"13"}],
		"extension_attributes":{"website_ids":[1]}}}`)
	assertJSONBody(t, requests[2].Body, `{"product":{
		"sku":"PHONE-01","name":"Phone 01","attribute_set_id":4,"type_id":"configurable",
		"price":"699","status":1,"visibility":4,
		"extension_attributes":{"website_ids":[1],"configurable_product_options":[
			{"attribute_id":"93","label":"color","position":0,
			 "values":[{"value_index":12},{"value_index":13}]}]}}}`)
	for i, want := range []string{"PHONE-01-BLK", "PHONE-01-WHT"} {
		request := requests[3+i]
		if request.Method != http.MethodPost || request.Path != "/rest/default/V1/configurable-products/PHONE-01/child" {
			t.Fatalf("link request = %s %s", request.Method, request.Path)
		}
		if got := linkChildSKU(t, request.Body); got != want {
			t.Fatalf("link %d childSku = %q, want %q", i, got, want)
		}
	}
}

func TestCreateConfigurableFailureStages(t *testing.T) {
	tests := []struct {
		name          string
		fail          func(kind, sku string) int
		wantCode      errs.Code
		wantStage     string
		wantCompleted string
		wantFailed    string
	}{
		{
			name: "children stage", wantCode: errs.CodeUpstreamUnavailable, wantStage: "children",
			wantCompleted: "PHONE-01-BLK", wantFailed: "PHONE-01-WHT",
			fail: func(kind, sku string) int {
				if kind == "product" && sku == "PHONE-01-WHT" {
					return http.StatusInternalServerError
				}
				return 0
			},
		},
		{
			name: "parent stage", wantCode: errs.CodeValidationFailed, wantStage: "parent",
			wantCompleted: "PHONE-01-BLK,PHONE-01-WHT", wantFailed: "PHONE-01",
			fail: func(kind, sku string) int {
				if kind == "product" && sku == "PHONE-01" {
					return http.StatusBadRequest
				}
				return 0
			},
		},
		{
			name: "links stage", wantCode: errs.CodeConflict, wantStage: "links",
			wantCompleted: "PHONE-01-BLK,PHONE-01-WHT,PHONE-01", wantFailed: "PHONE-01-WHT",
			fail: func(kind, sku string) int {
				if kind == "link" && sku == "PHONE-01-WHT" {
					return http.StatusConflict
				}
				return 0
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service, _ := newWriteService(t, configurableResponder(t, tc.fail))
			_, err := service.CreateConfigurable(context.Background(), validConfigurableInput())
			var e *errs.Error
			if !errors.As(err, &e) {
				t.Fatalf("error is not *errs.Error: %v", err)
			}
			if e.Code != tc.wantCode {
				t.Fatalf("code = %s, want %s", e.Code, tc.wantCode)
			}
			if e.Details["stage"] != tc.wantStage {
				t.Fatalf("stage = %q, want %q", e.Details["stage"], tc.wantStage)
			}
			if e.Details["completed_skus"] != tc.wantCompleted {
				t.Fatalf("completed_skus = %q, want %q", e.Details["completed_skus"], tc.wantCompleted)
			}
			if e.Details["failed_sku"] != tc.wantFailed {
				t.Fatalf("failed_sku = %q, want %q", e.Details["failed_sku"], tc.wantFailed)
			}
			if !strings.Contains(err.Error(), "not rolled back") || !strings.Contains(err.Error(), "compensation") {
				t.Fatalf("error must state the non-atomic contract: %v", err)
			}
			if e.Retryable {
				t.Fatal("a non-atomic failure must not be marked retryable")
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("error leaked the upstream body: %v", err)
			}
		})
	}
}

func TestCreateConfigurableValidationBeforeWrites(t *testing.T) {
	service, rec := newWriteService(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid input reached the upstream: %s %s", r.Method, r.URL.Path)
	})
	qty := 2
	tests := []struct {
		name   string
		mutate func(*CreateConfigurableInput)
	}{
		{name: "bad parent sku", mutate: func(in *CreateConfigurableInput) { in.Parent.SKU = "bad sku" }},
		{name: "empty parent name", mutate: func(in *CreateConfigurableInput) { in.Parent.Name = "" }},
		{name: "bad parent price", mutate: func(in *CreateConfigurableInput) { in.Parent.Price = "699.00001" }},
		{name: "zero attribute set", mutate: func(in *CreateConfigurableInput) { in.Parent.AttributeSetID = 0 }},
		{name: "parent type simple", mutate: func(in *CreateConfigurableInput) { in.Parent.TypeID = "simple" }},
		{name: "parent quantity", mutate: func(in *CreateConfigurableInput) { in.Parent.Quantity = &qty }},
		{name: "no variants", mutate: func(in *CreateConfigurableInput) { in.Variants = nil }},
		{name: "bad variant code", mutate: func(in *CreateConfigurableInput) { in.Variants[0].Code = "Color" }},
		{name: "zero attribute id", mutate: func(in *CreateConfigurableInput) { in.Variants[0].AttributeID = 0 }},
		{name: "variant without values", mutate: func(in *CreateConfigurableInput) { in.Variants[0].Values = nil }},
		{name: "duplicate variant code", mutate: func(in *CreateConfigurableInput) {
			in.Variants = append(in.Variants, in.Variants[0])
		}},
		{name: "too many variants", mutate: func(in *CreateConfigurableInput) {
			for i := 0; i < 5; i++ {
				code := fmt.Sprintf("attr_%d", i)
				in.Variants = append(in.Variants, VariantAttribute{
					AttributeID: 100 + i, Code: code, Values: []VariantValue{{ValueIndex: 1}},
				})
				in.Children[0].Attributes[code] = "1"
			}
		}},
		{name: "no children", mutate: func(in *CreateConfigurableInput) { in.Children = nil }},
		{name: "too many children", mutate: func(in *CreateConfigurableInput) {
			in.Children = make([]ChildProduct, 0, 51)
			for i := 0; i < 51; i++ {
				in.Children = append(in.Children, ChildProduct{
					SKU: fmt.Sprintf("PHONE-01-%02d", i), Attributes: map[string]string{"color": "12"},
				})
			}
		}},
		{name: "bad child sku", mutate: func(in *CreateConfigurableInput) { in.Children[0].SKU = "bad sku" }},
		{name: "bad child price", mutate: func(in *CreateConfigurableInput) { in.Children[0].Price = "free" }},
		{name: "child missing variant value", mutate: func(in *CreateConfigurableInput) {
			in.Children[0].Attributes = map[string]string{}
		}},
		{name: "duplicate child sku", mutate: func(in *CreateConfigurableInput) { in.Children[1].SKU = in.Children[0].SKU }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := validConfigurableInput()
			tc.mutate(&in)
			_, err := service.CreateConfigurable(context.Background(), in)
			if code := errs.CodeOf(err); code != errs.CodeInvalidArgument {
				t.Fatalf("code = %s, want invalid_argument (%v)", code, err)
			}
		})
	}
	if rec.count() != 0 {
		t.Fatalf("invalid input reached the upstream %d times", rec.count())
	}
}

func TestSetOptions(t *testing.T) {
	service, rec := newWriteService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `true`)
	})
	err := service.SetOptions(context.Background(), "PHONE-01", []VariantAttribute{
		{AttributeID: 93, Code: "color", Values: []VariantValue{{ValueIndex: 12}, {ValueIndex: 13}}},
		{AttributeID: 120, Code: "storage", Values: []VariantValue{{ValueIndex: 31}}},
	})
	if err != nil {
		t.Fatalf("SetOptions: %v", err)
	}
	requests := rec.all()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	for _, request := range requests {
		if request.Method != http.MethodPost || request.Path != "/rest/default/V1/configurable-products/PHONE-01/options" {
			t.Fatalf("request = %s %s", request.Method, request.Path)
		}
		assertOAuthHeader(t, request.Auth)
	}
	assertJSONBody(t, requests[0].Body, `{"option":{"attribute_id":"93","label":"color","position":0,`+
		`"values":[{"value_index":12},{"value_index":13}]}}`)
	assertJSONBody(t, requests[1].Body, `{"option":{"attribute_id":"120","label":"storage","position":1,`+
		`"values":[{"value_index":31}]}}`)
}

func TestSetOptionsValidation(t *testing.T) {
	service, rec := newWriteService(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid input reached the upstream: %s %s", r.Method, r.URL.Path)
	})
	tests := []struct {
		name    string
		parent  string
		options []VariantAttribute
	}{
		{name: "bad parent sku", parent: "bad sku", options: []VariantAttribute{{AttributeID: 93, Code: "color", Values: []VariantValue{{ValueIndex: 12}}}}},
		{name: "no options", parent: "PHONE-01"},
		{name: "bad code", parent: "PHONE-01", options: []VariantAttribute{{AttributeID: 93, Code: "Color", Values: []VariantValue{{ValueIndex: 12}}}}},
		{name: "zero attribute id", parent: "PHONE-01", options: []VariantAttribute{{AttributeID: 0, Code: "color", Values: []VariantValue{{ValueIndex: 12}}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := service.SetOptions(context.Background(), tc.parent, tc.options)
			if code := errs.CodeOf(err); code != errs.CodeInvalidArgument {
				t.Fatalf("code = %s, want invalid_argument (%v)", code, err)
			}
		})
	}
	if rec.count() != 0 {
		t.Fatalf("invalid input reached the upstream %d times", rec.count())
	}
}

func TestAddChild(t *testing.T) {
	service, rec := newWriteService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `true`)
	})
	if err := service.AddChild(context.Background(), "PHONE-01", "PHONE-01-BLK"); err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	requests := rec.all()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	got := requests[0]
	if got.Method != http.MethodPost || got.Path != "/rest/default/V1/configurable-products/PHONE-01/child" {
		t.Fatalf("request = %s %s", got.Method, got.Path)
	}
	assertOAuthHeader(t, got.Auth)
	assertJSONBody(t, got.Body, `{"childSku":"PHONE-01-BLK"}`)
}

func TestAddChildValidation(t *testing.T) {
	service, rec := newWriteService(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid input reached the upstream: %s %s", r.Method, r.URL.Path)
	})
	for _, tc := range []struct{ name, parent, child string }{
		{name: "bad parent sku", parent: "bad sku", child: "PHONE-01-BLK"},
		{name: "bad child sku", parent: "PHONE-01", child: "bad sku"},
		{name: "self link", parent: "PHONE-01", child: "PHONE-01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := service.AddChild(context.Background(), tc.parent, tc.child)
			if code := errs.CodeOf(err); code != errs.CodeInvalidArgument {
				t.Fatalf("code = %s, want invalid_argument (%v)", code, err)
			}
		})
	}
	if rec.count() != 0 {
		t.Fatalf("invalid input reached the upstream %d times", rec.count())
	}
}

func TestChildrenAndGetConfigurable(t *testing.T) {
	service, rec := newWriteService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/default/V1/products/PHONE-01":
			_, _ = io.WriteString(w, `{"id":100,"sku":"PHONE-01","name":"Phone 01",`+
				`"type_id":"configurable","status":1,"visibility":4,"price":699,"attribute_set_id":4}`)
		case "/rest/default/V1/configurable-products/PHONE-01/children":
			_, _ = io.WriteString(w, `[{"id":1,"sku":"PHONE-01-BLK","type_id":"simple"},`+
				`{"id":2,"sku":"PHONE-01-WHT","type_id":"simple"}]`)
		default:
			http.NotFound(w, r)
		}
	})
	result, err := service.GetConfigurable(context.Background(), "PHONE-01")
	if err != nil {
		t.Fatalf("GetConfigurable: %v", err)
	}
	if result.Parent.SKU != "PHONE-01" || len(result.Children) != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	requests := rec.all()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if requests[0].Path != "/rest/default/V1/products/PHONE-01" ||
		requests[1].Path != "/rest/default/V1/configurable-products/PHONE-01/children" {
		t.Fatalf("unexpected request order: %+v", requests)
	}
	assertOAuthHeader(t, requests[0].Auth)
	assertOAuthHeader(t, requests[1].Auth)
}

func TestGetConfigurableRejectsNonConfigurableProduct(t *testing.T) {
	service, rec := newWriteService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":1,"sku":"SKU-1","type_id":"simple"}`)
	})
	_, err := service.GetConfigurable(context.Background(), "SKU-1")
	if code := errs.CodeOf(err); code != errs.CodeNotFound {
		t.Fatalf("code = %s, want not_found (%v)", code, err)
	}
	if rec.count() != 1 {
		t.Fatalf("requests = %d, want 1 (children must not be read)", rec.count())
	}
}

func TestChildrenReturnsEmptySlice(t *testing.T) {
	service, _ := newWriteService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[]`)
	})
	children, err := service.Children(context.Background(), "PHONE-01")
	if err != nil {
		t.Fatalf("Children: %v", err)
	}
	if children == nil || len(children) != 0 {
		t.Fatalf("children = %v, want a non-nil empty slice", children)
	}
}

func TestCreateConfigurableAssignsChildQuantity(t *testing.T) {
	service, rec := newWriteService(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products":
			body, _ := io.ReadAll(r.Body)
			sku := productSKUFromBody(t, body)
			_, _ = fmt.Fprintf(w, `{"id":1,"sku":%q,"type_id":"simple"}`, sku)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/inventory/source-items":
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/child"):
			_, _ = io.WriteString(w, `true`)
		default:
			http.NotFound(w, r)
		}
	})
	in := validConfigurableInput()
	qty := 4
	in.Children[0].Quantity = &qty
	if _, err := service.CreateConfigurable(context.Background(), in); err != nil {
		t.Fatalf("CreateConfigurable: %v", err)
	}
	requests := rec.all()
	if len(requests) != 6 {
		t.Fatalf("requests = %d, want 6 (2 children, 1 inventory, 1 parent, 2 links)", len(requests))
	}
	if requests[1].Path != "/rest/default/V1/inventory/source-items" {
		t.Fatalf("request 1 path = %s, want the child inventory assign", requests[1].Path)
	}
	assertJSONBody(t, requests[1].Body, `{"sourceItems":[{"sku":"PHONE-01-BLK",`+
		`"source_code":"default","quantity":4,"status":1}]}`)
	if got := productSKUFromBody(t, requests[2].Body); got != "PHONE-01-WHT" {
		t.Fatalf("request 2 sku = %q, want the second child", got)
	}
	if got := productSKUFromBody(t, requests[3].Body); got != "PHONE-01" {
		t.Fatalf("request 3 sku = %q, want the parent", got)
	}
}

func TestConfigurableVariantValueValidation(t *testing.T) {
	parent := CreateProductInput{
		SKU: "PARENT-1", Name: "Parent", AttributeSetID: 4, Price: "9.99", WebsiteIDs: []int{1},
	}
	variants := []VariantAttribute{{
		AttributeID: 93, Code: "color",
		Values: []VariantValue{{ValueIndex: 12, Label: "Red"}, {ValueIndex: 13, Label: "Blue"}},
	}}
	child := func(sku, value string) ChildProduct {
		return ChildProduct{SKU: sku, Attributes: map[string]string{"color": value}}
	}
	valid := CreateConfigurableInput{Parent: parent, Variants: variants, Children: []ChildProduct{child("C-1", "12"), child("C-2", "13")}}
	if err := validateConfigurableInput(valid); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	undeclared := CreateConfigurableInput{Parent: parent, Variants: variants, Children: []ChildProduct{child("C-1", "99")}}
	if err := validateConfigurableInput(undeclared); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("undeclared value err = %v", err)
	}
	duplicate := CreateConfigurableInput{Parent: parent, Variants: variants, Children: []ChildProduct{child("C-1", "12"), child("C-2", "12")}}
	if err := validateConfigurableInput(duplicate); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("duplicate combination err = %v", err)
	}
}

func TestUpdateConfigurable(t *testing.T) {
	status := 2
	var method, path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":1,"sku":"CFG-1","name":"Parent","type_id":"configurable","price":19.99,"attribute_set_id":4}`))
		case http.MethodPut:
			_, _ = w.Write([]byte(`{"id":1,"sku":"CFG-1","name":"Parent","type_id":"configurable","price":19.99,"attribute_set_id":4,"status":2}`))
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	product, err := svc.UpdateConfigurable(context.Background(), "CFG-1", UpdateProductInput{Status: &status})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut || path != "/rest/default/V1/products/CFG-1" {
		t.Fatalf("request = %s %s", method, path)
	}
	if product.Status != 2 || product.TypeID != "configurable" {
		t.Fatalf("product = %+v", product)
	}
}

func TestUpdateConfigurableRejectsSimple(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"sku":"SIMPLE-1","name":"Simple","type_id":"simple","price":9.99,"attribute_set_id":4}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	if _, err := svc.UpdateConfigurable(context.Background(), "SIMPLE-1", UpdateProductInput{}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateVariations(t *testing.T) {
	created := 0
	linked := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/products/CFG":
			_, _ = w.Write([]byte(`{"id":1,"sku":"CFG","name":"Parent","type_id":"configurable","price":19.99,"attribute_set_id":4}`))
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/products/CFG-EXISTS":
			_, _ = w.Write([]byte(`{"id":2,"sku":"CFG-EXISTS","name":"Existing","type_id":"simple","price":1,"attribute_set_id":4}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/default/V1/products/"):
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products":
			created++
			_, _ = w.Write([]byte(`{"id":3,"sku":"CFG-1","name":"Parent CFG-1","type_id":"simple","price":19.99,"attribute_set_id":4}`))
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/configurable-products/CFG/child":
			linked++
			_, _ = w.Write([]byte(`true`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	result, err := svc.CreateVariations(context.Background(), "CFG", []VariationInput{
		{SKU: "CFG-1", Attributes: map[string]string{"color": "12"}},
		{SKU: "CFG-EXISTS", Attributes: map[string]string{"color": "13"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || result.Created != 1 || result.Skipped != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}
	if created != 1 || linked != 1 {
		t.Fatalf("created=%d linked=%d", created, linked)
	}
}

func TestCreateVariationsValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	valid := []VariationInput{{SKU: "CFG-1", Attributes: map[string]string{"color": "12"}}}
	cases := []struct {
		parent   string
		variants []VariationInput
	}{
		{"bad sku!", valid},
		{"CFG", nil},
		{"CFG", []VariationInput{{SKU: "CFG-1"}}},
		{"CFG", []VariationInput{{SKU: "CFG-1", Attributes: map[string]string{"bad code!": "12"}}}},
		{"CFG", []VariationInput{{SKU: "CFG-1", Attributes: map[string]string{"color": " "}}}},
		{"CFG", []VariationInput{valid[0], valid[0]}},
	}
	for i, tc := range cases {
		if _, err := svc.CreateVariations(ctx, tc.parent, tc.variants); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
}
