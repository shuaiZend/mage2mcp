// SPDX-License-Identifier: Apache-2.0

// Command check-test-inventory is the process entry point for the
// test-inventory gate. The shell wrapper scripts/check-test-inventory.sh calls
// it; the logic and its tests live in scripts/testinventory.
package main

import (
	"os"

	"github.com/shuaiZend/mage2mcp/scripts/testinventory"
)

func main() {
	os.Exit(testinventory.Run(os.Args[1:], os.Stdout, os.Stderr))
}
