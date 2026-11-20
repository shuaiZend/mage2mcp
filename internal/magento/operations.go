// SPDX-License-Identifier: Apache-2.0

package magento

// Operation ledger client for the money-moving tools.
//
// The problem: after a timeout, a transport error or a 5xx the gateway cannot
// tell "the mutation did not happen" from "it may have happened". Retrying the
// first is correct; retrying the second duplicates a charge, a refund or an
// order. The durable operation ledger (internal/storage/operation_ledger.go,
// design in docs/audit/reliability-review.md §3) records the intent before the
// call and the outcome after it, and has a state for "we do not know".
//
// This file is deliberately thin: it derives the idempotency key, hashes the
// canonical arguments with the confirmation flow's function, carries money as
// integer minor units, starts/ends a ledger operation, and classifies an
// upstream error as applied / not-applied / ambiguous. It does not re-implement
// the state machine — every transition is a call into the ledger.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

// OperationLedger is the slice of the durable ledger the tools use. It is an
// interface so a contract test can install a deterministic fake and so the
// PostgreSQL ledger stays the only implementation in production.
type OperationLedger interface {
	Begin(ctx context.Context, op storage.Operation) (storage.BeginResult, error)
	Execute(ctx context.Context, id string, lease time.Duration) (storage.Execution, bool, error)
	Succeed(ctx context.Context, id, fence, upstreamRef string, result json.RawMessage) (bool, error)
	Fail(ctx context.Context, id, fence, errorCode string) (bool, error)
	Unknown(ctx context.Context, id, fence, errorCode string, retryAfter time.Duration) (bool, error)
	Get(ctx context.Context, id string) (*storage.OperationRecord, error)
}

// operationLease is the ownership window of one attempt. It is longer than the
// per-request HTTP timeout (30s) so a slow call cannot lose its lease mid-flight.
const operationLease = 2 * time.Minute

var (
	operationLedgerMu sync.RWMutex
	operationLedger   OperationLedger
)

// ConfigureOperations installs the durable ledger used by the money-moving
// tools. Until it is called those tools fail closed: an unguarded capture, refund
// or order placement is worse than a disabled tool. The server wires it from the
// PostgreSQL store; a nil argument uninstalls it.
func ConfigureOperations(ledger OperationLedger) {
	operationLedgerMu.Lock()
	defer operationLedgerMu.Unlock()
	operationLedger = ledger
}

// OperationsConfigured reports whether a ledger is installed.
func OperationsConfigured() bool {
	operationLedgerMu.RLock()
	defer operationLedgerMu.RUnlock()
	return operationLedger != nil
}

func configuredOperations() (*Operations, error) {
	operationLedgerMu.RLock()
	ledger := operationLedger
	operationLedgerMu.RUnlock()
	if ledger == nil {
		return nil, errs.New(errs.CodeUnsupportedCapability,
			"the operation ledger is not configured, so money-moving tools are disabled")
	}
	return &Operations{ledger: ledger}, nil
}

// Operations records money-moving calls in the durable ledger.
type Operations struct {
	ledger OperationLedger
}

// Money is an amount in integer minor units plus an ISO-4217 currency code. No
// float64 ever enters the ledger path.
type Money struct {
	Minor    int64
	Currency string
}

// MoneyFromDecimal converts a decimal amount ("129.99") into minor units without
// going through a float. A third or fourth fraction digit is rounded half away
// from zero, which matches how Magento stores currency amounts.
func MoneyFromDecimal(text, currency string) (Money, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if len(currency) != 3 {
		return Money{}, invalidf("currency must be an ISO-4217 code such as USD")
	}
	amount := strings.TrimSpace(text)
	if amount == "" {
		return Money{}, invalidf("amount is required")
	}
	negative := strings.HasPrefix(amount, "-")
	amount = strings.TrimPrefix(amount, "-")
	whole, fraction, _ := strings.Cut(amount, ".")
	if whole == "" {
		whole = "0"
	}
	units, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || units < 0 {
		return Money{}, invalidf("amount %q is not a non-negative decimal", text)
	}
	var cents int64
	if fraction != "" {
		roundUp := false
		if len(fraction) > 2 {
			roundUp = fraction[2] >= '5'
			fraction = fraction[:2]
		}
		for len(fraction) < 2 {
			fraction += "0"
		}
		cents, err = strconv.ParseInt(fraction, 10, 64)
		if err != nil || cents < 0 {
			return Money{}, invalidf("amount %q is not a decimal", text)
		}
		if roundUp {
			cents++
			if cents == 100 {
				cents = 0
				units++
			}
		}
	}
	minor := units*100 + cents
	if negative {
		minor = -minor
	}
	return Money{Minor: minor, Currency: currency}, nil
}

