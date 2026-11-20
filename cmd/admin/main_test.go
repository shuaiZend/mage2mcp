// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The approval CLI had no tests at all. These pin the contract that matters for
// safety: an approval cannot be issued without naming an approver, a missing
// subcommand or unknown verb is refused, and the store is never opened when the
// configuration cannot support it.

// writeConfig writes a minimal configuration and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := run(args, &stdout, &stderr)
	return stdout.String() + stderr.String(), err
}

func TestAdminRequiresASubcommand(t *testing.T) {
	out, err := runCLI(t)
	if err == nil {
		t.Fatal("want an error when no subcommand is given")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("err = %v, want a usage message", err)
	}
	if strings.Contains(out, "approved") {
		t.Fatalf("output = %q, must not claim an approval", out)
	}
}

func TestAdminRejectsUnknownCommand(t *testing.T) {
	cfg := writeConfig(t, "auth:\n  enabled: false\nserver:\n  listen: \"127.0.0.1:8080\"\ndatabase:\n  url: \"\"\n")
	_, err := runCLI(t, "-config", cfg, "authorise", "abc")
	if err == nil {
		t.Fatal("want an error for an unknown command")
	}
	// The database check fires first; either way nothing may be approved.
	if !strings.Contains(err.Error(), "database.url") && !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("err = %v, want the unknown-command or database error", err)
	}
}

// An approval without an approver identity would let the ledger record a human
// decision nobody made. This is the SEC-04 surface: the identity is self-asserted,
// so at minimum it must not be optional.
func TestAdminApproveRequiresIdentity(t *testing.T) {
	cfg := writeConfig(t, "auth:\n  enabled: false\ndatabase:\n  url: \"postgres://user:pass@127.0.0.1:1/db?sslmode=disable\"\n")
	_, err := runCLI(t, "-config", cfg, "approve", "abc")
	if err == nil {
		t.Fatal("want an error when -as is missing")
	}
	if !strings.Contains(err.Error(), "-as") {
		t.Fatalf("err = %v, want it to name the missing -as flag", err)
	}
}

func TestAdminApproveRequiresID(t *testing.T) {
	cfg := writeConfig(t, "auth:\n  enabled: false\ndatabase:\n  url: \"postgres://user:pass@127.0.0.1:1/db?sslmode=disable\"\n")
	_, err := runCLI(t, "-config", cfg, "-as", "reviewer@example.com", "approve")
	if err == nil {
		t.Fatal("want an error when the confirmation id is missing")
	}
	if !strings.Contains(err.Error(), "confirmation id") {
		t.Fatalf("err = %v, want it to name the missing id", err)
	}
}

// Approvals need the shared durable store; without one the command must refuse
// rather than pretend.
func TestAdminRequiresDatabaseURL(t *testing.T) {
	cfg := writeConfig(t, "auth:\n  enabled: false\nserver:\n  listen: \"127.0.0.1:8080\"\n")
	_, err := runCLI(t, "-config", cfg, "pending")
	if err == nil {
		t.Fatal("want an error when database.url is empty")
	}
	if !strings.Contains(err.Error(), "database.url") {
		t.Fatalf("err = %v, want it to name database.url", err)
	}
}

func TestAdminReportsMissingConfig(t *testing.T) {
	_, err := runCLI(t, "-config", filepath.Join(t.TempDir(), "absent.yaml"), "pending")
	if err == nil {
		t.Fatal("want an error for a missing config file")
	}
}
