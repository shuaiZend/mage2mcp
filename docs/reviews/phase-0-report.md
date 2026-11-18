# Phase 0 report — architecture and open-source baseline

**Prepared for:** the project maintainer
**Date:** 2026-11-18
**Status:** design complete; independent reviews returned blocking findings; remediation underway — Phase 1 is not yet authorized
**Repository:** <https://github.com/shuaiZend/mage2mcp> (public)
**Branch:** `feat/phase-0-design`

This report summarizes what Phase 0 produced, what was verified, what was not,
which decisions need the maintainer, and what Phase 1 will do first. It is the
approval package required by the project plan.

## 1. Executive summary

Phase 0 delivered a complete design baseline for mage2mcp: architecture, module
boundaries with Go contracts, a 72-tool MCP catalog, database design, three
vendor integration mappings, a unified analytics model with explicit metric
semantics, a STRIDE threat model, a security control review, CI/CD and review
processes, a license policy with a 55-module dependency audit, and thirteen ADRs.

Three assumptions in the original brief were corrected by evidence and the
design now follows the corrected position:

1. **Cloudflare Logpush is not Enterprise-only.** It is available on Free, Pro,
   and Business with usage-based pricing (25 GB/month included, then $0.03/GB to
   R2 or $0.10/GB external). The real risks are cost, dataset entitlement, and
   permanent log loss during job failure — not availability.
2. **Magento GraphQL cannot manage products.** Product writes must use REST.
   GraphQL is used only for storefront verification that a configurable product
   actually renders its variants.
3. **The target GA4 property has no ecommerce data.** It served 370,599 sessions
   in the last measured week and returned zero ecommerce events and zero revenue
   across four independent probes. Ecommerce capability must be probed, never
   assumed.

A fourth finding changes the implementation plan: **Magento's `import/csv` and
`import/json` endpoints are Adobe Commerce features**, not available on Magento
Open Source, and **integration tokens are not usable as bearer tokens by default**
— OAuth 1.0a is the production authentication path. Both are now reflected in the
design, the ADRs, and the risk register.

## 2. Repository and process status

| Item | State |
|---|---|
| Public repository | Created: `github.com/shuaiZend/mage2mcp` |
| License | Apache-2.0, with NOTICE and a third-party notices release bundle |
| Branch protection | `main` protected: PR required, 1 approval, linear history, no force push, required `docs` check |
| Security features | Secret scanning + push protection enabled, Dependabot alerts and security updates enabled, private vulnerability reporting available |
| Release environment | `release` environment requires maintainer approval |
| CI | Docs lint and internal link checks run on every PR; Go jobs activate when `go.mod` lands |
| Workflow validation | `actionlint` clean; YAML parse clean |
| Markdown | `markdownlint-cli2`: 0 issues across 41 files |
| Internal links | 0 broken once this report exists |

The single-maintainer review constraint is handled transparently: `main` requires
one approval, and the maintainer's administrative bypass is documented, with an
independent agent review record committed under `docs/reviews/`. The bypass is to
be removed when a second maintainer joins.

## 3. Deliverables

| Category | Files |
|---|---|
| Root governance | `LICENSE`, `NOTICE`, `README.md`, `README.zh-CN.md`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `SECURITY.md`, `SUPPORT.md`, `CHANGELOG.md`, `CODEOWNERS`, `.editorconfig`, `.gitattributes`, `.gitignore` |
| GitHub | Issue templates, PR template, `ci.yml`, `security.yml`, `dependency-review.yml`, `release.yml`, `dependabot.yml` |
| Core design | `docs/architecture.md`, `docs/module-boundaries.md`, `docs/mcp-tools.md`, `docs/database-design.md`, `docs/unified-analytics.md` |
| Integrations | `docs/integrations/magento.md`, `cloudflare.md`, `ga4.md` plus evidence files |
| Security | `docs/security/threat-model.md`, `docs/security/security-review.md` |
| Development | `docs/development-plan.md`, `agent-workflow.md`, `review-process.md`, `ci-cd.md`, `testing-strategy.md`, `license-policy.md`, `risk-register.md`, `research-baseline.md` |
| Decisions | `docs/adr/0001`–`0013` |
| Reviews | This report and three independent review records under `docs/reviews/` |

