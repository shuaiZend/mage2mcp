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
- `instance` is schema-validated routing metadata. Every tool routes to the
  configured instance (Magento reads and writes; Cloudflare and GA4 reads), and
  each instance has its own credential. Multi-instance unified bindings remain
  the current limitation.

<!-- GENERATED INVENTORY START -->

## Generated inventory

Generated from the tool registry by `go run ./scripts/gen-implemented-tools -write`.
Do not edit the tables by hand; run the generator after changing registration.

### Configuration counts

| Configuration | Tools | Magento |
|---|---:|---:|
| Maximal (all modules, `tools.writes: true`, `database.url` set, bindings configured) | 112 | 101 |
| Read-only default (all modules, no writes, no database) | 54 | 43 |
| Read-only, database, no bindings | 56 | 46 |

`tools.writes` gates 52 Magento write/destructive tools; `database.url` gates 3 import read tools (status/batches/batch); start/cancel/resume require both. Module enablement and bindings gate the Cloudflare, GA4, and unified tools; `platform.info` is always registered.

A database-enabled read-only deployment registers only the three read import tools; start/cancel/resume also require `tools.writes`.

### Platform (1 tool)

| Tool | Scope | Write? | Confirmation | Timeout | Gate | Notes |
|---|---|---|---|---|---|---|
| `platform.info` | read | no | no | 30s | always | Server information: version, protocol versions, registered tools, instances, and the writes flag. |

### Magento 2 (101 tools)

