// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/ga4"
	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/magento"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	"github.com/shuaiZend/mage2mcp/internal/mcp/toolerr"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

func newTestServer(t *testing.T) (*mcp.Server, *registry.Registry) {
	t.Helper()
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:        "magento.product.get",
		Title:       "Get product",
		Description: "Returns one product by SKU.",
		Capability:  "magento.product.read",
		Permission:  registry.PermissionRead,
		Timeout:     30 * time.Second,
		Annotations: mcp.ToolAnnotations{ReadOnlyHint: true},
		Register: func(*mcp.Server) error {
			return nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	srv, err := NewMCP(Options{Version: "test", Registry: reg,
		Instances: map[string][]string{"cloudflare": {"zone-a", "zone-b"}}})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	return srv, reg
}

func TestMCPClientListsAndCallsPlatformInfo(t *testing.T) {
	srv, _ := newTestServer(t)
	ts := httptest.NewServer(Handler(srv, Options{Registry: mustRegistry(t)}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == "platform.info" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Fatal("platform.info must be read-only")
			}
			if tool.OutputSchema == nil {
				t.Fatal("platform.info must declare an output schema")
			}
			if tool.InputSchema == nil {
				t.Fatal("platform.info must declare an input schema")
			}
		}
	}
	if !found {
		t.Fatal("platform.info not registered")
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "platform.info"})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if res.IsError {
		t.Fatalf("platform.info returned an error: %+v", res.Content)
	}
	if len(res.Content) == 0 {
		t.Fatal("expected a text fallback in content")
	}
	if res.StructuredContent == nil {
		t.Fatal("expected structured content")
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var info InfoOutput
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("structured content is not InfoOutput: %v", err)
	}
	if info.Name != "mage2mcp" || info.Version != "test" {
		t.Fatalf("unexpected info: %+v", info)
	}
	if len(info.Instances["cloudflare"]) != 2 {
		t.Fatalf("info does not report instances: %+v", info)
	}
	if info.WritesEnabled {
		t.Fatalf("writes_enabled = true with a read-only registry: %+v", info)
	}
	if len(info.ProtocolVersions) != len(mcp.SupportedProtocolVersions()) {
		t.Fatalf("protocol_versions = %v, want the SDK list", info.ProtocolVersions)
	}
	if !slices.Contains(info.Tools, "platform.info") {
		t.Fatalf("platform.info does not list itself: %v", info.Tools)
	}
}

func TestHealthAndReady(t *testing.T) {
	srv, _ := newTestServer(t)
	ts := httptest.NewServer(Handler(srv, Options{Registry: mustRegistry(t)}))
	defer ts.Close()

	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d", path, resp.StatusCode)
		}
	}
}

