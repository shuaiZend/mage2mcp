// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/jsonschema-go/jsonschema"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// Regression tests for the defects found by the live run against a real Magento
// store on 2026-11-21 (docs/audit/live-magento-defects.md).
//
// Each of these passed CI before the live run, because the fakes encoded the
// gateway's assumption instead of the wire contract. The point of every test here
// is to assert the wire shape — method, body key, projected field — not the
// gateway's own idea of it.

// LIVE-3: Magento answers a successful inventory-source create with an empty
// array. Treating that as unreadable told the caller the create had failed while
// the source existed.
func TestCreateSourceAcceptsEmptyArrayResponse(t *testing.T) {
	var posted bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/inventory/sources":
			posted = true
			// Exactly what a real store returns on success.
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/inventory/sources":
			_, _ = io.WriteString(w, `{"items":[{"source_code":"mcptest_src","name":"MCPTEST Source","enabled":true}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	source, err := svc.CreateSource(context.Background(), InventorySourceInput{
		SourceCode: "mcptest_src", Name: "MCPTEST Source",
	})
	if err != nil {
		t.Fatalf("CreateSource with an empty-array response: %v", err)
	}
	if !posted {
		t.Fatal("create was not attempted")
	}
	if source == nil || source.SourceCode != "mcptest_src" {
		t.Fatalf("source = %+v, want the read-back source", source)
	}
}

// LIVE-3 control: a create that reports success but whose source cannot be read
// back must say so rather than fabricate an object.
func TestCreateSourceReportsUnconfirmedWhenReadBackMisses(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"items":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	source, err := svc.CreateSource(context.Background(), InventorySourceInput{
		SourceCode: "mcptest_src", Name: "MCPTEST Source",
	})
	if err == nil {
		t.Fatalf("CreateSource fabricated %+v despite a failed read-back", source)
	}
}

// LIVE-4: not every build exposes a customer-address create route. The supported
// fallback is PUT /customers/{id} with the address appended.
func TestCreateCustomerAddressFallsBackToCustomerUpdate(t *testing.T) {
	var putBody map[string]any
	ts := httptest.NewServer(customerAddressFallbackHandler(t, &putBody))
	defer ts.Close()
	svc := testService(t, ts.URL)

	address, err := svc.CreateCustomerAddress(context.Background(), 7, CustomerAddressInput{
		Firstname: "Ada", Lastname: "Lovelace", Street: []string{"1 Analytical Engine Way"},
		City: "London", Postcode: "N1 9GU", CountryID: "GB", Telephone: "0123456789",
	})
	if err != nil {
		t.Fatalf("CreateCustomerAddress fallback: %v", err)
	}
	if address == nil || address.ID == 0 {
		t.Fatalf("address = %+v, want an id from the read-back", address)
	}
	customer, _ := putBody["customer"].(map[string]any)
	if customer == nil {
		t.Fatalf("PUT body = %v, want a customer object", putBody)
	}
	addresses, _ := customer["addresses"].([]any)
	if len(addresses) != 1 {
		t.Fatalf("customer.addresses = %v, want the new address appended", addresses)
	}
}

// customerAddressFallbackHandler answers the two-phase flow the fallback performs:
// a 404 on the dedicated address route, then a customer read, a PUT, and a second
// read that reports the address the store assigned.
func customerAddressFallbackHandler(t *testing.T, putBody *map[string]any) http.Handler {
	t.Helper()
	var gets int
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/addresses"):
			http.NotFound(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/customers/7"):
			gets++
			if gets == 1 {
				_, _ = io.WriteString(w, `{"id":7,"website_id":1,"email":"a@example.com","firstname":"Ada","lastname":"Lovelace","addresses":[]}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":7,"addresses":[{"id":42,"city":"London","country_id":"GB"}]}`)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/customers/7"):
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, putBody)
			_, _ = io.WriteString(w, `{"id":7}`)
		default:
			http.NotFound(w, r)
		}
	})
}

