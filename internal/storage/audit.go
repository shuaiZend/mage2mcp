// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/shuaiZend/mage2mcp/internal/security/audit"
)

// DecisionPending is the decision value an intent row carries: the call was
// attempted, but no authorization decision has been made yet. Outcome rows carry
// the real decision (allow / deny / error). Using a distinct value means a
// phase-blind query for allowed calls cannot return a call that was denied.
const DecisionPending = "pending"

// AuditRecorder persists audit events in PostgreSQL.
type AuditRecorder struct {
	store *Store
}

// NewAuditRecorder returns a recorder backed by store.
func NewAuditRecorder(store *Store) *AuditRecorder {
	return &AuditRecorder{store: store}
}

// Record appends one event. Required fields mirror the in-memory recorder.
func (r *AuditRecorder) Record(ctx context.Context, e audit.Event) error {
	if r == nil || r.store == nil || r.store.pool == nil {
		return fmt.Errorf("storage: audit recorder not configured")
	}
	if e.Subject == "" || e.Tool == "" || e.Decision == "" || e.Phase == "" {
		return fmt.Errorf("storage: audit event is missing required fields")
	}
	occurredAt := e.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	subjectHash := sha256.Sum256([]byte(e.Subject))
	_, err := r.store.pool.Exec(ctx, `
		INSERT INTO audit_events
			(event_id, occurred_at, subject_hash, identity_class, tool, instance_id,
			 scope, decision, phase, state, error_code, request_id)
		VALUES
			(COALESCE(NULLIF($1, '')::uuid, gen_random_uuid()), $2, $3, NULLIF($4,''),
			 $5, NULLIF($6,''), NULLIF($7,''), $8, $9, $10, NULLIF($11,''), NULLIF($12,''))`,
		e.EventID, occurredAt, subjectHash[:], e.IdentityClass, e.Tool, e.Instance,
		e.Scope, e.Decision, e.Phase, e.State, e.ErrorCode, e.RequestID)
	if err != nil {
		return fmt.Errorf("storage: insert audit event: %w", err)
	}
	return nil
}

// RecordInTx appends one event inside an existing transaction, so an outcome can
// be committed atomically with the operation-ledger transition that produced it.
// A crash can then never leave an intent whose outcome was silently lost.
func (r *AuditRecorder) RecordInTx(ctx context.Context, tx pgx.Tx, e audit.Event) error {
	if tx == nil {
		return fmt.Errorf("storage: audit transaction is required")
	}
	if e.Subject == "" || e.Tool == "" || e.Decision == "" || e.Phase == "" {
		return fmt.Errorf("storage: audit event is missing required fields")
	}
	occurredAt := e.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	subjectHash := sha256.Sum256([]byte(e.Subject))
	_, err := tx.Exec(ctx, `
		INSERT INTO audit_events
			(event_id, occurred_at, subject_hash, identity_class, tool, instance_id,
			 scope, decision, phase, state, error_code, request_id)
		VALUES
			(COALESCE(NULLIF($1, '')::uuid, gen_random_uuid()), $2, $3, NULLIF($4,''),
			 $5, NULLIF($6,''), NULLIF($7,''), $8, $9, $10, NULLIF($11,''), NULLIF($12,''))`,
		e.EventID, occurredAt, subjectHash[:], e.IdentityClass, e.Tool, e.Instance,
		e.Scope, e.Decision, e.Phase, e.State, e.ErrorCode, e.RequestID)
	if err != nil {
		return fmt.Errorf("storage: insert audit event: %w", err)
	}
	return nil
}

// AuditRecord is a row read back for verification.
type AuditRecord struct {
	OccurredAt    time.Time
	SubjectHash   string
	IdentityClass string
	Tool          string
	Instance      string
	Decision      string
	Phase         string
	State         string
	ErrorCode     string
}

// ListAudit returns the most recent events for a tool, newest first.
func (s *Store) ListAudit(ctx context.Context, tool string, limit int) ([]AuditRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT occurred_at, COALESCE(encode(subject_hash,'hex'),''), COALESCE(identity_class,''),
		       tool, COALESCE(instance_id,''), decision, phase, state, COALESCE(error_code,'')
		FROM audit_events
		WHERE ($1 = '' OR tool = $1)
		ORDER BY occurred_at DESC, seq DESC
		LIMIT $2`, tool, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: list audit: %w", err)
	}
	defer rows.Close()
	var out []AuditRecord
	for rows.Next() {
		var rec AuditRecord
		if err := rows.Scan(&rec.OccurredAt, &rec.SubjectHash, &rec.IdentityClass, &rec.Tool,
			&rec.Instance, &rec.Decision, &rec.Phase, &rec.State, &rec.ErrorCode); err != nil {
			return nil, fmt.Errorf("storage: scan audit: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// SubjectHash is a helper for tests and callers that need the stored form.
func SubjectHash(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return hex.EncodeToString(sum[:])
}
