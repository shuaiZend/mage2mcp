// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	mcpserver "github.com/shuaiZend/mage2mcp/internal/mcp/server"
)

func TestGetOrderReturnsProjection(t *testing.T) {
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"entity_id": 42, "increment_id": "000000042", "status": "processing", "state": "processing",
			"store_id": 1, "customer_id": 7, "customer_email": "buyer@example.com",
			"created_at": "2026-11-17 10:00:00", "grand_total": 129.99, "total_qty_ordered": 2,
			"order_currency_code": "USD",
			"items": [{"item_id": 1, "sku": "SKU-1", "name": "Widget", "product_type": "simple",
				"qty_ordered": 2, "price": 64.995, "row_total": 129.99}],
			"billing_address": {"city": "Shanghai"}
		}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	order, err := svc.GetOrder(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rest/default/V1/orders/42" {
		t.Fatalf("path = %q", path)
	}
	if order.EntityID != 42 || order.Status != "processing" || order.GrandTotal != 129.99 || order.Currency != "USD" {
		t.Fatalf("order = %+v", order)
	}
	if len(order.Items) != 1 || order.Items[0].SKU != "SKU-1" || order.Items[0].QtyOrdered != 2 {
		t.Fatalf("items = %+v", order.Items)
	}
}

func TestGetOrderRejectsInvalidID(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	if _, err := svc.GetOrder(context.Background(), 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("err = %v", err)
	}
}

func TestSearchOrdersBuildsCriteria(t *testing.T) {
	var query url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"entity_id":1,"increment_id":"000000001","status":"complete","grand_total":10}],"total_count":1}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	orders, total, err := svc.SearchOrders(context.Background(), OrderQuery{
		Status: "complete", IncrementID: "000000001", CreatedFrom: "2026-11-01", CreatedTo: "2026-11-30",
		CustomerID: 7, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(orders) != 1 || orders[0].IncrementID != "000000001" {
		t.Fatalf("orders = %+v total=%d", orders, total)
	}
	if query.Get("searchCriteria[pageSize]") != "10" || query.Get("searchCriteria[currentPage]") != "1" {
		t.Fatalf("pagination = %v", query)
	}
	checks := map[string]string{
		"searchCriteria[filter_groups][0][filters][0][field]":          "increment_id",
		"searchCriteria[filter_groups][0][filters][0][value]":          "000000001",
		"searchCriteria[filter_groups][1][filters][0][field]":          "status",
		"searchCriteria[filter_groups][1][filters][0][value]":          "complete",
		"searchCriteria[filter_groups][2][filters][0][value]":          "2026-11-01 00:00:00",
		"searchCriteria[filter_groups][3][filters][0][condition_type]": "lt",
		"searchCriteria[filter_groups][4][filters][0][field]":          "customer_id",
		"searchCriteria[filter_groups][4][filters][0][value]":          "7",
	}
	for key, want := range checks {
		if got := query.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	fields := query.Get("fields")
	if !strings.HasPrefix(fields, "items[") {
		t.Fatalf("fields = %q", fields)
	}
	if strings.Contains(fields, "customer_email") {
		t.Fatal("search must not request customer email")
	}
}

func TestSearchOrdersValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	cases := []OrderQuery{
		{Status: "Not A Status"},
		{State: "../etc"},
		{IncrementID: "bad id!"},
		{Limit: 500},
		{CursorOffset: 3, Limit: 10},
		{CreatedFrom: "yesterday"},
		{CreatedFrom: "2026-11-30", CreatedTo: "2026-11-01"},
	}
	for i, q := range cases {
		if _, _, err := svc.SearchOrders(ctx, q); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
}

func TestSalesSummaryAggregates(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"created_at":"2026-11-17 10:00:00","grand_total":100,"total_qty_ordered":2,"order_currency_code":"USD"},
			{"created_at":"2026-11-18 11:00:00","grand_total":50,"total_qty_ordered":1,"order_currency_code":"USD"},
			{"created_at":"2026-11-18 12:00:00","grand_total":25,"total_qty_ordered":1,"order_currency_code":"USD"}
		],"total_count":3}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	from := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	summary, err := svc.SalesSummary(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalOrders != 3 || summary.TotalRevenue != 175 || summary.TotalItems != 4 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(summary.Days) != 2 || summary.Days[0].Date != "2026-11-17" || summary.Days[1].Orders != 2 {
		t.Fatalf("days = %+v", summary.Days)
	}
	if summary.Truncated || summary.MixedCurrency || summary.Currency != "USD" {
		t.Fatalf("flags = %+v", summary)
	}
}

