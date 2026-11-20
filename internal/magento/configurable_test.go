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
	"sort"
	"strconv"
	"strings"
	"sync"
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
// Children are created without a quantity, so the source-item read-back returns
// no rows and the parent is reported out of stock.
func configurableResponder(t *testing.T, fail func(kind, sku string) int) func(http.ResponseWriter, *http.Request) {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/inventory/source-items" {
			_, _ = io.WriteString(w, `{"items":[],"total_count":0}`)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/products/attribute-sets/4/attributes" {
			_, _ = io.WriteString(w, `[{"attribute_id":93,"attribute_code":"color","frontend_label":"Color",`+
				`"frontend_input":"select","is_required":false,"is_user_defined":true}]`)
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/children") {
			// The links are not tracked by this responder; report both children
			// so the post-link verification sees a fully linked parent.
			_, _ = io.WriteString(w, `[{"sku":"PHONE-01-BLK","type_id":"simple"},`+
				`{"sku":"PHONE-01-WHT","type_id":"simple"}]`)
			return
		}
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
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/stockItems/") {
			_, _ = io.WriteString(w, `true`)
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

// stageState returns one stage's state from an execution record.
func stageState(result *ConfigurableProduct, name string) ConfigurableStageState {
	for _, stage := range result.Stages {
		if stage.Name == name {
			return stage.State
		}
	}
	return ""
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
	if result.Outcome != ConfigurableCompleted {
		t.Fatalf("outcome = %q, want completed", result.Outcome)
	}
	if result.OperationID == "" || !strings.HasPrefix(result.OperationID, "cfg_") {
		t.Fatalf("operation id = %q, want a cfg_ id", result.OperationID)
	}
	if result.FailedStage != "" || len(result.OrphanSKUs) != 0 {
		t.Fatalf("completed run must have no failed stage or orphans: %+v", result)
	}
	for _, name := range []string{stagePrecheck, stageChildren, stageParent, stageLinks, stageStock} {
		if got := stageState(result, name); got != ConfigurableStageCompleted {
			t.Fatalf("stage %q = %q, want completed", name, got)
		}
		if !contains(result.CompletedStages, name) {
			t.Fatalf("completed_stages = %v, want %q", result.CompletedStages, name)
		}
	}

	// The precheck reads every SKU before the first write, the links are
	// verified by reading the children back, and the stock status is written
	// last.
	requests := rec.all()
	want := []string{
		"GET /rest/default/V1/products/attribute-sets/4/attributes",
		"GET /rest/default/V1/products/PHONE-01-BLK",
		"GET /rest/default/V1/products/PHONE-01-WHT",
		"GET /rest/default/V1/products/PHONE-01",
		"POST /rest/default/V1/products",
		"POST /rest/default/V1/products",
		"POST /rest/default/V1/products",
		"POST /rest/default/V1/configurable-products/PHONE-01/child",
		"POST /rest/default/V1/configurable-products/PHONE-01/child",
		"GET /rest/default/V1/configurable-products/PHONE-01/children",
		"GET /rest/default/V1/inventory/source-items",
		"PUT /rest/default/V1/products/PHONE-01/stockItems/100",
	}
	if len(requests) != len(want) {
		paths := make([]string, 0, len(requests))
		for _, request := range requests {
			paths = append(paths, request.Method+" "+request.Path)
		}
		t.Fatalf("requests = %v, want %v", paths, want)
	}
	for i, request := range requests {
		if got := request.Method + " " + request.Path; got != want[i] {
			t.Fatalf("request %d = %s, want %s", i, got, want[i])
		}
	}
	for _, request := range requests {
		assertOAuthHeader(t, request.Auth)
	}
	if got := productSKUFromBody(t, requests[4].Body); got != "PHONE-01-BLK" {
		t.Fatalf("request 4 sku = %q", got)
	}
	if got := productSKUFromBody(t, requests[5].Body); got != "PHONE-01-WHT" {
		t.Fatalf("request 5 sku = %q", got)
	}
	if got := productSKUFromBody(t, requests[6].Body); got != "PHONE-01" {
		t.Fatalf("request 6 sku = %q, want the parent", got)
	}
	assertJSONBody(t, requests[6].Body, `{"product":{
		"sku":"PHONE-01","name":"Phone 01","attribute_set_id":4,"type_id":"configurable",
		"price":"699","status":1,"visibility":4,
		"extension_attributes":{"website_ids":[1],"configurable_product_options":[
			{"attribute_id":"93","label":"color","position":0,
			 "values":[{"value_index":12},{"value_index":13}]}]}}}`)
	for i, wantChild := range []string{"PHONE-01-BLK", "PHONE-01-WHT"} {
		if got := linkChildSKU(t, requests[7+i].Body); got != wantChild {
			t.Fatalf("link %d childSku = %q, want %q", i, got, wantChild)
		}
	}
	// No child holds stock, so the parent is written out of stock.
	assertJSONBody(t, requests[11].Body, `{"stockItem":{"is_in_stock":false}}`)
	if result.ParentInStock {
		t.Fatalf("parent_in_stock = true, want the observed value (no child stock)")
	}
	if !strings.HasPrefix(result.ParentStockSource, "source_items:") {
		t.Fatalf("parent_stock_source = %q, want the source-item derivation", result.ParentStockSource)
	}
}

