// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestRemoveChild(t *testing.T) {
	var method, path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	if err := svc.RemoveChild(context.Background(), "PHONE-01", "PHONE-01-BLK"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || path != "/rest/default/V1/configurable-products/PHONE-01/children/PHONE-01-BLK" {
		t.Fatalf("request = %s %s", method, path)
	}
	if err := svc.RemoveChild(context.Background(), "SAME", "SAME"); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("self unlink err = %v", err)
	}
}

func TestUnassignSourceItems(t *testing.T) {
	var method, path string
	var body struct {
		SourceItems []struct {
			SKU        string `json:"sku"`
			SourceCode string `json:"source_code"`
		} `json:"sourceItems"`
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	err := svc.UnassignSourceItems(context.Background(), []SourceItemRef{
		{SKU: "SKU-1", SourceCode: "default"},
		{SKU: "SKU-2", SourceCode: "warehouse"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// MSI delete endpoints are POSTs with a -delete suffix; DELETE is not a route.
	if method != http.MethodPost || path != "/rest/default/V1/inventory/source-items-delete" {
		t.Fatalf("request = %s %s", method, path)
	}
	if len(body.SourceItems) != 2 || body.SourceItems[1].SourceCode != "warehouse" {
		t.Fatalf("body = %+v", body)
	}
}

func TestUnassignSourceItemsValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if err := svc.UnassignSourceItems(ctx, nil); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty err = %v", err)
	}
	if err := svc.UnassignSourceItems(ctx, []SourceItemRef{{SKU: "bad sku!", SourceCode: "default"}}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("sku err = %v", err)
	}
	if err := svc.UnassignSourceItems(ctx, []SourceItemRef{{SKU: "SKU-1", SourceCode: "BAD SOURCE"}}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("source err = %v", err)
	}
	dup := []SourceItemRef{{SKU: "SKU-1", SourceCode: "default"}, {SKU: "SKU-1", SourceCode: "default"}}
	if err := svc.UnassignSourceItems(ctx, dup); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("duplicate err = %v", err)
	}
}
