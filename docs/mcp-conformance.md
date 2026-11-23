# MCP conformance and best practices

**Status:** as-built for v0.11.10
(2026-11-20). This document maps the MCP specification's requirements and
security best practices to the shipped gateway and names the test that proves
each row. Rows that are not implemented are marked **PLANNED**.

**References:** MCP specification `2025-11-25` (tools, transports, security best
practices), the official Go SDK v1.8.0 (which negotiates every version in
`mcp.SupportedProtocolVersions()`: `2026-07-28`, `2025-11-25`, `2025-06-18`,
`2025-03-26`, `2024-11-05`), and [ADR-0017](adr/0017-mcp-conformance.md).

**Verification boundary.** No live Magento, Cloudflare, or GA4 instance was
available. Tool behaviour is tested against `httptest` fakes (`CONTRACT`); the
database-backed controls are tested against PostgreSQL. No row is
`INTEGRATION_TESTED`.

## 1. Status labels

| Label | Meaning |
|---|---|
| **TESTED (CONTRACT)** | A test exercises the behaviour against a fake upstream or an in-process client. |
| **TESTED (PostgreSQL)** | A test exercises the behaviour against a real PostgreSQL instance. |
| **IMPLEMENTED** | Code exists; no direct test or only partial coverage is stated. |
| **PLANNED** | Designed, not implemented. |
| **DEPLOYMENT CONTROL** | The gateway does not enforce it; the operator must. |

## 2. Tools

| # | Requirement | As-built position | Status | Evidence |
|---|---|---|---|---|
| T1 | Tool names are 1–128 characters from `[A-Za-z0-9_.-]`, unique | `module.domain.action`, lowercase, dot-separated; stricter than the spec | TESTED (CONTRACT) | `internal/mcp/registry/registry_test.go` (`TestRegisterRejectsInvalidTools`, `TestRegisterRejectsDuplicate`) |
| T2 | Every tool has a `title` and a static description | Registry requires both; descriptions are compiled constants | TESTED (CONTRACT) | `registry_test.go`; `internal/magento/tools_test.go` (`TestRegisterMetadata`) |
| T3 | `inputSchema` is a JSON Schema object with `additionalProperties: false` | Every tool declares one; `toolerr` compiles and validates it | TESTED (CONTRACT) | `internal/mcp/toolerr/toolerr_test.go` (`TestInvalidArgumentsEnvelope`), `registry_test.go` |
| T4 | `outputSchema` is present for structured results | `toolerr.Add` infers it from the output type when not set | TESTED (CONTRACT) | `internal/mcp/server/server_test.go` (`TestMCPClientListsAndCallsPlatformInfo` checks the schema) |
| T5 | Structured results use `structuredContent` plus a JSON `TextContent` fallback | `toolerr.Add` sets both; the fallback is the same JSON | TESTED (CONTRACT) | `toolerr_test.go` (`TestStructuredSuccessContent`) |
| T6 | Tool errors carry `{code, message, details, retryable}` in `structuredContent` | `toolerr.errorResult` builds the envelope; schema failures return `invalid_argument` | TESTED (CONTRACT) | `toolerr_test.go` (`TestStructuredErrorEnvelope`, `TestErrorEnvelopeIsUniform`) |
| T7 | Annotations are accurate for the declared scope | Registry rejects a read tool without `readOnlyHint` and a destructive tool without `destructiveHint` + confirmation | TESTED (CONTRACT) | `registry_test.go`; `internal/magento/write_tools_test.go` |
| T8 | Invalid arguments fail before the handler; execution failures are tool errors | The SDK schema check and `toolerr`'s validator reject before the handler; handler errors return `isError: true` | TESTED (CONTRACT) | `toolerr_test.go`; `internal/magento/client_test.go` (`TestGetRejectsInvalidSKUWithoutRequest`) |
| T9 | List-shaped results paginate and are bounded | Magento list tools use `cursor` (offset) + `limit`; each tool has a cap; no unbounded array | TESTED (CONTRACT) | `internal/magento/tools_test.go`; per-tool schema tests |
| T10 | Long operations support progress and cancellation | **Not implemented.** No `progressToken`, no `notifications/cancelled`, no `platform.job.cancel`; imports are polled with `magento.import.status` | PLANNED | — |
| T11 | Large payloads use `resource_link` | **Not implemented.** Results are returned inline within each tool's row cap | PLANNED | — |
| T12 | Tool results are bounded in size | Each tool declares row/field caps; the HTTP body cap is 16 MiB by default | TESTED (CONTRACT) | `internal/magento/import_test.go` (`TestImportLimitsAgreeAcrossPackages`) |
| T13 | No universal execution tool | No `execute_api`, `execute_sql`, or raw GraphQL tool | TESTED (CONTRACT) | `scripts/check-contracts.mjs`; `registry_test.go` |

