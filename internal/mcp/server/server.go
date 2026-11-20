// SPDX-License-Identifier: Apache-2.0

// Package server assembles the MCP server, its HTTP surface, and the health
// endpoints. It owns no business logic: tools are supplied by the registry.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shuaiZend/mage2mcp/internal/mcp/toolerr"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	"github.com/shuaiZend/mage2mcp/internal/security/audit"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
)

// Options configures the server.
type Options struct {
	Version        string
	Logger         *slog.Logger
	Registry       *registry.Registry
	AuthMiddleware func(http.Handler) http.Handler
	Metrics        http.Handler
	SessionTimeout time.Duration
	MaxBodyBytes   int64
	// Authorize is consulted for every tools/call. requiredScope is the tool's
	// declared registry permission, resolved by installPolicy; the server never
	// defaults it to read. A nil Authorize allows all calls and is only
	// acceptable in local development without authentication.
	Authorize func(ctx context.Context, tool, instance, requiredScope string) error
	// Audit records intent and outcome for every tools/call.
	Audit func(ctx context.Context, tool, instance, phase, state, errorCode string)
	// Confirm enforces the two-step approval protocol for tools that require it.
	Confirm *confirm.Service
	// BeginOperation and FinishOperation wire the operation ledger in at the one
	// choke point every write tool passes through. When they are nil the server
	// runs without a ledger (development only). BeginOperation must consume the
	// confirmation (OperationRequest.ConfirmationID) in the same transaction as
	// the ledger row; FinishOperation must write the audit outcome in the same
	// transaction as the transition.
	BeginOperation  func(ctx context.Context, req OperationRequest) (OperationTicket, error)
	FinishOperation func(ctx context.Context, ticket OperationTicket, out OperationOutcome) error
	// SelfLedgeredTools names tools that own their operation lifecycle.
	//
	// There are two write paths in this server, and every write tool is on
	// exactly one of them:
	//
	//   - middleware-gated (the default): installPolicy opens the operation,
	//     runs the handler, and records the outcome plus the audit row in one
	//     transaction. It needs no per-tool knowledge.
	//   - self-ledgered (this set): the tool opens and drives its own operation
	//     from inside its handler, because its safety depends on work that has to
	//     happen around the upstream call and that only it knows how to do —
	//     CaptureInvoice reads the invoice before capturing, CreateOrder
	//     reconciles against cart totals and the customer's order window instead
	//     of replaying a recorded result. A middleware-owned operation would
	//     either duplicate the row or lose that reconciliation.
	//
	// This is not an opt-out from the ledger: a self-ledgered tool records a
	// richer operation than the middleware could derive. It is explicit and
	// test-checked (cmd/server asserts every write tool in the real catalog is on
	// one of the two paths), so a new write tool cannot silently escape both. The
	// set should shrink as those tools gain a way to accept an externally owned
	// operation.
	SelfLedgeredTools map[string]bool
	// Instances reports the configured instance ids per module for platform.info.
	Instances map[string][]string
	// WriteTools reports whether write tools are registered.
	WriteTools bool
}

// InfoInput is empty: platform.info takes no parameters.
type InfoInput struct{}

// InfoOutput is the structured result of platform.info.
type InfoOutput struct {
	Name             string              `json:"name"`
	Version          string              `json:"version"`
	ProtocolVersions []string            `json:"protocol_versions"`
	Tools            []string            `json:"tools"`
	Instances        map[string][]string `json:"instances,omitempty"`
	WritesEnabled    bool                `json:"writes_enabled"`
}

