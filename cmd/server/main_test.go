// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shuaiZend/mage2mcp/internal/core/config"
	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/magento"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	"github.com/shuaiZend/mage2mcp/internal/mcp/server"
	"github.com/shuaiZend/mage2mcp/internal/mcp/toolerr"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
	"github.com/shuaiZend/mage2mcp/internal/security/rbac"
)

func TestIsLoopbackListen(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8080": true,
		"localhost:8080": true,
		"[::1]:8080":     true,
		"0.0.0.0:8080":   false,
		":8080":          false,
		"10.0.0.5:8080":  false,
		"garbage":        false,
	}
	for listen, want := range cases {
		if got := isLoopbackListen(listen); got != want {
			t.Errorf("isLoopbackListen(%q) = %v, want %v", listen, got, want)
		}
	}
}

func TestRegisterModulesBuildsTools(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Modules.Magento = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "store-us", Endpoint: "https://shop.example.com", StoreCode: "default",
		Credential: "bearer:test-token",
	}}}
	cfg.Modules.Cloudflare = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "zone-us", ZoneID: "zone-id", AccountID: "account-id", Credential: "cf-token",
	}}}
	cfg.Modules.GA4 = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "giztop", PropertyID: "123456", Credential: "ga-token",
	}}}
	reg := registry.New()
	if err := registerModules(cfg, reg); err != nil {
		t.Fatalf("registerModules: %v", err)
	}
	want := map[string]bool{
		"magento.product.get":          false,
		"magento.product.search":       false,
		"cloudflare.analytics.traffic": false,
		"cloudflare.security.summary":  false,
		"ga4.analytics.traffic":        false,
		"ga4.property.metadata":        false,
	}
	for _, tool := range reg.List() {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("tool %s was not registered", name)
		}
	}
}

func TestMagentoSignerForms(t *testing.T) {
	if _, err := magento.ParseCredential("bearer:abc"); err != nil {
		t.Fatalf("bearer: %v", err)
	}
	if _, err := magento.ParseCredential("oauth1:k:s:t:ts"); err != nil {
		t.Fatalf("oauth1: %v", err)
	}
	if _, err := magento.ParseCredential("oauth1:bad"); err == nil {
		t.Fatal("malformed oauth1 credential accepted")
	}
	if _, err := magento.ParseCredential("plain-token"); err == nil {
		t.Fatal("unsupported credential form accepted")
	}
}

func TestRegisterModulesBuildsUnifiedTool(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Modules.Magento = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "store-us", Endpoint: "https://shop.example.com", StoreCode: "default", Credential: "bearer:x",
	}}}
	cfg.Modules.Cloudflare = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "zone-us", ZoneID: "z", AccountID: "a", Credential: "t",
	}}}
	cfg.Modules.GA4 = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "giztop", PropertyID: "123456", Credential: "ga-token",
	}}}
	cfg.Bindings = []config.Binding{{ID: "b1", Magento: "store-us", Cloudflare: "zone-us", GA4: "giztop"}}
	reg := registry.New()
	if err := registerModules(cfg, reg); err != nil {
		t.Fatalf("registerModules: %v", err)
	}
	if _, ok := reg.Lookup("unified.traffic.compare"); !ok {
		t.Fatal("unified.traffic.compare was not registered")
	}
}

func TestRegisterModulesRequiresSourcesForBindings(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Modules.Magento = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "store-us", Endpoint: "https://shop.example.com", StoreCode: "default", Credential: "bearer:x",
	}}}
	cfg.Bindings = []config.Binding{{ID: "b1", Magento: "store-us", Cloudflare: "zone-us", GA4: "giztop"}}
	if err := registerModules(cfg, registry.New()); err == nil {
		t.Fatal("expected missing cloudflare/ga4 to be rejected")
	}
}

func TestRegisterModulesSupportsMultipleCloudflareZones(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Modules.Cloudflare = config.Module{Enabled: true, Instances: []config.Instance{
		{ID: "zone-a", ZoneID: "za", Credential: "t"},
		{ID: "zone-b", ZoneID: "zb", Credential: "t"},
	}}
	reg := registry.New()
	if err := registerModules(cfg, reg); err != nil {
		t.Fatalf("registerModules: %v", err)
	}
	if _, ok := reg.Lookup("cloudflare.analytics.traffic"); !ok {
		t.Fatal("traffic tool not registered")
	}
}

