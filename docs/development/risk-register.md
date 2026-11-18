# Risk register

**Status:** Phase 0 baseline. Reviewed at the end of every phase and whenever a
new integration or data source is added.

Scoring: **Likelihood** and **Impact** are Low / Medium / High. A risk with
Impact High and no acceptable mitigation blocks the phase that introduces it.

## 1. Technical risks

| ID | Risk | L | I | Mitigation | Owner | Review point |
|---|---|---|---|---|---|---|
| R1 | Go 1.26+ requirement excludes contributors on older toolchains | M | M | Document the floor (ADR-0013); `toolchain` directive in `go.mod`; CI matrix 1.26/1.27; toolchain auto-download in the quickstart | Architect | Phase 1 |
| R2 | Module proxy unreachable in some regions blocks local builds | M | L | Document `GOPROXY=https://goproxy.cn,direct` as a fallback; CI uses the default proxy; no vendoring by default | Architect | Phase 1 |
| R3 | Google API domains unreachable from some networks prevents GA4 verification | M | M | GA4 verification runs in CI or from an allowed network; document the constraint; keep `[U]` markers honest | GA4 owner | Phase 4 |
| R4 | Target GA4 property has no ecommerce data, so ecommerce scenarios cannot be numerically validated | H | M | Capability probe returns `ecommerce: absent`; tests use a synthetic property or recorded fixtures; never claim numerical validation | GA4 owner | Phase 4–5 |
| R5 | Magento 2.4.x endpoint or payload differences from documentation | H | H | Treat `/rest/{store}/schema?services=all` as source of truth; capability detection at runtime; per-version integration tests | Magento owner | Phase 2 |
| R6 | Cloudflare plan entitlements differ from assumptions (bot fields, Logpush datasets, Instant Logs) | H | M | Startup capability detection; plan matrix documented; tools degrade with explicit `unsupported_capability` | Cloudflare owner | Phase 3 |
| R7 | Logpush costs grow unexpectedly or logs are lost during job failure | M | H | Off by default; byte budgets and alerts; field minimization; R2 archive for replay; document no-backfill | Cloudflare owner | Phase 3 |
| R8 | Time zone and currency mismatches corrupt unified analysis | H | H | UTC storage; per-source local-day mapping; property time zone carried in every GA4 result; explicit currency in every money value; tests for DST and currency mismatch | Unified owner | Phase 5 |
| R9 | MCP protocol and SDK evolution breaks clients | M | M | Pin SDK minor; upgrade through an ADR and contract tests; negotiate protocol versions rather than hardcoding one | Architect | Every phase |
| R10 | Single maintainer cannot satisfy independent-review requirements | H | M | Independent agent review with committed review records; temporary admin bypass documented; recruit a second maintainer before 1.0 | Maintainer | Phase 6–7 |
| R11 | SSRF through configurable base URLs or image URLs | M | H | Allowlist schemes and hosts; no redirects; block link-local and metadata IPs; no user-supplied URLs in tools | Security | Phase 1 |
| R12 | Prompt injection through upstream data (product names, log fields, item names) manipulates the agent | H | H | Treat all upstream text as untrusted; no instruction-bearing fields in tool descriptions; confirmation for writes; output encoding; documented residual risk | Security | Phase 1–6 |
| R13 | Confirmation token replay or parameter substitution approves a different write | L | H | Canonical parameter hash, nonce, TTL, single use, identity and tool binding; adversarial tests | Security | Phase 1 |
| R14 | Supply chain compromise (dependency, action, base image) | M | H | Pinned versions, go.sum, dependency review, Dependabot, SBOM, cosign, action SHA pinning policy | Maintainer | Continuous |
| R15 | Import partial failure leaves an inconsistent catalog | M | H | Validate → preview → execute; idempotency keys; per-item results; resumable batches; compensation for parent/child link steps; never report success on partial failure | Magento owner | Phase 2 |
| R16 | Vendor rate limits or quota exhaustion cause cascading failures | H | M | Per-instance token buckets, query budgets, caching, backoff, `rate_limited` errors with retry hints, quota metrics | Module owners | Phase 2–4 |
| R17 | Multi-instance misconfiguration leaks data across stores or zones | L | H | Instance-scoped credentials, explicit `instance` parameter, authorization per instance, tests for cross-instance access | Security | Phase 1–2 |
| R18 | Long-running tools trigger agent retry storms and duplicate work | M | M | Synchronous timeout budget, job handles for long work, idempotency, `Retryable` false for non-idempotent writes | MCP owner | Phase 1 |
| R19 | Documentation drifts from implementation | H | M | Docs updated in the same PR; CI link and lint checks; phase exit criteria include docs review | Docs owner | Continuous |
| R20 | No load or soak testing before release | M | H | Phase 6 load tests against mock upstreams with realistic concurrency; publish results | QA | Phase 6 |
| R21 | Adobe Commerce as a Service (IMS auth) users cannot use the gateway | M | M | Detect and reject with a clear error; document out of scope; revisit post-1.0 | Architect | Phase 7 |
| R22 | Third-party GitHub Action compromise | L | H | Pin actions to commit SHAs for security-sensitive jobs; restrict `GITHUB_TOKEN` permissions; review action changes | Maintainer | Phase 1 |
| R23 | Audit write failure policy is undefined, risking silent loss of evidence | M | H | Decide fail-closed vs degraded before Phase 1 exit; default fail-closed for writes | Security | Phase 1 |
| R24 | Credential rotation requires a restart | M | M | Document restart requirement initially; design hot reload for a later phase | Architect | Post-1.0 |
| R29 | Magento OAuth 1.0a signing is implemented incorrectly (canonicalization, nonce, timestamp) and every request fails or is replayable | M | H | Signature-vector tests against Magento's own client; nonce/timestamp uniqueness; clock-skew handling; fallback documented | Magento owner | Phase 2 |
| R30 | Import tools unavailable on Magento Open Source (F25), surprising operators | H | M | Capability detection returns `unsupported_capability`; documentation states the edition requirement; chunked REST fallback designed | Magento owner | Phase 2 |
| R31 | Magento 2.4.9 has no read-only ACL for catalog, source-item, or sales-document reads, so a strictly read-only deployment loses those read tools | H | M | Document the trade-off; offer a "read-mostly" profile with an explicitly accepted residual write capability; startup probe reports the profile in use | Magento owner + Security | Phase 2 |
| R32 | The bounded XLSX converter is a security-sensitive parser with a large input surface | M | H | Fuzz the converter; cap decompressed size, entries, and shared strings; reject macros and external links; keep XLSX opt-in per instance | Magento owner | Phase 2 |