// NewMCP builds the MCP server and registers every tool in the registry.
func NewMCP(opts Options) (*mcp.Server, error) {
	if opts.Registry == nil {
		return nil, errs.New(errs.CodeInternal, "tool registry is required")
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "mage2mcp", Version: opts.Version}, nil)
	// platform.info is a first-class registry entry: it is validated by the same
	// metadata contract as every module tool, appears in tools/list and in its
	// own tools field, and resolves its authorization scope from the registry.
	if _, exists := opts.Registry.Lookup(platformInfoToolName); !exists {
		if err := opts.Registry.Register(platformInfoTool(opts)); err != nil {
			return nil, err
		}
	}
	// A high-risk tool with no confirmation service would execute without the
	// human approval ADR-0014 requires. That is a fail-open default, and a
	// library consumer that forgets the field would never notice — so refuse to
	// start instead of silently dropping the gate.
	if opts.Confirm == nil {
		for _, tool := range opts.Registry.List() {
			if tool.RequiresConfirmation {
				return nil, errs.New(errs.CodeInternal,
					"confirmation service is required: a registered tool requires human approval").
					WithDetail("tool", tool.Name)
			}
		}
	}
	registerWriteSafetyPrompt(srv)
	for _, tool := range opts.Registry.List() {
		if err := registerTool(srv, tool); err != nil {
			return nil, err
		}
	}
	installPolicy(srv, opts)
	// Schema validation is unconditional: every wire tool, including one that
	// bypassed toolerr.Add via a direct mcp.AddTool, must carry a closed schema.
	if err := validateRegisteredToolSchemas(srv); err != nil {
		return nil, err
	}
	return srv, nil
}

// validateRegisteredToolSchemas lists the tools the SDK actually exposes and
// applies toolerr.ValidateSchema to each. This catches a direct mcp.AddTool that
// bypassed toolerr.Add's registration-time validation.
func validateRegisteredToolSchemas(srv *mcp.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		return fmt.Errorf("registry: schema validation connect: %w", err)
	}
	defer func() { _ = serverSession.Close() }()
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "schema-validator", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		return fmt.Errorf("registry: schema validation client: %w", err)
	}
	defer func() { _ = clientSession.Close() }()
	list, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("registry: list tools for schema validation: %w", err)
	}
	for _, tool := range list.Tools {
		if err := toolerr.ValidateSchema(tool); err != nil {
			return fmt.Errorf("registry: tool %q schema is invalid: %w", tool.Name, err)
		}
	}
	return nil
}

// registerTool turns a registration panic (an invalid schema is a programming
// error at startup) into a startup error instead of crashing the process.
func registerTool(srv *mcp.Server, tool registry.Tool) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("registry: tool %q registration failed: %v", tool.Name, recovered)
		}
	}()
	return tool.Register(srv)
}

// requiredScope resolves the scope a tools/call must be authorized at. ok is
// false when the tool has no declared permission, which the caller must treat
// as a denial.
func requiredScope(reg *registry.Registry, tool string) (string, bool) {
	if reg == nil {
		return "", false
	}
	meta, found := reg.Lookup(tool)
	if !found || meta.Permission == "" {
		return "", false
	}
	return meta.Permission, true
}

