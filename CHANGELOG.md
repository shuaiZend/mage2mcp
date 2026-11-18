# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Phase 1 foundation: runnable MCP server on the official Go SDK with Streamable
  HTTP, tool registry enforcing the tool contract, `platform.info` tool, health
  and readiness endpoints, Prometheus metrics, structured logging with secret
  redaction, Cloudflare Access JWT verification, default-deny RBAC, append-only
  audit recorder, YAML configuration with validation, distroless container image,
  Compose stack, and Makefile targets.
- Read-only module tools: `magento.product.get`, `magento.product.search`
  (OAuth 1.0a or bearer), `cloudflare.analytics.traffic`,
  `cloudflare.security.summary` (fixed GraphQL templates), `ga4.analytics.traffic`,
  and `ga4.property.metadata` (with the compatibility validator).
- Authorization and audit middleware around every `tools/call`, with a default-deny
  RBAC policy and an append-only in-memory recorder.
- GA4 ecommerce overview: `ga4.ecommerce.overview` reports purchases and
  revenue per day, but only after the capability probe confirms ecommerce
  tracking; otherwise it returns `ecommerce_not_tracked` instead of zeros.
- Cloudflare security events: `cloudflare.security.events` returns bounded,
  newest-first sampled events (action, rule, country, path, user agent) without
  client IPs; 30-day window, 31-day retention disclosure, limit 1-1000.
- Magento import execution: bounded CSV/JSON batches executed through the
  existing product services with per-row outcomes, existing-SKU skip/update
  semantics, and explicit partial-success reporting (no rollback). Requires
  confirmation and `tools.writes`.
- Magento import validation: bounded CSV/JSON parsing (8 MiB, 5000 rows,
  64 columns) with per-row errors, duplicate-SKU detection, and the
  `magento.import.validate` read-only tool.
- Configuration reference covering every key and environment override.
- Unified analytics: declared bindings, a Cloudflare-requests vs GA4-sessions
  comparison with per-source values, a nil ratio when sessions are zero, and
  quality notes; `unified.traffic.compare` tool and binding configuration.
- Confirmation protocol (ADR-0014): canonical argument hashing, pending
  requests, approval by a different human identity, 5-minute TTL (15 maximum),
  single use, and enforcement in the tool-call pipeline. The `mage2mcp-admin`
  CLI is the approval surface; the PostgreSQL store is shared with the gateway.
- Magento writes behind `tools.writes`: product create/update, configurable
  create (children → parent → links with staged, non-atomic error reporting),
  add-child, and MSI source-item assignment; five write tools marked as requiring
  confirmation (the confirmation middleware is the next slice, so the flag stays
  off by default).
- GA4 completion: service-account OAuth token source (RS256 JWT-bearer, cached,
  single-flight), ecommerce capability probe with evidence, realtime reports, and
  the `ga4.property.capabilities` / `ga4.analytics.realtime` tools.
- Storage and jobs: embedded PostgreSQL migrations, connection pool, durable
  audit recorder, and a Redis/Asynq worker with a liveness task and graceful
  shutdown. The server uses durable audit when `database.url` is configured.
- Test suite with unit, integration, and race coverage; CI runs formatting, vet,
  lint, module integrity, tests with race detector, coverage floor, secret
  scanning, vulnerability scanning, license review, and action pinning.
- Phase 0 design baseline: architecture, module boundaries, database design,
  MCP tool catalog, unified analytics model, security threat model, integration
  mappings for Magento 2, Cloudflare, and Google Analytics 4.
- Open source governance: Apache-2.0 license, contributing guide, code of
  conduct, security policy, support policy, issue and pull request templates.
- Continuous integration for documentation linting and link checking, with
  Go build, test, race, and coverage jobs enabled once the module lands.
- Security automation: secret scanning, dependency review, govulncheck, gosec,
  and OpenSSF Scorecard.
- Architecture decision records covering module architecture, MCP transport,
  Magento client design, Cloudflare log storage, GA4 integration, authentication,
  multi-agent development, licensing, data stores, error model, configuration,
  and observability.

[Unreleased]: https://github.com/shuaiZend/mage2mcp/commits/main