func TestConfiguredInstances(t *testing.T) {
	cfg := config.Default()
	cfg.Modules.Cloudflare = config.Module{Enabled: true, Instances: []config.Instance{{ID: "zone-a"}, {ID: "zone-b"}}}
	cfg.Modules.GA4 = config.Module{Enabled: true, Instances: []config.Instance{{ID: "giztop"}}}
	got := configuredInstances(cfg)
	if len(got["cloudflare"]) != 2 || len(got["ga4"]) != 1 {
		t.Fatalf("instances = %+v", got)
	}
	if _, ok := got["magento"]; ok {
		t.Fatal("disabled module must not be reported")
	}
}

func TestRegisterModulesSupportsMultipleGA4Properties(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Modules.GA4 = config.Module{Enabled: true, Instances: []config.Instance{
		{ID: "prop-a", PropertyID: "111", Credential: "t"},
		{ID: "prop-b", PropertyID: "222", Credential: "t"},
	}}
	reg := registry.New()
	if err := registerModules(cfg, reg); err != nil {
		t.Fatalf("registerModules: %v", err)
	}
	for _, name := range []string{"ga4.analytics.traffic", "ga4.property.metadata", "ga4.property.capabilities", "ga4.analytics.realtime", "ga4.ecommerce.overview"} {
		if _, ok := reg.Lookup(name); !ok {
			t.Fatalf("%s not registered", name)
		}
	}
}

func TestRegisterModulesSupportsMultipleMagentoStoresForReads(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Modules.Magento = config.Module{Enabled: true, Instances: []config.Instance{
		{ID: "store-us", Endpoint: "https://us.example.com", StoreCode: "default", Credential: "bearer:t"},
		{ID: "store-eu", Endpoint: "https://eu.example.com", StoreCode: "default", Credential: "bearer:t"},
	}}
	reg := registry.New()
	if err := registerModules(cfg, reg); err != nil {
		t.Fatalf("registerModules: %v", err)
	}
	for _, name := range []string{"magento.product.get", "magento.product.search", "magento.import.validate"} {
		if _, ok := reg.Lookup(name); !ok {
			t.Fatalf("%s not registered", name)
		}
	}
}

func TestRegisterModulesSupportsMultiStoreWrites(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Tools.Writes = true
	cfg.Modules.Magento = config.Module{Enabled: true, Instances: []config.Instance{
		{ID: "store-us", Endpoint: "https://us.example.com", StoreCode: "default", Credential: "bearer:t"},
		{ID: "store-eu", Endpoint: "https://eu.example.com", StoreCode: "default", Credential: "bearer:t"},
	}}
	reg := registry.New()
	if err := registerModules(cfg, reg); err != nil {
		t.Fatalf("registerModules: %v", err)
	}
	if _, ok := reg.Lookup("magento.product.create"); !ok {
		t.Fatal("write tool not registered for multi-store")
	}
}

func TestBuildAuthAllowsDevInsecureOnNonLoopback(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Server.Listen = "0.0.0.0:8080"
	if _, err := buildAuth(cfg, slog.Default(), false); err == nil {
		t.Fatal("non-loopback without auth must be rejected by default")
	}
	if _, err := buildAuth(cfg, slog.Default(), true); err != nil {
		t.Fatalf("dev-insecure should allow non-loopback: %v", err)
	}
}

// policyFixtureInput is the argument shape for the authorization fixtures.
type policyFixtureInput struct {
	Instance string `json:"instance,omitempty"`
}

type policyFixtureOutput struct {
	OK bool `json:"ok" jsonschema:"True when the fixture call succeeded."`
}

