// SPDX-License-Identifier: Apache-2.0

package storage

// Regression tests for the operation ledger (docs/audit/reliability-review.md §3,
// fault-injection cases F1, F2, F4, F5, F6, F7). These are properties of the SQL
// state machine, so every one of them needs a real PostgreSQL.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/security/audit"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
)

// ledgerFixture returns a ledger plus an instance id unique to the test, and
// clears that instance's rows before and after so the suite is repeatable on a
// database that already holds other tests' data.
func ledgerFixture(t *testing.T, name string) (*OperationLedger, *Store, string) {
	t.Helper()
	store := testStore(t)
	ledger := NewOperationLedger(store)
	instance := "store-ledger-" + name
	// The reconciler claims "any due unknown row", so these tests must own the
	// whole table. They run sequentially in this package, and the ledger is not
	// used by any other test yet.
	clean := func() {
		ctx := context.Background()
		if _, err := store.pool.Exec(ctx, `DELETE FROM operation_transitions`); err != nil {
			t.Fatalf("cleanup transitions: %v", err)
		}
		if _, err := store.pool.Exec(ctx, `DELETE FROM operations`); err != nil {
			t.Fatalf("cleanup operations: %v", err)
		}
	}
	clean()
	t.Cleanup(clean)
	return ledger, store, instance
}

// intent builds a valid Operation for the fixture.
func intent(t *testing.T, instance, key string, args json.RawMessage) Operation {
	t.Helper()
	hash, err := ArgsHash(args)
	if err != nil {
		t.Fatalf("ArgsHash: %v", err)
	}
	return Operation{
		IdempotencyKey: key,
		Operation:      "magento.invoice.capture",
		InstanceID:     instance,
		Subject:        "agent-1",
		EntityType:     EntityInvoice,
		EntityID:       "100000123",
		ArgsHash:       hash,
	}
}

func money(v int64) *int64 { return &v }

// makeDue advances the reconciler's backoff for one operation, which is what a
// clock would do. Unknown() schedules a real delay, so a test that wants to
// reconcile immediately has to move the deadline.
func makeDue(t *testing.T, store *Store, id string) {
	t.Helper()
	if _, err := store.pool.Exec(context.Background(),
		`UPDATE operations SET reconcile_after = now() - interval '1 second' WHERE id = $1::uuid`, id); err != nil {
		t.Fatalf("make due: %v", err)
	}
}

// mustProceed asserts Begin returned a fresh intent and claims it.
func mustProceed(ctx context.Context, t *testing.T, ledger *OperationLedger, op Operation) (string, Execution) {
	t.Helper()
	res, err := ledger.Begin(ctx, op)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if res.Decision != DecisionProceed {
		t.Fatalf("Begin decision = %s, want proceed", res.Decision)
	}
	exec, applied, err := ledger.Execute(ctx, res.OperationID, time.Minute)
	if err != nil || !applied {
		t.Fatalf("Execute: applied=%v err=%v", applied, err)
	}
	return res.OperationID, exec
}

// F1: a stale attempt cannot write. After the fence is regenerated, every write
// carrying the old token affects 0 rows and returns a typed conflict.
func TestLedgerStaleFenceIsRejected(t *testing.T) {
	ledger, _, instance := ledgerFixture(t, "fence")
	ctx := context.Background()
	id, first := mustProceed(ctx, t, ledger, intent(t, instance, "key-fence", json.RawMessage(`{"amount":10}`)))

	// Force the row back to pending so a second Execute can take ownership: this
	// is the shape of an operator retry, and it regenerates the fence.
	if applied, err := ledger.Abort(ctx, id, OpExecuting, "test reset"); err != nil || !applied {
		t.Fatalf("Abort: applied=%v err=%v", applied, err)
	}
	if applied, err := ledger.Retry(ctx, id); err != nil || !applied {
		t.Fatalf("Retry: applied=%v err=%v", applied, err)
	}
	second, applied, err := ledger.Execute(ctx, id, time.Minute)
	if err != nil || !applied {
		t.Fatalf("second Execute: applied=%v err=%v", applied, err)
	}
	if first.Fence == second.Fence {
		t.Fatal("re-claim reused the fencing token")
	}

	// The first attempt, still running, now reports. Nothing may land.
	if applied, err := ledger.Succeed(ctx, id, first.Fence, "inv-1", nil); applied || err == nil {
		t.Fatalf("stale Succeed: applied=%v err=%v, want applied=false and a conflict", applied, err)
	} else if errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("stale Succeed error code = %s, want conflict", errs.CodeOf(err))
	}
	if applied, err := ledger.Fail(ctx, id, first.Fence, "upstream_error"); applied || err == nil {
		t.Fatalf("stale Fail: applied=%v err=%v", applied, err)
	}
	if applied, err := ledger.Unknown(ctx, id, first.Fence, "upstream_timeout", time.Minute); applied || err == nil {
		t.Fatalf("stale Unknown: applied=%v err=%v", applied, err)
	}
	rec, err := ledger.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != OpExecuting || rec.Fence != second.Fence {
		t.Fatalf("after stale writes: state=%s fence=%s, want executing/%s", rec.State, rec.Fence, second.Fence)
	}
	// The live attempt still owns it.
	if applied, err := ledger.Succeed(ctx, id, second.Fence, "inv-1", json.RawMessage(`{"ok":true}`)); err != nil || !applied {
		t.Fatalf("live Succeed: applied=%v err=%v", applied, err)
	}
}

// F2: two goroutines racing T2 on one pending row produce exactly one owner.
func TestLedgerConcurrentExecuteSingleOwner(t *testing.T) {
	ledger, _, instance := ledgerFixture(t, "race")
	ctx := context.Background()
	res, err := ledger.Begin(ctx, intent(t, instance, "key-race", json.RawMessage(`{"amount":1}`)))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	const racers = 8
	var wins atomic.Int32
	var fences sync.Map
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			exec, applied, err := ledger.Execute(ctx, res.OperationID, time.Minute)
			if err != nil {
				// A loser gets a typed conflict, never a silent success.
				if errs.CodeOf(err) != errs.CodeConflict && errs.CodeOf(err) != errs.CodeIllegalTransition {
					t.Errorf("loser error code = %s (%v)", errs.CodeOf(err), err)
				}
				return
			}
			if applied {
				wins.Add(1)
				fences.Store(exec.Fence, true)
			}
		}()
	}
	close(start)
	wg.Wait()

	if wins.Load() != 1 {
		t.Fatalf("%d goroutines claimed the operation, want exactly 1", wins.Load())
	}
	rec, err := ledger.Get(ctx, res.OperationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != OpExecuting || rec.Attempt != 1 {
		t.Fatalf("state=%s attempt=%d, want executing/1", rec.State, rec.Attempt)
	}
}

