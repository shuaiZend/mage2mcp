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

func TestOrderStatusHistoryEnvelope(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"entity_id":1,"status":"pending","comment":null,"is_customer_notified":1}],"total_count":1}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	history, err := svc.OrderStatusHistory(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Status != "pending" || !history[0].IsCustomerNotified {
		t.Fatalf("history = %+v", history)
	}
}

func TestOrderInvoicesAndGet(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/invoices":
			if r.URL.Query().Get("searchCriteria[filter_groups][0][filters][0][field]") != "order_id" ||
				r.URL.Query().Get("searchCriteria[filter_groups][0][filters][0][value]") != "42" {
				http.Error(w, `{"message":"missing order filter"}`, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"entity_id":7,"increment_id":"000000007","order_id":42,"state":2,"grand_total":129.99,"total_qty":2,"order_currency_code":"USD"}],"total_count":1}`))
		case "/rest/default/V1/invoices/7":
			_, _ = w.Write([]byte(`{"entity_id":7,"increment_id":"000000007","order_id":42,"state":2,"grand_total":129.99,"subtotal":120,"total_qty":2,"order_currency_code":"USD"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	invoices, _, err := svc.OrderInvoices(context.Background(), 42)
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
		case "/rest/default/V1/shipments":
			_, _ = w.Write([]byte(`{"items":[{"entity_id":3,"increment_id":"000000003","order_id":42,"total_qty":2,"tracks":[{"title":"UPS","track_number":"1Z999","carrier_code":"ups"}]}],"total_count":1}`))
		case "/rest/default/V1/shipment/3":
			_, _ = w.Write([]byte(`{"entity_id":3,"increment_id":"000000003","order_id":42,"total_qty":2}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	shipments, _, err := svc.OrderShipments(context.Background(), 42)
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
	if _, _, err := svc.OrderInvoices(ctx, -1); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("invoices err = %v", err)
	}
	if _, err := svc.GetInvoice(ctx, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("invoice err = %v", err)
	}
	if _, _, err := svc.OrderShipments(ctx, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("shipments err = %v", err)
	}
	if _, err := svc.GetShipment(ctx, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("shipment err = %v", err)
	}
}

func TestOrderCreditmemosAndGet(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/creditmemos":
			_, _ = w.Write([]byte(`{"items":[{"entity_id":9,"increment_id":"000000009","order_id":42,"state":2,"grand_total":25,"total_qty":1,"order_currency_code":"USD"}],"total_count":1}`))
		case "/rest/default/V1/creditmemo/9":
			_, _ = w.Write([]byte(`{"entity_id":9,"increment_id":"000000009","order_id":42,"grand_total":25,"adjustment":-5,"order_currency_code":"USD"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	creditmemos, _, err := svc.OrderCreditmemos(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(creditmemos) != 1 || creditmemos[0].GrandTotal != 25 {
		t.Fatalf("creditmemos = %+v", creditmemos)
	}
	creditmemo, err := svc.GetCreditmemo(context.Background(), 9)
	if err != nil {
		t.Fatal(err)
	}
	if creditmemo.Adjustment != -5 || creditmemo.OrderID != 42 {
		t.Fatalf("creditmemo = %+v", creditmemo)
	}
}

func TestCreditmemoValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if _, _, err := svc.OrderCreditmemos(ctx, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("list err = %v", err)
	}
	if _, err := svc.GetCreditmemo(ctx, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("get err = %v", err)
	}
}

func TestSendDocumentEmail(t *testing.T) {
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	ctx := context.Background()
	for _, tc := range []struct{ kind, path string }{
		{"invoice", "POST /rest/default/V1/invoices/5/emails"},
		{"shipment", "POST /rest/default/V1/shipment/6/emails"},
		{"creditmemo", "POST /rest/default/V1/creditmemo/7/emails"},
	} {
		if err := svc.SendDocumentEmail(ctx, tc.kind, 5); err != nil {
			t.Fatal(err)
		}
		_ = tc
	}
	if len(paths) != 3 || paths[0] != "POST /rest/default/V1/invoices/5/emails" ||
		paths[1] != "POST /rest/default/V1/shipment/5/emails" || paths[2] != "POST /rest/default/V1/creditmemo/5/emails" {
		t.Fatalf("paths = %v", paths)
	}
	if err := svc.SendDocumentEmail(ctx, "bogus", 5); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("kind err = %v", err)
	}
	if err := svc.SendDocumentEmail(ctx, "invoice", 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("id err = %v", err)
	}
}