// LIVE-5: the `fields` projection is a whitelist, so total_count must be part of
// it. Without it the reported total is always zero.
func TestOrderSearchProjectsTotalCount(t *testing.T) {
	var fields string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields = r.URL.Query().Get("fields")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[{"entity_id":1}],"total_count":37}`)
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	orders, total, err := svc.SearchOrders(context.Background(), OrderQuery{Limit: 1})
	if err != nil {
		t.Fatalf("SearchOrders: %v", err)
	}
	if !strings.Contains(fields, "total_count") {
		t.Fatalf("fields = %q, want it to project total_count", fields)
	}
	if len(orders) != 1 {
		t.Fatalf("orders = %d, want 1", len(orders))
	}
	if total != 37 {
		t.Fatalf("total = %d, want the projected total_count 37", total)
	}
}

// LIVE-5: a scan that stops at the cap has been truncated even when the store did
// not report a total. Claiming completeness in that case is the defect.
func TestSalesSummaryReportsTruncationWhenCapReached(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A full page of `maxOrderLimit` orders, and no total_count at all.
		items := make([]string, 0, maxOrderLimit)
		for i := 0; i < maxOrderLimit; i++ {
			items = append(items, fmt.Sprintf(
				`{"entity_id":%d,"created_at":"2026-01-02 03:04:05","grand_total":10,"total_qty_ordered":1,"order_currency_code":"PLN"}`,
				i+1))
		}
		_, _ = io.WriteString(w, `{"items":[`+strings.Join(items, ",")+`]}`)
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	summary, err := svc.SalesSummary(context.Background(), time.Now().AddDate(0, -1, 0), time.Now())
	if err != nil {
		t.Fatalf("SalesSummary: %v", err)
	}
	if summary.ScannedOrders != maxSummaryOrders {
		t.Fatalf("scanned = %d, want the cap %d", summary.ScannedOrders, maxSummaryOrders)
	}
	if !summary.Truncated {
		t.Fatal("a scan that stopped at the cap reported complete")
	}
}

// LIVE-5 control: a short page proves the window is exhausted, so the scan is
// complete and must not claim truncation.
func TestSalesSummaryCompleteWhenPageIsShort(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[{"entity_id":1,"created_at":"2026-01-02 03:04:05","grand_total":10,"total_qty_ordered":1,"order_currency_code":"PLN"}]}`)
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	summary, err := svc.SalesSummary(context.Background(), time.Now().AddDate(0, -1, 0), time.Now())
	if err != nil {
		t.Fatalf("SalesSummary: %v", err)
	}
	if summary.Truncated {
		t.Fatalf("a complete scan reported truncated: %+v", summary)
	}
}

// MAG-20 / LIVE-1 companion: Magento rejects a Polish address without a numeric
// region id ("regionId" is required), so the field must survive into the payload.
func TestOrderAddressCarriesRegionID(t *testing.T) {
	payload := OrderAddressInput{
		Firstname: "Ada", Lastname: "Lovelace", Street: []string{"1 Way"},
		City: "Wrocław", Postcode: "50-001", CountryID: "PL", Telephone: "123", RegionID: 997,
	}.toPayload()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"region_id":997`) {
		t.Fatalf("payload = %s, want region_id present", raw)
	}
	// An unset region id must be omitted, not sent as 0, which Magento rejects.
	empty, _ := json.Marshal(OrderAddressInput{Firstname: "A"}.toPayload())
	if strings.Contains(string(empty), "region_id") {
		t.Fatalf("payload = %s, want region_id omitted when unset", empty)
	}
}

// LIVE-8: an empty reconciliation read is not proof of non-placement. A live run
// recorded `failed / reconciled_not_applied` for an order that existed on the
// store, because the order committed about a second after the failed call. Only a
// surviving cart proves nothing was placed; a consumed cart means the placement
// may have succeeded and the operation must stay unknown.
func TestCreateOrderAmbiguousWithConsumedCartStaysUnknown(t *testing.T) {
	fastReconcile(t)
	fake := &orderFake{
		placeStatus: http.StatusRequestTimeout,
		ordersJSON:  ordersJSON(),
		cartGone:    true,
	}
	ts := httptest.NewServer(fake.handler())
	defer ts.Close()
	ledger := newFakeLedger()
	useLedger(t, ledger)
	svc := testService(t, ts.URL)
	in := validOrderInput()
	in.IdempotencyKey = "order-cart-consumed"

	_, err := svc.CreateOrder(context.Background(), in)
	if errs.CodeOf(err) != errs.CodeOperationUnknown {
		t.Fatalf("err = %v, want operation_unknown", err)
	}
	if errs.RetryableError(err) {
		t.Fatal("a possibly placed order must not be marked retryable")
	}
	if c := ledger.counts(); c.fails != 0 {
		t.Fatalf("ledger failures = %d, want 0: a possibly placed order must not be recorded failed", c.fails)
	}
}

// LIVE-8: magento.order.create stores the customer id as the entity id, because
// the order does not exist yet. The observer must not read /orders/{customerID}:
// that is an unrelated order, and reporting it as applied would claim a placement
// that never happened.
func TestObserveOperationDoesNotReadOrderForOrderCreate(t *testing.T) {
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"entity_id":8,"status":"complete","state":"complete"}`)
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	observation, err := svc.ObserveOperation(context.Background(), "magento.order.create", "order", "8")
	if err != nil {
		t.Fatalf("ObserveOperation: %v", err)
	}
	if observation != ObservationInconclusive {
		t.Fatalf("observation = %q, want inconclusive", observation)
	}
	if len(paths) != 0 {
		t.Fatalf("observer read %v, want no upstream read for order.create", paths)
	}
}

