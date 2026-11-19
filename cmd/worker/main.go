// SPDX-License-Identifier: Apache-2.0

// Command worker runs the asynchronous task worker.
//
// Tasks live in PostgreSQL (see internal/storage.JobQueue); there is no Redis
// dependency. The worker claims with a lease, executes handlers, and records
// success, failure, or retry.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/shuaiZend/mage2mcp/internal/core/config"
	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/observability"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

// version is set at build time.
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mage2mcp-worker:", err)
		os.Exit(1)
	}
}

func run() error {
	var configPath string
	flag.StringVar(&configPath, "config", "config.yaml", "path to the YAML configuration")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	logger := observability.NewLogger(cfg.Log.Level, cfg.Log.Format, os.Stderr)
	if cfg.Database.URL == "" {
		return errors.New("worker: database.url is required: the queue lives in PostgreSQL")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(ctx, storage.Config{URL: cfg.Database.URL, MaxConns: cfg.Database.MaxConns})
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		return err
	}

	worker := jobs.NewWorker(storage.NewJobQueue(store), jobs.WorkerOptions{Logger: logger})
	worker.Handle(jobs.TypeNoop, func(_ context.Context, task jobs.Task) error {
		var payload jobs.NoopPayload
		if err := json.Unmarshal(task.Payload, &payload); err != nil {
			return fmt.Errorf("decode noop payload: %w", err)
		}
		logger.Info("noop task executed", "job_id", task.ID, "message", payload.Message)
		return nil
	})

	logger.Info("worker starting", "version", version, "queue", "postgresql")
	if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Info("worker stopped")
	return nil
}
