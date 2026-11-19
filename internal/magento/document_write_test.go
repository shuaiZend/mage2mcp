// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestCreateShipment(t *testing.T) {
	var method, path string
	var body shipmentCreateBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entity_id":3,"increment_id":"000000003","order_id":42,"total_qty":2,"tracks":[{"title":"UPS","track_number":"1Z999","carrier_code":"ups"}]}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	shipment, err := svc.CreateShipment(context.Background(), ShipmentCreateInput{
		OrderID: 42,
		Items:   []DocumentLine{{OrderItemID: 1, Qty: 2}},
		Tracks:  []ShipmentTrackInput{{TrackNumber: "1Z999", Title: "UPS", CarrierCode: "ups"}},
		Comment: "Shipped today", Notify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/rest/default/V1/shipments" {
		t.Fatalf("request = %s %s", method, path)
	}
	if body.Entity.OrderID != 42 || len(body.Entity.Items) != 1 || len(body.Entity.Tracks) != 1 {
		t.Fatalf("body = %+v", body)
	}
	if body.Entity.Tracks[0].TrackNumber != "1Z999" || !body.Entity.Notify || body.Entity.Comment.Comment != "Shipped today" {
		t.Fatalf("body = %+v", body)
	}
	if shipment.EntityID != 3 || len(shipment.Tracks) != 1 || shipment.Tracks[0].TrackNumber != "1Z999" {
		t.Fatalf("shipment = %+v", shipment)
	}
}

func TestCreateCreditmemo(t *testing.T) {
	var body creditmemoCreateBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entity_id":9,"increment_id":"000000009","order_id":42,"grand_total":25,"adjustment":-5}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	creditmemo, err := svc.CreateCreditmemo(context.Background(), CreditmemoCreateInput{
		OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 1}},
		AdjustmentPositive: 10, AdjustmentNegative: 15, Comment: "Partial refund",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body.Entity.OrderID != 42 || body.Entity.AdjustmentPositive != 10 || body.Entity.AdjustmentNegative != 15 {
		t.Fatalf("body = %+v", body)
	}
	if creditmemo.EntityID != 9 || creditmemo.Adjustment != -5 {
		t.Fatalf("creditmemo = %+v", creditmemo)
	}
}

func TestDocumentWriteValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	line := []DocumentLine{{OrderItemID: 1, Qty: 1}}

	shipmentCases := []ShipmentCreateInput{
		{OrderID: 0, Items: line},
		{OrderID: 42},
		{OrderID: 42, Items: []DocumentLine{{OrderItemID: 1, Qty: 0}}},
		{OrderID: 42, Items: line, Tracks: []ShipmentTrackInput{{TrackNumber: "", CarrierCode: "ups"}}},
		{OrderID: 42, Items: line, Tracks: []ShipmentTrackInput{{TrackNumber: "1Z", CarrierCode: "BAD CODE"}}},
		{OrderID: 42, Items: line, Comment: "bad\x00comment"},
	}
	for i, in := range shipmentCases {
		if _, err := svc.CreateShipment(ctx, in); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("shipment case %d: err = %v", i, err)
		}
	}
	tooManyTracks := make([]ShipmentTrackInput, maxShipmentTracks+1)
	for i := range tooManyTracks {
		tooManyTracks[i] = ShipmentTrackInput{TrackNumber: "1Z", CarrierCode: "ups"}
	}
	if _, err := svc.CreateShipment(ctx, ShipmentCreateInput{OrderID: 42, Items: line, Tracks: tooManyTracks}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("track ceiling err = %v", err)
	}

	creditmemoCases := []CreditmemoCreateInput{
		{OrderID: 0, Items: line},
		{OrderID: 42},
		{OrderID: 42, Items: line, AdjustmentPositive: -1},
		{OrderID: 42, Items: line, AdjustmentNegative: -1},
	}
	for i, in := range creditmemoCases {
		if _, err := svc.CreateCreditmemo(ctx, in); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("creditmemo case %d: err = %v", i, err)
		}
	}
}