## 2. Product and ecosystem risks

| ID | Risk | L | I | Mitigation | Owner |
|---|---|---|---|---|---|
| R25 | Vendor API deprecations (Magento 2.4.x, Cloudflare legacy endpoints, GA4 schema) | H | M | Capability detection, version matrix, deprecation watch, contract tests per version | Module owners |
| R26 | Scope creep into a general-purpose data platform | M | H | Explicit non-goals in README and ADRs; reject `execute_sql`/`execute_api` requests; roadmap discipline | Maintainer |
| R27 | Low adoption because setup is complex | M | M | Docker Compose quickstart, fake data sources for evaluation, clear configuration reference | Docs owner |
| R28 | Trademark or naming conflict with Adobe/Magento | L | M | Use "Magento 2" descriptively, no logos, disclaimer of affiliation in README | Maintainer |

## 3. Accepted risks

| Risk | Why accepted | Revisit when |
|---|---|---|
| Single-maintainer review bypass | No second maintainer yet; mitigated by independent agent review records and protected status checks | A second maintainer joins |
| No multi-tenancy | Out of scope for v0.x; schema reserves `tenant_id` | Enterprise demand appears |
| No Adobe Commerce as a Service support | IMS auth differs materially; better to reject clearly than half-support | Post-1.0 |
| No BigQuery execution | Cost and injection risk; documented boundary instead | A safe, parameterized design exists |
| In-memory MCP sessions | Acceptable for single-replica deployments; stateless mode documented for scale | Multi-replica becomes the default deployment |

## 4. Review cadence

- End of every phase: full register review, close resolved risks, add new ones.
- Quarterly: re-check vendor API changes and dependency advisories.
- On any security advisory affecting the project: immediate review.
