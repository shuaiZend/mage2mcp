# ADR-0012: Observability and audit

- **Status:** Accepted
- **Date:** 2026-11-18
- **Deciders:** maintainer, Architect, Security
- **Supersedes:** none

## Context

Operators must diagnose failed upstream calls and quota exhaustion. Security
reviewers must reconstruct who did what, with which parameters, and whether it
was approved. Agents must not receive internal details. Logs are a common leak
path for credentials and PII.

## Problem

What do we record, where, for how long, and how do we keep operational logs and
the audit trail separate?

## Options considered

1. **Logs only.** Cheap, but mutable, unstructured, and unsuitable as evidence.
2. **Audit table only.** Good evidence, but poor operational debugging.
3. **Structured logs + metrics + separate append-only audit.** More work, correct
   separation of concerns.
4. **Full distributed tracing from day one.** Valuable later, premature now.

## Decision

Adopt option 3, with tracing deferred.

- **Logs:** `slog` JSON, one event per tool call with tool, instance, identity
  ID, permission, outcome, duration, and parameter hash. Parameters, tokens, and
  response bodies are never logged.
- **Metrics:** Prometheus counters, histograms, and gauges for tool calls,
  upstream latency and status, quota remaining, job state, and auth failures.
- **Audit:** append-only PostgreSQL table written for every tool call, including
  denied calls and confirmation events. Records identity, identity class, tool,
  instance, permission decision, canonical parameter hash, correlation ID,
  outcome, and timestamp. No payload contents.
- **Health:** `/healthz`, `/readyz`, `/livez` with per-module status.
- **Retention:** logs per operator policy; audit 365 days by default, configurable.

## Rationale

Audit is evidence and must be immutable and queryable; logs are operational and
may be sampled or shipped elsewhere. Mixing them either weakens evidence or makes
debugging expensive. Parameter hashes give tamper evidence without storing
sensitive payloads.

## Consequences

### Positive

- Security investigations are possible without reading raw payloads.
- Quota and latency problems are visible before users report them.
- Log hygiene is enforced by construction.

### Negative

- Audit write failure fails write and destructive operations (fail closed) and
  degrades read operations with a warning. This is a deliberate availability
  trade-off in favor of evidence integrity.
- Extra storage for audit.

### Security impact

Audit immutability requires database-level restrictions (no UPDATE/DELETE grants
for the application role). Log redaction must be tested, not assumed.

### Maintenance cost

Low: standard library plus Prometheus client.

## Follow-up

- Phase 1: implement logging, metrics, health, and audit writer.
- Audit failure policy, two phases:
  - **Intent row (gate):** for write and destructive tools, no durable intent row means no
    execution; the call returns `internal` with a correlation id. For reads, a failed intent
    row logs a warning and proceeds with `audit_state=degraded`.
  - **Terminal row (record):** if the upstream operation succeeded but the terminal row
    cannot be written, the result is returned with `audit_state=pending`. It is **not**
    reported as an error, because an error would trigger the duplicate retry that ADR-0010
    exists to prevent. A recovery job resolves pending rows by reading the entity back.
- Post-1.0: OpenTelemetry tracing.

## References

- [Threat model](../security/threat-model.md)
- [Security review](../security/security-review.md)
