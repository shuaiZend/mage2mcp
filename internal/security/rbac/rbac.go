// SPDX-License-Identifier: Apache-2.0

// Package rbac implements the default-deny authorization policy.
//
// A grant names a role, a tool (exact, namespace prefix, or "*"), an instance,
// a scope, and an effect. Authorization resolves the caller's roles against
// those grants: deny beats allow, and a grant must cover the required scope on
// the read < write < destructive < admin ladder.
//
// Instance scoping is explicit. A grant must name an instance; "" is rejected so
// a policy cannot authorize every store by accident. WildcardInstance ("*") is
// the opt-in for a deployment that deliberately treats all instances as one
// trust domain. Without that opt-in, a role authorized for one store cannot
// read another.
package rbac

import (
	"fmt"
	"sort"
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

// WildcardInstance is the grant value that matches every instance. It is opt-in
// on purpose: the default policy binds concrete instances, so a cross-instance
// grant has to be written or asked for explicitly.
const WildcardInstance = "*"

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
		case g.Instance == "":
			return nil, fmt.Errorf("rbac: grant %d has no instance; name one, or use %q for every instance",
				i, WildcardInstance)
		case scopeRank[g.Scope] == 0:
			return nil, fmt.Errorf("rbac: grant %d has unknown scope %q", i, g.Scope)
		case g.Effect != EffectAllow && g.Effect != EffectDeny:
			return nil, fmt.Errorf("rbac: grant %d has unknown effect %q", i, g.Effect)
		}
		p.byRole[g.Role] = append(p.byRole[g.Role], g)
	}
	return p, nil
}

// DefaultPolicy returns the shipped role ladder bound to the deployment's
// instances, grouped by data-source module.
//
// The ladder is expressed as one grant per role per instance, each at the role's
// highest scope: a grant covers every lower rung (admin > destructive > write >
// read), so `viewer` and `analyst` can never reach a write, and an `operator`
// write grant can never reach a destructive call. Destructive and admin
// operations therefore require a role whose grant was deliberately placed at
// that rung; no read-only role is given a wildcard that permits a write.
//
// Instance scoping is the point of the signature (SEM-02). Every grant names one
// instance, so a role authorized for one store cannot read another. The shipped
// shape is one instance per module (one Magento store, one Cloudflare zone, one
// GA4 property): the ladder binds each of them and no store is reachable twice.
//
// A module with more than one instance is a multi-tenant deployment. The shipped
// ladder cannot express "store A yes, store B no", so DefaultPolicy refuses
// instead of silently granting every store: the deployment must write
// per-instance grants, or opt in to one trust domain with
// DefaultPolicyAllInstances.
//
// No instances is valid: a deployment with no data sources can still serve
// platform.info, and every data tool stays denied. platform.info is granted at
// WildcardInstance because it returns the gateway's own metadata, never store
// data.
//
// The role names match the seeded roles table (viewer, analyst, operator,
// admin) and the matrix in docs/security/threat-model.md §6.2.
func DefaultPolicy(instances map[string][]string) (*Policy, error) {
	modules, err := instanceModules(instances)
	if err != nil {
		return nil, err
	}
	grants := make([]Grant, 0, len(modules)*8)
	for _, module := range modules {
		instance := module.ids[0]
		grants = append(grants,
			Grant{Role: "viewer", Tool: "*", Instance: instance, Scope: ScopeRead, Effect: EffectAllow},
			Grant{Role: "analyst", Tool: "*", Instance: instance, Scope: ScopeRead, Effect: EffectAllow},
			Grant{Role: "operator", Tool: "*", Instance: instance, Scope: ScopeWrite, Effect: EffectAllow},
			Grant{Role: "admin", Tool: "*", Instance: instance, Scope: ScopeAdmin, Effect: EffectAllow},
		)
	}
	for _, role := range []string{"viewer", "analyst", "operator", "admin"} {
		grants = append(grants, Grant{
			Role: role, Tool: "platform.info", Instance: WildcardInstance,
			Scope: ScopeRead, Effect: EffectAllow,
		})
	}
	return New(grants)
}

// moduleInstances is one module's validated instance list.
type moduleInstances struct {
	module string
	ids    []string
}

// instanceModules validates the configured instances and rejects a module that
// declares more than one, because the shipped ladder cannot express per-instance
// trust inside a module.
func instanceModules(instances map[string][]string) ([]moduleInstances, error) {
	modules := make([]string, 0, len(instances))
	for module := range instances {
		modules = append(modules, module)
	}
	sort.Strings(modules)
	out := make([]moduleInstances, 0, len(modules))
	for _, module := range modules {
		if strings.TrimSpace(module) == "" {
			return nil, fmt.Errorf("rbac: instance group has no module name")
		}
		ids, err := instanceIDs(instances[module])
		if err != nil {
			return nil, fmt.Errorf("rbac: %s: %w", module, err)
		}
		if len(ids) > 1 {
			return nil, fmt.Errorf(
				"rbac: %s has %d instances (%s); the default policy binds one instance per module. "+
					"Grant per-instance policies explicitly, or call DefaultPolicyAllInstances() to treat them as one trust domain",
				module, len(ids), strings.Join(ids, ", "))
		}
		if len(ids) == 0 {
			continue
		}
		out = append(out, moduleInstances{module: module, ids: ids})
	}
	return out, nil
}

// DefaultPolicyAllInstances is the explicit single-trust-domain opt-in: the same
// ladder with every grant bound to WildcardInstance. Use it only when the
// configured instances are known to be one tenant.
func DefaultPolicyAllInstances() (*Policy, error) {
	grants := make([]Grant, 0, 4)
	for _, role := range []string{"viewer", "analyst", "operator", "admin"} {
		scope := ScopeRead
		switch role {
		case "operator":
			scope = ScopeWrite
		case "admin":
			scope = ScopeAdmin
		}
		grants = append(grants, Grant{
			Role: role, Tool: "*", Instance: WildcardInstance, Scope: scope, Effect: EffectAllow,
		})
	}
	return New(grants)
}

// instanceIDs validates and de-duplicates the configured instance ids.
func instanceIDs(instances []string) ([]string, error) {
	seen := make(map[string]struct{}, len(instances))
	out := make([]string, 0, len(instances))
	for _, raw := range instances {
		id := strings.TrimSpace(raw)
		switch {
		case id == "":
			return nil, fmt.Errorf("rbac: configured instance ids must not be empty")
		case id == WildcardInstance:
			return nil, fmt.Errorf("rbac: %q is the wildcard; use DefaultPolicyAllInstances to opt in to every instance",
				WildcardInstance)
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
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

// matchesInstance reports whether a grant covers an instance. Only an explicit
// wildcard grant crosses instances: an unbound grant cannot exist (New rejects
// it), and a named grant matches only its own instance.
func matchesInstance(grant, instance string) bool {
	return grant == WildcardInstance || grant == instance
}

func denied(reason string) error {
	return errs.New(errs.CodePermissionDenied, "permission denied").WithDetail("reason", reason)
}
