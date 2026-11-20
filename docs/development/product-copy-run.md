# Product copy run: Giztop screen protector to the Magento demo (2026-11-20)

## Target and method

- **Source product**:
  <https://www.giztop.com/gorilla-guard-high-clear-glass-screen-protector-for-honor-magic-9-pro-max-2pcs.html>,
  read only through the public page and the read-only Magento REST API
  (`store` = `default`). No writes were made to Giztop.
- **Target**: `https://demo-magento-2.auroracreation.com`, store view `pl`
  (locale `pl_PL`, currency PLN), standard Magento 2 demo that resets every six
  hours. Only the demo was written to.
- **Build under test**: released image
  `ghcr.io/shuaizend/mage2mcp:v0.9.0`, run as a temporary gateway with
  `tools.writes: true`, a PostgreSQL-backed confirmation store, and one Magento
  datasource (`demo`, store code `pl`). `/readyz` reported `tools: 101`.
- **Credentials**: admin bearer token from
  `POST /rest/V1/integration/admin/token` (OAuth 1.0a is rejected by this demo).
  The token, database URL, and admin password are deliberately not recorded
  here; the token expires after about one hour.
- **Approvals**: every write returned `confirmation_required`; the confirmation
  was approved by the separate identity `demo-reviewer` through
  `mage2mcp-admin` (a different binary against the same PostgreSQL store), then
  the same tool call was retried with `confirmation_id`. 28 confirmations were
  approved in total.
- **Transport**: MCP Streamable HTTP (`POST /mcp`) driven by a small Python
  client; responses were read from `structuredContent` and verified against the
  demo's read-only REST API where the MCP read tools were insufficient
  (defects D5 and D6).

## Source product (as read)

| Field | Value |
|---|---|
| SKU | `HNM9PM01` |
| Product id | 37038 |
| Name | Gorilla Guard High Clear Glass Screen Protector for Honor Magic 9 Pro Max (2pcs) |
| Type | `simple` |
| Attribute set | 10 (source-specific; the demo copy uses 4, Default) |
| Price / weight | 14.99 USD / 0.15 |
| Visibility / status | 4 (catalog, search) / 1 |
| Categories | 2 (Default Category) and 25 (`CASE`) |
| Custom option | `For`, `drop_down`, required, values `Honor Magic 9` (`9`), `Honor Magic 9 Pro Max` (`9PM`), `Honor Magic 9 Super Power Edition` (`9SP`) |
| Image | `/h/o/honor_magic_9_pro_max_screen_protector.jpg` |
| URL key | `gorilla-guard-high-clear-glass-screen-protector-for-honor-magic-9-pro-max-2pcs` |

The source copy was rewritten for the demo: the description is a 1.3 KB HTML
block (high-alumina glass, 0.25 mm substrate, 0.33 mm adhesive, 93%+
transmittance, ultrasonic fingerprint support), with a 176-character short
description, a 60-character meta title and a 151-character meta description.
Meta keywords were supplied too, but this Magento has no `meta_keywords`
attribute (D4).

## Copied products (demo)

| SKU | Type | Id | Role | Price | Visibility |
|---|---|---:|---|---:|---|
| `HNM9PM01` | simple | 2078 | faithful copy of the source product | 14.99 | 4 |
| `HNM9PM01-CFG` | configurable | 2082 | variant model of the same product | derived (14.99 from children) | 4 |
| `HNM9PM01-CFG-9` | simple | 2079 | child: Honor Magic 9 | 14.99 | 1 |
| `HNM9PM01-CFG-9PM` | simple | 2080 | child: Honor Magic 9 Pro Max | 14.99 | 1 |
| `HNM9PM01-CFG-9SP` | simple | 2081 | child: Honor Magic 9 Super Power Edition | 14.99 | 1 |

Both products are assigned to categories `Screen Protectors` (id 48) and
`Honor Accessories` (id 49); the images are `gorilla-guard-honor-magic-9-pro-max-screen-protector.jpg`
(media id 3428) and `gorilla-guard-honor-magic-9-pro-max-screen-protector-2pcs.jpg`
(media id 3429).

Two representations exist because the source product is a simple product with a
required `For` custom option, while the released write surface has no working
custom-option creation path on this Magento (D1). The configurable product
reproduces the three `For` values as selectable variants, which is the only way
to expose the choice through the v0.9.0 tools.

## Tool call sequence

Each numbered write was `confirmation_required` → `approved` by `demo-reviewer`
→ retried with the confirmation id.

