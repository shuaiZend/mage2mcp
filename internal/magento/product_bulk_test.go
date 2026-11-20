// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestBulkUpdateProductsPartialSuccess(t *testing.T) {
	// The writes run concurrently, so the counter has to be atomic.
	var calls atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/FAIL-1") {
			http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"id":1,"sku":"SKU-1","name":"Widget","type_id":"simple","price":9.99,"attribute_set_id":4}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	status := 2
	result, err := svc.BulkUpdateProducts(context.Background(), []string{"SKU-1", "FAIL-1", "SKU-1"}, UpdateProductInput{Status: &status})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 3 || result.Updated != 1 || result.Failed != 1 || len(result.Rows) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.Rows[0].Status != "updated" || result.Rows[1].Status != "failed" || result.Rows[1].Error == "" {
		t.Fatalf("rows = %+v", result.Rows)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2 (duplicate skipped)", got)
	}
}

func TestBulkUpdateProductsValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	status := 2
	name := "New name"
	cases := []struct {
		skus   []string
		update UpdateProductInput
	}{
		{nil, UpdateProductInput{Status: &status}},
		{[]string{"bad sku!"}, UpdateProductInput{Status: &status}},
		{[]string{"SKU-1"}, UpdateProductInput{}},
		{[]string{"SKU-1"}, UpdateProductInput{Name: &name}},
		{[]string{"SKU-1"}, UpdateProductInput{Status: &status, Name: &name}},
	}
	for i, tc := range cases {
		if _, err := svc.BulkUpdateProducts(ctx, tc.skus, tc.update); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
	tooMany := make([]string, maxBulkUpdateSKUs+1)
	for i := range tooMany {
		tooMany[i] = "SKU"
	}
	if _, err := svc.BulkUpdateProducts(ctx, tooMany, UpdateProductInput{Status: &status}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("ceiling err = %v", err)
	}
}
