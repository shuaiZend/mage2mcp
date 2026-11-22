// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

const (
	maxOrderItems    = 50
	maxAddressStreet = 4
	maxAddressBytes  = 255

	// orderReconcileWindow bounds the order lookup used to resolve an ambiguous
	// place-order outcome. It is deliberately short: it only has to cover the
	// seconds between the cart being placed and the reconciliation read.
	orderReconcileWindow = 15 * time.Minute
	// reconcileReads is how many times an ambiguous outcome is re-read before an
	// empty result is treated as proof that nothing was placed. One empty read is
	// not evidence: a lagging replica would turn it into a duplicate order.
	reconcileReads = 6
	// reconciledNotApplied is the ledger error code for an operation that an
	// ambiguous failure left in doubt and a reconciliation read proved did not
	// happen. It is not in the taxonomy's ambiguous set, so the ledger accepts it
	// as a definite failure.
	reconciledNotApplied = "reconciled_not_applied"
)

// OrderAddressInput is one billing or shipping address for order creation.
type OrderAddressInput struct {
	Firstname string   `json:"firstname" jsonschema:"Given name."`
	Lastname  string   `json:"lastname" jsonschema:"Family name."`
	Street    []string `json:"street" jsonschema:"Street lines, most specific first."`
	City      string   `json:"city" jsonschema:"City or locality."`
	Postcode  string   `json:"postcode" jsonschema:"Postal code as the destination country writes it."`
	CountryID string   `json:"country_id" jsonschema:"ISO 3166-1 alpha-2 country code, uppercase."`
	Telephone string   `json:"telephone" jsonschema:"Contact phone number."`
	Region    string   `json:"region,omitempty" jsonschema:"Region name."`
	// RegionID is the numeric region (state/province) id. Magento requires it for
	// countries that have a region directory — Poland among them — and rejects the
	// address with `"regionId" is required` without it.
	RegionID int `json:"region_id,omitempty" jsonschema:"Numeric region id; Magento requires it for countries with a region directory."`
}

// OrderItemInput is one line to add to the cart.
type OrderItemInput struct {
	SKU string  `json:"sku" jsonschema:"Product SKU."`
	Qty float64 `json:"qty" jsonschema:"Quantity at this source."`
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

	// IdempotencyKey makes the call replayable: repeating it with the same
	// arguments returns the recorded order instead of placing a second one.
	IdempotencyKey string
	// Instance is the configured instance id recorded in the operation ledger.
	Instance string
}

// CreateOrderResult is the recorded outcome of one order placement.
type CreateOrderResult struct {
	Order Order `json:"order" jsonschema:"The order that was read."`
	// CartID is the Magento cart the placement used. It is recorded because the
	// cart, not the order, is what a disputed placement has to be traced back
	// through: Magento reuses a customer's active cart, so the cart id is the only
	// handle that explains why an order holds items nobody asked for (t57).
	CartID      int    `json:"cart_id,omitempty" jsonschema:"Magento cart id the placement used; the handle for tracing a disputed order."`
	OperationID string `json:"operation_id,omitempty" jsonschema:"Ledger operation id, for polling an unknown outcome."`
	Replayed    bool   `json:"replayed,omitempty" jsonschema:"True when a previous result was replayed for the same idempotency_key, so nothing new happened."`
	// Reconciled reports that an ambiguous place-order failure was resolved by
	// reading the customer's orders back rather than by trusting the response.
	Reconciled bool `json:"reconciled,omitempty" jsonschema:"True when the result was confirmed by reading the store back rather than by its response."`
}

