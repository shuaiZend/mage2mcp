// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

const maxInvoiceItems = 100

// InvoiceLine is one invoice line: an order item and the quantity to invoice.
type InvoiceLine struct {
	OrderItemID int     `json:"order_item_id"`
	Qty         float64 `json:"qty" jsonschema:"Quantity at this source."`
}

// InvoiceCreateInput is one invoice creation request.
type InvoiceCreateInput struct {
	OrderID int           `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	Capture bool          `json:"capture"`
	Items   []InvoiceLine `json:"items" jsonschema:"Entries returned by this call."`

	// IdempotencyKey makes the call replayable: repeating it with the same
	// arguments returns the recorded invoice instead of creating a second one.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// Instance is the configured instance id recorded in the operation ledger.
	Instance string `json:"instance,omitempty" jsonschema:"Configured instance id the result came from."`
}

// InvoiceCreateResult is the recorded outcome of an invoice creation.
type InvoiceCreateResult struct {
	Invoice Invoice `json:"invoice" jsonschema:"The invoice that was created or read."`
	// Captured reports the invoice state Magento returned: a paid invoice proves
	// the capture flag was honoured by the workflow route.
	Captured bool `json:"captured"`
	// OperationID identifies the ledger operation; a replay returns the same id.
	OperationID string `json:"operation_id,omitempty" jsonschema:"Ledger operation id, for polling an unknown outcome."`
	Replayed    bool   `json:"replayed,omitempty" jsonschema:"True when a previous result was replayed for the same idempotency_key, so nothing new happened."`
}

// CreateInvoice invoices an order through the invoice workflow route
// (POST /V1/order/{orderId}/invoice), which is the documented route that honours
// the capture parameter. The repository route (POST /V1/invoices) persists an
// InvoiceInterface that has no capture field, so using it would silently ignore
// capture=true; this gateway refuses to do that.
//
// capture=false records an open invoice and charges nothing; capture=true asks
// Magento to capture the payment, and the returned invoice state (2 = paid)
// proves whether it did.
//
// The call is recorded in the operation ledger before the POST. An ambiguous
// outcome (timeout, transport failure, 5xx) is recorded as unknown and returned
// as a non-retryable operation_unknown: a retry could invoice the order twice.
func (s *Service) CreateInvoice(ctx context.Context, in InvoiceCreateInput) (*InvoiceCreateResult, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if err := validateInvoiceCreate(in); err != nil {
		return nil, err
	}
	order, err := s.GetOrder(ctx, in.OrderID)
	if err != nil {
		return nil, err
	}
	amount, err := authorisedAmount(in.Items, order.Items, order.Currency)
	if err != nil {
		return nil, err
	}
	ops, err := configuredOperations()
	if err != nil {
		return nil, err
	}
	run, begun, err := ops.Begin(ctx, OperationRequest{
		Tool: "magento.invoice.create", Instance: s.operationInstanceID(in.Instance),
		Subject: callerSubject(ctx), IdempotencyKey: in.IdempotencyKey,
		EntityType: storage.EntityInvoice, EntityID: strconv.Itoa(in.OrderID),
		Amount: &amount,
		Args:   invoiceCreateArgs{OrderID: in.OrderID, Capture: in.Capture, Items: in.Items},
	})
	if err != nil {
		return nil, err
	}
	if begun != nil && begun.Decision == storage.DecisionReplay {
		var recorded InvoiceCreateResult
		if err := Replay(begun, &recorded); err != nil {
			return nil, err
		}
		recorded.Replayed = true
		return &recorded, nil
	}

	body := invoiceOrderBody{Capture: in.Capture, Items: make([]invoiceItemBody, 0, len(in.Items))}
	for _, line := range in.Items {
		body.Items = append(body.Items, invoiceItemBody(line))
	}
	raw, postErr := s.client.do(ctx, http.MethodPost, "/order/"+strconv.Itoa(in.OrderID)+"/invoice", nil, body)
	switch classifyOperationError(postErr) {
	case operationNotApplied:
		_ = run.Fail(ctx, string(errs.CodeOf(postErr)))
		return nil, postErr
	case operationAmbiguous:
		_ = run.Unknown(ctx, string(errs.CodeOf(postErr)))
		return nil, operationUnknownError(run.ID(), "invoice creation", string(errs.CodeOf(postErr)))
	}
	var invoice Invoice
	if err := json.Unmarshal(raw, &invoice); err != nil {
		// Magento accepted the call but the response is unreadable: the invoice
		// exists, so the outcome is unknown rather than a failure.
		_ = run.Unknown(ctx, string(errs.CodeUpstreamUnavailable))
		return nil, operationUnknownError(run.ID(), "invoice creation", string(errs.CodeUpstreamUnavailable))
	}
	result := &InvoiceCreateResult{
		Invoice:     invoice,
		Captured:    invoice.State == invoiceStatePaid,
		OperationID: run.ID(),
	}
	ref := "invoice:" + invoice.IncrementID
	if invoice.IncrementID == "" {
		ref = "invoice:" + strconv.Itoa(invoice.EntityID)
	}
	if err := run.Succeed(ctx, ref, result); err != nil {
		return nil, err
	}
	return result, nil
}

// invoiceStatePaid is the Magento invoice state for a captured invoice.
const invoiceStatePaid = 2

// invoiceCreateArgs is the canonical argument set hashed for replay detection.
type invoiceCreateArgs struct {
	OrderID int           `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	Capture bool          `json:"capture"`
	Items   []InvoiceLine `json:"items" jsonschema:"Entries returned by this call."`
}

