// SPDX-License-Identifier: Apache-2.0

package storage

// Operation ledger: the durable state machine for a business mutation.
//
// The problem it solves: after a timeout, a transport error or a crash, the
// gateway cannot tell "the mutation definitely did not happen" from "it may have
// happened". Recording that as `failed` makes a retry duplicate a charge, a
// refund, a cancellation or an inventory movement. The ledger records the intent
// before the call and the outcome after it, and it has a state for "we do not
// know" — `unknown` — which is never a licence to retry, only to reconcile.
//
// Design and transition table: docs/audit/reliability-review.md §3.2-§3.5.
//
// States and transitions (every one is a single conditional statement):
//
//	T1  none        -> pending      Begin
//	T2  pending     -> executing    Execute          (new fence, attempt+1, lease)
//	T3  executing   -> succeeded    Succeed
//	T4  executing   -> failed       Fail             (only when not-applied is proven)
//	T5  executing   -> unknown      Unknown          (the ambiguous outcome)
//	T6  unknown     -> reconciling  ClaimReconcilable
//	T7  reconciling -> succeeded    Resolve(ResolvedSucceeded)
//	T8  reconciling -> failed       Resolve(ResolvedFailed)
//	T9  reconciling -> unknown      Resolve(ResolvedUnknown)
//	T10 pending|executing|unknown -> failed   Abort (operator, proven not-applied)
//	T11 failed      -> pending      Retry (only when the failure was definite)
//
// Every transition after T1 is guarded by the current state and the fencing token
// issued by T2/T6, and records a row in operation_transitions. A write that
// matches no row returns applied=false: the caller lost ownership and must not
// treat it as success.

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/security/audit"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
)

// executor is the subset of pgx the ledger needs. *pgxpool.Pool and pgx.Tx both
// satisfy it, so the same transition SQL runs standalone or inside a caller's
// transaction (which is what makes confirmation+ledger and ledger+audit atomic).
type executor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Operation states. The database CHECK constraint is the authority; these are the
// names callers use.
const (
	OpPending     = "pending"
	OpExecuting   = "executing"
	OpSucceeded   = "succeeded"
	OpFailed      = "failed"
	OpUnknown     = "unknown"
	OpReconciling = "reconciling"
)

// Entity types the ledger accepts. Keeping this closed is what lets a reconciler
// know which upstream read proves or disproves an operation.
const (
	EntityOrder      = "order"
	EntityInvoice    = "invoice"
	EntityCreditmemo = "creditmemo"
	EntityShipment   = "shipment"
	EntityProduct    = "product"
	EntityStock      = "stock"
	EntityCategory   = "category"
	EntityCustomer   = "customer"
)

// Decision tells the caller what to do after Begin. The caller never needs to
// know the transition table: it switches on this value.
type Decision string

const (
	// DecisionProceed: the operation is new (or was reset by a retry) and nobody
	// owns it. Call Execute, perform the mutation, then Succeed/Fail/Unknown.
	DecisionProceed Decision = "proceed"
	// DecisionRetry: a previous attempt failed definitively. Call Retry, then
	// Execute.
	DecisionRetry Decision = "retry"
	// DecisionInProgress: another attempt owns the operation. Poll by id; do not
	// call the upstream system again.
	DecisionInProgress Decision = "in_progress"
	// DecisionReplay: the operation already succeeded. Return Result as the
	// response with replayed=true; do not call the upstream system.
	DecisionReplay Decision = "replay"
	// DecisionUnknown: the outcome is ambiguous. Poll; never retry. Begin also
	// returns an error with code operation_unknown.
	DecisionUnknown Decision = "unknown"
)

// BeginResult is what Begin decided. OperationID is always populated.
type BeginResult struct {
	OperationID string
	Decision    Decision
	State       string
	Fence       string
	Attempt     int
	Replayed    bool
	Result      json.RawMessage
	UpstreamRef string
	ErrorCode   string
}

// Operation is the intent to mutate, as the caller declares it.
//
// ArgsHash must be the canonical argument hash (ArgsHash below) so a ledger row
// and a confirmation can be correlated. Subject is hashed on the way in; the raw
// identity never reaches the database.
type Operation struct {
	IdempotencyKey string
	Operation      string // tool name, e.g. "magento.invoice.capture"
	InstanceID     string
	Module         string // optional; derived from the operation prefix when empty
	Subject        string
	EntityType     string
	EntityID       string
	AmountMinor    *int64 // money in minor units; never a float
	Currency       string // ISO-4217, required with AmountMinor
	ArgsHash       string // hex sha256(JCS(args))
	ConfirmationID string
}

