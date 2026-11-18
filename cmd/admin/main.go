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
	"os"
	"text/tabwriter"
	"time"

	"github.com/shuaiZend/mage2mcp/internal/core/config"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mage2mcp-admin:", err)
		os.Exit(1)
	}
}

func run() error {
	var configPath, as string
	flag.StringVar(&configPath, "config", "config.yaml", "path to the YAML configuration")
	flag.StringVar(&as, "as", "", "approver identity (email or subject); required for approve")
	flag.Parse()

	if flag.NArg() < 1 {
		return fmt.Errorf("usage: mage2mcp-admin [-config path] [-as identity] pending|approve <id>")
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

	switch flag.Arg(0) {
	case "pending":
		requests, err := svc.Pending(ctx)
		if err != nil {
			return err
		}
		if len(requests) == 0 {
			fmt.Println("no pending confirmations")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tTOOL\tSUMMARY\tEXPIRES")
		for _, r := range requests {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.ID, r.Tool, r.Summary, r.ExpiresAt.Format(time.RFC3339))
		}
		return w.Flush()
	case "approve":
		if flag.NArg() < 2 {
			return fmt.Errorf("approve requires a confirmation id")
		}
		if as == "" {
			return fmt.Errorf("-as <approver identity> is required")
		}
		if err := svc.Approve(ctx, flag.Arg(1), as); err != nil {
			return err
		}
		fmt.Printf("approved %s by %s\n", flag.Arg(1), as)
		return nil
	default:
		return fmt.Errorf("unknown command %q", flag.Arg(0))
	}
}
