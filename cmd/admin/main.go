// SPDX-License-Identifier: Apache-2.0

// Command admin is the human approval surface for high-risk operations.
//
// It talks to the same PostgreSQL store as the gateway. It is intentionally a
// separate binary and must be run by a human operator, never by an agent.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/shuaiZend/mage2mcp/internal/core/config"
	"github.com/shuaiZend/mage2mcp/internal/security/audit"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
	"github.com/shuaiZend/mage2mcp/internal/security/jwt"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "mage2mcp-admin:", err)
		os.Exit(1)
	}
}

// run is the testable body of the command. Arguments and output are injected so
// the usage contract can be exercised without a database or a child process.
func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("mage2mcp-admin", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var configPath, as, tokenFile string
	flags.StringVar(&configPath, "config", "config.yaml", "path to the YAML configuration")
	flags.StringVar(&as, "as", "", "approver identity; only used when authentication is disabled")
	flags.StringVar(&tokenFile, "token-file", "",
		"file holding the approver's bearer token; required for approve when auth is enabled")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if flags.NArg() < 1 {
		return fmt.Errorf("usage: mage2mcp-admin [-config path] [-as identity] pending|approve <id>")
	}
	// Validate the request before touching the store. Opening the pool first made a
	// typo cost a full connect timeout, and made the usage errors untestable.
	command := flags.Arg(0)
	switch command {
	case "pending":
	case "approve":
		if flags.NArg() < 2 {
			return fmt.Errorf("approve requires a confirmation id")
		}
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	// Resolve the approver before opening the store: with authentication enabled
	// the identity comes from the verified token, so -as is optional and a
	// missing token is reported as such (not as a missing -as flag).
	var approver, approverClass string
	if command == "approve" {
		verifier, err := adminVerifierForRun(cfg)
		if err != nil {
			return err
		}
		token, err := readApproverToken(tokenFile)
		if err != nil {
			return err
		}
		approver, approverClass, err = resolveApprover(context.Background(), verifier, token, as, stderr)
		if err != nil {
			return err
		}
	}
	if cfg.Database.URL == "" {
		return fmt.Errorf("database.url is required: approvals need the shared durable store")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, storage.Config{URL: cfg.Database.URL, MaxConns: cfg.Database.MaxConns})
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		return err
	}
	svc, err := confirm.NewService(storage.NewConfirmStore(store), confirm.DefaultTTL)
	if err != nil {
		return err
	}

	switch command {
	case "pending":
		requests, err := svc.Pending(ctx)
		if err != nil {
			return err
		}
		if len(requests) == 0 {
			if _, err := fmt.Fprintln(stdout, "no pending confirmations"); err != nil {
				return fmt.Errorf("write output: %w", err)
			}
			return nil
		}
		// The approver must see what is being approved, on which instance, and
		// until when. WritePending renders a bounded, sanitised argument summary.
		return confirm.WritePending(stdout, requests)
	case "approve":
		id := flags.Arg(1)
		request, err := findPending(ctx, svc, id)
		if err != nil {
			return err
		}
		// U-76: the verified identity class decides whether this identity may be an
		// approver of record at all. A service/agent token is refused here.
		if err := svc.ApproveAs(ctx, id, approver, approverClass); err != nil {
			return err
		}
		// SEC-04: an approval used to leave no audit trail at all — only a hash on
		// the confirmation row. Record who approved what, on which instance.
		if err := recordApproval(ctx, storage.NewAuditRecorder(store), request, approver); err != nil {
			return fmt.Errorf("approved, but the audit row could not be written: %w", err)
		}
		if _, err := fmt.Fprintf(stdout, "approved %s by %s\n", id, approver); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q", flags.Arg(0))
	}
}

// findPending returns the pending confirmation with the given id.
//
// Looking it up first means an unknown or already-resolved id fails with a clear
// message instead of a storage error, and gives the audit row the tool and instance
// it needs.
func findPending(ctx context.Context, svc *confirm.Service, id string) (*confirm.Request, error) {
	requests, err := svc.Pending(ctx)
	if err != nil {
		return nil, err
	}
	for _, request := range requests {
		if request.ID == id {
			return request, nil
		}
	}
	return nil, fmt.Errorf("no pending confirmation with id %s", id)
}

