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
	"strconv"
	"strings"
	"sync"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func TestAssignSourceItemsRequest(t *testing.T) {
	service, rec := newWriteService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[]`)
	})
	items := []SourceItem{
		{SKU: "SKU-1", SourceCode: "default", Quantity: 5, Status: 1},
		{SKU: "SKU-2", SourceCode: "warehouse_a", Quantity: 0, Status: 0},
	}
	if err := service.AssignSourceItems(context.Background(), items); err != nil {
		t.Fatalf("AssignSourceItems: %v", err)
	}
	requests := rec.all()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	got := requests[0]
	if got.Method != http.MethodPost || got.Path != "/rest/default/V1/inventory/source-items" {
		t.Fatalf("request = %s %s, want POST /rest/default/V1/inventory/source-items", got.Method, got.Path)
	}
	assertOAuthHeader(t, got.Auth)
	assertJSONBody(t, got.Body, `{"sourceItems":[
		{"sku":"SKU-1","source_code":"default","quantity":5,"status":1},
		{"sku":"SKU-2","source_code":"warehouse_a","quantity":0,"status":0}]}`)
}

func TestAssignSourceItemsValidation(t *testing.T) {
	service, rec := newWriteService(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid input reached the upstream: %s %s", r.Method, r.URL.Path)
	})
	valid := SourceItem{SKU: "SKU-1", SourceCode: "default", Quantity: 1, Status: 1}
	tooMany := make([]SourceItem, 0, 101)
	for i := 0; i < 101; i++ {
		tooMany = append(tooMany, SourceItem{
			SKU: "SKU-" + strconv.Itoa(i), SourceCode: "default", Quantity: 1, Status: 1,
		})
	}
	tests := []struct {
		name  string
		items []SourceItem
	}{
		{name: "empty"},
		{name: "more than 100 items", items: tooMany},
		{name: "bad sku", items: []SourceItem{{SKU: "bad sku", SourceCode: "default"}}},
		{name: "bad source code", items: []SourceItem{{SKU: "SKU-1", SourceCode: "Warehouse"}}},
		{name: "negative quantity", items: []SourceItem{{SKU: "SKU-1", SourceCode: "default", Quantity: -1}}},
		{name: "bad status", items: []SourceItem{{SKU: "SKU-1", SourceCode: "default", Status: 2}}},
		{name: "duplicate pair", items: []SourceItem{valid, valid}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := service.AssignSourceItems(context.Background(), tc.items)
			if code := errs.CodeOf(err); code != errs.CodeInvalidArgument {
				t.Fatalf("code = %s, want invalid_argument (%v)", code, err)
			}
		})
	}
	if rec.count() != 0 {
		t.Fatalf("invalid input reached the upstream %d times", rec.count())
	}
}

func TestAssignSourceItemsErrorMapping(t *testing.T) {
	tests := []struct {
		status    int
		code      errs.Code
		retryable bool
	}{
		{status: http.StatusBadRequest, code: errs.CodeValidationFailed},
		{status: http.StatusUnauthorized, code: errs.CodeUnauthenticated},
		{status: http.StatusForbidden, code: errs.CodePermissionDenied},
		{status: http.StatusConflict, code: errs.CodeConflict},
		{status: http.StatusTooManyRequests, code: errs.CodeRateLimited, retryable: true},
		{status: http.StatusServiceUnavailable, code: errs.CodeUpstreamUnavailable, retryable: true},
	}
	for _, tc := range tests {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			service, _ := newWriteService(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"message":"SECRET-UPSTREAM"}`)
			})
			err := service.AssignSourceItems(context.Background(), []SourceItem{
				{SKU: "SKU-1", SourceCode: "default", Quantity: 1, Status: 1},
			})
			var e *errs.Error
			if !errors.As(err, &e) {
				t.Fatalf("error is not *errs.Error: %v", err)
			}
			if e.Code != tc.code || e.Retryable != tc.retryable {
				t.Fatalf("code = %s retryable = %v, want %s/%v", e.Code, e.Retryable, tc.code, tc.retryable)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("error leaked the upstream body: %v", err)
			}
		})
	}
}

