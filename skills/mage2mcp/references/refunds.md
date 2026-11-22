# Refunds and credit memos

A refund is a credit memo. The gateway reads the order and its invoices first, so the mistakes
below are rejected before Magento is asked — the money questions are yours.

## Why the defaults look strange

- **Shipping follows `shipping_amount`.** Omit it and a partial refund returns no shipping; a memo
  consuming the last refundable units returns the remaining shipping; `0` means "none". The trap
  is the middle case.
- **A quantity above `qty_invoiced - qty_refunded` is rejected** before any refund request, per
  invoice: "one more unit" can exceed the invoice, not the order.
- **Adjustments change the total, not the lines.** `adjustment_positive` consumes refundable
  amount, so a later line refund can be rejected as "more than refundable".
- **`captured` is not `state`**: offline methods mark an invoice paid regardless of capture.
- **The invoice link is read back**: `invoice_linked: false` means no stored link.

## Recipe: refund one line (partial refund)

1. `magento.order.get` — item ids, `qty_invoiced`, `qty_refunded`, shipping amount.
2. `magento.invoice.list` — pick the invoice; with several, pass `invoice_id`.
3. `magento.creditmemo.create` — one line per item with the quantity, plus `shipping_amount`
   (omit = none on a partial refund, `0` = explicitly none) and a `comment`.
4. Read `creditmemo.grand_total`, `invoice_linked` and `replayed` (`true` = an identical call
   already refunded; do not refund again).
5. `magento.creditmemo.get` — verify the stored document.

Mutually exclusive with: refunding the same units twice (rejected by the pre-check), and refunding
an order that was never invoiced.

## Recipe: refund the rest and close the order

1. `magento.order.get` — confirm what is still refundable; do not assume "one unit left".
2. `magento.creditmemo.create` — refund every remaining unit; consuming the last refundable units
   refunds the remaining shipping too. Pass `shipping_amount: 0` only if the business keeps it.
3. `magento.creditmemo.get` and `magento.order.get` — check the totals.
Not with: canceling the order — a refund does not cancel, and a canceled order is not refundable.

## Reading refunds back

`magento.creditmemo.list`, `magento.creditmemo.get` (`invoice_id` when Magento stored it),
`magento.creditmemo.email` (not idempotent).

## What a refund is not

It does not restock inventory or change the status beyond what Magento derives: returned goods are
a separate `magento.inventory.transfer`.