// installPolicy wraps tools/call with authorization and audit. The MCP session
// context carries the verified identity from the HTTP auth middleware.
//
// Authorization is resolved from the tool's declared permission and is
// fail-closed: a tool that is not in the registry is denied before any handler
// runs.
func installPolicy(srv *mcp.Server, opts Options) {
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			call, ok := req.(*mcp.CallToolRequest)
			if !ok {
				return next(ctx, method, req)
			}
			tool := call.Params.Name
			instance := instanceArgument(call.Params.Arguments)
			if opts.Audit != nil {
				opts.Audit(ctx, tool, instance, "intent", "pending", "")
			}
			if opts.Authorize != nil {
				authErr := authorizeCall(ctx, opts, tool, instance)
				if authErr != nil {
					recordOutcome(opts, ctx, tool, instance, "denied", errs.CodeOf(authErr))
					return deniedResult(authErr), nil
				}
			}

			// The confirmation is validated here but consumed by BeginOperation,
			// so burning the approval and recording the operation are one atomic
			// step (REL-02 / F7).
			confirmationID, confErr := opts.prepareConfirmation(ctx, tool, instance, call.Params.Arguments)
			if confErr != nil {
				recordOutcome(opts, ctx, tool, instance, "denied", errs.CodeOf(confErr))
				return toolErrorResult(confErr), nil
			}

			meta, _ := opts.Registry.Lookup(tool)
			gated := opts.BeginOperation != nil && IsWritePermission(meta.Permission) &&
				!opts.SelfLedgeredTools[tool]
			var ticket OperationTicket
			if gated {
				opReq, buildErr := buildOperationRequest(ctx, tool, instance, confirmationID, call.Params.Arguments)
				if buildErr != nil {
					recordOutcome(opts, ctx, tool, instance, "failed", errs.CodeOf(buildErr))
					return toolErrorResult(buildErr), nil
				}
				began, beginErr := opts.BeginOperation(ctx, opReq)
				if beginErr != nil {
					// conflict (in flight), operation_unknown (poll, never retry)
					// and idempotency_key_reuse are decisions, not failures.
					state := "failed"
					if errs.CodeOf(beginErr) == errs.CodeOperationUnknown {
						state = "outcome_unknown"
					}
					recordOutcome(opts, ctx, tool, instance, state, errs.CodeOf(beginErr))
					return toolErrorResult(beginErr), nil
				}
				// Only a decision that names this caller as the owner may run the
				// tool. Anything else is a refusal: running it would either
				// duplicate a mutation another attempt owns or re-issue one that
				// already failed or may already have been applied.
				switch began.Decision {
				case DecisionReplay:
					// Already succeeded: return the recorded result without touching
					// the upstream system again.
					return replayResult(began)
				case DecisionProceed:
					ticket = began
				case DecisionRetry:
					recordOutcome(opts, ctx, tool, instance, "failed", errs.CodeConflict)
					return toolErrorResult(errs.New(errs.CodeConflict,
						"the operation is retryable but this call did not claim it").
						WithDetail("operation_id", began.OperationID)), nil
				case DecisionInProgress:
					recordOutcome(opts, ctx, tool, instance, "failed", errs.CodeConflict)
					return toolErrorResult(errs.New(errs.CodeConflict,
						"another attempt owns this operation; poll it instead of retrying").
						WithDetail("operation_id", began.OperationID).
						WithDetail("reason", "operation_in_progress")), nil
				case DecisionUnknown:
					recordOutcome(opts, ctx, tool, instance, "outcome_unknown", errs.CodeOperationUnknown)
					return toolErrorResult(errs.New(errs.CodeOperationUnknown,
						"operation outcome is unknown; poll for reconciliation and do not retry").
						WithDetail("operation_id", began.OperationID)), nil
				default:
					// Fail closed: an unrecognised decision must never run the tool.
					recordOutcome(opts, ctx, tool, instance, "failed", errs.CodeInternal)
					return toolErrorResult(errs.New(errs.CodeInternal,
						"operation gate returned an unrecognised decision").
						WithDetail("operation_id", began.OperationID)), nil
				}
			} else if confirmationID != "" && opts.Confirm != nil {
				if err := opts.Confirm.Consume(ctx, confirmationID, requesterOf(ctx), tool, instance, call.Params.Arguments); err != nil {
					recordOutcome(opts, ctx, tool, instance, "denied", errs.CodeOf(err))
					return toolErrorResult(err), nil
				}
			}

			// MCP-05: the declared per-tool timeout is a real deadline, not
			// metadata. It is applied even when no authorizer/audit/confirmation
			// is configured, so a slow handler cannot hang until the transport
			// timeout.
			timeout := time.Duration(0)
			if meta.Timeout > 0 {
				timeout = meta.Timeout
			}
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			result, err := next(ctx, method, req)
			timedOut := timeout > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded)
			if timedOut {
				// A tool that ran out of time after dispatch may have applied its
				// mutation: that is an unknown outcome, not a failure.
				err = errs.New(errs.CodeUpstreamTimeout, "tool exceeded its declared timeout").
					WithDetail("timeout", timeout.String())
			}

			if gated {
				outcome := classifyResult(result, err)
				outcome.Audit = auditEventFor(ctx, tool, instance, meta.Permission, outcome)
				if finishErr := opts.FinishOperation(ctx, ticket, outcome); finishErr != nil {
					// The call itself already happened; record the bookkeeping
					// failure without changing what the caller sees, unless the
					// caller succeeded and the ledger could not.
					if err == nil {
						return toolErrorResult(errs.New(errs.CodeInternal,
							"the operation completed but its ledger record could not be written").
							WithDetail("operation_id", ticket.OperationID).WithCause(finishErr)), nil
					}
				}
				if err != nil {
					return toolErrorResult(err), nil
				}
				return result, err
			}

			if timedOut {
				recordOutcome(opts, ctx, tool, instance, "failed", errs.CodeOf(err))
				return toolErrorResult(err), nil
			}
			state, code := "succeeded", errs.Code("")
			if err != nil {
				state, code = "failed", errs.CodeOf(err)
				if ambiguousOutcome(err) {
					state = "outcome_unknown"
				}
			}
			recordOutcome(opts, ctx, tool, instance, state, code)
			return result, err
		}
	})
}

