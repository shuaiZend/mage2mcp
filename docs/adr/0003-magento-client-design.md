# ADR-0003: Magento client design — REST admin only (GraphQL verification superseded)

- **Status:** Accepted; the GraphQL-verification half is superseded by ADR-0022 (no Magento GraphQL client ships)
- **Date:** 2026-11-18
- **Deciders:** maintainer, Architect, Magento owner
- **Supersedes:** none

## Context

The project must manage Magento 2.4.x products, including full configurable
product lifecycle, without installing a Magento module and without touching the
database. Magento exposes REST and GraphQL, with different capabilities and
authentication models across self-hosted and SaaS deployments.

## Problem

Which Magento surface should each capability use, and how do we authenticate,
model EAV data, and prove that a configurable product is actually correct rather
than merely accepted by the API?

## Options considered

1. **Magento plugin + internal PHP API.** Richest access, explicitly forbidden by
   the project brief, and a maintenance burden across Magento versions.
2. **Direct database access.** Fast for reads, unsafe, version-fragile, and
   explicitly forbidden.
3. **REST only.** Complete admin coverage, stable versioned endpoints, but no
   storefront-shaped read model.
4. **GraphQL only.** Excellent storefront reads, but Magento GraphQL has no
   product write mutations.
5. **REST for writes and admin reads, GraphQL for storefront verification.**
   Uses each surface for what it actually supports.

## Decision

Adopt option 5.

- **REST** (`/rest/{store}/V1/...`) is the only write path and the primary admin
  read path: products, configurable parent/child/options, attributes and sets,
  categories, media, MSI inventory, orders, bulk import and bulk status.
- **GraphQL** was proposed for read-only storefront verification. It is **not**
  implemented: the gateway has no Magento GraphQL client (ADR-0022). Verification
  is an admin REST read-back (`configurable.children`, `product.get`).
- **Authentication:** integration credentials with OAuth 1.0a signing are the
  production default (HMAC-signed requests, no global configuration toggle).
  Integration access tokens used as bearer tokens work only when the store sets
  `oauth/consumer/enable_integration_as_bearer=1`. Admin username/password token
  exchange is development-only, disabled by default, and needs a 2FA-capable
  route when 2FA is enabled. Credentials are per instance; admin tokens are
  cached for four hours minus skew and never persisted.
- **EAV:** attributes are discovered at runtime from attribute sets and
  attributes endpoints; no store-specific field is hardcoded. Attribute scope,
  `frontend_input`, and `is_configurable` are validated before any variant work.
- **Deployment scope:** self-hosted / PaaS Magento 2.4.x, primary target 2.4.9.
  Adobe Commerce as a Service (IMS OAuth) is out of scope for v0.x and must be
  detected and rejected with a clear error rather than failing obscurely.

## Rationale

GraphQL cannot create or update products, so a GraphQL-only design is impossible.
REST alone cannot prove the storefront result, which the brief explicitly
requires. Using both, each for its strength, satisfies the functional requirement
without a plugin. Runtime EAV discovery is the only way to support arbitrary
stores. Integration tokens avoid storing admin passwords and reduce blast radius.

## Consequences

### Positive

- Full lifecycle without a Magento module or database access.
- Storefront verification catches the failure mode where Magento returns 200 but
  the product is not buyable.
- Store-agnostic: no hardcoded attribute codes.

### Negative

- Two API surfaces to maintain and test.
- GraphQL verification depends on storefront visibility and may be affected by
  cache; the client must account for cache warm-up.
- Bulk import depends on Magento's message queue being configured, and the
  `import/csv` and `import/json` endpoints are Adobe Commerce features; Magento
  Open Source must fall back to chunked REST writes.
- OAuth 1.0a request signing is more complex than a bearer header and must be
  covered by signature-vector tests.

### Security impact

Integration tokens are scoped and revocable; admin credentials are never
persisted. The client must not follow arbitrary base URLs from user input (SSRF),
must enforce TLS, and must never log tokens. Write tools require explicit
permission and confirmation for destructive operations.

### Maintenance cost

Moderate: two clients, plus version compatibility work for 2.4.x. Offset by
avoiding a PHP module that would need its own release cycle per Magento version.

## Follow-up

- Phase 0: verify the endpoint map against a live 2.4.9 schema endpoint.
- Phase 2: implement storefront verification and cache-warm handling.
- Post-1.0: evaluate Adobe Commerce as a Service (IMS) support.

## References

- [Magento integration design](../integrations/magento.md)
- [research baseline](../development/research-baseline.md) facts F6–F12