// approvalAuditor is the audit sink an approval writes to.
type approvalAuditor interface {
	Record(ctx context.Context, e audit.Event) error
}

// recordApproval writes the audit row for one approval.
//
// With authentication enabled the approver identity is the verified token subject;
// with authentication disabled it is the operator-supplied -as value, labelled as
// self-asserted by resolveApprover. The decision is recorded with the tool, instance
// and scope it applied to.
func recordApproval(ctx context.Context, auditor approvalAuditor, request *confirm.Request, approver string) error {
	if auditor == nil || request == nil {
		return nil
	}
	return auditor.Record(ctx, audit.Event{
		Subject:    approver,
		Tool:       request.Tool,
		Instance:   request.Instance,
		Scope:      request.Scope,
		Decision:   "approve",
		Phase:      "approval",
		State:      "approved",
		OccurredAt: time.Now().UTC(),
	})
}

// tokenVerifier is the subset of jwt.Verifier an approval needs.
type tokenVerifier interface {
	Verify(ctx context.Context, rawToken string) (*auth.Identity, error)
}

// adminVerifierForRun is adminVerifier, replaceable in tests so the run-level
// identity path can be exercised without a live JWKS endpoint.
var adminVerifierForRun = adminVerifier

// adminVerifier builds the verifier the gateway uses, so an approval is checked
// against the same identity provider. It returns nil when authentication is
// disabled, which is the only case where a self-asserted identity is accepted.
func adminVerifier(cfg config.Config) (tokenVerifier, error) {
	if !cfg.Auth.Enabled {
		return nil, nil
	}
	verifier, err := jwt.NewVerifier(jwt.Config{
		Issuer:   cfg.Auth.Issuer,
		Audience: cfg.Auth.Audience,
		JWKSURL:  cfg.Auth.JWKSURL,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	return verifier, nil
}

// readApproverToken returns the approver's bearer token.
//
// The token comes from a file so it never appears in a process listing, which is
// what a flag would do.
func readApproverToken(path string) (string, error) {
	if path == "" {
		if token := strings.TrimSpace(os.Getenv("MAGE2MCP_ADMIN_TOKEN")); token != "" {
			return token, nil
		}
		return "", nil
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the operator names the file on the command line
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// resolveApprover returns the identity to record for an approval.
//
// With a verifier the subject is taken from the verified token and the -as flag
// may not contradict it. Without one the identity is whatever the operator typed,
// and that is stated on stderr rather than silently accepted as fact.
func resolveApprover(ctx context.Context, verifier tokenVerifier, token, as string, warn io.Writer) (string, string, error) {
	if verifier != nil {
		if token == "" {
			return "", "", fmt.Errorf("authentication is enabled, so an approval needs the approver's token: set MAGE2MCP_ADMIN_TOKEN or pass -token-file; the approver identity is taken from the verified token's subject")
		}
		identity, err := verifier.Verify(ctx, token)
		if err != nil {
			return "", "", fmt.Errorf("approver token rejected: %w", err)
		}
		if as != "" && as != identity.Subject {
			return "", "", fmt.Errorf("-as %q does not match the verified subject %q; the approver identity comes from the token", as, identity.Subject)
		}
		return identity.Subject, identity.Class, nil
	}
	if as == "" {
		return "", "", fmt.Errorf("-as <approver identity> is required")
	}
	if warn != nil {
		// Best effort: the warning is informational and a failure to write it must
		// not turn a valid approval into an error.
		_, _ = fmt.Fprintf(warn, "warning: authentication is disabled, so %q is a self-asserted approver identity and is not verified\n", as)
	}
	// No verifier means no class to trust; the identity is self-asserted and the
	// warning above says so.
	return as, "", nil
}
