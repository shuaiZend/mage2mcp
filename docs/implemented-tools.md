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
| `magento.product.search` | read | 30s | Search with filters, sorting, page size |
| `magento.import.validate` | read | 30s | CSV/JSON preview with per-row errors; never writes |
| `magento.product.create` | write + confirmation | 60s | Simple/virtual/downloadable/bundle create with EAV attributes |
| `magento.product.update` | write + confirmation | 60s | Merge update of provided fields |
| `magento.configurable.create` | write + confirmation | 60s | Children → parent options → links; staged non-atomic errors |
| `magento.configurable.add_child` | write + confirmation | 60s | Link one variant |
| `magento.inventory.assign` | write + confirmation | 60s | MSI source items, at most 100 per call |
| `magento.import.execute` | write + confirmation | 60s | Bounded batch execution with partial-success report |

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

Write deletes (product/configurable link/inventory unassign), media upload,
category and attribute tools, Logpush inspection, GA4 ecommerce reports, and the
remaining unified scenarios. They are designed in [mcp-tools.md](mcp-tools.md)
but are not registered.