// Replay semantics (§3.5): every state tells the caller exactly what to do.
func TestLedgerReplaySemantics(t *testing.T) {
	ledger, store, instance := ledgerFixture(t, "replay")
	ctx := context.Background()
	args := json.RawMessage(`{"amount":42}`)
	op := intent(t, instance, "key-replay", args)

	// pending -> conflict/in_progress.
	res, err := ledger.Begin(ctx, op)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	again, err := ledger.Begin(ctx, op)
	if errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("repeat on pending: code=%s err=%v, want conflict", errs.CodeOf(err), err)
	}
	if again.Decision != DecisionInProgress || again.OperationID != res.OperationID {
		t.Fatalf("repeat on pending: decision=%s id=%s", again.Decision, again.OperationID)
	}

	// executing -> the operation id, no error, poll.
	exec, applied, err := ledger.Execute(ctx, res.OperationID, time.Minute)
	if err != nil || !applied {
		t.Fatalf("Execute: applied=%v err=%v", applied, err)
	}
	polled, err := ledger.Begin(ctx, op)
	if err != nil {
		t.Fatalf("repeat on executing: %v", err)
	}
	if polled.Decision != DecisionInProgress || polled.OperationID != res.OperationID || polled.Fence != exec.Fence {
		t.Fatalf("repeat on executing = %+v, want in_progress with the live fence", polled)
	}

	// succeeded -> replayed result.
	stored := json.RawMessage(`{"invoice_id":555,"state":"paid"}`)
	if applied, err := ledger.Succeed(ctx, res.OperationID, exec.Fence, "inv-555", stored); err != nil || !applied {
		t.Fatalf("Succeed: applied=%v err=%v", applied, err)
	}
	replay, err := ledger.Begin(ctx, op)
	if err != nil {
		t.Fatalf("repeat on succeeded: %v", err)
	}
	if replay.Decision != DecisionReplay || !replay.Replayed {
		t.Fatalf("repeat on succeeded = %+v, want replay", replay)
	}
	var decoded map[string]any
	if err := json.Unmarshal(replay.Result, &decoded); err != nil {
		t.Fatalf("replayed result is not JSON: %v (%s)", err, replay.Result)
	}
	if decoded["invoice_id"] != float64(555) || replay.UpstreamRef != "inv-555" {
		t.Fatalf("replayed result = %s upstream_ref=%q", replay.Result, replay.UpstreamRef)
	}

	// failed -> retry permitted.
	failedID, failedExec := mustProceed(ctx, t, ledger, intent(t, instance, "key-replay-failed", args))
	if applied, err := ledger.Fail(ctx, failedID, failedExec.Fence, "upstream_rejected"); err != nil || !applied {
		t.Fatalf("Fail: applied=%v err=%v", applied, err)
	}
	retryable, err := ledger.Begin(ctx, intent(t, instance, "key-replay-failed", args))
	if err != nil {
		t.Fatalf("repeat on failed: %v", err)
	}
	if retryable.Decision != DecisionRetry {
		t.Fatalf("repeat on failed: decision=%s, want retry", retryable.Decision)
	}
	if applied, err := ledger.Retry(ctx, failedID); err != nil || !applied {
		t.Fatalf("Retry: applied=%v err=%v", applied, err)
	}
	if applied, err := ledger.Retry(ctx, failedID); applied || err == nil {
		t.Fatal("a second Retry from pending must not apply")
	}

	// unknown and reconciling -> operation_unknown, never a retry.
	for _, state := range []string{OpUnknown, OpReconciling} {
		key := "key-replay-" + state
		id, exec2 := mustProceed(ctx, t, ledger, intent(t, instance, key, args))
		if applied, err := ledger.Unknown(ctx, id, exec2.Fence, "upstream_timeout", time.Minute); err != nil || !applied {
			t.Fatalf("Unknown: applied=%v err=%v", applied, err)
		}
		makeDue(t, store, id)
		if state == OpReconciling {
			if _, applied, err := ledger.ClaimReconcilable(ctx, time.Minute); err != nil || !applied {
				t.Fatalf("ClaimReconcilable: applied=%v err=%v", applied, err)
			}
		}
		got, err := ledger.Begin(ctx, intent(t, instance, key, args))
		if errs.CodeOf(err) != errs.CodeOperationUnknown {
			t.Fatalf("repeat on %s: code=%s err=%v, want operation_unknown", state, errs.CodeOf(err), err)
		}
		if got.Decision != DecisionUnknown {
			t.Fatalf("repeat on %s: decision=%s, want unknown", state, got.Decision)
		}
	}
}

// A reused key with different arguments is an error, not a replay.
func TestLedgerIdempotencyKeyReuse(t *testing.T) {
	ledger, _, instance := ledgerFixture(t, "reuse")
	ctx := context.Background()
	first := intent(t, instance, "key-reuse", json.RawMessage(`{"amount":10}`))
	if _, err := ledger.Begin(ctx, first); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	changed := first
	hash, err := ArgsHash(json.RawMessage(`{"amount":999}`))
	if err != nil {
		t.Fatalf("ArgsHash: %v", err)
	}
	changed.ArgsHash = hash
	if _, err := ledger.Begin(ctx, changed); errs.CodeOf(err) != errs.CodeIdempotencyKeyReuse {
		t.Fatalf("reuse with different args: code=%s err=%v, want idempotency_key_reuse", errs.CodeOf(err), err)
	}
}

// F5: terminal states are final. Nothing moves a succeeded row, and a failed row
// only moves through the explicit Retry.
func TestLedgerTerminalStatesAreFinal(t *testing.T) {
	ledger, _, instance := ledgerFixture(t, "terminal")
	ctx := context.Background()

	id, exec := mustProceed(ctx, t, ledger, intent(t, instance, "key-terminal", json.RawMessage(`{"amount":1}`)))
	if applied, err := ledger.Succeed(ctx, id, exec.Fence, "inv-1", nil); err != nil || !applied {
		t.Fatalf("Succeed: applied=%v err=%v", applied, err)
	}
	for name, call := range map[string]func() (bool, error){
		"fail": func() (bool, error) { return ledger.Fail(ctx, id, exec.Fence, "x") },
		"unknown": func() (bool, error) {
			return ledger.Unknown(ctx, id, exec.Fence, "upstream_timeout", time.Minute)
		},
		"execute": func() (bool, error) { _, applied, err := ledger.Execute(ctx, id, time.Minute); return applied, err },
		"retry":   func() (bool, error) { return ledger.Retry(ctx, id) },
		"abort":   func() (bool, error) { return ledger.Abort(ctx, id, OpExecuting, "nope") },
	} {
		if applied, err := call(); applied {
			t.Fatalf("%s moved a succeeded operation", name)
		} else if err != nil && errs.CodeOf(err) != errs.CodeConflict && errs.CodeOf(err) != errs.CodeIllegalTransition && errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("%s: unexpected error %v", name, err)
		}
	}
	rec, err := ledger.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != OpSucceeded || rec.FinishedAt == nil {
		t.Fatalf("state=%s finished=%v, want succeeded with finished_at", rec.State, rec.FinishedAt)
	}

	// A failed row can only leave through Retry.
	failID, failExec := mustProceed(ctx, t, ledger, intent(t, instance, "key-terminal-failed", json.RawMessage(`{"amount":2}`)))
	if applied, err := ledger.Fail(ctx, failID, failExec.Fence, "upstream_rejected"); err != nil || !applied {
		t.Fatalf("Fail: applied=%v err=%v", applied, err)
	}
	if _, applied, err := ledger.Execute(ctx, failID, time.Minute); applied || err == nil {
		t.Fatal("Execute moved a failed operation without a retry")
	}
	if applied, err := ledger.Succeed(ctx, failID, failExec.Fence, "inv-2", nil); applied || err == nil {
		t.Fatal("Succeed moved a failed operation")
	}
}

