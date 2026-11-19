// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestProductMedia(t *testing.T) {
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1,"media_type":"image","label":"Front","position":1,"disabled":false,"types":["image","small_image"],"file":"/w/i/widget.jpg"}]`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	media, err := svc.ProductMedia(context.Background(), "SKU-1")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rest/default/V1/products/SKU-1/media" {
		t.Fatalf("path = %q", path)
	}
	if len(media) != 1 || media[0].Label != "Front" || len(media[0].Types) != 2 {
		t.Fatalf("media = %+v", media)
	}
}

func TestStockStatus(t *testing.T) {
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"item_id":9,"product_id":5,"stock_id":1,"qty":12,"is_in_stock":true,"manage_stock":true,"use_config_manage_stock":true,"backorders":0,"min_qty":0}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	status, err := svc.StockStatus(context.Background(), "SKU-1")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rest/default/V1/stockItems/SKU-1" {
		t.Fatalf("path = %q", path)
	}
	if status.SKU != "SKU-1" || status.Qty != 12 || !status.IsInStock {
		t.Fatalf("status = %+v", status)
	}
}

func TestSourceItemsQuery(t *testing.T) {
	var query url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"sku":"SKU-1","source_code":"default","quantity":5,"status":1}],"total_count":1}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	items, err := svc.SourceItems(context.Background(), []string{"SKU-1", "SKU-2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].SourceCode != "default" || items[0].Quantity != 5 {
		t.Fatalf("items = %+v", items)
	}
	if got := query.Get("searchCriteria[filter_groups][0][filters][0][value]"); got != "SKU-1,SKU-2" {
		t.Fatalf("sku filter = %q", got)
	}
	if got := query.Get("searchCriteria[filter_groups][0][filters][0][condition_type]"); got != "in" {
		t.Fatalf("condition = %q", got)
	}
	if got := query.Get("searchCriteria[pageSize]"); got != "100" {
		t.Fatalf("pageSize = %q", got)
	}
}

func TestMediaAndInventoryValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if _, err := svc.ProductMedia(ctx, "bad sku!"); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("media sku err = %v", err)
	}
	if _, err := svc.StockStatus(ctx, ""); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("stock sku err = %v", err)
	}
	if _, err := svc.SourceItems(ctx, nil); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty skus err = %v", err)
	}
	tooMany := make([]string, 21)
	for i := range tooMany {
		tooMany[i] = "SKU"
	}
	if _, err := svc.SourceItems(ctx, tooMany); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("too many skus err = %v", err)
	}
}
