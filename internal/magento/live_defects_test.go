// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Regression tests for the defects found by the live run against a real Magento
// store on 2026-11-21 (docs/audit/live-magento-defects.md).
//
// Each of these passed CI before the live run, because the fakes encoded the
// gateway's assumption instead of the wire contract. The point of every test here
// is to assert the wire shape — method, body key, projected field — not the
// gateway's own idea of it.

// LIVE-3: Magento answers a successful inventory-source create with an empty
// array. Treating that as unreadable told the caller the create had failed while
// the source existed.
func TestCreateSourceAcceptsEmptyArrayResponse(t *testing.T) {
	var posted bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/inventory/sources":
			posted = true
			// Exactly what a real store returns on success.
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/inventory/sources":
			_, _ = io.WriteString(w, `{"items":[{"source_code":"mcptest_src","name":"MCPTEST Source","enabled":true}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	source, err := svc.CreateSource(context.Background(), InventorySourceInput{
		SourceCode: "mcptest_src", Name: "MCPTEST Source",
	})
	if err != nil {
		t.Fatalf("CreateSource with an empty-array response: %v", err)
	}
	if !posted {
		t.Fatal("create was not attempted")
	}
	if source == nil || source.SourceCode != "mcptest_src" {
		t.Fatalf("source = %+v, want the read-back source", source)
	}
}

// LIVE-3 control: a create that reports success but whose source cannot be read
// back must say so rather than fabricate an object.
func TestCreateSourceReportsUnconfirmedWhenReadBackMisses(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"items":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	source, err := svc.CreateSource(context.Background(), InventorySourceInput{
		SourceCode: "mcptest_src", Name: "MCPTEST Source",
	})
	if err == nil {
		t.Fatalf("CreateSource fabricated %+v despite a failed read-back", source)
	}
}

// LIVE-4: not every build exposes a customer-address create route. The supported
// fallback is PUT /customers/{id} with the address appended.
func TestCreateCustomerAddressFallsBackToCustomerUpdate(t *testing.T) {
	var putBody map[string]any
	ts := httptest.NewServer(customerAddressFallbackHandler(t, &putBody))
	defer ts.Close()
	svc := testService(t, ts.URL)

	address, err := svc.CreateCustomerAddress(context.Background(), 7, CustomerAddressInput{
		Firstname: "Ada", Lastname: "Lovelace", Street: []string{"1 Analytical Engine Way"},
		City: "London", Postcode: "N1 9GU", CountryID: "GB", Telephone: "0123456789",
	})
	if err != nil {
		t.Fatalf("CreateCustomerAddress fallback: %v", err)
	}
	if address == nil || address.ID == 0 {
		t.Fatalf("address = %+v, want an id from the read-back", address)
	}
	customer, _ := putBody["customer"].(map[string]any)
	if customer == nil {
		t.Fatalf("PUT body = %v, want a customer object", putBody)
	}
	addresses, _ := customer["addresses"].([]any)
	if len(addresses) != 1 {
		t.Fatalf("customer.addresses = %v, want the new address appended", addresses)
	}
}

// customerAddressFallbackHandler answers the two-phase flow the fallback performs:
// a 404 on the dedicated address route, then a customer read, a PUT, and a second
// read that reports the address the store assigned.
func customerAddressFallbackHandler(t *testing.T, putBody *map[string]any) http.Handler {
	t.Helper()
	var gets int
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/addresses"):
			http.NotFound(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/customers/7"):
			gets++
			if gets == 1 {
				_, _ = io.WriteString(w, `{"id":7,"website_id":1,"email":"a@example.com","firstname":"Ada","lastname":"Lovelace","addresses":[]}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":7,"addresses":[{"id":42,"city":"London","country_id":"GB"}]}`)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/customers/7"):
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, putBody)
			_, _ = io.WriteString(w, `{"id":7}`)
		default:
			http.NotFound(w, r)
		}
	})
}

// LIVE-5: the `fields` projection is a whitelist, so total_count must be part of
// it. Without it the reported total is always zero.
func TestOrderSearchProjectsTotalCount(t *testing.T) {
	var fields string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields = r.URL.Query().Get("fields")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[{"entity_id":1}],"total_count":37}`)
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	orders, total, err := svc.SearchOrders(context.Background(), OrderQuery{Limit: 1})
	if err != nil {
		t.Fatalf("SearchOrders: %v", err)
	}
	if !strings.Contains(fields, "total_count") {
		t.Fatalf("fields = %q, want it to project total_count", fields)
	}
	if len(orders) != 1 {
		t.Fatalf("orders = %d, want 1", len(orders))
	}
	if total != 37 {
		t.Fatalf("total = %d, want the projected total_count 37", total)
	}
}

// LIVE-5: a scan that stops at the cap has been truncated even when the store did
// not report a total. Claiming completeness in that case is the defect.
func TestSalesSummaryReportsTruncationWhenCapReached(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A full page of `maxOrderLimit` orders, and no total_count at all.
		items := make([]string, 0, maxOrderLimit)
		for i := 0; i < maxOrderLimit; i++ {
			items = append(items, fmt.Sprintf(
				`{"entity_id":%d,"created_at":"2026-01-02 03:04:05","grand_total":10,"total_qty_ordered":1,"order_currency_code":"PLN"}`,
				i+1))
		}
		_, _ = io.WriteString(w, `{"items":[`+strings.Join(items, ",")+`]}`)
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	summary, err := svc.SalesSummary(context.Background(), time.Now().AddDate(0, -1, 0), time.Now())
	if err != nil {
		t.Fatalf("SalesSummary: %v", err)
	}
	if summary.ScannedOrders != maxSummaryOrders {
		t.Fatalf("scanned = %d, want the cap %d", summary.ScannedOrders, maxSummaryOrders)
	}
	if !summary.Truncated {
		t.Fatal("a scan that stopped at the cap reported complete")
	}
}

// LIVE-5 control: a short page proves the window is exhausted, so the scan is
// complete and must not claim truncation.
func TestSalesSummaryCompleteWhenPageIsShort(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[{"entity_id":1,"created_at":"2026-01-02 03:04:05","grand_total":10,"total_qty_ordered":1,"order_currency_code":"PLN"}]}`)
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	summary, err := svc.SalesSummary(context.Background(), time.Now().AddDate(0, -1, 0), time.Now())
	if err != nil {
		t.Fatalf("SalesSummary: %v", err)
	}
	if summary.Truncated {
		t.Fatalf("a complete scan reported truncated: %+v", summary)
	}
}

// MAG-20 / LIVE-1 companion: Magento rejects a Polish address without a numeric
// region id ("regionId" is required), so the field must survive into the payload.
func TestOrderAddressCarriesRegionID(t *testing.T) {
	payload := OrderAddressInput{
		Firstname: "Ada", Lastname: "Lovelace", Street: []string{"1 Way"},
		City: "Wrocław", Postcode: "50-001", CountryID: "PL", Telephone: "123", RegionID: 997,
	}.toPayload()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"region_id":997`) {
		t.Fatalf("payload = %s, want region_id present", raw)
	}
	// An unset region id must be omitted, not sent as 0, which Magento rejects.
	empty, _ := json.Marshal(OrderAddressInput{Firstname: "A"}.toPayload())
	if strings.Contains(string(empty), "region_id") {
		t.Fatalf("payload = %s, want region_id omitted when unset", empty)
	}
}
