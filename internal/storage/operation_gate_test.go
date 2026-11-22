// SPDX-License-Identifier: Apache-2.0

package storage

// Tests for the write-call gate: confirmation consumption and the ledger row are
// one transaction, and the audit outcome is committed with the transition.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/security/audit"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
)

// gateFixture returns a ledger, a confirmation store, and a clean slate.
func gateFixture(t *testing.T, name string) (*OperationLedger, *ConfirmStore, *Store, string) {
	t.Helper()
	store := testStore(t)
	ledger := NewOperationLedger(store)
	cs := NewConfirmStore(store)
	instance := "store-gate-" + name
	clean := func() {
		ctx := context.Background()
		// One atomic statement for the related tables: deleting transitions and
		// operations separately leaves a window where a concurrent writer can
		// insert a transition for a row the second DELETE removes, which fails on
		// operation_transitions_operation_id_fkey. CASCADE also clears anything
		// that references them.
		if _, err := store.pool.Exec(ctx,
			`TRUNCATE operation_transitions, operations, confirmation_tokens CASCADE`); err != nil {
			t.Fatalf("cleanup ledger tables: %v", err)
		}
		if _, err := store.pool.Exec(ctx, `DELETE FROM audit_events WHERE tool LIKE 'magento.gate.%'`); err != nil {
			t.Fatalf("cleanup audit: %v", err)
		}
	}
	clean()
	t.Cleanup(clean)
	return ledger, cs, store, instance
}

// approvedConfirmation seeds an approved request and returns its id.
func approvedConfirmation(t *testing.T, cs *ConfirmStore, tool, instance, requester, args string) string {
	t.Helper()
	svc, err := confirm.NewService(cs, confirm.DefaultTTL)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	req, err := svc.Require(context.Background(), tool, instance, "write", confirm.Summarize([]byte(args)), requester, []byte(args))
	if err != nil {
		t.Fatalf("Require: %v", err)
	}
	if err := svc.Approve(context.Background(), req.ID, "human-1"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	return req.ID
}

func gateRequest(t *testing.T, instance, tool, confirmationID, args string) GateRequest {
	return gateRequestWithKey(t, instance, tool, confirmationID, args, "auto:test")
}

func gateRequestWithKey(t *testing.T, instance, tool, confirmationID, args, key string) GateRequest {
	t.Helper()
	hash, err := confirm.CanonicalHash([]byte(args))
	if err != nil {
		t.Fatalf("CanonicalHash: %v", err)
	}
	return GateRequest{
		Operation: tool, InstanceID: instance, Subject: "agent-1", ArgsHash: hash,
		IdempotencyKey: key, ConfirmationID: confirmationID,
		EntityType: EntityInvoice, EntityID: "100000123",
	}
}

func confirmationState(t *testing.T, cs *ConfirmStore, id string) confirm.State {
	t.Helper()
	req, err := cs.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get confirmation: %v", err)
	}
	return req.State
}

// The approval is burned and the operation created in one transaction.
func TestBeginCallConsumesConfirmationAtomically(t *testing.T) {
	ledger, cs, store, instance := gateFixture(t, "atomic")
	ctx := context.Background()
	args := `{"instance":"store-us","invoice_id":100000123}`
	confirmationID := approvedConfirmation(t, cs, "magento.gate.capture", instance, "agent-1", args)

	ticket, err := ledger.BeginCall(ctx, gateRequest(t, instance, "magento.gate.capture", confirmationID, args))
	if err != nil {
		t.Fatalf("BeginCall: %v", err)
	}
	if ticket.OperationID == "" || ticket.Decision != GateProceed {
		t.Fatalf("ticket = %+v, want a proceed ticket", ticket)
	}
	if got := confirmationState(t, cs, confirmationID); got != confirm.StateConsumed {
		t.Fatalf("confirmation state = %s, want consumed", got)
	}
	var operations int
	if err := store.pool.QueryRow(ctx,
		`SELECT count(*) FROM operations WHERE instance_id = $1`, instance).Scan(&operations); err != nil {
		t.Fatalf("count operations: %v", err)
	}
	if operations != 1 {
		t.Fatalf("%d operations, want exactly 1", operations)
	}
}

