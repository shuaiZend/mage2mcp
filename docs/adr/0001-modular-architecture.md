# ADR-0001: Modular architecture with a shared data source contract

- **Status:** Accepted
- **Date:** 2026-11-18
- **Deciders:** maintainer, Architect
- **Supersedes:** none

## Context

mage2mcp integrates three unrelated vendors (Magento 2, Cloudflare, Google
Analytics 4) and must later accept additional sources (Search Console, BigQuery,
Shopify, WooCommerce) without rewriting what exists. The project is public and
will be maintained by contributors who do not share the original author's
context.

## Problem

How do we structure the code so that modules can be developed, tested, and
released independently, while still supporting cross-source analysis, without
creating a distributed monolith or a shared "utils" layer that erodes boundaries?

## Options considered

1. **Single package monolith.** Fastest to start. Any change risks every feature;
   tests require all credentials; contributors collide.
2. **Microservices per source.** Strong isolation, but three deployables plus
   orchestration for a tool that is, at its core, an API gateway. Operationally
   disproportionate for self-hosted users.
3. **Modular monolith with a shared contract and capability declarations.**
   One binary, strict internal boundaries, modules independently testable,
   cross-source work isolated in a dedicated module.
4. **Plugin architecture with runtime loading (`plugin` package or WASM).**
   Attractive for third parties, but Go plugins are platform-fragile, and WASM
   adds a large security surface before the core is stable.

## Decision

Adopt option 3: a modular monolith.

- Each source lives in `internal/<source>` and owns its client, service, DTOs,
  configuration, errors, tools, and tests.
- Every module implements the small `core/datasource.DataSource` contract
  (`Name`, `HealthCheck`, `Capabilities`) and declares capabilities.
- Modules never import each other. `internal/unified` declares narrow consumer
  interfaces over neutral types in `internal/core/models`; wiring happens in
  `cmd/server`.
- The MCP registry registers tools only for capabilities that are enabled and
  healthy.

## Rationale

The dominant risk in this project is not scaling; it is boundary erosion under
parallel development by multiple agents and contributors. A modular monolith
with enforced import direction keeps the review surface small, makes each module
testable with fakes, and keeps deployment to two binaries. Capability
declarations solve the "one broken vendor must not break the others" problem
without runtime plugin loading.

## Consequences

### Positive

- A contributor can work on GA4 without understanding Magento.
- A failing Cloudflare token disables Cloudflare tools only.
- Cross-source logic is forced into one reviewable place.
- Deployment stays simple: server, worker, admin CLI, PostgreSQL (no Redis).

### Negative

- Import direction must be enforced by tooling; discipline alone is not enough.
- A shared binary means a dependency upgrade affects all modules at once.
- `unified` needs adapter code in `cmd/`, which is boilerplate.

### Security impact

Smaller, explicit interfaces reduce the attack surface reachable from a single
tool. The absence of runtime plugin loading avoids an entire class of code
execution risk. The main residual risk is a module importing a privileged
internal package; mitigated by the import-boundary check.

### Maintenance cost

Low to moderate: one extra indirection layer (consumer interfaces + adapters).
The payoff is that adding a source does not require touching existing modules.

## Follow-up

- Phase 1: implement the import-boundary check in CI.
- Phase 1: publish `docs/integrations/README.md` describing the module template.
- Revisit plugin loading only after 1.0, with a dedicated ADR.

## References

- [Architecture](../architecture.md)
- [Module boundaries](../module-boundaries.md)
