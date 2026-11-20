// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

func validOrderInput() CreateOrderInput {
	address := OrderAddressInput{
		Firstname: "Ada", Lastname: "Lovelace", Street: []string{"1 Analytical Engine Way"},
		City: "London", Postcode: "N1 9GU", CountryID: "GB", Telephone: "0123456789",
	}
	return CreateOrderInput{
		CustomerID: 7, Items: []OrderItemInput{{SKU: "SKU-1", Qty: 2}},
		BillingAddress: address, ShippingAddress: address,
		ShippingCarrierCode: "flatrate", ShippingMethodCode: "flatrate", PaymentMethod: "checkmo",
	}
}

func TestCreateOrderCartFlow(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/customers/7/carts":
			_, _ = w.Write([]byte(`42`))
		case "/rest/default/V1/carts/42/items":
			_, _ = w.Write([]byte(`{"item_id":1}`))
		case "/rest/default/V1/carts/42/shipping-information":
			_, _ = w.Write([]byte(`{"payment_methods":[]}`))
		case "/rest/default/V1/carts/42/order":
			_, _ = w.Write([]byte(`{"entity_id":99,"increment_id":"000000099","status":"pending","grand_total":19.98}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	ledger := newFakeLedger()
	useLedger(t, ledger)
	svc := testService(t, ts.URL)
	result, err := svc.CreateOrder(context.Background(), validOrderInput())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST /rest/default/V1/customers/7/carts",
		"POST /rest/default/V1/carts/42/items",
		"POST /rest/default/V1/carts/42/shipping-information",
		"POST /rest/default/V1/carts/42/order",
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %v", paths)
		}
	}
	item, _ := bodies[1]["cartItem"].(map[string]any)
	if item["sku"] != "SKU-1" || item["qty"] != float64(2) || item["quote_id"] != "42" {
		t.Fatalf("cart item = %+v", item)
	}
	info, _ := bodies[2]["addressInformation"].(map[string]any)
	if info["shipping_carrier_code"] != "flatrate" {
		t.Fatalf("shipping info = %+v", info)
	}
	payment, _ := bodies[3]["paymentMethod"].(map[string]any)
	if payment["method"] != "checkmo" {
		t.Fatalf("payment = %+v", payment)
	}
	if result.Order.EntityID != 99 || result.Order.IncrementID != "000000099" {
		t.Fatalf("order = %+v", result.Order)
	}
	if result.OperationID == "" {
		t.Fatal("operation id is empty")
	}
	row := ledger.row(result.OperationID)
	if row == nil || row.state != storage.OpSucceeded || row.entityType != storage.EntityOrder {
		t.Fatalf("ledger row = %+v, want a succeeded order operation", row)
	}
}

func TestCreateOrderValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	cases := []func(*CreateOrderInput){
		func(in *CreateOrderInput) { in.CustomerID = 0 },
		func(in *CreateOrderInput) { in.Items = nil },
		func(in *CreateOrderInput) { in.Items = []OrderItemInput{{SKU: "SKU-1", Qty: 0}} },
		func(in *CreateOrderInput) { in.Items = []OrderItemInput{{SKU: "bad sku!", Qty: 1}} },
		func(in *CreateOrderInput) { in.Items = append(in.Items, in.Items[0]) },
		func(in *CreateOrderInput) { in.BillingAddress.City = "" },
		func(in *CreateOrderInput) { in.BillingAddress.CountryID = "gb" },
		func(in *CreateOrderInput) { in.ShippingAddress.Street = nil },
		func(in *CreateOrderInput) { in.PaymentMethod = "BAD METHOD" },
		func(in *CreateOrderInput) { in.ShippingCarrierCode = "Bad Carrier" },
	}
	for i, mutate := range cases {
		in := validOrderInput()
		mutate(&in)
		if _, err := svc.CreateOrder(ctx, in); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
}

func TestCreateOrderUnsupportedPlacementCleansCart(t *testing.T) {
	var deleted bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/customers/7/carts":
			_, _ = w.Write([]byte(`42`))
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/carts/42/items":
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/carts/42/shipping-information":
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/carts/42/order":
			http.NotFound(w, r)
		case r.Method == http.MethodDelete && r.URL.Path == "/rest/default/V1/carts/42":
			deleted = true
			_, _ = w.Write([]byte(`true`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	ledger := newFakeLedger()
	useLedger(t, ledger)
	svc := testService(t, ts.URL)
	_, err := svc.CreateOrder(context.Background(), validOrderInput())
	if errs.CodeOf(err) != errs.CodeUnsupportedCapability {
		t.Fatalf("err = %v", err)
	}
	var taxonomy *errs.Error
	if !errors.As(err, &taxonomy) {
		t.Fatal("not a taxonomy error")
	}
	if taxonomy.Details["cart_cleanup"] != "deleted" || taxonomy.Details["stage"] != "place_order" {
		t.Fatalf("details = %+v", taxonomy.Details)
	}
	if !deleted {
		t.Fatal("cart was not cleaned up")
	}
	if c := ledger.counts(); c.fails != 1 {
		t.Fatalf("ledger failures = %d, want 1 (a proven not-applied placement)", c.fails)
	}
}

// orderFake answers the cart flow, the order search, and the order read-back.
type orderFake struct {
	mu          sync.Mutex
	requests    []string
	placeStatus int
	placeBody   string
	ordersJSON  string
	ordersSeq   []string // successive GET /orders responses; the last repeats
	deletes     int
	orderReads  int
}

func (f *orderFake) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodDelete {
			f.deletes++
		}
		placeStatus := f.placeStatus
		placeBody := f.placeBody
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/customers/7/carts":
			_, _ = io.WriteString(w, `42`)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/carts/42/items":
			_, _ = io.WriteString(w, `{"item_id":1}`)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/carts/42/shipping-information":
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/carts/42/order":
			if placeStatus != 0 {
				w.WriteHeader(placeStatus)
				_, _ = io.WriteString(w, `{"message":"The order could not be placed."}`)
				return
			}
			_, _ = io.WriteString(w, placeBody)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/orders":
			f.mu.Lock()
			f.orderReads++
			body := f.ordersJSON
			if len(f.ordersSeq) > 0 {
				body = f.ordersSeq[0]
				if len(f.ordersSeq) > 1 {
					f.ordersSeq = f.ordersSeq[1:]
				}
			}
			f.mu.Unlock()
			_, _ = io.WriteString(w, body)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/default/V1/orders/"):
			id := strings.TrimPrefix(r.URL.Path, "/rest/default/V1/orders/")
			_, _ = fmt.Fprintf(w, `{"entity_id":%s,"increment_id":"%09s","status":"pending",`+
				`"grand_total":19.98,"order_currency_code":"USD"}`, id, id)
		default:
			http.NotFound(w, r)
		}
	}
}

func (f *orderFake) countPath(suffix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, request := range f.requests {
		if strings.HasSuffix(request, suffix) {
			n++
		}
	}
	return n
}

func (f *orderFake) searchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.orderReads
}

// fastReconcile removes the pause between reconciliation reads.
func fastReconcile(t *testing.T) {
	t.Helper()
	previous := reconcileBackoff
	reconcileBackoff = 0
	t.Cleanup(func() { reconcileBackoff = previous })
}

// orderJSON renders one order candidate; quoteID is the cart that placed it. The
// total matches the cart's total in these fixtures on purpose: only the cart link
// may decide which order is ours.
func orderJSON(entityID int, quoteID int) string {
	return fmt.Sprintf(`{"entity_id":%d,"increment_id":"%09d","quote_id":%d,"status":"pending",`+
		`"grand_total":19.98,"order_currency_code":"USD","created_at":"2026-11-20 12:00:00"}`, entityID, entityID, quoteID)
}

// ordersJSON renders an order search page.
func ordersJSON(items ...string) string {
	return `{"items":[` + strings.Join(items, ",") + `],"total_count":` + fmt.Sprint(len(items)) + `}`
}

// TestCreateOrderReplaysSameKey proves a retried create does not place a second
// order.
func TestCreateOrderReplaysSameKey(t *testing.T) {
	fake := &orderFake{placeBody: `{"entity_id":99,"increment_id":"000000099","status":"pending","grand_total":19.98}`}
	ts := httptest.NewServer(fake.handler())
	defer ts.Close()
	useLedger(t, newFakeLedger())
	svc := testService(t, ts.URL)
	in := validOrderInput()
	in.IdempotencyKey = "order-1"

	first, err := svc.CreateOrder(context.Background(), in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	places := fake.countPath("/order")
	if places != 1 {
		t.Fatalf("place calls = %d, want 1", places)
	}
	second, err := svc.CreateOrder(context.Background(), in)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !second.Replayed || second.OperationID != first.OperationID {
		t.Fatalf("second = %+v, want a replay", second)
	}
	if got := fake.countPath("/order"); got != places {
		t.Fatalf("place calls after the replay = %d, want %d (no second order)", got, places)
	}
	if fake.countPath("/customers/7/carts") != 1 {
		t.Fatal("the replay must not create a cart")
	}
}

// TestCreateOrderAmbiguousReconciles proves an ambiguous placement is resolved
// by reading the customer's orders back and matching this cart's quote_id, and
// that the cart is kept.
func TestCreateOrderAmbiguousReconciles(t *testing.T) {
	fastReconcile(t)
	fake := &orderFake{
		placeStatus: http.StatusRequestTimeout,
		ordersJSON:  ordersJSON(orderJSON(99, 42)),
	}
	ts := httptest.NewServer(fake.handler())
	defer ts.Close()
	ledger := newFakeLedger()
	useLedger(t, ledger)
	svc := testService(t, ts.URL)
	in := validOrderInput()
	in.IdempotencyKey = "order-reconcile"

	result, err := svc.CreateOrder(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if !result.Reconciled || result.Order.EntityID != 99 {
		t.Fatalf("result = %+v, want the reconciled order", result)
	}
	if fake.searchCount() != 1 {
		t.Fatalf("order searches = %d, want 1", fake.searchCount())
	}
	if fake.deletes != 0 {
		t.Fatalf("deletes = %d, want 0 (the cart is the reconciliation handle)", fake.deletes)
	}
	if fake.countPath("/carts/42/order") != 1 {
		t.Fatalf("place calls = %d, want 1", fake.countPath("/carts/42/order"))
	}
	row := ledger.row(result.OperationID)
	if row == nil || row.state != storage.OpSucceeded || row.upstreamRef != "order:000000099" {
		t.Fatalf("ledger row = %+v, want succeeded with the order ref", row)
	}
}

// TestCreateOrderReconcileIgnoresForeignMatchingTotal is the MAG-R02 regression:
// an order with the same total but a different cart is NOT ours. Reporting it as
// a success would let the caller invoice or ship the wrong order.
func TestCreateOrderReconcileIgnoresForeignMatchingTotal(t *testing.T) {
	fastReconcile(t)
	fake := &orderFake{
		placeStatus: http.StatusRequestTimeout,
		ordersJSON:  ordersJSON(orderJSON(99, 7)), // foreign cart, same total
	}
	ts := httptest.NewServer(fake.handler())
	defer ts.Close()
	ledger := newFakeLedger()
	useLedger(t, ledger)
	svc := testService(t, ts.URL)
	in := validOrderInput()
	in.IdempotencyKey = "order-foreign"

	result, err := svc.CreateOrder(context.Background(), in)
	if result != nil {
		t.Fatalf("result = %+v, want no order claimed", result)
	}
	if errs.CodeOf(err) != errs.CodeOperationUnknown {
		t.Fatalf("err = %v, want operation_unknown", err)
	}
	if errs.RetryableError(err) {
		t.Fatal("a foreign candidate must not be retryable")
	}
	var typed *errs.Error
	if !errors.As(err, &typed) {
		t.Fatalf("err = %v, want a typed error", err)
	}
	if typed.Details["candidate_order_ids"] != "99" {
		t.Fatalf("details = %v, want candidate_order_ids=99", typed.Details)
	}
	if !strings.Contains(typed.Details["reconcile"], "quote_id") {
		t.Fatalf("reconcile detail = %q, want the cart-link explanation", typed.Details["reconcile"])
	}
	if fake.deletes != 0 {
		t.Fatalf("deletes = %d, want 0", fake.deletes)
	}
	if c := ledger.counts(); c.unknowns != 1 || c.succeeds != 0 {
		t.Fatalf("ledger unknowns=%d succeeds=%d, want 1/0", c.unknowns, c.succeeds)
	}
}

// TestCreateOrderReconcileMatchesByQuoteID proves two same-customer, same-total
// orders in the window are told apart by the cart link.
func TestCreateOrderReconcileMatchesByQuoteID(t *testing.T) {
	fastReconcile(t)
	fake := &orderFake{
		placeStatus: http.StatusRequestTimeout,
		ordersJSON: ordersJSON(
			orderJSON(99, 7),   // foreign order, same total
			orderJSON(100, 42), // ours
		),
	}
	ts := httptest.NewServer(fake.handler())
	defer ts.Close()
	ledger := newFakeLedger()
	useLedger(t, ledger)
	svc := testService(t, ts.URL)
	in := validOrderInput()
	in.IdempotencyKey = "order-two-candidates"

	result, err := svc.CreateOrder(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if !result.Reconciled || result.Order.EntityID != 100 {
		t.Fatalf("result = %+v, want the order carrying this cart's quote_id", result)
	}
	row := ledger.row(result.OperationID)
	if row == nil || row.state != storage.OpSucceeded || row.upstreamRef != "order:000000100" {
		t.Fatalf("ledger row = %+v, want succeeded with order 100", row)
	}
}

// TestCreateOrderAmbiguousNoMatch proves an empty reconciliation is only called a
// definite failure after repeated agreeing reads, and keeps the cart.
func TestCreateOrderAmbiguousNoMatch(t *testing.T) {
	fastReconcile(t)
	fake := &orderFake{
		placeStatus: http.StatusInternalServerError,
		ordersJSON:  ordersJSON(),
	}
	ts := httptest.NewServer(fake.handler())
	defer ts.Close()
	ledger := newFakeLedger()
	useLedger(t, ledger)
	svc := testService(t, ts.URL)
	in := validOrderInput()
	in.IdempotencyKey = "order-no-match"

	_, err := svc.CreateOrder(context.Background(), in)
	if err == nil {
		t.Fatal("want an error when the order was not placed")
	}
	if !errs.RetryableError(err) {
		t.Fatal("a proven not-placed order should be retryable with a new key")
	}
	if fake.searchCount() != reconcileReads {
		t.Fatalf("order searches = %d, want %d agreeing reads", fake.searchCount(), reconcileReads)
	}
	if fake.deletes != 0 {
		t.Fatalf("deletes = %d, want 0", fake.deletes)
	}
	if c := ledger.counts(); c.fails != 1 {
		t.Fatalf("ledger failures = %d, want 1", c.fails)
	}
}

// TestCreateOrderReconcileRetriesEmptyRead proves one empty read is not proof:
// a later read that finds the order resolves the placement.
func TestCreateOrderReconcileRetriesEmptyRead(t *testing.T) {
	fastReconcile(t)
	fake := &orderFake{
		placeStatus: http.StatusRequestTimeout,
		ordersSeq:   []string{ordersJSON(), ordersJSON(orderJSON(99, 42))},
	}
	ts := httptest.NewServer(fake.handler())
	defer ts.Close()
	useLedger(t, newFakeLedger())
	svc := testService(t, ts.URL)
	in := validOrderInput()
	in.IdempotencyKey = "order-late-visible"

	result, err := svc.CreateOrder(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if !result.Reconciled || result.Order.EntityID != 99 {
		t.Fatalf("result = %+v, want the order found on the second read", result)
	}
	if fake.searchCount() != 2 {
		t.Fatalf("order searches = %d, want 2", fake.searchCount())
	}
}

// TestCreateOrderAmbiguousMultipleMatches proves an inconclusive reconciliation
// stays unknown for a human.
func TestCreateOrderAmbiguousMultipleMatches(t *testing.T) {
	fastReconcile(t)
	fake := &orderFake{
		placeStatus: http.StatusInternalServerError,
		ordersJSON:  ordersJSON(orderJSON(99, 7), orderJSON(100, 8)),
	}
	ts := httptest.NewServer(fake.handler())
	defer ts.Close()
	ledger := newFakeLedger()
	useLedger(t, ledger)
	svc := testService(t, ts.URL)
	in := validOrderInput()
	in.IdempotencyKey = "order-multi"

	_, err := svc.CreateOrder(context.Background(), in)
	if errs.CodeOf(err) != errs.CodeOperationUnknown {
		t.Fatalf("err = %v, want operation_unknown", err)
	}
	if errs.RetryableError(err) {
		t.Fatal("an inconclusive reconciliation must not be retryable")
	}
	if fake.deletes != 0 {
		t.Fatalf("deletes = %d, want 0", fake.deletes)
	}
	if c := ledger.counts(); c.unknowns != 1 {
		t.Fatalf("ledger unknowns = %d, want 1", c.unknowns)
	}
}

// MAG-19: store_id used to be accepted, validated, and then silently dropped —
// the order was placed in the instance's store regardless. The cart and order
// service contracts take no store id, so the only honest behaviour is to refuse
// the field rather than place an order somewhere the caller did not ask for.
func TestCreateOrderRejectsStoreID(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	svc := testService(t, srv.URL)
	in := validOrderInput()
	in.StoreID = 5
	_, err := svc.CreateOrder(context.Background(), in)
	if err == nil {
		t.Fatal("CreateOrder accepted store_id; it cannot honour it")
	}
	if code := errs.CodeOf(err); code != errs.CodeUnsupportedCapability {
		t.Fatalf("code = %s, want unsupported_capability", code)
	}
	if got := posts.Load(); got != 0 {
		t.Fatalf("upstream POSTs = %d, want 0: the request must be refused before any call", got)
	}
}
