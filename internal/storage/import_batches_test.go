// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"testing"

	"github.com/shuaiZend/mage2mcp/internal/jobs"
)

// REL-11: a retried import must resume its open batch, not open a second one,
// and the by-job lookup must be deterministic when history exists.
func TestCreateImportBatchResumesOpenBatch(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	jobID := enqueueTestJob(t, NewJobQueue(store), "import", 2)
	first, err := store.CreateImportBatch(ctx, "store-us", jobID, "csv", []byte("sku,name\nA,Apple\n"))
	if err != nil {
		t.Fatalf("CreateImportBatch: %v", err)
	}
	second, err := store.CreateImportBatch(ctx, "store-us", jobID, "csv", []byte("sku,name\nA,Apple\n"))
	if err != nil {
		t.Fatalf("second CreateImportBatch: %v", err)
	}
	if first != second {
		t.Fatalf("a retry opened a second batch: %s then %s", first, second)
	}

	// Once the batch is terminal, a later attempt opens a new one (history is
	// not resumed).
	if err := store.FinishImportBatch(ctx, first, &jobs.ImportBatchResult{Total: 1, Created: 1}, "succeeded", ""); err != nil {
		t.Fatalf("FinishImportBatch: %v", err)
	}
	third, err := store.CreateImportBatch(ctx, "store-us", jobID, "csv", []byte("sku,name\nA,Apple\n"))
	if err != nil {
		t.Fatalf("CreateImportBatch after completion: %v", err)
	}
	if third == first {
		t.Fatal("a terminal batch was resumed; a new attempt must open its own")
	}

	// The by-job lookup returns the newest deterministically.
	got, err := store.GetImportBatchByJob(ctx, jobID, "store-us", "system")
	if err != nil {
		t.Fatalf("GetImportBatchByJob: %v", err)
	}
	if got == nil || got.ID != third {
		t.Fatalf("GetImportBatchByJob = %+v, want the newest batch %s", got, third)
	}
}

// REL-12: a batch opened 'running' is only closed by FinishImportBatch, so a
// crashed worker leaves it running forever and magento.import.batches reports an
// import that is not happening. The lease plus the reaper is the exit a dead
// process never reaches.
func TestReapImportBatchesRecoversOrphanedRunningBatch(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	jobID := enqueueTestJob(t, NewJobQueue(store), "import", 2)
	id, err := store.CreateImportBatch(ctx, "store-us", jobID, "csv", []byte("sku,name\nA,Apple\n"))
	if err != nil {
		t.Fatalf("CreateImportBatch: %v", err)
	}
	// A fresh batch has a live lease, so nothing may be reaped yet.
	if reaped, err := store.ReapImportBatches(ctx); err != nil {
		t.Fatalf("ReapImportBatches: %v", err)
	} else if reaped != 0 {
		t.Fatalf("reaped = %d, want 0 while the lease is live", reaped)
	}
	// Simulate a dead worker: expire the batch lease and the job lease.
	if _, err := store.pool.Exec(ctx,
		`UPDATE import_batches SET lease_expires_at = now() - interval '1 minute' WHERE id = $1::uuid`, id); err != nil {
		t.Fatalf("expire batch lease: %v", err)
	}
	if _, err := store.pool.Exec(ctx,
		`UPDATE jobs SET lease_expires_at = now() - interval '1 minute' WHERE id = $1::uuid`, jobID); err != nil {
		t.Fatalf("expire job lease: %v", err)
	}
	reaped, err := store.ReapImportBatches(ctx)
	if err != nil {
		t.Fatalf("ReapImportBatches: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped = %d, want 1", reaped)
	}
	var state, code string
	if err := store.pool.QueryRow(ctx,
		`SELECT state, COALESCE(error_code,'') FROM import_batches WHERE id = $1::uuid`, id).Scan(&state, &code); err != nil {
		t.Fatalf("read batch: %v", err)
	}
	if state != "failed" || code != "worker_lost" {
		t.Fatalf("batch = %s/%s, want failed/worker_lost", state, code)
	}
}

// A live job that still owns the batch means the worker is alive, just slow: the
// reaper must leave it alone.
func TestReapImportBatchesKeepsBatchWithLiveJob(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	jobID := enqueueTestJob(t, NewJobQueue(store), "import", 2)
	id, err := store.CreateImportBatch(ctx, "store-us", jobID, "csv", []byte("sku,name\nA,Apple\n"))
	if err != nil {
		t.Fatalf("CreateImportBatch: %v", err)
	}
	// Expire only the batch lease and leave the job genuinely live: a queued job
	// holds no lease, so it does not protect the batch — only a running, leased
	// job does.
	if _, err := store.pool.Exec(ctx,
		`UPDATE jobs SET state = 'running', lease_token = gen_random_uuid(),
		        lease_expires_at = now() + interval '10 minutes' WHERE id = $1::uuid`, jobID); err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if _, err := store.pool.Exec(ctx,
		`UPDATE import_batches SET lease_expires_at = now() - interval '1 minute' WHERE id = $1::uuid`, id); err != nil {
		t.Fatalf("expire batch lease: %v", err)
	}
	reaped, err := store.ReapImportBatches(ctx)
	if err != nil {
		t.Fatalf("ReapImportBatches: %v", err)
	}
	if reaped != 0 {
		t.Fatalf("reaped = %d, want 0 while a live job still owns the batch", reaped)
	}
}