// MAG-21: a bundle or downloadable product cannot be made sellable through this
// gateway — the option/selection and link/sample endpoints do not exist — so
// creating one used to produce an enabled, visible, unsellable product that was
// reported as created. It must be refused with a reason.
func TestCreateProductRejectsBundleAndDownloadable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected upstream call %s %s: the request must be refused before it is sent", r.Method, r.URL.Path)
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	for _, typeID := range []string{"bundle", "downloadable"} {
		in := CreateProductInput{
			SKU: "MCPTEST-TYPE-" + typeID, Name: "MCPTEST Type", AttributeSetID: 4,
			TypeID: typeID, Price: "9.99",
		}
		if _, err := svc.Create(context.Background(), in); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("type_id %q: err = %v, want invalid_argument", typeID, err)
		}
	}
}

// The schema must not advertise what the gateway refuses to create.
func TestProductCreateSchemaOffersOnlySellableTypes(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(productCreateSchema), &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	typeProp, _ := props["type_id"].(map[string]any)
	values, _ := typeProp["enum"].([]any)
	got := make([]string, 0, len(values))
	for _, v := range values {
		got = append(got, fmt.Sprint(v))
	}
	want := map[string]bool{"simple": true, "virtual": true}
	if len(got) != len(want) {
		t.Fatalf("type_id enum = %v, want simple and virtual only", got)
	}
	for _, v := range got {
		if !want[v] {
			t.Fatalf("type_id enum = %v, want simple and virtual only", got)
		}
	}
}

// MAG-17: a staged workflow used to discard Magento's own diagnostics, leaving
// the caller with "verify store_code and field values" when the real problem was
// a specific attribute value. The stage wrapper must carry the cause's upstream
// status and message, and must never become blindly retryable: a stage failure
// means part of the write already committed.
func TestStageErrorPreservesUpstreamDiagnostics(t *testing.T) {
	cause := errs.New(errs.CodeInvalidArgument, "Magento rejected the request as invalid").
		WithDetail("upstream_status", "400").
		WithDetail("upstream_message", `The value of attribute "color" is incorrect.`).
		WithDetail("store_code", "pl")
	cause.Retryable = true

	for name, wrapped := range map[string]error{
		"stageFailure":    stageFailure("parent", "SKU-1", cause),
		"writeStageError": writeStageError("children", []string{"SKU-1"}, "SKU-2", cause),
	} {
		var e *errs.Error
		if !errors.As(wrapped, &e) {
			t.Fatalf("%s: wrapped error is not a taxonomy error: %v", name, wrapped)
		}
		if got := e.Details["upstream_status"]; got != "400" {
			t.Fatalf("%s: upstream_status = %q, want 400", name, got)
		}
		// SEC-10: the reason is kept for the server log, not for the agent. It must
		// reach e.Upstream and must not appear in the serialised details.
		if !strings.Contains(e.Upstream, `"color" is incorrect`) {
			t.Fatalf("%s: upstream = %q, want Magento's own reason kept server-side", name, e.Upstream)
		}
		if _, leaked := e.Details["upstream_message"]; leaked {
			t.Fatalf("%s: upstream_message leaked into the agent-visible details", name)
		}
		if got := e.Details["store_code"]; got != "pl" {
			t.Fatalf("%s: store_code = %q, want pl", name, got)
		}
		if e.Retryable {
			t.Fatalf("%s: a partly applied stage must not be retryable", name)
		}
	}
}

