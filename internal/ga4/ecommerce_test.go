// SPDX-License-Identifier: Apache-2.0

package ga4

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestEcommerceOverviewAbsentCapability(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "eventName") {
			_, _ = w.Write([]byte(`{"rows":[],"metricHeaders":[{"name":"eventCount"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"rows":[],"metricHeaders":[{"name":"ecommercePurchases"}],"metricAggregations":[]}`))
	}))
	defer server.Close()
	svc, err := NewService(Config{BaseURL: server.URL, PropertyID: "123456", TokenSource: StaticToken("t")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EcommerceOverview(context.Background(), "", ""); errs.CodeOf(err) != errs.CodeEcommerceNotTracked {
		t.Fatalf("err = %v, want ecommerce_not_tracked", err)
	}
}

func TestEcommerceOverviewTracked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "eventName"):
			_, _ = w.Write([]byte(`{"rows":[{"dimensionValues":[{"value":"purchase"}],"metricValues":[{"value":"3"}]}],
				"metricHeaders":[{"name":"eventCount"}],
				"metadata":{"currencyCode":"USD","timeZone":"America/Los_Angeles"}}`))
		case strings.Contains(string(body), "ecommercePurchases"):
			_, _ = w.Write([]byte(`{"rows":[{"dimensionValues":[{"value":"20261117"}],"metricValues":[{"value":"3"},{"value":"150.00"},{"value":"50.00"}]}],
				"metricHeaders":[{"name":"ecommercePurchases"},{"name":"purchaseRevenue"},{"name":"averagePurchaseRevenue"}],
				"totals":[{"metricValues":[{"value":"3"}]}],
				"metadata":{"currencyCode":"USD","timeZone":"America/Los_Angeles"}}`))
		default:
			_, _ = w.Write([]byte(`{"rows":[]}`))
		}
	}))
	defer server.Close()
	svc, err := NewService(Config{BaseURL: server.URL, PropertyID: "123456", TokenSource: StaticToken("t")})
	if err != nil {
		t.Fatal(err)
	}
	overview, err := svc.EcommerceOverview(context.Background(), "2026-11-17", "2026-11-18")
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Rows) != 1 || overview.Currency != "USD" || overview.TimeZone != "America/Los_Angeles" {
		raw, _ := json.Marshal(overview)
		t.Fatalf("overview = %s", raw)
	}
	if overview.Rows[0].Metrics["purchaseRevenue"] != 150 {
		t.Fatalf("revenue = %v", overview.Rows[0].Metrics)
	}
}

func TestTopProductsTracked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "eventName"):
			_, _ = w.Write([]byte(`{"rows":[{"dimensionValues":[{"value":"purchase"}],"metricValues":[{"value":"1"}]}],"metricHeaders":[{"name":"eventCount"}],"metadata":{"currencyCode":"USD","timeZone":"America/Los_Angeles"}}`))
		case strings.Contains(string(body), "itemId"):
			_, _ = w.Write([]byte(`{"rows":[{"dimensionValues":[{"value":"SKU-1"}],"metricValues":[{"value":"10"},{"value":"3"},{"value":"1"},{"value":"99.50"}]}],"metricHeaders":[{"name":"itemsViewed"},{"name":"itemsAddedToCart"},{"name":"itemsPurchased"},{"name":"itemRevenue"}],"metadata":{"currencyCode":"USD","timeZone":"America/Los_Angeles"}}`))
		default:
			_, _ = w.Write([]byte(`{"rows":[]}`))
		}
	}))
	defer server.Close()
	svc, err := NewService(Config{BaseURL: server.URL, PropertyID: "123456", TokenSource: StaticToken("t")})
	if err != nil {
		t.Fatal(err)
	}
	perf, err := svc.TopProducts(context.Background(), "2026-11-17", "2026-11-18", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(perf.Products) != 1 || perf.Products[0].ItemID != "SKU-1" || perf.Products[0].ItemRevenue != 99.5 {
		t.Fatalf("products = %+v", perf.Products)
	}
}
