// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestOrderGeography(t *testing.T) {
	var pages []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("searchCriteria[filter_groups][0][filters][0][condition_type]") != "gteq" ||
			q.Get("searchCriteria[filter_groups][1][filters][0][condition_type]") != "lt" {
			t.Errorf("filters not split into separate groups: %v", q)
		}
		pages = append(pages, q.Get("searchCriteria[currentPage]"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("searchCriteria[currentPage]") == "1" {
			_, _ = w.Write([]byte(`{"total_count":3,"items":[
				{"grand_total":100,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"US"}}}]}},
				{"grand_total":50,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"US"}}}]}},
				{"grand_total":25,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"DE"}}}]}}
			]}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	from, _ := time.Parse("2006-01-02", "2026-10-21")
	to, _ := time.Parse("2006-01-02", "2026-11-20")
	geo, err := svc.OrderGeography(context.Background(), from, to, 100)
	if err != nil {
		t.Fatal(err)
	}
	if geo.TotalOrders != 3 || geo.TotalRevenue != 175 || geo.Truncated || geo.TotalAvailable != 3 {
		t.Fatalf("geo = %+v", geo)
	}
	if len(geo.Countries) != 2 || geo.Countries[0].CountryID != "US" || geo.Countries[0].Orders != 2 || geo.Countries[0].Revenue != 150 {
		t.Fatalf("countries = %+v", geo.Countries)
	}
	if len(pages) != 1 || pages[0] != "1" {
		t.Fatalf("pages = %v", pages)
	}
}

func TestOrderGeographyValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	from := time.Now()
	to := from.Add(-time.Hour)
	if _, err := svc.OrderGeography(ctx, from, to, 10); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("window err = %v", err)
	}
	if _, err := svc.OrderGeography(ctx, from, from.Add(time.Hour), 20000); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("max err = %v", err)
	}
}

func TestOrderGeographyTruncation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":10,"items":[
			{"grand_total":10,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"US"}}}]}},
			{"grand_total":20,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"US"}}}]}},
			{"grand_total":30,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"US"}}}]}}
		]}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	from, _ := time.Parse("2006-01-02", "2026-10-21")
	to, _ := time.Parse("2006-01-02", "2026-11-20")
	geo, err := svc.OrderGeography(context.Background(), from, to, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !geo.Truncated || geo.OrdersScanned != 2 || geo.TotalAvailable != 10 {
		t.Fatalf("geo = %+v", geo)
	}
	// The cap excluded 8 orders and the response must say so.
	if geo.UnscannedOrders != 8 {
		t.Fatalf("unscanned_orders = %d, want 8", geo.UnscannedOrders)
	}
	if geo.DuplicatesSkipped != 0 {
		t.Fatalf("duplicates_skipped = %d, want 0", geo.DuplicatesSkipped)
	}
}

// TestOrderGeographyCompleteScanReportsNoRemainder pins the other side of the
// completeness contract: a scan that covered the window reports zero unscanned.
func TestOrderGeographyCompleteScanReportsNoRemainder(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":3,"items":[
			{"entity_id":1,"grand_total":10,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"US"}}}]}},
			{"entity_id":2,"grand_total":20,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"DE"}}}]}},
			{"entity_id":3,"grand_total":30,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"FR"}}}]}}
		]}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	from, _ := time.Parse("2006-01-02", "2026-10-21")
	to, _ := time.Parse("2006-01-02", "2026-11-20")
	geo, err := svc.OrderGeography(context.Background(), from, to, 100)
	if err != nil {
		t.Fatal(err)
	}
	if geo.Truncated || geo.UnscannedOrders != 0 {
		t.Fatalf("truncated = %v unscanned = %d, want a complete scan", geo.Truncated, geo.UnscannedOrders)
	}
	if geo.OrdersScanned != 3 || geo.TotalAvailable != 3 || geo.TotalRevenue != 60 {
		t.Fatalf("geo = %+v", geo)
	}
}

// TestOrderGeographyDropsDuplicateOrders proves an order repeated by a shifted
// page boundary is counted once and the repeat is reported.
func TestOrderGeographyDropsDuplicateOrders(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":2,"items":[
			{"entity_id":1,"grand_total":10,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"US"}}}]}},
			{"entity_id":1,"grand_total":10,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"US"}}}]}},
			{"entity_id":2,"grand_total":20,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"DE"}}}]}}
		]}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	from, _ := time.Parse("2006-01-02", "2026-10-21")
	to, _ := time.Parse("2006-01-02", "2026-11-20")
	geo, err := svc.OrderGeography(context.Background(), from, to, 100)
	if err != nil {
		t.Fatal(err)
	}
	if geo.OrdersScanned != 2 || geo.TotalRevenue != 30 {
		t.Fatalf("geo = %+v, want 2 orders and revenue 30", geo)
	}
	if geo.DuplicatesSkipped != 1 {
		t.Fatalf("duplicates_skipped = %d, want 1", geo.DuplicatesSkipped)
	}
}

// --- report-caliber fixes (t11 / G6) -------------------------------------

// TestOrderGeographyScansOldestFirst pins the retention end shared with
// sales.analysis: both scans run oldest first, so a max_orders cap keeps the
// same orders in both tools and two capped reports of one window stay
// comparable. It also pins the paging metadata that tells a caller which end was
// kept.
func TestOrderGeographyScansOldestFirst(t *testing.T) {
	var sorts []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		sorts = append(sorts,
			q.Get("searchCriteria[sortOrders][0][field]")+" "+q.Get("searchCriteria[sortOrders][0][direction]"),
			q.Get("searchCriteria[sortOrders][1][field]")+" "+q.Get("searchCriteria[sortOrders][1][direction]"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":3,"items":[
			{"entity_id":1,"grand_total":10,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"PL"}}}]}},
			{"entity_id":2,"grand_total":20,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"PL"}}}]}},
			{"entity_id":3,"grand_total":30,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"PL"}}}]}}
		]}`))
	}))
	defer ts.Close()
	from, _ := time.Parse("2006-01-02", "2026-10-21")
	to, _ := time.Parse("2006-01-02", "2026-11-20")
	geo, err := testService(t, ts.URL).OrderGeography(context.Background(), from, to, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(sorts) < 2 || sorts[0] != "created_at asc" || sorts[1] != "entity_id asc" {
		t.Fatalf("sort orders = %v, want created_at asc + entity_id asc (oldest first, like sales.analysis)", sorts)
	}
	// The cap keeps the first (oldest) row; a descending scan would keep 30.
	if geo.TotalRevenue != 10 {
		t.Fatalf("total_revenue = %v, want 10 (the oldest order, not the newest)", geo.TotalRevenue)
	}
	if !geo.Truncated || geo.UnscannedOrders != 2 {
		t.Fatalf("truncated = %v unscanned = %d", geo.Truncated, geo.UnscannedOrders)
	}
	if geo.Paging.Method != analysisPagingMethod || !strings.HasPrefix(geo.Paging.Caveat, "offset paging") {
		t.Fatalf("paging = %+v, want the same method and a caveat as sales.analysis", geo.Paging)
	}
	if geo.Paging.OrderBy != analysisPagingOrder {
		t.Fatalf("paging.order_by = %q, want %q (one shared retention end)", geo.Paging.OrderBy, analysisPagingOrder)
	}
	if geo.Paging.SnapshotConsistent {
		t.Fatal("offset paging must not claim snapshot consistency")
	}
}