| # | Tool | Arguments (summary) | Result |
|---:|---|---|---|
| 1 | `magento.category.create` | parent 2, `Screen Protectors`, `screen-protectors` | category 48 |
| 2-4 | `magento.attributes.options.create` | `color`, labels `Honor Magic 9`, `Honor Magic 9 Pro Max`, `Honor Magic 9 Super Power Edition` | option values 218, 219, 220 |
| 5 | `magento.product.create` | full SEO fields, weight 0.15, category 48, quantity 9999 | product 2078 (`HNM9PM01`) |
| 6 | `magento.product.options.create` | `For`, `drop_down`, three values | **failed** `not_found` (D1) |
| 7 | `magento.product.media.upload` | JPEG from the public source URL, base64, 3 image types | media 3428 |
| 8 | `magento.configurable.create` | parent `HNM9PM01-CFG`, attribute 93 (`color`) with values 218-220, three children with price and quantity | parent 2082, children 2079-2081 |
| 9-17 | `magento.product.update` | meta title fix, child names, five variant-specific descriptions | all applied |
| 18 | `magento.product.update` | `weight: "0.2"` (defect probe) | **silently ignored** (D2) |
| 19 | `magento.category.create` | parent 2, `Honor Accessories`, `honor-accessories` | category 49 |
| 20-21 | `magento.category.assign_product` | categories 49, SKUs `HNM9PM01`, `HNM9PM01-CFG` | assigned |
| 22 | `magento.inventory.assign` | 4 SKUs, source `default`, quantity 9999, status 1 | assigned: 4 |
| 23 | `magento.product.media.upload` | JPEG, base64, 3 image types, SKU `HNM9PM01-CFG` | media 3429 |
| 24 | `magento.inventory.assign` | parent `HNM9PM01-CFG`, quantity 9999 | assigned: 1, but stock status unchanged (D3) |
| 25-26 | `magento.product.delete` | control products `TMP-CFG-T1`, `TMP-CFG-T` | deleted: true |
| 27-28 | `magento.product.create` / `magento.product.delete` | price/weight probe `TMP-PRICE-TEST` | created then deleted |

Read-only verification calls: `magento.product.get`,
`magento.configurable.children`, `magento.product.media`,
`magento.product.seo`, `magento.product.search`,
`magento.inventory.source_items`, `magento.category.tree`,
`magento.attributes.list`, `magento.attributes.options`,
`magento.inventory.sources`.

## Final verification

### MCP reads

`magento.product.get` returns the summary record only:

```json
{"product":{"id":2082,"sku":"HNM9PM01-CFG","name":"Gorilla Guard High Clear Glass Screen Protector for Honor Magic 9 Pro Max (2pcs)","type_id":"configurable","status":1,"visibility":4,"price":0,"attribute_set_id":4,"category_ids":[48,49],"website_ids":[1]}}
```

`magento.configurable.children` for `HNM9PM01-CFG` returns the three children
(2079, 2080, 2081) with price 14.99 and visibility 1, but no variant attribute
values (D6).

`magento.product.media` returns one image per product, with
`types: ["image","small_image","thumbnail"]` and the uploaded files.

`magento.inventory.source_items` returns `quantity: 9999`, `status: 1`,
`source_code: default` for all five SKUs.

### Read-only REST verification

Because `product.get` omits custom attributes (D5), the stored SEO fields were
verified through the demo's read-only REST API:

| Field | `HNM9PM01` | `HNM9PM01-CFG` |
|---|---|---|
| `url_key` | `gorilla-guard-high-clear-glass-screen-protector-honor-magic-9-pro-max-2pcs` | `gorilla-guard-honor-magic-9-pro-max-screen-protector-2pcs` |
| `meta_title` | Honor Magic 9 Pro Max Glass Screen Protector \| Gorilla Guard | Gorilla Guard Honor Magic 9 Pro Max Screen Protector |
| `meta_description` | 151 chars | 149 chars |
| `description` | 1,284 chars of HTML | 1,325 chars of HTML |
| `weight` | 0.15 (after the REST workaround) | 0.15 (after the REST workaround) |
| `category_ids` | 48, 49 | 48, 49 |
| `media` | media 3428, 3 image types | media 3429, 3 image types |

The configurable parent exposes `configurable_product_options` for attribute 93
with `value_index` 218, 219 and 220, and `configurable_product_links`
`[2079, 2080, 2081]`. Each child stores the matching `color` value
(`HNM9PM01-CFG-9PM` stores `219`).

### SEO audit

`magento.product.seo` returns 83/100 for all five SKUs. The only non-passing
check is `meta_keywords` (`warn`, "optional ... little ranking value"), and that
attribute cannot be written on this Magento at all (D4). No description, URL key,
meta title or meta description checks fail.

### Storefront check

- `gorilla-guard-high-clear-glass-screen-protector-honor-magic-9-pro-max-2pcs.html`:
  title, meta description, price 14.99 PLN, image and "W magazynie" (in stock).
- `gorilla-guard-honor-magic-9-pro-max-screen-protector-2pcs.html`: title, meta
  description, price 14.99, and a swatch option list containing
  `Honor Magic 9`, `Honor Magic 9 Pro Max` and
  `Honor Magic 9 Super Power Edition`, each mapped to its child product.
  Before the stock-status workaround the page said "Brak w magazynie" (out of
  stock) and rendered no option selector at all (D3).

### Workarounds applied outside MCP

Two writes could not be completed through the v0.9.0 tools and were applied to
the demo with direct REST calls, then verified:

