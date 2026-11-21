// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	// ShippingAmount is the shipping refunded with this credit memo. Omitting it
	// no longer sweeps the order's whole remaining shipping into a partial refund:
	// nil means "refund the remaining shipping only when this memo refunds every
	// remaining refundable unit", and 0 means "refund no shipping" (U-11).
	ShippingAmount *float64 `json:"shipping_amount,omitempty"`
	Comment        string   `json:"comment,omitempty"`
	Notify         bool     `json:"notify"`

	// IdempotencyKey makes the call replayable: repeating it with the same
	// arguments returns the recorded credit memo instead of refunding twice.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// Instance is the configured instance id recorded in the operation ledger.
	Instance string `json:"instance,omitempty" jsonschema:"Configured instance id the result came from."`
}

// CreditmemoCreateResult is the recorded outcome of a credit memo creation.
type CreditmemoCreateResult struct {
	Creditmemo Creditmemo `json:"creditmemo" jsonschema:"The credit memo that was created or read."`
	// InvoiceID is the invoice this call validated the refund against. Magento
	// does not always store that link, so InvoiceLinked says whether the read-back
	// proved it (U-15).
	InvoiceID     int    `json:"invoice_id"`
	InvoiceLinked bool   `json:"invoice_linked" jsonschema:"True when Magento's read-back reports the same invoice link this call validated against."`
	OperationID   string `json:"operation_id,omitempty" jsonschema:"Ledger operation id, for polling an unknown outcome."`
	Replayed      bool   `json:"replayed,omitempty" jsonschema:"True when a previous result was replayed for the same idempotency_key, so nothing new happened."`
}

// CreateShipment ships order items with POST /order/{id}/ship.
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
	body := shipmentOrderBody{
		Items: items, Tracks: tracks,
		AppendComment: in.Comment != "",
		Notify:        in.Notify,
	}
	if in.Comment != "" {
		body.Comment = &documentComment{Comment: in.Comment, IsVisibleOnFront: 0}
	}
	// The create route is /order/{id}/ship and it answers with the new shipment id;
	// /shipments is not a route at all, and the id is not the shipment object.
	raw, err := s.client.do(ctx, http.MethodPost,
		"/order/"+strconv.Itoa(in.OrderID)+"/ship", nil, body)
	if err != nil {
		return nil, err
	}
	shipmentID, err := decodeCreatedDocumentID(raw)
	if err != nil {
		return nil, err
	}
	return s.GetShipment(ctx, shipmentID)
}

