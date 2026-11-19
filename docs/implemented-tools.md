# Implemented tools

This is the list of tools the gateway registers today. The design catalog in
[mcp-tools.md](mcp-tools.md) describes the full planned surface and marks which
of its tools are implemented; this page is the honest subset that exists in
code, including tools added or renamed during implementation.

Common behavior:

- Every tool validates its input against a JSON Schema (`additionalProperties:
  false`) before the handler runs.
- Read tools are registered by default. Write tools are registered only when
  `tools.writes: true` and require two-step human approval.
- Every call is authorized (default-deny RBAC) and audited (intent + outcome).
- `instance` is schema-validated routing metadata. This release supports one
  instance per module; the value does not select credentials yet.

## Platform

| Tool | Scope | Timeout | Notes |
|---|---|---|---|
| `platform.info` | read | 30s | Name, version, protocol versions, registered tools, configured instances, and whether write tools are enabled |

## Magento 2

| Tool | Scope | Timeout | Notes |
|---|---|---|---|
| `magento.product.get` | read | 30s | One product by SKU |
| `magento.products.get` | read | 30s | Up to 50 products by SKU in one search (duplicates ignored) |
| `magento.product.tier_prices` | read | 30s | Quantity-based tier prices for one SKU |
| `magento.product.seo` | read | 30s | Deterministic SEO/content audit (description, short description, url_key, meta_title, meta_description, meta_keywords) with a score and suggestions |
| `magento.configurable.children` | read | 30s | Child products linked to a configurable product |
| `magento.product.search` | read | 30s | Search by name/SKU/type/status plus price and creation windows |
| `magento.import.validate` | read | 30s | CSV/JSON preview with per-row errors plus bounded SEO/content warnings; never writes |
| `magento.category.get` | read | 30s | One category by id |
| `magento.category.tree` | read | 30s | Category subtree under a root, flattened depth-first (depth 1-5) |
| `magento.category.products` | read | 30s | Products assigned to a category, paginated |
| `magento.customer.get` | read | 30s | One customer with a PII-safe projection (no email, name, or addresses) |
| `magento.customer.search` | read | 30s | Exact-email customer lookup; response omits email, name, and addresses |
| `magento.customer.create` | write + confirmation | 60s | Creates a customer (accepts PII); response is the PII-safe projection |
| `magento.customer.update` | write + confirmation | 60s | Partial update of email/name/group; response is PII-safe |
| `magento.customer.addresses` | read | 30s | PII-safe address projection: country, region, and default flags only |
| `magento.attribute.sets` | read | 30s | Product attribute sets (max 200) |
| `magento.attributes.list` | read | 30s | Attributes of one attribute set |
| `magento.attributes.options` | read | 30s | Options of a select/multiselect attribute (value_index) |
| `magento.product.media` | read | 30s | Media gallery of one product |
| `magento.product.links` | read | 30s | Related, upsell, or cross-sell links of a product |
| `magento.product.media.upload` | write + confirmation | 60s | Adds an image to a gallery; base64 capped at 5 MiB, MIME/filename/types validated |
| `magento.product.media.update` | write + confirmation | 60s | Updates label/position/types/disabled of one gallery entry |
| `magento.product.media.delete` | destructive + confirmation | 60s | Deletes one media entry from a gallery |
| `magento.invoice.create` | write + confirmation | 60s | Invoices an order; `capture` defaults to false and does not charge |
| `magento.shipment.create` | write + confirmation | 60s | Ships order items with up to 20 tracking numbers |
| `magento.creditmemo.create` | write + confirmation | 60s | Refund record with order-item lines and optional adjustments |
| `magento.order.create` | write + confirmation | 60s | Places an order for an existing customer through the admin cart flow; Magento computes totals; stage-tagged failures |
| `magento.order.comment` | write + confirmation | 60s | Adds a status-history comment; a non-empty status also transitions the order |
| `magento.order.hold` | write + confirmation | 60s | Places an order on hold |
| `magento.order.unhold` | write + confirmation | 60s | Releases an order hold |
| `magento.order.cancel` | destructive + confirmation | 60s | Cancels a cancellable order; irreversible |
| `magento.category.create` | write + confirmation | 60s | Creates a category under a parent |
| `magento.category.update` | write + confirmation | 60s | Partial update of name/active/position/menu visibility |
| `magento.category.assign_product` | write + confirmation | 60s | Links a product to a category |
| `magento.product.links.add` | write + confirmation | 60s | Creates a related/upsell/crosssell link between two products |
| `magento.product.links.remove` | write + confirmation | 60s | Deletes a related/upsell/crosssell link |
| `magento.invoice.capture` | write + confirmation | 60s | Captures payment for an existing invoice; charges the payment method |
| `magento.order.email` | write + confirmation | 60s | Sends the order confirmation email |
| `magento.inventory.status` | read | 30s | Catalog quantity, in-stock flag, and backorders for one SKU |
| `magento.inventory.status_batch` | read | 60s | Up to 25 SKUs, each reporting its own result or error |
| `magento.inventory.source_items` | read | 30s | MSI per-source quantities for up to 20 SKUs |
| `magento.inventory.sources` | read | 30s | MSI inventory sources with location fields |
| `magento.inventory.stocks` | read | 30s | MSI inventory stocks |
| `magento.order.get` | read | 30s | One order with line items; no customer email, addresses, or payment data |
| `magento.order.search` | read | 30s | Orders by status, state, increment id, creation window, store view, or customer id; items omitted |
| `magento.order.status_history` | read | 30s | Status history with comment and notification flags |
| `magento.invoice.list` | read | 30s | Invoices of one order |
| `magento.invoice.get` | read | 30s | One invoice by id |
| `magento.shipment.list` | read | 30s | Shipments of one order, including tracking numbers |
| `magento.shipment.get` | read | 30s | One shipment by id |
| `magento.creditmemo.list` | read | 30s | Credit memos of one order |
| `magento.creditmemo.get` | read | 30s | One credit memo by id |
| `magento.sales.summary` | read | 30s | Per-day order count, grand-total revenue, and item quantity over a bounded window (max 1000 orders, truncation reported) |
| `magento.product.create` | write + confirmation | 60s | Simple/virtual/downloadable/bundle create with EAV attributes plus description, short_description, url_key, meta_title, meta_keywords, meta_description (SEO guidance in the schema) |
| `magento.product.update` | write + confirmation | 60s | Merge update of provided fields, including SEO/content fields and category/website reassignment |
| `magento.configurable.create` | write + confirmation | 60s | Children → parent options → links; staged non-atomic errors; parent and children support SEO/content fields |
| `magento.configurable.add_child` | write + confirmation | 60s | Link one variant |
| `magento.inventory.assign` | write + confirmation | 60s | MSI source items, at most 100 per call |
| `magento.import.execute` | write + confirmation | 60s | Bounded batch execution with partial-success report |
| `magento.import.start` | write + confirmation | 30s | Queues a bounded import for the worker (up to 2 attempts on pre-execution failures); optional `idempotency_key` deduplicates submissions; requires the database queue |
| `magento.import.status` | read | 30s | Job state, attempts, error code, live progress (`done`/`total`), and a bounded result report (first 100 failed rows) |
| `magento.import.cancel` | write + confirmation | 30s | Cancels a queued or running import job |
| `magento.product.tier_prices.set` | write + confirmation | 60s | Replaces a SKU's fixed-price tiers (1-50); omitted tiers are removed |
| `magento.product.delete` | destructive + confirmation | 60s | Deletes one product by SKU; irreversible, 404 on unknown SKU |
| `magento.configurable.remove_child` | write + confirmation | 60s | Unlinks a child from a configurable product; child product remains |
| `magento.inventory.unassign` | write + confirmation | 60s | Removes MSI source assignments for up to 100 sku/source pairs |
| `magento.inventory.transfer` | destructive + confirmation | 60s | Moves quantity between two sources for one SKU; refuses an insufficient source; not transactional |