// policyFixtureTool builds a stub with the same name and declared permission as
// a real catalog tool, so the shipped policy can be exercised end to end
// without reaching an upstream. TestRealToolPermissionsMatchFixture keeps the
// permissions here honest.
func policyFixtureTool(t *testing.T, name, permission string) registry.Tool {
	t.Helper()
	annotations := mcp.ToolAnnotations{ReadOnlyHint: permission == registry.PermissionRead}
	tool := registry.Tool{
		Name: name, Title: name, Description: "policy fixture tool",
		Capability: "test." + strings.ReplaceAll(name, ".", "_"),
		Permission: permission, Timeout: 5 * time.Second, Annotations: annotations,
	}
	if permission == registry.PermissionDestructive {
		destructive := true
		tool.RequiresConfirmation = true
		tool.Annotations.DestructiveHint = &destructive
	}
	tool.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, &mcp.Tool{Name: name, Title: name, Description: "policy fixture tool",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: annotations.ReadOnlyHint, DestructiveHint: annotations.DestructiveHint}},
			func(_ context.Context, _ *mcp.CallToolRequest, _ policyFixtureInput) (*mcp.CallToolResult, policyFixtureOutput, error) {
				return nil, policyFixtureOutput{OK: true}, nil
			})
		return nil
	}
	return tool
}

// policyFixtureRegistry holds the three permission classes the shipped policy
// must distinguish: read, write and destructive.
func policyFixtureRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New()
	for _, spec := range []struct{ name, permission string }{
		{"magento.product.get", registry.PermissionRead},
		{"magento.invoice.capture", registry.PermissionWrite},
		{"magento.product.delete", registry.PermissionDestructive},
	} {
		if err := reg.Register(policyFixtureTool(t, spec.name, spec.permission)); err != nil {
			t.Fatalf("register %s: %v", spec.name, err)
		}
	}
	return reg
}

// callToolAsRole drives one tools/call through the production authorization
// wiring (server.NewMCP + authorizeFunc + the shipped policy) with the given
// role on the verified identity.
func callToolAsRole(t *testing.T, reg *registry.Registry, policy *rbac.Policy, role, tool string) *mcp.CallToolResult {
	t.Helper()
	cfg := config.Default()
	cfg.Auth.Enabled = true
	srv, err := server.NewMCP(server.Options{
		Version: "test", Registry: reg,
		Authorize: authorizeFunc(cfg, policy, slog.New(slog.DiscardHandler)),
		Confirm:   testConfirmService(t),
	})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	handler := server.Handler(srv, server.Options{
		Registry: reg,
		AuthMiddleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := &auth.Identity{Subject: "user-" + role, Class: auth.ClassHuman, Roles: []string{role}}
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
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool,
		Arguments: map[string]any{"instance": "store-us"}})
	if err != nil {
		t.Fatalf("call %s as %s: %v", tool, role, err)
	}
	return res
}

// TestShippedPolicyEnforcesScopeLadder is the end-to-end proof for ARCH-01 /
// SEC-01: the production wiring denies a viewer every write and destructive
// tool, allows it a read, and lets an operator write without letting it destroy.

// testConfirmService returns an in-memory confirmation service. NewMCP refuses
// to start when a registered tool requires approval and no service is supplied,
// so every test that builds a server needs one.
func testConfirmService(t *testing.T) *confirm.Service {
	t.Helper()
	svc, err := confirm.NewService(confirm.NewMemoryStore(), time.Minute)
	if err != nil {
		t.Fatalf("confirm.NewService: %v", err)
	}
	return svc
}

func TestShippedPolicyEnforcesScopeLadder(t *testing.T) {
	reg := policyFixtureRegistry(t)
	policy, err := rbac.DefaultPolicy()
	if err != nil {
		t.Fatalf("DefaultPolicy: %v", err)
	}
	cases := []struct {
		role string
		tool string
		want bool
	}{
		{"viewer", "magento.product.get", true},
		{"viewer", "magento.invoice.capture", false},
		{"viewer", "magento.product.delete", false},
		{"analyst", "magento.product.get", true},
		{"analyst", "magento.invoice.capture", false},
		{"operator", "magento.invoice.capture", true},
		{"operator", "magento.product.delete", false},
		{"admin", "magento.product.delete", true},
	}
	for _, tc := range cases {
		res := callToolAsRole(t, reg, policy, tc.role, tc.tool)
		code := toolErrorCode(t, res)
		// This test is about authorization, not confirmation. An authorized
		// write or destructive call now stops at the human-approval gate
		// (confirmation_required) rather than running, so "allowed" means "not
		// denied" and, when it errored, that the gate is what stopped it.
		if tc.want {
			if code == string(errs.CodePermissionDenied) {
				t.Errorf("role=%s tool=%s: denied, want authorized", tc.role, tc.tool)
				continue
			}
			if res.IsError && code != string(errs.CodeConfirmationRequired) {
				t.Errorf("role=%s tool=%s: authorized but failed with %q, want success or confirmation_required",
					tc.role, tc.tool, code)
			}
			continue
		}
		if code != string(errs.CodePermissionDenied) {
			t.Errorf("role=%s tool=%s: error code = %q, want permission_denied", tc.role, tc.tool, code)
		}
	}
}

