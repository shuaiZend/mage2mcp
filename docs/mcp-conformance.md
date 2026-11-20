# MCP conformance and best practices

**Status:** Phase 0/1 boundary. This document maps the MCP specification's
requirements and security best practices to mage2mcp's design, records the gaps,
and defines the tests that prove conformance. It is the checklist a reviewer uses
to decide whether the gateway is a well-behaved MCP server.

**References:** MCP specification 2025-11-25 (tools, transports, security best
practices), the official Go SDK v1.8.0 (which negotiates 2025-11-25 through
2026-07-28), and [ADR-0017](adr/0017-mcp-conformance.md).

## 1. Conformance matrix

Legend: **Designed** = specified in this repository, not implemented; **Planned**
= implementation scheduled; **Verified** = test evidence exists.

### 1.1 Tools

| # | Requirement | Design position | Status | Test |
|---|---|---|---|---|
| T1 | Tool names are 1–128 characters from `[A-Za-z0-9_.-]`, unique | Names are `module.domain.action`, lowercase, dot-separated; stricter than the spec and a subset of it | Designed | `scripts/check-contracts.mjs` rejects a name outside the grammar or a duplicate |
| T2 | Every tool has a `title` and a description that states what it does and does not do | Required fields in the tool contract; descriptions are static and reviewed like code | Designed | Contract test asserts both fields exist for every catalog row |
| T3 | `inputSchema` is a valid JSON Schema object with `additionalProperties: false` | Required; SDK validates inputs before the handler runs | Designed | Schema lint in Phase 1; reference schemas in the catalog |
| T4 | `outputSchema` is present for structured results | Required for every tool that returns data; free-form text is the exception, not the default | Designed | Phase 1 registry test rejects a structured tool without an output schema |
| T5 | Structured results use `structuredContent` and also return serialized JSON in a `TextContent` block for backward compatibility | Adopted: structured content plus a text fallback containing the same JSON | Implemented | Conformance test parses both and compares |
| T6 | Tool errors carry a machine-readable envelope (`code`, `message`, `details`, `retryable`) in `structuredContent` | Adopted: every tool registers through `internal/mcp/toolerr`; schema failures return `invalid_argument` | Implemented | `internal/mcp/toolerr` tests cover handler errors, schema errors, and success |
| T6 | Annotations (`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`) are accurate | Required per tool; a write marked read-only is a defect | Designed | Matrix test compares annotation to the declared scope |
| T7 | Invalid arguments are protocol errors; execution failures are tool errors (`isError: true`) | SDK handles schema rejection as a protocol error; handlers return typed tool errors | Designed | Integration test: bad schema → protocol error; upstream failure → `isError` |
| T8 | List-shaped results paginate; no unbounded arrays | Opaque cursors, per-tool caps, `meta.pagination` | Designed | Test asserts a cursor is returned at the cap and that a page never exceeds it |
| T9 | Long operations support progress and cancellation | `progressToken` is honored; `notifications/cancelled` cancels the context; jobs expose `platform.job.cancel` | Designed | Test cancels mid-flight and asserts no upstream call after cancellation |
| T10 | Large payloads use `resource_link` instead of inflating the tool result | Adopted for reports and import reports above the inline cap; `result_ref` stores the object | Designed | Test returns a link for a 10k-row report and never the full body |
| T11 | Tool results are bounded in size | Inline result cap 8 KiB; larger results go to object storage behind a resource link | Designed | Test asserts the cap and the link fallback |
| T12 | No universal execution tool | No `execute_api`, `execute_sql`, or raw GraphQL tool | Designed | Contract test fails if such a name appears |

### 1.2 Transport and sessions

| # | Requirement | Design position | Status | Test |
|---|---|---|---|---|
| S1 | Streamable HTTP with `Mcp-Session-Id` | `mcp.NewStreamableHTTPHandler`; stateful by default | Designed | Protocol test initializes, calls, terminates |
| S2 | Session IDs are cryptographically random and bound to the authenticated identity | SDK generates the ID; the gateway binds it to `TokenInfo.UserID` and rejects a foreign session | Designed | Test presents a session ID with a different identity → 404 |
| S3 | Sessions terminate on `DELETE` and expire on an idle timeout | Redis-backed session store with a 30-minute idle TTL; `DELETE` removes it | Designed | Test terminates and asserts the session is gone |
| S4 | Resumability via `Last-Event-ID` when an event store is configured | Optional `EventStore`; required for long-running streamed operations | Designed | Test reconnects with `Last-Event-ID` and receives missed events |
| S5 | Origin header validation and DNS-rebinding protection | SDK's cross-origin protection and localhost protection stay enabled; disabling requires an ADR | Designed | Test sends a hostile `Origin` and a rebound `Host` → rejected |
| S6 | Request body and header limits | 1 MiB body cap, 8 KiB token cap, header count limit | Designed | Fuzz test with oversized bodies |

### 1.3 Authorization and security best practices

