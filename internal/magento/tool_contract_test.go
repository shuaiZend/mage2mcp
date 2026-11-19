// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
)

// TestMagentoToolContract pins the public tool names. Renaming a tool is a
// breaking change: update the golden file and the changelog together.
func TestMagentoToolContract(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	reg := registry.New()
	if err := Register(reg, svc); err != nil {
		t.Fatal(err)
	}
	if err := RegisterWrites(reg, svc); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(map[string]*Service{"store-us": svc})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterImportJobs(reg, resolver, &fakeImportQueue{}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile("testdata/tool-names.txt")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			want = append(want, line)
		}
	}
	sort.Strings(want)

	var got []string
	for _, tool := range reg.List() {
		got = append(got, tool.Name)
	}
	sort.Strings(got)

	if len(got) != len(want) {
		t.Fatalf("registered %d tools, golden file has %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tool name drift at %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestImplementedToolsDocsMatchRegistry keeps docs/implemented-tools.md honest:
// every registered Magento tool is listed, and nothing extra is listed.
func TestImplementedToolsDocsMatchRegistry(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	reg := registry.New()
	if err := Register(reg, svc); err != nil {
		t.Fatal(err)
	}
	if err := RegisterWrites(reg, svc); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(map[string]*Service{"store-us": svc})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterImportJobs(reg, resolver, &fakeImportQueue{}); err != nil {
		t.Fatal(err)
	}
	registered := make(map[string]bool)
	for _, tool := range reg.List() {
		registered[tool.Name] = true
	}

	raw, err := os.ReadFile("../../docs/implemented-tools.md")
	if err != nil {
		t.Fatal(err)
	}
	documented := make(map[string]bool)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "| `magento.") {
			continue
		}
		name := strings.Split(strings.TrimPrefix(line, "| `"), "`")[0]
		documented[name] = true
	}
	for name := range registered {
		if !documented[name] {
			t.Errorf("tool %q is registered but missing from docs/implemented-tools.md", name)
		}
	}
	for name := range documented {
		if !registered[name] {
			t.Errorf("tool %q is documented but not registered", name)
		}
	}
}
