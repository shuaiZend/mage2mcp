// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestCreateProductSendsSEOAttributes(t *testing.T) {
	var body struct {
		Product struct {
			Weight           string             `json:"weight"`
			CustomAttributes []productAttribute `json:"custom_attributes"`
		} `json:"product"`
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"sku":"SKU-1","name":"Widget","type_id":"simple","price":9.99,"attribute_set_id":4}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	_, err := svc.Create(context.Background(), CreateProductInput{
		SKU: "SKU-1", Name: "Widget", AttributeSetID: 4, TypeID: "simple", Price: "9.99",
		Description: "A long description", ShortDescription: "Short", URLKey: "blue-widget",
		MetaTitle: "Blue Widget | Shop", MetaKeywords: "widget, blue", MetaDescription: "Buy the blue widget.",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, attr := range body.Product.CustomAttributes {
		got[attr.AttributeCode] = attr.Value
	}
	if got["url_key"] != "blue-widget" || got["meta_title"] != "Blue Widget | Shop" || got["description"] != "A long description" {
		t.Fatalf("attributes = %+v", got)
	}
}

func TestUpdateProductSendsOnlyProvidedSEOFields(t *testing.T) {
	var body map[string]map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"sku":"SKU-1","name":"Widget","type_id":"simple","price":9.99,"attribute_set_id":4}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	title := "New title"
	_, err := svc.Update(context.Background(), "SKU-1", UpdateProductInput{MetaTitle: &title})
	if err != nil {
		t.Fatal(err)
	}
	attrs, _ := body["product"]["custom_attributes"].([]any)
	if len(attrs) != 1 {
		t.Fatalf("custom_attributes = %+v", attrs)
	}
	first, _ := attrs[0].(map[string]any)
	if first["attribute_code"] != "meta_title" || first["value"] != "New title" {
		t.Fatalf("attribute = %+v", first)
	}
}

func TestSEOValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	cases := []CreateProductInput{
		{SKU: "SKU-1", Name: "Widget", AttributeSetID: 4, TypeID: "simple", Price: "9.99", URLKey: "Bad Slug"},
		{SKU: "SKU-1", Name: "Widget", AttributeSetID: 4, TypeID: "simple", Price: "9.99", MetaTitle: strings.Repeat("x", 256)},
		{SKU: "SKU-1", Name: "Widget", AttributeSetID: 4, TypeID: "simple", Price: "9.99", Description: "bad\x00copy"},
		{SKU: "SKU-1", Name: "Widget", AttributeSetID: 4, TypeID: "simple", Price: "9.99", Attributes: map[string]string{"meta_title": "duplicate"}},
	}
	for i, in := range cases {
		if _, err := svc.Create(ctx, in); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
}

func TestAuditSEOMissingFields(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"sku":"SKU-1","custom_attributes":[]}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	audit, err := svc.AuditSEO(context.Background(), "SKU-1")
	if err != nil {
		t.Fatal(err)
	}
	if audit.Score != 0 {
		t.Fatalf("score = %d, want 0", audit.Score)
	}
	statuses := map[string]string{}
	for _, check := range audit.Checks {
		statuses[check.Field] = check.Status
	}
	if statuses["description"] != "fail" || statuses["meta_title"] != "warn" {
		t.Fatalf("checks = %+v", audit.Checks)
	}
}

