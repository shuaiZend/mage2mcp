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
		if err := svc.Approve(ctx, flags.Arg(1), as); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "approved %s by %s\n", flags.Arg(1), as)
		return nil
	default:
		return fmt.Errorf("unknown command %q", flags.Arg(0))
	}
}
