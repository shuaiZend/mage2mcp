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
	"time"

	"github.com/shuaiZend/mage2mcp/internal/core/config"
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
				return err
			}
			raw, err := json.Marshal(magento.CompactImportReport(result))
			if err != nil {
				return fmt.Errorf("encode import report: %w", err)
			}
			return queue.SetResult(ctx, task.ID, string(raw))
		})
	}

	logger.Info("worker starting", "version", version, "queue", "postgresql")
	if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Info("worker stopped")
	return nil
}