// CreateCreditmemo refunds order items with POST /order/{id}/refund.
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
	// U-13: a credit memo may not refund more units than the invoice it is linked
	// to actually invoiced. The order-level check below only knows qty_invoiced
	// across all invoices, so refunding 2 units against a 1-unit invoice passed.
	invoiced, err := s.invoiceItemQuantities(ctx, invoiceID)
	if err != nil {
		return nil, err
	}
	if err := checkInvoiceQuantities(in.Items, invoiced); err != nil {
		return nil, err
	}
	// U-11: decide the shipping refund explicitly instead of letting Magento
	// sweep the order's whole remaining shipping into a partial refund.
	shipping, err := state.shippingRefund(in)
	if err != nil {
		return nil, err
	}
	// U-69: the ledger records the document total (lines + shipping +
	// adjustments), not the line subtotal it used to under-report.
	amount, err := state.creditmemoTotal(in, shipping)
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
			ShippingAmount:     shipping,
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
	// U-12: bound the money too. adjustment_positive used to consume the
	// refundable amount while the local check only counted units, so the last
	// unit could never be refunded and the upstream reason was unreadable.
	if err := state.checkRefundableAmount(amount); err != nil {
		_ = run.Fail(ctx, string(errs.CodeOf(err)))
		return nil, err
	}
	items := make([]invoiceItemBody, 0, len(in.Items))
	for _, line := range in.Items {
		items = append(items, invoiceItemBody(line))
	}
	body := refundOrderBody{
		Items: items, AppendComment: in.Comment != "", Notify: in.Notify,
	}
	if in.Comment != "" {
		body.Comment = &documentComment{Comment: in.Comment, IsVisibleOnFront: 0}
	}
	// Always send arguments.shipping_amount, including 0. Leaving the field out is
	// what let Magento refund the order's whole remaining shipping on a one-unit
	// refund (U-11): the upstream default is "all of it", not "none".
	body.Arguments = &refundOrderArguments{
		ShippingAmount:     &shipping,
		AdjustmentPositive: in.AdjustmentPositive, AdjustmentNegative: in.AdjustmentNegative,
	}
	// The create route is /order/{id}/refund; /creditmemos is not a route, and the
	// answer is the new credit memo id as a JSON scalar rather than the document.
	raw, postErr := s.client.do(ctx, http.MethodPost,
		"/order/"+strconv.Itoa(in.OrderID)+"/refund", nil, body)
	switch classifyOperationError(postErr) {
	case operationNotApplied:
		_ = run.Fail(ctx, string(errs.CodeOf(postErr)))
		return nil, postErr
	case operationAmbiguous:
		_ = run.Unknown(ctx, string(errs.CodeOf(postErr)))
		return nil, operationUnknownError(run.ID(), "credit memo creation", string(errs.CodeOf(postErr)))
	}
	creditmemoID, idErr := decodeCreatedDocumentID(raw)
	if idErr != nil {
		_ = run.Unknown(ctx, string(errs.CodeUpstreamUnavailable))
		return nil, operationUnknownError(run.ID(), "credit memo creation", string(errs.CodeUpstreamUnavailable))
	}
	created, readErr := s.GetCreditmemo(ctx, creditmemoID)
	if readErr != nil {
		_ = run.Unknown(ctx, string(errs.CodeOf(readErr)))
		return nil, operationUnknownError(run.ID(), "credit memo creation", string(errs.CodeOf(readErr)))
	}
	creditmemo := *created
	if creditmemo.TotalQty == nil && len(in.Items) > 0 {
		// The read-back carried neither total_qty nor item rows; the quantity that
		// was refunded is exactly the quantity this call requested.
		total := 0.0
		for _, line := range in.Items {
			total += line.Qty
		}
		creditmemo.TotalQty = &total
	}
	result := &CreditmemoCreateResult{
		Creditmemo: creditmemo, InvoiceID: invoiceID,
		InvoiceLinked: creditmemo.InvoiceID != 0 && creditmemo.InvoiceID == invoiceID,
		OperationID:   run.ID(),
	}
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
	ShippingAmount     float64        `json:"shipping_amount"`
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

	// ShippingAmount and ShippingRefunded bound how much shipping a credit memo
	// may return, so a partial refund cannot sweep the whole amount (U-11).
	ShippingAmount   float64 `json:"shipping_amount"`
	ShippingRefunded float64 `json:"shipping_refunded"`
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
		EntityID         int     `json:"entity_id" jsonschema:"Magento entity id."`
		Currency         string  `json:"order_currency_code" jsonschema:"ISO 4217 code the amounts are expressed in."`
		GrandTotal       float64 `json:"grand_total" jsonschema:"Document total including shipping and tax."`
		ShippingAmount   float64 `json:"shipping_amount"`
		ShippingRefunded float64 `json:"shipping_refunded"`
		Items            []struct {
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
	state := &orderRefundState{
		EntityID: wire.EntityID, Currency: wire.Currency, GrandTotal: wire.GrandTotal,
		ShippingAmount: wire.ShippingAmount, ShippingRefunded: wire.ShippingRefunded,
	}
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
			remaining := math.Max(0, available)
			// U-21: the refundable quantity is data, not prose: put it in details so
			// a caller does not have to parse the sentence, and get the singular right.
			unit := "units"
			if remaining == 1 {
				unit = "unit"
			}
			return errs.New(errs.CodeInvalidArgument,
				fmt.Sprintf("order item %d has %g refundable %s, cannot refund %g",
					line.OrderItemID, remaining, unit, line.Qty)).
				WithDetail("order_item_id", strconv.Itoa(line.OrderItemID)).
				WithDetail("refundable_units", strconv.FormatFloat(remaining, 'f', -1, 64)).
				WithDetail("requested_units", strconv.FormatFloat(line.Qty, 'f', -1, 64))
		}
	}
	return nil
}

