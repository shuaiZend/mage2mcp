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

	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	"github.com/shuaiZend/mage2mcp/internal/mcp/requestid"
	"github.com/shuaiZend/mage2mcp/internal/mcp/toolerr"
	"github.com/shuaiZend/mage2mcp/internal/security/audit"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
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
	// List and Dispose are the polling and disposition surface INT-07 was missing:
	// an operation_unknown that no tool could read and no tool could close.
	List(ctx context.Context, f storage.OperationFilter) (*storage.OperationPage, error)
	Dispose(ctx context.Context, id string, judgment storage.DisposeJudgment, note, upstreamRef string, event *audit.Event) (bool, error)
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
	// EntityID is a stable handle that is known BEFORE the mutation and that
	// identifies the entity the operation is about. It is also what the reconciler
	// reads when the outcome is unknown, so it must never be a proxy for the
	// document the call is going to create: a placement that does not exist yet
	// records the customer it is for (U-70), and the created document's id goes to
	// upstream_ref when the call succeeds.
	EntityID       string
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
//
// A request without an idempotency key is deduplicated by its arguments only
// inside storage.DerivedKeyWindow: a repeat inside the window replays, and a
// later repeat is a new operation. "The same arguments" is a double-submit guard,
// not a permanent promise that this subject may never do it again (INT-12).
func (o *Operations) Begin(ctx context.Context, req OperationRequest) (*OperationRun, *storage.BeginResult, error) {
	// The tool now owns this call's outcome audit row: it writes it inside its own
	// ledger transaction, so the middleware must not write a second one afterwards.
	audit.MarkOperationOwned(ctx)
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

// ---------------------------------------------------------------------------
// Polling and disposition of operations
//
// operation_unknown tells the caller to poll, and until these tools existed there
// was nothing to poll with: 102 registered tools, none of them able to read an
// operation. An ERP that lost a response had no way to learn whether the order
// was placed, and a row the reconciler escalated had no exit (INT-07).
//
// The three tools are the complete surface: read one, find the ones that need a
// human, and record what the human found.
// ---------------------------------------------------------------------------

// Get returns one operation for polling.
func (o *Operations) Get(ctx context.Context, id string) (*storage.OperationRecord, error) {
	if strings.TrimSpace(id) == "" {
		return nil, invalidf("operation_id is required")
	}
	return o.ledger.Get(ctx, id)
}

// List returns a page of operations.
func (o *Operations) List(ctx context.Context, f storage.OperationFilter) (*storage.OperationPage, error) {
	if f.State != "" && !knownOperationState(f.State) {
		return nil, invalidf("state must be one of pending, executing, succeeded, failed, unknown, reconciling")
	}
	return o.ledger.List(ctx, f)
}

// Dispose records an operator's finding about an unknown operation.
//
// The judgement is required, and so is the note: the whole point of an unknown
// outcome is that nobody knows yet, so "close it" is only meaningful together with
// what was checked. Nothing here can turn an unknown into "assume it never
// happened": not_applied is a claim the operator verified upstream.
func (o *Operations) Dispose(ctx context.Context, id string, judgment storage.DisposeJudgment, note, upstreamRef string) (*storage.OperationRecord, error) {
	if strings.TrimSpace(id) == "" {
		return nil, invalidf("operation_id is required")
	}
	if !judgment.Valid() {
		return nil, invalidf("judgment must be applied, not_applied or requeue")
	}
	if strings.TrimSpace(note) == "" {
		return nil, invalidf("note is required: record what was checked before closing an unknown outcome")
	}
	record, err := o.ledger.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if record.State != storage.OpUnknown {
		return nil, errs.New(errs.CodeConflict,
			"only an operation whose outcome is unknown can be disposed; this one is "+record.State).
			WithDetail("operation_id", id).
			WithDetail("state", record.State)
	}
	event := &audit.Event{
		Subject: callerSubject(ctx), IdentityClass: callerIdentityClass(ctx),
		Tool: operationDisposeToolName, Instance: record.InstanceID,
		Scope: "dispose", Phase: "outcome",
		Decision: disposeAuditDecision(judgment), State: disposeAuditState(judgment),
		// The request id is what ties this row to the middleware's intent row for
		// the same call. It lives in a package both sides can import because this
		// one cannot import internal/mcp/server (cycle).
		RequestID: requestid.From(ctx),
	}
	if _, err := o.ledger.Dispose(ctx, id, judgment, note, upstreamRef, event); err != nil {
		return nil, err
	}
	return o.ledger.Get(ctx, id)
}

// knownOperationState reports whether state names one of the ledger's states.
func knownOperationState(state string) bool {
	switch state {
	case storage.OpPending, storage.OpExecuting, storage.OpSucceeded,
		storage.OpFailed, storage.OpUnknown, storage.OpReconciling:
		return true
	}
	return false
}

// callerIdentityClass is the verified identity class, or "anonymous" without auth.
func callerIdentityClass(ctx context.Context) string {
	if id, ok := auth.IdentityFromContext(ctx); ok && id.Class != "" {
		return id.Class
	}
	return "anonymous"
}

// disposeAuditDecision and disposeAuditState describe the disposition in the audit
// trail: a requeue changed nothing upstream (error), while a confirmed finding is
// the recorded truth about it.
func disposeAuditDecision(judgment storage.DisposeJudgment) string {
	if judgment == storage.DisposedRequeue {
		return "error"
	}
	return "allow"
}

func disposeAuditState(judgment storage.DisposeJudgment) string {
	switch judgment {
	case storage.DisposedApplied:
		return "succeeded"
	case storage.DisposedNotApplied:
		return "failed"
	default:
		return "outcome_unknown"
	}
}

const (
	operationGetToolName      = "magento.operation.get"
	operationGetToolTitle     = "Get a Magento operation"
	operationListToolName     = "magento.operation.list"
	operationListToolTitle    = "List Magento operations"
	operationDisposeToolName  = "magento.operation.dispose"
	operationDisposeToolTitle = "Dispose of a Magento operation"
)

const operationGetToolDescription = "Reads one ledger operation by its operation_id: the tool it " +
	"represents, the entity, the state (pending, executing, succeeded, failed, unknown, reconciling), " +
	"the recorded result, the reconciliation count and whether it needs a human. " +
	"Read-only. This is the polling endpoint operation_unknown and conflict responses point at: " +
	"an unknown outcome is never resolved by retrying, only by reading the operation."

const operationListToolDescription = "Lists ledger operations newest first, optionally filtered by " +
	"instance, tool name, state, or needs_attention. needs_attention selects the rows no automatic " +
	"reconciliation will touch again (an escalated unknown, or one whose backoff is unreachable) — " +
	"those are the rows an operator has to dispose of. Reports the matching total and whether the " +
	"page was truncated, and returns a next_cursor. Read-only."

const operationDisposeToolDescription = "Records what an operator found out about an operation whose " +
	"outcome is unknown, and moves it to the matching state. judgment=applied (the mutation is in the " +
	"upstream, verified by reading it) marks the operation succeeded so a replay returns it instead of " +
	"writing again; judgment=not_applied (verified absent) marks it failed and frees its derived " +
	"idempotency key; judgment=requeue leaves it unknown but restarts the reconciliation budget so the " +
	"reconciler reads the upstream again instead of leaving the row escalated forever. The note is " +
	"required and is kept in the transition history. Only an unknown operation can be disposed. " +
	"The operation belongs to the resolved instance: its id is the only handle, and the ledger row " +
	"records which instance and tool it came from. Requires approval."

const operationGetSchema = `{"type": "object", "properties": {
	"operation_id": {"type": "string", "pattern": "^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$", "description": "Operation id from an operation_unknown or conflict response, or from magento.operation.list."}
}, "required": ["operation_id"], "additionalProperties": false}`

const operationListSchema = `{"type": "object", "properties": {
	` + instanceOptionalProp + `,
	"operation": {"type": "string", "maxLength": 128, "description": "Tool name to filter by, for example magento.order.create."},
	"state": {"type": "string", "enum": ["pending", "executing", "succeeded", "failed", "unknown", "reconciling"], "description": "Ledger state to filter by."},
	"needs_attention": {"type": "boolean", "description": "True returns only the rows no automatic reconciliation will touch again: escalated unknown outcomes and rows whose backoff is unreachable. These need an operator decision."},
	"limit": {"type": "integer", "minimum": 1, "maximum": 200, "description": "Page size; defaults to 50."},
	"cursor": {"type": "string", "maxLength": 256, "description": "next_cursor from the previous page."}
}, "additionalProperties": false}`

const operationDisposeSchema = `{"type": "object", "properties": {
	"operation_id": {"type": "string", "pattern": "^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$", "description": "The unknown operation to dispose of."},
	"judgment": {"type": "string", "enum": ["applied", "not_applied", "requeue"], "description": "What you found: applied (verified present upstream), not_applied (verified absent upstream), requeue (not determined yet; let the reconciler read upstream again)."},
	"note": {"type": "string", "minLength": 3, "maxLength": 1024, "description": "What you checked and where. Kept in the operation's transition history."},
	"upstream_ref": {"type": "string", "maxLength": 256, "description": "Upstream handle you found, for example the Magento increment id. Recorded with judgment=applied."},
	` + confirmationProp + `
}, "required": ["operation_id", "judgment", "note"], "additionalProperties": false}`

// instanceOptionalProp is the instance input for tools that may span instances.
const instanceOptionalProp = `"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id to filter by; omit to list every instance."}`

type operationGetInput struct {
	OperationID string `json:"operation_id" jsonschema:"Operation id from an operation_unknown or conflict response."`
}

type operationGetOutput struct {
	Operation storage.OperationRecord `json:"operation" jsonschema:"The operation as the ledger records it."`
	// Next is the action the state implies, so a caller does not have to know the
	// state machine to act on the answer.
	Next string `json:"next" jsonschema:"What to do next with this operation."`
}

type operationListInput struct {
	Instance       string `json:"instance,omitempty" jsonschema:"Configured instance id to filter by."`
	Operation      string `json:"operation,omitempty" jsonschema:"Tool name to filter by."`
	State          string `json:"state,omitempty" jsonschema:"Ledger state to filter by."`
	NeedsAttention bool   `json:"needs_attention,omitempty" jsonschema:"Only the rows that need an operator decision."`
	Limit          int    `json:"limit,omitempty" jsonschema:"Page size; defaults to 50."`
	Cursor         string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous page."`
}

type operationListOutput struct {
	Operations []storage.OperationRecord `json:"operations" jsonschema:"Entries returned by this call."`
	Total      int                       `json:"total" jsonschema:"Rows matching the filter, independent of the page."`
	Truncated  bool                      `json:"truncated" jsonschema:"True when more rows match than this page carries."`
	NextCursor string                    `json:"next_cursor,omitempty" jsonschema:"Cursor for the next page; empty on the last page."`
}

type operationDisposeInput struct {
	OperationID    string `json:"operation_id" jsonschema:"The unknown operation to dispose of."`
	Judgment       string `json:"judgment" jsonschema:"applied, not_applied or requeue."`
	Note           string `json:"note" jsonschema:"What was checked and where."`
	UpstreamRef    string `json:"upstream_ref,omitempty" jsonschema:"Upstream handle that was found."`
	ConfirmationID string `json:"confirmation_id,omitempty" jsonschema:"Approval id from a confirmation_required response."`
}

type operationDisposeOutput struct {
	Operation storage.OperationRecord `json:"operation" jsonschema:"The operation after the disposition."`
	Judgment  string                  `json:"judgment" jsonschema:"The judgement that was recorded."`
}

// newOperationGetTool returns the polling tool for one operation.
func newOperationGetTool() registry.Tool {
	t := toolMetadata(operationGetToolName, operationGetToolTitle, operationGetToolDescription)
	t.Capability = capabilityFor(operationGetToolName, "read")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpTool(operationGetToolName, operationGetToolTitle, operationGetToolDescription, operationGetSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in operationGetInput) (*mcp.CallToolResult, operationGetOutput, error) {
				ops, err := configuredOperations()
				if err != nil {
					return nil, operationGetOutput{}, err
				}
				record, err := ops.Get(ctx, in.OperationID)
				if err != nil {
					return nil, operationGetOutput{}, err
				}
				return nil, operationGetOutput{Operation: *record, Next: operationNextStep(*record)}, nil
			})
		return nil
	}
	return t
}