// F6: unknown is reachable, reconciling follows only from unknown, and a
// resolution closes the operation.
func TestLedgerUnknownThenReconcile(t *testing.T) {
	ledger, store, instance := ledgerFixture(t, "reconcile")
	ctx := context.Background()
	id, exec := mustProceed(ctx, t, ledger, intent(t, instance, "key-reconcile", json.RawMessage(`{"amount":7}`)))

	// Nothing is reconcilable before an ambiguous outcome exists for this fixture.
	if rec, applied, err := ledger.ClaimReconcilable(ctx, time.Minute); err != nil {
		t.Fatalf("ClaimReconcilable: %v", err)
	} else if applied && rec.InstanceID == instance {
		t.Fatal("claimed an operation that is not unknown")
	}

	if applied, err := ledger.Unknown(ctx, id, exec.Fence, "upstream_timeout", 0); err != nil || !applied {
		t.Fatalf("Unknown: applied=%v err=%v", applied, err)
	}
	makeDue(t, store, id)
	rec, err := ledger.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != OpUnknown || rec.FinishedAt != nil {
		t.Fatalf("state=%s finished=%v, want unknown with no finished_at", rec.State, rec.FinishedAt)
	}

	// Reconciling is only reachable through T6, and T6 needs a fresh fence.
	claimed, applied, err := ledger.ClaimReconcilable(ctx, time.Minute)
	if err != nil || !applied || claimed == nil {
		t.Fatalf("ClaimReconcilable: %v applied=%v", err, applied)
	}
	if claimed.OperationID != id || claimed.Fence == exec.Fence {
		t.Fatalf("claimed %+v, want the unknown operation with a new fence", claimed)
	}
	if claimed.ReconcileCount != 1 {
		t.Fatalf("reconcile_count = %d, want 1", claimed.ReconcileCount)
	}

	// T7 from reconciling.
	if applied, err := ledger.Resolve(ctx, id, claimed.Fence, ResolvedSucceeded, "inv-77", "", json.RawMessage(`{"invoice_id":77}`), nil); err != nil || !applied {
		t.Fatalf("Resolve succeeded: applied=%v err=%v", applied, err)
	}
	rec, err = ledger.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != OpSucceeded || rec.FinishedAt == nil || rec.UpstreamRef != "inv-77" {
		t.Fatalf("after resolve: state=%s finished=%v upstream=%q", rec.State, rec.FinishedAt, rec.UpstreamRef)
	}

	// The same fence cannot resolve twice.
	if applied, err := ledger.Resolve(ctx, id, claimed.Fence, ResolvedFailed, "", "x", nil, nil); applied || err == nil {
		t.Fatal("a second Resolve applied")
	}

	// T8: a reconciling row the upstream proves was not applied ends failed.
	failID, failExec := mustProceed(ctx, t, ledger, intent(t, instance, "key-reconcile-failed", json.RawMessage(`{"amount":8}`)))
	if applied, err := ledger.Unknown(ctx, failID, failExec.Fence, "upstream_timeout", 0); err != nil || !applied {
		t.Fatalf("Unknown: applied=%v err=%v", applied, err)
	}
	makeDue(t, store, failID)
	claimed2, applied, err := ledger.ClaimReconcilable(ctx, time.Minute)
	if err != nil || !applied || claimed2 == nil || claimed2.OperationID != failID {
		t.Fatalf("second ClaimReconcilable = %+v, %v", claimed2, err)
	}
	if applied, err := ledger.Resolve(ctx, failID, claimed2.Fence, ResolvedFailed, "", "not_applied", nil, nil); err != nil || !applied {
		t.Fatalf("Resolve failed: applied=%v err=%v", applied, err)
	}
	rec, err = ledger.Get(ctx, failID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != OpFailed || rec.ErrorCode != "not_applied" {
		t.Fatalf("state=%s code=%q, want failed/not_applied", rec.State, rec.ErrorCode)
	}

	// T9: an inconclusive read backs off and stays unknown.
	unknownID, unknownExec := mustProceed(ctx, t, ledger, intent(t, instance, "key-reconcile-unknown", json.RawMessage(`{"amount":9}`)))
	if applied, err := ledger.Unknown(ctx, unknownID, unknownExec.Fence, "upstream_timeout", 0); err != nil || !applied {
		t.Fatalf("Unknown: applied=%v err=%v", applied, err)
	}
	makeDue(t, store, unknownID)
	claimed3, applied, err := ledger.ClaimReconcilable(ctx, time.Minute)
	if err != nil || !applied || claimed3 == nil || claimed3.OperationID != unknownID {
		t.Fatalf("third ClaimReconcilable = %+v, %v", claimed3, err)
	}
	if applied, err := ledger.Resolve(ctx, unknownID, claimed3.Fence, ResolvedUnknown, "", "inconclusive", nil, nil); err != nil || !applied {
		t.Fatalf("Resolve unknown: applied=%v err=%v", applied, err)
	}
	rec, err = ledger.Get(ctx, unknownID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != OpUnknown || rec.ReconcileCount != 1 {
		t.Fatalf("state=%s reconcile_count=%d, want unknown/1", rec.State, rec.ReconcileCount)
	}
}

// F7 / acceptance: operation_transitions records every transition with the fence
// that caused it, in order.
func TestLedgerTransitionHistory(t *testing.T) {
	ledger, store, instance := ledgerFixture(t, "history")
	ctx := context.Background()
	id, exec := mustProceed(ctx, t, ledger, intent(t, instance, "key-history", json.RawMessage(`{"amount":3}`)))
	if applied, err := ledger.Unknown(ctx, id, exec.Fence, "upstream_timeout", 0); err != nil || !applied {
		t.Fatalf("Unknown: applied=%v err=%v", applied, err)
	}
	makeDue(t, store, id)
	claimed, applied, err := ledger.ClaimReconcilable(ctx, time.Minute)
	if err != nil || !applied || claimed.OperationID != id {
		t.Fatalf("ClaimReconcilable: %+v %v", claimed, err)
	}
	if applied, err := ledger.Resolve(ctx, id, claimed.Fence, ResolvedSucceeded, "inv-3", "", nil, nil); err != nil || !applied {
		t.Fatalf("Resolve: applied=%v err=%v", applied, err)
	}

	history, err := ledger.Transitions(ctx, id)
	if err != nil {
		t.Fatalf("Transitions: %v", err)
	}
	want := []struct{ from, to, reason string }{
		{"none", OpPending, "begin"},
		{OpPending, OpExecuting, "execute"},
		{OpExecuting, OpUnknown, "unknown"},
		{OpUnknown, OpReconciling, "reconcile_claim"},
		{OpReconciling, OpSucceeded, "reconciled_succeeded"},
	}
	if len(history) != len(want) {
		t.Fatalf("history has %d entries, want %d: %+v", len(history), len(want), history)
	}
	for i, w := range want {
		got := history[i]
		if got.FromState != w.from || got.ToState != w.to || got.Reason != w.reason {
			t.Fatalf("history[%d] = %s->%s (%s), want %s->%s (%s)",
				i, got.FromState, got.ToState, got.Reason, w.from, w.to, w.reason)
		}
		if got.Fence == "" {
			t.Fatalf("history[%d] has no fence", i)
		}
	}
	if history[1].Fence != exec.Fence {
		t.Fatalf("execute transition fence = %s, want %s", history[1].Fence, exec.Fence)
	}
	if history[3].Fence != claimed.Fence {
		t.Fatalf("reconcile claim fence = %s, want %s", history[3].Fence, claimed.Fence)
	}
	// The attempt counter advances with ownership, not with every transition.
	if history[1].Attempt != 1 || history[4].Attempt != 1 {
		t.Fatalf("attempts = %d/%d, want 1/1", history[1].Attempt, history[4].Attempt)
	}
}

// F4: a reconciler takes one row at a time, in order, and never the same row twice.
func TestLedgerClaimReconcilableIsExclusive(t *testing.T) {
	ledger, store, instance := ledgerFixture(t, "claim")
	ctx := context.Background()
	const n = 5
	for i := 0; i < n; i++ {
		id, exec := mustProceed(ctx, t, ledger, intent(t, instance, fmt.Sprintf("key-claim-%d", i), json.RawMessage(`{"amount":1}`)))
		if applied, err := ledger.Unknown(ctx, id, exec.Fence, "upstream_timeout", 0); err != nil || !applied {
			t.Fatalf("Unknown: applied=%v err=%v", applied, err)
		}
		makeDue(t, store, id)
	}

	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				rec, applied, err := ledger.ClaimReconcilable(ctx, time.Minute)
				if err != nil {
					t.Errorf("ClaimReconcilable: %v", err)
					return
				}
				if !applied {
					return
				}
				mu.Lock()
				seen[rec.OperationID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("claimed %d distinct operations, want %d", len(seen), n)
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("operation %s claimed %d times", id, count)
		}
	}
}

// T10/T11: an operator can end a stuck operation, and a definite failure can be
// retried — but an ambiguous one cannot.
func TestLedgerAbortAndRetry(t *testing.T) {
	ledger, store, instance := ledgerFixture(t, "abort")
	ctx := context.Background()

	id, exec := mustProceed(ctx, t, ledger, intent(t, instance, "key-abort", json.RawMessage(`{"amount":1}`)))
	if applied, err := ledger.Abort(ctx, id, OpExecuting, "operator confirmed no charge"); err != nil || !applied {
		t.Fatalf("Abort: applied=%v err=%v", applied, err)
	}
	rec, err := ledger.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != OpFailed || rec.ErrorCode != "admin_abort" || rec.FinishedAt == nil {
		t.Fatalf("after abort: state=%s code=%q finished=%v", rec.State, rec.ErrorCode, rec.FinishedAt)
	}
	// Abort is not available from a terminal state.
	if applied, err := ledger.Abort(ctx, id, OpExecuting, "again"); applied || err == nil {
		t.Fatal("Abort applied to a terminal operation")
	}
	// Retry re-opens it.
	if applied, err := ledger.Retry(ctx, id); err != nil || !applied {
		t.Fatalf("Retry: applied=%v err=%v", applied, err)
	}
	if rec, _ := ledger.Get(ctx, id); rec.State != OpPending || rec.FinishedAt != nil {
		t.Fatalf("after retry: state=%s finished=%v, want pending/nil", rec.State, rec.FinishedAt)
	}
	_ = exec

	// An ambiguous outcome is not retryable: record it as failed with a reserved
	// code only through the reconciler, and Retry must refuse it.
	ambID, ambExec := mustProceed(ctx, t, ledger, intent(t, instance, "key-abort-amb", json.RawMessage(`{"amount":2}`)))
	if _, err := ledger.Fail(ctx, ambID, ambExec.Fence, "upstream_timeout"); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("Fail with an ambiguous code: %v, want invalid_argument", err)
	}
	if applied, err := ledger.Unknown(ctx, ambID, ambExec.Fence, "upstream_timeout", 0); err != nil || !applied {
		t.Fatalf("Unknown: applied=%v err=%v", applied, err)
	}
	makeDue(t, store, ambID)
	if applied, err := ledger.Retry(ctx, ambID); applied || err == nil {
		t.Fatal("Retry re-opened an operation whose outcome is unknown")
	}
}

