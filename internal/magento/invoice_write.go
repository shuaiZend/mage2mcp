// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const maxInvoiceItems = 100

// InvoiceLine is one invoice line: an order item and the quantity to invoice.
type InvoiceLine struct {
	OrderItemID int     `json:"order_item_id"`
	Qty         float64 `json:"qty"`
}

// InvoiceCreateInput is one invoice creation request.
type InvoiceCreateInput struct {
	OrderID int           `json:"order_id"`
	Capture bool          `json:"capture"`
	Items   []InvoiceLine `json:"items"`
}

// CreateInvoice invoices an order with POST /invoices.
//
// capture=false is the default and does not charge the payment method; callers
// must opt in explicitly. The tool is confirmation-gated.
func (s *Service) CreateInvoice(ctx context.Context, in InvoiceCreateInput) (*Invoice, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if err := validateInvoiceCreate(in); err != nil {
		return nil, err
	}
	items := make([]invoiceItemBody, 0, len(in.Items))
	for _, line := range in.Items {
		items = append(items, invoiceItemBody(line))
	}
	body := invoiceCreateBody{Entity: invoiceEntityBody{
		OrderID: in.OrderID, Capture: in.Capture, Items: items,
	}}
	raw, err := s.client.do(ctx, http.MethodPost, "/invoices", nil, body)
	if err != nil {
		return nil, err
	}
	var invoice Invoice
	if err := json.Unmarshal(raw, &invoice); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable invoice payload").WithCause(err)
	}
	return &invoice, nil
}

type invoiceCreateBody struct {
	Entity invoiceEntityBody `json:"entity"`
}

type invoiceEntityBody struct {
	OrderID int               `json:"order_id"`
	Capture bool              `json:"capture"`
	Items   []invoiceItemBody `json:"items"`
}

type invoiceItemBody struct {
	OrderItemID int     `json:"order_item_id"`
	Qty         float64 `json:"qty"`
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
