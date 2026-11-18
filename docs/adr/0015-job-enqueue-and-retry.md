# ADR-0015: Job enqueue path and retry semantics

- **Status:** Accepted
- **Date:** 2026-11-19
- **Deciders:** maintainer, Architect
- **Supersedes:** none
- **Trigger:** independent architecture review AR-05, AR-08, AR-10, AR-33

## Context

The architecture says long-running work is converted into jobs, but the frozen
dependency table gave no legal way for a tool handler to enqueue one: `internal/mcp`
could not import `internal/jobs`, and modules could import only `core`. The job
table also required a non-null `instance_id` while admitting `unified` and
`platform` job classes, and retried every failed handler regardless of whether the
operation was idempotent.

## Problem

How does a tool enqueue work without breaking the module boundary, and when may a
failed job be retried?

## Options considered

1. **Let `internal/mcp` import `internal/jobs`.** Works, but breaks the dependency
   table and couples the protocol layer to a queue implementation.
2. **Let modules import `internal/jobs`.** Same problem, worse: every module
   depends on Asynq.
3. **Define a small `core/jobs.Enqueuer` interface, implemented by `internal/jobs`
   and injected in `cmd/server`.** Keeps the graph acyclic and lets tests use a
   fake.
4. **Retry every failure up to `max_attempts`.** Simple, duplicates writes.

## Decision

Adopt option 3 and tighten retries.

- `core/jobs.Enqueuer` exposes `Enqueue`, `Status`, and `Cancel`. Tool handlers and
  module services accept it; only `cmd/server` and the worker know the concrete
  implementation.
- `jobs.instance_id` becomes nullable and `jobs.binding_id` is added, with a CHECK
  that enforces exactly one scope per module class (`platform` jobs have neither).
- Retry is gated on `Retryable` **and** on the tool's `idempotentHint`. Write and
  import jobs default to `max_attempts = 1`; read and report jobs may retry. The
  retry decision is recorded on `job_attempts`.
- Results larger than the 8 KiB inline cap are written to object storage and
  referenced by `result_ref`; the inline `result` column is not silently truncated.
- `platform.job.get` and `platform.job.cancel` are part of the tool catalog, so a
  caller that receives a job handle can always poll and cancel it.

## Rationale

The interface keeps the dependency graph honest while still allowing asynchronous
work. Retry gating prevents the exact failure mode ADR-0010 exists to prevent: a
lost response followed by a duplicated write.

## Consequences

### Positive

- The async design is implementable without boundary exceptions.
- Non-idempotent operations are never retried automatically.
- Every job handle is usable.

### Negative

- One more interface to wire and fake.
- Large results need object storage.

### Security impact

Jobs are scoped to an identity and to an instance or binding; a foreign job returns
404. Cancellation is cooperative and re-checked before each upstream call.

### Maintenance cost

Low: one interface, one column, one policy table.

## Follow-up

- Phase 1: implement the enqueuer, the job tools, and retry gating.
- Phase 3: object-storage result references for large reports.

## References

- [Module boundaries](../module-boundaries.md) section 4.3
- [Database design](../database-design.md) jobs, job_attempts
- [ADR-0010](0010-error-model-and-tool-contract.md)
