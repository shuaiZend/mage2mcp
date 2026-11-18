// SPDX-License-Identifier: Apache-2.0

package cloudflare

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
)

const (
	trafficGraphQLReply  = `{"data":{"viewer":{"zones":[{"httpRequests1hGroups":[{"dimensions":{"hostname":"www.example.com"},"sum":{"requests":120,"bytes":4096,"cachedRequests":100,"cachedBytes":3000}}]}]}}}`
	securityGraphQLReply = `{"data":{"viewer":{"zones":[{"firewallEventsAdaptiveGroups":[{"count":7,"dimensions":{"action":"block"}},{"count":3,"dimensions":{"action":"managed_challenge"}}]}]}}}`
)

func toolGraphQLReply(call graphqlCall) string {
	switch {
	case strings.Contains(call.Query, "httpRequests1hGroups"):
		return trafficGraphQLReply
	case strings.Contains(call.Query, "firewallEventsAdaptiveGroups"):
		return securityGraphQLReply
	default:
		return `{"errors":[{"message":"unexpected query"}]}`
	}
}

func newToolService(t *testing.T) (*Service, *graphqlRecorder) {
	t.Helper()
	server, recorder := newFakeGraphQL(t, toolGraphQLReply)
	return NewService(mustClient(t, testConfig(server.URL))), recorder
}

func TestRegisterMetadata(t *testing.T) {
	service, _ := newToolService(t)
	reg := registry.New()
	if err := Register(reg, service); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if reg.Len() != 3 {
		t.Fatalf("registered tools = %d, want 3", reg.Len())
	}
	tests := []struct {
		name       string
		capability string
		source     string
	}{
		{trafficToolName, "cloudflare.analytics.read", "httpRequests1hGroups"},
		{securityToolName, "cloudflare.security.read", "firewallEventsAdaptiveGroups"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tool, ok := reg.Lookup(tc.name)
			if !ok {
				t.Fatalf("%s is not registered", tc.name)
			}
			if err := registry.Validate(tool); err != nil {
				t.Fatalf("registry validation: %v", err)
			}
			if tool.Permission != registry.PermissionRead {
				t.Fatalf("permission = %q", tool.Permission)
			}
			if tool.Capability != tc.capability {
				t.Fatalf("capability = %q", tool.Capability)
			}
			if tool.Timeout != 60*time.Second {
				t.Fatalf("timeout = %s", tool.Timeout)
			}
			if !tool.Annotations.ReadOnlyHint {
				t.Fatal("tool must be read-only")
			}
			if tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint {
				t.Fatal("tool must not be destructive")
			}
			if tool.RequiresConfirmation {
				t.Fatal("read tool must not require confirmation")
			}
			if len(tool.Title) > 60 {
				t.Fatalf("title is %d bytes", len(tool.Title))
			}
			description := strings.ToLower(tool.Description)
			for _, want := range []string{"read-only", "sampl", strings.ToLower(tc.source)} {
				if !strings.Contains(description, want) {
					t.Fatalf("description missing %q: %s", want, tool.Description)
				}
			}
		})
	}
}

func TestRegisterRejectsNilInputs(t *testing.T) {
	service, _ := newToolService(t)
	if err := Register(nil, service); err == nil {
		t.Fatal("expected an error for a nil registry")
	}
	if err := Register(registry.New(), nil); err == nil {
		t.Fatal("expected an error for a nil service")
	}
}

func newMCPClient(t *testing.T) (*mcp.ClientSession, *graphqlRecorder) {
	t.Helper()
	service, recorder := newToolService(t)
	reg := registry.New()
	if err := Register(reg, service); err != nil {
		t.Fatalf("Register: %v", err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "mage2mcp-test", Version: "0"}, nil)
	for _, tool := range reg.List() {
		if err := tool.Register(server); err != nil {
			t.Fatalf("register %s: %v", tool.Name, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession, recorder
}

func trafficArguments(limit int) map[string]any {
	args := map[string]any{
		"instance": "store-us",
		"since":    "2026-11-01T00:00:00Z",
		"until":    "2026-11-02T00:00:00Z",
		"group_by": "hostname",
	}
	if limit > 0 {
		args["limit"] = limit
	}
	return args
}

func decodeTrafficOutput(t *testing.T, content any) TrafficOutput {
	t.Helper()
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var out TrafficOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("structured content is not TrafficOutput: %v", err)
	}
	return out
}

func decodeSecurityOutput(t *testing.T, content any) SecurityOutput {
	t.Helper()
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var out SecurityOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("structured content is not SecurityOutput: %v", err)
	}
	return out
}

func resultText(res *mcp.CallToolResult) string {
	var builder strings.Builder
	for _, content := range res.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			builder.WriteString(text.Text)
		}
	}
	return builder.String()
}