// Validation: money is minor units plus a currency, args_hash is the canonical
// hash, and the module is derivable.
func TestLedgerRejectsInvalidOperations(t *testing.T) {
	ledger, _, instance := ledgerFixture(t, "invalid")
	ctx := context.Background()
	base := intent(t, instance, "key-invalid", json.RawMessage(`{"amount":1}`))

	cases := map[string]func(o Operation) Operation{
		"empty key":           func(o Operation) Operation { o.IdempotencyKey = ""; return o },
		"empty operation":     func(o Operation) Operation { o.Operation = ""; return o },
		"empty instance":      func(o Operation) Operation { o.InstanceID = ""; return o },
		"empty subject":       func(o Operation) Operation { o.Subject = ""; return o },
		"unknown entity":      func(o Operation) Operation { o.EntityType = "planet"; return o },
		"empty entity id":     func(o Operation) Operation { o.EntityID = ""; return o },
		"money without curr":  func(o Operation) Operation { o.AmountMinor = money(100); return o },
		"currency only":       func(o Operation) Operation { o.Currency = "USD"; return o },
		"bad currency":        func(o Operation) Operation { o.AmountMinor = money(100); o.Currency = "usd"; return o },
		"short args hash":     func(o Operation) Operation { o.ArgsHash = "abcd"; return o },
		"non-hex args hash":   func(o Operation) Operation { o.ArgsHash = string(make([]byte, 64)); return o },
		"unknown module":      func(o Operation) Operation { o.Operation = "shopify.order.create"; return o },
		"bad confirmation id": func(o Operation) Operation { o.ConfirmationID = "not-a-uuid"; return o },
	}
	for name, mutate := range cases {
		op := mutate(base)
		if _, err := ledger.Begin(ctx, op); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("%s: code=%s err=%v, want invalid_argument", name, errs.CodeOf(err), err)
		}
	}
	// A valid operation with money stores integer minor units.
	op := base
	op.IdempotencyKey = "key-invalid-money"
	op.AmountMinor = money(1999)
	op.Currency = "USD"
	op.ConfirmationID = "11111111-1111-1111-1111-111111111111"
	res, err := ledger.Begin(ctx, op)
	if err != nil {
		t.Fatalf("Begin with money: %v", err)
	}
	rec, err := ledger.Get(ctx, res.OperationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.AmountMinor == nil || *rec.AmountMinor != 1999 || rec.Currency != "USD" {
		t.Fatalf("stored money = %v %q, want 1999 USD", rec.AmountMinor, rec.Currency)
	}
}

