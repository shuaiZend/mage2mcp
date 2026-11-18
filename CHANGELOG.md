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
