// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

const (
	maxDocumentItems    = 100
	maxDocumentComment  = 1024
	maxShipmentTracks   = 20
	maxTrackNumberBytes = 128
	maxTrackTitleBytes  = 255
)

var carrierCodeRE = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// DocumentLine is one order item and the quantity to process.
type DocumentLine struct {
	OrderItemID int     `json:"order_item_id"`
	Qty         float64 `json:"qty" jsonschema:"Quantity at this source."`
}

// ShipmentTrackInput is one tracking number to attach to a shipment.
type ShipmentTrackInput struct {
	TrackNumber string `json:"track_number"`
	Title       string `json:"title" jsonschema:"Display title for this entry."`
	CarrierCode string `json:"carrier_code"`
}

// ShipmentCreateInput is one shipment creation request.
type ShipmentCreateInput struct {
	OrderID int                  `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	Items   []DocumentLine       `json:"items" jsonschema:"Entries returned by this call."`
	Tracks  []ShipmentTrackInput `json:"tracks,omitempty" jsonschema:"Tracking numbers attached to the shipment."`
	Comment string               `json:"comment,omitempty"`
	Notify  bool                 `json:"notify"`
}

// CreditmemoCreateInput is one credit memo creation request.
type CreditmemoCreateInput struct {
	OrderID            int            `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	InvoiceID          int            `json:"invoice_id,omitempty"`
	Items              []DocumentLine `json:"items" jsonschema:"Entries returned by this call."`
	AdjustmentPositive float64        `json:"adjustment_positive,omitempty"`
	AdjustmentNegative float64        `json:"adjustment_negative,omitempty"`
	Comment            string         `json:"comment,omitempty"`
	Notify             bool           `json:"notify"`

	// IdempotencyKey makes the call replayable: repeating it with the same
	// arguments returns the recorded credit memo instead of refunding twice.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// Instance is the configured instance id recorded in the operation ledger.
	Instance string `json:"instance,omitempty" jsonschema:"Configured instance id the result came from."`
}

// CreditmemoCreateResult is the recorded outcome of a credit memo creation.
type CreditmemoCreateResult struct {
	Creditmemo  Creditmemo `json:"creditmemo" jsonschema:"The credit memo that was created or read."`
	InvoiceID   int        `json:"invoice_id"`
	OperationID string     `json:"operation_id,omitempty" jsonschema:"Ledger operation id, for polling an unknown outcome."`
	Replayed    bool       `json:"replayed,omitempty" jsonschema:"True when a previous result was replayed for the same idempotency_key, so nothing new happened."`
}

// CreateShipment ships order items with POST /shipments.
func (s *Service) CreateShipment(ctx context.Context, in ShipmentCreateInput) (*Shipment, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if err := validateShipmentCreate(in); err != nil {
		return nil, err
	}
	items := make([]invoiceItemBody, 0, len(in.Items))
	for _, line := range in.Items {
		items = append(items, invoiceItemBody(line))
	}
	tracks := make([]shipmentTrackBody, 0, len(in.Tracks))
	for _, track := range in.Tracks {
		tracks = append(tracks, shipmentTrackBody(track))
	}
	body := shipmentCreateBody{Entity: shipmentEntityBody{
		OrderID: in.OrderID, Items: items, Tracks: tracks,
		Comment: documentComment{Comment: in.Comment, IsVisibleOnFront: false},
		Notify:  in.Notify,
	}}
	raw, err := s.client.do(ctx, http.MethodPost, "/shipments", nil, body)
	if err != nil {
		return nil, err
	}
	var shipment Shipment
	if err := json.Unmarshal(raw, &shipment); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable shipment payload").WithCause(err)
	}
	return &shipment, nil
}