// toolErrorCode extracts the taxonomy code from a tool result's structured
// envelope, or "" when the call succeeded.
func toolErrorCode(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || !res.IsError {
		return ""
	}
	var envelope struct {
		Code string `json:"code"`
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("encode tool error envelope: %v", err)
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode tool error envelope: %v", err)
	}
	return envelope.Code
}

// TestRealToolPermissionsMatchFixture keeps the end-to-end fixture honest: the
// real catalog tools must still declare the permissions the fixture assumes.
func TestRealToolPermissionsMatchFixture(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Writes = true
	cfg.Modules.Magento.Enabled = true
	cfg.Modules.Magento.Instances = []config.Instance{{
		ID: "store-us", Endpoint: "https://shop.example.com", StoreCode: "default",
		Credential: "bearer:test-token",
	}}
	reg := registry.New()
	if err := registerModules(cfg, reg); err != nil {
		t.Fatalf("registerModules: %v", err)
	}
	want := map[string]string{
		"magento.product.get":     registry.PermissionRead,
		"magento.invoice.capture": registry.PermissionWrite,
		"magento.product.delete":  registry.PermissionDestructive,
	}
	for name, permission := range want {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered; the policy fixture no longer mirrors the catalog", name)
		}
		if tool.Permission != permission {
			t.Fatalf("%s permission = %q, want %q", name, tool.Permission, permission)
		}
	}
}

// TestAuthorizeFuncPassesRequiredScope proves the production callback forwards
// the server-supplied scope instead of substituting a literal read.
func TestAuthorizeFuncPassesRequiredScope(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = true
	policy, err := rbac.DefaultPolicy()
	if err != nil {
		t.Fatalf("DefaultPolicy: %v", err)
	}
	authorize := authorizeFunc(cfg, policy, slog.New(slog.DiscardHandler))
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject: "viewer-1", Class: auth.ClassHuman, Roles: []string{"viewer"},
	})
	if err := authorize(ctx, "magento.product.get", "store-us", rbac.ScopeRead); err != nil {
		t.Fatalf("viewer denied a read: %v", err)
	}
	if err := authorize(ctx, "magento.invoice.capture", "store-us", rbac.ScopeWrite); err == nil {
		t.Fatal("viewer authorized for a write scope")
	} else if errs.CodeOf(err) != errs.CodePermissionDenied {
		t.Fatalf("write denial code = %v, want permission_denied", errs.CodeOf(err))
	}
	// An identity that never passed authentication is rejected, not defaulted.
	if err := authorize(context.Background(), "magento.product.get", "store-us", rbac.ScopeRead); errs.CodeOf(err) != errs.CodeUnauthenticated {
		t.Fatalf("missing identity code = %v, want unauthenticated", errs.CodeOf(err))
	}
}

// TestAuthorizeFuncWarnsWhenAuthDisabled pins requirement 5: with authentication
// off there is no authorizer, but the process must say so loudly at startup.
func TestAuthorizeFuncWarnsWhenAuthDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	policy, err := rbac.DefaultPolicy()
	if err != nil {
		t.Fatalf("DefaultPolicy: %v", err)
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	if authorize := authorizeFunc(cfg, policy, logger); authorize != nil {
		t.Fatal("authorization must be disabled when authentication is disabled")
	}
	if !strings.Contains(buf.String(), "AUTHORIZATION DISABLED") {
		t.Fatalf("no prominent warning logged; got %q", buf.String())
	}
}

// readOnlyImportQueue is enough to register the import tools in a test; no
// handler is called.
type readOnlyImportQueue struct{}

func (readOnlyImportQueue) Enqueue(context.Context, jobs.Task) (string, error)   { return "job-1", nil }
func (readOnlyImportQueue) Cancel(context.Context, string, string, string) error { return nil }
func (readOnlyImportQueue) GetJob(context.Context, string, string, string) (*jobs.JobRecord, error) {
	return nil, nil
}

