// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

// captureFake answers the invoice pre-flight read and the capture POST.
type captureFake struct {
	mu          sync.Mutex
	requests    []string
	state       int
	captureCode int // HTTP status for the capture POST (0 = success)
}

func (f *captureFake) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		state := f.state
		status := f.captureCode
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/default/V1/invoices/"):
			id := strings.TrimPrefix(r.URL.Path, "/rest/default/V1/invoices/")
			_, _ = fmt.Fprintf(w, `{"entity_id":%s,"increment_id":"0000000%s","order_id":42,"state":%d,`+
				`"grand_total":129.99,"order_currency_code":"USD"}`, id, id, state)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/invoices/9/capture":
			if status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"message":"The payment gateway did not answer."}`)
				return
			}
			// A real capture changes the invoice state; the fake does too, so a
			// repeat without the ledger would hit the paid pre-flight.
			f.mu.Lock()
			f.state = invoiceStatePaid
			f.mu.Unlock()
			_, _ = io.WriteString(w, `true`)
		default:
			http.NotFound(w, r)
		}
	}
}

// TestCaptureInvoice proves MAG-01: the invoice is read first, an already-paid
// invoice is a conflict with no capture POST, and a capture is recorded.
func TestCaptureInvoice(t *testing.T) {
	t.Run("captures an open invoice", func(t *testing.T) {
		fake := &captureFake{state: 1}
		ts := httptest.NewServer(fake.handler())
		defer ts.Close()
		ledger := newFakeLedger()
		useLedger(t, ledger)
		svc := testService(t, ts.URL)

		result, err := svc.CaptureInvoice(context.Background(), CaptureInvoiceInput{InvoiceID: 9, IdempotencyKey: "capture-9"})
		if err != nil {
			t.Fatalf("CaptureInvoice: %v", err)
		}
		if !result.Captured || result.InvoiceID != 9 || result.OperationID == "" {
			t.Fatalf("result = %+v", result)
		}
		if posts := postsIn(fake.requests); posts != 1 {
			t.Fatalf("POSTs = %d, want 1", posts)
		}
		row := ledger.row(result.OperationID)
		if row == nil || row.state != storage.OpSucceeded {
			t.Fatalf("ledger row = %+v, want succeeded", row)
		}
		if row.amountMinor == nil || *row.amountMinor != 12999 || row.currency != "USD" {
			t.Fatalf("ledger amount = %v %s, want 12999 USD", row.amountMinor, row.currency)
		}
	})

	t.Run("refuses an already paid invoice", func(t *testing.T) {
		fake := &captureFake{state: 2}
		ts := httptest.NewServer(fake.handler())
		defer ts.Close()
		ledger := newFakeLedger()
		useLedger(t, ledger)
		svc := testService(t, ts.URL)

		_, err := svc.CaptureInvoice(context.Background(), CaptureInvoiceInput{InvoiceID: 9})
		if errs.CodeOf(err) != errs.CodeConflict {
			t.Fatalf("err = %v, want conflict", err)
		}
		if posts := postsIn(fake.requests); posts != 0 {
			t.Fatalf("POSTs = %d, want 0 (no second capture)", posts)
		}
		// The rejection is recorded: a repeat with the same key returns it instead
		// of executing again.
		if c := ledger.counts(); c.begins != 1 || c.fails != 1 {
			t.Fatalf("ledger begins=%d fails=%d, want 1/1", c.begins, c.fails)
		}
	})

	t.Run("refuses a canceled invoice", func(t *testing.T) {
		fake := &captureFake{state: 3}
		ts := httptest.NewServer(fake.handler())
		defer ts.Close()
		useLedger(t, newFakeLedger())
		svc := testService(t, ts.URL)
		if _, err := svc.CaptureInvoice(context.Background(), CaptureInvoiceInput{InvoiceID: 9}); errs.CodeOf(err) != errs.CodeConflict {
			t.Fatalf("err = %v, want conflict", err)
		}
		if posts := postsIn(fake.requests); posts != 0 {
			t.Fatalf("POSTs = %d, want 0", posts)
		}
	})

	t.Run("validation", func(t *testing.T) {
		svc := testService(t, "https://shop.example.com")
		if _, err := svc.CaptureInvoice(context.Background(), CaptureInvoiceInput{InvoiceID: 0}); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("err = %v, want invalid_argument", err)
		}
	})
}

// TestCaptureInvoiceTimeoutIsUnknown proves an ambiguous capture is recorded as
// unknown, returned as non-retryable, and not repeated.
func TestCaptureInvoiceTimeoutIsUnknown(t *testing.T) {
	fake := &captureFake{state: 1, captureCode: http.StatusRequestTimeout}
	ts := httptest.NewServer(fake.handler())
	defer ts.Close()
	ledger := newFakeLedger()
	useLedger(t, ledger)
	svc := testService(t, ts.URL)

	_, err := svc.CaptureInvoice(context.Background(), CaptureInvoiceInput{InvoiceID: 9, IdempotencyKey: "capture-timeout"})
	if errs.CodeOf(err) != errs.CodeOperationUnknown {
		t.Fatalf("err = %v, want operation_unknown", err)
	}
	if errs.RetryableError(err) {
		t.Fatal("an unknown capture must not be retryable")
	}
	if posts := postsIn(fake.requests); posts != 1 {
		t.Fatalf("POSTs = %d, want 1 (no automatic retry)", posts)
	}
	if c := ledger.counts(); c.executes != 1 || c.unknowns != 1 {
		t.Fatalf("executes=%d unknowns=%d, want 1/1", c.executes, c.unknowns)
	}
	// A second call with the same key refuses to execute again.
	_, err = svc.CaptureInvoice(context.Background(), CaptureInvoiceInput{InvoiceID: 9, IdempotencyKey: "capture-timeout"})
	if errs.CodeOf(err) != errs.CodeOperationUnknown {
		t.Fatalf("second err = %v, want operation_unknown", err)
	}
	if posts := postsIn(fake.requests); posts != 1 {
		t.Fatalf("POSTs after the second call = %d, want 1", posts)
	}
}

// TestCaptureInvoiceReplays proves a repeated key replays the recorded capture
// even though the invoice is paid by then (the ledger is consulted before the
// terminal-state pre-flight).
func TestCaptureInvoiceReplays(t *testing.T) {
	fake := &captureFake{state: 1}
	ts := httptest.NewServer(fake.handler())
	defer ts.Close()
	useLedger(t, newFakeLedger())
	svc := testService(t, ts.URL)
	in := CaptureInvoiceInput{InvoiceID: 9, IdempotencyKey: "capture-replay"}
	first, err := svc.CaptureInvoice(context.Background(), in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.CaptureInvoice(context.Background(), in)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !second.Replayed || second.OperationID != first.OperationID {
		t.Fatalf("second = %+v, want a replay", second)
	}
	if posts := postsIn(fake.requests); posts != 1 {
		t.Fatalf("POSTs = %d, want 1", posts)
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

// TestCaptureInvoiceKeyReuseWithDifferentArgs proves the idempotency key cannot
// be reused for a different invoice.
func TestCaptureInvoiceKeyReuseWithDifferentArgs(t *testing.T) {
	fake := &captureFake{state: 1}
	ts := httptest.NewServer(fake.handler())
	defer ts.Close()
	useLedger(t, newFakeLedger())
	svc := testService(t, ts.URL)
	if _, err := svc.CaptureInvoice(context.Background(), CaptureInvoiceInput{InvoiceID: 9, IdempotencyKey: "capture-reuse"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	postsAfterFirst := postsIn(fake.requests)
	_, err := svc.CaptureInvoice(context.Background(), CaptureInvoiceInput{InvoiceID: 10, IdempotencyKey: "capture-reuse"})
	if errs.CodeOf(err) != errs.CodeIdempotencyKeyReuse {
		t.Fatalf("err = %v, want idempotency_key_reuse", err)
	}
	if posts := postsIn(fake.requests); posts != postsAfterFirst {
		t.Fatalf("POSTs = %d, want %d (reuse must not mutate)", posts, postsAfterFirst)
	}
}
