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

func TestCreateCategory(t *testing.T) {
	var method, path string
	var body categoryCreateBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":12,"parent_id":2,"name":"Shoes","is_active":true,"position":1,"level":2}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	category, err := svc.CreateCategory(context.Background(), CategoryCreateInput{
		ParentID: 2, Name: "Shoes", IsActive: true, Position: 1, IncludeInMenu: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/rest/default/V1/categories" {
		t.Fatalf("request = %s %s", method, path)
	}
	if body.Category.ParentID != 2 || body.Category.Name != "Shoes" || !body.Category.IncludeInMenu {
		t.Fatalf("body = %+v", body)
	}
	if category.ID != 12 || category.Name != "Shoes" {
		t.Fatalf("category = %+v", category)
	}
}

func TestUpdateCategoryPartial(t *testing.T) {
	var body map[string]map[string]any
	var method, path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":12,"name":"New Name","is_active":false}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	name := "New Name"
	inactive := false
	category, err := svc.UpdateCategory(context.Background(), CategoryUpdateInput{
		CategoryID: 12, Name: &name, IsActive: &inactive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut || path != "/rest/default/V1/categories/12" {
		t.Fatalf("request = %s %s", method, path)
	}
	if body["category"]["name"] != "New Name" || body["category"]["is_active"] != false {
		t.Fatalf("body = %+v", body)
	}
	if _, ok := body["category"]["position"]; ok {
		t.Fatal("unset position must be omitted")
	}
	if category.Name != "New Name" {
		t.Fatalf("category = %+v", category)
	}
}

func TestAssignProductToCategory(t *testing.T) {
	var body categoryProductBody
	var method, path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	if err := svc.AssignProductToCategory(context.Background(), 12, "SKU-1", 5); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/rest/default/V1/categories/12/products" {
		t.Fatalf("request = %s %s", method, path)
	}
	if body.ProductLink.SKU != "SKU-1" || body.ProductLink.CategoryID != "12" || body.ProductLink.Position != 5 {
		t.Fatalf("body = %+v", body)
	}
}

func TestCategoryWriteValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if _, err := svc.CreateCategory(ctx, CategoryCreateInput{ParentID: 0, Name: "x"}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("parent err = %v", err)
	}
	if _, err := svc.CreateCategory(ctx, CategoryCreateInput{ParentID: 2, Name: ""}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("name err = %v", err)
	}
	if _, err := svc.CreateCategory(ctx, CategoryCreateInput{ParentID: 2, Name: "x", Position: 20000}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("position err = %v", err)
	}
	if _, err := svc.UpdateCategory(ctx, CategoryUpdateInput{CategoryID: 0}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("update id err = %v", err)
	}
	if _, err := svc.UpdateCategory(ctx, CategoryUpdateInput{CategoryID: 12}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty update err = %v", err)
	}
	if err := svc.AssignProductToCategory(ctx, 0, "SKU-1", 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("assign category err = %v", err)
	}
	if err := svc.AssignProductToCategory(ctx, 12, "bad sku!", 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("assign sku err = %v", err)
	}
}

func TestDeleteCategory(t *testing.T) {
	var method, path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	if err := svc.DeleteCategory(context.Background(), 12); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || path != "/rest/default/V1/categories/12" {
		t.Fatalf("request = %s %s", method, path)
	}
	if err := svc.DeleteCategory(context.Background(), 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("validation err = %v", err)
	}
}