// OperationRecord is a read-only view used for polling and diagnostics.
type OperationRecord struct {
	ID             string          `json:"id" jsonschema:"Magento entity id."`
	IdempotencyKey string          `json:"idempotency_key" jsonschema:"Replay key the operation was recorded under; a derived key carries the window it was scoped to and, after a proven failure, a :released: suffix."`
	Operation      string          `json:"operation" jsonschema:"Tool name the operation represents, for example magento.order.create."`
	InstanceID     string          `json:"instance_id" jsonschema:"Configured instance the result came from."`
	EntityType     string          `json:"entity_type" jsonschema:"Entity kind the operation targets: order, invoice, creditmemo, shipment, product, stock, category or customer."`
	EntityID       string          `json:"entity_id" jsonschema:"Magento entity id."`
	State          string          `json:"state" jsonschema:"Current state."`
	AmountMinor    *int64          `json:"amount_minor,omitempty" jsonschema:"Money in integer minor units, when the operation moves money."`
	Currency       string          `json:"currency,omitempty" jsonschema:"ISO 4217 code the amounts are expressed in."`
	Fence          string          `json:"fence" jsonschema:"Fencing token of the current attempt; a write that presents a stale one is rejected."`
	Attempt        int             `json:"attempt" jsonschema:"How many times the operation has been claimed."`
	UpstreamRef    string          `json:"upstream_ref,omitempty" jsonschema:"Upstream handle recorded with the outcome, for example order:1042."`
	Result         json.RawMessage `json:"result,omitempty" jsonschema:"The operation result."`
	ErrorCode      string          `json:"error_code,omitempty" jsonschema:"Taxonomy code of the failure, when there is one."`
	ReconcileCount int             `json:"reconcile_count" jsonschema:"How many times the reconciler has read the upstream for this operation."`
	// ReconcileAfter is when the reconciler will next look at an unknown
	// operation. A nil value means the reconciler will never look again: the row
	// was escalated for a human (INT-07) and needs an explicit disposition.
	ReconcileAfter *time.Time `json:"reconcile_after,omitempty" jsonschema:"When the reconciler will next look at this operation; absent when it never will."`
	// NeedsAttention marks a row no automatic pass will touch again: an unknown
	// outcome that was escalated, or one whose backoff was pushed to infinity.
	// It is derived, never stored, so it cannot drift from the row it describes.
	NeedsAttention bool       `json:"needs_attention" jsonschema:"True when no automatic reconciliation will touch this row again, so an operator has to dispose of it."`
	CreatedAt      time.Time  `json:"created_at" jsonschema:"Store time the operation was recorded, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
	FinishedAt     *time.Time `json:"finished_at,omitempty" jsonschema:"When the operation reached a terminal state; absent while it is unresolved."`
}

// Reconcilable is one operation the reconciler has claimed.
type Reconcilable struct {
	OperationID    string
	Operation      string
	InstanceID     string
	EntityType     string
	EntityID       string
	AmountMinor    *int64
	Currency       string
	UpstreamRef    string
	ErrorCode      string
	ReconcileCount int
	Fence          string
	Attempt        int
}

// Resolution is the outcome of a reconciliation read.
type Resolution string

const (
	// ResolvedSucceeded: the upstream system proves the mutation was applied.
	ResolvedSucceeded Resolution = "succeeded"
	// ResolvedFailed: the upstream system proves it was not applied.
	ResolvedFailed Resolution = "failed"
	// ResolvedUnknown: the read was inconclusive; back off and try again.
	ResolvedUnknown Resolution = "unknown"
)

// OperationLedger is the PostgreSQL-backed operation ledger.
type OperationLedger struct {
	store    *Store
	confirms *ConfirmStore
	auditor  *AuditRecorder
}

// NewOperationLedger returns a ledger backed by store.
func NewOperationLedger(store *Store) *OperationLedger {
	return &OperationLedger{
		store:    store,
		confirms: NewConfirmStore(store),
		auditor:  NewAuditRecorder(store),
	}
}

// ArgsHash returns the canonical argument hash the confirmation flow uses, so a
// ledger row and a confirmation_tokens row for the same call share args_hash.
func ArgsHash(args json.RawMessage) (string, error) {
	return confirm.CanonicalHash(args)
}

// DeriveKey returns a deterministic idempotency key for a caller that did not
// supply one: exactly-once-by-arguments for one subject and one operation.
//
// The key alone is not the whole story: beginInTx scopes a derived key to
// DerivedKeyWindow, because "the same arguments" is not a promise of "the same
// intent". Two identical orders an hour apart are two orders (INT-12).
func DeriveKey(subject, argsHash string) string {
	return fmt.Sprintf("auto:%s:%s", hex.EncodeToString(subjectHash(subject)), argsHash)
}

// derivedKeyPrefix marks a key the gateway derived rather than one the caller
// supplied. Only derived keys get the window treatment and the release-on-failure
// treatment: an explicit key is a caller's promise of exactly-once and is honoured
// for as long as the row exists.
const derivedKeyPrefix = "auto:"

// DerivedKeyWindow bounds how long a derived key keeps deduplicating. Inside one
// window a repeated identical call replays instead of writing twice, which is the
// double-submit protection the derived key exists for. Across windows it is a new
// operation, because a caller that did not ask for exactly-once cannot be given
// "never again": an ERP that re-sends an order after a lost response, or a
// customer who buys the same item twice, must not be silently swallowed.
//
// It is a variable so a test can shrink it instead of sleeping for minutes.
var DerivedKeyWindow = 5 * time.Minute

// ledgerClock is the clock the derived-key window is measured against. A test
// replaces it to move between windows deterministically.
var ledgerClock = func() time.Time { return time.Now().UTC() }

// windowedDerivedKey scopes a derived key to the window containing now. The
// suffix is stable inside a window (so a retry lands on the same row) and
// different across windows (so the next window is a new operation).
func windowedDerivedKey(key string, now time.Time) string {
	if DerivedKeyWindow <= 0 || !strings.HasPrefix(key, derivedKeyPrefix) {
		return key
	}
	suffix := ":w" + strconv.FormatInt(now.Truncate(DerivedKeyWindow).Unix(), 10)
	if len(key)+len(suffix) > 256 {
		// The column is 256 characters; a caller who stuffed a derived-looking key
		// to the limit keeps the unwindowed key rather than failing the insert.
		return key
	}
	return key + suffix
}

// releasedKey renames a derived key so the row stays (as evidence) while the key
// is free for a new attempt. An explicit key is never released: the caller asked
// for exactly-once and the recorded failure is part of that promise.
func releasedKeySQL() string {
	return `CASE WHEN idempotency_key LIKE '` + derivedKeyPrefix + `%'
	                  AND idempotency_key NOT LIKE '%:released:%'
	                  AND length(idempotency_key) <= 238
	             THEN idempotency_key || ':released:' || substr(fence::text, 1, 8)
	             ELSE idempotency_key END`
}

// fenceGuard is the shared "this row is still mine" predicate. It is the same
// shape as jobLeaseGuard in jobqueue.go; jobqueue.go is out of scope for this
// change, so the queue can be pointed at this helper in a follow-up.
//
// $1 = operation id, $2 = fence.
func fenceGuard(states ...string) string {
	quoted := make([]string, len(states))
	for i, s := range states {
		quoted[i] = "'" + s + "'"
	}
	return fmt.Sprintf("id = $1::uuid AND fence = $2::uuid AND state IN (%s)", strings.Join(quoted, ", "))
}

var (
	guardExecuting   = fenceGuard(OpExecuting)
	guardReconciling = fenceGuard(OpReconciling)
)

// beginInTx is T1 plus the replay decision, inside a caller-supplied
// transaction. It never commits: the caller owns the transaction so that the
// confirmation consume and the ledger row land together.
func (l *OperationLedger) beginInTx(ctx context.Context, tx pgx.Tx, prepared prepared) (BeginResult, error) {
	if l == nil || l.store == nil || l.store.pool == nil {
		return BeginResult{}, fmt.Errorf("storage: operation ledger not configured")
	}
	// The ledger references the instance registry; register a configured instance
	// lazily in the same transaction so a first write cannot fail on the FK.
	if _, err := tx.Exec(ctx, `
		INSERT INTO datasource_instances (id, module) VALUES ($1, $2)
		ON CONFLICT (id) DO NOTHING`, prepared.instanceID, prepared.module); err != nil {
		return BeginResult{}, fmt.Errorf("storage: register instance: %w", err)
	}

	// T1: create the intent, or discover that it already exists.
	var result BeginResult
	err := tx.QueryRow(ctx, `
		WITH created AS (
			INSERT INTO operations (
				idempotency_key, operation, instance_id, subject_hash, entity_type, entity_id,
				amount_minor, currency, args_hash, confirmation_id, state, fence)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, '')::uuid, 'pending', gen_random_uuid())
			ON CONFLICT (instance_id, operation, idempotency_key) DO NOTHING
			RETURNING id, state, fence, attempt
		), logged AS (
			INSERT INTO operation_transitions (operation_id, from_state, to_state, fence, attempt, reason)
			SELECT id, 'none', state, fence, attempt, 'begin' FROM created
		)
		SELECT id::text, state, fence::text, attempt FROM created`,
		prepared.idempotencyKey, prepared.operation, prepared.instanceID, prepared.subjectHash,
		prepared.entityType, prepared.entityID, prepared.amountMinor, prepared.currency,
		prepared.argsHash, prepared.confirmationID).
		Scan(&result.OperationID, &result.State, &result.Fence, &result.Attempt)
	switch {
	case err == nil:
		result.Decision = DecisionProceed
		return result, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return BeginResult{}, fmt.Errorf("storage: create operation: %w", err)
	}

	// The key already exists: decide from its state, exactly as §3.5 requires.
	var existingArgsHash string
	err = tx.QueryRow(ctx, `
		SELECT id::text, state, fence::text, attempt, encode(args_hash, 'hex'),
		       COALESCE(result, 'null'::jsonb), COALESCE(upstream_ref, ''), COALESCE(error_code, '')
		FROM operations
		WHERE instance_id = $1 AND operation = $2 AND idempotency_key = $3`,
		prepared.instanceID, prepared.operation, prepared.idempotencyKey).
		Scan(&result.OperationID, &result.State, &result.Fence, &result.Attempt,
			&existingArgsHash, &result.Result, &result.UpstreamRef, &result.ErrorCode)
	if err != nil {
		return BeginResult{}, fmt.Errorf("storage: load operation: %w", err)
	}
	if existingArgsHash != prepared.argsHashHex {
		return result, errs.New(errs.CodeIdempotencyKeyReuse,
			"idempotency key was already used with different arguments").
			WithDetail("operation_id", result.OperationID).
			WithDetail("state", result.State)
	}
	switch result.State {
	case OpSucceeded:
		result.Decision = DecisionReplay
		result.Replayed = true
		// A replay must be visible in structuredContent, not only in the text
		// fallback: an agent that reads the JSON (INT-02) could not otherwise tell
		// "nothing new happened" from "a second write happened".
		result.Result = markReplayed(result.Result)
		return result, nil
	case OpFailed:
		if isAmbiguousCode(result.ErrorCode) {
			// The row says failed but its code says "we did not know": a retry
			// would risk a second mutation, so treat it as unresolved.
			result.Decision = DecisionUnknown
			return result, errs.New(errs.CodeOperationUnknown,
				"operation outcome is ambiguous; reconcile before retrying").
				WithDetail("operation_id", result.OperationID).
				WithDetail("state", result.State)
		}
		result.Decision = DecisionRetry
		return result, nil
	case OpPending:
		result.Decision = DecisionInProgress
		return result, errs.New(errs.CodeConflict, "operation is in progress").
			WithDetail("operation_id", result.OperationID).
			WithDetail("state", result.State)
	case OpExecuting:
		result.Decision = DecisionInProgress
		return result, nil
	default: // unknown, reconciling
		result.Decision = DecisionUnknown
		return result, errs.New(errs.CodeOperationUnknown,
			"operation outcome is unknown; poll for reconciliation and do not retry").
			WithDetail("operation_id", result.OperationID).
			WithDetail("state", result.State)
	}
}

// Begin implements T1 and the replay semantics of §3.5.
//
// It never returns a bare "ok": the caller is told what to do. The error cases
// are deliberate and typed:
//
//	args_hash differs for the same key   -> idempotency_key_reuse
//	state pending                        -> conflict (in flight, not yet started)
//	state unknown / reconciling          -> operation_unknown (poll, never retry)
func (l *OperationLedger) Begin(ctx context.Context, op Operation) (BeginResult, error) {
	if l == nil || l.store == nil || l.store.pool == nil {
		return BeginResult{}, fmt.Errorf("storage: operation ledger not configured")
	}
	prepared, err := prepareOperation(op)
	if err != nil {
		return BeginResult{}, err
	}
	tx, err := l.store.pool.Begin(ctx)
	if err != nil {
		return BeginResult{}, fmt.Errorf("storage: begin operation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := l.beginInTx(ctx, tx, prepared)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BeginResult{}, fmt.Errorf("storage: begin commit: %w", err)
	}
	return result, nil
}

// Execution is the ownership token returned by Execute.
type Execution struct {
	Fence   string
	Attempt int
}

// Execute implements T2: pending -> executing. It issues a fresh fence, which
// every later write must present. Two callers racing on one pending row produce
// exactly one winner; the loser gets applied=false.
func (l *OperationLedger) Execute(ctx context.Context, id string, lease time.Duration) (Execution, bool, error) {
	return l.executeInTx(ctx, l.store.pool, id, lease)
}

func (l *OperationLedger) executeInTx(ctx context.Context, ex executor, id string, lease time.Duration) (Execution, bool, error) {
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	var exec Execution
	err := ex.QueryRow(ctx, `
		WITH claimed AS (
			UPDATE operations
			   SET state = 'executing', fence = gen_random_uuid(), attempt = attempt + 1,
			       lease_expires_at = now() + $2::interval, finished_at = NULL,
			       error_code = NULL, updated_at = now()
			 WHERE id = $1::uuid AND state = 'pending'
			   AND (lease_expires_at IS NULL OR lease_expires_at < now())
			RETURNING id, state, fence, attempt
		), logged AS (
			INSERT INTO operation_transitions (operation_id, from_state, to_state, fence, attempt, reason)
			SELECT id, 'pending', state, fence, attempt, 'execute' FROM claimed
		)
		SELECT fence::text, attempt FROM claimed`,
		id, lease.String()).Scan(&exec.Fence, &exec.Attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Execution{}, false, l.executeNotApplied(ctx, id)
	}
	if err != nil {
		return Execution{}, false, fmt.Errorf("storage: execute operation: %w", err)
	}
	return exec, true, nil
}

// executeNotApplied explains a lost T2 race: another attempt already owns the
// row (conflict) or it is already terminal (illegal transition).
func (l *OperationLedger) executeNotApplied(ctx context.Context, id string) error {
	var state string
	err := l.store.pool.QueryRow(ctx,
		`SELECT state FROM operations WHERE id = $1::uuid`, id).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return errs.New(errs.CodeNotFound, "execute: operation not found").WithDetail("operation_id", id)
	}
	if err != nil {
		return fmt.Errorf("storage: classify execute: %w", err)
	}
	switch state {
	case OpExecuting, OpReconciling:
		return errs.New(errs.CodeConflict, "execute: another attempt already owns this operation").
			WithDetail("operation_id", id).WithDetail("state", state)
	default:
		return errs.New(errs.CodeIllegalTransition, "execute: operation is not pending").
			WithDetail("operation_id", id).WithDetail("state", state)
	}
}

// Succeed implements T3: executing -> succeeded, storing the replayable result.
func (l *OperationLedger) Succeed(ctx context.Context, id, fence, upstreamRef string, result json.RawMessage) (bool, error) {
	return l.succeedExec(ctx, l.store.pool, id, fence, upstreamRef, result, true)
}

func (l *OperationLedger) succeedExec(ctx context.Context, ex executor, id, fence, upstreamRef string, result json.RawMessage, writeAudit bool) (bool, error) {
	return l.transition(ctx, ex, id, "succeeded", OpExecuting, `
		WITH updated AS (
			UPDATE operations
			   SET state = 'succeeded', finished_at = now(), lease_expires_at = NULL,
			       upstream_ref = NULLIF($3, ''), result = $4::jsonb, updated_at = now()
			 WHERE `+guardExecuting+`
			RETURNING id, state, fence, attempt
		), logged AS (
			INSERT INTO operation_transitions (operation_id, from_state, to_state, fence, attempt, reason)
			SELECT id, 'executing', state, fence, attempt, 'succeeded' FROM updated
		), audited AS (
			-- The outcome audit row is written in the same statement as the transition,
			-- so a crash between the tool's commit and the middleware cannot lose it.
			-- This is the five self-ledgered money tools' only audit write.
			INSERT INTO audit_events (event_id, occurred_at, subject_hash, identity_class, tool,
			                          instance_id, scope, decision, phase, state, error_code)
			SELECT gen_random_uuid(), now(), o.subject_hash, 'service', o.operation,
			       o.instance_id, 'self', 'allow', 'outcome', 'succeeded', NULLIF('', '')
			  FROM updated u JOIN operations o ON o.id = u.id
		 WHERE $5::boolean
		)
		SELECT id FROM updated`, id, fence, upstreamRef, jsonArg(result), writeAudit)
}

// Fail implements T4: executing -> failed. Only call it when the upstream system
// proves the mutation was not applied; an ambiguous outcome is Unknown.
func (l *OperationLedger) Fail(ctx context.Context, id, fence, errorCode string) (bool, error) {
	return l.failExec(ctx, l.store.pool, id, fence, errorCode, true)
}

func (l *OperationLedger) failExec(ctx context.Context, ex executor, id, fence, errorCode string, writeAudit bool) (bool, error) {
	if isAmbiguousCode(errorCode) {
		return false, errs.New(errs.CodeInvalidArgument,
			"ambiguous outcomes must be recorded with Unknown, not Fail").
			WithDetail("error_code", errorCode)
	}
	return l.transition(ctx, ex, id, "failed", OpExecuting, `
		WITH updated AS (
			UPDATE operations
			   SET state = 'failed', finished_at = now(), lease_expires_at = NULL,
			       error_code = NULLIF($3, ''), updated_at = now(),
			       -- A derived key is not a caller's promise of exactly-once: once
			       -- the mutation is proven not to have happened, holding the key
			       -- would answer the caller's retry with "the operation previously
			       -- failed" (INT-12 / FIN-09). Rename the row so the key is free;
			       -- the operation, its history and its audit row all survive.
			       idempotency_key = `+releasedKeySQL()+`
			 WHERE `+guardExecuting+`
			RETURNING id, state, fence, attempt
		), logged AS (
			INSERT INTO operation_transitions (operation_id, from_state, to_state, fence, attempt, reason)
			SELECT id, 'executing', state, fence, attempt, 'failed' FROM updated
		), audited AS (
			-- The outcome audit row is written in the same statement as the transition,
			-- so a crash between the tool's commit and the middleware cannot lose it.
			-- This is the five self-ledgered money tools' only audit write.
			INSERT INTO audit_events (event_id, occurred_at, subject_hash, identity_class, tool,
			                          instance_id, scope, decision, phase, state, error_code)
			SELECT gen_random_uuid(), now(), o.subject_hash, 'service', o.operation,
			       o.instance_id, 'self', 'allow', 'outcome', 'failed', NULLIF($3, '')
			  FROM updated u JOIN operations o ON o.id = u.id
		 WHERE $4::boolean
		)
		SELECT id FROM updated`, id, fence, errorCode, writeAudit)
}

// Unknown implements T5: executing -> unknown. This is the transition that makes
// a retry safe, because it stops the retry: the row must be reconciled first.
func (l *OperationLedger) Unknown(ctx context.Context, id, fence, errorCode string, retryAfter time.Duration) (bool, error) {
	return l.unknownExec(ctx, l.store.pool, id, fence, errorCode, retryAfter, true)
}

func (l *OperationLedger) unknownExec(ctx context.Context, ex executor, id, fence, errorCode string, retryAfter time.Duration, writeAudit bool) (bool, error) {
	if retryAfter <= 0 {
		retryAfter = time.Minute
	}
	return l.transition(ctx, ex, id, "unknown", OpExecuting, `
		WITH updated AS (
			UPDATE operations
			   SET state = 'unknown', lease_expires_at = NULL,
			       error_code = NULLIF($3, ''),
			       reconcile_after = now() + $4::interval, updated_at = now()
			 WHERE `+guardExecuting+`
			RETURNING id, state, fence, attempt
		), logged AS (
			INSERT INTO operation_transitions (operation_id, from_state, to_state, fence, attempt, reason)
			SELECT id, 'executing', state, fence, attempt, 'unknown' FROM updated
		), audited AS (
			-- The outcome audit row is written in the same statement as the transition,
			-- so a crash between the tool's commit and the middleware cannot lose it.
			-- This is the five self-ledgered money tools' only audit write.
			INSERT INTO audit_events (event_id, occurred_at, subject_hash, identity_class, tool,
			                          instance_id, scope, decision, phase, state, error_code)
			SELECT gen_random_uuid(), now(), o.subject_hash, 'service', o.operation,
			       o.instance_id, 'self', 'allow', 'outcome', 'outcome_unknown', NULLIF($3, '')
			  FROM updated u JOIN operations o ON o.id = u.id
		 WHERE $5::boolean
		)
		SELECT id FROM updated`, id, fence, errorCode, retryAfter.String(), writeAudit)
}

// RecoverExpired resolves rows whose owner died, so a money operation can never
// sit in executing/reconciling forever. It is bounded, ordered and non-blocking
// (FOR UPDATE SKIP LOCKED), and it never auto-retries: an executing or
// reconciling row becomes unknown (reconcile it, do not re-run it), and a pending
// row that was never claimed becomes failed with an explicit reason.
//
// It returns the number of rows it recovered.
func (l *OperationLedger) RecoverExpired(ctx context.Context, stalePending, limit int) (int, error) {
	if stalePending <= 0 {
		stalePending = 10 * 60 // seconds
	}
	if limit <= 0 {
		limit = 64
	}
	var recovered int
	err := l.store.pool.QueryRow(ctx, `
		WITH expired AS (
			SELECT id, state
			  FROM operations
			 WHERE (state IN ('executing', 'reconciling')
			        AND lease_expires_at IS NOT NULL AND lease_expires_at < now())
			    OR (state = 'pending' AND created_at < now() - ($1 || ' seconds')::interval)
			 ORDER BY updated_at, id
			 LIMIT $2
			 FOR UPDATE SKIP LOCKED
		), updated AS (
			UPDATE operations o
			   SET state = CASE WHEN e.state = 'pending' THEN 'failed' ELSE 'unknown' END,
			       error_code = CASE
			                      WHEN e.state = 'pending' THEN 'abandoned_pending'
			                      WHEN e.state = 'executing' THEN 'lease_expired'
			                      ELSE 'reconcile_lease_expired'
			                    END,
			       finished_at = CASE WHEN e.state = 'pending' THEN now() ELSE NULL END,
			       lease_expires_at = NULL,
			       reconcile_after = CASE WHEN e.state = 'pending' THEN o.reconcile_after ELSE now() END,
			       updated_at = now()
			  FROM expired e
			 WHERE o.id = e.id
			RETURNING o.id, o.state, o.fence, o.attempt, o.error_code, e.state AS from_state
		), logged AS (
			INSERT INTO operation_transitions (operation_id, from_state, to_state, fence, attempt, reason)
			SELECT id, from_state, state, fence, attempt, error_code FROM updated
		), audited AS (
			-- REL43-02: a recovered row is a state change, so it gets an audit
			-- outcome in the same statement. The subject is the operation's stored
			-- hash; recovery is a service action.
			INSERT INTO audit_events (event_id, occurred_at, subject_hash, identity_class, tool,
			                          instance_id, scope, decision, phase, state, error_code)
			SELECT gen_random_uuid(), now(), o.subject_hash, 'service', o.operation,
			       o.instance_id, 'recovery', 'error', 'outcome',
			       CASE WHEN u.state = 'failed' THEN 'failed' ELSE 'outcome_unknown' END,
			       u.error_code
			  FROM updated u JOIN operations o ON o.id = u.id
		)
		SELECT count(*) FROM updated`, strconv.Itoa(stalePending), limit).Scan(&recovered)
	if err != nil {
		return 0, fmt.Errorf("storage: recover expired operations: %w", err)
	}
	return recovered, nil
}

// ClaimReconcilable implements T6: unknown -> reconciling. It is bounded,
// ordered and non-blocking (FOR UPDATE SKIP LOCKED), so several reconcilers can
// run at once and cannot take the same row or deadlock.
func (l *OperationLedger) ClaimReconcilable(ctx context.Context, lease time.Duration) (*Reconcilable, bool, error) {
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	var rec Reconcilable
	var amount *int64
	err := l.store.pool.QueryRow(ctx, `
		WITH claimed AS (
			UPDATE operations
			   SET state = 'reconciling', fence = gen_random_uuid(),
			       lease_expires_at = now() + $1::interval,
			       reconcile_count = reconcile_count + 1, updated_at = now()
			 WHERE id = (
				SELECT id FROM operations
				 WHERE state = 'unknown' AND reconcile_after <= now()
				 ORDER BY reconcile_after, id
				 LIMIT 1
				 FOR UPDATE SKIP LOCKED)
			RETURNING id, operation, instance_id, entity_type, entity_id, amount_minor, currency,
			          upstream_ref, error_code, reconcile_count, fence, attempt
		), logged AS (
			INSERT INTO operation_transitions (operation_id, from_state, to_state, fence, attempt, reason)
			SELECT id, 'unknown', 'reconciling', fence, attempt, 'reconcile_claim' FROM claimed
		)
		SELECT id::text, operation, instance_id, entity_type, entity_id, amount_minor,
		       COALESCE(currency, ''), COALESCE(upstream_ref, ''), COALESCE(error_code, ''),
		       reconcile_count, fence::text, attempt
		FROM claimed`, lease.String()).
		Scan(&rec.OperationID, &rec.Operation, &rec.InstanceID, &rec.EntityType, &rec.EntityID,
			&amount, &rec.Currency, &rec.UpstreamRef, &rec.ErrorCode,
			&rec.ReconcileCount, &rec.Fence, &rec.Attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("storage: claim reconcilable operation: %w", err)
	}
	rec.AmountMinor = amount
	return &rec, true, nil
}

// Resolve implements T7/T8/T9 from a reconciling row. When event is non-nil the
// audit outcome is written in the same transaction as the transition, so a
// resolved operation can never be left without its audit row.
func (l *OperationLedger) Resolve(ctx context.Context, id, fence string, resolution Resolution, upstreamRef, errorCode string, result json.RawMessage, event *audit.Event) (bool, error) {
	tx, err := l.store.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("storage: resolve operation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	applied, err := l.resolveInTx(ctx, tx, id, fence, resolution, upstreamRef, errorCode, result)
	if err != nil || !applied {
		return applied, err
	}
	if event != nil {
		if err := l.auditor.RecordInTx(ctx, tx, *event); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("storage: resolve commit: %w", err)
	}
	return true, nil
}

func (l *OperationLedger) resolveInTx(ctx context.Context, tx pgx.Tx, id, fence string, resolution Resolution, upstreamRef, errorCode string, result json.RawMessage) (bool, error) {
	switch resolution {
	case ResolvedSucceeded:
		return l.transition(ctx, tx, id, "resolved_succeeded", OpReconciling, `
			WITH updated AS (
				UPDATE operations
				   SET state = 'succeeded', finished_at = now(), lease_expires_at = NULL,
				       upstream_ref = NULLIF($3, ''), result = $4::jsonb,
				       error_code = NULL, updated_at = now()
				 WHERE `+guardReconciling+`
				RETURNING id, state, fence, attempt
			), logged AS (
				INSERT INTO operation_transitions (operation_id, from_state, to_state, fence, attempt, reason)
				SELECT id, 'reconciling', state, fence, attempt, 'reconciled_succeeded' FROM updated
			)
			SELECT id FROM updated`, id, fence, upstreamRef, jsonArg(result))
	case ResolvedFailed:
		if isAmbiguousCode(errorCode) {
			return false, errs.New(errs.CodeInvalidArgument,
				"an ambiguous outcome cannot be resolved as failed").
				WithDetail("error_code", errorCode)
		}
		return l.transition(ctx, tx, id, "resolved_failed", OpReconciling, `
			WITH updated AS (
				UPDATE operations
				   SET state = 'failed', finished_at = now(), lease_expires_at = NULL,
				       error_code = NULLIF($3, ''), updated_at = now()
				 WHERE `+guardReconciling+`
				RETURNING id, state, fence, attempt
			), logged AS (
				INSERT INTO operation_transitions (operation_id, from_state, to_state, fence, attempt, reason)
				SELECT id, 'reconciling', state, fence, attempt, 'reconciled_failed' FROM updated
			)
			SELECT id FROM updated`, id, fence, errorCode)
	case ResolvedUnknown:
		return l.transition(ctx, tx, id, "resolved_unknown", OpReconciling, `
			WITH updated AS (
				UPDATE operations
				   SET state = 'unknown', lease_expires_at = NULL,
				       error_code = NULLIF($3, ''),
				       reconcile_after = now() + LEAST(3600, 60 * power(2, reconcile_count)) * interval '1 second',
				       updated_at = now()
				 WHERE `+guardReconciling+`
				RETURNING id, state, fence, attempt
			), logged AS (
				INSERT INTO operation_transitions (operation_id, from_state, to_state, fence, attempt, reason)
				SELECT id, 'reconciling', state, fence, attempt, 'reconciled_unknown' FROM updated
			)
			SELECT id FROM updated`, id, fence, errorCode)
	default:
		return false, errs.New(errs.CodeInvalidArgument, "unknown resolution").
			WithDetail("resolution", string(resolution))
	}
}

// Abort implements T10: an operator ends a stuck operation as failed. The caller
// must have proven (by reading upstream) that the mutation was not applied.
func (l *OperationLedger) Abort(ctx context.Context, id, fromState, reason string) (bool, error) {
	switch fromState {
	case OpPending, OpExecuting, OpUnknown, OpReconciling:
	default:
		return false, errs.New(errs.CodeInvalidArgument, "operation cannot be aborted from that state").
			WithDetail("state", fromState)
	}
	if strings.TrimSpace(reason) == "" {
		return false, errs.New(errs.CodeInvalidArgument, "abort reason is required")
	}
	tag, err := l.store.pool.Exec(ctx, `
		WITH updated AS (
			UPDATE operations
			   SET state = 'failed', finished_at = now(), lease_expires_at = NULL,
			       error_code = 'admin_abort', updated_at = now()
			 WHERE id = $1::uuid AND state = $2
			RETURNING id, state, fence, attempt
		), logged AS (
			INSERT INTO operation_transitions (operation_id, from_state, to_state, fence, attempt, reason)
			SELECT id, $2, state, fence, attempt, $3 FROM updated
		), audited AS (
			-- REL43-02: an operator abort is a state change, so it gets an audit
			-- outcome in the same statement.
			INSERT INTO audit_events (event_id, occurred_at, subject_hash, identity_class, tool,
			                          instance_id, scope, decision, phase, state, error_code)
			SELECT gen_random_uuid(), now(), o.subject_hash, 'service', o.operation,
			       o.instance_id, 'abort', 'error', 'outcome', 'failed', 'admin_abort'
			  FROM updated u JOIN operations o ON o.id = u.id
		)
		SELECT id FROM updated`, id, fromState, reason)
	if err != nil {
		return false, fmt.Errorf("storage: abort operation: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	return false, l.notApplied(ctx, l.store.pool, id, fromState, "abort")
}

// Retry implements T11: failed -> pending. It is refused when the failure was
// ambiguous, because a retry would risk a second charge.
func (l *OperationLedger) Retry(ctx context.Context, id string) (bool, error) {
	return l.retryExec(ctx, l.store.pool, id)
}

func (l *OperationLedger) retryExec(ctx context.Context, ex executor, id string) (bool, error) {
	// Refuse an ambiguous failure before touching the row: a retry of a mutation
	// that may have been applied is a second mutation.
	var code string
	err := ex.QueryRow(ctx, `SELECT COALESCE(error_code, '') FROM operations WHERE id = $1::uuid`, id).Scan(&code)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, errs.New(errs.CodeNotFound, "operation not found").WithDetail("operation_id", id)
	}
	if err != nil {
		return false, fmt.Errorf("storage: retry operation: %w", err)
	}
	if isAmbiguousCode(code) {
		return false, errs.New(errs.CodeConflict,
			"the previous outcome is ambiguous; reconcile it instead of retrying").
			WithDetail("operation_id", id).WithDetail("error_code", code)
	}
	tag, err := ex.Exec(ctx, `
		WITH updated AS (
			UPDATE operations
			   SET state = 'pending', finished_at = NULL, error_code = NULL,
			       lease_expires_at = NULL, updated_at = now()
			 WHERE id = $1::uuid AND state = 'failed'
			   AND COALESCE(error_code, '') NOT IN ('unknown', 'ambiguous', 'upstream_timeout')
			RETURNING id, state, fence, attempt
		), logged AS (
			INSERT INTO operation_transitions (operation_id, from_state, to_state, fence, attempt, reason)
			SELECT id, 'failed', state, fence, attempt, 'retry' FROM updated
		)
		SELECT id FROM updated`, id)
	if err != nil {
		return false, fmt.Errorf("storage: retry operation: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	return false, l.notApplied(ctx, ex, id, OpFailed, "retry")
}

// retryInTx is Retry inside a caller's transaction (BeginCall re-opens a failed
// operation and claims it atomically).
func (l *OperationLedger) retryInTx(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	return l.retryExec(ctx, tx, id)
}

// Get returns one operation for polling and diagnostics.
func (l *OperationLedger) Get(ctx context.Context, id string) (*OperationRecord, error) {
	if l == nil || l.store == nil || l.store.pool == nil {
		return nil, fmt.Errorf("storage: operation ledger not configured")
	}
	row := l.store.pool.QueryRow(ctx, `
		SELECT `+operationRecordColumns+`
		FROM operations WHERE id = $1::uuid`, id)
	rec, err := scanOperationRecord(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.New(errs.CodeNotFound, "operation not found").WithDetail("operation_id", id)
		}
		return nil, fmt.Errorf("storage: get operation: %w", err)
	}
	return &rec, nil
}

// Transitions returns the append-only history for one operation, oldest first.
func (l *OperationLedger) Transitions(ctx context.Context, id string) ([]TransitionRecord, error) {
	rows, err := l.store.pool.Query(ctx, `
		SELECT seq, from_state, to_state, fence::text, attempt, reason, occurred_at
		FROM operation_transitions WHERE operation_id = $1::uuid ORDER BY seq`, id)
	if err != nil {
		return nil, fmt.Errorf("storage: list operation transitions: %w", err)
	}
	defer rows.Close()
	var out []TransitionRecord
	for rows.Next() {
		var t TransitionRecord
		if err := rows.Scan(&t.Seq, &t.FromState, &t.ToState, &t.Fence, &t.Attempt, &t.Reason, &t.OccurredAt); err != nil {
			return nil, fmt.Errorf("storage: scan operation transition: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TransitionRecord is one entry of the append-only history.
type TransitionRecord struct {
	Seq        int64
	FromState  string
	ToState    string
	Fence      string
	Attempt    int
	Reason     string
	OccurredAt time.Time
}

// transition runs one guarded statement that also appends its history row, and
// reports whether it applied. A statement that matches no row means the caller
// lost ownership (stale fence, wrong state) and must not treat it as success.
func (l *OperationLedger) transition(ctx context.Context, ex executor, id, what, expectedState, stmt string, args ...any) (bool, error) {
	tag, err := ex.Exec(ctx, stmt, args...)
	if err != nil {
		return false, fmt.Errorf("storage: %s operation: %w", what, err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	return false, l.notApplied(ctx, ex, id, expectedState, what)
}

// notApplied explains why a guarded statement matched no row: the operation is
// gone, it is in the wrong state, or another attempt owns it. A caller must never
// read a no-op as success.
func (l *OperationLedger) notApplied(ctx context.Context, ex executor, id, expectedState, what string) error {
	var state, fence string
	err := ex.QueryRow(ctx,
		`SELECT state, fence::text FROM operations WHERE id = $1::uuid`, id).Scan(&state, &fence)
	if errors.Is(err, pgx.ErrNoRows) {
		return errs.New(errs.CodeNotFound, what+": operation not found").
			WithDetail("operation_id", id)
	}
	if err != nil {
		return fmt.Errorf("storage: classify %s: %w", what, err)
	}
	if state != expectedState {
		return errs.New(errs.CodeIllegalTransition, what+": operation is not in the expected state").
			WithDetail("operation_id", id).WithDetail("state", state)
	}
	return errs.New(errs.CodeConflict, what+": fencing token is stale; another attempt owns this operation").
		WithDetail("operation_id", id).WithDetail("state", state)
}

// prepared is a validated Operation with the derived module and hashes.
type prepared struct {
	idempotencyKey string
	operation      string
	instanceID     string
	module         string
	subjectHash    []byte
	entityType     string
	entityID       string
	amountMinor    *int64
	currency       *string
	argsHash       []byte
	argsHashHex    string
	confirmationID string
}

var validEntityTypes = map[string]struct{}{
	EntityOrder: {}, EntityInvoice: {}, EntityCreditmemo: {}, EntityShipment: {},
	EntityProduct: {}, EntityStock: {}, EntityCategory: {}, EntityCustomer: {},
}

// validModules matches the datasource_instances CHECK constraint.
var validModules = map[string]struct{}{"magento": {}, "cloudflare": {}, "ga4": {}}

// uuidPattern keeps a malformed confirmation id a validation error rather than a
// cast failure deep in the statement.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func prepareOperation(op Operation) (prepared, error) {
	invalid := func(field, why string) (prepared, error) {
		return prepared{}, errs.New(errs.CodeInvalidArgument, "operation is invalid: "+why).
			WithDetail("field", field)
	}
	if strings.TrimSpace(op.IdempotencyKey) == "" || len(op.IdempotencyKey) > 256 {
		return invalid("idempotency_key", "must be 1-256 characters")
	}
	if strings.TrimSpace(op.Operation) == "" {
		return invalid("operation", "is required")
	}
	if strings.TrimSpace(op.InstanceID) == "" {
		return invalid("instance_id", "is required")
	}
	if _, ok := validEntityTypes[op.EntityType]; !ok {
		return invalid("entity_type", "is not a known entity type")
	}
	if strings.TrimSpace(op.EntityID) == "" || len(op.EntityID) > 256 {
		return invalid("entity_id", "must be 1-256 characters")
	}
	if strings.TrimSpace(op.Subject) == "" {
		return invalid("subject", "is required")
	}
	if (op.AmountMinor == nil) != (op.Currency == "") {
		return invalid("amount", "amount_minor and currency must be set together")
	}
	if op.AmountMinor != nil {
		if len(op.Currency) != 3 || strings.ToUpper(op.Currency) != op.Currency {
			return invalid("currency", "must be an ISO-4217 code such as USD")
		}
	}
	if op.ConfirmationID != "" && !uuidPattern.MatchString(op.ConfirmationID) {
		return invalid("confirmation_id", "must be a UUID")
	}
	rawHash, err := hex.DecodeString(op.ArgsHash)
	if err != nil || len(rawHash) != 32 {
		return invalid("args_hash", "must be the hex sha256 of the canonical arguments")
	}
	module := op.Module
	if module == "" {
		module = strings.SplitN(op.Operation, ".", 2)[0]
	}
	if _, ok := validModules[module]; !ok {
		return invalid("module", "cannot be derived from the operation name; set it explicitly")
	}
	p := prepared{
		idempotencyKey: windowedDerivedKey(op.IdempotencyKey, ledgerClock()),
		operation:      op.Operation,
		instanceID:     op.InstanceID,
		module:         module,
		subjectHash:    subjectHash(op.Subject),
		entityType:     op.EntityType,
		entityID:       op.EntityID,
		amountMinor:    op.AmountMinor,
		argsHash:       rawHash,
		argsHashHex:    op.ArgsHash,
		confirmationID: op.ConfirmationID,
	}
	if op.Currency != "" {
		currency := op.Currency
		p.currency = &currency
	}
	return p, nil
}

// isAmbiguousCode reports whether an error code means "we do not know", which must
// never be recorded as a definite failure.
func isAmbiguousCode(code string) bool {
	switch strings.ToLower(code) {
	case "unknown", "ambiguous", "upstream_timeout":
		return true
	}
	return false
}

// jsonArg turns an empty result into SQL NULL rather than an empty jsonb value.
func jsonArg(result json.RawMessage) any {
	if len(result) == 0 {
		return nil
	}
	return []byte(result)
}

// ---------------------------------------------------------------------------
// Write-call gate
//
// The middleware calls BeginCall before a write tool runs and FinishCall after
// it returns. Those two calls are the only ledger surface the MCP layer needs:
// the transition table stays here.
// ---------------------------------------------------------------------------

// GateDecision tells the middleware what to do with a write call.
type GateDecision string

const (
	// GateProceed: this caller owns the operation; run the tool, then FinishCall.
	GateProceed GateDecision = "proceed"
	// GateRetry: the previous attempt failed definitively; call Retry then
	// BeginCall again (the middleware does this in one step).
	GateRetry GateDecision = "retry"
	// GateInProgress: another attempt owns it; do not call upstream.
	GateInProgress GateDecision = "in_progress"
	// GateReplay: it already succeeded; return the stored result.
	GateReplay GateDecision = "replay"
	// GateUnknown: the outcome is ambiguous; never retry, only reconcile.
	GateUnknown GateDecision = "unknown"
)

// GateRequest is one write-tool call as the middleware sees it.
type GateRequest struct {
	Operation      string
	InstanceID     string
	Subject        string
	ArgsHash       string
	IdempotencyKey string
	// ConfirmationID, when set, is consumed in the same transaction as the
	// ledger row: a crash cannot burn an approval without recording why.
	ConfirmationID string
	EntityType     string
	EntityID       string
	AmountMinor    *int64
	Currency       string
	Module         string
}

// GateTicket is the ownership token for the attempt the caller now owns.
type GateTicket struct {
	OperationID  string
	Fence        string
	Attempt      int
	Decision     GateDecision
	Replayed     bool
	ReplayResult json.RawMessage
	UpstreamRef  string
}

// GateOutcome is the result of the tool call.
type GateOutcome struct {
	// State is OpSucceeded, OpFailed or OpUnknown.
	State       string
	ErrorCode   string
	UpstreamRef string
	Result      json.RawMessage
	RetryAfter  time.Duration
	// Audit, when set, is written in the same transaction as the transition, so
	// an outcome can never be lost while the intent persists (REL-16).
	Audit *audit.Event
}

// BeginCall creates (or re-opens) the operation for a write call and, when a
// confirmation id is present, burns the approval in the same transaction.
//
// The returned error is the caller's instruction: conflict (in flight),
// operation_unknown (poll, never retry), idempotency_key_reuse (different
// arguments under the same key), or a confirmation error.
func (l *OperationLedger) BeginCall(ctx context.Context, req GateRequest) (GateTicket, error) {
	if l == nil || l.store == nil || l.store.pool == nil {
		return GateTicket{}, fmt.Errorf("storage: operation ledger not configured")
	}
	op := Operation{
		IdempotencyKey: req.IdempotencyKey,
		Operation:      req.Operation,
		InstanceID:     req.InstanceID,
		Module:         req.Module,
		Subject:        req.Subject,
		EntityType:     req.EntityType,
		EntityID:       req.EntityID,
		AmountMinor:    req.AmountMinor,
		Currency:       req.Currency,
		ArgsHash:       req.ArgsHash,
		ConfirmationID: req.ConfirmationID,
	}
	prepared, err := prepareOperation(op)
	if err != nil {
		return GateTicket{}, err
	}
	if req.ConfirmationID != "" && req.IdempotencyKey == "" {
		return GateTicket{}, errs.New(errs.CodeInvalidArgument,
			"a confirmed write requires an idempotency key")
	}

	tx, err := l.store.pool.Begin(ctx)
	if err != nil {
		return GateTicket{}, fmt.Errorf("storage: begin operation: %w", err)
	}
	// Every path that does not execute rolls back, so a replay, a conflict or an
	// unknown outcome can never burn the approval.
	defer func() { _ = tx.Rollback(ctx) }()

	result, beginErr := l.beginInTx(ctx, tx, prepared)
	ticket := GateTicket{
		OperationID:  result.OperationID,
		Fence:        result.Fence,
		Attempt:      result.Attempt,
		Decision:     GateDecision(result.Decision),
		Replayed:     result.Replayed,
		ReplayResult: result.Result,
		UpstreamRef:  result.UpstreamRef,
	}
	switch ticket.Decision {
	case GateReplay:
		// Nothing to consume: the operation already succeeded.
		return ticket, nil
	case GateProceed, GateRetry:
		// Both paths claim the row here, so every ticket that will run carries a
		// fence and FinishCall can write the terminal transition unconditionally.
		// A retried operation is re-opened (T11) first.
		if ticket.Decision == GateRetry {
			if applied, err := l.retryInTx(ctx, tx, ticket.OperationID); err != nil {
				return ticket, err
			} else if !applied {
				return ticket, errs.New(errs.CodeConflict,
					"the operation is no longer retryable").
					WithDetail("operation_id", ticket.OperationID)
			}
		}
		execution, claimed, err := l.executeInTx(ctx, tx, ticket.OperationID, callLease)
		if err != nil {
			return ticket, err
		}
		if !claimed {
			return ticket, errs.New(errs.CodeConflict,
				"another attempt already owns this operation").
				WithDetail("operation_id", ticket.OperationID)
		}
		ticket.Decision = GateProceed
		ticket.Fence = execution.Fence
		ticket.Attempt = execution.Attempt
	default:
		// InProgress / Unknown, or a begin failure: the typed error (if any) is
		// the instruction; never nil for a decision that will not execute.
		if beginErr == nil {
			beginErr = errs.New(errs.CodeConflict, "operation cannot be started").
				WithDetail("operation_id", ticket.OperationID).
				WithDetail("state", result.State)
		}
		return ticket, beginErr
	}

	if req.ConfirmationID != "" {
		if err := l.confirms.consumeInTx(ctx, tx, req.ConfirmationID, req.Subject,
			req.Operation, req.InstanceID, req.ArgsHash, time.Now().UTC()); err != nil {
			return ticket, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return GateTicket{}, fmt.Errorf("storage: begin commit: %w", err)
	}
	return ticket, nil
}

// callLease bounds how long the middleware may hold an operation. It is longer
// than any declared tool timeout so a slow tool is not recovered mid-flight.
const callLease = 2 * time.Minute

// FinishCall records the outcome of the attempt the ticket owns and, when
// supplied, the audit outcome, in one transaction.
func (l *OperationLedger) FinishCall(ctx context.Context, ticket GateTicket, out GateOutcome) error {
	if l == nil || l.store == nil || l.store.pool == nil {
		return fmt.Errorf("storage: operation ledger not configured")
	}
	tx, err := l.store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("storage: finish operation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// One finish implementation for both the fresh and the retried path: BeginCall
	// claimed the row and returned a fence, so the terminal transition is always a
	// fenced write. (Self-ledgered tools never reach FinishCall: they are excluded
	// from BeginCall by Options.SelfLedgeredTools and drive their own transitions.)
	if ticket.Fence == "" {
		return errs.New(errs.CodeInternal, "operation ticket carries no fence").
			WithDetail("operation_id", ticket.OperationID)
	}

	var applied bool
	switch out.State {
	case OpSucceeded:
		applied, err = l.succeedExec(ctx, tx, ticket.OperationID, ticket.Fence, out.UpstreamRef, out.Result, false)
	case OpFailed:
		applied, err = l.failExec(ctx, tx, ticket.OperationID, ticket.Fence, out.ErrorCode, false)
	case OpUnknown:
		applied, err = l.unknownExec(ctx, tx, ticket.OperationID, ticket.Fence, out.ErrorCode, out.RetryAfter, false)
	default:
		return errs.New(errs.CodeInvalidArgument, "unknown operation outcome").
			WithDetail("state", out.State)
	}
	if err != nil {
		return err
	}
	if !applied {
		return errs.New(errs.CodeConflict, "operation is no longer owned by this attempt").
			WithDetail("operation_id", ticket.OperationID)
	}
	if out.Audit != nil {
		if err := l.auditor.RecordInTx(ctx, tx, *out.Audit); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("storage: finish commit: %w", err)
	}
	return nil
}

// MakeReconcilable brings an unknown operation forward so the next reconciler
// pass claims it. It is the operator's "I have looked at this, reconcile it now"
// action, and it is what a test uses instead of waiting out the backoff.
func (l *OperationLedger) MakeReconcilable(ctx context.Context, id string) (bool, error) {
	tag, err := l.store.pool.Exec(ctx, `
		UPDATE operations SET reconcile_after = now(), updated_at = now()
		WHERE id = $1::uuid AND state = 'unknown'`, id)
	if err != nil {
		return false, fmt.Errorf("storage: make operation reconcilable: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// Escalate stops the reconciler from retrying an operation forever: the row stays
// unknown (its outcome genuinely is unknown) but reconcile_after becomes
// 'infinity', so no automatic pass will read the upstream for it again.
//
// Escalation is not the end of the road any more. The row keeps
// error_code='needs_manual_reconciliation', which is what List's NeedsAttention
// filter selects and what Dispose acts on: a human either records a finding
// (applied / not_applied) or re-queues it, which restarts the attempt budget.
// Before that exit existed, eleven production order.create rows had nowhere to go.
func (l *OperationLedger) Escalate(ctx context.Context, id, fence, reason string) (bool, error) {
	if reason == "" {
		reason = "escalated"
	}
	tag, err := l.store.pool.Exec(ctx, `
		WITH updated AS (
			UPDATE operations
			   SET state = 'unknown', lease_expires_at = NULL,
			       error_code = 'needs_manual_reconciliation',
			       reconcile_after = 'infinity', updated_at = now()
			 WHERE id = $1::uuid AND fence = $2::uuid AND state = 'reconciling'
			RETURNING id, state, fence, attempt
		), logged AS (
			INSERT INTO operation_transitions (operation_id, from_state, to_state, fence, attempt, reason)
			SELECT id, 'reconciling', state, fence, attempt, 'escalated' FROM updated
		)
		SELECT id FROM updated`, id, fence)
	if err != nil {
		return false, fmt.Errorf("storage: escalate operation: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ---------------------------------------------------------------------------
// Disposition of operations no automatic pass will touch again
//
// An unknown outcome means "the gateway could not tell whether the mutation was
// applied". The reconciler retries the upstream read a bounded number of times and
// then escalates (Escalate), which pushes reconcile_after to infinity. Until this
// section existed the row then had no exit at all: nothing in the MCP surface
// could read it, and nothing could record what a human decided (INT-07). Eleven
// production order.create rows sat in that state.
//
// The disposition is deliberately a *recorded judgement*, not a delete: the caller
// must say which of the three findings it reached, and why. "Assume it never
// happened" is not one of the options, because it would turn an unknown outcome
// into a silently duplicated order the moment the upstream had in fact applied it.
// ---------------------------------------------------------------------------

// DisposeJudgment is the finding an operator reached about an unknown operation.
type DisposeJudgment string

const (
	// DisposedApplied: the upstream was read and the mutation is there. The row
	// becomes succeeded so a replay returns it instead of writing again.
	DisposedApplied DisposeJudgment = "applied"
	// DisposedNotApplied: the upstream was read and the mutation is not there. The
	// row becomes failed, which also frees a derived idempotency key.
	DisposedNotApplied DisposeJudgment = "not_applied"
	// DisposedRequeue: the finding is not in yet. The row stays unknown but its
	// reconciliation budget restarts, so the reconciler reads upstream again
	// instead of leaving the row at reconcile_after = infinity forever.
	DisposedRequeue DisposeJudgment = "requeue"
)

// Valid reports whether j is one of the documented judgements.
func (j DisposeJudgment) Valid() bool {
	switch j {
	case DisposedApplied, DisposedNotApplied, DisposedRequeue:
		return true
	}
	return false
}

// EscalatedErrorCode marks a row the reconciler handed to a human.
const EscalatedErrorCode = "needs_manual_reconciliation"

// Error codes a disposition writes, so a resolved row says how it was resolved.
const (
	disposedAppliedCode    = "manual_applied"
	disposedNotAppliedCode = "manual_not_applied"
)

// OperationFilter selects rows for a status query. The zero value means "the most
// recent rows, whatever their state".
type OperationFilter struct {
	InstanceID string
	Operation  string
	State      string
	// NeedsAttention restricts the query to rows the reconciler will never look at
	// again: an escalated unknown, or one whose backoff is unreachable.
	NeedsAttention bool
	// Limit is the page size; 0 means 50 and the maximum is 200.
	Limit int
	// Cursor is the opaque value from a previous page's NextCursor. Rows are
	// ordered newest first, so a cursor continues strictly after the last row.
	Cursor string
}

// OperationPage is one page of a status query.
type OperationPage struct {
	Operations []OperationRecord
	// Total is how many rows match the filter, independent of the page.
	Total int
	// Truncated is true when more rows match than this page carries.
	Truncated bool
	// NextCursor continues the listing; empty when this is the last page.
	NextCursor string
}

const (
	defaultOperationPageSize = 50
	maxOperationPageSize     = 200
)

// attentionPredicate is the SQL for "no automatic pass will touch this again".
const attentionPredicate = `(state = 'unknown' AND (
		COALESCE(error_code, '') = '` + EscalatedErrorCode + `'
		OR reconcile_after IS NULL
		OR reconcile_after = 'infinity'))`

// List returns a page of operations for polling and for finding the rows that
// need a human. It is read-only.
func (l *OperationLedger) List(ctx context.Context, f OperationFilter) (*OperationPage, error) {
	if l == nil || l.store == nil || l.store.pool == nil {
		return nil, fmt.Errorf("storage: operation ledger not configured")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultOperationPageSize
	}
	if limit > maxOperationPageSize {
		limit = maxOperationPageSize
	}
	where := []string{"TRUE"}
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if f.InstanceID != "" {
		add("instance_id = $%d", f.InstanceID)
	}
	if f.Operation != "" {
		add("operation = $%d", f.Operation)
	}
	if f.State != "" {
		add("state = $%d", f.State)
	}
	if f.NeedsAttention {
		where = append(where, attentionPredicate)
	}
	filter := strings.Join(where, " AND ")

	var total int
	if err := l.store.pool.QueryRow(ctx,
		`SELECT count(*) FROM operations WHERE `+filter, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("storage: count operations: %w", err)
	}

	page := []string{filter}
	if f.Cursor != "" {
		after, err := decodeOperationCursor(f.Cursor)
		if err != nil {
			return nil, err
		}
		args = append(args, after.createdAt, after.id)
		page = append(page, fmt.Sprintf("(created_at, id) < ($%d::timestamptz, $%d::uuid)", len(args)-1, len(args)))
	}
	args = append(args, limit+1)
	rows, err := l.store.pool.Query(ctx, `
		SELECT `+operationRecordColumns+`
		FROM operations
		WHERE `+strings.Join(page, " AND ")+`
		ORDER BY created_at DESC, id DESC
		LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("storage: list operations: %w", err)
	}
	defer rows.Close()
	out := make([]OperationRecord, 0, limit)
	for rows.Next() {
		rec, err := scanOperationRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list operations: %w", err)
	}
	result := &OperationPage{Operations: out, Total: total}
	if len(out) > limit {
		result.Operations = out[:limit]
		result.Truncated = true
		last := result.Operations[len(result.Operations)-1]
		result.NextCursor = encodeOperationCursor(last.CreatedAt, last.ID)
	}
	return result, nil
}