func TestSalesSummaryTruncatedAndMixedCurrency(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"created_at":"2026-11-17 10:00:00","grand_total":10,"order_currency_code":"USD"},
			{"created_at":"2026-11-17 11:00:00","grand_total":10,"order_currency_code":"EUR"}
		],"total_count":50}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	from := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	summary, err := svc.SalesSummary(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Truncated {
		t.Fatalf("truncated = false, want true: %+v", summary)
	}
	if !summary.MixedCurrency {
		t.Fatalf("mixed currency not flagged: %+v", summary)
	}
}

func TestSalesSummaryValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	from := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	if _, err := svc.SalesSummary(ctx, from, from); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty window err = %v", err)
	}
	if _, err := svc.SalesSummary(ctx, from, from.Add(400*24*time.Hour)); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("oversized window err = %v", err)
	}
}

func TestOrderSummaryTool(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"entity_id":1,"increment_id":"000000001","grand_total":20,"total_qty_ordered":2,
			"created_at":"2026-11-18 10:00:00","status":"processing"}]}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	reg := registry.New()
	if err := Register(reg, svc); err != nil {
		t.Fatal(err)
	}
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: magentoConfirmService(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "magento.order.summary",
		Arguments: map[string]any{"instance": "store-us", "from": "2026-11-01", "to": "2026-11-19"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("summary error: %+v", result.Content)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(raw), `"orders":1`) && !strings.Contains(string(raw), `"Orders":1`) {
		t.Fatalf("summary payload = %s", raw)
	}
}