// remainingShipping is the shipping still refundable on the order.
func (s *orderRefundState) remainingShipping() float64 {
	remaining := s.ShippingAmount - s.ShippingRefunded
	if remaining < 0 || math.IsNaN(remaining) {
		return 0
	}
	return remaining
}

// coversAllRefundable reports whether the requested lines consume every
// remaining refundable unit of the order, which is the only case where omitting
// shipping_amount refunds the remaining shipping.
func (s *orderRefundState) coversAllRefundable(lines []DocumentLine) bool {
	requested := make(map[int]float64, len(lines))
	for _, line := range lines {
		requested[line.OrderItemID] += line.Qty
	}
	for _, item := range s.Refund {
		remaining := item.QtyInvoiced - item.QtyRefunded
		if remaining <= 1e-9 {
			continue
		}
		if requested[item.ItemID] < remaining-1e-9 {
			return false
		}
	}
	return true
}

// shippingRefund resolves the shipping amount this memo returns. An explicit
// shipping_amount wins (and is bounded by what is still refundable); otherwise a
// full refund returns the remaining shipping and a partial refund returns none,
// instead of letting Magento refund the whole amount (U-11).
func (s *orderRefundState) shippingRefund(in CreditmemoCreateInput) (float64, error) {
	remaining := s.remainingShipping()
	if in.ShippingAmount != nil {
		requested := *in.ShippingAmount
		if requested < 0 {
			return 0, invalidf("shipping_amount must not be negative")
		}
		if requested > remaining+1e-9 {
			return 0, errs.New(errs.CodeValidationFailed,
				"shipping_amount exceeds the shipping still refundable on this order").
				WithDetail("refundable_shipping", formatAmount(remaining)).
				WithDetail("currency", s.Currency)
		}
		return requested, nil
	}
	if s.coversAllRefundable(in.Items) {
		return remaining, nil
	}
	return 0, nil
}

// creditmemoTotal is the document total this memo should produce: the refunded
// line amount plus shipping and adjustments. The ledger records this figure, so
// a document that returns shipping is no longer under-reported (U-69).
func (s *orderRefundState) creditmemoTotal(in CreditmemoCreateInput, shipping float64) (Money, error) {
	lines, err := authorisedAmount(documentLinesToInvoiceLines(in.Items), s.Items, s.Currency)
	if err != nil {
		return Money{}, err
	}
	shippingMoney, err := moneyFromAmount(shipping, s.Currency)
	if err != nil {
		return Money{}, err
	}
	adjust := int64(math.Round(in.AdjustmentPositive*100)) - int64(math.Round(in.AdjustmentNegative*100))
	total := lines.Minor + shippingMoney.Minor + adjust
	if total < 0 {
		total = 0
	}
	return Money{Minor: total, Currency: lines.Currency}, nil
}

