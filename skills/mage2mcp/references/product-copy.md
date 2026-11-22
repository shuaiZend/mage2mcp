# Copying products from another store

A half-copied configurable product looks right in a list and is unsellable on the storefront.

## Why discovery comes first

A configurable product's sellable identity is its children, option values, attribute set and
categories. **Do not infer an option from a description**; read the children.

## Recipe: copy a product from another store

1. **Discover.** `magento.product.get` returns the record only: type, attribute set, status,
   visibility, `url_key`, price, description, SEO fields, `category_ids` — **not** media, tier
   prices or custom options. Read those with `magento.product.media`,
   `magento.product.tier_prices`, `magento.product.options`. Then
   `magento.configurable.children` (every child and its values), `magento.attributes.list` for the
   whole set, and `magento.inventory.status` for the parent **and every child** (no stock = visible,
   not buyable).
2. **Check the target**: `magento.attribute.sets`, `magento.attributes.list`,
   `magento.attributes.options`, `magento.categories.flat`. Anything missing is a taxonomy change.
3. **Ask in writing** before creating a category, attribute, option value or attribute set.
4. **Create in one call.** One `magento.configurable.create` builds the children, the parent and the
   links. Do **not** pre-create children with `magento.product.create`: the fresh create refuses
   with `conflict: SKU already exists`. `magento.configurable.create_variations` only adds variants
   to a parent that **already exists**.
5. **Media is two jobs**: the parent gallery **and one image per child**
   (`magento.product.media.upload`). Parent images alone leave every child at `media: []` and the
   storefront `jsonConfig.images` empty, so choosing a colour changes nothing.
6. **Place it**: categories (`magento.category.assign_product`), links
   (`magento.product.links.add`).
7. **Verify like a shopper**: every child listed, same type and attribute values,
   `magento.inventory.status` showing the parent in stock; a parent whose children have stock is
   still not salable until its own stock says so.
8. **Report the gaps.** A copy is done only when type, options and attribute values match; name the
   product and field you could not reproduce.

## When the source is not Magento

Shopify, WooCommerce, a static site, a marketplace: the model rarely maps one-to-one, so **do not
translate names**. Record **how the source represents variants**, what its option and attribute
fields are, whether categories are **a tree or a flat tag list**, **whether it has SKUs at all**,
and what it does with currency, tax class, visibility and stock. Map: a variant set becomes one
configurable product with **one child per variant**; a text or number field becomes an attribute
plus an attribute set carrying it; a flat tag list becomes a category tree; no SKUs means one
invented per variant, agreed with the human. **Confirm every uncertain mapping before writing**,
and again before changing an existing product's type, options or attributes. **A wrong mapping is
not visible on the product page** until someone buys, filters or reports on it. **Never create an
attribute, attribute set, category, website or store view** to make a mapping fit without the
human agreeing to that change.

## Traps specific to this gateway

- **`price` is the global base price**; a store-scope price or `special_price` decides what a
  shopper pays, so read an order line for what the customer paid.
- **A configurable parent's price is derived from its children**; a price written on the parent may
  read back as 0.
- **Changing `url_key` changes the storefront URL**; API writes create no redirect.
- **Media is per store view**; check the image roles after `magento.product.media.upload`.
- **Tier prices are per customer group** (`magento.customer.groups`); without the group ids you
  copy a price nobody sees.
