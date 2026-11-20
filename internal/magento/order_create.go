// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	maxOrderItems    = 50
	maxAddressStreet = 4
	maxAddressBytes  = 255
)

// OrderAddressInput is one billing or shipping address for order creation.
type OrderAddressInput struct {
	Firstname string   `json:"firstname"`
	Lastname  string   `json:"lastname"`
	Street    []string `json:"street"`
	City      string   `json:"city"`
	Postcode  string   `json:"postcode"`
	CountryID string   `json:"country_id"`
	Telephone string   `json:"telephone"`
	Region    string   `json:"region,omitempty"`
}

// OrderItemInput is one line to add to the cart.
type OrderItemInput struct {
	SKU string  `json:"sku"`
	Qty float64 `json:"qty"`
}

// CreateOrderInput describes one order to place for an existing customer.
type CreateOrderInput struct {
	CustomerID          int
	StoreID             int
	Items               []OrderItemInput
	BillingAddress      OrderAddressInput
	ShippingAddress     OrderAddressInput
	ShippingCarrierCode string
	ShippingMethodCode  string
	PaymentMethod       string
}

// CreateOrder places an order through Magento's admin cart flow:
// customer cart -> items -> shipping information -> order.
//
// Totals are always computed by Magento; the tool never accepts client-supplied
// amounts. Any failure leaves a cart behind (Magento has no rollback for the
// cart API); the error names the stage so the operator can clean up.
func (s *Service) CreateOrder(ctx context.Context, in CreateOrderInput) (*Order, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if err := validateCreateOrder(in); err != nil {
		return nil, err
	}
	raw, err := s.client.do(ctx, http.MethodPost, "/customers/"+strconv.Itoa(in.CustomerID)+"/carts", nil, nil)
	if err != nil {
		return nil, writeStageError("cart", nil, "", err)
	}
	var cartID int
	if err := json.Unmarshal(raw, &cartID); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable cart id").WithCause(err)
	}
	for _, item := range in.Items {
		body := cartItemBody{CartItem: cartItem{
			SKU: item.SKU, Qty: item.Qty, QuoteID: strconv.Itoa(cartID),
		}}
		if _, err := s.client.do(ctx, http.MethodPost, "/carts/"+strconv.Itoa(cartID)+"/items", nil, body); err != nil {
			return nil, writeStageError("cart_items", nil, item.SKU, err)
		}
	}
	info := addressInformationBody{AddressInformation: addressInformation{
		ShippingAddress:     in.ShippingAddress.toPayload(),
		BillingAddress:      in.BillingAddress.toPayload(),
		ShippingCarrierCode: in.ShippingCarrierCode,
		ShippingMethodCode:  in.ShippingMethodCode,
	}}
	if _, err := s.client.do(ctx, http.MethodPost, "/carts/"+strconv.Itoa(cartID)+"/shipping-information", nil, info); err != nil {
		return nil, writeStageError("shipping_information", nil, "", err)
	}
	orderBody := placeOrderBody{PaymentMethod: paymentMethod{Method: in.PaymentMethod}}
	raw, err = s.client.do(ctx, http.MethodPost, "/carts/"+strconv.Itoa(cartID)+"/order", nil, orderBody)
	if err != nil {
		cleanup := s.cleanupCart(ctx, cartID)
		if errs.CodeOf(err) == errs.CodeNotFound {
			return nil, errs.New(errs.CodeUnsupportedCapability,
				"this Magento instance does not expose the cart place-order route; order creation is unsupported here").
				WithDetail("stage", "place_order").
				WithDetail("cart_id", strconv.Itoa(cartID)).
				WithDetail("cart_cleanup", cleanup)
		}
		stageErr := writeStageError("place_order", nil, "", err)
		var taxonomy *errs.Error
		if errors.As(stageErr, &taxonomy) {
			taxonomy.Details["cart_cleanup"] = cleanup
		}
		return nil, stageErr
	}
	var order Order
	if err := json.Unmarshal(raw, &order); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable order payload").WithCause(err)
	}
	return &order, nil
}

type cartItemBody struct {
	CartItem cartItem `json:"cartItem"`
}