func TestAuditSEOCompleteProduct(t *testing.T) {
	payload := map[string]any{
		"id": 1, "sku": "SKU-1",
		"custom_attributes": []map[string]any{
			{"attribute_code": "description", "value": strings.Repeat("copy ", 100)},
			{"attribute_code": "short_description", "value": "Short summary"},
			{"attribute_code": "url_key", "value": "blue-widget"},
			{"attribute_code": "meta_title", "value": "Blue Widget | Example Store Official"},
			{"attribute_code": "meta_description", "value": strings.Repeat("a", 140)},
			{"attribute_code": "meta_keywords", "value": "widget"},
		},
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	audit, err := svc.AuditSEO(context.Background(), "SKU-1")
	if err != nil {
		t.Fatal(err)
	}
	if audit.Score != 100 {
		t.Fatalf("score = %d, checks = %+v", audit.Score, audit.Checks)
	}
}

func TestImportRowSEOValidation(t *testing.T) {
	rows, err := parseImportRows("csv", []byte("sku,name,meta_title\nSKU-1,Widget,"+strings.Repeat("x", 300)+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	problems := validateImportRow(rows[0], map[string]int{})
	if len(problems) == 0 {
		t.Fatal("oversized meta_title was accepted")
	}
	if problems[0].Field != "meta_title" {
		t.Fatalf("problem = %+v", problems[0])
	}
}

func TestConfigurableChildCarriesSEOFields(t *testing.T) {
	var bodies []map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"sku":"CHILD-1","name":"Child","type_id":"simple","price":9.99,"attribute_set_id":4}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	parent := CreateProductInput{Name: "Parent", AttributeSetID: 4, Price: "9.99", WebsiteIDs: []int{1}}
	_, err := svc.createChild(context.Background(), parent, ChildProduct{
		SKU: "CHILD-1", Attributes: map[string]string{"color": "1"},
		URLKey: "parent-blue", MetaTitle: "Parent Blue | Shop", Description: "Child copy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 {
		t.Fatalf("bodies = %+v", bodies)
	}
	product, _ := bodies[0]["product"].(map[string]any)
	attrs, _ := product["custom_attributes"].([]any)
	got := map[string]string{}
	for _, attr := range attrs {
		entry, _ := attr.(map[string]any)
		code, _ := entry["attribute_code"].(string)
		value, _ := entry["value"].(string)
		got[code] = value
	}
	if got["url_key"] != "parent-blue" || got["meta_title"] != "Parent Blue | Shop" || got["description"] != "Child copy" {
		t.Fatalf("attributes = %+v", got)
	}
}

func TestUpdateProductReassignsCategoriesAndWebsites(t *testing.T) {
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"sku":"SKU-1","name":"Widget","type_id":"simple","price":9.99,"attribute_set_id":4}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	_, err := svc.Update(context.Background(), "SKU-1", UpdateProductInput{
		CategoryIDs: []int{12, 13}, WebsiteIDs: []int{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	product, _ := body["product"].(map[string]any)
	ext, _ := product["extension_attributes"].(map[string]any)
	links, _ := ext["category_links"].([]any)
	if len(links) != 2 {
		t.Fatalf("category_links = %+v", ext)
	}
	first, _ := links[0].(map[string]any)
	if first["category_id"] != "12" || first["position"] != float64(0) {
		t.Fatalf("link = %+v", first)
	}
	websites, _ := ext["website_ids"].([]any)
	if len(websites) != 1 {
		t.Fatalf("website_ids = %+v", ext)
	}
}

func TestUpdateProductClearsCategories(t *testing.T) {
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"sku":"SKU-1","name":"Widget","type_id":"simple","price":9.99,"attribute_set_id":4}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	if _, err := svc.Update(context.Background(), "SKU-1", UpdateProductInput{CategoryIDs: []int{}}); err != nil {
		t.Fatal(err)
	}
	product, _ := body["product"].(map[string]any)
	ext, _ := product["extension_attributes"].(map[string]any)
	if links, ok := ext["category_links"]; !ok || links == nil {
		t.Fatalf("clearing must send an empty category_links array: %+v", ext)
	}
	if links, _ := ext["category_links"].([]any); len(links) != 0 {
		t.Fatalf("category_links = %+v", links)
	}
}

func TestUpdateProductRejectsBadCategoryIDs(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	if _, err := svc.Update(context.Background(), "SKU-1", UpdateProductInput{CategoryIDs: []int{0}}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateProductSendsPricingAttributes(t *testing.T) {
	var body struct {
		Product struct {
			Weight           string             `json:"weight"`
			CustomAttributes []productAttribute `json:"custom_attributes"`
		} `json:"product"`
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"sku":"SKU-1","name":"Widget","type_id":"simple","price":9.99,"attribute_set_id":4}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	_, err := svc.Create(context.Background(), CreateProductInput{
		SKU: "SKU-1", Name: "Widget", AttributeSetID: 4, TypeID: "simple", Price: "9.99",
		Weight: "1.25", SpecialPrice: "7.99", SpecialFromDate: "2026-11-01", SpecialToDate: "2026-11-30",
		Cost: "5.00", TaxClassID: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, attr := range body.Product.CustomAttributes {
		got[attr.AttributeCode] = attr.Value
	}
	if body.Product.Weight != "1.25" {
		t.Fatalf("weight = %q, want top-level weight", body.Product.Weight)
	}
	if _, ok := got["weight"]; ok {
		t.Fatalf("weight must not be a custom attribute: %+v", got)
	}
	if got["special_price"] != "7.99" || got["tax_class_id"] != "2" {
		t.Fatalf("attributes = %+v", got)
	}
	if got["special_from_date"] != "2026-11-01" || got["special_to_date"] != "2026-11-30" {
		t.Fatalf("attributes = %+v", got)
	}
}

func TestPricingValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	base := CreateProductInput{SKU: "SKU-1", Name: "Widget", AttributeSetID: 4, TypeID: "simple", Price: "9.99"}
	cases := []CreateProductInput{
		{Weight: "heavy"},
		{SpecialPrice: "1.23456"},
		{SpecialFromDate: "01/11/2026"},
		{SpecialFromDate: "2026-11-30", SpecialToDate: "2026-11-01"},
		{Cost: "-1"},
		{TaxClassID: -1},
	}
	for i, patch := range cases {
		in := base
		in.Weight = patch.Weight
		in.SpecialPrice = patch.SpecialPrice
		in.SpecialFromDate = patch.SpecialFromDate
		in.SpecialToDate = patch.SpecialToDate
		in.Cost = patch.Cost
		in.TaxClassID = patch.TaxClassID
		if _, err := svc.Create(ctx, in); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
	duplicate := base
	duplicate.Attributes = map[string]string{"weight": "1"}
	if _, err := svc.Create(ctx, duplicate); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("duplicate weight err = %v", err)
	}
}
