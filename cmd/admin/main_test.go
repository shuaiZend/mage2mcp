// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shuaiZend/mage2mcp/internal/core/config"
	"github.com/shuaiZend/mage2mcp/internal/security/audit"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
	"github.com/shuaiZend/mage2mcp/internal/storage"
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

// SEC-04: an approval used to leave no audit trail beyond a hash on the
// confirmation row. Every approval now records who approved what, on which
// instance and at which scope.
func TestRecordApprovalWritesAuditRow(t *testing.T) {
	fake := &fakeAuditor{}
	request := &confirm.Request{
		ID: "conf-1", Tool: "magento.product.delete", Instance: "store-us",
		Scope: "write", Requester: "agent-a",
	}
	if err := recordApproval(context.Background(), fake, request, "reviewer@example.com"); err != nil {
		t.Fatalf("recordApproval: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("audit calls = %d, want 1", fake.calls)
	}
	got := fake.last
	if got.Subject != "reviewer@example.com" || got.Tool != "magento.product.delete" ||
		got.Instance != "store-us" || got.Scope != "write" {
		t.Fatalf("event = %+v, want the approval recorded with its tool and instance", got)
	}
	if got.Decision != "approve" || got.Phase != "approval" || got.State != "approved" {
		t.Fatalf("event = %+v, want decision/phase/state set", got)
	}
}

// A nil sink or request is not an error: the approval itself already succeeded.
func TestRecordApprovalToleratesMissingSink(t *testing.T) {
	if err := recordApproval(context.Background(), nil, &confirm.Request{}, "x"); err != nil {
		t.Fatalf("recordApproval with no sink: %v", err)
	}
	if err := recordApproval(context.Background(), &fakeAuditor{}, nil, "x"); err != nil {
		t.Fatalf("recordApproval with no request: %v", err)
	}
}

type fakeAuditor struct {
	calls int
	last  audit.Event
}

func (f *fakeAuditor) Record(_ context.Context, e audit.Event) error {
	f.calls++
	f.last = e
	return nil
}

// SEC-04: with authentication configured the approver proves who they are with the
// same verifier the gateway uses, so the recorded identity is not self-asserted.
func TestResolveApproverUsesTheVerifiedSubject(t *testing.T) {
	verifier := &fakeVerifier{subject: "reviewer@example.com"}
	approver, class, err := resolveApprover(context.Background(), verifier, "token-abc", "", io.Discard)
	if err != nil {
		t.Fatalf("resolveApprover: %v", err)
	}
	if approver != "reviewer@example.com" {
		t.Fatalf("approver = %q, want the verified subject", approver)
	}
	// U-76: the verified class travels with the subject so the approval can be
	// refused for a non-human identity.
	if class != "human" {
		t.Fatalf("class = %q, want human", class)
	}
	if verifier.calls != 1 {
		t.Fatalf("verifier calls = %d, want 1", verifier.calls)
	}
}

// A -as that contradicts the token is refused: silently preferring one or the other
// would let an operator record an identity the token does not support.
func TestResolveApproverRejectsContradictingFlag(t *testing.T) {
	verifier := &fakeVerifier{subject: "reviewer@example.com"}
	if _, _, err := resolveApprover(context.Background(), verifier, "token-abc", "someone-else", io.Discard); err == nil {
		t.Fatal("want an error when -as contradicts the verified subject")
	}
}

// With authentication enabled an approval without a token is refused rather than
// falling back to the self-asserted string.
func TestResolveApproverRequiresTokenWhenAuthEnabled(t *testing.T) {
	verifier := &fakeVerifier{subject: "reviewer@example.com"}
	_, _, err := resolveApprover(context.Background(), verifier, "", "reviewer@example.com", io.Discard)
	if err == nil {
		t.Fatal("want an error when auth is enabled and no token is supplied")
	}
	if !strings.Contains(err.Error(), "MAGE2MCP_ADMIN_TOKEN") {
		t.Fatalf("err = %v, want it to name how to supply the token", err)
	}
}

// Without an identity provider the identity is still accepted, but the fallback is
// labelled as unverified instead of being presented as fact.
func TestResolveApproverWarnsWhenUnverified(t *testing.T) {
	var warn bytes.Buffer
	approver, class, err := resolveApprover(context.Background(), nil, "", "operator", &warn)
	if err != nil {
		t.Fatalf("resolveApprover: %v", err)
	}
	if approver != "operator" {
		t.Fatalf("approver = %q", approver)
	}
	// No verifier means no class to trust: the caller must not present the
	// self-asserted identity as a verified human.
	if class != "" {
		t.Fatalf("class = %q, want empty for a self-asserted approver", class)
	}
	if !strings.Contains(warn.String(), "not verified") {
		t.Fatalf("warning = %q, want it to say the identity is unverified", warn.String())
	}
}

type fakeVerifier struct {
	subject string
	class   string
	err     error
	calls   int
}

func (f *fakeVerifier) Verify(context.Context, string) (*auth.Identity, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	class := f.class
	if class == "" {
		class = auth.ClassHuman
	}
	return &auth.Identity{Subject: f.subject, Class: class}, nil
}

// TestRecordApprovalPersistsUnderAuditConstraints is the SEM-03 regression: the
// approval row has to satisfy the database's decision/phase/state constraints.
// Before migration 000009 the insert failed with
// `violates check constraint "audit_events_decision_check"`, so an approval left
// no audit trail at all. Rolling the migration back fails this test.
//
// It needs a disposable PostgreSQL, like the internal/storage integration tests.
func TestRecordApprovalPersistsUnderAuditConstraints(t *testing.T) {
	url := os.Getenv("MAGE2MCP_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("MAGE2MCP_TEST_DATABASE_URL is not set; skipping PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, storage.Config{URL: url, MaxConns: 2})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	recorder := storage.NewAuditRecorder(store)
	request := &confirm.Request{
		ID: "conf-approval-audit", Tool: "magento.product.delete",
		Instance: "store-us", Scope: "destructive", Requester: "agent-a",
	}
	if err := recordApproval(ctx, recorder, request, "reviewer@example.com"); err != nil {
		t.Fatalf("recordApproval against the real schema: %v", err)
	}

	rows, err := store.ListAudit(ctx, "magento.product.delete", 20)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	for _, row := range rows {
		if row.Phase != "approval" {
			continue
		}
		if row.Decision != "approve" {
			t.Fatalf("decision = %q, want approve", row.Decision)
		}
		if row.State != "approved" {
			t.Fatalf("state = %q, want approved", row.State)
		}
		if row.Instance != "store-us" {
			t.Fatalf("instance = %q, want store-us", row.Instance)
		}
		return
	}
	t.Fatalf("no audit row with phase=approval was written; rows = %+v", rows)
}

// With authentication enabled, -as is not the identity source: a missing token
// must be reported as a token problem, not as a missing -as flag. Before t80 the
// run() pre-check returned "-as is required" before the config was even loaded.
func TestAdminApproveAuthEnabledReportsTokenNotAS(t *testing.T) {
	cfg := writeConfig(t, `auth:
  enabled: true
  issuer: "https://local.test"
  audience: "mage2mcp"
  jwks_url: "https://127.0.0.1:1/jwks"
database:
  url: ""
`)
	out, err := runCLI(t, "-config", cfg, "approve", "abc")
	if err == nil {
		t.Fatal("want an error when auth is enabled and no token is supplied")
	}
	if strings.Contains(err.Error(), "-as") {
		t.Fatalf("err = %v, must not require -as when auth is enabled", err)
	}
	if !strings.Contains(err.Error(), "token") || !strings.Contains(err.Error(), "subject") {
		t.Fatalf("err = %v, want it to say the identity comes from the token subject", err)
	}
	if strings.Contains(out, "approved") {
		t.Fatalf("output = %q, must not claim an approval", out)
	}
}

// The token's verified subject is the approver identity when -as is omitted.
func TestAdminApproveTokenNoASUsesSubject(t *testing.T) {
	verifier := &fakeVerifier{subject: "reviewer@example.com"}
	approver, _, err := resolveApprover(context.Background(), verifier, "token-abc", "", io.Discard)
	if err != nil {
		t.Fatalf("resolveApprover: %v", err)
	}
	if approver != "reviewer@example.com" {
		t.Fatalf("approver = %q, want token subject", approver)
	}
}

// A contradicting -as is still refused even though it is optional.
func TestAdminApproveTokenContradictingASIsRejected(t *testing.T) {
	verifier := &fakeVerifier{subject: "reviewer@example.com"}
	_, _, err := resolveApprover(context.Background(), verifier, "token-abc", "someone-else", io.Discard)
	if err == nil {
		t.Fatal("want a contradiction error")
	}
	if !strings.Contains(err.Error(), "verified subject") || !strings.Contains(err.Error(), "token") {
		t.Fatalf("err = %v, want it to say the token is authoritative", err)
	}
}

// A run-level proof that a token alone (no -as) passes the identity stage: with
// a verifier that returns a subject and no database URL, the next error is the
// database one, not "-as is required".
func TestAdminApproveTokenNoASPassesIdentityStage(t *testing.T) {
	previous := adminVerifierForRun
	adminVerifierForRun = func(config.Config) (tokenVerifier, error) {
		return &fakeVerifier{subject: "reviewer@example.com"}, nil
	}
	t.Cleanup(func() { adminVerifierForRun = previous })

	cfg := writeConfig(t, `auth:
  enabled: true
  issuer: "https://local.test"
  audience: "mage2mcp"
  jwks_url: "https://127.0.0.1:1/jwks"
database:
  url: ""
`)
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("token-abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "-config", cfg, "-token-file", tokenPath, "approve", "abc")
	if err == nil {
		t.Fatal("want the database error after identity resolution")
	}
	if strings.Contains(err.Error(), "-as") {
		t.Fatalf("err = %v, must not require -as with a token", err)
	}
	if !strings.Contains(err.Error(), "database.url") {
		t.Fatalf("err = %v, want the next failure to be database.url", err)
	}
	if strings.Contains(out, "approved") {
		t.Fatalf("output = %q, must not claim an approval", out)
	}
}