// The schema keeps itself honest: a terminal row must have finished_at, a
// non-terminal row must not, and money must be paired with a currency.
func TestLedgerSchemaInvariants(t *testing.T) {
	// ledgerFixture opens the store and migrates it; a second testStore call here
	// only shadowed it.
	ledger, store, instance := ledgerFixture(t, "schema")
	ctx := context.Background()
	id, _ := mustProceed(ctx, t, ledger, intent(t, instance, "key-schema", json.RawMessage(`{"amount":1}`)))

	if _, err := store.pool.Exec(ctx,
		`UPDATE operations SET state='succeeded' WHERE id=$1::uuid`, id); err == nil {
		t.Fatal("a succeeded row with no finished_at was accepted")
	}
	if _, err := store.pool.Exec(ctx,
		`UPDATE operations SET finished_at=now() WHERE id=$1::uuid`, id); err == nil {
		t.Fatal("a finished_at on a non-terminal row was accepted")
	}
	if _, err := store.pool.Exec(ctx,
		`UPDATE operations SET amount_minor=100 WHERE id=$1::uuid`, id); err == nil {
		t.Fatal("money without a currency was accepted")
	}
}

// Every terminal transition is fenced and state-guarded in the SQL text itself,
// so a later refactor cannot quietly drop the guard.
func TestLedgerStatementsAreGuarded(t *testing.T) {
	for name, guard := range map[string]string{"executing": guardExecuting, "reconciling": guardReconciling} {
		for _, want := range []string{"id = $1::uuid", "fence = $2::uuid", "state IN ('" + name + "')"} {
			if !contains(guard, want) {
				t.Fatalf("%s guard %q is missing %q", name, guard, want)
			}
		}
	}
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// The ledger's args_hash must be the exact hash the confirmation flow binds an
// approval to, so a confirmation and an operation for the same call correlate.
func TestLedgerArgsHashMatchesConfirmation(t *testing.T) {
	ledger, store, instance := ledgerFixture(t, "argshash")
	ctx := context.Background()
	args := json.RawMessage(`{"invoice_id":100000123,"capture":true,"amount":{"value":19.99,"currency":"USD"}}`)

	ledgerHash, err := ArgsHash(args)
	if err != nil {
		t.Fatalf("ArgsHash: %v", err)
	}
	confirmationHash, err := confirm.CanonicalHash(args)
	if err != nil {
		t.Fatalf("confirm.CanonicalHash: %v", err)
	}
	if ledgerHash != confirmationHash {
		t.Fatalf("ledger hash %s != confirmation hash %s", ledgerHash, confirmationHash)
	}
	// Key order and whitespace must not change the hash.
	reordered, err := ArgsHash(json.RawMessage(`{"amount":{"currency":"USD","value":19.99}, "capture":true,"invoice_id":100000123}`))
	if err != nil {
		t.Fatalf("ArgsHash: %v", err)
	}
	if reordered != ledgerHash {
		t.Fatalf("hash is not canonical: %s != %s", reordered, ledgerHash)
	}

	op := intent(t, instance, "key-argshash", args)
	op.Operation = "magento.invoice.capture"
	res, err := ledger.Begin(ctx, op)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	var stored string
	if err := store.pool.QueryRow(ctx,
		`SELECT encode(args_hash,'hex') FROM operations WHERE id = $1::uuid`, res.OperationID).Scan(&stored); err != nil {
		t.Fatalf("read args_hash: %v", err)
	}
	if stored != confirmationHash {
		t.Fatalf("stored args_hash %s != confirmation hash %s", stored, confirmationHash)
	}
}

// DeriveKey is deterministic per (subject, arguments) and differs across subjects.
func TestLedgerDeriveKey(t *testing.T) {
	hash, err := ArgsHash(json.RawMessage(`{"a":1}`))
	if err != nil {
		t.Fatalf("ArgsHash: %v", err)
	}
	first := DeriveKey("agent-1", hash)
	if first != DeriveKey("agent-1", hash) {
		t.Fatal("DeriveKey is not deterministic")
	}
	if first == DeriveKey("agent-2", hash) {
		t.Fatal("DeriveKey does not separate subjects")
	}
	if len(first) > 256 {
		t.Fatalf("derived key is %d characters, longer than the column allows", len(first))
	}
}

// A row recorded as failed with an ambiguous code must not advertise a retry:
// Begin says "unknown", and both Retry and Resolve(failed) refuse it.
func TestLedgerAmbiguousFailureIsNotRetryable(t *testing.T) {
	ledger, store, instance := ledgerFixture(t, "ambiguous")
	ctx := context.Background()
	op := intent(t, instance, "key-ambiguous", json.RawMessage(`{"amount":5}`))
	res, err := ledger.Begin(ctx, op)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	// Only reachable by a bad manual write or an older row: Fail refuses this code.
	if _, err := store.pool.Exec(ctx, `
		UPDATE operations SET state='failed', finished_at=now(), error_code='ambiguous'
		WHERE id = $1::uuid`, res.OperationID); err != nil {
		t.Fatalf("seed ambiguous failure: %v", err)
	}
	got, err := ledger.Begin(ctx, op)
	if errs.CodeOf(err) != errs.CodeOperationUnknown {
		t.Fatalf("Begin on an ambiguous failure: code=%s err=%v, want operation_unknown", errs.CodeOf(err), err)
	}
	if got.Decision != DecisionUnknown {
		t.Fatalf("Begin on an ambiguous failure: decision=%s, want unknown", got.Decision)
	}
	if applied, err := ledger.Retry(ctx, res.OperationID); applied || err == nil {
		t.Fatal("Retry re-opened an ambiguously failed operation")
	}
}

// swapLedgerClock pins the derived-key window's clock for one test and restores
// it afterwards, so a window boundary is a step instead of a sleep.
func swapLedgerClock(t *testing.T, at time.Time) *time.Time {
	t.Helper()
	previous := ledgerClock
	now := at
	ledgerClock = func() time.Time { return now }
	t.Cleanup(func() { ledgerClock = previous })
	return &now
}

// derivedIntent builds an Operation whose key the gateway would have derived for
// these arguments (the caller supplied none).
func derivedIntent(t *testing.T, instance string, args json.RawMessage) Operation {
	t.Helper()
	hash, err := ArgsHash(args)
	if err != nil {
		t.Fatalf("ArgsHash: %v", err)
	}
	op := intent(t, instance, DeriveKey("agent-1", hash), args)
	return op
}

// INT-12: a derived key deduplicates inside its window and stops afterwards. The
// same arguments a window later are a new operation, not a permanent no-op — an
// ERP that re-sends an order, or a customer who buys the same item twice, must not
// be silently swallowed.
func TestDerivedKeyDeduplicatesOnlyInsideItsWindow(t *testing.T) {
	ledger, _, instance := ledgerFixture(t, "derived-window")
	ctx := context.Background()
	start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	clock := swapLedgerClock(t, start)

	args := json.RawMessage(`{"sku":"24-MB01","qty":1}`)
	op := derivedIntent(t, instance, args)

	id, exec := mustProceed(ctx, t, ledger, op)
	if applied, err := ledger.Succeed(ctx, id, exec.Fence, "order:1", json.RawMessage(`{"order_id":1}`)); err != nil || !applied {
		t.Fatalf("Succeed: applied=%v err=%v", applied, err)
	}

	// Same window: the double submit replays instead of writing twice.
	replay, err := ledger.Begin(ctx, derivedIntent(t, instance, args))
	if err != nil {
		t.Fatalf("Begin in window: %v", err)
	}
	if replay.Decision != DecisionReplay || !replay.Replayed || replay.OperationID != id {
		t.Fatalf("in-window begin = %+v, want a replay of %s", replay, id)
	}

	// Next window: a new operation. This is the defect INT-12 reported: the second
	// legitimate order used to come back as replayed:true and never reach Magento.
	*clock = start.Add(DerivedKeyWindow + time.Second)
	next, err := ledger.Begin(ctx, derivedIntent(t, instance, args))
	if err != nil {
		t.Fatalf("Begin next window: %v", err)
	}
	if next.Decision != DecisionProceed || next.OperationID == id {
		t.Fatalf("next-window begin = %+v, want a new operation", next)
	}
	page, err := ledger.List(ctx, OperationFilter{InstanceID: instance})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("operations = %d, want 2 (one per window)", page.Total)
	}
}

