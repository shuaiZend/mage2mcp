# ADR-0016: Unified analytics semantics

- **Status:** Accepted
- **Date:** 2026-11-19
- **Deciders:** maintainer, Architect, Unified owner
- **Supersedes:** none
- **Trigger:** architecture review AR-07, AR-17, AR-21, AR-22

## Context

mage2mcp joins three populations that are routinely conflated: Cloudflare HTTP
requests (including bots and static assets), GA4 sessions (client-side tagged),
and Magento orders (booked revenue). They have different grains, clocks,
currencies, latencies, and failure modes. The unified module exists to compare
them, not to blend them.

## Problem

What does a unified result mean, what may be joined with what, and how does the
gateway avoid presenting a plausible but wrong number?

## Options considered

1. **A single blended metric per question** (for example "visits" combining
   requests and sessions). Simple to present, statistically meaningless.
2. **Raw numbers with a caveat string.** Honest in intent, ignored in practice.
3. **Comparisons with declared semantics**: explicit sources, formulas, windows,
   quality flags, and non-equivalence rules, enforced by the result contract.

## Decision

Adopt option 3.

- A unified tool takes a **binding** (a declared Magento/Cloudflare/GA4 triple).
  Every member instance must be granted; the tool never guesses a triple.
- Joins happen at **daily grain** on `(date_local, binding)` and optionally
  `(date_local, sku)`, `(date_local, channel)`, `(date_local, country)`. Joining
  request-level data to session-level data is forbidden unless Logpush and
  ClickHouse are enabled, and even then it is a separate, declared query.
- Every result carries `sources`, `formulas`, `time_range` with per-source local
  zones, `currencies`, `quality` flags (`sampled`, `partial`, `stale`,
  `incomplete_window`, `mapping_incomplete`, `capability_absent`), and
  `mapping_coverage`.
- Non-equivalences are contractual: Requests ≠ Sessions, GA4 Users ≠ Magento
  Customers, GA4 Purchase Revenue ≠ Magento Revenue, Cloudflare bot-classified
  requests ≠ GA4 bot-filtered sessions.
- When a source cannot answer (no ecommerce tracking, no Logpush), the tool
  returns `ecommerce_not_tracked` or `requires_logpush` instead of zeros.
- Currency conversion is never implicit; a mixed-currency result has
  `amount: null` and `mixed: true` unless an explicit rate and date are supplied.

## Rationale

The failure mode of cross-source analytics is not missing data; it is a
confident wrong number. Encoding the semantics in the contract, the flags, and
the error codes makes the comparison reviewable and testable. Daily grain keeps
the join deterministic and avoids implying request-level attribution the data
cannot support.

## Consequences

### Positive

- Every unified number is explainable and reproducible.
- Missing capabilities degrade honestly.
- The eight required scenarios share one testable contract.

### Negative

- Some questions are refused rather than approximated.
- Daily grain cannot answer intraday attribution questions.
- Binding configuration adds operator setup.

### Security impact

Bindings are an authorization subject: a grant on one member never authorizes a
unified read. Results never include client IPs without a `pii:read` grant.

### Maintenance cost

Moderate: the semantic tables and the scenario formulas need review whenever a
source changes its data model.

## Follow-up

- Phase 5: implement the binding resolution, the daily join lattice, and the
  quality-flag contract.
- Phase 5: validate four scenarios structurally with fixtures; numerically
  validate only those the available property can support.
- Re-review after the first real deployment.

## References

- [Unified analytics](../unified-analytics.md)
- [Module boundaries](../module-boundaries.md) section 5.4
- [ADR-0014](0014-confirmation-approval-and-bindings.md)
