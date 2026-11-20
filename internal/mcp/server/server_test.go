// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
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
	OK bool `json:"ok"`
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
	OK bool `json:"ok"`
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
