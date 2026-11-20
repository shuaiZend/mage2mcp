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
	"github.com/shuaiZend/mage2mcp/internal/security/audit"
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
		// The import handler is declared idempotent: creates skip existing SKUs and
		// updates are set operations, so a bounded retry converges rather than
		// duplicating. (REL-06 notes the residual duplicate-create race; the
		// operation ledger is the authority for money mutations.)
		worker.HandleIdempotent(jobs.TypeMagentoImport, func(ctx context.Context, task jobs.Task) error {
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
					// Fenced: if this attempt lost the lease the write is a no-op
					// and the progress belongs to whichever worker owns the row.
					_ = queue.SetResult(ctx, task.ID, task.LeaseToken, string(raw))
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
			if err := queue.SetResult(ctx, task.ID, task.LeaseToken, string(raw)); err != nil {
				if errors.Is(err, jobs.ErrLeaseLost) {
					// Another attempt owns the row; its outcome is the truth.
					logger.Warn("import result discarded: lease lost", "job_id", task.ID)
					return nil
				}
				return err
			}
			return nil
		})
	}

	ledger := storage.NewOperationLedger(store)
	worker.Handle(jobs.TypeReconcile, newReconcileHandler(ledger, magentoObserver(magentoResolver), logger))
	go enqueueReconcileLoop(ctx, queue, logger)
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

// maxReconcileAttempts bounds how many times the reconciler will read upstream
// for one operation before it stops and asks for a human.
const maxReconcileAttempts = 8

// reconcileBatch is how many unknown operations one job resolves.
const reconcileBatch = 20

// operationObserver is the upstream read the reconciler applies. *magento.Service
// satisfies it; tests use a fake.
type operationObserver interface {
	ObserveOperation(ctx context.Context, operation, entityType, entityID string) (magento.Observation, error)
}

// observerFor resolves the observer for one operation's instance.
type observerFor func(instance string) (operationObserver, error)

// magentoObserver adapts the configured resolver to observerFor.
func magentoObserver(resolver *magento.Resolver) observerFor {
	return func(instance string) (operationObserver, error) {
		if resolver == nil {
			return nil, fmt.Errorf("magento is not configured")
		}
		svc, err := resolver.Resolve(instance)
		if err != nil {
			return nil, err
		}
		return svc, nil
	}
}

// newReconcileHandler builds the platform:reconcile task handler. It claims
// operations whose upstream outcome is unknown, asks Magento what actually
// happened, and resolves them. An inconclusive read re-marks the operation
// unknown with backoff; after maxReconcileAttempts it escalates to a human
// instead of looping forever.
func newReconcileHandler(ledger *storage.OperationLedger, observers observerFor, logger *slog.Logger) jobs.Handler {
	return func(ctx context.Context, task jobs.Task) error {
		// Resolve rows whose owner died before claiming anything: an executing or
		// reconciling row with an expired lease becomes unknown (never retried),
		// and a stale pending row becomes failed with an explicit reason.
		if recovered, err := ledger.RecoverExpired(ctx, 0, 0); err != nil {
			logger.Error("recover expired operations failed", "err", err)
		} else if recovered > 0 {
			logger.Warn("recovered abandoned operations", "count", recovered)
		}
		resolved, escalated, deferred := 0, 0, 0
		for i := 0; i < reconcileBatch; i++ {
			rec, applied, err := ledger.ClaimReconcilable(ctx, time.Minute)
			if err != nil {
				return fmt.Errorf("claim reconcilable operation: %w", err)
			}
			if !applied || rec == nil {
				break
			}
			if rec.ReconcileCount > maxReconcileAttempts {
				if _, err := ledger.Escalate(ctx, rec.OperationID, rec.Fence,
					"reconciliation attempts exhausted; manual review required"); err != nil {
					logger.Error("escalate operation failed", "operation_id", rec.OperationID, "err", err)
					continue
				}
				escalated++
				logger.Warn("operation escalated for manual reconciliation",
					"operation_id", rec.OperationID, "operation", rec.Operation,
					"entity", rec.EntityType+"/"+rec.EntityID, "attempts", rec.ReconcileCount)
				continue
			}
			observation, err := observe(ctx, observers, rec)
			if err != nil {
				// The read itself failed: back off, do not guess.
				logger.Warn("reconciliation read failed", "operation_id", rec.OperationID, "err", err)
				if _, resolveErr := ledger.Resolve(ctx, rec.OperationID, rec.Fence,
					storage.ResolvedUnknown, "", string(errs.CodeOf(err)), nil,
					reconcileAudit(rec, "error", "outcome_unknown", string(errs.CodeOf(err)))); resolveErr != nil {
					logger.Error("re-mark unknown failed", "operation_id", rec.OperationID, "err", resolveErr)
				}
				deferred++
				continue
			}
			switch observation {
			case magento.ObservationApplied:
				ok, err := ledger.Resolve(ctx, rec.OperationID, rec.Fence,
					storage.ResolvedSucceeded, rec.UpstreamRef, "", nil,
					reconcileAudit(rec, "allow", "succeeded", ""))
				if err != nil || !ok {
					logger.Error("resolve succeeded failed", "operation_id", rec.OperationID, "err", err)
					continue
				}
				resolved++
			case magento.ObservationNotApplied:
				ok, err := ledger.Resolve(ctx, rec.OperationID, rec.Fence,
					storage.ResolvedFailed, "", "reconciled_not_applied", nil,
					reconcileAudit(rec, "error", "failed", "reconciled_not_applied"))
				if err != nil || !ok {
					logger.Error("resolve failed failed", "operation_id", rec.OperationID, "err", err)
					continue
				}
				resolved++
			default:
				ok, err := ledger.Resolve(ctx, rec.OperationID, rec.Fence,
					storage.ResolvedUnknown, "", "reconciliation_inconclusive", nil,
					reconcileAudit(rec, "error", "outcome_unknown", "reconciliation_inconclusive"))
				if err != nil || !ok {
					logger.Error("re-mark unknown failed", "operation_id", rec.OperationID, "err", err)
					continue
				}
				deferred++
			}
		}
		logger.Info("reconciliation pass complete",
			"job_id", task.ID, "resolved", resolved, "escalated", escalated, "deferred", deferred)
		return nil
	}
}

