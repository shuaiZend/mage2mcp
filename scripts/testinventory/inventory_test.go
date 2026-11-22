// SPDX-License-Identifier: Apache-2.0

package testinventory

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTestNames(t *testing.T) {
	src := []byte(`package sample

import "testing"

func TestAlpha(t *testing.T) {}
func TestBeta_with_underscores_and_9(t *testing.T) {}
func TestMain(m *testing.M) { os.Exit(m.Run()) }
func BenchmarkNotATest(b *testing.B) {}
func FuzzNotATest(f *testing.F) {}
func ExampleNotATest() {}
func helper() {}
func (s *suite) TestMethodOnAType(t *testing.T) {}
// func TestCommentedOut(t *testing.T) {}
`)
	got := parseTestNames(src)
	want := []string{"TestAlpha", "TestBeta_with_underscores_and_9", "TestMain"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("parseTestNames = %v, want %v", got, want)
	}
}

func inventoryOf(tests ...Test) *Inventory {
	inv := &Inventory{Tests: map[string][]string{}}
	for _, test := range tests {
		k := key(test.Dir, test.Name)
		inv.Tests[k] = appendUnique(inv.Tests[k], test.File)
	}
	return inv
}

func TestCompareReportsRemoval(t *testing.T) {
	base := inventoryOf(Test{Dir: "pkg", File: "pkg/a_test.go", Name: "TestKeep"}, Test{Dir: "pkg", File: "pkg/a_test.go", Name: "TestGone"})
	head := inventoryOf(Test{Dir: "pkg", File: "pkg/a_test.go", Name: "TestKeep"})
	diff := Compare(base, head, nil)
	if !diff.Failed() {
		t.Fatal("a removed test must fail the check")
	}
	if len(diff.Removed) != 1 || diff.Removed[0].Name != "TestGone" || diff.Removed[0].File != "pkg/a_test.go" {
		t.Fatalf("removed = %+v, want TestGone in pkg/a_test.go", diff.Removed)
	}
	if len(diff.Added) != 0 || len(diff.Moved) != 0 {
		t.Fatalf("added/moved = %+v/%+v, want none", diff.Added, diff.Moved)
	}
}

func TestCompareReportsMoveNotRemoval(t *testing.T) {
	base := inventoryOf(Test{Dir: "pkg/a", File: "pkg/a/x_test.go", Name: "TestMoved"})
	head := inventoryOf(Test{Dir: "pkg/b", File: "pkg/b/y_test.go", Name: "TestMoved"})
	diff := Compare(base, head, nil)
	if diff.Failed() {
		t.Fatalf("a test that changed package is a move, not a deletion: %+v", diff.Removed)
	}
	if len(diff.Moved) != 1 || diff.Moved[0].From != "pkg/a" || diff.Moved[0].To != "pkg/b" {
		t.Fatalf("moved = %+v", diff.Moved)
	}
}

func TestCompareAllowsRemoval(t *testing.T) {
	base := inventoryOf(Test{Dir: "pkg", File: "pkg/a_test.go", Name: "TestLegacy"})
	head := inventoryOf()
	diff := Compare(base, head, map[string]bool{"TestLegacy": true})
	if diff.Failed() {
		t.Fatalf("an allowed removal must not fail: %+v", diff.Removed)
	}
	if len(diff.Allowed) != 1 || diff.Allowed[0].Name != "TestLegacy" {
		t.Fatalf("allowed = %+v", diff.Allowed)
	}
}

