// SPDX-License-Identifier: Apache-2.0

package storage

// Regression tests for the job-queue integrity properties from
// docs/audit/reliability-review.md (REL-01, REL-04, REL-05, REL-09, REL-10,
// REL-13, REL-14, REL-17, REL-18, REL-19). Every test needs a real PostgreSQL:
// these are properties of the SQL transitions, and a fake cannot prove them.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/jobs"
)

// resetJobs gives each queue test a deterministic starting point and leaves the
// table as it found it. The package's older tests assume a virgin database (see
// REL-15 in the audit); a queue-integrity test must not depend on test order.
func resetJobs(t *testing.T, store *Store) {
	t.Helper()
	clean := func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM jobs`); err != nil {
			t.Fatalf("reset jobs: %v", err)
		}
	}
	clean()
	t.Cleanup(clean)
}

// enqueueTestJob inserts a job and returns its id.
func enqueueTestJob(t *testing.T, queue *JobQueue, queueName string, maxAttempts int) string {
	t.Helper()
	id, err := queue.Enqueue(context.Background(), jobs.Task{
		Kind: jobs.TypeNoop, Queue: queueName, MaxAttempts: maxAttempts,
		Payload: []byte(`{"message":"test"}`),
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return id
}

// expireLease makes a running job's lease lapse, as a stalled or dead worker's
// lease would.
func expireLease(t *testing.T, store *Store, id string) {
	t.Helper()
	if _, err := store.pool.Exec(context.Background(), `
		UPDATE jobs SET lease_expires_at = now() - interval '1 second' WHERE id = $1::uuid`, id); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
}

type jobState struct {
	State       string
	Attempts    int
	LeaseToken  *string
	ErrorCode   *string
	ScheduledAt time.Time
}

func readJob(t *testing.T, store *Store, id string) jobState {
	t.Helper()
	var s jobState
	if err := store.pool.QueryRow(context.Background(), `
		SELECT state, attempt_count, lease_token::text, last_error_code, scheduled_at
		FROM jobs WHERE id = $1::uuid`, id).
		Scan(&s.State, &s.Attempts, &s.LeaseToken, &s.ErrorCode, &s.ScheduledAt); err != nil {
		t.Fatalf("read job: %v", err)
	}
	return s
}

// requireLeaseLost asserts the typed conflict contract: the caller can recognise
// the loss with errors.Is, and a tool boundary maps it to `conflict`.
func requireLeaseLost(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected ErrLeaseLost, got nil (a stale write was silently accepted)", what)
	}
	if !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("%s: error %v does not wrap jobs.ErrLeaseLost", what, err)
	}
	if code := errs.CodeOf(err); code != errs.CodeConflict {
		t.Fatalf("%s: code = %s, want %s", what, code, errs.CodeConflict)
	}
}

// REL-01: once a lease has expired and the task has been reclaimed, the first
// attempt owns nothing. Two goroutines make the interleaving deterministic: A
// holds the claim, B reclaims, then A tries to write.
func TestJobQueueStaleAttemptCannotWrite(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	queue := NewJobQueue(store)

	id := enqueueTestJob(t, queue, "default", 5)
	claimA, err := queue.Claim(ctx, []string{"default"}, 300*time.Millisecond)
	if err != nil || claimA == nil || claimA.ID != id {
		t.Fatalf("claim A = %+v, %v", claimA, err)
	}

	reclaimed := make(chan *jobs.Task, 1)
	claimErr := make(chan error, 1)
	go func() {
		// Worker B: poll until A's lease lapses and recovery requeues the job.
		// A fixed sleep is timing-dependent under load, so wait for the
		// observable condition instead of guessing how long it takes.
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			task, err := queue.Claim(ctx, []string{"default"}, time.Minute)
			if err != nil {
				claimErr <- err
				reclaimed <- nil
				return
			}
			if task != nil {
				claimErr <- nil
				reclaimed <- task
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		claimErr <- errors.New("timed out waiting for the lease to lapse and the job to be reclaimed")
		reclaimed <- nil
	}()
	claimB := <-reclaimed
	if err := <-claimErr; err != nil {
		t.Fatalf("claim B: %v", err)
	}
	if claimB == nil || claimB.ID != id || claimB.Attempts != 2 {
		t.Fatalf("claim B = %+v, want the reclaimed task at attempt 2", claimB)
	}
	if claimA.LeaseToken == claimB.LeaseToken {
		t.Fatal("reclaim reused the fencing token; the stale attempt is not fenced out")
	}

	// Worker A is still running and tries to report. Every write must be a no-op.
	requireLeaseLost(t, queue.Complete(ctx, id, claimA.LeaseToken), "stale Complete")
	requireLeaseLost(t, queue.Fail(ctx, id, claimA.LeaseToken, "handler_error", true), "stale Fail")
	_, extendErr := queue.Extend(ctx, id, claimA.LeaseToken, time.Minute)
	requireLeaseLost(t, extendErr, "stale Extend")
	requireLeaseLost(t, queue.SetResult(ctx, id, claimA.LeaseToken, `{"progress":1}`), "stale SetResult")

	got := readJob(t, store, id)
	if got.State != "running" || got.Attempts != 2 {
		t.Fatalf("after stale writes: state=%s attempts=%d, want running/2 (a stale write changed the row)",
			got.State, got.Attempts)
	}
	if got.LeaseToken == nil || *got.LeaseToken != claimB.LeaseToken {
		t.Fatalf("lease token changed to %v; the live attempt no longer owns the row", got.LeaseToken)
	}

	// The live attempt still owns the row.
	if err := queue.Complete(ctx, id, claimB.LeaseToken); err != nil {
		t.Fatalf("live Complete: %v", err)
	}
	if got := readJob(t, store, id); got.State != "succeeded" || got.LeaseToken != nil {
		t.Fatalf("after live Complete: state=%s token=%v, want succeeded/nil", got.State, got.LeaseToken)
	}
}

// REL-04/REL-05/REL-19: cancellation is a real state, it cannot be undone by a
// late retry, and a running task is asked to stop through its lease renewal.
func TestJobQueueCancelIsTerminal(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	queue := NewJobQueue(store)

	t.Run("queued job is canceled immediately and cannot be resurrected", func(t *testing.T) {
		id := enqueueTestJob(t, queue, "default", 3)
		if err := queue.Cancel(ctx, id, "", "system"); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		if got := readJob(t, store, id); got.State != "canceled" {
			t.Fatalf("state = %s, want canceled", got.State)
		}
		// A worker that was between Claim and Fail retries anyway.
		requireLeaseLost(t, queue.Fail(ctx, id, "", "handler_error", true), "Fail after cancel")
		if got := readJob(t, store, id); got.State != "canceled" {
			t.Fatalf("a late Fail(retry=true) moved a canceled job to %s", got.State)
		}
	})

	t.Run("running job enters canceling, then ends canceled", func(t *testing.T) {
		id := enqueueTestJob(t, queue, "default", 3)
		task, err := queue.Claim(ctx, []string{"default"}, time.Minute)
		if err != nil || task == nil || task.ID != id {
			t.Fatalf("Claim = %+v, %v", task, err)
		}
		if err := queue.Cancel(ctx, id, "", "system"); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		got := readJob(t, store, id)
		if got.State != "canceling" {
			t.Fatalf("state = %s, want canceling", got.State)
		}
		if got.LeaseToken == nil || *got.LeaseToken != task.LeaseToken {
			t.Fatal("canceling dropped the lease token; the worker can no longer finish or be fenced")
		}

		// The heartbeat is how the worker learns about the cancel.
		status, err := queue.Extend(ctx, id, task.LeaseToken, time.Minute)
		if err != nil {
			t.Fatalf("Extend: %v", err)
		}
		if !status.CancelRequested {
			t.Fatal("Extend did not report the pending cancel; the handler would never be stopped")
		}

		// The stopped handler reports failure; retry must not win over cancel.
		if err := queue.Fail(ctx, id, task.LeaseToken, "canceled", true); err != nil {
			t.Fatalf("Fail: %v", err)
		}
		if got := readJob(t, store, id); got.State != "canceled" {
			t.Fatalf("state = %s, want canceled (a retry must not resurrect a canceled job)", got.State)
		}
		// And the terminal state is final.
		requireLeaseLost(t, queue.Complete(ctx, id, task.LeaseToken), "Complete after cancel")
		if got := readJob(t, store, id); got.State != "canceled" {
			t.Fatalf("a late Complete overwrote canceled with %s", got.State)
		}
	})

	t.Run("cancel is delivered when the handler already succeeded", func(t *testing.T) {
		id := enqueueTestJob(t, queue, "default", 3)
		task, err := queue.Claim(ctx, []string{"default"}, time.Minute)
		if err != nil || task == nil {
			t.Fatalf("Claim = %+v, %v", task, err)
		}
		if err := queue.Complete(ctx, id, task.LeaseToken); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if err := queue.Cancel(ctx, id, "", "system"); err == nil {
			t.Fatal("cancel of a succeeded job reported success")
		}
		if got := readJob(t, store, id); got.State != "succeeded" {
			t.Fatalf("state = %s, want succeeded", got.State)
		}
	})
}

// REL-14: Cancel reports success only when a row actually transitioned.
func TestJobQueueCancelReportsWhatHappened(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	queue := NewJobQueue(store)

	if err := queue.Cancel(ctx, "00000000-0000-0000-0000-000000000000", "", "system"); errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("cancel missing job: code = %s, want not_found", errs.CodeOf(err))
	}

	id := enqueueTestJob(t, queue, "default", 2)
	if err := queue.Cancel(ctx, id, "", "system"); err != nil {
		t.Fatalf("first Cancel: %v", err)
	}
	err := queue.Cancel(ctx, id, "", "system")
	if errs.CodeOf(err) != errs.CodeIllegalTransition {
		t.Fatalf("second cancel: code = %s, want illegal_transition", errs.CodeOf(err))
	}
	if !errors.Is(err, jobs.ErrNotCancelable) {
		t.Fatalf("second cancel: %v does not wrap jobs.ErrNotCancelable", err)
	}
}

// REL-10: recovery honours max_attempts, so a job that kills its worker every
// time is dead-lettered instead of looping forever.
func TestJobQueueRecoveryRespectsMaxAttempts(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	queue := NewJobQueue(store)

	id := enqueueTestJob(t, queue, "default", 2)

	task, err := queue.Claim(ctx, []string{"default"}, time.Minute)
	if err != nil || task == nil || task.Attempts != 1 {
		t.Fatalf("claim 1 = %+v, %v", task, err)
	}
	expireLease(t, store, id)

	task, err = queue.Claim(ctx, []string{"default"}, time.Minute)
	if err != nil || task == nil || task.Attempts != 2 {
		t.Fatalf("claim 2 = %+v, %v (recovery should re-queue at attempt 2)", task, err)
	}
	expireLease(t, store, id)

	// The budget is spent: the next claim must not hand the job out again.
	next, err := queue.Claim(ctx, []string{"default"}, time.Minute)
	if err != nil {
		t.Fatalf("claim 3: %v", err)
	}
	if next != nil && next.ID == id {
		t.Fatalf("poison-pill job was claimed again at attempt %d with max_attempts=2", next.Attempts)
	}
	got := readJob(t, store, id)
	if got.State != "failed" {
		t.Fatalf("state = %s, want failed (dead-lettered)", got.State)
	}
	if got.ErrorCode == nil || *got.ErrorCode != "lease_expired" {
		t.Fatalf("error code = %v, want lease_expired", got.ErrorCode)
	}
	if got.LeaseToken != nil {
		t.Fatal("dead-lettered job still holds a lease token")
	}
}

// REL-09: recovery is bounded, ordered and non-blocking. Concurrent claims must
// be disjoint, must not deadlock, and must not wait on rows in another queue.
func TestJobQueueClaimContention(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	queue := NewJobQueue(store)

	const nJobs, nWorkers = 40, 8
	for i := 0; i < nJobs; i++ {
		enqueueTestJob(t, queue, "default", 1)
	}

	var mu sync.Mutex
	claimed := map[string]int{}
	var duplicates, deadlocks atomic.Int32
	var wg sync.WaitGroup
	for w := 0; w < nWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				task, err := queue.Claim(ctx, []string{"default"}, time.Minute)
				if err != nil {
					var pgErr *pgconn.PgError
					if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
						deadlocks.Add(1)
					}
					t.Errorf("Claim: %v", err)
					return
				}
				if task == nil {
					return
				}
				mu.Lock()
				claimed[task.ID]++
				if claimed[task.ID] > 1 {
					duplicates.Add(1)
				}
				mu.Unlock()
				if err := queue.Complete(ctx, task.ID, task.LeaseToken); err != nil {
					t.Errorf("Complete: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	if deadlocks.Load() != 0 {
		t.Fatalf("%d deadlocks (40P01) during concurrent claims", deadlocks.Load())
	}
	if duplicates.Load() != 0 {
		t.Fatalf("%d duplicate claims out of %d jobs", duplicates.Load(), nJobs)
	}
	if len(claimed) != nJobs {
		t.Fatalf("claimed %d of %d jobs", len(claimed), nJobs)
	}
}

// REL-09: a lock on an expired row in one queue must not delay claims for
// another queue. Before the fix, Claim's unbounded recovery UPDATE blocked on it.
func TestJobQueueClaimDoesNotWaitOnOtherQueues(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	queue := NewJobQueue(store)

	importID := enqueueTestJob(t, queue, "import", 3)
	enqueueTestJob(t, queue, "report", 3)
	if _, err := store.pool.Exec(ctx, `
		UPDATE jobs SET state='running', lease_token=gen_random_uuid(),
			lease_expires_at = now() - interval '1 minute' WHERE id = $1::uuid`, importID); err != nil {
		t.Fatalf("seed expired lease: %v", err)
	}

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM jobs WHERE id = $1::uuid FOR UPDATE`, importID).Scan(&locked); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("lock expired row: %v", err)
	}
	hold := 1200 * time.Millisecond
	go func() {
		time.Sleep(hold)
		_ = tx.Commit(ctx)
	}()

	start := time.Now()
	task, err := queue.Claim(ctx, []string{"report"}, time.Minute)
	elapsed := time.Since(start)
	if err != nil || task == nil {
		t.Fatalf("Claim(report) = %+v, %v", task, err)
	}
	if elapsed > hold/2 {
		t.Fatalf("Claim(report) waited %s on a locked row in the import queue (held %s); "+
			"recovery is not using SKIP LOCKED", elapsed, hold)
	}
}

