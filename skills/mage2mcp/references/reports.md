# Sales reports

## Why the numbers disagree

- **`revenue` is gross**: every order's `grand_total`, canceled included, refunds not deducted,
  tax and shipping included. Not GA4 revenue, not net sales.
- **`top_skus.revenue` is a line subtotal**: it sums item `row_total` (no shipping, tax or
  discounts), so the list sums to `subtotal`, never to `revenue`.
- **`refunded_amount` is document level** (`total_refunded`, shipping refunds and adjustments
  included); `refunded_items_amount` is the line share only.
- **A day is a store-timezone calendar day**: 23:30 UTC is the next day for a store an hour
  ahead. A date-only `from`/`to` is a day there; `window_local` shows the bounds.

## Recipe: monthly revenue report

1. `magento.store.configs` — confirm timezone and currency once.
2. `magento.sales.summary` — one call per window; capped at 366 days and 1000 orders (oldest
   first). Split long ranges.
3. `magento.sales.analysis` — the same window for tax, discounts, refunds, status/payment mix,
   coupons, top SKUs. Both bucket by the same timezone, so their daily rows must agree; if not,
   report it rather than pick one.
4. Quote `revenue` with its caveat ("canceled included, refunds not deducted") plus
   `refunded_amount`; never call it net revenue.

Mutually exclusive: `magento.order.summary` (deprecated alias of `magento.sales.summary`).

## Recipe: product performance

1. `magento.sales.analysis` — `top_skus`, `skus_total`, `subtotal`.
2. Check it is not a sample: `len(top_skus)` vs `skus_total` (capped at 50).
3. State the denominator: `subtotal` or `revenue`.
Not with: `magento.sales.summary` — its revenue is not the SKU list's subtotal.

## Recipe: refund and cancellation report

1. `magento.sales.analysis` — `refunded_amount`, `refunded_items_amount`, `refunded_qty`,
   `refunded_skus` (capped at 10; `refunded_skus_total` has the true count).
2. `by_status` — canceled revenue is `by_status[canceled].revenue`, never a subtraction.
3. `magento.orders.geography` — `UNKNOWN` = no shipping address (virtual or API-created).
Not with: `magento.sales.summary` — it has no refund or cancellation fields.

## Truncation is reported, not hidden

`sales.analysis` reports `truncated`, `excluded_orders`, `total_available` and `paging`;
`orders.geography` reports `truncated` and `unscanned_orders`. Both keep the **oldest** orders when
capped: quote them as "sample, oldest N". `truncated: true` is honest; hiding it is not.
