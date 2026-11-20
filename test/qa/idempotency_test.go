//go:build qa

package qa

import (
	"net/http"
	"strings"
	"testing"
)

// TestIdempotentRequestReplaysRecordedOutcome guards REL-02 / ARCH-04: the same
// request submitted twice performs one upstream mutation; the second submission
// returns the recorded outcome instead of repeating the write.
//
// Each submission is separately approved, which is what a human retry looks like;
// the ledger is what prevents the duplicate mutation.
func TestIdempotentRequestReplaysRecordedOutcome(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products":
			writeJSON(w, http.StatusOK, map[string]any{"id": 1, "sku": "QA-IDEM-1", "type_id": "simple"})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/default/V1/products/"):
			writeJSON(w, http.StatusOK, map[string]any{"id": 1, "sku": "QA-IDEM-1", "type_id": "simple"})
		default:
			http.NotFound(w, r)
		}
	})
	inst := uniqueInstance(t, "idem")
	f := newFixture(t, fixtureOptions{upstreams: map[string]string{inst: up.url()}, writes: true})
	agent := f.session(t, "agent-1")
	args := productArgs(inst, "QA-IDEM-1")

	firstApproval := f.requireConfirmation(t, agent, "magento.product.create", args)
	f.approve(t, firstApproval, "human-1")
	first := f.call(t, agent, "magento.product.create", withConfirmation(args, firstApproval))
	first.mustSucceed(t)
	if got := up.count(http.MethodPost, "/rest/default/V1/products"); got != 1 {
		t.Fatalf("qa: first submission made %d upstream mutations, want 1", got)
	}
	if first.Structured["product"] == nil {
		t.Fatalf("qa: first response carried no product: %v", first.Structured)
	}

	secondApproval := f.requireConfirmation(t, agent, "magento.product.create", args)
	if secondApproval == firstApproval {
		t.Fatal("qa: the gateway reused a consumed approval id")
	}
	f.approve(t, secondApproval, "human-1")
	second := f.call(t, agent, "magento.product.create", withConfirmation(args, secondApproval))
	second.mustSucceed(t)
	if got := up.count(http.MethodPost, "/rest/default/V1/products"); got != 1 {
		t.Fatalf("qa: the second submission mutated the upstream (%d POSTs); it must replay the recorded outcome", got)
	}
	if !strings.Contains(second.Text, "replayed") {
		t.Fatalf("qa: the replay was not labelled as such: %q", second.Text)
	}
	if second.Structured["product"] == nil {
		t.Fatalf("qa: the replay did not return the recorded product: %v", second.Structured)
	}

	// A different request under the same arguments but a different SKU is a new
	// operation and must mutate once.
	other := productArgs(inst, "QA-IDEM-2")
	otherApproval := f.requireConfirmation(t, agent, "magento.product.create", other)
	f.approve(t, otherApproval, "human-1")
	f.call(t, agent, "magento.product.create", withConfirmation(other, otherApproval)).mustSucceed(t)
	if got := up.count(http.MethodPost, "/rest/default/V1/products"); got != 2 {
		t.Fatalf("qa: a genuinely different request did not mutate: %d POSTs, want 2", got)
	}
}
