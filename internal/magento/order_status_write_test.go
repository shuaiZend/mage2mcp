// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/storage"
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

// TestCancelOrder proves the cancellation is guarded: the order is read first,
// an already-canceled order is a conflict with no request, and a success is
// recorded in the ledger.
func TestCancelOrder(t *testing.T) {
	t.Run("cancels an open order", func(t *testing.T) {
		var paths []string
		state := "processing"
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.Method+" "+r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/orders/42":
				_, _ = io.WriteString(w, `{"entity_id":42,"increment_id":"000000042","state":"`+state+`","grand_total":19.98,"order_currency_code":"USD"}`)
			case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/orders/42/cancel":
				state = "canceled"
				_, _ = io.WriteString(w, `true`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()
		ledger := newFakeLedger()
		useLedger(t, ledger)
		svc := testService(t, ts.URL)

		result, err := svc.CancelOrder(context.Background(), CancelOrderInput{OrderID: 42, IdempotencyKey: "cancel-42"})
		if err != nil {
			t.Fatalf("CancelOrder: %v", err)
		}
		if !result.Canceled || result.OperationID == "" {
			t.Fatalf("result = %+v", result)
		}
		if !strings.Contains(strings.Join(paths, ","), "POST /rest/default/V1/orders/42/cancel") {
			t.Fatalf("paths = %v", paths)
		}
		row := ledger.row(result.OperationID)
		if row == nil || row.state != storage.OpSucceeded {
			t.Fatalf("ledger row = %+v, want succeeded", row)
		}
		if row.amountMinor == nil || *row.amountMinor != 1998 {
			t.Fatalf("ledger amount = %v, want 1998", row.amountMinor)
		}
	})

	t.Run("refuses an already canceled order", func(t *testing.T) {
		var posts int
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodPost {
				posts++
			}
			_, _ = io.WriteString(w, `{"entity_id":42,"state":"canceled"}`)
		}))
		defer ts.Close()
		useLedger(t, newFakeLedger())
		svc := testService(t, ts.URL)
		_, err := svc.CancelOrder(context.Background(), CancelOrderInput{OrderID: 42})
		if errs.CodeOf(err) != errs.CodeConflict {
			t.Fatalf("err = %v, want conflict", err)
		}
		if posts != 0 {
			t.Fatalf("POSTs = %d, want 0", posts)
		}
	})

	t.Run("validation", func(t *testing.T) {
		svc := testService(t, "https://shop.example.com")
		if _, err := svc.CancelOrder(context.Background(), CancelOrderInput{OrderID: 0}); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("validation err = %v", err)
		}
	})
}

