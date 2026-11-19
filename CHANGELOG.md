# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.4.0] - 2026-11-19

Fourth minor pre-release: configurable parent updates and variation creation,
document emails, and the complete MSI source lifecycle (create, link, unlink).

### Added

- `magento.configurable.create_variations` (confirmation-gated): creates simple
  children for an existing configurable parent from explicit SKU/attribute pairs,
  links them, skips existing SKUs (idempotent per SKU), and reports per-row
  partial success.

### Added

- `magento.inventory.stock_source_unlink` (destructive, confirmation-gated):
  detaches a source from a stock; per-source quantities are kept.

### Added

- MSI management: `magento.inventory.source_create` (new source with location)
  and `magento.inventory.stock_source_link` (attach a source to a stock), both
  confirmation-gated.

### Added

- Document emails: `magento.invoice.email`, `magento.shipment.email`, and
  `magento.creditmemo.email` (confirmation-gated). The recipient is the order's
  customer; the gateway never accepts an address.

### Added

- `magento.configurable.update` (confirmation-gated): updates a configurable
  parent's fields, SEO/pricing, and assignments through the product update
  endpoint, refusing SKUs that are not configurable.

## [0.3.0] - 2026-11-19

Third minor pre-release: single media reads, order summary alias, bulk product
links and bulk attribute options, and verified documentation for variant media
and interface limits.

### Added

- `magento.attributes.options.bulk_create` (confirmation-gated): adds up to 50
  option labels to one attribute with a per-label partial-success report and
  duplicate skipping.

### Added

- `magento.product.links.bulk` (confirmation-gated): add or remove one link
  type (related, upsell, crosssell) for up to 50 targets, one call each, with a
  per-target partial-success report.

### Added

- `magento.order.summary`, the design-catalog name for the daily order/revenue
  aggregation already exposed as `magento.sales.summary`; both remain available.

### Documented

- A limits-and-pagination table for the Magento surface (batch sizes, order
  search paging, summary window, tier prices, media, imports).

### Added

- `magento.product.media.get` (read): one gallery entry with file, label,
  position, types, and disabled flag.

### Documented

- Variant images: Magento has no configurable media association endpoint; the
  supported path is uploading and role-tagging the image on the child SKU with
  the existing media tools.

## [0.2.0] - 2026-11-19

Second minor pre-release: persisted import batches with unbounded resume and
retention, customer and address writes, bulk product updates, category deletion,
attribute option lifecycle, media/order extras, and a broader E2E scenario.

### Added

- The end-to-end test now covers bulk product updates, a failing import, resume
  of the failed row, and the import batch list/detail reads.

### Changed

- The worker prunes import batch history older than 30 days every six hours;
  failure rows cascade with their batch, so the tables stay bounded.

### Added

- Import batch reads: `magento.import.batches` (recent batches per instance) and
  `magento.import.batch` (one batch with every failed row), backed by the
  persisted batch tables.

### Changed

- Asynchronous imports persist full batch state (`import_batches`,
  `import_batch_failures`, migration 000004). The worker finalizes each batch
  with every failed row, and `magento.import.resume` reads those rows instead of
  the 100-row job report, falling back to the report for older jobs (ADR-0021).

### Added

- `magento.customer.addresses.update` (confirmation-gated): partial address
  update including the default billing/shipping flags, with a PII-safe response.

### Added

- Customer address writes: `magento.customer.addresses.create` (accepts the full
  address but returns the PII-safe projection) and
  `magento.customer.addresses.delete` (destructive, confirmation-gated).

### Added

- `magento.category.delete` (destructive, confirmation-gated): deletes a
  category and its subtree; products keep existing but lose that assignment.

### Added

- `magento.products.bulk_update` (confirmation-gated): applies the same status,
  visibility, or custom attributes to up to 50 SKUs with a per-SKU partial-success
  report; name and price are excluded, and failed SKUs are never rolled back.

### Added

- `magento.attributes.options.delete` (destructive, confirmation-gated): removes
  a select/multiselect option; Magento rejects the call while products reference
  it.

### Added

- `magento.import.resume` (confirmation-gated): queues a new asynchronous import
  containing only the failed rows of a previous job, using the stored job payload
  and bounded failure report. Resuming the same job twice returns the same new
  job (`resume:<job_id>` idempotency key).

### Added

- `magento.attributes.options.create` (confirmation-gated): adds a select or
  multiselect option, which is how configurable variant values are introduced.

### Added

- `magento.product.media.update` (label/position/types/disabled of one gallery
  entry) and `magento.order.cancel` (destructive, confirmation-gated;
  irreversible and only allowed while the order is cancellable).

## [0.1.0] - 2026-11-19

First minor pre-release: the Magento surface is broad (77 tools), writes are
SEO/pricing aware, confirmations validate arguments first, and the project ships
with a repository end-to-end test.

### Added

- Customer writes: `magento.customer.create` and `magento.customer.update`
  (confirmation-gated). They accept PII because a customer requires it, but both
  return the PII-safe projection and never echo email or name.

### Added

- `docs/compatibility.md` (supported components, interface stability promises,
  version support) and `docs/releases/v0.1.0.md` (draft release notes with
  breaking changes, upgrade steps, verification checklist, known limitations).
- The documentation index now describes the project as a running gateway rather
  than a Phase 0 design baseline.

### Added

- The end-to-end test now asserts two safety properties: a malformed write is
  rejected with `invalid_argument` before a confirmation is issued, and changing
  arguments after approval is rejected with `confirmation_param_mismatch`.

### Added

- `magento.inventory.transfer` (destructive, confirmation-gated): moves
  quantity between two MSI sources for one SKU, refusing an insufficient source
  and documenting that the two-row upsert is not transactional.