func TestReadyzReportsWithoutBusinessTools(t *testing.T) {
	reg := registry.New()
	srv, err := NewMCP(Options{Version: "test", Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(Handler(srv, Options{Registry: reg}))
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Tools int `json:"tools"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("readyz body: %v", err)
	}
	if body.Tools != 1 {
		t.Fatalf("readyz tools = %d, want 1 (platform.info); the Handler must use the same registry", body.Tools)
	}
}

// platform.info is always present, even in a fresh quickstart with no modules.
// This test deliberately passes the same registry to NewMCP and Handler.
func TestPlatformInfoIsAlwaysPresentInFreshRegistry(t *testing.T) {
	reg := registry.New()
	srv, err := NewMCP(Options{Version: "test", Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(Handler(srv, Options{Registry: reg}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "platform.info"})
	if err != nil {
		t.Fatalf("platform.info: %v", err)
	}
	if res.IsError {
		t.Fatalf("platform.info error: %+v", res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var info InfoOutput
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	if len(info.Tools) != 1 || info.Tools[0] != "platform.info" {
		t.Fatalf("fresh quickstart tools = %v, want [platform.info]", info.Tools)
	}
	if !slices.Equal(info.ProtocolVersions, mcp.SupportedProtocolVersions()) {
		t.Fatalf("protocol_versions = %v, want the full SDK list", info.ProtocolVersions)
	}
}

func mustRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	_, reg := newTestServer(t)
	return reg
}

func TestAuthorizationDeniesToolCall(t *testing.T) {
	reg := mustRegistry(t)
	srv, err := NewMCP(Options{
		Version: "test", Registry: reg,
		Authorize: func(context.Context, string, string, string) error {
			return errs.New(errs.CodePermissionDenied, "denied by test")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(Handler(srv, Options{Registry: reg}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "platform.info"})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected the call to be denied")
	}
	if len(res.Content) == 0 {
		t.Fatal("expected a denial message")
	}
}

func TestAuditReceivesIntentAndOutcome(t *testing.T) {
	reg := mustRegistry(t)
	var mu sync.Mutex
	var phases, instances []string
	srv, err := NewMCP(Options{
		Version: "test", Registry: reg,
		Audit: func(_ context.Context, _, instance, phase, _, _ string) {
			mu.Lock()
			defer mu.Unlock()
			phases = append(phases, phase)
			instances = append(instances, instance)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(Handler(srv, Options{Registry: reg}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "platform.info",
		Arguments: map[string]any{"instance": "store-us"}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(phases) < 2 || phases[0] != "intent" || phases[1] != "outcome" {
		t.Fatalf("unexpected audit phases: %v", phases)
	}
	if len(instances) < 2 || instances[0] != "store-us" {
		t.Fatalf("audit did not record the instance: %v", instances)
	}
}

type confirmInput struct {
	ConfirmationID string `json:"confirmation_id,omitempty"`
	Value          string `json:"value"`
}

type confirmOutput struct {
	OK bool `json:"ok" jsonschema:"True when the call succeeded."`
}

func TestConfirmationFlowThroughHTTP(t *testing.T) {
	reg := registry.New()
	tool := registry.Tool{
		Name: "magento.product.update", Title: "Update product",
		Description: "Test write tool.", Capability: "magento.product.write",
		Permission: registry.PermissionWrite, Timeout: 30 * time.Second,
		RequiresConfirmation: true,
		Annotations:          mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false},
		Register: func(srv *mcp.Server) error {
			mcp.AddTool(srv, &mcp.Tool{Name: "magento.product.update", Title: "Update product",
				Description: "Test write tool.",
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false}},
				func(_ context.Context, _ *mcp.CallToolRequest, in confirmInput) (*mcp.CallToolResult, confirmOutput, error) {
					if in.Value == "" {
						return nil, confirmOutput{}, errs.New(errs.CodeInvalidArgument, "value is required")
					}
					return nil, confirmOutput{OK: true}, nil
				})
			return nil
		},
	}
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	confirmSvc, err := confirm.NewService(confirm.NewMemoryStore(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewMCP(Options{Version: "test", Registry: reg, Confirm: confirmSvc})
	if err != nil {
		t.Fatal(err)
	}
	authMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := auth.WithIdentity(r.Context(), &auth.Identity{Subject: "agent-1", Class: auth.ClassService})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	ts := httptest.NewServer(Handler(srv, Options{Registry: reg, AuthMiddleware: authMiddleware}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	// First call: no confirmation id, expect confirmation_required with an id.
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "magento.product.update",
		Arguments: map[string]any{"value": "new name"}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected confirmation_required")
	}
	details, _ := res.StructuredContent.(map[string]any)
	id, _ := details["details"].(map[string]any)["confirmation_id"].(string)
	if id == "" {
		t.Fatalf("no confirmation id in %+v", res.StructuredContent)
	}

	// Approve as a different human.
	if err := confirmSvc.Approve(ctx, id, "human-1"); err != nil {
		t.Fatal(err)
	}

	// Second call with the id succeeds.
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "magento.product.update",
		Arguments: map[string]any{"value": "new name", "confirmation_id": id}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("approved call failed: %+v", res.Content)
	}

	// Replay is rejected.
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "magento.product.update",
		Arguments: map[string]any{"value": "new name", "confirmation_id": id}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("replay must be rejected")
	}
}

func TestAuthorizationReceivesInstance(t *testing.T) {
	reg := mustRegistry(t)
	var gotInstance string
	srv, err := NewMCP(Options{
		Version: "test", Registry: reg,
		Authorize: func(_ context.Context, _, instance, _ string) error {
			gotInstance = instance
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(Handler(srv, Options{Registry: reg}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "platform.info",
		Arguments: map[string]any{"instance": "zone-b"}}); err != nil {
		t.Fatal(err)
	}
	if gotInstance != "zone-b" {
		t.Fatalf("authorizer instance = %q, want zone-b", gotInstance)
	}
}

type instanceOnlyInput struct {
	Instance string `json:"instance"`
}

func TestCrossInstanceAuthorizationIsEnforced(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "magento.product.get", Title: "Get product", Description: "Test read tool.",
		Capability: "magento.product.read", Permission: registry.PermissionRead,
		Timeout: 30 * time.Second, Annotations: mcp.ToolAnnotations{ReadOnlyHint: true},
		Register: func(srv *mcp.Server) error {
			mcp.AddTool(srv, &mcp.Tool{Name: "magento.product.get", Title: "Get product",
				Description: "Test read tool.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
				func(_ context.Context, _ *mcp.CallToolRequest, _ instanceOnlyInput) (*mcp.CallToolResult, confirmOutput, error) {
					return nil, confirmOutput{OK: true}, nil
				})
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	srv, err := NewMCP(Options{
		Version: "test", Registry: reg,
		Authorize: func(_ context.Context, _, instance, _ string) error {
			if instance == "zone-b" {
				return errs.New(errs.CodePermissionDenied, "grant covers zone-a only")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(Handler(srv, Options{Registry: reg}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	// Instance inside the grant succeeds.
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "magento.product.get",
		Arguments: map[string]any{"instance": "zone-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("allowed instance denied: %+v", res.Content)
	}

	// Instance outside the grant is a typed tool error, not a protocol error.
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "magento.product.get",
		Arguments: map[string]any{"instance": "zone-b"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("cross-instance call must be denied")
	}
	body, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(body), "permission_denied") {
		t.Fatalf("denial body = %s", body)
	}
}

// authzInput is the argument shape for the authorization stub tools below.
type authzInput struct {
	Instance string `json:"instance,omitempty"`
	// ConfirmationID is part of the schema because a destructive stub requires
	// approval; a real tool declares it too, and the SDK rejects unknown fields.
	ConfirmationID string `json:"confirmation_id,omitempty"`
}

type authzOutput struct {
	OK bool `json:"ok" jsonschema:"True when the call succeeded."`
}

// authzStubTool builds a tool with the given declared permission. ran, when not
// nil, is set when the handler executes, so a test can prove a call was denied
// before reaching the handler.
func authzStubTool(t *testing.T, name, permission string, ran *bool) registry.Tool {
	t.Helper()
	annotations := mcp.ToolAnnotations{ReadOnlyHint: permission == registry.PermissionRead}
	tool := registry.Tool{
		Name: name, Title: name, Description: "authorization stub tool",
		Capability: "test." + strings.ReplaceAll(name, ".", "_"),
		Permission: permission, Timeout: 5 * time.Second, Annotations: annotations,
	}
	if permission == registry.PermissionDestructive {
		destructive := true
		// The registry requires a destructive tool to demand confirmation, so
		// these stubs do; the call helpers below approve it, which is how a real
		// client gets past the gate.
		tool.RequiresConfirmation = true
		tool.Annotations.DestructiveHint = &destructive
	}
	tool.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, &mcp.Tool{Name: name, Title: name, Description: "authorization stub tool",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: annotations.ReadOnlyHint, DestructiveHint: annotations.DestructiveHint}},
			func(_ context.Context, _ *mcp.CallToolRequest, _ authzInput) (*mcp.CallToolResult, authzOutput, error) {
				if ran != nil {
					*ran = true
				}
				return nil, authzOutput{OK: true}, nil
			})
		return nil
	}
	return tool
}

// callToolHTTP drives one tools/call over the real HTTP transport.
func callToolHTTP(t *testing.T, srv *mcp.Server, reg *registry.Registry, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	ts := httptest.NewServer(Handler(srv, Options{Registry: reg}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	// A destructive tool requires approval; approve as a different human and
	// retry, exactly as a real client must.
	id := confirmationIDFromResult(t, res)
	if id == "" || serverConfirm == nil {
		return res
	}
	if aerr := serverConfirm.Approve(ctx, id, "qa-approver"); aerr != nil {
		t.Fatalf("approve %s: %v", name, aerr)
	}
	approved := make(map[string]any, len(args)+1)
	for k, v := range args {
		approved[k] = v
	}
	approved["confirmation_id"] = id
	second, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: approved})
	if err != nil {
		t.Fatalf("call %s with confirmation: %v", name, err)
	}
	return second
}

// TestAuthorizationReceivesRequiredScope proves the server resolves the scope
// from the tool's declared permission instead of assuming read.
func TestAuthorizationReceivesRequiredScope(t *testing.T) {
	reg := registry.New()
	for _, tool := range []registry.Tool{
		authzStubTool(t, "magento.product.get", registry.PermissionRead, nil),
		authzStubTool(t, "magento.invoice.capture", registry.PermissionWrite, nil),
		authzStubTool(t, "magento.product.delete", registry.PermissionDestructive, nil),
	} {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	var mu sync.Mutex
	got := map[string]string{}
	srv, err := NewMCP(Options{
		Version: "test", Registry: reg, Confirm: serverTestConfirm(t),
		Authorize: func(_ context.Context, tool, _, requiredScope string) error {
			mu.Lock()
			defer mu.Unlock()
			got[tool] = requiredScope
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"magento.product.get":     registry.PermissionRead,
		"magento.invoice.capture": registry.PermissionWrite,
		"magento.product.delete":  registry.PermissionDestructive,
	}
	for tool, wantScope := range want {
		res := callToolHTTP(t, srv, reg, tool, map[string]any{"instance": "store-us"})
		if res.IsError {
			t.Fatalf("%s denied by an allowing authorizer: %s", tool, toolResultText(res))
		}
		mu.Lock()
		scope := got[tool]
		mu.Unlock()
		if scope != wantScope {
			t.Fatalf("%s authorized at scope %q, want %q", tool, scope, wantScope)
		}
	}
}

// TestAuthorizationFailsClosedForUnregisteredTool proves an unknown tool is
// denied locally: the authorizer is never consulted, so a missing registration
// cannot become an allow even with a permissive policy.
func TestAuthorizationFailsClosedForUnregisteredTool(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(authzStubTool(t, "magento.product.get", registry.PermissionRead, nil)); err != nil {
		t.Fatalf("register: %v", err)
	}
	called := false
	srv, err := NewMCP(Options{
		Version: "test", Registry: reg,
		Authorize: func(context.Context, string, string, string) error {
			called = true
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := callToolHTTP(t, srv, reg, "magento.not.registered", map[string]any{"instance": "store-us"})
	if !res.IsError {
		t.Fatal("an unregistered tool must be denied")
	}
	if called {
		t.Fatal("the authorizer must not be consulted for an unregistered tool")
	}
	body, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "permission_denied") {
		t.Fatalf("denial body = %s, want permission_denied", body)
	}
}

// TestAuthorizationCoversPlatformInfo proves the server-owned tool keeps a
// declared read scope rather than falling through the registry lookup.
func TestAuthorizationCoversPlatformInfo(t *testing.T) {
	reg := registry.New()
	var gotScope string
	srv, err := NewMCP(Options{
		Version: "test", Registry: reg,
		Authorize: func(_ context.Context, tool, _, requiredScope string) error {
			if tool == "platform.info" {
				gotScope = requiredScope
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := callToolHTTP(t, srv, reg, "platform.info", nil)
	if res.IsError {
		t.Fatalf("platform.info denied: %+v", res.Content)
	}
	if gotScope != registry.PermissionRead {
		t.Fatalf("platform.info authorized at scope %q, want %q", gotScope, registry.PermissionRead)
	}
}

// orderStateInput mirrors the shared schema of the order hold/unhold tools.
type orderStateInput struct {
	Instance       string `json:"instance,omitempty"`
	OrderID        int    `json:"order_id"`
	ConfirmationID string `json:"confirmation_id,omitempty"`
}

// callToolAsSubject drives one tools/call with a fixed verified identity.
func callToolAsSubject(t *testing.T, srv *mcp.Server, reg *registry.Registry, subject, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	handler := Handler(srv, Options{
		Registry: reg,
		AuthMiddleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := &auth.Identity{Subject: subject, Class: auth.ClassHuman}
				next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
			})
		},
	})
	ts := httptest.NewServer(handler)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

// confirmationIDFrom extracts the confirmation id from a confirmation_required
// tool result.
func confirmationIDFrom(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	body, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("no structured content in %+v", res.Content)
	}
	details, ok := body["details"].(map[string]any)
	if !ok {
		t.Fatalf("no details in %+v", body)
	}
	id, _ := details["confirmation_id"].(string)
	if id == "" {
		t.Fatalf("no confirmation_id in %+v", details)
	}
	return id
}

// TestConfirmationBoundToToolAndInstanceThroughHTTP is the end-to-end proof for
// SEC-03: the production middleware binds an approval to the tool and the
// instance, so an approval for magento.order.hold cannot execute
// magento.order.unhold, and one issued for store-a cannot run against store-b.
func TestConfirmationBoundToToolAndInstanceThroughHTTP(t *testing.T) {
	reg := registry.New()
	for _, name := range []string{"magento.order.hold", "magento.order.unhold"} {
		toolName := name
		tool := registry.Tool{
			Name: toolName, Title: toolName, Description: "Change order state.",
			Capability: "magento.order.write", Permission: registry.PermissionWrite,
			Timeout: 5 * time.Second, RequiresConfirmation: true,
			Annotations: mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false},
			Register: func(srv *mcp.Server) error {
				mcp.AddTool(srv, &mcp.Tool{Name: toolName, Title: toolName, Description: "Change order state.",
					Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false}},
					func(_ context.Context, _ *mcp.CallToolRequest, _ orderStateInput) (*mcp.CallToolResult, confirmOutput, error) {
						return nil, confirmOutput{OK: true}, nil
					})
				return nil
			},
		}
		if err := reg.Register(tool); err != nil {
			t.Fatalf("register %s: %v", toolName, err)
		}
	}
	confirmSvc, err := confirm.NewService(confirm.NewMemoryStore(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewMCP(Options{Version: "test", Registry: reg, Confirm: confirmSvc})
	if err != nil {
		t.Fatal(err)
	}

	// 1. The agent asks to HOLD order 42 on store-a.
	first := callToolAsSubject(t, srv, reg, "agent-1", "magento.order.hold",
		map[string]any{"instance": "store-a", "order_id": 42})
	if !first.IsError {
		t.Fatal("expected confirmation_required for the first call")
	}
	id := confirmationIDFrom(t, first)

	// 2. A different human approves exactly that request.
	if err := confirmSvc.Approve(context.Background(), id, "human-1"); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	// 3. Byte-identical arguments, different tool: must be rejected.
	res := callToolAsSubject(t, srv, reg, "agent-1", "magento.order.unhold",
		map[string]any{"instance": "store-a", "order_id": 42, "confirmation_id": id})
	if !res.IsError {
		t.Fatal("an approval for order.hold must not execute order.unhold")
	}
	body, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "confirmation_param_mismatch") {
		t.Fatalf("cross-tool denial body = %s, want confirmation_param_mismatch", body)
	}

	// 4. Same tool, different instance: must be rejected.
	res = callToolAsSubject(t, srv, reg, "agent-1", "magento.order.hold",
		map[string]any{"instance": "store-b", "order_id": 42, "confirmation_id": id})
	if !res.IsError {
		t.Fatal("an approval for store-a must not execute on store-b")
	}
	body, err = json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "confirmation_param_mismatch") {
		t.Fatalf("cross-instance denial body = %s, want confirmation_param_mismatch", body)
	}

	// 5. The approval it was issued for still works: a rejected attempt does
	//    not burn it.
	res = callToolAsSubject(t, srv, reg, "agent-1", "magento.order.hold",
		map[string]any{"instance": "store-a", "order_id": 42, "confirmation_id": id})
	if res.IsError {
		t.Fatalf("legitimate approved call failed: %+v", res.Content)
	}
}

// TestConfirmationStoresArgumentSummary proves the human approval surface sees
// the instance and the arguments, not just a tool title.
func TestConfirmationStoresArgumentSummary(t *testing.T) {
	reg := registry.New()
	tool := registry.Tool{
		Name: "magento.order.hold", Title: "Hold an order", Description: "Change order state.",
		Capability: "magento.order.write", Permission: registry.PermissionWrite,
		Timeout: 5 * time.Second, RequiresConfirmation: true,
		Annotations: mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false},
		Register: func(srv *mcp.Server) error {
			mcp.AddTool(srv, &mcp.Tool{Name: "magento.order.hold", Title: "Hold an order", Description: "Change order state.",
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false}},
				func(_ context.Context, _ *mcp.CallToolRequest, _ orderStateInput) (*mcp.CallToolResult, confirmOutput, error) {
					return nil, confirmOutput{OK: true}, nil
				})
			return nil
		},
	}
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	confirmSvc, err := confirm.NewService(confirm.NewMemoryStore(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewMCP(Options{Version: "test", Registry: reg, Confirm: confirmSvc})
	if err != nil {
		t.Fatal(err)
	}
	res := callToolAsSubject(t, srv, reg, "agent-1", "magento.order.hold",
		map[string]any{"instance": "store-a", "order_id": 42})
	if !res.IsError {
		t.Fatal("expected confirmation_required")
	}
	pending, err := confirmSvc.Pending(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %v, %v", pending, err)
	}
	if pending[0].Instance != "store-a" {
		t.Fatalf("stored instance = %q, want store-a", pending[0].Instance)
	}
	if !strings.Contains(pending[0].Summary, "order_id=42") || !strings.Contains(pending[0].Summary, "store-a") {
		t.Fatalf("stored summary does not describe the call: %q", pending[0].Summary)
	}
}

// --- t19 contract tests -----------------------------------------------------

type serverTestSigner struct{}

func (serverTestSigner) Sign(*http.Request) error { return nil }

type serverTestQueue struct{}

func (serverTestQueue) Enqueue(context.Context, jobs.Task) (string, error) { return "job-1", nil }
func (serverTestQueue) Cancel(context.Context, string, string, string) error {
	return nil
}
func (serverTestQueue) GetJob(context.Context, string, string, string) (*jobs.JobRecord, error) {
	return nil, nil
}

func magentoReadRegistry(t *testing.T) (*registry.Registry, *magento.Resolver) {
	t.Helper()
	mc, err := magento.NewClient(magento.ClientConfig{
		BaseURL: "https://shop.example.com", StoreCode: "default", Signer: serverTestSigner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := magento.NewService(mc)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := magento.NewResolver(map[string]*magento.Service{"store-us": svc})
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	if err := magento.RegisterWithResolver(reg, resolver); err != nil {
		t.Fatal(err)
	}
	return reg, resolver
}

func resultMap(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("no text content")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content = %T", res.Content[0])
	}
	return tc.Text
}

func listServerTools(t *testing.T, srv *mcp.Server) []*mcp.Tool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	go func() { _, _ = srv.Connect(ctx, st, nil) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "tool-list", Version: "0"}, nil)
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	return list.Tools
}

// TestReadOnlyConfigurationRegistersNoWriteTools is the MCP-03 contract: with
// writes disabled the registry rejects every non-read tool, and the import
// registration split keeps the read-only job tools without the three writes.
func TestReadOnlyConfigurationRegistersNoWriteTools(t *testing.T) {
	reg, resolver := magentoReadRegistry(t)
	reg.SetWritesEnabled(false)
	if err := magento.RegisterImportReadTools(reg, resolver, serverTestQueue{}); err != nil {
		t.Fatalf("read-only import tools: %v", err)
	}
	for _, tool := range reg.List() {
		if tool.Permission != registry.PermissionRead {
			t.Errorf("read-only registry contains %s tool %q", tool.Permission, tool.Name)
		}
	}
	for _, name := range []string{"magento.import.start", "magento.import.cancel", "magento.import.resume"} {
		if _, ok := reg.Lookup(name); ok {
			t.Errorf("write tool %q registered with writes disabled", name)
		}
	}
	// The gate is at the registry, so a module that forgets its own check still
	// cannot register a write.
	if err := magento.RegisterImportJobs(reg, resolver, serverTestQueue{}); err == nil {
		t.Fatal("RegisterImportJobs succeeded with writes disabled")
	}
}

// TestPlatformInfoReportsRegistryTruth is the MCP-02 contract: platform.info is
// a registry entry, lists itself, derives writes_enabled from the registry, and
// reads protocol versions from the SDK.
func TestPlatformInfoReportsRegistryTruth(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(authzStubTool(t, "magento.product.get", registry.PermissionRead, nil)); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(authzStubTool(t, "magento.invoice.capture", registry.PermissionWrite, nil)); err != nil {
		t.Fatal(err)
	}
	srv, err := NewMCP(Options{
		Version: "test", Registry: reg,
		Instances: map[string][]string{"magento": {"store-us"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Lookup("platform.info"); !ok {
		t.Fatal("platform.info is not a registry entry")
	}
	res := callToolHTTP(t, srv, reg, "platform.info", nil)
	if res.IsError {
		t.Fatalf("platform.info error: %+v", res.Content)
	}
	var info InfoOutput
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	if !info.WritesEnabled {
		t.Fatal("writes_enabled = false with a write tool registered")
	}
	if !slices.Equal(info.Tools, []string{"magento.invoice.capture", "magento.product.get", "platform.info"}) {
		t.Fatalf("tools = %v", info.Tools)
	}
	if !slices.Equal(info.ProtocolVersions, mcp.SupportedProtocolVersions()) {
		t.Fatalf("protocol_versions = %v, want SDK list", info.ProtocolVersions)
	}
	if got := info.Instances["magento"]; len(got) != 1 || got[0] != "store-us" {
		t.Fatalf("instances = %v", info.Instances)
	}
}

// TestNewMCPRejectsOpenSchema is the MCP-04 contract: a schema without
// additionalProperties:false fails registration (via a startup error, not a
// panic escaping NewMCP).
func TestNewMCPRejectsOpenSchema(t *testing.T) {
	reg := registry.New()
	err := reg.Register(registry.Tool{
		Name: "magento.product.open", Title: "Open schema",
		Description: "Open schema fixture.", Capability: "magento.product.read",
		Permission: registry.PermissionRead, Timeout: 5 * time.Second,
		Annotations: mcp.ToolAnnotations{ReadOnlyHint: true},
		Register: func(srv *mcp.Server) error {
			toolerr.Add(srv, &mcp.Tool{
				Name: "magento.product.open", Title: "Open schema", Description: "Open schema fixture.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`),
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			}, func(context.Context, *mcp.CallToolRequest, authzInput) (*mcp.CallToolResult, authzOutput, error) {
				return nil, authzOutput{OK: true}, nil
			})
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewMCP(Options{Registry: reg}); err == nil || !strings.Contains(err.Error(), "additionalProperties") {
		t.Fatalf("NewMCP error = %v, want an additionalProperties rejection", err)
	}
}

