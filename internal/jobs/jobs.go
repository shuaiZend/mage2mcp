// SPDX-License-Identifier: Apache-2.0

// Package jobs defines the asynchronous task queue contract and worker.
//
// Tasks that are too slow for a synchronous MCP call (imports, large reports,
// log ingestion) are persisted in PostgreSQL and executed by cmd/worker. There
// is no Redis dependency: the jobs table is the queue, and claiming uses
// FOR UPDATE SKIP LOCKED with a lease so crashed workers are recovered.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// Task types. A type is stable once released; handlers are registered by the
// worker and producers enqueue by name.
const (
	TypeNoop          = "platform:noop"
	TypeReindex       = "platform:reindex"
	TypeMagentoImport = "magento:import"
)

// QueueImport is the queue name for asynchronous imports.
const QueueImport = "import"

// JobRecord is a read-only view of a persisted job.
type JobRecord struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	State       string          `json:"state"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"max_attempts"`
	ErrorCode   string          `json:"error_code,omitempty"`
	Result      string          `json:"result,omitempty"`
	Payload     json.RawMessage `json:"-"`
	CreatedAt   time.Time       `json:"created_at"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
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
}

// Queue persists and claims tasks. The PostgreSQL implementation lives in
// internal/storage; tests use an in-memory fake.
type Queue interface {
	Enqueue(ctx context.Context, t Task) (string, error)
	Claim(ctx context.Context, queues []string, lease time.Duration) (*Task, error)
	Complete(ctx context.Context, id string) error
	Fail(ctx context.Context, id, errorCode string, retry bool) error
	Extend(ctx context.Context, id string, lease time.Duration) error
	Cancel(ctx context.Context, id string) error
}

// Handler executes one task. Returning an error marks the attempt failed; the
// worker retries only while attempts remain and the task's max_attempts allows.
type Handler func(ctx context.Context, t Task) error

// WorkerOptions configures the worker loop.
type WorkerOptions struct {
	Concurrency  int
	PollInterval time.Duration
	Lease        time.Duration
	TaskTimeout  time.Duration
	Queues       []string
	Logger       *slog.Logger
}

func (o *WorkerOptions) defaults() {
	if o.Concurrency <= 0 {
		o.Concurrency = 4
	}
	if o.PollInterval <= 0 {
		o.PollInterval = time.Second
	}
	if o.Lease <= 0 {
		o.Lease = 2 * time.Minute
	}
	if o.TaskTimeout <= 0 {
		o.TaskTimeout = 5 * time.Minute
	}
	if len(o.Queues) == 0 {
		o.Queues = []string{"critical", "default", "import", "report"}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// Worker claims and executes tasks from a Queue.
type Worker struct {
	queue    Queue
	handlers map[string]Handler
	opts     WorkerOptions
}

// NewWorker returns a worker with no handlers registered.
func NewWorker(queue Queue, opts WorkerOptions) *Worker {
	opts.defaults()
	return &Worker{queue: queue, handlers: make(map[string]Handler), opts: opts}
}

// Handle registers a handler for a task kind. Registering twice replaces the
// previous handler, which is useful for tests.
func (w *Worker) Handle(kind string, h Handler) *Worker {
	w.handlers[kind] = h
	return w
}

// Run claims and executes tasks until ctx is cancelled. Every goroutine drains
// its current task before returning, so shutdown does not abandon a claim.
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
	handler, ok := w.handlers[task.Kind]
	if !ok {
		w.opts.Logger.Error("no handler for task kind", "kind", task.Kind, "job_id", task.ID)
		_ = w.queue.Fail(ctx, task.ID, "unsupported_task", false)
		return
	}
	taskCtx, cancel := context.WithTimeout(ctx, w.opts.TaskTimeout)
	err := handler(taskCtx, *task)
	cancel()
	if err == nil {
		if err := w.queue.Complete(ctx, task.ID); err != nil {
			w.opts.Logger.Error("job completion failed", "job_id", task.ID, "err", err)
		}
		return
	}
	retry := task.Attempts < task.MaxAttempts
	w.opts.Logger.Warn("job failed", "job_id", task.ID, "kind", task.Kind,
		"attempt", task.Attempts, "max_attempts", task.MaxAttempts, "retry", retry, "err", err)
	if failErr := w.queue.Fail(ctx, task.ID, "handler_error", retry); failErr != nil {
		w.opts.Logger.Error("job failure record failed", "job_id", task.ID, "err", failErr)
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
	ID         string
	InstanceID string
	JobID      string
	Format     string
	State      string
	Total      int
	Processed  int
	Created    int
	Updated    int
	Skipped    int
	Failed     int
	ErrorCode  string
	Failures   []ImportBatchFailure
}

// ImportBatchFailure is one failed row persisted for resume.
type ImportBatchFailure struct {
	RowNumber int
	SKU       string
	Action    string
	Error     string
}
