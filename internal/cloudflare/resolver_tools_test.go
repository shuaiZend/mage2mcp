// SPDX-License-Identifier: Apache-2.0

package cloudflare

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
)

func fakeZoneServer(t *testing.T, requests int) *Service {
	t.Helper()
	server, _ := newFakeGraphQL(t, func(graphqlCall) string {
		return `{"data":{"viewer":{"zones":[{"httpRequests1hGroups":[
			{"dimensions":{"clientRequestHTTPHost":"h"},"sum":{"requests":` + itoa(requests) + `,"bytes":2,"cachedRequests":0,"cachedBytes":0}}
		]}]}}}`
	})
	return NewService(mustClient(t, testConfig(server.URL)))
}

func TestRegisterWithResolverSelectsInstance(t *testing.T) {
	resolver, err := NewResolver(map[string]*Service{
		"zone-a": fakeZoneServer(t, 1),
		"zone-b": fakeZoneServer(t, 2),
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	if err := RegisterWithResolver(reg, resolver); err != nil {
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

	args := map[string]any{"since": "2026-11-17T00:00:00Z", "until": "2026-11-18T00:00:00Z", "group_by": "hostname"}
	args["instance"] = "zone-b"
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: trafficToolName, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(raw), `"requests":2`) {
		t.Fatalf("wrong instance served: %s", raw)
	}

	args["instance"] = "zone-missing"
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: trafficToolName, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("unknown instance must be a tool error")
	}
}

func itoa(v int) string { return strconv.Itoa(v) }
