# ADR-0010: Error model and tool contract

- **Status:** Accepted
- **Date:** 2026-11-18
- **Deciders:** maintainer, Architect
- **Supersedes:** none

## Context

MCP tool errors are consumed by language models. A raw upstream error string
teaches the model nothing and may leak credentials or internals. Three modules
produce errors from three vendors with incompatible shapes. Agents retry, and a
wrong retry policy causes duplicate writes.

## Problem

How do we represent errors so they are actionable for an agent, safe to expose,
consistent across modules, and sufficient for correct retry behavior?

## Options considered

1. **Pass upstream errors through.** Fast, leaks internals, inconsistent.
2. **Free-form strings.** Simple, unactionable, untestable.
3. **gRPC-style numeric codes.** Standard, but poor fit for MCP/JSON and loses
   nuance such as "confirmation required".
4. **Typed error taxonomy with safe message, retryable flag, and redacted
   details.** Slightly more work per module, testable and safe.

## Decision

Adopt option 4.

- A single `errs.Code` enumeration (see module boundaries) covers every failure
  class the agent can act on.
- Every error carries `Code`, `Message` (safe), `Retryable`, `Upstream`, `Op`,
  and bounded redacted `Details`.
- Tool results set `IsError` and return the typed error as structured content;
  protocol errors are reserved for transport-level failures.
- Modules map upstream errors with table-driven tests. Unmapped errors become
  `internal` with a generic message and a correlation ID.
- Retry policy is derived from `Retryable` plus the tool's idempotency
  annotation: non-idempotent writes are never retried automatically.

## Rationale

Models act on codes and messages, not on HTTP status codes buried in text. A
typed taxonomy makes the "should I retry?" decision explicit and prevents the
most damaging failure mode: an agent retrying a create because the first call
succeeded but the response was lost.

## Consequences

### Positive

- Consistent agent experience across Magento, Cloudflare, and GA4.
- Retry safety is encoded, not inferred.
- No credential or internal leakage in error paths.

### Negative

- Each module needs an error mapping table and tests.
- New failure classes require a taxonomy update.

### Security impact

Redaction happens before the error leaves the process. Upstream bodies, URLs
with query secrets, and stack traces never reach the agent. Correlation IDs allow
operators to find the full detail in logs without exposing it.

### Maintenance cost

Low, with a one-time mapping cost per module.

## Follow-up

- Phase 1: implement the error package and MCP serialization.
- Phase 2–4: mapping tables per module.

## References

- [Module boundaries](../module-boundaries.md)
- [MCP tool catalog](../mcp-tools.md)
