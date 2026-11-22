# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.11.7] - 2026-11-22

The persona pass: seven agents with different jobs tested the gateway the way their work
actually uses it, produced 101 findings, and the deduplicated 85 were repaired. Full notes:
[docs/releases/v0.11.7.md](docs/releases/v0.11.7.md).

### Fixed

- **A weight edit silently repriced the product.** `buildUpdatePayload` passed
  `[Weight, SpecialPrice, SpecialFromDate, SpecialToDate, Cost]` to a helper aligned against
  `[special_price, special_from_date, special_to_date, cost, tax_class_id]`, shifting every
  field one slot. Setting weight 9.99 on a product priced 100 put it on sale at 9.99 with no
  error anywhere. The create path was correct, which is why only updates were wrong.
- **Any credit memo or shipment carrying a comment was rejected.** `is_visible_on_front` is an
  integer in Magento; the gateway sent a boolean.
- **A partial refund refunded the whole shipping amount**, with no parameter to control it. It
  now takes `shipping_amount` and refunds remaining shipping only when the last units go.
- **Credit memos were not checked against the invoice's own quantity**, so a refund could
  exceed the invoice it was against.
- **Tier prices were unusable in both directions.** The write treated Magento's
  200-with-an-error-array as success and stored nothing; the read decoded the write interface's
  field names, so price and quantity always came back zero.
- **`order.create` recorded the customer id as the order id**, so an operator reading an
  `operation_unknown` row would look up an unrelated order.
- **The reconciler would have treated an existing customer as proof of an order** once the
  entity type changed; a guard keeps a placement that never happened from resolving as applied.
- **An `operation_unknown` had no way out**: no tool could query it, and the production ledger
  held eleven that could never resolve. `operation.get` / `.list` / `.dispose` now exist, and
  disposal records a human judgement rather than flipping the state.
- **Upstream 400 diagnostics were discarded**, so a missing field arrived as "verify store_code
  and field values"; Magento's reason and field now reach the details, including through staged
  writes.
- **Ambiguous money outcomes carried no `retryable` key at all** rather than an explicit false.
- **`bulk_update` silently dropped `price` and `name`** while reporting the rows updated.
- **`invoice.create` with `capture: false` reported `captured: true`.**
- **The default RBAC policy allowed a viewer to read another instance**; approval now requires a
  human identity rather than any verified token.
- **Reports put a 23:30Z order on different days** in summary and analysis; `refunded_amount`
  was a line subtotal rather than the document total; truncation and paging metadata did not say
  what they meant.
- **The design-contract check had been failing since v0.11.6**, including an assertion that
  demanded `platform.job.get`, a tool that was never built.

### Added

- `magento.operation.get`, `.list` and `.dispose` — query and disposition for an
  `operation_unknown`, which previously had no exit.
- `shipping_amount` on credit memo creation.

### Changed

- `order.create` records the customer as its ledger entity, since the order does not exist when
  the operation begins; the order number is written to `upstream_ref` on success.
- The tool catalog is 105 tools.

## [0.11.6] - 2026-11-22

The semantic-layer pass: seven parallel domain tests, 51 findings, and the two release
blockers they exposed. Full notes: [docs/releases/v0.11.6.md](docs/releases/v0.11.6.md).

### Fixed

- **Partial refunds and partial shipments were applied to the whole order.**
  `/order/{id}/refund` and `/order/{id}/ship` take their parameters at the top level, but the
  gateway wrapped them in `{"entity": {...}}`. Magento ignored the unknown field, received no
  items, and applied the whole order: a refund of 1 of 2 items returned the full invoice
  amount, and a shipment of 1 of 2 items shipped both. The invoice route was already correct,
  which is why only two of the three documents were wrong.
- `order.cancel` reported `canceled: true` when Magento answered `200` with `false`.
- `customer.addresses.create/update` dropped `region_id` on the fallback paths, and an update
  carrying only `region_id` was rejected.
- Credit memo `total_qty` leaked a zero when Magento omitted the field.
- The default RBAC policy did not bind an instance, so a viewer could read another store.
- Every approval audit row was rejected by a CHECK constraint, so approvals had no audit
  trail at all; migration 000009 admits `approve`, `approval` and `approved`.
- A tool error returned as a result (rather than as an error) audited as succeeded.
- `_meta.request_id` and the audit `request_id` were always empty.
- A replayed confirmation was not validated, and a successful replay left an intent row with
  no outcome.
