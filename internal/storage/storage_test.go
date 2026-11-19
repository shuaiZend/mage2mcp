// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"os"
	"testing"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/security/audit"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("MAGE2MCP_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("MAGE2MCP_TEST_DATABASE_URL is not set; skipping PostgreSQL integration test")
	}
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
	if err := svc.Consume(ctx, req.ID, "agent-1", args); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if err := svc.Consume(ctx, req.ID, "agent-1", args); err == nil {
		t.Fatal("replay must be rejected")
	}
}

func TestJobQueueLifecycle(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	queue := NewJobQueue(store)
	id, err := queue.Enqueue(ctx, jobs.Task{Kind: jobs.TypeNoop, Payload: []byte(`{"message":"hi"}`), MaxAttempts: 2})
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
	if err := queue.Complete(ctx, id); err != nil {
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
	record, err := queue.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != id || record.Kind != jobs.TypeMagentoImport || record.State != "queued" || record.MaxAttempts != 2 {
		t.Fatalf("record = %+v", record)
	}
	if len(record.Payload) == 0 {
		t.Fatal("job payload is not returned by GetJob")
	}
	if err := queue.SetResult(ctx, id, `{"total":3}`); err != nil {
		t.Fatal(err)
	}
	record, err = queue.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if record.Result != `{"total":3}` {
		t.Fatalf("result = %q", record.Result)
	}
	if _, err := queue.GetJob(ctx, "00000000-0000-0000-0000-000000000000"); errs.CodeOf(err) != errs.CodeNotFound {
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
	batch, err := store.GetImportBatchByJob(ctx, "")
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
