# Implemented tools

This is the list of tools the gateway registers today. The design catalog in
[mcp-tools.md](mcp-tools.md) describes the full planned surface; this page is
the honest subset that exists in code.

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
| `magento.configurable.children` | read | 30s | Child products linked to a configurable product |
| `magento.product.search` | read | 30s | Search with filters, sorting, page size |
| `magento.import.validate` | read | 30s | CSV/JSON preview with per-row errors; never writes |
| `magento.category.get` | read | 30s | One category by id |
| `magento.category.tree` | read | 30s | Category subtree under a root, flattened depth-first (depth 1-5) |
| `magento.category.products` | read | 30s | Products assigned to a category, paginated |
| `magento.customer.get` | read | 30s | One customer with a PII-safe projection (no email, name, or addresses) |
| `magento.customer.search` | read | 30s | Exact-email customer lookup; response omits email, name, and addresses |
| `magento.attribute.sets` | read | 30s | Product attribute sets (max 200) |
| `magento.attributes.list` | read | 30s | Attributes of one attribute set |
| `magento.attributes.options` | read | 30s | Options of a select/multiselect attribute (value_index) |
| `magento.product.media` | read | 30s | Media gallery of one product |
| `magento.inventory.status` | read | 30s | Catalog quantity, in-stock flag, and backorders for one SKU |
| `magento.inventory.source_items` | read | 30s | MSI per-source quantities for up to 20 SKUs |
| `magento.order.get` | read | 30s | One order with line items; no customer email, addresses, or payment data |
| `magento.order.search` | read | 30s | Orders by status, state, increment id, creation window, or store view; items omitted |
| `magento.order.status_history` | read | 30s | Status history with comment and notification flags |
| `magento.invoice.list` | read | 30s | Invoices of one order |
| `magento.invoice.get` | read | 30s | One invoice by id |
| `magento.shipment.list` | read | 30s | Shipments of one order, including tracking numbers |
| `magento.shipment.get` | read | 30s | One shipment by id |
| `magento.creditmemo.list` | read | 30s | Credit memos of one order |
| `magento.creditmemo.get` | read | 30s | One credit memo by id |
| `magento.sales.summary` | read | 30s | Per-day order count, grand-total revenue, and item quantity over a bounded window (max 1000 orders, truncation reported) |
| `magento.product.create` | write + confirmation | 60s | Simple/virtual/downloadable/bundle create with EAV attributes |
| `magento.product.update` | write + confirmation | 60s | Merge update of provided fields |
| `magento.configurable.create` | write + confirmation | 60s | Children → parent options → links; staged non-atomic errors |
| `magento.configurable.add_child` | write + confirmation | 60s | Link one variant |
| `magento.inventory.assign` | write + confirmation | 60s | MSI source items, at most 100 per call |
| `magento.import.execute` | write + confirmation | 60s | Bounded batch execution with partial-success report |
| `magento.product.delete` | destructive + confirmation | 60s | Deletes one product by SKU; irreversible, 404 on unknown SKU |
| `magento.configurable.remove_child` | write + confirmation | 60s | Unlinks a child from a configurable product; child product remains |
| `magento.inventory.unassign` | write + confirmation | 60s | Removes MSI source assignments for up to 100 sku/source pairs |

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

## Not implemented yet

Media upload, customer/address tools, invoice/shipment/creditmemo creation,
category and attribute tools, Logpush inspection, GA4 ecommerce reports, and the
remaining unified scenarios. They are designed in [mcp-tools.md](mcp-tools.md)
but are not registered.
