// SPDX-License-Identifier: Apache-2.0

// Package toolinventory derives the implemented-tool inventory from the tool
// registry. It is the single source of truth for docs/implemented-tools.md:
// the generator and the staleness test call the same Generate function.
package toolinventory

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/shuaiZend/mage2mcp/internal/cloudflare"
	"github.com/shuaiZend/mage2mcp/internal/ga4"
	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/magento"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	"github.com/shuaiZend/mage2mcp/internal/unified"
)

// Row is one generated inventory row.
type Row struct {
	Name         string
	Module       string
	Scope        string
	Write        bool
	Confirmation bool
	Timeout      string
	Gate         string
	Notes        string
}

// Inventory is the derived inventory plus the counts the documentation must
// state. Maximal and ReadOnly include the server-owned platform.info tool,
// which is not in the module registry.
type Inventory struct {
	Rows              []Row
	MaximalCount      int
	MagentoCount      int
	ReadOnlyCount     int
	MagentoReadOnly   int
	ReadOnlyDBCount   int
	MagentoReadOnlyDB int
	ReadOnlyDBNonRead int
	WritesGate        int
	DatabaseGate      int
	CombinedGate      int
	WritesGateNames   []string
	DatabaseGateList  []string
}

const (
	gateAlways         = "always"
	gateWrites         = "tools.writes"
	gateDatabase       = "database.url"
	gateWritesDatabase = "tools.writes + database.url"
	platformTool       = "platform.info"
	generatedMarker    = "<!-- GENERATED INVENTORY START -->"
)

var importJobTools = map[string]bool{
	"magento.import.start":   true,
	"magento.import.cancel":  true,
	"magento.import.resume":  true,
	"magento.import.status":  true,
	"magento.import.batch":   true,
	"magento.import.batches": true,
}

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

type trafficProvider struct{}
type sessionProvider struct{}

func (trafficProvider) Traffic(context.Context, string, time.Time, time.Time) (unified.Traffic, error) {
	return unified.Traffic{}, nil
}
func (sessionProvider) Sessions(context.Context, string, time.Time, time.Time) (unified.Sessions, error) {
	return unified.Sessions{}, nil
}