// moneyFromAmount converts a Magento JSON amount (decoded as float64) through its
// two-decimal text form. This is the only float-to-money boundary; everything
// after it is integer minor units.
func moneyFromAmount(amount float64, currency string) (Money, error) {
	return MoneyFromDecimal(strconv.FormatFloat(amount, 'f', 2, 64), currency)
}

// OperationRequest declares one money-moving call to the ledger.
type OperationRequest struct {
	Tool     string // the MCP tool name, e.g. magento.invoice.capture
	Instance string // configured instance id (or a stable derived id)
	Subject  string // authenticated caller; hashed by the ledger
	// IdempotencyKey is optional and derived from the arguments when empty.
	//
	// It is NOT caller-scoped: the ledger's unique key is
	// (instance, operation, idempotency_key) and its replay predicate does not
	// include the subject, so a caller who learns another caller's key could
	// replay (or block) that operation. Treat the key as a secret: use an
	// unguessable value such as a UUID rather than a predictable one.
	IdempotencyKey string
	EntityType     string // storage.EntityOrder, storage.EntityInvoice, ...
	EntityID       string // stable handle known before the mutation
	Amount         *Money // money in minor units; nil when Magento computes it
	Args           any    // canonical arguments, hashed for replay detection
	ConfirmationID string // the approval that authorised the call, when known
}

// OperationRun is one owned attempt: the fence every terminal write must present.
type OperationRun struct {
	ops   *Operations
	id    string
	fence string
}

// ID returns the ledger operation id.
func (r *OperationRun) ID() string { return r.id }

// Succeed records the applied mutation and its replayable result.
func (r *OperationRun) Succeed(ctx context.Context, upstreamRef string, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return errs.New(errs.CodeInternal, "cannot encode the operation result").WithCause(err)
	}
	applied, err := r.ops.ledger.Succeed(ctx, r.id, r.fence, upstreamRef, raw)
	if err != nil {
		return err
	}
	if !applied {
		return errs.New(errs.CodeConflict,
			"the mutation succeeded but the operation record was taken over by another attempt; reconcile before acting").
			WithDetail("operation_id", r.id)
	}
	return nil
}

// Fail records a mutation that is proven not to have been applied.
func (r *OperationRun) Fail(ctx context.Context, errorCode string) error {
	applied, err := r.ops.ledger.Fail(ctx, r.id, r.fence, errorCode)
	if err != nil {
		return err
	}
	if !applied {
		return errs.New(errs.CodeConflict,
			"the operation record was taken over by another attempt; its outcome is unknown").
			WithDetail("operation_id", r.id)
	}
	return nil
}

// Unknown records an ambiguous outcome. It is never a licence to retry.
func (r *OperationRun) Unknown(ctx context.Context, errorCode string) error {
	applied, err := r.ops.ledger.Unknown(ctx, r.id, r.fence, errorCode, time.Minute)
	if err != nil {
		return err
	}
	if !applied {
		return errs.New(errs.CodeConflict,
			"the operation record was taken over by another attempt; its outcome is unknown").
			WithDetail("operation_id", r.id)
	}
	return nil
}

// Begin starts (or resumes) an operation and, when it is new, executes it.
//
// It returns the run to use for the upstream call, the ledger decision, and an
// error. A nil run with a nil error means the ledger replayed a recorded success:
// the caller returns begun.Result without touching Magento. Every other decision
// that is not "proceed" comes back as a typed error:
//
//	replay        -> nil run, begun.Decision == DecisionReplay
//	failed before -> the recorded failure (never an automatic retry)
//	in progress   -> conflict, poll operation_id
//	unknown       -> operation_unknown, poll and never retry
func (o *Operations) Begin(ctx context.Context, req OperationRequest) (*OperationRun, *storage.BeginResult, error) {
	argsHash, err := operationArgsHash(req.Args)
	if err != nil {
		return nil, nil, err
	}
	key := strings.TrimSpace(req.IdempotencyKey)
	if key == "" {
		key = storage.DeriveKey(req.Subject, argsHash)
	}
	op := storage.Operation{
		IdempotencyKey: key,
		Operation:      req.Tool,
		InstanceID:     req.Instance,
		Subject:        req.Subject,
		EntityType:     req.EntityType,
		EntityID:       req.EntityID,
		ArgsHash:       argsHash,
		ConfirmationID: req.ConfirmationID,
	}
	if req.Amount != nil {
		amount := req.Amount.Minor
		op.AmountMinor = &amount
		op.Currency = req.Amount.Currency
	}
	begun, err := o.ledger.Begin(ctx, op)
	if err != nil {
		return nil, &begun, withOperationID(err, begun.OperationID)
	}
	switch begun.Decision {
	case storage.DecisionProceed:
		execution, applied, err := o.ledger.Execute(ctx, begun.OperationID, operationLease)
		if err != nil {
			return nil, &begun, withOperationID(err, begun.OperationID)
		}
		if !applied {
			return nil, &begun, errs.New(errs.CodeConflict,
				"another attempt already owns this operation; poll it instead of retrying").
				WithDetail("operation_id", begun.OperationID)
		}
		return &OperationRun{ops: o, id: begun.OperationID, fence: execution.Fence}, &begun, nil
	case storage.DecisionReplay:
		return nil, &begun, nil
	case storage.DecisionRetry:
		// A previous attempt failed definitively. Do not retry automatically: the
		// caller gets the recorded failure and decides, with a new key if the
		// request itself changed.
		code := errs.Code(begun.ErrorCode)
		if code == "" {
			code = errs.CodeInternal
		}
		return nil, &begun, errs.New(code,
			"the operation previously failed and was not retried; start a new operation to try again").
			WithDetail("operation_id", begun.OperationID).
			WithDetail("state", begun.State)
	case storage.DecisionInProgress:
		return nil, &begun, errs.New(errs.CodeConflict,
			"the operation is already in progress; poll it instead of retrying").
			WithDetail("operation_id", begun.OperationID).
			WithDetail("state", begun.State)
	default: // DecisionUnknown
		return nil, &begun, errs.New(errs.CodeOperationUnknown,
			"the operation outcome is unknown; poll it and never retry").
			WithDetail("operation_id", begun.OperationID).
			WithDetail("state", begun.State)
	}
}

