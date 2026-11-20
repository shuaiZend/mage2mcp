//go:build qa

package qa

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/shuaiZend/mage2mcp/internal/storage"
)

// TestPostgresOutageFailsClosed guards REL-02 / REL-16: when PostgreSQL becomes
// unavailable after the approval is granted, the write fails closed — the caller
// is told, the upstream is never mutated, and nothing is reported as success.
//
// The test uses its own store so closing it cannot affect the other tests.
func TestPostgresOutageFailsClosed(t *testing.T) {
	url := os.Getenv("MAGE2MCP_TEST_DATABASE_URL")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, storage.Config{URL: url, MaxConns: 4})
	if err != nil {
		t.Fatalf("qa: open dedicated store: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("qa: migrate dedicated store: %v", err)
	}

	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products" {
			writeJSON(w, http.StatusOK, map[string]any{"id": 1, "sku": "QA-PG-1", "type_id": "simple"})
			return
		}
		http.NotFound(w, r)
	})
	inst := uniqueInstance(t, "pg")
	f := newFixtureWithStore(t, store, fixtureOptions{
		upstreams: map[string]string{inst: up.url()},
		writes:    true,
	})
	agent := f.session(t, "agent-1")
	args := productArgs(inst, "QA-PG-1")
	approval := f.requireConfirmation(t, agent, "magento.product.create", args)
	f.approve(t, approval, "human-1")

	// The database goes away after the approval, before the write.
	store.Close()

	res := f.call(t, agent, "magento.product.create", withConfirmation(args, approval))
	if !res.IsError {
		t.Fatalf("qa: a write reported success while PostgreSQL was down: %v", res.Structured)
	}
	if code := res.code(); code == "" {
		t.Fatalf("qa: the failure carried no error code: %s", res.Text)
	}
	if got := up.count(http.MethodPost, "/rest/default/V1/products"); got != 0 {
		t.Fatalf("qa: the upstream was mutated %d times despite the database outage", got)
	}
	if err := store.Ping(ctx); err == nil {
		t.Fatal("qa: the closed store still reports healthy")
	}
}
