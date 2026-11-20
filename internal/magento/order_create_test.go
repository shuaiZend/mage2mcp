// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
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
	svc := testService(t, ts.URL)
	order, err := svc.CreateOrder(context.Background(), validOrderInput())
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
	if order.EntityID != 99 || order.IncrementID != "000000099" {
		t.Fatalf("order = %+v", order)
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
}