- Ledger-gated write outcomes carried no `request_id`, so money and catalog writes could not
  be correlated.
- Rate-limit exhaustion was reported as `upstream_timeout` instead of `rate_limited`.
- Upstream 400 diagnostics were discarded; Magento's reason and field now reach the error
  details, including through staged writes.
- Inventory `unknown` outcomes were marked retryable; `products.bulk_update` silently folded
  duplicate SKUs and accepted unknown ones.
- Import jobs reported `succeeded` for a partial batch; `batch_id` and `rows` descriptions
  were wrong; cancelling a cancelled job succeeded.
- The RBAC policy was built before the auth branch, so a multi-store deployment with
  authentication disabled could not start.
- `idempotentHint` contradicted the idempotency the tool descriptions state, for 11 tools.
- `cmd/admin` required `-as` even when an approver token supplied the identity.

### Added

- `magento.product.media.get` verified live; all 102 registered tools now have a live case.
- The server instructions require discovering a source product's type, options, attributes
  and categories before copying it, and asking a human before creating any taxonomy —
  including when the source is not Magento and the model needs an explicit mapping.

### Changed

- The live matrix is deterministic: two cases that flipped on uncontrolled state now set up
  their own preconditions.

## [0.11.5] - 2026-11-21

An import fix that would have turned a bulk delisting into a bulk listing, and two rules
in the server instructions for agents that copy products from another store. Full notes:
[docs/releases/v0.11.5.md](docs/releases/v0.11.5.md).

### Added

- **The server instructions now state the product-copy rule.** Before copying a product,
  an agent must record the source's type, every configurable option and dropdown with its
  values, its attribute set and its categories, then diff that against the target with
  `magento.attribute.sets`, `magento.attributes`, `magento.attributes.options`,
  `magento.category.tree` and `magento.product.media.types`. If a category, attribute,
  option value or attribute set is missing, it must stop and ask a human, naming the exact
  one needed: a new category or attribute changes the store for everyone and is not undone
  by deleting the product. A copy is not done until it carries the same type, options and
  attribute values as the source.

  The rule exists because a product was copied as simple when its source was configurable
  with two colour options, and the copy looked finished. `docs/mcp-tools.md` §1.1 carries
  the reference form.

- **The instructions also cover sources that are not Magento.** Shopify, WooCommerce, a
  static site or a marketplace rarely maps one-to-one, and translating field names
  produces a product that looks right and behaves wrong. The agent must analyse how the
  source represents variants, options, attributes, categories, SKUs, currency, tax class,
  visibility and stock, then map each concept onto Magento explicitly — a variant set
  becomes a configurable product with one child per variant, a source field becomes a
  Magento attribute and needs an attribute set that carries it, a flat tag list becomes a
  category tree, and a source without SKUs needs an agreed scheme. Every uncertain mapping
  must be confirmed with a human before writing, and again before an update that changes
  an existing product's type, options or attributes, because a wrong mapping is invisible
  on the product page until someone tries to buy, filter or report on it.

### Fixed

- **An import that said "disabled, not visible" produced an enabled, visible product.**
  `status` and `visibility` are top-level product fields, not EAV attributes, but they
  were not in the reserved-column set, so they were sent as attributes. Magento ignores
  unknown attributes on create, so the operator's intent was discarded silently while the
  import reported success. Found by the production run on the demo store; see
  [docs/audit/production-test-and-fix-plan-zh.md](docs/audit/production-test-and-fix-plan-zh.md).

## [0.11.4] - 2026-11-21

Six defects in the money path, found by exercising the part of the surface that had never
run against a real store. Full notes: [docs/releases/v0.11.4.md](docs/releases/v0.11.4.md).

### Fixed

- **The money path was never verified against a real store.** Six defects, all the same
  class as the order-placement `POST`/`PUT` bug — a route or a response shape assumed
  rather than observed:
  - `order.create`, `invoice.create` and `creditmemo.create` decoded their response as the
    document object, but Magento answers with the new document's **id as a JSON scalar**.
    The decode failed and a successful write was recorded `operation_unknown` with the
    caller told not to retry. This is the root cause of LIVE-8.
  - `shipment.create` used `POST /shipments` and `creditmemo.create` used
    `POST /creditmemos`; neither is a route. They are `POST /order/{id}/ship` and
    `POST /order/{id}/refund`.
  - `shipment.get` and `creditmemo.get` used the plural read routes; Magento is
    inconsistent — invoices are plural, shipments and credit memos singular.
  - `shipment.email` and `creditmemo.email` had the same plural/singular split.

