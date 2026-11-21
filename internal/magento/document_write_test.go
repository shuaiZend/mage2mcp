// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"errors"
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
	var body shipmentOrderBody
	var rawBody []byte
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
		rawBody = raw
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
	if len(body.Items) != 1 || body.Items[0].OrderItemID != 1 || body.Items[0].Qty != 2 {
		t.Fatalf("body = %+v, want the requested line top-level", body)
	}
	if len(body.Tracks) != 1 || body.Tracks[0].TrackNumber != "1Z999" {
		t.Fatalf("tracks = %+v", body.Tracks)
	}
	if !body.Notify || !body.AppendComment || body.Comment == nil || body.Comment.Comment != "Shipped today" {
		t.Fatalf("body = %+v", body)
	}
	// The route takes top-level parameters; an {"entity": ...} wrapper makes
	// Magento receive no items and ship the whole order.
	var raw map[string]any
	if err := json.Unmarshal(rawBody, &raw); err != nil {
		t.Fatal(err)
	}
	if _, wrapped := raw["entity"]; wrapped {
		t.Fatalf("shipment body must not be wrapped in entity: %s", raw)
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
	// creditmemoJSON overrides the credit memo read-back; empty uses the default.
	creditmemoJSON string
	// invoiceJSON is the single-invoice read the invoice-level quantity cap uses.
	invoiceJSON string
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
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/invoices/7":
			payload := f.invoiceJSON
			if payload == "" {
				payload = `{"items":[]}`
			}
			_, _ = io.WriteString(w, payload)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/creditmemo/9":
			payload := f.creditmemoJSON
			if payload == "" {
				payload = `{"entity_id":9,"increment_id":"000000009","order_id":42,"invoice_id":7,` +
					`"grand_total":25,"adjustment":-5,"order_currency_code":"USD"}`
			}
			_, _ = io.WriteString(w, payload)
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
	var body refundOrderBody
	if err := json.Unmarshal(fake.postBody, &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body.Arguments == nil || body.Arguments.AdjustmentPositive != 10 || body.Arguments.AdjustmentNegative != 15 {
		t.Fatalf("body = %+v, want the adjustments in arguments", body)
	}
	if len(body.Items) != 1 || body.Items[0].OrderItemID != 1 || body.Items[0].Qty != 1 {
		t.Fatalf("items = %+v, want the requested line top-level", body.Items)
	}
	// The route takes top-level parameters; an {"entity": ...} wrapper makes
	// Magento receive no items and refund the whole invoice.
	var raw map[string]any
	if err := json.Unmarshal(fake.postBody, &raw); err != nil {
		t.Fatal(err)
	}
	if _, wrapped := raw["entity"]; wrapped {
		t.Fatalf("refund body must not be wrapped in entity: %s", raw)
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
	// U-69: the ledger records the document total, not the line subtotal:
	// 1 x 25.00 + 0 shipping + 10.00 - 15.00 = 20.00 in integer minor units.
	if row.amountMinor == nil || *row.amountMinor != 2000 || row.currency != "USD" {
		t.Fatalf("ledger amount = %v %s, want 2000 USD", row.amountMinor, row.currency)
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

// TestCreateCreditmemoPartialQuantityIsSentAndReported proves the partial refund
// path end to end at the tool boundary: the requested quantity is what goes on
// the wire, and the reported total_qty is the refunded quantity — not a zero
// leaked from a field Magento omits.
func TestCreateCreditmemoPartialQuantityIsSentAndReported(t *testing.T) {
	fake := &creditmemoFake{orderJSON: refundableOrder, invoices: singleInvoice}
	// The read-back carries no total_qty (Magento's creditmemo resource omits it)
	// but does carry the refunded item row.
	fake.creditmemoJSON = `{"entity_id":9,"increment_id":"000000009","order_id":42,"invoice_id":7,` +
		`"grand_total":25,"subtotal":25,"shipping_amount":0,"order_currency_code":"USD",` +
		`"items":[{"order_item_id":1,"qty":1,"row_total":25}]}`
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	useLedger(t, newFakeLedger())
	svc := testService(t, ts.URL)

	result, err := svc.CreateCreditmemo(context.Background(), CreditmemoCreateInput{
		OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 1}}, IdempotencyKey: "refund-partial",
	})
	if err != nil {
		t.Fatalf("CreateCreditmemo: %v", err)
	}
	var body refundOrderBody
	if err := json.Unmarshal(fake.postBody, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].Qty != 1 {
		t.Fatalf("wire items = %+v, want the requested 1 unit", body.Items)
	}
	if result.Creditmemo.TotalQty == nil || *result.Creditmemo.TotalQty != 1 {
		t.Fatalf("total_qty = %v, want 1 (the refunded quantity)", result.Creditmemo.TotalQty)
	}
}

// TestCreditmemoTotalQtyIsOmittedWhenUnknown proves a credit memo whose quantity
// Magento does not report is rendered without a false zero.
func TestCreditmemoTotalQtyIsOmittedWhenUnknown(t *testing.T) {
	cm, err := decodeCreditmemo([]byte(`{"entity_id":9,"grand_total":0,"order_currency_code":"USD"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cm.TotalQty != nil {
		t.Fatalf("total_qty = %v, want nil when Magento does not report it", *cm.TotalQty)
	}
	raw, err := json.Marshal(cm)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "total_qty") {
		t.Fatalf("unknown total_qty leaked as a value: %s", raw)
	}
}

// U-10: Magento's documentComment declares is_visible_on_front as an int. Sending
// a JSON boolean made every commented refund or shipment fail with HTTP 400.
// Reverting the field type to bool makes this test fail.
func TestDocumentCommentSendsIntegerVisibility(t *testing.T) {
	fake := &creditmemoFake{orderJSON: refundableOrder, invoices: singleInvoice}
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	useLedger(t, newFakeLedger())
	if _, err := testService(t, ts.URL).CreateCreditmemo(context.Background(), CreditmemoCreateInput{
		OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 1}}, Comment: "Please refund",
	}); err != nil {
		t.Fatalf("CreateCreditmemo: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(fake.postBody, &raw); err != nil {
		t.Fatal(err)
	}
	comment, _ := raw["comment"].(map[string]any)
	if comment == nil {
		t.Fatalf("body = %s, want a comment", raw)
	}
	visible, ok := comment["is_visible_on_front"].(float64)
	if !ok {
		t.Fatalf("is_visible_on_front = %#v, want a JSON number (Magento wants int)", comment["is_visible_on_front"])
	}
	if visible != 0 {
		t.Fatalf("is_visible_on_front = %v, want 0", visible)
	}
}

// U-11: a partial refund must not return the order's whole shipping. Omitting
// shipping_amount used to let Magento refund it all.
func TestCreateCreditmemoPartialRefundReturnsNoShipping(t *testing.T) {
	order := `{"entity_id":42,"order_currency_code":"USD","grand_total":60,"shipping_amount":10,` +
		`"shipping_refunded":0,"items":[{"item_id":1,"sku":"SKU-1","price":25,"qty_invoiced":2,"qty_refunded":0}]}`
	fake := &creditmemoFake{orderJSON: order, invoices: singleInvoice}
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	useLedger(t, newFakeLedger())
	if _, err := testService(t, ts.URL).CreateCreditmemo(context.Background(), CreditmemoCreateInput{
		OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 1}},
	}); err != nil {
		t.Fatalf("CreateCreditmemo: %v", err)
	}
	var body refundOrderBody
	if err := json.Unmarshal(fake.postBody, &body); err != nil {
		t.Fatal(err)
	}
	if body.Arguments == nil || body.Arguments.ShippingAmount == nil {
		t.Fatalf("body = %+v, want an explicit shipping_amount", body)
	}
	if *body.Arguments.ShippingAmount != 0 {
		t.Fatalf("shipping_amount = %v, want 0 for a partial refund", *body.Arguments.ShippingAmount)
	}
}

// U-11: a refund that consumes every remaining unit returns the remaining
// shipping, so a full return still refunds what the customer paid.
func TestCreateCreditmemoFullRefundReturnsRemainingShipping(t *testing.T) {
	order := `{"entity_id":42,"order_currency_code":"USD","grand_total":60,"shipping_amount":10,` +
		`"shipping_refunded":0,"items":[{"item_id":1,"sku":"SKU-1","price":25,"qty_invoiced":2,"qty_refunded":0}]}`
	fake := &creditmemoFake{orderJSON: order, invoices: singleInvoice}
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	useLedger(t, newFakeLedger())
	if _, err := testService(t, ts.URL).CreateCreditmemo(context.Background(), CreditmemoCreateInput{
		OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 2}},
	}); err != nil {
		t.Fatalf("CreateCreditmemo: %v", err)
	}
	var body refundOrderBody
	if err := json.Unmarshal(fake.postBody, &body); err != nil {
		t.Fatal(err)
	}
	if body.Arguments == nil || body.Arguments.ShippingAmount == nil || *body.Arguments.ShippingAmount != 10 {
		t.Fatalf("body = %+v, want shipping_amount 10", body)
	}
}

// U-11: an explicit shipping_amount is honoured, so an operator can refund part
// of the shipping.
func TestCreateCreditmemoExplicitShippingAmount(t *testing.T) {
	order := `{"entity_id":42,"order_currency_code":"USD","grand_total":60,"shipping_amount":10,` +
		`"shipping_refunded":0,"items":[{"item_id":1,"sku":"SKU-1","price":25,"qty_invoiced":2,"qty_refunded":0}]}`
	fake := &creditmemoFake{orderJSON: order, invoices: singleInvoice}
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	useLedger(t, newFakeLedger())
	shipping := 4.0
	if _, err := testService(t, ts.URL).CreateCreditmemo(context.Background(), CreditmemoCreateInput{
		OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 1}}, ShippingAmount: &shipping,
	}); err != nil {
		t.Fatalf("CreateCreditmemo: %v", err)
	}
	var body refundOrderBody
	if err := json.Unmarshal(fake.postBody, &body); err != nil {
		t.Fatal(err)
	}
	if body.Arguments == nil || body.Arguments.ShippingAmount == nil || *body.Arguments.ShippingAmount != 4 {
		t.Fatalf("body = %+v, want shipping_amount 4", body)
	}
}

// U-11: shipping_amount above what is still refundable is refused locally.
func TestCreateCreditmemoRejectsShippingOverRefundable(t *testing.T) {
	order := `{"entity_id":42,"order_currency_code":"USD","grand_total":60,"shipping_amount":10,` +
		`"shipping_refunded":8,"items":[{"item_id":1,"sku":"SKU-1","price":25,"qty_invoiced":2,"qty_refunded":0}]}`
	fake := &creditmemoFake{orderJSON: order, invoices: singleInvoice}
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	useLedger(t, newFakeLedger())
	shipping := 5.0
	_, err := testService(t, ts.URL).CreateCreditmemo(context.Background(), CreditmemoCreateInput{
		OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 1}}, ShippingAmount: &shipping,
	})
	if errs.CodeOf(err) != errs.CodeValidationFailed {
		t.Fatalf("err = %v, want validation_failed", err)
	}
	if !strings.Contains(err.Error(), "shipping still refundable") {
		t.Fatalf("err = %v, want the refundable shipping named", err)
	}
	if posts := postsIn(fake.requests); posts != 0 {
		t.Fatalf("POSTs = %d, want 0", posts)
	}
}

// U-12: the local pre-flight must bound the money, not only the quantity. An
// adjustment_positive used to consume the refundable amount silently and leave
// the last unit unrefundable upstream.
func TestCreateCreditmemoRejectsAdjustmentOverRefundable(t *testing.T) {
	order := `{"entity_id":42,"order_currency_code":"USD","grand_total":50,"shipping_amount":0,` +
		`"items":[{"item_id":1,"sku":"SKU-1","price":25,"qty_invoiced":1,"qty_refunded":0}]}`
	fake := &creditmemoFake{orderJSON: order, invoices: singleInvoice}
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	useLedger(t, newFakeLedger())
	_, err := testService(t, ts.URL).CreateCreditmemo(context.Background(), CreditmemoCreateInput{
		OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 1}}, AdjustmentPositive: 5,
	})
	if errs.CodeOf(err) != errs.CodeValidationFailed {
		t.Fatalf("err = %v, want validation_failed", err)
	}
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("not a taxonomy error: %v", err)
	}
	if e.Details["refundable_amount"] != "25.00" {
		t.Fatalf("details = %v, want refundable_amount 25.00", e.Details)
	}
	if posts := postsIn(fake.requests); posts != 0 {
		t.Fatalf("POSTs = %d, want 0", posts)
	}
}

// U-13: a credit memo may not refund more units than the invoice it is linked to
// invoiced. The order-level check alone allowed refunding 2 units against a
// 1-unit invoice.
func TestCreateCreditmemoRejectsInvoiceLevelOverRefund(t *testing.T) {
	order := `{"entity_id":42,"order_currency_code":"USD","grand_total":50,"shipping_amount":0,` +
		`"items":[{"item_id":1,"sku":"SKU-1","price":25,"qty_invoiced":2,"qty_refunded":0}]}`
	fake := &creditmemoFake{
		orderJSON:   order,
		invoices:    singleInvoice,
		invoiceJSON: `{"entity_id":7,"items":[{"order_item_id":1,"qty":1}]}`,
	}
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	useLedger(t, newFakeLedger())
	_, err := testService(t, ts.URL).CreateCreditmemo(context.Background(), CreditmemoCreateInput{
		OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 2}},
	})
	if errs.CodeOf(err) != errs.CodeValidationFailed {
		t.Fatalf("err = %v, want validation_failed", err)
	}
	if !strings.Contains(err.Error(), "invoice") {
		t.Fatalf("err = %v, want the invoice cap named", err)
	}
	if posts := postsIn(fake.requests); posts != 0 {
		t.Fatalf("POSTs = %d, want 0", posts)
	}
}

// U-13: refunding within the invoice quantity still works.
func TestCreateCreditmemoAllowsInvoiceLevelRefund(t *testing.T) {
	order := `{"entity_id":42,"order_currency_code":"USD","grand_total":50,"shipping_amount":0,` +
		`"items":[{"item_id":1,"sku":"SKU-1","price":25,"qty_invoiced":2,"qty_refunded":0}]}`
	fake := &creditmemoFake{
		orderJSON:   order,
		invoices:    singleInvoice,
		invoiceJSON: `{"entity_id":7,"items":[{"order_item_id":1,"qty":2}]}`,
	}
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	useLedger(t, newFakeLedger())
	if _, err := testService(t, ts.URL).CreateCreditmemo(context.Background(), CreditmemoCreateInput{
		OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 2}},
	}); err != nil {
		t.Fatalf("CreateCreditmemo: %v", err)
	}
}

// U-15: the create result used to present a locally derived invoice_id as fact
// even though Magento stored no link. The result now says whether the read-back
// proved the link.
func TestCreateCreditmemoReportsInvoiceLinkFromReadBack(t *testing.T) {
	cases := map[string]struct {
		payload string
		linked  bool
	}{
		"linked": {
			payload: `{"entity_id":9,"increment_id":"000000009","order_id":42,"invoice_id":7,"grand_total":25,"order_currency_code":"USD"}`,
			linked:  true,
		},
		"unlinked": {
			payload: `{"entity_id":9,"increment_id":"000000009","order_id":42,"grand_total":25,"order_currency_code":"USD"}`,
			linked:  false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake := &creditmemoFake{orderJSON: refundableOrder, invoices: singleInvoice, creditmemoJSON: tc.payload}
			ts := httptest.NewServer(fake.handler(t))
			defer ts.Close()
			useLedger(t, newFakeLedger())
			result, err := testService(t, ts.URL).CreateCreditmemo(context.Background(), CreditmemoCreateInput{
				OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 1}},
			})
			if err != nil {
				t.Fatalf("CreateCreditmemo: %v", err)
			}
			if result.InvoiceLinked != tc.linked {
				t.Fatalf("invoice_linked = %v, want %v", result.InvoiceLinked, tc.linked)
			}
			if result.InvoiceID != 7 {
				t.Fatalf("invoice_id = %d, want the validated invoice 7", result.InvoiceID)
			}
		})
	}
}

// U-21: the refundable quantity is structured data, not prose, and a single unit
// is not "1 units".
func TestCreateCreditmemoRefundableQuantityIsStructured(t *testing.T) {
	order := `{"entity_id":42,"order_currency_code":"USD","grand_total":25,"shipping_amount":0,` +
		`"items":[{"item_id":1,"sku":"SKU-1","price":25,"qty_invoiced":1,"qty_refunded":0}]}`
	fake := &creditmemoFake{orderJSON: order, invoices: singleInvoice}
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	useLedger(t, newFakeLedger())
	_, err := testService(t, ts.URL).CreateCreditmemo(context.Background(), CreditmemoCreateInput{
		OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 2}},
	})
	if errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	var typed *errs.Error
	if !errors.As(err, &typed) {
		t.Fatalf("not a taxonomy error: %v", err)
	}
	if typed.Details["refundable_units"] != "1" || typed.Details["requested_units"] != "2" {
		t.Fatalf("details = %v, want structured refundable/requested units", typed.Details)
	}
	if !strings.Contains(typed.Message, "1 refundable unit,") {
		t.Fatalf("message = %q, want the singular form", typed.Message)
	}
}