## Cloudflare (read-only)

| Tool | Scope | Timeout | Notes |
|---|---|---|---|
| `cloudflare.analytics.traffic` | read | 60s | Hourly requests/bytes/cache by hostname, country, or status |
| `cloudflare.security.summary` | read | 60s | Security event counts by action; sampled, 31-day retention |
| `cloudflare.security.events` | read | 60s | Bounded event page without client IPs; sampled, 31-day retention |

## Google Analytics 4 (read-only, aggregate-only)

| Tool | Scope | Timeout | Notes |
|---|---|---|---|
| `ga4.analytics.traffic` | read | 60s | Sessions, users, views, engagement by date |
| `ga4.analytics.realtime` | read | 60s | Last 30 minutes, allowlisted dimensions/metrics |
| `ga4.property.metadata` | read | 30s | Available dimensions and metrics |
| `ga4.property.capabilities` | read | 60s | Ecommerce tracking presence with evidence |
| `ga4.ecommerce.overview` | read | 60s | Purchases/revenue per day; returns `ecommerce_not_tracked` instead of zeros when the property has no ecommerce data |
| `ga4.ecommerce.products` | read | 60s | Item-level views, add-to-carts, purchases, and item revenue per `itemId`, capability-gated |

## Unified analytics

| Tool | Scope | Timeout | Notes |
|---|---|---|---|
| `unified.traffic.compare` | read | 60s | Cloudflare requests vs GA4 sessions for a declared binding; ratio is null without sessions |

Field-level requirements and SEO guidance for the Magento writes are in
[magento-fields.md](magento-fields.md).

## Not implemented yet

Customer address tools, order creation, invoice capture,
category and attribute tools, Logpush inspection, GA4 ecommerce reports, and the
remaining unified scenarios. They are designed in [mcp-tools.md](mcp-tools.md)
but are not registered.
