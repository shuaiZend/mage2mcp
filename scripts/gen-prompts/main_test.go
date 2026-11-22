// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/shuaiZend/mage2mcp/scripts/toolinventory"
)

// repoRoot is where the skill and the generated file live, relative to this
// package's directory (go test runs in the package dir).
const (
	repoRoot = "../.."
	skillDir = "../../skills/mage2mcp"
)

// budgets are the per-file character ceilings from the guidance design. A file
// that outgrows its budget is a review failure: shorten it or move the detail,
// do not raise the number.
var budgets = map[string]int{
	"SKILL.md":          2000,
	"write-contract.md": 3000,
	"refunds.md":        2500,
	"reports.md":        2500,
	"imports.md":        2500,
	"product-copy.md":   4000,
	"capabilities.md":   2000,
	"catalog-safety.md": 2500,
}

// skillFiles lists every markdown file the skill ships: the entry point and the
// references. Tests below run over all of them unless they say otherwise.
func skillFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	entries, err := os.ReadDir(filepath.Join(skillDir, "references"))
	if err != nil {
		t.Fatalf("read references: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(skillDir, "references", entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		files[entry.Name()] = string(raw)
	}
	raw, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	files["SKILL.md"] = string(raw)
	return files
}

// TestSkillFilesStayWithinBudget enforces the design's per-file ceilings. The
// skill is a resident cost for a client that loads it, so growth is a decision,
// not an accident.
func TestSkillFilesStayWithinBudget(t *testing.T) {
	files := skillFiles(t)
	for name, text := range files {
		budget, ok := budgets[name]
		if !ok {
			t.Fatalf("%s has no budget; add one or move the content", name)
		}
		if got := len([]rune(text)); got > budget {
			t.Fatalf("%s is %d characters, budget %d", name, got, budget)
		}
	}
	for name := range budgets {
		if _, ok := files[name]; !ok {
			t.Fatalf("budget names %s but the file does not exist", name)
		}
	}
}

// TestSkillNeverCopiesSchema is D1: the skill references tools by name, never by
// schema. A copied schema drifts the moment a tool changes.
func TestSkillNeverCopiesSchema(t *testing.T) {
	for name, text := range skillFiles(t) {
		for _, forbidden := range []string{`"type": "object"`, `"properties"`, "additionalProperties", "jsonschema"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s contains %q: the skill must reference a tool, not copy its schema", name, forbidden)
			}
		}
	}
}

// TestSkillNeverHardcodesToolCounts is D2: a tool count in prose is a claim that
// goes stale silently (README once said 112 while the registry had 105). The
// skill points at tools/list instead.
func TestSkillNeverHardcodesToolCounts(t *testing.T) {
	countClaim := regexp.MustCompile(`\b\d{2,3}\s*(tools|个工具)\b`)
	for name, text := range skillFiles(t) {
		if match := countClaim.FindString(text); match != "" {
			t.Fatalf("%s hardcodes a tool count (%q); point at tools/list instead", name, match)
		}
	}
	// A tool table in the entry point is a list by another name.
	for _, line := range strings.Split(skillFiles(t)["SKILL.md"], "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "| `magento.") || strings.HasPrefix(trimmed, "| `platform.") {
			t.Fatalf("SKILL.md lists a tool (%q); the authoritative list is tools/list", trimmed)
		}
	}
}

// TestSkillToolNamesExist is D3: every tool the skill names must be registered.
// The captain's rule this round: check names with a script, never by eye.
func TestSkillToolNamesExist(t *testing.T) {
	inventory, err := toolinventory.Build()
	if err != nil {
		t.Fatalf("build tool inventory: %v", err)
	}
	known := map[string]bool{"platform.info": true}
	for _, row := range inventory.Rows {
		known[row.Name] = true
	}
	toolName := regexp.MustCompile(`\b(?:magento|cloudflare|ga4|platform)(?:\.[a-z0-9_]+)+\b`)
	var unknown []string
	seen := map[string]bool{}
	for name, text := range skillFiles(t) {
		for _, match := range toolName.FindAllString(text, -1) {
			if known[match] || seen[match] {
				continue
			}
			seen[match] = true
			unknown = append(unknown, name+": "+match)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		t.Fatalf("the skill names tools that are not registered:\n%s", strings.Join(unknown, "\n"))
	}
}

// TestSkillIndexMatchesReferences is D4: the entry point links every reference,
// and every link resolves to an existing file and heading. A renamed heading
// breaks the index on purpose.
func TestSkillIndexMatchesReferences(t *testing.T) {
	files := skillFiles(t)
	index := files["SKILL.md"]
	link := regexp.MustCompile(`\]\((references/[a-z0-9-]+\.md)(#[a-z0-9-]+)?\)`)

	linked := map[string]bool{}
	for _, match := range link.FindAllStringSubmatch(index, -1) {
		path := match[1]
		name := strings.TrimPrefix(path, "references/")
		text, ok := files[name]
		if !ok {
			t.Fatalf("SKILL.md links %s, which does not exist", path)
		}
		linked[name] = true
		if anchor := strings.TrimPrefix(match[2], "#"); anchor != "" {
			if !containsAnchor(text, anchor) {
				t.Fatalf("SKILL.md links %s#%s, but no heading produces that anchor", path, anchor)
			}
		}
	}
	for name := range files {
		if name == "SKILL.md" {
			continue
		}
		if !linked[name] {
			t.Fatalf("references/%s is not linked from SKILL.md", name)
		}
	}
}

// containsAnchor reports whether a markdown heading in text slugifies to anchor.
func containsAnchor(text, anchor string) bool {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		if slug(strings.TrimSpace(strings.TrimLeft(trimmed, "#"))) == anchor {
			return true
		}
	}
	return false
}

// slug renders a heading the way GitHub anchors it: lowercase, punctuation
// dropped, spaces to hyphens.
func slug(heading string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			out.WriteRune(r)
		case r == ' ':
			out.WriteRune('-')
		}
	}
	return out.String()
}

// TestGeneratedPromptsAreCurrent is D5: the generated file must equal what the
// references produce. Editing one side without the other fails here.
func TestGeneratedPromptsAreCurrent(t *testing.T) {
	want, err := GenerateAt(repoRoot)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(repoRoot, "internal", "mcp", "server", "prompts_gen.go"))
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("internal/mcp/server/prompts_gen.go is stale; run `go run ./scripts/gen-prompts -write`")
	}
}

// skillOnlyReferences are references that ship with the skill but deliberately
// generate no MCP prompt: capabilities.md is the "what no tool can do" list,
// which belongs to the skill a reader loads, not to a prompt an agent asks for.
var skillOnlyReferences = map[string]bool{"capabilities.md": true}

// TestEveryReferenceBacksOnePrompt keeps the mapping honest: every reference file
// is the source of exactly one prompt (or is a declared skill-only reference), so
// no reference is dead weight and no prompt has a second source.
func TestEveryReferenceBacksOnePrompt(t *testing.T) {
	files := skillFiles(t)
	for _, source := range promptSources {
		if _, ok := files[source.File]; !ok {
			t.Fatalf("prompt %s is generated from %s, which does not exist", source.Name, source.File)
		}
	}
	used := map[string]bool{}
	for _, source := range promptSources {
		if used[source.File] {
			t.Fatalf("reference %s backs more than one prompt", source.File)
		}
		used[source.File] = true
	}
	for name := range files {
		if name == "SKILL.md" || skillOnlyReferences[name] {
			continue
		}
		if !used[name] {
			t.Fatalf("reference %s backs no prompt; add it to promptSources or declare it skill-only", name)
		}
	}
}
