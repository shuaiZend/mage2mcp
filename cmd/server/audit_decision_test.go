// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shuaiZend/mage2mcp/internal/mcp/server"
	"github.com/shuaiZend/mage2mcp/scripts/toolinventory"
)

// SEC-06: the audit callback used to write decision="allow" for every row, so a
// denied call read as an authorized one.
func TestAuditDecisionMapsPhaseAndState(t *testing.T) {
	cases := []struct {
		phase, state, want string
	}{
		{"intent", "pending", "pending"},
		{"outcome", "succeeded", "allow"},
		{"outcome", "denied", "deny"},
		{"outcome", "failed", "error"},
		{"outcome", "outcome_unknown", "error"},
		{"outcome", "", "error"},
	}
	for _, tc := range cases {
		if got := auditDecision(tc.phase, tc.state); got != tc.want {
			t.Fatalf("auditDecision(%q,%q) = %q, want %q", tc.phase, tc.state, got, tc.want)
		}
	}
}

// TestEveryWriteToolIsGatedOrDeclaredSelfLedgered is the completeness guard the
// two-path model depends on: it enumerates the REAL catalog and fails if any
// write tool is on neither path, or if a declaration has gone stale.
func TestEveryWriteToolIsGatedOrDeclaredSelfLedgered(t *testing.T) {
	inventory, err := toolinventory.Build()
	if err != nil {
		t.Fatalf("build the tool inventory: %v", err)
	}
	if len(inventory.Rows) == 0 {
		t.Fatal("the inventory is empty; the catalog did not build")
	}
	declared := selfLedgeredTools()

	writeTools := 0
	gated := 0
	selfLedgered := 0
	for _, row := range inventory.Rows {
		if !row.Write {
			continue
		}
		writeTools++
		if declared[row.Name] {
			selfLedgered++
			continue
		}
		// Not declared, so the middleware must gate it. Assert the gate's own
		// predicate actually covers this tool's declared permission.
		if !server.IsWritePermission(row.Scope) {
			t.Fatalf("write tool %q (permission %q) is neither self-ledgered nor covered by the middleware gate",
				row.Name, row.Scope)
		}
		gated++
	}
	// Pinned counts: a new write tool must be consciously placed on one of the
	// two paths (and this test updated), never silently inherit a default.
	// 56 write tools = the previous 55 plus magento.operation.dispose, which is
	// self-ledgered: a disposition is itself a ledger write, so the middleware
	// must not open a second operation for it (gated stays 50, self-ledgered 5->6).
	const wantWrite, wantGated, wantSelfLedgered = 56, 50, 6
	if writeTools != wantWrite || gated != wantGated || selfLedgered != wantSelfLedgered {
		t.Fatalf("catalog coverage changed: %d write tools (%d gated, %d self-ledgered), want %d/%d/%d; "+
			"place the new tool on a path and update this guard",
			writeTools, gated, selfLedgered, wantWrite, wantGated, wantSelfLedgered)
	}
	if selfLedgered == 0 {
		t.Fatal("no self-ledgered tools found; the declaration list has gone stale")
	}
	if gated == 0 {
		t.Fatal("no middleware-gated write tools found")
	}
	// Every declaration must still name a real write tool, or the set is lying
	// about what it covers.
	known := make(map[string]bool, len(inventory.Rows))
	for _, row := range inventory.Rows {
		if row.Write {
			known[row.Name] = true
		}
	}
	for name := range declared {
		if !known[name] {
			t.Fatalf("declared self-ledgered %q is not a write tool in the catalog (stale declaration)", name)
		}
	}
	t.Logf("write tools: %d total, %d middleware-gated, %d self-ledgered", writeTools, gated, selfLedgered)
}

// selfLedgeredEvidence reports, for each declared tool, whether some file under
// internal/magento both names the tool and drives the ledger (configuredOperations).
// REL38-05: the exception must be earned by real code, not by adding a name to the
// map — otherwise the completeness guard is satisfiable by declaration alone.
func selfLedgeredEvidence(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir("../../internal/magento")
	if err != nil {
		t.Fatalf("read internal/magento: %v", err)
	}
	type source struct {
		name string
		body string
	}
	sources := make([]source, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("../../internal/magento", entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		sources = append(sources, source{name: entry.Name(), body: string(raw)})
	}
	evidence := map[string]bool{}
	for name := range selfLedgeredTools() {
		quoted := `"` + name + `"`
		for _, src := range sources {
			if strings.Contains(src.body, quoted) && strings.Contains(src.body, "configuredOperations") {
				evidence[name] = true
			}
		}
	}
	return evidence
}

// Every declared self-ledgered tool must actually drive the ledger from the code
// that handles it, and no undeclared tool may do so (that would be a double row).
func TestSelfLedgeredToolsAreBackedByCode(t *testing.T) {
	evidence := selfLedgeredEvidence(t)
	for name := range selfLedgeredTools() {
		if !evidence[name] {
			t.Fatalf("tool %q is declared self-ledgered but no file under internal/magento both names it "+
				"and calls configuredOperations; the declaration is not backed by code", name)
		}
	}

	inventory, err := toolinventory.Build()
	if err != nil {
		t.Fatalf("build the tool inventory: %v", err)
	}
	declared := selfLedgeredTools()
	entries, err := os.ReadDir("../../internal/magento")
	if err != nil {
		t.Fatalf("read internal/magento: %v", err)
	}
	for _, row := range inventory.Rows {
		if !row.Write || declared[row.Name] {
			continue
		}
		quoted := `"` + row.Name + `"`
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
				strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join("../../internal/magento", entry.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", entry.Name(), err)
			}
			if strings.Contains(string(raw), quoted) && strings.Contains(string(raw), "configuredOperations") {
				t.Fatalf("write tool %q drives the ledger from %s but is not declared self-ledgered; "+
					"the middleware would create a second operation", row.Name, entry.Name())
			}
		}
	}
}
