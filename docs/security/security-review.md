# Security review

**Status:** As-built review for v0.11.9
(2026-11-20). This document records the controls that exist, where they live, and
which test proves them. It supersedes the Phase 0 design review. Controls that
are not implemented are marked `PLANNED` and their residual risk is stated in the
[threat model](threat-model.md) and [known limitations](../audit/known-limitations.md).

**Verification boundary.** No live Magento, Cloudflare, or GA4 instance was
available. Magento coverage is `TESTED (CONTRACT)` against `httptest` fakes; the
end-to-end harness uses a fake Magento. Nothing here is `INTEGRATION_TESTED`.

**Related:** [Threat model](threat-model.md), [SECURITY.md](../../SECURITY.md),
[ADR-0006](../adr/0006-authentication.md), [ADR-0014](../adr/0014-confirmation-approval-and-bindings.md),
[ADR-0017](../adr/0017-mcp-conformance.md).

## 1. Scope and method

The review covers the gateway process: HTTP ingress and credential verification,
the tool registry and RBAC, the confirmation ceremony, the operation ledger and
reconciler, audit, the module clients, configuration, and the release pipeline.
It does not cover Magento, Cloudflare, Google, the IdP, the agent runtime, or the
operator's network.

Status labels: **TESTED (CONTRACT)**, **TESTED (PostgreSQL)**,
**IMPLEMENTED (no direct test)**, **PLANNED**, **DEPLOYMENT CONTROL**,
**DEPRECATED**. `INTEGRATION_TESTED` is not used because no live Magento test
exists.

## 2. Control status

