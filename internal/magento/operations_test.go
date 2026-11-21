// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	mcpserver "github.com/shuaiZend/mage2mcp/internal/mcp/server"
	"github.com/shuaiZend/mage2mcp/internal/security/audit"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

// fakeLedger is an in-memory double of the durable ledger. It mirrors the
// documented transition table so a contract test can exercise the client without
// PostgreSQL; the real implementation is storage.OperationLedger.
type fakeLedger struct {
	mu    sync.Mutex
	rows  map[string]*fakeLedgerOp
	byKey map[string]string
	next  int

	begins, executes, succeeds, fails, unknowns, disposes int
}

type fakeLedgerOp struct {
	id             string
	key            string
	operation      string
	instance       string
	entityType     string
	entityID       string
	argsHash       string
	amountMinor    *int64
	currency       string
	state          string
	fence          string
	upstreamRef    string
	result         json.RawMessage
	errorCode      string
	reconcileCount int
	needsAttention bool
	createdAt      time.Time
	note           string
	auditEvent     *audit.Event
}

// fakeOperationID returns a UUID-shaped id, because the tool schemas (and the
// real ledger) address operations by UUID.
func fakeOperationID(n int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{rows: map[string]*fakeLedgerOp{}, byKey: map[string]string{}}
}

func (f *fakeLedger) Begin(_ context.Context, op storage.Operation) (storage.BeginResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.begins++
	key := op.InstanceID + "\x00" + op.Operation + "\x00" + op.IdempotencyKey
	if id, ok := f.byKey[key]; ok {
		row := f.rows[id]
		begun := storage.BeginResult{OperationID: row.id, State: row.state, Fence: row.fence,
			UpstreamRef: row.upstreamRef, ErrorCode: row.errorCode, Result: row.result}
		if row.argsHash != op.ArgsHash {
			return begun, errs.New(errs.CodeIdempotencyKeyReuse, "idempotency key was already used with different arguments")
		}
		switch row.state {
		case storage.OpSucceeded:
			begun.Decision = storage.DecisionReplay
			begun.Replayed = true
		case storage.OpFailed:
			begun.Decision = storage.DecisionRetry
		case storage.OpPending:
			begun.Decision = storage.DecisionInProgress
		case storage.OpExecuting:
			begun.Decision = storage.DecisionInProgress
		default:
			begun.Decision = storage.DecisionUnknown
		}
		return begun, nil
	}
	f.next++
	row := &fakeLedgerOp{
		id: fakeOperationID(f.next), key: op.IdempotencyKey, operation: op.Operation,
		instance: op.InstanceID, entityType: op.EntityType, entityID: op.EntityID,
		argsHash: op.ArgsHash, amountMinor: op.AmountMinor, currency: op.Currency,
		state: storage.OpPending, createdAt: time.Now().UTC(),
	}
	f.rows[row.id] = row
	f.byKey[key] = row.id
	return storage.BeginResult{OperationID: row.id, State: row.state, Decision: storage.DecisionProceed}, nil
}

func (f *fakeLedger) Execute(_ context.Context, id string, _ time.Duration) (storage.Execution, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.executes++
	row, ok := f.rows[id]
	if !ok || row.state != storage.OpPending {
		return storage.Execution{}, false, errs.New(errs.CodeIllegalTransition, "not pending")
	}
	row.state = storage.OpExecuting
	row.fence = fmt.Sprintf("fence-%s-%d", id, f.executes)
	return storage.Execution{Fence: row.fence, Attempt: 1}, true, nil
}

func (f *fakeLedger) terminal(id, fence, state, ref, code string, result json.RawMessage) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return false, errs.New(errs.CodeNotFound, "operation not found")
	}
	if row.state != storage.OpExecuting || row.fence != fence {
		return false, nil
	}
	row.state = state
	row.upstreamRef = ref
	row.errorCode = code
	if len(result) > 0 {
		row.result = result
	}
	return true, nil
}

