# Security review

**Status:** Phase 0 design review. This document records the controls the system
is required to implement, where each control lives, and how it will be proven.
Nothing here is implemented yet; implementation status is tracked in the table in
§2 and updated at every phase exit.

**Related:** [Threat model](threat-model.md), [ADR-0006](../adr/0006-authentication.md),
[ADR-0010](../adr/0010-error-model-and-tool-contract.md),
[ADR-0012](../adr/0012-observability-and-audit.md),
[SECURITY.md](../../SECURITY.md).

## 1. Scope and method

The review covers the gateway process, its authentication and authorization
path, tool execution, upstream integrations, storage, and the CI/CD supply chain.
It does not cover the security of Magento, Cloudflare, or Google themselves; those
are reported upstream.

Method:

1. Enumerate assets and trust boundaries (threat model).
2. Derive required controls from STRIDE analysis and the project brief's fifteen
   mandatory threat classes.
3. Map each control to an implementation location and a test.
4. Record residual risk where a control cannot fully eliminate a threat.
5. Re-review at every phase exit and on any trust-boundary change.

## 2. Control status

Status legend: **Designed** (this phase), **Planned** (implementation scheduled),
**Implemented**, **Verified** (test evidence exists).

| ID | Control | Location | Test | Status |
|---|---|---|---|---|
| A1 | JWT signature verification with JWKS | `internal/security/jwt` | Unit + adversarial | Designed |
| A2 | Algorithm allowlist (RS256/ES256), reject `none`/HMAC confusion | `internal/security/jwt` | Adversarial | Designed |
| A3 | `iss`/`aud`/`exp`/`nbf` validation with bounded clock skew | `internal/security/jwt` | Unit | Designed |
| A4 | JWKS caching by `kid` with rotation and fetch limits | `internal/security/jwt` | Unit + integration | Designed |
| A5 | Session bound to verified identity | `internal/mcp` | Integration | Designed |
| A6 | Human vs service identity classes separated | `internal/security/auth` | Unit | Designed |
| Z1 | Default-deny RBAC over role × tool × instance | `internal/security/rbac` | Table-driven | Designed |
| Z2 | Tool-level permission declaration enforced by registry | `internal/mcp/registry` | Unit | Designed |
| Z3 | Instance scoping on every service call | Modules | Integration | Designed |
| Z4 | Read-only default; write tools disabled unless configured | Registry + config | Integration | Designed |
| C1 | Confirmation token bound to canonical parameter hash | `internal/security/confirm` | Adversarial | Designed |
| C2 | Opaque confirmation id, approver identity, TTL, single use, parameter binding | `internal/security/confirm` | Adversarial | Designed |
| C3 | Confirmation audited, including refusals | `internal/security/audit` | Integration | Designed |
| V1 | JSON Schema 2020-12 validation, `additionalProperties:false` | SDK + tool specs | Contract | Designed |
| V2 | Size limits on tool inputs and request bodies | `internal/mcp` | Integration | Designed |
| V3 | Enum and pattern constraints on identifiers (SKU, attribute code) | Module DTOs | Unit | Designed |
| E1 | No user-supplied URLs; base URLs from validated config only | Config + clients | Integration | Designed |
| E2 | Scheme allowlist (https), no redirect following | HTTP clients | Unit | Designed |
| E3 | Block link-local, loopback, and metadata addresses | `internal/core/httpsec` | Unit | Designed |
| E4 | Response size limits and content-type checks | HTTP clients | Unit | Designed |
| F1 | Import file type/size validation, no zip bombs | `internal/magento/import` | Adversarial | Designed |
| F2 | Image type validation and re-encode | `internal/magento/media` | Adversarial | Designed |
| F3 | Spreadsheet formula injection neutralized on export | Import/report | Unit | Designed |
| S1 | Secrets only from environment or secret manager | `internal/security/secrets` | Unit | Designed |
| S2 | Secret redaction in logs, errors, and metrics labels | `internal/observability` | Adversarial | Designed |
| S3 | Startup refusal on literal secrets in YAML | Config | Unit | Designed |
| L1 | Append-only audit for every tool call, allow and deny | `internal/security/audit` | Integration | Designed |
| L2 | Parameter hash only; no payload contents in audit | Audit | Review + test | Designed |
| L3 | Audit write failure policy (fail closed for writes) | Audit | Integration | Designed |
| R1 | Per-instance token buckets and concurrency semaphores | Modules | Load | Designed |
| R2 | Query budgets (Cloudflare 300/5 min, GA4 10 concurrent) | Modules | Integration | Designed |
| R3 | Job timeouts, cancellation, and retry caps | `internal/jobs` | Integration | Designed |
| P1 | PII minimization and documented data classes | All | Review | Designed |
| P2 | No raw vendor payloads in responses or logs | All | Adversarial | Designed |
| SC1 | Pinned dependencies, `go.sum`, dependency review | CI | CI | Designed |
| SC2 | SBOM, checksums, cosign signature per release | Release | Release dry run | Designed |
| SC3 | `govulncheck`, `gosec`, `gitleaks`, Scorecard | CI | CI | Designed |
| SC4 | Actions pinned by SHA for every job | CI | Policy check | Designed |
| A7 | Service tokens verified only at the Access edge; gateway accepts the JWT assertion only | `internal/security/auth` | Adversarial | Designed |
| A8 | Replay detection bound to `(token hash, session id)` with a bounded window | `internal/security/jwt` | Adversarial | Designed |
| E5 | Startup credential-scope probe: read-only mode requires a 403 on a write endpoint | `internal/magento` | Integration | Designed |
| L4 | Audit intent row written before execution, terminal outcome appended after | `internal/security/audit` | Integration | Designed |
| D1 | R2 and ClickHouse classified as Restricted, with retention and access control | `internal/logs` | Review + test | Designed |