## [0.11.3] - 2026-11-21

Four more defects the v0.11.2 live run exposed once the customer-address failure was out
of the way. Full notes: [docs/releases/v0.11.3.md](docs/releases/v0.11.3.md).

### Fixed

- **Three inventory routes and one field name were wrong.** `inventory.unassign` and
  `inventory.stock_source_unlink` used `DELETE`; Magento's MSI delete endpoints are
  `POST` with a `-delete` suffix, so both returned 404 and never worked.
  `inventory.stock_source_link` sent `position` where Magento requires `priority`.
  Their unit tests asserted the wrong routes, which is why they looked covered.
- **`magento.customer.addresses.update` gave up on a store without a dedicated address
  route.** It now falls back to replacing the address through `PUT /customers/{id}`,
  the same way create already did.
- **`magento.customer.addresses` failed for any customer that has an address.** Magento
  returns a customer address's `region` as an object
  (`{region_code, region, region_id}`) while the order address returns a plain string.
  Both were decoded as a string, so the address array element was rejected and the tool
  reported "unreadable customer payload" — pointing at the envelope rather than the
  element. Found by the v0.11.2 live run; see
  [docs/audit/live-magento-test-report-v0.11.2-zh.md](docs/audit/live-magento-test-report-v0.11.2-zh.md).

## [0.11.2] - 2026-11-21

The defects a live Magento run found, fixed, plus the audit findings that did not need
a design decision. Full notes: [docs/releases/v0.11.2.md](docs/releases/v0.11.2.md).

### Fixed

- **Order placement used `POST`; Magento requires `PUT`.** The money path — order,
  invoice, capture, shipment, credit memo — was unreachable on a standard instance.
- **A placed order could be recorded as definitely not placed.** An ambiguous placement
  with empty reconciliation reads was written `failed / reconciled_not_applied` while
  the order existed, and the caller was told to retry. Empty reads now prove nothing.
- **The reconciler's queue was never polled**, so no UNKNOWN operation could resolve.
- Upstream error text reached the agent (Magento, Cloudflare); `op` carried identifiers.
- An MCP session was not bound to the subject that created it.
- `/metrics` was unauthenticated on a non-loopback listener.
- A credential printed itself under `%v`/`%+v`/`%#v`; the approval CLI trusted a
  self-asserted identity and wrote no audit row.
- Import batches had no lease, so a crashed worker left one `running` forever.
- The admin token refresh held a mutex across the HTTP call, serialising every request
  of an instance.
- The five self-ledgered money tools wrote their outcome audit row after committing.
- `product.create` accepted `bundle`/`downloadable`, producing unsellable products.
- Addresses could not express `region_id`, so order creation failed on Poland.
- Document lists stopped at 100 rows with no total and no truncation flag.
- Attribute option ids returned as a bare number were reported as unreadable.
- Media uploads did not dedupe and moved the `image` role silently.

### Added

- `test/live/` — a harness that drives a deployed gateway against a real store.
- `test/fixtures/magento/` — payloads recorded from a real store, replayed in CI.
- `.github/workflows/live.yml` — runs the read matrix when store secrets are set.
- Per-scope coverage floors in CI (security 85%, core 84%, mcp 78%) as a ratchet.
- Result correlation in MCP `_meta`: `instance`, `tool`, `subject`, `request_id`.

### Changed

- Every required tool input and every output property carries a description, enforced
  at registration by `requireDescribedOutput`.
- The initialize instructions declare tool results to be upstream data, never
  instructions.
- Magento tools declare `openWorldHint: false`; a nil value reads as true.
- Read tools derive their capability from their own namespace.

## [0.11.1] - 2026-11-20

Production-readiness remediation: a full audit of the security, Magento,
reliability, MCP, performance and documentation surfaces, with every confirmed
P0 fixed and independently reviewed. The audit record lives in
[`docs/audit/`](docs/audit/README.md).

The theme of this release is **contracts that were declared but never wired**.
Four were found: the RBAC scope ladder was never applied to a tool call, the
retryability rule was only called from tests, confirmations were bound to
arguments but not to the tool or the instance, and the idempotency table was dead
schema. All four are now enforced and covered by tests that fail when the wiring
is reverted.

### Added

