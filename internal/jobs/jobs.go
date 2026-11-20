// SPDX-License-Identifier: Apache-2.0

// Package jobs defines the asynchronous task queue contract and worker.
//
// Tasks that are too slow for a synchronous MCP call (imports, large reports,
// log ingestion) are persisted in PostgreSQL and executed by cmd/worker. There
// is no Redis dependency: the jobs table is the queue, and claiming uses
// FOR UPDATE SKIP LOCKED with a lease so crashed workers are recovered.
//
// Every claim is fenced: Claim returns a lease token, and every write that
// reports an outcome for the task must present that token. A worker whose lease
// expired and whose task was reclaimed therefore cannot record anything — its
// write affects zero rows and surfaces ErrLeaseLost instead of a silent success.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// Task types. A type is stable once released; handlers are registered by the
// worker and producers enqueue by name.
const (
	TypeNoop          = "platform:noop"
	TypeReindex       = "platform:reindex"
	TypeMagentoImport = "magento:import"
	// TypeReconcile drives the operation ledger's reconciler: it claims
	// operations whose upstream outcome is unknown and resolves them.
	TypeReconcile = "platform:reconcile"
)

// QueueImport is the queue name for asynchronous imports.
const QueueImport = "import"

// Sentinel errors returned by a Queue implementation.
//
// ErrLeaseLost means the attempt no longer owns the row: the lease expired and
// another worker reclaimed the task, or a terminal transition already happened.
// The worker must discard the result; it must never retry the write with the
// same token, and it must never treat the loss as success.
var ErrLeaseLost = errors.New("jobs: lease lost")

// ErrNotCancelable means the task exists but is in a state from which it cannot
// be canceled (already terminal), or does not exist at all. Implementations
// return a typed conflict/not-found error that wraps this sentinel.
var ErrNotCancelable = errors.New("jobs: task is not cancelable")