// TestGA4TrafficRejectsUnknownField proves the open GA4 schema is closed and
// the shared validator rejects an unknown field before the handler.
func TestGA4TrafficRejectsUnknownField(t *testing.T) {
	svc, err := ga4.NewService(ga4.Config{PropertyID: "123456", TokenSource: ga4.StaticToken("test")})
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	if err := ga4.Register(reg, svc); err != nil {
		t.Fatal(err)
	}
	if err := ga4.RegisterExtra(reg, svc); err != nil {
		t.Fatal(err)
	}
	srv, err := NewMCP(Options{Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	res := callToolHTTP(t, srv, reg, "ga4.analytics.traffic", map[string]any{
		"instance": "ga", "metrics": []string{"sessions"}, "bogus": "unknown",
	})
	if !res.IsError {
		t.Fatal("unknown field accepted")
	}
	if code := resultMap(t, res)["code"]; code != "invalid_argument" {
		t.Fatalf("code = %v, want invalid_argument", code)
	}
}

// TestDeclaredTimeoutFires is the MCP-05 contract: the per-tool timeout is a
// real deadline and a slow handler returns a typed timeout tool error.
func TestDeclaredTimeoutFires(t *testing.T) {
	reg := registry.New()
	err := reg.Register(registry.Tool{
		Name: "magento.product.slow", Title: "Slow tool",
		Description: "Slow timeout fixture.", Capability: "magento.product.read",
		Permission: registry.PermissionRead, Timeout: 20 * time.Millisecond,
		Annotations: mcp.ToolAnnotations{ReadOnlyHint: true},
		Register: func(srv *mcp.Server) error {
			mcp.AddTool(srv, &mcp.Tool{
				Name: "magento.product.slow", Title: "Slow tool", Description: "Slow timeout fixture.",
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			}, func(ctx context.Context, _ *mcp.CallToolRequest, _ authzInput) (*mcp.CallToolResult, authzOutput, error) {
				<-ctx.Done()
				return nil, authzOutput{}, ctx.Err()
			})
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewMCP(Options{Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	res := callToolHTTP(t, srv, reg, "magento.product.slow", nil)
	if !res.IsError {
		t.Fatal("slow handler did not time out")
	}
	body := resultMap(t, res)
	if body["code"] != "upstream_timeout" {
		t.Fatalf("code = %v, want upstream_timeout", body["code"])
	}
	if !strings.Contains(resultText(t, res), "declared timeout") {
		t.Fatalf("text = %q", resultText(t, res))
	}
}

// TestDeniedAndHandlerEnvelopesMatch is the MCP-09 residual contract: a denied
// call and a handler failure carry the same envelope and text code once.
func TestDeniedAndHandlerEnvelopesMatch(t *testing.T) {
	typed := func() error {
		return errs.New(errs.CodePermissionDenied, "denied").WithOp("authz").WithDetail("reason", "test")
	}

	deniedReg := registry.New()
	if err := deniedReg.Register(authzStubTool(t, "magento.product.get", registry.PermissionRead, nil)); err != nil {
		t.Fatal(err)
	}
	deniedSrv, err := NewMCP(Options{
		Version: "test", Registry: deniedReg,
		Authorize: func(context.Context, string, string, string) error { return typed() },
	})
	if err != nil {
		t.Fatal(err)
	}
	denied := callToolHTTP(t, deniedSrv, deniedReg, "magento.product.get", map[string]any{"instance": "store-us"})

	handlerReg := registry.New()
	if err := handlerReg.Register(registry.Tool{
		Name: "magento.product.get", Title: "Get product",
		Description: "Handler envelope fixture.", Capability: "magento.product.read",
		Permission: registry.PermissionRead, Timeout: 5 * time.Second,
		Annotations: mcp.ToolAnnotations{ReadOnlyHint: true},
		Register: func(srv *mcp.Server) error {
			toolerr.Add(srv, &mcp.Tool{
				Name: "magento.product.get", Title: "Get product", Description: "Handler envelope fixture.",
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			}, func(context.Context, *mcp.CallToolRequest, authzInput) (*mcp.CallToolResult, authzOutput, error) {
				return nil, authzOutput{}, typed()
			})
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	handlerSrv, err := NewMCP(Options{Version: "test", Registry: handlerReg})
	if err != nil {
		t.Fatal(err)
	}
	handled := callToolHTTP(t, handlerSrv, handlerReg, "magento.product.get", map[string]any{"instance": "store-us"})

	deniedBody := resultMap(t, denied)
	handledBody := resultMap(t, handled)
	if !reflect.DeepEqual(deniedBody, handledBody) {
		t.Fatalf("denied envelope = %v, handler envelope = %v", deniedBody, handledBody)
	}
	for _, key := range []string{"code", "message", "op", "details"} {
		if _, ok := deniedBody[key]; !ok {
			t.Errorf("denied envelope missing %q: %v", key, deniedBody)
		}
	}
	if got := strings.Count(resultText(t, denied), "permission_denied"); got != 1 {
		t.Fatalf("denied text contains the code %d times: %q", got, resultText(t, denied))
	}
}

// TestOrderSummaryIsADistinctDeprecatedAlias is the MCP-11 contract: the two
// summary tools no longer have identical behaviour or output schemas.
func TestOrderSummaryIsADistinctDeprecatedAlias(t *testing.T) {
	reg, _ := magentoReadRegistry(t)
	orderTool, ok := reg.Lookup("magento.order.summary")
	if !ok {
		t.Fatal("magento.order.summary not registered")
	}
	salesTool, ok := reg.Lookup("magento.sales.summary")
	if !ok {
		t.Fatal("magento.sales.summary not registered")
	}
	if !strings.Contains(strings.ToLower(orderTool.Description), "deprecated") {
		t.Fatalf("order.summary description is not marked deprecated: %q", orderTool.Description)
	}
	if strings.Contains(strings.ToLower(salesTool.Description), "deprecated") {
		t.Fatalf("sales.summary description is deprecated: %q", salesTool.Description)
	}

	srv, err := NewMCP(Options{Version: "test", Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	var orderSchema, salesSchema []byte
	for _, tool := range listServerTools(t, srv) {
		switch tool.Name {
		case "magento.order.summary":
			orderSchema, _ = json.Marshal(tool.OutputSchema)
		case "magento.sales.summary":
			salesSchema, _ = json.Marshal(tool.OutputSchema)
		}
	}
	if len(orderSchema) == 0 || len(salesSchema) == 0 {
		t.Fatal("summary tools missing from tools/list")
	}
	if string(orderSchema) == string(salesSchema) {
		t.Fatalf("summary output schemas are identical: %s", orderSchema)
	}
	if !strings.Contains(string(orderSchema), "deprecated") || !strings.Contains(string(orderSchema), "use") {
		t.Fatalf("order.summary schema lacks migration fields: %s", orderSchema)
	}
}

// ---------------------------------------------------------------------------
// Operation ledger gate (t30)
// ---------------------------------------------------------------------------

// fakeGate records what the middleware asked the ledger to do.
type fakeGate struct {
	mu       sync.Mutex
	begins   []OperationRequest
	finishes []OperationOutcome
	beginFn  func(OperationRequest) (OperationTicket, error)
}

func (g *fakeGate) begin(_ context.Context, req OperationRequest) (OperationTicket, error) {
	g.mu.Lock()
	g.begins = append(g.begins, req)
	g.mu.Unlock()
	if g.beginFn != nil {
		return g.beginFn(req)
	}
	return OperationTicket{OperationID: "op-" + req.Tool, Fence: "fence", Decision: "proceed"}, nil
}

func (g *fakeGate) finish(_ context.Context, _ OperationTicket, out OperationOutcome) error {
	g.mu.Lock()
	g.finishes = append(g.finishes, out)
	g.mu.Unlock()
	return nil
}

// gateServer builds a server whose registry contains one tool per (name,
// permission, handler) and whose gate is the fake.
func gateServer(t *testing.T, gate *fakeGate, tools []registry.Tool) (*mcp.Server, *registry.Registry) {
	t.Helper()
	reg := registry.New()
	for _, tool := range tools {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name, err)
		}
	}
	srv, err := NewMCP(Options{
		Version: "test", Registry: reg,
		BeginOperation: gate.begin, FinishOperation: gate.finish,
	})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	return srv, reg
}

// gatedTool builds a registry tool whose handler returns err (nil = success).
func gatedTool(t *testing.T, name, permission string, handlerErr error) registry.Tool {
	t.Helper()
	tool := registry.Tool{
		Name: name, Title: name, Description: "test tool",
		Capability: "test.capability", Permission: permission,
		Timeout: 5 * time.Second,
		// A destructive tool must require confirmation (registry invariant); the
		// call helper approves it so the ledger gate under test is reached.
		RequiresConfirmation: permission == registry.PermissionDestructive,
		Annotations: mcp.ToolAnnotations{
			ReadOnlyHint:    permission == registry.PermissionRead,
			DestructiveHint: boolPtr(permission == registry.PermissionDestructive),
		},
	}
	tool.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, &mcp.Tool{Name: name, Title: name, Description: "test tool"},
			func(_ context.Context, _ *mcp.CallToolRequest, _ orderStateInput) (*mcp.CallToolResult, map[string]any, error) {
				if handlerErr != nil {
					return nil, nil, handlerErr
				}
				return nil, map[string]any{"ok": true}, nil
			})
		return nil
	}
	return tool
}

func callWithOptions(t *testing.T, opts Options, subject, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	if opts.Confirm == nil {
		opts.Confirm = serverTestConfirm(t)
	}
	handler := HandlerForTest(t, opts, subject)
	ts := httptest.NewServer(handler)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	// A destructive tool requires approval; approve as a different human and
	// retry, exactly as a real client must.
	id := confirmationIDFromResult(t, res)
	if id == "" || opts.Confirm == nil {
		return res
	}
	if aerr := opts.Confirm.Approve(ctx, id, "qa-approver"); aerr != nil {
		t.Fatalf("approve %s: %v", name, aerr)
	}
	approved := make(map[string]any, len(args)+1)
	for k, v := range args {
		approved[k] = v
	}
	approved["confirmation_id"] = id
	second, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: approved})
	if err != nil {
		t.Fatalf("call %s with confirmation: %v", name, err)
	}
	return second
}

// confirmationIDFromResult returns the pending-approval id from a tool result,
// or "" when the result is not a confirmation_required response.
func confirmationIDFromResult(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || !res.IsError {
		return ""
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return ""
	}
	var envelope struct {
		Code    string            `json:"code"`
		Details map[string]string `json:"details"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ""
	}
	if envelope.Code != string(errs.CodeConfirmationRequired) {
		return ""
	}
	return envelope.Details["confirmation_id"]
}

// HandlerForTest is Handler with a fixed verified identity.

// serverTestConfirm returns an in-memory confirmation service. NewMCP refuses to
// start when a registered tool requires approval and no service is supplied, so
// tests that build a server with write or destructive tools need one.
func serverTestConfirm(t *testing.T) *confirm.Service {
	t.Helper()
	serverConfirmOnce.Do(func() {
		svc, err := confirm.NewService(confirm.NewMemoryStore(), time.Minute)
		if err != nil {
			t.Fatalf("confirm.NewService: %v", err)
		}
		serverConfirm = svc
	})
	return serverConfirm
}

var (
	serverConfirmOnce sync.Once
	serverConfirm     *confirm.Service
)

func HandlerForTest(t *testing.T, opts Options, subject string) http.Handler {
	t.Helper()
	if opts.Confirm == nil {
		opts.Confirm = serverTestConfirm(t)
	}
	srv, err := NewMCP(opts)
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	return Handler(srv, Options{
		Registry: opts.Registry,
		AuthMiddleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := &auth.Identity{Subject: subject, Class: auth.ClassHuman}
				next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
			})
		},
	})
}

// Every write tool is gated at the middleware, with no per-tool opt-in; read
// tools are not.
func TestEveryWriteToolIsGated(t *testing.T) {
	gate := &fakeGate{}
	tools := []registry.Tool{
		gatedTool(t, "magento.product.create", registry.PermissionWrite, nil),
		gatedTool(t, "magento.product.update", registry.PermissionWrite, nil),
		gatedTool(t, "magento.invoice.capture", registry.PermissionWrite, nil),
		gatedTool(t, "magento.product.delete", registry.PermissionDestructive, nil),
		gatedTool(t, "magento.customer.update", registry.PermissionWrite, nil),
		gatedTool(t, "magento.product.get", registry.PermissionRead, nil),
	}
	opts := Options{Version: "test", Registry: registry.New()}
	for _, tool := range tools {
		if err := opts.Registry.Register(tool); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	opts.BeginOperation, opts.FinishOperation = gate.begin, gate.finish

	for _, tool := range tools {
		res := callWithOptions(t, opts, "agent-1", tool.Name, map[string]any{"order_id": 42})
		if res.IsError {
			t.Fatalf("%s: %+v", tool.Name, res.Content)
		}
	}

	gate.mu.Lock()
	defer gate.mu.Unlock()
	writeTools := 0
	for _, tool := range tools {
		if tool.Permission == registry.PermissionRead {
			continue
		}
		writeTools++
	}
	if len(gate.begins) != writeTools {
		t.Fatalf("%d operations begun for %d write tools: %+v", len(gate.begins), writeTools, gate.begins)
	}
	if len(gate.finishes) != writeTools {
		t.Fatalf("%d operations finished for %d write tools", len(gate.finishes), writeTools)
	}
	for _, begun := range gate.begins {
		if begun.Tool == "magento.product.get" {
			t.Fatal("a read tool was gated")
		}
		if begun.ArgsHash == "" || begun.IdempotencyKey == "" || begun.EntityID == "" {
			t.Fatalf("incomplete ledger request: %+v", begun)
		}
	}
	for _, out := range gate.finishes {
		if out.State != "succeeded" || out.Audit == nil || out.Audit.Decision != "allow" {
			t.Fatalf("outcome = %+v, want succeeded with an allow audit", out)
		}
	}
}

// An ambiguous upstream outcome is UNKNOWN, never FAILED.
func TestAmbiguousOutcomesBecomeUnknown(t *testing.T) {
	ambiguous := []struct {
		name string
		err  error
	}{
		{"upstream_timeout", errs.New(errs.CodeUpstreamTimeout, "the call may have completed")},
		{"5xx after send", func() error {
			e := errs.New(errs.CodeUpstreamUnavailable, "Magento is unavailable")
			e.Retryable = true
			return e.WithDetail("outcome", "unknown")
		}()},
		{"transport after send", func() error {
			return errs.New(errs.CodeUpstreamUnavailable, "connection reset").WithDetail("outcome", "unknown")
		}()},
	}
	for _, tc := range ambiguous {
		t.Run(tc.name, func(t *testing.T) {
			gate := &fakeGate{}
			reg := registry.New()
			if err := reg.Register(gatedTool(t, "magento.invoice.capture", registry.PermissionWrite, tc.err)); err != nil {
				t.Fatal(err)
			}
			opts := Options{Version: "test", Registry: reg,
				BeginOperation: gate.begin, FinishOperation: gate.finish}
			res := callWithOptions(t, opts, "agent-1", "magento.invoice.capture", map[string]any{"order_id": 42})
			if !res.IsError {
				t.Fatalf("ambiguous failure returned a success: %+v", res.StructuredContent)
			}
			gate.mu.Lock()
			defer gate.mu.Unlock()
			if len(gate.finishes) != 1 {
				t.Fatalf("%d finishes, want 1", len(gate.finishes))
			}
			if gate.finishes[0].State != "unknown" {
				t.Fatalf("state = %q, want unknown (a retry would duplicate the mutation)", gate.finishes[0].State)
			}
			if gate.finishes[0].Audit == nil || gate.finishes[0].Audit.Decision != "error" {
				t.Fatalf("audit = %+v, want decision error", gate.finishes[0].Audit)
			}
		})
	}
}

// A lost lease means the attempt no longer owns its work: unknown, never failed.
// It surfaces as a Go error (the tool could not record its own outcome), not as
// a folded result envelope, so it is classified on that path.
func TestLeaseLostClassifiesAsUnknown(t *testing.T) {
	err := errs.New(errs.CodeConflict, "lease lost").WithCause(jobs.ErrLeaseLost)
	if out := classifyResult(nil, err); out.State != "unknown" {
		t.Fatalf("state = %q, want unknown", out.State)
	}
	if out := classifyOutcome(err); out.State != "unknown" {
		t.Fatalf("state = %q, want unknown", out.State)
	}
}

// A proven-not-applied failure is FAILED.
func TestProvenFailureBecomesFailed(t *testing.T) {
	gate := &fakeGate{}
	reg := registry.New()
	if err := reg.Register(gatedTool(t, "magento.product.create", registry.PermissionWrite,
		errs.New(errs.CodeValidationFailed, "Magento rejected the request as invalid"))); err != nil {
		t.Fatal(err)
	}
	opts := Options{Version: "test", Registry: reg,
		BeginOperation: gate.begin, FinishOperation: gate.finish}
	_ = callWithOptions(t, opts, "agent-1", "magento.product.create", map[string]any{"order_id": 42})
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.finishes) != 1 || gate.finishes[0].State != "failed" {
		t.Fatalf("finishes = %+v, want one failed", gate.finishes)
	}
}

// An operation that is UNKNOWN (or reconciling) is refused before the handler
// runs: the caller is told to poll, and no upstream request is issued.
func TestUnknownOperationIsRefusedWithoutUpstream(t *testing.T) {
	gate := &fakeGate{}
	gate.beginFn = func(OperationRequest) (OperationTicket, error) {
		return OperationTicket{OperationID: "op-1", Decision: "unknown"},
			errs.New(errs.CodeOperationUnknown, "operation outcome is unknown; poll and never retry")
	}
	ran := false
	tool := gatedTool(t, "magento.invoice.capture", registry.PermissionWrite, nil)
	original := tool.Register
	tool.Register = func(srv *mcp.Server) error {
		_ = original(srv) // register the same definition
		return nil
	}
	reg := registry.New()
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	opts := Options{Version: "test", Registry: reg,
		BeginOperation: gate.begin, FinishOperation: gate.finish}
	res := callWithOptions(t, opts, "agent-1", "magento.invoice.capture", map[string]any{"order_id": 42})
	if !res.IsError {
		t.Fatal("an unknown operation was allowed to run")
	}
	body, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(body), "operation_unknown") {
		t.Fatalf("body = %s, want operation_unknown", body)
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.finishes) != 0 {
		t.Fatalf("%d finishes after a refused call, want 0", len(gate.finishes))
	}
	_ = ran
}

// A replay returns the recorded result without calling the handler.
func TestReplayReturnsRecordedResult(t *testing.T) {
	gate := &fakeGate{}
	gate.beginFn = func(OperationRequest) (OperationTicket, error) {
		return OperationTicket{
			OperationID: "op-1", Decision: DecisionReplay, Replayed: true,
			ReplayResult: json.RawMessage(`{"invoice_id":555}`),
		}, nil
	}
	called := false
	tool := gatedTool(t, "magento.invoice.capture", registry.PermissionWrite, nil)
	original := tool.Register
	tool.Register = func(srv *mcp.Server) error {
		called = true
		return original(srv)
	}
	reg := registry.New()
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	opts := Options{Version: "test", Registry: reg,
		BeginOperation: gate.begin, FinishOperation: gate.finish}
	res := callWithOptions(t, opts, "agent-1", "magento.invoice.capture", map[string]any{"order_id": 42})
	if res.IsError {
		t.Fatalf("replay returned an error: %+v", res.Content)
	}
	body, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(body), "555") {
		t.Fatalf("replay body = %s, want the recorded result", body)
	}
	_ = called
}

// The middleware's derived key must be byte-identical to storage.DeriveKey, or a
// tool that drives its own ledger operation would create a second row.
func TestDeriveOperationKeyMatchesLedger(t *testing.T) {
	hash, err := confirm.CanonicalHash(json.RawMessage(`{"order_id":42}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := DeriveOperationKey("agent-1", hash), storage.DeriveKey("agent-1", hash); got != want {
		t.Fatalf("server key %q != ledger key %q", got, want)
	}
	// And the outcome-detail contract must match the client's.
	if outcomeDetail != magento.OutcomeDetail || outcomeUnknown != magento.OutcomeUnknown {
		t.Fatalf("outcome detail contract drifted: server(%q,%q) magento(%q,%q)",
			outcomeDetail, outcomeUnknown, magento.OutcomeDetail, magento.OutcomeUnknown)
	}
}

// A self-ledgered tool is the one declared exception: the middleware must not
// open a second operation for it. Every other write tool is gated.
func TestSelfLedgeredToolsAreSkippedButEverythingElseIsGated(t *testing.T) {
	gate := &fakeGate{}
	selfLedgered := "magento.invoice.capture"
	opts := Options{
		Version:           "test",
		Registry:          registry.New(),
		BeginOperation:    gate.begin,
		FinishOperation:   gate.finish,
		SelfLedgeredTools: map[string]bool{selfLedgered: true},
	}
	for _, name := range []string{selfLedgered, "magento.product.create"} {
		if err := opts.Registry.Register(gatedTool(t, name, registry.PermissionWrite, nil)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{selfLedgered, "magento.product.create"} {
		if res := callWithOptions(t, opts, "agent-1", name, map[string]any{"order_id": 42}); res.IsError {
			t.Fatalf("%s: %+v", name, res.Content)
		}
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.begins) != 1 || gate.begins[0].Tool != "magento.product.create" {
		t.Fatalf("gated tools = %+v, want only magento.product.create", gate.begins)
	}
}

// The middleware's derived key must be byte-identical to storage.DeriveKey, or a
// tool that drives its own ledger operation would create a second row.

// REL38-01: the middleware switches on every operation decision, and only
// proceed/replay may reach the upstream tool.
func TestEveryOperationDecisionIsHandled(t *testing.T) {
	cases := []struct {
		decision    string
		wantRun     bool
		wantIsError bool
		wantCode    string
	}{
		{DecisionProceed, true, false, ""},
		{DecisionReplay, false, false, ""},
		{DecisionRetry, false, true, "conflict"},
		{DecisionInProgress, false, true, "conflict"},
		{DecisionUnknown, false, true, "operation_unknown"},
		{"something-new", false, true, "internal"},
	}
	for _, tc := range cases {
		t.Run(tc.decision, func(t *testing.T) {
			gate := &fakeGate{}
			gate.beginFn = func(OperationRequest) (OperationTicket, error) {
				return OperationTicket{OperationID: "op-1", Fence: "fence", Decision: tc.decision,
					ReplayResult: json.RawMessage(`{"ok":true}`)}, nil
			}
			var calls atomic.Int32
			tool := registry.Tool{
				Name: "magento.product.create", Title: "create", Description: "test tool",
				Capability: "test.capability", Permission: registry.PermissionWrite,
				Timeout: 5 * time.Second,
			}
			tool.Register = func(srv *mcp.Server) error {
				toolerr.Add(srv, &mcp.Tool{Name: tool.Name, Title: tool.Title, Description: tool.Description},
					func(_ context.Context, _ *mcp.CallToolRequest, _ orderStateInput) (*mcp.CallToolResult, map[string]any, error) {
						calls.Add(1)
						return nil, map[string]any{"ok": true}, nil
					})
				return nil
			}
			reg := registry.New()
			if err := reg.Register(tool); err != nil {
				t.Fatal(err)
			}
			opts := Options{Version: "test", Registry: reg,
				BeginOperation: gate.begin, FinishOperation: gate.finish}
			res := callWithOptions(t, opts, "agent-1", "magento.product.create", map[string]any{"order_id": 42})
			if tc.wantRun && calls.Load() != 1 {
				t.Fatalf("handler called %d times, want 1", calls.Load())
			}
			if !tc.wantRun && calls.Load() != 0 {
				t.Fatalf("UPSTREAM CALLED %d times for decision %q; only proceed may run the tool", calls.Load(), tc.decision)
			}
			if res.IsError != tc.wantIsError {
				t.Fatalf("IsError = %v, want %v (%+v)", res.IsError, tc.wantIsError, res.StructuredContent)
			}
			if tc.wantCode != "" {
				body, _ := json.Marshal(res.StructuredContent)
				if !strings.Contains(string(body), tc.wantCode) {
					t.Fatalf("body = %s, want code %s", body, tc.wantCode)
				}
			}
			gate.mu.Lock()
			defer gate.mu.Unlock()
			if tc.wantRun {
				if len(gate.finishes) != 1 || gate.finishes[0].State != "succeeded" {
					t.Fatalf("finishes = %+v, want one succeeded", gate.finishes)
				}
				return
			}
			if len(gate.finishes) != 0 {
				t.Fatalf("%d finishes for a refused decision, want 0", len(gate.finishes))
			}
		})
	}
}

// A decision the middleware does not recognise must fail closed.
func TestUnknownDecisionFailsClosed(t *testing.T) {
	gate := &fakeGate{}
	gate.beginFn = func(OperationRequest) (OperationTicket, error) {
		return OperationTicket{OperationID: "op-1", Decision: "proceed-lol"}, nil
	}
	reg := registry.New()
	if err := reg.Register(gatedTool(t, "magento.product.create", registry.PermissionWrite, nil)); err != nil {
		t.Fatal(err)
	}
	opts := Options{Version: "test", Registry: reg,
		BeginOperation: gate.begin, FinishOperation: gate.finish}
	res := callWithOptions(t, opts, "agent-1", "magento.product.create", map[string]any{"order_id": 42})
	if !res.IsError {
		t.Fatal("an unrecognised decision was allowed to run the tool")
	}
}

// F6: a tool that registers through mcp.AddTool directly bypasses toolerr.Add.
// NewMCP's schema validation must still reject its open schema.
func TestNewMCPValidatesDirectAddToolSchema(t *testing.T) {
	reg := registry.New()
	err := reg.Register(registry.Tool{
		Name: "magento.product.direct", Title: "Direct schema",
		Description: "Direct schema fixture.", Capability: "magento.product.read",
		Permission: registry.PermissionRead, Timeout: 5 * time.Second,
		Annotations: mcp.ToolAnnotations{ReadOnlyHint: true},
		Register: func(srv *mcp.Server) error {
			mcp.AddTool(srv, &mcp.Tool{
				Name: "magento.product.direct", Title: "Direct schema", Description: "Direct schema fixture.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`),
			}, func(context.Context, *mcp.CallToolRequest, authzInput) (*mcp.CallToolResult, authzOutput, error) {
				return nil, authzOutput{OK: true}, nil
			})
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewMCP(Options{Registry: reg}); err == nil ||
		!strings.Contains(err.Error(), "additionalProperties") {
		t.Fatalf("NewMCP error = %v, want a direct mcp.AddTool open-schema rejection", err)
	}
}

// ARCH-07: a high-risk tool with no confirmation service used to execute with
// the human-approval gate silently skipped — a fail-open default a library
// consumer would never notice. NewMCP must refuse to start instead.
func TestNewMCPRequiresConfirmationServiceForApprovalTools(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "magento.product.delete", Title: "Delete product", Description: "Deletes a product.",
		Capability: "magento.product.write", Permission: registry.PermissionDestructive,
		Timeout: 30 * time.Second, RequiresConfirmation: true,
		Annotations: mcp.ToolAnnotations{DestructiveHint: boolPtr(true)},
		Register: func(srv *mcp.Server) error {
			mcp.AddTool(srv, &mcp.Tool{Name: "magento.product.delete"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
				return nil, struct{}{}, nil
			})
			return nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, err := NewMCP(Options{Registry: reg}); err == nil {
		t.Fatal("NewMCP accepted a confirmation-requiring tool with no confirmation service")
	} else if code := errs.CodeOf(err); code != errs.CodeInternal {
		t.Fatalf("code = %s, want internal", code)
	}

	// With a service supplied, the same registry starts.
	svc, err := confirm.NewService(confirm.NewMemoryStore(), time.Minute)
	if err != nil {
		t.Fatalf("confirm.NewService: %v", err)
	}
	if _, err := NewMCP(Options{Registry: reg, Confirm: svc}); err != nil {
		t.Fatalf("NewMCP with a confirmation service: %v", err)
	}
}

// A registry with no approval-requiring tool still starts without a service:
// the check must not make the confirmation service mandatory everywhere.
func TestNewMCPAllowsMissingConfirmationServiceWithoutApprovalTools(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "magento.product.get", Title: "Get product", Description: "Reads a product.",
		Capability: "magento.product.read", Permission: registry.PermissionRead,
		Timeout: 30 * time.Second, Annotations: mcp.ToolAnnotations{ReadOnlyHint: true},
		Register: func(srv *mcp.Server) error {
			mcp.AddTool(srv, &mcp.Tool{Name: "magento.product.get"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
				return nil, struct{}{}, nil
			})
			return nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := NewMCP(Options{Registry: reg}); err != nil {
		t.Fatalf("NewMCP refused a read-only registry without a confirmation service: %v", err)
	}
}

// toolResultText renders a tool result's text content for failure messages.
func toolResultText(res *mcp.CallToolResult) string {
	if res == nil {
		return "<nil>"
	}
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, " | ")
}

// SEC-12: /metrics exposes the tool inventory, per-tool counts and build_info.
// On a listener reachable off-host it must sit behind the same middleware as
// /mcp; health probes stay open so orchestrators can still use them.
func TestMetricsRequireAuthOnPublicListener(t *testing.T) {
	denied := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
	srv, err := NewMCP(Options{Version: "test", Registry: mustRegistry(t)})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	ts := httptest.NewServer(Handler(srv, Options{
		Registry:           mustRegistry(t),
		AuthMiddleware:     denied,
		Metrics:            http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }),
		MetricsRequireAuth: true,
	}))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/metrics status = %d, want 401 on a public listener", resp.StatusCode)
	}
	// Health probes must remain reachable without credentials.
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, resp.StatusCode)
		}
	}
}

// A loopback deployment keeps the unauthenticated scrape path it has today.
func TestMetricsStayOpenOnLoopback(t *testing.T) {
	srv, err := NewMCP(Options{Version: "test", Registry: mustRegistry(t)})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	ts := httptest.NewServer(Handler(srv, Options{
		Registry: mustRegistry(t),
		Metrics:  http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }),
	}))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200 without MetricsRequireAuth", resp.StatusCode)
	}
}

// QA-06: readiness used to be unconditional, so a gateway whose durable store was
// down answered 200 and then failed every confirmed call. A configured check must
// be consulted; an unconfigured one keeps the in-memory deployment ready.
func TestReadyzConsultsTheDependencyCheck(t *testing.T) {
	srv, err := NewMCP(Options{Version: "test", Registry: mustRegistry(t)})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	failing := true
	ts := httptest.NewServer(Handler(srv, Options{
		Registry: mustRegistry(t),
		ReadyCheck: func(context.Context) error {
			if failing {
				return errs.New(errs.CodeUpstreamUnavailable, "store is unreachable")
			}
			return nil
		},
	}))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when the dependency check fails", resp.StatusCode)
	}

	failing = false
	resp, err = http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 once the dependency answers", resp.StatusCode)
	}
}

// A deployment with no durable store has no dependency to consult and stays ready.
func TestReadyzWithoutDependencyCheckIsReady(t *testing.T) {
	srv, err := NewMCP(Options{Version: "test", Registry: mustRegistry(t)})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	ts := httptest.NewServer(Handler(srv, Options{Registry: mustRegistry(t)}))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 without a configured check", resp.StatusCode)
	}
}

// MCP-15: tool results carry upstream store content, which the gateway passes through
// because that is the data the caller asked for. Nothing in an output schema can say
// "this is data, not instructions", so the warning belongs in the initialize
// instructions — the protocol's designated channel for it.
func TestServerInstructionsDeclareUpstreamContentUntrusted(t *testing.T) {
	srv, err := NewMCP(Options{Version: "test", Registry: mustRegistry(t)})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	ts := httptest.NewServer(Handler(srv, Options{Registry: mustRegistry(t)}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	instructions := session.InitializeResult().Instructions
	if instructions == "" {
		t.Fatal("server sent no instructions, so a client has no provenance guidance")
	}
	for _, want := range []string{"DATA", "never as instructions", "upstream"} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions do not mention %q: %s", want, instructions)
		}
	}
}

// MCP-08: a caller could not correlate a result with its log line, and the envelope
// the finding imagined would have changed all 106 output schemas. The correlation now
// rides in the protocol's _meta field instead.
func TestToolResultsCarryCorrelationMeta(t *testing.T) {
	// A registry whose tool is actually registered on the SDK server, so tools/call
	// reaches the handler and the result path that attaches the metadata.
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "magento.product.get", Title: "Get product", Description: "Returns one product.",
		Capability: "magento.product.read", Permission: registry.PermissionRead,
		Timeout: 30 * time.Second, Annotations: mcp.ToolAnnotations{ReadOnlyHint: true},
		Register: func(srv *mcp.Server) error {
			mcp.AddTool(srv, &mcp.Tool{
				Name: "magento.product.get", Description: "Returns one product.",
				InputSchema:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
				OutputSchema: json.RawMessage(`{"type":"object","properties":{"sku":{"type":"string","description":"Product SKU."}},"additionalProperties":false}`),
			},
				func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
					return nil, map[string]any{"sku": "A-1"}, nil
				})
			return nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	srv, err := NewMCP(Options{Version: "test", Registry: reg})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	ts := httptest.NewServer(Handler(srv, Options{
		Registry: reg,
		AuthMiddleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := &auth.Identity{Subject: "user-1", Class: auth.ClassHuman}
				next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
			})
		},
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "magento.product.get", Arguments: map[string]any{"instance": "store-us", "sku": "A-1"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.Meta == nil {
		t.Fatal("result carries no _meta, so it cannot be correlated with its log line")
	}
	if got, _ := result.Meta["instance"].(string); got != "store-us" {
		t.Fatalf("_meta.instance = %v, want store-us", result.Meta["instance"])
	}
	if got, _ := result.Meta["tool"].(string); got != "magento.product.get" {
		t.Fatalf("_meta.tool = %v", result.Meta["tool"])
	}
	if got, _ := result.Meta["subject"].(string); got != "user-1" {
		t.Fatalf("_meta.subject = %v, want the verified identity", result.Meta["subject"])
	}
}

// The copy rule has to reach an agent before it writes, so it lives in the initialize
// instructions — the one place every client reads — rather than only in a tool
// description, which is capped at 1024 characters.
func TestServerInstructionsRequireDiscoveryBeforeCopying(t *testing.T) {
	srv, err := NewMCP(Options{Version: "test", Registry: mustRegistry(t)})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	ts := httptest.NewServer(Handler(srv, Options{Registry: mustRegistry(t)}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	instructions := session.InitializeResult().Instructions
	// The copy contract is compressed to red lines; the step-by-step recipe moved to
	// the product_copy prompt and the skill (t21 design §3.3). The red lines must
	// still be resident, because a client that never loads a prompt sees only this.
	for _, want := range []string{
		"COPYING PRODUCTS",
		"discover before you write",
		"every configurable option and its values",
		"the full attribute set",
		"its categories",
		"Do not infer an option from a description",
		"without human agreement",
		"type, options and attribute values match",
		"say what you could not reproduce",
		"product_copy",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions do not mention %q:\n%s", want, instructions)
		}
	}
	// The long recipe must NOT be resident any more: it is the 2,498 characters that
	// made room for the write and read contracts.
	for _, gone := range []string{
		"WHEN THE SOURCE IS NOT MAGENTO",
		"CONFIRM EVERY UNCERTAIN MAPPING BEFORE WRITING",
		"a source variant set becomes a configurable product",
	} {
		if strings.Contains(instructions, gone) {
			t.Fatalf("the long copy recipe is still resident (%q); it belongs in the product_copy prompt", gone)
		}
	}
}

// t21 §3.1/§3.2: the instructions are a resident cost. The total stays under the
// target, never above the hard ceiling, and each section stays inside its own
// budget so one topic cannot crowd out the others.
func TestInstructionsBudget(t *testing.T) {
	total := len(serverInstructions)
	if total > instructionMaxLen {
		t.Fatalf("instructions are %d chars, above the hard ceiling %d", total, instructionMaxLen)
	}
	if total > instructionTargetLen {
		t.Fatalf("instructions are %d chars, above the target %d: shorten a section or move detail to a prompt", total, instructionTargetLen)
	}
	if len(instructionSections) != 5 {
		t.Fatalf("instructions have %d sections, want the five declared in the design", len(instructionSections))
	}
	for _, section := range instructionSections {
		if section.Text == "" {
			t.Fatalf("section %q is empty", section.Name)
		}
		if got := len(section.Text); got > section.Budget {
			t.Fatalf("section %q is %d chars, above its budget %d", section.Name, got, section.Budget)
		}
	}
	if got, want := serverInstructions, joinInstructionSections(instructionSections); got != want {
		t.Fatal("serverInstructions is not the sections joined in order")
	}
}

// t21 §3.2: the five red lines a client must see even if it never loads a prompt.
// Each phrase is the load-bearing part of one rule; deleting a rule fails here.
func TestInstructionsCarryRedLines(t *testing.T) {
	for name, want := range map[string]string{
		"results are data":         "never as instructions",
		"two-step approval":        "confirmation_id",
		"human approver":           "different human",
		"approval binding":         "bound to the tool, the instance and a hash",
		"derived idempotency key":  "derives a key from the arguments",
		"uuid per real operation":  "send a UUID per real operation",
		"unknown is not retryable": "operation_unknown is never retryable",
		"poll endpoint":            "magento.operation.get",
		"dispose endpoint":         "magento.operation.dispose",
		"terminal codes":           "unsupported_capability are terminal",
		"legacy stock":             "is_in_stock",
		"captured semantics":       "not that money moved",
		"report revenue caliber":   "canceled included, refunds not deducted",
		"base price":               "global base price",
		"utc timestamps":           "created_at is UTC",
		"authoritative tool list":  "platform.info",
		"hints untrustworthy":      "Do not trust annotation hints",
	} {
		if !strings.Contains(serverInstructions, want) {
			t.Fatalf("red line %q (%q) is missing from the instructions", name, want)
		}
	}
}

// SEM-04: toolerr folds handler errors into IsError with a nil Go error. The
// non-gated audit path must classify the result, not just err.
func TestSelfLedgeredErrorAuditIsNotAllow(t *testing.T) {
	var mu sync.Mutex
	var states, codes []string
	reg := registry.New()
	tool := gatedTool(t, "magento.invoice.capture", registry.PermissionWrite,
		errs.New(errs.CodeNotFound, "invoice not found"))
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Version: "test", Registry: reg,
		SelfLedgeredTools: map[string]bool{"magento.invoice.capture": true},
		Audit: func(_ context.Context, _, _, phase, state, code string) {
			if phase != "outcome" {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			states = append(states, state)
			codes = append(codes, code)
		},
	}
	res := callWithOptions(t, opts, "agent-1", "magento.invoice.capture", map[string]any{"order_id": 42})
	if !res.IsError {
		t.Fatal("handler error was not returned")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(states) != 1 || states[0] != "failed" || len(codes) != 1 || codes[0] != string(errs.CodeNotFound) {
		t.Fatalf("audit outcome = states %v codes %v, want failed/not_found", states, codes)
	}
}

// SEM-18: the request id is installed on the context and appears in both the
// audit callback and the result _meta.
func TestRequestIDFlowsToAuditAndMeta(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(authzStubTool(t, "magento.product.get", registry.PermissionRead, nil)); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var ids []string
	opts := Options{Version: "test", Registry: reg,
		Audit: func(ctx context.Context, _, _, _, _, _ string) {
			mu.Lock()
			defer mu.Unlock()
			ids = append(ids, RequestID(ctx))
		},
	}
	res := callWithOptions(t, opts, "agent-1", "magento.product.get", map[string]any{"instance": "store-us"})
	if res.IsError {
		t.Fatalf("call failed: %+v", res.Content)
	}
	meta, _ := res.Meta["request_id"].(string)
	if meta == "" {
		t.Fatalf("result _meta has no request_id: %+v", res.Meta)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ids) < 2 || ids[0] == "" || ids[0] != ids[1] || ids[0] != meta {
		t.Fatalf("audit request ids = %v, meta = %q; want two equal non-empty ids", ids, meta)
	}
}

// SEM-22: a budget error that arrives at the deadline stays rate_limited and
// retryable; it is not rewritten to upstream_timeout.
func TestRateLimitedNotMaskedByTimeout(t *testing.T) {
	reg := registry.New()
	tool := registry.Tool{
		Name: "magento.product.rate", Title: "Rate-limited tool",
		Description: "Rate-limit fixture.", Capability: "magento.product.read",
		Permission: registry.PermissionRead, Timeout: 20 * time.Millisecond,
		Annotations: mcp.ToolAnnotations{ReadOnlyHint: true},
	}
	tool.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, &mcp.Tool{Name: tool.Name, Title: tool.Title, Description: tool.Description,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
			func(ctx context.Context, _ *mcp.CallToolRequest, _ authzInput) (*mcp.CallToolResult, authzOutput, error) {
				<-ctx.Done()
				e := errs.New(errs.CodeRateLimited, "instance request budget exhausted")
				e.Retryable = true
				return nil, authzOutput{}, e
			})
		return nil
	}
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	srv, err := NewMCP(Options{Version: "test", Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	res := callToolHTTP(t, srv, reg, "magento.product.rate", nil)
	body := resultMap(t, res)
	if body["code"] != string(errs.CodeRateLimited) {
		t.Fatalf("code = %v, want rate_limited (not upstream_timeout)", body["code"])
	}
	if body["retryable"] != true {
		t.Fatalf("retryable = %v, want true: %+v", body["retryable"], body)
	}
}

// SEM-19: a replay validates any supplied confirmation. A never-issued UUID and
// an expired approval must not become a successful replay.
func TestReplayValidatesConfirmation(t *testing.T) {
	store := confirm.NewMemoryStore()
	svc, err := confirm.NewService(store, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"instance": "store-us", "order_id": 42}
	raw, _ := json.Marshal(args)
	hash, err := confirm.CanonicalHash(raw)
	if err != nil {
		t.Fatal(err)
	}
	req, err := svc.Require(context.Background(), "magento.invoice.capture", "store-us", "write", "order 42", "agent-1", raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(context.Background(), req.ID, "human-1"); err != nil {
		t.Fatal(err)
	}
	gate := &fakeGate{beginFn: func(OperationRequest) (OperationTicket, error) {
		return OperationTicket{OperationID: "op-1", Decision: DecisionReplay, Replayed: true,
			ReplayResult: json.RawMessage(`{"invoice_id":555}`)}, nil
	}}
	reg := registry.New()
	replayTool := gatedTool(t, "magento.invoice.capture", registry.PermissionWrite, nil)
	replayTool.RequiresConfirmation = true
	if err := reg.Register(replayTool); err != nil {
		t.Fatal(err)
	}
	opts := Options{Version: "test", Registry: reg, Confirm: svc,
		BeginOperation: gate.begin, FinishOperation: gate.finish}
	valid := callWithOptions(t, opts, "agent-1", "magento.invoice.capture",
		map[string]any{"instance": "store-us", "order_id": 42, "confirmation_id": req.ID})
	if valid.IsError {
		t.Fatalf("valid replay failed: content=%+v structured=%+v", valid.Content, valid.StructuredContent)
	}
	if got, _ := store.Get(context.Background(), req.ID); got == nil || got.State != confirm.StateApproved {
		t.Fatalf("read-only replay validation changed the approval: %+v", got)
	}

	random := callWithOptions(t, opts, "agent-1", "magento.invoice.capture",
		map[string]any{"instance": "store-us", "order_id": 42, "confirmation_id": "00000000-0000-4000-8000-000000000000"})
	if !random.IsError || resultMap(t, random)["code"] != string(errs.CodeNotFound) {
		t.Fatalf("never-issued confirmation replay = %+v, want not_found", random.StructuredContent)
	}

	expired := &confirm.Request{
		ID: "11111111-1111-4111-8111-111111111111", Tool: "magento.invoice.capture",
		Instance: "store-us", Scope: "write", ParamsHash: hash, Requester: "agent-1",
		State: confirm.StateApproved, ExpiresAt: time.Now().Add(-time.Minute),
	}
	if err := store.Create(context.Background(), expired); err != nil {
		t.Fatal(err)
	}
	expiredRes := callWithOptions(t, opts, "agent-1", "magento.invoice.capture",
		map[string]any{"instance": "store-us", "order_id": 42, "confirmation_id": expired.ID})
	if !expiredRes.IsError || resultMap(t, expiredRes)["code"] != string(errs.CodeConfirmationExpired) {
		t.Fatalf("expired confirmation replay = %+v, want confirmation_expired", expiredRes.StructuredContent)
	}
}

// SEM-41: early policy paths carry the same correlation _meta as a handler result.
func TestEarlyPolicyResultsCarryMeta(t *testing.T) {
	t.Run("denied", func(t *testing.T) {
		reg := registry.New()
		if err := reg.Register(authzStubTool(t, "magento.product.get", registry.PermissionRead, nil)); err != nil {
			t.Fatal(err)
		}
		opts := Options{Version: "test", Registry: reg,
			Authorize: func(context.Context, string, string, string) error {
				return errs.New(errs.CodePermissionDenied, "denied")
			}}
		res := callWithOptions(t, opts, "agent-1", "magento.product.get", map[string]any{"instance": "store-us"})
		if res.Meta == nil || res.Meta["request_id"] == nil || res.Meta["request_id"] == "" {
			t.Fatalf("denied result meta = %+v", res.Meta)
		}
	})
	t.Run("confirmation_required", func(t *testing.T) {
		reg := registry.New()
		if err := reg.Register(gatedTool(t, "magento.invoice.capture", registry.PermissionWrite, nil)); err != nil {
			t.Fatal(err)
		}
		svc, err := confirm.NewService(confirm.NewMemoryStore(), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		opts := Options{Version: "test", Registry: reg, Confirm: svc}
		res := callToolNoApprove(t, opts, "agent-1", "magento.invoice.capture", map[string]any{"order_id": 42})
		if res.Meta == nil || res.Meta["request_id"] == nil || res.Meta["request_id"] == "" {
			t.Fatalf("confirmation_required meta = %+v", res.Meta)
		}
	})
	t.Run("replay", func(t *testing.T) {
		gate := &fakeGate{beginFn: func(OperationRequest) (OperationTicket, error) {
			return OperationTicket{OperationID: "op-1", Decision: DecisionReplay, Replayed: true,
				ReplayResult: json.RawMessage(`{"ok":true}`)}, nil
		}}
		reg := registry.New()
		if err := reg.Register(gatedTool(t, "magento.invoice.capture", registry.PermissionWrite, nil)); err != nil {
			t.Fatal(err)
		}
		opts := Options{Version: "test", Registry: reg,
			BeginOperation: gate.begin, FinishOperation: gate.finish}
		res := callWithOptions(t, opts, "agent-1", "magento.invoice.capture", map[string]any{"order_id": 42})
		if res.Meta == nil || res.Meta["request_id"] == nil || res.Meta["request_id"] == "" {
			t.Fatalf("replay meta = %+v", res.Meta)
		}
	})
}

// SEM-42: a schema rejection larger than errs' 1024-byte detail cap still
// carries details.validation (bounded), rather than dropping it.
// schemaFixtureInput matches the schema the two schema-validation fixtures declare;
// the registration-time schema/struct check requires the two to agree.
type schemaFixtureInput struct {
	Value string `json:"value"`
}

func TestSchemaValidationDetailSurvivesBoundedDetail(t *testing.T) {
	reg := registry.New()
	tool := registry.Tool{
		Name: "magento.product.schema", Title: "Schema fixture",
		Description: "Schema detail fixture.", Capability: "magento.product.read",
		Permission: registry.PermissionRead, Timeout: 5 * time.Second,
		Annotations: mcp.ToolAnnotations{ReadOnlyHint: true},
	}
	tool.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, &mcp.Tool{
			Name: tool.Name, Title: tool.Title, Description: tool.Description,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string","maxLength":10}},"required":["value"],"additionalProperties":false}`),
		}, func(context.Context, *mcp.CallToolRequest, schemaFixtureInput) (*mcp.CallToolResult, authzOutput, error) {
			return nil, authzOutput{OK: true}, nil
		})
		return nil
	}
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	srv, err := NewMCP(Options{Version: "test", Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	res := callToolHTTP(t, srv, reg, "magento.product.schema", map[string]any{"value": strings.Repeat("x", 2000)})
	body := resultMap(t, res)
	details, _ := body["details"].(map[string]any)
	validation, _ := details["validation"].(string)
	if validation == "" || len(validation) > 1024 {
		t.Fatalf("details.validation = %q (len %d), want non-empty <=1024", validation, len(validation))
	}
}

// callToolNoApprove is callWithOptions without the automatic approval retry, so
// the early confirmation_required result can be inspected.
func callToolNoApprove(t *testing.T, opts Options, subject, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	handler := HandlerForTest(t, opts, subject)
	ts := httptest.NewServer(handler)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// CV-PROTO-01: a successful replay is a completed call and must write an
// outcome audit row, not leave the intent dangling.
func TestReplayWritesOutcomeAudit(t *testing.T) {
	gate := &fakeGate{beginFn: func(OperationRequest) (OperationTicket, error) {
		return OperationTicket{OperationID: "op-replay", Decision: DecisionReplay, Replayed: true,
			ReplayResult: json.RawMessage(`{"invoice_id":555}`)}, nil
	}}
	reg := registry.New()
	if err := reg.Register(gatedTool(t, "magento.invoice.capture", registry.PermissionWrite, nil)); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var phases, states, codes []string
	opts := Options{Version: "test", Registry: reg,
		BeginOperation: gate.begin, FinishOperation: gate.finish,
		Audit: func(_ context.Context, _, _, phase, state, code string) {
			mu.Lock()
			defer mu.Unlock()
			phases = append(phases, phase)
			states = append(states, state)
			codes = append(codes, code)
		},
	}
	res := callWithOptions(t, opts, "agent-1", "magento.invoice.capture", map[string]any{"order_id": 42})
	if res.IsError {
		t.Fatalf("replay failed: %+v", res.Content)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(phases) != 2 || phases[0] != "intent" || phases[1] != "outcome" {
		t.Fatalf("audit phases = %v, want intent then outcome", phases)
	}
	if states[1] != "succeeded" || codes[1] != "" {
		t.Fatalf("replay outcome = state %q code %q, want succeeded/empty", states[1], codes[1])
	}
}

// CV-PROTO-02: the outcome audit event built for a ledger-gated write carries
// the same request_id as the intent and the result _meta. This test uses a real
// write tool and a real BeginOperation/FinishOperation path, not a read tool.
func TestLedgerGatedWriteOutcomeAuditHasRequestID(t *testing.T) {
	gate := &fakeGate{}
	reg := registry.New()
	if err := reg.Register(gatedTool(t, "magento.product.update", registry.PermissionWrite, nil)); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var intentID string
	opts := Options{Version: "test", Registry: reg,
		BeginOperation: gate.begin, FinishOperation: gate.finish,
		Audit: func(ctx context.Context, _, _, phase, _, _ string) {
			if phase != "intent" {
				return
			}
			mu.Lock()
			intentID = RequestID(ctx)
			mu.Unlock()
		},
	}
	res := callWithOptions(t, opts, "agent-1", "magento.product.update", map[string]any{"instance": "store-us", "order_id": 42})
	if res.IsError {
		t.Fatalf("gated write failed: content=%+v structured=%+v", res.Content, res.StructuredContent)
	}
	metaID, _ := res.Meta["request_id"].(string)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.finishes) != 1 || gate.finishes[0].Audit == nil {
		t.Fatalf("finishes = %+v, want one outcome with audit", gate.finishes)
	}
	outcomeID := gate.finishes[0].Audit.RequestID
	mu.Lock()
	defer mu.Unlock()
	if intentID == "" || outcomeID == "" || metaID == "" {
		t.Fatalf("request ids: intent=%q outcome=%q meta=%q; want all non-empty", intentID, outcomeID, metaID)
	}
	if intentID != outcomeID || intentID != metaID {
		t.Fatalf("request ids: intent=%q outcome=%q meta=%q; want all equal", intentID, outcomeID, metaID)
	}
	if gate.finishes[0].Audit.Phase != "outcome" || gate.finishes[0].Audit.Decision != "allow" {
		t.Fatalf("outcome audit = %+v, want phase outcome/decision allow", gate.finishes[0].Audit)
	}
}

// U-80 / SEC-05: an instance value that cannot name any configured instance is
// refused before the audit sink sees it. The raw argument used to be recorded
// first and rejected by the tool schema afterwards, so a caller could write
// arbitrary strings — 5000-character values, embedded newlines, forged
// attribution lines — into audit_events.
func TestForgedInstanceIsRejectedBeforeAudit(t *testing.T) {
	reg := registry.New()
	ran := false
	if err := reg.Register(authzStubTool(t, "magento.product.get", registry.PermissionRead, &ran)); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var audited []string
	snapshot := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), audited...)
	}
	opts := Options{Version: "test", Registry: reg,
		Audit: func(_ context.Context, _, instance, phase, _, _ string) {
			mu.Lock()
			defer mu.Unlock()
			audited = append(audited, phase+"="+instance)
		}}
	srv, err := NewMCP(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, forged := range []string{
		"demo\nFORGED audit line: subject=admin",
		strings.Repeat("A", 5000),
		"../../demo",
		"demo zone",
	} {
		res := callToolHTTP(t, srv, reg, "magento.product.get", map[string]any{"instance": forged})
		if code := resultMap(t, res)["code"]; code != "invalid_argument" {
			t.Fatalf("instance %q: code = %v, want invalid_argument", forged, code)
		}
		if res.Meta != nil {
			if echoed, ok := res.Meta["instance"]; ok {
				t.Fatalf("instance %q was echoed in _meta: %v", forged, echoed)
			}
		}
		details, _ := resultMap(t, res)["details"].(map[string]any)
		if details["validation"] == "" || details["validation"] == nil {
			t.Fatalf("instance %q: details.validation is missing (%v)", forged, details)
		}
	}
	if ran {
		t.Fatal("the handler ran for a forged instance")
	}
	if got := snapshot(); len(got) != 0 {
		t.Fatalf("audit rows written for rejected instances: %v", got)
	}

	// A well-formed id must still be audited even when it names no configured
	// instance: the guard bounds the shape, it does not decide authorization.
	callToolHTTP(t, srv, reg, "magento.product.get", map[string]any{"instance": "demo2"})
	if got := snapshot(); len(got) != 2 || got[0] != "intent=demo2" || got[1] != "outcome=demo2" {
		t.Fatalf("audit rows for a well-formed unknown instance = %v, want intent+outcome on demo2", got)
	}
}

// U-28: an unknown tool name is a caller error. Before this check the call fell
// through to the SDK, whose plain error became code=internal — the same code a
// real crash returns, so a client could not tell "fix your tool name" from
// "page someone".
func TestUnknownToolReturnsNotFoundNotInternal(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(authzStubTool(t, "magento.product.get", registry.PermissionRead, nil)); err != nil {
		t.Fatal(err)
	}
	srv, err := NewMCP(Options{Version: "test", Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	res := callToolHTTP(t, srv, reg, "magento.operation.status", map[string]any{"instance": "demo"})
	body := resultMap(t, res)
	if body["code"] != "not_found" {
		t.Fatalf("code = %v, want not_found (internal means the SDK error leaked)", body["code"])
	}
	details, _ := body["details"].(map[string]any)
	if details["tool"] != "magento.operation.status" {
		t.Fatalf("details.tool = %v, want the requested tool name", details["tool"])
	}
	if text := resultText(t, res); !strings.Contains(text, "not_found") {
		t.Fatalf("text = %q, want the code", text)
	}
}

// U-28 (second half): the text channel of a schema rejection names the field and
// rule, not just "arguments do not match the tool schema". An agent that prints
// only content used to have no way to see which property was wrong.
func TestSchemaRejectionTextCarriesValidationDetail(t *testing.T) {
	reg := registry.New()
	tool := registry.Tool{
		Name: "magento.product.schema", Title: "Schema fixture",
		Description: "Schema detail fixture.", Capability: "magento.product.read",
		Permission: registry.PermissionRead, Timeout: 5 * time.Second,
		Annotations: mcp.ToolAnnotations{ReadOnlyHint: true},
	}
	tool.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, &mcp.Tool{
			Name: tool.Name, Title: tool.Title, Description: tool.Description,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string","maxLength":10,"description":"Bounded value."}},"required":["value"],"additionalProperties":false}`),
		}, func(context.Context, *mcp.CallToolRequest, schemaFixtureInput) (*mcp.CallToolResult, authzOutput, error) {
			return nil, authzOutput{OK: true}, nil
		})
		return nil
	}
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	srv, err := NewMCP(Options{Version: "test", Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	res := callToolHTTP(t, srv, reg, "magento.product.schema", map[string]any{"value": strings.Repeat("x", 40)})
	if code := resultMap(t, res)["code"]; code != "invalid_argument" {
		t.Fatalf("code = %v, want invalid_argument", code)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "validation:") || !strings.Contains(text, "maxLength") {
		t.Fatalf("text = %q, want the validation detail (property + rule) inline", text)
	}
}

// U-85: the event stream must declare UTF-8. The SDK writes
// "Content-Type: text/event-stream" with no charset, and HTTP then defaults a
// text/* type to ISO-8859-1, so a client that follows the default decodes Polish
// product names and error text as mojibake.
func TestEventStreamResponseDeclaresUTF8(t *testing.T) {
	srv, _ := newTestServer(t)
	ts := httptest.NewServer(Handler(srv, Options{Registry: mustRegistry(t)}))
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"charset-test","version":"0"}}}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want an event stream for this request", contentType)
	}
	if !strings.Contains(strings.ToLower(contentType), "charset=utf-8") {
		t.Fatalf("Content-Type = %q, want charset=utf-8", contentType)
	}
}

// U-81: a required `instance` property may not be described as resolved by the
// server. 101 tools require the field; the description used to promise it could
// be omitted when one instance was configured, so the first call an agent made
// from the description failed with invalid_argument.
func TestMagentoInstanceDescriptionsMatchRequiredSchema(t *testing.T) {
	signer, err := magento.ParseCredential("bearer:test")
	if err != nil {
		t.Fatal(err)
	}
	client, err := magento.NewClient(magento.ClientConfig{
		BaseURL: "https://shop.example.com", StoreCode: "default", Signer: signer, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := magento.NewService(client)
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	if err := magento.Register(reg, svc); err != nil {
		t.Fatal(err)
	}
	if err := magento.RegisterWrites(reg, svc); err != nil {
		t.Fatal(err)
	}
	resolver, err := magento.NewResolver(map[string]*magento.Service{"demo": svc})
	if err != nil {
		t.Fatal(err)
	}
	if err := magento.RegisterImportJobs(reg, resolver, importQueueStub{}, importBatchStub{}); err != nil {
		t.Fatal(err)
	}
	srv, err := NewMCP(Options{Version: "test", Registry: reg, Confirm: serverTestConfirm(t)})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()
	mcpClient, err := mcp.NewClient(&mcp.Implementation{Name: "schema-audit", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mcpClient.Close() }()
	list, err := mcpClient.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	claims := []string{"resolved by the server", "may be omitted", "can be omitted"}
	checked := 0
	for _, tool := range list.Tools {
		for _, claim := range claims {
			if strings.Contains(strings.ToLower(tool.Description), claim) {
				t.Errorf("%s: tool description still says %q while instance is required", tool.Name, claim)
			}
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("%s: input schema: %v", tool.Name, err)
		}
		var schema struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s: input schema: %v", tool.Name, err)
		}
		property, hasInstance := schema.Properties["instance"]
		if !hasInstance {
			continue
		}
		required := slices.Contains(schema.Required, "instance")
		if required {
			checked++
			for _, claim := range claims {
				if strings.Contains(strings.ToLower(property.Description), claim) {
					t.Errorf("%s: instance is required but described as %q", tool.Name, claim)
				}
			}
		}
	}
	if checked < 100 {
		t.Fatalf("checked %d tools with a required instance, want the whole catalog (read, write and import)", checked)
	}
}

// importQueueStub and importBatchStub satisfy magento's async-import interfaces so
// the schema audit can register the import tools without a database or worker.
type importQueueStub struct{}

func (importQueueStub) Enqueue(context.Context, jobs.Task) (string, error) { return "", nil }
func (importQueueStub) Cancel(context.Context, string, string, string) error {
	return nil
}
func (importQueueStub) GetJob(context.Context, string, string, string) (*jobs.JobRecord, error) {
	return nil, nil
}

type importBatchStub struct{}

func (importBatchStub) GetImportBatchByJob(context.Context, string, string, string) (*jobs.ImportBatch, error) {
	return nil, nil
}
func (importBatchStub) GetImportBatch(context.Context, string, string, string) (*jobs.ImportBatch, error) {
	return nil, nil
}
func (importBatchStub) ListImportBatches(context.Context, string, int) ([]jobs.ImportBatch, error) {
	return nil, nil
}

// F1/U-78: the outcome audit row is written after the tool returns, which is
// exactly when a timed-out call has an already-expired context. recordOutcome
// used to pass that dead context to the audit sink, so the write failed and the
// intent row dangled (SEC-01). Reverting to the request context makes this test
// fail: the callback would observe context.DeadlineExceeded.
func TestOutcomeAuditSeesLiveContextAfterTimeout(t *testing.T) {
	reg := registry.New()
	tool := registry.Tool{
		Name: "magento.product.slow", Title: "Slow tool",
		Description: "Timeout fixture.", Capability: "magento.product.read",
		Permission: registry.PermissionRead, Timeout: 20 * time.Millisecond,
		Annotations: mcp.ToolAnnotations{ReadOnlyHint: true},
	}
	tool.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, &mcp.Tool{Name: tool.Name, Title: tool.Title, Description: tool.Description,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
			func(ctx context.Context, _ *mcp.CallToolRequest, _ authzInput) (*mcp.CallToolResult, authzOutput, error) {
				<-ctx.Done()
				return nil, authzOutput{}, ctx.Err()
			})
		return nil
	}
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var outcomeErrs []error
	var sawOutcome bool
	opts := Options{Version: "test", Registry: reg,
		Audit: func(ctx context.Context, _, _, phase, _, _ string) {
			if phase != "outcome" {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			sawOutcome = true
			outcomeErrs = append(outcomeErrs, ctx.Err())
		},
	}
	res := callWithOptions(t, opts, "agent-1", tool.Name, map[string]any{"instance": "store-us"})
	if !res.IsError {
		t.Fatalf("a tool that exceeded its deadline must fail: %+v", res.StructuredContent)
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawOutcome {
		t.Fatal("no outcome audit event: the intent row would dangle")
	}
	if len(outcomeErrs) != 1 || outcomeErrs[0] != nil {
		t.Fatalf("outcome audit context error = %v, want nil (the audit write must not inherit the request's cancellation)", outcomeErrs)
	}
}

// fullMagentoTools builds every Magento tool the production server registers and
// reads them back over an in-memory MCP session, so the guidance tests below run
// over the real catalogue (descriptions AND input schemas). Only exported
// constructors are used; no network call is made.
// guideTool is the part of a registered tool the guidance checks read.
type guideTool struct {
	Name          string
	Description   string
	Properties    map[string]any
	OutProperties map[string]any
	Output        map[string]any
}

func fullMagentoTools(t *testing.T) map[string]guideTool {
	t.Helper()
	client, err := magento.NewClient(magento.ClientConfig{
		BaseURL: "https://shop.example.com", StoreCode: "default", Signer: noopTestSigner{},
	})
	if err != nil {
		t.Fatalf("magento client: %v", err)
	}
	svc, err := magento.NewService(client)
	if err != nil {
		t.Fatalf("magento service: %v", err)
	}
	resolver, err := magento.NewResolver(map[string]*magento.Service{"demo": svc})
	if err != nil {
		t.Fatalf("magento resolver: %v", err)
	}
	reg := registry.New()
	if err := magento.RegisterWithResolver(reg, resolver); err != nil {
		t.Fatalf("register magento read tools: %v", err)
	}
	if err := magento.RegisterWritesWithResolver(reg, resolver); err != nil {
		t.Fatalf("register magento write tools: %v", err)
	}
	if err := magento.RegisterImportJobs(reg, resolver, inertImportQueue{}); err != nil {
		t.Fatalf("register import tools: %v", err)
	}
	srv, err := NewMCP(Options{Version: "test", Registry: reg, Confirm: serverTestConfirm(t)})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()
	sdkClient := mcp.NewClient(&mcp.Implementation{Name: "guidance-test", Version: "0"}, nil)
	session, err := sdkClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	out := make(map[string]guideTool, len(listed.Tools))
	for _, tool := range listed.Tools {
		schema, _ := normaliseSchema(tool.InputSchema).(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		outputSchema, _ := normaliseSchema(tool.OutputSchema).(map[string]any)
		outProperties, _ := outputSchema["properties"].(map[string]any)
		out[tool.Name] = guideTool{
			Name: tool.Name, Description: tool.Description,
			Properties: properties, OutProperties: outProperties, Output: outputSchema,
		}
	}
	if len(out) < 90 {
		t.Fatalf("catalogue has %d tools; the guidance checks expect the full Magento catalogue", len(out))
	}
	return out
}

// normaliseSchema turns the wire schema (any) into the map shape the assertions
// walk; the SDK hands back a decoded JSON value.
func normaliseSchema(schema any) any {
	switch value := schema.(type) {
	case map[string]any:
		return value
	case json.RawMessage:
		var decoded map[string]any
		if err := json.Unmarshal(value, &decoded); err == nil {
			return decoded
		}
	case []byte:
		var decoded map[string]any
		if err := json.Unmarshal(value, &decoded); err == nil {
			return decoded
		}
	}
	return schema
}

// inertImportQueue satisfies magento.ImportQueue for a registry that never runs a
// job; the guidance checks only need the tools to exist.
type inertImportQueue struct{}

func (inertImportQueue) Enqueue(context.Context, jobs.Task) (string, error) { return "", nil }
func (inertImportQueue) Cancel(context.Context, string, string, string) error {
	return nil
}
func (inertImportQueue) GetJob(context.Context, string, string, string) (*jobs.JobRecord, error) {
	return nil, nil
}

// noopTestSigner satisfies magento.Signer for a registry that never calls out.
type noopTestSigner struct{}

func (noopTestSigner) Sign(*http.Request) error { return nil }

// t21 Task A acceptance: every tool description stays inside the 1,024-character
// contract limit and the catalogue total stays inside its budget.
func TestToolDescriptionBudget(t *testing.T) {
	tools := fullMagentoTools(t)
	total := 0
	for name, tool := range tools {
		if tool.Description == "" {
			t.Fatalf("%s has no description", name)
		}
		if got := len(tool.Description); got > 1024 {
			t.Fatalf("%s description is %d chars, above the 1024 limit", name, got)
		}
		total += len(tool.Description)
	}
	if total > 45000 {
		t.Fatalf("descriptions total %d chars, above the 45000 budget", total)
	}
}

// t21 GG-16: write tools must document every input property, and the whole
// catalogue must stay above 95% so a new undocumented field fails here.
func TestWriteToolFieldDescriptions(t *testing.T) {
	tools := fullMagentoTools(t)
	totalFields, described := 0, 0
	var writeMissing []string
	for name, tool := range tools {
		properties := tool.Properties
		if len(properties) == 0 {
			continue
		}
		missing := undocumentedProperties(properties)
		if _, exempt := guidanceExemptions[name]; exempt {
			// Exempt tools keep their fields in the coverage denominator but are
			// not failed on: their description constants live outside this task's
			// file scope and the follow-up is a one-line edit there.
			missing = nil
		}
		totalFields += countProperties(properties)
		described += countProperties(properties) - len(missing)
		if isWriteToolName(name) && len(missing) > 0 {
			writeMissing = append(writeMissing, fmt.Sprintf("%s: %v", name, missing))
		}
	}
	if totalFields == 0 {
		t.Fatal("no input properties found")
	}
	if len(writeMissing) > 0 {
		t.Fatalf("write tools with undocumented input properties:\n%s", strings.Join(writeMissing, "\n"))
	}
	if ratio := float64(described) / float64(totalFields); ratio < 0.95 {
		t.Fatalf("input property description coverage is %.1f%% (%d/%d), want at least 95%%",
			ratio*100, described, totalFields)
	}
}

// isWriteToolName reports whether a tool mutates state. The registry permission is
// not on the wire tool, so the read-only annotation is the signal; the few write
// tools without a destructive hint still carry readOnlyHint=false.
func isWriteToolName(name string) bool {
	return !readOnlyToolName(name)
}

// readOnlyToolName lists the read tools by the prefix they share with their write
// siblings; it is the inverse of the write catalogue and is checked by
// TestToolDescriptionBudget's catalogue size.
func readOnlyToolName(name string) bool {
	for _, write := range []string{
		".create", ".update", ".delete", ".cancel", ".hold", ".unhold", ".capture",
		".email", ".comment", ".assign", ".unassign", ".transfer", ".set", ".add",
		".remove", ".upload", ".execute", ".start", ".resume", ".dispose", ".bulk",
	} {
		if strings.Contains(name, write) {
			return false
		}
	}
	return true
}

// outputPropertyDescription returns one output field's description.
func outputPropertyDescription(t *testing.T, tool guideTool, path string) string {
	t.Helper()
	current := map[string]any{"properties": tool.OutProperties}
	for _, field := range strings.Split(path, ".") {
		properties, _ := current["properties"].(map[string]any)
		prop, ok := properties[field].(map[string]any)
		if !ok {
			t.Fatalf("%s has no output field %q", tool.Name, path)
		}
		if items, ok := prop["items"].(map[string]any); ok {
			prop = items
		}
		current = prop
	}
	description, _ := current["description"].(string)
	return description
}

// withoutField drops one property name from a missing list.
func withoutField(missing []string, field string) []string {
	out := missing[:0]
	for _, name := range missing {
		if name != field {
			out = append(out, name)
		}
	}
	return out
}

// outputDescriptionContaining reports whether any output property description
// (recursively, including array items) contains the phrase.
func outputDescriptionContaining(schema map[string]any, phrase string) bool {
	properties, _ := schema["properties"].(map[string]any)
	for _, raw := range properties {
		prop, _ := raw.(map[string]any)
		if description, _ := prop["description"].(string); strings.Contains(description, phrase) {
			return true
		}
		if items, ok := prop["items"].(map[string]any); ok {
			if outputDescriptionContaining(items, phrase) {
				return true
			}
		}
	}
	return false
}

func undocumentedProperties(properties map[string]any) []string {
	var missing []string
	for name, raw := range properties {
		prop, _ := raw.(map[string]any)
		if description, _ := prop["description"].(string); description == "" {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func countProperties(properties map[string]any) int {
	total := 0
	for _, raw := range properties {
		prop, _ := raw.(map[string]any)
		total++
		if nested, ok := prop["properties"].(map[string]any); ok {
			total += countProperties(nested)
		}
		if items, ok := prop["items"].(map[string]any); ok {
			if nested, ok := items["properties"].(map[string]any); ok {
				total += countProperties(nested)
			}
		}
	}
	return total
}

// t21 §7: every `magento.*` name a description points at must exist in the
// registry, so a cross-reference cannot rot into a dead end.
func TestCrossReferencesExist(t *testing.T) {
	tools := fullMagentoTools(t)
	known := make(map[string]bool, len(tools))
	for name := range tools {
		known[name] = true
	}
	// Prompts are not tools but are legitimately named in guidance text.
	known["product_copy"] = true
	known["write_safety"] = true
	referenceRE := regexp.MustCompile(`magento\.[a-z][a-z0-9_.]*[a-z0-9]`)
	check := func(where, text string) {
		t.Helper()
		for _, ref := range referenceRE.FindAllString(text, -1) {
			ref = strings.TrimSuffix(ref, ".")
			if !known[ref] {
				t.Fatalf("%s references %q, which is not a registered tool", where, ref)
			}
		}
	}
	for name, tool := range tools {
		check("tool "+name, tool.Description)
	}
	check("server instructions", serverInstructions)
}

// t21 GG-07/GG-14 are closed by output field descriptions; these assertions stop
// a future edit from deleting the clarification again.
func TestClosedGuidanceRegressionsStayDocumented(t *testing.T) {
	tools := fullMagentoTools(t)
	inventoryStatus := outputPropertyDescription(t, tools["magento.inventory.status"], "status.qty")
	if !strings.Contains(inventoryStatus, "Legacy") || !strings.Contains(inventoryStatus, "is_in_stock") {
		t.Fatalf("inventory.status.qty no longer documents the legacy caveat: %q", inventoryStatus)
	}
	// The nested country row may be emitted as a $ref, so the check is on the
	// serialised output schema: the sentinel must be documented somewhere in it.
	geographySchema, err := json.Marshal(tools["magento.orders.geography"].Output)
	if err != nil {
		t.Fatalf("marshal geography output schema: %v", err)
	}
	if !strings.Contains(string(geographySchema), "UNKNOWN") {
		t.Fatal("orders.geography no longer documents the UNKNOWN country sentinel in its output schema")
	}
}

// t21 Task A: the description corrections of GG-02/06/08/09/10/11/12/13/18/19 must
// stay in place. Each phrase is the actionable half of one correction, so a
// revert to the old wording fails here.
func TestGuidanceCorrectionsArePresent(t *testing.T) {
	tools := fullMagentoTools(t)
	cases := map[string][]string{
		"magento.order.get":                      {"No tool can change a placed order's address"},
		"magento.product.get":                    {"configurable.children", "inventory.status"},
		"magento.order.search":                   {"multiple of limit", "entity_id"},
		"magento.sales.summary":                  {"canceled orders included"},
		"magento.sales.analysis":                 {"not a line subtotal"},
		"magento.products.bulk_update":           {"silently ignored", "magento.product.update"},
		"magento.attributes.options.bulk_create": {"already exists", "does not appear in rows"},
		"magento.order.create":                   {"replayed:true", "UUID"},
		"magento.import.validate":                {"update_existing"},
		"magento.product.tier_prices.set":        {"magento.customer.groups"},
		"magento.product.options.create":         {"unsupported_capability"},
	}
	for tool, phrases := range cases {
		description := tools[tool].Description
		for _, phrase := range phrases {
			if !strings.Contains(description, phrase) {
				t.Fatalf("%s description lost %q:\n%s", tool, phrase, description)
			}
		}
	}
}

// guidanceExemptions names tools whose description constants live in a file this
// task does not own. Each is a one-line follow-up, not a waiver of the rule.
var guidanceExemptions = map[string]string{
	"magento.import.start":  "description constants live in internal/magento/import_jobs.go, outside this task's file scope",
	"magento.import.cancel": "description constants live in internal/magento/import_jobs.go, outside this task's file scope",
	"magento.import.resume": "description constants live in internal/magento/import_jobs.go, outside this task's file scope",
	"magento.operation.list": "instance here is an optional filter, not a routing id; the constant lives in " +
		"internal/magento/operations.go, outside this task's file scope",
}

// t21 GG-15: one instance wording inside the Magento module.
func TestInstanceWordingIsUniform(t *testing.T) {
	tools := fullMagentoTools(t)
	const want = "Configured Magento instance id; required, and must name a configured instance."
	checked := 0
	for name, tool := range tools {
		if _, exempt := guidanceExemptions[name]; exempt {
			continue
		}
		properties := tool.Properties
		instance, ok := properties["instance"].(map[string]any)
		if !ok {
			continue
		}
		checked++
		if description, _ := instance["description"].(string); description != want {
			t.Fatalf("%s instance description = %q, want the module-standard sentence", name, description)
		}
	}
	if checked < 90 {
		t.Fatalf("only %d tools expose instance; expected the full Magento catalogue", checked)
	}
}

// t21 GG-12: every tool that accepts idempotency_key must state the derived-key
// consequence, not just "optional replay key".
func TestIdempotencyKeyDescriptionsStateTheDerivedKey(t *testing.T) {
	tools := fullMagentoTools(t)
	seen := 0
	for name, tool := range tools {
		if _, exempt := guidanceExemptions[name]; exempt {
			continue
		}
		properties := tool.Properties
		prop, ok := properties["idempotency_key"].(map[string]any)
		if !ok {
			continue
		}
		seen++
		if description, _ := prop["description"].(string); !strings.Contains(description, "derives a key from the arguments") {
			t.Fatalf("%s idempotency_key description does not state the derived key: %q", name, description)
		}
	}
	if seen == 0 {
		t.Fatal("no tool exposes idempotency_key")
	}
}
