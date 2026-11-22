# Bulk imports

Imports are the only path that writes many products at once, and they are asynchronous. Two gates
apply: `tools.writes` to start or change a job, and `database.url` for the job tools to exist.

## Why the row rules matter

- **Every CSV row needs `name`.** A "price only" file is rejected row by row: the importer writes
  whole products. Read the product first and send the field back.
- **`partial: true` is a real write.** Rows succeed or fail individually; a partial job changed
  data and needs follow-up.
- **`job.result` is a JSON string**, not an object: parse it before reading counts.
- **`import.resume` is a new job** from the failed rows only.
- **A repeated `import.start` is not a second import.** Same arguments return the original job;
  change the file or the arguments to import again.

## Recipe: bulk price update by CSV

1. `magento.product.search` / `magento.products.get` — SKUs with current `name`, `price`,
   `status`; the CSV needs those back.
2. `magento.import.validate` — a preview and per-row errors; fix them before writing.
3. `magento.import.start` — one unique `idempotency_key` per import.
4. `magento.import.status` — poll to a terminal state, then parse `job.result` for counts and
   row errors.
5. `magento.import.batches` / `magento.import.batch` — which batch and rows failed.
6. `magento.import.resume` to re-run only the failed rows, or `magento.import.cancel` to stop.
7. `magento.products.get` on a sample: a "successful" job with an unchanged price is the failure
   to check for.

Mutually exclusive with `magento.products.bulk_update` for `price` and `name`: both are ignored
inside `attributes` while the call reports `updated`. Use `magento.product.update` per SKU.

## Recipe: bulk stock and status changes

1. `magento.inventory.status_batch` — current `is_in_stock`; its `qty` is legacy and can be 0
   while `is_in_stock` is true.
2. `magento.import.validate` then `magento.import.start` — CSV with the new
   `quantity_and_stock_status`. Per-source stock is `magento.inventory.source_items`.
3. `magento.import.status` → `job.result`; verify one SKU with `magento.inventory.status`.
Not with: `magento.inventory.source_items` — a CSV cannot set per-source quantities.

## Async lifecycle

`start` → `status` (queued, running, partial, failed, complete) → `batches`/`batch` → `resume` or
`cancel`. `execute` is the synchronous variant for a handful of rows. Nothing here is retryable on
`operation_unknown`: reconcile the job.