// CreateCreditmemo refunds order items with POST /creditmemos.
//
// Before the call it reads the order and its invoices and validates, locally:
//   - the invoice linkage (an explicit invoice_id, or the order's single invoice;
//     several invoices require an explicit id, none is a validation error);
//   - the refundable quantity per order item (qty_invoiced - qty_refunded), so an
//     over-refund is rejected without any upstream write.
//
// The credit memo is recorded in the operation ledger with the authorised line
// amount in integer minor units. An ambiguous outcome (timeout, transport
// failure, 5xx) is recorded as unknown and returned as a non-retryable
// operation_unknown: a retry could refund twice.
func (s *Service) CreateCreditmemo(ctx context.Context, in CreditmemoCreateInput) (*CreditmemoCreateResult, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if err := validateCreditmemoCreate(in); err != nil {
		return nil, err
	}
	if in.IdempotencyKey != "" && len(in.IdempotencyKey) > 256 {
		return nil, invalidf("idempotency_key must be at most 256 characters")
	}
	state, err := s.orderRefundState(ctx, in.OrderID)
	if err != nil {
		return nil, err
	}
	invoiceID, err := s.refundInvoiceID(ctx, in.OrderID, in.InvoiceID)
	if err != nil {
		return nil, err
	}
	amount, err := authorisedAmount(documentLinesToInvoiceLines(in.Items), state.Items, state.Currency)
	if err != nil {
		return nil, err
	}
	ops, err := configuredOperations()
	if err != nil {
		return nil, err
	}
	// The ledger is consulted before the refundable-quantity check: a repeat of a
	// successful refund must replay the recorded credit memo, not be rejected as
	// an over-refund because the quantity is now spent.
	run, begun, err := ops.Begin(ctx, OperationRequest{
		Tool: "magento.creditmemo.create", Instance: s.operationInstanceID(in.Instance),
		Subject: callerSubject(ctx), IdempotencyKey: in.IdempotencyKey,
		EntityType: storage.EntityCreditmemo, EntityID: strconv.Itoa(invoiceID),
		Amount: &amount,
		Args: creditmemoArgs{
			OrderID: in.OrderID, InvoiceID: invoiceID, Items: in.Items,
			AdjustmentPositive: in.AdjustmentPositive, AdjustmentNegative: in.AdjustmentNegative,
		},
	})
	if err != nil {
		return nil, err
	}
	if begun != nil && begun.Decision == storage.DecisionReplay {
		var recorded CreditmemoCreateResult
		if err := Replay(begun, &recorded); err != nil {
			return nil, err
		}
		recorded.Replayed = true
		return &recorded, nil
	}
	// Pre-flight on a fresh attempt: reject an over-refund locally, before any
	// refund request. The ledger row is closed as failed so a repeat with the same
	// key returns this rejection instead of executing again.
	if err := state.checkRefundable(in.Items); err != nil {
		_ = run.Fail(ctx, string(errs.CodeOf(err)))
		return nil, err
	}
	items := make([]invoiceItemBody, 0, len(in.Items))
	for _, line := range in.Items {
		items = append(items, invoiceItemBody(line))
	}
	body := creditmemoCreateBody{Entity: creditmemoEntityBody{
		OrderID: in.OrderID, InvoiceID: invoiceID, Items: items,
		AdjustmentPositive: in.AdjustmentPositive, AdjustmentNegative: in.AdjustmentNegative,
		Comment: documentComment{Comment: in.Comment, IsVisibleOnFront: false},
		Notify:  in.Notify,
	}}
	raw, postErr := s.client.do(ctx, http.MethodPost, "/creditmemos", nil, body)
	switch classifyOperationError(postErr) {
	case operationNotApplied:
		_ = run.Fail(ctx, string(errs.CodeOf(postErr)))
		return nil, postErr
	case operationAmbiguous:
		_ = run.Unknown(ctx, string(errs.CodeOf(postErr)))
		return nil, operationUnknownError(run.ID(), "credit memo creation", string(errs.CodeOf(postErr)))
	}
	var creditmemo Creditmemo
	if err := json.Unmarshal(raw, &creditmemo); err != nil {
		_ = run.Unknown(ctx, string(errs.CodeUpstreamUnavailable))
		return nil, operationUnknownError(run.ID(), "credit memo creation", string(errs.CodeUpstreamUnavailable))
	}
	result := &CreditmemoCreateResult{Creditmemo: creditmemo, InvoiceID: invoiceID, OperationID: run.ID()}
	ref := "creditmemo:" + creditmemo.IncrementID
	if creditmemo.IncrementID == "" {
		ref = "creditmemo:" + strconv.Itoa(creditmemo.EntityID)
	}
	if err := run.Succeed(ctx, ref, result); err != nil {
		return nil, err
	}
	return result, nil
}