func TestAssignSourceItemsAcceptsExactlyOneHundred(t *testing.T) {
	service, rec := newWriteService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[]`)
	})
	items := make([]SourceItem, 0, 100)
	for i := 0; i < 100; i++ {
		items = append(items, SourceItem{
			SKU: fmt.Sprintf("SKU-%d", i), SourceCode: "default", Quantity: i, Status: 1,
		})
	}
	if err := service.AssignSourceItems(context.Background(), items); err != nil {
		t.Fatalf("AssignSourceItems(100 items): %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("requests = %d, want 1", rec.count())
	}
}

func TestInventorySourcesAndStocks(t *testing.T) {
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/inventory/sources":
			_, _ = w.Write([]byte(`{"items":[{"source_code":"default","name":"Default Source","enabled":true,"country_id":"CN","city":"Shanghai","latitude":31.2,"longitude":121.5}]}`))
		case "/rest/default/V1/inventory/stocks":
			_, _ = w.Write([]byte(`{"items":[{"stock_id":1,"name":"Default Stock"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	sources, total, err := svc.Sources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].SourceCode != "default" || !sources[0].Enabled || sources[0].City != "Shanghai" {
		t.Fatalf("sources = %+v", sources)
	}
	if total < len(sources) {
		t.Fatalf("total = %d, want at least the number returned (%d)", total, len(sources))
	}
	stocks, err := svc.Stocks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(stocks) != 1 || stocks[0].StockID != 1 || stocks[0].Name != "Default Stock" {
		t.Fatalf("stocks = %+v", stocks)
	}
	if len(paths) != 2 || !strings.Contains(paths[0], "searchCriteria%5BpageSize%5D=100") {
		t.Fatalf("paths = %v", paths)
	}
}

// sourceItemsResponse renders a source-item page for the transfer fakes.
func sourceItemsResponse(from, to int) string {
	return fmt.Sprintf(`{"items":[
		{"sku":"SKU-1","source_code":"default","quantity":%d,"status":1},
		{"sku":"SKU-1","source_code":"warehouse","quantity":%d,"status":1}
	],"total_count":2}`, from, to)
}

// transferScript drives the transfer fakes: successive GETs return the scripted
// quantity pairs (the last entry repeats) and a write returns writeStatus. The
// script, not the fake, decides what the post-write read observes, so partial
// and mismatched writes are expressible without a mutable backend.
type transferScript struct {
	gets        [][2]int
	getStatus   []int
	writeStatus int
}

// transferServer starts a fake MSI endpoint from a script and returns the
// service plus the recorded writes.
func transferServer(t *testing.T, script transferScript) (*Service, *[]sourceItemsBody, *int) {
	t.Helper()
	writes := &[]sourceItemsBody{}
	gets := new(int)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			n := *gets
			*gets++
			if n < len(script.getStatus) && script.getStatus[n] != 0 {
				w.WriteHeader(script.getStatus[n])
				_, _ = io.WriteString(w, `{"message":"read failed"}`)
				return
			}
			pair := script.gets[len(script.gets)-1]
			if n < len(script.gets) {
				pair = script.gets[n]
			}
			_, _ = io.WriteString(w, sourceItemsResponse(pair[0], pair[1]))
		case http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			var body sourceItemsBody
			_ = json.Unmarshal(raw, &body)
			*writes = append(*writes, body)
			if script.writeStatus >= 400 {
				w.WriteHeader(script.writeStatus)
				_, _ = io.WriteString(w, `{"message":"The source item could not be saved."}`)
				return
			}
			_, _ = io.WriteString(w, `[]`)
		}
	}))
	t.Cleanup(ts.Close)
	return testService(t, ts.URL), writes, gets
}

// intPtr returns a pointer to value, for the optional observed quantities.
func intPtr(value int) *int { return &value }

// equalIntPtr compares two optional quantities.
func equalIntPtr(got, want *int) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}

