# MCP tool catalog and contract

**Status:** Phase 0 design baseline. Tool names, scopes, and permissions are
frozen. Full input and output schemas are specified here for the Phase 1–2 tools
and are finalized per tool in Phase 1–2; the two schemas below are the reference
style. Additive changes are allowed; breaking changes require an ADR.

This document defines how tools are named, described, validated, authorized, and
versioned, and lists the initial catalog. It is the contract between the MCP layer
and the module services in [module-boundaries.md](module-boundaries.md).

## 1. Principles

1. **No universal escape hatch.** There is no `execute_api`, `execute_sql`, or
   raw GraphQL tool. Every tool maps to a typed service method.
2. **One tool, one intent.** A tool that does two unrelated things is split.
3. **Schemas are strict.** `additionalProperties: false`, every required field
   listed, enums for fixed values, patterns for identifiers.
4. **Permissions are declared, not inferred.** A tool without a permission is a
   startup error.
5. **Read-only by default.** Write tools are registered only when the module's
   write capability is enabled in configuration.
6. **Results are self-describing.** Every result states its instance, time range,
   and data-quality metadata; analytics results also state time zone and currency.
7. **Upstream text is data, not instructions.** Tool descriptions are static and
   authored by the project; they never interpolate upstream content.

## 2. Naming

```text
<module>.<domain>.<action>
```

- `module`: `magento`, `cloudflare`, `ga4`, `unified`.
- `domain`: the resource or analysis area (`product`, `configurable`,
  `attribute`, `inventory`, `order`, `import`, `category`, `media`, `analytics`,
  `security`, `logs`, `ecommerce`, `traffic`, `property`).
- `action`: a verb, lower_snake_case, no abbreviations that are not obvious
  (`get`, `search`, `create`, `update`, `delete`, `list`, `assign`, `summary`).

Examples: `magento.product.get`, `magento.configurable.add_child`,
`cloudflare.security.events`, `ga4.ecommerce.products`,
`unified.traffic.compare`.

Rules:

- No version suffix in the name. Versioning is handled by additive fields and
  deprecation (see §9).
- No instance-specific names. Instances are a parameter.
- Names are stable once released; renaming requires a deprecation cycle.

## 3. Tool definition fields

| Field | Required | Rules |
|---|---|---|
| `name` | yes | §2 |
| `title` | yes | ≤ 60 characters, human-readable |
| `description` | yes | What it does, what it does not do, data source, read/write |
| `inputSchema` | yes | JSON Schema 2020-12, object root, `additionalProperties: false` |
| `outputSchema` | yes for structured results | Same dialect; every tool that returns data declares one before registration |
| `capability` | yes | Capability ID from the owning module |
| `permission` | yes | `read`, `write`, `destructive`, or `admin` scope string |
| `timeout` | yes | Default 30 s; max 120 s synchronous |
| `annotations` | yes | `readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint` |
| `audit` | implicit | Identity, instance, param hash, outcome recorded for every call |

Annotations must be accurate. A tool that writes and is marked `readOnlyHint:
true` is a defect, because clients use annotations to decide whether to prompt.

## 4. Input schema rules

- Root type is `object`.
- `additionalProperties: false` at every level.
- Identifiers use `pattern` and `maxLength`; SKUs allow `[A-Za-z0-9._:-]` and up
  to 128 characters; attribute codes `[a-z0-9_]+`.
- Dates use RFC 3339 strings with an explicit zone, or a `date` string
  (`YYYY-MM-DD`) for day-granularity reports.
- Time ranges are `{ "start": ..., "end": ... }` and are validated server-side
  against the instance's allowed window (default max 400 days for GA4, 31 days
  for Cloudflare security events).
- Pagination uses `{ "cursor": "...", "limit": N }` with `limit` capped per tool.
- Enums are used for every fixed choice; free-form strings are allowed only where
  the upstream accepts arbitrary text (product name, description) and are length
  bounded.
- No field may contain a URL, SQL fragment, or GraphQL document.
- Defaults are expressed in the schema, not hidden in the handler.

## 5. Result envelope

Every tool returns structured content with a stable shape:

```json
{
  "data": { },
  "meta": {
    "instance": "store-us",
    "source": "magento",
    "retrieved_at": "2026-11-18T12:00:00Z",
    "time_range": { "start": "...", "end": "...", "zone": "UTC" },
    "quality": {
      "sampled": false,
      "partial": false,
      "freshness_seconds": 3600,
      "notes": []
    },
    "pagination": { "next_cursor": null, "total_known": 42 }
  }
}
```

Analytics results add `time_zone` and `currency`. A result missing `meta.source`
or `meta.retrieved_at` is a contract violation.

Structured results are returned in `structuredContent` and, for backward
compatibility, the same JSON is also returned in a `TextContent` block. A result
larger than the 8 KiB inline cap is stored as an object and returned as a
`resource_link`; it is never truncated.

Invalid arguments are rejected by the SDK as protocol errors before the handler
runs. Failures during execution are tool errors (`isError: true`) with a typed
`errs.Code`.

Errors are returned as tool errors (`isError: true`) with:

```json
{
  "error": {
    "code": "confirmation_required",
    "message": "This operation requires confirmation.",
    "retryable": false,
    "details": { "confirmation_id": "...", "expires_at": "2026-11-18T12:15:00Z" }
  },
  "meta": { "correlation_id": "..." }
}
```

## 6. Permissions

| Scope | Meaning | Examples |
|---|---|---|
| `read` | Reads upstream data; no side effects | `magento.product.get`, `ga4.analytics.report` |
| `write` | Creates or updates. **Confirmation is required by default**; an operator may disable it per tool only for additive writes that accept no free-form text | `magento.product.create`, `magento.inventory.assign` |
| `destructive` | Deletes or overwrites. Confirmation is always required | `magento.product.delete`, `magento.configurable.remove_child`, `magento.import.start` |
| `admin` | Inspects gateway or credential metadata only (job metadata, never upstream content or destination credentials) | `cloudflare.logs.jobs` |

Roles map to scopes: `viewer` → read; `analyst` → read; `operator` → read + write;
`admin` → read + write + destructive. A deployment may narrow this per instance.
Cloudflare and GA4 tools are read-only except `cloudflare.logs.jobs`, which is `admin` and returns metadata only.

## 7. Catalog

### 7.1 Magento — products and catalog

| Tool | Scope | Purpose |
|---|---|---|
| `magento.product.get` | read | Fetch one product by SKU, including type-specific attributes **Implemented.** |
| `magento.product.search` | read | Search products with filters, sorting, and pagination **Implemented.** |
| `magento.product.create` | write | Create a simple, virtual, downloadable, or bundle product **Implemented.** |
| `magento.product.update` | write | Update attributes, websites, categories, status, visibility, price **Implemented.** |
| `magento.product.delete` | destructive | Delete a product by SKU **Implemented.** |
| `magento.product.media_list` | read | List media entries and roles for a product |
| `magento.product.media_upload` | write | Upload a base64 image and assign roles |
| `magento.product.media_delete` | destructive | Delete a media entry |
| `magento.category.list` | read | List categories with hierarchy |
| `magento.category.get` | read | Fetch one category **Implemented.** |
| `magento.category.create` | write | Create a category **Implemented.** |
| `magento.category.update` | write | Update a category **Implemented.** |
| `magento.category.assign_products` | write | Assign products to a category |

### 7.2 Magento — attributes and EAV

| Tool | Scope | Purpose |
|---|---|---|
| `magento.attribute.list` | read | List product attributes with scope and type metadata |
| `magento.attribute.get` | read | Fetch one attribute definition |
| `magento.attribute.options` | read | List options for a select-type attribute |
| `magento.attribute.sets` | read | List attribute sets **Implemented.** |
| `magento.attribute.set_attributes` | read | List attributes in a set, with groups and required flags |

### 7.3 Magento — configurable products

| Tool | Scope | Purpose |
|---|---|---|
| `magento.configurable.create` | write | Create a configurable parent with variants in one validated operation **Implemented.** |
| `magento.configurable.get` | read | Fetch a configurable product with options and children |
| `magento.configurable.update` | write | Update parent attributes, status, visibility, websites |
| `magento.configurable.add_child` | write | Link an existing simple product as a variant **Implemented.** |
| `magento.configurable.remove_child` | destructive | Remove a variant link (optionally delete the child) **Implemented.** |
| `magento.configurable.children` | read | List variant children with their attribute values **Implemented.** |
| `magento.configurable.set_options` | write | Declare the variant attributes and option ordering |
| `magento.configurable.create_variations` | write | Create missing child products from an attribute matrix |
| `magento.configurable.verify_storefront` | read | Verify variants through the storefront GraphQL API |

