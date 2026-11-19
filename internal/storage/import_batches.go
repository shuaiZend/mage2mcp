// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/shuaiZend/mage2mcp/internal/jobs"
)

// CreateImportBatch inserts a queued batch and returns its id.
func (s *Store) CreateImportBatch(ctx context.Context, instanceID, jobID, format string, payload []byte) (string, error) {
	if s == nil || s.pool == nil {
		return "", fmt.Errorf("storage: not open")
	}
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO import_batches (instance_id, job_id, format, state, payload)
		VALUES ($1, NULLIF($2,'')::uuid, $3, 'running', $4)
		RETURNING id::text`, instanceID, jobID, format, payload).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("storage: create import batch: %w", err)
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

// GetImportBatchByJob returns the batch for a job id, including failures.
func (s *Store) GetImportBatchByJob(ctx context.Context, jobID string) (*jobs.ImportBatch, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("storage: not open")
	}
	var batch jobs.ImportBatch
	var payload []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, instance_id, COALESCE(job_id::text,''), format, state, total, processed,
		       created, updated, skipped, failed, COALESCE(error_code,''), payload
		FROM import_batches WHERE job_id = $1::uuid`, jobID).
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
