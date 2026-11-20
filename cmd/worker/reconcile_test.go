// SPDX-License-Identifier: Apache-2.0

package main

// Tests for the platform:reconcile handler: it claims unknown operations, applies
// the per-capability read, and resolves each one exactly once — or escalates.

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/magento"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

// fakeObserver answers ObserveOperation from a table.
type fakeObserver struct {
	byEntity map[string]magento.Observation
	err      error
}

func (f *fakeObserver) ObserveOperation(_ context.Context, _, _, entityID string) (magento.Observation, error) {
	if f.err != nil {
		return magento.ObservationInconclusive, f.err
	}
	if observation, ok := f.byEntity[entityID]; ok {
		return observation, nil
	}
	return magento.ObservationInconclusive, nil
}

func reconcileStore(t *testing.T) (*storage.Store, *storage.OperationLedger) {
	t.Helper()
	url := workerTestDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, storage.Config{URL: url, MaxConns: 4})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// ClaimReconcilable is global: it returns the oldest due unknown operation in
	// the schema, whatever test created it. A deferred row from an earlier run
	// stays unknown and becomes due again, so without this the handler spends its
	// batch on leftovers and the test's own operations are never reached.
	pushDueOperationsAside(t, url)
	return store, storage.NewOperationLedger(store)
}

// pushDueOperationsAside makes every pre-existing unknown operation not due, so a
// test observes only the operations it created.
func pushDueOperationsAside(t *testing.T, url string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect for cleanup: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx,
		`UPDATE operations SET reconcile_after = now() + interval '1 hour'
		 WHERE state = 'unknown'`); err != nil {
		t.Fatalf("defer leftover unknown operations: %v", err)
	}
}