### 7.4 Magento — inventory (MSI)

| Tool | Scope | Purpose |
|---|---|---|
| `magento.inventory.sources` | read | List inventory sources **Implemented.** |
| `magento.inventory.stocks` | read | List stocks and their source links **Implemented.** |
| `magento.inventory.source_items` | read | List per-source quantities for SKUs **Implemented.** |
| `magento.inventory.assign` | write | Assign sources and quantities to products **Implemented.** |
| `magento.inventory.unassign` | destructive | Remove sources from products (clears quantities) **Implemented.** |
| `magento.inventory.transfer` | destructive | Transfer quantities between sources |

### 7.5 Magento — orders, sales, import

| Tool | Scope | Purpose |
|---|---|---|
| `magento.order.get` | read | Fetch one order with items and totals **Implemented.** |
| `magento.order.search` | read | Search orders by date, status, store, customer email hash **Implemented.** |
| `magento.order.summary` | read | Aggregated sales for a time range: revenue, orders, AOV, top SKUs |
| `magento.import.validate` | read | Validate a CSV/JSON/XLSX import through the bounded converter and return a preview **Implemented.** |
| `magento.import.start` | destructive | Start an async import (requires confirmation) **Implemented.** |
| `magento.import.status` | read | Batch status by `batch_id`, counts, and per-item errors **Implemented.** |
| `magento.import.resume` | destructive | Resume a paused or failed batch from its last checkpoint; confirmation is bound to the batch id, not the payload |
| `magento.import.cancel` | write | Cancel a running import **Implemented.** |
| `magento.import.report` | read | Export per-item results |

Import endpoints (`/V1/import/csv`, `/V1/import/json`) are Adobe Commerce features.
On Magento Open Source the import tools return `unsupported_capability` and point to
the chunked REST write path.

### 7.5a Jobs

| Tool | Scope | Purpose |
|---|---|---|
| `platform.job.get` | read | Status, progress, attempt count, and typed error for any job the caller may see |
| `platform.job.cancel` | write | Cooperative cancellation; terminal state is checked before each upstream call |

A tool that returns a job handle always returns a `job_id` usable with these tools.
Jobs are scoped to the caller's identity and to the instances or binding the job touches.

### 7.6 Cloudflare

Cloudflare exposes no "list Logpush datasets" API; dataset identifiers are static
and documented. There is therefore no `cloudflare.logs.datasets` tool. Field-level
entitlement for a dataset is checked per query and reported as
`unsupported_capability` when absent.

| Tool | Scope | Purpose |
|---|---|---|
| `cloudflare.zone.list` | read | List zones visible to the token, with plan names |
| `cloudflare.analytics.traffic` | read | Requests, bytes, cached ratio over time by dimension **Implemented.** |
| `cloudflare.analytics.status_codes` | read | Status-code distribution |
| `cloudflare.analytics.top` | read | Top hostnames, paths, countries, or ASNs where the dataset exposes the field |
| `cloudflare.analytics.cache` | read | Cache hit ratio and origin response breakdown |
| `cloudflare.security.events` | read | Sampled security events with action and rule metadata **Implemented.** |
| `cloudflare.security.summary` | read | Counts by action, service, and rule for a window **Implemented.** |
| `cloudflare.security.bots` | read | Bot-related events where the plan exposes them |
| `cloudflare.logs.query` | read | Request-level queries over the Logpush archive (R2/ClickHouse). Requires Logpush ingestion to be enabled and a `pii:read` grant to return client IPs; otherwise IPs are truncated. Bounded time range and row cap |
| `cloudflare.logs.jobs` | admin | List Logpush job metadata (id, name, enabled, dataset, last error). The destination configuration is never returned, because it contains storage credentials. Cloudflare requires `Logs: Write` even for this GET; registered only when the optional log-inspection profile is enabled |

### 7.7 GA4