// TestRunCatchesWholeFileRewrite is the incident this gate exists for: one
// agent rewrote a *_test.go file wholesale and dropped tests another agent had
// appended. The file still compiled and the suite still passed.
func TestRunCatchesWholeFileRewrite(t *testing.T) {
	repo := initRepo(t)
	first := commitFiles(t, repo, "add tests", map[string]string{
		"internal/magento/order_test.go": `package magento

import "testing"

func TestGetOrderReturnsFulfilmentCounters(t *testing.T) {}
func TestGetOrderReadsShippingAddressFromAssignments(t *testing.T) {}
func TestGetOrderPrefersTopLevelShippingAddress(t *testing.T) {}
func TestGetOrderRejectsInvalidID(t *testing.T) {}
`,
	})
	// The rewrite keeps the pre-existing test and drops the three appended ones.
	second := commitFiles(t, repo, "rewrite order_test.go", map[string]string{
		"internal/magento/order_test.go": `package magento

import "testing"

func TestGetOrderRejectsInvalidID(t *testing.T) {}
`,
	})

	var stdout, stderr bytes.Buffer
	code := Run([]string{"--repo", repo, "--base", first, "--head", second}, &stdout, &stderr)
	if code != ExitRemovals {
		t.Fatalf("exit = %d, want %d (stderr %s)", code, ExitRemovals, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"TestGetOrderReturnsFulfilmentCounters",
		"TestGetOrderReadsShippingAddressFromAssignments",
		"TestGetOrderPrefersTopLevelShippingAddress",
		"internal/magento/order_test.go",
		"FAIL: 3 test function(s) disappeared",
		"--allow-removed",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("report does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "TestGetOrderRejectsInvalidID\t") {
		t.Fatalf("a surviving test was reported as removed:\n%s", out)
	}
}

func TestRunAllowsDeliberateRemoval(t *testing.T) {
	repo := initRepo(t)
	first := commitFiles(t, repo, "add tests", map[string]string{
		"pkg/a_test.go": "package pkg\n\nimport \"testing\"\n\nfunc TestLegacy(t *testing.T) {}\n",
	})
	second := commitFiles(t, repo, "drop legacy", map[string]string{
		"pkg/a_test.go": "package pkg\n\nimport \"testing\"\n\nfunc TestCurrent(t *testing.T) {}\n",
	})
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--repo", repo, "--base", first, "--head", second,
		"--allow-removed", "TestLegacy"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %s)\n%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "allowed removals: 1") {
		t.Fatalf("report does not record the allowed removal:\n%s", stdout.String())
	}
}

func TestRunPassesWhenNothingDisappeared(t *testing.T) {
	repo := initRepo(t)
	first := commitFiles(t, repo, "add tests", map[string]string{
		"pkg/a_test.go": "package pkg\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n",
	})
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--repo", repo, "--base", first, "--head", first}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %s)", code, stderr.String())
	}
}

func TestRunHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--help"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "Usage:") || stderr.Len() != 0 {
		t.Fatalf("help must go to stdout once: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunUnknownRevision(t *testing.T) {
	repo := initRepo(t)
	commitFiles(t, repo, "add tests", map[string]string{
		"pkg/a_test.go": "package pkg\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n",
	})
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--repo", repo, "--base", "does-not-exist", "--head", "HEAD"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "git fetch origin") {
		t.Fatalf("stderr does not hint at fetching the base: %q", stderr.String())
	}
}

// TestLoadCountsBuildTaggedFiles keeps the qa-tagged suite in the inventory: a
// deletion there is just as silent as anywhere else.
func TestLoadCountsBuildTaggedFiles(t *testing.T) {
	repo := initRepo(t)
	commit := commitFiles(t, repo, "add qa test", map[string]string{
		"test/qa/money_test.go": "//go:build qa\n\npackage qa\n\nimport \"testing\"\n\nfunc TestMoneyPath(t *testing.T) {}\n",
	})
	inv, err := Load(repo, commit)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Count() != 1 {
		t.Fatalf("count = %d, want 1: %+v", inv.Count(), inv.Tests)
	}
	if _, ok := inv.Tests[key("test/qa", "TestMoneyPath")]; !ok {
		t.Fatalf("qa-tagged test missing from inventory: %+v", inv.Tests)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	return dir
}

// commitFiles writes files (path -> content) and commits them, returning the
// commit sha. Each call is one commit, so the test can diff any two of them.
func commitFiles(t *testing.T, repo, message string, files map[string]string) string {
	t.Helper()
	for name, content := range files {
		full := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.com",
		"-c", "commit.gpgsign=false", "commit", "-q", "-m", message)
	return strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
}

func runGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
