// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestAssignSourceItemsRequest(t *testing.T) {
	service, rec := newWriteService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[]`)
	})
	items := []SourceItem{
		{SKU: "SKU-1", SourceCode: "default", Quantity: 5, Status: 1},
		{SKU: "SKU-2", SourceCode: "warehouse_a", Quantity: 0, Status: 0},
	}
	if err := service.AssignSourceItems(context.Background(), items); err != nil {
		t.Fatalf("AssignSourceItems: %v", err)
	}
	requests := rec.all()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	got := requests[0]
	if got.Method != http.MethodPost || got.Path != "/rest/default/V1/inventory/source-items" {
		t.Fatalf("request = %s %s, want POST /rest/default/V1/inventory/source-items", got.Method, got.Path)
	}
	assertOAuthHeader(t, got.Auth)
	assertJSONBody(t, got.Body, `{"sourceItems":[
		{"sku":"SKU-1","source_code":"default","quantity":5,"status":1},
		{"sku":"SKU-2","source_code":"warehouse_a","quantity":0,"status":0}]}`)
}

func TestAssignSourceItemsValidation(t *testing.T) {
	service, rec := newWriteService(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid input reached the upstream: %s %s", r.Method, r.URL.Path)
	})
	valid := SourceItem{SKU: "SKU-1", SourceCode: "default", Quantity: 1, Status: 1}
	tooMany := make([]SourceItem, 0, 101)
	for i := 0; i < 101; i++ {
		tooMany = append(tooMany, SourceItem{
			SKU: "SKU-" + strconv.Itoa(i), SourceCode: "default", Quantity: 1, Status: 1,
		})
	}
	tests := []struct {
		name  string
		items []SourceItem
	}{
		{name: "empty"},
		{name: "more than 100 items", items: tooMany},
		{name: "bad sku", items: []SourceItem{{SKU: "bad sku", SourceCode: "default"}}},
		{name: "bad source code", items: []SourceItem{{SKU: "SKU-1", SourceCode: "Warehouse"}}},
		{name: "negative quantity", items: []SourceItem{{SKU: "SKU-1", SourceCode: "default", Quantity: -1}}},
		{name: "bad status", items: []SourceItem{{SKU: "SKU-1", SourceCode: "default", Status: 2}}},
		{name: "duplicate pair", items: []SourceItem{valid, valid}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := service.AssignSourceItems(context.Background(), tc.items)
			if code := errs.CodeOf(err); code != errs.CodeInvalidArgument {
				t.Fatalf("code = %s, want invalid_argument (%v)", code, err)
			}
		})
	}
	if rec.count() != 0 {
		t.Fatalf("invalid input reached the upstream %d times", rec.count())
	}
}

func TestAssignSourceItemsErrorMapping(t *testing.T) {
	tests := []struct {
		status    int
		code      errs.Code
		retryable bool
	}{
		{status: http.StatusBadRequest, code: errs.CodeValidationFailed},
		{status: http.StatusUnauthorized, code: errs.CodeUnauthenticated},
		{status: http.StatusForbidden, code: errs.CodePermissionDenied},
		{status: http.StatusConflict, code: errs.CodeConflict},
		{status: http.StatusTooManyRequests, code: errs.CodeRateLimited, retryable: true},
		{status: http.StatusServiceUnavailable, code: errs.CodeUpstreamUnavailable, retryable: true},
	}
	for _, tc := range tests {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			service, _ := newWriteService(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"message":"SECRET-UPSTREAM"}`)
			})
			err := service.AssignSourceItems(context.Background(), []SourceItem{
				{SKU: "SKU-1", SourceCode: "default", Quantity: 1, Status: 1},
			})
			var e *errs.Error
			if !errors.As(err, &e) {
				t.Fatalf("error is not *errs.Error: %v", err)
			}
			if e.Code != tc.code || e.Retryable != tc.retryable {
				t.Fatalf("code = %s retryable = %v, want %s/%v", e.Code, e.Retryable, tc.code, tc.retryable)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("error leaked the upstream body: %v", err)
			}
		})
	}
}

func TestAssignSourceItemsAcceptsExactlyOneHundred(t *testing.T) {
	service, rec := newWriteService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[]`)
	})
	items := make([]SourceItem, 0, 100)
	for i := 0; i < 100; i++ {
		items = append(items, SourceItem{
			SKU: fmt.Sprintf("SKU-%d", i), SourceCode: "default", Quantity: i, Status: 1,
		})
	}
	if err := service.AssignSourceItems(context.Background(), items); err != nil {
		t.Fatalf("AssignSourceItems(100 items): %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("requests = %d, want 1", rec.count())
	}
}

func TestInventorySourcesAndStocks(t *testing.T) {
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/inventory/sources":
			_, _ = w.Write([]byte(`{"items":[{"source_code":"default","name":"Default Source","enabled":true,"country_id":"CN","city":"Shanghai","latitude":31.2,"longitude":121.5}]}`))
		case "/rest/default/V1/inventory/stocks":
			_, _ = w.Write([]byte(`{"items":[{"stock_id":1,"name":"Default Stock"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	sources, err := svc.Sources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].SourceCode != "default" || !sources[0].Enabled || sources[0].City != "Shanghai" {
		t.Fatalf("sources = %+v", sources)
	}
	stocks, err := svc.Stocks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(stocks) != 1 || stocks[0].StockID != 1 || stocks[0].Name != "Default Stock" {
		t.Fatalf("stocks = %+v", stocks)
	}
	if len(paths) != 2 || !strings.Contains(paths[0], "searchCriteria%5BpageSize%5D=100") {
		t.Fatalf("paths = %v", paths)
	}
}
