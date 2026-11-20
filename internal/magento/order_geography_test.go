// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"net/http"
	"net/http/httptest"
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