| Tool | Scope | Purpose |
|---|---|---|
| `ga4.analytics.report` | read | Validated report over allowlisted dimensions and metrics |
| `ga4.analytics.traffic` | read | Sessions, users, views, engagement over time **Implemented.** |
| `ga4.analytics.acquisition` | read | Channels, sources, campaigns |
| `ga4.analytics.landing_pages` | read | Landing page performance |
| `ga4.analytics.events` | read | Event counts and key events |
| `ga4.analytics.realtime` | read | Last 30 minutes of activity **Implemented.** |
| `ga4.ecommerce.overview` | read | Revenue, transactions, AOV — requires ecommerce capability **Implemented.** |
| `ga4.ecommerce.products` | read | Item-level views, carts, purchases, revenue **Implemented.** |
| `ga4.ecommerce.funnel` | read | View → cart → checkout → purchase with rates |
| `ga4.property.metadata` | read | Available dimensions and metrics for the property **Implemented.** |
| `ga4.property.capabilities` | read | Capability profile: ecommerce present, currency, time zone **Implemented.** |

### 7.8 Unified analytics

`instance` is always present in the schema. When exactly one instance of a module
exists, the server fills it from configuration and the caller may omit it; the
schema does not change between deployments.

Unified tools take `binding` instead of `instance`: a named `(magento, cloudflare, ga4)`
triple declared in configuration. The caller must be authorized for every member
instance; a grant on one member is not sufficient. When exactly one binding exists it
is the default and may be omitted.

| Tool | Scope | Purpose |
|---|---|---|
| `unified.traffic.compare` | read | Cloudflare requests vs GA4 sessions over aligned windows **Implemented.** |
| `unified.traffic.bot_vs_human` | read | Bot-classified requests vs human sessions |
| `unified.security.waf_impact` | read | Security actions against observed traffic and sessions |
| `unified.ecommerce.product_funnel` | read | GA4 item funnel vs Magento sales for matched SKUs |
| `unified.ecommerce.channel_revenue` | read | GA4 channel attribution vs Magento revenue |
| `unified.analytics.sku_performance` | read | Views, carts, orders, and revenue per SKU with mapping confidence |
| `unified.traffic.geography` | read | Country and ASN distribution compared across Cloudflare and GA4 |
| `unified.analytics.decline_correlation` | read | Correlate a traffic decline with a sales decline over aligned windows |
| `unified.mapping.sku_propose` | write | Propose a GA4 itemId to Magento SKU mapping from normalized keys, with confidence |
| `unified.mapping.sku_confirm` | destructive | Confirm or supersede a SKU mapping; requires confirmation bound to the mapping hash |

## 8. Example schemas

### 8.1 `magento.product.search`

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["instance"],
  "properties": {
    "instance": { "type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,63}$" },
    "query": { "type": "string", "maxLength": 256 },
    "sku": { "type": "string", "pattern": "^[A-Za-z0-9._:-]{1,64}$" },
    "type": { "enum": ["simple", "configurable", "virtual", "downloadable", "bundle"] },
    "status": { "enum": ["enabled", "disabled"] },
    "category_id": { "type": "integer", "minimum": 1 },
    "limit": { "type": "integer", "minimum": 1, "maximum": 200, "default": 50 },
    "cursor": { "type": "string", "maxLength": 512 }
  }
}
```

### 8.2 `magento.configurable.create`

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["instance", "idempotency_key", "parent", "variant_attributes", "children"],
  "properties": {
    "instance": { "type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,63}$" },
    "parent": {
      "type": "object",
      "additionalProperties": false,
      "required": ["sku", "name", "attribute_set_id", "price"],
      "properties": {
        "sku": { "type": "string", "pattern": "^[A-Za-z0-9._:-]{1,64}$" },
        "name": { "type": "string", "maxLength": 255 },
        "attribute_set_id": { "type": "integer", "minimum": 1 },
        "price": { "type": "string", "pattern": "^[0-9]+(\\.[0-9]{1,4})?$" },
        "website_ids": { "type": "array", "items": { "type": "integer" }, "minItems": 1 },
        "category_ids": { "type": "array", "items": { "type": "integer" } },
        "status": { "enum": ["enabled", "disabled"], "default": "disabled" },
        "visibility": { "enum": ["not_visible", "catalog", "search", "catalog_search"] }
      }
    },
    "variant_attributes": {
      "type": "array",
      "minItems": 1,
      "maxItems": 5,
      "items": { "type": "string", "pattern": "^[a-z0-9_]{1,64}$" }
    },
    "children": {
      "type": "array",
      "minItems": 1,
      "maxItems": 50,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["sku", "attributes"],
        "properties": {
          "sku": { "type": "string", "pattern": "^[A-Za-z0-9._:-]{1,64}$" },
          "attributes": { "type": "object", "additionalProperties": false, "propertyNames": { "pattern": "^[a-z0-9_]{1,64}$" }, "patternProperties": { "^[a-z0-9_]{1,64}$": { "type": "string", "maxLength": 255 } } },
          "price": { "type": "string", "pattern": "^[0-9]+(\\.[0-9]{1,4})?$" },
          "quantity": { "type": "integer", "minimum": 0 }
        }
      }
    },
    "idempotency_key": {
      "type": "string",
      "maxLength": 128,
      "description": "Required for multi-write operations. A replay with the same key returns the original result instead of creating duplicates."
    },
    "confirmation_id": {
      "type": "string",
      "maxLength": 128,
      "description": "Opaque id returned by a confirmation_required error. It becomes usable only after a different human identity approves it on the approval surface; the requester never receives the approval secret."
    }
  }
}
```

