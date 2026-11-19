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

func TestGetCategory(t *testing.T) {
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":12,"parent_id":2,"name":"Shoes","is_active":true,"position":3,"level":2,"include_in_menu":true,"path":"1/2/12","children_count":"4"}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	category, err := svc.GetCategory(context.Background(), 12)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rest/default/V1/categories/12" {
		t.Fatalf("path = %q", path)
	}
	if category.ID != 12 || category.Name != "Shoes" || !category.IsActive || category.Level != 2 || category.ChildrenCount != "4" {
		t.Fatalf("category = %+v", category)
	}
}

func TestCategoryTreeFlattensChildren(t *testing.T) {
	var query url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":2,"parent_id":1,"name":"Default","level":1,"children_data":[
			{"id":3,"parent_id":2,"name":"Men","level":2,"children_data":[
				{"id":4,"parent_id":3,"name":"Shoes","level":3,"children_data":[]}
			]},
			{"id":5,"parent_id":2,"name":"Women","level":2,"children_data":[]}
		]}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	categories, err := svc.CategoryTree(context.Background(), 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if query.Get("rootCategoryId") != "2" || query.Get("depth") != "3" {
		t.Fatalf("query = %v", query)
	}
	if len(categories) != 4 {
		t.Fatalf("categories = %+v", categories)
	}
	wantIDs := []int{2, 3, 4, 5}
	for i, want := range wantIDs {
		if categories[i].ID != want {
			t.Fatalf("order = %+v", categories)
		}
	}
	if categories[2].ParentID != 3 || categories[2].Level != 3 {
		t.Fatalf("nested node = %+v", categories[2])
	}
}

func TestAttributeSetsAndAttributes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/products/attribute-sets/sets/list":
			_, _ = w.Write([]byte(`{"items":[{"attribute_set_id":4,"attribute_set_name":"Default","entity_type_id":4}],"total_count":1}`))
		case "/rest/default/V1/products/attribute-sets/4/attributes":
			_, _ = w.Write([]byte(`[{"attribute_id":73,"attribute_code":"color","frontend_label":"Color","frontend_input":"select","is_required":false,"is_user_defined":true,"default_value":""}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	sets, err := svc.AttributeSets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || sets[0].ID != 4 || sets[0].Name != "Default" {
		t.Fatalf("sets = %+v", sets)
	}
	attributes, err := svc.Attributes(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(attributes) != 1 || attributes[0].Code != "color" || attributes[0].Input != "select" {
		t.Fatalf("attributes = %+v", attributes)
	}
}

func TestAttributesAcceptsEnvelope(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"attribute_id":1,"attribute_code":"sku"}]}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	attributes, err := svc.Attributes(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(attributes) != 1 || attributes[0].Code != "sku" {
		t.Fatalf("attributes = %+v", attributes)
	}
}

func TestCatalogValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if _, err := svc.GetCategory(ctx, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("category err = %v", err)
	}
	if _, err := svc.CategoryTree(ctx, 2, 9); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("depth err = %v", err)
	}
	if _, err := svc.CategoryTree(ctx, 0, 2); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("root err = %v", err)
	}
	if _, err := svc.Attributes(ctx, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("attribute set err = %v", err)
	}
}

func TestCreateAttributeOption(t *testing.T) {
	var method, path string
	var body attributeOptionBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"label":"Green","value":"14"}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	option, err := svc.CreateAttributeOption(context.Background(), "color", "Green")
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/rest/default/V1/products/attributes/color/options" {
		t.Fatalf("request = %s %s", method, path)
	}
	if body.Option.Label != "Green" {
		t.Fatalf("body = %+v", body)
	}
	if option.Label != "Green" || option.Value != "14" {
		t.Fatalf("option = %+v", option)
	}
}

func TestCreateAttributeOptionValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if _, err := svc.CreateAttributeOption(ctx, "Bad Code!", "Green"); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("code err = %v", err)
	}
	if _, err := svc.CreateAttributeOption(ctx, "color", ""); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("label err = %v", err)
	}
}

func TestDeleteAttributeOption(t *testing.T) {
	var method, path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	if err := svc.DeleteAttributeOption(context.Background(), "color", 14); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || path != "/rest/default/V1/products/attributes/color/options/14" {
		t.Fatalf("request = %s %s", method, path)
	}
	if err := svc.DeleteAttributeOption(context.Background(), "Bad Code!", 14); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("code err = %v", err)
	}
	if err := svc.DeleteAttributeOption(context.Background(), "color", 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("id err = %v", err)
	}
}

func TestBulkCreateAttributeOptions(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 2 {
			http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"label":"Green","value":"14"}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	result, err := svc.BulkCreateAttributeOptions(context.Background(), "color", []string{"Green", "Blue", "Green"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 3 || result.Created != 1 || result.Failed != 1 || len(result.Rows) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.Rows[0].Value != "14" || result.Rows[1].Status != "failed" || calls != 2 {
		t.Fatalf("rows = %+v calls = %d", result.Rows, calls)
	}
}

func TestBulkCreateAttributeOptionsValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if _, err := svc.BulkCreateAttributeOptions(ctx, "Bad Code!", []string{"Green"}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("code err = %v", err)
	}
	if _, err := svc.BulkCreateAttributeOptions(ctx, "color", nil); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty err = %v", err)
	}
	if _, err := svc.BulkCreateAttributeOptions(ctx, "color", []string{"Green", " "}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("blank err = %v", err)
	}
	tooMany := make([]string, maxBulkOptions+1)
	for i := range tooMany {
		tooMany[i] = "Label"
	}
	if _, err := svc.BulkCreateAttributeOptions(ctx, "color", tooMany); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("ceiling err = %v", err)
	}
}
