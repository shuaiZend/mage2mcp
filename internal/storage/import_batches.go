// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
)

// CreateImportBatch opens the batch for a job and returns its id.
//
// A retried job must resume its batch rather than open a second one: two open
// rows for one job make "which batch is this job's?" ambiguous and leave a
// stale `running` row behind after the retry finishes. A batch that already
// reached a terminal state is history, so a later attempt opens a new one.
func (s *Store) CreateImportBatch(ctx context.Context, instanceID, jobID, format string, payload []byte) (string, error) {
	if s == nil || s.pool == nil {
		return "", fmt.Errorf("storage: not open")
	}
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO import_batches (instance_id, job_id, format, state, payload)
		SELECT $1, NULLIF($2,'')::uuid, $3, 'running', $4
		WHERE NOT EXISTS (
			SELECT 1 FROM import_batches
			WHERE job_id = NULLIF($2,'')::uuid AND state = 'running')
		RETURNING id::text`, instanceID, jobID, format, payload).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("storage: create import batch: %w", err)
	}
	// The job already has an open batch: resume it.
	if err := s.pool.QueryRow(ctx, `
		SELECT id::text FROM import_batches
		WHERE job_id = NULLIF($1,'')::uuid AND state = 'running'
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, jobID).Scan(&id); err != nil {
		return "", fmt.Errorf("storage: resume import batch: %w", err)
	}
	return id, nil
}

// FinishImportBatch records the final counts, failures, and state.
func (s *Store) FinishImportBatch(ctx context.Context, id string, result *jobs.ImportBatchResult, state string, errorCode string) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage: not open")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("storage: finish import batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE import_batches SET state = $2, total = $3, processed = $4, created = $5,
			updated = $6, skipped = $7, failed = $8, error_code = NULLIF($9,''),
			updated_at = now(), finished_at = now()
		WHERE id = $1::uuid`, id, state, result.Total, result.Processed, result.Created,
		result.Updated, result.Skipped, result.Failed, errorCode); err != nil {
		return fmt.Errorf("storage: update import batch: %w", err)
	}
	for _, failure := range result.Failures {
		if _, err := tx.Exec(ctx, `
			INSERT INTO import_batch_failures (batch_id, row_number, sku, action, error)
			VALUES ($1::uuid, $2, $3, $4, $5)
			ON CONFLICT (batch_id, row_number) DO UPDATE
			SET sku = EXCLUDED.sku, action = EXCLUDED.action, error = EXCLUDED.error`,
			id, failure.RowNumber, failure.SKU, failure.Action, failure.Error); err != nil {
			return fmt.Errorf("storage: insert import failure: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// GetImportBatchByJob returns the batch for a job id, including failures, only
// when the owning job belongs to the given instance and subject.
func (s *Store) GetImportBatchByJob(ctx context.Context, jobID, instanceID, subject string) (*jobs.ImportBatch, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("storage: not open")
	}
	if jobID == "" {
		return nil, nil
	}
	var batch jobs.ImportBatch
	var payload []byte
	err := s.pool.QueryRow(ctx, `
		SELECT b.id::text, b.instance_id, COALESCE(b.job_id::text,''), b.format, b.state, b.total, b.processed,
		       b.created, b.updated, b.skipped, b.failed, COALESCE(b.error_code,''), b.payload
		FROM import_batches b
		JOIN jobs j ON j.id = b.job_id
		WHERE b.job_id = $1::uuid
		  AND b.instance_id = $2
		  AND j.subject_hash = $3
		-- A retried import can leave more than one batch row for a job. Return
		-- the newest deterministically instead of an arbitrary one.
		ORDER BY b.created_at DESC, b.id DESC
		LIMIT 1`, jobID, instanceID, subjectHash(subject)).
		Scan(&batch.ID, &batch.InstanceID, &batch.JobID, &batch.Format, &batch.State, &batch.Total,
			&batch.Processed, &batch.Created, &batch.Updated, &batch.Skipped, &batch.Failed, &batch.ErrorCode, &payload)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("storage: get import batch: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT row_number, sku, action, error FROM import_batch_failures
		WHERE batch_id = $1::uuid ORDER BY row_number`, batch.ID)
	if err != nil {
		return nil, fmt.Errorf("storage: list import failures: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var failure jobs.ImportBatchFailure
		if err := rows.Scan(&failure.RowNumber, &failure.SKU, &failure.Action, &failure.Error); err != nil {
			return nil, fmt.Errorf("storage: scan import failure: %w", err)
		}
		batch.Failures = append(batch.Failures, failure)
	}
	return &batch, rows.Err()
}

