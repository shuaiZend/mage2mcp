//go:build qa

package qa

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/shuaiZend/mage2mcp/internal/storage"
)

// invoiceFake answers the invoice read and delegates the capture POST.
func invoiceFake(t *testing.T, capture func(w http.ResponseWriter, r *http.Request)) *fakeUpstream {
	t.Helper()
	return newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/invoices/7":
			writeJSON(w, http.StatusOK, map[string]any{
				"entity_id": 7, "state": 1, "grand_total": 10.0, "order_currency_code": "USD",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/invoices/7/capture":
			capture(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

// TestMoneyPathAmbiguityEndsUnknown guards REL-02 / MAG-01 / MAG-18: a network
// drop and a Magento 500 on a money path are ambiguous, so the operation ends
// UNKNOWN, is recorded as unknown in the ledger, is not retryable, and a second
// attempt does not re-issue the mutation.
func TestMoneyPathAmbiguityEndsUnknown(t *testing.T) {
	cases := []struct {
		name    string
		capture func(w http.ResponseWriter, r *http.Request)
	}{
		{
			name: "network drop mid-request",
			capture: func(w http.ResponseWriter, _ *http.Request) {
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					panic("qa: response writer is not a hijacker")
				}
				conn, _, err := hijacker.Hijack()
				if err == nil {
					_ = conn.Close()
				}
			},
		},
		{
			name: "magento 500",
			capture: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "boom"})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := invoiceFake(t, tc.capture)
			inst := uniqueInstance(t, "money")
			f := newFixture(t, fixtureOptions{upstreams: map[string]string{inst: up.url()}, writes: true})
			agent := f.session(t, "agent-1")
			args := map[string]any{"instance": inst, "invoice_id": 7}

			approval := f.requireConfirmation(t, agent, "magento.invoice.capture", args)
			f.approve(t, approval, "human-1")
			res := f.call(t, agent, "magento.invoice.capture", withConfirmation(args, approval))
			res.mustFail(t, "operation_unknown")
			if _, retryable := res.Structured["retryable"]; retryable {
				t.Fatal("qa: an ambiguous money outcome must not be marked retryable")
			}
			opID := res.detail("operation_id")
			if opID == "" {
				t.Fatalf("qa: operation_unknown carried no operation_id: %s", res.Text)
			}
			record, err := f.ledger.Get(context.Background(), opID)
			if err != nil {
				t.Fatalf("qa: ledger get %s: %v", opID, err)
			}
			if record.State != storage.OpUnknown {
				t.Fatalf("qa: ledger state = %q, want %q (FAILED would be unsafe to retry)", record.State, storage.OpUnknown)
			}
			if got := up.count(http.MethodPost, "/rest/default/V1/invoices/7/capture"); got != 1 {
				t.Fatalf("qa: capture POSTs = %d, want 1", got)
			}

			// A second, separately approved attempt must not re-issue the capture.
			second := f.requireConfirmation(t, agent, "magento.invoice.capture", args)
			f.approve(t, second, "human-1")
			again := f.call(t, agent, "magento.invoice.capture", withConfirmation(args, second))
			again.mustFail(t, "operation_unknown")
			if got := up.count(http.MethodPost, "/rest/default/V1/invoices/7/capture"); got != 1 {
				t.Fatalf("qa: a second attempt re-issued the capture (%d POSTs); it must be blocked as unknown", got)
			}
		})
	}
}

// TestCaptureTimeoutEndsUnknownAndDoesNotDoubleCapture guards MAG-01 / REL-02:
// a capture that times out is UNKNOWN, non-retryable, and a second attempt does
// not double-capture.
func TestCaptureTimeoutEndsUnknownAndDoesNotDoubleCapture(t *testing.T) {
	up := invoiceFake(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		writeJSON(w, http.StatusOK, map[string]any{"captured": true})
	})
	inst := uniqueInstance(t, "capture-timeout")
	f := newFixture(t, fixtureOptions{
		upstreams:     map[string]string{inst: up.url()},
		writes:        true,
		clientTimeout: 150 * time.Millisecond,
	})
	agent := f.session(t, "agent-1")
	args := map[string]any{"instance": inst, "invoice_id": 7}

	approval := f.requireConfirmation(t, agent, "magento.invoice.capture", args)
	f.approve(t, approval, "human-1")
	res := f.call(t, agent, "magento.invoice.capture", withConfirmation(args, approval))
	res.mustFail(t, "operation_unknown")
	if _, retryable := res.Structured["retryable"]; retryable {
		t.Fatal("qa: a timed-out capture must not be retryable")
	}
	opID := res.detail("operation_id")
	record, err := f.ledger.Get(context.Background(), opID)
	if err != nil {
		t.Fatalf("qa: ledger get: %v", err)
	}
	if record.State != storage.OpUnknown {
		t.Fatalf("qa: ledger state = %q, want unknown", record.State)
	}
	if got := up.count(http.MethodPost, "/rest/default/V1/invoices/7/capture"); got != 1 {
		t.Fatalf("qa: capture POSTs = %d, want 1", got)
	}

	second := f.requireConfirmation(t, agent, "magento.invoice.capture", args)
	f.approve(t, second, "human-1")
	again := f.call(t, agent, "magento.invoice.capture", withConfirmation(args, second))
	again.mustFail(t, "operation_unknown")
	if got := up.count(http.MethodPost, "/rest/default/V1/invoices/7/capture"); got != 1 {
		t.Fatalf("qa: a timed-out capture was retried (%d POSTs)", got)
	}
}
