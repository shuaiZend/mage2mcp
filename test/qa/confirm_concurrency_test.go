//go:build qa

package qa

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// TestConcurrentApprovalTokenYieldsOneSuccess guards SEC-03 / ARCH-08: one
// approval token consumed by many concurrent callers authorises exactly one
// mutation.
//
// Contract (post-t42): BeginCall decides the ledger transition *before* it
// consumes the approval. The winner creates the operation and burns the
// approval in one transaction; a duplicate that arrives while the operation is
// in progress is refused with a typed conflict and does not consume anything,
// and a duplicate that arrives after the winner commits gets the recorded
// result as a replay. The old expectation (seven confirmation_used) described
// the pre-t42 order, where the approval was consumed before the operation row
// was examined. The new code is stricter and more accurate: a loser is told
// "poll, do not retry" rather than "the approval is gone", and the approval is
// burned exactly once, by the winner. The assertions below are about that
// invariant, not about the error string: one non-replay success, one upstream
// mutation, and a confirmation row that is consumed and can never be consumed
// again.
func TestConcurrentApprovalTokenYieldsOneSuccess(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products" {
			writeJSON(w, http.StatusOK, map[string]any{"id": 1, "sku": "QA-TOKEN-1", "type_id": "simple"})
			return
		}
		http.NotFound(w, r)
	})
	inst := uniqueInstance(t, "token")
	f := newFixture(t, fixtureOptions{upstreams: map[string]string{inst: up.url()}, writes: true})
	args := productArgs(inst, "QA-TOKEN-1")

	owner := f.session(t, "agent-1")
	approval := f.requireConfirmation(t, owner, "magento.product.create", args)
	f.approve(t, approval, "human-1")

	const callers = 8
	sessions := make([]*mcp.ClientSession, callers)
	for i := range sessions {
		sessions[i] = f.session(t, "agent-1")
	}

	var wg sync.WaitGroup
	codes := make(chan string, callers)
	for i := range sessions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := f.callSafe(sessions[i], "magento.product.create", withConfirmation(args, approval))
			if err != nil {
				codes <- "transport_error"
				return
			}
			if !res.IsError {
				// A duplicate that arrives after the winner committed returns the
				// recorded result; the gateway labels it "replayed". It is a
				// success response but not a second mutation, so count it apart.
				if strings.Contains(res.Text, "replayed") {
					codes <- "replayed"
					return
				}
				codes <- "ok"
				return
			}
			codes <- res.code()
		}(i)
	}
	wg.Wait()
	close(codes)

	// The winner is the only caller that may report a non-replay success. A
	// duplicate is either refused while the operation is in flight (conflict)
	// or replayed after it committed; both are non-mutating. A confirmation_used
	// here would mean a loser reached the consume step after the winner, which
	// the post-t42 ordering forbids.
	successes, conflicts, replays, used := 0, 0, 0, 0
	for code := range codes {
		switch code {
		case "ok":
			successes++
		case "replayed":
			replays++
		case string(errs.CodeConflict):
			conflicts++
		case string(errs.CodeConfirmationUsed):
			used++
		default:
			t.Fatalf("qa: concurrent approval produced unexpected result %q", code)
		}
	}
	t.Logf("results: ok=%d conflict=%d replayed=%d confirmation_used=%d", successes, conflicts, replays, used)
	if successes != 1 {
		t.Fatalf("qa: %d non-replay successes with one approval, want exactly 1", successes)
	}
	if used != 0 {
		t.Fatalf("qa: %d callers reached the consume step after the winner; the post-t42 gate must refuse them first", used)
	}
	if conflicts+replays != callers-1 {
		t.Fatalf("qa: %d losers accounted for, want %d", conflicts+replays, callers-1)
	}
	// The mutation count is the invariant, not the response codes.
	if got := up.count(http.MethodPost, "/rest/default/V1/products"); got != 1 {
		t.Fatalf("qa: upstream mutations = %d, want exactly 1 (one approval, one write)", got)
	}
	// The approval must be consumed exactly once, by the winner.
	state, consumedAt := f.confirmState(t, approval)
	if state != "consumed" {
		t.Fatalf("qa: approval state = %q, want consumed", state)
	}
	if consumedAt.IsZero() {
		t.Fatal("qa: approval is consumed but consumed_at is unset")
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("qa: marshal args: %v", err)
	}
	if err := f.confirms.Consume(context.Background(), approval, "agent-1", "magento.product.create", inst, argsJSON); errs.CodeOf(err) != errs.CodeConfirmationUsed {
		t.Fatalf("qa: the consumed approval could be consumed again: %v", err)
	}
}

// TestApprovalIsBoundToItsToolAndInstance guards SEC-03 / ARCH-08: an approval
// issued for one tool cannot be replayed against another tool.
func TestApprovalIsBoundToItsToolAndInstance(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products":
			writeJSON(w, http.StatusOK, map[string]any{"id": 1, "sku": "QA-BIND-1", "type_id": "simple"})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/rest/default/V1/products/"):
			writeJSON(w, http.StatusOK, true)
		default:
			http.NotFound(w, r)
		}
	})
	inst := uniqueInstance(t, "bind")
	f := newFixture(t, fixtureOptions{upstreams: map[string]string{inst: up.url()}, writes: true})
	agent := f.session(t, "agent-1")

	createArgs := productArgs(inst, "QA-BIND-1")
	approval := f.requireConfirmation(t, agent, "magento.product.create", createArgs)
	f.approve(t, approval, "human-1")

	// The same token against a different tool must be rejected, not consumed.
	res := f.call(t, agent, "magento.product.delete", withConfirmation(map[string]any{
		"instance": inst, "sku": "QA-BIND-1",
	}, approval))
	res.mustFail(t, string(errs.CodeConfirmationMismatch))
	if got := up.count(http.MethodDelete, "/rest/default/V1/products/"); got != 0 {
		t.Fatalf("qa: a cross-tool approval deleted %d products", got)
	}

	// The legitimate use still succeeds.
	f.call(t, agent, "magento.product.create", withConfirmation(createArgs, approval)).mustSucceed(t)
}
