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

func TestCreateShipment(t *testing.T) {
	var method, path string
	var body shipmentCreateBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			// The read-back after the create route.
			_, _ = w.Write([]byte(`{"entity_id":3,"increment_id":"000000003","order_id":42,"total_qty":2,` +
				`"tracks":[{"title":"UPS","track_number":"1Z999","carrier_code":"ups"}]}`))
			return
		}
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		// The create route answers with the new id as a JSON scalar.
		_, _ = w.Write([]byte(`"3"`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	shipment, err := svc.CreateShipment(context.Background(), ShipmentCreateInput{
		OrderID: 42,
		Items:   []DocumentLine{{OrderItemID: 1, Qty: 2}},
		Tracks:  []ShipmentTrackInput{{TrackNumber: "1Z999", Title: "UPS", CarrierCode: "ups"}},
		Comment: "Shipped today", Notify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/rest/default/V1/order/42/ship" {
		t.Fatalf("request = %s %s", method, path)
	}
	if body.Entity.OrderID != 42 || len(body.Entity.Items) != 1 || len(body.Entity.Tracks) != 1 {
		t.Fatalf("body = %+v", body)
	}
	if body.Entity.Tracks[0].TrackNumber != "1Z999" || !body.Entity.Notify || body.Entity.Comment.Comment != "Shipped today" {
		t.Fatalf("body = %+v", body)
	}
	if shipment.EntityID != 3 || len(shipment.Tracks) != 1 || shipment.Tracks[0].TrackNumber != "1Z999" {
		t.Fatalf("shipment = %+v", shipment)
	}
}

// creditmemoFake answers the credit memo pre-flight reads and the write.
type creditmemoFake struct {
	mu          sync.Mutex
	requests    []string
	orderJSON   string
	invoices    string
	writeStatus int
	postBody    []byte
	// refunded is added to the order's qty_refunded after a successful write, so
	// a repeat sees the quantity already spent.
	refunded float64
}

func (f *creditmemoFake) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/orders/42":
			f.mu.Lock()
			orderJSON := f.orderJSON
			f.mu.Unlock()
			_, _ = io.WriteString(w, orderJSON)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/invoices":
			_, _ = io.WriteString(w, f.invoices)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/creditmemo/9":
			_, _ = io.WriteString(w, `{"entity_id":9,"increment_id":"000000009","order_id":42,"invoice_id":7,`+
				`"grand_total":25,"adjustment":-5,"order_currency_code":"USD"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/order/42/refund":
			raw, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.postBody = raw
			status := f.writeStatus
			f.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"message":"The credit memo could not be saved."}`)
				return
			}
			f.mu.Lock()
			f.refunded++
			f.mu.Unlock()
			// The create route answers with the new id as a JSON scalar.
			_, _ = io.WriteString(w, `"9"`)
		default:
			http.NotFound(w, r)
		}
	}
}

// refundableOrder is an order with one invoiced, not yet refunded line.
const refundableOrder = `{"entity_id":42,"order_currency_code":"USD","grand_total":50,` +
	`"items":[{"item_id":1,"sku":"SKU-1","price":25,"qty_invoiced":2,"qty_refunded":0}]}`

// singleInvoice is the order's one invoice.
const singleInvoice = `{"items":[{"entity_id":7,"increment_id":"000000007","order_id":42,"state":2,"grand_total":50}],"total_count":1}`

// postsIn counts the POST requests in a recorded request log.
func postsIn(requests []string) int {
	posts := 0
	for _, request := range requests {
		if strings.HasPrefix(request, "POST ") {
			posts++
		}
	}
	return posts
}

