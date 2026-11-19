// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestGetCustomerOmitsPII(t *testing.T) {
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":7,"group_id":1,"website_id":1,"store_id":1,"created_in":"Default Store View",
			"created_at":"2026-01-02 03:04:05","updated_at":"2026-02-03 04:05:06",
			"email":"buyer@example.com","firstname":"Ada","lastname":"Lovelace",
			"addresses":[{"city":"Shanghai","street":["Secret Road 1"]}]}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	customer, err := svc.GetCustomer(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rest/default/V1/customers/7" {
		t.Fatalf("path = %q", path)
	}
	if customer.ID != 7 || customer.GroupID != 1 || customer.CreatedIn != "Default Store View" {
		t.Fatalf("customer = %+v", customer)
	}
	raw, _ := json.Marshal(customer)
	for _, forbidden := range []string{"buyer@example.com", "Ada", "Lovelace", "Shanghai", "Secret Road"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("projection leaks %q: %s", forbidden, raw)
		}
	}
}

func TestSearchCustomersByEmail(t *testing.T) {
	var query url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":7,"group_id":1,"website_id":1,"store_id":1}],"total_count":1}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	customers, total, err := svc.SearchCustomers(context.Background(), "buyer@example.com", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(customers) != 1 || customers[0].ID != 7 {
		t.Fatalf("customers = %+v total=%d", customers, total)
	}
	if got := query.Get("searchCriteria[filter_groups][0][filters][0][field]"); got != "email" {
		t.Fatalf("filter field = %q", got)
	}
	if got := query.Get("searchCriteria[filter_groups][0][filters][0][value]"); got != "buyer@example.com" {
		t.Fatalf("filter value = %q", got)
	}
	fields := query.Get("fields")
	if strings.Contains(fields, "email") || strings.Contains(fields, "firstname") {
		t.Fatalf("fields request PII: %q", fields)
	}
}

func TestCustomerValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if _, err := svc.GetCustomer(ctx, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("id err = %v", err)
	}
	if _, _, err := svc.SearchCustomers(ctx, "", 10, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty email err = %v", err)
	}
	if _, _, err := svc.SearchCustomers(ctx, "not-an-email", 10, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("bad email err = %v", err)
	}
	if _, _, err := svc.SearchCustomers(ctx, "buyer@example.com", 500, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("limit err = %v", err)
	}
	if _, _, err := svc.SearchCustomers(ctx, "buyer@example.com", 10, 3); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("cursor err = %v", err)
	}
}
