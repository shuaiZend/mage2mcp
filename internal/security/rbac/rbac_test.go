// SPDX-License-Identifier: Apache-2.0

package rbac

import (
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
)

func mustPolicy(t *testing.T, grants ...Grant) *Policy {
	t.Helper()
	p, err := New(grants)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func analyst() *auth.Identity {
	return &auth.Identity{Subject: "user-1", Class: auth.ClassHuman, Roles: []string{"analyst"}}
}

func TestNewRejectsInvalidGrants(t *testing.T) {
	cases := [][]Grant{
		{{Role: "", Tool: "x", Scope: ScopeRead, Effect: EffectAllow}},
		{{Role: "r", Tool: "", Scope: ScopeRead, Effect: EffectAllow}},
		{{Role: "r", Tool: "x", Scope: "superuser", Effect: EffectAllow}},
		{{Role: "r", Tool: "x", Scope: ScopeRead, Effect: "maybe"}},
	}
	for i, grants := range cases {
		if _, err := New(grants); err == nil {
			t.Fatalf("case %d: expected validation error", i)
		}
	}
}

func TestDefaultDeny(t *testing.T) {
	p := mustPolicy(t)
	err := p.Authorize(analyst(), "magento.product.get", "store-us", ScopeRead)
	if errs.CodeOf(err) != errs.CodePermissionDenied {
		t.Fatalf("expected permission_denied, got %v", err)
	}
}

func TestAllowExactAndPrefix(t *testing.T) {
	p := mustPolicy(t,
		Grant{Role: "analyst", Tool: "magento.product.get", Scope: ScopeRead, Effect: EffectAllow},
		Grant{Role: "analyst", Tool: "ga4.analytics", Scope: ScopeRead, Effect: EffectAllow},
	)
	if err := p.Authorize(analyst(), "magento.product.get", "store-us", ScopeRead); err != nil {
		t.Fatalf("exact allow: %v", err)
	}
	if err := p.Authorize(analyst(), "ga4.analytics.report", "giztop", ScopeRead); err != nil {
		t.Fatalf("prefix allow: %v", err)
	}
	if err := p.Authorize(analyst(), "magento.product.search", "store-us", ScopeRead); err == nil {
		t.Fatal("prefix must not match a sibling namespace")
	}
}

func TestInstanceScoping(t *testing.T) {
	p := mustPolicy(t, Grant{Role: "analyst", Tool: "*", Instance: "store-us", Scope: ScopeRead, Effect: EffectAllow})
	if err := p.Authorize(analyst(), "magento.product.get", "store-us", ScopeRead); err != nil {
		t.Fatalf("matching instance: %v", err)
	}
	if err := p.Authorize(analyst(), "magento.product.get", "store-eu", ScopeRead); err == nil {
		t.Fatal("foreign instance must be denied")
	}
}

func TestDenyOverridesAllow(t *testing.T) {
	p := mustPolicy(t,
		Grant{Role: "operator", Tool: "magento.product", Scope: ScopeWrite, Effect: EffectAllow},
		Grant{Role: "operator", Tool: "magento.product.delete", Scope: ScopeDestructive, Effect: EffectDeny},
	)
	op := &auth.Identity{Subject: "user-2", Class: auth.ClassHuman, Roles: []string{"operator"}}
	if err := p.Authorize(op, "magento.product.update", "store-us", ScopeWrite); err != nil {
		t.Fatalf("allowed write denied: %v", err)
	}
	if err := p.Authorize(op, "magento.product.delete", "store-us", ScopeDestructive); err == nil {
		t.Fatal("explicit deny ignored")
	}
}

func TestScopeHierarchy(t *testing.T) {
	p := mustPolicy(t, Grant{Role: "admin", Tool: "*", Scope: ScopeAdmin, Effect: EffectAllow})
	admin := &auth.Identity{Subject: "admin-1", Class: auth.ClassHuman, Roles: []string{"admin"}}
	for _, scope := range []string{ScopeRead, ScopeWrite, ScopeDestructive, ScopeAdmin} {
		if err := p.Authorize(admin, "magento.product.delete", "store-us", scope); err != nil {
			t.Fatalf("admin should cover %s: %v", scope, err)
		}
	}
	readOnly := mustPolicy(t, Grant{Role: "viewer", Tool: "*", Scope: ScopeRead, Effect: EffectAllow})
	viewer := &auth.Identity{Subject: "v", Roles: []string{"viewer"}}
	if err := readOnly.Authorize(viewer, "magento.product.update", "store-us", ScopeWrite); err == nil {
		t.Fatal("read grant must not cover write")
	}
}

func TestNilIdentityOrPolicyDenies(t *testing.T) {
	var p *Policy
	if err := p.Authorize(analyst(), "magento.product.get", "i", ScopeRead); err == nil {
		t.Fatal("nil policy must deny")
	}
	valid := mustPolicy(t, Grant{Role: "analyst", Tool: "*", Scope: ScopeRead, Effect: EffectAllow})
	if err := valid.Authorize(nil, "magento.product.get", "i", ScopeRead); err == nil {
		t.Fatal("nil identity must deny")
	}
}

func TestUnknownRequiredScopeDenies(t *testing.T) {
	p := mustPolicy(t, Grant{Role: "analyst", Tool: "*", Scope: ScopeAdmin, Effect: EffectAllow})
	if err := p.Authorize(analyst(), "magento.product.get", "i", "root"); err == nil {
		t.Fatal("unknown required scope must deny")
	}
}

// identityFor returns a human identity holding exactly one role.
func identityFor(role string) *auth.Identity {
	return &auth.Identity{Subject: "user-" + role, Class: auth.ClassHuman, Roles: []string{role}}
}

// TestDefaultPolicyExpressesTheLadder pins the shipped policy: read-only roles
// stay read-only, a write grant stops below destructive, and only the admin
// rung covers destructive and admin calls.
func TestDefaultPolicyExpressesTheLadder(t *testing.T) {
	p, err := DefaultPolicy()
	if err != nil {
		t.Fatalf("DefaultPolicy: %v", err)
	}
	const (
		readTool        = "magento.product.get"
		writeTool       = "magento.invoice.capture"
		destructiveTool = "magento.product.delete"
		adminTool       = "platform.config.reload"
	)
	cases := []struct {
		role  string
		tool  string
		scope string
		want  bool
	}{
		// viewer and analyst are read-only.
		{"viewer", readTool, ScopeRead, true},
		{"viewer", writeTool, ScopeWrite, false},
		{"viewer", destructiveTool, ScopeDestructive, false},
		{"viewer", adminTool, ScopeAdmin, false},
		{"analyst", readTool, ScopeRead, true},
		{"analyst", writeTool, ScopeWrite, false},
		{"analyst", destructiveTool, ScopeDestructive, false},
		// operator writes but never destroys.
		{"operator", readTool, ScopeRead, true},
		{"operator", writeTool, ScopeWrite, true},
		{"operator", destructiveTool, ScopeDestructive, false},
		{"operator", adminTool, ScopeAdmin, false},
		// admin covers every rung.
		{"admin", readTool, ScopeRead, true},
		{"admin", writeTool, ScopeWrite, true},
		{"admin", destructiveTool, ScopeDestructive, true},
		{"admin", adminTool, ScopeAdmin, true},
		// an unknown role is default-denied even for a read.
		{"intruder", readTool, ScopeRead, false},
	}
	for _, tc := range cases {
		err := p.Authorize(identityFor(tc.role), tc.tool, "store-us", tc.scope)
		if got := err == nil; got != tc.want {
			t.Errorf("role=%s tool=%s scope=%s: allowed=%v want=%v (err=%v)",
				tc.role, tc.tool, tc.scope, got, tc.want, err)
		}
		if !tc.want && err != nil && errs.CodeOf(err) != errs.CodePermissionDenied {
			t.Errorf("role=%s tool=%s: error code = %v, want permission_denied", tc.role, tc.tool, errs.CodeOf(err))
		}
	}
}

// TestDefaultPolicyDenyStillBeatsAllow proves the shipped policy has not traded
// away deny-beats-allow: a deny grant layered on a covering allow still wins.
func TestDefaultPolicyDenyStillBeatsAllow(t *testing.T) {
	p, err := DefaultPolicy()
	if err != nil {
		t.Fatalf("DefaultPolicy: %v", err)
	}
	denied, err := New([]Grant{
		{Role: "operator", Tool: "*", Scope: ScopeWrite, Effect: EffectAllow},
		{Role: "operator", Tool: "magento.product.delete", Scope: ScopeWrite, Effect: EffectDeny},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Authorize(identityFor("operator"), "magento.product.update", "store-us", ScopeWrite); err != nil {
		t.Fatalf("operator write denied by the shipped policy: %v", err)
	}
	if err := denied.Authorize(identityFor("operator"), "magento.product.delete", "store-us", ScopeWrite); err == nil {
		t.Fatal("explicit deny must beat a covering allow")
	}
}

// TestUnknownRoleCannotWidenAccess pins SEC-09's fail-closed property. The IdP
// is the role authority, so a token can claim any role name it can obtain; a
// name the policy does not grant must reach nothing, and a near-miss spelling
// must not be normalised into a grant.
func TestUnknownRoleCannotWidenAccess(t *testing.T) {
	p, err := DefaultPolicy()
	if err != nil {
		t.Fatalf("DefaultPolicy: %v", err)
	}
	const (
		readTool        = "magento.product.get"
		writeTool       = "magento.invoice.capture"
		destructiveTool = "magento.product.delete"
	)
	cases := []struct {
		name  string
		roles []string
		tool  string
		scope string
		want  bool
	}{
		{"unknown role alone cannot read", []string{"superuser"}, readTool, ScopeRead, false},
		{"unknown role alone cannot destroy", []string{"root"}, destructiveTool, ScopeDestructive, false},
		{"no roles cannot read", nil, readTool, ScopeRead, false},
		{"unknown role adds nothing to viewer", []string{"superuser", "viewer"}, readTool, ScopeRead, true},
		{"unknown role adds nothing to viewer for a write", []string{"superuser", "viewer"}, writeTool, ScopeWrite, false},
		{"padded role name is not normalised", []string{"viewer "}, readTool, ScopeRead, false},
		{"case-different role name is not normalised", []string{"Viewer"}, readTool, ScopeRead, false},
		{"privileged-sounding prefix is not a wildcard", []string{"viewer:admin"}, readTool, ScopeRead, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := &auth.Identity{Subject: "claimant", Class: auth.ClassHuman, Roles: tc.roles}
			err := p.Authorize(id, tc.tool, "store-us", tc.scope)
			if got := err == nil; got != tc.want {
				t.Fatalf("roles=%q tool=%s scope=%s: allowed=%v want=%v (err=%v)",
					tc.roles, tc.tool, tc.scope, got, tc.want, err)
			}
			if !tc.want && errs.CodeOf(err) != errs.CodePermissionDenied {
				t.Fatalf("roles=%q: code = %v, want permission_denied", tc.roles, errs.CodeOf(err))
			}
		})
	}
}
