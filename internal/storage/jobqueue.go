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

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/jobs"
)

// JobQueue is a PostgreSQL-backed jobs.Queue.
//
// Claiming uses FOR UPDATE SKIP LOCKED so several workers can run safely, and
// expired leases are returned to the queue so a crashed worker does not strand
// work. No Redis is involved.
//
// Every state change is a guarded transition: the UPDATE names the states it may
// start from and the lease token it must match, and the caller is told whether
// the transition applied. A write that matches no row is not "success" — it means
// this attempt no longer owns the task.
type JobQueue struct {
	store *Store
}

// NewJobQueue returns a queue backed by store.
func NewJobQueue(store *Store) *JobQueue {
	return &JobQueue{store: store}
}

// recoveryLimit bounds one lease-recovery pass. Recovery runs inside every Claim,
// so it must never scan or lock an unbounded set of rows.
const recoveryLimit = 64

// jobLeaseGuard is the reusable fencing predicate shared by every transition that
// reports an outcome for a claim. It is deliberately a single constant: the
// operation ledger (next migration) uses the same shape, and a transition that
// omits it is a bug, not a variant.
//
// $1 = job id, $2 = lease token.
const jobLeaseGuard = `id = $1::uuid AND lease_token = $2::uuid AND state IN ('running', 'canceling')`

// completeSQL records success. A stale attempt matches no row.
const completeSQL = `
	UPDATE jobs SET state = 'succeeded', finished_at = now(), lease_expires_at = NULL,
		lease_token = NULL, updated_at = now()
	WHERE ` + jobLeaseGuard

// failSQL records a failed attempt. A 'canceling' row always ends 'canceled': a
// cancel is never undone by a late retry. The retry delay is exponential
// (5s * 2^(attempt-1), capped at 300s) with jitter, and only a row that still has
// attempt budget is re-queued.
//
// $3 = error code, $4 = retry.
const failSQL = `
	UPDATE jobs SET
		state = CASE
			WHEN state = 'canceling' THEN 'canceled'
			WHEN $4::boolean AND state = 'running' AND attempt_count < max_attempts THEN 'queued'
			ELSE 'failed'
		END,
		last_error_code = NULLIF($3, ''),
		finished_at = CASE
			WHEN state = 'canceling' THEN now()
			WHEN $4::boolean AND state = 'running' AND attempt_count < max_attempts THEN NULL
			ELSE now()
		END,
		scheduled_at = CASE
			WHEN $4::boolean AND state = 'running' AND attempt_count < max_attempts
			THEN now() + (
				LEAST(300, 5 * power(2, GREATEST(attempt_count - 1, 0)))
				* (0.75 + random() * 0.5)
			) * interval '1 second'
			ELSE scheduled_at
		END,
		lease_expires_at = NULL,
		lease_token = NULL,
		updated_at = now()
	WHERE ` + jobLeaseGuard + `
	RETURNING state`

// extendSQL renews the lease and reports a pending cancel in the same round trip.
// $3 = lease.
const extendSQL = `
	UPDATE jobs SET lease_expires_at = now() + $3::interval, updated_at = now()
	WHERE ` + jobLeaseGuard + `
	RETURNING lease_expires_at, (state = 'canceling')`

// setResultSQL records progress for the current attempt only.
// $3 = result.
const setResultSQL = `
	UPDATE jobs SET result_ref = $3, updated_at = now()
	WHERE ` + jobLeaseGuard

// cancelSQL implements two-phase cancellation: a queued task stops now, a running
// task is asked to stop and keeps its lease so its worker can report the outcome.
const cancelSQL = `
	UPDATE jobs SET
		state = CASE WHEN state = 'running' THEN 'canceling' ELSE 'canceled' END,
		cancel_requested_at = now(),
		finished_at = CASE WHEN state = 'queued' THEN now() ELSE finished_at END,
		lease_expires_at = CASE WHEN state = 'queued' THEN NULL ELSE lease_expires_at END,
		updated_at = now()
	WHERE id = $1::uuid AND state IN ('queued', 'running')`