func TestTransferSourceQty(t *testing.T) {
	svc, writes, gets := transferServer(t, transferScript{gets: [][2]int{{10, 0}, {10, 0}, {7, 3}}})
	result, err := svc.TransferSourceQty(context.Background(), "SKU-1", "default", "warehouse", 3)
	if err != nil {
		t.Fatalf("TransferSourceQty: %v", err)
	}
	if result.Outcome != TransferApplied || !result.FromApplied || !result.ToApplied {
		t.Fatalf("result = %+v, want an applied transfer", result)
	}
	if !strings.HasPrefix(result.TransferID, "tr_") || len(result.TransferID) != len("tr_")+16 {
		t.Fatalf("transfer id = %q, want tr_<16 hex>", result.TransferID)
	}
	if result.FromBefore != 10 || !equalIntPtr(result.FromAfter, intPtr(7)) ||
		result.ToBefore != 0 || !equalIntPtr(result.ToAfter, intPtr(3)) {
		t.Fatalf("quantities = %+v", result)
	}
	if result.IntendedFrom != 7 || result.IntendedTo != 3 {
		t.Fatalf("intended = %+v", result)
	}
	if *gets != 3 {
		t.Fatalf("reads = %d, want 3 (read, re-read, read-back)", *gets)
	}
	if len(*writes) != 1 || len((*writes)[0].SourceItems) != 2 {
		t.Fatalf("writes = %+v", *writes)
	}
	posted := (*writes)[0]
	if posted.SourceItems[0].Quantity != 7 || posted.SourceItems[0].Status != 1 {
		t.Fatalf("from item = %+v", posted.SourceItems[0])
	}
	if posted.SourceItems[1].Quantity != 3 || posted.SourceItems[1].Status != 1 {
		t.Fatalf("to item = %+v", posted.SourceItems[1])
	}
}

func TestTransferSourceQtyInsufficient(t *testing.T) {
	svc, writes, _ := transferServer(t, transferScript{gets: [][2]int{{1, 0}}})
	_, err := svc.TransferSourceQty(context.Background(), "SKU-1", "default", "warehouse", 5)
	if errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("err = %v", err)
	}
	if len(*writes) != 0 {
		t.Fatal("insufficient transfer must not write")
	}
}

func TestTransferSourceQtyValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	cases := []struct {
		sku, from, to string
		qty           int
	}{
		{"bad sku!", "default", "warehouse", 1},
		{"SKU-1", "BAD SOURCE", "warehouse", 1},
		{"SKU-1", "default", "default", 1},
		{"SKU-1", "default", "warehouse", 0},
	}
	for i, tc := range cases {
		if _, err := svc.TransferSourceQty(ctx, tc.sku, tc.from, tc.to, tc.qty); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
}

// TestTransferSourceQtyTransferIDIsUnique asserts the caller-visible id differs
// per transfer.
func TestTransferSourceQtyTransferIDIsUnique(t *testing.T) {
	svc, _, _ := transferServer(t, transferScript{gets: [][2]int{{10, 0}, {10, 0}, {9, 1}, {9, 1}, {9, 1}, {8, 2}}})
	first, err := svc.TransferSourceQty(context.Background(), "SKU-1", "default", "warehouse", 1)
	if err != nil {
		t.Fatalf("first transfer: %v", err)
	}
	second, err := svc.TransferSourceQty(context.Background(), "SKU-1", "default", "warehouse", 1)
	if err != nil {
		t.Fatalf("second transfer: %v", err)
	}
	if first.TransferID == second.TransferID || first.TransferID == "" {
		t.Fatalf("transfer ids = %q, %q, want two distinct ids", first.TransferID, second.TransferID)
	}
}