Totals: 35 documents, roughly 8,900 lines, 96 `[V-live]` claims, 229 `[V-docs]`
claims, and 75 `[U]` claims each with a stated verification step.

## 4. What the design commits to

- **Standalone modular monolith.** One server binary and one worker; modules
  (`internal/magento`, `internal/cloudflare`, `internal/ga4`) never import each
  other; `internal/unified` declares consumer-side interfaces over neutral types.
- **Capability-driven registration.** Tools exist only for enabled, healthy
  capabilities. One broken vendor disables that module, not the gateway.
- **Read-only by default.** Write tools require explicit configuration and
  permission; destructive operations require a confirmation bound to a canonical
  parameter hash, with a nonce, TTL, and single use.
- **Typed contracts.** JSON Schema 2020-12 with `additionalProperties: false`,
  a fixed error taxonomy, opaque cursors, UTC timestamps, decimal money.
- **Storage separation.** PostgreSQL for state and audit, Redis for queues and
  rate limits, raw Cloudflare logs only in R2/ClickHouse, never PostgreSQL.
- **Explicit analytics semantics.** Unified results carry sources, formulas,
  windows, time zones, currencies, and quality flags. Requests are never treated
  as sessions, and GA4 revenue is never treated as Magento revenue.

## 5. Research findings that changed the design

| Finding | Effect |
|---|---|
| Logpush available on Pro with usage pricing (F13) | Logpush is an opt-in, budgeted feature with a documented cost model, not an Enterprise-only exclusion |
| Bot score fields require Bot Management (F15) | Bot visibility on Pro is explicitly degraded; tools return capability errors rather than guesses |
| Magento GraphQL has no product mutations (F8) | REST is the only write path; GraphQL is storefront verification |
| Integration tokens are not bearer by default (F12) | OAuth 1.0a is the production default; admin tokens are dev-only |
| Import API is Adobe Commerce only (F25) | Open Source import falls back to chunked REST; tools return `unsupported_capability` |
| GA4 compatibility is per field pair (F18, revised) | The validator uses live probes, not blanket scope rules; `itemSku` does not exist |
| Target property has no ecommerce (F21) | Ecommerce scenarios are structurally defined but marked not numerically validated |
| Two dependencies require Go 1.26 | Toolchain floor raised to 1.26 (ADR-0013) |
| Cloudflare requires `Logs: Write` even for Logpush GET | A separate, opt-in credential profile is documented; the default token stays read-only |
| `gitleaks-action` is proprietary | Replaced with the MIT gitleaks CLI in CI |

## 6. Verification status

| Area | Status | Detail |
|---|---|---|
| MCP Go SDK v1.8.0 API and protocol versions | `[V-live]` | Module source inspected; tool registration, Streamable HTTP, auth middleware confirmed |
| Go toolchain | `[V-live]` | Toolchain auto-download to 1.25.0 executed; 1.26 floor derived from dependency `.mod` files |
| Dependency licenses | `[V-live]` | 55 modules resolved and checked via GitHub API and repository LICENSE files |
| GA4 property behavior | `[V-live]` | Reports, compatibility probes, quota, capability detection executed against property 354542598 |
| Magento 2.4.9 API surface | `[V-docs]` | Endpoint map verified against the 2.4.9 OpenAPI schema and released `webapi.xml`; no live instance available in this session |
| Cloudflare API surface | `[V-docs]` | Datasets, quotas, plan matrix, Logpush pricing verified against official docs; no live credentials in this session |
| Magento storefront behavior | `[U]` | Requires a live 2.4.9 instance and admin credentials |
| Cloudflare plan entitlements | `[U]` | Requires a token and zone on the operator's account |
| End-to-end gateway behavior | Not applicable | No implementation exists in Phase 0 |

