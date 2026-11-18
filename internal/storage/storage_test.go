// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shuaiZend/mage2mcp/internal/security/audit"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("MAGE2MCP_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("MAGE2MCP_TEST_DATABASE_URL is not set; skipping PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, Config{URL: url, MaxConns: 4})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return store
}

func TestMigrateIsIdempotent(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestAuditRecorderRoundTrip(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	rec := NewAuditRecorder(store)
	tool := "platform.info"
	if err := rec.Record(ctx, audit.Event{
		Subject: "user-1", IdentityClass: "human", Tool: tool,
		Decision: "allow", Phase: "intent", State: "pending",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := rec.Record(ctx, audit.Event{
		Subject: "user-1", IdentityClass: "human", Tool: tool,
		Decision: "allow", Phase: "outcome", State: "succeeded",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	records, err := store.ListAudit(ctx, tool, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(records) < 2 {
		t.Fatalf("records = %d, want >= 2", len(records))
	}
	if records[0].SubjectHash != SubjectHash("user-1") {
		t.Fatal("subject hash mismatch")
	}
	if records[0].Phase != "outcome" {
		t.Fatalf("newest phase = %q, want outcome", records[0].Phase)
	}
}

func TestAuditRejectsMissingFields(t *testing.T) {
	store := testStore(t)
	rec := NewAuditRecorder(store)
	if err := rec.Record(context.Background(), audit.Event{Tool: "x"}); err == nil {
		t.Fatal("expected validation error")
	}
}
