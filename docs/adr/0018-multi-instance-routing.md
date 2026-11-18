# ADR-0018: Multi-instance routing and per-instance authorization

- **Status:** Accepted (implementation in progress)
- **Date:** 2026-11-19
- **Deciders:** maintainer, Architect
- **Supersedes:** the single-instance limitation documented in the module READMEs

## Context

Every module currently binds one `Service` to one configured instance, and the
gateway rejects a module with more than one instance at startup. The tool schemas
already carry an `instance` field, but it is validated as metadata and ignored by
the handler. Real deployments commonly run several Magento stores, Cloudflare
zones, or GA4 properties behind one gateway, and unified tools already reference
instance triples through bindings. The single-instance restriction is therefore
the main remaining functional gap.

## Problem

How does a tool call select the right upstream service, and how is that choice
authorized and audited, without duplicating tool definitions or letting a caller
reach an instance it was not granted?

## Options considered

1. **One process per instance.** Simplest authorization story, but multiplies
   deployments, credentials, and MCP endpoints; bindings across instances become
   impossible without a second gateway.
2. **Duplicate tool names per instance.** Rejected: tool names are frozen and
   model-visible; `magento.product.get.store-us` would be a permanent API wart.
3. **A resolver map inside each module.** The module exposes
   `Register(reg, resolver)` where `resolver` maps `instance` to a `*Service`.
   Handlers resolve at call time; the registry is unchanged. Authorization stays
   in the existing middleware, extended with instance scope.
4. **A gateway-level router that rewrites requests.** Adds a second dispatch
   layer and hides instance errors from the module; rejected.

## Decision

Adopt option 3, implemented module by module (GA4 first, then Cloudflare, then
Magento).

- Each module gains a small resolver:

  ```go
  type Resolver struct { services map[string]*Service; fallback string }
  func (r *Resolver) Resolve(instance string) (*Service, error)
  ```

  With exactly one configured instance, an empty `instance` resolves to it; with
  several, `instance` is required and an unknown value is `not_found`.
- Tool handlers take the raw `instance` argument and call `Resolve` before any
  upstream request. No tool name changes and no schema change: `instance` is
  already present.
- RBAC is evaluated per call as today, with the instance passed through to the
  policy. A grant scoped to instance A never authorizes instance B; a grant with
  an empty instance scope applies to all instances of that tool.
- Audit records the resolved instance and the grant that matched, so a
  cross-instance call is attributable.
- Bindings reference instance ids; a unified tool authorizes the binding and
  verifies every member instance exists and is enabled.
- Startup validates that every binding member exists; a module with N instances
  registers one tool per tool kind, not per instance.
- Per-instance health is reported by `/readyz` as degraded when one instance is
  unavailable but another is usable.

## Rationale

Resolvers keep tool definitions single and instance-agnostic, which preserves
the frozen public surface and the model's view of the catalog. Passing the
instance through authorization and audit is what makes multi-instance safe: the
credential choice and the permission decision become the same decision.

## Consequences

### Positive

- One gateway serves several stores, zones, and properties.
- Bindings and unified analytics work across real multi-instance deployments.
- No tool-name or schema churn.

### Negative

- Every handler gains a resolution step and an error path.
- Per-instance rate limits and quotas must be tracked separately.
- Tests must cover resolution, unknown instance, and cross-instance denial for
  each module.

### Security impact

Positive: instance selection becomes an authorization input instead of an
ignored field. The main risk is a handler that resolves after performing a
side-effect-free lookup with the wrong service; the contract is "resolve first,
then call", enforced by tests.

### Maintenance cost

Low per module: one resolver type, one wiring change, and a table of resolution
tests. The pattern is identical across modules.

## Implementation progress

| Step | Status |
|---|---|
| GA4 resolver (`internal/ga4/resolver.go`) with single-instance fast path and tests | Done |
| Audit records the requested instance on every tool call | Done |
| GA4 tools resolve `instance` through the resolver; instance enters RBAC | Next |
| Cloudflare tools resolve `instance`; configuration accepts N zones | Done |
| Cloudflare resolver (`internal/cloudflare/resolver.go`) with tests | Done |
| Magento resolver (`internal/magento/resolver.go`) with tests | Done |
| Configuration accepts N instances; module READMEs updated | Planned |

## Follow-up

- Phase 2: wire the GA4 resolver into the tool handlers and pass the instance to RBAC and audit; then Cloudflare; then Magento.
- Phase 2: extend the RBAC policy and audit schema to carry the instance.
- Phase 2: allow N instances in configuration validation, keeping the
  single-instance fast path.
- Remove the "one instance per module" limitation from the module READMEs and
  the configuration reference when all three modules are converted.

## References

- [Module boundaries](../module-boundaries.md)
- [Architecture](../architecture.md)
- [Unified analytics](../unified-analytics.md)
