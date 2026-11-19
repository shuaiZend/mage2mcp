// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// OrderStatusHistory is one entry of an order's status history.
type OrderStatusHistory struct {
	EntityID           int      `json:"entity_id"`
	ParentID           int      `json:"parent_id"`
	Status             string   `json:"status"`
	Comment            string   `json:"comment"`
	IsCustomerNotified flexBool `json:"is_customer_notified"`
	IsVisibleOnFront   flexBool `json:"is_visible_on_front"`
	CreatedAt          string   `json:"created_at"`
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
	EntityID       int     `json:"entity_id"`
	IncrementID    string  `json:"increment_id"`
	OrderID        int     `json:"order_id"`
	State          int     `json:"state"`
	GrandTotal     float64 `json:"grand_total"`
	Subtotal       float64 `json:"subtotal"`
	DiscountAmount float64 `json:"discount_amount"`
	ShippingAmount float64 `json:"shipping_amount"`
	TaxAmount      float64 `json:"tax_amount"`
	TotalQty       float64 `json:"total_qty"`
	Currency       string  `json:"order_currency_code"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

// ShipmentTrack is one tracking number of a shipment.
type ShipmentTrack struct {
	Title       string `json:"title"`
	TrackNumber string `json:"track_number"`
	CarrierCode string `json:"carrier_code"`
}

// Shipment is the shipment projection this slice exposes.
type Shipment struct {
	EntityID    int             `json:"entity_id"`
	IncrementID string          `json:"increment_id"`
	OrderID     int             `json:"order_id"`
	TotalQty    float64         `json:"total_qty"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
	Tracks      []ShipmentTrack `json:"tracks,omitempty"`
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
func (s *Service) OrderInvoices(ctx context.Context, orderID int) ([]Invoice, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if orderID <= 0 {
		return nil, invalidf("order_id must be a positive integer")
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/orders/"+strconv.Itoa(orderID)+"/invoices", nil, nil)
	if err != nil {
		return nil, err
	}
	var invoices []Invoice
	if err := json.Unmarshal(raw, &invoices); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable invoice payload").WithCause(err)
	}
	if invoices == nil {
		invoices = []Invoice{}
	}
	return invoices, nil
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
func (s *Service) OrderShipments(ctx context.Context, orderID int) ([]Shipment, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if orderID <= 0 {
		return nil, invalidf("order_id must be a positive integer")
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/orders/"+strconv.Itoa(orderID)+"/shipments", nil, nil)
	if err != nil {
		return nil, err
	}
	var shipments []Shipment
	if err := json.Unmarshal(raw, &shipments); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable shipment payload").WithCause(err)
	}
	if shipments == nil {
		shipments = []Shipment{}
	}
	return shipments, nil
}

// GetShipment returns one shipment by id.
func (s *Service) GetShipment(ctx context.Context, shipmentID int) (*Shipment, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if shipmentID <= 0 {
		return nil, invalidf("shipment_id must be a positive integer")
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/shipments/"+strconv.Itoa(shipmentID), nil, nil)
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
	EntityID       int     `json:"entity_id"`
	IncrementID    string  `json:"increment_id"`
	OrderID        int     `json:"order_id"`
	State          int     `json:"state"`
	GrandTotal     float64 `json:"grand_total"`
	Subtotal       float64 `json:"subtotal"`
	DiscountAmount float64 `json:"discount_amount"`
	ShippingAmount float64 `json:"shipping_amount"`
	TaxAmount      float64 `json:"tax_amount"`
	Adjustment     float64 `json:"adjustment"`
	TotalQty       float64 `json:"total_qty"`
	Currency       string  `json:"order_currency_code"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

// OrderCreditmemos lists the credit memos of one order.
func (s *Service) OrderCreditmemos(ctx context.Context, orderID int) ([]Creditmemo, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if orderID <= 0 {
		return nil, invalidf("order_id must be a positive integer")
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/orders/"+strconv.Itoa(orderID)+"/creditmemos", nil, nil)
	if err != nil {
		return nil, err
	}
	var creditmemos []Creditmemo
	if err := json.Unmarshal(raw, &creditmemos); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable creditmemo payload").WithCause(err)
	}
	if creditmemos == nil {
		creditmemos = []Creditmemo{}
	}
	return creditmemos, nil
}

// GetCreditmemo returns one credit memo by id.
func (s *Service) GetCreditmemo(ctx context.Context, creditmemoID int) (*Creditmemo, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if creditmemoID <= 0 {
		return nil, invalidf("creditmemo_id must be a positive integer")
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/creditmemos/"+strconv.Itoa(creditmemoID), nil, nil)
	if err != nil {
		return nil, err
	}
	var creditmemo Creditmemo
	if err := json.Unmarshal(raw, &creditmemo); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable creditmemo payload").WithCause(err)
	}
	return &creditmemo, nil
}

// documentEmailPaths maps each sales document to its email endpoint.
var documentEmailPaths = map[string]string{ // #nosec G101 -- document kinds, not credentials
	"invoice":    "/invoices/%d/emails",
	"shipment":   "/shipments/%d/emails",
	"creditmemo": "/creditmemos/%d/emails",
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