// refundableRemaining is the money still refundable on the order: every unit
// still refundable at its unit price, plus the remaining shipping.
func (s *orderRefundState) refundableRemaining() (Money, error) {
	priceByID := make(map[int]float64, len(s.Items))
	for _, item := range s.Items {
		priceByID[item.ItemID] = item.Price
	}
	var total int64
	for _, item := range s.Refund {
		remaining := item.QtyInvoiced - item.QtyRefunded
		if remaining <= 1e-9 {
			continue
		}
		unit, err := moneyFromAmount(priceByID[item.ItemID], s.Currency)
		if err != nil {
			return Money{}, err
		}
		qtyMilli := int64(math.Round(remaining * 1000))
		total += (unit.Minor*qtyMilli + 500) / 1000
	}
	shipping, err := moneyFromAmount(s.remainingShipping(), s.Currency)
	if err != nil {
		return Money{}, err
	}
	return Money{Minor: total + shipping.Minor, Currency: s.Currency}, nil
}

// checkRefundableAmount rejects a memo whose total exceeds what the order can
// still refund, before any upstream write (U-12).
func (s *orderRefundState) checkRefundableAmount(requested Money) error {
	refundable, err := s.refundableRemaining()
	if err != nil {
		return err
	}
	if requested.Minor <= refundable.Minor {
		return nil
	}
	return errs.New(errs.CodeValidationFailed,
		"the requested credit memo exceeds the amount still refundable").
		WithDetail("refundable_amount", formatAmount(float64(refundable.Minor)/100)).
		WithDetail("requested_amount", formatAmount(float64(requested.Minor)/100)).
		WithDetail("currency", refundable.Currency)
}

// formatAmount renders a currency amount with two decimals for an error detail.
func formatAmount(amount float64) string {
	return strconv.FormatFloat(amount, 'f', 2, 64)
}

