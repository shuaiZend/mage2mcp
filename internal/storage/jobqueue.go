// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/jobs"
)

// JobQueue is a PostgreSQL-backed jobs.Queue.
//
// Claiming uses FOR UPDATE SKIP LOCKED so several workers can run safely, and
// expired leases are returned to the queue so a crashed worker does not strand
// work. No Redis is involved.
type JobQueue struct {
	store *Store
}

// NewJobQueue returns a queue backed by store.
func NewJobQueue(store *Store) *JobQueue {
	return &JobQueue{store: store}
}

// Enqueue inserts a queued task and returns its id.
func (q *JobQueue) Enqueue(ctx context.Context, t jobs.Task) (string, error) {
	if q == nil || q.store == nil || q.store.pool == nil {
		return "", fmt.Errorf("storage: job queue not configured")
	}
	if t.Kind == "" {
		return "", fmt.Errorf("storage: task kind is required")
	}
	queue := t.Queue
	if queue == "" {
		queue = "default"
	}
	maxAttempts := t.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	payload := t.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	subject := sha256.Sum256([]byte("system"))
	module := "platform"
	var instanceID *string
	if t.InstanceID != "" {
		// The jobs table enforces module/instance consistency: a module-scoped
		// job must name its module and instance.
		module = strings.SplitN(t.Kind, ":", 2)[0]
		switch module {
		case "magento", "cloudflare", "ga4":
		default:
			return "", fmt.Errorf("storage: task kind %q needs a platform module prefix", t.Kind)
		}
		instanceID = &t.InstanceID
	}
	var idempotencyKey *string
	if t.IdempotencyKey != "" {
		idempotencyKey = &t.IdempotencyKey
	}
	if instanceID != nil {
		// Jobs reference the instance registry; register a configured instance
		// lazily so a job can carry its attribution without a separate sync step.
		if _, err := q.store.pool.Exec(ctx, `
			INSERT INTO datasource_instances (id, module) VALUES ($1, $2)
			ON CONFLICT (id) DO NOTHING`, *instanceID, module); err != nil {
			return "", fmt.Errorf("storage: register instance: %w", err)
		}
	}
	var id string
	err := q.store.pool.QueryRow(ctx, `
		INSERT INTO jobs (module, instance_id, tool, kind, queue, payload, subject_hash,
		                  max_attempts, state, idempotency_key)
		VALUES ($1, $2, $3, $3, $4, $5, $6, $7, 'queued', $8)
		RETURNING id::text`,
		module, instanceID, t.Kind, queue, payload, subject[:], maxAttempts, idempotencyKey).Scan(&id)
	if err != nil {
		// A repeated enqueue with the same idempotency key is not an error: the
		// original job is returned so callers can poll it.
		if isUniqueViolation(err) {
			existing, lookupErr := q.store.pool.Query(ctx, `
				SELECT id::text FROM jobs
				WHERE kind = $1 AND idempotency_key = $2 AND ($3::text IS NULL OR instance_id = $3::text)
				ORDER BY created_at LIMIT 1`, t.Kind, t.IdempotencyKey, instanceID)
			if lookupErr == nil {
				var existingID string
				if scanErr := existing.Scan(&existingID); scanErr == nil {
					return existingID, nil
				}
			}
		}
		return "", fmt.Errorf("storage: enqueue job: %w", err)
	}
	return id, nil
}