// ordersPageHandler serves a synthetic order window of `total` orders, paged the
// way Magento pages: pageSize items per currentPage, with total_count repeated on
// every response.
func ordersPageHandler(total int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("searchCriteria[currentPage]"))
		if page == 0 {
			page = 1
		}
		size, _ := strconv.Atoi(r.URL.Query().Get("searchCriteria[pageSize]"))
		if size == 0 {
			size = defaultOrderLimit
		}
		from := (page - 1) * size
		to := from + size
		if to > total {
			to = total
		}
		items := make([]string, 0, size)
		for i := from; i < to; i++ {
			items = append(items, fmt.Sprintf(
				`{"entity_id":%d,"created_at":"2026-11-%02d 10:00:00","grand_total":10,"total_qty_ordered":1,"order_currency_code":"USD"}`,
				i+1, 1+i%28))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"items":[%s],"total_count":%d}`, strings.Join(items, ","), total)
	}
}

// TestSalesSummaryTruncationIsNotSticky covers the audit finding that a complete
// 924-order scan reported truncated=true: the flag latched on the first page
// because total_count exceeded the page size.
func TestSalesSummaryTruncationIsNotSticky(t *testing.T) {
	const total = 924
	ts := httptest.NewServer(ordersPageHandler(total))
	defer ts.Close()

	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	summary, err := testService(t, ts.URL).SalesSummary(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ScannedOrders != total {
		t.Fatalf("scanned_orders = %d, want %d", summary.ScannedOrders, total)
	}
	if summary.TotalAvailable != total {
		t.Fatalf("total_available = %d, want %d", summary.TotalAvailable, total)
	}
	if summary.ExcludedOrders != 0 {
		t.Fatalf("excluded_orders = %d, want 0", summary.ExcludedOrders)
	}
	if summary.Truncated {
		t.Fatalf("truncated = true for a complete %d-order scan: %+v", total, summary)
	}
	if summary.TotalRevenue != float64(total)*10 {
		t.Fatalf("total_revenue = %v, want %v", summary.TotalRevenue, float64(total)*10)
	}
}

// TestSalesSummaryReportsExcludedOrders proves the flag is set only when orders
// were actually excluded, and that the number excluded is reported.
func TestSalesSummaryReportsExcludedOrders(t *testing.T) {
	const total = 20000
	ts := httptest.NewServer(ordersPageHandler(total))
	defer ts.Close()

	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	summary, err := testService(t, ts.URL).SalesSummary(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ScannedOrders != maxSummaryOrders {
		t.Fatalf("scanned_orders = %d, want the cap %d", summary.ScannedOrders, maxSummaryOrders)
	}
	if summary.TotalAvailable != total {
		t.Fatalf("total_available = %d, want %d", summary.TotalAvailable, total)
	}
	if want := total - maxSummaryOrders; summary.ExcludedOrders != want {
		t.Fatalf("excluded_orders = %d, want %d", summary.ExcludedOrders, want)
	}
	if !summary.Truncated {
		t.Fatal("truncated = false although orders were excluded")
	}
}

// --- summary timezone wiring (t16) ---------------------------------------

// summaryTimezoneServer serves a store view timezone and one order whose UTC
// timestamp is 23:30 on the 21st: 00:30 on the 22nd in Europe/Warsaw.
func summaryTimezoneServer(t *testing.T, queries *[]url.Values) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/store/storeConfigs":
			_, _ = w.Write([]byte(`[{"id":1,"code":"pl","timezone":"Europe/Warsaw","base_currency_code":"PLN"}]`))
		case "/rest/default/V1/orders":
			if queries != nil {
				*queries = append(*queries, r.URL.Query())
			}
			_, _ = w.Write([]byte(`{"items":[
				{"created_at":"2026-11-21 23:30:00","grand_total":10,"total_qty_ordered":1,"order_currency_code":"PLN"}
			],"total_count":1}`))
		default:
			_, _ = w.Write([]byte(`{"items":[],"total_count":0}`))
		}
	}))
}

// TestSalesSummaryBucketsByReportTimezone pins the wired timezone: the day bucket
// is the store view's calendar day, the date-only window is sent as that day's
// UTC instants, and the scan is ordered oldest first like sales.analysis.
func TestSalesSummaryBucketsByReportTimezone(t *testing.T) {
	var queries []url.Values
	ts := summaryTimezoneServer(t, &queries)
	defer ts.Close()
	svc := testService(t, ts.URL)

	summary, err := svc.SalesSummaryWindow(context.Background(), "2026-11-21", "2026-11-22")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Timezone != "Europe/Warsaw" {
		t.Fatalf("timezone = %q, want Europe/Warsaw", summary.Timezone)
	}
	if len(summary.Days) != 1 || summary.Days[0].Date != "2026-11-22" {
		t.Fatalf("days = %+v, want the order on the local day 2026-11-22", summary.Days)
	}
	if summary.Window != "2026-11-20T23:00:00Z/2026-11-21T23:00:00Z" {
		t.Fatalf("window = %q, want the UTC instants of the Warsaw day", summary.Window)
	}
	if len(queries) == 0 {
		t.Fatal("no order query was issued")
	}
	got := queries[0]
	if v := got.Get("searchCriteria[filter_groups][0][filters][0][value]"); v != "2026-11-20 23:00:00" {
		t.Fatalf("lower bound sent = %q, want the local day start in UTC", v)
	}
	if v := got.Get("searchCriteria[filter_groups][1][filters][0][value]"); v != "2026-11-21 23:00:00" {
		t.Fatalf("upper bound sent = %q, want the local day end in UTC", v)
	}
	if f, d := got.Get("searchCriteria[sortOrders][0][field]"), got.Get("searchCriteria[sortOrders][0][direction]"); f != reportSortField || d != reportSortDirection {
		t.Fatalf("primary sort = %s %s, want %s %s", f, d, reportSortField, reportSortDirection)
	}
	if f := got.Get("searchCriteria[sortOrders][1][field]"); f != reportSortTiebreak {
		t.Fatalf("tiebreak sort = %q, want %q", f, reportSortTiebreak)
	}
}

// TestSalesSummaryAndAnalysisAgreeOnTheOrderDay is the cross-tool check: an order
// created between 23:00 and 24:00 UTC must land on the same calendar day in
// sales.summary and sales.analysis. Before the wiring, summary sliced created_at
// (UTC day) while analysis used the store timezone, so the two reports of one
// window disagreed by a day.
func TestSalesSummaryAndAnalysisAgreeOnTheOrderDay(t *testing.T) {
	ts := summaryTimezoneServer(t, nil)
	defer ts.Close()
	svc := testService(t, ts.URL)
	ctx := context.Background()

	summary, err := svc.SalesSummaryWindow(ctx, "2026-11-21", "2026-11-22")
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := svc.AnalyzeSalesWindow(ctx, "2026-11-21", "2026-11-22", 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Days) != 1 || len(analysis.ByDay) != 1 {
		t.Fatalf("summary days = %+v, analysis by_day = %+v", summary.Days, analysis.ByDay)
	}
	if summary.Timezone != analysis.Timezone {
		t.Fatalf("timezones disagree: summary %q analysis %q", summary.Timezone, analysis.Timezone)
	}
	if summary.Days[0].Date != analysis.ByDay[0].Date {
		t.Fatalf("the same 23:30Z order lands on %q in summary and %q in analysis",
			summary.Days[0].Date, analysis.ByDay[0].Date)
	}
	if summary.Days[0].Date != "2026-11-22" {
		t.Fatalf("day = %q, want the store's local 2026-11-22", summary.Days[0].Date)
	}
}

// TestSalesSummaryWindowRejectsInvalidDate pins the input validation that moved
// with the window parsing.
func TestSalesSummaryWindowRejectsInvalidDate(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	if _, err := svc.SalesSummaryWindow(context.Background(), "2026-11-21", "yesterday-ish"); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if _, err := svc.SalesSummaryWindow(context.Background(), "", "2026-11-22"); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty from err = %v, want invalid_argument", err)
	}
}

// summaryPagedServer serves a fixed number of orders across the 200-row pages the
// summary scans, optionally reporting total_count.
func summaryPagedServer(t *testing.T, total int, reportTotal bool) *httptest.Server {
	t.Helper()
	perPage := maxOrderLimit
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/rest/default/V1/orders" {
			_, _ = w.Write([]byte(`{"items":[],"total_count":0}`))
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("searchCriteria[currentPage]"))
		if page < 1 {
			page = 1
		}
		from := (page - 1) * perPage
		count := perPage
		if from+count > total {
			count = total - from
		}
		if count < 0 {
			count = 0
		}
		items := make([]string, 0, count)
		for i := 0; i < count; i++ {
			items = append(items, `{"created_at":"2026-11-17 10:00:00","grand_total":1,"total_qty_ordered":1,"order_currency_code":"PLN"}`)
		}
		if reportTotal {
			_, _ = fmt.Fprintf(w, `{"items":[%s],"total_count":%d}`, strings.Join(items, ","), total)
			return
		}
		_, _ = fmt.Fprintf(w, `{"items":[%s]}`, strings.Join(items, ","))
	}))
}

// TestSalesSummaryExactCapIsNotTruncated covers U-54: a window holding exactly
// maxSummaryOrders orders ran out of loop iterations instead of seeing a short
// page, so it claimed truncated=true while excluding nothing.
func TestSalesSummaryExactCapIsNotTruncated(t *testing.T) {
	ts := summaryPagedServer(t, maxSummaryOrders, true)
	defer ts.Close()
	from := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	summary, err := testService(t, ts.URL).SalesSummary(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ScannedOrders != maxSummaryOrders || summary.TotalAvailable != maxSummaryOrders {
		t.Fatalf("scanned = %d total_available = %d, want %d both", summary.ScannedOrders, summary.TotalAvailable, maxSummaryOrders)
	}
	if summary.ExcludedOrders != 0 {
		t.Fatalf("excluded_orders = %d, want 0", summary.ExcludedOrders)
	}
	if summary.Truncated {
		t.Fatalf("truncated = true for a complete %d-order scan", maxSummaryOrders)
	}
}

// TestSalesSummaryUnknownTotalAtCapStaysTruncated is the other half of U-54: when
// the store omits total_count, reaching the cap is not proof of completeness, so
// the summary must keep saying it may be truncated.
func TestSalesSummaryUnknownTotalAtCapStaysTruncated(t *testing.T) {
	ts := summaryPagedServer(t, maxSummaryOrders, false)
	defer ts.Close()
	from := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	summary, err := testService(t, ts.URL).SalesSummary(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ScannedOrders != maxSummaryOrders {
		t.Fatalf("scanned = %d, want %d", summary.ScannedOrders, maxSummaryOrders)
	}
	if !summary.Truncated {
		t.Fatal("truncated = false although total_count was omitted: an unknown total at the cap must stay truncated")
	}
}

// U-31 / GG-02: the single-order read is the only way support can answer "where was
// it sent?" — no tool can read an order address after placement. The projection must
// therefore carry both addresses, the address email and phone, and the shipping
// method, while the order-level customer email stays out.
func TestGetOrderReturnsAddressesForSupport(t *testing.T) {
	// Magento carries the delivery address in extension_attributes.shipping_assignments;
	// the top-level form is kept because a single-store payload may spell it that way.
	// Both shapes must project the same address, so a decoding change cannot quietly
	// empty the field support depends on.
	cases := map[string]string{
		"top-level address": `{"entity_id": 42, "increment_id": "000000042", "status": "processing",
			"state": "processing", "customer_email": "buyer@example.com",
			"shipping_description": "Flat Rate - Fixed",
			"shipping_address": {"firstname": "Anna", "lastname": "Kowalska", "street": ["ul. Testowa 1"],
				"city": "Warszawa", "postcode": "00-001", "country_id": "PL", "telephone": "500100200",
				"email": "anna@example.com"},
			"billing_address": {"firstname": "Anna", "lastname": "Kowalska", "city": "Warszawa",
				"country_id": "PL", "email": "anna@example.com"}}`,
		"shipping assignment address": `{"entity_id": 42, "increment_id": "000000042", "status": "processing",
			"state": "processing", "customer_email": "buyer@example.com",
			"shipping_description": "Flat Rate - Fixed",
			"billing_address": {"firstname": "Anna", "lastname": "Kowalska", "city": "Warszawa",
				"country_id": "PL", "email": "anna@example.com"},
			"extension_attributes": {"shipping_assignments": [{"shipping": {
				"method": "flatrate_flatrate",
				"address": {"firstname": "Anna", "lastname": "Kowalska", "street": ["ul. Testowa 1"],
					"city": "Warszawa", "postcode": "00-001", "country_id": "PL", "telephone": "500100200",
					"email": "anna@example.com"}}}]}}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(payload))
			}))
			defer ts.Close()
			order, err := testService(t, ts.URL).GetOrder(context.Background(), 42)
			if err != nil {
				t.Fatal(err)
			}
			if order.ShippingDescription == "" {
				t.Fatal("shipping_description is empty; support cannot say how the parcel was sent")
			}
			ship := order.ShippingAddress
			if ship == nil {
				t.Fatal("shipping_address is nil; support cannot confirm a delivery without it")
			}
			if ship.Firstname != "Anna" || ship.Lastname != "Kowalska" || ship.City != "Warszawa" ||
				ship.Postcode != "00-001" || ship.CountryID != "PL" {
				t.Fatalf("shipping_address = %+v", ship)
			}
			if ship.Email != "anna@example.com" || ship.Telephone != "500100200" {
				t.Fatalf("contact fields = %q/%q, want the address email and phone", ship.Email, ship.Telephone)
			}
			if order.BillingAddress == nil || order.BillingAddress.City != "Warszawa" {
				t.Fatalf("billing_address = %+v, want the billing address", order.BillingAddress)
			}
			raw, err := json.Marshal(order)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "buyer@example.com") {
				t.Fatalf("order-level customer email leaked into the projection: %s", raw)
			}
		})
	}
}

