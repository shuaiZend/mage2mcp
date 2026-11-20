// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	mcpserver "github.com/shuaiZend/mage2mcp/internal/mcp/server"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
)

func TestWriteArgumentsValidatedBeforeConfirmation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	reg := registry.New()
	if err := RegisterWrites(reg, svc); err != nil {
		t.Fatal(err)
	}
	confirmSvc, err := confirm.NewService(confirm.NewMemoryStore(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: confirmSvc})
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
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	// A malformed write must fail validation instead of asking a human to
	// approve it (the bogus property is not in the product.create schema).
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "magento.product.create",
		Arguments: map[string]any{
			"instance": "store-us", "sku": "SKU-1", "name": "Widget",
			"attribute_set_id": 4, "type_id": "simple", "price": "9.99",
			"bogus": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("malformed write was accepted")
	}
	raw := result.StructuredContent
	if code, _ := raw.(map[string]any)["code"].(string); code != "invalid_argument" {
		t.Fatalf("code = %v, want invalid_argument (confirmation must not be issued)", raw)
	}
}

// magentoConfirmService returns an in-memory confirmation service. NewMCP
// refuses to start when a registered tool requires approval and no service is
// supplied, so every test that builds a server needs one.
// magentoConfirmService returns the package-wide confirmation service.
//
// It is shared rather than per-server because a test may build several servers
// (one per simulated caller) and the approval must be visible to all of them —
// exactly as it is in production, where the store is shared. No test in this
// package calls t.Parallel, so the singleton cannot be observed concurrently.
func magentoConfirmService(t *testing.T) *confirm.Service {
	t.Helper()
	testConfirmOnce.Do(func() {
		svc, err := confirm.NewService(confirm.NewMemoryStore(), time.Minute)
		if err != nil {
			t.Fatalf("confirm.NewService: %v", err)
		}
		testConfirmService = svc
	})
	return testConfirmService
}

var (
	testConfirmOnce    sync.Once
	testConfirmService *confirm.Service
)