// A caller-supplied key keeps exactly-once semantics across windows: the window is
// a property of derived keys, not of keys in general.
func TestExplicitKeyIsNotWindowed(t *testing.T) {
	ledger, _, instance := ledgerFixture(t, "explicit-key")
	ctx := context.Background()
	start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	clock := swapLedgerClock(t, start)

	args := json.RawMessage(`{"amount":5}`)
	op := intent(t, instance, "caller-key-1", args)
	id, exec := mustProceed(ctx, t, ledger, op)
	if applied, err := ledger.Succeed(ctx, id, exec.Fence, "ref", nil); err != nil || !applied {
		t.Fatalf("Succeed: applied=%v err=%v", applied, err)
	}
	*clock = start.Add(24 * time.Hour)
	replay, err := ledger.Begin(ctx, intent(t, instance, "caller-key-1", args))
	if err != nil {
		t.Fatalf("Begin a day later: %v", err)
	}
	if replay.Decision != DecisionReplay || replay.OperationID != id {
		t.Fatalf("begin = %+v, want the same replay", replay)
	}
}

// INT-12 / FIN-09: a proven failure must not hold a derived key hostage. The
// recorded failure used to answer the caller's retry with "operation previously
// failed", so a local rejection or a 400 poisoned the key for good.
func TestFailedAttemptReleasesDerivedKey(t *testing.T) {
	ledger, _, instance := ledgerFixture(t, "derived-release")
	ctx := context.Background()
	swapLedgerClock(t, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))

	args := json.RawMessage(`{"order_id":9}`)
	id, exec := mustProceed(ctx, t, ledger, derivedIntent(t, instance, args))
	if applied, err := ledger.Fail(ctx, id, exec.Fence, "validation_failed"); err != nil || !applied {
		t.Fatalf("Fail: applied=%v err=%v", applied, err)
	}
	failed, err := ledger.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if failed.State != OpFailed || !strings.Contains(failed.IdempotencyKey, ":released:") {
		t.Fatalf("state=%s key=%q, want failed with a released key", failed.State, failed.IdempotencyKey)
	}

	// The same arguments in the same window now start a new attempt instead of
	// being refused with the recorded failure.
	retry, err := ledger.Begin(ctx, derivedIntent(t, instance, args))
	if err != nil {
		t.Fatalf("Begin after failure: %v", err)
	}
	if retry.Decision != DecisionProceed || retry.OperationID == id {
		t.Fatalf("begin after failure = %+v, want a fresh attempt", retry)
	}
	// The failed row is still evidence: it was renamed, not deleted.
	if _, err := ledger.Get(ctx, id); err != nil {
		t.Fatalf("failed row disappeared: %v", err)
	}
}

// An explicit key keeps its failure: the caller asked for exactly-once, and a
// silent second attempt is exactly what the recorded failure is there to prevent.
func TestExplicitKeyIsNotReleasedOnFailure(t *testing.T) {
	ledger, _, instance := ledgerFixture(t, "explicit-release")
	ctx := context.Background()
	args := json.RawMessage(`{"order_id":9}`)
	id, exec := mustProceed(ctx, t, ledger, intent(t, instance, "caller-key-2", args))
	if applied, err := ledger.Fail(ctx, id, exec.Fence, "validation_failed"); err != nil || !applied {
		t.Fatalf("Fail: applied=%v err=%v", applied, err)
	}
	rec, err := ledger.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if strings.Contains(rec.IdempotencyKey, ":released:") {
		t.Fatalf("explicit key %q was released; exactly-once was the caller's promise", rec.IdempotencyKey)
	}
	again, err := ledger.Begin(ctx, intent(t, instance, "caller-key-2", args))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if again.Decision != DecisionRetry {
		t.Fatalf("decision = %s, want retry (the recorded failure)", again.Decision)
	}
}

