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
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shuaiZend/mage2mcp/internal/core/config"
	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/magento"
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

	queue := storage.NewJobQueue(store)

	var magentoResolver *magento.Resolver
	if cfg.Modules.Magento.Enabled && len(cfg.Modules.Magento.Instances) > 0 {
		settings := make([]magento.InstanceSettings, 0, len(cfg.Modules.Magento.Instances))
		for _, inst := range cfg.Modules.Magento.Instances {
			settings = append(settings, magento.InstanceSettings{
				ID: inst.ID, Endpoint: inst.Endpoint, StoreCode: inst.StoreCode, Credential: inst.Credential,
			})
		}
		magentoResolver, err = magento.NewResolverFromSettings(settings)
		if err != nil {
			return err
		}
	}

	worker := jobs.NewWorker(queue, jobs.WorkerOptions{Logger: logger})
	worker.Handle(jobs.TypeNoop, func(_ context.Context, task jobs.Task) error {
		var payload jobs.NoopPayload
		if err := json.Unmarshal(task.Payload, &payload); err != nil {
			return fmt.Errorf("decode noop payload: %w", err)
		}
		logger.Info("noop task executed", "job_id", task.ID, "message", payload.Message)
		return nil
	})

	if magentoResolver != nil {
		worker.Handle(jobs.TypeMagentoImport, func(ctx context.Context, task jobs.Task) error {
			var payload magento.ImportJobPayload
			if err := json.Unmarshal(task.Payload, &payload); err != nil {
				return fmt.Errorf("decode import payload: %w", err)
			}
			svc, err := magentoResolver.Resolve(payload.Instance)
			if err != nil {
				return err
			}
			data, err := magento.DecodeImportPayload(payload.DataBase64)
			if err != nil {
				return err
			}
			batchID, err := store.CreateImportBatch(ctx, payload.Instance, task.ID, payload.Format, data)
			if err != nil {
				return err
			}
			lastProgress := time.Time{}
			progress := func(done, total int) {
				if time.Since(lastProgress) < 2*time.Second {
					return
				}
				lastProgress = time.Now()
				raw, err := json.Marshal(map[string]any{
					"progress": map[string]int{"done": done, "total": total},
				})
				if err == nil {
					_ = queue.SetResult(ctx, task.ID, string(raw))
				}
			}
			var result *magento.ImportResult
			if len(payload.OnlyRows) > 0 {
				result, err = svc.ExecuteImportRows(ctx, payload.Format, data, payload.UpdateExisting, payload.OnlyRows)
			} else {
				result, err = svc.ExecuteImportWithProgress(ctx, payload.Format, data, payload.UpdateExisting, progress)
			}
			if err != nil {
				_ = store.FinishImportBatch(ctx, batchID, &jobs.ImportBatchResult{}, "failed", string(errs.CodeOf(err)))
				return err
			}
			batch := importBatchResult(result)
			state := "succeeded"
			if batch.Failed > 0 {
				state = "partial"
			}
			if err := store.FinishImportBatch(ctx, batchID, batch, state, ""); err != nil {
				logger.Error("import batch finalize failed", "batch_id", batchID, "err", err)
			}
			raw, err := json.Marshal(magento.CompactImportReport(result))
			if err != nil {
				return fmt.Errorf("encode import report: %w", err)
			}
			return queue.SetResult(ctx, task.ID, string(raw))
		})
	}

	go pruneImportBatches(ctx, store, logger)
	logger.Info("worker starting", "version", version, "queue", "postgresql")
	if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Info("worker stopped")
	return nil
}

// importBatchResult converts an execution result into the persisted batch shape,
// keeping every failed row (no report cap).
func importBatchResult(result *magento.ImportResult) *jobs.ImportBatchResult {
	batch := &jobs.ImportBatchResult{
		Total: result.Total, Processed: result.Total,
		Created: result.Created, Updated: result.Updated, Skipped: result.Skipped, Failed: result.Failed,
	}
	for _, row := range result.Rows {
		if row.Status != "failed" {
			continue
		}
		batch.Failures = append(batch.Failures, jobs.ImportBatchFailure{
			RowNumber: row.RowNumber, SKU: row.SKU, Action: row.Action, Error: row.Error,
		})
	}
	return batch
}

// pruneImportBatches removes import batch history older than the retention
// window. Failure rows cascade with their batch.
// importBatchPruner is the subset of storage used by the retention loop.
type importBatchPruner interface {
	PruneImportBatches(ctx context.Context, olderThan time.Time) (int64, error)
}

func pruneImportBatches(ctx context.Context, store importBatchPruner, logger *slog.Logger) {
	const (
		retention = 30 * 24 * time.Hour
		interval  = 6 * time.Hour
	)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		removed, err := store.PruneImportBatches(ctx, time.Now().Add(-retention))
		switch {
		case err != nil && ctx.Err() == nil:
			logger.Error("import batch pruning failed", "err", err)
		case removed > 0:
			logger.Info("pruned import batches", "count", removed)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