// creditmemoArgs is the canonical argument set hashed for replay detection.
type creditmemoArgs struct {
	OrderID            int            `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	InvoiceID          int            `json:"invoice_id"`
	Items              []DocumentLine `json:"items" jsonschema:"Entries returned by this call."`
	AdjustmentPositive float64        `json:"adjustment_positive"`
	AdjustmentNegative float64        `json:"adjustment_negative"`
}

// orderRefundState is the part of an order a refund decision needs.
type orderRefundState struct {
	EntityID   int               `json:"entity_id" jsonschema:"Magento entity id."`
	Currency   string            `json:"order_currency_code" jsonschema:"ISO 4217 code the amounts are expressed in."`
	GrandTotal float64           `json:"grand_total" jsonschema:"Document total including shipping and tax."`
	Items      []OrderItem       `json:"items" jsonschema:"Entries returned by this call."`
	Refund     []orderRefundItem `json:"-"`
}

// orderRefundItem carries the invoiced and refunded quantities of one line.
type orderRefundItem struct {
	ItemID      int     `json:"item_id"`
	QtyInvoiced float64 `json:"qty_invoiced"`
	QtyRefunded float64 `json:"qty_refunded"`
}

// orderRefundState reads the order and decodes the invoiced/refunded quantities
// per line item.
func (s *Service) orderRefundState(ctx context.Context, orderID int) (*orderRefundState, error) {
	raw, err := s.client.do(ctx, http.MethodGet, "/orders/"+strconv.Itoa(orderID), nil, nil)
	if err != nil {
		return nil, err
	}
	var wire struct {
		EntityID   int     `json:"entity_id" jsonschema:"Magento entity id."`
		Currency   string  `json:"order_currency_code" jsonschema:"ISO 4217 code the amounts are expressed in."`
		GrandTotal float64 `json:"grand_total" jsonschema:"Document total including shipping and tax."`
		Items      []struct {
			ItemID      int     `json:"item_id"`
			SKU         string  `json:"sku" jsonschema:"Product SKU."`
			Price       float64 `json:"price"`
			RowTotal    float64 `json:"row_total"`
			QtyInvoiced float64 `json:"qty_invoiced"`
			QtyRefunded float64 `json:"qty_refunded"`
		} `json:"items" jsonschema:"Entries returned by this call."`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable order payload").WithCause(err)
	}
	state := &orderRefundState{EntityID: wire.EntityID, Currency: wire.Currency, GrandTotal: wire.GrandTotal}
	state.Items = make([]OrderItem, 0, len(wire.Items))
	state.Refund = make([]orderRefundItem, 0, len(wire.Items))
	for _, item := range wire.Items {
		state.Items = append(state.Items, OrderItem{
			ItemID: item.ItemID, SKU: item.SKU, Price: item.Price, RowTotal: item.RowTotal,
			QtyOrdered: item.QtyInvoiced,
		})
		state.Refund = append(state.Refund, orderRefundItem{
			ItemID: item.ItemID, QtyInvoiced: item.QtyInvoiced, QtyRefunded: item.QtyRefunded,
		})
	}
	return state, nil
}

// checkRefundable rejects a request whose quantity exceeds what is still
// refundable, before any upstream write.
func (s *orderRefundState) checkRefundable(lines []DocumentLine) error {
	refundable := make(map[int]float64, len(s.Refund))
	for _, item := range s.Refund {
		refundable[item.ItemID] = item.QtyInvoiced - item.QtyRefunded
	}
	for _, line := range lines {
		available, ok := refundable[line.OrderItemID]
		if !ok {
			return invalidf("order item %d is not part of the order", line.OrderItemID)
		}
		if line.Qty > available+1e-9 {
			return invalidf("order item %d has %g refundable units, cannot refund %g",
				line.OrderItemID, math.Max(0, available), line.Qty)
		}
	}
	return nil
}

// refundInvoiceID resolves the invoice a credit memo is linked to: the explicit
// id when given, otherwise the order's single invoice. Several invoices need an
// explicit id; none is a validation error.
func (s *Service) refundInvoiceID(ctx context.Context, orderID, requested int) (int, error) {
	invoices, _, err := s.OrderInvoices(ctx, orderID)
	if err != nil {
		return 0, err
	}
	if requested > 0 {
		for _, invoice := range invoices {
			if invoice.EntityID == requested {
				return requested, nil
			}
		}
		return 0, invalidf("invoice %d does not belong to order %d", requested, orderID)
	}
	switch len(invoices) {
	case 0:
		return 0, invalidf("order %d has no invoice, so there is nothing to refund", orderID)
	case 1:
		return invoices[0].EntityID, nil
	default:
		return 0, invalidf("order %d has %d invoices; pass invoice_id to choose the one to refund", orderID, len(invoices))
	}
}

// documentLinesToInvoiceLines adapts the refund lines to the shared amount helper.
func documentLinesToInvoiceLines(lines []DocumentLine) []InvoiceLine {
	out := make([]InvoiceLine, 0, len(lines))
	for _, line := range lines {
		out = append(out, InvoiceLine(line))
	}
	return out
}

type documentComment struct {
	Comment          string `json:"comment,omitempty"`
	IsVisibleOnFront bool   `json:"is_visible_on_front"`
}

type shipmentCreateBody struct {
	Entity shipmentEntityBody `json:"entity"`
}

type shipmentEntityBody struct {
	OrderID int                 `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	Items   []invoiceItemBody   `json:"items" jsonschema:"Entries returned by this call."`
	Tracks  []shipmentTrackBody `json:"tracks,omitempty" jsonschema:"Tracking numbers attached to the shipment."`
	Comment documentComment     `json:"comment"`
	Notify  bool                `json:"notify"`
}