// If the ledger write fails, the approval must NOT be burned: either both or
// neither. A NUL byte in entity_id makes the insert fail after the consume has
// already run inside the transaction.
func TestBeginCallRollsBackConfirmationOnLedgerFailure(t *testing.T) {
	ledger, cs, store, instance := gateFixture(t, "rollback")
	ctx := context.Background()
	args := `{"instance":"store-us","invoice_id":100000123}`
	confirmationID := approvedConfirmation(t, cs, "magento.gate.capture", instance, "agent-1", args)

	req := gateRequest(t, instance, "magento.gate.capture", confirmationID, args)
	req.EntityID = "bad\x00entity" // PostgreSQL rejects NUL in text

	if _, err := ledger.BeginCall(ctx, req); err == nil {
		t.Fatal("BeginCall with an unstorable entity id succeeded")
	}
	if got := confirmationState(t, cs, confirmationID); got != confirm.StateApproved {
		t.Fatalf("confirmation state = %s, want approved: a failed ledger write burned the approval", got)
	}
	var operations int
	if err := store.pool.QueryRow(ctx,
		`SELECT count(*) FROM operations WHERE instance_id = $1`, instance).Scan(&operations); err != nil {
		t.Fatalf("count operations: %v", err)
	}
	if operations != 0 {
		t.Fatalf("%d operations after a failed BeginCall, want 0", operations)
	}
}