## 3. Authentication

**Verifier requirements**

1. Parse the compact JWT with a bounded size (≤ 8 KiB) and reject malformed
   tokens without reflection or panics.
2. Select the key by `kid` from a JWKS cache; refuse tokens whose `kid` is
   absent, unknown, or maps to a disallowed algorithm.
3. Enforce the algorithm allowlist by inspecting the header **and** the key type;
   never trust the header alone. `none` and symmetric algorithms are rejected.
4. Validate `iss` exactly, `aud` exactly (per environment), `exp` with no more
   than 60 seconds of skew, and `nbf` when present.
5. Reject tokens with an empty subject or a subject that cannot be mapped to an
   identity.
6. Cache JWKS with a bounded TTL, single-flight refresh, and a hard cap on
   refresh frequency to prevent a forced-refresh DoS.
7. Never log the token, its signature, or its claims beyond the identity ID.

**Identity classes**

| Class | Source | Permissions | Audit field |
|---|---|---|---|
| Human | Interactive SSO through Cloudflare Access | Role assigned by maintainer | `identity_class=human` |
| Service | Cloudflare Access service token | Separate role; never inherits a human role | `identity_class=service` |

Vendor credentials (Magento token, Cloudflare token, Google service account) are
**never** treated as caller identity. A caller authenticated as a service cannot
use the gateway to obtain a vendor credential.

## 4. Authorization

- Permissions are explicit rows: `(role, tool_name, instance_id)`. Wildcards are
  expressed only as `tool_name='*'` for a role and require an ADR to add.
- Resolution order: deny by default → role grants → instance scope → capability
  availability. Any missing element denies.
- Read tools require `read` grants; write tools require `write`; destructive
  tools require `destructive` plus confirmation.
- The registry refuses to register a tool without a permission string. A tool
  with no permission is a startup error, not a silent allow.
- Authorization decisions are audited even when denied, with the reason.

## 5. Confirmation protocol

High-risk operations (product deletion, configurable link removal, import start or
resume, inventory overwrite) require approval by a **different human identity**
through a separate surface. The requesting agent never holds both halves of the
ceremony.

1. The agent calls the tool without a confirmation id. The gateway validates the
   parameters, computes `param_hash = SHA-256(canonical_json(params))`, and stores
   `(requester_hash, tool, instance_or_binding, param_hash, confirmation_id,
   expires_at, state='pending')`.
2. The gateway returns `confirmation_required` with the opaque `confirmation_id`
   and a summary generated from the validated parameters. It never returns the
   approval secret and never marks the request approved.
3. A human operator opens the **approval surface**: the `mage2mcp-admin` CLI or a
   separate HTTP endpoint bound to a human-only Access application. The surface
   lists pending confirmations with the parameter summary and hash.
4. The operator approves or rejects. The approval writes `approver_hash`,
   `approved_at`, and `state='approved'`. The approval is rejected unless
   `approver_hash <> requester_hash`, the approver holds the `approver` permission,
   the request is unexpired, and the hash still matches.
