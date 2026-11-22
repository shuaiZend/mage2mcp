// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	mcpserver "github.com/shuaiZend/mage2mcp/internal/mcp/server"
)

// fakeMagento records the requests a tool makes against a fake Magento REST API.
type fakeMagento struct {
	calls   atomic.Int32
	mu      sync.Mutex
	lastURL *url.URL
}

func (f *fakeMagento) record(r *http.Request) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastURL = r.URL
}

func (f *fakeMagento) lastQuery() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastURL == nil {
		return nil
	}
	return f.lastURL.Query()
}

// newRegisteredTools starts a fake Magento REST server and registers both tools
// in a fresh registry.
func newRegisteredTools(t *testing.T) (*registry.Registry, *fakeMagento) {
	t.Helper()
	fake := &fakeMagento{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.record(r)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/products/SKU-1":
			_, _ = io.WriteString(w, `{"id":1,"sku":"SKU-1","name":"Test Shirt","type_id":"simple","status":1}`)
		case "/rest/default/V1/products":
			_, _ = io.WriteString(w, `{"items":[{"id":1,"sku":"SKU-1","name":"Test Shirt"}],"total_count":7}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	reg := registry.New()
	if err := Register(reg, testService(t, ts.URL)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return reg, fake
}

func TestRegisterMetadata(t *testing.T) {
	reg, _ := newRegisteredTools(t)
	if reg.Len() != 45 {
		t.Fatalf("registry has %d tools, want 45", reg.Len())
	}
	for _, name := range []string{getToolName, searchToolName} {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("tool %q is not registered", name)
		}
		if tool.Permission != registry.PermissionRead || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("tool %q is not read-only: %+v", name, tool)
		}
		if tool.Capability != capabilityProductRead {
			t.Fatalf("tool %q capability = %q", name, tool.Capability)
		}
		if tool.Timeout != 30*time.Second {
			t.Fatalf("tool %q timeout = %s", name, tool.Timeout)
		}
		if tool.RequiresConfirmation {
			t.Fatalf("read tool %q must not require confirmation", name)
		}
		if tool.Title == "" || len(tool.Title) > 60 {
			t.Fatalf("tool %q title is invalid: %q", name, tool.Title)
		}
		if tool.Description == "" || len(tool.Description) > 1024 {
			t.Fatalf("tool %q description is invalid", name)
		}
		if !strings.Contains(tool.Description, "Read-only") || !strings.Contains(tool.Description, "instance") {
			t.Fatalf("tool %q description must state read-only and instance resolution: %q", name, tool.Description)
		}
	}
	client, err := NewClient(ClientConfig{BaseURL: "https://shop.example.com", StoreCode: "default", Signer: testSigner(t)})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := Register(nil, service); err == nil {
		t.Fatal("Register(nil, service) must fail")
	}
	if err := Register(registry.New(), nil); err == nil {
		t.Fatal("Register(registry, nil) must fail")
	}
}

// toolResultText concatenates the text content of a tool result.
func toolResultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, content := range res.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

func TestToolsThroughInMemoryTransport(t *testing.T) {
	reg, fake := newRegisteredTools(t)
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: magentoConfirmService(t)})
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

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	found := map[string]bool{}
	for _, tool := range listed.Tools {
		found[tool.Name] = true
		if tool.Name == getToolName || tool.Name == searchToolName {
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Fatalf("tool %q must be read-only", tool.Name)
			}
			if tool.InputSchema == nil {
				t.Fatalf("tool %q has no input schema", tool.Name)
			}
		}
	}
	if !found[getToolName] || !found[searchToolName] {
		t.Fatalf("registered tools are missing from tools/list: %v", found)
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      getToolName,
		Arguments: map[string]any{"instance": "default", "sku": "SKU-1"},
	})
	if err != nil {
		t.Fatalf("call %s: %v", getToolName, err)
	}
	if res.IsError {
		t.Fatalf("%s returned a tool error: %s", getToolName, toolResultText(t, res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var got productGetOutput
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("structured content is not productGetOutput: %v", err)
	}
	if got.Product.SKU != "SKU-1" || got.Product.Name != "Test Shirt" {
		t.Fatalf("unexpected product output: %+v", got.Product)
	}

	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      searchToolName,
		Arguments: map[string]any{"instance": "default", "query": "shirt", "limit": 10},
	})
	if err != nil {
		t.Fatalf("call %s: %v", searchToolName, err)
	}
	if res.IsError {
		t.Fatalf("%s returned a tool error: %s", searchToolName, toolResultText(t, res))
	}
	raw, err = json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var search productSearchOutput
	if err := json.Unmarshal(raw, &search); err != nil {
		t.Fatalf("structured content is not productSearchOutput: %v", err)
	}
	if search.Total != 7 || len(search.Products) != 1 || search.Products[0].SKU != "SKU-1" {
		t.Fatalf("unexpected search output: %+v", search)
	}
	if got := fake.lastQuery().Get("searchCriteria[pageSize]"); got != "10" {
		t.Fatalf("explicit pageSize = %q, want 10", got)
	}

	// Omitting limit must apply the schema default of 50.
	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      searchToolName,
		Arguments: map[string]any{"instance": "default", "query": "shirt"},
	})
	if err != nil || res.IsError {
		t.Fatalf("default-limit search failed: err=%v result=%+v", err, res)
	}
	if got := fake.lastQuery().Get("searchCriteria[pageSize]"); got != "50" {
		t.Fatalf("default pageSize = %q, want 50", got)
	}

	// A schema-valid but service-invalid SKU must be a tool error, and must not
	// reach the upstream.
	before := fake.calls.Load()
	badSKU := strings.Repeat("A", 65)
	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      getToolName,
		Arguments: map[string]any{"instance": "default", "sku": badSKU},
	})
	if err != nil {
		t.Fatalf("call %s with a bad SKU returned a protocol error: %v", getToolName, err)
	}
	if !res.IsError {
		t.Fatal("bad SKU must be rejected as a tool error")
	}
	if text := toolResultText(t, res); !strings.Contains(text, "invalid_argument") {
		t.Fatalf("bad SKU error does not carry invalid_argument: %q", text)
	}
	if fake.calls.Load() != before {
		t.Fatal("bad SKU must not reach the upstream")
	}

	// A cursor that is not page-aligned is rejected by the service.
	before = fake.calls.Load()
	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      searchToolName,
		Arguments: map[string]any{"instance": "default", "cursor": "5"},
	})
	if err != nil {
		t.Fatalf("call %s with a bad cursor: %v", searchToolName, err)
	}
	if !res.IsError || !strings.Contains(toolResultText(t, res), "invalid_argument") {
		t.Fatalf("bad cursor must be an invalid_argument tool error: %v", res.Content)
	}
	if fake.calls.Load() != before {
		t.Fatal("bad cursor must not reach the upstream")
	}

	// The SDK schema validation rejects a missing required argument.
	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      getToolName,
		Arguments: map[string]any{"instance": "default"},
	})
	if err != nil {
		t.Fatalf("call %s without sku: %v", getToolName, err)
	}
	if !res.IsError {
		t.Fatal("missing required sku must be a tool error")
	}
}

// --- operation.* registration and summary description (t16) ---------------

// TestToolListCarriesTheOperationTools is the registration contract: the three
// ledger tools appear in tools/list, the two read tools are read-only, and the
// disposition tool is a write that requires confirmation. The count is pinned
// because the tools/list surface is a public contract (102 -> 105 with
// platform.info).
func TestToolListCarriesTheOperationTools(t *testing.T) {
	reg := registry.New()
	svc := testService(t, "https://shop.example.com")
	if err := Register(reg, svc); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := RegisterWrites(reg, svc); err != nil {
		t.Fatalf("RegisterWrites: %v", err)
	}
	resolver, err := NewResolver(map[string]*Service{"store-us": svc})
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	if err := RegisterImportJobs(reg, resolver, &fakeImportQueue{}); err != nil {
		t.Fatalf("RegisterImportJobs: %v", err)
	}
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: magentoConfirmService(t)})
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
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(listed.Tools) != 105 {
		t.Fatalf("tools/list has %d tools, want 105 (104 Magento + platform.info)", len(listed.Tools))
	}
	readOnly := map[string]bool{}
	for _, tool := range listed.Tools {
		if tool.Annotations != nil {
			readOnly[tool.Name] = tool.Annotations.ReadOnlyHint
		}
	}
	for _, name := range []string{operationGetToolName, operationListToolName} {
		if !readOnly[name] {
			t.Fatalf("tool %q must be read-only", name)
		}
	}
	if readOnly[operationDisposeToolName] {
		t.Fatalf("tool %q must not be read-only", operationDisposeToolName)
	}
	dispose, ok := reg.Lookup(operationDisposeToolName)
	if !ok {
		t.Fatalf("%s is not registered", operationDisposeToolName)
	}
	if !dispose.RequiresConfirmation || dispose.Permission != registry.PermissionWrite {
		t.Fatalf("dispose metadata = permission %q confirmation %v, want a confirmed write",
			dispose.Permission, dispose.RequiresConfirmation)
	}
}

// TestOperationReadToolsStayOutOfTheReadPath pins the deployment rule behind the
// split registration: a read-only deployment (tools.writes=false) must not expose
// the disposition write tool, so only the two read tools may be registered by
// Register.
func TestOperationReadToolsStayOutOfTheReadPath(t *testing.T) {
	reg, _ := newRegisteredTools(t)
	for _, name := range []string{operationGetToolName, operationListToolName} {
		if _, ok := reg.Lookup(name); !ok {
			t.Fatalf("%s is missing from the read registry", name)
		}
	}
	if _, ok := reg.Lookup(operationDisposeToolName); ok {
		t.Fatalf("%s must not be registered by Register: a read-only deployment would expose a write tool",
			operationDisposeToolName)
	}
}

// TestSalesSummaryDescriptionStatesTheWindowCap covers U-56 and the timezone
// documentation: a caller must learn the 366-day cap and the day-bucket timezone
// from the tool description, not from an invalid_argument after the fact.
func TestSalesSummaryDescriptionStatesTheWindowCap(t *testing.T) {
	reg, _ := newRegisteredTools(t)
	for _, name := range []string{salesSummaryToolName, orderSummaryToolName} {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		for _, want := range []string{"366 days", "timezone"} {
			if !strings.Contains(tool.Description, want) {
				t.Fatalf("%s description must mention %q: %q", name, want, tool.Description)
			}
		}
	}
}

// GG-02: a single-order read returns the shipping and billing addresses, including
// the email and phone carried by the address. Three places used to promise the
// opposite — the tool description, the Order struct doc, and the README — so a
// support agent was told the one field it needs does not exist. They must describe
// the same behaviour; this test fails if any of them drifts back to the old
// promise or stops naming what is returned.
func TestOrderGetAddressDisclosureIsConsistent(t *testing.T) {
	sources := map[string]string{"tools.go order.get description": orderGetToolDescription}
	orderSource, err := os.ReadFile("order.go")
	if err != nil {
		t.Fatalf("read order.go: %v", err)
	}
	sources["order.go Order doc comment"] = string(orderSource)
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	sources["README.md"] = string(readme)

	denials := []string{
		"addresses are not returned",
		"addresses, and payment data are not returned",
		"addresses are intentionally absent",
		"order reads omit email, addresses",
	}
	for where, text := range sources {
		lower := strings.ToLower(text)
		if !strings.Contains(lower, "shipping and billing addresses") {
			t.Fatalf("%s does not say the read returns the shipping and billing addresses", where)
		}
		if !strings.Contains(lower, "address email") {
			t.Fatalf("%s does not disclose that the address carries an email", where)
		}
		for _, denial := range denials {
			if strings.Contains(lower, denial) {
				t.Fatalf("%s still promises %q", where, denial)
			}
		}
	}
}
