# Copying products from another store

The instructions carry only the red lines. This is the full recipe: a half-copied configurable
product looks right in a list and is unsellable on the storefront.

## Why discovery comes first

A configurable product is a parent whose sellable identity is its children, option values,
attribute set and categories. Copying "the same name and price" produces a product that cannot be bought: no children, option
values missing from the attribute, or an attribute set that does not carry them. **Do not infer an option from a description**;
read the configurable children, not the prose.

## Recipe: copy a product from another store

1. **Discover the source**: `magento.product.get` (type, attribute set, status, visibility,
   `url_key`, price, tier prices, options, media), `magento.configurable.children` (every child and
   its attribute values), `magento.attributes.list` for the whole attribute set, and its categories.
2. **Check what exists on the target**: `magento.attribute.sets`, `magento.attributes.list`,
   `magento.attributes.options`, `magento.categories.flat`. Anything missing is a taxonomy change.
3. **Ask for taxonomy changes in writing.** Creating a category, attribute, option value or
   attribute set changes the store's structure and URLs.
4. **Create in dependency order**: children (`magento.product.create`), parent
   (`magento.configurable.create`), media (`magento.product.media.upload`), categories
   (`magento.category.assign_product`), links (`magento.product.links.add`).
5. **Verify like a shopper**: every child listed, same type and attribute values,
   `magento.inventory.status` showing the parent in stock. A parent whose children have stock
   is still not salable until its own stock says so.
6. **Report the gaps.** A copy is done only when type, options and attribute values match; name the
   product and field that could not be reproduced.

## When the source is not Magento

Shopify, WooCommerce, a static site, a marketplace: their model rarely maps one-to-one, so **do not
translate names** — analyse the source model and map explicitly. Record **how the
source represents variants** (one product with options, or separate products), what its option and
attribute fields actually are, whether its categories are **a tree or a flat tag list**, **whether
it has SKUs at all**, and what it does with currency, tax class, visibility and stock. Then map: a
variant set becomes one configurable product with **one child per variant**; a text or number field
becomes an attribute plus an attribute set that carries it; a flat tag list becomes a category
tree; a source without SKUs needs one invented per variant, agreed with the human. **Confirm every
uncertain mapping before writing**, and again before an update that changes an existing product's
type, options or attributes: say what you are mapping, what the target needs and what you are unsure
about. **A wrong mapping is not visible on the product page** until someone tries to buy, filter or
report on it. **Never create an attribute, attribute set, category, website or store view** to make
a mapping fit without the human agreeing to that change.

## Traps specific to this gateway

- **`price` is the global base price**; a store-scope price or `special_price` decides what a
  shopper pays, so read an order line for what the customer paid.
- **A configurable parent's price is derived from its children**; a price written on the parent may
  read back as 0.
- **Changing `url_key` changes the storefront URL**; API writes create no redirect.
- **Media is per store view**; check the image roles after `magento.product.media.upload`.
- **Tier prices are per customer group** (`magento.customer.groups`); without the group ids you
  copy a price nobody sees.

Mutually exclusive with `magento.product.create` for a configurable parent and
`magento.configurable.create_variations` when the children exist.
