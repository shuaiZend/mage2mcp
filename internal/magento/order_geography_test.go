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
		pages = append(pages, r.URL.Query().Get("searchCriteria[currentPage]"))
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
}