// Build derives the maximal and read-only registries and their rows.
func Build() (*Inventory, error) {
	maximal, err := buildRegistry(true, true, true)
	if err != nil {
		return nil, err
	}
	readOnly, err := buildRegistry(false, false, true)
	if err != nil {
		return nil, err
	}
	readOnlyDB, err := buildRegistry(false, true, false)
	if err != nil {
		return nil, err
	}

	readNames := map[string]bool{platformTool: true}
	for _, tool := range readOnly.List() {
		readNames[tool.Name] = true
	}

	rows := make([]Row, 0, maximal.Len()+1)
	rows = append(rows, platformRow())
	for _, tool := range maximal.List() {
		row := Row{
			Name:         tool.Name,
			Module:       moduleOf(tool.Name),
			Scope:        tool.Permission,
			Write:        tool.Permission != registry.PermissionRead,
			Confirmation: tool.RequiresConfirmation,
			Timeout:      timeoutOf(tool.Timeout),
			Gate:         gateFor(tool.Name, readNames),
			Notes:        firstSentence(tool.Description),
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	inv := &Inventory{Rows: rows, MaximalCount: len(rows)}
	for _, tool := range readOnlyDB.List() {
		if tool.Permission != registry.PermissionRead {
			inv.ReadOnlyDBNonRead++
		}
		if moduleOf(tool.Name) == "magento" {
			inv.MagentoReadOnlyDB++
		}
	}
	inv.ReadOnlyDBCount = readOnlyDB.Len() + 1 // + platform.info
	for _, row := range rows {
		if row.Module == "magento" {
			inv.MagentoCount++
			if readNames[row.Name] {
				inv.MagentoReadOnly++
			}
		}
		if row.Gate == gateWrites {
			inv.WritesGate++
			inv.WritesGateNames = append(inv.WritesGateNames, row.Name)
		}
		if row.Gate == gateDatabase {
			inv.DatabaseGate++
			inv.DatabaseGateList = append(inv.DatabaseGateList, row.Name)
		}
		if row.Gate == gateWritesDatabase {
			inv.CombinedGate++
		}
	}
	inv.ReadOnlyCount = len(readNames)
	return inv, nil
}

func buildRegistry(writes, importJobs, bindings bool) (*registry.Registry, error) {
	magentoClient, err := magento.NewClient(magento.ClientConfig{
		BaseURL: "https://shop.example.com", StoreCode: "default", Signer: noopSigner{},
	})
	if err != nil {
		return nil, err
	}
	magentoService, err := magento.NewService(magentoClient)
	if err != nil {
		return nil, err
	}
	resolver, err := magento.NewResolver(map[string]*magento.Service{"store-us": magentoService})
	if err != nil {
		return nil, err
	}
	cloudflareClient, err := cloudflare.NewClient(cloudflare.Config{
		APIBaseURL: "https://api.cloudflare.com/client/v4", APIToken: "inventory", ZoneID: "inventory",
	})
	if err != nil {
		return nil, err
	}
	ga4Service, err := ga4.NewService(ga4.Config{PropertyID: "123456", TokenSource: ga4.StaticToken("inventory")})
	if err != nil {
		return nil, err
	}
	unifiedService, err := unified.NewService(
		[]unified.Binding{{ID: "inventory", Magento: "store-us", Cloudflare: "cf", GA4: "ga"}},
		trafficProvider{}, sessionProvider{},
	)
	if err != nil {
		return nil, err
	}

	reg := registry.New()
	if err := magento.RegisterWithResolver(reg, resolver); err != nil {
		return nil, err
	}
	if writes {
		if err := magento.RegisterWritesWithResolver(reg, resolver); err != nil {
			return nil, err
		}
	}
	if importJobs {
		if writes {
			if err := magento.RegisterImportJobs(reg, resolver, fakeQueue{}); err != nil {
				return nil, err
			}
		} else if err := magento.RegisterImportReadTools(reg, resolver, fakeQueue{}); err != nil {
			return nil, err
		}
	}
	if err := cloudflare.Register(reg, cloudflare.NewService(cloudflareClient)); err != nil {
		return nil, err
	}
	if err := ga4.Register(reg, ga4Service); err != nil {
		return nil, err
	}
	if err := ga4.RegisterExtra(reg, ga4Service); err != nil {
		return nil, err
	}
	if bindings {
		if err := unified.Register(reg, unifiedService); err != nil {
			return nil, err
		}
	}
	return reg, nil
}

func platformRow() Row {
	return Row{
		Name: platformTool, Module: "platform", Scope: "read",
		Timeout: "30s", Gate: gateAlways,
		Notes: "Server information: version, protocol versions, registered tools, instances, and the writes flag.",
	}
}

func moduleOf(name string) string {
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return name[:i]
	}
	return name
}

func firstSentence(description string) string {
	description = strings.TrimSpace(description)
	if i := strings.Index(description, ". "); i >= 0 {
		description = description[:i+1]
	}
	if len(description) > 180 {
		description = description[:177] + "..."
	}
	return description
}

func timeoutOf(timeout time.Duration) string {
	if timeout <= 0 {
		return "none"
	}
	return fmt.Sprintf("%ds", int(timeout.Seconds()))
}

func gateFor(name string, readOnly map[string]bool) string {
	if readOnly[name] {
		return gateAlways
	}
	if importJobTools[name] {
		switch name {
		case "magento.import.start", "magento.import.cancel", "magento.import.resume":
			return gateWritesDatabase
		default:
			return gateDatabase
		}
	}
	return gateWrites
}

// Generate returns the complete docs/implemented-tools.md content. The prose
// before the generated marker is preserved; the tables and counts are derived
// from Build so a registration change cannot leave the document stale.
func Generate() ([]byte, error) {
	inv, err := Build()
	if err != nil {
		return nil, err
	}
	preamble, err := readPreamble()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.Write(preamble)
	buf.WriteString(generatedMarker)
	buf.WriteString("\n\n")
	writeGenerated(&buf, inv)
	out := bytes.TrimRight(buf.Bytes(), "\n")
	out = append(out, '\n')
	return out, nil
}

func readPreamble() ([]byte, error) {
	path, err := docPath()
	if err != nil {
		return nil, err
	}
	// G304: docPath returns a constant repository-relative path
	// (docs/implemented-tools.md); nothing from the caller or the environment
	// reaches it.
	data, err := os.ReadFile(path) //nolint:gosec // see above
	if err != nil {
		if os.IsNotExist(err) {
			return []byte("# Implemented tools\n\n"), nil
		}
		return nil, err
	}
	if i := bytes.Index(data, []byte(generatedMarker)); i >= 0 {
		return data[:i], nil
	}
	if i := bytes.Index(data, []byte("## Platform")); i >= 0 {
		return data[:i], nil
	}
	return data, nil
}

func docPath() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("inventory: cannot locate repository root")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "docs", "implemented-tools.md"), nil
}