// Replay decodes a recorded result into out.
func Replay(begun *storage.BeginResult, out any) error {
	if begun == nil || len(begun.Result) == 0 {
		return errs.New(errs.CodeInternal, "the recorded operation has no replayable result")
	}
	if err := json.Unmarshal(begun.Result, out); err != nil {
		return errs.New(errs.CodeInternal, "the recorded operation result is unreadable").WithCause(err)
	}
	return nil
}

// operationArgsHash hashes the canonical arguments with the confirmation flow's
// function, so a ledger row and a confirmation row for the same call agree.
func operationArgsHash(args any) (string, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return "", errs.New(errs.CodeInternal, "cannot encode the operation arguments").WithCause(err)
	}
	hash, err := storage.ArgsHash(raw)
	if err != nil {
		return "", err
	}
	return hash, nil
}

// withOperationID attaches the operation id to a typed error when it has one.
func withOperationID(err error, operationID string) error {
	if operationID == "" {
		return err
	}
	var typed *errs.Error
	if errors.As(err, &typed) {
		_ = typed.WithDetail("operation_id", operationID)
	}
	return err
}

// operationClass is how an upstream error maps onto the ledger.
type operationClass int

const (
	// operationApplied: Magento confirmed the mutation.
	operationApplied operationClass = iota
	// operationNotApplied: Magento proved the mutation did not happen.
	operationNotApplied
	// operationAmbiguous: the mutation may or may not have happened.
	operationAmbiguous
)

// classifyOperationError maps an upstream error onto the ledger's three outcomes.
// A nil error is applied; a 4xx is a rejection Magento made before doing the
// work; a timeout, a transport failure, a 5xx or an unclassifiable error is
// ambiguous.
func classifyOperationError(err error) operationClass {
	if err == nil {
		return operationApplied
	}
	switch errs.CodeOf(err) {
	case errs.CodeInvalidArgument, errs.CodeValidationFailed, errs.CodeNotFound,
		errs.CodeConflict, errs.CodePermissionDenied, errs.CodeUnauthenticated,
		errs.CodeRateLimited, errs.CodeUnsupportedCapability:
		return operationNotApplied
	default:
		return operationAmbiguous
	}
}

// operationUnknownError is the one error an ambiguous money mutation returns. It
// is never retryable: the caller must poll the operation, not repeat it.
func operationUnknownError(operationID, action, upstreamCode string) *errs.Error {
	return errs.New(errs.CodeOperationUnknown,
		"the "+action+" outcome is unknown: Magento did not confirm it, so the operation was recorded as unknown. "+
			"Do not retry: poll the operation until it is reconciled").
		WithDetail("operation_id", operationID).
		WithDetail("upstream_code", upstreamCode).
		WithDetail("next", "poll the operation status")
}

// operationInstanceID returns the instance identity recorded in the ledger.
// The configured instance id is used when the caller named one; otherwise a
// stable id is derived from the endpoint and store code, which keeps idempotency
// working across replicas of a single-instance deployment.
func (s *Service) operationInstanceID(instance string) string {
	if trimmed := strings.TrimSpace(instance); trimmed != "" {
		return trimmed
	}
	if s == nil || s.client == nil {
		return "magento-unknown"
	}
	sum := sha256.Sum256([]byte(s.client.BaseURL + "\x00" + s.client.StoreCode))
	return "magento-" + hex.EncodeToString(sum[:8])
}