- **Operation ledger** (`internal/storage/operation_ledger.go`, migration
  `000006`) with `pending → executing → {succeeded | failed | unknown}` plus
  `reconciling`, a fencing token per attempt, an append-only transition log, and
  recorded-result replay. A middleware gate opens one operation per write tool;
  five money tools are explicitly self-ledgered because their safety depends on
  pre-flight reads and reconciliation a generic wrapper cannot perform.
- **Reconciler** (`platform:reconcile`) that resolves `unknown` operations by
  reading upstream state, with bounded retries and terminal escalation.
- **Per-instance outbound limits** (`limits:` in configuration): a token bucket
  and concurrency cap per Magento instance. An exhausted budget returns a
  retryable `rate_limited` without reaching the store.
- **Job fencing** (migration `000005`): a lease token per claim, guarded terminal
  transitions, lease renewal, bounded ordered recovery, and cancellation that
  reaches the running handler.
- **Registry-generated** `docs/implemented-tools.md`, with a test that fails when
  the checked-in file drifts from the registry.
- **Independent QA suite** (`test/qa`, `scripts/qa-independent.sh`) covering the
  twelve scenarios the audit called blind spots; it fails on any skipped test.
- `operation_unknown` error code and `pending` audit-decision value (migration
  `000007`).

### Security

- The RBAC scope ladder is enforced at the call boundary: a `viewer` is denied
  write and destructive tools; `operator` is denied destructive.
- A confirmation is bound to its tool and its instance; an approval for
  `order.hold` can no longer be consumed by `order.unhold`.
- Import jobs are tenant- and subject-scoped for status, cancel, resume and batch
  reads, so a job id is no longer a cross-tenant capability.
- `Cf-Access-Jwt-Assertion` is accepted and verified through the same verifier as
  a bearer token, with explicit precedence and duplicate rejection.
- Denied calls are audited as `deny`; intent rows no longer assert `allow`; the
  outcome row shares the ledger transaction for middleware-gated tools.
- Real GA4 account and property identifiers removed from documentation and the
  shipped configuration examples.

### Fixed

- **Money safety**: capture refuses an already-paid invoice with no upstream call;
  a timeout is `unknown`, never retried; credit memos validate invoice linkage and
  refundable quantity before writing; order creation keeps the cart on an
  ambiguous placement and reconciles on `quote_id` rather than a statistical
  guess. Repeat calls replay the recorded outcome.
- **Job queue**: a stale worker can no longer overwrite a newer attempt; the lease
  is renewed; recovery is bounded and cannot deadlock.
- **MSI transfer**: concurrent modification is detected, the write is verified by
  reading back, a partial application names the row it wrote, and
  `needs_verification` reports `applied_rows=unknown` instead of `none`.
- **Configurable products**: creation is a staged workflow with a SKU precheck,
  link verification, resume via `operation_id`, and explicit orphan reporting.
- **Catalog**: media upload accepts Magento's integer entry id; the custom-option
  fallback no longer fabricates a successful option.
- **Sales**: invoice creation uses the workflow route that honours `capture`;
  `store_id` is refused rather than silently ignored.
- **History**: a retried import resumes its open batch instead of opening a second
  one, the by-job lookup is deterministic, and terminal jobs are pruned.
- **Analysis**: silent response truncation is an explicit typed error; capped
  scans report how many rows they excluded.

### Changed

- `server.max_body_bytes` default is 16 MiB (was 1 MiB) so a documented
  8 MiB import actually fits; a write-enabled deployment below 12 MiB now fails at
  startup instead of at the transport.

### Breaking changes

Behaviour that was previously wrong is now enforced. See
[`docs/releases/v0.11.1.md`](docs/releases/v0.11.1.md) for the migration checklist.

- Authorization enforces each tool's declared scope. A deployment that relied on
  a read-only role invoking writes will now be denied — correctly.
- `server.public_url` / `MAGE2MCP_PUBLIC_URL` removed.
- Bindings require all three modules enabled and exactly one Cloudflare and one
  GA4 instance.
- The four `magento.import.*` job tools require `instance`.
- `magento.order.create` rejects a non-zero `store_id`.
- `magento.import.cancel` returns a typed error for an already-terminal job.
- The storage test suite requires `MAGE2MCP_TEST_DATABASE_URL` and is now
  repeatable; each database-backed package uses its own schema.

## [0.11.0] - 2026-11-20

Write-safety guidance and product-copy defect fixes.

### Added

- `write_safety` MCP prompt and write-tool descriptions warn that category,
  attribute/option, and `url_key` changes can alter product URLs and require
  explicit confirmation; `docs/write-safety.md` documents the checklist.

### Fixed

