// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
		if r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/products" {
			_, _ = io.WriteString(w, `{"items":[{"sku":"SKU-1"},{"sku":"FAIL-1"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/FAIL-1") {
			http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"id":1,"sku":"SKU-1","name":"Widget","type_id":"simple","price":9.99,"attribute_set_id":4}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	status := 2
	result, err := svc.BulkUpdateProducts(context.Background(), []string{"SKU-1", "FAIL-1"}, UpdateProductInput{Status: &status})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || result.Updated != 1 || result.Failed != 1 || len(result.Rows) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.Rows[0].Status != "updated" || result.Rows[1].Status != "failed" || result.Rows[1].Error == "" {
		t.Fatalf("rows = %+v", result.Rows)
	}
	// One existence check plus one PUT per SKU; the old silent duplicate fold is gone.
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3 (existence check + 2 PUTs)", got)
	}
}

// TestBulkUpdateProductsRejectsDuplicateSKUs guards SEM-CAT-01: a duplicate used
// to be folded silently, which made the result disagree with the request. It is
// now a validation error and nothing is written.
func TestBulkUpdateProductsRejectsDuplicateSKUs(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	status := 2
	_, err := svc.BulkUpdateProducts(context.Background(), []string{"SKU-1", "sku-1"}, UpdateProductInput{Status: &status})
	if errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("err = %v, want a duplicate explanation", err)
	}
}

// TestBulkUpdateProductsRejectsUnknownSKUs guards SEM-CAT-01: the tool schema
// promises that every SKU must exist or the whole call is rejected. The check
// runs before any write, so a typo cannot be reported as a per-row failure after
// other products were already updated.
func TestBulkUpdateProductsRejectsUnknownSKUs(t *testing.T) {
	var calls atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/products" {
			_, _ = io.WriteString(w, `{"items":[{"sku":"SKU-1"}]}`)
			return
		}
		t.Errorf("unexpected write after an unknown SKU: %s %s", r.Method, r.URL.Path)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	status := 2
	_, err := svc.BulkUpdateProducts(context.Background(), []string{"SKU-1", "NO-SUCH-SKU-999"}, UpdateProductInput{Status: &status})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if e.Details["unknown_skus"] != "NO-SUCH-SKU-999" {
		t.Fatalf("unknown_skus = %q, want NO-SUCH-SKU-999", e.Details["unknown_skus"])
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want only the existence check (no writes)", got)
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

// INT-02: a replayed bulk update must be visible in structuredContent, not only
// in the text fallback. The ledger injects the marker into the recorded result;
// this pins that the result type can carry it, so a replayed call is
// distinguishable from a fresh one at the JSON boundary.
func TestBulkUpdateResultCarriesTheReplayMarker(t *testing.T) {
	fresh := BulkUpdateResult{Total: 1, Updated: 1, Rows: []BulkUpdateRow{{SKU: "SKU-1", Status: "updated"}}}
	raw, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "replayed") {
		t.Fatalf("a fresh result must not claim to be a replay: %s", raw)
	}

	replayed := fresh
	replayed.Replayed = true
	raw, err = json.Marshal(replayed)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["replayed"] != true {
		t.Fatalf("replayed result = %s, want replayed:true in structuredContent", raw)
	}
	if decoded["updated"] != float64(1) {
		t.Fatalf("replayed result = %s, the counts must survive the marker", raw)
	}
}