// contains reports whether names holds name.
func contains(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

func TestCreateConfigurableFailureStages(t *testing.T) {
	tests := []struct {
		name          string
		fail          func(kind, sku string) int
		wantCode      errs.Code
		wantStage     string
		wantCompleted string
		wantFailed    string
		wantOrphans   string
	}{
		{
			name: "children stage", wantCode: errs.CodeUpstreamUnavailable, wantStage: "children",
			wantCompleted: "PHONE-01-BLK", wantFailed: "PHONE-01-WHT", wantOrphans: "PHONE-01-BLK",
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
			wantOrphans: "PHONE-01-BLK,PHONE-01-WHT",
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
			wantOrphans: "PHONE-01-WHT",
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
			result, err := service.CreateConfigurable(context.Background(), validConfigurableInput())
			if result == nil {
				t.Fatal("a partial run must return the execution record")
			}
			if result.Outcome != ConfigurablePartial {
				t.Fatalf("outcome = %q, want partial", result.Outcome)
			}
			if result.FailedStage != tc.wantStage {
				t.Fatalf("failed_stage = %q, want %q", result.FailedStage, tc.wantStage)
			}
			if result.OperationID == "" {
				t.Fatal("operation id is empty")
			}
			if got := strings.Join(result.OrphanSKUs, ","); got != tc.wantOrphans {
				t.Fatalf("orphan_skus = %q, want %q", got, tc.wantOrphans)
			}
			if !contains(result.CompletedStages, stagePrecheck) {
				t.Fatalf("completed_stages = %v, want the precheck", result.CompletedStages)
			}
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
			if e.Details["operation_id"] != result.OperationID ||
				e.Details["outcome"] != string(ConfigurablePartial) ||
				e.Details["failed_stage"] != tc.wantStage ||
				e.Details["orphan_skus"] != tc.wantOrphans {
				t.Fatalf("error details = %v, want the execution record", e.Details)
			}
			if !contains(strings.Split(e.Details["completed_stages"], ","), stagePrecheck) {
				t.Fatalf("completed_stages detail = %q, want the precheck", e.Details["completed_stages"])
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
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/inventory/source-items":
			// The read-back observes the quantity just assigned to the first
			// child; the second child has no source item.
			_, _ = io.WriteString(w, `{"items":[{"sku":"PHONE-01-BLK","source_code":"default",`+
				`"quantity":4,"status":1}],"total_count":1}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/child"):
			_, _ = io.WriteString(w, `true`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/children"):
			_, _ = io.WriteString(w, `[{"sku":"PHONE-01-BLK","type_id":"simple"},{"sku":"PHONE-01-WHT","type_id":"simple"}]`)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/stockItems/"):
			_, _ = io.WriteString(w, `true`)
		default:
			http.NotFound(w, r)
		}
	})
	in := validConfigurableInput()
	qty := 4
	in.Children[0].Quantity = &qty
	result, err := service.CreateConfigurable(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateConfigurable: %v", err)
	}
	requests := rec.all()
	if len(requests) != 13 {
		paths := make([]string, 0, len(requests))
		for _, request := range requests {
			paths = append(paths, request.Method+" "+request.Path)
		}
		t.Fatalf("requests = %v, want 13 (precheck reads, children, inventory, parent, links, verification, source-item read, parent stock)", paths)
	}
	if requests[11].Method != http.MethodGet || requests[11].Path != "/rest/default/V1/inventory/source-items" {
		t.Fatalf("request 11 = %s %s, want the child source-item read", requests[11].Method, requests[11].Path)
	}
	if requests[12].Method != http.MethodPut || !strings.Contains(requests[12].Path, "/stockItems/") {
		t.Fatalf("last request = %s %s, want the parent stock status", requests[12].Method, requests[12].Path)
	}
	// One child holds 4 units, so the parent is in stock and the legacy flag is
	// written from the observed value.
	assertJSONBody(t, requests[12].Body, `{"stockItem":{"is_in_stock":true}}`)
	if !result.ParentInStock {
		t.Fatalf("parent_in_stock = false, want true from the observed child quantity")
	}
	if !strings.Contains(result.ParentStockSource, "1 of 2 children in stock") {
		t.Fatalf("parent_stock_source = %q, want the observed child count", result.ParentStockSource)
	}
	if requests[5].Path != "/rest/default/V1/inventory/source-items" {
		t.Fatalf("request 5 path = %s, want the child inventory assign", requests[5].Path)
	}
	assertJSONBody(t, requests[5].Body, `{"sourceItems":[{"sku":"PHONE-01-BLK",`+
		`"source_code":"default","quantity":4,"status":1}]}`)
	if got := productSKUFromBody(t, requests[6].Body); got != "PHONE-01-WHT" {
		t.Fatalf("request 6 sku = %q, want the second child", got)
	}
	if got := productSKUFromBody(t, requests[7].Body); got != "PHONE-01" {
		t.Fatalf("request 7 sku = %q, want the parent", got)
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
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/configurable-products/CFG/children":
			// CFG-EXISTS is already linked, so it is skipped rather than relinked.
			_, _ = w.Write([]byte(`[{"sku":"CFG-EXISTS","type_id":"simple"}]`))
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

func TestSetConfigurableOptions(t *testing.T) {
	var bodies []configurableOptionBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"id":1,"sku":"CFG","name":"Parent","type_id":"configurable","price":19.99,"attribute_set_id":4}`))
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/configurable-products/CFG/options":
			var body configurableOptionBody
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			bodies = append(bodies, body)
			_, _ = w.Write([]byte(`{"id":1}`))
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	err := svc.SetConfigurableOptions(context.Background(), "CFG", []ConfigurableOptionInput{
		{AttributeID: 93, Label: "Color", Position: 0, Values: []int{12, 13}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || bodies[0].Option.AttributeID != "93" || len(bodies[0].Option.Values) != 2 {
		t.Fatalf("bodies = %+v", bodies)
	}
	if bodies[0].Option.Values[1].ValueIndex != 13 {
		t.Fatalf("values = %+v", bodies[0].Option.Values)
	}
}

func TestSetConfigurableOptionsValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	valid := ConfigurableOptionInput{AttributeID: 93, Label: "Color", Values: []int{12}}
	cases := []struct {
		sku     string
		options []ConfigurableOptionInput
	}{
		{"bad sku!", []ConfigurableOptionInput{valid}},
		{"CFG", nil},
		{"CFG", []ConfigurableOptionInput{{AttributeID: 0, Label: "Color", Values: []int{12}}}},
		{"CFG", []ConfigurableOptionInput{{AttributeID: 93, Label: "", Values: []int{12}}}},
		{"CFG", []ConfigurableOptionInput{{AttributeID: 93, Label: "Color", Values: nil}}},
		{"CFG", []ConfigurableOptionInput{{AttributeID: 93, Label: "Color", Values: []int{0}}}},
		{"CFG", []ConfigurableOptionInput{{AttributeID: 93, Label: "Color", Values: []int{12, 12}}}},
		{"CFG", []ConfigurableOptionInput{valid, valid}},
	}
	for i, tc := range cases {
		if err := svc.SetConfigurableOptions(ctx, tc.sku, tc.options); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
}

func TestCreateConfigurableUsesChildName(t *testing.T) {
	var names []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products":
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				Product struct {
					SKU  string `json:"sku"`
					Name string `json:"name"`
				} `json:"product"`
			}
			_ = json.Unmarshal(raw, &body)
			names = append(names, body.Product.Name)
			_, _ = w.Write([]byte(`{"id":1,"sku":"` + body.Product.SKU + `","type_id":"simple"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/child"):
			_, _ = w.Write([]byte(`true`))
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/inventory/source-items":
			_, _ = w.Write([]byte(`{"items":[],"total_count":0}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/children"):
			_, _ = w.Write([]byte(`[{"sku":"PHONE-01-BLK","type_id":"simple"},{"sku":"PHONE-01-WHT","type_id":"simple"}]`))
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/stockItems/"):
			_, _ = w.Write([]byte(`true`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	in := validConfigurableInput()
	in.Children[0].Name = "Custom Child Name"
	if _, err := svc.CreateConfigurable(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 || names[0] != "Custom Child Name" {
		t.Fatalf("names = %v", names)
	}
}

// configurableInputWithChildren builds a configurable input with one child per SKU
// and a declared variant value for each, so every child is a distinct variant.
func configurableInputWithChildren(skus []string) CreateConfigurableInput {
	in := validConfigurableInput()
	in.Children = make([]ChildProduct, 0, len(skus))
	in.Variants[0].Values = nil
	for i, sku := range skus {
		value := strconv.Itoa(i + 1)
		in.Variants[0].Values = append(in.Variants[0].Values, VariantValue{ValueIndex: i + 1})
		in.Children = append(in.Children, ChildProduct{SKU: sku, Attributes: map[string]string{"color": value}})
	}
	return in
}

// TestCreateConfigurableCompletedSKUsWholeTokens guards MAG-06: the completed
// list is the only compensation record for a partially created configurable, so
// it must never contain a partial SKU. Whole SKUs only, and the omitted count is
// stated explicitly.
func TestCreateConfigurableCompletedSKUsWholeTokens(t *testing.T) {
	longSKUs := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		longSKUs = append(longSKUs, fmt.Sprintf("CFG-%s-%02d", strings.Repeat("L", 52), i))
	}
	shortSKUs := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		shortSKUs = append(shortSKUs, fmt.Sprintf("CFG-%02d", i))
	}
	tests := []struct {
		name        string
		skus        []string
		wantOmitted int
		wantTotal   int
	}{
		// 40 x 59-byte SKUs: the 900-byte detail budget binds before the
		// 20-entry cap, and the old code cut the last SKU mid-token.
		{name: "byte budget binds", skus: longSKUs, wantOmitted: 24, wantTotal: 39},
		// 30 short SKUs: the 20-entry cap binds.
		{name: "entry cap binds", skus: shortSKUs, wantOmitted: 9, wantTotal: 29},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := configurableInputWithChildren(tc.skus)
			failing := in.Children[len(in.Children)-1].SKU
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products" {
					raw, _ := io.ReadAll(r.Body)
					if strings.Contains(string(raw), failing) {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"message":"The value of attribute is incorrect."}`)
						return
					}
					_, _ = io.WriteString(w, `{"id":1,"type_id":"simple"}`)
					return
				}
				http.NotFound(w, r)
			}))
			defer ts.Close()
			svc := testService(t, ts.URL)
			_, err := svc.CreateConfigurable(context.Background(), in)
			var typed *errs.Error
			if !errors.As(err, &typed) {
				t.Fatalf("err = %v, want a stage error", err)
			}
			if typed.Details["stage"] != stageChildren {
				t.Fatalf("stage = %q, want %q", typed.Details["stage"], stageChildren)
			}
			reported := typed.Details["completed_skus"]
			if reported == "" {
				t.Fatal("completed_skus is empty, want the created SKUs")
			}
			if len(reported) > maxCompletedSKUsBytes {
				t.Fatalf("completed_skus is %d bytes, over the %d budget", len(reported), maxCompletedSKUsBytes)
			}
			known := make(map[string]struct{}, len(in.Children))
			for _, child := range in.Children {
				known[child.SKU] = struct{}{}
			}
			tokens := strings.Split(reported, ",")
			for _, token := range tokens {
				if _, ok := known[token]; !ok {
					t.Fatalf("completed_skus contains %q, which is not a complete input SKU", token)
				}
			}
			if len(tokens) > maxCompletedSKUs {
				t.Fatalf("completed_skus has %d entries, over the %d cap", len(tokens), maxCompletedSKUs)
			}
			if got := typed.Details["completed_omitted"]; got != strconv.Itoa(tc.wantOmitted) {
				t.Fatalf("completed_omitted = %q, want %d", got, tc.wantOmitted)
			}
			if got := typed.Details["completed_total"]; got != strconv.Itoa(tc.wantTotal) {
				t.Fatalf("completed_total = %q, want %d", got, tc.wantTotal)
			}
			if len(tokens)+tc.wantOmitted != tc.wantTotal {
				t.Fatalf("%d reported + %d omitted != %d total", len(tokens), tc.wantOmitted, tc.wantTotal)
			}
		})
	}
}

// TestCreateConfigurableFailsAtStockStage guards MAG-08: a failed parent
// stock-status write is no longer swallowed.
func TestCreateConfigurableFailsAtStockStage(t *testing.T) {
	service, _ := newWriteService(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products":
			body, _ := io.ReadAll(r.Body)
			sku := productSKUFromBody(t, body)
			if sku == "PHONE-01" {
				_, _ = io.WriteString(w, `{"id":100,"sku":"PHONE-01","type_id":"configurable"}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":1,"sku":%q,"type_id":"simple"}`, sku)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/inventory/source-items":
			_, _ = io.WriteString(w, `{"items":[{"sku":"PHONE-01-BLK","source_code":"default",`+
				`"quantity":2,"status":1}],"total_count":1}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/children"):
			_, _ = io.WriteString(w, `[{"sku":"PHONE-01-BLK","type_id":"simple"},{"sku":"PHONE-01-WHT","type_id":"simple"}]`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/default/V1/products/"):
			http.NotFound(w, r)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/stockItems/"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"Internal error"}`)
		default:
			_, _ = io.WriteString(w, `true`)
		}
	})
	result, err := service.CreateConfigurable(context.Background(), validConfigurableInput())
	if result == nil {
		t.Fatal("a partial run must return the execution record")
	}
	if result.Outcome != ConfigurablePartial || result.FailedStage != stageStock {
		t.Fatalf("record = %+v, want a partial run that failed at stock", result)
	}
	if len(result.OrphanSKUs) != 0 {
		t.Fatalf("orphan_skus = %v, want none once every child is linked", result.OrphanSKUs)
	}
	var typed *errs.Error
	if !errors.As(err, &typed) {
		t.Fatalf("err = %v, want a stage error", err)
	}
	if typed.Details["stage"] != stageStock {
		t.Fatalf("stage = %q, want %q", typed.Details["stage"], stageStock)
	}
	if typed.Details["failed_sku"] != "PHONE-01" {
		t.Fatalf("failed_sku = %q, want the parent", typed.Details["failed_sku"])
	}
	// Everything before the stock write is in the compensation record.
	if got := typed.Details["completed_skus"]; got != "PHONE-01-BLK,PHONE-01-WHT,PHONE-01" {
		t.Fatalf("completed_skus = %q", got)
	}
	if !strings.Contains(typed.Message, "stage "+stageStock) {
		t.Fatalf("message = %q, want the stage named", typed.Message)
	}
}

// TestCreateConfigurableReportsUnobservedStock guards the honest fallback: when
// the child stock cannot be read back, no stock status is written and the result
// says the value is unobserved instead of claiming a salable parent.
func TestCreateConfigurableReportsUnobservedStock(t *testing.T) {
	var stockWrites int
	service, _ := newWriteService(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products":
			body, _ := io.ReadAll(r.Body)
			sku := productSKUFromBody(t, body)
			if sku == "PHONE-01" {
				_, _ = io.WriteString(w, `{"id":100,"sku":"PHONE-01","type_id":"configurable"}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":1,"sku":%q,"type_id":"simple"}`, sku)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/inventory/source-items":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"The consumer isn't authorized to access the inventory."}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/children"):
			_, _ = io.WriteString(w, `[{"sku":"PHONE-01-BLK","type_id":"simple"},{"sku":"PHONE-01-WHT","type_id":"simple"}]`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/default/V1/products/"):
			http.NotFound(w, r)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/stockItems/"):
			stockWrites++
			_, _ = io.WriteString(w, `true`)
		default:
			_, _ = io.WriteString(w, `true`)
		}
	})
	result, err := service.CreateConfigurable(context.Background(), validConfigurableInput())
	if err != nil {
		t.Fatalf("CreateConfigurable: %v", err)
	}
	if result.ParentInStock {
		t.Fatal("parent_in_stock = true, want false when the child stock is unobserved")
	}
	if !strings.HasPrefix(result.ParentStockSource, "unobserved: ") {
		t.Fatalf("parent_stock_source = %q, want the unobserved marker", result.ParentStockSource)
	}
	if stockWrites != 0 {
		t.Fatalf("stock writes = %d, want 0 (no status may be written without an observation)", stockWrites)
	}
}

// TestCreateConfigurableUnobservedStockOnPageCap guards the truncated-read
// case: when the source-item read hits its page cap the observation is
// incomplete, so no stock status is written and the result says so.
func TestCreateConfigurableUnobservedStockOnPageCap(t *testing.T) {
	rows := make([]string, 0, maxSourceItemsRead)
	for i := 0; i < maxSourceItemsRead; i++ {
		rows = append(rows, fmt.Sprintf(`{"sku":"PHONE-01-BLK","source_code":"src_%03d","quantity":1,"status":1}`, i))
	}
	var stockWrites int
	service, _ := newWriteService(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products":
			body, _ := io.ReadAll(r.Body)
			sku := productSKUFromBody(t, body)
			if sku == "PHONE-01" {
				_, _ = io.WriteString(w, `{"id":100,"sku":"PHONE-01","type_id":"configurable"}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":1,"sku":%q,"type_id":"simple"}`, sku)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/inventory/source-items":
			_, _ = io.WriteString(w, `{"items":[`+strings.Join(rows, ",")+`],"total_count":120}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/children"):
			_, _ = io.WriteString(w, `[{"sku":"PHONE-01-BLK","type_id":"simple"},{"sku":"PHONE-01-WHT","type_id":"simple"}]`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/default/V1/products/"):
			http.NotFound(w, r)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/stockItems/"):
			stockWrites++
			_, _ = io.WriteString(w, `true`)
		default:
			_, _ = io.WriteString(w, `true`)
		}
	})
	result, err := service.CreateConfigurable(context.Background(), validConfigurableInput())
	if err != nil {
		t.Fatalf("CreateConfigurable: %v", err)
	}
	if result.ParentInStock {
		t.Fatal("parent_in_stock = true, want false when the observation is incomplete")
	}
	if !strings.HasPrefix(result.ParentStockSource, "unobserved: ") {
		t.Fatalf("parent_stock_source = %q, want the unobserved marker", result.ParentStockSource)
	}
	if stockWrites != 0 {
		t.Fatalf("stock writes = %d, want 0 (no status may be written from a truncated read)", stockWrites)
	}
}

// stagedFake is a small stateful fake of the catalog routes the staged
// configurable workflow uses: products, the attribute set, child links, and the
// source-item read. It records every request so a test can prove what was (and
// was not) written.
type stagedFake struct {
	mu           sync.Mutex
	products     map[string]string // sku -> type_id
	variants     map[string]string // child sku -> color value
	linked       map[string]bool
	linkFail     map[string]int  // child sku -> HTTP status for the link call
	linkDrop     map[string]bool // child sku hidden from the children read
	attributeSet []Attribute
	requests     []string
	productPOSTs int
	deletes      int
}

func newStagedFake() *stagedFake {
	return &stagedFake{
		products:     map[string]string{},
		variants:     map[string]string{},
		linked:       map[string]bool{},
		linkFail:     map[string]int{},
		linkDrop:     map[string]bool{},
		attributeSet: []Attribute{{ID: 93, Code: "color", Label: "Color", Input: "select"}},
	}
}

func (f *stagedFake) addProduct(sku, typeID, variant string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.products[sku] = typeID
	if variant != "" {
		f.variants[sku] = variant
	}
}

// productJSON renders a product projection; the mutex must be held.
func (f *stagedFake) productJSON(sku string) string {
	custom := ""
	if value, ok := f.variants[sku]; ok {
		custom = fmt.Sprintf(`,"custom_attributes":[{"attribute_code":"color","value":%q}]`, value)
	}
	return fmt.Sprintf(`{"id":1,"sku":%q,"type_id":%q,"name":%q,"price":699,`+
		`"attribute_set_id":4,"status":1,"visibility":4%s}`,
		sku, f.products[sku], "Name "+sku, custom)
}

// requestPaths returns the recorded requests as "METHOD /path".
func (f *stagedFake) requestPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *stagedFake) counts() (productPOSTs, deletes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.productPOSTs, f.deletes
}

func (f *stagedFake) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodDelete {
			f.deletes++
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && path == "/rest/default/V1/products/attribute-sets/4/attributes":
			f.mu.Lock()
			attributes := append([]Attribute(nil), f.attributeSet...)
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(attributes)
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/rest/default/V1/products/"):
			sku := strings.TrimPrefix(path, "/rest/default/V1/products/")
			f.mu.Lock()
			_, ok := f.products[sku]
			body := ""
			if ok {
				body = f.productJSON(sku)
			}
			f.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, body)
		case r.Method == http.MethodPost && path == "/rest/default/V1/products":
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				Product struct {
					SKU              string `json:"sku"`
					TypeID           string `json:"type_id"`
					CustomAttributes []struct {
						Code  string `json:"attribute_code"`
						Value string `json:"value"`
					} `json:"custom_attributes"`
				} `json:"product"`
			}
			_ = json.Unmarshal(raw, &body)
			f.mu.Lock()
			f.products[body.Product.SKU] = body.Product.TypeID
			for _, attribute := range body.Product.CustomAttributes {
				if attribute.Code == "color" {
					f.variants[body.Product.SKU] = attribute.Value
				}
			}
			f.productPOSTs++
			rendered := f.productJSON(body.Product.SKU)
			f.mu.Unlock()
			_, _ = io.WriteString(w, rendered)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/child"):
			raw, _ := io.ReadAll(r.Body)
			child := linkChildSKU(t, raw)
			f.mu.Lock()
			status := f.linkFail[child]
			if status == 0 {
				f.linked[child] = true
			}
			f.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"message":"The child could not be linked."}`)
				return
			}
			_, _ = io.WriteString(w, `true`)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/children"):
			f.mu.Lock()
			items := make([]string, 0, len(f.linked))
			for sku := range f.linked {
				if f.linkDrop[sku] {
					continue
				}
				items = append(items, fmt.Sprintf(`{"id":1,"sku":%q,"type_id":"simple"}`, sku))
			}
			f.mu.Unlock()
			sort.Strings(items)
			_, _ = io.WriteString(w, "["+strings.Join(items, ",")+"]")
		case r.Method == http.MethodGet && path == "/rest/default/V1/inventory/source-items":
			f.mu.Lock()
			rows := make([]string, 0, len(f.products))
			for sku, typeID := range f.products {
				if typeID != "simple" {
					continue
				}
				rows = append(rows, fmt.Sprintf(`{"sku":%q,"source_code":"default","quantity":5,"status":1}`, sku))
			}
			f.mu.Unlock()
			sort.Strings(rows)
			_, _ = io.WriteString(w, `{"items":[`+strings.Join(rows, ",")+`],"total_count":`+strconv.Itoa(len(rows))+`}`)
		case r.Method == http.MethodPut && strings.Contains(path, "/stockItems/"):
			_, _ = io.WriteString(w, `true`)
		default:
			http.NotFound(w, r)
		}
	}
}

// TestCreateConfigurablePrecheckRejectsCollision proves a fresh create never
// starts writing when a SKU already exists upstream.
func TestCreateConfigurablePrecheckRejectsCollision(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*stagedFake)
	}{
		{
			name:  "child sku exists",
			setup: func(f *stagedFake) { f.addProduct("PHONE-01-BLK", "simple", "12") },
		},
		{
			name:  "parent sku exists",
			setup: func(f *stagedFake) { f.addProduct("PHONE-01", "configurable", "") },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newStagedFake()
			tc.setup(fake)
			ts := httptest.NewServer(fake.handler(t))
			defer ts.Close()
			svc := testService(t, ts.URL)

			_, err := svc.CreateConfigurable(context.Background(), validConfigurableInput())
			if got := errs.CodeOf(err); got != errs.CodeConflict {
				t.Fatalf("code = %s, want conflict (%v)", got, err)
			}
			var typed *errs.Error
			if !errors.As(err, &typed) {
				t.Fatalf("err = %v, want a typed error", err)
			}
			if typed.Details["stage"] != stagePrecheck || typed.Details["collision_skus"] == "" {
				t.Fatalf("details = %v, want the precheck collision", typed.Details)
			}
			if posts, _ := fake.counts(); posts != 0 {
				t.Fatalf("product POSTs = %d, want 0 before a failed precheck", posts)
			}
			for _, request := range fake.requestPaths() {
				if strings.HasPrefix(request, "POST") {
					t.Fatalf("a failed precheck issued a write: %s", request)
				}
			}
		})
	}
}

// TestCreateConfigurablePrecheckRejectsUnknownVariantAttribute proves the
// attribute-set check fails before any write.
func TestCreateConfigurablePrecheckRejectsUnknownVariantAttribute(t *testing.T) {
	fake := newStagedFake()
	fake.attributeSet = []Attribute{{ID: 73, Code: "size", Label: "Size", Input: "select"}}
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	svc := testService(t, ts.URL)

	_, err := svc.CreateConfigurable(context.Background(), validConfigurableInput())
	if got := errs.CodeOf(err); got != errs.CodeInvalidArgument {
		t.Fatalf("code = %s, want invalid_argument (%v)", got, err)
	}
	if !strings.Contains(err.Error(), "color") {
		t.Fatalf("err = %v, want the offending attribute named", err)
	}
	if posts, _ := fake.counts(); posts != 0 {
		t.Fatalf("product POSTs = %d, want 0", posts)
	}
}

// TestCreateConfigurableResumeRejectsForeignSKU proves the ownership check: a
// resumed run does not adopt a product whose variant values differ.
func TestCreateConfigurableResumeRejectsForeignSKU(t *testing.T) {
	fake := newStagedFake()
	fake.addProduct("PHONE-01-BLK", "simple", "99") // not one of the requested values
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	svc := testService(t, ts.URL)

	in := validConfigurableInput()
	in.OperationID = "op-foreign"
	_, err := svc.CreateConfigurable(context.Background(), in)
	if got := errs.CodeOf(err); got != errs.CodeConflict {
		t.Fatalf("code = %s, want conflict (%v)", got, err)
	}
	if posts, _ := fake.counts(); posts != 0 {
		t.Fatalf("product POSTs = %d, want 0", posts)
	}
}

// TestCreateConfigurableResumesAfterLinkFailure is the MAG-07 regression: a
// failed link leaves an orphan child; re-running with the same operation id
// detects it, links it, and never re-POSTs an existing SKU.
func TestCreateConfigurableResumesAfterLinkFailure(t *testing.T) {
	fake := newStagedFake()
	fake.linkFail["PHONE-01-WHT"] = http.StatusInternalServerError
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	svc := testService(t, ts.URL)

	in := validConfigurableInput()
	in.OperationID = "op-resume-1"

	first, err := svc.CreateConfigurable(context.Background(), in)
	if err == nil {
		t.Fatal("a failed link must not be reported as success")
	}
	if first == nil || first.Outcome != ConfigurablePartial || first.FailedStage != stageLinks {
		t.Fatalf("first run = %+v, want a partial run that failed at links", first)
	}
	if got := strings.Join(first.OrphanSKUs, ","); got != "PHONE-01-WHT" {
		t.Fatalf("orphan_skus = %q, want the unlinked child", got)
	}
	if !contains(first.CompletedStages, stageChildren) || !contains(first.CompletedStages, stageParent) {
		t.Fatalf("completed_stages = %v, want children and parent", first.CompletedStages)
	}
	if stageState(first, stageLinks) != ConfigurableStageFailed {
		t.Fatalf("links stage = %q, want failed", stageState(first, stageLinks))
	}
	firstPosts, _ := fake.counts()
	if firstPosts != 3 {
		t.Fatalf("first run product POSTs = %d, want 3", firstPosts)
	}

	// Repair the link and resume with the same operation id.
	fake.mu.Lock()
	delete(fake.linkFail, "PHONE-01-WHT")
	fake.mu.Unlock()

	second, err := svc.CreateConfigurable(context.Background(), in)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if second.Outcome != ConfigurableCompleted || second.FailedStage != "" {
		t.Fatalf("resume = %+v, want completed", second)
	}
	if len(second.OrphanSKUs) != 0 {
		t.Fatalf("orphan_skus = %v, want none after the repair", second.OrphanSKUs)
	}
	if got := stageState(second, stageChildren); got != ConfigurableStageSkipped {
		t.Fatalf("children stage = %q, want skipped on resume", got)
	}
	if got := stageState(second, stageParent); got != ConfigurableStageSkipped {
		t.Fatalf("parent stage = %q, want skipped on resume", got)
	}
	if got := stageState(second, stageLinks); got != ConfigurableStageCompleted {
		t.Fatalf("links stage = %q, want completed", got)
	}
	if posts, _ := fake.counts(); posts != firstPosts {
		t.Fatalf("resume product POSTs = %d, want %d (an existing SKU is never re-posted)", posts, firstPosts)
	}
	// Exactly one link call on the resume: the orphan.
	linkCalls := 0
	for _, request := range fake.requestPaths() {
		if strings.HasSuffix(request, "/child") {
			linkCalls++
		}
	}
	if linkCalls != 3 {
		t.Fatalf("link calls = %d, want 3 (2 on the first run, 1 on the resume)", linkCalls)
	}
	if _, deletes := fake.counts(); deletes != 0 {
		t.Fatalf("deletes = %d, want 0 (no compensation without evidence)", deletes)
	}
}

// TestCreateConfigurableLinkVerificationFails proves the link call alone is not
// trusted: when the read-back does not report the child, the links stage fails.
func TestCreateConfigurableLinkVerificationFails(t *testing.T) {
	fake := newStagedFake()
	fake.linkDrop["PHONE-01-WHT"] = true // the link call succeeds, the read hides it
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	svc := testService(t, ts.URL)

	result, err := svc.CreateConfigurable(context.Background(), validConfigurableInput())
	if err == nil {
		t.Fatal("a link the parent does not report must not be a success")
	}
	if result == nil || result.Outcome != ConfigurablePartial || result.FailedStage != stageLinks {
		t.Fatalf("result = %+v, want a partial run that failed at links", result)
	}
	if got := strings.Join(result.OrphanSKUs, ","); got != "PHONE-01-WHT" {
		t.Fatalf("orphan_skus = %q, want the unverified child", got)
	}
	if !strings.Contains(stageDetail(err), "does not report") {
		t.Fatalf("err = %v, want the verification failure", err)
	}
	if _, deletes := fake.counts(); deletes != 0 {
		t.Fatalf("deletes = %d, want 0", deletes)
	}
}

// TestCreateConfigurableCompletedRecord proves a successful run exposes the full
// execution record.
func TestCreateConfigurableCompletedRecord(t *testing.T) {
	fake := newStagedFake()
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	svc := testService(t, ts.URL)

	result, err := svc.CreateConfigurable(context.Background(), validConfigurableInput())
	if err != nil {
		t.Fatalf("CreateConfigurable: %v", err)
	}
	if result.Outcome != ConfigurableCompleted {
		t.Fatalf("outcome = %q, want completed", result.Outcome)
	}
	if result.OperationID == "" {
		t.Fatal("operation id is empty")
	}
	if len(result.Stages) != len(configurableStages) {
		t.Fatalf("stages = %v, want %v", result.Stages, configurableStages)
	}
	for i, name := range configurableStages {
		if result.Stages[i].Name != name {
			t.Fatalf("stage %d = %q, want %q", i, result.Stages[i].Name, name)
		}
		if result.Stages[i].State != ConfigurableStageCompleted {
			t.Fatalf("stage %q = %q, want completed", name, result.Stages[i].State)
		}
		if result.Stages[i].Detail == "" {
			t.Fatalf("stage %q has no detail", name)
		}
	}
	if len(result.CompletedStages) != len(configurableStages) {
		t.Fatalf("completed_stages = %v", result.CompletedStages)
	}
	if result.FailedStage != "" || len(result.OrphanSKUs) != 0 {
		t.Fatalf("record = %+v, want no failure", result)
	}
	if !result.ParentInStock {
		t.Fatal("parent_in_stock = false, want true from the observed child stock")
	}
}

// TestCreateVariationsRepairsOrphanOnRetry is the MAG-07 regression for
// create_variations: a child created before a failed link used to be reported as
// "exists" and abandoned forever. The retry now repairs the link.
func TestCreateVariationsRepairsOrphanOnRetry(t *testing.T) {
	var mu sync.Mutex
	linked := map[string]bool{}
	failLink := true
	productPOSTs := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/products/CFG":
			_, _ = w.Write([]byte(`{"id":1,"sku":"CFG","name":"Parent","type_id":"configurable","price":19.99,"attribute_set_id":4}`))
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/products/CFG-ORPHAN":
			if productPOSTs == 0 {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"id":3,"sku":"CFG-ORPHAN","name":"Parent CFG-ORPHAN","type_id":"simple","price":19.99,"attribute_set_id":4}`))
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/configurable-products/CFG/children":
			items := make([]string, 0, len(linked))
			for sku := range linked {
				items = append(items, fmt.Sprintf(`{"sku":%q,"type_id":"simple"}`, sku))
			}
			sort.Strings(items)
			_, _ = io.WriteString(w, "["+strings.Join(items, ",")+"]")
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products":
			productPOSTs++
			_, _ = w.Write([]byte(`{"id":3,"sku":"CFG-ORPHAN","name":"Parent CFG-ORPHAN","type_id":"simple","price":19.99,"attribute_set_id":4}`))
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/configurable-products/CFG/child":
			if failLink {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"The child could not be linked."}`))
				return
			}
			linked["CFG-ORPHAN"] = true
			_, _ = w.Write([]byte(`true`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	variants := []VariationInput{{SKU: "CFG-ORPHAN", Attributes: map[string]string{"color": "12"}}}

	first, err := svc.CreateVariations(context.Background(), "CFG", variants)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if first.Failed != 1 || first.Rows[0].Status != "failed" {
		t.Fatalf("first call = %+v, want one failed row", first)
	}
	if productPOSTs != 1 {
		t.Fatalf("product POSTs = %d, want 1", productPOSTs)
	}

	mu.Lock()
	failLink = false
	mu.Unlock()

	second, err := svc.CreateVariations(context.Background(), "CFG", variants)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if second.Linked != 1 || second.Skipped != 0 || second.Created != 0 || second.Failed != 0 {
		t.Fatalf("second call = %+v, want the orphan repaired", second)
	}
	if second.Rows[0].Status != "linked" {
		t.Fatalf("row = %+v, want linked", second.Rows[0])
	}
	if productPOSTs != 1 {
		t.Fatalf("product POSTs = %d, want 1 (the orphan is never re-created)", productPOSTs)
	}
	if !linked["CFG-ORPHAN"] {
		t.Fatal("the orphan was not linked")
	}
}
