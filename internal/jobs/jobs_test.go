// SPDX-License-Identifier: Apache-2.0

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
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
	return &task, nil
}

func (f *fakeQueue) Complete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, id)
	return nil
}

func (f *fakeQueue) Fail(_ context.Context, id, code string, retry bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, struct {
		id    string
		code  string
		retry bool
	}{id, code, retry})
	return nil
}

func (f *fakeQueue) Extend(context.Context, string, time.Duration) error { return nil }
func (f *fakeQueue) Cancel(context.Context, string) error                { return nil }

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

func TestWorkerRetriesWhenAttemptsRemain(t *testing.T) {
	queue := &fakeQueue{}
	queue.tasks = []Task{{ID: "j1", Kind: TypeNoop, Attempts: 1, MaxAttempts: 2}}
	worker := NewWorker(queue, WorkerOptions{Concurrency: 1, PollInterval: 5 * time.Millisecond})
	worker.Handle(TypeNoop, func(context.Context, Task) error { return errors.New("boom") })
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