## 3. Transport and sessions

| # | Requirement | As-built position | Status | Evidence |
|---|---|---|---|---|
| S1 | Streamable HTTP with `Mcp-Session-Id` | The official SDK's `NewStreamableHTTPHandler`; stateful by default, 30-minute session timeout | TESTED (CONTRACT) | `internal/mcp/server/server_test.go` (`TestMCPClientListsAndCallsPlatformInfo`) |
| S2 | Session IDs are cryptographically random and bound to the authenticated identity | The SDK generates the ID; the gateway binds it to the verified subject and answers 404 when a different subject presents it (SEC-15) | TESTED (CONTRACT) | `internal/mcp/server/session_test.go` (`TestSessionIsBoundToItsCreator`, `TestSessionBindingIsInertWithoutIdentity`) |
| S3 | Sessions terminate on `DELETE` and expire on idle | SDK session timeout; no Redis, no server-side session store | IMPLEMENTED (SDK) | `server.go` options; no direct test |
| S4 | Resumability via `Last-Event-ID` with an event store | **Not implemented**; no `EventStore` | PLANNED | — |
| S5 | Origin validation and DNS-rebinding protection | The SDK's defaults are left in place; origin lockdown is a deployment control, not gateway-enforced | DEPLOYMENT CONTROL | — |
| S6 | Request body and header limits | `server.max_body_bytes` (default 16 MiB, write-enabled floor 12 MiB); 8 KiB JWT cap; SDK header limits | TESTED (CONTRACT) | `internal/magento/import_test.go` (`TestHTTPBodyCapRejectsOversizeRequest`), `internal/security/jwt/jwt_test.go` (`TestVerifyRejectsOversizedToken`) |

There is no Redis anywhere in the project (ADR-0019). The job queue and all
durable state are PostgreSQL.

## 4. Authorization and security best practices

| # | Requirement | As-built position | Status | Evidence |
|---|---|---|---|---|
| A1 | **No token passthrough** | The caller credential is never forwarded; each module uses its per-instance vendor credential | IMPLEMENTED (no direct test) | `internal/magento/client_test.go` (`TestGetProductRequestAndResponse` asserts the vendor signer header) |
| A2 | **Audience binding** | `aud` must equal the configured Access application audience | TESTED (CONTRACT) | `internal/security/jwt/jwt_test.go` (`TestVerifyClaimRejections`) |
| A3 | **Scope minimization** | Per-tool scope from the registry; default-deny RBAC; instance-scoped grants; read-only default | TESTED (CONTRACT) | `internal/security/rbac/rbac_test.go`; `server_test.go` (`TestAuthorizationReceivesRequiredScope`) |
| A4 | **Confused deputy** | Every call is authorized against the caller's grants and the resolved instance; no shared upstream identity | TESTED (CONTRACT) | `server_test.go` (`TestCrossInstanceAuthorizationIsEnforced`) |
| A5 | **Session hijacking** | Bound to the verified subject (SEC-15); a mismatched session id gets 404, and transport auth still gates each request | TESTED (CONTRACT) | `internal/mcp/server/session_test.go` (`TestSessionIsBoundToItsCreator`) |
| A6 | **Local server compromise** | The gateway binds a configured interface; localhost protection and origin lockdown are deployment controls | DEPLOYMENT CONTROL | — |
| A7 | RFC 9728 protected-resource metadata | **Not implemented**; Cloudflare Access deployments rely on the edge | PLANNED | — |
| A8 | OAuth 2.1 resource-server mode | Out of scope for v0.x; Cloudflare Access or a direct OIDC issuer is the IdP | PLANNED | — |
| A9 | Ingress accepts `Cf-Access-Jwt-Assertion` or Bearer; assertion precedence; duplicates rejected | Exactly one credential; assertion wins when present; duplicate header of either kind is rejected | TESTED (CONTRACT) | `internal/security/auth/auth_test.go` (`TestMiddlewareAcceptsAssertionHeader`, `TestMiddlewareAssertionTakesPrecedence`, `TestMiddlewareRejectsBadAssertionHeaders`) |
| A10 | Roles come from the verified IdP claim; unknown role grants nothing | The `roles` claim is parsed verbatim; the policy grants only known roles | TESTED (CONTRACT) | `jwt_test.go` (`TestVerifyRolesComeFromTheClaim`), `rbac_test.go` (`TestUnknownRoleCannotWidenAccess`) |

## 5. Protocol hygiene