// invoiceOrderBody is the body of POST /V1/order/{orderId}/invoice
// (InvoiceOrderInterface::execute). The capture flag is a top-level parameter of
// that service method; it is not an invoice entity field.
type invoiceOrderBody struct {
	Capture bool              `json:"capture"`
	Items   []invoiceItemBody `json:"items" jsonschema:"Entries returned by this call."`
}

type invoiceItemBody struct {
	OrderItemID int     `json:"order_item_id"`
	Qty         float64 `json:"qty" jsonschema:"Quantity at this source."`
}

func validateInvoiceCreate(in InvoiceCreateInput) error {
	if in.OrderID <= 0 {
		return invalidf("order_id must be a positive integer")
	}
	if len(in.Items) == 0 {
		return invalidf("at least one invoice item is required")
	}
	if len(in.Items) > maxInvoiceItems {
		return invalidf("at most %d invoice items are allowed per call", maxInvoiceItems)
	}
	if in.IdempotencyKey != "" && len(in.IdempotencyKey) > 256 {
		return invalidf("idempotency_key must be at most 256 characters")
	}
	seen := make(map[int]struct{}, len(in.Items))
	for _, line := range in.Items {
		if line.OrderItemID <= 0 {
			return invalidf("order_item_id must be a positive integer")
		}
		if line.Qty <= 0 {
			return invalidf("invoice quantity must be greater than zero")
		}
		if _, duplicate := seen[line.OrderItemID]; duplicate {
			return invalidf("order item %d is duplicated", line.OrderItemID)
		}
		seen[line.OrderItemID] = struct{}{}
	}
	return nil
}

// CaptureInvoiceInput is one invoice capture request.
type CaptureInvoiceInput struct {
	InvoiceID int `json:"invoice_id"`
	// IdempotencyKey makes the call replayable: repeating it with the same
	// arguments returns the recorded outcome instead of capturing twice.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// Instance is the configured instance id recorded in the operation ledger.
	Instance string `json:"instance,omitempty" jsonschema:"Configured instance id the result came from."`
}

// CaptureResult is the recorded outcome of one capture.
type CaptureResult struct {
	InvoiceID   int    `json:"invoice_id"`
	Captured    bool   `json:"captured"`
	OperationID string `json:"operation_id,omitempty" jsonschema:"Ledger operation id, for polling an unknown outcome."`
	Replayed    bool   `json:"replayed,omitempty" jsonschema:"True when a previous result was replayed for the same idempotency_key, so nothing new happened."`
}

