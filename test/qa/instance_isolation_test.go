//go:build qa

package qa

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// TestInstanceRoutingNeverCrossesTenants guards SEC-02 and the MAG cross-instance
// findings: a call routed to instance A must never reach instance B's upstream,
// including through the import job tools, and the job tools must not let a caller
// read or cancel another instance's job.
func TestInstanceRoutingNeverCrossesTenants(t *testing.T) {
	makeUpstream := func(label string) *fakeUpstream {
		return newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
			switch {
			case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/default/V1/products/"):
				sku := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				writeJSON(w, http.StatusOK, map[string]any{"id": 1, "sku": sku, "type_id": "simple"})
			case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products":
				writeJSON(w, http.StatusOK, map[string]any{"id": 1, "sku": label + "-CREATED", "type_id": "simple"})
			default:
				http.NotFound(w, r)
			}
		})
	}
	upA, upB := makeUpstream("A"), makeUpstream("B")
	instA, instB := uniqueInstance(t, "a"), uniqueInstance(t, "b")
	f := newFixture(t, fixtureOptions{
		upstreams: map[string]string{instA: upA.url(), instB: upB.url()},
		writes:    true,
		imports:   true,
	})
	agent := f.session(t, "agent-1")

	// A read on A reaches only A.
	res := f.call(t, agent, "magento.product.get", map[string]any{"instance": instA, "sku": "SKU-A"})
	res.mustSucceed(t)
	if got := upA.count(http.MethodGet, "/rest/default/V1/products/SKU-A"); got != 1 {
		t.Fatalf("qa: instance A saw %d reads of SKU-A, want 1", got)
	}
	if upB.total() != 0 {
		t.Fatalf("qa: instance B's upstream saw %d requests after a call routed to A", upB.total())
	}

	// A write on A reaches only A.
	f.approvedWrite(t, agent, "magento.product.create", productArgs(instA, "SKU-A-W"), "human-1")
	if upB.total() != 0 {
		t.Fatalf("qa: a write routed to A reached B's upstream %d times", upB.total())
	}

	// The asynchronous import tools are tenant-scoped: a job started on A cannot
	// be read or canceled from B.
	csv := "sku,name,type_id,price,attribute_set_id\nIMP-1,Imported,simple,1.00,4\n"
	startArgs := map[string]any{
		"instance": instA, "format": "csv",
		"data_base64": base64.StdEncoding.EncodeToString([]byte(csv)),
	}
	start := f.approvedWrite(t, agent, "magento.import.start", startArgs, "human-1")
	start.mustSucceed(t)
	jobID, _ := start.Structured["job_id"].(string)
	if jobID == "" {
		t.Fatalf("qa: import.start returned no job id: %v", start.Structured)
	}

	status := f.call(t, agent, "magento.import.status", map[string]any{"instance": instB, "job_id": jobID})
	status.mustFail(t, string(errs.CodeNotFound))
	// import.cancel is a write: the approval gate runs before the tenant check,
	// so the cancel must be approved to reach the scoping decision.
	cancelArgs := map[string]any{"instance": instB, "job_id": jobID}
	cancelApproval := f.requireConfirmation(t, agent, "magento.import.cancel", cancelArgs)
	f.approve(t, cancelApproval, "human-1")
	cancel := f.call(t, agent, "magento.import.cancel", withConfirmation(cancelArgs, cancelApproval))
	cancel.mustFail(t, string(errs.CodeNotFound))
	own := f.call(t, agent, "magento.import.status", map[string]any{"instance": instA, "job_id": jobID})
	own.mustSucceed(t)

	if upB.total() != 0 {
		t.Fatalf("qa: instance B's upstream was reached %d times during an A-only test", upB.total())
	}
}

// TestImportJobIsScopedToTheCallerSubject guards SEC-02: a second caller on the
// same instance cannot read or cancel another caller's job.
func TestImportJobIsScopedToTheCallerSubject(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeJSON(w, http.StatusOK, map[string]any{})
	})
	inst := uniqueInstance(t, "tenant")
	f := newFixture(t, fixtureOptions{
		upstreams: map[string]string{inst: up.url()},
		writes:    true,
		imports:   true,
	})
	alice := f.session(t, "alice")
	bob := f.session(t, "bob")
	csv := "sku,name,type_id,price,attribute_set_id\nTEN-1,Tenant,simple,1.00,4\n"
	start := f.approvedWrite(t, alice, "magento.import.start", map[string]any{
		"instance": inst, "format": "csv",
		"data_base64": base64.StdEncoding.EncodeToString([]byte(csv)),
	}, "human-1")
	start.mustSucceed(t)
	jobID, _ := start.Structured["job_id"].(string)
	if jobID == "" {
		t.Fatalf("qa: no job id: %v", start.Structured)
	}
	status := f.call(t, bob, "magento.import.status", map[string]any{"instance": inst, "job_id": jobID})
	status.mustFail(t, string(errs.CodeNotFound))
	cancelArgs := map[string]any{"instance": inst, "job_id": jobID}
	cancelApproval := f.requireConfirmation(t, bob, "magento.import.cancel", cancelArgs)
	f.approve(t, cancelApproval, "human-1")
	cancel := f.call(t, bob, "magento.import.cancel", withConfirmation(cancelArgs, cancelApproval))
	cancel.mustFail(t, string(errs.CodeNotFound))
}
