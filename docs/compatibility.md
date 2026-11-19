# Compatibility and stability

## Supported components

| Component | Supported | Notes |
|---|---|---|
| Go (building from source) | 1.26, 1.27 | The module floor is `go 1.26.0`; CI tests both. |
| PostgreSQL | 16+ | The job queue, audit, confirmations, and instance registry live here. |
| Magento | 2.4.x | REST `V1` with an integration token (bearer or OAuth 1.0a). The primary target is 2.4.9. |
| Cloudflare API | current v4 + GraphQL Analytics | Read-only; one zone per instance. |
| GA4 Data API | v1beta | Aggregate-only; one property per instance. |
| MCP protocol | `2025-11-25` and `2026-07-28` | Negotiated by the official Go SDK; older clients are rejected. |
| Container runtime | Docker with Compose | Distroless image, non-root, loopback-bound dev stack. |
| Redis | not required | Removed in ADR-0019; the queue is PostgreSQL-only. |

## Interface stability

The gateway exposes MCP tools, configuration keys, and HTTP endpoints. The
following promises apply:

- **Tool names** are stable. A rename requires a deprecation period (the old
  name keeps working for at least one minor release) and a changelog entry.
- **Required tool fields** are stable. Optional fields may be added in a minor
  release; unknown fields are rejected, so clients should send only documented
  fields.
- **Configuration keys** are stable. Unknown keys are rejected rather than
  ignored, so a typo fails at startup. Removing a key requires an ADR and a
  migration note.
- **HTTP endpoints** `/mcp`, `/healthz`, `/readyz`, and `/metrics` keep their
  paths and semantics.
- **Error codes** in the `Code` taxonomy are stable identifiers. New codes may
  be added; existing meanings do not change.
- **Wire formats**: `platform.info`, health/readiness JSON, and Prometheus metric
  names are additive-only within a major version.

Pre-1.0, a minor release may still make a breaking change when an ADR and a
changelog entry record it (see [Upgrade](operations/upgrade.md)).

## Version support

| Version | Status |
|---|---|
| `v0.4.0` | Supported pre-release |
| `v0.3.0` | Superseded |
| `v0.2.0` | Superseded |
| `v0.1.0` | Superseded |
| `v0.0.2` | Superseded |
| `v0.0.1` | Superseded |

Security fixes are provided for the latest pre-release only until 1.0.

## Data-source caveats

- Magento's REST surface differs between Adobe Commerce and Magento Open Source;
  Adobe-only endpoints (for example `import/csv`) are not used.
- Cloudflare bot-score fields require Bot Management; the module degrades to
  documented fields when it is absent.
- GA4 ecommerce data is only reported after the capability probe confirms
  ecommerce tracking; otherwise the tools return `ecommerce_not_tracked`.
