// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
)

// ConfirmStore persists confirmation requests in PostgreSQL.
type ConfirmStore struct {
	store *Store
}

// NewConfirmStore returns a confirmation store backed by store.
func NewConfirmStore(store *Store) *ConfirmStore {
	return &ConfirmStore{store: store}
}

func subjectHash(subject string) []byte {
	sum := sha256.Sum256([]byte(subject))
	return sum[:]
}

// Create inserts a pending request.
func (c *ConfirmStore) Create(ctx context.Context, r *confirm.Request) error {
	if c == nil || c.store == nil || c.store.pool == nil {
		return fmt.Errorf("storage: confirmation store not configured")
	}
	_, err := c.store.pool.Exec(ctx, `
		INSERT INTO confirmation_tokens
			(id, tool, instance_id, scope, params_hash, requester_hash, summary, state, expires_at, created_at)
		VALUES ($1::uuid, $2, NULLIF($3,''), $4, decode($5,'hex'), $6, $7, $8, $9, $10)`,
		r.ID, r.Tool, r.Instance, r.Scope, r.ParamsHash, subjectHash(r.Requester),
		r.Summary, string(r.State), r.ExpiresAt, r.CreatedAt)
	if err != nil {
		return fmt.Errorf("storage: create confirmation: %w", err)
	}
	return nil
}

// Get loads a request by id.
func (c *ConfirmStore) Get(ctx context.Context, id string) (*confirm.Request, error) {
	row := c.store.pool.QueryRow(ctx, `
		SELECT id::text, tool, COALESCE(instance_id,''), scope, encode(params_hash,'hex'),
		       COALESCE(summary,''), state, created_at, expires_at, COALESCE(approved_at, 'epoch'::timestamptz),
		       COALESCE(consumed_at, 'epoch'::timestamptz)
		FROM confirmation_tokens WHERE id = $1::uuid`, id)
	var r confirm.Request
	var approvedAt, consumedAt time.Time
	if err := row.Scan(&r.ID, &r.Tool, &r.Instance, &r.Scope, &r.ParamsHash, &r.Summary,
		&r.State, &r.CreatedAt, &r.ExpiresAt, &approvedAt, &consumedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.New(errs.CodeNotFound, "confirmation not found")
		}
		return nil, fmt.Errorf("storage: get confirmation: %w", err)
	}
	if !approvedAt.IsZero() && approvedAt.Unix() > 0 {
		r.ApprovedAt = approvedAt
	}
	if !consumedAt.IsZero() && consumedAt.Unix() > 0 {
		r.ConsumedAt = consumedAt
	}
	return &r, nil
}

// Approve records a human approval; self-approval is rejected by the database.
func (c *ConfirmStore) Approve(ctx context.Context, id, approver string, now time.Time) error {
	tag, err := c.store.pool.Exec(ctx, `
		UPDATE confirmation_tokens
		SET state = 'approved', approver_hash = $2, approved_at = $3
		WHERE id = $1::uuid AND state = 'pending' AND expires_at > $3
		  AND requester_hash <> $2`,
		id, subjectHash(approver), now)
	if err != nil {
		return fmt.Errorf("storage: approve confirmation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return classifyApproveFailure(ctx, c, id, approver, now)
	}
	return nil
}

func classifyApproveFailure(ctx context.Context, c *ConfirmStore, id, approver string, now time.Time) error {
	row := c.store.pool.QueryRow(ctx,
		`SELECT state, expires_at, requester_hash = $2 FROM confirmation_tokens WHERE id = $1::uuid`,
		id, subjectHash(approver))
	var state string
	var expires time.Time
	var selfApproval bool
	if err := row.Scan(&state, &expires, &selfApproval); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errs.New(errs.CodeNotFound, "confirmation not found")
		}
		return errs.New(errs.CodeInternal, "confirmation lookup failed").WithCause(err)
	}
	switch {
	case selfApproval:
		return errs.New(errs.CodePermissionDenied, "self-approval is not allowed")
	case state != "pending":
		return errs.New(errs.CodeConflict, "confirmation is not pending")
	case now.After(expires):
		return errs.New(errs.CodeConfirmationExpired, "confirmation has expired")
	default:
		return errs.New(errs.CodeConflict, "confirmation cannot be approved")
	}
}