| # | Requirement | Design position | Status | Test |
|---|---|---|---|---|
| A1 | **No token passthrough**: a client token is never forwarded to an upstream API | Vendor credentials are separate per instance; the caller's JWT never leaves the gateway | Designed | Test asserts no upstream request carries the caller token |
| A2 | **Audience binding**: a token is valid only for this gateway and instance | `aud` must equal the configured Access application audience; a mismatch is 401 | Designed | Forged `aud` test |
| A3 | **Scope minimization** | Per-tool scopes, default deny, per-instance grants, read-only default | Designed | Role × tool × instance matrix |
| A4 | **Confused deputy**: no proxy path that lets a client use the gateway's authority for a resource it cannot access | Every upstream call is authorized against the caller's grants and instance; no shared upstream identity across instances | Designed | Cross-instance access test returns 404 |
| A5 | **Session hijacking**: session ID alone is not authority | Session bound to identity; a session ID without the matching identity is rejected | Designed | Session fixation test |
| A6 | **Local server compromise**: localhost binding and DNS rebinding are handled | Gateway binds a configured interface; localhost protection stays on; production deployment is behind Cloudflare Tunnel | Designed | Rebinding test |
| A7 | Protected resource metadata (RFC 9728) and `WWW-Authenticate` for OAuth discovery | Served when OAuth mode is enabled; optional for Access-only deployments | Planned | Metadata endpoint test |
| A8 | OAuth 2.1 resource server behavior (PKCE, no implicit flow) when OAuth mode is used | Out of scope for v0.x; Cloudflare Access is the IdP. The OIDC fallback is documented but not implemented | Planned | OAuth conformance suite in Phase 6 |

### 1.4 Protocol hygiene

| # | Requirement | Design position | Status | Test |
|---|---|---|---|---|
| P1 | Protocol version negotiation | SDK negotiates 2025-11-25 through 2026-07-28; the server does not hardcode a version | Designed | Test initializes with each supported version |
| P2 | `notifications/tools/list_changed` when the registry changes | Emitted when a module is disabled or enabled at runtime | Designed | Test disables a module and observes the notification |
| P3 | Logging does not leak secrets and does not use MCP logging as a control channel | `slog` to stderr; MCP logging is deprecated in 2026-07-28 and is not used | Designed | Redaction tests |
| P4 | Health and readiness are HTTP endpoints, not MCP tools | `/healthz`, `/readyz`, `/livez` | Designed | Deployment test |
| P5 | Errors are stable and documented | Closed `errs.Code` set, documented in the tool catalog | Designed | Contract test asserts the closed set |

## 2. Gaps found in this review

The design already covered naming, schemas, annotations, pagination, errors, and
the main security controls. This review found five gaps, all now closed in the
design:

1. **Output schemas were required by the contract but only two examples existed.**
   The catalog now states the policy per tool class and Phase 1 requires a schema
   for every structured tool before registration.
2. **Structured content had no text fallback.** The spec says a tool returning
   structured content *should* also return serialized JSON in `TextContent`.
   Adopted; the conformance test compares both.
3. **Progress and cancellation were unspecified.** `progressToken` handling and
   `notifications/cancelled` are now part of the tool contract, with the job
   cancellation path for long work.
4. **Large results had no resource-link path.** Reports above the inline cap now
   return a `resource_link` backed by `result_ref` instead of truncating.
5. **Token passthrough and audience binding were implied but not stated.** Both
   are now explicit controls with tests, and the security review lists them.

## 3. Tool description quality

The MCP specification treats descriptions as model-facing interface text. Rules:

- First sentence: what the tool does, the data source, and whether it reads or writes.
- State the most important limitation (sampling, freshness, missing ecommerce, plan gating).
- State the unit, time zone, and currency of any number returned.
- Never include upstream data in a description; descriptions are static.
- Do not instruct the model to chain tools or to bypass confirmation.
- Keep descriptions under 1,024 characters; put detail in the output schema and docs.

## 4. Conformance tests to implement

| Test | Phase | Level |
|---|---|---|
| Tool name grammar, uniqueness, title, description, scope, annotations | 1 | Contract |
| Input schema rejects unknown fields and oversized values | 1 | Unit |
| Output schema present and structured content validates | 1 | Unit |
| Text fallback equals structured content | 1 | Unit |
| Protocol error vs tool error distinction | 1 | Integration |
| Pagination cap and cursor behavior | 1 | Integration |
| Progress notification emitted for a long tool | 1 | Integration |
| Cancellation stops upstream calls | 1 | Integration |
| Resource link for oversized result | 2 | Integration |
| Session identity binding and termination | 1 | Integration |
| Hostile Origin and rebinding rejected | 1 | Security |
| No token passthrough | 1 | Security |
| Audience mismatch rejected | 1 | Security |
| Protocol version negotiation | 1 | Protocol |
| `tools/list_changed` on module toggle | 1 | Protocol |

## 5. Open questions

| Question | Owner | Resolution point |
|---|---|---|
| Whether to advertise MCP `resources` for large report bodies or keep `resource_link` only | MCP owner | Phase 2 |
| Whether to expose prompts for common analyses | MCP owner | Post-1.0 |
| Whether the OAuth resource-server mode is needed for non-Cloudflare deployments | Security | Phase 6 |

## 6. Verification status

The gateway is implemented and released as a pre-1.0 product. Rows marked
`Implemented` have passing tests in the repository; remaining `Planned` or
`Designed` rows are roadmap items tracked in the documentation. A release is not
considered conformant until every `Designed` row has a passing test.