- Product-copy defects D1–D3, D5, D6, D8–D11: custom-option creation falls back to
  the product update endpoint (this demo rejects both paths, so the error now
  carries both statuses and messages); `weight` is a top-level field (no longer
  silently dropped); configurable parents get their stock status after linking and
  report `parent_in_stock`; product reads include SEO/content/weight/custom
  attributes; configurable children expose variant attributes; child/variation
  names are optional; `configurable.children` accepts a `sku` alias; optional SEO
  checks no longer lower the score.

### Breaking changes

None; new response fields are additive. See
[`docs/releases/v0.11.0.md`](docs/releases/v0.11.0.md).

## [0.10.0] - 2026-11-20

Admin-credential auto-token, faster CI, and a dependency advisory fix.

### Added

- Magento `admin:<username>:<password>` credential form: the gateway mints an
  admin bearer token from
  `POST /rest/{store_code}/V1/integration/admin/token`, caches it for 4 hours
  minus 5 minutes of skew, refreshes it before the assumed expiry, and re-mints
  it once on a 401. The token request has a 15 s deadline and never follows a
  redirect; the password and token are never logged, persisted, or echoed in
  errors. `bearer:` and `oauth1:` are unchanged. Admin credentials are more
  powerful than an integration token, so prefer a dedicated API-only admin with
  a least-privilege ACL role.

### Changed

- CI: docs-only changes take a fast path through the classifier; Docker layers are
  shared between the docker and release-dry-run jobs; golangci-lint runs once;
  job-level concurrency and timeouts added; a `ci-gate` job aggregates the result.
- GitHub Actions bumped to the current majors (checkout v7, setup-go v7,
  setup-node v7, upload-artifact v7, download-artifact v8, docker actions
  v4/v6/v7, cosign-installer v4, attest v4, gh-release v3, sbom-action v0.24,
  scorecard v2.5, dependency-review v5) with SHA pins retained.

### Fixed

- `katex` (transitive dev dependency of markdownlint-cli2) overridden to
  `^0.18.2`, closing GHSA-238p-pmpm-9mq7 / CVE-2026-103923.

### Documentation

- Added the product-copy run report (`docs/development/product-copy-run.md`) with
  11 recorded MCP defects (D1–D11), and the GitHub Actions bump review
  (`docs/development/actions-bump-review.md`).

## [0.9.0] - 2026-11-20

Write-verification release: fixes W1–W6, W9, W11, W12 from the Magento demo
verification, adds upstream validation messages and actionable capability
errors, and renames the category tree output key.

### Fixed

- Write verification defects W1–W6, W9, W11, W12:
  media upload and attribute-option create accept Magento's scalar ids and no
  longer report failure after a successful write; media update sends a complete
  entry (`media_type`, `file`) and accepts a boolean acknowledgement; upstream
  400/422 messages are surfaced as `details.upstream_message`; a missing
  place-order route returns `unsupported_capability` with a best-effort cart
  cleanup; missing customer-address routes return `unsupported_capability` with
  guidance; `category.create` accepts `url_key` via `custom_attributes`;
  `inventory.assign` defaults `status` to 1; minimal CSV imports succeed.
- `category.tree` returns its list under `categories` instead of `roots`.

## [0.8.1] - 2026-11-20

Patch release: structured tool-error envelopes (G12) and a documentation refresh.

### Fixed

- G12: every tool now returns a structured error envelope
  (`structuredContent: {code, message, details, retryable}`) for handler errors
  and `invalid_argument` for schema failures, replacing text-only tool errors.

## [0.8.0] - 2026-11-20

Minor release: fixes G1, G2, G4–G9 from the production business drill-down, adds
store config and flat category reads, and extends the analysis dimensions.

### Fixed

- Business analysis gaps G1, G2, G4–G9: the product projection now carries
  category/website ids; `magento.sales.analysis` adds repeat/new customers,
  normalized coupons, shipping-method and AOV-band breakdowns, weekday/hour
  buckets in the store timezone, refunds by SKU, shipment latency, mixed-currency
  flags and per-currency AOV; store names are normalized. New reads:
  `magento.store.configs` and `magento.categories.flat`.

## [0.7.6] - 2026-11-20

Minor release: PII-free business analysis aggregate for order windows.

### Added

- `magento.sales.analysis` (read): a PII-free business summary of an order window
  — orders, revenue, AOV, discounts, shipping, tax, guest share, refunds, daily
  trend, status/payment/store/currency mix, shipping country, and top SKUs.

