// SPDX-License-Identifier: Apache-2.0

package ga4

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
)

// extraAPIHandler serves the capability probes and a canned realtime report.
func extraAPIHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, ":runRealtimeReport"):
			_, _ = io.WriteString(w, cannedRealtimeReport)
		case strings.HasSuffix(r.URL.Path, ":runReport"):
			var request capabilityProbeRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode probe body: %v", err)
				return
			}
			if len(request.Metrics) == 1 && request.Metrics[0].Name == "ecommercePurchases" {
				_, _ = io.WriteString(w, capabilityPurchasesPresent)
				return
			}
			_, _ = io.WriteString(w, capabilityEventsPresent)
		default:
			http.NotFound(w, r)
		}
	})
}

// newExtraToolSession registers the extra GA4 tools on an in-memory MCP server
// and returns a connected client session.
func newExtraToolSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	reg := registry.New()
	if err := RegisterExtra(reg, newTestService(t, extraAPIHandler(t))); err != nil {
		t.Fatalf("RegisterExtra: %v", err)
	}
	if reg.Len() != 4 {
		t.Fatalf("registered tools = %d, want 4", reg.Len())
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "ga4-extra-test", Version: "0"}, nil)
	for _, tool := range reg.List() {
		if err := tool.Register(server); err != nil {
			t.Fatalf("register %s: %v", tool.Name, err)
		}
	}
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestRegisterExtraMetadata(t *testing.T) {
	reg := registry.New()
	if err := RegisterExtra(reg, newTestService(t, extraAPIHandler(t))); err != nil {
		t.Fatalf("RegisterExtra: %v", err)
	}
	if reg.Len() != 4 {
		t.Fatalf("registered tools = %d, want 4", reg.Len())
	}
	cases := []struct {
		name       string
		capability string
		timeout    string
	}{
		{capabilitiesToolName, "ga4.property.read", "1m0s"},
		{realtimeToolName, "ga4.analytics.read", "1m0s"},
	}
	for _, tc := range cases {
		tool, ok := reg.Lookup(tc.name)
		if !ok {
			t.Fatalf("%s is not registered", tc.name)
		}
		if err := registry.Validate(tool); err != nil {
			t.Fatalf("%s is invalid: %v", tc.name, err)
		}
		if tool.Capability != tc.capability {
			t.Fatalf("%s capability = %q, want %q", tc.name, tool.Capability, tc.capability)
		}
		if tool.Timeout.String() != tc.timeout {
			t.Fatalf("%s timeout = %v, want %s", tc.name, tool.Timeout, tc.timeout)
		}
		if !tool.Annotations.ReadOnlyHint {
			t.Fatalf("%s must be read-only", tc.name)
		}
		if len(tool.Title) == 0 || len(tool.Title) > 60 {
			t.Fatalf("%s title length = %d", tc.name, len(tool.Title))
		}
		if !strings.Contains(tool.Description, "Google Analytics 4") || !strings.Contains(tool.Description, "aggregate-only") {
			t.Fatalf("%s description must state the source and aggregate-only behaviour: %q", tc.name, tool.Description)
		}
	}
	realtime, _ := reg.Lookup(realtimeToolName)
	if !strings.Contains(realtime.Description, "30") {
		t.Fatalf("realtime description must state the 30-minute window: %q", realtime.Description)
	}
	capabilities, _ := reg.Lookup(capabilitiesToolName)
	if !strings.Contains(capabilities.Description, "ecommerce") {
		t.Fatalf("capabilities description must state the ecommerce probe: %q", capabilities.Description)
	}
}

func TestRegisterExtraRejectsNilDependencies(t *testing.T) {
	if err := RegisterExtra(nil, &Service{}); err == nil {
		t.Fatal("expected an error for a nil registry")
	}
	if err := RegisterExtra(registry.New(), nil); err == nil {
		t.Fatal("expected an error for a nil service")
	}
}

func TestExtraToolSchemas(t *testing.T) {
	session := newExtraToolSession(t)
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	found := make(map[string]*mcp.Tool, len(tools.Tools))
	for _, tool := range tools.Tools {
		found[tool.Name] = tool
	}
	realtime, ok := found[realtimeToolName]
	if !ok {
		t.Fatalf("%s not listed", realtimeToolName)
	}
	if realtime.InputSchema == nil || realtime.OutputSchema == nil {
		t.Fatal("realtime tool must declare input and output schemas")
	}
	schema, err := json.Marshal(realtime.InputSchema)
	if err != nil {
		t.Fatalf("marshal realtime schema: %v", err)
	}
	for _, want := range []string{`"enum"`, `"unifiedScreenName"`, `"activeUsers"`, `"maximum":100`} {
		if !strings.Contains(string(schema), want) {
			t.Fatalf("realtime schema is missing %q: %s", want, schema)
		}
	}
	capabilities, ok := found[capabilitiesToolName]
	if !ok {
		t.Fatalf("%s not listed", capabilitiesToolName)
	}
	if capabilities.InputSchema == nil || capabilities.OutputSchema == nil {
		t.Fatal("capabilities tool must declare input and output schemas")
	}
	output, err := json.Marshal(capabilities.OutputSchema)
	if err != nil {
		t.Fatalf("marshal capabilities output schema: %v", err)
	}
	for _, want := range []string{"ecommerce_tracked", "currency", "time_zone", "checked_at", "evidence"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("capabilities output schema is missing %q: %s", want, output)
		}
	}
}

func TestCapabilitiesToolCall(t *testing.T) {
	session := newExtraToolSession(t)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      capabilitiesToolName,
		Arguments: map[string]any{"instance": "giztop"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned an error: %s", contentText(t, result))
	}
	if result.StructuredContent == nil {
		t.Fatal("expected structured content")
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var capabilities PropertyCapabilities
	if err := json.Unmarshal(raw, &capabilities); err != nil {
		t.Fatalf("structured content is not PropertyCapabilities: %v", err)
	}
	if !capabilities.EcommerceTracked {
		t.Fatalf("ecommerce_tracked = false, evidence: %v", capabilities.Evidence)
	}
	if capabilities.Currency != "USD" || capabilities.TimeZone != "America/Los_Angeles" {
		t.Fatalf("envelope = %+v", capabilities)
	}
	if capabilities.CheckedAt.IsZero() {
		t.Fatal("checked_at must be set")
	}
	if len(capabilities.Evidence) == 0 {
		t.Fatal("evidence must not be empty")
	}
}

func TestRealtimeToolCall(t *testing.T) {
	session := newExtraToolSession(t)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: realtimeToolName,
		Arguments: map[string]any{
			"instance":   "giztop",
			"dimensions": []any{"eventName"},
			"metrics":    []any{"eventCount"},
			"limit":      10,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned an error: %s", contentText(t, result))
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var output RealtimeOutput
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatalf("structured content is not RealtimeOutput: %v", err)
	}
	if len(output.Rows) != 2 {
		t.Fatalf("rows = %+v", output.Rows)
	}
	if output.Rows[0].Date != "page_view" || output.Rows[0].Metrics["eventCount"] != 12 {
		t.Fatalf("first row = %+v", output.Rows[0])
	}
	if output.TimeZone != "America/Los_Angeles" || output.Currency != "USD" {
		t.Fatalf("envelope = %+v", output)
	}
}

func TestRealtimeToolRejectsUnknownMetric(t *testing.T) {
	session := newExtraToolSession(t)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: realtimeToolName,
		Arguments: map[string]any{
			"instance": "giztop",
			"metrics":  []any{"purchaseRevenue"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected the schema enum to reject an unknown realtime metric")
	}
}