func recordOutcome(opts Options, ctx context.Context, tool, instance, state string, code errs.Code) {
	if opts.Audit != nil {
		opts.Audit(ctx, tool, instance, "outcome", state, string(code))
	}
}

// authorizeCall resolves the tool's required scope and asks the caller's
// authorizer. An unregistered tool is denied without consulting the authorizer,
// so a missing registration can never become an allow.
func authorizeCall(ctx context.Context, opts Options, tool, instance string) error {
	scope, known := requiredScope(opts.Registry, tool)
	if !known {
		return errs.New(errs.CodePermissionDenied, "permission denied").
			WithDetail("reason", "unregistered_tool").
			WithDetail("tool", tool)
	}
	return opts.Authorize(ctx, tool, instance, scope)
}

// prepareConfirmation applies ADR-0014 up to the point of consumption: it
// creates the pending request when the caller supplied no id, and returns the id
// to consume. The consume itself happens either in BeginOperation (so it is
// atomic with the ledger row) or, when no ledger is configured, immediately.
func (o Options) prepareConfirmation(ctx context.Context, tool, instance string, args json.RawMessage) (string, error) {
	if o.Confirm == nil {
		return "", nil
	}
	meta, ok := o.Registry.Lookup(tool)
	if !ok || !meta.RequiresConfirmation {
		return "", nil
	}
	var fields map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &fields); err != nil {
			return "", errs.New(errs.CodeInvalidArgument, "arguments are not a JSON object")
		}
	}
	if meta.ValidateInput != nil {
		if err := meta.ValidateInput(args); err != nil {
			return "", errs.New(errs.CodeInvalidArgument, "arguments do not match the tool schema").
				WithDetail("validation", err.Error())
		}
	}
	confirmationID, _ := fields["confirmation_id"].(string)
	if confirmationID != "" {
		return confirmationID, nil
	}
	req, err := o.Confirm.Require(ctx, tool, instance, meta.Permission, confirm.Summarize(args), requesterOf(ctx), args)
	if err != nil {
		return "", err
	}
	return "", errs.New(errs.CodeConfirmationRequired, "this operation requires approval by a different human").
		WithDetail("confirmation_id", req.ID).
		WithDetail("expires_at", req.ExpiresAt.Format(time.RFC3339))
}

// instanceArgument extracts the routing instance from raw tool arguments so the
// audit row records the instance the caller asked for, even when resolution
// later rejects it.
func instanceArgument(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal(args, &fields); err != nil {
		return ""
	}
	value, _ := fields["instance"].(string)
	return value
}

