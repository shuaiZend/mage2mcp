# Bulk imports

Imports are the only bulk write path, and they are asynchronous. Gates: `tools.writes` to start a
job, `database.url` for the job tools to exist.

## Why the row rules matter

- **Only `sku` is checked before the job exists.** A missing or invalid `sku` rejects the whole
  payload (`validation_failed`, `details.rows`); every other column is checked per row at
  execution, so you still get a job id.
- **`name`, `attribute_set_id` and `type_id` (`simple` or `virtual`) are required only on a row
  that creates a product.** An update row may omit them, so a `sku,price` file is a valid bulk
  reprice; a create row that omits one fails alone, with `name is required to create a product`.
- **`partial: true` is a real write.** Rows succeed or fail individually; a partial job changed
  data and needs follow-up. Parse `failures[]` for the rows and reconcile.
- **`job.result` is a JSON string**, not an object: parse it before reading counts.
- **A repeated `import.start` is not a second import**: same arguments return the original job.

## Recipe: bulk price update by CSV

1. `magento.product.search` — the SKUs and their current `price`.
2. `magento.import.validate` — a preview and per-row errors; fix them before writing.
3. `magento.import.start` — one unique `idempotency_key` per import.
4. `magento.import.status` — poll to a terminal state, parse `job.result` for counts;
   `magento.import.batch` names the failed rows.
6. `magento.import.resume` (failed rows only) or `magento.import.cancel`.
7. `magento.products.get` on a sample: a "successful" job with an unchanged price is the failure
   to check for.

Mutually exclusive with `magento.products.bulk_update` for `price`/`name`: both are ignored inside
`attributes` while the call reports `updated`; use `magento.product.update` per SKU.

## Recipe: bulk stock and status changes

`magento.import.validate` then `magento.import.start` with `quantity_and_stock_status`, poll
`magento.import.status`, verify one SKU with `magento.inventory.status`. A CSV cannot set per-source
quantities (`magento.inventory.source_items`); `qty` from `inventory.status_batch` is legacy and can
be 0 while `is_in_stock` is true.

## Async lifecycle

`start` → `status` (queued, running, partial, failed, succeeded, canceled) → `batches`/`batch` →
`resume` or `cancel`. `execute` is the synchronous variant for a handful of rows. Nothing here is
retryable on `operation_unknown`: reconcile the job.
