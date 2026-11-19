// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestCaptureInvoice(t *testing.T) {
	var method, path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	if err := svc.CaptureInvoice(context.Background(), 9); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/rest/default/V1/invoices/9/capture" {
		t.Fatalf("request = %s %s", method, path)
	}
	if err := svc.CaptureInvoice(context.Background(), 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("validation err = %v", err)
	}
}

func TestSendOrderEmail(t *testing.T) {
	var method, path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	if err := svc.SendOrderEmail(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/rest/default/V1/orders/42/emails" {
		t.Fatalf("request = %s %s", method, path)
	}
	if err := svc.SendOrderEmail(context.Background(), -1); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("validation err = %v", err)
	}
}
