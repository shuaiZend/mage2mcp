# Compatibility and stability

## Supported components

| Component | Supported | Status | Notes |
|---|---|---|---|
| Go (building from source) | 1.26, 1.27 | IMPLEMENTED | The module floor is `go 1.26.0`; the manual CI matrix tests both. |
| PostgreSQL | 16+ | IMPLEMENTED, TESTED (PostgreSQL) | The job queue, audit, confirmations, operation ledger, and instance registry live here. |
| Magento | 2.4.x | IMPLEMENTED, TESTED (CONTRACT) | REST `V1` with a bearer, OAuth 1.0a, or `admin:<username>:<password>` credential. The primary target is 2.4.9. **No live instance was available, so 2.4.9 compatibility is unverified.** |
| Cloudflare API | current v4 + GraphQL Analytics | IMPLEMENTED, TESTED (CONTRACT) | Read-only; one zone per instance. Live API behaviour is unverified. |
| GA4 Data API | v1beta | IMPLEMENTED, TESTED (CONTRACT) | Aggregate-only; one property per instance. Live API behaviour is unverified. |
| MCP protocol | `2026-07-28`, `2025-11-25`, `2025-06-18`, `2025-03-26`, `2024-11-05` | IMPLEMENTED, TESTED (CONTRACT) | All versions returned by `mcp.SupportedProtocolVersions()`; negotiated by the official Go SDK. The SDK is expected to reject a client that offers none of them; that rejection is not directly tested. |
| Container runtime | Docker with Compose | IMPLEMENTED | Distroless image, non-root, loopback-bound dev stack. |
| Redis | not required | DEPRECATED | Removed in ADR-0019; the queue is PostgreSQL-only. |

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
| `v0.11.x` (latest: v0.11.3) | Supported pre-release |
| `v0.10.0` | Superseded |
| `v0.9.0` | Superseded |
| `v0.8.1` | Superseded |
| `v0.8.0` | Superseded |
| `v0.7.6` | Superseded |
| `v0.7.5` | Superseded |
| `v0.7.4` | Superseded |
| `v0.7.3` | Superseded |
| `v0.7.2` | Superseded |
| `v0.7.1` | Superseded |
| `v0.7.0` | Superseded |
| `v0.6.0` | Superseded |
| `v0.5.0` | Superseded |
| `v0.4.0` | Superseded |
| `v0.3.0` | Superseded |
| `v0.2.0` | Superseded |
| `v0.1.0` | Superseded |
| `v0.0.2` | Superseded |
| `v0.0.1` | Superseded |

Security fixes are provided for the latest pre-release only until 1.0. The
current release is v0.11.3; older minors are superseded and receive no fixes.

## Verification status

There is no automated Magento integration test: every Magento control is verified
against the REST contract and `httptest` fakes (`CONTRACT`), and the end-to-end
harness uses a fake Magento. Cloudflare and GA4 are likewise unverified against a
live API. Treat the first production use as a verification period and see the
[known limitations](audit/known-limitations.md).

## Data-source caveats

- Magento's REST surface differs between Adobe Commerce and Magento Open Source;
  Adobe-only endpoints (for example `import/csv`) are not used. The gateway does
  not use Magento GraphQL; storefront verification is not implemented (ADR-0022).
- Cloudflare bot-score fields require Bot Management; the module degrades to
  documented fields when it is absent.
- GA4 ecommerce data is only reported after the capability probe confirms
  ecommerce tracking; otherwise the tools return `ecommerce_not_tracked`.
