// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// OrderStatusHistory is one entry of an order's status history.
type OrderStatusHistory struct {
	EntityID           int      `json:"entity_id" jsonschema:"Magento entity id."`
	ParentID           int      `json:"parent_id" jsonschema:"Parent category id; 0 for the root."`
	Status             string   `json:"status" jsonschema:"Current status."`
	Comment            string   `json:"comment"`
	IsCustomerNotified flexBool `json:"is_customer_notified"`
	IsVisibleOnFront   flexBool `json:"is_visible_on_front"`
	CreatedAt          string   `json:"created_at" jsonschema:"Store time, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
}

// flexBool decodes Magento's habit of returning booleans as 0/1 integers or
// JSON booleans depending on the endpoint.
type flexBool bool

// UnmarshalJSON accepts true/false and 0/1.
func (b *flexBool) UnmarshalJSON(data []byte) error {
	switch string(data) {
	case "true", "1":
		*b = true
	case "false", "0", "null":
		*b = false
	default:
		return errs.New(errs.CodeUpstreamUnavailable, "Magento returned a non-boolean flag")
	}
	return nil
}

// Invoice is the invoice projection this slice exposes.
type Invoice struct {
	EntityID       int     `json:"entity_id" jsonschema:"Magento entity id."`
	IncrementID    string  `json:"increment_id" jsonschema:"Human-facing document number, for example 000000123."`
	OrderID        int     `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	State          int     `json:"state" jsonschema:"Current state."`
	GrandTotal     float64 `json:"grand_total" jsonschema:"Document total including shipping and tax."`
	Subtotal       float64 `json:"subtotal" jsonschema:"Line subtotal before shipping, tax and discount."`
	DiscountAmount float64 `json:"discount_amount" jsonschema:"Discount applied, as a positive number."`
	ShippingAmount float64 `json:"shipping_amount" jsonschema:"Shipping charged."`
	TaxAmount      float64 `json:"tax_amount" jsonschema:"Tax charged."`
	TotalQty       float64 `json:"total_qty" jsonschema:"Total quantity on the document."`
	Currency       string  `json:"order_currency_code" jsonschema:"ISO 4217 code the amounts are expressed in."`
	CreatedAt      string  `json:"created_at" jsonschema:"Store time, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
	UpdatedAt      string  `json:"updated_at" jsonschema:"Store time, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
}

// ShipmentTrack is one tracking number of a shipment.
type ShipmentTrack struct {
	Title       string `json:"title" jsonschema:"Display title for this entry."`
	TrackNumber string `json:"track_number"`
	CarrierCode string `json:"carrier_code"`
}