// cancelScopedSQL is cancelSQL plus the ownership predicate: a caller may only
// cancel a job attributed to its own instance and subject hash.
const cancelScopedSQL = cancelSQL + `
	  AND instance_id IS NOT DISTINCT FROM NULLIF($2::text, '')
	  AND subject_hash = $3`

// recoverExpiredLeases resolves every expired claim it can grab without waiting:
//
//   - a canceled task whose worker died is finished as 'canceled';
//   - a task that has used its whole attempt budget is dead-lettered as 'failed'
//     with last_error_code 'lease_expired', so a job that kills its worker every
//     time cannot loop forever;
//   - anything else goes back to 'queued' for another attempt.
//
// It is bounded (LIMIT), ordered (lease_expires_at, id) and non-blocking
// (FOR UPDATE SKIP LOCKED), so two workers running it concurrently take disjoint
// rows and cannot deadlock against each other or against a claim.
var recoverExpiredLeasesSQL = fmt.Sprintf(`
WITH expired AS (
    SELECT id, state, attempt_count, max_attempts
      FROM jobs
     WHERE state IN ('running', 'canceling')
       AND lease_expires_at IS NOT NULL
       AND lease_expires_at < now()
     ORDER BY lease_expires_at, id
     LIMIT %d
     FOR UPDATE SKIP LOCKED
)
UPDATE jobs j
   SET state = CASE
                 WHEN e.state = 'canceling' THEN 'canceled'
                 WHEN e.attempt_count >= e.max_attempts THEN 'failed'
                 ELSE 'queued'
               END,
       last_error_code = CASE
                 WHEN e.state = 'running' AND e.attempt_count >= e.max_attempts THEN 'lease_expired'
                 ELSE j.last_error_code
               END,
       finished_at = CASE
                 WHEN e.state = 'canceling' OR e.attempt_count >= e.max_attempts THEN now()
                 ELSE NULL
               END,
       scheduled_at = CASE
                 WHEN e.state = 'running' AND e.attempt_count < e.max_attempts THEN now()
                 ELSE j.scheduled_at
               END,
       lease_expires_at = NULL,
       lease_token = NULL,
       updated_at = now()
  FROM expired e
 WHERE j.id = e.id`, recoveryLimit)