func toolErrorResult(err error) *mcp.CallToolResult {
	return toolerr.ErrorResult(err)
}

func deniedResult(err error) *mcp.CallToolResult {
	return toolerr.DeniedResult(err)
}

const platformInfoToolName = "platform.info"

// platformInfoTool is the registry metadata for platform.info. Keeping it in
// the registry (rather than a server-owned side map) means it gets the same
// name/title/description/scope/timeout validation, authorization resolution,
// timeout enforcement, and tools/list representation as every module tool.
func platformInfoTool(opts Options) registry.Tool {
	tool := registry.Tool{
		Name: platformInfoToolName, Title: "Server information",
		Description: "Returns the gateway name, version, supported MCP protocol versions, " +
			"registered tools, configured instances, and whether writes are enabled. Read-only.",
		Capability: "platform.info.read", Permission: registry.PermissionRead,
		Timeout: 30 * time.Second,
		Annotations: mcp.ToolAnnotations{
			ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPtr(false),
		},
	}
	tool.Register = func(srv *mcp.Server) error {
		definition := &mcp.Tool{
			Name: platformInfoToolName, Title: tool.Title, Description: tool.Description,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPtr(false)},
		}
		toolerr.Add(srv, definition, func(_ context.Context, _ *mcp.CallToolRequest, _ InfoInput) (*mcp.CallToolResult, InfoOutput, error) {
			names := make([]string, 0, opts.Registry.Len())
			for _, registered := range opts.Registry.List() {
				names = append(names, registered.Name)
			}
			sort.Strings(names)
			return nil, InfoOutput{
				Name:             "mage2mcp",
				Version:          opts.Version,
				ProtocolVersions: mcp.SupportedProtocolVersions(),
				Tools:            names,
				Instances:        opts.Instances,
				WritesEnabled:    opts.Registry.HasWrites(),
			}, nil
		})
		return nil
	}
	return tool
}

func boolPtr(v bool) *bool { return &v }

// Handler returns the HTTP surface: /mcp, /healthz, /readyz, and optionally /metrics.
func Handler(srv *mcp.Server, opts Options) http.Handler {
	sessionTimeout := opts.SessionTimeout
	if sessionTimeout <= 0 {
		sessionTimeout = 30 * time.Minute
	}
	maxBody := opts.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = 1 << 20
	}
	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{
			Logger:              opts.Logger,
			SessionTimeout:      sessionTimeout,
			MaxRequestBodyBytes: maxBody,
		},
	)
	mcpHandler := http.Handler(streamable)
	if opts.AuthMiddleware != nil {
		mcpHandler = opts.AuthMiddleware(mcpHandler)
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		// Readiness means the process can serve MCP. A deployment with no
		// business module enabled still serves platform.info, so it is ready;
		// the tool count is reported for operators.
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "tools": opts.Registry.Len()})
	})
	if opts.Metrics != nil {
		mux.Handle("/metrics", opts.Metrics)
	}
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

const writeSafetyPromptName = "write_safety"

// registerWriteSafetyPrompt exposes the catalog-structure warning as an MCP
// prompt so agents can load it before planning a write.
func registerWriteSafetyPrompt(srv *mcp.Server) {
	srv.AddPrompt(&mcp.Prompt{
		Name:  writeSafetyPromptName,
		Title: "Write safety and URL structure",
		Description: "Checklist before Magento writes that can change catalog structure, product URLs, or " +
			"attribute semantics. Load this before creating categories, attributes/options, or changing url_key.",
	}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{
			Description: "Catalog-structure write checklist",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: writeSafetyText},
			}},
		}, nil
	})
}