func writeGenerated(buf *bytes.Buffer, inv *Inventory) {
	buf.WriteString("## Generated inventory\n\n")
	buf.WriteString("Generated from the tool registry by `go run ./scripts/gen-implemented-tools -write`.\n")
	buf.WriteString("Do not edit the tables by hand; run the generator after changing registration.\n\n")
	buf.WriteString("### Configuration counts\n\n")
	buf.WriteString("| Configuration | Tools | Magento |\n|---|---:|---:|\n")
	fmt.Fprintf(buf, "| Maximal (all modules, `tools.writes: true`, `database.url` set, bindings configured) | %d | %d |\n", inv.MaximalCount, inv.MagentoCount)
	fmt.Fprintf(buf, "| Read-only default (all modules, no writes, no database) | %d | %d |\n", inv.ReadOnlyCount, inv.MagentoReadOnly)
	fmt.Fprintf(buf, "| Read-only, database, no bindings | %d | %d |\n\n", inv.ReadOnlyDBCount, inv.MagentoReadOnlyDB)
	fmt.Fprintf(buf, "`tools.writes` gates %d Magento write/destructive tools; `database.url` gates %d import read tools (status/batches/batch); start/cancel/resume require both. Module enablement and bindings gate the Cloudflare, GA4, and unified tools; `%s` is always registered.\n\n",
		inv.WritesGate, inv.DatabaseGate, platformTool)
	buf.WriteString("A database-enabled read-only deployment registers only the three read import tools; start/cancel/resume also require `tools.writes`.\n\n")

	for _, group := range []struct{ module, title string }{
		{"platform", "Platform"},
		{"magento", "Magento 2"},
		{"cloudflare", "Cloudflare"},
		{"ga4", "Google Analytics 4"},
		{"unified", "Unified analytics"},
	} {
		rows := rowsFor(inv.Rows, group.module)
		fmt.Fprintf(buf, "### %s (%d %s)\n\n", group.title, len(rows), plural(len(rows)))
		buf.WriteString("| Tool | Scope | Write? | Confirmation | Timeout | Gate | Notes |\n")
		buf.WriteString("|---|---|---|---|---|---|---|\n")
		for _, row := range rows {
			write := "no"
			if row.Write {
				write = "yes"
			}
			confirmation := "no"
			if row.Confirmation {
				confirmation = "yes"
			}
			fmt.Fprintf(buf, "| `%s` | %s | %s | %s | %s | %s | %s |\n",
				row.Name, row.Scope, write, confirmation, row.Timeout, row.Gate, row.Notes)
		}
		buf.WriteString("\n")
	}
}

func plural(n int) string {
	if n == 1 {
		return "tool"
	}
	return "tools"
}

func rowsFor(rows []Row, module string) []Row {
	out := make([]Row, 0, len(rows))
	for _, row := range rows {
		if row.Module == module {
			out = append(out, row)
		}
	}
	return out
}