// invoiceItemQuantities reads how many units of each order item the invoice
// actually invoiced, so a refund cannot exceed the invoice it is linked to
// (U-13).
func (s *Service) invoiceItemQuantities(ctx context.Context, invoiceID int) (map[int]float64, error) {
	raw, err := s.client.do(ctx, http.MethodGet, "/invoices/"+strconv.Itoa(invoiceID), nil, nil)
	if err != nil {
		// A store that does not expose the single-invoice route cannot be checked
		// at invoice level; the order-level quantity check still applies.
		if errs.CodeOf(err) == errs.CodeNotFound {
			return nil, nil
		}
		return nil, err
	}
	var payload struct {
		Items []struct {
			OrderItemID int     `json:"order_item_id"`
			Qty         float64 `json:"qty"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable invoice payload").WithCause(err)
	}
	out := make(map[int]float64, len(payload.Items))
	for _, item := range payload.Items {
		if item.OrderItemID <= 0 {
			continue
		}
		out[item.OrderItemID] += item.Qty
	}
	return out, nil
}

// checkInvoiceQuantities enforces the invoice-level quantity cap. A build that
// reports no item rows leaves the cap to the order-level check.
func checkInvoiceQuantities(lines []DocumentLine, invoiced map[int]float64) error {
	if len(invoiced) == 0 {
		return nil
	}
	for _, line := range lines {
		available := invoiced[line.OrderItemID]
		if line.Qty > available+1e-9 {
			return errs.New(errs.CodeValidationFailed,
				"the refund exceeds the quantity on the invoice it is linked to").
				WithDetail("order_item_id", strconv.Itoa(line.OrderItemID)).
				WithDetail("invoice_qty", strconv.FormatFloat(available, 'f', -1, 64)).
				WithDetail("requested_qty", strconv.FormatFloat(line.Qty, 'f', -1, 64))
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

// documentComment is Magento's sales Data\DocumentCommentInterface. Its
// is_visible_on_front member is declared as an int, so a JSON boolean made every
// commented refund or shipment fail with HTTP 400 (U-10).
type documentComment struct {
	Comment          string `json:"comment,omitempty"`
	IsVisibleOnFront int    `json:"is_visible_on_front"`
}

// shipmentOrderBody is the body of POST /order/{orderId}/ship
// (ShipOrderInterface::execute). The parameters are TOP-LEVEL: the route does not
// accept an {"entity": ...} wrapper. A wrapped body is silently ignored, Magento
// receives no items, and the whole order is shipped instead of the requested
// lines.
type shipmentOrderBody struct {
	Items         []invoiceItemBody   `json:"items"`
	Tracks        []shipmentTrackBody `json:"tracks,omitempty"`
	Comment       *documentComment    `json:"comment,omitempty"`
	AppendComment bool                `json:"appendComment"`
	Notify        bool                `json:"notify"`
}

type shipmentTrackBody struct {
	TrackNumber string `json:"track_number"`
	Title       string `json:"title,omitempty" jsonschema:"Display title for this entry."`
	CarrierCode string `json:"carrier_code"`
}

// refundOrderBody is the body of POST /order/{orderId}/refund
// (RefundOrderInterface::execute). The parameters are TOP-LEVEL: the route does
// not accept an {"entity": ...} wrapper. A wrapped body is silently ignored,
// Magento receives no items, and the whole invoice is refunded instead of the
// requested lines.
type refundOrderBody struct {
	Items         []invoiceItemBody     `json:"items"`
	Comment       *documentComment      `json:"comment,omitempty"`
	Arguments     *refundOrderArguments `json:"arguments,omitempty"`
	AppendComment bool                  `json:"appendComment"`
	Notify        bool                  `json:"notify"`
}

// refundOrderArguments carries the optional credit-memo adjustments; they are a
// parameter of the refund route, not top-level fields.
type refundOrderArguments struct {
	// ShippingAmount is the shipping refunded by this memo. Magento's
	// CreditmemoArgumentsInterface takes it here, and leaving it out is what made
	// a one-unit refund return the order's whole shipping (U-11).
	ShippingAmount     *float64 `json:"shipping_amount,omitempty"`
	AdjustmentPositive float64  `json:"adjustment_positive,omitempty"`
	AdjustmentNegative float64  `json:"adjustment_negative,omitempty"`
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
	if in.ShippingAmount != nil && *in.ShippingAmount < 0 {
		return invalidf("shipping_amount must not be negative")
	}
	return nil
}

// decodeCreatedDocumentID reads the id a Magento document-create route returns.
//
// POST /order/{id}/invoice, /ship and /refund answer with the new document's id as a
// JSON scalar — `"4"` or `4`, not the document object. Decoding that into the document
// struct failed, and the failure was reported as an unreadable response, so a
// successful invoice was recorded as operation_unknown and the caller was told not to
// retry. An object carrying entity_id is also accepted, for builds that answer that way.
func decodeCreatedDocumentID(raw []byte) (int, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0, errs.New(errs.CodeUpstreamUnavailable,
			"Magento accepted the write but returned no document id")
	}
	switch trimmed[0] {
	case '"':
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return 0, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable document id").WithCause(err)
		}
		id, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil {
			return 0, errs.New(errs.CodeUpstreamUnavailable, "Magento returned a non-numeric document id").
				WithCause(err)
		}
		return id, nil
	case '{':
		var object struct {
			EntityID     int `json:"entity_id"`
			InvoiceID    int `json:"invoice_id"`
			ShipmentID   int `json:"shipment_id"`
			CreditmemoID int `json:"creditmemo_id"`
		}
		if err := json.Unmarshal(trimmed, &object); err != nil {
			return 0, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable document payload").WithCause(err)
		}
		for _, candidate := range []int{object.EntityID, object.InvoiceID, object.ShipmentID, object.CreditmemoID} {
			if candidate > 0 {
				return candidate, nil
			}
		}
		return 0, errs.New(errs.CodeUpstreamUnavailable, "Magento returned a document payload with no id")
	default:
		var number json.Number
		if err := json.Unmarshal(trimmed, &number); err != nil {
			return 0, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable document id").WithCause(err)
		}
		id, err := strconv.Atoi(number.String())
		if err != nil {
			return 0, errs.New(errs.CodeUpstreamUnavailable, "Magento returned a non-numeric document id").WithCause(err)
		}
		return id, nil
	}
}