// TestTransferSourceQtyOutcomes is the table-driven CONTRACT test for MAG-10 and
// MAG-11: every non-applied outcome is typed, carries the observed state, and
// never triggers a second write (no silent retry, no compensation).
func TestTransferSourceQtyOutcomes(t *testing.T) {
	tests := []struct {
		name          string
		script        transferScript
		wantOutcome   TransferOutcome
		wantCode      errs.Code
		wantWrites    int
		wantFromAfter *int
		wantToAfter   *int
		wantApplied   string
		wantDetail    string
		wantObserved  bool
	}{
		{
			// MAG-10: T1 read 10/0 at t0; T2 (another replica) committed 7/3 at
			// t1; T1 re-reads 7/3 at t2 and must abort instead of overwriting.
			name: "concurrent transfer detected (lost update)",
			script: transferScript{
				gets: [][2]int{{10, 0}, {7, 3}},
			},
			wantOutcome: TransferConflict, wantCode: errs.CodeConflict, wantWrites: 0,
			wantFromAfter: intPtr(7), wantToAfter: intPtr(3), wantApplied: "none",
			wantDetail: "another writer changed the source quantities", wantObserved: true,
		},
		{
			// MAG-11: the batch is not atomic; the source row lands and the
			// destination row is rejected.
			name: "partial application",
			script: transferScript{
				gets:        [][2]int{{10, 0}, {10, 0}, {7, 0}},
				writeStatus: http.StatusBadRequest,
			},
			wantOutcome: TransferPartial, wantCode: errs.CodeConflict, wantWrites: 1,
			wantFromAfter: intPtr(7), wantToAfter: intPtr(0), wantApplied: "from",
			wantDetail: "only the from row was applied", wantObserved: true,
		},
		{
			// Magento acknowledged the write but the state does not match.
			name: "verification mismatch",
			script: transferScript{
				gets: [][2]int{{10, 0}, {10, 0}, {10, 0}},
			},
			wantOutcome: TransferVerificationMismatch, wantCode: errs.CodeConflict, wantWrites: 1,
			wantFromAfter: intPtr(10), wantToAfter: intPtr(0), wantApplied: "none",
			wantDetail: "does not match the intended post-state", wantObserved: true,
		},
		{
			// The write was rejected and nothing changed: retry-safe.
			name: "not applied",
			script: transferScript{
				gets:        [][2]int{{10, 0}, {10, 0}, {10, 0}},
				writeStatus: http.StatusBadRequest,
			},
			wantOutcome: TransferNotApplied, wantCode: errs.CodeValidationFailed, wantWrites: 1,
			wantFromAfter: intPtr(10), wantToAfter: intPtr(0), wantApplied: "none",
			wantDetail: "neither source row changed", wantObserved: true,
		},
		{
			// A timeout with an unchanged read-back proves nothing: Magento may
			// still commit the lost response, so this must stay unverified.
			name: "timeout with unchanged state",
			script: transferScript{
				gets:        [][2]int{{10, 0}, {10, 0}, {10, 0}},
				writeStatus: http.StatusRequestTimeout,
			},
			wantOutcome: TransferNeedsVerification, wantCode: errs.CodeConflict, wantWrites: 1,
			wantFromAfter: intPtr(10), wantToAfter: intPtr(0), wantApplied: "unknown",
			wantDetail: "may still commit it", wantObserved: true,
		},
		{
			// The write failed and the read-back failed: the state is unknown and
			// the operator must verify. No compensation is issued.
			name: "needs verification",
			script: transferScript{
				gets:        [][2]int{{10, 0}},
				getStatus:   []int{0, 0, http.StatusInternalServerError},
				writeStatus: http.StatusInternalServerError,
			},
			wantOutcome: TransferNeedsVerification, wantCode: errs.CodeConflict, wantWrites: 1,
			wantFromAfter: nil, wantToAfter: nil, wantApplied: "unknown",
			wantDetail: "verify both source quantities",
		},
		{
			// The write reported an error but both rows hold the intended state:
			// the read-back is the ground truth, so this is a success and must not
			// invite a retry that would move another 3 units.
			name: "write error but both rows applied",
			script: transferScript{
				gets:        [][2]int{{10, 0}, {10, 0}, {7, 3}},
				writeStatus: http.StatusInternalServerError,
			},
			wantOutcome: TransferApplied, wantWrites: 1,
			wantFromAfter: intPtr(7), wantToAfter: intPtr(3), wantApplied: "both",
			wantDetail: "both rows reached the intended state", wantObserved: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, writes, _ := transferServer(t, tc.script)
			result, err := svc.TransferSourceQty(context.Background(), "SKU-1", "default", "warehouse", 3)
			if result == nil {
				t.Fatalf("result = nil, want the observed transfer state (err=%v)", err)
			}
			if result.Outcome != tc.wantOutcome {
				t.Fatalf("outcome = %q, want %q (%v)", result.Outcome, tc.wantOutcome, err)
			}
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil for an applied transfer", err)
				}
			} else {
				if got := errs.CodeOf(err); got != tc.wantCode {
					t.Fatalf("code = %s, want %s (%v)", got, tc.wantCode, err)
				}
			}
			if len(*writes) != tc.wantWrites {
				t.Fatalf("writes = %d, want %d (no retry, no compensation)", len(*writes), tc.wantWrites)
			}
			if !equalIntPtr(result.FromAfter, tc.wantFromAfter) || !equalIntPtr(result.ToAfter, tc.wantToAfter) {
				t.Fatalf("observed after = from %s to %s, want from %s to %s",
					observedAfter(result.FromAfter), observedAfter(result.ToAfter),
					observedAfter(tc.wantFromAfter), observedAfter(tc.wantToAfter))
			}
			// An unobserved post-state must omit the after quantities entirely: a
			// zero would read as "the source went to zero".
			encoded, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				t.Fatalf("result does not encode: %v", marshalErr)
			}
			if !result.AfterObserved && (strings.Contains(string(encoded), `"from_after"`) ||
				strings.Contains(string(encoded), `"to_after"`)) {
				t.Fatalf("unobserved result still carries after quantities: %s", encoded)
			}
			if got := result.AppliedRows(); got != tc.wantApplied {
				t.Fatalf("applied rows = %q, want %q", got, tc.wantApplied)
			}
			// An unobserved post-state must never be reported as "none": an agent
			// keying on that could conclude nothing was applied and retry.
			if !result.AfterObserved && result.AppliedRows() == "none" {
				t.Fatal("an unobserved transfer must not claim applied_rows=none")
			}
			if result.Outcome == TransferNeedsVerification && result.AppliedRows() != "unknown" {
				t.Fatalf("needs_verification reported applied rows %q, want unknown", result.AppliedRows())
			}
			if result.AfterObserved != tc.wantObserved {
				t.Fatalf("after_observed = %v, want %v", result.AfterObserved, tc.wantObserved)
			}
			if tc.wantDetail != "" && !strings.Contains(result.Detail, tc.wantDetail) {
				t.Fatalf("detail = %q, want it to contain %q", result.Detail, tc.wantDetail)
			}
			if result.TransferID == "" || result.FromSource != "default" || result.ToSource != "warehouse" || result.Quantity != 3 {
				t.Fatalf("result is missing the caller-visible transfer facts: %+v", result)
			}
			if retryable := errs.RetryableError(err); retryable != (tc.wantOutcome == TransferNotApplied) {
				t.Fatalf("retryable = %v, want %v for outcome %q", retryable, tc.wantOutcome == TransferNotApplied, tc.wantOutcome)
			}
			if tc.wantCode != "" {
				var typed *errs.Error
				if !errors.As(err, &typed) {
					t.Fatalf("err = %v, want a typed error", err)
				}
				if typed.Details["transfer_id"] != result.TransferID ||
					typed.Details["outcome"] != string(result.Outcome) ||
					typed.Details["applied_rows"] != tc.wantApplied {
					t.Fatalf("error details = %v, want the transfer facts", typed.Details)
				}
			}
		})
	}
}

