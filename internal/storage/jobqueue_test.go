// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/shuaiZend/mage2mcp/internal/jobs"
)

// PERF-19: the audit recorded a fixed 5-second retry with no growth and no jitter.
// The scheduling SQL already computes 5s * 2^(attempt-1), capped at 5 minutes, with
// ±25% jitter. This asserts the growth rather than trusting the expression, so a
// future simplification back to a constant fails here.
func TestJobRetryBackoffGrowsWithAttempts(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	queue := NewJobQueue(store)
	// Claim is global and recovers expired leases before it selects, so a job left
	// queued or running by an earlier run is a candidate. Retire everything else
	// outright: deferring only the queued ones left abandoned running rows to be
	// recovered and claimed instead of this test's job.
	if _, err := store.pool.Exec(ctx,
		`UPDATE jobs SET state = 'failed', finished_at = now(),
		        lease_token = NULL, lease_expires_at = NULL
		 WHERE state IN ('queued','running','canceling')`); err != nil {
		t.Fatalf("retire leftover jobs: %v", err)
	}

	id, err := queue.Enqueue(ctx, jobs.Task{
		Kind: "platform:noop", Queue: "default", MaxAttempts: 5,
		IdempotencyKey: "backoff-" + runSuffix(t),
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	var delays []time.Duration
	for attempt := 0; attempt < 3; attempt++ {
		claimed, err := queue.Claim(ctx, []string{"default"}, time.Minute)
		if err != nil || claimed == nil || claimed.ID != id {
			t.Fatalf("Claim %d: claimed=%v err=%v", attempt, claimed, err)
		}
		if err := queue.Fail(ctx, id, claimed.LeaseToken, "upstream_unavailable", true); err != nil {
			t.Fatalf("Fail %d: %v", attempt, err)
		}
		var delay time.Duration
		if err := store.pool.QueryRow(ctx,
			`SELECT scheduled_at - now() FROM jobs WHERE id = $1::uuid`, id).Scan(&delay); err != nil {
			t.Fatalf("read scheduled_at: %v", err)
		}
		delays = append(delays, delay)
		// Make it due immediately so the next Claim picks it up.
		if _, err := store.pool.Exec(ctx,
			`UPDATE jobs SET scheduled_at = now() WHERE id = $1::uuid`, id); err != nil {
			t.Fatalf("reschedule: %v", err)
		}
	}
	if len(delays) != 3 {
		t.Fatalf("delays = %v", delays)
	}
	if delays[0] >= delays[1] || delays[1] >= delays[2] {
		t.Fatalf("delays = %v, want each retry scheduled further out", delays)
	}
	// 5s, 10s, 20s with ±25% jitter; the third must be well beyond a constant 5s.
	if delays[2] < 12*time.Second {
		t.Fatalf("third delay = %v, want exponential growth past the 5s base", delays[2])
	}
}

// runSuffix keeps each run's idempotency keys unique on a persistent test database.
func runSuffix(t *testing.T) string {
	t.Helper()
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

// PERF-17: FinishImportBatch inserted one round-trip per failed row. The batch
// history exists for imports where many rows fail, so the slow path was the one that
// matters. Failures are now written in chunks, and the rows must be identical.
func TestFinishImportBatchWritesEveryFailure(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	jobID := enqueueTestJob(t, NewJobQueue(store), "import", 2)
	id, err := store.CreateImportBatch(ctx, "store-us", jobID, "csv", []byte("sku,name\n"))
	if err != nil {
		t.Fatalf("CreateImportBatch: %v", err)
	}
	const rows = 1200 // spans more than one chunk
	failures := make([]jobs.ImportBatchFailure, 0, rows)
	for i := 1; i <= rows; i++ {
		failures = append(failures, jobs.ImportBatchFailure{
			RowNumber: i, SKU: fmt.Sprintf("SKU-%d", i), Action: "create", Error: "rejected",
		})
	}
	result := &jobs.ImportBatchResult{Total: rows, Processed: rows, Failed: rows, Failures: failures}
	if err := store.FinishImportBatch(ctx, id, result, "failed", "validation_failed"); err != nil {
		t.Fatalf("FinishImportBatch: %v", err)
	}
	var stored int
	if err := store.pool.QueryRow(ctx,
		`SELECT count(*) FROM import_batch_failures WHERE batch_id = $1::uuid`, id).Scan(&stored); err != nil {
		t.Fatalf("count failures: %v", err)
	}
	if stored != rows {
		t.Fatalf("stored failures = %d, want %d", stored, rows)
	}
	// A re-run must upsert rather than duplicate.
	if err := store.FinishImportBatch(ctx, id, result, "failed", "validation_failed"); err != nil {
		t.Fatalf("second FinishImportBatch: %v", err)
	}
	if err := store.pool.QueryRow(ctx,
		`SELECT count(*) FROM import_batch_failures WHERE batch_id = $1::uuid`, id).Scan(&stored); err != nil {
		t.Fatalf("count failures: %v", err)
	}
	if stored != rows {
		t.Fatalf("after re-run stored failures = %d, want %d (upsert, not duplicate)", stored, rows)
	}
}

// QA-10: the lifecycle test claimed from the shared "default" queue and asserted on a
// specific task id, so it passed only while no other default-queue job was queued. This
// exercises the property that actually matters — two concurrent claims of the same job
// yield exactly one winner — which nothing covered before.
func TestJobQueueConcurrentClaimHasOneWinner(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	queue := NewJobQueue(store)
	// Queue names are fixed by a CHECK constraint, so isolation comes from retiring
	// whatever an earlier run left behind.
	if _, err := store.pool.Exec(ctx,
		`UPDATE jobs SET state = 'failed', finished_at = now(),
		        lease_token = NULL, lease_expires_at = NULL
		 WHERE state IN ('queued','running','canceling')`); err != nil {
		t.Fatalf("retire leftover jobs: %v", err)
	}

	id, err := queue.Enqueue(ctx, jobs.Task{
		Kind: jobs.TypeNoop, Payload: []byte(`{}`), MaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	const racers = 8
	results := make(chan *jobs.Task, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := queue.Claim(ctx, []string{"default"}, time.Minute)
			if err != nil {
				t.Errorf("Claim: %v", err)
				return
			}
			results <- claimed
		}()
	}
	wg.Wait()
	close(results)

	winners := 0
	for claimed := range results {
		if claimed != nil && claimed.ID == id {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly one claim of the job", winners)
	}
}

// QA-09: the two tests that claimed to cover a closed store only exercised a nil
// receiver, so a use-after-close regression would have passed. This opens a real store,
// closes it, and asserts every path refuses.
func TestImportBatchStoreAfterClose(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	store.Close()

	if _, err := store.CreateImportBatch(ctx, "i", "", "csv", nil); err == nil {
		t.Fatal("create after Close must fail")
	}
	if err := store.FinishImportBatch(ctx, "i", &jobs.ImportBatchResult{}, "failed", ""); err == nil {
		t.Fatal("finish after Close must fail")
	}
	if _, err := store.ListImportBatches(ctx, "i", 10); err == nil {
		t.Fatal("list after Close must fail")
	}
	if _, err := store.GetImportBatch(ctx, "i", "", ""); err == nil {
		t.Fatal("get after Close must fail")
	}
	if _, err := store.PruneImportBatches(ctx, time.Now()); err == nil {
		t.Fatal("prune after Close must fail")
	}
}