// unknownOperation seeds one operation and drives it to unknown.
func unknownOperation(t *testing.T, ledger *storage.OperationLedger, instance, key, entityID string) string {
	t.Helper()
	ctx := context.Background()
	hash, err := storage.ArgsHash([]byte(`{"id":"` + entityID + `"}`))
	if err != nil {
		t.Fatalf("ArgsHash: %v", err)
	}
	res, err := ledger.Begin(ctx, storage.Operation{
		IdempotencyKey: key, Operation: "magento.invoice.capture", InstanceID: instance,
		Subject: "agent-1", EntityType: storage.EntityInvoice, EntityID: entityID,
		ArgsHash: hash,
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	exec, applied, err := ledger.Execute(ctx, res.OperationID, time.Minute)
	if err != nil || !applied {
		t.Fatalf("Execute: applied=%v err=%v", applied, err)
	}
	if ok, err := ledger.Unknown(ctx, res.OperationID, exec.Fence, "upstream_timeout", time.Minute); err != nil || !ok {
		t.Fatalf("Unknown: ok=%v err=%v", ok, err)
	}
	// Make it due now.
	if _, err := ledger.MakeReconcilable(ctx, res.OperationID); err != nil {
		t.Fatalf("MakeReconcilable: %v", err)
	}
	return res.OperationID
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// runSuffix keeps each test run's idempotency keys unique: the audit database is
// persistent, so a fixed key would replay the previous run's operation.
func runSuffix() string { return strconv.FormatInt(time.Now().UnixNano(), 36) }

// One operation resolves to succeeded, one to failed, one stays unknown.
func TestReconcileHandlerResolvesEachWay(t *testing.T) {
	store, ledger := reconcileStore(t)
	ctx := context.Background()
	suffix := runSuffix()
	instance := "store-reconcile-" + suffix

	succeeded := unknownOperation(t, ledger, instance, "key-ok-"+suffix, "inv-1-"+suffix)
	failed := unknownOperation(t, ledger, instance, "key-no-"+suffix, "inv-2-"+suffix)
	deferred := unknownOperation(t, ledger, instance, "key-maybe-"+suffix, "inv-3-"+suffix)

	observer := &fakeObserver{byEntity: map[string]magento.Observation{
		"inv-1-" + suffix: magento.ObservationApplied,
		"inv-2-" + suffix: magento.ObservationNotApplied,
		"inv-3-" + suffix: magento.ObservationInconclusive,
	}}
	handler := newReconcileHandler(ledger,
		func(string) (operationObserver, error) { return observer, nil }, testLogger())

	if err := handler(ctx, jobs.Task{ID: "reconcile-1", Kind: jobs.TypeReconcile}); err != nil {
		t.Fatalf("handler: %v", err)
	}

	ok, err := ledger.Get(ctx, succeeded)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok.State != storage.OpSucceeded || ok.FinishedAt == nil {
		t.Fatalf("applied operation = %s, want succeeded", ok.State)
	}
	no, err := ledger.Get(ctx, failed)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if no.State != storage.OpFailed || no.ErrorCode != "reconciled_not_applied" {
		t.Fatalf("not-applied operation = %s/%s, want failed/reconciled_not_applied", no.State, no.ErrorCode)
	}
	maybe, err := ledger.Get(ctx, deferred)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if maybe.State != storage.OpUnknown {
		t.Fatalf("inconclusive operation = %s, want unknown", maybe.State)
	}
	if maybe.ReconcileCount != 1 {
		t.Fatalf("reconcile count = %d, want 1", maybe.ReconcileCount)
	}

	// REL38-07: the resolution must leave an audit outcome, not just an intent.
	audits, err := store.ListAudit(ctx, "magento.invoice.capture", 50)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var succeededAudit, failedAudit bool
	for _, entry := range audits {
		if entry.Phase != "outcome" {
			continue
		}
		if entry.State == "succeeded" {
			succeededAudit = true
		}
		if entry.State == "failed" {
			failedAudit = true
		}
	}
	if !succeededAudit || !failedAudit {
		t.Fatalf("reconciliation audit outcomes missing (succeeded=%v failed=%v): %+v",
			succeededAudit, failedAudit, audits)
	}
}

// A read failure defers the operation instead of guessing.
func TestReconcileHandlerDefersOnReadFailure(t *testing.T) {
	_, ledger := reconcileStore(t)
	ctx := context.Background()
	suffix := runSuffix()
	id := unknownOperation(t, ledger, "store-reconcile-err-"+suffix, "key-err-"+suffix, "inv-9-"+suffix)

	observer := &fakeObserver{err: context.DeadlineExceeded}
	handler := newReconcileHandler(ledger,
		func(string) (operationObserver, error) { return observer, nil }, testLogger())
	if err := handler(ctx, jobs.Task{ID: "reconcile-2", Kind: jobs.TypeReconcile}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	rec, err := ledger.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != storage.OpUnknown {
		t.Fatalf("state = %s, want unknown (a failed read must not resolve)", rec.State)
	}
}

// After the bounded attempt count the reconciler escalates instead of looping.
func TestReconcileHandlerEscalates(t *testing.T) {
	_, ledger := reconcileStore(t)
	ctx := context.Background()
	suffix := runSuffix()
	id := unknownOperation(t, ledger, "store-reconcile-esc-"+suffix, "key-esc-"+suffix, "inv-10-"+suffix)

	// An inconclusive read each time: the reconciler must stop after the bound.
	observer := &fakeObserver{byEntity: map[string]magento.Observation{"inv-10-" + suffix: magento.ObservationInconclusive}}
	handler := newReconcileHandler(ledger,
		func(string) (operationObserver, error) { return observer, nil }, testLogger())
	for attempt := 0; attempt <= maxReconcileAttempts; attempt++ {
		if _, err := ledger.MakeReconcilable(ctx, id); err != nil {
			t.Fatalf("MakeReconcilable: %v", err)
		}
		if err := handler(ctx, jobs.Task{ID: "reconcile-3", Kind: jobs.TypeReconcile}); err != nil {
			t.Fatalf("handler pass %d: %v", attempt, err)
		}
	}

	rec, err := ledger.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != storage.OpUnknown {
		t.Fatalf("state = %s, want unknown (escalated, not resolved)", rec.State)
	}
	history, err := ledger.Transitions(ctx, id)
	if err != nil {
		t.Fatalf("Transitions: %v", err)
	}
	escalated := false
	for _, entry := range history {
		if entry.Reason == "escalated" {
			escalated = true
		}
	}
	if !escalated {
		t.Fatalf("no escalation transition recorded: %+v", history)
	}
	// REL38-09: escalation is terminal — a second pass must not re-escalate.
	if err := handler(ctx, jobs.Task{ID: "reconcile-4", Kind: jobs.TypeReconcile}); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	history, err = ledger.Transitions(ctx, id)
	if err != nil {
		t.Fatalf("Transitions: %v", err)
	}
	count := 0
	for _, entry := range history {
		if entry.Reason == "escalated" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d escalation transitions after a second pass, want 1", count)
	}
}