// INT-02: a replay must be visible in the recorded result itself, not only in the
// text fallback, so an agent reading structuredContent can tell a replay from a
// second write.
func TestReplayResultCarriesTheReplayedMarker(t *testing.T) {
	ledger, _, instance := ledgerFixture(t, "replay-marker")
	ctx := context.Background()
	args := json.RawMessage(`{"skus":["24-MB01"],"status":1}`)
	id, exec := mustProceed(ctx, t, ledger, intent(t, instance, "key-marker", args))
	// The tool's own result carries no replay marker: the ledger adds it.
	if applied, err := ledger.Succeed(ctx, id, exec.Fence, "ref",
		json.RawMessage(`{"total":1,"updated":1,"failed":0}`)); err != nil || !applied {
		t.Fatalf("Succeed: applied=%v err=%v", applied, err)
	}
	replay, err := ledger.Begin(ctx, intent(t, instance, "key-marker", args))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if replay.Decision != DecisionReplay {
		t.Fatalf("decision = %s, want replay", replay.Decision)
	}
	var recorded map[string]any
	if err := json.Unmarshal(replay.Result, &recorded); err != nil {
		t.Fatalf("result %s: %v", replay.Result, err)
	}
	if recorded["replayed"] != true {
		t.Fatalf("recorded result = %s, want replayed:true in the payload", replay.Result)
	}
	if recorded["updated"] != float64(1) {
		t.Fatalf("recorded result = %s, the original payload must survive", replay.Result)
	}
	// Polling reports the operation, not a replay: the marker belongs to the
	// replayed response, and a polled row says "succeeded" instead.
	rec, err := ledger.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != OpSucceeded {
		t.Fatalf("state = %s, want succeeded", rec.State)
	}
	var polled map[string]any
	if err := json.Unmarshal(rec.Result, &polled); err != nil {
		t.Fatalf("recorded result %s: %v", rec.Result, err)
	}
	if _, ok := polled["replayed"]; ok {
		t.Fatalf("polled result = %s, the replay marker must not be written into the record", rec.Result)
	}
}

