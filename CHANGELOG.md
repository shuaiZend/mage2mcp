# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Asynchronous Magento imports: `magento.import.start` validates the batch shape
  and enqueues a `magento:import` job in the PostgreSQL queue,
  `magento.import.status` reports job state/attempts/result, and
  `magento.import.cancel` cancels it. `cmd/worker` resolves the store from
  configuration and stores the partial-success report on the job.
- Shared Magento credential parsing and resolver construction
  (`magento.ParseCredential`, `magento.NewResolverFromSettings`) used by both the
  server and the worker.
- `magento.invoice.capture` (charges the payment method for an existing invoice;
  confirmation-gated and documented as money-moving) and `magento.order.email`
  (sends the order confirmation email).
- Magento product link writes: `magento.product.links.add` and
  `magento.product.links.remove` for related/upsell/cross-sell relations, with
  self-link, type, and position validation. Both require confirmation.
- Magento category writes: `magento.category.create`,
  `magento.category.update` (partial update; at least one field required), and
  `magento.category.assign_product`. All are confirmation-gated with name,
  position, and parent validation.
- Magento order status writes: `magento.order.comment` (history comment with an
  optional status transition and notify flag), `magento.order.hold`, and
  `magento.order.unhold`. All are confirmation-gated.
- Magento fulfillment writes: `magento.shipment.create` (order-item lines plus
  up to 20 tracking numbers) and `magento.creditmemo.create` (refund record with
  optional positive/negative adjustments). Both are confirmation-gated with
  bounded lines, validated tracking, and comment/quantity checks.
- `magento.product.links` (related/upsell/cross-sell reads).
- `magento.invoice.create`: confirmation-gated invoicing with `capture=false` by
  default, strict order-item/quantity validation, and duplicate rejection.
- `magento.product.media.upload`: confirmation-gated image upload with a 5 MiB
  decoded payload ceiling, MIME/filename allowlists, media-type subset and
  duplicate checks, and label/position bounds.
- Magento customer reads with a PII-safe projection:
  `magento.customer.get` and `magento.customer.search` (exact email filter).
  Responses carry id, group, website, store, and timestamps only; email, name,
  and addresses are never echoed, and the search `fields` projection does not
  request them.
- Magento catalog discovery: `magento.configurable.children`,
  `magento.attributes.options` (select option value_index for variants), and
  `magento.category.products` (paginated products in a category).
- Magento removal writes: `magento.configurable.remove_child` (unlink a child
  without deleting it) and `magento.inventory.unassign` (remove MSI source
  assignments for up to 100 sku/source pairs). Both require confirmation.
- Magento credit memos: `magento.creditmemo.list` and `magento.creditmemo.get`.
- `magento.product.delete`: destructive write requiring confirmation; Magento
  returns 404 for an unknown SKU, so a repeated delete is not a no-op.

### Fixed

- Write tool schemas now accept `confirmation_id`. Previously only the import
  tool declared it, so the approval retry of every other write tool failed
  schema validation before the handler ran.
- Magento order document reads: `magento.order.status_history`,
  `magento.invoice.list`, `magento.invoice.get`, `magento.shipment.list`, and
  `magento.shipment.get` (tracking numbers included). Booleans that Magento
  returns as 0/1 are decoded flexibly.
- Magento media and inventory reads: `magento.product.media`,
  `magento.inventory.status` (catalog quantity/in-stock/backorders), and
  `magento.inventory.source_items` (MSI per-source quantities for up to 20 SKUs).
- Magento catalog interfaces: `magento.category.get`, `magento.category.tree`
  (flattened depth-first, depth 1-5), `magento.attribute.sets`, and
  `magento.attributes.list`, all read-only with strict id/depth validation.
- Magento order interfaces: `magento.order.get` (order with line items, no
  personal data), `magento.order.search` (status, state, increment id, creation
  window, store view; items omitted), and `magento.sales.summary` (per-day order
  count, grand-total revenue, and item quantity, bounded to 1000 orders with
  explicit truncation and mixed-currency flags).
## [0.0.1] - 2026-11-19

First public pre-release: a runnable MCP gateway with typed, audited tools
for Magento 2, Cloudflare, and Google Analytics 4.

### Added

- Removed the Redis dependency (ADR-0019): the job queue is now PostgreSQL-only
  (`FOR UPDATE SKIP LOCKED` with leases), the worker requires `database.url`, and
  the Redis configuration, Compose service, and Asynq/Redis dependencies are gone.
- Dockerization: a distroless image with baked configuration and a real
  `HEALTHCHECK`, a development Compose stack that runs with one command, a
  production override that requires every secret, and a CI job that builds the
  image, runs it, and probes health, readiness, and metrics.
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
- GA4 product performance: `ga4.ecommerce.products` reports item-level views,
  add-to-carts, purchases, and item revenue per `itemId`, capability-gated like
  the overview so an untracked property returns `ecommerce_not_tracked`.
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
- Multi-instance routing (ADR-0018): Magento stores, Cloudflare zones, and GA4
  properties can share one gateway; tools resolve the requested instance, RBAC
  and audit are instance-aware, and Magento writes keep two-step confirmation.
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

[Unreleased]: https://github.com/shuaiZend/mage2mcp/compare/v0.0.1...HEAD
[0.0.1]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.0.1