// Enqueue inserts a queued task and returns its id.
//
// The instance registration and the job insert share one transaction, so a failed
// insert cannot leave a registry row for a job that does not exist. A repeated
// idempotency key returns the original job: the conflict target is the actual
// unique index, and the fallback lookup matches exactly the same columns.
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
	// The job's ownership key is the hash of the verified caller. A task with no
	// subject is enqueued by the gateway itself and attributed to "system".
	subject := t.Subject
	if subject == "" {
		subject = "system"
	}
	subjectHash := sha256.Sum256([]byte(subject))
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

	tx, err := q.store.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("storage: enqueue begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if instanceID != nil {
		// Jobs reference the instance registry; register a configured instance
		// lazily so a job can carry its attribution without a separate sync step.
		// This is part of the same transaction as the insert below.
		if _, err := tx.Exec(ctx, `
			INSERT INTO datasource_instances (id, module) VALUES ($1, $2)
			ON CONFLICT (id) DO NOTHING`, *instanceID, module); err != nil {
			return "", fmt.Errorf("storage: register instance: %w", err)
		}
	}

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO jobs (module, instance_id, tool, kind, queue, payload, subject_hash,
		                  max_attempts, state, idempotency_key)
		VALUES ($1, $2, $3, $3, $4, $5, $6, $7, 'queued', $8)
		ON CONFLICT (instance_id, binding_id, subject_hash, tool, idempotency_key)
			WHERE idempotency_key IS NOT NULL
		DO NOTHING
		RETURNING id::text`,
		module, instanceID, t.Kind, queue, payload, subjectHash[:], maxAttempts, idempotencyKey).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// A repeated enqueue with the same idempotency key is not an error: the
		// original job is returned so callers can poll it. The predicate matches
		// the unique index exactly (instance_id, binding_id, subject_hash, tool,
		// idempotency_key), so it can never return a row from another scope.
		lookupErr := tx.QueryRow(ctx, `
			SELECT id::text FROM jobs
			WHERE instance_id IS NOT DISTINCT FROM $1::text
			  AND binding_id IS NULL
			  AND subject_hash = $2
			  AND tool = $3
			  AND idempotency_key = $4`,
			instanceID, subjectHash[:], t.Kind, t.IdempotencyKey).Scan(&id)
		if lookupErr != nil {
			return "", fmt.Errorf("storage: lookup idempotent job: %w", lookupErr)
		}
		err = nil
	}
	if err != nil {
		return "", fmt.Errorf("storage: enqueue job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("storage: enqueue commit: %w", err)
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
	// Recover work whose worker died while holding a lease. This is a statement of
	// its own, not part of the claim transaction: recovery must persist even when
	// the claim finds nothing, and a failed claim must not undo it. It is bounded,
	// ordered and non-blocking; see recoverExpiredLeasesSQL.
	if _, err := q.store.pool.Exec(ctx, recoverExpiredLeasesSQL); err != nil {
		return nil, fmt.Errorf("storage: recover expired leases: %w", err)
	}

	tx, err := q.store.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage: claim begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

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
	// The row is locked by the SELECT above, so this transition cannot race; the
	// state predicate is still spelled out so the statement is safe on its own.
	if err := tx.QueryRow(ctx, `
		UPDATE jobs SET state = 'running', started_at = COALESCE(started_at, now()),
			lease_expires_at = now() + $2::interval, lease_token = gen_random_uuid(),
			attempt_count = $3, updated_at = now()
		WHERE id = $1::uuid AND state = 'queued'
		RETURNING lease_token::text`, task.ID, lease.String(), task.Attempts).Scan(&task.LeaseToken); err != nil {
		return nil, fmt.Errorf("storage: claim update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("storage: claim commit: %w", err)
	}
	return &task, nil
}

// Complete marks a task succeeded. It is fenced: a stale attempt affects no row
// and gets ErrLeaseLost instead of overwriting the current owner's outcome.
func (q *JobQueue) Complete(ctx context.Context, id, leaseToken string) error {
	if leaseToken == "" {
		return leaseLostError(id)
	}
	tag, err := q.store.pool.Exec(ctx, completeSQL, id, leaseToken)
	if err != nil {
		return fmt.Errorf("storage: complete job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return leaseLostError(id)
	}
	return nil
}

// Fail records a failed attempt. With retry it returns the task to the queue
// after a bounded exponential backoff; otherwise it is terminal. A task in
// 'canceling' always ends 'canceled' — a cancel is never undone by a late retry.
func (q *JobQueue) Fail(ctx context.Context, id, leaseToken, errorCode string, retry bool) error {
	if leaseToken == "" {
		return leaseLostError(id)
	}
	var state string
	err := q.store.pool.QueryRow(ctx, failSQL, id, leaseToken, errorCode, retry).Scan(&state)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return leaseLostError(id)
		}
		return fmt.Errorf("storage: fail job: %w", err)
	}
	return nil
}

// Extend renews the lease of a running task and reports whether a cancel was
// requested. It is the worker's heartbeat and its cancel poll in one round trip.
func (q *JobQueue) Extend(ctx context.Context, id, leaseToken string, lease time.Duration) (jobs.LeaseStatus, error) {
	if leaseToken == "" {
		return jobs.LeaseStatus{}, leaseLostError(id)
	}
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	var status jobs.LeaseStatus
	err := q.store.pool.QueryRow(ctx, extendSQL, id, leaseToken, lease.String()).
		Scan(&status.ExpiresAt, &status.CancelRequested)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return jobs.LeaseStatus{}, leaseLostError(id)
		}
		return jobs.LeaseStatus{}, fmt.Errorf("storage: extend job lease: %w", err)
	}
	return status, nil
}

// PruneJobs deletes terminal jobs that finished before olderThan and returns
// the number removed.
//
// The jobs table is the queue and the execution record, so it grows with every
// task forever if nothing prunes it. Only terminal states are eligible: a queued
// or running job is live work, and deleting it would silently drop a task.
func (s *Store) PruneJobs(ctx context.Context, olderThan time.Time) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("storage: store not configured")
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM jobs
		WHERE state IN ('succeeded','failed','canceled')
		  AND COALESCE(finished_at, updated_at) < $1`, olderThan)
	if err != nil {
		return 0, fmt.Errorf("storage: prune jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Cancel asks a queued task to stop immediately and a running task to stop at its
// next heartbeat. It reports a typed error when nothing transitioned.
//
// It stops a job only when the job belongs to the given instance and subject: a
// job owned by another tenant or another caller is reported as not_found, so one
// tenant cannot cancel another's work by guessing an id.
func (q *JobQueue) Cancel(ctx context.Context, id, instanceID, subject string) error {
	tag, err := q.store.pool.Exec(ctx, cancelScopedSQL, id, instanceID, subjectHash(subject))
	if err != nil {
		return fmt.Errorf("storage: cancel job: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var state string
	if err := q.store.pool.QueryRow(ctx, `
		SELECT state FROM jobs
		WHERE id = $1::uuid
		  AND instance_id IS NOT DISTINCT FROM NULLIF($2::text, '')
		  AND subject_hash = $3`, id, instanceID, subjectHash(subject)).Scan(&state); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errs.New(errs.CodeNotFound, "job not found").WithDetail("job_id", id)
		}
		return fmt.Errorf("storage: cancel job: %w", err)
	}
	return errs.New(errs.CodeIllegalTransition, "job is already in a terminal state").
		WithDetail("job_id", id).WithDetail("state", state).WithCause(jobs.ErrNotCancelable)
}

// leaseLostError is the single typed conflict returned when a fenced write
// matches no row. errors.Is(err, jobs.ErrLeaseLost) identifies it; the code is
// CodeConflict so a tool boundary can map it to a retryable conflict.
func leaseLostError(id string) error {
	return errs.New(errs.CodeConflict, "job lease was lost; another attempt owns the task").
		WithDetail("job_id", id).
		WithCause(jobs.ErrLeaseLost)
}

// GetJob returns one job by id.
// GetJob returns a job only when it belongs to the given instance and subject.
// A job owned by another tenant or another caller is reported as not_found, not
// permission_denied, so the response cannot be used as an existence oracle.
func (q *JobQueue) GetJob(ctx context.Context, id, instanceID, subject string) (*jobs.JobRecord, error) {
	if q == nil || q.store == nil || q.store.pool == nil {
		return nil, fmt.Errorf("storage: job queue not configured")
	}
	var record jobs.JobRecord
	var finished *time.Time
	var result *string
	var errorCode *string
	err := q.store.pool.QueryRow(ctx, `
		SELECT id::text, kind, COALESCE(instance_id, ''), state, attempt_count, max_attempts,
		       last_error_code, result_ref, created_at, finished_at, payload,
		       cancel_requested_at IS NOT NULL
		FROM jobs
		WHERE id = $1::uuid
		  AND instance_id IS NOT DISTINCT FROM NULLIF($2::text, '')
		  AND subject_hash = $3`, id, instanceID, subjectHash(subject)).
		Scan(&record.ID, &record.Kind, &record.InstanceID, &record.State, &record.Attempts, &record.MaxAttempts,
			&errorCode, &result, &record.CreatedAt, &finished, &record.Payload,
			&record.CancelRequested)
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

// SetResult stores a handler result summary on a running job. It is fenced, so a
// stale worker cannot overwrite the progress or result of the current attempt.
func (q *JobQueue) SetResult(ctx context.Context, id, leaseToken, result string) error {
	if q == nil || q.store == nil || q.store.pool == nil {
		return fmt.Errorf("storage: job queue not configured")
	}
	if leaseToken == "" {
		return leaseLostError(id)
	}
	tag, err := q.store.pool.Exec(ctx, setResultSQL, id, leaseToken, result)
	if err != nil {
		return fmt.Errorf("storage: set job result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return leaseLostError(id)
	}
	return nil
}
