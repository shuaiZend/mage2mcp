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

The SEO/content fields must not also appear in `attributes`; the gateway rejects
duplicates. Audit existing products with `magento.product.seo`, which returns
pass/warn/fail checks and a score without generating copy.

## Product update (`magento.product.update`)

Same fields as create except `sku` is the path identifier and every field is
optional. Unset fields are not sent, so Magento preserves the stored values.
Setting a field to an empty string clears it.

## Configurable create (`magento.configurable.create`)

- `parent`: same fields as product create plus SEO/content fields.
- `variant_attributes`: 1-5 attributes, each with `attribute_id`, `code`, and
  1-100 `values` carrying the `value_index` from
  `magento.attributes.options`.
- `children`: 1-50 products. Each child must carry a value for **every** variant
  attribute, that value must be one of the declared `value_index` values, and no
  two children may describe the same combination. Children accept the same
  SEO/content fields as the parent (their own slug and metadata).
- Creation order is children -> parent -> links. Failures report the stage and
  completed SKUs; nothing is rolled back.

## Inventory

| Tool | Fields | Notes |
|---|---|---|
| `magento.inventory.assign` | `items[]`: `sku`, `source_code`, `quantity` (>=0), `status` (0/1) | 1-100 items; quantity replaces, it does not add. |
| `magento.inventory.unassign` | `items[]`: `sku`, `source_code` | 1-100 pairs; duplicate pairs rejected. |

## Media (`magento.product.media.upload`)

`filename` (safe name), `mime_type` (jpeg/png/webp/gif), `data_base64` (<= 5 MiB
decoded), optional `label` (<= 255), `position` (0-1000), and `types` (subset of
`image`, `small_image`, `thumbnail`, `swatch_image`).

## Customer addresses

`magento.customer.addresses` returns country, region, and default flags only;
name, street, city, postcode, and telephone are never returned.

## Orders and documents

| Tool | Key fields |
|---|---|
| `magento.invoice.create` | `order_id`, `items[]` (`order_item_id`, `qty` > 0), `capture` (default false) |
| `magento.invoice.capture` | `invoice_id`; charges the payment method |
| `magento.shipment.create` | `order_id`, `items[]`, optional `tracks[]` (`track_number`, `carrier_code`), `comment`, `notify` |
| `magento.creditmemo.create` | `order_id`, `items[]`, optional non-negative `adjustment_positive` / `adjustment_negative` |
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
`attribute_set_id`, and any other column as a custom attribute. SEO columns
(`description`, `short_description`, `url_key`, `meta_title`, `meta_keywords`,
`meta_description`) are validated with the same rules as the product tools.
`magento.import.validate` reports non-blocking SEO/content warnings (capped at
200) so a batch can be improved before it is queued. `magento.import.execute`
runs bounded batches synchronously;
`magento.import.start` queues them for the worker and needs `database.url`.
