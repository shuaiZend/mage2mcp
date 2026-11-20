// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/shuaiZend/mage2mcp/internal/magento"
	"github.com/shuaiZend/mage2mcp/internal/observability"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

// workerTestDSN returns the test database DSN scoped to the worker package's own
// schema. Database-backed test packages share one PostgreSQL instance and
// `go test ./...` runs packages in parallel, so an unscoped DSN lets another
// package's cleanup destroy this package's rows mid-test — and lets this
// package's reconcile backlog fill another package's claim window.
func workerTestDSN(t *testing.T) string {
	t.Helper()
	const schema = "test_worker"
	base := os.Getenv("MAGE2MCP_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("MAGE2MCP_TEST_DATABASE_URL is not set; skipping PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect to create test schema: %v", err)
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{schema}.Sanitize()); err != nil {
		_ = conn.Close(ctx)
		t.Fatalf("create test schema %q: %v", schema, err)
	}
	if err := conn.Close(ctx); err != nil {
		t.Fatalf("close schema-setup connection: %v", err)
	}
	parsed, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse MAGE2MCP_TEST_DATABASE_URL: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func TestImportBatchResultKeepsAllFailures(t *testing.T) {
	result := &magento.ImportResult{
		Total: 3, Created: 1, Failed: 2,
		Rows: []magento.ImportResultRow{
			{RowNumber: 2, SKU: "A", Action: "create", Status: "succeeded"},
			{RowNumber: 3, SKU: "B", Action: "create", Status: "failed", Error: "boom"},
			{RowNumber: 4, SKU: "C", Action: "update", Status: "failed", Error: "boom"},
		},
	}
	batch := importBatchResult(result)
	if batch.Total != 3 || batch.Processed != 3 || batch.Failed != 2 || len(batch.Failures) != 2 {
		t.Fatalf("batch = %+v", batch)
	}
	if batch.Failures[1].RowNumber != 4 || batch.Failures[1].SKU != "C" {
		t.Fatalf("failures = %+v", batch.Failures)
	}
}

func TestPruneImportBatchesStopsOnCancel(t *testing.T) {
	url := workerTestDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, storage.Config{URL: url, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	logger := observability.NewLogger("error", "json", io.Discard)
	runCtx, stop := context.WithCancel(context.Background())
	stop()
	done := make(chan struct{})
	go func() {
		pruneImportBatches(runCtx, store, logger)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("prune loop did not stop after cancellation")
	}
}

type fakePruner struct {
	removed int64
	err     error
	onCall  func()
	calls   int
}

// ReapImportBatches satisfies importBatchPruner; the pruning tests do not exercise
// the reaper, but the interface is shared.
func (f *fakePruner) ReapImportBatches(context.Context) (int64, error) { return 0, nil }

func (f *fakePruner) PruneImportBatches(context.Context, time.Time) (int64, error) {
	f.calls++
	if f.onCall != nil {
		f.onCall()
	}
	return f.removed, f.err
}

func (f *fakePruner) PruneJobs(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func TestPruneImportBatchesLogsAndStops(t *testing.T) {
	logger := observability.NewLogger("error", "json", io.Discard)
	for _, tc := range []struct {
		name   string
		pruner *fakePruner
	}{
		{"removed", &fakePruner{removed: 3}},
		{"error", &fakePruner{err: errors.New("boom")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			tc.pruner.onCall = cancel
			done := make(chan struct{})
			go func() {
				pruneImportBatches(ctx, tc.pruner, logger)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("prune loop did not stop")
			}
			if tc.pruner.calls != 1 {
				t.Fatalf("calls = %d", tc.pruner.calls)
			}
		})
	}
}
