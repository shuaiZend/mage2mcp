// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/shuaiZend/mage2mcp/internal/magento"
	"github.com/shuaiZend/mage2mcp/internal/observability"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

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
	url := os.Getenv("MAGE2MCP_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("MAGE2MCP_TEST_DATABASE_URL is not set; skipping PostgreSQL integration test")
	}
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
