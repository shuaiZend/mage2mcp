// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestOrderStatusHistory(t *testing.T) {
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"entity_id":1,"parent_id":42,"status":"processing","comment":"Paid","is_customer_notified":1,"is_visible_on_front":0,"created_at":"2026-11-17 10:00:00"}]`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	history, err := svc.OrderStatusHistory(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rest/default/V1/orders/42/comments" {
		t.Fatalf("path = %q", path)
	}
	if len(history) != 1 || history[0].Status != "processing" || history[0].Comment != "Paid" || !history[0].IsCustomerNotified {
		t.Fatalf("history = %+v", history)
	}
}

func TestOrderInvoicesAndGet(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/orders/42/invoices":
			_, _ = w.Write([]byte(`[{"entity_id":7,"increment_id":"000000007","order_id":42,"state":2,"grand_total":129.99,"total_qty":2,"order_currency_code":"USD"}]`))
		case "/rest/default/V1/invoices/7":
			_, _ = w.Write([]byte(`{"entity_id":7,"increment_id":"000000007","order_id":42,"state":2,"grand_total":129.99,"subtotal":120,"total_qty":2,"order_currency_code":"USD"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	invoices, err := svc.OrderInvoices(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(invoices) != 1 || invoices[0].IncrementID != "000000007" || invoices[0].GrandTotal != 129.99 {
		t.Fatalf("invoices = %+v", invoices)
	}
	invoice, err := svc.GetInvoice(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if invoice.EntityID != 7 || invoice.Subtotal != 120 || invoice.Currency != "USD" {
		t.Fatalf("invoice = %+v", invoice)
	}
}

func TestOrderShipmentsAndGet(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/orders/42/shipments":
			_, _ = w.Write([]byte(`[{"entity_id":3,"increment_id":"000000003","order_id":42,"total_qty":2,"tracks":[{"title":"UPS","track_number":"1Z999","carrier_code":"ups"}]}]`))
		case "/rest/default/V1/shipments/3":
			_, _ = w.Write([]byte(`{"entity_id":3,"increment_id":"000000003","order_id":42,"total_qty":2}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	shipments, err := svc.OrderShipments(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(shipments) != 1 || len(shipments[0].Tracks) != 1 || shipments[0].Tracks[0].TrackNumber != "1Z999" {
		t.Fatalf("shipments = %+v", shipments)
	}
	shipment, err := svc.GetShipment(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if shipment.EntityID != 3 || shipment.TotalQty != 2 {
		t.Fatalf("shipment = %+v", shipment)
	}
}

func TestOrderDocumentValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if _, err := svc.OrderStatusHistory(ctx, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("history err = %v", err)
	}
	if _, err := svc.OrderInvoices(ctx, -1); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("invoices err = %v", err)
	}
	if _, err := svc.GetInvoice(ctx, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("invoice err = %v", err)
	}
	if _, err := svc.OrderShipments(ctx, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("shipments err = %v", err)
	}
	if _, err := svc.GetShipment(ctx, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("shipment err = %v", err)
	}
}
