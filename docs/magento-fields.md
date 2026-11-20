# Magento write fields

Field requirements for the Magento write tools. The JSON Schemas are
authoritative; this page explains what to put in each field and why. All write
tools require `instance` and, after approval, `confirmation_id`.

## Product create (`magento.product.create`)

| Field | Required | Rules | Guidance |
|---|---|---|---|
| `sku` | yes | 1-64 chars `[A-Za-z0-9._:-]`, starts alphanumeric | Stable identifier; never reuse a deleted SKU for a different product. |
| `name` | yes | 1-255 bytes, no control characters | Customer-facing name; front-load the main keyword. |
| `attribute_set_id` | yes | positive integer | Use `magento.attribute.sets` to find the right set. |
| `type_id` | yes | `simple`, `virtual`, `downloadable`, `bundle` | Configurable parents use `magento.configurable.create`. |
| `price` | yes | decimal string, up to 4 fraction digits | Base price; variants may override it. |
| `status` | no | `1` enabled, `2` disabled | Defaults to enabled. |
| `visibility` | no | `1`-`4` | Defaults to catalog+search. |
| `website_ids` | no | 1-50 positive ids | Required when the store has multiple websites. |
| `category_ids` | no | up to 500 positive ids | Position follows array order. |
| `quantity` | no | >= 0 | Assigned to the default source after creation. |
| `description` | no | <= 65535 bytes, no control characters | **SEO:** 300+ characters of unique, keyword-relevant copy. |
| `short_description` | no | <= 2048 bytes | 1-2 sentence benefit statement for listings. |
| `url_key` | no | `^[a-z0-9][a-z0-9-]{0,254}$` | **SEO:** lowercase slug with the main keyword, no stop words. |
| `meta_title` | no | <= 255 bytes | **SEO:** 30-60 characters, keyword first, then brand. |
| `meta_description` | no | <= 255 bytes | **SEO:** 120-160 characters with a benefit and call to action. |
| `meta_keywords` | no | <= 255 bytes | Optional; low ranking value, keep it short. |
| `weight` | no | decimal, <= 4 fraction digits | Shipping weight; required by carriers for physical products. |
| `special_price` | no | decimal, <= 4 fraction digits | Promotional price; pair it with the date window. |
| `special_from_date` | no | `YYYY-MM-DD` | Special price start. |
| `special_to_date` | no | `YYYY-MM-DD`, not before the start | Special price end. |
| `cost` | no | decimal, <= 4 fraction digits | Cost of goods for margin reporting. |
| `tax_class_id` | no | integer >= 0 | `0` leaves the store default. |

Tier prices use their own tools: `magento.product.tier_prices` reads them and
`magento.product.tier_prices.set` replaces them (1-50 tiers; each needs `price`,
`customer_group`, and `quantity`, with optional `website_id`). Magento replaces
the SKU's stored tiers, so omitted tiers are deleted.

The SEO/content fields must not also appear in `attributes`; the gateway rejects
duplicates. Audit existing products with `magento.product.seo`, which returns
pass/warn/fail checks and a score without generating copy.

## Product update (`magento.product.update`)

Bulk updates use `magento.products.bulk_update`: the same `status`, `visibility`,
or `attributes` are applied to up to 50 SKUs, one PUT per SKU, with a
per-SKU partial-success report. Name and price are intentionally excluded.

Same fields as create except `sku` is the path identifier and every field is
optional. Unset fields are not sent, so Magento preserves the stored values.
Setting a field to an empty string clears it. Pricing/shipping fields use the
same rules as create. `category_ids` and `website_ids`
replace the product's assignments when provided; an empty array clears them and
omitting them leaves the assignments untouched.

## Configurable create (`magento.configurable.create`)

- `parent`: same fields as product create plus SEO/content fields.
- `variant_attributes`: 1-5 attributes, each with `attribute_id`, `code`, and
  1-100 `values` carrying the `value_index` from
  `magento.attributes.options`.
- `children`: 1-50 products. Each child must carry a value for **every** variant
  attribute, that value must be one of the declared `value_index` values, and no
  two children may describe the same combination. Children accept the same
  SEO/content fields as the parent (their own slug and metadata).
- Creation is a staged workflow: **precheck -> children -> parent -> links
  (verified by read-back) -> stock**. The result always carries `operation_id`,
  `outcome` (`completed` or `partial`), `stages`, `completed_stages`,
  `failed_stage`, and `orphan_skus`.
- Precheck aborts on a pre-existing SKU with `conflict` and zero writes. A
  partial run leaves completed writes in place and reports unlinked children as
  `orphan_skus`; nothing is deleted or compensated.
- Resume by passing the returned `operation_id`: existing children and the parent
  are reused, missing links are repaired, and a resumed run writes no duplicate
  SKU. A fresh run is not idempotent.

## Inventory

| Tool | Fields | Notes |
|---|---|---|
| `magento.inventory.assign` | `items[]`: `sku`, `source_code`, `quantity` (>=0), optional `status` (0/1, default 1) | 1-100 items; quantity replaces, it does not add. |
| `magento.inventory.unassign` | `items[]`: `sku`, `source_code` | 1-100 pairs; duplicate pairs rejected. |
| `magento.inventory.transfer` | `sku`, `from_source`, `to_source`, `quantity` (integer > 0) | Reads both rows, re-reads immediately before writing, then upserts both. Returns `transfer_id`, before/after quantities, and `outcome` (`applied`, `conflict`, `verification_mismatch`, `partial`, `not_applied`, `needs_verification`). Only `applied` is success; `needs_verification` means the post-state is unknown and an operator must decide. Fractional quantities are refused (input is an integer; a fractional read-back is `needs_verification`). Not transactional. |