// Recovery must persist even when the claim finds nothing. Before the fix it ran
// inside the claim transaction and was rolled back on an empty queue, so expired
// leases were only ever recovered by a worker that happened to find new work.
func TestJobQueueRecoveryPersistsWithoutClaimableWork(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	queue := NewJobQueue(store)

	// An expired job in a queue this worker does not serve...
	id := enqueueTestJob(t, queue, "import", 3)
	if _, err := store.pool.Exec(ctx, `
		UPDATE jobs SET state='running', lease_token=gen_random_uuid(),
			lease_expires_at = now() - interval '1 minute' WHERE id = $1::uuid`, id); err != nil {
		t.Fatalf("seed expired lease: %v", err)
	}
	// ...and a claim on a queue with nothing in it.
	task, err := queue.Claim(ctx, []string{"report"}, time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if task != nil {
		t.Fatalf("claimed %+v from an empty queue", task)
	}
	if got := readJob(t, store, id); got.State != "queued" {
		t.Fatalf("state = %s, want queued: recovery was rolled back with the empty claim", got.State)
	}
	if got := readJob(t, store, id); got.LeaseToken != nil {
		t.Fatal("recovered job still holds a lease token")
	}
}

// REL-09 (second half): a large backlog of expired leases must not make
// concurrent claims fail.
func TestJobQueueRecoveryBounded(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	queue := NewJobQueue(store)

	if _, err := store.pool.Exec(ctx, `
		INSERT INTO jobs (module, tool, kind, queue, payload, subject_hash, max_attempts,
		                  state, lease_token, lease_expires_at)
		SELECT 'platform', 'platform:noop', 'platform:noop', 'default', '{}'::jsonb,
		       decode(repeat('00',32),'hex'), 3, 'running', gen_random_uuid(),
		       now() - (g || ' seconds')::interval
		FROM generate_series(1, 500) g`); err != nil {
		t.Fatalf("seed expired leases: %v", err)
	}
	enqueueTestJob(t, queue, "default", 1)

	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := queue.Claim(ctx, []string{"default"}, time.Minute); err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
					t.Errorf("deadlock during recovery: %v", err)
				}
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("%d of 4 concurrent claims failed over a 500-row expired backlog", failures.Load())
	}
	// The backlog is drained in bounded passes, not in one unbounded statement.
	var remaining int
	if err := store.pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE state='running' AND lease_expires_at < now()`).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining >= 500 {
		t.Fatalf("bounded recovery made no progress: %d of 500 expired rows still pending", remaining)
	}
	t.Logf("expired rows still waiting for a bounded recovery pass: %d of 500 (LIMIT %d per pass)",
		remaining, recoveryLimit)
}

// REL-13: the idempotent-enqueue conflict target and lookup are the unique index
// columns, so a repeated key returns the original job and never inserts twice.
func TestJobQueueIdempotentEnqueueMatchesIndex(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	queue := NewJobQueue(store)

	task := jobs.Task{
		Kind: jobs.TypeMagentoImport, Queue: jobs.QueueImport,
		InstanceID: "store-idem-us", IdempotencyKey: "batch-1", MaxAttempts: 1,
	}
	first, err := queue.Enqueue(ctx, task)
	if err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	second, err := queue.Enqueue(ctx, task)
	if err != nil {
		t.Fatalf("repeat enqueue: %v", err)
	}
	if first != second {
		t.Fatalf("repeat enqueue returned %q then %q", first, second)
	}
	var rows int
	if err := store.pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE idempotency_key = 'batch-1'`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Fatalf("%d rows for one idempotency key, want 1", rows)
	}

	// The same key in a different instance is a different job: the lookup must not
	// leak a job id across instances.
	other := task
	other.InstanceID = "store-idem-eu"
	third, err := queue.Enqueue(ctx, other)
	if err != nil {
		t.Fatalf("other-instance enqueue: %v", err)
	}
	if third == first {
		t.Fatal("a different instance reused another instance's job id")
	}

	// A platform-scoped job with the same key is a different scope too.
	platform := jobs.Task{Kind: jobs.TypeNoop, Queue: "default", IdempotencyKey: "batch-1", MaxAttempts: 1}
	fourth, err := queue.Enqueue(ctx, platform)
	if err != nil {
		t.Fatalf("platform enqueue: %v", err)
	}
	if fourth == first || fourth == third {
		t.Fatal("a platform job reused an instance-scoped job id")
	}
	fifth, err := queue.Enqueue(ctx, platform)
	if err != nil {
		t.Fatalf("platform repeat enqueue: %v", err)
	}
	if fifth != fourth {
		t.Fatalf("platform repeat enqueue returned %q then %q", fourth, fifth)
	}
}