func TestCreateCreditmemo(t *testing.T) {
	fake := &creditmemoFake{orderJSON: refundableOrder, invoices: singleInvoice}
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	ledger := newFakeLedger()
	useLedger(t, ledger)
	svc := testService(t, ts.URL)

	result, err := svc.CreateCreditmemo(context.Background(), CreditmemoCreateInput{
		OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 1}},
		AdjustmentPositive: 10, AdjustmentNegative: 15, Comment: "Partial refund",
		IdempotencyKey: "refund-1",
	})
	if err != nil {
		t.Fatalf("CreateCreditmemo: %v", err)
	}
	var body creditmemoCreateBody
	if err := json.Unmarshal(fake.postBody, &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body.Entity.OrderID != 42 || body.Entity.InvoiceID != 7 {
		t.Fatalf("body = %+v, want the invoice linkage", body.Entity)
	}
	if body.Entity.AdjustmentPositive != 10 || body.Entity.AdjustmentNegative != 15 {
		t.Fatalf("body = %+v", body.Entity)
	}
	if len(body.Entity.Items) != 1 || body.Entity.Items[0].OrderItemID != 1 || body.Entity.Items[0].Qty != 1 {
		t.Fatalf("items = %+v", body.Entity.Items)
	}
	if result.Creditmemo.EntityID != 9 || result.Creditmemo.Adjustment != -5 {
		t.Fatalf("creditmemo = %+v", result.Creditmemo)
	}
	if result.InvoiceID != 7 || result.OperationID == "" {
		t.Fatalf("result = %+v", result)
	}
	row := ledger.row(result.OperationID)
	if row == nil || row.state != storage.OpSucceeded {
		t.Fatalf("ledger row = %+v, want succeeded", row)
	}
	// 1 x 25.00 in integer minor units.
	if row.amountMinor == nil || *row.amountMinor != 2500 || row.currency != "USD" {
		t.Fatalf("ledger amount = %v %s, want 2500 USD", row.amountMinor, row.currency)
	}
	if row.entityType != storage.EntityCreditmemo || row.entityID != "7" {
		t.Fatalf("ledger entity = %s/%s", row.entityType, row.entityID)
	}
}

// TestCreateCreditmemoRejectsOverRefund proves MAG-03: an over-refund is
// rejected after the pre-flight reads and before any refund request.
func TestCreateCreditmemoRejectsOverRefund(t *testing.T) {
	fake := &creditmemoFake{orderJSON: refundableOrder, invoices: singleInvoice}
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	ledger := newFakeLedger()
	useLedger(t, ledger)
	svc := testService(t, ts.URL)

	_, err := svc.CreateCreditmemo(context.Background(), CreditmemoCreateInput{
		OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 3}},
	})
	if errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if !strings.Contains(err.Error(), "refundable") {
		t.Fatalf("err = %v, want the refundable quantity named", err)
	}
	if posts := postsIn(fake.requests); posts != 0 {
		t.Fatalf("POSTs = %d, want 0 (rejected before the refund call)", posts)
	}
	// The rejection is recorded: a repeat with the same key returns it.
	if c := ledger.counts(); c.begins != 1 || c.fails != 1 {
		t.Fatalf("ledger begins=%d fails=%d, want 1/1", c.begins, c.fails)
	}
}

// TestCreateCreditmemoInvoiceLinkage proves the invoice linkage rules.
func TestCreateCreditmemoInvoiceLinkage(t *testing.T) {
	tests := []struct {
		name     string
		invoices string
		request  int
		wantErr  string
	}{
		{name: "no invoice", invoices: `{"items":[],"total_count":0}`, wantErr: "no invoice"},
		{
			name:     "several invoices need an explicit id",
			invoices: `{"items":[{"entity_id":7},{"entity_id":8}],"total_count":2}`,
			wantErr:  "pass invoice_id",
		},
		{
			name:     "foreign invoice id",
			invoices: `{"items":[{"entity_id":7}],"total_count":1}`, request: 99,
			wantErr: "does not belong",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &creditmemoFake{orderJSON: refundableOrder, invoices: tc.invoices}
			ts := httptest.NewServer(fake.handler(t))
			defer ts.Close()
			useLedger(t, newFakeLedger())
			svc := testService(t, ts.URL)
			_, err := svc.CreateCreditmemo(context.Background(), CreditmemoCreateInput{
				OrderID: 42, InvoiceID: tc.request, Items: []DocumentLine{{OrderItemID: 1, Qty: 1}},
			})
			if errs.CodeOf(err) != errs.CodeInvalidArgument {
				t.Fatalf("err = %v, want invalid_argument", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if posts := postsIn(fake.requests); posts != 0 {
				t.Fatalf("POSTs = %d, want 0", posts)
			}
		})
	}
}

// TestCreateCreditmemoUnknownOnAmbiguousOutcome proves an ambiguous refund is
// recorded as unknown and never reported as retryable.
func TestCreateCreditmemoUnknownOnAmbiguousOutcome(t *testing.T) {
	fake := &creditmemoFake{orderJSON: refundableOrder, invoices: singleInvoice, writeStatus: http.StatusRequestTimeout}
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	ledger := newFakeLedger()
	useLedger(t, ledger)
	svc := testService(t, ts.URL)

	_, err := svc.CreateCreditmemo(context.Background(), CreditmemoCreateInput{
		OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 1}}, IdempotencyKey: "refund-timeout",
	})
	if errs.CodeOf(err) != errs.CodeOperationUnknown {
		t.Fatalf("err = %v, want operation_unknown", err)
	}
	if errs.RetryableError(err) {
		t.Fatal("an unknown refund must not be retryable")
	}
	if posts := postsIn(fake.requests); posts != 1 {
		t.Fatalf("POSTs = %d, want 1 (no retry)", posts)
	}
	if c := ledger.counts(); c.unknowns != 1 {
		t.Fatalf("ledger unknowns = %d, want 1", c.unknowns)
	}
}

func TestDocumentWriteValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	line := []DocumentLine{{OrderItemID: 1, Qty: 1}}

	shipmentCases := []ShipmentCreateInput{
		{OrderID: 0, Items: line},
		{OrderID: 42},
		{OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 0}}},
		{OrderID: 42, Items: line, Tracks: []ShipmentTrackInput{{TrackNumber: "", CarrierCode: "ups"}}},
		{OrderID: 42, Items: line, Tracks: []ShipmentTrackInput{{TrackNumber: "1Z", CarrierCode: "BAD CODE"}}},
		{OrderID: 42, Items: line, Comment: "bad\x00comment"},
	}
	for i, in := range shipmentCases {
		if _, err := svc.CreateShipment(ctx, in); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("shipment case %d: err = %v", i, err)
		}
	}
	tooManyTracks := make([]ShipmentTrackInput, maxShipmentTracks+1)
	for i := range tooManyTracks {
		tooManyTracks[i] = ShipmentTrackInput{TrackNumber: "1Z", CarrierCode: "ups"}
	}
	if _, err := svc.CreateShipment(ctx, ShipmentCreateInput{OrderID: 42, Items: line, Tracks: tooManyTracks}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("track ceiling err = %v", err)
	}

	creditmemoCases := []CreditmemoCreateInput{
		{OrderID: 0, Items: line},
		{OrderID: 42},
		{OrderID: 42, Items: line, AdjustmentPositive: -1},
		{OrderID: 42, Items: line, AdjustmentNegative: -1},
	}
	for i, in := range creditmemoCases {
		if _, err := svc.CreateCreditmemo(ctx, in); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("creditmemo case %d: err = %v", i, err)
		}
	}
}

// TestCreateCreditmemoReplaysAfterFullRefund proves the ledger is consulted
// before the refundable-quantity check: a repeat of a full refund replays the
// recorded credit memo instead of being rejected as an over-refund.
func TestCreateCreditmemoReplaysAfterFullRefund(t *testing.T) {
	orderJSON := `{"entity_id":42,"order_currency_code":"USD","grand_total":50,` +
		`"items":[{"item_id":1,"sku":"SKU-1","price":25,"qty_invoiced":1,"qty_refunded":0}]}`
	fake := &creditmemoFake{orderJSON: orderJSON, invoices: singleInvoice}
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	useLedger(t, newFakeLedger())
	svc := testService(t, ts.URL)
	in := CreditmemoCreateInput{
		OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 1}}, IdempotencyKey: "refund-full",
	}
	first, err := svc.CreateCreditmemo(context.Background(), in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	postsAfterFirst := postsIn(fake.requests)
	// The refund spent the whole invoiced quantity: a second fresh attempt would
	// be an over-refund, but the replay must return the recorded memo.
	second, err := svc.CreateCreditmemo(context.Background(), in)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !second.Replayed || second.OperationID != first.OperationID {
		t.Fatalf("second = %+v, want a replay of %s", second, first.OperationID)
	}
	if posts := postsIn(fake.requests); posts != postsAfterFirst {
		t.Fatalf("POSTs = %d, want %d (no second refund)", posts, postsAfterFirst)
	}
}
