// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestSearchWithPriceAndCreatedFilters(t *testing.T) {
	var query url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"total_count":0}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	_, _, err := svc.Search(context.Background(), ProductQuery{
		PriceFrom: "10.5", PriceTo: "99.99",
		CreatedFrom: "2026-11-01", CreatedTo: "2026-12-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	var fields []string
	for i := 0; i < 8; i++ {
		field := query.Get("searchCriteria[filter_groups][" + strconv.Itoa(i) + "][filters][0][field]")
		if field == "" {
			continue
		}
		fields = append(fields, field+"="+query.Get("searchCriteria[filter_groups]["+strconv.Itoa(i)+"][filters][0][value]"))
	}
	joined := strings.Join(fields, " ")
	for _, want := range []string{"price=10.5", "price=99.99", "created_at=2026-11-01 00:00:00", "created_at=2026-12-01 00:00:00"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("filters %q do not contain %q", joined, want)
		}
	}
}

func TestSearchPriceValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	for _, q := range []ProductQuery{
		{PriceFrom: "10,5"},
		{PriceTo: "-1"},
		{PriceTo: "1.23456"},
		{CreatedFrom: "yesterday"},
		{CreatedFrom: "2026-12-01", CreatedTo: "2026-11-01"},
	} {
		if _, _, err := svc.Search(ctx, q); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("query %+v err = %v", q, err)
		}
	}
}

func TestBatchGet(t *testing.T) {
	var query url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":1,"sku":"SKU-1","name":"One"},{"id":2,"sku":"SKU-2","name":"Two"}]}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	products, err := svc.BatchGet(context.Background(), []string{"SKU-1", "SKU-2", "SKU-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 2 {
		t.Fatalf("products = %+v", products)
	}
	if got := query.Get("searchCriteria[filter_groups][0][filters][0][value]"); got != "SKU-1,SKU-2" {
		t.Fatalf("in-filter = %q", got)
	}
	if got := query.Get("searchCriteria[filter_groups][0][filters][0][condition_type]"); got != "in" {
		t.Fatalf("condition = %q", got)
	}
	if got := query.Get("searchCriteria[pageSize]"); got != "2" {
		t.Fatalf("pageSize = %q", got)
	}
}

func TestBatchGetValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if _, err := svc.BatchGet(ctx, nil); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty err = %v", err)
	}
	if _, err := svc.BatchGet(ctx, []string{"bad sku!"}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("sku err = %v", err)
	}
	tooMany := make([]string, maxBatchGetSKUs+1)
	for i := range tooMany {
		tooMany[i] = "SKU"
	}
	if _, err := svc.BatchGet(ctx, tooMany); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("ceiling err = %v", err)
	}
}