// REL-17: retry delay grows with the attempt number and is capped.
func TestJobQueueRetryBackoffGrows(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	queue := NewJobQueue(store)

	id := enqueueTestJob(t, queue, "default", 5)
	delays := make([]time.Duration, 0, 2)
	for attempt := 0; attempt < 2; attempt++ {
		task, err := queue.Claim(ctx, []string{"default"}, time.Minute)
		if err != nil || task == nil || task.ID != id {
			t.Fatalf("claim %d = %+v, %v", attempt+1, task, err)
		}
		before := time.Now()
		if err := queue.Fail(ctx, id, task.LeaseToken, "handler_error", true); err != nil {
			t.Fatalf("Fail: %v", err)
		}
		got := readJob(t, store, id)
		if got.State != "queued" {
			t.Fatalf("state = %s, want queued", got.State)
		}
		delays = append(delays, got.ScheduledAt.Sub(before))
		// Make the retry immediately claimable without touching the backoff we
		// just measured.
		if _, err := store.pool.Exec(ctx,
			`UPDATE jobs SET scheduled_at = now() WHERE id = $1::uuid`, id); err != nil {
			t.Fatalf("advance schedule: %v", err)
		}
	}
	// base 5s, jitter 0.75..1.25 -> attempt 1 in [3.5s, 6.5s], attempt 2 in [7s, 13s].
	if delays[0] < 3500*time.Millisecond || delays[0] > 6500*time.Millisecond {
		t.Fatalf("attempt 1 backoff = %s, want 3.5s..6.5s", delays[0])
	}
	if delays[1] < 7*time.Second || delays[1] > 13*time.Second {
		t.Fatalf("attempt 2 backoff = %s, want 7s..13s (exponential growth)", delays[1])
	}
	if delays[1] <= delays[0] {
		t.Fatalf("backoff did not grow: %s then %s", delays[0], delays[1])
	}
}