// reconcileAudit builds the outcome audit row the reconciler commits with its
// transition, so a resolved operation is never left with only an intent.
func reconcileAudit(rec *storage.Reconcilable, decision, state, errorCode string) *audit.Event {
	return &audit.Event{
		Subject: "reconciler", IdentityClass: "service", Tool: rec.Operation,
		Instance: rec.InstanceID, Scope: "reconcile", Phase: "outcome",
		Decision: decision, State: state, ErrorCode: errorCode,
	}
}

// observe applies the per-capability read for one claimed operation.
func observe(ctx context.Context, observers observerFor, rec *storage.Reconcilable) (magento.Observation, error) {
	if observers == nil {
		return magento.ObservationInconclusive, fmt.Errorf("no observer is configured")
	}
	observer, err := observers(rec.InstanceID)
	if err != nil {
		return magento.ObservationInconclusive, err
	}
	if observer == nil {
		return magento.ObservationInconclusive, fmt.Errorf("no observer for instance %q", rec.InstanceID)
	}
	return observer.ObserveOperation(ctx, rec.Operation, rec.EntityType, rec.EntityID)
}

// enqueueReconcileLoop keeps one reconcile task per minute in the queue. The
// handler drains what is due; the enqueue only has to be frequent enough that an
// unknown operation is looked at promptly.
func enqueueReconcileLoop(ctx context.Context, queue *storage.JobQueue, logger *slog.Logger) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		key := fmt.Sprintf("auto-reconcile:%d", time.Now().UTC().Unix()/60)
		if _, err := queue.Enqueue(ctx, jobs.Task{
			Kind: jobs.TypeReconcile, Queue: "maintenance", MaxAttempts: 1,
			IdempotencyKey: key, Payload: json.RawMessage(`{}`),
		}); err != nil && ctx.Err() == nil {
			logger.Warn("enqueue reconcile task failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// pruneImportBatches removes terminal job and import-batch history older than
// the retention window. Failure rows cascade with their batch.
//
// The jobs table is both the queue and the execution record, so without this it
// grows without bound; only terminal states are eligible, because deleting a
// queued or running job would silently drop live work.
type importBatchPruner interface {
	PruneImportBatches(ctx context.Context, olderThan time.Time) (int64, error)
	PruneJobs(ctx context.Context, olderThan time.Time) (int64, error)
}

func pruneImportBatches(ctx context.Context, store importBatchPruner, logger *slog.Logger) {
	const (
		retention = 30 * 24 * time.Hour
		interval  = 6 * time.Hour
	)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		cutoff := time.Now().Add(-retention)
		removed, err := store.PruneImportBatches(ctx, cutoff)
		switch {
		case err != nil && ctx.Err() == nil:
			logger.Error("import batch pruning failed", "err", err)
		case removed > 0:
			logger.Info("pruned import batches", "count", removed)
		}
		if jobs, jerr := store.PruneJobs(ctx, cutoff); jerr != nil && ctx.Err() == nil {
			logger.Error("job pruning failed", "err", jerr)
		} else if jobs > 0 {
			logger.Info("pruned jobs", "count", jobs)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