// A recorded result that is not a JSON object has nowhere to put the marker; the
// ledger must not mangle it.
func TestReplayMarkerLeavesNonObjectResultsAlone(t *testing.T) {
	for _, tc := range []struct{ name, result string }{
		{name: "null", result: `null`},
		{name: "array", result: `[1,2]`},
		{name: "scalar", result: `"42"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := markReplayed(json.RawMessage(tc.result))
			if string(got) != tc.result {
				t.Fatalf("markReplayed(%s) = %s, want it untouched", tc.result, got)
			}
		})
	}
}

// escalateUnknown drives one operation to the state the reconciler leaves it in
// after it gives up: unknown, escalated, never reconcilable again.
func escalateUnknown(ctx context.Context, t *testing.T, ledger *OperationLedger, store *Store, instance, key string) string {
	t.Helper()
	id, exec := mustProceed(ctx, t, ledger, intent(t, instance, key, json.RawMessage(`{"amount":3}`)))
	if applied, err := ledger.Unknown(ctx, id, exec.Fence, "upstream_timeout", time.Minute); err != nil || !applied {
		t.Fatalf("Unknown: applied=%v err=%v", applied, err)
	}
	makeDue(t, store, id)
	rec, applied, err := ledger.ClaimReconcilable(ctx, time.Minute)
	if err != nil || !applied || rec == nil || rec.OperationID != id {
		t.Fatalf("ClaimReconcilable: rec=%v applied=%v err=%v", rec, applied, err)
	}
	if applied, err := ledger.Escalate(ctx, id, rec.Fence, "attempts exhausted"); err != nil || !applied {
		t.Fatalf("Escalate: applied=%v err=%v", applied, err)
	}
	return id
}

// INT-07: the rows the reconciler has given up on must be findable, and the query
// must say honestly how many there are.
func TestListFindsOperationsThatNeedAttention(t *testing.T) {
	ledger, store, instance := ledgerFixture(t, "attention")
	ctx := context.Background()

	escalated := escalateUnknown(ctx, t, ledger, store, instance, "key-escalated")

	// A plain unknown row: the reconciler still owns it, so it is not attention.
	unknown, exec := mustProceed(ctx, t, ledger, intent(t, instance, "key-unknown", json.RawMessage(`{"amount":4}`)))
	if applied, err := ledger.Unknown(ctx, unknown, exec.Fence, "upstream_timeout", time.Hour); err != nil || !applied {
		t.Fatalf("Unknown: applied=%v err=%v", applied, err)
	}
	// A succeeded row: nothing to do.
	done, doneExec := mustProceed(ctx, t, ledger, intent(t, instance, "key-done", json.RawMessage(`{"amount":5}`)))
	if applied, err := ledger.Succeed(ctx, done, doneExec.Fence, "ref", nil); err != nil || !applied {
		t.Fatalf("Succeed: applied=%v err=%v", applied, err)
	}

	attention, err := ledger.List(ctx, OperationFilter{InstanceID: instance, NeedsAttention: true})
	if err != nil {
		t.Fatalf("List needs_attention: %v", err)
	}
	if len(attention.Operations) != 1 || attention.Operations[0].ID != escalated {
		t.Fatalf("needs_attention = %+v, want only %s", attention.Operations, escalated)
	}
	rec := attention.Operations[0]
	if !rec.NeedsAttention || rec.ErrorCode != EscalatedErrorCode {
		t.Fatalf("record = %+v, want the escalated row", rec)
	}
	if rec.ReconcileAfter != nil {
		t.Fatalf("reconcile_after = %v, want nil for an escalated row: 'never again' must not read as a date", rec.ReconcileAfter)
	}

	unknowns, err := ledger.List(ctx, OperationFilter{InstanceID: instance, State: OpUnknown})
	if err != nil {
		t.Fatalf("List unknown: %v", err)
	}
	if unknowns.Total != 2 || len(unknowns.Operations) != 2 {
		t.Fatalf("unknown rows = %d/%d, want 2", unknowns.Total, len(unknowns.Operations))
	}
	for _, row := range unknowns.Operations {
		if row.ID == escalated && !row.NeedsAttention {
			t.Fatal("the escalated row lost its needs_attention flag when listed by state")
		}
	}

	// Pagination is honest: the total is the match count, not the page size.
	first, err := ledger.List(ctx, OperationFilter{InstanceID: instance, Limit: 1})
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}
	if first.Total != 3 || len(first.Operations) != 1 || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("page 1 = total %d rows %d truncated %v cursor %q, want 3/1/true/non-empty",
			first.Total, len(first.Operations), first.Truncated, first.NextCursor)
	}
	second, err := ledger.List(ctx, OperationFilter{InstanceID: instance, Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if len(second.Operations) != 2 || second.Truncated {
		t.Fatalf("page 2 = %d rows truncated=%v, want the remaining 2 and no truncation",
			len(second.Operations), second.Truncated)
	}
	seen := map[string]bool{first.Operations[0].ID: true}
	for _, row := range second.Operations {
		if seen[row.ID] {
			t.Fatalf("row %s appeared on both pages", row.ID)
		}
		seen[row.ID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("pages covered %d rows, want 3", len(seen))
	}

	if _, err := ledger.List(ctx, OperationFilter{Cursor: "not-a-cursor"}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("bad cursor error = %v, want invalid_argument", err)
	}
}

// INT-07: an escalated row has an exit, and the exit records what a human found
// rather than pretending the mutation never happened.
func TestDisposeRecordsTheJudgement(t *testing.T) {
	t.Run("applied", func(t *testing.T) {
		ledger, store, instance := ledgerFixture(t, "dispose-applied")
		ctx := context.Background()
		id := escalateUnknown(ctx, t, ledger, store, instance, "key-applied")
		event := &audit.Event{Subject: "operator@test", IdentityClass: "human",
			Tool: "magento.operation.dispose", Scope: "dispose", Phase: "outcome",
			Decision: "allow", State: "succeeded"}
		applied, err := ledger.Dispose(ctx, id, DisposedApplied, "read /V1/orders/1042: the order exists", "order:1042", event)
		if err != nil || !applied {
			t.Fatalf("Dispose: applied=%v err=%v", applied, err)
		}
		rec, err := ledger.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if rec.State != OpSucceeded || rec.ErrorCode != disposedAppliedCode {
			t.Fatalf("state=%s code=%s, want succeeded/%s", rec.State, rec.ErrorCode, disposedAppliedCode)
		}
		if rec.UpstreamRef != "order:1042" || rec.FinishedAt == nil || rec.NeedsAttention {
			t.Fatalf("record = %+v, want the upstream ref, a finish time and no attention flag", rec)
		}
		history, err := ledger.Transitions(ctx, id)
		if err != nil {
			t.Fatalf("Transitions: %v", err)
		}
		last := history[len(history)-1]
		if last.ToState != OpSucceeded || !strings.Contains(last.Reason, "read /V1/orders/1042") {
			t.Fatalf("last transition = %+v, want the operator's note in the reason", last)
		}
	})

	t.Run("not_applied", func(t *testing.T) {
		ledger, store, instance := ledgerFixture(t, "dispose-not-applied")
		ctx := context.Background()
		id := escalateUnknown(ctx, t, ledger, store, instance, "key-not-applied")
		applied, err := ledger.Dispose(ctx, id, DisposedNotApplied, "read /V1/orders/1043: no such order", "", nil)
		if err != nil || !applied {
			t.Fatalf("Dispose: applied=%v err=%v", applied, err)
		}
		rec, err := ledger.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if rec.State != OpFailed || rec.ErrorCode != disposedNotAppliedCode {
			t.Fatalf("state=%s code=%s, want failed/%s", rec.State, rec.ErrorCode, disposedNotAppliedCode)
		}
	})

	t.Run("requeue", func(t *testing.T) {
		ledger, store, instance := ledgerFixture(t, "dispose-requeue")
		ctx := context.Background()
		id := escalateUnknown(ctx, t, ledger, store, instance, "key-requeue")
		before, err := ledger.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if before.ReconcileCount == 0 {
			t.Fatal("fixture must have consumed reconciliation attempts")
		}
		applied, err := ledger.Dispose(ctx, id, DisposedRequeue, "upstream read timed out; try again", "", nil)
		if err != nil || !applied {
			t.Fatalf("Dispose: applied=%v err=%v", applied, err)
		}
		rec, err := ledger.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if rec.State != OpUnknown || rec.NeedsAttention || rec.ReconcileCount != 0 || rec.ErrorCode != "" {
			t.Fatalf("record = %+v, want unknown, no attention flag, a reset budget and no error code", rec)
		}
		// The reconciler can claim it again instead of the row sitting at infinity.
		claimed, applied, err := ledger.ClaimReconcilable(ctx, time.Minute)
		if err != nil || !applied || claimed == nil || claimed.OperationID != id {
			t.Fatalf("ClaimReconcilable after requeue: rec=%v applied=%v err=%v", claimed, applied, err)
		}
	})
}

// A disposition is only meaningful for an unknown outcome: anything else either
// belongs to a live attempt or is already decided.
func TestDisposeRefusesNonUnknownStates(t *testing.T) {
	ledger, _, instance := ledgerFixture(t, "dispose-refuse")
	ctx := context.Background()

	running, exec := mustProceed(ctx, t, ledger, intent(t, instance, "key-running", json.RawMessage(`{"amount":1}`)))
	_ = exec
	if _, err := ledger.Dispose(ctx, running, DisposedApplied, "checked", "", nil); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("dispose executing: err = %v, want conflict", err)
	}

	done, doneExec := mustProceed(ctx, t, ledger, intent(t, instance, "key-done-2", json.RawMessage(`{"amount":2}`)))
	if applied, err := ledger.Succeed(ctx, done, doneExec.Fence, "ref", nil); err != nil || !applied {
		t.Fatalf("Succeed: applied=%v err=%v", applied, err)
	}
	if _, err := ledger.Dispose(ctx, done, DisposedNotApplied, "checked", "", nil); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("dispose succeeded: err = %v, want conflict", err)
	}
}

// The judgement and the note are the point: without them a disposition is the
// "assume it never happened" shortcut this API exists to prevent.
func TestDisposeRequiresJudgementAndNote(t *testing.T) {
	ledger, store, instance := ledgerFixture(t, "dispose-validation")
	ctx := context.Background()
	id := escalateUnknown(ctx, t, ledger, store, instance, "key-validation")

	if _, err := ledger.Dispose(ctx, id, "whatever", "note", "", nil); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("bad judgement: err = %v, want invalid_argument", err)
	}
	if _, err := ledger.Dispose(ctx, id, DisposedApplied, "   ", "", nil); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty note: err = %v, want invalid_argument", err)
	}
	if _, err := ledger.Dispose(ctx, "not-a-uuid", DisposedApplied, "note", "", nil); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("bad id: err = %v, want invalid_argument", err)
	}
	// None of the rejected calls may have moved the row.
	rec, err := ledger.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != OpUnknown || !rec.NeedsAttention {
		t.Fatalf("record = %+v, want it untouched and still needing attention", rec)
	}
}