| Tool | Scope | Write? | Confirmation | Timeout | Gate | Notes |
|---|---|---|---|---|---|---|
| `magento.attribute.sets` | read | no | no | 30s | always | Lists the product attribute sets (GET /rest/{store_code}/V1/products/attribute-sets/sets/list), at most 200. |
| `magento.attributes.list` | read | no | no | 30s | always | Lists the attributes of one product attribute set (GET /rest/{store_code}/V1/products/attribute-sets/{setId}/attributes). |
| `magento.attributes.options` | read | no | no | 30s | always | Lists the options of a select or multiselect attribute (GET /rest/{store_code}/V1/products/attributes/{code}/options); configurable variants reference options by value_index. |
| `magento.attributes.options.bulk_create` | write | yes | yes | 120s | tools.writes | Magento write operation: adds up to 50 option labels to one select or multiselect attribute (POST /rest/{store_code}/V1/products/attributes/{code}/options, one call per label) w... |
| `magento.attributes.options.create` | write | yes | yes | 60s | tools.writes | Magento write operation: adds a value to a select or multiselect attribute (POST /rest/{store_code}/V1/products/attributes/{code}/options) on the resolved instance. |
| `magento.attributes.options.delete` | destructive | yes | yes | 60s | tools.writes | Magento write operation: deletes a select or multiselect option (DELETE /rest/{store_code}/V1/products/attributes/{code}/options/{id}) on the resolved instance. |
| `magento.categories.flat` | read | no | no | 30s | always | Flattens the category tree (GET /rest/{store_code}/V1/categories) into id, parent, name, level, and slash-separated path rows, which is what product-to-category mapping needs. |
| `magento.category.assign_product` | write | yes | yes | 60s | tools.writes | Magento write operation: links a product to a category (POST /rest/{store_code}/V1/categories/{id}/products) on the resolved instance. |
| `magento.category.create` | write | yes | yes | 60s | tools.writes | Magento write operation: creates a category under a parent (POST /rest/{store_code}/V1/categories) on the resolved instance. |
| `magento.category.delete` | destructive | yes | yes | 60s | tools.writes | Magento write operation: deletes a category and its subtree (DELETE /rest/{store_code}/V1/categories/{id}) on the resolved instance. |
| `magento.category.get` | read | no | no | 30s | always | Reads one category from the configured Magento 2 REST API (GET /rest/{store_code}/V1/categories/{id}). |
| `magento.category.products` | read | no | no | 30s | always | Lists the products assigned to a category (GET /rest/{store_code}/V1/categories/{id}/products) with pagination. |
| `magento.category.tree` | read | no | no | 30s | always | Reads the category subtree under a root category (GET /rest/{store_code}/V1/categories?rootCategoryId&depth), nested up to depth 1-5. |
| `magento.category.update` | write | yes | yes | 60s | tools.writes | Magento write operation: updates category name, active flag, position, or menu visibility (PUT /rest/{store_code}/V1/categories/{id}) on the resolved instance. |
| `magento.configurable.add_child` | write | yes | yes | 60s | tools.writes | Links an existing simple product to a configurable parent in the configured Magento 2 REST catalog (POST /rest/{store_code}/V1/configurable-products/{sku}/child) using the integ... |
| `magento.configurable.children` | read | no | no | 30s | always | Lists the child products linked to a configurable product (GET /rest/{store_code}/V1/configurable-products/{sku}/children). |
| `magento.configurable.create` | write | yes | yes | 60s | tools.writes | Magento write operation: Creates a configurable product in the configured Magento 2 REST catalog as a staged workflow: precheck, children, parent, links (verified by read-back),... |
| `magento.configurable.create_variations` | write | yes | yes | 120s | tools.writes | Magento write operation: creates simple children for an existing configurable parent and links them (POST /rest/{store_code}/V1/products then POST /rest/{store_code}/V1/configur... |
| `magento.configurable.remove_child` | write | yes | yes | 60s | tools.writes | Magento write operation: unlinks a child product from a configurable product (DELETE /rest/{store_code}/V1/configurable-products/{parentSku}/children/{childSku}) on the resolved... |
| `magento.configurable.set_options` | write | yes | yes | 120s | tools.writes | Magento write operation: declares the variant attributes and value ordering of an existing configurable parent (POST /rest/{store_code}/V1/configurable-products/{sku}/options, o... |
| `magento.configurable.update` | write | yes | yes | 60s | tools.writes | Magento write operation: updates a configurable parent's name, price, status, visibility, attributes, SEO fields, pricing fields, or assignments via the product update endpoint ... |
| `magento.creditmemo.create` | write | yes | yes | 60s | tools.writes | Magento write operation: creates a credit memo (POST /rest/{store_code}/V1/creditmemos) with 1-100 order-item lines and optional adjustments on the resolved instance. |
| `magento.creditmemo.email` | write | yes | yes | 60s | tools.writes | Magento write operation: sends the credit memo email (POST /rest/{store_code}/V1/creditmemos/{id}/emails) on the resolved instance. |
| `magento.creditmemo.get` | read | no | no | 30s | always | Reads one credit memo by id (GET /rest/{store_code}/V1/creditmemos/{id}) with totals and quantities. |
| `magento.creditmemo.list` | read | no | no | 30s | always | Lists the credit memos of one order (GET /rest/{store_code}/V1/creditmemos filtered by order_id) with totals and quantities. |
| `magento.customer.addresses` | read | no | no | 30s | always | Lists a customer's addresses as a PII-safe projection (GET /rest/{store_code}/V1/customers/{id}): country, region, and default flags only; name, street, city, postcode, and tele... |
| `magento.customer.addresses.create` | write | yes | yes | 60s | tools.writes | Magento write operation: adds an address to a customer (POST /rest/{store_code}/V1/customers/{id}/addresses) on the resolved instance. |
| `magento.customer.addresses.delete` | destructive | yes | yes | 60s | tools.writes | Magento write operation: deletes a customer address (DELETE /rest/{store_code}/V1/addresses/{id}) on the resolved instance. |
| `magento.customer.addresses.update` | write | yes | yes | 60s | tools.writes | Magento write operation: partially updates a customer address (PUT /rest/{store_code}/V1/customers/{id}/addresses/{addressId}) on the resolved instance. |
| `magento.customer.create` | write | yes | yes | 60s | tools.writes | Magento write operation: creates a customer (POST /rest/{store_code}/V1/customers) on the resolved instance. |
| `magento.customer.get` | read | no | no | 30s | always | Reads one customer from the configured Magento 2 REST API (GET /rest/{store_code}/V1/customers/{id}). |
| `magento.customer.groups` | read | no | no | 30s | always | Lists customer groups (GET /rest/{store_code}/V1/customerGroups/search) with id, code, and tax class id, which the customer write tools accept as group_id. |
| `magento.customer.search` | read | no | no | 30s | always | Finds customers by exact email (GET /rest/{store_code}/V1/customers/search with an email eq filter). |
| `magento.customer.update` | write | yes | yes | 60s | tools.writes | Magento write operation: updates customer email, first/last name, or group (PUT /rest/{store_code}/V1/customers/{id}) on the resolved instance. |
| `magento.import.batch` | read | no | no | 30s | database.url | Returns one asynchronous import batch by id on the named instance with its counts and every failed row. |
| `magento.import.batches` | read | no | no | 30s | database.url | Lists recent asynchronous import batches for the configured instance (newest first, at most 100) with state, counts, and error code. |
| `magento.import.cancel` | write | yes | yes | 60s | tools.writes + database.url | Magento write operation: Cancels a queued or running asynchronous import job on the named instance. |
| `magento.import.execute` | write | yes | yes | 60s | tools.writes | Magento write operation: executes a bounded CSV or JSON product import (base64, at most 8 MiB and 5000 rows) for the resolved instance. |
| `magento.import.resume` | write | yes | yes | 60s | tools.writes + database.url | Magento write operation: Queues a new asynchronous import on the named instance containing only the failed rows of a previous job (from its bounded report). |
| `magento.import.start` | write | yes | yes | 60s | tools.writes + database.url | Queues a bounded CSV/JSON product import for the worker (at most 8 MiB decoded and 5000 rows, and small enough that its base64 form fits the server body limit, 16 MiB by default... |
| `magento.import.status` | read | no | no | 30s | database.url | Reads the state, attempts, error code, and result summary of an asynchronous import job on the named instance. |
| `magento.import.validate` | read | no | no | 30s | always | Parses and validates a CSV or JSON product import (base64-encoded, at most 8 MiB decoded and 5000 rows; the encoded payload must fit the server body limit, 16 MiB by default) an... |
| `magento.inventory.assign` | write | yes | yes | 60s | tools.writes | Assigns per-source quantities in the configured Magento 2 REST catalog (POST /rest/{store_code}/V1/inventory/source-items) using the integration credential. |
| `magento.inventory.source_create` | write | yes | yes | 60s | tools.writes | Magento write operation: creates an MSI source (POST /rest/{store_code}/V1/inventory/sources) on the resolved instance with code, name, enabled flag, and location. |
| `magento.inventory.source_items` | read | no | no | 30s | always | Lists MSI source quantities for up to 20 SKUs (GET /rest/{store_code}/V1/inventory/source-items with a sku in-filter). |
| `magento.inventory.sources` | read | no | no | 30s | always | Lists the MSI inventory sources (GET /rest/{store_code}/V1/inventory/sources), at most 100, with code, name, enabled flag, and location fields. |
| `magento.inventory.status` | read | no | no | 30s | always | Reads the catalog inventory status of one SKU (GET /rest/{store_code}/V1/stockItems/{sku}): quantity, in-stock flag, and backorders. |
| `magento.inventory.status_batch` | read | no | no | 60s | always | Reads catalog inventory status for up to 25 SKUs (GET /rest/{store_code}/V1/stockItems/{sku} per SKU). |
| `magento.inventory.stock_source_link` | write | yes | yes | 60s | tools.writes | Magento write operation: attaches an MSI source to a stock (POST /rest/{store_code}/V1/inventory/stock-source-links) on the resolved instance with a position. |
| `magento.inventory.stock_source_unlink` | destructive | yes | yes | 60s | tools.writes | Magento write operation: detaches an MSI source from a stock (DELETE /rest/{store_code}/V1/inventory/stock-source-links) on the resolved instance. |
| `magento.inventory.stocks` | read | no | no | 30s | always | Lists the MSI inventory stocks (GET /rest/{store_code}/V1/inventory/stocks), at most 100, with stock id and name. |
| `magento.inventory.transfer` | destructive | yes | yes | 60s | tools.writes | Magento write operation: moves whole units for one SKU between MSI sources (read, re-read to detect a concurrent writer, upsert both rows, read them back) on the resolved instance. |
| `magento.inventory.unassign` | write | yes | yes | 60s | tools.writes | Magento write operation: removes per-source inventory assignments (DELETE /rest/{store_code}/V1/inventory/source-items) on the resolved instance for at most 100 sku/source pairs. |
| `magento.invoice.capture` | write | yes | yes | 60s | tools.writes | Magento write operation: captures the payment for an existing invoice (POST /rest/{store_code}/V1/invoices/{id}/capture) on the resolved instance. |
| `magento.invoice.create` | write | yes | yes | 60s | tools.writes | Magento write operation: invoices an order through the workflow route (POST /rest/{store_code}/V1/order/{orderId}/invoice) on the resolved instance, which is the route that hono... |
| `magento.invoice.email` | write | yes | yes | 60s | tools.writes | Magento write operation: sends the invoice email (POST /rest/{store_code}/V1/invoices/{id}/emails) on the resolved instance. |
| `magento.invoice.get` | read | no | no | 30s | always | Reads one invoice by id (GET /rest/{store_code}/V1/invoices/{id}) with totals and quantities. |
| `magento.invoice.list` | read | no | no | 30s | always | Lists the invoices of one order (GET /rest/{store_code}/V1/invoices filtered by order_id) with totals and quantities. |
| `magento.order.cancel` | destructive | yes | yes | 60s | tools.writes | Magento write operation: cancels an order (POST /rest/{store_code}/V1/orders/{id}/cancel) on the resolved instance. |
| `magento.order.comment` | write | yes | yes | 60s | tools.writes | Magento write operation: appends a status-history comment to an order (POST /rest/{store_code}/V1/orders/{id}/comments) on the resolved instance; a non-empty status also transit... |
| `magento.order.create` | write | yes | yes | 60s | tools.writes | Magento write operation: places an order through the admin cart flow (cart, items, shipping information, then POST /rest/{store_code}/V1/carts/{cartId}/order) on the resolved in... |
| `magento.order.email` | write | yes | yes | 60s | tools.writes | Magento write operation: sends the order confirmation email (POST /rest/{store_code}/V1/orders/{id}/emails) on the resolved instance. |
| `magento.order.get` | read | no | no | 30s | always | Reads one order from the configured Magento 2 REST API (GET /rest/{store_code}/V1/orders/{id}) including its line items. |
| `magento.order.hold` | write | yes | yes | 60s | tools.writes | Magento write operation: places an order on hold (POST /rest/{store_code}/V1/orders/{id}/hold) on the resolved instance. |
| `magento.order.search` | read | no | no | 30s | always | Searches the configured Magento 2 REST API (GET /rest/{store_code}/V1/orders with searchCriteria filter groups) by status, state, increment id, creation window, or store view. |
| `magento.order.status_history` | read | no | no | 30s | always | Reads the status history of one order (GET /rest/{store_code}/V1/orders/{id}/comments), including status, comment, and notification flags. |
| `magento.order.summary` | read | no | no | 30s | always | Deprecated alias for magento.sales.summary. |
| `magento.order.unhold` | write | yes | yes | 60s | tools.writes | Magento write operation: releases an order hold (POST /rest/{store_code}/V1/orders/{id}/unhold) on the resolved instance. |
| `magento.orders.geography` | read | no | no | 120s | always | Aggregates orders created in a window by the country of their shipping address (GET /rest/{store_code}/V1/orders with a created_at filter), returning country codes, order counts... |
| `magento.product.create` | write | yes | yes | 60s | tools.writes | Creates one product in the configured Magento 2 REST catalog (POST /rest/{store_code}/V1/products) using the integration credential. |
| `magento.product.delete` | destructive | yes | yes | 60s | tools.writes | Magento write operation: deletes one product by SKU (DELETE /rest/{store_code}/V1/products/{sku}) on the resolved instance. |
| `magento.product.get` | read | no | no | 30s | always | Reads one product from the configured Magento 2 REST catalog (GET /rest/{store_code}/V1/products/{sku}) using the integration credential. |
| `magento.product.links` | read | no | no | 30s | always | Lists related, upsell, or cross-sell links of a product (GET /rest/{store_code}/V1/products/{sku}/links?linkType=...). |
| `magento.product.links.add` | write | yes | yes | 60s | tools.writes | Magento write operation: creates a related, upsell, or cross-sell link between two products (POST /rest/{store_code}/V1/products/{sku}/links) on the resolved instance. |
| `magento.product.links.bulk` | write | yes | yes | 120s | tools.writes | Magento write operation: adds (POST /rest/{store_code}/V1/products/{sku}/links) or removes (DELETE /rest/{store_code}/V1/products/{sku}/links/{type}/{linkedSku}) one link type (... |
| `magento.product.links.remove` | write | yes | yes | 60s | tools.writes | Magento write operation: deletes a related, upsell, or cross-sell link (DELETE /rest/{store_code}/V1/products/{sku}/links/{type}/{linkedSku}) on the resolved instance. |
| `magento.product.media` | read | no | no | 30s | always | Lists the media gallery of one product (GET /rest/{store_code}/V1/products/{sku}/media). |
| `magento.product.media.delete` | destructive | yes | yes | 60s | tools.writes | Magento write operation: deletes one media entry from a product gallery (DELETE /rest/{store_code}/V1/products/{sku}/media/{id}) on the resolved instance. |
| `magento.product.media.get` | read | no | no | 30s | always | Returns one product gallery entry (GET /rest/{store_code}/V1/products/{sku}/media/{id}) with file, label, position, types, and disabled flag. |
| `magento.product.media.types` | read | no | no | 30s | always | Lists the media roles available for an attribute set (GET /rest/{store_code}/V1/products/media/types/{attributeSetName}) so media uploads use valid type codes. |
| `magento.product.media.update` | write | yes | yes | 60s | tools.writes | Magento write operation: updates one gallery entry (label, position, types, disabled) (PUT /rest/{store_code}/V1/products/{sku}/media/{id}) on the resolved instance. |
| `magento.product.media.upload` | write | yes | yes | 60s | tools.writes | Magento write operation: adds an image to a product gallery (POST /rest/{store_code}/V1/products/{sku}/media) on the resolved instance. |
| `magento.product.options` | read | no | no | 30s | always | Lists a product's custom options (not configurable variant options) (GET /rest/{store_code}/V1/products/{sku}/options), including each value's price and price type. |
| `magento.product.options.create` | write | yes | yes | 60s | tools.writes | Magento write operation: adds one custom option to a product (POST /rest/{store_code}/V1/products/{sku}/options) on the resolved instance. |
| `magento.product.options.delete` | destructive | yes | yes | 60s | tools.writes | Magento write operation: deletes one custom option (DELETE /rest/{store_code}/V1/products/{sku}/options/{optionId}) on the resolved instance. |
| `magento.product.options.update` | write | yes | yes | 60s | tools.writes | Magento write operation: updates one custom option (PUT /rest/{store_code}/V1/products/{sku}/options/{optionId}) on the resolved instance. |
| `magento.product.search` | read | no | no | 30s | always | Searches the configured Magento 2 REST catalog (GET /rest/{store_code}/V1/products with searchCriteria filter groups) using the integration credential. |
| `magento.product.seo` | read | no | no | 30s | always | Audits one product's content and SEO fields (GET /rest/{store_code}/V1/products/{sku}) and returns deterministic pass/warn/fail checks for description, short_description, url_ke... |
| `magento.product.tier_prices` | read | no | no | 30s | always | Lists the quantity-based tier prices of one product (GET /rest/{store_code}/V1/products/tier-prices?skus={sku}), including website, customer group, quantity, and price. |
| `magento.product.tier_prices.set` | write | yes | yes | 60s | tools.writes | Magento write operation: replaces the tier prices of one product (POST /rest/{store_code}/V1/products/tier-prices) on the resolved instance with 1-50 fixed-price tiers. |
| `magento.product.update` | write | yes | yes | 60s | tools.writes | Updates one product in the configured Magento 2 REST catalog Changing url_key changes the storefront URL; Magento does not create a 301 redirect for API writes. |
| `magento.products.bulk_update` | write | yes | yes | 120s | tools.writes | Magento write operation: applies the same status, visibility, or custom attributes to up to 50 products (one PUT per SKU) on the resolved instance. |
| `magento.products.get` | read | no | no | 30s | always | Reads up to 50 products by SKU in one search (GET /rest/{store_code}/V1/products with a sku in-filter). |
| `magento.sales.analysis` | read | no | no | 120s | always | Aggregates an order window into business metrics: orders, revenue, AOV, discounts, shipping, tax, mixed-currency flag, guest share, repeat/new customers, coupons, refunds (total... |
| `magento.sales.summary` | read | no | no | 30s | always | Aggregates Magento orders per day over a bounded window: order count, grand-total revenue, and item quantity. |
| `magento.shipment.create` | write | yes | yes | 60s | tools.writes | Magento write operation: creates a shipment for an order (POST /rest/{store_code}/V1/shipments) on the resolved instance, with 1-100 order-item lines and up to 20 tracking numbers. |
| `magento.shipment.email` | write | yes | yes | 60s | tools.writes | Magento write operation: sends the shipment email (POST /rest/{store_code}/V1/shipments/{id}/emails) on the resolved instance. |
| `magento.shipment.get` | read | no | no | 30s | always | Reads one shipment by id (GET /rest/{store_code}/V1/shipments/{id}) with total quantity and tracking numbers. |
| `magento.shipment.list` | read | no | no | 30s | always | Lists the shipments of one order (GET /rest/{store_code}/V1/shipments filtered by order_id) with total quantity and tracking numbers. |
| `magento.store.configs` | read | no | no | 30s | always | Lists store views (GET /rest/{store_code}/V1/store/storeConfigs) with id, code, website, locale, currencies, timezone, and base URL. |
| `magento.tax.classes` | read | no | no | 30s | always | Lists tax classes (GET /rest/{store_code}/V1/taxClasses/search) with id, name, and type, which product writes accept as tax_class_id. |

### Cloudflare (3 tools)

| Tool | Scope | Write? | Confirmation | Timeout | Gate | Notes |
|---|---|---|---|---|---|---|
| `cloudflare.analytics.traffic` | read | no | no | 60s | always | Read-only. |
| `cloudflare.security.events` | read | no | no | 60s | always | Read-only. |
| `cloudflare.security.summary` | read | no | no | 60s | always | Read-only. |

### Google Analytics 4 (6 tools)

| Tool | Scope | Write? | Confirmation | Timeout | Gate | Notes |
|---|---|---|---|---|---|---|
| `ga4.analytics.realtime` | read | no | no | 60s | always | Reads the last 30 minutes of activity from the Google Analytics 4 Data API for the configured property. |
| `ga4.analytics.traffic` | read | no | no | 60s | always | Reads aggregate traffic from the Google Analytics 4 Data API for the configured property. |
| `ga4.ecommerce.overview` | read | no | no | 60s | always | Read-only aggregate report of GA4 purchases and revenue per day for the configured property. |
| `ga4.ecommerce.products` | read | no | no | 60s | always | Read-only item-level GA4 report: views, add-to-carts, purchases, and item revenue per itemId for the configured property. |
| `ga4.property.capabilities` | read | no | no | 60s | always | Reads the ecommerce capability profile of the configured Google Analytics 4 property: whether any of the eleven standard ecommerce events fired in the last seven complete days, ... |
| `ga4.property.metadata` | read | no | no | 30s | always | Lists the dimension and metric API names available on the configured Google Analytics 4 property, including custom fields. |

### Unified analytics (1 tool)

| Tool | Scope | Write? | Confirmation | Timeout | Gate | Notes |
|---|---|---|---|---|---|---|
| `unified.traffic.compare` | read | no | no | 60s | always | Compares Cloudflare HTTP requests with GA4 sessions for a declared binding and UTC window. |
