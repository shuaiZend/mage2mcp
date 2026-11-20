//go:build probe

// SPDX-License-Identifier: Apache-2.0

// PostgreSQL-side measurements for docs/audit/performance-review.md.
//
// These run only when MAGE2MCP_TEST_DATABASE_URL points at a disposable
// database, matching internal/storage's own integration-test convention:
//
//	MAGE2MCP_TEST_DATABASE_URL=postgres://mage2mcp:mage2mcp-dev@127.0.0.1:55440/mage2mcp_perf?sslmode=disable \
//	  go test ./docs/audit/perf-probe/ -run TestPerfDB -v -count=1
package perfprobe

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/security/audit"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

func perfStore(t *testing.T) *storage.Store {
	t.Helper()
	url := os.Getenv("MAGE2MCP_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("MAGE2MCP_TEST_DATABASE_URL is not set; skipping PostgreSQL probe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, storage.Config{URL: url, MaxConns: 4})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return store
}

// TestPerfDBClaimPlan checks that the job-claim query uses the partial claim
// index rather than scanning the queue, at a realistic backlog size.
func TestPerfDBClaimPlan(t *testing.T) {
	store := perfStore(t)
	ctx := context.Background()
	queue := storage.NewJobQueue(store)

	const backlog = 20000
	for i := 0; i < backlog; i++ {
		if _, err := queue.Enqueue(ctx, jobs.Task{
			Kind: jobs.TypeNoop, Queue: "default",
			Payload: []byte(`{"message":"perf"}`), MaxAttempts: 1,
		}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	start := time.Now()
	claimed := 0
	for {
		task, err := queue.Claim(ctx, []string{"critical", "default", "import", "report"}, 2*time.Minute)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if task == nil {
			break
		}
		claimed++
	}
	elapsed := time.Since(start)
	t.Logf("PROBE db claim backlog=%d claimed=%d total=%s per_claim=%s",
		backlog, claimed, elapsed.Round(time.Millisecond),
		(elapsed / time.Duration(claimed)).Round(time.Microsecond))
}

// TestPerfDBImportFailureWrite measures the per-row INSERT loop in
// FinishImportBatch, which writes one round-trip per failed import row.
func TestPerfDBImportFailureWrite(t *testing.T) {
	store := perfStore(t)
	ctx := context.Background()

	for _, failures := range []int{100, 1000} {
		failures := failures
		batchID, err := store.CreateImportBatch(ctx, "inst-perf", "", "csv", []byte("sku,name\nA,1\n"))
		if err != nil {
			t.Fatalf("CreateImportBatch: %v", err)
		}
		result := &jobs.ImportBatchResult{Total: failures, Processed: failures, Failed: failures}
		for i := 0; i < failures; i++ {
			result.Failures = append(result.Failures, jobs.ImportBatchFailure{
				RowNumber: i + 1, SKU: fmt.Sprintf("SKU-%05d", i), Action: "create", Error: "validation_failed: bad row",
			})
		}
		start := time.Now()
		if err := store.FinishImportBatch(ctx, batchID, result, "partial", ""); err != nil {
			t.Fatalf("FinishImportBatch: %v", err)
		}
		elapsed := time.Since(start)
		t.Logf("PROBE db import-failure write rows=%d elapsed=%s per_row=%s",
			failures, elapsed.Round(time.Millisecond), (elapsed / time.Duration(failures)).Round(time.Microsecond))
	}
}

// TestPerfDBAuditListPlan shows whether the audit list query can use an index
// when no tool filter is supplied.
func TestPerfDBAuditListPlan(t *testing.T) {
	store := perfStore(t)
	ctx := context.Background()
	rec := storage.NewAuditRecorder(store)
	for i := 0; i < 5000; i++ {
		if err := rec.Record(ctx, audit.Event{
			OccurredAt: time.Now().UTC().Add(time.Duration(i) * time.Millisecond),
			Subject:    "subject", IdentityClass: "human", Tool: fmt.Sprintf("magento.product.get"),
			Decision: "allow", Phase: "outcome", State: "succeeded",
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	start := time.Now()
	events, err := store.ListAudit(ctx, "", 50)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	elapsed := time.Since(start)
	t.Logf("PROBE db audit list rows=5000 returned=%d elapsed=%s", len(events), elapsed.Round(time.Millisecond))
	start = time.Now()
	filtered, err := store.ListAudit(ctx, "magento.product.get", 50)
	if err != nil {
		t.Fatalf("ListAudit(tool): %v", err)
	}
	t.Logf("PROBE db audit list filtered returned=%d elapsed=%s", len(filtered), time.Since(start).Round(time.Millisecond))
}