// TestCancelOrderAmbiguousReconciles proves an ambiguous cancel is resolved by
// reading the order back rather than retried.
func TestCancelOrderAmbiguousReconciles(t *testing.T) {
	var reads int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			reads++
			state := "processing"
			if reads > 1 {
				state = "canceled"
			}
			_, _ = io.WriteString(w, `{"entity_id":42,"state":"`+state+`","grand_total":19.98,"order_currency_code":"USD"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/orders/42/cancel":
			w.WriteHeader(http.StatusRequestTimeout)
			_, _ = io.WriteString(w, `{"message":"The request timed out."}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	ledger := newFakeLedger()
	useLedger(t, ledger)
	svc := testService(t, ts.URL)
	result, err := svc.CancelOrder(context.Background(), CancelOrderInput{OrderID: 42, IdempotencyKey: "cancel-timeout"})
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if !result.Canceled || !result.Reconciled {
		t.Fatalf("result = %+v, want a reconciled cancellation", result)
	}
	row := ledger.row(result.OperationID)
	if row == nil || row.state != storage.OpSucceeded {
		t.Fatalf("ledger row = %+v, want succeeded", row)
	}
}

// TestCancelOrderReplays proves a repeated key replays the recorded cancellation
// even though the order is canceled by then (the ledger is consulted before the
// terminal-state pre-flight), with no second upstream mutation.
func TestCancelOrderReplays(t *testing.T) {
	var mu sync.Mutex
	state, posts := "processing", 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		current := state
		if r.Method == http.MethodPost {
			posts++
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/orders/42":
			_, _ = io.WriteString(w, `{"entity_id":42,"increment_id":"000000042","state":"`+current+`","grand_total":19.98,"order_currency_code":"USD"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/orders/42/cancel":
			mu.Lock()
			state = "canceled"
			mu.Unlock()
			_, _ = io.WriteString(w, `true`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	useLedger(t, newFakeLedger())
	svc := testService(t, ts.URL)
	in := CancelOrderInput{OrderID: 42, IdempotencyKey: "cancel-replay"}
	first, err := svc.CancelOrder(context.Background(), in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	mu.Lock()
	postsAfterFirst := posts
	mu.Unlock()
	second, err := svc.CancelOrder(context.Background(), in)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !second.Replayed || second.OperationID != first.OperationID {
		t.Fatalf("second = %+v, want a replay of %s", second, first.OperationID)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != postsAfterFirst {
		t.Fatalf("POSTs = %d, want %d (the replay must not cancel again)", posts, postsAfterFirst)
	}
}

// TestCancelOrderAmbiguousNeedsTwoReads proves a single "still open" read is not
// treated as proof that the cancellation failed.
func TestCancelOrderAmbiguousNeedsTwoReads(t *testing.T) {
	t.Run("two agreeing reads are a failure", func(t *testing.T) {
		reads := 0
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.Method == http.MethodGet:
				reads++
				_, _ = io.WriteString(w, `{"entity_id":42,"state":"processing","grand_total":19.98,"order_currency_code":"USD"}`)
			case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/orders/42/cancel":
				w.WriteHeader(http.StatusRequestTimeout)
				_, _ = io.WriteString(w, `{"message":"The request timed out."}`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()
		useLedger(t, newFakeLedger())
		svc := testService(t, ts.URL)
		_, err := svc.CancelOrder(context.Background(), CancelOrderInput{OrderID: 42, IdempotencyKey: "cancel-open"})
		if err == nil || errs.RetryableError(err) != true {
			t.Fatalf("err = %v, want a retryable not-canceled failure", err)
		}
		if reads < 3 { // one pre-flight read plus at least two reconciliation reads
			t.Fatalf("reads = %d, want the pre-flight plus two reconciliation reads", reads)
		}
	})

	t.Run("one read is not enough", func(t *testing.T) {
		reads := 0
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.Method == http.MethodGet:
				reads++
				if reads > 1 {
					// Every reconciliation read fails: no agreement.
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = io.WriteString(w, `{"message":"Internal error"}`)
					return
				}
				_, _ = io.WriteString(w, `{"entity_id":42,"state":"processing","grand_total":19.98,"order_currency_code":"USD"}`)
			case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/orders/42/cancel":
				w.WriteHeader(http.StatusRequestTimeout)
				_, _ = io.WriteString(w, `{"message":"The request timed out."}`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()
		useLedger(t, newFakeLedger())
		svc := testService(t, ts.URL)
		_, err := svc.CancelOrder(context.Background(), CancelOrderInput{OrderID: 42, IdempotencyKey: "cancel-unreadable"})
		if errs.CodeOf(err) != errs.CodeOperationUnknown {
			t.Fatalf("err = %v, want operation_unknown", err)
		}
	})
}

// TestCancelOrderMagentoDeclines proves a 200 with body `false` is not reported as
// a cancellation. Magento answers exactly that way for an order it will not
// cancel (for example an already-invoiced one); trusting the status code alone
// reported canceled:true for an order that never changed.
func TestCancelOrderMagentoDeclines(t *testing.T) {
	var posts int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/orders/42":
			_, _ = io.WriteString(w, `{"entity_id":42,"increment_id":"000000042","state":"processing","grand_total":78,"order_currency_code":"USD"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/orders/42/cancel":
			posts++
			_, _ = io.WriteString(w, `false`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	ledger := newFakeLedger()
	useLedger(t, ledger)
	svc := testService(t, ts.URL)

	result, err := svc.CancelOrder(context.Background(), CancelOrderInput{OrderID: 42, IdempotencyKey: "cancel-declined"})
	if result != nil {
		t.Fatalf("result = %+v, want nil for a declined cancellation", result)
	}
	if errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("err = %v, want conflict", err)
	}
	if posts != 1 {
		t.Fatalf("POSTs = %d, want 1", posts)
	}
	// The ledger must record a failure, never a success, for a cancellation that
	// did not happen.
	if c := ledger.counts(); c.succeeds != 0 || c.fails != 1 {
		t.Fatalf("ledger succeeds=%d fails=%d, want 0/1", c.succeeds, c.fails)
	}
}