// Shipment is the shipment projection this slice exposes.
type Shipment struct {
	EntityID    int             `json:"entity_id" jsonschema:"Magento entity id."`
	IncrementID string          `json:"increment_id" jsonschema:"Human-facing document number, for example 000000123."`
	OrderID     int             `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	TotalQty    float64         `json:"total_qty" jsonschema:"Total quantity on the document."`
	CreatedAt   string          `json:"created_at" jsonschema:"Store time, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
	UpdatedAt   string          `json:"updated_at" jsonschema:"Store time, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
	Tracks      []ShipmentTrack `json:"tracks,omitempty" jsonschema:"Tracking numbers attached to the shipment."`
}

// OrderStatusHistory returns the status history of one order.
func (s *Service) OrderStatusHistory(ctx context.Context, orderID int) ([]OrderStatusHistory, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if orderID <= 0 {
		return nil, invalidf("order_id must be a positive integer")
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/orders/"+strconv.Itoa(orderID)+"/comments", nil, nil)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Items []OrderStatusHistory `json:"items" jsonschema:"Entries returned by this call."`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Items != nil {
		return envelope.Items, nil
	}
	var history []OrderStatusHistory
	if err := json.Unmarshal(raw, &history); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable order history payload").WithCause(err)
	}
	if history == nil {
		history = []OrderStatusHistory{}
	}
	return history, nil
}

// OrderInvoices lists the invoices of one order.
func (s *Service) OrderInvoices(ctx context.Context, orderID int) ([]Invoice, int, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, 0, err
	}
	if orderID <= 0 {
		return nil, 0, invalidf("order_id must be a positive integer")
	}
	return scanOrderDocuments[Invoice](ctx, s, orderID, "/invoices", "invoice")
}

// GetInvoice returns one invoice by id.
func (s *Service) GetInvoice(ctx context.Context, invoiceID int) (*Invoice, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if invoiceID <= 0 {
		return nil, invalidf("invoice_id must be a positive integer")
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/invoices/"+strconv.Itoa(invoiceID), nil, nil)
	if err != nil {
		return nil, err
	}
	var invoice Invoice
	if err := json.Unmarshal(raw, &invoice); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable invoice payload").WithCause(err)
	}
	return &invoice, nil
}

// OrderShipments lists the shipments of one order.
func (s *Service) OrderShipments(ctx context.Context, orderID int) ([]Shipment, int, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, 0, err
	}
	if orderID <= 0 {
		return nil, 0, invalidf("order_id must be a positive integer")
	}
	return scanOrderDocuments[Shipment](ctx, s, orderID, "/shipments", "shipment")
}

// GetShipment returns one shipment by id.
func (s *Service) GetShipment(ctx context.Context, shipmentID int) (*Shipment, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if shipmentID <= 0 {
		return nil, invalidf("shipment_id must be a positive integer")
	}
	// Magento's read-by-id routes are inconsistent: shipments and credit memos are
	// singular, invoices are plural. /shipments/{id} is a 404 on a standard instance.
	raw, err := s.client.do(ctx, http.MethodGet, "/shipment/"+strconv.Itoa(shipmentID), nil, nil)
	if err != nil {
		return nil, err
	}
	var shipment Shipment
	if err := json.Unmarshal(raw, &shipment); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable shipment payload").WithCause(err)
	}
	return &shipment, nil
}

func (s *Service) requireOrderService() error {
	if s == nil || s.client == nil {
		return errs.New(errs.CodeInternal, "Magento order service is not configured")
	}
	return nil
}

// Creditmemo is the creditmemo projection this slice exposes.
type Creditmemo struct {
	EntityID       int     `json:"entity_id" jsonschema:"Magento entity id."`
	IncrementID    string  `json:"increment_id" jsonschema:"Human-facing document number, for example 000000123."`
	OrderID        int     `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	State          int     `json:"state" jsonschema:"Current state."`
	GrandTotal     float64 `json:"grand_total" jsonschema:"Document total including shipping and tax."`
	Subtotal       float64 `json:"subtotal" jsonschema:"Line subtotal before shipping, tax and discount."`
	DiscountAmount float64 `json:"discount_amount" jsonschema:"Discount applied, as a positive number."`
	ShippingAmount float64 `json:"shipping_amount" jsonschema:"Shipping charged."`
	TaxAmount      float64 `json:"tax_amount" jsonschema:"Tax charged."`
	Adjustment     float64 `json:"adjustment" jsonschema:"Manual adjustment applied to the credit memo total."`
	// TotalQty is a pointer because Magento's creditmemo resource does not always
	// report it. A missing field must be omitted, never rendered as a false zero
	// that reads as "nothing was refunded".
	TotalQty  *float64 `json:"total_qty,omitempty" jsonschema:"Total quantity on the document; omitted when Magento does not report it."`
	Currency  string   `json:"order_currency_code" jsonschema:"ISO 4217 code the amounts are expressed in."`
	CreatedAt string   `json:"created_at" jsonschema:"Store time, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
	UpdatedAt string   `json:"updated_at" jsonschema:"Store time, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
}

// OrderCreditmemos lists the credit memos of one order.
func (s *Service) OrderCreditmemos(ctx context.Context, orderID int) ([]Creditmemo, int, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, 0, err
	}
	if orderID <= 0 {
		return nil, 0, invalidf("order_id must be a positive integer")
	}
	return scanOrderDocuments[Creditmemo](ctx, s, orderID, "/creditmemos", "credit memo")
}

