# MCP gaps found during the 2026-11-20 business drill-down

Recorded during a read-only analysis of 924 production orders (2026-10-21 to
2026-11-20) using the deployed `v0.7.6` gateway plus direct read-only REST calls
where the gateway had no equivalent. **No binaries were rebuilt for this list.**
These are scheduled for a single follow-up fix, not fixed individually here.

| ID | Type | Area | Evidence | Impact | Proposed fix |
|---|---|---|---|---|---|
| G1 | Gap | Product projection | `magento.product.get` / `products.get` return sku/name/type/status/visibility/price/attribute_set only; category ids and custom attributes are dropped | Category/brand performance needs 624 direct REST product reads; 17.4% of item revenue stays unmapped | Add `category_ids`, `website_ids`, and a curated custom-attribute map to the product projection, or a dedicated `product.categories` read |
| G2 | Gap | Sales analysis | `magento.sales.analysis` has no repeat/new customer, coupon, shipping-method, AOV-band, hour/weekday, refund-by-SKU/category, category mix, or fulfillment-latency dimensions | The drill-down required a separate Python/REST pass; the tool alone cannot answer the boss-level questions | Extend the aggregate with those dimensions (or add `magento.sales.drilldown`) |
| G3 | Gap | Conversion data | No traffic/session source is wired: the deployment has no GA4 datasource, and unified tools require it | Conversion rate cannot be computed from Magento alone; the report can only give order-side proxies | Configure GA4, add a conversion/funnel aggregate, and document the dependency |
| G4 | Gap | Fulfillment | `magento.shipment.list` requires an `order_id`; there is no window-based shipment search | Latency needed 924 per-order calls, so it was read via REST instead | Add `magento.shipments.search` (created_at window, order ids) or a fulfillment aggregate |
| G5 | Gap | Categories | `magento.category.tree` returns a nested tree only; no flat id → path/name lookup | Mapping SKUs to categories needed a client-side tree walk | Return a flat list or add `magento.categories.flat` |
| G6 | Defect | Currency mixing | `sales.analysis` sums `grand_total` across 9 currencies and exposes `currencies` only as a dimension | Blended revenue/AOV are misleading (one PHP order is 112,804) | Add `mixed_currency` plus per-currency totals and AOV |
| G7 | Defect | Timezone | Analysis buckets `created_at` strings as-is; no store-timezone field or parameter | Day/hour boundaries are ambiguous for non-UTC stores | Expose the store timezone and bucket explicitly |
| G8 | Defect | Store label | `by_store` keys contain embedded newlines (`"Main Website\nMain Website Store\nDefault Store View"`) | Ugly/unsafe grouping keys | Normalize to the store view name or store id |
| G9 | Defect | Coupon normalization | Coupon codes appear as `BF2026`/`Bf2026` and `D4`/`d4` | Future coupon reporting would split the same promotion | Normalize case when coupon data is added |
| G10 | Gap | Catalog drift | 193 of 624 parent SKUs from the window are absent from the product catalog (discontinued) | 17.4% of item revenue is "unmapped" and cannot be categorized | Snapshot category/name on order items or fall back to the order item name with an explicit "unmapped" bucket |
| G11 | Gap | Scale | `sales.analysis` caps at 20,000 orders per call with a truncation flag, no cursor | Fine at 924 orders; larger stores must slice windows manually | Add window slicing or a cursor to the tool |
| G12 | Gap | Error ergonomics | Argument-validation failures are text-only; no `structuredContent.code` | Agents must parse text to branch on errors | Return a structured error envelope for every tool (known since the v0.7.1 acceptance) |
| G13 | Gap | Guest checkout | The store records 0 guest orders (all registered), so analysis cannot measure guest-vs-registered conversion | Conversion work needs a guest cohort once enabled | Add a customer-type dimension and revisit after enabling guest checkout |

## Follow-up plan

1. Fix G6, G7, G8 in one `sales.analysis` patch (small, low risk).
2. Add G2/G4 dimensions (repeat/new, coupon, shipping method, AOV bands, latency) in a
   dedicated `sales.drilldown` tool so `sales.analysis` stays small.
3. Add G1/G5 category reads to remove the direct-REST dependency for category mix.
4. Wire GA4 (G3) and expose a conversion aggregate.
5. Address G9–G13 opportunistically.