5. The agent re-submits the same parameters plus `confirmation_id`. The gateway
   verifies state, hash, identity, tool, instance or binding, TTL (default 300 s,
   hard cap 900 s), and single use, then consumes the token and executes.
6. Consumption and rejection are audited with both identities.

Canonicalization rules are fixed and tested: sorted keys, no insignificant
whitespace, explicit null handling, UTF-8 normalization, and numbers rendered in a
canonical decimal form. A change to canonicalization invalidates all outstanding
confirmations and requires an ADR.

If the approval surface is unavailable, destructive operations fail closed. Reads
and non-destructive writes are unaffected.

## 6. Input and output handling

- Tool inputs are validated against the declared JSON Schema before the handler
  runs; unknown fields are rejected.
- Identifiers (SKU, attribute code, instance ID, property ID) are constrained by
  pattern and length; they are never interpolated into URLs without escaping.
- Upstream responses are decoded into typed structs with size limits; unknown
  fields are ignored, not reflected.
- Upstream text that reaches the agent (product names, log fields) is treated as
  data. It is never concatenated into instructions, tool descriptions, or error
  messages in a way that could be interpreted as guidance.
- Errors expose a code, a safe message, and a correlation ID. Stack traces,
  upstream bodies, and URLs with query parameters never leave the process.

## 7. SSRF and egress

- The only outbound destinations are configured base URLs and fixed vendor
  endpoints. No tool accepts a URL.
- Outbound HTTP uses a dedicated transport: `https` only, redirects disabled,
  dialer that rejects loopback, link-local, and cloud metadata addresses after
  DNS resolution (not only at parse time), and a bounded response body.
- Magento base URL configuration is validated at startup; a URL that resolves to
  a private address is rejected unless the operator explicitly opts in with a
  documented flag for on-premises deployments.
- Image import accepts bytes, not URLs. If a URL fetch is ever added, it requires
  a new ADR and an allowlist.

**Vendor credential profiles.** Magento and GA4 credentials are read-only by
default (GA4 uses `analytics.readonly`). Cloudflare Logpush inspection is the one
exception: Cloudflare requires `Logs: Write` even for `GET /logpush/jobs`, because
job configurations contain destination credentials. The gateway therefore defines
two Cloudflare credential profiles: the default read-only analytics profile, and
an optional log-inspection profile that carries `Logs: Write`. The second is off
by default, is registered only for operators who explicitly enable it, and its
residual risk is documented in the Cloudflare integration design. The gateway
never calls a Cloudflare mutation endpoint, even with that token.

## 8. Files, images, and imports

- Imports accept CSV, JSON, and normalized XLSX within a configured size limit;
  content type is sniffed, not trusted.
- XLSX is parsed with a bounded converter: 64 MiB decompressed, 10 000 entries, a
  shared-strings cap, macros rejected, external links ignored. Limits apply after
  decompression, and gzip CSV is opt-in with the same cap.
- Exported CSV/XLSX cells beginning with `=`, `+`, `-`, or `@` are prefixed to
  prevent formula injection when opened in a spreadsheet.
- Images are decoded and re-encoded before upload; SVG is rejected. Dimensions
  and pixel count are bounded to prevent decompression bombs.
- Import execution is asynchronous, idempotent per batch, and auditable per item.

## 9. Secrets

- Secrets come from environment variables or a secret manager adapter. YAML holds
  references (`${VAR}`) only; a literal that looks like a credential is a startup
  error.
- Secrets are never written to PostgreSQL, Redis, logs, metrics labels, error
  messages, or MCP responses.
- The redactor is applied at the logging boundary and again at the error boundary;
  both are tested with representative token shapes.
- Rotation requires a restart in v0.x; this is documented and tracked in the risk
  register.

## 10. Audit

Every tool call produces an audit row:

| Field | Content |
|---|---|
| `event_id`, `occurred_at` | UUIDv7, UTC timestamp |
| `subject_hash`, `identity_class` | Verified subject and class |
| `tool`, `instance_id` or `binding_id` | Registry name and target scope |
| `scope`, `decision` | Permission checked and allow/deny |
| `phase`, `state` | `intent`/`outcome`; `pending`, `succeeded`, `failed`, `denied`, `outcome_unknown`, `degraded` |
| `params_hash` | Canonical hash, never the parameters |
| `error_code` | Typed failure, if any |
| `request_id` | Joins to logs without exposing payloads |
| `confirmation_id` | Present when a confirmation was used |
| `intent_id` | Links an outcome row to its intent row |