// newOperationListTool returns the tool that finds operations, including the rows
// that need a human.
func newOperationListTool() registry.Tool {
	t := toolMetadata(operationListToolName, operationListToolTitle, operationListToolDescription)
	t.Capability = capabilityFor(operationListToolName, "read")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpTool(operationListToolName, operationListToolTitle, operationListToolDescription, operationListSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in operationListInput) (*mcp.CallToolResult, operationListOutput, error) {
				ops, err := configuredOperations()
				if err != nil {
					return nil, operationListOutput{}, err
				}
				page, err := ops.List(ctx, storage.OperationFilter{
					InstanceID: in.Instance, Operation: in.Operation, State: in.State,
					NeedsAttention: in.NeedsAttention, Limit: in.Limit, Cursor: in.Cursor,
				})
				if err != nil {
					return nil, operationListOutput{}, err
				}
				out := operationListOutput{Total: page.Total, Truncated: page.Truncated, NextCursor: page.NextCursor}
				out.Operations = page.Operations
				if out.Operations == nil {
					out.Operations = []storage.OperationRecord{}
				}
				return nil, out, nil
			})
		return nil
	}
	return t
}

// newOperationDisposeTool returns the tool that records an operator's finding.
func newOperationDisposeTool() registry.Tool {
	t := writeToolMetadata(operationDisposeToolName, operationDisposeToolTitle,
		operationDisposeToolDescription, capabilityFor(operationDisposeToolName, "write"))
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(operationDisposeToolName, operationDisposeToolTitle,
			operationDisposeToolDescription, operationDisposeSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in operationDisposeInput) (*mcp.CallToolResult, operationDisposeOutput, error) {
				ops, err := configuredOperations()
				if err != nil {
					return nil, operationDisposeOutput{}, err
				}
				record, err := ops.Dispose(ctx, in.OperationID, storage.DisposeJudgment(in.Judgment), in.Note, in.UpstreamRef)
				if err != nil {
					return nil, operationDisposeOutput{}, err
				}
				return nil, operationDisposeOutput{Operation: *record, Judgment: in.Judgment}, nil
			})
		return nil
	}
	return t
}

// operationNextStep states what a caller should do with an operation in its
// current state, so the answer is actionable without knowing the state machine.
func operationNextStep(rec storage.OperationRecord) string {
	switch rec.State {
	case storage.OpSucceeded:
		return "succeeded: the mutation is applied and recorded; a repeat with the same idempotency key replays it instead of writing again"
	case storage.OpFailed:
		return "failed: the mutation is proven not to have happened; start a new operation (a new idempotency key) to try again"
	case storage.OpUnknown:
		if rec.NeedsAttention {
			return "unknown and escalated: read the upstream and dispose of it with magento.operation.dispose (applied, not_applied or requeue); do not retry the write"
		}
		return "unknown: the reconciler will read the upstream; poll this operation, do not retry the write"
	case storage.OpReconciling:
		return "reconciling: a worker is reading the upstream right now; poll again shortly"
	default:
		return "in flight: another attempt owns this operation; poll it instead of retrying"
	}
}
