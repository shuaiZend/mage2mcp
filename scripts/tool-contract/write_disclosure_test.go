// SPDX-License-Identifier: Apache-2.0

package toolcontract

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/magento"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
)

type noopSigner struct{}

func (noopSigner) Sign(*http.Request) error { return nil }

type fakeQueue struct{}

func (fakeQueue) Enqueue(context.Context, jobs.Task) (string, error) { return "job-1", nil }
func (fakeQueue) Cancel(context.Context, string, string, string) error {
	return nil
}
func (fakeQueue) GetJob(context.Context, string, string, string) (*jobs.JobRecord, error) {
	return nil, nil
}

// TestWriteToolDescriptionsDiscloseSideEffects is the MCP-06 contract. It
// enumerates the registry, not a hand-maintained list, so a new write tool
// without a disclosure entry fails here instead of shipping an undocumented
// side effect. Every write must state:
//
//   - read/write nature ("write operation"),
//   - what it mutates ("Mutates:"),
//   - whether it is idempotent ("idempotent"),
//   - that it needs confirmation ("confirmation").
//
// The five tools that historically omitted confirmation (configurable.add_child,
// configurable.create, inventory.assign, product.create, product.update) are
// not exempted: discloseWrite adds the sentence when the description lacks it.
func TestWriteToolDescriptionsDiscloseSideEffects(t *testing.T) {
	mc, err := magento.NewClient(magento.ClientConfig{
		BaseURL: "https://shop.example.com", StoreCode: "default", Signer: noopSigner{},
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
	if err := magento.RegisterWritesWithResolver(reg, resolver); err != nil {
		t.Fatal(err)
	}
	if err := magento.RegisterImportJobs(reg, resolver, fakeQueue{}); err != nil {
		t.Fatal(err)
	}

	writes := 0
	for _, tool := range reg.List() {
		if tool.Permission == registry.PermissionRead {
			continue
		}
		writes++
		checkWriteDisclosure(t, "registry", tool.Name, tool.Description)
	}
	// 56 = the previous 55 plus magento.operation.dispose, the ledger disposition
	// write tool. A new write tool must be counted here on purpose.
	if writes != 56 {
		t.Fatalf("write tool count = %d, want 56", writes)
	}

	// The agent-facing mcp.Tool descriptions are built separately from the
	// registry metadata. Enumerate them through a real in-memory MCP session so
	// a custom mcp.Tool literal cannot bypass discloseWrite.
	srv := mcp.NewServer(&mcp.Implementation{Name: "disclosure-test", Version: "0"}, nil)
	for _, tool := range reg.List() {
		if err := tool.Register(srv); err != nil {
			t.Fatalf("register %s: %v", tool.Name, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	go func() { _, _ = srv.Connect(ctx, st, nil) }()
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "disclosure-client", Version: "0"}, nil)
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tool := range list.Tools {
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint {
			continue
		}
		seen++
		checkWriteDisclosure(t, "mcp", tool.Name, tool.Description)
	}
	if seen != writes {
		t.Fatalf("mcp write descriptions = %d, registry write tools = %d", seen, writes)
	}
}

func checkWriteDisclosure(t *testing.T, surface, name, description string) {
	t.Helper()
	lower := strings.ToLower(description)
	for _, phrase := range []string{"write operation", "mutates:", "idempotent", "confirmation"} {
		if !strings.Contains(lower, phrase) {
			t.Errorf("%s description for %q is missing %q: %q", surface, name, phrase, description)
		}
	}
	if len(description) > 1024 {
		t.Errorf("%s description for %q is %d bytes, over the 1024 registry limit", surface, name, len(description))
	}
}