const writeSafetyText = `Before any Magento write, confirm the intent and the blast radius with the operator.

Catalog structure (requires explicit confirmation):
- Creating, moving, renaming, or deleting a category can change product URL keys and
  URL rewrites. Existing links, search rankings, and feeds may break. Prefer adding a
  category under an existing branch; never delete a category that still has products
  or child categories without a redirect and reindex plan.
- Creating an attribute or attribute option is a schema change: it affects attribute
  sets, filters, and layered navigation. Deleting an option that products reference
  (especially a configurable variant) breaks those products; check usage first.
- Changing a product url_key changes its storefront URL. Magento does not create a
  301 redirect for API writes; plan the redirect and reindex before announcing it.
- Changing product type, visibility, website assignment, or status changes what
  storefronts and feeds see.

Inventory and configurables:
- Assigning MSI source items to children does not make a configurable parent salable;
  verify the parent stock status and the storefront option selector afterwards.
- The configurable parent price is derived from its children; a requested parent
  price may read back as 0.

Approval:
- Every write is two-step: inspect the preview/confirmation, then approve as a
  different identity. Do not approve destructive or URL-affecting changes without
  the operator's explicit go-ahead.`

// ---------------------------------------------------------------------------
// Operation ledger gate (REL-02 / REL-06 / ARCH-02)
//
// Every write call is recorded as an operation before the tool runs and its
// outcome is recorded after. The gate is driven from the tool's declared
// registry permission, so there is no per-tool opt-in: a new write tool is
// covered the moment it is registered.
// ---------------------------------------------------------------------------

// Operation decisions returned by BeginOperation. The middleware switches on
// every one of them: only proceed runs the tool, and only replay skips it with a
// recorded result.
const (
	DecisionProceed    = "proceed"
	DecisionRetry      = "retry"
	DecisionInProgress = "in_progress"
	DecisionReplay     = "replay"
	DecisionUnknown    = "unknown"
)

// Outcome detail contract shared with internal/magento (which sets it) and
// internal/jobs (which refuses to retry it). A test asserts the three agree.
const (
	outcomeDetail  = "outcome"
	outcomeUnknown = "unknown"
)

// OperationRequest is one write call as the ledger sees it.
type OperationRequest struct {
	Tool           string
	Instance       string
	Subject        string
	ArgsHash       string
	IdempotencyKey string
	ConfirmationID string
	EntityType     string
	EntityID       string
	AmountMinor    *int64
	Currency       string
}

// OperationTicket is the ledger's ownership token for the attempt that is now
// allowed to run.
type OperationTicket struct {
	OperationID  string
	Fence        string
	Decision     string
	Attempt      int
	Replayed     bool
	ReplayResult json.RawMessage
	UpstreamRef  string
}

// OperationOutcome is what the call produced.
type OperationOutcome struct {
	// State is "succeeded", "failed" or "unknown".
	State       string
	ErrorCode   string
	UpstreamRef string
	Result      json.RawMessage
	RetryAfter  time.Duration
	// Audit is committed in the same transaction as the transition.
	Audit *audit.Event
}

// IsWritePermission reports whether a declared permission mutates state. The gate
// covers write, destructive and admin; read tools are untouched. It is exported
// so the completeness test can assert coverage with the same predicate the
// middleware uses.
func IsWritePermission(permission string) bool {
	switch permission {
	case registry.PermissionWrite, registry.PermissionDestructive, registry.PermissionAdmin:
		return true
	}
	return false
}

// requesterOf returns the verified subject, or "anonymous" when the deployment
// runs without authentication.
func requesterOf(ctx context.Context) string {
	if id, ok := auth.IdentityFromContext(ctx); ok && id.Subject != "" {
		return id.Subject
	}
	return "anonymous"
}

