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

// dataAPIHandler serves canned runReport and metadata responses.
func dataAPIHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, ":runReport"):
			_, _ = io.WriteString(w, cannedReport)
		case strings.HasSuffix(r.URL.Path, "/metadata"):
			_, _ = io.WriteString(w, cannedMetadata)
		default:
			http.NotFound(w, r)
		}
	})
}

// newToolSession registers the GA4 tools on an in-memory MCP server and returns
// a connected client session.
func newToolSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	reg := registry.New()
	if err := Register(reg, newTestService(t, dataAPIHandler(t))); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if reg.Len() != 2 {
		t.Fatalf("registered tools = %d", reg.Len())
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "ga4-test", Version: "0"}, nil)
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

func contentText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			return text.Text
		}
	}
	t.Fatal("tool result has no text content")
	return ""
}

func TestRegisterToolMetadata(t *testing.T) {
	reg := registry.New()
	if err := Register(reg, newTestService(t, dataAPIHandler(t))); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cases := []struct {
		name       string
		capability string
		timeout    string
	}{
		{trafficToolName, trafficCapability, "1m0s"},
		{metadataToolName, metadataCapability, "30s"},
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
			t.Fatalf("%s capability = %q", tc.name, tool.Capability)
		}
		if tool.Timeout.String() != tc.timeout {
			t.Fatalf("%s timeout = %v", tc.name, tool.Timeout)
		}
		if !tool.Annotations.ReadOnlyHint {
			t.Fatalf("%s must be read-only", tc.name)
		}
		if len(tool.Title) == 0 || len(tool.Title) > 60 {
			t.Fatalf("%s title length = %d", tc.name, len(tool.Title))
		}
		if !strings.Contains(tool.Description, "Google Analytics 4") || !strings.Contains(tool.Description, "aggregate") {
			t.Fatalf("%s description must state the source and aggregate-only behaviour: %q", tc.name, tool.Description)
		}
		if !strings.Contains(tool.Description, "capability probe") {
			t.Fatalf("%s description must mention the ecommerce capability probe", tc.name)
		}
	}
}

func TestRegisterRejectsNilDependencies(t *testing.T) {
	if err := Register(nil, &Service{}); err == nil {
		t.Fatal("expected an error for a nil registry")
	}
	if err := Register(registry.New(), nil); err == nil {
		t.Fatal("expected an error for a nil service")
	}
}

func TestTrafficToolCall(t *testing.T) {
	session := newToolSession(t)
	ctx := context.Background()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var traffic *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == trafficToolName {
			traffic = tool
		}
	}
	if traffic == nil {
		t.Fatalf("%s not listed", trafficToolName)
	}
	if traffic.Annotations == nil || !traffic.Annotations.ReadOnlyHint {
		t.Fatal("ga4.analytics.traffic must be read-only")
	}
	if traffic.InputSchema == nil || traffic.OutputSchema == nil {
		t.Fatal("ga4.analytics.traffic must declare input and output schemas")
	}
	schema, err := json.Marshal(traffic.InputSchema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}
	if !strings.Contains(string(schema), `"enum"`) || !strings.Contains(string(schema), `"sessions"`) {
		t.Fatalf("input schema must advertise the metric enum: %s", schema)
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: trafficToolName,
		Arguments: map[string]any{
			"instance": "giztop",
			"metrics":  []any{"sessions", "totalUsers"},
		},
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
	var report ReportResult
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("structured content is not a ReportResult: %v", err)
	}
	if len(report.Rows) != 2 || report.Rows[0].Date != "2026-11-12" {
		t.Fatalf("rows = %+v", report.Rows)
	}
	if report.Totals["sessions"] != 250 || report.Totals["totalUsers"] != 185 {
		t.Fatalf("totals = %+v", report.Totals)
	}
	if report.TimeZone != "America/Los_Angeles" || report.Currency != "USD" || !report.Sampled {
		t.Fatalf("envelope = %+v", report)
	}
}

func TestTrafficToolInvalidMetric(t *testing.T) {
	session := newToolSession(t)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: trafficToolName,
		Arguments: map[string]any{
			"instance": "giztop",
			"metrics":  []any{"purchaseRevenue"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected a tool error for an unsupported metric")
	}
	text := contentText(t, result)
	if !strings.Contains(text, "validation_failed") || !strings.Contains(text, "purchaseRevenue") {
		t.Fatalf("tool error = %q", text)
	}
}

func TestMetadataToolCall(t *testing.T) {
	session := newToolSession(t)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      metadataToolName,
		Arguments: map[string]any{"instance": "giztop"},
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
	var metadata PropertyMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatalf("structured content is not PropertyMetadata: %v", err)
	}
	if len(metadata.Dimensions) != 2 || metadata.Dimensions[0] != "date" {
		t.Fatalf("dimensions = %+v", metadata.Dimensions)
	}
	if len(metadata.Metrics) != 2 || metadata.Metrics[0] != "sessions" {
		t.Fatalf("metrics = %+v", metadata.Metrics)
	}
}