## Media (`magento.product.media.upload`)

`filename` (safe name), `mime_type` (jpeg/png/webp/gif), `data_base64` (<= 5 MiB
decoded), optional `label` (<= 255), `position` (0-1000), and `types` (subset of
`image`, `small_image`, `thumbnail`, `swatch_image`). `magento.product.media.delete`
takes `sku` and `media_id` and is destructive.

## Customers

`magento.customer.create` accepts email, first/last name, `website_id`, and
optional `store_id`/`group_id`; `magento.customer.update` accepts a customer id
plus at least one of email, first/last name, or group. Both require confirmation
and return the PII-safe projection (no email or name in the response).

## Customer addresses

`magento.customer.addresses` returns country, region, and default flags only;
name, street, city, postcode, and telephone are never returned.
`magento.customer.addresses.create` accepts the full address (first/last name,
street 1-4 lines, city, postcode, two-letter uppercase country, telephone,
optional region) plus `default_billing`/`default_shipping`; the response is the
same PII-safe projection. `magento.customer.addresses.delete` removes an address.

## Orders and documents

| Tool | Key fields |
|---|---|
| `magento.invoice.create` | `order_id`, `items[]` (`order_item_id`, `qty` > 0), `capture` (default false), optional `idempotency_key`. Uses the workflow route `POST /V1/order/{orderId}/invoice`, which honours `capture`; the repository route would silently ignore it. Returns `captured` and `operation_id`. |
| `magento.invoice.capture` | `invoice_id`, optional `idempotency_key`; charges the payment method. An ambiguous outcome is `operation_unknown`, never a retry. |
| `magento.shipment.create` | `order_id`, `items[]`, optional `tracks[]` (`track_number`, `carrier_code`), `comment`, `notify` |
| `magento.creditmemo.create` | `order_id`, `items[]`, optional non-negative `adjustment_positive` / `adjustment_negative` |
| `magento.order.create` | `customer_id`, 1-50 `items` (`sku`, `qty` > 0), billing and shipping addresses (firstname, lastname, street 1-4 lines, city, postcode, two-letter uppercase country, telephone), `shipping_carrier_code`, `shipping_method_code`, `payment_method`, optional `idempotency_key`; totals are computed by Magento. An ambiguous place-order is reconciled by reading the customer's recent orders; an inconclusive read is `operation_unknown`. |
| `magento.order.comment` | `order_id`, `comment` (1-1024), optional `status`, `notify` |
| `magento.order.hold` / `unhold` | `order_id` |

## Categories and links

| Tool | Key fields |
|---|---|
| `magento.category.create` | `parent_id` (2 is the default root), `name` (1-255), `is_active`, `position` (0-10000), `include_in_menu` |
| `magento.category.update` | `category_id` plus at least one of `name`, `is_active`, `position`, `include_in_menu` |
| `magento.category.assign_product` | `category_id`, `sku`, optional `position` |
| `magento.product.links.add` / `remove` | `sku`, `link_type` (`related`/`upsell`/`crosssell`), `linked_product_sku`; self-links rejected |

## Imports

CSV/JSON rows use `sku`, `name`, `type_id`, `price`, optional
`attribute_set_id`, and any other column as a custom attribute. The SEO and
pricing columns are routed to their typed fields during execution (never sent
as duplicate attributes). SEO columns
(`description`, `short_description`, `url_key`, `meta_title`, `meta_keywords`,
`meta_description`) are validated with the same rules as the product tools.
`magento.import.validate` reports non-blocking SEO/content warnings (capped at
200) so a batch can be improved before it is queued. Only CSV and JSON are
accepted (no XLSX or gzip); the decoded payload is capped at 8 MiB and 5,000
rows with 64 columns, and the base64 form must fit the request body cap (default
16 MiB; a write-enabled deployment must configure at least 12 MiB).
`magento.import.resume` re-queues only the failed rows of a previous job, read
from the persisted import batch when one exists (unbounded by the 100-row
report), and is idempotent per source job. `magento.import.execute` runs bounded
batches synchronously; `magento.import.start` queues them for the worker and
needs `database.url`.

## Limits and pagination

| Surface | Limit |
|---|---|
| Product batch read (`products.get`) | 50 SKUs per call |
| Bulk update (`products.bulk_update`) | 50 SKUs per call, one PUT each |
| Bulk links (`product.links.bulk`) | 50 targets per call, one add/remove each |
| Bulk attribute options (`attributes.options.bulk_create`) | 50 labels per call, one create each |
| Variations (`configurable.create_variations`) | 50 children per call; existing SKUs skipped |
| Order search (`order.search`) | `limit` 1-200, cursor for the next page |
| `order.summary` | 1000 orders scanned per window; truncation is reported |
| `sales.analysis` | 20,000 orders per call (10,000 default); truncation is reported |
| Tier prices | 50 tiers per SKU per call |
| Media upload | 5 MiB decoded per file |
| Import | 8 MiB decoded and 5,000 rows per batch, 64 columns; the base64 form must fit `server.max_body_bytes` (default 16 MiB, write-enabled floor 12 MiB); async jobs retry twice |

All limits are enforced before any upstream call; a request above a limit fails
with `invalid_argument` rather than being silently truncated.
