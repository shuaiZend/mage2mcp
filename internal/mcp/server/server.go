// SPDX-License-Identifier: Apache-2.0

// Package server assembles the MCP server, its HTTP surface, and the health
// endpoints. It owns no business logic: tools are supplied by the registry.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
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
	// Authorize is consulted for every tools/call. A nil Authorize allows all
	// calls and is only acceptable in local development without authentication.
	Authorize func(ctx context.Context, tool, instance string) error
	// Audit records intent and outcome for every tools/call.
	Audit func(ctx context.Context, tool, instance, phase, state, errorCode string)
	// Confirm enforces the two-step approval protocol for tools that require it.
	Confirm *confirm.Service
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
	srv := mcp.NewServer(&mcp.Implementation{Name: "mage2mcp", Version: opts.Version}, nil)
	registerPlatformInfo(srv, opts)
	for _, tool := range opts.Registry.List() {
		if err := tool.Register(srv); err != nil {
			return nil, err
		}
	}
	installPolicy(srv, opts)
	return srv, nil
}

// installPolicy wraps tools/call with authorization and audit. The MCP session
// context carries the verified identity from the HTTP auth middleware.
func installPolicy(srv *mcp.Server, opts Options) {
	if opts.Authorize == nil && opts.Audit == nil && opts.Confirm == nil {
		return
	}
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
				if err := opts.Authorize(ctx, tool, instance); err != nil {
					if opts.Audit != nil {
						opts.Audit(ctx, tool, instance, "outcome", "denied", string(errs.CodeOf(err)))
					}
					return deniedResult(err), nil
				}
			}
			if err := opts.enforceConfirmation(ctx, tool, call.Params.Arguments); err != nil {
				if opts.Audit != nil {
					opts.Audit(ctx, tool, instance, "outcome", "denied", string(errs.CodeOf(err)))
				}
				return toolErrorResult(err), nil
			}
			result, err := next(ctx, method, req)
			if opts.Audit != nil {
				state, code := "succeeded", ""
				if err != nil {
					state, code = "failed", string(errs.CodeOf(err))
				}
				opts.Audit(ctx, tool, instance, "outcome", state, code)
			}
			return result, err
		}
	})
}

// enforceConfirmation applies ADR-0014 to tools whose metadata requires it.
func (o Options) enforceConfirmation(ctx context.Context, tool string, args json.RawMessage) error {
	if o.Confirm == nil {
		return nil
	}
	meta, ok := o.Registry.Lookup(tool)
	if !ok || !meta.RequiresConfirmation {
		return nil
	}
	requester := "anonymous"
	if id, ok := auth.IdentityFromContext(ctx); ok && id.Subject != "" {
		requester = id.Subject
	}
	var fields map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &fields); err != nil {
			return errs.New(errs.CodeInvalidArgument, "arguments are not a JSON object")
		}
	}
	if meta.ValidateInput != nil {
		if err := meta.ValidateInput(args); err != nil {
			return errs.New(errs.CodeInvalidArgument, "arguments do not match the tool schema").
				WithDetail("validation", err.Error())
		}
	}
	confirmationID, _ := fields["confirmation_id"].(string)
	if confirmationID == "" {
		req, err := o.Confirm.Require(ctx, tool, "", meta.Permission, meta.Title, requester, args)
		if err != nil {
			return err
		}
		return errs.New(errs.CodeConfirmationRequired, "this operation requires approval by a different human").
			WithDetail("confirmation_id", req.ID).
			WithDetail("expires_at", req.ExpiresAt.Format(time.RFC3339))
	}
	if err := o.Confirm.Consume(ctx, confirmationID, requester, args); err != nil {
		return err
	}
	return nil
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
	code := string(errs.CodeOf(err))
	body := map[string]any{"code": code, "message": err.Error()}
	var typed *errs.Error
	if errors.As(err, &typed) && len(typed.Details) > 0 {
		body["details"] = typed.Details
	}
	return &mcp.CallToolResult{IsError: true, StructuredContent: body,
		Content: []mcp.Content{&mcp.TextContent{Text: code + ": " + err.Error()}}}
}

func deniedResult(err error) *mcp.CallToolResult {
	code := string(errs.CodeOf(err))
	return &mcp.CallToolResult{
		IsError:           true,
		StructuredContent: map[string]any{"code": code, "message": err.Error()},
		Content:           []mcp.Content{&mcp.TextContent{Text: code + ": " + err.Error()}},
	}
}

func registerPlatformInfo(srv *mcp.Server, opts Options) {
	tool := &mcp.Tool{
		Name:        "platform.info",
		Title:       "Server information",
		Description: "Returns the gateway name, version, supported MCP protocol versions, and registered tools. Read-only.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(false)},
	}
	mcp.AddTool(srv, tool, func(_ context.Context, _ *mcp.CallToolRequest, _ InfoInput) (*mcp.CallToolResult, InfoOutput, error) {
		names := make([]string, 0, opts.Registry.Len())
		for _, t := range opts.Registry.List() {
			names = append(names, t.Name)
		}
		sort.Strings(names)
		return nil, InfoOutput{
			Name:             "mage2mcp",
			Version:          opts.Version,
			ProtocolVersions: []string{"2025-11-25", "2026-07-28"},
			Tools:            names,
			Instances:        opts.Instances,
			WritesEnabled:    opts.WriteTools,
		}, nil
	})
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