**Explicitly not verified:** no request was sent to a live Magento or Cloudflare
instance because no credentials were present in this environment. Every such claim
is tagged `[V-docs]` or `[U]`, and `docs/evidence/*.md` contains the exact
read-only probes that would upgrade them to `[V-live]`. Providing credentials for
a test Magento 2.4.9 instance and a Cloudflare test zone is the fastest way to
close this gap before Phase 2 and Phase 3.

## 7. Security summary

The threat model covers 35 threats across seven trust boundaries and all fifteen
mandatory threat classes from the brief. The security review defines 38 controls
with a location and a test for each.

Highest residual risks, in order:

1. **Prompt injection is contained, not prevented.** No write or exfiltration path
   remains reachable without permission, but a manipulated agent can still read
   data the caller is entitled to.
2. **Audit and job state share one PostgreSQL instance.** Without an external
   append-only sink, database compromise loses evidence and state together.
3. **Bearer tokens cannot be revoked before expiry.** Session binding and a
   bounded lifetime limit the window; they do not close it.
4. **Magento integration credentials are powerful.** Blast radius depends on an
   ACL-scoped integration user, which the documentation requires but cannot enforce.
5. **Agents compete with humans for vendor quota.** Budgets and concurrency caps
   reduce the risk; they do not eliminate it.

## 8. License and dependency audit

Apache-2.0 is recommended and adopted: explicit patent grant and retaliation,
trademark clause, enterprise acceptance, and compatibility with every dependency.
MIT was rejected for the missing patent grant; GPL-3.0 for the copyleft burden on
self-hosters and integrators.

The audit resolved 55 modules (9 direct, 46 transitive). All are permissive
(MIT, BSD-2/3, Apache-2.0, ISC, MIT-0). Two exceptions were handled: MIT-0 is now
explicitly allowlisted, and the proprietary `gitleaks-action` was replaced by the
MIT CLI. Two transitive repositories are archived (`gopkg.in/yaml.v3`,
`json-iterator/go`); the first is scheduled for replacement in Phase 1, the second
is an upstream risk we cannot fix.

## 8.1 Independent review outcomes

Three reviewers who authored none of the reviewed documents returned:

| Review | Findings | Verdict | Blocking items |
|---|---|---|---|
| Architecture | 5 Critical, 16 High, 13 Medium, 4 Low | Block (not re-reviewed) | AR-01 to AR-05 |
| Security | final pass: 0 Critical, 10 High, 20 Medium, 3 Low | Approve with changes | F-01 to F-10 are Phase 1 gate conditions |
| Documentation | 0 Critical, 8 High, 21 Medium, 4 Low | Approve with changes | D-01 to D-08 |

The architecture review's five Critical findings, and the four Critical findings in
the security reviewer's first pass, were real design defects, not prose issues. They
were fixed during the review round; the security reviewer re-reviewed the updated
tree and moved to "Approve with changes".

- **Confirmation had no approver.** The nonce was returned to the requester and no
  component could mint an approval. Fixed by ADR-0014: an opaque `confirmation_id`,
  a separate human-only approval surface, a different approver identity, and a
  database constraint that forbids an approved token without an approver.
- **Import idempotency was asserted but not enforced.** Fixed with a partial unique
  index on `(instance_id, payload_sha256)` for active batches.
- **Unified tools had no authorization semantics.** Fixed with named bindings:
  every member instance must be granted, and jobs, permissions, and confirmations
  carry either `instance_id` or `binding_id`.
- **Configurable creation could issue 501 writes in one synchronous call.** Capped
  at 50 children, idempotency key required, larger sets routed to import.
- **No legal path to enqueue a job.** Fixed with `core/jobs.Enqueuer` and ADR-0015,
  plus `platform.job.get` and `platform.job.cancel`.
- **Logpush job inspection exposed destination credentials to read roles.** The tool
  is now `admin`, returns metadata only, and never returns `destination_conf`.
- **The Magento design reintroduced a self-satisfied `confirm=true` flag.** Removed;
  one confirmation implementation now governs every write.
- **The cache key ignored authorization context.** Authorization scope is now part of
  the key; restricted fields are hashed or not cached.