type shipmentTrackBody struct {
	TrackNumber string `json:"track_number"`
	Title       string `json:"title,omitempty" jsonschema:"Display title for this entry."`
	CarrierCode string `json:"carrier_code"`
}

type creditmemoCreateBody struct {
	Entity creditmemoEntityBody `json:"entity"`
}

type creditmemoEntityBody struct {
	OrderID            int               `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	InvoiceID          int               `json:"invoice_id,omitempty"`
	Items              []invoiceItemBody `json:"items" jsonschema:"Entries returned by this call."`
	AdjustmentPositive float64           `json:"adjustment_positive,omitempty"`
	AdjustmentNegative float64           `json:"adjustment_negative,omitempty"`
	Comment            documentComment   `json:"comment"`
	Notify             bool              `json:"notify"`
}

func validateDocumentLines(orderID int, lines []DocumentLine) error {
	if orderID <= 0 {
		return invalidf("order_id must be a positive integer")
	}
	if len(lines) == 0 {
		return invalidf("at least one order item is required")
	}
	if len(lines) > maxDocumentItems {
		return invalidf("at most %d order items are allowed per call", maxDocumentItems)
	}
	seen := make(map[int]struct{}, len(lines))
	for _, line := range lines {
		if line.OrderItemID <= 0 {
			return invalidf("order_item_id must be a positive integer")
		}
		if line.Qty <= 0 {
			return invalidf("quantity must be greater than zero")
		}
		if _, duplicate := seen[line.OrderItemID]; duplicate {
			return invalidf("order item %d is duplicated", line.OrderItemID)
		}
		seen[line.OrderItemID] = struct{}{}
	}
	return nil
}

func validateDocumentComment(comment string) error {
	if len(comment) > maxDocumentComment || strings.ContainsFunc(comment, unicode.IsControl) {
		return invalidf("comment must be at most %d bytes and contain no control characters", maxDocumentComment)
	}
	return nil
}

func validateShipmentCreate(in ShipmentCreateInput) error {
	if err := validateDocumentLines(in.OrderID, in.Items); err != nil {
		return err
	}
	if err := validateDocumentComment(in.Comment); err != nil {
		return err
	}
	if len(in.Tracks) > maxShipmentTracks {
		return invalidf("at most %d tracking numbers are allowed per shipment", maxShipmentTracks)
	}
	for _, track := range in.Tracks {
		if track.TrackNumber == "" || len(track.TrackNumber) > maxTrackNumberBytes ||
			strings.ContainsFunc(track.TrackNumber, unicode.IsControl) {
			return invalidf("track_number must be 1-%d bytes without control characters", maxTrackNumberBytes)
		}
		if len(track.Title) > maxTrackTitleBytes {
			return invalidf("track title must be at most %d bytes", maxTrackTitleBytes)
		}
		if !carrierCodeRE.MatchString(track.CarrierCode) {
			return invalidf("carrier_code must match %s", carrierCodeRE.String())
		}
	}
	return nil
}

func validateCreditmemoCreate(in CreditmemoCreateInput) error {
	if err := validateDocumentLines(in.OrderID, in.Items); err != nil {
		return err
	}
	if err := validateDocumentComment(in.Comment); err != nil {
		return err
	}
	if in.AdjustmentPositive < 0 || in.AdjustmentNegative < 0 {
		return invalidf("adjustments must not be negative")
	}
	return nil
}