func TestToolsListAndSchemas(t *testing.T) {
	session, _ := newMCPClient(t)
	ctx := context.Background()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) != 3 {
		t.Fatalf("tools = %d, want 3", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("%s must be read-only", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Fatalf("%s has no input schema", tool.Name)
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal schema: %v", err)
		}
		schema := string(raw)
		if !strings.Contains(schema, "additionalProperties") || !strings.Contains(schema, "instance") {
			t.Fatalf("%s has an unexpected schema: %s", tool.Name, schema)
		}
		if tool.Name == trafficToolName {
			for _, want := range []string{"group_by", "hostname", "country", "status"} {
				if !strings.Contains(schema, want) {
					t.Fatalf("traffic schema missing %q: %s", want, schema)
				}
			}
		}
	}
}

func TestMCPTrafficTool(t *testing.T) {
	session, recorder := newMCPClient(t)
	ctx := context.Background()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      trafficToolName,
		Arguments: trafficArguments(10),
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned an error: %s", resultText(res))
	}
	out := decodeTrafficOutput(t, res.StructuredContent)
	if len(out.Rows) != 1 {
		t.Fatalf("rows = %d", len(out.Rows))
	}
	if out.Rows[0].Dimension != "www.example.com" || out.Rows[0].Requests != 120 {
		t.Fatalf("unexpected row: %+v", out.Rows[0])
	}
	if out.Sampled {
		t.Fatal("httpRequests1hGroups is not sampled")
	}
	if len(out.Quality.Notes) == 0 {
		t.Fatal("expected a quality note")
	}
	if got := recorder.last(t).Variables["limit"]; got != float64(10) {
		t.Fatalf("limit = %#v", got)
	}

	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      trafficToolName,
		Arguments: trafficArguments(0),
	})
	if err != nil {
		t.Fatalf("CallTool without limit: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned an error: %s", resultText(res))
	}
	if got := recorder.last(t).Variables["limit"]; got != float64(defaultTrafficLimit) {
		t.Fatalf("default limit = %#v, want %d", got, defaultTrafficLimit)
	}
}

func TestMCPSecurityTool(t *testing.T) {
	session, recorder := newMCPClient(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: securityToolName,
		Arguments: map[string]any{
			"instance": "store-us",
			"since":    "2026-11-01T00:00:00Z",
			"until":    "2026-11-02T00:00:00Z",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned an error: %s", resultText(res))
	}
	out := decodeSecurityOutput(t, res.StructuredContent)
	if out.Actions["block"] != 7 || out.Actions["managed_challenge"] != 3 {
		t.Fatalf("unexpected actions: %+v", out.Actions)
	}
	if out.Total != 10 {
		t.Fatalf("total = %d", out.Total)
	}
	if !out.Sampled || out.RetentionDays != 31 {
		t.Fatalf("sampling/retention = %v/%d", out.Sampled, out.RetentionDays)
	}
	if got := recorder.last(t).Variables["zoneTag"]; got != "zone-test" {
		t.Fatalf("zoneTag = %#v", got)
	}
}

func TestMCPBadGroupByIsToolError(t *testing.T) {
	session, _ := newMCPClient(t)
	args := trafficArguments(10)
	args["group_by"] = "path"
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      trafficToolName,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("a bad group_by must be a tool error")
	}
}

func TestMCPBadTimestampIsToolError(t *testing.T) {
	session, _ := newMCPClient(t)
	args := trafficArguments(10)
	args["since"] = "2026-11-01"
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      trafficToolName,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("a bad timestamp must be a tool error")
	}
	if text := resultText(res); !strings.Contains(text, string(errs.CodeInvalidArgument)) {
		t.Fatalf("tool error does not carry invalid_argument: %s", text)
	}
}

func TestMCPMissingRequiredArgumentIsToolError(t *testing.T) {
	session, _ := newMCPClient(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: trafficToolName,
		Arguments: map[string]any{
			"since": "2026-11-01T00:00:00Z",
			"until": "2026-11-02T00:00:00Z",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("a missing instance must be a tool error")
	}
}