## 9. Versioning and deprecation

- Tool names do not carry versions.
- Additive changes (new optional field, new enum value) are minor releases.
- Removing or renaming a field, changing a type, or changing semantics is a
  breaking change: it requires an ADR, a minor release with both fields, a
  deprecation warning in the description and result metadata, and removal no
  earlier than the next minor.
- A deprecated tool remains registered and functional until removal; it returns
  `meta.quality.notes: ["deprecated: use <new tool>"]`.
- Tool schemas are covered by contract tests; a schema change without a test
  update fails CI.

## 10. Tool description guidelines

Descriptions are read by models and are a prompt-injection surface if they ever
contain upstream data. Rules:

- Static text only, reviewed like code.
- State the data source and the read/write nature in the first sentence.
- State the most important limitation (sampling, freshness, missing ecommerce).
- Never include examples that look like instructions to the agent.
- Never promise a capability that is not guaranteed by the instance.

## 11. Timeouts and long-running work

| Operation class | Timeout | Behavior |
|---|---|---|
| Single read | 30 s | Return error on timeout |
| Search with pagination | 30 s | Return partial page with cursor if upstream is slow |
| Report (GA4, Cloudflare) | 60 s | Return `upstream_timeout` with a narrower-window hint |
| Import start | 30 s | Returns job handle; execution is async |
| Import status | 15 s | Reads local job state, not upstream |

Long-running tools honor `progressToken` with `notifications/progress` and stop
upstream work when `notifications/cancelled` arrives. The cancellation path is
tested by asserting that no upstream request is issued after cancellation.
| Storefront verification | 60 s | May wait for cache warm-up; documented |

## 12. Open questions

| Question | Owner | Resolution point |
|---|---|---|
| Whether `ga4.analytics.report` should remain public or be internal-only behind the named tools | GA4 owner | Phase 4 |
| Whether unified tools should accept explicit source mappings or rely on configured SKU maps | Unified owner | Phase 5 |
| Whether tool results should support MCP resource links for large payloads | MCP owner | Phase 2 |

## Implemented tools outside this catalog

These tools exist in the gateway but were not part of the original design table
(mostly because implementation split or renamed a planned tool):

- `magento.attributes.list`
- `magento.attributes.options`
- `magento.category.assign_product`
- `magento.category.products`
- `magento.category.tree`
- `magento.creditmemo.create`
- `magento.creditmemo.get`
- `magento.creditmemo.list`
- `magento.customer.addresses`
- `magento.customer.get`
- `magento.customer.search`
- `magento.import.execute`
- `magento.inventory.status`
- `magento.inventory.status_batch`
- `magento.invoice.capture`
- `magento.invoice.create`
- `magento.invoice.get`
- `magento.invoice.list`
- `magento.order.comment`
- `magento.order.create`
- `magento.order.email`
- `magento.order.hold`
- `magento.order.status_history`
- `magento.order.unhold`
- `magento.product.links`
- `magento.product.links.add`
- `magento.product.links.remove`
- `magento.product.media`
- `magento.product.media.delete`
- `magento.product.media.upload`
- `magento.product.seo`
- `magento.product.tier_prices`
- `magento.product.tier_prices.set`
- `magento.products.get`
- `magento.sales.summary`
- `magento.shipment.create`
- `magento.shipment.get`
- `magento.shipment.list`
- `platform.info`
