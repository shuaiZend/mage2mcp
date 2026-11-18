// SPDX-License-Identifier: Apache-2.0

// Package server assembles the MCP server, its HTTP surface, and the health
// endpoints. It owns no business logic: tools are supplied by the registry.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
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
}

// InfoInput is empty: platform.info takes no parameters.
type InfoInput struct{}

// InfoOutput is the structured result of platform.info.
type InfoOutput struct {
	Name             string   `json:"name"`
	Version          string   `json:"version"`
	ProtocolVersions []string `json:"protocol_versions"`
	Tools            []string `json:"tools"`
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
	return srv, nil
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
		if opts.Registry.Len() == 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "no tools registered"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
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