// Consume burns an approved confirmation for the exact same operation: the same
// requester, tool, instance, and arguments. The compare-and-set is a single
// UPDATE, so concurrent consumes of one approval yield exactly one winner.
func (c *ConfirmStore) Consume(ctx context.Context, id, requester, tool, instance, paramsHash string, now time.Time) error {
	tag, err := c.store.pool.Exec(ctx, `
		UPDATE confirmation_tokens
		SET state = 'consumed', consumed_at = $5
		WHERE id = $1::uuid AND state = 'approved' AND expires_at > $5
		  AND requester_hash = $2
		  AND tool = $3 AND COALESCE(instance_id,'') = $4
		  AND params_hash = decode($6,'hex')`,
		id, subjectHash(requester), tool, instance, now, paramsHash)
	if err != nil {
		return fmt.Errorf("storage: consume confirmation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return classifyConsumeFailure(ctx, c, id, requester, tool, instance, paramsHash, now)
	}
	return nil
}

// consumeInTx is Consume inside a caller-supplied transaction. The operation
// ledger uses it so that burning an approval and recording the operation are one
// atomic step: a crash between them can leave neither, never "approval burned
// with no operation".
func (c *ConfirmStore) consumeInTx(ctx context.Context, tx pgx.Tx, id, requester, tool, instance, paramsHash string, now time.Time) error {
	if tx == nil {
		return fmt.Errorf("storage: consume requires a transaction")
	}
	tag, err := tx.Exec(ctx, `
		UPDATE confirmation_tokens
		SET state = 'consumed', consumed_at = $5
		WHERE id = $1::uuid AND state = 'approved' AND expires_at > $5
		  AND requester_hash = $2
		  AND tool = $3 AND COALESCE(instance_id,'') = $4
		  AND params_hash = decode($6,'hex')`,
		id, subjectHash(requester), tool, instance, now, paramsHash)
	if err != nil {
		return fmt.Errorf("storage: consume confirmation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return classifyConsumeFailure(ctx, c, id, requester, tool, instance, paramsHash, now)
	}
	return nil
}

func classifyConsumeFailure(ctx context.Context, c *ConfirmStore, id, requester, tool, instance, paramsHash string, now time.Time) error {
	row := c.store.pool.QueryRow(ctx, `
		SELECT state, expires_at, requester_hash = $2,
		       (tool = $3 AND COALESCE(instance_id,'') = $4),
		       encode(params_hash,'hex') = $5
		FROM confirmation_tokens WHERE id = $1::uuid`,
		id, subjectHash(requester), tool, instance, paramsHash)
	var state string
	var expires time.Time
	var sameRequester, sameOperation, sameParams bool
	if err := row.Scan(&state, &expires, &sameRequester, &sameOperation, &sameParams); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errs.New(errs.CodeNotFound, "confirmation not found")
		}
		return errs.New(errs.CodeInternal, "confirmation lookup failed").WithCause(err)
	}
	switch {
	case state == "consumed":
		return errs.New(errs.CodeConfirmationUsed, "confirmation has already been used")
	case state == "expired" || now.After(expires):
		return errs.New(errs.CodeConfirmationExpired, "confirmation has expired")
	case state != "approved":
		return errs.New(errs.CodeConfirmationRequired, "confirmation has not been approved")
	case !sameRequester:
		return errs.New(errs.CodePermissionDenied, "confirmation belongs to a different requester")
	case !sameOperation || !sameParams:
		// One generic message: the caller must not learn which binding failed.
		return errs.New(errs.CodeConfirmationMismatch, "confirmation does not match this operation")
	default:
		return errs.New(errs.CodeConflict, "confirmation cannot be consumed")
	}
}

// ListPending returns requests awaiting approval, oldest first.
func (c *ConfirmStore) ListPending(ctx context.Context) ([]*confirm.Request, error) {
	rows, err := c.store.pool.Query(ctx, `
		SELECT id::text, tool, COALESCE(instance_id,''), scope, encode(params_hash,'hex'),
		       COALESCE(summary,''), created_at, expires_at
		FROM confirmation_tokens WHERE state = 'pending' ORDER BY created_at ASC LIMIT 200`)
	if err != nil {
		return nil, fmt.Errorf("storage: list confirmations: %w", err)
	}
	defer rows.Close()
	var out []*confirm.Request
	for rows.Next() {
		r := &confirm.Request{State: confirm.StatePending}
		if err := rows.Scan(&r.ID, &r.Tool, &r.Instance, &r.Scope, &r.ParamsHash, &r.Summary, &r.CreatedAt, &r.ExpiresAt); err != nil {
			return nil, fmt.Errorf("storage: scan confirmation: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