// ListImportBatches returns recent batches for an instance (counts only, no
// failure rows).
func (s *Store) ListImportBatches(ctx context.Context, instanceID string, limit int) ([]jobs.ImportBatch, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("storage: not open")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	// The list is scoped to the caller's subject as well as the instance: batch
	// ids, job ids, state, counts and error codes are one caller's business, and
	// the get/by-job paths already refuse another subject's batch. The subject
	// comes from the verified identity in ctx because the ImportBatchReader
	// interface is owned by the Magento module; with no identity the scope is
	// "anonymous", never every subject.
	subject := "anonymous"
	if id, ok := auth.IdentityFromContext(ctx); ok && id.Subject != "" {
		subject = id.Subject
	}
	rows, err := s.pool.Query(ctx, `
		SELECT b.id::text, b.instance_id, COALESCE(b.job_id::text,''), b.format, b.state, b.total,
		       b.processed, b.created, b.updated, b.skipped, b.failed, COALESCE(b.error_code,'')
		FROM import_batches b
		LEFT JOIN jobs j ON j.id = b.job_id
		WHERE ($1 = '' OR b.instance_id = $1)
		  AND (b.job_id IS NULL OR j.subject_hash = $3)
		ORDER BY b.created_at DESC
		LIMIT $2`, instanceID, limit, subjectHash(subject))
	if err != nil {
		return nil, fmt.Errorf("storage: list import batches: %w", err)
	}
	defer rows.Close()
	batches := make([]jobs.ImportBatch, 0, limit)
	for rows.Next() {
		var batch jobs.ImportBatch
		if err := rows.Scan(&batch.ID, &batch.InstanceID, &batch.JobID, &batch.Format, &batch.State,
			&batch.Total, &batch.Processed, &batch.Created, &batch.Updated, &batch.Skipped,
			&batch.Failed, &batch.ErrorCode); err != nil {
			return nil, fmt.Errorf("storage: scan import batch: %w", err)
		}
		batches = append(batches, batch)
	}
	return batches, rows.Err()
}

// GetImportBatch returns one batch by id, including failure rows, only when it
// belongs to the given instance and (through its owning job) the given subject.
//
// Ownership rule: a batch created by the worker always names its job, so the
// caller must own that job. A batch with no job_id is legacy or operator-made
// and has no owner to compare against, so it is scoped by instance only; it is
// still unreachable across tenants.
func (s *Store) GetImportBatch(ctx context.Context, id, instanceID, subject string) (*jobs.ImportBatch, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("storage: not open")
	}
	if id == "" {
		return nil, nil
	}
	var batch jobs.ImportBatch
	err := s.pool.QueryRow(ctx, `
		SELECT b.id::text, b.instance_id, COALESCE(b.job_id::text,''), b.format, b.state, b.total, b.processed,
		       b.created, b.updated, b.skipped, b.failed, COALESCE(b.error_code,'')
		FROM import_batches b
		LEFT JOIN jobs j ON j.id = b.job_id
		WHERE b.id = $1::uuid
		  AND b.instance_id = $2
		  AND (b.job_id IS NULL OR j.subject_hash = $3)`, id, instanceID, subjectHash(subject)).
		Scan(&batch.ID, &batch.InstanceID, &batch.JobID, &batch.Format, &batch.State, &batch.Total,
			&batch.Processed, &batch.Created, &batch.Updated, &batch.Skipped, &batch.Failed, &batch.ErrorCode)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("storage: get import batch: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT row_number, sku, action, error FROM import_batch_failures
		WHERE batch_id = $1::uuid ORDER BY row_number`, batch.ID)
	if err != nil {
		return nil, fmt.Errorf("storage: list import failures: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var failure jobs.ImportBatchFailure
		if err := rows.Scan(&failure.RowNumber, &failure.SKU, &failure.Action, &failure.Error); err != nil {
			return nil, fmt.Errorf("storage: scan import failure: %w", err)
		}
		batch.Failures = append(batch.Failures, failure)
	}
	return &batch, rows.Err()
}

// PruneImportBatches deletes batches created before olderThan and returns the
// number removed. Failure rows cascade.
func (s *Store) PruneImportBatches(ctx context.Context, olderThan time.Time) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("storage: not open")
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM import_batches WHERE created_at < $1`, olderThan)
	if err != nil {
		return 0, fmt.Errorf("storage: prune import batches: %w", err)
	}
	return tag.RowsAffected(), nil
}
