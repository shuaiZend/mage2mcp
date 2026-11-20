// SPDX-License-Identifier: Apache-2.0

// Command gen-implemented-tools regenerates docs/implemented-tools.md from the
// tool registry. Run with -write to update the checked-in file; without it the
// generated document is printed to stdout for inspection.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/shuaiZend/mage2mcp/scripts/toolinventory"
)

func main() {
	write := flag.Bool("write", false, "write the generated document to -path")
	path := flag.String("path", "docs/implemented-tools.md", "document path")
	flag.Parse()

	data, err := toolinventory.Generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen-implemented-tools:", err)
		os.Exit(1)
	}
	if !*write {
		_, _ = os.Stdout.Write(data)
		return
	}
	if err := os.WriteFile(*path, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "gen-implemented-tools:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", *path)
}