// MCP-03: a database-enabled read-only deployment registers exactly the read
// import tools plus the always-present platform.info; zero non-read tools.
func TestReadOnlyDatabaseRegistrationPinsInventory(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Tools.Writes = false
	cfg.Modules.Magento = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "store-us", Endpoint: "https://shop.example.com", StoreCode: "default", Credential: "bearer:t",
	}}}
	cfg.Modules.Cloudflare = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "zone", ZoneID: "z", AccountID: "a", Credential: "t",
	}}}
	cfg.Modules.GA4 = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "ga", PropertyID: "123456", Credential: "t",
	}}}
	// No bindings: this is the "read-only, database, no bindings" row.
	reg := registry.New()
	if err := registerModules(cfg, reg, importWiring{Queue: readOnlyImportQueue{}}); err != nil {
		t.Fatalf("registerModules: %v", err)
	}
	if _, err := server.NewMCP(server.Options{Version: "test", Registry: reg, Confirm: testConfirmService(t)}); err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	if got := reg.Len(); got != 56 {
		t.Fatalf("read-only+database registry = %d tools, want 56", got)
	}
	magento, nonRead := 0, 0
	for _, tool := range reg.List() {
		if strings.HasPrefix(tool.Name, "magento.") {
			magento++
		}
		if tool.Permission != registry.PermissionRead {
			nonRead++
		}
	}
	if magento != 46 || nonRead != 0 {
		t.Fatalf("read-only+database: magento=%d non-read=%d, want 46/0", magento, nonRead)
	}
	for _, name := range []string{"magento.import.status", "magento.import.batches", "magento.import.batch"} {
		if _, ok := reg.Lookup(name); !ok {
			t.Errorf("read import tool %s is not registered", name)
		}
	}
	for _, name := range []string{"magento.import.start", "magento.import.cancel", "magento.import.resume"} {
		if _, ok := reg.Lookup(name); ok {
			t.Errorf("write import tool %s leaked into a read-only registry", name)
		}
	}
}

// F10: instances for a disabled module are not reported by platform.info.
func TestConfiguredInstancesOmitsDisabledModules(t *testing.T) {
	cfg := config.Default()
	cfg.Modules.Magento = config.Module{Enabled: false, Instances: []config.Instance{{ID: "disabled-magento"}}}
	cfg.Modules.Cloudflare = config.Module{Enabled: false, Instances: []config.Instance{{ID: "disabled-cf"}}}
	cfg.Modules.GA4 = config.Module{Enabled: false, Instances: []config.Instance{{ID: "disabled-ga"}}}
	if got := configuredInstances(cfg); len(got) != 0 {
		t.Fatalf("configuredInstances reported disabled modules: %+v", got)
	}
}

// F6: enumerate the real maximal server and validate every wire schema, so a
// direct mcp.AddTool that bypasses toolerr.Add cannot ship an open schema.
func TestEveryRegisteredToolSchemaIsClosed(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Tools.Writes = true
	cfg.Modules.Magento = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "store-us", Endpoint: "https://shop.example.com", StoreCode: "default", Credential: "bearer:t",
	}}}
	cfg.Modules.Cloudflare = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "zone", ZoneID: "z", AccountID: "a", Credential: "t",
	}}}
	cfg.Modules.GA4 = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "ga", PropertyID: "123456", Credential: "t",
	}}}
	cfg.Bindings = []config.Binding{{ID: "b1", Magento: "store-us", Cloudflare: "zone", GA4: "ga"}}
	reg := registry.New()
	if err := registerModules(cfg, reg, importWiring{Queue: readOnlyImportQueue{}}); err != nil {
		t.Fatalf("registerModules: %v", err)
	}
	srv, err := server.NewMCP(server.Options{Version: "test", Registry: reg, Confirm: testConfirmService(t)})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ct, st := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "schema-check", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = clientSession.Close() }()
	list, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(list.Tools) == 0 {
		t.Fatal("maximal server listed no tools")
	}
	for _, tool := range list.Tools {
		if err := toolerr.ValidateSchema(tool); err != nil {
			t.Errorf("tool %s has an invalid input schema: %v", tool.Name, err)
		}
	}
}