// CreateOrder places an order through Magento's admin cart flow:
// customer cart -> items -> shipping information -> order.
//
// Totals are always computed by Magento; the tool never accepts client-supplied
// amounts. The operation is recorded in the ledger before the cart is created,
// so a retry with the same idempotency key replays the recorded order instead of
// placing a second one.
//
// An ambiguous place-order outcome (timeout, transport failure, 5xx) does not
// delete the cart — the cart id is the only handle for reconciliation — and it
// is resolved by reading the customer's orders back (customer + created_at
// window + cart total): exactly one match is the order, no match proves the
// order was not placed, and several matches are recorded as unknown for a human.
func (s *Service) CreateOrder(ctx context.Context, in CreateOrderInput) (*CreateOrderResult, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if err := validateCreateOrder(in); err != nil {
		return nil, err
	}
	if in.IdempotencyKey != "" && len(in.IdempotencyKey) > 256 {
		return nil, invalidf("idempotency_key must be at most 256 characters")
	}
	ops, err := configuredOperations()
	if err != nil {
		return nil, err
	}
	run, begun, err := ops.Begin(ctx, OperationRequest{
		Tool: "magento.order.create", Instance: s.operationInstanceID(in.Instance),
		Subject: callerSubject(ctx), IdempotencyKey: in.IdempotencyKey,
		// The order does not exist yet, so there is no order id to record: the
		// only entity that provably exists when the placement begins is the
		// customer. Recording the customer id under entity_type=order made an
		// operator look up order 5 (someone else's order) for a row that was
		// about customer 5 (U-70). The order id is written to upstream_ref when
		// the placement succeeds.
		EntityType: storage.EntityCustomer, EntityID: strconv.Itoa(in.CustomerID),
		// Magento computes the order total; the cart total is read only for
		// reconciliation, so the intent carries no amount.
		Args: orderCreateArgsFrom(in),
	})
	if err != nil {
		return nil, err
	}
	if begun != nil && begun.Decision == storage.DecisionReplay {
		var recorded CreateOrderResult
		if err := Replay(begun, &recorded); err != nil {
			return nil, err
		}
		recorded.Replayed = true
		return &recorded, nil
	}

	// Magento answers POST /customers/{id}/carts with the customer's ACTIVE cart,
	// not a new one. Two callers for one customer therefore share a cart, and a
	// cart left behind by an earlier failed call is picked up again: either way the
	// next order silently carries items nobody asked for (t57: 3 of 6 concurrent
	// rounds produced a mixed order, and a cart left over REST produced order 25).
	//
	// Two gates, because they catch different halves:
	//   1. serialise per (instance, customer) so two calls cannot interleave
	//      between "the cart is empty" and "the order is placed";
	//   2. refuse a cart that already holds items instead of absorbing them.
	// Gate 2 alone turns the concurrency race into a failure (better than a mixed
	// order, but still a failure); gate 1 alone cannot see a cart left by another
	// process or by a crash between add-item and place-order.
	unlock, lockErr := lockOrderCustomer(ctx, s.operationInstanceID(in.Instance)+"/"+strconv.Itoa(in.CustomerID))
	if lockErr != nil {
		_ = run.Fail(ctx, string(errs.CodeOf(lockErr)))
		return nil, lockErr
	}
	defer unlock()

	raw, err := s.client.do(ctx, http.MethodPost, "/customers/"+strconv.Itoa(in.CustomerID)+"/carts", nil, nil)
	if err != nil {
		_ = run.Fail(ctx, string(errs.CodeOf(err)))
		return nil, writeStageError("cart", nil, "", err)
	}
	var cartID int
	if err := json.Unmarshal(raw, &cartID); err != nil {
		_ = run.Unknown(ctx, string(errs.CodeUpstreamUnavailable))
		return nil, operationUnknownError(run.ID(), "order placement", string(errs.CodeUpstreamUnavailable))
	}
	slog.Default().Info("order cart acquired",
		"instance", in.Instance, "customer_id", in.CustomerID, "cart_id", cartID)
	if err := s.requireEmptyCart(ctx, cartID); err != nil {
		_ = run.Fail(ctx, string(errs.CodeOf(err)))
		return nil, err
	}
	for _, item := range in.Items {
		body := cartItemBody{CartItem: cartItem{
			SKU: item.SKU, Qty: item.Qty, QuoteID: strconv.Itoa(cartID),
		}}
		if _, err := s.client.do(ctx, http.MethodPost, "/carts/"+strconv.Itoa(cartID)+"/items", nil, body); err != nil {
			_ = run.Fail(ctx, string(errs.CodeOf(err)))
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
		_ = run.Fail(ctx, string(errs.CodeOf(err)))
		return nil, writeStageError("shipping_information", nil, "", err)
	}
	orderBody := placeOrderBody{PaymentMethod: paymentMethod{Method: in.PaymentMethod}}
	// Magento's salesOrderManagementV1 places an order with PUT; POST is not a
	// route at all and returns 404, which used to be reported as "the store does
	// not expose the cart place-order route".
	raw, placeErr := s.client.do(ctx, http.MethodPut, "/carts/"+strconv.Itoa(cartID)+"/order", nil, orderBody)
	switch classifyOperationError(placeErr) {
	case operationNotApplied:
		_ = run.Fail(ctx, string(errs.CodeOf(placeErr)))
		cleanup := s.cleanupCart(ctx, cartID)
		if errs.CodeOf(placeErr) == errs.CodeNotFound {
			return nil, errs.New(errs.CodeUnsupportedCapability,
				"this Magento instance does not expose the cart place-order route; order creation is unsupported here").
				WithDetail("stage", "place_order").
				WithDetail("cart_id", strconv.Itoa(cartID)).
				WithDetail("cart_cleanup", cleanup)
		}
		stageErr := writeStageError("place_order", nil, "", placeErr)
		var taxonomy *errs.Error
		if errors.As(stageErr, &taxonomy) {
			taxonomy.Details["cart_cleanup"] = cleanup
		}
		return nil, stageErr
	case operationAmbiguous:
		// The cart is deliberately kept: it is the only handle for reconciliation.
		return s.reconcilePlacedOrder(ctx, run, in, cartID, placeErr)
	}
	// PUT /carts/{id}/order answers with the new order's id as a JSON scalar, not the
	// order. Decoding it as an object failed, and that failure was reported as an
	// unreadable response, so every successful placement was recorded as
	// operation_unknown — the order existed and the caller was told not to retry.
	orderID, idErr := decodeCreatedDocumentID(raw)
	if idErr != nil {
		// The id is unreadable, so the placement outcome is genuinely unknown.
		return s.reconcilePlacedOrder(ctx, run, in, cartID, idErr)
	}
	placed, readErr := s.GetOrder(ctx, orderID)
	if readErr != nil {
		return s.reconcilePlacedOrder(ctx, run, in, cartID, readErr)
	}
	order := *placed
	result := &CreateOrderResult{Order: order, OperationID: run.ID(), CartID: cartID}
	if err := run.Succeed(ctx, orderRef(order), result); err != nil {
		return nil, err
	}
	return result, nil
}

// reconcilePlacedOrder resolves an ambiguous place-order outcome by reading the
// customer's recent orders back. It never deletes the cart and never retries the
// placement: the ledger records exactly one of succeeded / failed / unknown.
//
// The match is exact, not statistical: a candidate order is ours only when its
// quote_id equals the cart this operation created. A matching total is not
// evidence — two orders for the same customer can share a total — so a candidate
// that cannot be tied to our cart is reported as UNKNOWN with its id, never as a
// success. An empty read is repeated a bounded number of times; only agreeing
// empty reads are treated as proof that nothing was placed.
func (s *Service) reconcilePlacedOrder(ctx context.Context, run *OperationRun, in CreateOrderInput, cartID int, placeErr error) (*CreateOrderResult, error) {
	upstreamCode := string(errs.CodeOf(placeErr))
	unknown := func(reason string, details ...string) (*CreateOrderResult, error) {
		_ = run.Unknown(ctx, upstreamCode)
		e := operationUnknownError(run.ID(), "order placement", upstreamCode).
			WithDetail("cart_id", strconv.Itoa(cartID)).
			WithDetail("reconcile", reason)
		for i := 0; i+1 < len(details); i += 2 {
			e = e.WithDetail(details[i], details[i+1])
		}
		return nil, e
	}
	since := time.Now().UTC().Add(-orderReconcileWindow)
	var candidates []orderCandidate
	for attempt := 1; attempt <= reconcileReads; attempt++ {
		if attempt > 1 {
			if err := sleepContext(ctx, reconcileBackoff); err != nil {
				return unknown("the reconciliation read was canceled")
			}
		}
		found, err := s.searchOrderCandidates(ctx, in.CustomerID, since)
		if err != nil {
			return unknown("the order search failed")
		}
		candidates = found
		if len(candidates) == 0 {
			continue
		}
		break
	}
	if len(candidates) == 0 {
		// Empty reads are not proof of non-placement: a real store committed the
		// order about a second after the failed call, so a window of agreeing empty
		// reads still missed it and the ledger recorded a definite failure for an
		// order that existed. The discriminator that does hold is the cart: a
		// successful placement consumes it, so a cart that still exists is positive
		// evidence nothing was placed.
		consumed, cartErr := s.cartConsumed(ctx, cartID)
		if cartErr == nil && !consumed {
			if err := run.Fail(ctx, reconciledNotApplied); err != nil {
				return nil, err
			}
			failure := errs.New(errs.CodeOf(placeErr),
				"the order was not placed: the cart still exists after repeated reconciliation reads, so it is safe to retry with a new idempotency key").
				WithDetail("operation_id", run.ID()).
				WithDetail("cart_id", strconv.Itoa(cartID)).
				WithDetail("upstream_code", upstreamCode)
			failure.Retryable = true
			return nil, failure
		}
		// The cart is gone or its state could not be read: the placement may have
		// succeeded, so stay unknown and let the reconciler decide.
		_ = run.Unknown(ctx, string(errs.CodeUpstreamUnavailable))
		return nil, operationUnknownError(run.ID(), "order placement", string(errs.CodeUpstreamUnavailable))
	}
	exact := make([]orderCandidate, 0, 1)
	for _, candidate := range candidates {
		if candidate.QuoteID == cartID {
			exact = append(exact, candidate)
		}
	}
	if len(exact) == 1 {
		order, err := s.GetOrder(ctx, exact[0].EntityID)
		if err != nil {
			return unknown("the matched order could not be read back")
		}
		result := &CreateOrderResult{Order: *order, OperationID: run.ID(), Reconciled: true, CartID: cartID}
		if err := run.Succeed(ctx, orderRef(*order), result); err != nil {
			return nil, err
		}
		return result, nil
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, strconv.Itoa(candidate.EntityID))
	}
	if len(exact) > 1 {
		return unknown("several orders carry this cart's quote_id; a human must choose",
			"candidate_order_ids", strings.Join(ids, ","))
	}
	return unknown("candidate orders exist but none carries this cart's quote_id; verify before acting",
		"candidate_order_ids", strings.Join(ids, ","))
}

