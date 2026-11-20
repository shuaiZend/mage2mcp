# Write safety

Magento writes can change more than the record you pass: they can move product
URLs, break storefront links, invalidate feeds, and change which products are
salable. The gateway enforces two-step confirmation for every write, and the
`write_safety` MCP prompt carries this checklist so an agent can load it before
planning catalog changes.

## Load the checklist

MCP clients can list prompts and fetch `write_safety`:

```json
{"jsonrpc": "2.0", "id": 1, "method": "prompts/get",
 "params": {"name": "write_safety"}}
```

The prompt text is also summarized here.

## Catalog structure requires explicit confirmation

- **Categories**: creating, moving, renaming, or deleting a category can change
  product URL keys and URL rewrites. Existing links, search rankings, and feeds
  may break. Prefer adding a category under an existing branch. Never delete a
  category that still has products or child categories without a redirect and
  reindex plan.
- **Attributes and options**: creating an attribute or option is a schema change
  that affects attribute sets, filters, and layered navigation. Deleting an
  option that products reference (especially a configurable variant) breaks those
  products. Check usage first.
- **URL keys**: changing a product `url_key` changes its storefront URL. Magento
  does not create a 301 redirect for API writes. Plan the redirect and reindex
  before announcing the change.
- **Type, visibility, website, status**: these change what storefronts and feeds
  see, and can hide a product without deleting it.

## Inventory and configurables

- MSI source items assigned to children do not make a configurable parent salable.
  `configurable.create` now attempts to set the parent stock status and reports
  `parent_in_stock`; always verify the storefront option selector afterwards.
- The configurable parent price is derived from its children; a requested parent
  price may read back as 0.

## Approval

Every write returns `confirmation_required` with an opaque id and an expiry. The
approval must come from a different identity, is bound to the canonical argument
hash, and cannot be reused. Do not approve destructive or URL-affecting changes
without the operator's explicit go-ahead.

## Related

- [MCP tools](mcp-tools.md) — tool contracts and confirmation model
- [Magento fields](magento-fields.md) — field requirements and SEO guidance
- [Product copy run](development/product-copy-run.md) — a real copy that exposed
  the URL and stock pitfalls above