// TestOrderGeographyLabelsMissingCountry proves the UNKNOWN sentinel is produced
// for an order without a shipping address, which is what the country_id
// description promises callers.
func TestOrderGeographyLabelsMissingCountry(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":2,"items":[
			{"entity_id":1,"grand_total":10,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"PL"}}}]}},
			{"entity_id":2,"grand_total":5,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":null}}]}}
		]}`))
	}))
	defer ts.Close()
	from, _ := time.Parse("2006-01-02", "2026-10-21")
	to, _ := time.Parse("2006-01-02", "2026-11-20")
	geo, err := testService(t, ts.URL).OrderGeography(context.Background(), from, to, 100)
	if err != nil {
		t.Fatal(err)
	}
	unknown, ok := geo.Countries[0], false
	for _, country := range geo.Countries {
		if country.CountryID == "UNKNOWN" {
			unknown, ok = country, true
		}
	}
	if !ok || unknown.Revenue != 5 || unknown.Orders != 1 {
		t.Fatalf("countries = %+v, want an UNKNOWN bucket with revenue 5", geo.Countries)
	}
}

// TestOrderGeographyRoundsMoneyToCents pins the money serialization.
func TestOrderGeographyRoundsMoneyToCents(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":2,"items":[
			{"entity_id":1,"grand_total":0.1,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"PL"}}}]}},
			{"entity_id":2,"grand_total":0.2,"extension_attributes":{"shipping_assignments":[{"shipping":{"address":{"country_id":"PL"}}}]}}
		]}`))
	}))
	defer ts.Close()
	from, _ := time.Parse("2006-01-02", "2026-10-21")
	to, _ := time.Parse("2006-01-02", "2026-11-20")
	geo, err := testService(t, ts.URL).OrderGeography(context.Background(), from, to, 100)
	if err != nil {
		t.Fatal(err)
	}
	if geo.TotalRevenue != 0.3 {
		t.Fatalf("total_revenue = %v, want 0.3 with no floating-point tail", geo.TotalRevenue)
	}
	if geo.Countries[0].Revenue != 0.3 {
		t.Fatalf("country revenue = %v, want 0.3", geo.Countries[0].Revenue)
	}
}