func (f *fakeLedger) Succeed(_ context.Context, id, fence, ref string, result json.RawMessage) (bool, error) {
	f.mu.Lock()
	f.succeeds++
	f.mu.Unlock()
	return f.terminal(id, fence, storage.OpSucceeded, ref, "", result)
}

func (f *fakeLedger) Fail(_ context.Context, id, fence, code string) (bool, error) {
	f.mu.Lock()
	f.fails++
	f.mu.Unlock()
	return f.terminal(id, fence, storage.OpFailed, "", code, nil)
}

func (f *fakeLedger) Unknown(_ context.Context, id, fence, code string, _ time.Duration) (bool, error) {
	f.mu.Lock()
	f.unknowns++
	f.mu.Unlock()
	return f.terminal(id, fence, storage.OpUnknown, "", code, nil)
}

func (f *fakeLedger) Get(_ context.Context, id string) (*storage.OperationRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return nil, errs.New(errs.CodeNotFound, "operation not found")
	}
	rec := row.record()
	return &rec, nil
}

// record projects a stored row the way the durable ledger's SELECT does.
func (o *fakeLedgerOp) record() storage.OperationRecord {
	return storage.OperationRecord{ID: o.id, IdempotencyKey: o.key, Operation: o.operation,
		InstanceID: o.instance, EntityType: o.entityType, EntityID: o.entityID,
		State: o.state, AmountMinor: o.amountMinor, Currency: o.currency,
		UpstreamRef: o.upstreamRef, ErrorCode: o.errorCode, Result: o.result,
		ReconcileCount: o.reconcileCount, NeedsAttention: o.needsAttention, CreatedAt: o.createdAt}
}

// List mirrors the durable ledger's filter semantics. Pagination is exercised
// against PostgreSQL in internal/storage; this double only has to answer the
// questions the tools ask.
func (f *fakeLedger) List(_ context.Context, filter storage.OperationFilter) (*storage.OperationPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	matched := make([]storage.OperationRecord, 0, len(f.rows))
	for _, row := range f.rows {
		if filter.InstanceID != "" && row.instance != filter.InstanceID {
			continue
		}
		if filter.Operation != "" && row.operation != filter.Operation {
			continue
		}
		if filter.State != "" && row.state != filter.State {
			continue
		}
		if filter.NeedsAttention && !row.needsAttention {
			continue
		}
		matched = append(matched, row.record())
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].ID > matched[j].ID
		}
		return matched[i].CreatedAt.After(matched[j].CreatedAt)
	})
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	page := &storage.OperationPage{Total: len(matched)}
	if len(matched) > limit {
		page.Operations = matched[:limit]
		page.Truncated = true
		page.NextCursor = page.Operations[len(page.Operations)-1].ID
	} else {
		page.Operations = matched
	}
	return page, nil
}

// Dispose mirrors the durable ledger's rules: only an unknown outcome can be
// disposed of, and the judgement must be one of the three documented ones.
func (f *fakeLedger) Dispose(_ context.Context, id string, judgment storage.DisposeJudgment, note, upstreamRef string, event *audit.Event) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !judgment.Valid() {
		return false, errs.New(errs.CodeInvalidArgument, "judgment must be applied, not_applied or requeue")
	}
	if strings.TrimSpace(note) == "" {
		return false, errs.New(errs.CodeInvalidArgument, "note is required")
	}
	row, ok := f.rows[id]
	if !ok {
		return false, errs.New(errs.CodeNotFound, "operation not found")
	}
	if row.state != storage.OpUnknown {
		return false, errs.New(errs.CodeConflict, "only an unknown outcome can be disposed").
			WithDetail("operation_id", id).WithDetail("state", row.state)
	}
	f.disposes++
	switch judgment {
	case storage.DisposedApplied:
		row.state = storage.OpSucceeded
		row.errorCode = "manual_applied"
		if upstreamRef != "" {
			row.upstreamRef = upstreamRef
		}
	case storage.DisposedNotApplied:
		row.state = storage.OpFailed
		row.errorCode = "manual_not_applied"
	default:
		row.reconcileCount = 0
		row.errorCode = ""
	}
	row.needsAttention = false
	row.note = note
	row.auditEvent = event
	return true, nil
}

