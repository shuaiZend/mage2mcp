# ADR-0022: Magento interface scope is the REST admin surface

- **Status:** Accepted
- **Date:** 2026-11-19
- **Deciders:** maintainer, Architect

## Context

The design catalog contains a few tools that would need a storefront session
(`configurable.verify_storefront`), a generic job queue surface
(`platform.job.*`), or runtime mutation of analytics mapping
(`unified.mapping.*`). The project's hard constraints are: no Magento plugin, no
direct database access, read-only by default, and a bounded, auditable write
surface.

## Decision

The Magento interface is the **REST admin API** (`/rest/{store_code}/V1/...`) with
an integration token. Concretely:

- No storefront GraphQL calls. Variant verification through the storefront is not
  implemented; correctness is checked with admin reads (`configurable.children`,
  `product.get`) and the SEO/field validators.
- No generic job-queue tools. Each feature exposes its own status/cancel tools.
- No runtime analytics-mapping mutation. SKU mapping is data reviewed through
  ADRs and docs.
- Media association for variants follows Magento's model: images live on the
  child SKU; there is no configurable media association endpoint.

## Consequences

### Positive

- One authentication model and one failure model for every Magento tool.
- No storefront credentials, no browser session, no CSRF handling.
- The write surface stays bounded and confirmable.

### Negative

- Storefront-only checks (rendered variant images, final prices with promotions)
  must be verified by an operator or a separate system.
- Job inspection is per feature rather than uniform.

## References

- [Magento integration](../integrations/magento.md)
- [Tool catalog](../mcp-tools.md)
- ADR-0007 (no Magento plugin), ADR-0011 (read-only default)
