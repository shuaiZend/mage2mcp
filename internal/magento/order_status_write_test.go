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

func TestAddOrderComment(t *testing.T) {
	var method, path string
	var body orderCommentBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	if err := svc.AddOrderComment(context.Background(), 42, "Paid by wire", "processing", true); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/rest/default/V1/orders/42/comments" {
		t.Fatalf("request = %s %s", method, path)
	}
	if body.StatusHistory.Comment != "Paid by wire" || body.StatusHistory.Status != "processing" {
		t.Fatalf("body = %+v", body)
	}
	if body.StatusHistory.IsCustomerNotified != 1 || body.StatusHistory.EntityName != "order" {
		t.Fatalf("body = %+v", body)
	}
}

func TestOrderHoldAndUnhold(t *testing.T) {
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	if err := svc.HoldOrder(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if err := svc.UnholdOrder(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /rest/default/V1/orders/42/hold", "POST /rest/default/V1/orders/42/unhold"}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %v", paths)
		}
	}
}

func TestOrderStatusWriteValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if err := svc.AddOrderComment(ctx, 0, "x", "", false); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("order id err = %v", err)
	}
	if err := svc.AddOrderComment(ctx, 42, "", "", false); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty comment err = %v", err)
	}
	if err := svc.AddOrderComment(ctx, 42, "bad\x00comment", "", false); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("control char err = %v", err)
	}
	if err := svc.AddOrderComment(ctx, 42, "ok", "NOT A STATUS", false); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("status err = %v", err)
	}
	if err := svc.HoldOrder(ctx, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("hold err = %v", err)
	}
	if err := svc.UnholdOrder(ctx, -1); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("unhold err = %v", err)
	}
}

func TestCancelOrder(t *testing.T) {
	var method, path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	if err := svc.CancelOrder(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/rest/default/V1/orders/42/cancel" {
		t.Fatalf("request = %s %s", method, path)
	}
	if err := svc.CancelOrder(context.Background(), 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("validation err = %v", err)
	}
}