// TestTransferSourceQtyConcurrentSameProcess runs two transfers of the same SKU
// concurrently against a stateful fake and asserts no units are lost: the
// per-SKU guard serialises them, so the second transfer sees the first one's
// result (4/6, not 7/3 twice).
func TestTransferSourceQtyConcurrentSameProcess(t *testing.T) {
	var mu sync.Mutex
	state := map[string]int{"default": 10, "warehouse": 0}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			mu.Lock()
			body := sourceItemsResponse(state["default"], state["warehouse"])
			mu.Unlock()
			_, _ = io.WriteString(w, body)
		case http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			var body sourceItemsBody
			_ = json.Unmarshal(raw, &body)
			mu.Lock()
			for _, item := range body.SourceItems {
				state[item.SourceCode] = item.Quantity
			}
			mu.Unlock()
			_, _ = io.WriteString(w, `[]`)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)

	results := make([]*TransferResult, 2)
	errsOut := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errsOut[i] = svc.TransferSourceQty(context.Background(), "SKU-1", "default", "warehouse", 3)
		}(i)
	}
	wg.Wait()
	for i, err := range errsOut {
		if err != nil {
			t.Fatalf("transfer %d: %v", i, err)
		}
		if results[i].Outcome != TransferApplied {
			t.Fatalf("transfer %d outcome = %q, want applied", i, results[i].Outcome)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if state["default"] != 4 || state["warehouse"] != 6 {
		t.Fatalf("state = %+v, want default=4 warehouse=6 (no lost update)", state)
	}
}

func TestCreateSourceAndLink(t *testing.T) {
	var paths []string
	var sourceBody inventorySourceBody
	var linkBody stockSourceLinkBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/inventory/sources":
			_ = json.Unmarshal(raw, &sourceBody)
			_, _ = w.Write([]byte(`{"source_code":"warehouse","name":"Warehouse","enabled":true,"country_id":"GB","city":"London"}`))
		case "/rest/default/V1/inventory/stock-source-links":
			_ = json.Unmarshal(raw, &linkBody)
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	source, err := svc.CreateSource(context.Background(), InventorySourceInput{
		SourceCode: "warehouse", Name: "Warehouse", Enabled: true, CountryID: "GB", City: "London",
	})
	if err != nil {
		t.Fatal(err)
	}
	if source.SourceCode != "warehouse" || !source.Enabled {
		t.Fatalf("source = %+v", source)
	}
	if sourceBody.Source.SourceCode != "warehouse" || sourceBody.Source.Name != "Warehouse" {
		t.Fatalf("body = %+v", sourceBody)
	}
	if err := svc.LinkStockSource(context.Background(), 1, "warehouse", 2); err != nil {
		t.Fatal(err)
	}
	// Magento's link endpoint takes `priority`; `position` is rejected outright.
	if len(linkBody.Links) != 1 || linkBody.Links[0].StockID != 1 || linkBody.Links[0].Priority != 2 {
		t.Fatalf("links = %+v", linkBody)
	}
}

func TestInventorySourceValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if _, err := svc.CreateSource(ctx, InventorySourceInput{SourceCode: "Bad Code", Name: "X"}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("code err = %v", err)
	}
	if _, err := svc.CreateSource(ctx, InventorySourceInput{SourceCode: "wh", Name: ""}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("name err = %v", err)
	}
	if _, err := svc.CreateSource(ctx, InventorySourceInput{SourceCode: "wh", Name: "X", CountryID: "gb"}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("country err = %v", err)
	}
	if _, err := svc.CreateSource(ctx, InventorySourceInput{SourceCode: "wh", Name: "X", Latitude: 100}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("latitude err = %v", err)
	}
	if err := svc.LinkStockSource(ctx, 0, "wh", 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("stock err = %v", err)
	}
	if err := svc.LinkStockSource(ctx, 1, "Bad Code", 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("source err = %v", err)
	}
	if err := svc.LinkStockSource(ctx, 1, "wh", -1); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("position err = %v", err)
	}
}

