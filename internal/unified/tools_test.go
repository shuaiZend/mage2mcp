// SPDX-License-Identifier: Apache-2.0

package unified

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
)

func TestRegisterAndCallThroughMCP(t *testing.T) {
	svc := testService(t,
		&fakeTraffic{value: Traffic{Requests: 100, Rows: 1}},
		&fakeSessions{value: Sessions{Sessions: 20}})
	reg := registry.New()
	if err := Register(reg, svc); err != nil {
		t.Fatal(err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	for _, tool := range reg.List() {
		if err := tool.Register(srv); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: compareToolName, Arguments: map[string]any{
		"binding": "b1",
		"start":   "2026-11-17T00:00:00Z",
		"end":     "2026-11-18T00:00:00Z",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out compareOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Comparison == nil || out.Comparison.GA4.Sessions != 20 {
		t.Fatalf("unexpected output: %s", raw)
	}
}