1. `PUT /rest/pl/V1/products/{sku}` with top-level `weight: 0.15` for the five
   SKUs (D2).
2. `PUT /rest/pl/V1/products/HNM9PM01-CFG/stockItems/2082` with
   `is_in_stock: true` to make the configurable parent salable and render its
   options (D3).

Both workarounds are demo-only; no production store was touched.

## MCP defects

| ID | Severity | Tool / area | Evidence | Suspected cause | Proposed fix |
|---|---|---|---|---|---|
| D1 | high | `product.options.create` | `POST /products/HNM9PM01/options` returns 404 `not_found` with `upstream_status: 404` and the Polish "no route" message; a direct `POST` to the same REST path also returns 404 while `GET /products/{sku}/options` returns `200 []` | this Magento exposes the custom-option route read-only; the tool neither preflights nor falls back | return `unsupported_capability` with guidance, and fall back to saving `options` through the product write endpoint |
| D2 | high | `product.create`, `product.update` | create with `weight: "0.15"` returns success, but `GET /products/{sku}` shows `weight: null`; `product.update` with `weight: "0.2"` leaves the old value; a direct REST `PUT` with top-level `weight` persists 0.15 | weight is sent as a `custom_attributes` entry; Magento ignores the static `weight` attribute there | send `weight` as a top-level product field (like `price`) on create and update; add a regression test |
| D3 | high | `configurable.create`, `inventory.assign` | after `configurable.create` plus `inventory.assign` on all children, the parent's legacy stock item is `qty: 0, is_in_stock: false`; the storefront shows "Brak w magazynie" and hides the option selector, so the product cannot be bought; `inventory.assign` on the parent returns `assigned: 1` but changes nothing; a direct stock-item update to `is_in_stock: true` fixes it | no reindex/parent-stock handling after linking children; MSI source items alone do not update the parent's stock status | after linking children, set the parent stock status (or trigger/require reindex) and verify salability; otherwise warn that the product is not yet storefront-visible |
| D4 | medium | `product.create`, `product.update`, `product.seo` | `meta_keywords` is accepted on create/update and the calls succeed, but `GET /products/attributes/meta_keywords` returns 404 (the attribute does not exist on this Magento) and the stored value is null; `product.seo` still warns about it | Magento 2.4 removed/disabled `meta_keywords`; the tool and the audit do not detect the missing attribute | probe the attribute and reject or ignore `meta_keywords` with a clear note; drop the check from the audit when the attribute is absent |
| D5 | medium | `product.get`, `product.search` | both return only core fields (id, sku, name, type, status, visibility, price, attribute set, categories, websites); description, short description, url key, meta fields and weight are absent | the read DTO is a deliberately small summary, but no full-detail read exists | add an `include_attributes`/`full` option or a `product.attributes` read so a write can be verified through MCP |
| D6 | medium | `configurable.children` | children are returned without their variant attribute values (`color` is missing), so the child-to-option mapping cannot be verified | the children DTO drops custom attributes | include the declared variant attributes and values per child |
| D7 | medium | `configurable.create`, `product.update` | parent is created with `price: 14.99`, but the response and `product.get` report `price: 0`; a direct REST `PUT` of the parent price also stays 0 while the children keep 14.99 | Magento ignores the configurable parent price and derives it from children; the tool returns success without saying so | document that the parent price is derived, or warn when the requested price differs from the stored one |
| D8 | medium | `configurable.create` | children are named `<parent name> <child SKU>` (e.g. "... (2pcs) HNM9PM01-CFG-9"); the child schema has no `name` field, so three extra `product.update` calls were needed | child name is generated from the parent name plus SKU | accept an optional per-child `name` (and use the variant label as the default suffix) |
| D9 | low | `configurable.children` | a call with `sku` is rejected with `invalid_argument: unexpected additional properties ["sku"]`; the parameter is `parent_sku`, while `product.get`, `product.seo` and `product.update` use `sku` | inconsistent argument naming across the configurable family | accept `sku` as an alias or rename to `parent_sku` consistently everywhere |
| D10 | low | `product.seo` | 5 passing checks out of 6 scores 83; the optional `meta_keywords` warning is counted as a failure and no weighting or explanation is returned | score is a plain percentage of passing checks | exclude optional checks from the score or report the weighting |
| D11 | low | `product.create` response | the response echoes `price`, categories and websites but not `weight` or the SEO fields, so a dropped field (D2) is invisible at write time | response DTO mirrors the summary read | echo the applied fields, or return the full product record |

## Follow-up

Fix D1-D3 first: they block a faithful, purchasable copy. D2 and D3 are the most
dangerous because the tool reports success while the storefront state is wrong.
Then D4-D6 (verification and SEO correctness), then D7-D11 (API polish).

## What was not verified

- The admin UI and checkout/cart flow were not exercised.
- The demo resets every six hours, so the products exist only for this window.
- The source `For` custom option could not be reproduced as a custom option
  (D1); the configurable variant model is a substitute.
- The demo's indexing/cron schedule was not inspected; D3 was worked around at
  the stock-item level, not by reindexing.