func TestUnlinkStockSource(t *testing.T) {
	var method, path string
	var body stockSourceLinkBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	if err := svc.UnlinkStockSource(context.Background(), 1, "warehouse"); err != nil {
		t.Fatal(err)
	}
	// MSI delete endpoints are POSTs with a -delete suffix; DELETE is not a route.
	if method != http.MethodPost || path != "/rest/default/V1/inventory/stock-source-links-delete" {
		t.Fatalf("request = %s %s", method, path)
	}
	if len(body.Links) != 1 || body.Links[0].StockID != 1 || body.Links[0].SourceCode != "warehouse" {
		t.Fatalf("body = %+v", body)
	}
	if err := svc.UnlinkStockSource(context.Background(), 0, "warehouse"); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("stock err = %v", err)
	}
}

// fractionalSourceItems renders a source-item page with fractional quantities.
func fractionalSourceItems(from, to float64) string {
	return fmt.Sprintf(`{"items":[
		{"sku":"SKU-1","source_code":"default","quantity":%g,"status":1},
		{"sku":"SKU-1","source_code":"warehouse","quantity":%g,"status":1}
	],"total_count":2}`, from, to)
}

// TestTransferSourceQtyRefusesFractionalQuantity is the MAG-R07 regression: a
// fractional observed quantity is refused with a typed error instead of being
// truncated into an integer write that is then reported as applied.
func TestTransferSourceQtyRefusesFractionalQuantity(t *testing.T) {
	tests := []struct {
		name string
		from float64
		to   float64
	}{
		{name: "fractional source", from: 7.9, to: 0},
		{name: "fractional destination", from: 10, to: 0.5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					writes++
					_, _ = io.WriteString(w, `[]`)
					return
				}
				_, _ = io.WriteString(w, fractionalSourceItems(tc.from, tc.to))
			}))
			defer ts.Close()
			svc := testService(t, ts.URL)
			result, err := svc.TransferSourceQty(context.Background(), "SKU-1", "default", "warehouse", 3)
			if errs.CodeOf(err) != errs.CodeUnsupportedCapability {
				t.Fatalf("err = %v, want unsupported_capability", err)
			}
			if !strings.Contains(err.Error(), "fractional") {
				t.Fatalf("err = %v, want the fractional quantity named", err)
			}
			if result != nil {
				t.Fatalf("result = %+v, want nil before any write", result)
			}
			if writes != 0 {
				t.Fatalf("writes = %d, want 0 (a fractional read must not be truncated into a write)", writes)
			}
		})
	}
}

