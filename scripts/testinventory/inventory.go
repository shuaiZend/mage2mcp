// SPDX-License-Identifier: Apache-2.0

// Package testinventory compares the set of top-level Go test functions between
// two git revisions.
//
// The gate exists because a green test run cannot prove that the tests which
// should exist still do. One agent rewrote a *_test.go file wholesale and
// dropped three tests another agent had appended; the full suite stayed green,
// because it runs the tests that remain. A deleted test leaves valid Go code
// that compiles and passes, so nothing turns red. Comparing the names between
// two revisions is the only check that notices the deletion.
//
// The package is deliberately small and git-only: it reads committed trees, so
// an untracked local test file is out of scope (commit it to have it counted).
package testinventory

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
)

// testFuncRE matches a top-level test function declaration. The anchor at
// column zero is what excludes methods (which have a receiver between func and
// the name) and indented declarations inside a function body.
var testFuncRE = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\s*\(`)

// Test is one top-level test function at one path.
type Test struct {
	Dir  string
	File string
	Name string
}

func (t Test) String() string { return t.File + "\t" + t.Name }

// Move is a test that kept its name but changed package directory.
type Move struct {
	Name string
	From string
	To   string
}

// Diff is the difference between two inventories.
type Diff struct {
	Added   []Test
	Removed []Test
	// Allowed holds removals the caller explicitly accepted with
	// --allow-removed. They are reported, but they do not fail the check.
	Allowed []Test
	Moved   []Move
}

// Failed reports whether any removal was not explicitly allowed.
func (d Diff) Failed() bool { return len(d.Removed) > 0 }

// Inventory is the set of test functions declared at one revision.
type Inventory struct {
	// Ref is the revision the caller asked for (a branch, tag or sha).
	Ref string
	// Commit is the resolved commit sha.
	Commit string
	// Tests maps a package directory and test name to the files declaring it.
	Tests map[string][]string
	// Files is the number of *_test.go files that were read.
	Files int
}

// Count returns the number of distinct test functions.
func (i *Inventory) Count() int {
	if i == nil {
		return 0
	}
	return len(i.Tests)
}

func key(dir, name string) string { return dir + "\x00" + name }

// parseTestNames returns the top-level Test functions declared in src.
func parseTestNames(src []byte) []string {
	matches := testFuncRE.FindAllSubmatch(src, -1)
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, string(m[1]))
	}
	return names
}

// Load reads every *_test.go in ref's tree and extracts the top-level Test
// functions it declares.
func Load(repoDir, ref string) (*Inventory, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, fmt.Errorf("testinventory: revision is empty")
	}
	commit, err := git(repoDir, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("testinventory: resolve %q: %w", ref, err)
	}
	commit = strings.TrimSpace(commit)
	files, err := git(repoDir, "ls-tree", "-r", "--name-only", commit)
	if err != nil {
		return nil, fmt.Errorf("testinventory: list tree %s: %w", commit, err)
	}
	inv := &Inventory{Ref: ref, Commit: commit, Tests: map[string][]string{}}
	for _, file := range strings.Split(strings.TrimSpace(files), "\n") {
		if file == "" || !strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := git(repoDir, "show", commit+":"+file)
		if err != nil {
			return nil, fmt.Errorf("testinventory: read %s at %s: %w", file, commit, err)
		}
		inv.Files++
		dir := path.Dir(file)
		for _, name := range parseTestNames([]byte(src)) {
			k := key(dir, name)
			inv.Tests[k] = appendUnique(inv.Tests[k], file)
		}
	}
	return inv, nil
}

func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	list = append(list, value)
	sort.Strings(list)
	return list
}

// Compare returns the tests added and removed between base and head. A test
// that moved to another package keeps its name, so it is reported as a move
// rather than as a deletion plus an addition: the coverage did not vanish.
func Compare(base, head *Inventory, allow map[string]bool) Diff {
	var d Diff
	removedByName := map[string][]Test{}
	for k, files := range base.Tests {
		if _, ok := head.Tests[k]; ok {
			continue
		}
		dir, name := splitKey(k)
		for _, file := range files {
			removedByName[name] = append(removedByName[name], Test{Dir: dir, File: file, Name: name})
		}
	}
	addedByName := map[string][]Test{}
	for k, files := range head.Tests {
		if _, ok := base.Tests[k]; ok {
			continue
		}
		dir, name := splitKey(k)
		for _, file := range files {
			addedByName[name] = append(addedByName[name], Test{Dir: dir, File: file, Name: name})
		}
	}
	for name, gone := range removedByName {
		if moved, ok := addedByName[name]; ok {
			sort.Slice(gone, func(i, j int) bool { return gone[i].Dir < gone[j].Dir })
			sort.Slice(moved, func(i, j int) bool { return moved[i].Dir < moved[j].Dir })
			d.Moved = append(d.Moved, Move{Name: name, From: gone[0].Dir, To: moved[0].Dir})
			delete(addedByName, name)
			continue
		}
		for _, t := range gone {
			if allow[t.Name] {
				d.Allowed = append(d.Allowed, t)
				continue
			}
			d.Removed = append(d.Removed, t)
		}
	}
	for _, added := range addedByName {
		d.Added = append(d.Added, added...)
	}
	sortTests(d.Added)
	sortTests(d.Removed)
	sortTests(d.Allowed)
	sort.Slice(d.Moved, func(i, j int) bool { return d.Moved[i].Name < d.Moved[j].Name })
	return d
}

func splitKey(k string) (dir, name string) {
	dir, name, _ = strings.Cut(k, "\x00")
	return dir, name
}

func sortTests(list []Test) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].File != list[j].File {
			return list[i].File < list[j].File
		}
		return list[i].Name < list[j].Name
	})
}

// WriteReport renders the diff. The report always names the file a lost test
// lived in: a bare test name is not enough to find what was dropped.
//
// Report writes are best-effort on purpose: the verdict is the process exit
// code, so a broken stdout (a closed pipe, a full disk behind a redirect) must
// not turn a detected removal into a crash. The buffered writer's Flush error is
// ignored for the same reason.
func WriteReport(w io.Writer, base, head *Inventory, d Diff) {
	bw := bufio.NewWriter(w)
	defer func() { _ = bw.Flush() }()
	_, _ = fmt.Fprintf(bw, "test inventory: %s (%s) -> %s (%s)\n",
		base.Ref, shortSHA(base.Commit), head.Ref, shortSHA(head.Commit))
	_, _ = fmt.Fprintf(bw, "tests: %d -> %d  (added %d, removed %d, moved %d, allowed %d)\n",
		base.Count(), head.Count(), len(d.Added), len(d.Removed), len(d.Moved), len(d.Allowed))
	writeSection(bw, "added", "+", d.Added)
	writeSection(bw, "removed", "-", d.Removed)
	if len(d.Allowed) > 0 {
		writeSection(bw, "allowed removals", "-", d.Allowed)
	}
	if len(d.Moved) > 0 {
		_, _ = fmt.Fprintln(bw, "moved:")
		for _, m := range d.Moved {
			_, _ = fmt.Fprintf(bw, "  ~ %s  %s -> %s\n", m.Name, m.From, m.To)
		}
	}
	if d.Failed() {
		_, _ = fmt.Fprintf(bw, "\nFAIL: %d test function(s) disappeared between %s and %s\n",
			len(d.Removed), base.Ref, head.Ref)
		_, _ = fmt.Fprintln(bw, "A test that no longer exists cannot fail, so the suite stays green while the")
		_, _ = fmt.Fprintln(bw, "coverage it provided is gone. Restore the test, or — if the deletion is")
		_, _ = fmt.Fprintln(bw, "deliberate — rerun with --allow-removed <TestName> for each one.")
	}
}

func writeSection(w io.Writer, title, marker string, tests []Test) {
	_, _ = fmt.Fprintf(w, "%s: %d\n", title, len(tests))
	for _, t := range tests {
		_, _ = fmt.Fprintf(w, "  %s %s\t%s\n", marker, t.File, t.Name)
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// git runs one git command and returns its stdout.
func git(repoDir string, args ...string) (string, error) {
	full := append([]string{"-C", repoDir, "-c", "core.quotepath=false"}, args...)
	// G204: the executable is a fixed literal and the arguments are git
	// subcommands plus revisions and repository-relative paths supplied by the
	// operator on the command line. Nothing here is passed through a shell.
	cmd := exec.Command("git", full...) //nolint:gosec,noctx // G204: see above; noctx: a local read-only git read finishes in milliseconds, so there is no long-running work to cancel.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr = strings.TrimSpace(string(exitErr.Stderr))
		}
		if stderr != "" {
			return "", fmt.Errorf("%w: %s", err, stderr)
		}
		return "", err
	}
	return string(out), nil
}