// operationCursor is the keyset position of the last row of a page.
type operationCursor struct {
	createdAt time.Time
	id        string
}

func encodeOperationCursor(createdAt time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeOperationCursor(cursor string) (operationCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return operationCursor{}, errs.New(errs.CodeInvalidArgument, "cursor is not a value this tool issued").
			WithDetail("field", "cursor")
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return operationCursor{}, errs.New(errs.CodeInvalidArgument, "cursor is not a value this tool issued").
			WithDetail("field", "cursor")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil || !uuidPattern.MatchString(id) {
		return operationCursor{}, errs.New(errs.CodeInvalidArgument, "cursor is not a value this tool issued").
			WithDetail("field", "cursor")
	}
	return operationCursor{createdAt: createdAt, id: id}, nil
}

// operationRecordColumns is the projection Get and List share, so a row read by
// one and a row filtered by the other cannot disagree.
//
// reconcile_after can literally be 'infinity' (Escalate writes it), which no
// *time.Time can hold: it is selected as NULL plus an explicit flag, and the flag
// is what needs_attention is computed from.
const operationRecordColumns = `id::text, idempotency_key, operation, instance_id, entity_type, entity_id, state,
	amount_minor, COALESCE(currency, ''), fence::text, attempt,
	COALESCE(upstream_ref, ''), COALESCE(result, 'null'::jsonb), COALESCE(error_code, ''),
	reconcile_count,
	CASE WHEN reconcile_after = 'infinity' THEN NULL ELSE reconcile_after END,
	COALESCE(reconcile_after = 'infinity', false),
	created_at, finished_at`

// rowScanner is the subset of pgx.Rows scanning needs.
type rowScanner interface{ Scan(dest ...any) error }

func scanOperationRecord(row rowScanner) (OperationRecord, error) {
	var rec OperationRecord
	var amount *int64
	var reconcileNever bool
	if err := row.Scan(&rec.ID, &rec.IdempotencyKey, &rec.Operation, &rec.InstanceID, &rec.EntityType,
		&rec.EntityID, &rec.State, &amount, &rec.Currency, &rec.Fence, &rec.Attempt,
		&rec.UpstreamRef, &rec.Result, &rec.ErrorCode, &rec.ReconcileCount,
		&rec.ReconcileAfter, &reconcileNever, &rec.CreatedAt, &rec.FinishedAt); err != nil {
		return OperationRecord{}, fmt.Errorf("storage: scan operation: %w", err)
	}
	rec.AmountMinor = amount
	rec.NeedsAttention = needsAttention(rec, reconcileNever)
	return rec, nil
}

// needsAttention mirrors attentionPredicate in Go, so a single row fetched by Get
// agrees with a filtered list.
func needsAttention(rec OperationRecord, reconcileNever bool) bool {
	if rec.State != OpUnknown {
		return false
	}
	return rec.ErrorCode == EscalatedErrorCode || reconcileNever || rec.ReconcileAfter == nil
}

// Dispose records what a human found out about an unknown operation and moves the
// row to the matching state. Only an unknown row can be disposed: a pending or
// executing row is owned by a live attempt, and a reconciling row is owned by the
// reconciler's lease.
//
// note is required and is stored in the transition history, because the whole
// point is that a person looked and this is what they saw.
func (l *OperationLedger) Dispose(ctx context.Context, id string, judgment DisposeJudgment, note, upstreamRef string, event *audit.Event) (bool, error) {
	if l == nil || l.store == nil || l.store.pool == nil {
		return false, fmt.Errorf("storage: operation ledger not configured")
	}
	if !judgment.Valid() {
		return false, errs.New(errs.CodeInvalidArgument,
			"judgment must be applied, not_applied or requeue").
			WithDetail("field", "judgment")
	}
	note = strings.TrimSpace(note)
	if note == "" {
		return false, errs.New(errs.CodeInvalidArgument,
			"a disposition must record what the operator checked; note is required").
			WithDetail("field", "note")
	}
	if !uuidPattern.MatchString(id) {
		return false, errs.New(errs.CodeInvalidArgument, "operation_id must be a UUID").
			WithDetail("field", "operation_id")
	}
	tx, err := l.store.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("storage: dispose operation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The state check is the answer a caller needs: disposing anything but an
	// unknown outcome is either a live attempt's row or an already-decided one.
	var state string
	err = tx.QueryRow(ctx,
		`SELECT state FROM operations WHERE id = $1::uuid FOR UPDATE`, id).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, errs.New(errs.CodeNotFound, "operation not found").WithDetail("operation_id", id)
	}
	if err != nil {
		return false, fmt.Errorf("storage: dispose operation: %w", err)
	}
	if state != OpUnknown {
		return false, errs.New(errs.CodeConflict,
			"only an operation whose outcome is unknown can be disposed").
			WithDetail("operation_id", id).
			WithDetail("state", state)
	}

	var applied bool
	switch judgment {
	case DisposedApplied:
		applied, err = l.transition(ctx, tx, id, "dispose_applied", OpUnknown, `
			WITH updated AS (
				UPDATE operations
				   SET state = 'succeeded', finished_at = now(), lease_expires_at = NULL,
				       error_code = '`+disposedAppliedCode+`',
				       upstream_ref = COALESCE(NULLIF($2, ''), upstream_ref),
				       reconcile_after = NULL, updated_at = now()
				 WHERE id = $1::uuid AND state = 'unknown'
				RETURNING id, state, fence, attempt
			), logged AS (
				INSERT INTO operation_transitions (operation_id, from_state, to_state, fence, attempt, reason)
				SELECT id, 'unknown', state, fence, attempt, 'disposed_applied: ' || $3 FROM updated
			)
			SELECT id FROM updated`, id, upstreamRef, note)
	case DisposedNotApplied:
		applied, err = l.transition(ctx, tx, id, "dispose_not_applied", OpUnknown, `
			WITH updated AS (
				UPDATE operations
				   SET state = 'failed', finished_at = now(), lease_expires_at = NULL,
				       error_code = '`+disposedNotAppliedCode+`',
				       reconcile_after = NULL, updated_at = now(),
				       idempotency_key = `+releasedKeySQL()+`
				 WHERE id = $1::uuid AND state = 'unknown'
				RETURNING id, state, fence, attempt
			), logged AS (
				INSERT INTO operation_transitions (operation_id, from_state, to_state, fence, attempt, reason)
				SELECT id, 'unknown', state, fence, attempt, 'disposed_not_applied: ' || $2 FROM updated
			)
			SELECT id FROM updated`, id, note)
	default: // DisposedRequeue
		applied, err = l.transition(ctx, tx, id, "dispose_requeue", OpUnknown, `
			WITH updated AS (
				UPDATE operations
				   SET reconcile_after = now(), reconcile_count = 0,
				       error_code = NULL, lease_expires_at = NULL, updated_at = now()
				 WHERE id = $1::uuid AND state = 'unknown'
				RETURNING id, state, fence, attempt
			), logged AS (
				INSERT INTO operation_transitions (operation_id, from_state, to_state, fence, attempt, reason)
				SELECT id, 'unknown', state, fence, attempt, 'disposed_requeue: ' || $2 FROM updated
			)
			SELECT id FROM updated`, id, note)
	}
	if err != nil || !applied {
		return applied, err
	}
	if event != nil {
		if err := l.auditor.RecordInTx(ctx, tx, *event); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("storage: dispose commit: %w", err)
	}
	return true, nil
}

// markReplayed adds the replay marker to a recorded result so a replayed call is
// visible in structuredContent and not only in the text fallback. A recorded
// result that is not a JSON object is left alone: there is nowhere to put it, and
// the text fallback still says so.
func markReplayed(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return raw
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return raw
	}
	if existing, ok := fields["replayed"]; ok && string(existing) == "true" {
		return raw
	}
	fields["replayed"] = json.RawMessage("true")
	marked, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return marked
}