// TestTransferSourceQtyFractionalReadBackIsUnknown proves a fractional read-back
// does not report the transfer as applied: the post-state was not established.
func TestTransferSourceQtyFractionalReadBackIsUnknown(t *testing.T) {
	gets := 0
	writes := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			writes++
			_, _ = io.WriteString(w, `[]`)
			return
		}
		gets++
		if gets <= 2 {
			// read and compare-before-write see whole units
			_, _ = io.WriteString(w, fractionalSourceItems(10, 0))
			return
		}
		// the read-back sees a fractional destination row
		_, _ = io.WriteString(w, fractionalSourceItems(7, 2.9))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	result, err := svc.TransferSourceQty(context.Background(), "SKU-1", "default", "warehouse", 3)
	if result == nil {
		t.Fatalf("result = nil, want the observed transfer state (err=%v)", err)
	}
	if result.Outcome != TransferNeedsVerification {
		t.Fatalf("outcome = %q, want needs_verification", result.Outcome)
	}
	if result.AfterObserved {
		t.Fatal("after_observed = true, want false when the read-back was refused")
	}
	if got := result.AppliedRows(); got != "unknown" {
		t.Fatalf("applied_rows = %q, want unknown", got)
	}
	if errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("code = %s, want conflict", errs.CodeOf(err))
	}
	if errs.RetryableError(err) {
		t.Fatal("a needs_verification transfer must not be retryable")
	}
	var typed *errs.Error
	if !errors.As(err, &typed) {
		t.Fatalf("err = %v, want a typed error", err)
	}
	if typed.Details["applied_rows"] != "unknown" || typed.Details["after_observed"] != "false" {
		t.Fatalf("details = %v, want applied_rows=unknown after_observed=false", typed.Details)
	}
	if writes != 1 {
		t.Fatalf("writes = %d, want 1 (no retry, no compensation)", writes)
	}
}
