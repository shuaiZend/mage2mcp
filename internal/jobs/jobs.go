// SPDX-License-Identifier: Apache-2.0

// Package jobs defines the asynchronous task queue.
//
// The worker executes tasks that are too slow for a synchronous MCP call:
// imports, large reports, and log ingestion. Task payloads never contain
// secrets; credentials are resolved by the worker from configuration.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/hibiken/asynq"
)

// Task types. A type is stable once released; handlers are registered by the
// worker and the server enqueues by name.
const (
	TypeNoop    = "platform:noop"
	TypeReindex = "platform:reindex"
)

// NoopPayload is a liveness task used to verify the queue end to end.
type NoopPayload struct {
	Message string `json:"message"`
}

// RedisOptions builds queue connection options.
func RedisOptions(addr, password string, db int) asynq.RedisClientOpt {
	return asynq.RedisClientOpt{Addr: addr, Password: password, DB: db}
}

// NewServer builds an Asynq server with bounded concurrency.
func NewServer(opt asynq.RedisClientOpt, concurrency int, logger *slog.Logger) *asynq.Server {
	if concurrency <= 0 {
		concurrency = 4
	}
	return asynq.NewServer(opt, asynq.Config{
		Concurrency: concurrency,
		Queues: map[string]int{
			"critical": 6,
			"default":  3,
			"import":   1,
			"report":   2,
		},
		Logger: asynqLogger{logger: logger},
	})
}

// NewMux registers every task handler.
func NewMux(logger *slog.Logger) *asynq.ServeMux {
	mux := asynq.NewServeMux()
	mux.HandleFunc(TypeNoop, func(_ context.Context, task *asynq.Task) error {
		var payload NoopPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("jobs: decode noop payload: %w", err)
		}
		logger.Info("noop task executed", "message", payload.Message)
		return nil
	})
	return mux
}

// EnqueueNoop enqueues a liveness task, used by tests and operators.
func EnqueueNoop(ctx context.Context, client *asynq.Client, message string) (*asynq.TaskInfo, error) {
	payload, err := json.Marshal(NoopPayload{Message: message})
	if err != nil {
		return nil, fmt.Errorf("jobs: encode noop payload: %w", err)
	}
	return client.EnqueueContext(ctx, asynq.NewTask(TypeNoop, payload))
}

// asynqLogger adapts slog to the Asynq logger interface.
type asynqLogger struct{ logger *slog.Logger }

func (l asynqLogger) Debug(args ...any) { l.logger.Debug(fmt.Sprint(args...)) }
func (l asynqLogger) Info(args ...any)  { l.logger.Info(fmt.Sprint(args...)) }
func (l asynqLogger) Warn(args ...any)  { l.logger.Warn(fmt.Sprint(args...)) }
func (l asynqLogger) Error(args ...any) { l.logger.Error(fmt.Sprint(args...)) }
func (l asynqLogger) Fatal(args ...any) { l.logger.Error(fmt.Sprint(args...)) }
