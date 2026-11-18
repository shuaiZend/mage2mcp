# ADR-0004: Cloudflare raw log storage and analytics boundary

- **Status:** Accepted
- **Date:** 2026-11-18
- **Deciders:** maintainer, Architect, Cloudflare owner
- **Supersedes:** none

## Context

Cloudflare offers several data planes: the GraphQL Analytics API (aggregated,
adaptive datasets), Security Events (event-level, sampled, 31-day retention), and
Logpush (raw HTTP request records, usage-billed, no backfill). The project brief
requires HTTP request analysis, security events, WAF and bot visibility, and raw
log reading, while warning that raw logs must not be stored in PostgreSQL.

## Problem

Where do raw HTTP request logs live, and how does the gateway answer questions
that need event-level data without turning into a log platform?

## Options considered

1. **PostgreSQL for everything.** Simple, but 1.5–3 kB per request makes a modest
   1M requests/day produce 45–90 GB/month; indexing and retention become
   untenable.
2. **GraphQL Analytics only.** No raw logs; cannot answer request-level
   questions; retention and sampling are vendor-controlled.
3. **Logpush to object storage only.** Cheap archive, but querying requires
   external tooling; poor agent experience.
4. **Logpush to R2 (archive) + ClickHouse (query layer).** Columnar storage for
   request-level queries, object storage for replay and retention.
5. **Logpush to a third-party SIEM.** Offloads storage, but adds a vendor and
   reduces self-hosted viability.

## Decision

Adopt option 4, with strict boundaries:

- **GraphQL Analytics** answers aggregate questions (traffic, cache, status
  codes, countries, ASN) and is the default for analytics tools.
- **Security Events** (`firewallEventsAdaptive`) answers event-level security
  questions within its 31-day window, with sampling disclosed in the result.
- **Logpush** is opt-in per zone. When enabled, Cloudflare pushes to R2; a worker
  loads Parquet into ClickHouse for request-level queries.
- **PostgreSQL never stores raw request logs.** It stores job state, config,
  mappings, and audit only.
- The gateway refuses to promise raw logs when Logpush is not enabled, and says
  exactly what is missing.

## Rationale

The three planes answer different questions and have different cost and fidelity
profiles. Forcing one to do the others' job produces wrong answers (treating
aggregates as logs) or unsustainable storage. R2 + ClickHouse is the cheapest
self-hostable path that still supports SQL over request-level data, and R2 gives
replay when ClickHouse ingestion fails.

## Consequences

### Positive

- Correct semantics per question, disclosed to the agent.
- Raw logs are queryable without bloating PostgreSQL.
- Logpush cost is bounded by explicit budgets and field selection.

### Negative

- Two additional optional components (R2, ClickHouse) for full log capability.
- Ingestion lag and duplicate delivery must be handled.
- Logpush has no backfill; gaps are permanent and must be reported.

### Security impact

Raw logs contain IPs, URLs, user agents, and potentially PII. Access must be
permissioned, queries must be bounded, and retention must be enforced. Logpush
tokens and R2 credentials are separate from analytics tokens and scoped to
read/write only where strictly required.

### Maintenance cost

Moderate for operators who enable raw logs; zero for those who do not. The
default configuration stays on the GraphQL path.

## Follow-up

- Phase 3: implement Logpush job inspection and the R2/ClickHouse loader.
- Phase 3: publish a cost calculator based on measured record sizes.
- Revisit if Cloudflare offers a native queryable log store.

## References

- [Cloudflare integration design](../integrations/cloudflare.md)
- [research baseline](../development/research-baseline.md) facts F13–F17
