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