// orderCandidate is the reconciliation projection of one order. quote_id is the
// cart that produced the order: it is the only exact link between a placement and
// an order.
type orderCandidate struct {
	EntityID    int     `json:"entity_id" jsonschema:"Magento entity id."`
	IncrementID string  `json:"increment_id" jsonschema:"Human-facing document number, for example 000000123."`
	QuoteID     int     `json:"quote_id"`
	GrandTotal  float64 `json:"grand_total" jsonschema:"Document total including shipping and tax."`
	Currency    string  `json:"order_currency_code" jsonschema:"ISO 4217 code the amounts are expressed in."`
	CreatedAt   string  `json:"created_at" jsonschema:"Store time, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
	Status      string  `json:"status" jsonschema:"Current status."`
}

// orderReconcileFields requests quote_id explicitly: without it the search
// projection has no cart handle and no order can be tied to this operation.
const orderReconcileFields = "items[entity_id,increment_id,quote_id,grand_total,order_currency_code,created_at,status]"

// searchOrderCandidates lists the customer's orders created since the given time,
// with the cart link.
func (s *Service) searchOrderCandidates(ctx context.Context, customerID int, since time.Time) ([]orderCandidate, error) {
	query, err := orderSearchCriteria(OrderQuery{
		CustomerID: customerID, CreatedFrom: since.Format(time.RFC3339), Limit: 20,
	})
	if err != nil {
		return nil, err
	}
	query.Set("fields", orderReconcileFields)
	raw, err := s.client.do(ctx, http.MethodGet, "/orders", query, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Items []orderCandidate `json:"items" jsonschema:"Entries returned by this call."`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable order search payload").WithCause(err)
	}
	if payload.Items == nil {
		payload.Items = []orderCandidate{}
	}
	return payload.Items, nil
}

// sleepContext waits for d or until the context is done.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// cartConsumed reports whether the cart no longer exists, which is what Magento
// does to a cart after a successful placement. A cart that is still readable is
// positive evidence that nothing was placed.
func (s *Service) cartConsumed(ctx context.Context, cartID int) (bool, error) {
	_, err := s.client.do(ctx, http.MethodGet, "/carts/"+strconv.Itoa(cartID), nil, nil)
	if err == nil {
		return false, nil
	}
	if errs.CodeOf(err) == errs.CodeNotFound {
		return true, nil
	}
	return false, err
}

// reconcileBackoff is the pause between reconciliation reads. It is a variable
// so a test can shrink it without sleeping for seconds.
//
// A real store commits the order slightly after the HTTP response is lost, so the
// window has to outlast that lag: with reconcileReads reads this covers about
// 2.5 seconds, which was enough on a slow demo store where the order appeared
// roughly a second after the failed call.
var reconcileBackoff = 500 * time.Millisecond

// orderRef is the ledger's upstream reference for an order.
func orderRef(order Order) string {
	if order.IncrementID != "" {
		return "order:" + order.IncrementID
	}
	return "order:" + strconv.Itoa(order.EntityID)
}

// orderCreateArgs is the canonical argument set hashed for replay detection.
type orderCreateArgs struct {
	CustomerID          int               `json:"customer_id"`
	StoreID             int               `json:"store_id" jsonschema:"Store view the entity belongs to."`
	Items               []OrderItemInput  `json:"items" jsonschema:"Entries returned by this call."`
	BillingAddress      OrderAddressInput `json:"billing_address"`
	ShippingAddress     OrderAddressInput `json:"shipping_address"`
	ShippingCarrierCode string            `json:"shipping_carrier_code"`
	ShippingMethodCode  string            `json:"shipping_method_code"`
	PaymentMethod       string            `json:"payment_method"`
}

func orderCreateArgsFrom(in CreateOrderInput) orderCreateArgs {
	return orderCreateArgs{
		CustomerID: in.CustomerID, StoreID: in.StoreID, Items: in.Items,
		BillingAddress: in.BillingAddress, ShippingAddress: in.ShippingAddress,
		ShippingCarrierCode: in.ShippingCarrierCode, ShippingMethodCode: in.ShippingMethodCode,
		PaymentMethod: in.PaymentMethod,
	}
}

type cartItemBody struct {
	CartItem cartItem `json:"cartItem"`
}

type cartItem struct {
	SKU     string  `json:"sku" jsonschema:"Product SKU."`
	Qty     float64 `json:"qty" jsonschema:"Quantity at this source."`
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
	Firstname string   `json:"firstname" jsonschema:"Given name."`
	Lastname  string   `json:"lastname" jsonschema:"Family name."`
	Street    []string `json:"street" jsonschema:"Street lines, most specific first."`
	City      string   `json:"city" jsonschema:"City or locality."`
	Postcode  string   `json:"postcode" jsonschema:"Postal code as the destination country writes it."`
	CountryID string   `json:"country_id" jsonschema:"ISO 3166-1 alpha-2 country code, uppercase."`
	Telephone string   `json:"telephone" jsonschema:"Contact phone number."`
	Region    string   `json:"region,omitempty" jsonschema:"Region name."`
	RegionID  int      `json:"region_id,omitempty" jsonschema:"Numeric region id; Magento requires it for countries with a region directory."`
}

func (a OrderAddressInput) toPayload() addressPayload {
	return addressPayload(a)
}

type placeOrderBody struct {
	PaymentMethod paymentMethod `json:"paymentMethod"`
}

type paymentMethod struct {
	Method string `json:"method" jsonschema:"How the scan was paged: offset or cursor."`
}

func validateCreateOrder(in CreateOrderInput) error {
	if in.CustomerID <= 0 {
		return invalidf("customer_id must be a positive integer")
	}
	if in.StoreID < 0 {
		return invalidf("store_id must not be negative")
	}
	if in.StoreID > 0 {
		// The cart flow derives the store from the request path
		// (/rest/{store_code}/V1/...); the cart and order service contracts take
		// no store id, so accepting one would silently place the order in the
		// instance's store while the caller believed otherwise.
		return errs.New(errs.CodeUnsupportedCapability,
			"store_id cannot be honoured: the store is selected by the instance's store_code").
			WithDetail("store_id", strconv.Itoa(in.StoreID)).
			WithDetail("field", "store_id").
			WithDetail("use_instead", "select an instance whose store_code is the intended store")
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
		code := string(errs.CodeOf(err))
		// A cart that outlives its failed placement is not harmless: Magento hands
		// the same cart to the customer's next order, so a silent cleanup failure
		// plants the next mixed order (t57 saw "failed: not_found" and nobody knew).
		slog.Default().Warn("order cart cleanup failed",
			"cart_id", cartID, "error_code", code)
		return "failed: " + code
	}
	return "deleted"
}

// orderCustomerLocks serialises order placement per (instance, customer).
//
// The lock is in-process on purpose: this deployment runs one gateway replica,
// and the second gate (requireEmptyCart) is what protects against writers this
// process cannot see — another replica, a REST caller, or a cart left by a crash.
// A cross-replica deployment needs a database advisory lock keyed the same way;
// the empty-cart gate keeps that future honest but does not replace it.
var orderCustomerLocks = struct {
	mu sync.Mutex
	m  map[string]chan struct{}
}{m: make(map[string]chan struct{})}

// lockOrderCustomer blocks until this (instance, customer) is free, or the
// caller's context ends. The returned function releases the lock.
func lockOrderCustomer(ctx context.Context, key string) (func(), error) {
	orderCustomerLocks.mu.Lock()
	slot, ok := orderCustomerLocks.m[key]
	if !ok {
		slot = make(chan struct{}, 1)
		orderCustomerLocks.m[key] = slot
	}
	orderCustomerLocks.mu.Unlock()
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, errs.New(errs.CodeUpstreamTimeout,
			"another order for this customer is still being placed; retry once it finishes").
			WithDetail("customer_id", key).WithCause(ctx.Err())
	}
}

// cartItemLine is the subset of GET /carts/{id}/items this gateway reads.
type cartItemLine struct {
	SKU string  `json:"sku"`
	Qty float64 `json:"qty"`
}

// requireEmptyCart refuses to place an order into a cart that already holds
// items.
//
// Magento's cart is per customer, not per call, so anything already in it — a
// concurrent caller's line, a cart left by a failed call, a cart a caller built
// over REST — would be carried into this order. The caller named the items they
// want; every extra line is a defect, so this refuses instead of absorbing them.
// The cart is deliberately left untouched: emptying someone else's cart is a
// destructive act this tool has no mandate for.
func (s *Service) requireEmptyCart(ctx context.Context, cartID int) error {
	raw, err := s.client.do(ctx, http.MethodGet, "/carts/"+strconv.Itoa(cartID)+"/items", nil, nil)
	if err != nil {
		if errs.CodeOf(err) == errs.CodeNotFound {
			return errs.New(errs.CodeConflict,
				"the customer's cart disappeared before it could be inspected; retry with a new idempotency key").
				WithDetail("cart_id", strconv.Itoa(cartID))
		}
		return writeStageError("cart_inspect", nil, "", err)
	}
	var items []cartItemLine
	if err := json.Unmarshal(raw, &items); err != nil {
		return errs.New(errs.CodeInternal, "the store's cart items response could not be read").
			WithDetail("cart_id", strconv.Itoa(cartID)).WithCause(err)
	}
	if len(items) == 0 {
		return nil
	}
	skus := make([]string, 0, len(items))
	lines := make([]string, 0, len(items))
	for _, item := range items {
		skus = append(skus, item.SKU)
		lines = append(lines, item.SKU+" x"+strconv.FormatFloat(item.Qty, 'f', -1, 64))
	}
	return errs.New(errs.CodeConflict,
		"the customer's cart already holds items, so placing this order would include them; "+
			"resolve the cart (a human decides what to do with it) and retry with a new idempotency key").
		WithDetail("cart_id", strconv.Itoa(cartID)).
		WithDetail("cart_item_count", strconv.Itoa(len(items))).
		WithDetail("cart_skus", strings.Join(skus, ",")).
		WithDetail("cart_items", strings.Join(lines, ", "))
}
