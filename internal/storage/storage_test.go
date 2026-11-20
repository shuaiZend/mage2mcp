// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/security/audit"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
)

// testSchemaDSN returns the test database DSN scoped to a per-package schema.
//
// Database-backed test packages share one PostgreSQL instance and `go test ./...`
// runs packages in parallel, so an unscoped DSN lets one package's cleanup — for
// example `DELETE FROM jobs` here — destroy another package's fixtures while it
// is still using them. A per-package search_path removes that interference
// without serialising the suite.
func testSchemaDSN(t *testing.T, schema string) string {
	t.Helper()
	base := os.Getenv("MAGE2MCP_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("MAGE2MCP_TEST_DATABASE_URL is not set; skipping PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect to create test schema: %v", err)
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{schema}.Sanitize()); err != nil {
		_ = conn.Close(ctx)
		t.Fatalf("create test schema %q: %v", schema, err)
	}
	if err := conn.Close(ctx); err != nil {
		t.Fatalf("close schema-setup connection: %v", err)
	}
	parsed, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse MAGE2MCP_TEST_DATABASE_URL: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func testStore(t *testing.T) *Store {
	t.Helper()
	url := testSchemaDSN(t, "test_storage")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, Config{URL: url, MaxConns: 4})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return store
}

func TestMigrateIsIdempotent(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestAuditRecorderRoundTrip(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	rec := NewAuditRecorder(store)
	tool := "platform.info"
	if err := rec.Record(ctx, audit.Event{
		Subject: "user-1", IdentityClass: "human", Tool: tool,
		Decision: "allow", Phase: "intent", State: "pending",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := rec.Record(ctx, audit.Event{
		Subject: "user-1", IdentityClass: "human", Tool: tool,
		Decision: "allow", Phase: "outcome", State: "succeeded",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	records, err := store.ListAudit(ctx, tool, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(records) < 2 {
		t.Fatalf("records = %d, want >= 2", len(records))
	}
	if records[0].SubjectHash != SubjectHash("user-1") {
		t.Fatal("subject hash mismatch")
	}
	if records[0].Phase != "outcome" {
		t.Fatalf("newest phase = %q, want outcome", records[0].Phase)
	}
}

func TestAuditRejectsMissingFields(t *testing.T) {
	store := testStore(t)
	rec := NewAuditRecorder(store)
	if err := rec.Record(context.Background(), audit.Event{Tool: "x"}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestConfirmStoreLifecycle(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	svc, err := confirm.NewService(NewConfirmStore(store), confirm.DefaultTTL)
	if err != nil {
		t.Fatal(err)
	}
	args := []byte(`{"sku":"A-1","value":"new name"}`)
	req, err := svc.Require(ctx, "magento.product.update", "store-us", "write", "Update A-1", "agent-1", args)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, req.ID, "agent-1"); err == nil {
		t.Fatal("self-approval must be rejected")
	}
	if err := svc.Approve(ctx, req.ID, "human-1"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := svc.Consume(ctx, req.ID, "agent-1", "magento.product.update", "store-us", args); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if err := svc.Consume(ctx, req.ID, "agent-1", "magento.product.update", "store-us", args); err == nil {
		t.Fatal("replay must be rejected")
	}
}

func TestJobQueueLifecycle(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	queue := NewJobQueue(store)
	// Claim has no task-id predicate, so a job left queued by an earlier run would be
	// claimed instead and the assertion below would fail for a reason that has nothing
	// to do with the code under test. Pushing leftovers aside removes that order
	// dependence (QA-10); the queue names themselves are fixed by a CHECK constraint.
	if _, err := store.pool.Exec(ctx,
		`UPDATE jobs SET state = 'failed', finished_at = now(),
		        lease_token = NULL, lease_expires_at = NULL
		 WHERE state IN ('queued','running','canceling')`); err != nil {
		t.Fatalf("retire leftover jobs: %v", err)
	}
	id, err := queue.Enqueue(ctx, jobs.Task{
		Kind: jobs.TypeNoop, Payload: []byte(`{"message":"hi"}`), MaxAttempts: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := queue.Claim(ctx, []string{"default"}, time.Minute)
	if err != nil || task == nil {
		t.Fatalf("Claim = %v, %v", task, err)
	}
	if task.ID != id || task.Attempts != 1 || task.MaxAttempts != 2 {
		t.Fatalf("task = %+v", task)
	}
	if task.LeaseToken == "" {
		t.Fatal("claim did not return a fencing token")
	}
	if err := queue.Complete(ctx, id, task.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if next, _ := queue.Claim(ctx, []string{"default"}, time.Minute); next != nil && next.ID == id {
		t.Fatal("completed task was claimed again")
	}
}

func TestJobQueueRecoversExpiredLease(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	queue := NewJobQueue(store)
	id, err := queue.Enqueue(ctx, jobs.Task{Kind: jobs.TypeNoop, MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Claim(ctx, []string{"default"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	// Simulate a worker that died holding the lease.
	if _, err := store.pool.Exec(ctx, `UPDATE jobs SET lease_expires_at = now() - interval '1 minute' WHERE id = $1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	recovered, err := queue.Claim(ctx, []string{"default"}, time.Minute)
	if err != nil || recovered == nil || recovered.ID != id {
		t.Fatalf("expired lease not recovered: %+v %v", recovered, err)
	}
	if recovered.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", recovered.Attempts)
	}
}

func TestJobQueueResultAndLookup(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	queue := NewJobQueue(store)
	id, err := queue.Enqueue(ctx, jobs.Task{Kind: jobs.TypeMagentoImport, Queue: jobs.QueueImport, MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	record, err := queue.GetJob(ctx, id, "", "system")
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != id || record.Kind != jobs.TypeMagentoImport || record.State != "queued" || record.MaxAttempts != 2 {
		t.Fatalf("record = %+v", record)
	}
	if len(record.Payload) == 0 {
		t.Fatal("job payload is not returned by GetJob")
	}
	claimed, err := queue.Claim(ctx, []string{"import"}, time.Minute)
	if err != nil || claimed == nil || claimed.ID != id {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if err := queue.SetResult(ctx, id, claimed.LeaseToken, `{"total":3}`); err != nil {
		t.Fatal(err)
	}
	record, err = queue.GetJob(ctx, id, "", "system")
	if err != nil {
		t.Fatal(err)
	}
	if record.Result != `{"total":3}` {
		t.Fatalf("result = %q", record.Result)
	}
	if _, err := queue.GetJob(ctx, "00000000-0000-0000-0000-000000000000", "", "system"); errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("missing job err = %v", err)
	}
}

func TestJobQueueIdempotentEnqueue(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	queue := NewJobQueue(store)
	task := jobs.Task{
		Kind: jobs.TypeMagentoImport, Queue: jobs.QueueImport,
		InstanceID: "store-us", IdempotencyKey: "batch-1", MaxAttempts: 1,
	}
	first, err := queue.Enqueue(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	second, err := queue.Enqueue(ctx, task)
	if err != nil {
		t.Fatalf("repeat enqueue: %v", err)
	}
	if first != second {
		t.Fatalf("idempotent enqueue returned %q then %q", first, second)
	}
	task.IdempotencyKey = "batch-2"
	third, err := queue.Enqueue(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("a different key must create a new job")
	}
}

func TestImportBatchLifecycle(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	// The assertions below read back by instance_id, so a previous run's rows must
	// not be in scope (the suite is re-run against a persistent database).
	if _, err := store.pool.Exec(ctx, `DELETE FROM import_batches WHERE instance_id = 'store-us'`); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	id, err := store.CreateImportBatch(ctx, "store-us", "", "csv", []byte("sku,name\nA,B\n"))
	if err != nil {
		t.Fatal(err)
	}
	result := &jobs.ImportBatchResult{
		Total: 2, Processed: 2, Created: 1, Failed: 1,
		Failures: []jobs.ImportBatchFailure{{RowNumber: 3, SKU: "B", Action: "create", Error: "boom"}},
	}
	if err := store.FinishImportBatch(ctx, id, result, "partial", ""); err != nil {
		t.Fatal(err)
	}
	batch, err := store.GetImportBatchByJob(ctx, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if batch != nil {
		t.Fatal("empty job id must not match a batch")
	}
	var storedID string
	if err := store.pool.QueryRow(ctx, `SELECT id::text FROM import_batches WHERE instance_id = 'store-us'`).Scan(&storedID); err != nil {
		t.Fatal(err)
	}
	if storedID != id {
		t.Fatalf("stored id = %q", storedID)
	}
}

func TestListAndGetImportBatch(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	const instance = "store-list-test"
	// ListImportBatches returns every row for the instance; clear the leftovers from
	// earlier runs so len(list) is a property of this test alone.
	if _, err := store.pool.Exec(ctx, `DELETE FROM import_batches WHERE instance_id = $1`, instance); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	id, err := store.CreateImportBatch(ctx, instance, "", "csv", []byte("sku\nA\n"))
	if err != nil {
		t.Fatal(err)
	}
	result := &jobs.ImportBatchResult{Total: 1, Processed: 1, Failed: 1,
		Failures: []jobs.ImportBatchFailure{{RowNumber: 2, SKU: "A", Action: "create", Error: "boom"}}}
	if err := store.FinishImportBatch(ctx, id, result, "partial", ""); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListImportBatches(ctx, instance, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != id || list[0].State != "partial" || list[0].Failed != 1 {
		t.Fatalf("list = %+v", list)
	}
	batch, err := store.GetImportBatch(ctx, id, instance, "system")
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || len(batch.Failures) != 1 || batch.Failures[0].SKU != "A" {
		t.Fatalf("batch = %+v", batch)
	}
	if missing, err := store.GetImportBatch(ctx, "00000000-0000-0000-0000-000000000000", instance, "system"); err != nil || missing != nil {
		t.Fatalf("missing batch = %+v, %v", missing, err)
	}
	// Default limit and empty instance filter.
	all, err := store.ListImportBatches(ctx, "", 0)
	if err != nil || len(all) == 0 {
		t.Fatalf("all batches = %+v, %v", all, err)
	}
}

func TestPruneImportBatches(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	id, err := store.CreateImportBatch(ctx, "store-prune-test", "", "csv", []byte("sku\nA\n"))
	if err != nil {
		t.Fatal(err)
	}
	result := &jobs.ImportBatchResult{Total: 1, Processed: 1, Failed: 1,
		Failures: []jobs.ImportBatchFailure{{RowNumber: 2, SKU: "A", Action: "create", Error: "boom"}}}
	if err := store.FinishImportBatch(ctx, id, result, "partial", ""); err != nil {
		t.Fatal(err)
	}
	// Backdate the batch so the retention window removes it.
	if _, err := store.pool.Exec(ctx, `UPDATE import_batches SET created_at = now() - interval '40 days' WHERE id = $1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	removed, err := store.PruneImportBatches(ctx, time.Now().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed < 1 {
		t.Fatalf("removed = %d", removed)
	}
	if batch, err := store.GetImportBatch(ctx, id, "store-prune-test", "system"); err != nil || batch != nil {
		t.Fatalf("pruned batch = %+v, %v", batch, err)
	}
	var failures int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM import_batch_failures WHERE batch_id = $1::uuid`, id).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != 0 {
		t.Fatalf("failure rows survived pruning: %d", failures)
	}
}

// A nil receiver is not a closed store: this covers the nil guard, and the name now
// says so (QA-09). The real close path is TestImportBatchStoreAfterClose.
func TestPruneImportBatchesNilStore(t *testing.T) {
	var store *Store
	if _, err := store.PruneImportBatches(context.Background(), time.Now()); err == nil {
		t.Fatal("pruning a nil store must fail")
	}
}

// This exercises the nil-receiver guards, not behaviour after Close. The name says so
// now, and TestImportBatchStoreAfterClose covers the real close path (QA-09).
func TestImportBatchNilStore(t *testing.T) {
	var store *Store
	ctx := context.Background()
	if _, err := store.CreateImportBatch(ctx, "i", "", "csv", nil); err == nil {
		t.Fatal("create on a nil store must fail")
	}
	if err := store.FinishImportBatch(ctx, "i", &jobs.ImportBatchResult{}, "failed", ""); err == nil {
		t.Fatal("finish on a nil store must fail")
	}
	if _, err := store.GetImportBatchByJob(ctx, "i", "", ""); err == nil {
		t.Fatal("get-by-job on a nil store must fail")
	}
	if _, err := store.ListImportBatches(ctx, "i", 10); err == nil {
		t.Fatal("list on a nil store must fail")
	}
	if _, err := store.GetImportBatch(ctx, "i", "", ""); err == nil {
		t.Fatal("get on a nil store must fail")
	}
}

// SEC-R01: the batch list is scoped to the caller's subject as well as the
// instance, so one caller on an instance cannot see another caller's batch
// metadata.
func TestListImportBatchesIsSubjectScoped(t *testing.T) {
	store := testStore(t)
	queue := NewJobQueue(store)
	ctx := context.Background()
	const instance = "store-batch-subject"
	if _, err := store.pool.Exec(ctx,
		`DELETE FROM import_batches WHERE instance_id = $1`, instance); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(),
			`DELETE FROM import_batches WHERE instance_id = $1`, instance)
	})

	// One job and one batch per subject, on the same instance.
	enqueue := func(subject, key string) string {
		jobID, err := queue.Enqueue(ctx, jobs.Task{
			Kind: jobs.TypeMagentoImport, Queue: jobs.QueueImport,
			InstanceID: instance, IdempotencyKey: key, MaxAttempts: 1,
			Subject: subject,
		})
		if err != nil {
			t.Fatalf("enqueue for %s: %v", subject, err)
		}
		batchID, err := store.CreateImportBatch(ctx, instance, jobID, "csv", []byte("sku\nA\n"))
		if err != nil {
			t.Fatalf("create batch for %s: %v", subject, err)
		}
		return batchID
	}
	aliceBatch := enqueue("alice", "batch-subject-alice")
	bobBatch := enqueue("bob", "batch-subject-bob")

	list := func(subject string) []jobs.ImportBatch {
		subjectCtx := auth.WithIdentity(ctx, &auth.Identity{Subject: subject})
		batches, err := store.ListImportBatches(subjectCtx, instance, 100)
		if err != nil {
			t.Fatalf("list for %s: %v", subject, err)
		}
		return batches
	}

	aliceSees := list("alice")
	if len(aliceSees) != 1 || aliceSees[0].ID != aliceBatch {
		t.Fatalf("alice sees %+v, want only her own batch %s", aliceSees, aliceBatch)
	}
	bobSees := list("bob")
	if len(bobSees) != 1 || bobSees[0].ID != bobBatch {
		t.Fatalf("bob sees %+v, want only his own batch %s", bobSees, bobBatch)
	}
	// The leaked id must also be unreadable, which the get path already enforced.
	aliceCtx := auth.WithIdentity(ctx, &auth.Identity{Subject: "alice"})
	if leaked, err := store.GetImportBatch(aliceCtx, bobBatch, instance, "alice"); err != nil || leaked != nil {
		t.Fatalf("alice read bob's batch by id: %+v, %v", leaked, err)
	}
}

// SEC-R02: an intent row records no decision, so a phase-blind query for allowed
// calls cannot return a call that was subsequently denied.
func TestAuditIntentRowDoesNotClaimAllow(t *testing.T) {
	store := testStore(t)
	rec := NewAuditRecorder(store)
	ctx := context.Background()
	const tool = "test.audit.pending"
	if _, err := store.pool.Exec(ctx, `DELETE FROM audit_events WHERE tool = $1`, tool); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM audit_events WHERE tool = $1`, tool)
	})

	// A denied call: intent then outcome.
	if err := rec.Record(ctx, audit.Event{
		Subject: "agent-1", IdentityClass: "human", Tool: tool,
		Decision: DecisionPending, Phase: "intent", State: "pending",
	}); err != nil {
		t.Fatalf("record intent: %v", err)
	}
	if err := rec.Record(ctx, audit.Event{
		Subject: "agent-1", IdentityClass: "human", Tool: tool,
		Decision: "deny", Phase: "outcome", State: "denied", ErrorCode: "permission_denied",
	}); err != nil {
		t.Fatalf("record denied outcome: %v", err)
	}

	var allowed int
	if err := store.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events WHERE tool = $1 AND decision = 'allow'`, tool).Scan(&allowed); err != nil {
		t.Fatalf("count allowed: %v", err)
	}
	if allowed != 0 {
		t.Fatalf("%d rows match decision=allow for a denied call; the intent row still asserts allow", allowed)
	}

	// A granted call is still found.
	const grantedTool = "test.audit.granted"
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM audit_events WHERE tool = $1`, grantedTool)
	})
	if err := rec.Record(ctx, audit.Event{
		Subject: "agent-1", IdentityClass: "human", Tool: grantedTool,
		Decision: DecisionPending, Phase: "intent", State: "pending",
	}); err != nil {
		t.Fatalf("record granted intent: %v", err)
	}
	if err := rec.Record(ctx, audit.Event{
		Subject: "agent-1", IdentityClass: "human", Tool: grantedTool,
		Decision: "allow", Phase: "outcome", State: "succeeded",
	}); err != nil {
		t.Fatalf("record granted outcome: %v", err)
	}
	var grantedAllowed int
	if err := store.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events WHERE tool = $1 AND decision = 'allow'`, grantedTool).Scan(&grantedAllowed); err != nil {
		t.Fatalf("count granted: %v", err)
	}
	if grantedAllowed != 1 {
		t.Fatalf("%d rows match decision=allow for a granted call, want 1", grantedAllowed)
	}
}
