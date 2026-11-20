// SPDX-License-Identifier: Apache-2.0

// Package rbac implements the default-deny authorization policy.
//
// A grant names a role, a tool (exact, namespace prefix, or "*"), an instance
// ("" means every instance), a scope, and an effect. Authorization resolves the
// caller's roles against those grants: deny beats allow, and a grant must cover
// the required scope on the read < write < destructive < admin ladder.
package rbac

import (
	"fmt"
	"strings"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
)

// Scopes, ordered from least to most privileged.
const (
	ScopeRead        = "read"
	ScopeWrite       = "write"
	ScopeDestructive = "destructive"
	ScopeAdmin       = "admin"
)

// Effects.
const (
	EffectAllow = "allow"
	EffectDeny  = "deny"
)

// Grant is one policy row.
type Grant struct {
	Role     string
	Tool     string
	Instance string
	Scope    string
	Effect   string
}

var scopeRank = map[string]int{
	ScopeRead:        1,
	ScopeWrite:       2,
	ScopeDestructive: 3,
	ScopeAdmin:       4,
}

// Policy is an immutable set of grants safe for concurrent use.
//
// The identity's roles are the IdP's verified claim (see internal/security/jwt):
// this package does not map subjects to roles, so the IdP is the role authority.
// An unknown role is simply a role with no grants, which is why default deny
// covers both an identity with no roles and one claiming a role the policy has
// never heard of.
type Policy struct {
	byRole map[string][]Grant
}

// New validates and indexes the grants.
func New(grants []Grant) (*Policy, error) {
	p := &Policy{byRole: make(map[string][]Grant, 8)}
	for i, g := range grants {
		switch {
		case g.Role == "":
			return nil, fmt.Errorf("rbac: grant %d has no role", i)
		case g.Tool == "":
			return nil, fmt.Errorf("rbac: grant %d has no tool", i)
		case scopeRank[g.Scope] == 0:
			return nil, fmt.Errorf("rbac: grant %d has unknown scope %q", i, g.Scope)
		case g.Effect != EffectAllow && g.Effect != EffectDeny:
			return nil, fmt.Errorf("rbac: grant %d has unknown effect %q", i, g.Effect)
		}
		p.byRole[g.Role] = append(p.byRole[g.Role], g)
	}
	return p, nil
}

// DefaultPolicy returns the shipped role ladder.
//
// The ladder is expressed as one grant per role, each at the role's highest
// scope: a grant covers every lower rung (admin > destructive > write > read),
// so `viewer` and `analyst` can never reach a write, and an `operator` write
// grant can never reach a destructive call. Destructive and admin operations
// therefore require a role whose grant was deliberately placed at that rung;
// no read-only role is given a wildcard that permits a write.
//
// The role names match the seeded roles table (viewer, analyst, operator,
// admin) and the matrix in docs/security/threat-model.md §6.2.
func DefaultPolicy() (*Policy, error) {
	return New([]Grant{
		{Role: "viewer", Tool: "*", Scope: ScopeRead, Effect: EffectAllow},
		{Role: "analyst", Tool: "*", Scope: ScopeRead, Effect: EffectAllow},
		{Role: "operator", Tool: "*", Scope: ScopeWrite, Effect: EffectAllow},
		{Role: "admin", Tool: "*", Scope: ScopeAdmin, Effect: EffectAllow},
	})
}

// Authorize returns nil when the identity may call the tool on the instance at
// the required scope. Every other outcome is a permission_denied error.
//
// A role that matches no grant contributes nothing; roles are additive across
// the identity's roles, and an explicit deny on any matching grant wins over
// every allow. Claiming an unknown or privileged-sounding role name therefore
// cannot widen access — only a grant in this policy can.
func (p *Policy) Authorize(id *auth.Identity, tool, instance, requiredScope string) error {
	if p == nil || id == nil {
		return denied("no policy or identity")
	}
	if scopeRank[requiredScope] == 0 {
		return denied("unknown required scope")
	}
	allowed := false
	for _, role := range id.Roles {
		for _, g := range p.byRole[role] {
			if !matchesTool(g.Tool, tool) || !matchesInstance(g.Instance, instance) {
				continue
			}
			if g.Effect == EffectDeny {
				return denied("explicit deny")
			}
			if scopeRank[g.Scope] >= scopeRank[requiredScope] {
				allowed = true
			}
		}
	}
	if !allowed {
		return denied("no matching grant")
	}
	return nil
}

func matchesTool(grant, tool string) bool {
	if grant == "*" || grant == tool {
		return true
	}
	return strings.HasPrefix(tool, grant+".")
}

func matchesInstance(grant, instance string) bool {
	return grant == "" || grant == instance
}

func denied(reason string) error {
	return errs.New(errs.CodePermissionDenied, "permission denied").WithDetail("reason", reason)
}
