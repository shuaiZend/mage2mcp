// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"testing"

	"github.com/shuaiZend/mage2mcp/internal/jobs"
)

// REL-11: a retried import must resume its open batch, not open a second one,
// and the by-job lookup must be deterministic when history exists.
func TestCreateImportBatchResumesOpenBatch(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	jobID := enqueueTestJob(t, NewJobQueue(store), "import", 2)
	first, err := store.CreateImportBatch(ctx, "store-us", jobID, "csv", []byte("sku,name\nA,Apple\n"))
	if err != nil {
		t.Fatalf("CreateImportBatch: %v", err)
	}
	second, err := store.CreateImportBatch(ctx, "store-us", jobID, "csv", []byte("sku,name\nA,Apple\n"))
	if err != nil {
		t.Fatalf("second CreateImportBatch: %v", err)
	}
	if first != second {
		t.Fatalf("a retry opened a second batch: %s then %s", first, second)
	}

	// Once the batch is terminal, a later attempt opens a new one (history is
	// not resumed).
	if err := store.FinishImportBatch(ctx, first, &jobs.ImportBatchResult{Total: 1, Created: 1}, "succeeded", ""); err != nil {
		t.Fatalf("FinishImportBatch: %v", err)
	}
	third, err := store.CreateImportBatch(ctx, "store-us", jobID, "csv", []byte("sku,name\nA,Apple\n"))
	if err != nil {
		t.Fatalf("CreateImportBatch after completion: %v", err)
	}
	if third == first {
		t.Fatal("a terminal batch was resumed; a new attempt must open its own")
	}

	// The by-job lookup returns the newest deterministically.
	got, err := store.GetImportBatchByJob(ctx, jobID, "store-us", "system")
	if err != nil {
		t.Fatalf("GetImportBatchByJob: %v", err)
	}
	if got == nil || got.ID != third {
		t.Fatalf("GetImportBatchByJob = %+v, want the newest batch %s", got, third)
	}
}
