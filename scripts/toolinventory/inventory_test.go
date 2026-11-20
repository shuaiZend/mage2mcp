// SPDX-License-Identifier: Apache-2.0

package toolinventory

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

func TestInventoryCounts(t *testing.T) {
	inv, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	if inv.MaximalCount != 112 {
		t.Errorf("maximal count = %d, want 112", inv.MaximalCount)
	}
	if inv.MagentoCount != 101 {
		t.Errorf("Magento count = %d, want 101", inv.MagentoCount)
	}
	if inv.ReadOnlyCount != 54 {
		t.Errorf("read-only count = %d, want 54", inv.ReadOnlyCount)
	}
	if inv.MagentoReadOnly != 43 {
		t.Errorf("read-only Magento count = %d, want 43", inv.MagentoReadOnly)
	}
	if inv.ReadOnlyDBCount != 56 {
		t.Errorf("read-only+database count = %d, want 56", inv.ReadOnlyDBCount)
	}
	if inv.MagentoReadOnlyDB != 46 {
		t.Errorf("read-only+database Magento count = %d, want 46", inv.MagentoReadOnlyDB)
	}
	if inv.ReadOnlyDBNonRead != 0 {
		t.Errorf("read-only+database non-read tools = %d, want 0", inv.ReadOnlyDBNonRead)
	}
	if inv.WritesGate != 52 {
		t.Errorf("tools.writes gate = %d, want 52", inv.WritesGate)
	}
	if inv.DatabaseGate != 3 {
		t.Errorf("database.url gate = %d, want 3", inv.DatabaseGate)
	}
	if inv.CombinedGate != 3 {
		t.Errorf("tools.writes+database.url gate = %d, want 3", inv.CombinedGate)
	}
	if len(inv.Rows) != 112 {
		t.Errorf("rows = %d, want 112", len(inv.Rows))
	}
}

func TestImportGatesAreDerived(t *testing.T) {
	inv, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	wantDatabaseRead := map[string]bool{
		"magento.import.status": true, "magento.import.batch": true, "magento.import.batches": true,
	}
	wantDatabaseWrite := map[string]bool{
		"magento.import.start": true, "magento.import.cancel": true, "magento.import.resume": true,
	}
	for _, row := range inv.Rows {
		switch {
		case wantDatabaseRead[row.Name]:
			if row.Gate != gateDatabase {
				t.Errorf("%s gate = %q, want %q", row.Name, row.Gate, gateDatabase)
			}
		case wantDatabaseWrite[row.Name]:
			if row.Gate != gateWritesDatabase {
				t.Errorf("%s gate = %q, want %q", row.Name, row.Gate, gateWritesDatabase)
			}
		case row.Module == "magento" && row.Write && row.Name != "magento.import.execute":
			if row.Gate != gateWrites {
				t.Errorf("%s gate = %q, want %q", row.Name, row.Gate, gateWrites)
			}
		}
	}
}

func TestGeneratedDocumentIsCurrent(t *testing.T) {
	want, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	path, err := docPath()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; run `go run ./scripts/gen-implemented-tools -write`", path)
	}
}

// The human-facing mcp-tools count table must agree with the generated
// read-only+database truth; this row drifted to 49/59 while the code was 46/56.
func TestMcpToolsReadOnlyDatabaseRowMatchesInventory(t *testing.T) {
	inv, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../docs/mcp-tools.md")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("| Read-only, database, no bindings | %d | %d |", inv.MagentoReadOnlyDB, inv.ReadOnlyDBCount)
	if !bytes.Contains(data, []byte(want)) {
		t.Fatalf("docs/mcp-tools.md does not contain %q (generated inventory says %d Magento / %d total)",
			want, inv.MagentoReadOnlyDB, inv.ReadOnlyDBCount)
	}
}
