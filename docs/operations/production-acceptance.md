# Production read-only acceptance (2026-11-20)

## Scope and safety

- Instance: `https://www.giztop.com`, store `default`, credential form
  `bearer:<token>` (integration token supplied by the operator).
- The gateway ran on loopback with **`tools.writes: false`**. Only the 37
  read-only Magento tools plus `platform.info` were registered; no write,
  destructive, or confirmation-gated tool was available or called.
- No customer records were read (the PII-bearing `customer.get` /
  `customer.search` tools were intentionally left out of the checks).

## Results

| Check | Result |
|---|---|
| `initialize`, `tools/list` | 38 tools (37 Magento read + platform), writes disabled |
| `platform.info` | OK |
| `product.get`, `product.search` (35k products) | OK |
| `attribute.sets`, `attributes.list` | OK (80 attributes on set 10) |
| `category.tree`, `category.products` | OK |
| `inventory.sources`, `inventory.stocks`, `inventory.source_items` | OK |
| `order.search`, `order.get`, `order.status_history` | OK |
| `invoice.list`, `shipment.list`, `creditmemo.list` | OK (after the fixes below) |
| `product.media`, `product.options`, `product.seo` | OK |
| `product.tier_prices` | OK (after the fallback below) |
| `product.links` | OK (after the fallback below) |
| `configurable.children` | OK |
| `sales.summary` (7-day window) | OK |

## Findings and fixes

The first run failed on five reads. Each failure reflected a real difference
between the documented Magento surface and this deployment:

1. **Tier prices**: `GET /V1/products/tier-prices` returns 404 here. The product
   resource carries the same `tier_prices` array, so the tool now falls back to
   `GET /products/{sku}` instead of failing.
2. **Product links**: `GET /V1/products/{sku}/links` returns 404 here. The tool
   now falls back to the product resource's `product_links` array and filters by
   link type.
3. **Order status history**: `GET /orders/{id}/comments` returns an envelope
   (`items`, `total_count`), not a bare array. The parser now accepts both.
4. **Invoice/shipment/credit-memo lists**: `GET /orders/{id}/invoices` (and the
   shipment/credit-memo equivalents) return 404. The tools now use the
   collection endpoints (`/invoices`, `/shipments`, `/creditmemos`) with an
   `order_id` search filter, which is the supported Magento route.

After the fixes, all 23 read checks passed. The changes are covered by unit
tests in `internal/magento` (`tier_prices_test.go`, `product_links_test.go`,
`order_documents_test.go`).

## Notes

- Tool argument-validation failures are returned as text without a
  `structuredContent.code`; the text names the offending properties. A
  structured error envelope for every tool is a future improvement.
- Endpoint availability varies by Magento build and hosting configuration. The
  fallbacks above make the read surface resilient without changing the tool
  contract.