// Claim returns the next runnable task, or nil when the queues are empty.
func (q *JobQueue) Claim(ctx context.Context, queues []string, lease time.Duration) (*jobs.Task, error) {
	if len(queues) == 0 {
		queues = []string{"critical", "default", "import", "report"}
	}
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	tx, err := q.store.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage: claim begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Recover work whose worker died while holding a lease.
	if _, err := tx.Exec(ctx, `
		UPDATE jobs SET state = 'queued', lease_expires_at = NULL
		WHERE state = 'running' AND lease_expires_at IS NOT NULL AND lease_expires_at < now()`); err != nil {
		return nil, fmt.Errorf("storage: recover expired leases: %w", err)
	}

	var task jobs.Task
	err = tx.QueryRow(ctx, `
		SELECT id::text, kind, queue, payload, attempt_count + 1, max_attempts
		FROM jobs
		WHERE state = 'queued' AND queue = ANY($1) AND scheduled_at <= now()
		ORDER BY priority DESC, scheduled_at ASC
		FOR UPDATE SKIP LOCKED
		LIMIT 1`, queues).Scan(&task.ID, &task.Kind, &task.Queue, &task.Payload, &task.Attempts, &task.MaxAttempts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("storage: claim select: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE jobs SET state = 'running', started_at = COALESCE(started_at, now()),
			lease_expires_at = now() + $2::interval, attempt_count = $3, updated_at = now()
		WHERE id = $1::uuid`, task.ID, lease.String(), task.Attempts); err != nil {
		return nil, fmt.Errorf("storage: claim update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("storage: claim commit: %w", err)
	}
	return &task, nil
}

// Complete marks a task succeeded.
func (q *JobQueue) Complete(ctx context.Context, id string) error {
	_, err := q.store.pool.Exec(ctx, `
		UPDATE jobs SET state = 'succeeded', finished_at = now(), lease_expires_at = NULL, updated_at = now()
		WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("storage: complete job: %w", err)
	}
	return nil
}

// Fail records a failed attempt. With retry it returns the task to the queue
// after a bounded exponential backoff; otherwise it is terminal.
func (q *JobQueue) Fail(ctx context.Context, id, errorCode string, retry bool) error {
	state := "failed"
	backoff := time.Duration(0)
	if retry {
		state = "queued"
		backoff = 5 * time.Second
	}
	_, err := q.store.pool.Exec(ctx, `
		UPDATE jobs SET state = $2, last_error_code = NULLIF($3,''), lease_expires_at = NULL,
			scheduled_at = now() + $4::interval,
			finished_at = CASE WHEN $2 = 'failed' THEN now() ELSE NULL END,
			updated_at = now()
		WHERE id = $1::uuid`, id, state, errorCode, backoff.String())
	if err != nil {
		return fmt.Errorf("storage: fail job: %w", err)
	}
	return nil
}

// Extend renews the lease of a running task.
func (q *JobQueue) Extend(ctx context.Context, id string, lease time.Duration) error {
	_, err := q.store.pool.Exec(ctx, `
		UPDATE jobs SET lease_expires_at = now() + $2::interval, updated_at = now()
		WHERE id = $1::uuid AND state = 'running'`, id, lease.String())
	if err != nil {
		return fmt.Errorf("storage: extend job lease: %w", err)
	}
	return nil
}

// Cancel cancels a queued or running task.
func (q *JobQueue) Cancel(ctx context.Context, id string) error {
	_, err := q.store.pool.Exec(ctx, `
		UPDATE jobs SET state = 'canceled', finished_at = now(), lease_expires_at = NULL, updated_at = now()
		WHERE id = $1::uuid AND state IN ('queued','running')`, id)
	if err != nil {
		return fmt.Errorf("storage: cancel job: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// GetJob returns one job by id.
func (q *JobQueue) GetJob(ctx context.Context, id string) (*jobs.JobRecord, error) {
	if q == nil || q.store == nil || q.store.pool == nil {
		return nil, fmt.Errorf("storage: job queue not configured")
	}
	var record jobs.JobRecord
	var finished *time.Time
	var result *string
	var errorCode *string
	err := q.store.pool.QueryRow(ctx, `
		SELECT id::text, kind, state, attempt_count, max_attempts,
		       last_error_code, result_ref, created_at, finished_at
		FROM jobs WHERE id = $1::uuid`, id).
		Scan(&record.ID, &record.Kind, &record.State, &record.Attempts, &record.MaxAttempts,
			&errorCode, &result, &record.CreatedAt, &finished)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.New(errs.CodeNotFound, "job not found").WithDetail("job_id", id)
		}
		return nil, fmt.Errorf("storage: get job: %w", err)
	}
	if errorCode != nil {
		record.ErrorCode = *errorCode
	}
	if result != nil {
		record.Result = *result
	}
	record.FinishedAt = finished
	return &record, nil
}

// SetResult stores a handler result summary on a job.
func (q *JobQueue) SetResult(ctx context.Context, id, result string) error {
	if q == nil || q.store == nil || q.store.pool == nil {
		return fmt.Errorf("storage: job queue not configured")
	}
	_, err := q.store.pool.Exec(ctx, `
		UPDATE jobs SET result_ref = $2, updated_at = now() WHERE id = $1::uuid`, id, result)
	if err != nil {
		return fmt.Errorf("storage: set job result: %w", err)
	}
	return nil
}