// row returns a stored operation for assertions.
func (f *fakeLedger) row(id string) *fakeLedgerOp {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rows[id]
}

// ledgerCounts is how many of each ledger call happened.
type ledgerCounts struct {
	begins, executes, succeeds, fails, unknowns, disposes int
}

// counts returns the ledger call counts.
func (f *fakeLedger) counts() ledgerCounts {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ledgerCounts{begins: f.begins, executes: f.executes, succeeds: f.succeeds,
		fails: f.fails, unknowns: f.unknowns, disposes: f.disposes}
}

// useLedger installs a fake ledger for one test and restores the previous one.
func useLedger(t *testing.T, ledger OperationLedger) {
	t.Helper()
	ConfigureOperations(ledger)
	t.Cleanup(func() { ConfigureOperations(nil) })
}

func TestMoneyFromDecimal(t *testing.T) {
	tests := []struct {
		amount, currency string
		wantMinor        int64
		wantCurrency     string
		wantErr          bool
	}{
		{amount: "129.99", currency: "USD", wantMinor: 12999, wantCurrency: "USD"},
		{amount: "0", currency: "USD", wantMinor: 0, wantCurrency: "USD"},
		{amount: "10", currency: "eur", wantMinor: 1000, wantCurrency: "EUR"},
		{amount: "10.5", currency: "GBP", wantMinor: 1050, wantCurrency: "GBP"},
		{amount: "10.005", currency: "USD", wantMinor: 1001, wantCurrency: "USD"},
		{amount: "10.004", currency: "USD", wantMinor: 1000, wantCurrency: "USD"},
		{amount: "9.999", currency: "USD", wantMinor: 1000, wantCurrency: "USD"},
		{amount: "-5.25", currency: "USD", wantMinor: -525, wantCurrency: "USD"},
		{amount: ".5", currency: "USD", wantMinor: 50, wantCurrency: "USD"},
		{amount: "", currency: "USD", wantErr: true},
		{amount: "abc", currency: "USD", wantErr: true},
		{amount: "1.2.3", currency: "USD", wantErr: true},
		{amount: "1", currency: "US", wantErr: true},
		{amount: "1", currency: "", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.amount+"_"+tc.currency, func(t *testing.T) {
			money, err := MoneyFromDecimal(tc.amount, tc.currency)
			if tc.wantErr {
				if errs.CodeOf(err) != errs.CodeInvalidArgument {
					t.Fatalf("err = %v, want invalid_argument", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("MoneyFromDecimal: %v", err)
			}
			if money.Minor != tc.wantMinor || money.Currency != tc.wantCurrency {
				t.Fatalf("money = %+v, want %d %s", money, tc.wantMinor, tc.wantCurrency)
			}
		})
	}
}

// TestMoneyFromAmountIsFloatFree pins the single float boundary: a Magento
// amount is converted through its two-decimal text, not by multiplying a float.
func TestMoneyFromAmountIsFloatFree(t *testing.T) {
	for _, tc := range []struct {
		amount float64
		want   int64
	}{
		{129.99, 12999}, {0.1, 10}, {0.29, 29}, {19.99, 1999}, {1.15, 115}, {100, 10000},
	} {
		money, err := moneyFromAmount(tc.amount, "USD")
		if err != nil {
			t.Fatalf("moneyFromAmount(%v): %v", tc.amount, err)
		}
		if money.Minor != tc.want {
			t.Fatalf("moneyFromAmount(%v) = %d, want %d", tc.amount, money.Minor, tc.want)
		}
	}
}

func TestClassifyOperationError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want operationClass
	}{
		{name: "nil is applied", err: nil, want: operationApplied},
		{name: "validation", err: errs.New(errs.CodeValidationFailed, "bad"), want: operationNotApplied},
		{name: "not found", err: errs.New(errs.CodeNotFound, "gone"), want: operationNotApplied},
		{name: "conflict", err: errs.New(errs.CodeConflict, "conflict"), want: operationNotApplied},
		{name: "permission", err: errs.New(errs.CodePermissionDenied, "denied"), want: operationNotApplied},
		{name: "timeout", err: errs.New(errs.CodeUpstreamTimeout, "timeout"), want: operationAmbiguous},
		{name: "unavailable", err: errs.New(errs.CodeUpstreamUnavailable, "5xx"), want: operationAmbiguous},
		{name: "internal", err: errs.New(errs.CodeInternal, "bug"), want: operationAmbiguous},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyOperationError(tc.err); got != tc.want {
				t.Fatalf("class = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOperationsFailClosedWithoutLedger proves a money tool cannot run unguarded.
func TestOperationsFailClosedWithoutLedger(t *testing.T) {
	ConfigureOperations(nil)
	if OperationsConfigured() {
		t.Fatal("OperationsConfigured = true without a ledger")
	}
	_, err := configuredOperations()
	if errs.CodeOf(err) != errs.CodeUnsupportedCapability {
		t.Fatalf("err = %v, want unsupported_capability", err)
	}
}

// TestOperationBeginDerivesKeyAndReplays proves the derived key is stable and a
// repeated call replays the recorded result instead of executing again.
func TestOperationBeginDerivesKeyAndReplays(t *testing.T) {
	ledger := newFakeLedger()
	useLedger(t, ledger)
	ops, err := configuredOperations()
	if err != nil {
		t.Fatal(err)
	}
	req := OperationRequest{
		Tool: "magento.invoice.capture", Instance: "store-us", Subject: "alice",
		EntityType: storage.EntityInvoice, EntityID: "9",
		Args: map[string]any{"invoice_id": 9},
	}
	run, begun, err := ops.Begin(context.Background(), req)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if begun.Decision != storage.DecisionProceed || run == nil || run.ID() == "" {
		t.Fatalf("begin = %+v run=%v", begun, run)
	}
	if err := run.Succeed(context.Background(), "invoice:9", map[string]any{"captured": true}); err != nil {
		t.Fatalf("Succeed: %v", err)
	}

	// Same arguments, no key: the derived key must replay.
	run2, begun2, err := ops.Begin(context.Background(), req)
	if err != nil {
		t.Fatalf("second Begin: %v", err)
	}
	if run2 != nil || begun2.Decision != storage.DecisionReplay || !begun2.Replayed {
		t.Fatalf("second begin = %+v run=%v, want a replay", begun2, run2)
	}
	var recorded map[string]any
	if err := Replay(begun2, &recorded); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if recorded["captured"] != true {
		t.Fatalf("recorded = %+v", recorded)
	}
	if c := ledger.counts(); c.executes != 1 {
		t.Fatalf("executes = %d, want 1", c.executes)
	}
}

// TestOperationKeyReuseWithDifferentArgs proves the key cannot be reused for a
// different request.
func TestOperationKeyReuseWithDifferentArgs(t *testing.T) {
	ledger := newFakeLedger()
	useLedger(t, ledger)
	ops, _ := configuredOperations()
	req := OperationRequest{
		Tool: "magento.invoice.capture", Instance: "store-us", Subject: "alice",
		IdempotencyKey: "capture-1", EntityType: storage.EntityInvoice, EntityID: "9",
		Args: map[string]any{"invoice_id": 9},
	}
	if _, _, err := ops.Begin(context.Background(), req); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	req.Args = map[string]any{"invoice_id": 10}
	if _, _, err := ops.Begin(context.Background(), req); errs.CodeOf(err) != errs.CodeIdempotencyKeyReuse {
		t.Fatalf("err = %v, want idempotency_key_reuse", err)
	}
}

// TestOperationUnknownRefusesToExecute proves an UNKNOWN operation is never
// retried: Begin returns operation_unknown and a non-retryable error.
func TestOperationUnknownRefusesToExecute(t *testing.T) {
	ledger := newFakeLedger()
	useLedger(t, ledger)
	ops, _ := configuredOperations()
	req := OperationRequest{
		Tool: "magento.invoice.capture", Instance: "store-us", Subject: "alice",
		IdempotencyKey: "capture-1", EntityType: storage.EntityInvoice, EntityID: "9",
		Args: map[string]any{"invoice_id": 9},
	}
	run, _, err := ops.Begin(context.Background(), req)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := run.Unknown(context.Background(), string(errs.CodeUpstreamTimeout)); err != nil {
		t.Fatalf("Unknown: %v", err)
	}
	_, begun, err := ops.Begin(context.Background(), req)
	if errs.CodeOf(err) != errs.CodeOperationUnknown {
		t.Fatalf("err = %v, want operation_unknown", err)
	}
	if errs.RetryableError(err) {
		t.Fatal("an unknown operation must not be retryable")
	}
	if begun == nil || begun.Decision != storage.DecisionUnknown {
		t.Fatalf("begun = %+v", begun)
	}
	if c := ledger.counts(); c.executes != 1 {
		t.Fatalf("executes = %d, want 1 (no retry)", c.executes)
	}
}

// TestOperationFailedIsNotRetriedAutomatically proves a recorded failure is
// returned to the caller instead of being re-executed.
func TestOperationFailedIsNotRetriedAutomatically(t *testing.T) {
	ledger := newFakeLedger()
	useLedger(t, ledger)
	ops, _ := configuredOperations()
	req := OperationRequest{
		Tool: "magento.creditmemo.create", Instance: "store-us", Subject: "alice",
		IdempotencyKey: "refund-1", EntityType: storage.EntityCreditmemo, EntityID: "7",
		Args: map[string]any{"order_id": 42},
	}
	run, _, err := ops.Begin(context.Background(), req)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := run.Fail(context.Background(), string(errs.CodeValidationFailed)); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	_, begun, err := ops.Begin(context.Background(), req)
	if errs.CodeOf(err) != errs.CodeValidationFailed {
		t.Fatalf("err = %v, want the recorded validation_failed", err)
	}
	if begun == nil || begun.Decision != storage.DecisionRetry {
		t.Fatalf("begun = %+v", begun)
	}
	if c := ledger.counts(); c.executes != 1 {
		t.Fatalf("executes = %d, want 1 (no automatic retry)", c.executes)
	}
}

// TestOperationInstanceIDIsStable proves the derived instance id does not change
// between calls, which is what makes a replay work across replicas.
func TestOperationInstanceIDIsStable(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	first := svc.operationInstanceID("")
	second := svc.operationInstanceID("")
	if first == "" || first != second || first == "magento-unknown" {
		t.Fatalf("instance ids = %q, %q", first, second)
	}
	if got := svc.operationInstanceID("store-us"); got != "store-us" {
		t.Fatalf("explicit instance = %q", got)
	}
	other := testService(t, "https://other.example.com")
	if other.operationInstanceID("") == first {
		t.Fatal("different endpoints must derive different instance ids")
	}
}

// startToolSession registers the write tools against svc and connects an
// in-memory MCP client, so a test can assert what actually reaches the wire.
func startToolSession(t *testing.T, svc *Service) *mcp.ClientSession {
	t.Helper()
	reg := registry.New()
	if err := RegisterWrites(reg, svc); err != nil {
		t.Fatalf("RegisterWrites: %v", err)
	}
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: magentoConfirmService(t)})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// TestConfigurableCreateForwardsParentStockSource is the MAG-R01 regression: the
// derivation of parent_in_stock must reach the MCP payload, otherwise an
// unobserved read is indistinguishable from an observed out-of-stock.
func TestConfigurableCreateForwardsParentStockSource(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/products/attribute-sets/4/attributes":
			_, _ = io.WriteString(w, `[{"attribute_id":93,"attribute_code":"color","frontend_input":"select"}]`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/inventory/source-items":
			// No child holds stock: the parent must be reported out of stock, and
			// the derivation must say so.
			_, _ = io.WriteString(w, `{"items":[],"total_count":0}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/children"):
			_, _ = io.WriteString(w, `[{"sku":"PHONE-01-BLK","type_id":"simple"}]`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/default/V1/products/"):
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products":
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				Product struct {
					SKU    string `json:"sku"`
					TypeID string `json:"type_id"`
				} `json:"product"`
			}
			_ = json.Unmarshal(raw, &body)
			_, _ = fmt.Fprintf(w, `{"id":100,"sku":%q,"type_id":%q,"name":"Phone 01","price":699,"attribute_set_id":4}`,
				body.Product.SKU, body.Product.TypeID)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/child"):
			_, _ = io.WriteString(w, `true`)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/stockItems/"):
			_, _ = io.WriteString(w, `true`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	session := startToolSession(t, testService(t, ts.URL))
	ctx := context.Background()

	res := callWriteTool(t, ctx, session, configurableCreateToolName, map[string]any{
		"instance": "default",
		"parent": map[string]any{
			"sku": "PHONE-01", "name": "Phone 01", "attribute_set_id": 4, "price": "699",
		},
		"variant_attributes": []any{map[string]any{
			"attribute_id": 93, "code": "color",
			"values": []any{map[string]any{"value_index": 12}},
		}},
		"children": []any{map[string]any{
			"sku": "PHONE-01-BLK", "attributes": map[string]string{"color": "12"},
		}},
	})
	if res.IsError {
		t.Fatalf("configurable.create returned a tool error: %s", writeToolResultText(t, res))
	}
	out := structuredOutput[configurableCreateOutput](t, res)
	if out.ParentInStock {
		t.Fatal("parent_in_stock = true, want false")
	}
	if !strings.HasPrefix(out.ParentStockSource, "source_items:") {
		t.Fatalf("parent_stock_source = %q, want the derivation in the MCP payload", out.ParentStockSource)
	}
}

// TestInvoiceCaptureToolReplays proves the tool accepts idempotency_key and
// returns the recorded outcome on a repeat, with one upstream capture.
func TestInvoiceCaptureToolReplays(t *testing.T) {
	var mu sync.Mutex
	posts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/invoices/9":
			_, _ = io.WriteString(w, `{"entity_id":9,"increment_id":"000000009","order_id":42,"state":1,`+
				`"grand_total":129.99,"order_currency_code":"USD"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/invoices/9/capture":
			mu.Lock()
			posts++
			mu.Unlock()
			_, _ = io.WriteString(w, `true`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	useLedger(t, newFakeLedger())
	session := startToolSession(t, testService(t, ts.URL))
	ctx := context.Background()
	args := map[string]any{"instance": "default", "invoice_id": 9, "idempotency_key": "capture-tool-1"}

	first := callWriteTool(t, ctx, session, invoiceCaptureToolName, args)
	if first.IsError {
		t.Fatalf("capture: %s", writeToolResultText(t, first))
	}
	firstOut := structuredOutput[invoiceCaptureOutput](t, first)
	second := callWriteTool(t, ctx, session, invoiceCaptureToolName, args)
	if second.IsError {
		t.Fatalf("replay: %s", writeToolResultText(t, second))
	}
	secondOut := structuredOutput[invoiceCaptureOutput](t, second)
	if !secondOut.Replayed || secondOut.OperationID != firstOut.OperationID {
		t.Fatalf("second = %+v, want a replay of %s", secondOut, firstOut.OperationID)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 {
		t.Fatalf("capture POSTs = %d, want 1", posts)
	}
}

// operationToolsServer registers the three operation tools the way tools.go will
// and returns a connected client session.
func operationToolsServer(t *testing.T) *mcp.ClientSession {
	t.Helper()
	reg := registry.New()
	for _, tool := range []registry.Tool{
		newOperationGetTool(), newOperationListTool(), newOperationDisposeTool(),
	} {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name, err)
		}
	}
	// The dispose tool is a write and therefore requires approval, so the server
	// needs a confirmation service.
	srv, err := mcpserver.NewMCP(mcpserver.Options{
		Version: "test", Registry: reg, Confirm: magentoConfirmService(t),
	})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// seedAttentionOperation drives the fake ledger to the state the reconciler leaves
// an operation in after it gives up, and returns its id.
func seedAttentionOperation(t *testing.T, ledger *fakeLedger) string {
	t.Helper()
	ops, err := configuredOperations()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	run, _, err := ops.Begin(ctx, OperationRequest{
		Tool: "magento.order.create", Instance: "store-us", Subject: "erp",
		EntityType: storage.EntityOrder, EntityID: "1042",
		Args: map[string]any{"customer_id": 3},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := run.Unknown(ctx, "upstream_timeout"); err != nil {
		t.Fatalf("Unknown: %v", err)
	}
	ledger.mu.Lock()
	ledger.rows[run.ID()].needsAttention = true
	ledger.rows[run.ID()].reconcileCount = 9
	ledger.mu.Unlock()
	return run.ID()
}

// INT-07: operation_unknown tells the caller to poll, so polling has to exist.
func TestOperationToolsExposePollingAndDisposition(t *testing.T) {
	ledger := newFakeLedger()
	useLedger(t, ledger)
	id := seedAttentionOperation(t, ledger)
	session := operationToolsServer(t)
	ctx := context.Background()

	got, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: operationGetToolName, Arguments: map[string]any{"operation_id": id},
	})
	if err != nil {
		t.Fatalf("operation.get: %v", err)
	}
	if got.IsError {
		t.Fatalf("operation.get returned an error: %v", got.StructuredContent)
	}
	record, ok := got.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content = %T, want an object", got.StructuredContent)
	}
	operation, _ := record["operation"].(map[string]any)
	if operation["state"] != storage.OpUnknown {
		t.Fatalf("state = %v, want unknown", operation["state"])
	}
	if operation["needs_attention"] != true {
		t.Fatalf("needs_attention = %v, want true: an escalated row must say so", operation["needs_attention"])
	}
	next, _ := record["next"].(string)
	if !strings.Contains(next, operationDisposeToolName) {
		t.Fatalf("next = %q, want it to name the disposition tool", next)
	}

	listed, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: operationListToolName, Arguments: map[string]any{"needs_attention": true},
	})
	if err != nil {
		t.Fatalf("operation.list: %v", err)
	}
	if listed.IsError {
		t.Fatalf("operation.list returned an error: %v", listed.StructuredContent)
	}
	page, _ := listed.StructuredContent.(map[string]any)
	if page["total"] != float64(1) {
		t.Fatalf("total = %v, want 1", page["total"])
	}
	rows, _ := page["operations"].([]any)
	if len(rows) != 1 {
		t.Fatalf("operations = %v, want the one escalated row", page["operations"])
	}

	// The disposition is a write: it must be approved before it lands.
	params := &mcp.CallToolParams{
		Name: operationDisposeToolName,
		Arguments: map[string]any{
			"operation_id": id, "judgment": "applied",
			"note": "read /V1/orders/1042: the order exists", "upstream_ref": "order:1042",
		},
	}
	unapproved, err := session.CallTool(ctx, params)
	if err != nil {
		t.Fatalf("operation.dispose: %v", err)
	}
	if !unapproved.IsError || confirmationIDFromResult(t, unapproved) == "" {
		t.Fatalf("dispose ran without approval: %v", unapproved.StructuredContent)
	}
	approved, err := callToolApproved(t, ctx, session, params)
	if err != nil {
		t.Fatalf("operation.dispose (approved): %v", err)
	}
	if approved.IsError {
		t.Fatalf("dispose failed: %v", approved.StructuredContent)
	}
	disposed, _ := approved.StructuredContent.(map[string]any)
	after, _ := disposed["operation"].(map[string]any)
	if after["state"] != storage.OpSucceeded {
		t.Fatalf("state after dispose = %v, want succeeded", after["state"])
	}
	if disposed["judgment"] != "applied" {
		t.Fatalf("judgment = %v, want applied", disposed["judgment"])
	}

	// The operator's finding is recorded, not just applied: the note and the
	// identity of the person who made the call are in the audit row.
	ledger.mu.Lock()
	row := ledger.rows[id]
	ledger.mu.Unlock()
	if !strings.Contains(row.note, "/V1/orders/1042") {
		t.Fatalf("note = %q, want the operator's evidence recorded", row.note)
	}
	if row.auditEvent == nil || row.auditEvent.State != "succeeded" {
		t.Fatalf("audit event = %+v, want a succeeded disposition", row.auditEvent)
	}
	if c := ledger.counts(); c.disposes != 1 {
		t.Fatalf("disposes = %d, want 1 (the unapproved call must not dispose)", c.disposes)
	}
}

// The tools are useless without a ledger, and must say so instead of panicking.
func TestOperationToolsFailClosedWithoutLedger(t *testing.T) {
	ConfigureOperations(nil)
	session := operationToolsServer(t)
	ctx := context.Background()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: operationGetToolName, Arguments: map[string]any{"operation_id": "8f9d3d0e-0000-4000-8000-000000000000"},
	})
	if err != nil {
		t.Fatalf("operation.get: %v", err)
	}
	if !res.IsError {
		t.Fatal("operation.get succeeded without a ledger")
	}
	envelope, _ := res.StructuredContent.(map[string]any)
	if envelope["code"] != string(errs.CodeUnsupportedCapability) {
		t.Fatalf("code = %v, want unsupported_capability", envelope["code"])
	}
}

// The constructors must be registrable as they stand, so wiring them into
// tools.go is a registration call and nothing else.
func TestOperationToolMetadataIsRegistrable(t *testing.T) {
	reg := registry.New()
	for _, tool := range []registry.Tool{
		newOperationGetTool(), newOperationListTool(), newOperationDisposeTool(),
	} {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("%s is not registrable: %v", tool.Name, err)
		}
	}
	dispose, ok := reg.Lookup(operationDisposeToolName)
	if !ok {
		t.Fatal("dispose tool is not registered")
	}
	if dispose.Permission != registry.PermissionWrite || !dispose.RequiresConfirmation {
		t.Fatalf("dispose = %+v, want a write that requires approval", dispose)
	}
	get, _ := reg.Lookup(operationGetToolName)
	if get.Permission != registry.PermissionRead || get.RequiresConfirmation {
		t.Fatalf("get = %+v, want a read that needs no approval", get)
	}
}

// The tool refuses to dispose anything that is not unknown, and refuses a
// disposition without a note, before it touches the ledger.
func TestOperationsDisposeGuards(t *testing.T) {
	ledger := newFakeLedger()
	useLedger(t, ledger)
	ops, err := configuredOperations()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	run, _, err := ops.Begin(ctx, OperationRequest{
		Tool: "magento.order.create", Instance: "store-us", Subject: "erp",
		EntityType: storage.EntityOrder, EntityID: "7", Args: map[string]any{"customer_id": 3},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := ops.Dispose(ctx, run.ID(), storage.DisposedApplied, "checked", ""); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("dispose an executing operation: err = %v, want conflict", err)
	}
	if err := run.Unknown(ctx, "upstream_timeout"); err != nil {
		t.Fatalf("Unknown: %v", err)
	}
	if _, err := ops.Dispose(ctx, run.ID(), storage.DisposedApplied, "   ", ""); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("dispose without a note: err = %v, want invalid_argument", err)
	}
	if _, err := ops.Dispose(ctx, run.ID(), "maybe", "checked", ""); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("dispose with a bad judgement: err = %v, want invalid_argument", err)
	}
	record, err := ops.Dispose(ctx, run.ID(), storage.DisposedNotApplied, "read upstream: absent", "")
	if err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if record.State != storage.OpFailed {
		t.Fatalf("state = %s, want failed", record.State)
	}
}