An audit **intent** row is written before execution (`decision=allow`, `state=pending`)
and a terminal outcome row after; a crash between them is detectable as a pending
row with no outcome. Audit rows are append-only. The application database role has no `UPDATE` or
`DELETE` grant on the table. Retention defaults to 365 days and is enforced by
partition archive-then-drop: export to the sink, record the digest in a signed checkpoint,
and only then drop the partition. A drop without a recorded checkpoint fails the maintenance run.

## 11. Rate limiting and resource control

- Per-instance token buckets sized from configuration, with a conservative
  default; Cloudflare queries additionally respect the documented 300-per-5-minute
  budget.
- Per-identity concurrency limits to stop a single agent from exhausting the
  gateway.
- Global in-flight request cap with a fast `429`/`rate_limited` response.
- Job queues have per-queue concurrency and retry caps; a poison job is parked,
  not retried forever.

## 12. Supply chain

- Dependencies are pinned; `go.sum` is enforced; `GOFLAGS=-mod=readonly` in CI.
- `govulncheck` and `gosec` run on every PR once Go code exists; `gitleaks` runs
  on every push and PR.
- Dependency review blocks high-severity advisories and disallowed licenses on
  PRs that change manifests.
- Releases produce an SPDX SBOM, SHA-256 checksums, and a cosign signature.
- Every action is pinned to a 40-character commit SHA. The `actions-pinned` CI job
  rejects a tag reference, and the repository enforces `sha_pinning_required`.
- No `pull_request_target` workflow may check out and execute fork code.

## 13. Secure defaults checklist

| Default | Value |
|---|---|
| All modules disabled until configured | Yes |
| Read-only tools enabled, write tools disabled | Yes |
| Destructive tools require confirmation | Yes |
| Unknown configuration keys | Rejected |
| Unknown JWT algorithms | Rejected |
| Unknown tool permissions | Startup error |
| Plaintext HTTP upstream | Rejected |
| Redirects | Not followed |
| Audit | On, fail-closed for writes |
| Metrics/admin surface | Private listener |
| Log level | Info, no payloads |
| Session timeout | Configured, non-zero |

## 14. Security test matrix

| Test | Phase | Type |
|---|---|---|
| Forged signature, `alg=none`, HMAC confusion, `kid` injection | 1 | Adversarial |
| Expired, not-yet-valid, wrong audience, wrong issuer tokens | 1 | Unit |
| JWKS rotation and forced-refresh flood | 1 | Integration |
| Missing permission, wrong instance, disabled module | 1 | Table-driven |
| Confirmation replay, parameter substitution, expired or self-approved confirmation | 1 | Adversarial |
| Cross-instance data access | 1–2 | Integration |
| SSRF via base URL, DNS rebinding, redirect to metadata | 1 | Adversarial |
| Oversized JSON, deep nesting, duplicate keys | 1 | Fuzz |
| Zip bomb, formula injection, polyglot image | 2 | Adversarial |
| Prompt injection through product and log fields | 2–3 | Adversarial |
| Rate limit and quota exhaustion behavior | 2–4 | Load |
| Concurrent imports for the same SKU | 2 | Race |
| Audit write failure behavior | 1 | Integration |
| Secret redaction in logs and errors | 1 | Adversarial |
| Dependency and license policy | 1 | CI |

## 15. Residual risks

1. **Prompt injection cannot be eliminated.** Upstream text can influence an
   agent's reasoning. Mitigations reduce impact (confirmation, least privilege,
   no instruction-bearing output), not likelihood.
2. **Confirmation depends on a human understanding the summary.** A misleading
   summary is a design risk; summaries must be generated from validated
   parameters, not from upstream text.
3. **Single-maintainer review.** Independent agent review is weaker than a second
   human maintainer; accepted until a second maintainer joins.
4. **Cloudflare Access as a single identity dependency.** A misconfigured Access
   policy or issuer outage affects all authentication. Documented OIDC fallback
   is planned.
5. **Vendor-side compromise.** A compromised Magento, Cloudflare, or Google
   response is handled as untrusted input, but data returned to the agent is only
   as trustworthy as the vendor.

## 16. Review history

| Date | Reviewer | Scope | Result |
|---|---|---|---|
| 2026-11-18 | Security architect (agent) | Phase 0 design | Controls defined; implementation pending |

This table is updated at every phase exit. A release without a completed review
row for the current phase is not releasable.
