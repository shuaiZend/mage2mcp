# Capabilities: what no tool can do

The authoritative list of what exists is `tools/list` or `platform.info` — never this file, never a
number.

## Not possible with any tool

- **Change the address of a placed order.** Addresses are readable on `magento.order.get`, not
  writable; a wrong address is a cancel-and-recreate or carrier intercept, decided by a human.
- **Create or edit tax rates and rules.** `magento.tax.classes` lists tax classes; the rate and rule
  routes are not exposed, so `tax: 0` may be configuration.
- **Set price or name through `magento.products.bulk_update`.** Both are ignored inside
  `attributes` while it reports `updated`; use `magento.product.update` or a CSV import.
- **Edit a shipment** or **delete a document**: create, read and email; cancel, refund or comment.
- **Refund without an invoice**, or **above what it covers**: a quantity over
  `qty_invoiced - qty_refunded` is rejected, per invoice.
- **Compute a storefront price from the catalogue.** `product.price` is the global base price;
  store-scope price, `special_price` and catalogue rules decide the shopper's price.
- **Trust a stock quantity as availability.** `inventory.status.qty` is legacy, 0 while
  `is_in_stock` is true; per-source numbers are `magento.inventory.source_items`.

## Field rules that surprise

- **Customer addresses need `region_id`** (or `region`) where the country has regions; a country
  code alone is rejected. Read `magento.customer.addresses` and copy the region back.
- **Tier prices are per customer group**: without the group ids the copy is a price nobody sees
  (`magento.customer.groups`).

## Routes this store may not expose

A registered tool can still fail with `unsupported_capability` when the deployment or store does
not expose that route. It is terminal: name the missing capability, do not retry.

## When the answer is no

Say the gateway cannot do it and what a human can do instead; do not simulate the missing step.
