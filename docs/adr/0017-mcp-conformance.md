# ADR-0017: MCP conformance requirements

- **Status:** Accepted
- **Date:** 2026-11-19
- **Deciders:** maintainer, Architect, MCP owner
- **Supersedes:** none
- **Trigger:** MCP best-practice review after the Phase 0 remediation round

## Context

The gateway's value depends on behaving like a well-behaved MCP server, not merely
on speaking the protocol. The specification defines tool metadata, structured
content, error semantics, pagination, progress, cancellation, resource links, and
a set of security best practices (token passthrough, audience binding, session
hijacking, confused deputy, local server compromise). Phase 0 covered naming,
schemas, annotations, pagination, and most security controls, but left five gaps:
output-schema coverage, the text fallback for structured content, progress and
cancellation, resource links for large results, and explicit token-passthrough and
audience rules.

## Problem

Which MCP behaviors are mandatory for mage2mcp, and how are they proven?

## Options considered

1. **Protocol-minimum**: speak JSON-RPC and Streamable HTTP, ignore the softer
   recommendations. Cheapest, but clients cannot rely on annotations, structured
   output, cancellation, or large-result handling.
2. **Full conformance with a documented test per requirement.** More design and
   test work, but predictable client behavior and a reviewable claim.
3. **Full conformance plus MCP resources and prompts.** Adds surface without a
   current use case; deferred.

## Decision

Adopt option 2. The normative list is [mcp-conformance.md](../mcp-conformance.md).

- Every structured tool declares an output schema and returns `structuredContent`
  plus a serialized JSON `TextContent` fallback.
- Results above the inline cap return a `resource_link` backed by object storage;
  they are never silently truncated.
- `progressToken` is honored and `notifications/cancelled` cancels the in-flight
  context; long work is a job with `platform.job.cancel`.
- Invalid arguments are protocol errors (SDK-enforced); execution failures are
  tool errors with `isError: true` and a typed code.
- The caller's token is never forwarded upstream (no token passthrough); `aud` is
  bound to the gateway and instance.
- Session IDs are cryptographically random, bound to the verified identity, and
  terminated on `DELETE` or idle timeout.
- Origin validation, DNS-rebinding protection, and localhost protection stay
  enabled; disabling any of them requires a new ADR.
- Tool descriptions follow the quality rules in the conformance document.

## Rationale

MCP clients make decisions from annotations and schemas, not from prose. A tool
without an output schema or an accurate annotation forces the client to guess, and
a truncated result is indistinguishable from a complete one. The security best
practices exist because MCP servers are proxies with authority; each one maps to a
control this project already needs.

## Consequences

### Positive

- Client behavior is predictable and testable.
- Large results have a defined path instead of a silent cap.
- Security claims are explicit and covered by named tests.

### Negative

- Every tool needs an output schema before registration, slowing tool delivery.
- Resource links add an object-storage dependency for large reports.
- Cancellation and progress require context plumbing through every layer.

### Security impact

No token passthrough and audience binding close the two most common MCP
authorization mistakes. Origin and rebinding protection close the local-server
class. Session identity binding closes session fixation.

### Maintenance cost

Moderate: conformance tests must be maintained as the specification evolves. The
SDK absorbs most protocol churn.

## Follow-up

- Phase 1: implement the conformance tests marked Phase 1 in §4 of the conformance
  document.
- Phase 2: resource links for large reports.
- Post-1.0: evaluate MCP resources and prompts.

## References

- [MCP conformance](../mcp-conformance.md)
- [Tool catalog](../mcp-tools.md)
- [Architecture](../architecture.md)