// REL-18: the instance registration and the job insert are one transaction, so a
// failed insert leaves no orphan registry row.
func TestJobQueueEnqueueIsTransactional(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	queue := NewJobQueue(store)

	const instance = "store-txn-probe"
	if _, err := store.pool.Exec(ctx, `DELETE FROM datasource_instances WHERE id = $1`, instance); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	// Invalid JSON cannot be stored in a jsonb column, so the insert fails after
	// the instance row would have been written.
	_, err := queue.Enqueue(ctx, jobs.Task{
		Kind: jobs.TypeMagentoImport, Queue: jobs.QueueImport, InstanceID: instance,
		Payload: []byte(`{"broken":`),
	})
	if err == nil {
		t.Fatal("enqueue with an invalid payload succeeded")
	}
	var instances int
	if err := store.pool.QueryRow(ctx,
		`SELECT count(*) FROM datasource_instances WHERE id = $1`, instance).Scan(&instances); err != nil {
		t.Fatalf("count instances: %v", err)
	}
	if instances != 0 {
		t.Fatalf("failed enqueue left %d orphan datasource_instances row(s)", instances)
	}
}

// TestJobQueueMigrationLeaseTokenInvariant asserts the schema keeps itself
// honest: a running row must carry a token and a terminal row must not.
func TestJobQueueLeaseTokenInvariant(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	id := enqueueTestJob(t, NewJobQueue(store), "default", 2)

	_, err := store.pool.Exec(ctx,
		`UPDATE jobs SET state='running', lease_token=NULL WHERE id = $1::uuid`, id)
	if err == nil {
		t.Fatal("a running job with no lease token was accepted; the fencing invariant is not enforced")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "jobs_lease_token_chk" {
		t.Fatalf("unexpected error %v (want a jobs_lease_token_chk violation)", err)
	}
}

// TestJobQueueNoOrphanFencingTokens is a guard against the recovery statement
// forgetting to clear the token.
func TestJobQueueNoOrphanFencingTokens(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	queue := NewJobQueue(store)
	id := enqueueTestJob(t, queue, "default", 3)

	if _, err := queue.Claim(ctx, []string{"default"}, time.Minute); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	expireLease(t, store, id)
	if _, err := queue.Claim(ctx, []string{"default"}, time.Minute); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	var stale int
	if err := store.pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		 WHERE state NOT IN ('running','canceling') AND lease_token IS NOT NULL`).Scan(&stale); err != nil {
		t.Fatalf("count: %v", err)
	}
	if stale != 0 {
		t.Fatalf("%d non-running rows still hold a fencing token", stale)
	}
}

// TestJobQueueFailNeverRequeuesBeyondMaxAttempts keeps the worker's retry
// decision and the SQL transition in agreement.
func TestJobQueueFailNeverRequeuesBeyondMaxAttempts(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	queue := NewJobQueue(store)
	id := enqueueTestJob(t, queue, "default", 1)

	task, err := queue.Claim(ctx, []string{"default"}, time.Minute)
	if err != nil || task == nil {
		t.Fatalf("Claim = %+v, %v", task, err)
	}
	// A caller that asks for a retry it has no budget for must still terminate.
	if err := queue.Fail(ctx, id, task.LeaseToken, "handler_error", true); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	got := readJob(t, store, id)
	if got.State != "failed" {
		t.Fatalf("state = %s, want failed: attempt_count %d already reached max_attempts", got.State, got.Attempts)
	}
}

// TestJobQueueQueuesAreIndependent makes sure a claim only sees its own queues.
func TestJobQueueQueuesAreIndependent(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	queue := NewJobQueue(store)
	id := enqueueTestJob(t, queue, "report", 1)

	if task, err := queue.Claim(ctx, []string{"critical"}, time.Minute); err != nil || task != nil {
		t.Fatalf("claim of an unrelated queue = %+v, %v; want nil", task, err)
	}
	task, err := queue.Claim(ctx, []string{"report"}, time.Minute)
	if err != nil || task == nil || task.ID != id {
		t.Fatalf("claim of the report queue = %+v, %v", task, err)
	}
}

// TestJobQueueCancelThenRecoverResolvesToCanceled covers the crash-during-cancel
// path: a worker dies after Cancel moved the row to 'canceling'.
func TestJobQueueCancelThenRecoverResolvesToCanceled(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	queue := NewJobQueue(store)

	id := enqueueTestJob(t, queue, "default", 3)
	task, err := queue.Claim(ctx, []string{"default"}, time.Minute)
	if err != nil || task == nil {
		t.Fatalf("Claim = %+v, %v", task, err)
	}
	if err := queue.Cancel(ctx, id, "", "system"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	expireLease(t, store, id)

	next, err := queue.Claim(ctx, []string{"default"}, time.Minute)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if next != nil && next.ID == id {
		t.Fatal("a canceled job was handed out again after its worker died")
	}
	got := readJob(t, store, id)
	if got.State != "canceled" {
		t.Fatalf("state = %s, want canceled", got.State)
	}
}

// TestJobQueueTransitionSQLIsGuarded is a static guard: every state-changing
// statement must name the states it may start from. It fails loudly if someone
// adds an unguarded UPDATE later.
func TestJobQueueTransitionSQLIsGuarded(t *testing.T) {
	for name, stmt := range map[string]string{
		"complete": completeSQL,
		"fail":     failSQL,
		"extend":   extendSQL,
		"cancel":   cancelSQL,
		"result":   setResultSQL,
	} {
		lower := strings.ToLower(stmt)
		if !strings.Contains(lower, "where") {
			t.Fatalf("%s has no WHERE clause", name)
		}
		if !strings.Contains(lower, "lease_token = $2::uuid") && name != "cancel" {
			t.Fatalf("%s is not fenced on the lease token", name)
		}
		if !strings.Contains(lower, "state in") {
			t.Fatalf("%s does not restrict the source state", name)
		}
	}
}

// REL-20: Migrate must run on the connection that holds the advisory lock. With a
// one-connection pool, checking out a second connection for the migration
// transaction deadlocks against itself.
func TestMigrateWithSingleConnection(t *testing.T) {
	url := testSchemaDSN(t, "test_storage")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := Open(ctx, Config{URL: url, MaxConns: 1})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate with MaxConns=1: %v", err)
	}
}

// PERF-14: the jobs table is both the queue and the execution record, so
// without pruning it grows forever. Only terminal history may be removed: a
// queued or running job is live work and deleting it would silently drop a task.
func TestPruneJobsRemovesOnlyTerminalHistory(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	queue := NewJobQueue(store)

	done := enqueueTestJob(t, queue, "default", 1)
	claimed, err := queue.Claim(ctx, []string{"default"}, time.Minute)
	if err != nil || claimed == nil || claimed.ID != done {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if err := queue.Complete(ctx, done, claimed.LeaseToken); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	failed := enqueueTestJob(t, queue, "default", 1)
	claimed, err = queue.Claim(ctx, []string{"default"}, time.Minute)
	if err != nil || claimed == nil || claimed.ID != failed {
		t.Fatalf("second claim = %+v, %v", claimed, err)
	}
	if err := queue.Fail(ctx, failed, claimed.LeaseToken, "handler_error", false); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	live := enqueueTestJob(t, queue, "default", 1)

	// A cutoff just after "now" captures every finished row and no live one.
	removed, err := store.PruneJobs(ctx, time.Now().Add(time.Second))
	if err != nil {
		t.Fatalf("PruneJobs: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2 (one succeeded, one failed)", removed)
	}

	var liveCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE id = $1::uuid`, live).Scan(&liveCount); err != nil {
		t.Fatalf("count live job: %v", err)
	}
	if liveCount != 1 {
		t.Fatalf("queued job was pruned: count = %d, want 1", liveCount)
	}

	var terminalCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE state IN ('succeeded','failed')`).Scan(&terminalCount); err != nil {
		t.Fatalf("count terminal jobs: %v", err)
	}
	if terminalCount != 0 {
		t.Fatalf("terminal jobs survived pruning: %d", terminalCount)
	}
}

// A cutoff in the past must not delete recent history.
func TestPruneJobsKeepsRecentHistory(t *testing.T) {
	store := testStore(t)
	resetJobs(t, store)
	ctx := context.Background()
	queue := NewJobQueue(store)

	id := enqueueTestJob(t, queue, "default", 1)
	claimed, err := queue.Claim(ctx, []string{"default"}, time.Minute)
	if err != nil || claimed == nil || claimed.ID != id {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if err := queue.Complete(ctx, id, claimed.LeaseToken); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	removed, err := store.PruneJobs(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("PruneJobs: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 for a cutoff before the job finished", removed)
	}
}
