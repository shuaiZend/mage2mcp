# ADR-0019: PostgreSQL job queue instead of Redis

- **Status:** Accepted
- **Date:** 2026-11-19
- **Deciders:** maintainer, Architect
- **Supersedes:** the Redis queue decision in ADR-0009 (Redis is removed)

## Context

The only production use of Redis was the Asynq task queue used by
`cmd/worker`. Sessions, rate limits, and response caches were designed around
Redis but never implemented. Running a second stateful service for a queue that
processes a small number of long tasks is operationally disproportionate for a
self-hosted gateway, and it doubles the deployment's failure modes and backup
surface.

PostgreSQL is already required for audit, confirmations, and job state. Its
`FOR UPDATE SKIP LOCKED` feature is a well-understood way to build a reliable
queue without another dependency.

## Problem

Can the worker queue live entirely in PostgreSQL without losing correctness,
crash recovery, or concurrency safety?

## Options considered

1. **Keep Redis + Asynq.** Mature and fast, but a second service to operate,
   secure, back up, and monitor, for a low-throughput queue.
2. **PostgreSQL queue with `FOR UPDATE SKIP LOCKED` and leases.** One fewer
   dependency; crash recovery via lease expiry; retries and idempotency keys
   already modeled in the `jobs` table.
3. **In-process goroutines only.** Loses durability across restarts; rejected.

## Decision

Adopt option 2 and remove Redis entirely.

- `internal/jobs` defines the `Queue` contract and a worker that claims, runs,
  completes, fails, or retries tasks.
- `internal/storage.JobQueue` implements it on the existing `jobs` table:
  - enqueue inserts a queued row;
  - claim runs in a transaction, first returning expired leases to the queue,
    then selects with `FOR UPDATE SKIP LOCKED`, increments the attempt, and sets
    a lease;
  - complete/fail/cancel are compare-and-set updates; retries use a bounded
    backoff and the row's `max_attempts`;
  - handlers run with a per-task timeout; shutdown drains in-flight tasks.
- `cmd/worker` requires `database.url` and no longer accepts Redis settings.
- The Redis configuration section, environment variables, Compose service, and
  Asynq/Redis dependencies are removed.

## Rationale

One durable store is simpler to operate and to reason about. The queue volume is
small (imports, reports), so PostgreSQL's write throughput is not a constraint.
Leases give crash recovery without a second system, and the existing schema
already models attempts, queues, priorities, and idempotency.

## Consequences

### Positive

- One stateful dependency (PostgreSQL) plus the gateway.
- Queue state is included in the existing backup and restore procedure.
- Fewer credentials, containers, and upgrade paths.

### Negative

- Polling adds up to `poll_interval` latency (default ~1s) versus Redis push.
- Very high task throughput would need partitioning or a dedicated queue again.
- Workers share the database connection budget with the server.

### Security impact

Positive: one fewer network service and credential. The worker must still be
run with a least-privilege database role.

### Maintenance cost

Low: the queue is ~200 lines of SQL and a small worker loop, both covered by
tests including lease recovery.

## Follow-up

- Add per-queue concurrency limits when import jobs land.
- Revisit only if sustained task throughput exceeds what one PostgreSQL
  instance can serve.

## References

- [Architecture](../architecture.md)
- [Database design](../database-design.md)
- [ADR-0009](0009-data-stores.md)