// JobRecord is a read-only view of a persisted job.
type JobRecord struct {
	ID string `json:"id"`
	// InstanceID is the tenant the job is attributed to. It is empty only for
	// platform-scoped jobs.
	InstanceID  string          `json:"instance_id,omitempty"`
	Kind        string          `json:"kind"`
	State       string          `json:"state"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"max_attempts"`
	ErrorCode   string          `json:"error_code,omitempty"`
	Result      string          `json:"result,omitempty"`
	Payload     json.RawMessage `json:"-"`
	CreatedAt   time.Time       `json:"created_at"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
	// CancelRequested is true while a running task has been asked to stop but has
	// not yet reached a terminal state.
	CancelRequested bool `json:"cancel_requested,omitempty"`
}

// NoopPayload is a liveness task used to verify the queue end to end.
type NoopPayload struct {
	Message string `json:"message"`
}

// Task is one claimed unit of work.
type Task struct {
	ID             string
	Kind           string
	Queue          string
	InstanceID     string
	IdempotencyKey string
	Payload        json.RawMessage
	Attempts       int
	MaxAttempts    int
	// Idempotent declares whether re-running this task is safe. It defaults to
	// false: a task that is not proven idempotent is never retried, because a
	// retry of a money mutation is a second charge.
	Idempotent bool
	// LeaseToken fences this claim. Every write that reports an outcome for the
	// task must present it. It is empty only for a task that was not claimed from
	// a queue (tests, in-memory fakes).
	LeaseToken string
	// Subject is the verified caller the job is attributed to. The queue hashes
	// it into subject_hash, which is the job's ownership key: a caller may read or
	// cancel only jobs carrying its own hash for the same instance. An empty
	// Subject is attributed to "system" and is reserved for tasks the gateway
	// enqueues on its own behalf.
	Subject string
}

// LeaseStatus is the outcome of a lease renewal. It doubles as the cancel poll:
// a renewal that reports CancelRequested asks the worker to stop the handler and
// end the task in 'canceled'.
type LeaseStatus struct {
	ExpiresAt       time.Time
	CancelRequested bool
}

// Queue persists and claims tasks. The PostgreSQL implementation lives in
// internal/storage; tests use an in-memory fake.
type Queue interface {
	Enqueue(ctx context.Context, t Task) (string, error)
	Claim(ctx context.Context, queues []string, lease time.Duration) (*Task, error)
	// Complete records success. It is a conditional transition: if the token does
	// not match the current claim, nothing changes and ErrLeaseLost is returned.
	Complete(ctx context.Context, id, leaseToken string) error
	// Fail records a failed attempt. With retry it returns the task to the queue
	// after a bounded exponential backoff; otherwise it is terminal. It never
	// moves a canceled task back to 'queued'.
	Fail(ctx context.Context, id, leaseToken, errorCode string, retry bool) error
	// Extend renews the lease and reports whether a cancel was requested.
	Extend(ctx context.Context, id, leaseToken string, lease time.Duration) (LeaseStatus, error)
	// Cancel asks a queued task to stop immediately and a running task to stop at
	// the next heartbeat. It returns a typed error when nothing transitioned.
	// Cancel stops a task only when it belongs to the given instance and
	// subject: a caller may never cancel another tenant's or another caller's
	// work. A mismatch is reported as not_found.
	Cancel(ctx context.Context, id, instanceID, subject string) error
}

// Handler executes one task. Returning an error marks the attempt failed; the
// worker retries only while attempts remain and the task's max_attempts allows.
// The context is canceled when a cancel is requested or the lease is lost.
type Handler func(ctx context.Context, t Task) error

// WorkerOptions configures the worker loop.
type WorkerOptions struct {
	Concurrency  int
	PollInterval time.Duration
	Lease        time.Duration
	TaskTimeout  time.Duration
	// HeartbeatInterval is how often a running task renews its lease. It must be
	// well below Lease, because a task whose lease lapses is executed twice.
	HeartbeatInterval time.Duration
	Queues            []string
	Logger            *slog.Logger
}

const (
	// terminalWriteTimeout bounds the Complete/Fail write so shutdown cannot hang
	// and a slow database cannot block a worker goroutine indefinitely.
	terminalWriteTimeout = 5 * time.Second
	// maxHeartbeatInterval caps how long a cancel request can take to reach a
	// handler, independently of how long the lease is.
	maxHeartbeatInterval = 15 * time.Second
	minHeartbeatInterval = 100 * time.Millisecond
)

func (o *WorkerOptions) defaults() {
	if o.Concurrency <= 0 {
		o.Concurrency = 4
	}
	if o.PollInterval <= 0 {
		o.PollInterval = time.Second
	}
	if o.TaskTimeout <= 0 {
		o.TaskTimeout = 5 * time.Minute
	}
	// The lease must outlive the task deadline even if the heartbeat is delayed or
	// fails for a while; otherwise a healthy worker's own task is reclaimed and
	// executed a second time.
	if o.Lease <= o.TaskTimeout {
		o.Lease = o.TaskTimeout + time.Minute
	}
	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = o.Lease / 3
		if o.HeartbeatInterval > maxHeartbeatInterval {
			o.HeartbeatInterval = maxHeartbeatInterval
		}
		if o.HeartbeatInterval < minHeartbeatInterval {
			o.HeartbeatInterval = minHeartbeatInterval
		}
	}
	if len(o.Queues) == 0 {
		o.Queues = []string{"critical", "default", "import", "report"}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// handlerEntry is one registered handler plus its retry contract.
type handlerEntry struct {
	fn         Handler
	idempotent bool
}

// Worker claims and executes tasks from a Queue.
type Worker struct {
	queue    Queue
	handlers map[string]handlerEntry
	opts     WorkerOptions
}

// NewWorker returns a worker with no handlers registered.
func NewWorker(queue Queue, opts WorkerOptions) *Worker {
	opts.defaults()
	return &Worker{queue: queue, handlers: make(map[string]handlerEntry), opts: opts}
}

// Handle registers a handler for a task kind. Registering twice replaces the
// previous handler, which is useful for tests. A handler registered this way is
// treated as NOT idempotent, so it is never retried automatically.
func (w *Worker) Handle(kind string, h Handler) *Worker {
	w.handlers[kind] = handlerEntry{fn: h}
	return w
}

// HandleIdempotent registers a handler whose task may be re-executed safely: a
// retryable failure is then retried while attempts remain.
func (w *Worker) HandleIdempotent(kind string, h Handler) *Worker {
	w.handlers[kind] = handlerEntry{fn: h, idempotent: true}
	return w
}

// Options returns the normalized options, for tests and diagnostics.
func (w *Worker) Options() WorkerOptions { return w.opts }

// Run claims and executes tasks until ctx is cancelled. Every goroutine drains
// its current task before returning, so shutdown does not abandon a claim: an
// in-flight handler is allowed to finish and record its outcome.
func (w *Worker) Run(ctx context.Context) error {
	if w.queue == nil {
		return fmt.Errorf("jobs: queue is required")
	}
	done := make(chan struct{}, w.opts.Concurrency)
	for i := 0; i < w.opts.Concurrency; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			w.loop(ctx)
		}()
	}
	for i := 0; i < w.opts.Concurrency; i++ {
		<-done
	}
	return ctx.Err()
}

func (w *Worker) loop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		task, err := w.queue.Claim(ctx, w.opts.Queues, w.opts.Lease)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.opts.Logger.Error("job claim failed", "err", err)
			sleep(ctx, w.opts.PollInterval)
			continue
		}
		if task == nil {
			sleep(ctx, jitter(w.opts.PollInterval))
			continue
		}
		w.execute(ctx, task)
	}
}

func (w *Worker) execute(ctx context.Context, task *Task) {
	entry, ok := w.handlers[task.Kind]
	handler := entry.fn
	if !ok {
		w.opts.Logger.Error("no handler for task kind", "kind", task.Kind, "job_id", task.ID)
		w.recordFailure(ctx, task, "unsupported_task", false, false)
		return
	}

	// The handler is bounded by TaskTimeout but not by shutdown: a deploy must
	// let an in-flight task reach its outcome instead of abandoning the claim and
	// having the work silently re-executed after the lease expires.
	taskCtx, cancelTask := context.WithTimeout(context.WithoutCancel(ctx), w.opts.TaskTimeout)
	defer cancelTask()

	var leaseLost, cancelRequested atomic.Bool
	heartbeatCtx, stopHeartbeat := context.WithCancel(context.WithoutCancel(ctx))
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		w.heartbeat(heartbeatCtx, task, cancelTask, &leaseLost, &cancelRequested)
	}()

	err := handler(taskCtx, *task)
	stopHeartbeat()
	<-heartbeatDone
	cancelTask()

	if leaseLost.Load() {
		// Another worker owns this row. Its outcome is the truth; recording ours
		// would overwrite it. The work may have been performed twice, which is
		// exactly what the operation ledger exists to reconcile.
		w.opts.Logger.Warn("job lease lost while running; result discarded",
			"job_id", task.ID, "kind", task.Kind, "attempt", task.Attempts, "err", err)
		return
	}
	if err == nil {
		if cerr := w.complete(ctx, task); cerr != nil {
			w.opts.Logger.Error("job completion failed", "job_id", task.ID, "err", cerr)
		}
		return
	}

	// ARCH-02: retry only when the budget allows it, the task is declared
	// idempotent, and the error itself is retryable. An operation whose outcome
	// is unknown is never retried — it must be reconciled first.
	task.Idempotent = entry.idempotent
	retry := w.retryAllowed(task, err)
	errorCode := "handler_error"
	if cancelRequested.Load() {
		// The row is 'canceling'; Fail resolves it to 'canceled'.
		retry = false
		errorCode = "canceled"
	}
	w.opts.Logger.Warn("job failed", "job_id", task.ID, "kind", task.Kind,
		"attempt", task.Attempts, "max_attempts", task.MaxAttempts, "retry", retry,
		"canceled", cancelRequested.Load(), "err", err)
	w.recordFailure(ctx, task, errorCode, retry, cancelRequested.Load())
}

// retryAllowed is the single retry decision: attempts remain, the task declared
// itself idempotent, the error is retryable, and the outcome is not ambiguous.
func (w *Worker) retryAllowed(task *Task, err error) bool {
	if task.Attempts >= task.MaxAttempts {
		return false
	}
	if !task.Idempotent {
		return false
	}
	if ambiguousOutcome(err) {
		// "It may have been applied" is never retryable, however idempotent the
		// task claims to be: reconcile first.
		return false
	}
	return errs.RetryableError(err)
}

// ambiguousOutcome reports whether an error means "we do not know whether the
// mutation was applied". The outcome detail is the contract internal/magento
// writes and internal/mcp/server reads; the timeout code means the same thing by
// construction.
func ambiguousOutcome(err error) bool {
	if err == nil {
		return false
	}
	var typed *errs.Error
	if errors.As(err, &typed) && typed.Details["outcome"] == "unknown" {
		return true
	}
	code := errs.CodeOf(err)
	return code == errs.CodeUpstreamTimeout || code == errs.CodeOperationUnknown
}

// heartbeat renews the lease and is the only channel through which a cancel
// request or a lost lease reaches the running handler.
func (w *Worker) heartbeat(ctx context.Context, task *Task, stopHandler func(), leaseLost, cancelRequested *atomic.Bool) {
	ticker := time.NewTicker(w.opts.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		status, err := w.queue.Extend(ctx, task.ID, task.LeaseToken, w.opts.Lease)
		switch {
		case err == nil:
			if status.CancelRequested {
				w.opts.Logger.Info("job cancel requested; stopping handler", "job_id", task.ID)
				cancelRequested.Store(true)
				stopHandler()
				return
			}
		case errors.Is(err, ErrLeaseLost):
			w.opts.Logger.Warn("job lease lost; stopping handler", "job_id", task.ID)
			leaseLost.Store(true)
			stopHandler()
			return
		case ctx.Err() != nil:
			// The heartbeat is being stopped because the handler returned; the
			// in-flight renewal failing is expected, not a fault.
			return
		default:
			// A transient renewal failure is not fatal: the lease is still valid
			// until lease_expires_at, and the next tick retries.
			w.opts.Logger.Warn("job lease renewal failed", "job_id", task.ID, "err", err)
		}
	}
}

// complete records success on a context that shutdown cannot cancel.
func (w *Worker) complete(ctx context.Context, task *Task) error {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalWriteTimeout)
	defer cancel()
	err := w.queue.Complete(writeCtx, task.ID, task.LeaseToken)
	if errors.Is(err, ErrLeaseLost) {
		w.opts.Logger.Warn("job completion discarded: lease lost", "job_id", task.ID)
		return nil
	}
	return err
}

// recordFailure records a failed attempt on a context that shutdown cannot cancel.
func (w *Worker) recordFailure(ctx context.Context, task *Task, errorCode string, retry, canceled bool) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalWriteTimeout)
	defer cancel()
	err := w.queue.Fail(writeCtx, task.ID, task.LeaseToken, errorCode, retry)
	switch {
	case err == nil:
		return
	case errors.Is(err, ErrLeaseLost):
		w.opts.Logger.Warn("job failure discarded: lease lost", "job_id", task.ID, "canceled", canceled)
	default:
		w.opts.Logger.Error("job failure record failed", "job_id", task.ID, "err", err)
	}
}

// EnqueueNoop enqueues a liveness task, used by tests and operators.
func EnqueueNoop(ctx context.Context, queue Queue, message string) (string, error) {
	payload, err := json.Marshal(NoopPayload{Message: message})
	if err != nil {
		return "", fmt.Errorf("jobs: encode noop payload: %w", err)
	}
	return queue.Enqueue(ctx, Task{Kind: TypeNoop, Queue: "default", Payload: payload, MaxAttempts: 1})
}

func sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// jitter spreads polling so several workers do not hit the table in lockstep.
func jitter(base time.Duration) time.Duration {
	if base <= 0 {
		return time.Second
	}
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return base
	}
	offset := int64(binary.BigEndian.Uint32(buf[:])) % int64(base)
	return base/2 + time.Duration(offset)
}

// ImportBatchResult is the persisted outcome of one import batch.
type ImportBatchResult struct {
	Total     int
	Processed int
	Created   int
	Updated   int
	Skipped   int
	Failed    int
	Failures  []ImportBatchFailure
}

// ImportBatch is the persisted state of one asynchronous import.
type ImportBatch struct {
	ID         string               `json:"id"`
	InstanceID string               `json:"instance_id"`
	JobID      string               `json:"job_id,omitempty"`
	Format     string               `json:"format"`
	State      string               `json:"state"`
	Total      int                  `json:"total"`
	Processed  int                  `json:"processed"`
	Created    int                  `json:"created"`
	Updated    int                  `json:"updated"`
	Skipped    int                  `json:"skipped"`
	Failed     int                  `json:"failed"`
	ErrorCode  string               `json:"error_code,omitempty"`
	Failures   []ImportBatchFailure `json:"failures,omitempty"`
}

// ImportBatchFailure is one failed row persisted for resume.
type ImportBatchFailure struct {
	RowNumber int    `json:"row_number"`
	SKU       string `json:"sku"`
	Action    string `json:"action"`
	Error     string `json:"error"`
}