// The audit outcome is committed with the transition.
func TestFinishCallWritesAuditInTheSameTransaction(t *testing.T) {
	ledger, cs, store, instance := gateFixture(t, "audit")
	ctx := context.Background()
	args := `{"instance":"store-us","invoice_id":100000123}`
	confirmationID := approvedConfirmation(t, cs, "magento.gate.capture", instance, "agent-1", args)
	ticket, err := ledger.BeginCall(ctx, gateRequest(t, instance, "magento.gate.capture", confirmationID, args))
	if err != nil {
		t.Fatalf("BeginCall: %v", err)
	}

	event := audit.Event{
		Subject: "agent-1", IdentityClass: "human", Tool: "magento.gate.capture",
		Instance: instance, Decision: "allow", Phase: "outcome", State: "succeeded",
	}
	if err := ledger.FinishCall(ctx, ticket, GateOutcome{
		State: OpSucceeded, UpstreamRef: "inv-1", Result: []byte(`{"ok":true}`), Audit: &event,
	}); err != nil {
		t.Fatalf("FinishCall: %v", err)
	}
	rec, err := ledger.Get(ctx, ticket.OperationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != OpSucceeded || rec.FinishedAt == nil {
		t.Fatalf("operation = %s finished=%v, want succeeded", rec.State, rec.FinishedAt)
	}
	var auditRows int
	if err := store.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_events WHERE tool = 'magento.gate.capture' AND state = 'succeeded'`).Scan(&auditRows); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if auditRows != 1 {
		t.Fatalf("%d audit rows, want 1", auditRows)
	}
}

// If the audit write fails, the transition must roll back with it, so an outcome
// can never be recorded without its audit row.
func TestFinishCallRollsBackTransitionWhenAuditFails(t *testing.T) {
	ledger, cs, _, instance := gateFixture(t, "auditfail")
	ctx := context.Background()
	args := `{"instance":"store-us","invoice_id":100000123}`
	confirmationID := approvedConfirmation(t, cs, "magento.gate.capture", instance, "agent-1", args)
	ticket, err := ledger.BeginCall(ctx, gateRequest(t, instance, "magento.gate.capture", confirmationID, args))
	if err != nil {
		t.Fatalf("BeginCall: %v", err)
	}

	// An event with no decision is rejected by RecordInTx.
	event := audit.Event{Subject: "agent-1", Tool: "magento.gate.capture", Phase: "outcome"}
	if err := ledger.FinishCall(ctx, ticket, GateOutcome{State: OpSucceeded, Audit: &event}); err == nil {
		t.Fatal("FinishCall with an invalid audit event succeeded")
	}
	rec, err := ledger.Get(ctx, ticket.OperationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// The claim is committed by BeginCall, so the row stays executing; what must
	// roll back is the terminal transition: no finished_at, no succeeded.
	if rec.State != OpExecuting || rec.FinishedAt != nil {
		t.Fatalf("operation = %s finished=%v, want executing with no terminal transition "+
			"(the transition must roll back with the audit)", rec.State, rec.FinishedAt)
	}
}

// A confirmation id that does not match the call is refused before any ledger row
// is created.
func TestBeginCallRefusesMismatchedConfirmation(t *testing.T) {
	ledger, cs, store, instance := gateFixture(t, "mismatch")
	ctx := context.Background()
	args := `{"instance":"store-us","invoice_id":100000123}`
	confirmationID := approvedConfirmation(t, cs, "magento.gate.capture", instance, "agent-1", args)

	req := gateRequest(t, instance, "magento.gate.capture", confirmationID, args)
	req.Operation = "magento.gate.refund" // different tool
	if _, err := ledger.BeginCall(ctx, req); errs.CodeOf(err) != errs.CodeConfirmationMismatch {
		t.Fatalf("mismatched confirmation: code=%s err=%v, want confirmation_param_mismatch", errs.CodeOf(err), err)
	}
	if got := confirmationState(t, cs, confirmationID); got != confirm.StateApproved {
		t.Fatalf("confirmation state = %s, want approved", got)
	}
	var operations int
	if err := store.pool.QueryRow(ctx,
		`SELECT count(*) FROM operations WHERE instance_id = $1`, instance).Scan(&operations); err != nil {
		t.Fatalf("count operations: %v", err)
	}
	if operations != 0 {
		t.Fatalf("%d operations after a refused confirmation, want 0", operations)
	}
}

// The transition and audit rows are written even when the outcome is unknown, so
// an ambiguous operation is never lost.
func TestFinishCallRecordsUnknownWithAudit(t *testing.T) {
	ledger, cs, store, instance := gateFixture(t, "unknown")
	ctx := context.Background()
	args := `{"instance":"store-us","invoice_id":100000123}`
	confirmationID := approvedConfirmation(t, cs, "magento.gate.capture", instance, "agent-1", args)
	ticket, err := ledger.BeginCall(ctx, gateRequest(t, instance, "magento.gate.capture", confirmationID, args))
	if err != nil {
		t.Fatalf("BeginCall: %v", err)
	}
	event := audit.Event{
		Subject: "agent-1", Tool: "magento.gate.capture", Instance: instance,
		Decision: "error", Phase: "outcome", State: "outcome_unknown",
	}
	if err := ledger.FinishCall(ctx, ticket, GateOutcome{
		State: OpUnknown, ErrorCode: "upstream_timeout", RetryAfter: time.Minute, Audit: &event,
	}); err != nil {
		t.Fatalf("FinishCall: %v", err)
	}
	rec, err := ledger.Get(ctx, ticket.OperationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != OpUnknown || rec.ErrorCode != "upstream_timeout" {
		t.Fatalf("operation = %s/%s, want unknown/upstream_timeout", rec.State, rec.ErrorCode)
	}
	var decision string
	if err := store.pool.QueryRow(ctx,
		`SELECT decision FROM audit_events WHERE tool = 'magento.gate.capture' AND state = 'outcome_unknown'`).
		Scan(&decision); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("no audit row for the unknown outcome")
		}
		t.Fatalf("read audit: %v", err)
	}
	if decision != "error" {
		t.Fatalf("audit decision = %q, want error", decision)
	}
}

// seedState drives a fresh operation into the given state and returns its id.
func seedState(t *testing.T, ledger *OperationLedger, instance, key string, want string, errorCode string) string {
	t.Helper()
	ctx := context.Background()
	args := `{"instance":"store-us","invoice_id":100000123}`
	hash, err := confirm.CanonicalHash([]byte(args))
	if err != nil {
		t.Fatalf("CanonicalHash: %v", err)
	}
	res, err := ledger.Begin(ctx, Operation{
		IdempotencyKey: key, Operation: "magento.gate.capture", InstanceID: instance,
		Subject: "agent-1", EntityType: EntityInvoice, EntityID: "100000123", ArgsHash: hash,
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if want == OpPending {
		return res.OperationID
	}
	exec, applied, err := ledger.Execute(ctx, res.OperationID, time.Minute)
	if err != nil || !applied {
		t.Fatalf("Execute: applied=%v err=%v", applied, err)
	}
	switch want {
	case OpExecuting:
	case OpSucceeded:
		if ok, err := ledger.Succeed(ctx, res.OperationID, exec.Fence, "inv-1", nil); err != nil || !ok {
			t.Fatalf("Succeed: ok=%v err=%v", ok, err)
		}
	case OpFailed:
		if ok, err := ledger.Fail(ctx, res.OperationID, exec.Fence, errorCode); err != nil || !ok {
			t.Fatalf("Fail: ok=%v err=%v", ok, err)
		}
	case OpUnknown:
		if ok, err := ledger.Unknown(ctx, res.OperationID, exec.Fence, errorCode, 0); err != nil || !ok {
			t.Fatalf("Unknown: ok=%v err=%v", ok, err)
		}
		if ok, err := ledger.MakeReconcilable(ctx, res.OperationID); err != nil || !ok {
			t.Fatalf("MakeReconcilable: ok=%v err=%v", ok, err)
		}
	default:
		t.Fatalf("unsupported seed state %q", want)
	}
	return res.OperationID
}

// REL38-02: a decision that will not execute must not burn the approval.
func TestBeginCallDoesNotConsumeForNonProceedDecisions(t *testing.T) {
	ledger, cs, _, instance := gateFixture(t, "noconsume")
	ctx := context.Background()
	args := `{"instance":"store-us","invoice_id":100000123}`

	cases := []struct {
		name       string
		seed       string
		errorCode  string
		wantDecide GateDecision
	}{
		{"in progress", OpExecuting, "", GateInProgress},
		{"unknown", OpUnknown, "upstream_timeout", GateUnknown},
		{"replay", OpSucceeded, "", GateReplay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := "seed-" + tc.name
			seedState(t, ledger, instance, key, tc.seed, tc.errorCode)
			confirmationID := approvedConfirmation(t, cs, "magento.gate.capture", instance, "agent-1", args)
			req := gateRequestWithKey(t, instance, "magento.gate.capture", confirmationID, args, key)
			ticket, err := ledger.BeginCall(ctx, req)
			if ticket.Decision != tc.wantDecide {
				t.Fatalf("decision = %s, want %s (err=%v)", ticket.Decision, tc.wantDecide, err)
			}
			if tc.wantDecide != GateReplay && err == nil {
				t.Fatal("a non-executing decision returned a nil error")
			}
			if got := confirmationState(t, cs, confirmationID); got != confirm.StateApproved {
				t.Fatalf("confirmation state = %s, want approved (a non-executing decision burned it)", got)
			}
		})
	}
}

// A retry decision re-opens and claims the row in the same transaction, so the
// caller gets a fenced Proceed and the approval is consumed only because the
// operation will actually run.
func TestBeginCallRetryClaimsTheOperation(t *testing.T) {
	ledger, cs, _, instance := gateFixture(t, "retry")
	ctx := context.Background()
	args := `{"instance":"store-us","invoice_id":100000123}`
	id := seedState(t, ledger, instance, "seed-retry", OpFailed, "upstream_rejected")

	confirmationID := approvedConfirmation(t, cs, "magento.gate.capture", instance, "agent-1", args)
	ticket, err := ledger.BeginCall(ctx, gateRequestWithKey(t, instance, "magento.gate.capture", confirmationID, args, "seed-retry"))
	if err != nil {
		t.Fatalf("BeginCall: %v", err)
	}
	if ticket.Decision != GateProceed || ticket.OperationID != id || ticket.Fence == "" {
		t.Fatalf("ticket = %+v, want a fenced proceed on %s", ticket, id)
	}
	rec, err := ledger.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != OpExecuting || rec.Attempt != 2 {
		t.Fatalf("operation = %s attempt=%d, want executing/2", rec.State, rec.Attempt)
	}
	if got := confirmationState(t, cs, confirmationID); got != confirm.StateConsumed {
		t.Fatalf("confirmation state = %s, want consumed (the operation will execute)", got)
	}
}

// REL38-04: Retry refuses every ambiguous failure, using the same predicate as
// the rest of the ledger.
func TestRetryRefusesAmbiguousCodes(t *testing.T) {
	ledger, _, store, instance := gateFixture(t, "retrycodes")
	ctx := context.Background()
	for _, code := range []string{"unknown", "ambiguous", "upstream_timeout"} {
		id := seedState(t, ledger, instance, "seed-"+code, OpFailed, "upstream_rejected")
		if _, err := store.pool.Exec(ctx,
			`UPDATE operations SET error_code = $2 WHERE id = $1::uuid`, id, code); err != nil {
			t.Fatalf("seed ambiguous code: %v", err)
		}
		applied, err := ledger.Retry(ctx, id)
		if applied {
			t.Fatalf("Retry re-opened an operation failed with %q", code)
		}
		if errs.CodeOf(err) != errs.CodeConflict {
			t.Fatalf("Retry(%q) code = %s, want conflict", code, errs.CodeOf(err))
		}
	}
	// A definite failure is still retryable.
	id := seedState(t, ledger, instance, "seed-definite", OpFailed, "upstream_rejected")
	if applied, err := ledger.Retry(ctx, id); err != nil || !applied {
		t.Fatalf("Retry on a definite failure: applied=%v err=%v", applied, err)
	}
}

// REL38-03: every crash window is recovered, and recovery never retries an
// ambiguous outcome.
func TestRecoverExpiredResolvesEveryCrashWindow(t *testing.T) {
	ledger, _, store, instance := gateFixture(t, "recover")
	ctx := context.Background()

	executing := seedState(t, ledger, instance, "crash-executing", OpExecuting, "")
	unknownID := seedState(t, ledger, instance, "crash-reconciling", OpUnknown, "upstream_timeout")
	// Claim the unknown one so it sits in reconciling with a live lease...
	if _, applied, err := ledger.ClaimReconcilable(ctx, time.Minute); err != nil || !applied {
		t.Fatalf("ClaimReconcilable: applied=%v err=%v", applied, err)
	}
	stalePending := seedState(t, ledger, instance, "crash-pending", OpPending, "")

	// ...then expire every lease and age the pending row.
	if _, err := store.pool.Exec(ctx, `
		UPDATE operations SET lease_expires_at = now() - interval '1 minute'
		WHERE id IN ($1::uuid, $2::uuid)`, executing, unknownID); err != nil {
		t.Fatalf("expire leases: %v", err)
	}
	if _, err := store.pool.Exec(ctx, `
		UPDATE operations SET created_at = now() - interval '1 hour' WHERE id = $1::uuid`, stalePending); err != nil {
		t.Fatalf("age pending: %v", err)
	}

	recovered, err := ledger.RecoverExpired(ctx, 0, 0)
	if err != nil {
		t.Fatalf("RecoverExpired: %v", err)
	}
	if recovered != 3 {
		t.Fatalf("recovered %d rows, want 3", recovered)
	}
	for _, tc := range []struct{ id, wantState, wantCode string }{
		{executing, OpUnknown, "lease_expired"},
		{unknownID, OpUnknown, "reconcile_lease_expired"},
		{stalePending, OpFailed, "abandoned_pending"},
	} {
		rec, err := ledger.Get(ctx, tc.id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if rec.State != tc.wantState || rec.ErrorCode != tc.wantCode {
			t.Fatalf("recovered %s = %s/%s, want %s/%s", tc.id, rec.State, rec.ErrorCode, tc.wantState, tc.wantCode)
		}
	}
	// The ambiguous row is unknown and due for reconciliation, never re-executed.
	rec, err := ledger.Get(ctx, executing)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != OpUnknown {
		t.Fatalf("an expired executing row was re-queued instead of marked unknown: %s", rec.State)
	}
}

// Abort is available from reconciling once a human has proven not-applied.
func TestAbortFromReconciling(t *testing.T) {
	ledger, _, _, instance := gateFixture(t, "abortrecon")
	ctx := context.Background()
	seedState(t, ledger, instance, "seed-abortrecon", OpUnknown, "upstream_timeout")
	rec, applied, err := ledger.ClaimReconcilable(ctx, time.Minute)
	if err != nil || !applied {
		t.Fatalf("ClaimReconcilable: applied=%v err=%v", applied, err)
	}
	if applied, err := ledger.Abort(ctx, rec.OperationID, OpReconciling, "operator proved no charge"); err != nil || !applied {
		t.Fatalf("Abort from reconciling: applied=%v err=%v", applied, err)
	}
	got, err := ledger.Get(ctx, rec.OperationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != OpFailed || got.ErrorCode != "admin_abort" {
		t.Fatalf("aborted operation = %s/%s, want failed/admin_abort", got.State, got.ErrorCode)
	}
}

// REL38-07: a resolution writes its audit outcome in the same transaction.
func TestResolveWritesAuditInTheSameTransaction(t *testing.T) {
	ledger, _, store, instance := gateFixture(t, "resolveaudit")
	ctx := context.Background()
	seedState(t, ledger, instance, "seed-resolveaudit", OpUnknown, "upstream_timeout")
	rec, applied, err := ledger.ClaimReconcilable(ctx, time.Minute)
	if err != nil || !applied {
		t.Fatalf("ClaimReconcilable: applied=%v err=%v", applied, err)
	}
	event := audit.Event{
		Subject: "reconciler", IdentityClass: "service", Tool: rec.Operation,
		Instance: instance, Scope: "reconcile", Phase: "outcome",
		Decision: "allow", State: "succeeded",
	}
	if ok, err := ledger.Resolve(ctx, rec.OperationID, rec.Fence, ResolvedSucceeded, "inv-9", "", nil, &event); err != nil || !ok {
		t.Fatalf("Resolve: ok=%v err=%v", ok, err)
	}
	var rows int
	if err := store.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		 WHERE tool = 'magento.gate.capture' AND phase = 'outcome' AND state = 'succeeded'`).Scan(&rows); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if rows != 1 {
		t.Fatalf("%d reconciliation audit rows, want 1", rows)
	}
	// A failing audit must roll the transition back.
	seedState(t, ledger, instance, "seed-resolveaudit2", OpUnknown, "upstream_timeout")
	rec2, applied, err := ledger.ClaimReconcilable(ctx, time.Minute)
	if err != nil || !applied {
		t.Fatalf("ClaimReconcilable: applied=%v err=%v", applied, err)
	}
	bad := audit.Event{Subject: "reconciler", Tool: rec2.Operation, Phase: "outcome"}
	if ok, err := ledger.Resolve(ctx, rec2.OperationID, rec2.Fence, ResolvedSucceeded, "", "", nil, &bad); err == nil || ok {
		t.Fatal("Resolve accepted an invalid audit event")
	}
	got, err := ledger.Get(ctx, rec2.OperationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != OpReconciling {
		t.Fatalf("state = %s, want reconciling (the transition must roll back with the audit)", got.State)
	}
}

// REL38-09: escalation is terminal — a second pass must not re-escalate.
func TestEscalateIsTerminal(t *testing.T) {
	ledger, _, _, instance := gateFixture(t, "escalate")
	ctx := context.Background()
	seedState(t, ledger, instance, "seed-escalate", OpUnknown, "upstream_timeout")
	rec, applied, err := ledger.ClaimReconcilable(ctx, time.Minute)
	if err != nil || !applied {
		t.Fatalf("ClaimReconcilable: applied=%v err=%v", applied, err)
	}
	if ok, err := ledger.Escalate(ctx, rec.OperationID, rec.Fence, "exhausted"); err != nil || !ok {
		t.Fatalf("Escalate: ok=%v err=%v", ok, err)
	}
	got, err := ledger.Get(ctx, rec.OperationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ErrorCode != "needs_manual_reconciliation" {
		t.Fatalf("error code = %q, want needs_manual_reconciliation", got.ErrorCode)
	}
	// A second pass cannot claim it, so it cannot be escalated twice.
	if _, applied, err := ledger.ClaimReconcilable(ctx, time.Minute); err != nil {
		t.Fatalf("ClaimReconcilable: %v", err)
	} else if applied {
		t.Fatal("an escalated operation was claimed again")
	}
	if ok, _ := ledger.Escalate(ctx, rec.OperationID, rec.Fence, "again"); ok {
		t.Fatal("Escalate applied a second time")
	}
}

// REL43-01: a retried operation reaches a terminal state. Before the fix the row
// stayed executing with finished_at NULL because FinishCall took the vestigial
// "tool-managed" branch for any row it did not find in pending.
func TestRetriedOperationReachesTerminalState(t *testing.T) {
	ledger, cs, _, instance := gateFixture(t, "retryfinish")
	ctx := context.Background()
	args := `{"instance":"store-us","invoice_id":100000123}`

	// retry -> success ends succeeded with finished_at, and the result replays.
	t.Run("success", func(t *testing.T) {
		key := "seed-retry-success"
		id := seedState(t, ledger, instance, key, OpFailed, "upstream_rejected")
		confirmationID := approvedConfirmation(t, cs, "magento.gate.capture", instance, "agent-1", args)
		ticket, err := ledger.BeginCall(ctx, gateRequestWithKey(t, instance, "magento.gate.capture", confirmationID, args, key))
		if err != nil {
			t.Fatalf("BeginCall: %v", err)
		}
		if ticket.Decision != GateProceed || ticket.Fence == "" {
			t.Fatalf("ticket = %+v, want a fenced proceed", ticket)
		}
		result := []byte(`{"invoice_id":555,"state":"paid"}`)
		if err := ledger.FinishCall(ctx, ticket, GateOutcome{
			State: OpSucceeded, UpstreamRef: "inv-555", Result: result,
		}); err != nil {
			t.Fatalf("FinishCall: %v", err)
		}
		rec, err := ledger.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if rec.State != OpSucceeded || rec.FinishedAt == nil {
			t.Fatalf("after a successful retry: state=%s finished=%v, want succeeded with finished_at",
				rec.State, rec.FinishedAt)
		}
		if rec.Attempt != 2 {
			t.Fatalf("attempt = %d, want 2", rec.Attempt)
		}
		// A later call replays the recorded result.
		replay, err := ledger.BeginCall(ctx, gateRequestWithKey(t, instance, "magento.gate.capture", "", args, key))
		if err != nil {
			t.Fatalf("BeginCall replay: %v", err)
		}
		if replay.Decision != GateReplay || !replay.Replayed {
			t.Fatalf("replay = %+v, want a replay decision", replay)
		}
		var decoded map[string]any
		if err := json.Unmarshal(replay.ReplayResult, &decoded); err != nil {
			t.Fatalf("replayed result is not JSON: %v", err)
		}
		if decoded["invoice_id"] != float64(555) {
			t.Fatalf("replayed result = %s", replay.ReplayResult)
		}
	})

	// retry -> failure ends failed.
	t.Run("failure", func(t *testing.T) {
		key := "seed-retry-failure"
		id := seedState(t, ledger, instance, key, OpFailed, "upstream_rejected")
		confirmationID := approvedConfirmation(t, cs, "magento.gate.capture", instance, "agent-1", args)
		ticket, err := ledger.BeginCall(ctx, gateRequestWithKey(t, instance, "magento.gate.capture", confirmationID, args, key))
		if err != nil {
			t.Fatalf("BeginCall: %v", err)
		}
		if err := ledger.FinishCall(ctx, ticket, GateOutcome{
			State: OpFailed, ErrorCode: "validation_failed",
		}); err != nil {
			t.Fatalf("FinishCall: %v", err)
		}
		rec, err := ledger.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if rec.State != OpFailed || rec.FinishedAt == nil || rec.ErrorCode != "validation_failed" {
			t.Fatalf("after a failed retry: state=%s code=%s finished=%v, want failed/validation_failed",
				rec.State, rec.ErrorCode, rec.FinishedAt)
		}
	})

	// retry -> ambiguous ends unknown, never executing and never succeeded.
	t.Run("ambiguous", func(t *testing.T) {
		key := "seed-retry-unknown"
		id := seedState(t, ledger, instance, key, OpFailed, "upstream_rejected")
		confirmationID := approvedConfirmation(t, cs, "magento.gate.capture", instance, "agent-1", args)
		ticket, err := ledger.BeginCall(ctx, gateRequestWithKey(t, instance, "magento.gate.capture", confirmationID, args, key))
		if err != nil {
			t.Fatalf("BeginCall: %v", err)
		}
		if err := ledger.FinishCall(ctx, ticket, GateOutcome{
			State: OpUnknown, ErrorCode: "upstream_timeout", RetryAfter: time.Minute,
		}); err != nil {
			t.Fatalf("FinishCall: %v", err)
		}
		rec, err := ledger.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if rec.State != OpUnknown {
			t.Fatalf("after an ambiguous retry: state=%s, want unknown (never executing, never succeeded)", rec.State)
		}
	})
}

// REL43-02: recovery writes an audit outcome for each row it recovers.
func TestRecoverExpiredWritesAudit(t *testing.T) {
	ledger, _, store, instance := gateFixture(t, "recoveraudit")
	ctx := context.Background()
	executing := seedState(t, ledger, instance, "audit-executing", OpExecuting, "")
	if _, err := store.pool.Exec(ctx,
		`UPDATE operations SET lease_expires_at = now() - interval '1 minute' WHERE id = $1::uuid`, executing); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	if _, err := ledger.RecoverExpired(ctx, 0, 0); err != nil {
		t.Fatalf("RecoverExpired: %v", err)
	}
	var state, code string
	err := store.pool.QueryRow(ctx, `
		SELECT state, COALESCE(error_code,'') FROM audit_events
		 WHERE tool = 'magento.gate.capture' AND scope = 'recovery' AND phase = 'outcome'
		 ORDER BY seq DESC LIMIT 1`).Scan(&state, &code)
	if err != nil {
		t.Fatalf("no recovery audit outcome: %v", err)
	}
	if state != "outcome_unknown" || code != "lease_expired" {
		t.Fatalf("recovery audit = %s/%s, want outcome_unknown/lease_expired", state, code)
	}
}

// REL43-02: an operator abort writes an audit outcome.
func TestAbortWritesAudit(t *testing.T) {
	ledger, _, store, instance := gateFixture(t, "abortaudit")
	ctx := context.Background()
	id := seedState(t, ledger, instance, "audit-abort", OpExecuting, "")
	if applied, err := ledger.Abort(ctx, id, OpExecuting, "operator proved no charge"); err != nil || !applied {
		t.Fatalf("Abort: applied=%v err=%v", applied, err)
	}
	var state, code string
	err := store.pool.QueryRow(ctx, `
		SELECT state, COALESCE(error_code,'') FROM audit_events
		 WHERE tool = 'magento.gate.capture' AND scope = 'abort' AND phase = 'outcome'
		 ORDER BY seq DESC LIMIT 1`).Scan(&state, &code)
	if err != nil {
		t.Fatalf("no abort audit outcome: %v", err)
	}
	if state != "failed" || code != "admin_abort" {
		t.Fatalf("abort audit = %s/%s, want failed/admin_abort", state, code)
	}
}
