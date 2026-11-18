// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
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
		Instances: map[string][]string{"cloudflare": {"zone-a", "zone-b"}}, WriteTools: true})
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
	if len(info.Instances["cloudflare"]) != 2 || !info.WritesEnabled {
		t.Fatalf("info does not report configuration: %+v", info)
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

func TestReadyzFailsWithoutTools(t *testing.T) {
	srv, err := NewMCP(Options{Version: "test", Registry: registry.New()})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(Handler(srv, Options{Registry: registry.New()}))
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz = %d, want 503", resp.StatusCode)
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
		Authorize: func(context.Context, string) error {
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
