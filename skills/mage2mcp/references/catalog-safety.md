# Catalog safety

Checklist before Magento writes that can change catalog structure, product URLs, or attribute
semantics. Load this before creating categories, attributes or options, or changing `url_key`.
The approval mechanics are in [write-contract.md](write-contract.md); this file is about blast
radius.

## Catalog structure (requires explicit confirmation)

- Creating, moving, renaming or deleting a category can change product URL keys and URL
  rewrites. Existing links, search rankings and feeds may break. Prefer adding a category under
  an existing branch; never delete a category that still has products or child categories
  without a redirect and a reindex plan.
- Creating an attribute or attribute option is a schema change: it affects attribute sets,
  filters and layered navigation. Deleting an option that products reference (especially a
  configurable variant) breaks those products; check usage first.
- Changing a product `url_key` changes its storefront URL. Magento does not create a 301
  redirect for API writes; plan the redirect and reindex before announcing it.
- Changing product type, visibility, website assignment or status changes what storefronts and
  feeds see.

## Inventory and configurables

- Assigning MSI source items to children does not make a configurable parent salable; verify the
  parent stock status and the storefront option selector afterwards.
- The configurable parent price is derived from its children; a requested parent price may read
  back as 0.
- `inventory.status.qty` is a legacy catalogue value: it can be 0 while `is_in_stock` is true.
  Judge availability by `is_in_stock`, and per-source numbers by
  `magento.inventory.source_items`.

## Approval

- Every write is two-step: inspect the preview and the confirmation, then approve as a different
  human identity. Do not approve destructive or URL-affecting changes without the operator's
  explicit go-ahead.
- The approval is bound to the tool, the instance and a hash of the arguments, so an edited
  request needs a new approval.
- If a write returns `operation_unknown`, stop: reconcile it with `magento.operation.get` before
  doing anything else. Never retry it.

## Reporting a structure change

Say what changed and what it can break: categories moved, URL keys changed, attributes or options
added, what needs reindexing or redirecting. "Done" without that list is not a report.
