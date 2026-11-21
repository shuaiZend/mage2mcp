// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

func TestProductLinks(t *testing.T) {
	var query url.Values
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"sku":"SKU-1","link_type":"related","linked_product_sku":"SKU-2","position":1}]`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	links, err := svc.ProductLinks(context.Background(), "SKU-1", "related")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rest/default/V1/products/SKU-1/links" || query.Get("linkType") != "related" {
		t.Fatalf("request = %s %v", path, query)
	}
	if len(links) != 1 || links[0].LinkedProductSKU != "SKU-2" || links[0].Position != 1 {
		t.Fatalf("links = %+v", links)
	}
	if _, err := svc.ProductLinks(context.Background(), "SKU-1", "bogus"); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("link type err = %v", err)
	}
	if _, err := svc.ProductLinks(context.Background(), "bad sku!", "related"); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("sku err = %v", err)
	}
}

// invoiceFake answers the invoice workflow routes: the order read the pre-flight
// needs and POST /order/{id}/invoice.
type invoiceFake struct {
	mu       sync.Mutex
	requests []string
	state    int // invoice state returned by the create
	postBody []byte
}

func (f *invoiceFake) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/orders/42":
			_, _ = io.WriteString(w, `{"entity_id":42,"order_currency_code":"USD","grand_total":129.98,`+
				`"items":[{"item_id":1,"sku":"SKU-1","price":64.99,"qty_ordered":2,"qty_invoiced":0,"qty_refunded":0}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/invoices/9":
			// The read-back after the create route.
			f.mu.Lock()
			state := f.state
			f.mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"entity_id":9,"increment_id":"000000009","order_id":42,"state":%d,`+
				`"grand_total":129.98,"total_qty":2,"order_currency_code":"USD"}`, state)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/order/42/invoice":
			raw, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.postBody = raw
			f.mu.Unlock()
			// The create route answers with the new id as a JSON scalar.
			_, _ = io.WriteString(w, `"9"`)
		default:
			http.NotFound(w, r)
		}
	}
}

// TestCreateInvoice proves MAG-02: the invoice is created through the workflow
// route that honours capture, and the returned invoice state proves whether the
// capture happened.
func TestCreateInvoice(t *testing.T) {
	tests := []struct {
		name         string
		capture      bool
		state        int
		wantCaptured bool
	}{
		{name: "record only", capture: false, state: 1, wantCaptured: false},
		{name: "capture", capture: true, state: 2, wantCaptured: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &invoiceFake{state: tc.state}
			ts := httptest.NewServer(fake.handler(t))
			defer ts.Close()
			ledger := newFakeLedger()
			useLedger(t, ledger)
			svc := testService(t, ts.URL)

			result, err := svc.CreateInvoice(context.Background(), InvoiceCreateInput{
				OrderID: 42, Capture: tc.capture,
				Items:          []InvoiceLine{{OrderItemID: 1, Qty: 2}},
				IdempotencyKey: "invoice-1",
			})
			if err != nil {
				t.Fatalf("CreateInvoice: %v", err)
			}
			// The documented route is the workflow route, not POST /V1/invoices.
			var createPath string
			for _, request := range fake.requests {
				if strings.HasPrefix(request, "POST ") {
					createPath = request
				}
			}
			if createPath != "POST /rest/default/V1/order/42/invoice" {
				t.Fatalf("create request = %q, want the workflow route", createPath)
			}
			var body invoiceOrderBody
			if err := json.Unmarshal(fake.postBody, &body); err != nil {
				t.Fatalf("body: %v", err)
			}
			if body.Capture != tc.capture || len(body.Items) != 1 || body.Items[0].OrderItemID != 1 || body.Items[0].Qty != 2 {
				t.Fatalf("body = %+v", body)
			}
			if result.Captured != tc.wantCaptured || result.Invoice.State != tc.state {
				t.Fatalf("result = %+v, want captured=%v state=%d", result, tc.wantCaptured, tc.state)
			}
			if result.Invoice.EntityID != 9 || result.Invoice.IncrementID != "000000009" {
				t.Fatalf("invoice = %+v", result.Invoice)
			}
			if result.OperationID == "" {
				t.Fatal("operation id is empty")
			}
			row := ledger.row(result.OperationID)
			if row == nil || row.state != storage.OpSucceeded {
				t.Fatalf("ledger row = %+v, want succeeded", row)
			}
			// The authorised amount is 2 x 64.99 in integer minor units.
			if row.amountMinor == nil || *row.amountMinor != 12998 || row.currency != "USD" {
				t.Fatalf("ledger amount = %v %s, want 12998 USD", row.amountMinor, row.currency)
			}
		})
	}
}

// TestCreateInvoiceReplays proves a repeated idempotency key returns the recorded
// invoice without a second upstream mutation.
func TestCreateInvoiceReplays(t *testing.T) {
	fake := &invoiceFake{state: 2}
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	ledger := newFakeLedger()
	useLedger(t, ledger)
	svc := testService(t, ts.URL)
	in := InvoiceCreateInput{OrderID: 42, Capture: true, Items: []InvoiceLine{{OrderItemID: 1, Qty: 2}}, IdempotencyKey: "invoice-replay"}
	first, err := svc.CreateInvoice(context.Background(), in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	postsAfterFirst := countPosts(fake.requests)
	if postsAfterFirst != 1 {
		t.Fatalf("POSTs after the first call = %d, want 1", postsAfterFirst)
	}
	second, err := svc.CreateInvoice(context.Background(), in)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !second.Replayed || second.OperationID != first.OperationID {
		t.Fatalf("second = %+v, want a replay of %s", second, first.OperationID)
	}
	if posts := countPosts(fake.requests); posts != postsAfterFirst {
		t.Fatalf("POSTs after the replay = %d, want %d (no second mutation)", posts, postsAfterFirst)
	}
	if c := ledger.counts(); c.executes != 1 {
		t.Fatalf("executes = %d, want 1", c.executes)
	}
}

func TestCreateInvoiceValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	cases := []InvoiceCreateInput{
		{OrderID: 0, Items: []InvoiceLine{{OrderItemID: 1, Qty: 1}}},
		{OrderID: 42},
		{OrderID: 42, Items: []InvoiceLine{{OrderItemID: 0, Qty: 1}}},
		{OrderID: 42, Items: []InvoiceLine{{OrderItemID: 1, Qty: 0}}},
		{OrderID: 42, Items: []InvoiceLine{{OrderItemID: 1, Qty: 1}, {OrderItemID: 1, Qty: 1}}},
	}
	for i, in := range cases {
		if _, err := svc.CreateInvoice(ctx, in); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
	tooMany := make([]InvoiceLine, maxInvoiceItems+1)
	for i := range tooMany {
		tooMany[i] = InvoiceLine{OrderItemID: i + 1, Qty: 1}
	}
	if _, err := svc.CreateInvoice(ctx, InvoiceCreateInput{OrderID: 42, Items: tooMany}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("too many err = %v", err)
	}
}

// countPosts counts the POST requests in a recorded request log.
func countPosts(requests []string) int {
	posts := 0
	for _, request := range requests {
		if strings.HasPrefix(request, "POST ") {
			posts++
		}
	}
	return posts
}