// buildOperationRequest derives the ledger request from the call. The argument
// hash is the confirmation flow's canonical hash, so an approval and an
// operation for the same call can be correlated.
func buildOperationRequest(ctx context.Context, tool, instance, confirmationID string, args json.RawMessage) (OperationRequest, error) {
	hash, err := confirm.CanonicalHash(args)
	if err != nil {
		return OperationRequest{}, errs.New(errs.CodeInvalidArgument, "arguments are not valid JSON")
	}
	var fields map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &fields); err != nil {
			return OperationRequest{}, errs.New(errs.CodeInvalidArgument, "arguments are not a JSON object")
		}
	}
	subject := requesterOf(ctx)
	key, _ := fields["idempotency_key"].(string)
	if key == "" {
		// Exactly-once-by-arguments for this subject, tool and instance. This is
		// byte-identical to storage.DeriveKey, so a tool that drives its own
		// ledger operation (the money tools do) shares this row instead of
		// creating a second one. A contract test asserts the two agree.
		key = DeriveOperationKey(subject, hash)
	}
	entityType, entityID := entityFor(tool, fields, hash)
	req := OperationRequest{
		Tool: tool, Instance: instance, Subject: subject, ArgsHash: hash,
		IdempotencyKey: key, ConfirmationID: confirmationID,
		EntityType: entityType, EntityID: entityID,
	}
	if amount, ok := amountOf(fields); ok {
		req.AmountMinor = &amount
		if currency, ok := fields["currency"].(string); ok {
			req.Currency = currency
		}
	}
	return req, nil
}

// DeriveOperationKey is storage.DeriveKey's formula. The two must stay identical:
// a tool that opens its own operation for the same call has to land on the same
// row, not a second one.
func DeriveOperationKey(subject, argsHash string) string {
	subjectHash := sha256.Sum256([]byte(subject))
	return "auto:" + hex.EncodeToString(subjectHash[:]) + ":" + argsHash
}

// entityFor maps a tool and its arguments onto the ledger's entity columns. The
// type comes from the tool's module segment (all write tools are magento.*); the
// id is the first recognised identifier argument, falling back to the argument
// hash so the row is still identifiable.
func entityFor(tool string, fields map[string]any, fallback string) (string, string) {
	entityType := "product"
	parts := strings.SplitN(tool, ".", 3)
	if len(parts) >= 2 {
		switch parts[1] {
		case "order", "orders":
			entityType = "order"
		case "invoice", "invoices":
			entityType = "invoice"
		case "creditmemo", "creditmemos":
			entityType = "creditmemo"
		case "shipment", "shipments":
			entityType = "shipment"
		case "inventory":
			entityType = "stock"
		case "category":
			entityType = "category"
		case "customer":
			entityType = "customer"
		}
	}
	for _, key := range entityIDKeys(entityType) {
		if value, ok := fields[key]; ok {
			if id := scalarString(value); id != "" {
				return entityType, id
			}
		}
	}
	return entityType, fallback
}

// entityIDKeys lists the identifier arguments for an entity type, most specific
// first.
func entityIDKeys(entityType string) []string {
	switch entityType {
	case "order":
		return []string{"order_id", "entity_id", "increment_id"}
	case "invoice":
		return []string{"invoice_id", "order_id", "entity_id"}
	case "creditmemo":
		return []string{"creditmemo_id", "order_id", "entity_id"}
	case "shipment":
		return []string{"shipment_id", "order_id", "entity_id"}
	case "stock":
		return []string{"sku", "source_code", "source_item_id"}
	case "category":
		return []string{"category_id", "entity_id"}
	case "customer":
		return []string{"customer_id", "email", "entity_id"}
	default:
		return []string{"sku", "product_id", "entity_id"}
	}
}

// scalarString renders a JSON scalar as the ledger's entity id.
func scalarString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		if v == math.Trunc(v) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	}
	return ""
}

// amountOf extracts a money amount in minor units plus its currency. Only an
// explicit amount/currency pair is recorded; nothing is inferred from a total.
func amountOf(fields map[string]any) (int64, bool) {
	raw, ok := fields["amount_minor"]
	if !ok {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		if v == math.Trunc(v) {
			return int64(v), true
		}
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n, true
		}
	}
	return 0, false
}