Remaining High findings are Phase 1 gate conditions: Magento ACL manifest and a
startup scope probe, service-token verification path, token replay binding, per-property
GA4 grants, XLSX/gzip decompression limits, R2/ClickHouse in the threat-model scope,
audit key custody, SHA-pinned actions, and release provenance. The architecture
review's High findings (AR-06 to AR-21) also remain open; the Criticals were fixed but
the architecture reviewer has not re-reviewed the updated contracts.

**This means Phase 0 is not approved as-is.** The correct next step is a short
remediation round that closes the remaining High findings and re-runs the security
review, then Phase 1.

## 9. Multi-agent execution and review

Phase 0 ran in five waves with disjoint file ownership:

| Wave | Agents | Output |
|---|---|---|
| 0 | Lead | Repository, governance, CI, architecture, boundaries, ADRs |
| 1 | Magento, Cloudflare, GA4, Security, Docs | Integration designs, threat model, READMEs, governance prose |
| 2 | Data, Platform, License | Database design, CI/CD and testing docs, license policy |
| 3 | Unified, Lead | Unified analytics, development plan, risk register, tool catalog |
| 4 | Architecture, Security, Documentation reviewers | Independent findings and verdicts |
| 5 | Lead | Reconciliation, review records, PR, this report |

Cross-document contradictions found during integration were resolved and the
resolutions recorded: idempotency is two-tier (Redis reservation, PostgreSQL
ledger), instance configuration is a read-only projection, job states are
`queued/running/succeeded/failed/canceled`, Redis keys are namespaced, SKU
normalization is confined to mapping proposals, and the import contract has an
explicit `batch_id` and a `Resume` method.

## 10. Top risks

| Risk | Mitigation |
|---|---|
| Magento schema drift from documentation | Treat `/rest/{store}/schema?services=all` as source of truth; capability detection |
| Cloudflare plan entitlements unknown until tested | Startup capability detection; explicit degradation |
| GA4 property lacks ecommerce | Capability probe; scenarios marked not numerically validated |
| Time zone and currency mismatches | UTC storage, per-source local-day alignment, explicit currency |
| Logpush cost and log loss | Off by default, budgets, R2 archive, documented no-backfill |
| OAuth 1.0a signing defects | Signature-vector tests in Phase 2 |
| Single-maintainer review | Independent agent review records; bypass removed when a second maintainer joins |

The full register has 30 entries in `docs/development/risk-register.md`.

## 11. Decisions requested from the maintainer

1. **Authorize a Phase 1 remediation round** to close the remaining High findings
   from the security review, followed by a re-review. Phase 0 is not approved as-is.
2. **Confirm the corrected positions** on Logpush, GraphQL writes, the GA4
   ecommerce gap, OAuth 1.0a, and the Go 1.26 floor.
3. **Decide how to supply test credentials** for live Magento and Cloudflare
   verification (environment variables on the operator's machine, never committed).
4. **Accept the temporary single-maintainer merge bypass**, with independent
   review records, until a second maintainer joins.
5. **Confirm the v0.x compatibility promise**: breaking changes allowed with an
   ADR and a changelog entry until 1.0.

## 12. Phase 1 readiness

Phase 1 can start immediately. The first tasks are:

1. `go.mod` with `go 1.26.0` and `toolchain go1.27.4`; directory skeleton.
2. Core packages: config, errors, models, capabilities, `DataSource`.
3. PostgreSQL schema and migrations from `database-design.md`.
4. JWT verifier, RBAC, audit writer, confirmation service.
5. MCP server with Streamable HTTP, registry, health and metrics.
6. Dockerfile and Compose file; `.env.example`.
7. Activate Go CI gates and add the `guard` job that fails when `go.mod` is absent.

Phase 1 exit criteria are listed in `docs/development-plan.md` §4.

## 13. Approval

Phase 0 is complete and internally reviewed. The design is implementable, the
external claims are tagged and sourced, the unresolved items are listed with
verification steps, and the security and licensing positions are explicit.

**Requested decision:** authorize the remediation round described in §8.1. Phase 1
starts after the remaining High findings are closed and the security review is
re-run. The alternative — accepting the listed High risks and starting Phase 1
immediately — is available but not recommended.
