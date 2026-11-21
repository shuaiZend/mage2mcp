// SPDX-License-Identifier: Apache-2.0

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

type fakeQueue struct {
	mu        sync.Mutex
	tasks     []Task
	completed []string
	failed    []struct {
		id    string
		code  string
		retry bool
	}
	extended  int
	leaseLost bool
	cancel    bool
}

func (f *fakeQueue) Enqueue(_ context.Context, t Task) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t.ID = "job-" + string(rune('a'+len(f.tasks)))
	f.tasks = append(f.tasks, t)
	return t.ID, nil
}

func (f *fakeQueue) Claim(_ context.Context, _ []string, _ time.Duration) (*Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tasks) == 0 {
		return nil, nil
	}
	task := f.tasks[0]
	f.tasks = f.tasks[1:]
	task.LeaseToken = "lease-" + task.ID
	return &task, nil
}

func (f *fakeQueue) Complete(ctx context.Context, id, leaseToken string) error {
	// Model pgx: a write on a cancelled context fails, and a stale token matches
	// no row. Both are the exact failure modes the worker must not mistake for
	// success.
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.leaseLost || leaseToken != "lease-"+id {
		return ErrLeaseLost
	}
	f.completed = append(f.completed, id)
	return nil
}

func (f *fakeQueue) Fail(ctx context.Context, id, leaseToken, code string, retry bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.leaseLost || leaseToken != "lease-"+id {
		return ErrLeaseLost
	}
	f.failed = append(f.failed, struct {
		id    string
		code  string
		retry bool
	}{id, code, retry})
	return nil
}