// ambiguousOutcome reports whether an error means "we do not know whether the
// mutation was applied". internal/magento marks those with the outcome detail;
// the timeout code means the same thing by construction.
func ambiguousOutcome(err error) bool {
	if err == nil {
		return false
	}
	var typed *errs.Error
	if errors.As(err, &typed) && typed.Details[outcomeDetail] == outcomeUnknown {
		return true
	}
	if errors.Is(err, jobs.ErrLeaseLost) {
		// The attempt lost ownership of its work: whatever it did may or may not
		// have landed, so it is never safe to retry.
		return true
	}
	code := errs.CodeOf(err)
	return code == errs.CodeUpstreamTimeout || code == errs.CodeOperationUnknown
}

// classifyResult maps the middleware's view of a call onto the ledger state.
// Handler errors reach us two ways: as a Go error, or folded into an
// IsError CallToolResult by toolerr. Both must classify identically.
func classifyResult(result mcp.Result, err error) OperationOutcome {
	if err != nil {
		return classifyOutcome(err)
	}
	call, ok := result.(*mcp.CallToolResult)
	if !ok || call == nil || !call.IsError {
		out := OperationOutcome{State: "succeeded"}
		if ok && call != nil && call.StructuredContent != nil {
			if raw, marshalErr := json.Marshal(call.StructuredContent); marshalErr == nil {
				out.Result = raw
			}
		}
		return out
	}
	var envelope struct {
		Code    string            `json:"code"`
		Details map[string]string `json:"details"`
	}
	if raw, marshalErr := json.Marshal(call.StructuredContent); marshalErr == nil {
		_ = json.Unmarshal(raw, &envelope)
	}
	out := OperationOutcome{ErrorCode: envelope.Code}
	if envelope.Details[outcomeDetail] == outcomeUnknown ||
		envelope.Code == string(errs.CodeUpstreamTimeout) ||
		envelope.Code == string(errs.CodeOperationUnknown) {
		out.State = "unknown"
		return out
	}
	out.State = "failed"
	return out
}

// classifyOutcome maps a call result to the ledger state. Only a proven-not-
// applied failure becomes "failed"; anything ambiguous becomes "unknown" so it
// is reconciled instead of retried.
func classifyOutcome(err error) OperationOutcome {
	if err == nil {
		return OperationOutcome{State: "succeeded"}
	}
	out := OperationOutcome{ErrorCode: string(errs.CodeOf(err))}
	if ambiguousOutcome(err) {
		out.State = "unknown"
		return out
	}
	out.State = "failed"
	return out
}

// auditEventFor builds the outcome audit row that FinishOperation commits with
// the transition. The decision is derived from the outcome: a denial is a
// denial, not an "allow".
func auditEventFor(ctx context.Context, tool, instance, scope string, out OperationOutcome) *audit.Event {
	subject, class := "anonymous", "anonymous"
	if id, ok := auth.IdentityFromContext(ctx); ok {
		if id.Subject != "" {
			subject = id.Subject
		}
		if id.Class != "" {
			class = id.Class
		}
	}
	event := &audit.Event{
		Subject: subject, IdentityClass: class, Tool: tool, Instance: instance,
		Scope: scope, Phase: "outcome", ErrorCode: out.ErrorCode,
	}
	switch out.State {
	case "succeeded":
		event.Decision, event.State = "allow", "succeeded"
	case "unknown":
		event.Decision, event.State = "error", "outcome_unknown"
	default:
		event.Decision, event.State = "error", "failed"
	}
	return event
}

// replayResult returns the recorded result of an operation that already
// succeeded, without touching the upstream system.
func replayResult(ticket OperationTicket) (mcp.Result, error) {
	var decoded any
	if len(ticket.ReplayResult) > 0 {
		_ = json.Unmarshal(ticket.ReplayResult, &decoded)
	}
	if decoded == nil {
		decoded = map[string]any{"operation_id": ticket.OperationID, "replayed": true}
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{
			Text: "replayed: this operation already succeeded (operation " + ticket.OperationID + ")",
		}},
		StructuredContent: decoded,
	}, nil
}