## [0.7.5] - 2026-11-20

Patch release: geography date bounds are split into separate filter groups so
windows filter correctly, with a larger page size.

### Fixed

- `magento.orders.geography` sends the created_at lower and upper bounds in
  separate filter groups. The production instance drops a group containing two
  conditions on the same field, which made every window return the full order
  collection. Page size is now 500 to cut the number of requests.

## [0.7.4] - 2026-11-20

Patch release: geography truncation is reported accurately with the available
total and a newest-first sample.

### Fixed

- `magento.orders.geography` now reports `total_available` from Magento's
  `total_count`, sorts by `created_at desc` for a deterministic newest-first
  sample, and sets `truncated` when the scan cap hides orders.

## [0.7.3] - 2026-11-20

Minor release: PII-free order geography aggregation by shipping country.

### Added

- `magento.orders.geography` (read): aggregates orders in a window by shipping
  country with order counts and revenue. Only country codes and totals are
  returned; address details and customer PII are never exposed.

## [0.7.2] - 2026-11-20

Patch release: reference reads use the Magento search collections, fixing the
routes shipped in v0.7.1.

### Fixed

- `magento.customer.groups` and `magento.tax.classes` use the Magento search
  collection routes (`/customerGroups/search`, `/taxClasses/search`) and decode
  the `items` envelope; the v0.7.1 release used non-existent collection routes,
  which the read-only verification against the live instance caught.

## [0.7.1] - 2026-11-20

Patch release: production endpoint-variance fixes from the read-only acceptance
and two reference reads (customer groups, tax classes).

### Added

- Reference reads: `magento.customer.groups` (customer group ids for customer
  writes) and `magento.tax.classes` (tax class ids for product writes).

### Fixed

- Production read-only acceptance against `www.giztop.com` found four endpoint
  differences, now handled without changing the tool contract:
  tier prices fall back to the product resource's `tier_prices`; product links
  fall back to `product_links`; order status history accepts the `items`
  envelope; and invoice/shipment/credit-memo lists use the collection endpoints
  with an `order_id` filter instead of the non-existent `/orders/{id}/...`
  routes.

## [0.7.0] - 2026-11-20

Seventh minor pre-release: the full product custom-option lifecycle (read,
create, update, delete).

### Added

- `magento.product.options.update` and `magento.product.options.delete`
  (confirmation-gated), completing the custom-option lifecycle.

### Added

- `magento.product.options.create` (confirmation-gated): adds one custom option,
  with selectable types requiring 1-50 values and fixed/percent pricing.

## [0.6.0] - 2026-11-20

Sixth minor pre-release: tool contract tests, product custom option reads, and
media-type discovery.

### Added

- `magento.product.media.types` (read): media roles available for an attribute
  set, so uploads use valid type codes.

### Added

- `magento.product.options` (read): a product's custom options with each value's
  price and price type.

### Added

- Tool contract tests: the 90 Magento tool names are pinned in
  `testdata/tool-names.txt`, and `docs/implemented-tools.md` is checked against
  the registry, so an accidental rename or doc drift fails CI.

## [0.5.0] - 2026-11-20

Fifth minor pre-release: configurable variant option declaration and a converged
interface scope (catalog equivalents, not-planned list, ADR-0022).

### Documented

- The tool catalog now maps design names to implemented equivalents (11),
  lists tools that are **not planned** with reasons (storefront verification,
  generic job tools, runtime mapping mutation), and separates the remaining
  roadmap items. ADR-0022 records the REST-admin scope decision.

### Added

- `magento.configurable.set_options` (confirmation-gated): declares the variant
  attributes and value ordering of an existing configurable parent (1-10
  options, one save per option).

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

[Unreleased]: https://github.com/shuaiZend/mage2mcp/compare/v0.9.0...HEAD
[0.9.0]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.9.0
[0.8.1]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.8.1
[0.8.0]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.8.0
[0.7.6]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.7.6
[0.7.5]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.7.5
[0.7.4]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.7.4
[0.7.3]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.7.3
[0.7.2]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.7.2
[0.7.1]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.7.1
[0.7.0]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.7.0
[0.6.0]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.6.0
[0.5.0]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.5.0
[0.4.0]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.4.0
[0.3.0]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.3.0
[0.2.0]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.2.0
[0.1.0]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.1.0
[0.0.2]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.0.2
[0.0.1]: https://github.com/shuaiZend/mage2mcp/releases/tag/v0.0.1