| ID | Control | Location | Test | Status |
|---|---|---|---|---|
| A1 | JWT signature verification with JWKS | `internal/security/jwt/jwt.go` | `TestVerifyValidRS256`, `TestVerifyValidES256` | TESTED (CONTRACT) |
| A2 | Algorithm allowlist (RS256/ES256); `none`/HMAC rejected | `internal/security/jwt/jwt.go` | `TestVerifyAlgorithmRejections` | TESTED (CONTRACT) |
| A3 | `iss`/`aud`/`exp`/`nbf` with bounded skew | `internal/security/jwt/jwt.go` | `TestVerifyClaimRejections` | TESTED (CONTRACT) |
| A4 | JWKS `kid` lookup, cache, refresh limiter, redirects refused | `internal/security/jwt/jwt.go` | `TestVerifyKidRejections`, `TestVerifyJWKSRedirectRejected`, `TestVerifyJWKSFailureIsRateLimited` | TESTED (CONTRACT) |
| A5 | Session bound to verified identity | `internal/mcp/server/session.go` | `TestSessionIsBoundToItsCreator`, `TestSessionBindingIsInertWithoutIdentity` | TESTED (CONTRACT) |
| A6 | Human vs service identity classes separated | `internal/security/auth/auth.go` | `TestVerifyServiceIdentity` | TESTED (CONTRACT) |
| A7 | Assertion or bearer ingress; assertion precedence; duplicates rejected | `internal/security/auth/auth.go` | `TestMiddlewareAcceptsAssertionHeader`, `TestMiddlewareAssertionTakesPrecedence`, `TestMiddlewareRejectsBadAssertionHeaders`, `TestMiddlewareVerifiesAssertionThroughTheSameVerifier` | TESTED (CONTRACT) |
| A8 | Origin lockdown (Tunnel / authenticated origin pull) | — | — | **DEPLOYMENT CONTROL** (not enforced) |
| A9 | No token passthrough: caller credential never sent upstream | `internal/magento/client.go`, `internal/cloudflare/client.go`, `internal/ga4/client.go` | `TestGetProductRequestAndResponse` (asserts the vendor signer header) | IMPLEMENTED (no direct test) |
| A10 | Audience binding to the configured application | `internal/security/jwt/jwt.go` | `TestVerifyClaimRejections` | TESTED (CONTRACT) |
| A11 | Origin/DNS-rebinding protection | MCP SDK / deployment | — | **DEPLOYMENT CONTROL** |
| A12 | Session ids random; identity binding | MCP SDK + `internal/mcp/server/session.go` | `TestSessionIsBoundToItsCreator` | TESTED (CONTRACT) |
| Z1 | Default-deny RBAC over role × tool × instance | `internal/security/rbac/rbac.go` | `TestDefaultDeny`, `TestScopeHierarchy`, `TestInstanceScoping`, `TestDenyOverridesAllow` | TESTED (CONTRACT) |
| Z2 | Tool-level permission enforced by the registry | `internal/mcp/registry/registry.go` | `TestRegisterRejectsInvalidTools`, `TestAuthorizationReceivesRequiredScope` | TESTED (CONTRACT) |
| Z3 | Instance scoping on every tool call | `internal/magento/resolver.go`, `internal/mcp/server/server.go` | `TestCrossInstanceAuthorizationIsEnforced` | TESTED (CONTRACT) |
| Z4 | Read-only default; writes gated by `tools.writes` | `internal/core/config/config.go`, `cmd/server/main.go` | `TestReadOnlyConfigurationRegistersNoWriteTools` | TESTED (CONTRACT) |
| Z5 | Role provenance: IdP claim, unknown role grants nothing | `internal/security/jwt/jwt.go`, `internal/security/rbac/rbac.go` | `TestVerifyRolesComeFromTheClaim`, `TestUnknownRoleCannotWidenAccess`, `TestDefaultPolicyExpressesTheLadder` | TESTED (CONTRACT) |
| C1 | Confirmation bound to canonical argument hash | `internal/security/confirm/confirm.go` | `TestConsumeRejectsChangedArguments`, `TestBeginCallRefusesMismatchedConfirmation` | TESTED (CONTRACT + PostgreSQL) |
| C2 | Opaque id, different human, TTL, single use, tool+instance binding | `internal/security/confirm/confirm.go`, `internal/storage/confirm.go` | `TestConsumeSingleUseUnderConcurrency`, `TestApproveRejectsSelfApproval`, `TestConsumeRejectsDifferentTool`, `TestConsumeRejectsDifferentInstance`, `TestConfirmationBoundToToolAndInstanceThroughHTTP` | TESTED (CONTRACT + PostgreSQL) |
| C3 | Confirmation consumption atomic with the ledger row | `internal/storage/operation_ledger.go` | `TestBeginCallConsumesConfirmationAtomically`, `TestBeginCallRollsBackConfirmationOnLedgerFailure` | TESTED (PostgreSQL) |
| C4 | Approval surface verifies the approver identity | — | — | **PLANNED** (SEC-04, accepted: self-asserted `-as`) |
| V1 | JSON Schema validation, `additionalProperties: false` | `internal/mcp/toolerr/toolerr.go`, `internal/mcp/registry/registry.go` | `TestInvalidArgumentsEnvelope`, `TestRegisterRejectsInvalidTools` | TESTED (CONTRACT) |
| V2 | Request body and import size limits | `internal/core/config/config.go`, `internal/magento/import.go` | `TestImportLimitsAgreeAcrossPackages`, `TestHTTPBodyCapRejectsOversizeRequest` | TESTED (CONTRACT) |
| V3 | Identifier patterns and length caps | Module schemas | `TestGetRejectsInvalidSKUWithoutRequest`, `TestUploadMediaValidation` | TESTED (CONTRACT) |
| E1 | No user-supplied URLs; base URLs from validated config | `internal/magento/client.go`, `internal/cloudflare/client.go`, `internal/ga4/client.go` | `TestNewClientValidation`, `TestNewClientConfig` | TESTED (CONTRACT) |
| E2 | https only (loopback http for dev); redirects refused | Module clients | `TestNewClientInstallsTunedTransport`, `TestNewClientValidation` | TESTED (CONTRACT) |
| E3 | IP allowlist / link-local / metadata guard | — | — | **PLANNED** |
| E4 | Response size caps and typed decoding | Module clients | `TestResponseTooLargeIsExplicit`, `TestDoResponseCap` | TESTED (CONTRACT) |
| F1 | Import type/size validation; CSV/JSON only; no zip/XLSX | `internal/magento/import.go` | `TestValidateImportRejectsUnsupportedFormatAndOversize`, `TestValidateImportBoundary` | TESTED (CONTRACT) |
| F2 | Image MIME/size validation | `internal/magento/media.go` | `TestUploadMediaValidation`, `TestUploadMediaRejectsOversizedPayload` | TESTED (CONTRACT) |
| F3 | Image decode/re-encode / EXIF strip | — | — | **PLANNED** |
| S1 | Secrets from environment; `${VAR}` convention | `internal/core/config/config.go` | `TestEnvExpansion` | TESTED (CONTRACT) |
| S2 | Secret redaction in logs | `internal/observability/logger.go` | `TestNewLoggerJSONRedactsSecrets`, `TestNewLoggerRedactsBearerValues` | TESTED (CONTRACT) |
| S3 | Startup refusal on literal secrets | — | — | **PLANNED** (the loader cannot distinguish a literal from an expanded value) |
| L1 | Audit intent/outcome for every call, allow and deny | `internal/mcp/server/server.go`, `internal/security/audit` | `TestAuditReceivesIntentAndOutcome`, `TestAuditDecisionMapsPhaseAndState` | TESTED (CONTRACT + PostgreSQL) |
| L2 | Canonical hash stored with the confirmation/operation, not in audit | `internal/storage/operation_ledger.go`, `internal/security/confirm` | `TestLedgerArgsHashMatchesConfirmation` | TESTED (PostgreSQL) |
| L3 | Audit outcome atomic with the ledger for gated writes | `internal/storage/operation_ledger.go` | `TestFinishCallWritesAuditInTheSameTransaction`, `TestFinishCallRollsBackTransitionWhenAuditFails` | TESTED (PostgreSQL) |
| L4 | Audit gap for the five self-ledgered money tools | `cmd/server/main.go`, `internal/magento/operations.go` | — | IMPLEMENTED (partial; REL-16-R gap) |
| L5 | Hash chain / signed checkpoint / WORM sink | — | — | **PLANNED** (SEC-06) |
| L6 | Audit retention partitions | — | — | **PLANNED** |
| R1 | Per-instance rate limit / concurrency cap | — | — | **PLANNED** (SEC-11, PERF-05) |
| R2 | Upstream quota accounting | — | — | **PLANNED** |
| R3 | Job timeouts, cancellation, retry caps | `internal/jobs/jobs.go` | `TestJobQueueLifecycle`, `TestJobQueueRecoversExpiredLease` | TESTED (PostgreSQL) |
| P1 | PII minimization (customer/order projections) | `internal/magento/customer.go`, `internal/magento/order.go` | `TestGetCustomerOmitsPII`, `TestCustomerAddressesOmitsPII` | TESTED (CONTRACT) |
| P2 | No raw upstream payloads in responses/logs | Module clients, `internal/observability` | `TestErrorMapping`, `TestDoMalformedAndEmpty` | TESTED (CONTRACT) |
| SC1 | Pinned dependencies and `go.sum` | `go.mod`, `go.sum` | `scripts/local-ci.sh` (`go mod verify`) | IMPLEMENTED (CI) |
| SC2 | SBOM, checksums, cosign, provenance | `.github/workflows/release.yml` | release workflow | IMPLEMENTED (CI) |
| SC3 | `govulncheck`, `gosec`, `gitleaks`, Scorecard | `.github/workflows/security.yml` | security workflow | IMPLEMENTED (CI) |
| SC4 | Actions pinned by SHA | `.github/workflows/ci.yml` | `actions-pinned` step | IMPLEMENTED (CI) |

