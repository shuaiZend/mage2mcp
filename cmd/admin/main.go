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
	"time"

	"github.com/shuaiZend/mage2mcp/internal/core/config"
	"github.com/shuaiZend/mage2mcp/internal/security/audit"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
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
	var configPath, as string
	flags.StringVar(&configPath, "config", "config.yaml", "path to the YAML configuration")
	flags.StringVar(&as, "as", "", "approver identity (email or subject); required for approve")
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
		if as == "" {
			return fmt.Errorf("-as <approver identity> is required")
		}
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
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
			fmt.Fprintln(stdout, "no pending confirmations")
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
		if err := svc.Approve(ctx, id, as); err != nil {
			return err
		}
		// SEC-04: an approval used to leave no audit trail at all — only a hash on
		// the confirmation row. Record who approved what, on which instance.
		if err := recordApproval(ctx, storage.NewAuditRecorder(store), request, as); err != nil {
			return fmt.Errorf("approved, but the audit row could not be written: %w", err)
		}
		fmt.Fprintf(stdout, "approved %s by %s\n", id, as)
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
// The approver identity is still whatever the operator typed (SEC-04 is only
// partially closed: the identity is not authenticated), but the decision is now
// recorded with the tool, instance and scope it applied to.
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
