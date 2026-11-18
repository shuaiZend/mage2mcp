# ADR-0002: MCP transport, protocol version, and session model

- **Status:** Accepted
- **Date:** 2026-11-18
- **Deciders:** maintainer, Architect
- **Supersedes:** none

## Context

The gateway serves remote AI agents over the network. The MCP specification has
moved from HTTP+SSE to Streamable HTTP, and the official Go SDK v1.8.0 supports
protocol versions up to 2026-07-28 while negotiating older versions.

## Problem

Which transport and session model should mage2mcp commit to, given that clients
vary in protocol support, deployments may run multiple replicas, and the
gateway must sit behind Cloudflare Access and a Tunnel?

## Options considered

1. **stdio.** Simple and local, but incompatible with remote multi-agent use and
   Cloudflare Access; would force a per-user process.
2. **HTTP+SSE (legacy).** Deprecated direction; SDK keeps it for compatibility.
3. **Streamable HTTP, stateful sessions.** Full protocol support including
   server-initiated messages; requires session affinity when scaling.
4. **Streamable HTTP, stateless.** Simplest horizontal scaling; no server→client
   requests; aligns with the sessionless direction of the spec.

## Decision

Use **Streamable HTTP** through `mcp.NewStreamableHTTPHandler`.

- Protocol versions 2025-11-25 through 2026-07-28, negotiated by the SDK.
- Stateful sessions by default, with `Mcp-Session-Id` bound to the authenticated
  identity so a session cannot be hijacked.
- Session state is stored in Redis under `m2m:session:{session_id}` with an idle
  TTL (default 30 minutes), so a restart does not orphan sessions and replicas do
  not require sticky routing. The store holds session metadata only, never
  conversation content.
- Multi-replica deployments share the Redis session store; sticky routing is not
  required. Stateless mode remains available for deployments that prefer it.
- Stateless mode is a documented deployment option, not the default.

## Rationale

Stateful Streamable HTTP gives full protocol fidelity (progress, cancellation,
list-changed notifications) and matches the SDK's best-supported path. Binding a
session to the verified identity closes the session-hijacking gap that remote
MCP servers commonly have. Stateless mode remains available because some
operators will prefer simple horizontal scaling over server-initiated messages.

## Consequences

### Positive

- Full protocol support with the official SDK.
- Session hijacking is prevented by identity binding.
- Operators can choose stateless mode for scale.

### Negative

- Redis becomes part of the MCP availability path; a Redis outage ends sessions
  and clients must reinitialize.
- Session data is operationally sensitive and must be protected like other
  ephemeral state.

### Security impact

Identity-bound sessions are a security requirement, not a convenience. The
transport must also enforce request body size limits, localhost DNS-rebinding
protection, and cross-origin protection; the SDK provides these and the gateway
must not disable them without an ADR.

### Maintenance cost

Low: the SDK owns protocol evolution. Upgrading the SDK is a deliberate action
with contract tests, not an automatic dependency bump.

## Follow-up

- Before Phase 1: decide the default session timeout.
- Phase 1: implement and test identity-bound sessions.
- Revisit stateless-by-default after 1.0 based on real client behavior.

## References

- [Architecture](../architecture.md)
- MCP Go SDK `mcp/streamable.go`
