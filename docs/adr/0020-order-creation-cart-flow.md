# ADR-0020: Order creation through Magento's cart flow

- **Status:** Accepted
- **Date:** 2026-11-19
- **Deciders:** maintainer, Architect

## Context

Agents can already read orders, invoices, shipments, and credit memos, and can
write comments, holds, and fulfillment documents. The remaining gap is placing an
order. Magento has no single "create order" endpoint for an admin-created order:
the supported path is a cart (quote) followed by shipping information and a
place-order call. Direct order-repository writes require a fully formed quote
with totals, which the gateway cannot compute safely.

## Problem

How can the gateway place an order without inventing totals, and what happens when
a step fails midway?

## Options considered

1. **Direct order repository write.** Rejected: it requires client-supplied
   totals and tax, which would let a caller dictate prices.
2. **Expose cart primitives** (`cart.create`, `cart.add_item`,
   `cart.place_order`) as separate tools. Composable but stateful: the agent must
   carry a cart id across calls, and a failed call leaves a cart behind anyway.
3. **One orchestrated `magento.order.create`** that runs cart → items →
   shipping information → place order in one confirmation-gated call. Magento
   computes all totals.

## Decision

Adopt option 3.

- The tool takes a customer id, item lines, billing and shipping addresses,
  shipping carrier/method codes, and a payment method code. It never accepts
  amounts.
- Each step is a separate Magento call; the stage is recorded on failure
  (`cart`, `cart_items`, `shipping_information`, `place_order`) with the failing
  SKU where applicable.
- Validation is fail-fast before any call: positive customer id, 1-50 unique
  SKUs with positive quantities, required address fields with a two-letter
  uppercase country code and 1-4 street lines, and safe carrier/method/payment
  tokens.
- The tool is a confirmation-gated write. Prerequisites are documented: the
  customer exists, the payment method is enabled, and the shipping method is
  available for the address.

## Rationale

Magento remains the source of truth for prices, taxes, discounts, and stock. A
single tool keeps the agent's contract small and makes the confirmation cover the
whole money-affecting operation. Stage-tagged errors make partial state visible.

## Consequences

### Positive

- Agents can place real orders without computing money.
- One approval covers the complete operation.
- Stage errors name exactly where the flow stopped.

### Negative

- A failure after the cart is created leaves an abandoned cart; Magento has no
  rollback for the cart API. Operators may need to clean up carts.
- The tool depends on store configuration (payment/shipping methods), so a
  misconfigured store fails at placement rather than at validation.
- Retrying after a partial failure creates a new cart and can duplicate an order
  if the first attempt actually succeeded but the response was lost. Callers must
  check `magento.order.search` before retrying.

### Security impact

The tool moves money through the configured payment method. It stays behind the
two-step confirmation and the instance-scoped RBAC, and it never accepts amounts
or raw payment data.

### Maintenance cost

Moderate: four Magento calls with stage mapping and tests for the request bodies
and validation. The cart flow is stable Magento 2.4 API.

## Follow-up

- Add an optional `cart_cleanup` on failure if Magento exposes a reliable
  delete-cart endpoint for admin carts.
- Consider a dry-run that validates addresses and shipping methods without
  placing the order.

## References

- [Magento fields](../magento-fields.md)
- [Implemented tools](../implemented-tools.md)