// F2: Magento's order resource has no top-level shipping_address; the delivery
// address is in extension_attributes.shipping_assignments[].shipping.address.
// Decoding only the top-level field left ShippingAddress permanently nil, so the
// U-31 support answer was still impossible. Removing
// fillShippingAddressFromAssignments makes this test fail.
func TestGetOrderReadsShippingAddressFromAssignments(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"entity_id": 7, "increment_id": "000000007", "status": "processing", "state": "processing",
			"shipping_description": "Flat Rate - Fixed",
			"extension_attributes": {"shipping_assignments": [
				{"stock_id": 1, "shipping": {"address": {
					"firstname": "Anna", "lastname": "Kowalska", "street": ["ul. Testowa 1"],
					"city": "Warszawa", "postcode": "00-001", "country_id": "PL",
					"region": "Mazowieckie", "telephone": "500100200", "email": "anna@example.com"},
					"method": "flatrate"}}
			]}
		}`))
	}))
	defer ts.Close()
	order, err := testService(t, ts.URL).GetOrder(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	ship := order.ShippingAddress
	if ship == nil {
		t.Fatal("shipping_address is nil: the delivery address in extension_attributes.shipping_assignments was not read")
	}
	if ship.Firstname != "Anna" || ship.Lastname != "Kowalska" || ship.City != "Warszawa" ||
		ship.Postcode != "00-001" || ship.CountryID != "PL" {
		t.Fatalf("shipping_address = %+v", ship)
	}
	if len(ship.Street) != 1 || ship.Street[0] != "ul. Testowa 1" {
		t.Fatalf("street = %v, want the delivery street", ship.Street)
	}
	if ship.Telephone != "500100200" || ship.Email != "anna@example.com" {
		t.Fatalf("contact fields = %q/%q, want the address phone and email", ship.Telephone, ship.Email)
	}
	if order.ShippingDescription != "Flat Rate - Fixed" {
		t.Fatalf("shipping_description = %q", order.ShippingDescription)
	}
}

// F2: a top-level shipping_address is still honoured when a build sends one, so
// the projection does not depend on a single Magento shape.
func TestGetOrderPrefersTopLevelShippingAddress(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"entity_id": 8, "increment_id": "000000008",
			"shipping_address": {"city": "Krakow", "country_id": "PL"},
			"extension_attributes": {"shipping_assignments": [
				{"shipping": {"address": {"city": "Warszawa", "country_id": "PL"}}}]}
		}`))
	}))
	defer ts.Close()
	order, err := testService(t, ts.URL).GetOrder(context.Background(), 8)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if order.ShippingAddress == nil || order.ShippingAddress.City != "Krakow" {
		t.Fatalf("shipping_address = %+v, want the top-level address", order.ShippingAddress)
	}
}

