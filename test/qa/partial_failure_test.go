//go:build qa

package qa

import (
	"encoding/base64"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// TestImportBatchPartialFailureIsReported guards MAG-05 (non-atomic multi-step
// writes) and the audit's "product batch create partial failure" blind spot: a batch create
// where one row is rejected reports the partial result honestly — per-row
// outcomes, the failing SKU, and the created count — instead of claiming
// success or rolling back the earlier writes.
func TestImportBatchPartialFailureIsReported(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/default/V1/products/"):
			// Both SKUs are new, so the import takes the create path.
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products":
			if strings.Contains(string(body), "FAIL-1") {
				writeJSON(w, http.StatusBadRequest, map[string]string{"message": "The value of attribute is incorrect."})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"id": 1, "sku": "OK-1", "type_id": "simple"})
		default:
			http.NotFound(w, r)
		}
	})
	inst := uniqueInstance(t, "import")
	f := newFixture(t, fixtureOptions{upstreams: map[string]string{inst: up.url()}, writes: true})
	agent := f.session(t, "agent-1")
	csv := "sku,name,type_id,price,attribute_set_id\nOK-1,Ok,simple,9.99,4\nFAIL-1,Bad,simple,9.99,4\n"
	args := map[string]any{
		"instance": inst, "format": "csv",
		"data_base64": base64.StdEncoding.EncodeToString([]byte(csv)),
	}
	res := f.approvedWrite(t, agent, "magento.import.execute", args, "human-1")
	res.mustSucceed(t)

	result, ok := res.Structured["result"].(map[string]any)
	if !ok {
		t.Fatalf("qa: import.execute returned no result object: %v", res.Structured)
	}
	if got := numberField(t, result, "total"); got != 2 {
		t.Fatalf("qa: total = %d, want 2", got)
	}
	if got := numberField(t, result, "created"); got != 1 {
		t.Fatalf("qa: created = %d, want 1", got)
	}
	if got := numberField(t, result, "failed"); got != 1 {
		t.Fatalf("qa: failed = %d, want 1", got)
	}
	rows, ok := result["rows"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("qa: rows = %v, want two per-row outcomes", result["rows"])
	}
	failedRow, ok := rows[1].(map[string]any)
	if !ok {
		t.Fatalf("qa: row is not an object: %v", rows[1])
	}
	if failedRow["sku"] != "FAIL-1" || failedRow["status"] != "failed" {
		t.Fatalf("qa: failing row = %v, want FAIL-1/failed", failedRow)
	}
	if got := up.count(http.MethodPost, "/rest/default/V1/products"); got != 2 {
		t.Fatalf("qa: upstream creates = %d, want 2 (one per row)", got)
	}
}

// TestInventoryTransferPartialFailureIsHonest guards MAG-11: a mid-batch failure
// names the row that was applied, carries the transfer id, and issues no
// compensation (exactly one write, never a second).
func TestInventoryTransferPartialFailureIsHonest(t *testing.T) {
	var reads atomic.Int32
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/inventory/source-items":
			n := reads.Add(1)
			from, to := 10, 0
			if n >= 3 {
				// The batch failed after the from row landed.
				from = 7
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"items": []any{
					map[string]any{"sku": "SKU-1", "source_code": "default", "quantity": from, "status": 1},
					map[string]any{"sku": "SKU-1", "source_code": "warehouse", "quantity": to, "status": 1},
				},
				"total_count": 2,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/inventory/source-items":
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "batch failed"})
		default:
			http.NotFound(w, r)
		}
	})
	inst := uniqueInstance(t, "transfer")
	f := newFixture(t, fixtureOptions{
		upstreams: map[string]string{inst: up.url()},
		writes:    true,
		policy:    mustPolicy(t, inst),
	})
	admin := f.session(t, "admin-1", "admin")
	res := f.approvedWrite(t, admin, "magento.inventory.transfer", transferArgs(inst, "SKU-1", 3), "human-1")
	res.mustFail(t, "conflict")
	if got := res.detail("applied_rows"); got != "from" {
		t.Fatalf("qa: applied_rows = %q, want from (the row that landed must be named)", got)
	}
	if res.detail("transfer_id") == "" {
		t.Fatalf("qa: the partial transfer carried no transfer id: %v", res.details())
	}
	if got := up.count(http.MethodPost, "/rest/default/V1/inventory/source-items"); got != 1 {
		t.Fatalf("qa: transfer writes = %d, want exactly 1 (no retry, no compensation)", got)
	}
	if got := reads.Load(); got != 3 {
		t.Fatalf("qa: source reads = %d, want 3 (read, re-read, read-back)", got)
	}
}

func numberField(t *testing.T, object map[string]any, key string) int {
	t.Helper()
	value, ok := object[key].(float64)
	if !ok {
		t.Fatalf("qa: %q is not a number in %v", key, object)
	}
	return int(value)
}

// TestInventoryTransferRefusesFractionalQuantities asserts the documented
// limitation for MAG-12 / MAG-R07: the gateway transfers whole units only, so a
// fractional source quantity is refused with a typed error instead of being
// truncated and reported as applied. This is deliberately deferred behaviour, not
// a skipped case.
func TestInventoryTransferRefusesFractionalQuantities(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/inventory/source-items":
			writeJSON(w, http.StatusOK, map[string]any{
				"items": []any{
					map[string]any{"sku": "SKU-1", "source_code": "default", "quantity": 10.5, "status": 1},
					map[string]any{"sku": "SKU-1", "source_code": "warehouse", "quantity": 0, "status": 1},
				},
				"total_count": 2,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/inventory/source-items":
			writeJSON(w, http.StatusOK, []any{})
		default:
			http.NotFound(w, r)
		}
	})
	inst := uniqueInstance(t, "fractional")
	f := newFixture(t, fixtureOptions{
		upstreams: map[string]string{inst: up.url()},
		writes:    true,
		policy:    mustPolicy(t, inst),
	})
	admin := f.session(t, "admin-1", "admin")
	res := f.approvedWrite(t, admin, "magento.inventory.transfer", transferArgs(inst, "SKU-1", 3), "human-1")
	res.mustFail(t, "unsupported_capability")
	if !strings.Contains(res.Text, "fractional") {
		t.Fatalf("qa: the refusal did not name the fractional limitation: %s", res.Text)
	}
	if got := up.count(http.MethodPost, "/rest/default/V1/inventory/source-items"); got != 0 {
		t.Fatalf("qa: a fractional quantity was written (%d writes); it must be refused, not truncated", got)
	}
}