## 3. Authentication

**Ingress.** The gateway accepts exactly one credential. `Cf-Access-Jwt-Assertion`
is authoritative when present; `Authorization: Bearer` is used only when the
assertion is absent. A duplicate header of either kind is rejected before
verification. Both go through the same verifier, so there is no weaker assertion
path.

**Verifier.** Token size ≤ 8 KiB; algorithm allowlist RS256/ES256; key selected by
`kid` from the cached JWKS (never a `kid`-derived URL); `jku`/`x5u` ignored;
redirects refused; exact `iss` and `aud`; `exp` required; `nbf`/`iat` honoured
with at most 60 s skew; JWKS refresh rate-limited with a bounded cache.

**Role provenance.** Roles are the verified token's `roles` claim verbatim. The
IdP is the role authority. The policy decides what each name grants; a name with
no grant grants nothing under default deny. An IdP that lets users choose their
`roles` claim lets them choose their roles — configure the IdP accordingly.

**Origin lockdown.** The gateway does **not** check that the request came from
Cloudflare. A Tunnel or authenticated origin pull is a deployment control. Without
it, any client that can reach the listener can send its own bearer token.

**Identity classes.** Human and service identities are distinct; a service
identity never inherits a human role.

## 4. Authorization

Default deny. The shipped policy grants `viewer`/`analyst` read, `operator`
write, and `admin` admin (which includes the lower scopes). A grant may be scoped
to an instance; a grant on A never authorizes B. An explicit deny beats every
allow. The required scope is read from the registry's declared permission; an
unregistered tool is denied before the authorizer is consulted.

## 5. Confirmation

A write that requires confirmation creates an opaque request when no
`confirmation_id` is supplied. The request is bound to the tool, the instance, and
`sha256(canonical(args))`, and carries a summary built from validated arguments.
A different human approves it through the `mage2mcp-admin` CLI; self-approval is
rejected, the default TTL is 300 s (hard cap 900 s), and consumption is a
compare-and-set. For gated writes the consume is atomic with the ledger row. The
CLI's `-as` identity is self-asserted (SEC-04, accepted): the approval trail is
only as trustworthy as the operator and the database access controls.

