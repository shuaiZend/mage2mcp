# ADR-0021: Persist import batches for unbounded resume

- **Status:** Accepted
- **Date:** 2026-11-19
- **Deciders:** maintainer, Architect

## Context

Asynchronous imports already carry their payload in the job and store a bounded
report (counts plus the first 100 failed rows) in `jobs.result_ref`. That is
enough for status, but resume could only re-run failures that fit in the report.
Large batches with many failures would silently leave rows behind.

## Decision

Persist each asynchronous import as a batch:

- `import_batches` stores instance, job id, format, state, counts, error code,
  the original payload, and timestamps.
- `import_batch_failures` stores every failed row (row number, SKU, action,
  error) keyed by batch and row.
- The worker creates the batch before execution and finalizes it with full
  failure rows and a state of `succeeded`, `partial`, or `failed`.
- `magento.import.resume` reads failures from the batch table (falling back to
  the bounded job report when no batch exists, e.g. for older jobs) and queues a
  new job limited to those row numbers.

## Consequences

### Positive

- Resume is not capped by the 100-row report.
- Batch history is queryable for operations and future status tooling.
- The job report stays small, while the database keeps the complete failure set.

### Negative

- The payload is stored twice (job and batch); both are bounded to 8 MiB.
- One more table pair to back up and retain; retention follows the job retention
  policy.

### Security impact

The payload contains product data, never credentials. The tables are internal and
not exposed through a tool.

## Follow-up

- Add a batch list/status tool and retention pruning.
- Consider storing only failed row payloads instead of the full batch.

## References

- [Database design](../database-design.md)
- [Magento fields](../magento-fields.md)