// GetCreditmemo returns one credit memo by id.
func (s *Service) GetCreditmemo(ctx context.Context, creditmemoID int) (*Creditmemo, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if creditmemoID <= 0 {
		return nil, invalidf("creditmemo_id must be a positive integer")
	}
	// Singular, like shipments; invoices are the plural exception.
	raw, err := s.client.do(ctx, http.MethodGet, "/creditmemo/"+strconv.Itoa(creditmemoID), nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeCreditmemo(raw)
}

// decodeCreditmemo decodes one credit memo. Magento omits total_qty on this
// resource, so when it is absent the quantity is derived from the item rows:
// reporting 0 for a document that refunded units is worse than reporting the
// truth, and omitting the field when there are no rows is honest.
func decodeCreditmemo(raw []byte) (*Creditmemo, error) {
	var wire struct {
		Creditmemo
		TotalQty *float64 `json:"total_qty"`
		Items    []struct {
			Qty float64 `json:"qty"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable creditmemo payload").WithCause(err)
	}
	creditmemo := wire.Creditmemo
	switch {
	case wire.TotalQty != nil:
		creditmemo.TotalQty = wire.TotalQty
	case len(wire.Items) > 0:
		var total float64
		for _, item := range wire.Items {
			total += item.Qty
		}
		creditmemo.TotalQty = &total
	}
	return &creditmemo, nil
}

// documentEmailPaths maps each sales document to its email endpoint.
var documentEmailPaths = map[string]string{ // #nosec G101 -- document kinds, not credentials
	// Same singular/plural split as the read routes: invoices are plural, shipments
	// and credit memos singular. /shipments/{id}/emails and /creditmemos/{id}/emails
	// are 404 on a standard instance.
	"invoice":    "/invoices/%d/emails",
	"shipment":   "/shipment/%d/emails",
	"creditmemo": "/creditmemo/%d/emails",
}

// SendDocumentEmail sends a sales document email (invoice, shipment, or credit
// memo). Magento sends to the order's customer email; the gateway never accepts
// a recipient address.
func (s *Service) SendDocumentEmail(ctx context.Context, kind string, documentID int) error {
	if err := s.requireOrderService(); err != nil {
		return err
	}
	path, ok := documentEmailPaths[kind]
	if !ok {
		return invalidf("document kind must be invoice, shipment, or creditmemo")
	}
	if documentID <= 0 {
		return invalidf("document_id must be a positive integer")
	}
	_, err := s.client.do(ctx, http.MethodPost, fmt.Sprintf(path, documentID), nil, nil)
	return err
}

// maxOrderDocuments bounds a document scan for one order. It is far above any real
// order; the point is that a bound exists and is reported rather than silently
// cutting the list at one page.
const maxOrderDocuments = 1000

// orderSearchQuery filters a sales document collection by order_id.
func orderSearchQuery(orderID, page int) url.Values {
	query := make(url.Values)
	query.Set("searchCriteria[filter_groups][0][filters][0][field]", "order_id")
	query.Set("searchCriteria[filter_groups][0][filters][0][value]", strconv.Itoa(orderID))
	query.Set("searchCriteria[filter_groups][0][filters][0][condition_type]", "eq")
	query.Set("searchCriteria[pageSize]", strconv.Itoa(orderDocumentPageSize))
	query.Set("searchCriteria[currentPage]", strconv.Itoa(page))
	return query
}

// orderDocumentPageSize is one page of a document scan.
const orderDocumentPageSize = 100

// scanOrderDocuments pages through one order's documents.
//
// The previous version requested a single page of 100 and returned it with no total
// and no flag, so an order with more documents was silently cut short. It now pages
// until the store reports no more, up to a bound, and returns the total the store
// reported so the caller can see whether anything was left out.
func scanOrderDocuments[T any](ctx context.Context, s *Service, orderID int, path, field string) ([]T, int, error) {
	var collected []T
	total := 0
	for page := 1; ; page++ {
		raw, err := s.client.do(ctx, http.MethodGet, path, orderSearchQuery(orderID, page), nil)
		if err != nil {
			return nil, 0, err
		}
		var envelope struct {
			Items      []T `json:"items"`
			TotalCount int `json:"total_count"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, 0, errs.New(errs.CodeUpstreamUnavailable,
				"Magento returned an unreadable "+field+" payload").WithCause(err)
		}
		if envelope.TotalCount > total {
			total = envelope.TotalCount
		}
		collected = append(collected, envelope.Items...)
		if len(envelope.Items) < orderDocumentPageSize || len(collected) >= maxOrderDocuments {
			break
		}
	}
	if total < len(collected) {
		total = len(collected)
	}
	return collected, total, nil
}
