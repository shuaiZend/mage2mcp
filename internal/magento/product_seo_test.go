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
