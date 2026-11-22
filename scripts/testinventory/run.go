// SPDX-License-Identifier: Apache-2.0

package testinventory

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// Exit codes. They are part of the contract the shell wrapper and CI rely on.
const (
	ExitOK       = 0
	ExitRemovals = 1
	ExitUsage    = 2
)

const usageText = `check-test-inventory: fail when a test function disappeared between two revisions

Usage:
  check-test-inventory.sh [--base <ref>] [--head <ref>] [--allow-removed <TestName>]... [--repo <dir>]

Every top-level ` + "`func Test...`" + ` in every *_test.go is read from both revisions and
compared by package directory and name. An added test is reported; a removed
test fails the check, because a deleted test cannot fail and the suite stays
green without it.

Flags:
  --base <ref>              base revision (default "origin/main")
  --head <ref>              head revision (default "HEAD")
  --allow-removed <name>    allow this test to disappear; repeatable. Use it only
                            for a deliberate deletion, and say why in the commit.
  --repo <dir>              git working tree to read (default ".")
  -h, --help                print this help and exit 0

Exit codes:
  0  no test disappeared (additions and moves are reported, not failed)
  1  at least one test disappeared and was not allowed
  2  usage error, or a revision could not be read

Examples:
  # the gate as CI runs it: the PR base branch against the working tree
  bash scripts/check-test-inventory.sh --base "origin/${GITHUB_BASE_REF:-main}" --head HEAD

  # a deliberate deletion, recorded in the command
  bash scripts/check-test-inventory.sh --base origin/main --allow-removed TestLegacyOrderSummary

  # inspect any two commits
  bash scripts/check-test-inventory.sh --base c49d201 --head 322b438
`

// stringList collects a repeatable flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("value must not be empty")
	}
	*s = append(*s, value)
	return nil
}

// Run executes the check and returns the process exit code. It takes its
// streams so tests can drive it without spawning a process. Writing to those
// streams is best-effort: the exit code is the contract, so a failed write to a
// closed pipe must not change the verdict.
func Run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check-test-inventory", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "origin/main", "base revision")
	head := fs.String("head", "HEAD", "head revision")
	repo := fs.String("repo", ".", "git working tree to read")
	var allow stringList
	fs.Var(&allow, "allow-removed", "test name that may disappear (repeatable)")
	// Help is handled below so it prints to stdout exactly once: flag's default
	// handler would also print the usage to stderr on -h.
	fs.Usage = func() {}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// -h and --help print the usage to stdout and succeed: asking for
			// help is not an error.
			_, _ = fmt.Fprint(stdout, usageText)
			return ExitOK
		}
		_, _ = fmt.Fprintf(stderr, "check-test-inventory: %v\n\n%s", err, usageText)
		return ExitUsage
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "check-test-inventory: unexpected argument %q\n\n%s", fs.Arg(0), usageText)
		return ExitUsage
	}

	baseInv, err := Load(*repo, *base)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "check-test-inventory: %v\n", err)
		_, _ = fmt.Fprintf(stderr, "hint: fetch the base revision first (git fetch origin) or pass --base <ref>\n")
		return ExitUsage
	}
	headInv, err := Load(*repo, *head)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "check-test-inventory: %v\n", err)
		return ExitUsage
	}

	allowed := make(map[string]bool, len(allow))
	for _, name := range allow {
		allowed[name] = true
	}
	diff := Compare(baseInv, headInv, allowed)
	WriteReport(stdout, baseInv, headInv, diff)
	if diff.Failed() {
		return ExitRemovals
	}
	return ExitOK
}
