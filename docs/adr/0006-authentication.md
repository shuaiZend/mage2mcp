# ADR-0006: Authentication and authorization model

- **Status:** Accepted
- **Date:** 2026-11-18
- **Deciders:** maintainer, Architect, Security
- **Supersedes:** none

## Context

The gateway is reachable by AI agents over the internet, sits behind Cloudflare
Access, and can perform writes in Magento. Identities include human operators,
human analysts, and agent service identities. Vendor credentials (Magento,
Cloudflare, Google) must never be confused with caller identity.

## Problem

How do we authenticate callers, separate human from machine identities, enforce
per-tool and per-instance authorization, and make high-risk writes safe against
parameter tampering?

## Options considered

1. **Static API keys.** Simple, but no expiry, no revocation story, no identity
   metadata, and easy to leak.
2. **OAuth token introspection per request.** Strong, but adds a network
   dependency and latency to every call.
3. **Cloudflare Access JWT verified locally with JWKS.** Standard OIDC assertions,
   local verification, short-lived tokens, centralized policy.
4. **mTLS.** Strong machine identity, operationally heavy for agent clients.

## Decision

Adopt option 3, with layered authorization:

- **AuthN:** verify the Cloudflare Access JWT signature against JWKS, enforce an
  algorithm allowlist (RS256/ES256), validate `iss`, `aud`, `exp`, `nbf` with a
  small clock skew, reject `alg=none`, cache keys by `kid`, and bound token size.
- **Identity classes:** `human` (interactive SSO) and `service` (service token).
  A service identity never inherits a human's roles; it has its own role
  assignment. The identity class is recorded in the audit event.
- **AuthZ:** roles (`viewer`, `analyst`, `operator`, `admin`) × tool permissions ×
  instance scope. Default deny, read-only by default.
- **High-risk writes:** require the two-step confirmation protocol defined in
  [ADR-0014](0014-confirmation-approval-and-bindings.md): an opaque
  `confirmation_id`, approval by a different human identity on a separate
  surface, a canonical parameter hash, a 300 s default / 900 s cap TTL, and
  single use. There is no nonce carried by the requester.
- **Sessions:** MCP sessions are bound to the verified identity (see ADR-0002).

## Rationale

Local JWT verification avoids a per-request network hop while preserving
centralized policy in Cloudflare Access. Separating human and service identities
prevents the common failure where a machine token silently gains an operator's
permissions. Parameter-hash confirmation is the only reliable defense against an
agent (or injected instruction) changing a write after a human approved it.

## Consequences

### Positive

- No long-lived shared secrets for callers.
- Centralized revocation through Cloudflare Access.
- Write operations are attributable and tamper-evident.

### Negative

- Depends on Cloudflare Access as the identity provider; self-hosters must
  configure an OIDC-compatible issuer (documented alternative).
- Confirmation adds a round trip for high-risk writes.

### Security impact

This is the primary control boundary. Key rotation, JWKS caching, and clock skew
handling must be tested adversarially. A forged or replayed token must fail
closed. The gateway must never accept a token because it merely parses.

### Maintenance cost

Moderate: JWT/JWKS edge cases and IdP changes. Mitigated by a small, well-tested
verifier and by contract tests with generated keys.

## Follow-up

- Phase 1: implement verifier, RBAC, and confirmation service.
- Phase 1: document a non-Cloudflare OIDC fallback for self-hosters.
- Phase 6: adversarial tests for algorithm confusion, kid injection, and replay.

## References

- [Threat model](../security/threat-model.md)
- [Security review](../security/security-review.md)
