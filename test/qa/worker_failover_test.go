//go:build qa

package qa

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("qa: condition not met within %s", timeout)
}

// drainQueue removes queued work from a dedicated queue so a previous failed run
// cannot satisfy (or steal) this run's claims.
func drainQueue(t *testing.T, queue *storage.JobQueue, queueName string) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		task, err := queue.Claim(ctx, []string{queueName}, 10*time.Second)
		if err != nil {
			t.Fatalf("qa: drain claim: %v", err)
		}
		if task == nil {
			return
		}
		if err := queue.Complete(ctx, task.ID, task.LeaseToken); err != nil {
			t.Fatalf("qa: drain complete: %v", err)
		}
	}
	t.Fatal("qa: queue did not drain")
}

func workerOptions(queueName string) jobs.WorkerOptions {
	return jobs.WorkerOptions{
		Concurrency:  1,
		PollInterval: 5 * time.Millisecond,
		TaskTimeout:  5 * time.Second,
		Lease:        10 * time.Second,
		Queues:       []string{queueName},
	}
}

// TestForcedWorkerExitRecoversTaskExactlyOnce guards REL-01 / REL-03 / REL-08 /
// REL-10: a worker that dies holding a lease does not duplicate the work. The
// lease is abandoned (the forced exit), a second worker recovers the task and
// runs the handler exactly once, the attempt count increments, the stale token's
// completion is rejected, and the succeeded task is never re-executed.
func TestForcedWorkerExitRecoversTaskExactlyOnce(t *testing.T) {
	queue := storage.NewJobQueue(mainStore)
	subject := "qa-worker"
	queueName := "report"
	ctx := context.Background()
	drainQueue(t, queue, queueName)

	// platform:* jobs are not module-scoped, so they carry no instance id.
	id, err := queue.Enqueue(ctx, jobs.Task{
		Kind: jobs.TypeNoop, Queue: queueName, Subject: subject,
		MaxAttempts: 3, Payload: []byte(`{"message":"qa"}`),
	})
	if err != nil {
		t.Fatalf("qa: enqueue: %v", err)
	}

	// Worker A claims the task and is then killed: it never completes or fails it.
	claimed, err := queue.Claim(ctx, []string{queueName}, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("qa: claim as worker A: %v", err)
	}
	if claimed == nil || claimed.ID != id {
		t.Fatalf("qa: worker A claimed %+v, want task %s", claimed, id)
	}
	staleToken := claimed.LeaseToken
	time.Sleep(300 * time.Millisecond) // the lease lapses

	var executions atomic.Int32
	worker := jobs.NewWorker(queue, workerOptions(queueName))
	worker.Handle(jobs.TypeNoop, func(context.Context, jobs.Task) error {
		executions.Add(1)
		return nil
	})
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = worker.Run(runCtx)
		close(done)
	}()
	waitFor(t, 10*time.Second, func() bool {
		record, getErr := queue.GetJob(ctx, id, "", subject)
		return getErr == nil && record != nil && record.State == "succeeded"
	})
	stop()
	<-done

	if got := executions.Load(); got != 1 {
		t.Fatalf("qa: the recovered task ran %d times, want exactly 1", got)
	}
	record, err := queue.GetJob(ctx, id, "", subject)
	if err != nil {
		t.Fatalf("qa: get job: %v", err)
	}
	if record.Attempts != 2 {
		t.Fatalf("qa: attempts = %d, want 2 (one abandoned claim, one recovery)", record.Attempts)
	}
	if err := queue.Complete(ctx, id, staleToken); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("qa: the stale worker's completion was accepted: %v", err)
	}
	time.Sleep(250 * time.Millisecond)
	if got := executions.Load(); got != 1 {
		t.Fatalf("qa: the succeeded task was executed again (%d runs)", got)
	}
}

// TestTwoWorkersClaimDisjointJobs guards REL-01 / REL-09: two workers claiming
// from the same queue under contention get disjoint tasks; every task runs
// exactly once.
func TestTwoWorkersClaimDisjointJobs(t *testing.T) {
	queue := storage.NewJobQueue(mainStore)
	subject := "qa-worker"
	queueName := "report"
	ctx := context.Background()
	drainQueue(t, queue, queueName)

	const total = 20
	enqueued := make(map[string]bool, total)
	for i := 0; i < total; i++ {
		id, err := queue.Enqueue(ctx, jobs.Task{
			Kind: jobs.TypeNoop, Queue: queueName, Subject: subject,
			MaxAttempts: 1, Payload: []byte(`{"message":"qa"}`),
		})
		if err != nil {
			t.Fatalf("qa: enqueue %d: %v", i, err)
		}
		enqueued[id] = true
	}

	var mu sync.Mutex
	seen := make(map[string]int, total)
	handler := func(_ context.Context, task jobs.Task) error {
		mu.Lock()
		seen[task.ID]++
		mu.Unlock()
		return nil
	}
	newWorker := func() *jobs.Worker {
		w := jobs.NewWorker(queue, workerOptions(queueName))
		w.Handle(jobs.TypeNoop, handler)
		return w
	}

	runCtx, stop := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = newWorker().Run(runCtx)
		}()
	}
	waitFor(t, 15*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == total
	})
	stop()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != total {
		t.Fatalf("qa: %d of %d tasks executed", len(seen), total)
	}
	for id, runs := range seen {
		if !enqueued[id] {
			t.Fatalf("qa: an unknown task id was executed: %s", id)
		}
		if runs != 1 {
			t.Fatalf("qa: task %s executed %d times; claims were not disjoint", id, runs)
		}
	}
}