### Added

- Magento MSI reads: `magento.inventory.sources` and
  `magento.inventory.stocks` (code/name/enabled/location and stock id/name).

### Changed

- `docs/mcp-tools.md` now reports implementation status: 32 of the 75 design
  tools are marked **Implemented.**, 39 implemented tools are listed outside the
  original catalog (renamed or added during implementation), and the remaining
  design tools are explicitly not yet implemented.

### Added

- Magento tier prices: `magento.product.tier_prices` (read) and
  `magento.product.tier_prices.set` (confirmation-gated full replacement of a
  SKU's fixed-price tiers, 1-50 entries, with duplicate/quantity/group
  validation).

### Fixed

- Write arguments are validated against the tool's JSON Schema **before** a
  confirmation is issued. Previously a malformed write could consume a human
  approval and then fail validation on the retry; it now fails fast with
  `invalid_argument`.

### Added

- The end-to-end test now also covers configurable product creation and order
  creation through the admin cart flow (both through the confirmation gate), with
  fake Magento cart and configurable endpoints.

### Added

- `magento.order.create`: places an order for an existing customer through
  Magento's admin cart flow (cart, items, shipping information, place order).
  Totals are computed by Magento; the tool is confirmation-gated, validates
  addresses/items/method codes before any call, and tags failures by stage.
  ADR-0020 records the decision and its partial-state caveats.

## [0.0.2] - 2026-11-19

Second pre-release: a broad Magento write/read surface, SEO-aware product
fields, asynchronous imports, and a repository end-to-end test.

### Added

- Repository end-to-end assets (`test/e2e/`): a fake Magento REST API, an MCP
  client, and `run.sh`, wired to `make e2e` and a non-required CI job that starts
  PostgreSQL, the worker, and the server, then exercises product creation through
  confirmation, the SEO audit, import validation, and an asynchronous import.

### Fixed

- Import execution routes reserved SEO and pricing columns
  (`description`, `short_description`, `url_key`, `meta_*`, `weight`,
  `special_price`, `special_*_date`, `cost`, `tax_class_id`) to their typed
  fields instead of the custom-attributes map, which the duplicate rejection
  would otherwise fail on every row.

### Verified

- End-to-end against a fake Magento REST API on the deployment host: MCP
  initialize/tools, product create through the two-step confirmation (approval
  by a different identity), SEO audit (83), import validation warnings, and an
  asynchronous import queued, approved, executed by the worker, and reported as
  `created: 1`. A mismatched confirmation retry was correctly rejected with
  `confirmation_param_mismatch`.

### Added
- Typed pricing/shipping fields on product create/update and configurable
  children: `weight`, `special_price`, `special_from_date`, `special_to_date`,
  `cost`, and `tax_class_id`, with decimal/date/range validation and duplicate
  rejection against the raw attributes map.
- `magento.product.media.delete` (destructive, confirmation-gated) and
  `magento.inventory.status_batch` (up to 25 SKUs, sequential with per-SKU
  errors because Magento has no catalog-inventory batch endpoint).
- `magento.product.update` accepts `category_ids` and `website_ids` to replace a
  product's assignments; an empty array clears them and omission leaves them
  untouched. The extension payload now emits explicitly empty arrays instead of
  omitting them.
- `magento.customer.addresses`: PII-safe address projection (country, region,
  default flags) for one customer.
- `magento.order.search` gains a `customer_id` filter.
- Import validation reports bounded SEO/content warnings (missing or short
  description, missing url_key, meta_title outside 30-60, meta_description
  outside 120-160) without failing rows.
- Async import jobs allow two attempts for pre-execution/upstream failures; the
  retry is safe because creates skip existing SKUs and updates are set
  operations.
- A docs contract test asserts `docs/magento-fields.md` and the product write
  schema agree on every SEO field and mention the main write tools.
- `docs/magento-fields.md`: field requirements, limits, and SEO guidance for
  every Magento write tool, linked from the docs index and the tool reference.
- Configurable creation now rejects a child value that is not one of the
  declared `value_index` values and two children describing the same variant
  combination, before any product is written.
- Configurable children accept the same SEO/content fields as the parent
  (description, short_description, url_key, meta_title, meta_keywords,
  meta_description), validated and sent as custom attributes.
- Asynchronous import progress: `ExecuteImportWithProgress` reports done/total
  every 25 rows, and the worker stores throttled progress on the job so
  `magento.import.status` shows live movement before the final report.
- SEO/content fields on Magento product writes: `description`,
  `short_description`, `url_key`, `meta_title`, `meta_keywords`, and
  `meta_description` on create, update, and the configurable parent, with
  length/slug/control-character validation and schema guidance for useful SEO
  ranges. Setting them through the raw attributes map is rejected to avoid
  duplicates.
- `magento.product.seo`: deterministic audit of those fields with pass/warn/fail
  checks, a score, and improvement suggestions (it never generates copy).
- Import rows validate SEO attribute bounds alongside the other columns.
- Magento product search gains `price_from`/`price_to` and
  `created_from`/`created_to` filters (decimal and date validation), and a new
  `magento.products.get` batch read for up to 50 SKUs in one search.
- Asynchronous import hardening: `idempotency_key` on
  `magento.import.start` (a repeated key returns the original job id via the
  queue's unique index), and the worker stores a bounded report (counts plus the
  first 100 failed rows) instead of every row.
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

[Unreleased]: https://github.com/shuaiZend/mage2mcp/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.4.0
[0.3.0]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.3.0
[0.2.0]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.2.0
[0.1.0]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.1.0
[0.0.2]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.0.2
[0.0.1]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.0.1