## 6. Operation ledger and replay

Every write tool is gated. The middleware opens an `operations` row
(`pending → executing`) with a fencing token and an idempotency key; the five
money tools (`invoice.create`, `invoice.capture`, `creditmemo.create`,
`order.create`, `order.cancel`) drive the ledger themselves. States:
`pending`, `executing`, `succeeded`, `failed`, `unknown`, `reconciling`. Every
transition after `pending` is guarded by the current state and the fence.

An ambiguous outcome (transport failure after send, timeout, 5xx) becomes
`unknown`, never `failed`; the caller must poll, not retry. A repeat with the same
key returns the recorded result with `replayed=true`. A repeated key with
different arguments is rejected. The worker runs a reconcile task every minute,
reads the upstream, and resolves `unknown` rows; inconclusive reads back off and
after eight attempts the operation is escalated for manual review.

**REL-16-R (known gap).** The five self-ledgered money tools write their outcome
audit row through the middleware after the tool commits. A crash in that window
leaves the operation row correct but the audit row missing. The ledger is the
authority on whether money moved; the audit trail has a gap. Closing it requires
those five paths to write their audit row inside their own transaction.

## 7. Input, output, and errors

Tool arguments are validated against the tool's JSON Schema before the handler
runs; unknown fields are rejected; identifiers use patterns and length caps. The
request body cap is 16 MiB by default and is validated to admit a maximum import.
Duplicate JSON keys are still accepted by `encoding/json` (no pre-parse scanner,
PLANNED). Upstream responses are size-capped and decoded into typed structs;
HTML fails decoding and is never echoed. Upstream validation text is
control-character stripped and bounded to 300 runes before it appears in
`details.upstream_message` (SEC-10 accepted: an authorized caller sees bounded
upstream text).

## 8. SSRF and egress

No tool accepts a URL. Media upload takes bytes. Base URLs come from configuration
and must be https (http only for loopback). Redirects are refused. There is no IP
allowlist, DNS pinning, or metadata-address guard (PLANNED); the operator is
trusted not to configure a malicious base URL.

## 9. Files, images, and imports

Imports accept CSV or JSON only, at most 8 MiB and 5 000 rows with 64 columns; the
base64 payload is size-checked before decode; XLSX and gzip are rejected. Import
jobs are bounded and report per-row failures. Media upload accepts base64
JPEG/PNG/WebP/GIF up to 5 MiB with MIME, filename, label and position validation;
it does not decode/re-encode, so EXIF and polyglot trailing bytes are not
stripped (PLANNED). SVG is not accepted.

## 10. Secrets

Credentials are `${VAR}` references by convention. The loader cannot tell a
literal from an expanded value and does not reject literals (PLANNED). The logger
redacts secret-shaped keys and bearer values. The admin credential form keeps the
password in memory and never logs it. The approval CLI trusts a self-asserted
approver (SEC-04).

## 11. Audit

Every tool call writes an intent row and an outcome row with subject hash,
identity class, tool, instance, scope, decision, phase, state, error code and
request id. Denials are recorded as denials. For gated writes the outcome row is
committed in the same transaction as the ledger transition. The canonical
argument hash is stored in `confirmation_tokens` and `operations`, not in
`audit_events` (the `params_hash` column is unused).

**Gaps:** no hash chain, signed checkpoint, WORM sink, or retention partitions
(PLANNED, SEC-06); the five self-ledgered money tools have the REL-16-R audit gap.

## 12. Rate limiting and resource control

**Magento outbound limiting is implemented.** Each instance has a token bucket
(default 50 requests/s, burst 100) and a concurrency cap (default 8), configured
by `datasources.magento.instances[].limits`; an exhausted budget returns a
retryable `rate_limited` without reaching Magento (`internal/magento/limits.go`,
`client.go`). There is **no inbound** per-subject/deployment limiter, no
Cloudflare/GA4 quota accounting, and no admission control; per-tool caps bound a
single call only. The operator must limit inbound concurrency at the agent or
edge (SEC-11).

## 13. Supply chain

Dependencies are pinned; `go.sum` is verified; the security workflow runs
`govulncheck`, `gosec`, `gitleaks` and Scorecard; the release workflow produces an
SPDX SBOM, checksums, cosign signatures and provenance attestations; Actions are
pinned to commit SHAs. CI is manual and release-time only; `scripts/local-ci.sh`
is the day-to-day gate.

## 14. Secure defaults checklist

