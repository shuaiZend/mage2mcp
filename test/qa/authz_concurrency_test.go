//go:build qa

package qa

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/security/rbac"
)

// mustPolicy returns the shipped ladder bound to one instance. The parameter is
// not variadic on purpose: every fixture must name the instance it authorizes,
// so a policy that silently authorizes nothing cannot slip in.
func mustPolicy(t *testing.T, instance string) *rbac.Policy {
	t.Helper()
	policy, err := rbac.DefaultPolicy(map[string][]string{"magento": {instance}})
	if err != nil {
		t.Fatalf("qa: default policy: %v", err)
	}
	return policy
}

// TestConcurrentViewerCannotWriteWhileOperatorReads guards SEC-01 / ARCH-01.
//
// Two identities with different roles hit the same gateway at the same time: a
// viewer attempts product.create in a loop while an operator reads. The viewer
// must be denied on every attempt and must never cause an upstream mutation; the
// operator must keep reading. A final operator write proves the ladder allows
// what it should, so the denials are authorization, not a broken fixture.
func TestConcurrentViewerCannotWriteWhileOperatorReads(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/default/V1/products/"):
			sku := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			writeJSON(w, http.StatusOK, map[string]any{"id": 1, "sku": sku, "type_id": "simple"})
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products":
			writeJSON(w, http.StatusOK, map[string]any{"id": 1, "sku": "QA-OP-1", "type_id": "simple"})
		default:
			http.NotFound(w, r)
		}
	})
	inst := uniqueInstance(t, "authz")
	f := newFixture(t, fixtureOptions{
		upstreams: map[string]string{inst: up.url()},
		writes:    true,
		policy:    mustPolicy(t, inst),
	})
	viewer := f.session(t, "viewer-1", "viewer")
	operator := f.session(t, "operator-1", "operator")

	const attempts = 8
	var wg sync.WaitGroup
	viewerCodes := make(chan string, attempts)
	operatorErrs := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			res, err := f.callSafe(viewer, "magento.product.create", productArgs(inst, fmt.Sprintf("QA-V-%02d", i)))
			if err != nil {
				operatorErrs <- err
				return
			}
			if !res.IsError {
				viewerCodes <- "SUCCESS"
				return
			}
			viewerCodes <- res.code()
		}(i)
		go func() {
			defer wg.Done()
			res, err := f.callSafe(operator, "magento.product.get", map[string]any{"instance": inst, "sku": "SKU-1"})
			if err != nil {
				operatorErrs <- err
				return
			}
			if res.IsError {
				operatorErrs <- fmt.Errorf("operator read denied: %s", res.Text)
			}
		}()
	}
	wg.Wait()
	close(viewerCodes)
	close(operatorErrs)

	for err := range operatorErrs {
		t.Fatalf("qa: concurrent operator call failed: %v", err)
	}
	denied := 0
	for code := range viewerCodes {
		if code == "SUCCESS" {
			t.Fatalf("qa: a viewer succeeded at a write (SEC-01 regression)")
		}
		if code != string(errs.CodePermissionDenied) {
			t.Fatalf("qa: viewer write denial code = %q, want permission_denied", code)
		}
		denied++
	}
	if denied != attempts {
		t.Fatalf("qa: %d viewer denials, want %d", denied, attempts)
	}
	if got := up.count(http.MethodPost, "/rest/default/V1/products"); got != 0 {
		t.Fatalf("qa: a denied viewer caused %d upstream mutations", got)
	}

	// Positive control: the same identity class that may write, still can.
	res := f.approvedWrite(t, operator, "magento.product.create", productArgs(inst, "QA-OP-1"), "human-1")
	res.mustSucceed(t)
	if got := up.count(http.MethodPost, "/rest/default/V1/products"); got != 1 {
		t.Fatalf("qa: operator write produced %d upstream mutations, want 1", got)
	}
}

// TestUnknownRoleGrantsNothing guards SEC-01: a role the policy does not know is
// additive-nothing, so a caller cannot widen access by naming a privileged role.
func TestUnknownRoleGrantsNothing(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/default/V1/products/") {
			writeJSON(w, http.StatusOK, map[string]any{"id": 1, "sku": "SKU-1", "type_id": "simple"})
			return
		}
		http.NotFound(w, r)
	})
	inst := uniqueInstance(t, "role")
	f := newFixture(t, fixtureOptions{
		upstreams: map[string]string{inst: up.url()},
		writes:    true,
		policy:    mustPolicy(t, inst),
	})
	session := f.session(t, "nobody-1", "superuser", "admin-of-everything")
	res := f.call(t, session, "magento.product.get", map[string]any{"instance": inst, "sku": "SKU-1"})
	res.mustFail(t, string(errs.CodePermissionDenied))
	if up.total() != 0 {
		t.Fatalf("qa: an ungranted role reached the upstream %d times", up.total())
	}
}

// TestAuthorizationIsFailClosedForUnregisteredTools guards SEC-01: an unregistered
// tool is denied before the authorizer is consulted, so a missing registration can
// never become an allow.
func TestAuthorizationIsFailClosedForUnregisteredTools(t *testing.T) {
	inst := uniqueInstance(t, "unregistered")
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeJSON(w, http.StatusOK, map[string]any{})
	})
	f := newFixture(t, fixtureOptions{
		upstreams: map[string]string{inst: up.url()},
		writes:    true,
		policy:    mustPolicy(t, inst),
	})
	admin := f.session(t, "admin-1", "admin")
	res := f.call(t, admin, "magento.tool.that.does.not.exist", map[string]any{"instance": inst})
	if !res.IsError {
		t.Fatalf("qa: an unregistered tool was not denied: %v", res.Structured)
	}
	if code := res.code(); code != string(errs.CodePermissionDenied) && code != string(errs.CodeNotFound) {
		t.Fatalf("qa: unregistered tool denial code = %q", code)
	}
}