| # | Requirement | As-built position | Status | Evidence |
|---|---|---|---|---|
| P1 | Protocol version negotiation | The SDK negotiates all versions in `mcp.SupportedProtocolVersions()`: `2026-07-28`, `2025-11-25`, `2025-06-18`, `2025-03-26`, `2024-11-05`; the server does not hardcode a version | IMPLEMENTED (SDK) | `platform.info` reports the full SDK list, asserted against `mcp.SupportedProtocolVersions()` (`server_test.go`) |
| P2 | `notifications/tools/list_changed` when the registry changes | **Not implemented**; the registry is static for the process lifetime | PLANNED | — |
| P3 | Logging does not leak secrets and does not use MCP logging as a control channel | `slog` to stderr with key-based redaction; no MCP logging channel | TESTED (CONTRACT) | `internal/observability/logger_test.go` |
| P4 | Health and readiness are HTTP endpoints, not MCP tools | `/healthz`, `/readyz`, `/metrics`; there is no `/livez` | IMPLEMENTED | `internal/mcp/server/server.go`; `server_test.go` (`TestHealthAndReady`) |
| P5 | Errors are stable and documented | Closed `errs.Code` set; documented in [module-boundaries.md](module-boundaries.md) §4.2 | TESTED (CONTRACT) | `internal/core/errors/errors_test.go`; `toolerr_test.go` |
| P6 | The MCP session is not an authorization boundary | Every `tools/call` is authorized independently; a session id does not carry authority | TESTED (CONTRACT) | `server_test.go` (`TestAuthorizationDeniesToolCall`) |

## 6. Gaps and planned work

| Gap | Status | Consequence |
|---|---|---|
| Progress notifications and cancellation | PLANNED (ADR-0017) | Long work is a polled job, not a cancellable stream |
| `resource_link` and an inline result cap | PLANNED (ADR-0017) | Large results are returned inline within per-tool caps |
| `notifications/tools/list_changed` | PLANNED | The registry does not change at runtime |
| RFC 9728 / OAuth resource-server mode | PLANNED | Access-only deployments rely on the edge |
| Origin lockdown | DEPLOYMENT CONTROL | The gateway does not verify Cloudflare source ranges |
| `/livez` | Not planned | `/healthz` and `/readyz` are the health surface |

## 7. Test map

| Area | Test file | Test names |
|---|---|---|
| Registry contract | `internal/mcp/registry/registry_test.go` | `TestRegisterAcceptsValidTool`, `TestRegisterRejectsInvalidTools`, `TestRegisterRejectsDuplicate` |
| Tool errors/content | `internal/mcp/toolerr/toolerr_test.go` | `TestStructuredErrorEnvelope`, `TestStructuredSuccessContent`, `TestInvalidArgumentsEnvelope`, `TestErrorEnvelopeIsUniform` |
| Server/authz/health | `internal/mcp/server/server_test.go` | `TestMCPClientListsAndCallsPlatformInfo`, `TestHealthAndReady`, `TestAuthorizationDeniesToolCall`, `TestAuthorizationReceivesRequiredScope`, `TestCrossInstanceAuthorizationIsEnforced`, `TestAuthorizationFailsClosedForUnregisteredTool` |
| JWT and ingress | `internal/security/jwt/jwt_test.go`, `internal/security/auth/auth_test.go` | `TestVerifyValidRS256`, `TestVerifyClaimRejections`, `TestVerifyAlgorithmRejections`, `TestMiddlewareAcceptsAssertionHeader`, `TestMiddlewareAssertionTakesPrecedence`, `TestMiddlewareRejectsBadAssertionHeaders` |
| RBAC | `internal/security/rbac/rbac_test.go` | `TestDefaultDeny`, `TestScopeHierarchy`, `TestInstanceScoping`, `TestUnknownRoleCannotWidenAccess` |
| Confirmation | `internal/security/confirm/confirm_test.go` | `TestConsumeRejectsChangedArguments`, `TestConsumeSingleUseUnderConcurrency`, `TestApproveRejectsSelfApproval` |
| Operation gate | `internal/storage/operation_gate_test.go` | `TestBeginCallConsumesConfirmationAtomically`, `TestFinishCallWritesAuditInTheSameTransaction` |
| Import limits | `internal/magento/import_test.go` | `TestImportLimitsAgreeAcrossPackages`, `TestHTTPBodyCapRejectsOversizeRequest` |
| End-to-end | `test/e2e/run.sh` | fake-Magento smoke test (not a live Magento test) |

## 8. Related

- [implemented-tools.md](implemented-tools.md) — the generated inventory.
- [mcp-tools.md](mcp-tools.md) — the tool contract.
- [security-review.md](security/security-review.md) — the as-built control matrix.
- [ADR-0017](adr/0017-mcp-conformance.md) — the conformance decision and its
  planned rows.
