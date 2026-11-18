# ADR-0014: Confirmation approval surface and instance bindings

- **Status:** Accepted
- **Date:** 2026-11-19
- **Deciders:** maintainer, Architect, Security
- **Supersedes:** amends ADR-0006
- **Trigger:** independent architecture review AR-01, AR-03, AR-07, AR-25, AR-32

## Context

Phase 0 review found two defects in the authorization design. First, the
confirmation protocol returned the approval nonce to the requesting agent, and no
component existed that could mint an approval, so the human control on destructive
writes was unimplementable and, if implemented naively, self-approvable. Second,
every permission row, job, and confirmation was scoped to exactly one instance,
while every `unified.*` tool reads two or three instances at once. Cross-instance
reads — the highest-risk read path — had no authorization semantics.

## Problem

How does a human approve a destructive operation without the agent holding both
halves of the ceremony, and how is a multi-source tool authorized?

## Options considered

1. **Return the nonce to the agent and trust the client to obtain approval.**
   Simple, unverifiable, and equivalent to no control.
2. **Approval through the same MCP connection by a second agent.** Cheap, but an
   agent cannot be an approver of record.
3. **A separate human-only approval surface plus an opaque confirmation id.**
   Requires an additional authenticated surface and a table column, but makes the
   control verifiable.
4. **Authorize unified tools against each instance independently.** Fails open for
   combinations: a caller granted one member could read data from another.
5. **Authorize against a named binding whose members must all be granted.**
   Explicit, auditable, and matches how the analysis is actually configured.

## Decision

Adopt options 3 and 5.

**Confirmation.** The requester receives an opaque `confirmation_id`, never an
approval secret. A human operator approves through a separate surface
(`mage2mcp-admin` CLI or a human-only Access-protected endpoint). Approval records
`approver_hash`, requires `approver_hash <> requester_hash`, the `approver`
permission, an unexpired request, and a matching parameter hash. The agent then
re-submits the same parameters with `confirmation_id`; the gateway verifies state,
hash, identity, tool, scope, TTL (default 300 s, cap 900 s), and single use. The
approval surface failing means destructive operations fail closed.

**Bindings.** A binding is a named `(magento, cloudflare, ga4)` triple declared in
configuration and projected read-only into `instance_bindings`. Unified tools take
`binding` instead of `instance`. Authorization requires a grant for the binding or
for every member instance; a grant on one member is never sufficient. Jobs and
confirmations carry either `instance_id` or `binding_id`, never both.

## Rationale

A control that the requester can complete alone is not a control. An opaque id
plus a separate approver identity makes the ceremony verifiable and auditable.
Bindings turn an implicit, guessed triple into a declared object that can be
authorized, stored, and reviewed.

## Consequences

### Positive

- Destructive writes require a second, distinct human identity.
- Cross-instance access has explicit semantics and a single audit subject.
- Unified tools no longer guess which instances to combine.

### Negative

- Operators must deploy and protect one more surface.
- Configuration gains a `bindings` section and a validation rule.
- A single-instance deployment still passes `instance`; single-binding deployments
  may omit `binding`.

### Security impact

Closes self-approval and confused-deputy paths. The approval surface becomes a
high-value target and must be protected by a human-only Access policy, its own
audit, and rate limiting.

### Maintenance cost

Low: one table, one CLI command, one config section.

## Follow-up

- Phase 1: implement `internal/admin`, the approval CLI, and binding resolution.
- Phase 1: adversarial tests for self-approval, expired approval, and partial
  binding grants.
- Re-review the confirmation path after implementation (security reviewer).

## References

- [Security review](../security/security-review.md) section 5
- [Database design](../database-design.md) `confirmation_tokens`, `instance_bindings`
- [Tool catalog](../mcp-tools.md) sections 5 and 7.8