type cartItem struct {
	SKU     string  `json:"sku"`
	Qty     float64 `json:"qty"`
	QuoteID string  `json:"quote_id"`
}

type addressInformationBody struct {
	AddressInformation addressInformation `json:"addressInformation"`
}

type addressInformation struct {
	ShippingAddress     addressPayload `json:"shipping_address"`
	BillingAddress      addressPayload `json:"billing_address"`
	ShippingCarrierCode string         `json:"shipping_carrier_code"`
	ShippingMethodCode  string         `json:"shipping_method_code"`
}

type addressPayload struct {
	Firstname string   `json:"firstname"`
	Lastname  string   `json:"lastname"`
	Street    []string `json:"street"`
	City      string   `json:"city"`
	Postcode  string   `json:"postcode"`
	CountryID string   `json:"country_id"`
	Telephone string   `json:"telephone"`
	Region    string   `json:"region,omitempty"`
}

func (a OrderAddressInput) toPayload() addressPayload {
	return addressPayload(a)
}

type placeOrderBody struct {
	PaymentMethod paymentMethod `json:"paymentMethod"`
}

type paymentMethod struct {
	Method string `json:"method"`
}

func validateCreateOrder(in CreateOrderInput) error {
	if in.CustomerID <= 0 {
		return invalidf("customer_id must be a positive integer")
	}
	if in.StoreID < 0 {
		return invalidf("store_id must not be negative")
	}
	if len(in.Items) == 0 {
		return invalidf("at least one item is required")
	}
	if len(in.Items) > maxOrderItems {
		return invalidf("at most %d items are allowed per order", maxOrderItems)
	}
	seen := make(map[string]struct{}, len(in.Items))
	for _, item := range in.Items {
		if err := validateSKU(item.SKU); err != nil {
			return err
		}
		if item.Qty <= 0 {
			return invalidf("item quantity must be greater than zero")
		}
		if _, duplicate := seen[item.SKU]; duplicate {
			return invalidf("sku %q is duplicated", item.SKU)
		}
		seen[item.SKU] = struct{}{}
	}
	if err := validateOrderAddress("billing_address", in.BillingAddress); err != nil {
		return err
	}
	if err := validateOrderAddress("shipping_address", in.ShippingAddress); err != nil {
		return err
	}
	if !methodRE.MatchString(in.ShippingCarrierCode) || !methodRE.MatchString(in.ShippingMethodCode) {
		return invalidf("shipping carrier and method must match %s", methodRE.String())
	}
	if !methodRE.MatchString(in.PaymentMethod) {
		return invalidf("payment_method must match %s", methodRE.String())
	}
	return nil
}

var methodRE = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

func validateOrderAddress(field string, address OrderAddressInput) error {
	required := map[string]string{
		"firstname": address.Firstname, "lastname": address.Lastname,
		"city": address.City, "postcode": address.Postcode, "telephone": address.Telephone,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return invalidf("%s.%s is required", field, name)
		}
		if len(value) > maxAddressBytes || strings.ContainsFunc(value, unicode.IsControl) {
			return invalidf("%s.%s must be at most %d bytes without control characters", field, name, maxAddressBytes)
		}
	}
	if len(address.CountryID) != 2 || strings.ToUpper(address.CountryID) != address.CountryID {
		return invalidf("%s.country_id must be a two-letter uppercase ISO country code", field)
	}
	if len(address.Street) == 0 || len(address.Street) > maxAddressStreet {
		return invalidf("%s.street must have 1-%d lines", field, maxAddressStreet)
	}
	for _, line := range address.Street {
		if strings.TrimSpace(line) == "" || len(line) > maxAddressBytes {
			return invalidf("%s.street lines must be non-empty and at most %d bytes", field, maxAddressBytes)
		}
	}
	return nil
}

// cleanupCart best-effort deletes an abandoned cart after a failed placement.
func (s *Service) cleanupCart(ctx context.Context, cartID int) string {
	if _, err := s.client.do(ctx, http.MethodDelete, "/carts/"+strconv.Itoa(cartID), nil, nil); err != nil {
		return "failed: " + string(errs.CodeOf(err))
	}
	return "deleted"
}