// The upstream message is bounded: a long store message must not turn the error
// into a data channel.
func TestStageErrorBoundsUpstreamMessage(t *testing.T) {
	long := strings.Repeat("x", maxUpstreamMessageBytes*3)
	cause := errs.New(errs.CodeInvalidArgument, "rejected").
		WithDetail("upstream_message", long)
	var e *errs.Error
	if !errors.As(stageFailure("parent", "", cause), &e) {
		t.Fatal("want a taxonomy error")
	}
	if got := len(e.Upstream); got > maxUpstreamMessageBytes+3 {
		t.Fatalf("upstream length = %d, want it bounded", got)
	}
}

// MAG-15: assigning `image` to a second entry moves the role silently in Magento,
// so the previous base image is demoted without the operator being told. The
// upload must clear the role on the previous holder and report which entry lost it.
func TestUploadMediaClearsPreviousRoleHolder(t *testing.T) {
	var putPaths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/media"):
			// Magento returns the new entry id as a scalar.
			_, _ = io.WriteString(w, `7`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/media"):
			_, _ = io.WriteString(w, `[
				{"id":3,"media_type":"image","file":"/a/old.jpg","types":["image","small_image"]},
				{"id":7,"media_type":"image","file":"/a/new.jpg","types":["image"]}]`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/media/3"):
			// UpdateMedia reads the entry before writing it.
			_, _ = io.WriteString(w, `{"id":3,"media_type":"image","file":"/a/old.jpg","types":["image","small_image"]}`)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/media/3"):
			putPaths = append(putPaths, r.URL.Path)
			_, _ = io.WriteString(w, `{"id":3,"types":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	entry, err := svc.UploadMedia(context.Background(), "SKU-1", MediaUploadInput{
		Filename: "new.png", MIMEType: "image/png", DataBase64: onePixelPNGBase64,
		Types: []string{"image"},
	})
	if err != nil {
		t.Fatalf("UploadMedia: %v", err)
	}
	if len(putPaths) != 1 {
		t.Fatalf("PUT calls = %v, want the previous holder cleared once", putPaths)
	}
	if len(entry.DemotedFrom) != 1 || entry.DemotedFrom[0] != 3 {
		t.Fatalf("demoted_from = %v, want [3] so the operator learns which base image was replaced", entry.DemotedFrom)
	}
}

// onePixelPNGBase64 is a 1x1 PNG, enough for the upload validation path.
const onePixelPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// MCP-08 (list completeness): inventory.sources is capped at maxInventoryList, and
// a caller that only receives the slice cannot tell a complete answer from a
// capped one. The tool now reports the store's total and whether it truncated.
func TestInventorySourcesReportsTruncation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// One source returned, but the store says it holds 250.
		_, _ = io.WriteString(w, `{"items":[{"source_code":"default","name":"Default"}],"total_count":250}`)
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	sources, total, err := svc.Sources(context.Background())
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(sources))
	}
	if total != 250 {
		t.Fatalf("total = %d, want the store's 250", total)
	}
}

// A store that omits total_count must not report a total smaller than what it
// returned.
func TestInventorySourcesTotalNeverBelowReturned(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[{"source_code":"a"},{"source_code":"b"}]}`)
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	sources, total, err := svc.Sources(context.Background())
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if total < len(sources) {
		t.Fatalf("total = %d, want at least %d", total, len(sources))
	}
}

// SEC-10: an upstream 4xx must not hand the agent Magento's raw validation text,
// which echoes SKUs, attribute codes and ACL resource names. The text is kept on
// the error for the server log; the operation label must not carry the identifier
// either.
func TestUpstreamDiagnosticsStayServerSide(t *testing.T) {
	const secretSKU = "SECRET-SKU-42"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"message":"The SKU `+secretSKU+` is not assigned to the ACL resource Magento_Catalog::products."}`)
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	_, err := svc.Get(context.Background(), secretSKU)
	if err == nil {
		t.Fatal("want an error")
	}
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("not a taxonomy error: %v", err)
	}
	// The agent-visible surface: details, message and op.
	for key, value := range e.Details {
		if strings.Contains(value, secretSKU) || strings.Contains(value, "ACL resource") {
			t.Fatalf("detail %q leaked upstream text: %q", key, value)
		}
	}
	if strings.Contains(e.Message, secretSKU) || strings.Contains(e.Message, "ACL resource") {
		t.Fatalf("message leaked upstream text: %q", e.Message)
	}
	if strings.Contains(e.Op, secretSKU) {
		t.Fatalf("op leaked the SKU: %q", e.Op)
	}
	// The server-side channel keeps the reason for the log.
	if !strings.Contains(e.Upstream, secretSKU) {
		t.Fatalf("upstream = %q, want the reason kept for the log", e.Upstream)
	}
}

// MAG-15: the integration document promised content-hash deduplication. A repeat of
// the same bytes with the same roles must not create a second gallery entry.
func TestUploadMediaDeduplicatesByHash(t *testing.T) {
	posts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/media"):
			posts++
			_, _ = io.WriteString(w, `7`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/media"):
			_, _ = io.WriteString(w, `[{"id":7,"media_type":"image","file":"/m/new.png","types":["image"]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	in := MediaUploadInput{
		Filename: "new.png", MIMEType: "image/png", DataBase64: onePixelPNGBase64,
		Types: []string{"image"},
	}
	first, err := svc.UploadMedia(context.Background(), "SKU-1", in)
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}
	second, err := svc.UploadMedia(context.Background(), "SKU-1", in)
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if posts != 1 {
		t.Fatalf("POSTs = %d, want the repeat skipped by content hash", posts)
	}
	if second.ID != first.ID || !second.Deduplicated {
		t.Fatalf("second = %+v, want the recorded entry marked deduplicated", second)
	}
}

// Different bytes must still upload: the hash is over the content, not the name.
func TestUploadMediaDifferentBytesAreNotDeduplicated(t *testing.T) {
	posts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/media"):
			posts++
			_, _ = io.WriteString(w, fmt.Sprint(10+posts))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/media"):
			_, _ = io.WriteString(w, `[{"id":11,"media_type":"image","file":"/m/a.png","types":[]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	base := MediaUploadInput{Filename: "a.png", MIMEType: "image/png", Types: []string{"image"}}
	first := base
	first.DataBase64 = onePixelPNGBase64
	if _, err := svc.UploadMedia(context.Background(), "SKU-1", first); err != nil {
		t.Fatalf("first upload: %v", err)
	}
	second := base
	second.DataBase64 = otherPNGBase64
	if _, err := svc.UploadMedia(context.Background(), "SKU-1", second); err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if posts != 2 {
		t.Fatalf("POSTs = %d, want 2 for different content", posts)
	}
}

// otherPNGBase64 is a different 1x1 PNG, used to prove the hash is over content.
const otherPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

// MCP-08: output schemas were not self-describing — 0 of 1103 output properties
// carried a description, so an agent had to guess what a field meant. The Product
// and Order projections are the ones every read tool returns.
func TestProductAndOrderOutputsCarryDescriptions(t *testing.T) {
	for name, typ := range map[string]any{"Product": Product{}, "Order": Order{}} {
		schema, err := jsonschema.ForType(reflect.TypeOf(typ), nil)
		if err != nil {
			t.Fatalf("%s: infer schema: %v", name, err)
		}
		described, total := 0, 0
		for field, prop := range schema.Properties {
			total++
			described++
			if prop.Description == "" {
				described--
				t.Errorf("%s.%s has no description", name, field)
			}
		}
		if described == 0 || described != total {
			t.Fatalf("%s: %d of %d properties described", name, described, total)
		}
	}
}

// MCP-07: the schema is the only contract an agent sees, so a required property
// without a description is a missing instruction. The audit named three write
// schemas whose required fields were undocumented; these are the ones that caused
// wrong values in practice.
func TestNamedWriteSchemasDocumentTheirRequiredFields(t *testing.T) {
	cases := map[string]string{
		"product.create":       productCreateSchema,
		"product.update":       productUpdateSchema,
		"configurable.create":  configurableCreateSchema,
		"inventory.transfer":   inventoryTransferSchema,
		"order.create address": addressSchema,
	}
	for name, schema := range cases {
		var decoded struct {
			Required   []string `json:"required"`
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal([]byte(schema), &decoded); err != nil {
			t.Fatalf("%s: decode schema: %v", name, err)
		}
		// Every required property, not a hand-picked sample: the sample is what
		// let the rest drift.
		for _, field := range decoded.Required {
			prop, ok := decoded.Properties[field]
			if !ok {
				t.Fatalf("%s: required %q is not in the schema", name, field)
			}
			if strings.TrimSpace(prop.Description) == "" {
				t.Errorf("%s: required property %q has no description", name, field)
			}
		}
	}
}

// The unit ambiguity the audit called out: `quantity` in inventory is whole units of
// physical stock, which is not the same number as salable quantity. The description
// has to say which one it is.
func TestTransferQuantityStatesWhichNumberItIs(t *testing.T) {
	if !strings.Contains(inventoryTransferSchema, "Physical source quantity") {
		t.Fatal("transfer quantity must state that it moves physical, not salable, stock")
	}
}

// MCP-08: the money-path outputs carry flags an agent must interpret before it can
// trust a result — replayed (nothing new happened), reconciled (the order is real
// but was confirmed by reading back), outcome (a partial create already wrote
// products). An undocumented flag is a guess.
func TestMoneyPathOutputsDocumentTheirFlags(t *testing.T) {
	for name, typ := range map[string]any{
		"orderCreateOutput":        orderCreateOutput{},
		"invoiceCreateOutput":      invoiceCreateOutput{},
		"invoiceCaptureOutput":     invoiceCaptureOutput{},
		"creditmemoCreateOutput":   creditmemoCreateOutput{},
		"inventoryTransferOutput":  inventoryTransferOutput{},
		"configurableCreateOutput": configurableCreateOutput{},
	} {
		schema, err := jsonschema.ForType(reflect.TypeOf(typ), nil)
		if err != nil {
			t.Fatalf("%s: infer schema: %v", name, err)
		}
		for field, prop := range schema.Properties {
			if strings.TrimSpace(prop.Description) == "" {
				t.Errorf("%s.%s has no description", name, field)
			}
		}
	}
}

// MCP-16: a nil openWorldHint is read as true by the MCP specification, so every
// Magento tool was telling clients it might reach the open web. These tools only
// ever talk to the configured instance.
func TestMagentoToolsDeclareAClosedWorld(t *testing.T) {
	tool := toolMetadata("magento.example.read", "Example", "An example read tool.")
	if tool.Annotations.OpenWorldHint == nil {
		t.Fatal("openWorldHint is nil, which the specification reads as true")
	}
	if *tool.Annotations.OpenWorldHint {
		t.Fatal("openWorldHint = true, want false for a configured instance")
	}
	definition := mcpTool("magento.example.read", "Example", "An example read tool.", `{"type":"object"}`)
	if definition.Annotations == nil || definition.Annotations.OpenWorldHint == nil {
		t.Fatal("registered tool has no openWorldHint")
	}
	if *definition.Annotations.OpenWorldHint {
		t.Fatal("registered tool claims an open world")
	}
}
