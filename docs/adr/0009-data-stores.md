# ADR-0009: Data stores — PostgreSQL for state, Redis for queues and limits

- **Status:** Accepted
- **Date:** 2026-11-18
- **Deciders:** maintainer, Architect
- **Supersedes:** none

## Context

The gateway needs durable state (instances, permissions, jobs, audit, mappings)
and ephemeral state (queues, rate limits, caches). It must remain deployable by a
small team and must not require a distributed database.

## Problem

Which stores do we adopt, and what is explicitly excluded from them?

## Options considered

1. **SQLite.** Zero-ops, but weak concurrent writes, no network access for the
   worker, and a poor fit for audit volume.
2. **MySQL.** Viable, but PostgreSQL has stronger JSON, indexing, and extension
   support, and better Go tooling alignment.
3. **PostgreSQL + Redis.** Durable relational state plus fast ephemeral state.
4. **PostgreSQL only (no Redis).** Simpler, but polling-based queues and
   database-backed rate limits add load and latency.

## Decision

Adopt option 3.

- **PostgreSQL 16+** for the read-only instance projection of YAML configuration,
  users, roles, permissions, jobs, audit, import batches, SKU mappings, and
  idempotency records. The server never writes configuration back to the database. Migrations run via
  `golang-migrate`, forward-only with tested rollback notes.
- **Redis 7+** for Asynq queues, rate-limit counters, idempotency reservations, and
  short-lived response caches. Durable idempotency records live in PostgreSQL:
  Redis provides the fast `SET NX` reservation, PostgreSQL provides the single
  authoritative ledger that survives a Redis flush. There is exactly one ledger;
  `jobs.idempotency_key` is a convenience index into it, not a second source of
  truth.
- **Explicitly excluded:** raw Cloudflare logs (ClickHouse/R2), secrets
  (environment or secret manager), and agent conversation content.

## Rationale

PostgreSQL is the smallest operationally sane durable store with strong
correctness guarantees for audit and job state. Redis is required anyway for
Asynq, and using it for rate limiting avoids hammering PostgreSQL on every tool
call. Excluding raw logs and secrets from both keeps blast radius and cost
predictable.

## Consequences

### Positive

- Clear separation: durable truth in PostgreSQL, ephemeral coordination in Redis.
- Redis loss degrades queues and limits but does not corrupt durable state.
- Migrations are versioned and reviewable.

### Negative

- Two stateful dependencies to operate and back up.
- Cache invalidation across instances needs care.

### Security impact

Database credentials are separate for server and worker. Audit rows are
append-only (no UPDATE/DELETE grants). Redis is not exposed publicly and requires
authentication. Secrets never enter either store.

### Maintenance cost

Low: both are mainstream, well-understood, and containerized.

## Follow-up

- Phase 1: schema, migrations, repositories.
- Phase 1: backup and restore runbook.
- Phase 3: ClickHouse for raw logs (separate ADR if deployment model changes).

## References

- [Database design](../database-design.md)
- [Architecture](../architecture.md)