| Default | Value | Status |
|---|---|---|
| All modules disabled until configured | Yes | TESTED (CONTRACT) |
| Read-only tools enabled, write tools disabled | Yes (`tools.writes: false`) | TESTED (CONTRACT) |
| Destructive tools require confirmation | Yes | TESTED (CONTRACT) |
| Unknown configuration keys rejected | Yes | TESTED (CONTRACT) |
| Unknown JWT algorithms rejected | Yes | TESTED (CONTRACT) |
| Unknown tool permissions rejected at registration | Yes | TESTED (CONTRACT) |
| Plaintext HTTP upstream rejected (loopback excepted) | Yes | TESTED (CONTRACT) |
| Redirects not followed | Yes | TESTED (CONTRACT) |
| Audit on; fail-closed for gated writes | Yes; REL-16-R gap for five money tools | TESTED (PostgreSQL) / known gap |
| Metrics/admin on a private listener | Not enforced by the gateway | DEPLOYMENT CONTROL |
| Rate limiting | Magento per-instance outbound budget implemented; inbound and Cloudflare/GA4 not implemented | IMPLEMENTED (Magento) / PLANNED (inbound) |
| Session identity binding | Bound to the verified subject; mismatched session id gets 404 | TESTED (CONTRACT) |
| Audit hash chain / retention | Not implemented | PLANNED |

## 15. Security test matrix

| Test | File | Status |
|---|---|---|
| Forged signature, `alg=none`, HMAC confusion, `kid` injection | `internal/security/jwt/jwt_test.go` | TESTED (CONTRACT) |
| Expired, not-yet-valid, wrong audience, wrong issuer | `internal/security/jwt/jwt_test.go` | TESTED (CONTRACT) |
| JWKS rotation and forced-refresh flood | `internal/security/jwt/jwt_test.go` | TESTED (CONTRACT) |
| Assertion precedence and duplicate headers | `internal/security/auth/auth_test.go` | TESTED (CONTRACT) |
| Missing permission, wrong instance, unknown role | `internal/security/rbac/rbac_test.go` | TESTED (CONTRACT) |
| Confirmation replay, substitution, tool/instance binding, self-approval | `internal/security/confirm/confirm_test.go` | TESTED (CONTRACT) |
| Cross-instance access | `internal/mcp/server/server_test.go` | TESTED (CONTRACT) |
| Ledger fencing, UNKNOWN, replay, idempotency | `internal/storage/operation_ledger_test.go` | TESTED (PostgreSQL) |
| Gate/audit atomicity | `internal/storage/operation_gate_test.go` | TESTED (PostgreSQL) |
| Money-tool replay and UNKNOWN | `internal/magento/operations_test.go`, `internal/magento/invoice_capture_test.go`, `internal/magento/order_create_test.go`, `internal/magento/document_write_test.go` | TESTED (CONTRACT) |
| Reconciler resolution, backoff, escalation | `cmd/worker/reconcile_test.go` | TESTED (CONTRACT + PostgreSQL) |
| Input schema and typed errors | `internal/mcp/toolerr/toolerr_test.go` | TESTED (CONTRACT) |
| Import bounds | `internal/magento/import_test.go` | TESTED (CONTRACT) |
| Media bounds | `internal/magento/media_upload_test.go` | TESTED (CONTRACT) |
| Response caps and error mapping | `internal/magento/client_test.go`, `internal/cloudflare/client_test.go` | TESTED (CONTRACT) |
| Log redaction | `internal/observability/logger_test.go` | TESTED (CONTRACT) |
| End-to-end fake Magento | `test/e2e/run.sh` | TESTED (E2E, fake Magento) |
| Rate limiting | `internal/magento/limits_test.go` | TESTED (CONTRACT) |
| Session binding | `internal/mcp/server/session_test.go` | TESTED (CONTRACT) |
| Audit hash chain | none | PLANNED |
| Egress IP guard | none | PLANNED |
| Duplicate-key scan | none | PLANNED |
| Image re-encode | none | PLANNED |
| Prompt-injection corpus | none | PLANNED |

## 16. Residual risks

The full list and review triggers are in the [threat model §10](threat-model.md).
The release-relevant ones are: no live Magento verification (MAG-23), no inbound
rate limiting (SEC-11; Magento outbound limits are implemented), no audit hash chain (SEC-06),
the REL-16-R audit gap, origin lockdown and metrics isolation as deployment
controls, multi-instance bindings unsupported, image EXIF not stripped, and
duplicate JSON keys accepted.

## 17. Review history

| Date | Reviewer | Scope | Result |
|---|---|---|---|
| 2026-11-18 | Security architect (agent) | Phase 0 design | Controls defined; superseded |
| 2026-11-20 | Security owner (agent) | As-built v0.11.9 (remediation) | Controls verified to the CONTRACT boundary; gaps listed above |
