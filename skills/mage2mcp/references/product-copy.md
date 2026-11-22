# Copying products from another store

The server's instructions carry only the red lines. This file is the full recipe, because a
half-copied configurable product looks correct in a list and is unsellable on the storefront.

## Why discovery comes first

A product is not a row of fields. A configurable product is a parent whose sellable identity is
the set of its children, its option values, its attribute set and its categories. Copying
"the same name and price" produces a product that exists and cannot be bought: the parent has no
children, the option values are not in the attribute, or the attribute set does not carry the
attributes the copy needs.

**Do not infer an option from a description.** If the source says "available in red and blue",
read the configurable children; do not create option values from prose.

## Recipe: copy a product from another store

1. **Discover the source.** For each product, record:
   - `magento.product.get` — type, attribute set, status, visibility, `url_key`, price, tier
     prices, custom options, media.
   - `magento.configurable.children` — every child SKU, and for each child its attribute values.
   - `magento.attributes.list` (for the attribute set) — the full attribute set, not the fields
     that happen to be filled in.
   - `magento.product.options` — custom options and their price types.
   - `magento.category.products` or the product's categories — where it is listed.
2. **Check what exists on the target.** `magento.attribute.sets`, `magento.attributes.list`,
   `magento.attributes.options`, `magento.categories.flat`. Anything missing is a taxonomy change.
3. **Ask for taxonomy changes in writing.** Creating a category, an attribute, an option value or
   an attribute set changes the store's structure and its URLs. Do not create any of them
   without a human agreeing to that specific change.
4. **Create in dependency order.** Simple children first (`magento.product.create`), then the
   configurable parent (`magento.configurable.create` with its options and children), then
   media (`magento.product.media.upload`), then category assignment
   (`magento.category.assign_product`), then links (`magento.product.links.add`).
5. **Verify like a shopper.** `magento.configurable.children` must list every child;
   `magento.product.get` must show the same type and attribute values; `magento.inventory.status`
   must show the parent as in stock. A parent whose children have source items is still not
   salable until its own stock says so.
6. **Report the gaps.** A copy is done only when type, options and attribute values match. If
   something could not be reproduced (a missing attribute set, an option value you were not
   allowed to create), say which product and which field — an incomplete copy reported as
   complete is worse than a copy that was not attempted.

## Traps specific to this gateway

- **`price` is the global base price.** A store-scope price or `special_price` decides what a
  shopper pays, and the product read may show the global value. Read an order line when the
  question is "what did the customer pay".
- **A configurable parent's price is derived from its children**; a price you write on the parent
  may read back as 0.
- **Changing `url_key` changes the storefront URL** and Magento creates no redirect for API
  writes. Treat it as a structure change.
- **Media is per store view.** Uploading once does not populate every store view; check the
  image roles after `magento.product.media.upload`.
- **Tier prices are per customer group** (`magento.customer.groups`); a copy without the group
  ids copies a price nobody sees.

Mutually exclusive with: `magento.product.create` for a configurable parent (it creates a simple
product), and `magento.configurable.create_variations` when you already have the children —
that path skips creating duplicates but still needs the option values to exist.
