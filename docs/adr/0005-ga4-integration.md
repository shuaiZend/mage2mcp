# ADR-0005: GA4 integration scope — Data API only, BigQuery as a documented boundary

- **Status:** Accepted
- **Date:** 2026-11-18
- **Deciders:** maintainer, Architect, GA4 owner
- **Supersedes:** none

## Context

Google Analytics 4 exposes the Data API (aggregate reports), the Admin API
(configuration), and BigQuery export (raw events). The project needs traffic,
acquisition, events, ecommerce, and realtime analysis, read-only, across multiple
properties. The Data API rejects incompatible dimension/metric combinations and
has per-property quotas.

## Problem

How much of GA4 should the gateway cover, and how do we handle questions the Data
API cannot answer without embedding a SQL engine or silently returning wrong
numbers?

## Options considered

1. **Data API only.** Correct, cheap, and safe; cannot answer raw-event,
   user-path, cohort, or unsampled questions.
2. **Data API + BigQuery SQL execution.** Full analytical power, but the gateway
   would need to build and execute SQL against a user's warehouse, with cost,
   permission, and injection implications.
3. **Data API + BigQuery via prebuilt, parameterized views.** Powerful and
   bounded, but requires the user to deploy views and grant access.
4. **Admin API included.** Unnecessary for analytics and increases write risk.

## Decision

Adopt option 1 for v0.x, with an explicit, documented BigQuery boundary:

- Only Data API methods: `runReport`, `batchRunReports`, `runPivotReport`,
  `runRealtimeReport`, `properties.getMetadata`.
- No Admin API. No BigQuery queries. No user-provided SQL.
- A compatibility validator rejects impossible field combinations before the
  request is sent, using curated rules plus live metadata.
- A property capability profile records whether ecommerce tracking is present;
  the gateway never assumes it is.
- Every result carries property time zone, currency, freshness, and quality
  metadata.
- When a question requires event-level data, the tool returns a structured
  `unsupported_capability` error explaining that BigQuery export is required,
  rather than approximating.

## Rationale

The Data API is the only surface that can be safely exposed to an untrusted agent
without a query-cost model or SQL sandbox. A property may have no ecommerce data
at all — the target property currently has none — so assuming capabilities is a
correctness bug. Prebuilt BigQuery views (option 3) are the natural post-1.0
extension; it is recorded as follow-up, not built now.

## Consequences

### Positive

- No SQL injection surface, no warehouse cost surprises.
- Quotas are understood and enforceable.
- Results are honest about sampling, freshness, and missing capabilities.

### Negative

- Some legitimate questions are refused rather than answered.
- Curated compatibility rules need maintenance as GA4 evolves.

### Security impact

Service-account credentials are read-only (`analytics.readonly`) and scoped per
property where possible. No Admin API means the gateway cannot change tracking
configuration. Report requests are audited without logging returned data.

### Maintenance cost

Low: one client, one validator, one capability probe. The validator's rule table
is the main ongoing cost.

## Follow-up

- Phase 4: implement the validator and capability probe.
- Post-1.0: evaluate parameterized BigQuery views under a new ADR.
- Track GA4 Data API schema changes via the metadata endpoint.

## References

- [GA4 integration design](../integrations/ga4.md)
- [research baseline](../development/research-baseline.md) facts F18–F22
