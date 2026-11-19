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

func TestDeleteMedia(t *testing.T) {
	var method, path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	if err := svc.DeleteMedia(context.Background(), "SKU-1", 9); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || path != "/rest/default/V1/products/SKU-1/media/9" {
		t.Fatalf("request = %s %s", method, path)
	}
	if err := svc.DeleteMedia(context.Background(), "SKU-1", 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("validation err = %v", err)
	}
}

func TestStockStatuses(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/MISSING") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"qty":5,"is_in_stock":true,"manage_stock":true}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	items, err := svc.StockStatuses(context.Background(), []string{"SKU-1", "MISSING", "SKU-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Status == nil || items[0].Status.Qty != 5 {
		t.Fatalf("first = %+v", items[0])
	}
	if items[1].Error == "" {
		t.Fatalf("missing sku must report a per-item error: %+v", items[1])
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (duplicate skipped)", calls.Load())
	}
}

func TestStockStatusesValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if _, err := svc.StockStatuses(ctx, nil); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty err = %v", err)
	}
	if _, err := svc.StockStatuses(ctx, []string{"bad sku!"}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("sku err = %v", err)
	}
	tooMany := make([]string, maxStockStatusBatch+1)
	for i := range tooMany {
		tooMany[i] = "SKU"
	}
	if _, err := svc.StockStatuses(ctx, tooMany); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("ceiling err = %v", err)
	}
}