func (f *fakeQueue) Extend(ctx context.Context, id, leaseToken string, lease time.Duration) (LeaseStatus, error) {
	if err := ctx.Err(); err != nil {
		return LeaseStatus{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.leaseLost || leaseToken != "lease-"+id {
		return LeaseStatus{}, ErrLeaseLost
	}
	f.extended++
	return LeaseStatus{ExpiresAt: time.Now().Add(lease), CancelRequested: f.cancel}, nil
}

func (f *fakeQueue) Cancel(context.Context, string, string, string) error { return nil }

func TestWorkerCompletesTask(t *testing.T) {
	queue := &fakeQueue{}
	queue.tasks = []Task{{ID: "j1", Kind: TypeNoop, Payload: json.RawMessage(`{"message":"hi"}`), Attempts: 1, MaxAttempts: 1}}
	ran := make(chan struct{}, 1)
	worker := NewWorker(queue, WorkerOptions{Concurrency: 1, PollInterval: 5 * time.Millisecond})
	worker.Handle(TypeNoop, func(_ context.Context, task Task) error {
		defer func() { ran <- struct{}{} }()
		var p NoopPayload
		if err := json.Unmarshal(task.Payload, &p); err != nil {
			return err
		}
		if p.Message != "hi" {
			t.Fatalf("message = %q", p.Message)
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case <-ran:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not run")
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("worker run: %v", err)
	}
	if len(queue.completed) != 1 || queue.completed[0] != "j1" {
		t.Fatalf("completed = %v", queue.completed)
	}
}

func TestWorkerFailsWithoutHandler(t *testing.T) {
	queue := &fakeQueue{}
	queue.tasks = []Task{{ID: "j1", Kind: "unknown", Attempts: 1, MaxAttempts: 1}}
	worker := NewWorker(queue, WorkerOptions{Concurrency: 1, PollInterval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = worker.Run(ctx) }()
	deadline := time.After(3 * time.Second)
	for {
		queue.mu.Lock()
		n := len(queue.failed)
		queue.mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("task was not failed")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.failed[0].code != "unsupported_task" || queue.failed[0].retry {
		t.Fatalf("failure = %+v", queue.failed[0])
	}
}

// A retryable failure on an idempotent task with attempts remaining is retried.
// (ARCH-02 narrowed the old "attempts remain" rule; the full matrix is covered
// by TestWorkerRetryGate.)
func TestWorkerRetriesWhenAttemptsRemain(t *testing.T) {
	queue := &fakeQueue{}
	queue.tasks = []Task{{ID: "j1", Kind: TypeNoop, Attempts: 1, MaxAttempts: 2}}
	worker := NewWorker(queue, WorkerOptions{Concurrency: 1, PollInterval: 5 * time.Millisecond})
	worker.HandleIdempotent(TypeNoop, func(context.Context, Task) error {
		e := errs.New(errs.CodeUpstreamUnavailable, "boom")
		e.Retryable = true
		return e
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = worker.Run(ctx) }()
	deadline := time.After(3 * time.Second)
	for {
		queue.mu.Lock()
		n := len(queue.failed)
		queue.mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("task was not failed")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if !queue.failed[0].retry {
		t.Fatalf("retry = false, want true: %+v", queue.failed[0])
	}
}

func TestEnqueueNoop(t *testing.T) {
	queue := &fakeQueue{}
	id, err := EnqueueNoop(context.Background(), queue, "hello")
	if err != nil {
		t.Fatal(err)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if id == "" || len(queue.tasks) != 1 || queue.tasks[0].Kind != TypeNoop {
		t.Fatalf("unexpected enqueue: %q %+v", id, queue.tasks)
	}
}

func TestWorkerLeaseOutlivesTaskTimeout(t *testing.T) {
	worker := NewWorker(&fakeQueue{}, WorkerOptions{})
	opts := worker.Options()
	if opts.Lease <= opts.TaskTimeout {
		t.Fatalf("lease %s must exceed task timeout %s, otherwise a healthy handler is reclaimed and run twice",
			opts.Lease, opts.TaskTimeout)
	}
	if opts.HeartbeatInterval <= 0 || opts.HeartbeatInterval >= opts.Lease {
		t.Fatalf("heartbeat interval %s must be positive and below the lease %s", opts.HeartbeatInterval, opts.Lease)
	}
	// An explicitly too-short lease must be corrected, not honoured.
	short := NewWorker(&fakeQueue{}, WorkerOptions{Lease: time.Second, TaskTimeout: time.Minute}).Options()
	if short.Lease <= short.TaskTimeout {
		t.Fatalf("explicit short lease was not corrected: %s <= %s", short.Lease, short.TaskTimeout)
	}
}

// A handler that outlives one lease period must be renewed, not reclaimed: it
// runs exactly once and the lease is extended while it runs.
func TestWorkerRenewsLeaseWhileHandlerRuns(t *testing.T) {
	queue := &fakeQueue{}
	queue.tasks = []Task{{ID: "j1", Kind: TypeNoop, Attempts: 1, MaxAttempts: 1}}
	worker := NewWorker(queue, WorkerOptions{
		Concurrency: 1, PollInterval: 5 * time.Millisecond,
		TaskTimeout: 200 * time.Millisecond, Lease: 300 * time.Millisecond, HeartbeatInterval: 20 * time.Millisecond,
	})
	// The invariant the renewal exists to preserve: the lease outlives the task
	// deadline, so a healthy handler can never be reclaimed mid-flight.
	if opts := worker.Options(); opts.Lease <= opts.TaskTimeout {
		t.Fatalf("effective lease %s must exceed the task timeout %s", opts.Lease, opts.TaskTimeout)
	}
	ran := make(chan struct{}, 1)
	var runs atomic.Int32
	worker.Handle(TypeNoop, func(context.Context, Task) error {
		runs.Add(1)
		select {
		case ran <- struct{}{}:
		default:
		}
		time.Sleep(120 * time.Millisecond) // several heartbeats
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = worker.Run(ctx) }()
	select {
	case <-ran:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not run")
	}
	time.Sleep(400 * time.Millisecond)
	queue.mu.Lock()
	extends, completed := queue.extended, len(queue.completed)
	queue.mu.Unlock()
	if extends < 2 {
		t.Fatalf("lease renewed %d times for a handler spanning several heartbeats; want >= 2", extends)
	}
	if completed != 1 {
		t.Fatalf("completed = %d, want exactly 1", completed)
	}
	if n := runs.Load(); n != 1 {
		t.Fatalf("handler ran %d times, want exactly 1", n)
	}
}

// A cancel request reaches the running handler through its context, and the task
// ends in 'canceled' rather than being retried.
func TestWorkerCancelRequestStopsHandler(t *testing.T) {
	queue := &fakeQueue{cancel: true}
	queue.tasks = []Task{{ID: "j1", Kind: TypeNoop, Attempts: 1, MaxAttempts: 3}}
	worker := NewWorker(queue, WorkerOptions{
		Concurrency: 1, PollInterval: 5 * time.Millisecond,
		TaskTimeout: 2 * time.Second, Lease: time.Minute, HeartbeatInterval: 10 * time.Millisecond,
	})
	observed := make(chan error, 1)
	worker.Handle(TypeNoop, func(ctx context.Context, _ Task) error {
		select {
		case <-ctx.Done():
			observed <- ctx.Err()
			return ctx.Err()
		case <-time.After(3 * time.Second):
			observed <- nil
			return nil
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = worker.Run(ctx) }()
	select {
	case err := <-observed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handler context error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel request never reached the handler")
	}
	deadline := time.After(3 * time.Second)
	for {
		queue.mu.Lock()
		n := len(queue.failed)
		var failure struct {
			id    string
			code  string
			retry bool
		}
		if n > 0 {
			failure = queue.failed[0]
		}
		queue.mu.Unlock()
		if n > 0 {
			if failure.code != "canceled" || failure.retry {
				t.Fatalf("failure = %+v, want code=canceled retry=false", failure)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("canceled task was not recorded")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// Once the lease is lost another attempt owns the row, so the result must be
// discarded: no Complete and no Fail may be written.
func TestWorkerLeaseLostDiscardsResult(t *testing.T) {
	queue := &fakeQueue{}
	queue.tasks = []Task{{ID: "j1", Kind: TypeNoop, Attempts: 1, MaxAttempts: 3}}
	worker := NewWorker(queue, WorkerOptions{
		Concurrency: 1, PollInterval: 5 * time.Millisecond,
		TaskTimeout: 2 * time.Second, Lease: time.Minute, HeartbeatInterval: 10 * time.Millisecond,
	})
	worker.Handle(TypeNoop, func(ctx context.Context, _ Task) error {
		queue.mu.Lock()
		queue.leaseLost = true
		queue.mu.Unlock()
		<-ctx.Done() // the heartbeat notices the loss and stops us
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = worker.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.completed) != 0 || len(queue.failed) != 0 {
		t.Fatalf("writes after lease loss: completed=%v failed=%v; want none",
			queue.completed, queue.failed)
	}
}

// Graceful shutdown must not abandon a claim: the in-flight handler is allowed to
// finish and its outcome is recorded on a context shutdown cannot cancel.
func TestWorkerShutdownRecordsCompletion(t *testing.T) {
	queue := &fakeQueue{}
	queue.tasks = []Task{{ID: "j1", Kind: TypeNoop, Attempts: 1, MaxAttempts: 1}}
	worker := NewWorker(queue, WorkerOptions{Concurrency: 1, PollInterval: 5 * time.Millisecond, HeartbeatInterval: 10 * time.Millisecond})
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	worker.Handle(TypeNoop, func(context.Context, Task) error {
		once.Do(func() { close(started) })
		<-release
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	<-started
	cancel()       // SIGTERM arrives while the handler is in flight
	close(release) // the handler finishes its work
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.completed) != 1 {
		t.Fatalf("completed = %v, want the in-flight task's completion recorded", queue.completed)
	}
}

// The same guarantee on the failure path: a shutdown must not lose the terminal
// write, or the task is silently re-executed after its lease expires.
func TestWorkerShutdownRecordsFailure(t *testing.T) {
	queue := &fakeQueue{}
	queue.tasks = []Task{{ID: "j1", Kind: TypeNoop, Attempts: 1, MaxAttempts: 2}}
	worker := NewWorker(queue, WorkerOptions{Concurrency: 1, PollInterval: 5 * time.Millisecond, HeartbeatInterval: 10 * time.Millisecond})
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	// A retryable, idempotent task: the shutdown failure must still be recorded
	// as retryable. (Retry eligibility itself is covered by TestWorkerRetryGate.)
	worker.HandleIdempotent(TypeNoop, func(context.Context, Task) error {
		once.Do(func() { close(started) })
		<-release
		e := errs.New(errs.CodeUpstreamUnavailable, "upstream went away")
		e.Retryable = true
		return e
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	<-started
	cancel()
	close(release)
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.failed) != 1 || !queue.failed[0].retry {
		t.Fatalf("failed = %+v, want one retryable failure recorded", queue.failed)
	}
}

// ARCH-02: the retry decision is attempts < max AND idempotent AND retryable.
func TestWorkerRetryGate(t *testing.T) {
	retryable := func() error {
		e := errs.New(errs.CodeUpstreamUnavailable, "upstream is unavailable")
		e.Retryable = true
		return e
	}
	ambiguous := func() error { return errs.New(errs.CodeOperationUnknown, "outcome unknown") }
	plain := func() error { return errors.New("boom") }
	// A 5xx after send: retryable by code, but the outcome is ambiguous.
	ambiguous5xx := func() error {
		e := errs.New(errs.CodeUpstreamUnavailable, "Magento is unavailable")
		e.Retryable = true
		return e.WithDetail("outcome", "unknown")
	}

	cases := []struct {
		name       string
		idempotent bool
		maxAttempt int
		err        func() error
		wantRetry  bool
	}{
		{"idempotent+retryable", true, 3, retryable, true},
		{"non-idempotent+retryable", false, 3, retryable, false},
		{"idempotent+plain error", true, 3, plain, false},
		{"idempotent+unknown outcome", true, 3, ambiguous, false},
		{"idempotent+ambiguous 5xx", true, 3, ambiguous5xx, false},
		{"idempotent+retryable, no budget", true, 1, retryable, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			queue := &fakeQueue{}
			queue.tasks = []Task{{ID: "j1", Kind: TypeNoop, Attempts: 1, MaxAttempts: tc.maxAttempt}}
			worker := NewWorker(queue, WorkerOptions{Concurrency: 1, PollInterval: 5 * time.Millisecond})
			handler := func(context.Context, Task) error { return tc.err() }
			if tc.idempotent {
				worker.HandleIdempotent(TypeNoop, handler)
			} else {
				worker.Handle(TypeNoop, handler)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = worker.Run(ctx) }()
			deadline := time.After(3 * time.Second)
			for {
				queue.mu.Lock()
				n := len(queue.failed)
				var got bool
				if n > 0 {
					got = queue.failed[0].retry
				}
				queue.mu.Unlock()
				if n > 0 {
					if got != tc.wantRetry {
						t.Fatalf("retry = %v, want %v", got, tc.wantRetry)
					}
					return
				}
				select {
				case <-deadline:
					t.Fatal("task was not failed")
				default:
					time.Sleep(5 * time.Millisecond)
				}
			}
		})
	}
}

// The reconciler enqueues platform:reconcile on the maintenance queue. A worker
// that does not poll that queue leaves every UNKNOWN operation unresolved, which
// is what happened before the default list included it.
func TestDefaultQueuesIncludeMaintenance(t *testing.T) {
	opts := WorkerOptions{}
	opts.defaults()
	found := false
	for _, q := range opts.Queues {
		if q == "maintenance" {
			found = true
		}
	}
	if !found {
		t.Fatalf("default queues = %v, want maintenance included", opts.Queues)
	}
}

// SEM-IMP-01: a partial import travels as a marker on the job result, because the
// queue state records execution and has no "partial".
func TestResultIsPartial(t *testing.T) {
	cases := []struct {
		result string
		want   bool
	}{
		{`{"total":2,"failed":1,"partial":true}`, true},
		// A report that recorded failures is partial even without the marker, so
		// jobs written before the marker existed are still reported honestly.
		{`{"total":2,"failed":1}`, true},
		{`{"total":2,"failed":0}`, false},
		{`{"total":2,"partial":false}`, false},
		{`{"total":2,"partial":"yes"}`, false},
		{``, false},
		{`not json`, false},
	}
	for _, tc := range cases {
		if got := ResultIsPartial(tc.result); got != tc.want {
			t.Fatalf("ResultIsPartial(%q) = %v, want %v", tc.result, got, tc.want)
		}
	}
}