// CaptureInvoice captures an existing invoice's payment with
// POST /invoices/{id}/capture.
//
// Pre-flight: the invoice is read first. An invoice that is already paid (state
// 2) or canceled (state 3) is a typed conflict and no capture request is sent, so
// a repeated call cannot charge twice. The capture itself is recorded in the
// operation ledger before the POST: a timeout, transport failure or 5xx after
// the send is recorded as unknown and returned as a non-retryable
// operation_unknown, because Magento may already have moved the money.
func (s *Service) CaptureInvoice(ctx context.Context, in CaptureInvoiceInput) (*CaptureResult, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if in.InvoiceID <= 0 {
		return nil, invalidf("invoice_id must be a positive integer")
	}
	if in.IdempotencyKey != "" && len(in.IdempotencyKey) > 256 {
		return nil, invalidf("idempotency_key must be at most 256 characters")
	}
	invoice, err := s.GetInvoice(ctx, in.InvoiceID)
	if err != nil {
		return nil, err
	}
	amount, err := moneyFromAmount(invoice.GrandTotal, invoice.Currency)
	if err != nil {
		return nil, err
	}
	ops, err := configuredOperations()
	if err != nil {
		return nil, err
	}
	// The ledger is consulted before the terminal-state pre-flight: a caller that
	// lost the response deserves the recorded outcome, not a conflict. The read
	// above is only there to price the operation.
	run, begun, err := ops.Begin(ctx, OperationRequest{
		Tool: "magento.invoice.capture", Instance: s.operationInstanceID(in.Instance),
		Subject: callerSubject(ctx), IdempotencyKey: in.IdempotencyKey,
		EntityType: storage.EntityInvoice, EntityID: strconv.Itoa(in.InvoiceID),
		Amount: &amount,
		Args:   captureArgs{InvoiceID: in.InvoiceID},
	})
	if err != nil {
		return nil, err
	}
	if begun != nil && begun.Decision == storage.DecisionReplay {
		var recorded CaptureResult
		if err := Replay(begun, &recorded); err != nil {
			return nil, err
		}
		recorded.Replayed = true
		return &recorded, nil
	}
	// Pre-flight on a fresh attempt: never capture an invoice that is already
	// paid or canceled. The ledger row is closed as failed so a repeat with the
	// same key returns this conflict instead of executing again.
	switch invoice.State {
	case invoiceStatePaid:
		_ = run.Fail(ctx, string(errs.CodeConflict))
		return nil, errs.New(errs.CodeConflict,
			"the invoice is already paid; no capture request was sent").
			WithDetail("invoice_id", strconv.Itoa(in.InvoiceID)).
			WithDetail("invoice_state", strconv.Itoa(invoice.State)).
			WithDetail("operation_id", run.ID())
	case invoiceStateCanceled:
		_ = run.Fail(ctx, string(errs.CodeConflict))
		return nil, errs.New(errs.CodeConflict,
			"the invoice is canceled; it cannot be captured").
			WithDetail("invoice_id", strconv.Itoa(in.InvoiceID)).
			WithDetail("invoice_state", strconv.Itoa(invoice.State)).
			WithDetail("operation_id", run.ID())
	}
	_, postErr := s.client.do(ctx, http.MethodPost,
		"/invoices/"+strconv.Itoa(in.InvoiceID)+"/capture", nil, nil)
	switch classifyOperationError(postErr) {
	case operationNotApplied:
		_ = run.Fail(ctx, string(errs.CodeOf(postErr)))
		return nil, postErr
	case operationAmbiguous:
		_ = run.Unknown(ctx, string(errs.CodeOf(postErr)))
		return nil, operationUnknownError(run.ID(), "invoice capture", string(errs.CodeOf(postErr)))
	}
	result := &CaptureResult{InvoiceID: in.InvoiceID, Captured: true, OperationID: run.ID()}
	if err := run.Succeed(ctx, "invoice:"+strconv.Itoa(in.InvoiceID), result); err != nil {
		return nil, err
	}
	return result, nil
}

// invoiceStateCanceled is the Magento invoice state for a canceled invoice.
const invoiceStateCanceled = 3

// captureArgs is the canonical argument set hashed for replay detection.
type captureArgs struct {
	InvoiceID int `json:"invoice_id"`
}

// authorisedAmount sums the requested lines at their order unit price and returns
// the amount in integer minor units.
//
// Magento computes the final document total (tax, discount, shipping) itself;
// this is the authorised line value recorded with the intent so a reconciler can
// bound the money at risk. Quantities are scaled to thousandths and the line
// product is rounded to the minor unit, so the sum stays in integer arithmetic.
func authorisedAmount(lines []InvoiceLine, items []OrderItem, currency string) (Money, error) {
	if len(items) == 0 {
		return Money{}, errs.New(errs.CodeUpstreamUnavailable,
			"the order has no line items, so the authorised amount cannot be computed")
	}
	byID := make(map[int]OrderItem, len(items))
	for _, item := range items {
		byID[item.ItemID] = item
	}
	var total int64
	for _, line := range lines {
		item, ok := byID[line.OrderItemID]
		if !ok {
			return Money{}, invalidf("order item %d is not part of the order", line.OrderItemID)
		}
		unit, err := moneyFromAmount(item.Price, currency)
		if err != nil {
			return Money{}, err
		}
		qtyMilli := int64(math.Round(line.Qty * 1000))
		if qtyMilli <= 0 {
			return Money{}, invalidf("order item %d has a non-positive quantity", line.OrderItemID)
		}
		total += (unit.Minor*qtyMilli + 500) / 1000
	}
	return Money{Minor: total, Currency: strings.ToUpper(currency)}, nil
}