// F1/U-31: the fulfilment counters must be on the single-order projection.
// Breaking their mapping (or dropping the fields) makes this test fail; the
// pre-repair TestGetOrderReturnsProjection only asserted sku and qty_ordered.
func TestGetOrderReturnsFulfilmentCounters(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"entity_id": 9, "increment_id": "000000009",
			"items": [{"item_id": 1, "sku": "SKU-1", "name": "Widget", "qty_ordered": 4,
				"qty_shipped": 3, "qty_invoiced": 2, "qty_refunded": 1,
				"price": 10, "row_total": 40}]
		}`))
	}))
	defer ts.Close()
	order, err := testService(t, ts.URL).GetOrder(context.Background(), 9)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if len(order.Items) != 1 {
		t.Fatalf("items = %+v", order.Items)
	}
	item := order.Items[0]
	if item.QtyShipped == nil || *item.QtyShipped != 3 {
		t.Fatalf("qty_shipped = %v, want 3", item.QtyShipped)
	}
	if item.QtyInvoiced == nil || *item.QtyInvoiced != 2 {
		t.Fatalf("qty_invoiced = %v, want 2", item.QtyInvoiced)
	}
	if item.QtyRefunded == nil || *item.QtyRefunded != 1 {
		t.Fatalf("qty_refunded = %v, want 1", item.QtyRefunded)
	}
	if item.QtyOrdered != 4 {
		t.Fatalf("qty_ordered = %v, want 4", item.QtyOrdered)
	}
}
