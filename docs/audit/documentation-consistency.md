# Documentation consistency audit and feature-status matrix

| Field | Value |
|---|---|
| Document | Documentation consistency audit (`DOC-*`) |
| Project | mage2mcp — MCP gateway for Magento 2 / Adobe Commerce |
| Commit audited | `66786a5d10c1f78d00e08a5b42d01fcc2da37a3f` (`main`, tag `v0.11.0`) |
| Audit date | 2026-11-20 |
| Owner | Documentation and open-source engineer |
| Method | Docs read in full, claims reproduced against the working tree, tool surface enumerated from the registry, shipped examples executed |
| Code changes | **None.** This report is the only file written by this task. |

> **Baseline note.** Every `path:line` citation is against commit `66786a5`
> (the baseline recorded in [initial-audit-report.md](initial-audit-report.md)).
> The working tree was being remediated concurrently while this report was
> written (source files and migrations were changing), so line numbers in the
> live tree may have shifted. Where a remediation is already in flight, the
> finding says so; the documentation fix is still required.

---

## 1. Executive summary

**Verdict: the documentation is not release-ready.** The repository currently
contains two mutually exclusive descriptions of itself:

1. a **Phase 0 design** with "no runnable server", "no release", "nothing
   implemented", `tests/security/*` suites, Redis, R2/ClickHouse, hash-chained
   audit, rate limiting, and an egress allowlist; and
2. a **shipped v0.11.0 product** with 112 registered tools at full configuration,
   a PostgreSQL job queue, a Docker image, signed releases, and 77 Go test files
   (80 with the in-flight probe tests).

Both descriptions are written in the present tense. A reader cannot tell which
one applies to the code they are looking at, and the security documents — the
ones a reviewer would rely on to approve a production deployment — are the most
divergent from the implementation.

The tool catalogue is the strongest document in the set: the 112 rows in
`docs/implemented-tools.md` match the registry (names exactly; scopes,
confirmation flags and timeouts semantically), and a Go test enforces the
Magento subset. The
weakness is everything around the catalogue: counts are unqualified, status
headers are stale, the security "proof" sections cite tests that do not exist,
and several operator-facing instructions do not run.

### 1.1 Finding counts

| Severity | Count | Character |
|---|---:|---|
| **P0** | 1 | The threat model's read-only sales assumption is false, and the document uses it to justify omitting replay controls for money-moving tools. The code-side defect is [SEC-05](security-audit-report.md); this report owns the documentation half. |
| **P1** | 6 | Repository-wide Phase 0 status; README self-contradiction on implemented controls; 55 cited security-control/test paths that do not exist; audit-integrity claims with no implementation; a quickstart that exits non-zero; release identity v0.9.0 vs v0.11.0. |
| **P2** | 13 | Unqualified tool counts; false single-instance claim; stale tool/conformance/architecture/database/deployment/config/evidence/marketing claims; `platform.info` wiring documented but dead. |
| **P3** | 6 | CHANGELOG structure; ADR statuses; development-doc drift; compatibility omissions; release/admin trust model; stale documentation index. |
| **Total** | **26** | |

No P0 is counted twice: the code defect behind DOC-01 is SEC-05 (P0) in the
security report, and the release verdict should count it once. DOC-01 is the
documentation correction that must land whichever way SEC-05 is resolved.

### 1.2 The five findings that block release

1. **DOC-01** — the threat model says orders and customers are read-only; 15
   order/customer-mutating tools ship (18 with the address tools), including
   `magento.invoice.capture` and `magento.creditmemo.create`. The document uses the false assumption to argue
   that duplicate-charge risk is "removed structurally".
2. **DOC-04** — the threat model's §5 "Proof" paragraphs and §9 test plan cite
   55 files; **none of the 55 exists**. `tests/security/` does not exist at all.
   The security assurance in that document is unbacked.
3. **DOC-02** — `SECURITY.md`, `SUPPORT.md`, `CONTRIBUTING.md`, and five design
   documents say Phase 0 / no release / no runnable server, against a tagged
   v0.11.0 with published artifacts.
4. **DOC-06** — the shipped example configuration exits non-zero:
   `MAGE2MCP_AUTH_ENABLED=false go run ./cmd/server -config configs/config.example.yaml`
   → `bindings require both cloudflare and ga4 modules to be enabled`. The
   README quickstart tells the reader to copy this file and run it.
5. **DOC-07** — README, Chinese README, and `docs/index.md` call v0.9.0 the
   latest release while linking the v0.11.0 tag, and the v0.11.0 table row
   links the v0.9.0 release notes.

### 1.3 What is accurate (verified, do not re-litigate)

- `docs/implemented-tools.md` tool names: 111 non-platform names, exact diff
  against the registry, no extras, no omissions. Scopes, confirmation flags and
  timeouts also match (timeouts differ only in formatting: `60s` vs `1m0s`).
- `internal/magento/tool_contract_test.go:63` (`TestImplementedToolsDocsMatchRegistry`)
  and `:16` (`TestMagentoToolContract`, golden file of 101 names) pass at the
  baseline and genuinely enforce the Magento catalogue.
- Every configuration key documented in `docs/configuration.md` exists in
  `internal/core/config/config.go` with the same name, type, default and
  validation (the omissions are environment variables, not YAML keys — DOC-14).
- `Dockerfile` build args (`VERSION`), `EXPOSE 8080`, distroless non-root base,
  healthcheck, and the Compose port bindings match the code and README.
- `docs/operations/upgrade.md` correctly describes forward-only, embedded,
  advisory-locked migrations recorded in `schema_migrations`
  (`internal/storage/storage.go:87`).
- Module READMEs (`internal/{magento,cloudflare,ga4,unified}/README.md`) are
  accurate as-built descriptions.
- `docs/write-safety.md` matches the shipped `write_safety` prompt and the
  confirmation behaviour: the prompt exists, is listed, its checklist is the
  prompt text, and "every write returns `confirmation_required`" matches
  `internal/mcp/server/server.go`'s confirmation gate.
- All 22 ADRs exist with contiguous numbering (`scripts/check-contracts.mjs`
  enforces 0001-0022); their status lines were reviewed, and the stale ones are
  DOC-22. ADR-0022 is the correct decision record and is contradicted by the
  README/integration docs (DOC-12).
- `CHANGELOG.md` top entry `[0.11.0] - 2026-11-20` matches the tag and date;
  `go.mod` (`go 1.26.0`) matches the README/CI Go floor.
- `docs/compatibility.md` component matrix is accurate for Go, PostgreSQL,
  Magento, Cloudflare, GA4, MCP and Redis-removal (the gaps are DOC-24).

---

## 2. Method and reproducible evidence

All commands were run from the repository root. None modified tracked files.

### 2.1 Tool-surface count (registry, not docs)

The registry is built by `registerModules` in `cmd/server/main.go:249`, which is
unexported. To count it without touching the repository, the audit used Go's
`-overlay` flag to add a temporary test to `package main`:

```bash
# /tmp/m2m-audit/zz_audit_toolcount_test.go (package main) calls
# registerModules(auditCfg(...), reg, importWiring{...}) for four configurations
# and prints reg.Len() plus the sorted tool names.
cat > /tmp/m2m-audit/overlay.json <<EOF
{"Replace": {"$(pwd)/cmd/server/zz_audit_toolcount_test.go":
             "/tmp/m2m-audit/zz_audit_toolcount_test.go"}}
EOF
go test -overlay /tmp/m2m-audit/overlay.json ./cmd/server \
  -run TestAuditToolSurface -v
# The full test source is in Appendix D.
```

Output (baseline `66786a5`):

```text
AUDIT read-only, no database   registry=53  (+platform.info=1) byModule=map[cloudflare:3 ga4:6 magento:43 unified:1]
AUDIT writes, no database      registry=105 (+platform.info=1) byModule=map[cloudflare:3 ga4:6 magento:95 unified:1]
AUDIT read-only, database      registry=59  (+platform.info=1) byModule=map[cloudflare:3 ga4:6 magento:49 unified:1]
AUDIT writes, database         registry=111 (+platform.info=1) byModule=map[cloudflare:3 ga4:6 magento:101 unified:1]
AUDIT multi-instance-with-bindings err=bindings require both cloudflare and ga4 modules to be enabled registry=52
AUDIT multi-instance-no-bindings   err=<nil> registry=52 byModule=map[cloudflare:3 ga4:6 magento:43]
```

The full-surface name list (111 registry names + `platform.info`) was diffed
against `docs/implemented-tools.md`: **0 differences**.

### 2.2 The shipped example configuration

```bash
MAGE2MCP_AUTH_ENABLED=false MAGE2MCP_LISTEN=127.0.0.1:18098 \
  go run ./cmd/server -config configs/config.example.yaml
# {"level":"WARN","msg":"database.url is not set: audit is in memory and lost on restart"}
# mage2mcp: bindings require both cloudflare and ga4 modules to be enabled
# exit status 1
```

### 2.3 Documentation contract checks

```bash
npm run test:contracts
# Design contracts OK: 75 tools, 22 ADRs
#   tools by module: {"magento":42,"platform":2,"cloudflare":10,"ga4":11,"unified":10}
go test ./internal/magento -run 'TestImplementedToolsDocsMatchRegistry|TestMagentoToolContract' -v
# --- PASS: TestMagentoToolContract
# --- PASS: TestImplementedToolsDocsMatchRegistry
```

`scripts/check-contracts.mjs` parses **only** `docs/mcp-tools.md`; it never reads
Go code. The Go docs test covers **only** Magento. Cloudflare, GA4, unified and
`platform.info` documentation drift is unchecked.

### 2.4 Cited-path existence in the threat model

A Python scan extracted every backticked `internal/…` / `tests/…` `.go` path
from `docs/security/threat-model.md` and tested it on disk:

```text
cited paths: 55 present: 0 missing: 55
```

The missing set includes every `tests/security/*.go` file named in §5 and §9,
plus `internal/{egress,quota,ratelimit,cache,redact,registry,secret,upstream,httpx,store,modules}`
and `internal/mcp/{strictjson.go,envelope.go}`.

### 2.5 Baseline test state

At `66786a5` the initial audit recorded `go test ./...` and `go test -race ./...`
green. During this audit, `go test ./internal/... ./cmd/... -count=1` also
passed before concurrent remediation edits began. Storage integration tests are
gated on `MAGE2MCP_TEST_DATABASE_URL` (`internal/storage/storage_test.go:22`)
and skip otherwise; the provisioned container is
`postgres://mage2mcp:mage2mcp-dev@127.0.0.1:55440/mage2mcp`.

---

## 3. Feature-status matrix

Legend:

| Status | Meaning |
|---|---|
| **TESTED** | Implementing code exists and at least one test exercises it against a fake/`httptest` upstream (CONTRACT coverage). |
| **INTEGRATION_TESTED** | Exercised against a real PostgreSQL (gated suite) or a live Magento instance (manual, documented). |
| **IMPLEMENTED** | Code exists with no direct test that proves the documented behaviour. |
| **PLANNED** | Designed and absent; the documentation says so. |
| **DEPRECATED** | Superseded by a later decision. |
| **UNSUPPORTED-BY-CODE** | A document claims current support; no code implements it and the document does not mark it planned. |

### 3.1 MCP platform and protocol

| Capability (documented) | Doc source | Code | Test evidence | Status |
|---|---|---|---|---|
| Streamable HTTP `POST /mcp` | README:143, architecture:295 | `internal/mcp/server/server.go:233-247` | `server_test.go:48-110` | TESTED |
| `platform.info` (name, version, protocols, tools, instances, writes flag) | implemented-tools:22 | `server.go:197-219` | `server_test.go:48-110` | TESTED, but instances/writes are never wired (DOC-20) |
| Tool registry contract (name, title, description, capability, permission, timeout, annotations) | mcp-tools:52-68 | `internal/mcp/registry/registry.go:56-190` | `registry_test.go`; `write_tools_test.go:40-140` | TESTED |
| Structured tool errors `{code,message,details,retryable}` | mcp-conformance:26 | `internal/mcp/toolerr/toolerr.go:22-120` | `toolerr_test.go:50-120` | TESTED |
| Text fallback equal to structured content | mcp-conformance:25 (claims a test) | `toolerr.go:55-66` | none | IMPLEMENTED (no test) |
| `write_safety` prompt | write-safety:11 | `server.go:277-330` | `prompt_test.go:14-60` | TESTED |
| `/healthz`, `/readyz`, `/metrics` | deployment:75-79 | `server.go:247-258` | CI docker smoke (`ci.yml:139-160`) | IMPLEMENTED |
| `/livez` | architecture:320, mcp-conformance:66 | none | none | PLANNED (not marked) |
| Progress notifications / cancellation | mcp-conformance:30 | none | none | PLANNED |
| `resource_link` + 8 KiB inline cap | mcp-conformance:31-32 | none | none | PLANNED |
| `notifications/tools/list_changed` | architecture:196 | none | none | PLANNED |
| Session bound to verified identity | mcp-conformance:40 | none (SDK session only) | none | PLANNED |
| Capability-gated tool registration (`core/datasource`) | architecture:176-207 | contract declared `internal/core/datasource/datasource.go`; **no module implements it** | none | UNSUPPORTED-BY-CODE |

### 3.2 Authentication, authorization, audit

| Capability (documented) | Doc source | Code | Test evidence | Status |
|---|---|---|---|---|
| JWT verification (RS256/ES256, iss/aud/exp/nbf, JWKS) | threat-model:484-501 | `internal/security/jwt/jwt.go` | `jwt_test.go` | TESTED |
| Auth middleware | README:258 | `internal/security/auth/auth.go` | `auth_test.go` | TESTED |
| Cloudflare Access `Cf-Access-Jwt-Assertion` header | threat-model:127,489 | only `Authorization: Bearer` (`auth.go:115-150`) | `auth_test.go` | UNSUPPORTED-BY-CODE (SEC-08) |
| IdP-group → role mapping | threat-model:286, §6.2 | `roles` claim taken from the token (`jwt.go:170-178`) | `jwt_test.go` | UNSUPPORTED-BY-CODE (SEC-09) |
| Default-deny RBAC engine | README:228 | `internal/security/rbac/rbac.go` | `rbac_test.go` | TESTED |
| Per-tool scope enforcement at the call site | threat-model:521-538 | `main.go:236` always passes `rbac.ScopeRead` | none | UNSUPPORTED-BY-CODE (ARCH-01 / SEC-01) |
| Two-step confirmation service | README:174-189 | `internal/security/confirm/confirm.go` | `confirm_test.go` | TESTED |
| Durable confirmation store + self-approval rejection | threat-model:542-552 | `internal/storage/confirm.go:76-110` | `storage_test.go:99` (gated) | INTEGRATION_TESTED |
| Audit append (memory) | README:306 | `internal/security/audit/audit.go` | `audit_test.go` | TESTED |
| Audit append (PostgreSQL) | README:306 | `internal/storage/audit.go:39-46` | `storage_test.go` (gated) | INTEGRATION_TESTED |
| Hash chain, Ed25519 checkpoints, `params_hash`, fail-closed writes, 365-day partitions | threat-model:566-578, ADR-0012:61 | none; `params_hash` is not in the INSERT | none | PLANNED (SEC-06 / REL-16) |
| Rate limiting (subject/instance/deployment) | threat-model:580-594 | none | none | PLANNED (SEC-11) |
| Egress allowlist + IP guard | threat-model:560-562 | none | none | PLANNED |
| Strict JSON duplicate-key scan | threat-model:271 | none | none | PLANNED |
| Log redaction | README:318 | `internal/observability/logger.go:24-100` (key-name based) | `logger_test.go` | TESTED |
| `/metrics` private / Access-protected | monitoring:11-12 | served on the public listener without auth (`server.go:258`) | none | UNSUPPORTED-BY-CODE (SEC-12) |

### 3.3 Magento module

| Capability (documented) | Doc source | Code | Test evidence | Status |
|---|---|---|---|---|
| 43 read tools | implemented-tools:24-128 | `internal/magento/tools.go:87-130` | `tools_test.go:75` asserts 43; per-tool tests | TESTED |
| 52 write tools (43 write + 9 destructive) | implemented-tools:44-128 | `internal/magento/write_tools.go:259-340` | `write_tools_test.go:40-140` | TESTED |
| 6 async import tools | implemented-tools:118-123 | `internal/magento/import_jobs.go:48-90` | `import_jobs_test.go` | TESTED (fake queue) |
| Product / configurable / category / media / MSI / customer / document writes | README:46-62 | `write_tools.go`, `configurable.go`, `category_write.go`, `media.go`, `inventory.go`, `customer.go`, `order_*.go` | per-tool tests (`*_test.go`) | TESTED (CONTRACT) |
| Multi-instance routing for reads **and** writes | configuration:57-60 | `internal/magento/resolver.go:75-90`; `write_tools.go:269-276` | `resolver_test.go`; `cmd/server/main_test.go:120-137` | TESTED |
| Order-mutating tools (15) | (docs deny them: threat-model:32) | `write_tools.go` — see DOC-01 for the full list | `order_create_test.go`, `order_status_write_test.go`, `document_write_test.go`, `invoice_capture_test.go` | TESTED (CONTRACT); **documented as absent** |
| Live read-only Magento acceptance | README:15-17 | n/a | `docs/operations/production-acceptance.md` (2026-11-20, 23 checks, 37 read tools) | INTEGRATION_TESTED (manual) |
| Live Magento write verification | CHANGELOG:16 | n/a | `docs/development/magento-write-verification.md` (v0.8.1, disposable demo) | INTEGRATION_TESTED (manual, demo only) |
| Automated live Magento test | threat-model:640-665 (claims one) | none | none | PLANNED (MAG-23) |
| Magento GraphQL storefront verification | README:356, integrations/magento:20 | none (ADR-0022:20-28 forbids it) | none | UNSUPPORTED-BY-CODE |

Note: the permission dump counts 46 `write` + 9 `destructive` across all
modules. `writeToolNames` has 52 Magento entries (43 write + 9 destructive);
the three write-capable async import tools (`import.start/cancel/resume`) add
the rest, for 55 write-capable Magento tools in total.

### 3.4 Cloudflare, GA4, unified

| Capability (documented) | Doc source | Code | Test evidence | Status |
|---|---|---|---|---|
| `cloudflare.analytics.traffic` | implemented-tools:134 | `internal/cloudflare/tools.go:128-200`, `analytics.go` | `analytics_test.go` | TESTED |
| `cloudflare.security.summary`, `.events` | implemented-tools:135-136 | `security.go` | `security_events_test.go`, `tools_test.go` | TESTED |
| Multi-zone routing | configuration:57-60 | `cloudflare/resolver.go` | `resolver_test.go`, `resolver_tools_test.go` | TESTED |
| `zone.list`, `analytics.status_codes/top/cache`, `security.bots` | mcp-tools:242-249 | none | none | PLANNED |
| `logs.query`, `logs.jobs`, R2/ClickHouse ingestion | mcp-tools:250-251 | `internal/logs/` is empty | none | PLANNED |
| 6 GA4 tools (traffic, realtime, metadata, capabilities, ecommerce overview/products) | implemented-tools:142-147 | `ga4/tools.go`, `ga4/extra_tools.go` | `report_test.go`, `realtime_test.go`, `ecommerce_test.go`, `capability_test.go`, `tools_test.go`, `extra_tools_test.go` | TESTED |
| Multi-property routing | configuration:57-60 | `ga4/resolver.go` | `resolver_test.go` | TESTED |
| GA4 acquisition/landing/events/funnel | mcp-tools:259-265 | none | none | PLANNED |
| BigQuery boundary | ga4 integration:11 | none (documented boundary) | none | PLANNED |
| `unified.traffic.compare` | implemented-tools:153 | `internal/unified/unified.go`, `tools.go` | `unified_test.go`, `tools_test.go` | TESTED |
| Other unified scenarios (bot/human, WAF impact, funnels, geography, SKU mapping) | mcp-tools:283-291 | none | none | PLANNED |
| Multi-instance **with bindings** | configuration:57-83 | startup error (`main.go:345-348`) | reproduced in §2.1 | UNSUPPORTED-BY-CODE (DOC-06) |

### 3.5 Storage, operations, release

| Capability (documented) | Doc source | Code | Test evidence | Status |
|---|---|---|---|---|
| 4 embedded migrations + advisory lock | upgrade:16-17 | `internal/storage/storage.go:87-150`; `migrations/000001..000004` | `storage_test.go:36` (gated) | INTEGRATION_TESTED |
| PostgreSQL job queue, leases, retries | README:291 | `internal/storage/jobqueue.go`, `internal/jobs/jobs.go` | `jobs_test.go`, `storage_test.go` (gated) | INTEGRATION_TESTED |
| Worker executes imports, batch history, resume | README:292 | `cmd/worker/main.go`, `import_batches.go` | `cmd/worker/main_test.go`, `import_jobs_test.go` | TESTED / INTEGRATION_TESTED |
| Audit retention 365 days + monthly partitions | backup-restore:46-48, threat-model:576 | none (no partitions, no retention job) | none | PLANNED |
| Docker image (distroless, non-root, healthcheck) | deployment:22-28 | `Dockerfile` | `ci.yml` docker job | IMPLEMENTED (CI) |
| Compose development stack | README:152-154 | `docker-compose.yml` | `test/e2e/run.sh` | INTEGRATION_TESTED (CI e2e, fake Magento) |
| Compose production stack | README:165-169 | `docker-compose.prod.yml` (no datasources, no config mount) | none | IMPLEMENTED (serves `platform.info` only) |
| Release pipeline (15 binaries, SBOM, cosign, attestation, multi-arch) | README:82-89 | `.github/workflows/release.yml`, `scripts/build-release.sh` | none locally | IMPLEMENTED (not executed in this environment) |
| Admin approval CLI | README:179-189 | `cmd/admin/main.go` | **none** | IMPLEMENTED (no test; SEC-04) |
| Configuration loader/validation | configuration:1-95 | `internal/core/config/config.go` | `config_test.go` | TESTED |
| Shipped example configuration starts | README:130-136 | `configs/config.example.yaml` | none | UNSUPPORTED-BY-CODE (DOC-06) |

---

## 4. Findings

### DOC-01: Threat model assumption A5 is false and is used to omit replay controls for money-moving tools

- **Severity**: P0
- **State**: CONFIRMED
- **Location**: `docs/security/threat-model.md:32` (A5), `:409` (§5.12);
  `docs/integrations/magento.md:18` (Sales "Read-only");
  `docs/implemented-tools.md:44-128` (the tools themselves).
- **Evidence**: A5 states "Orders and customers are **read-only** in v1; no
  order-mutating tool ships". §5.12 then argues: "Order-mutating tools are absent
  in v1 (A5), which removes double-order operations structurally". The registry
  contains 15 order/customer-mutating tools (18 including customer-address tools):

  | Tool | Permission | Line |
  |---|---|---|
  | `magento.order.create` | write | `internal/magento/write_tools.go:1421` |
  | `magento.order.comment` | write | `:977` |
  | `magento.order.hold` | write | `:982` |
  | `magento.order.unhold` | write | `:986` |
  | `magento.order.email` | write | `:1303` |
  | `magento.order.cancel` | destructive | `:1739` |
  | `magento.invoice.create` | write | `:801` |
  | `magento.invoice.capture` | write | `:1298` |
  | `magento.invoice.email` | write | `:2308` |
  | `magento.shipment.create` | write | `:856` |
  | `magento.shipment.email` | write | `:2309` |
  | `magento.creditmemo.create` | write | `:861` |
  | `magento.creditmemo.email` | write | `:2310` |
  | `magento.customer.create` | write | `:1631` |
  | `magento.customer.update` | write | `:1637` |

  plus `magento.customer.addresses.{create,update,delete}` (`write_tools.go:2012,2017,2118`). The code-side defect
  (no idempotency key, no ledger, no read-back; only `import.start` accepts
  `idempotency_key`) is already recorded as **SEC-05 (P0)** and **REL-02/REL-13**.
  No other document in the set claims the tools are absent; `implemented-tools.md`
  and the README list them, so the doc set contradicts itself.
- **Trigger**: a reviewer uses the threat model as the security authority for a
  deployment with `tools.writes: true`; a client retries `order.create`,
  `invoice.capture`, or `creditmemo.create` after a timeout
  (`internal/magento/client.go:153` says "the call may have completed").
- **Impact**: the justification for omitting duplicate-charge/refund controls is
  false. A release approved on this document can double-charge or double-refund.
- **Proposed fix**: correct A5 and §5.12 in the same change as SEC-05. If the
  tools stay, A5 becomes "orders and customers are mutable; every money-moving
  write is confirmation-gated and must carry an idempotency key"; if they are
  removed from the default profile, say so and list the removal. Do not leave a
  false structural-safety claim in the threat model.
- **Test requirement**: a documentation test (extend the existing registry test)
  that asserts every tool named in the threat model's scope matches the
  registered set; and the SEC-05 idempotency tests.
- **Verified by**: (independent review)

### DOC-02: Security, support, contribution and design documents still describe Phase 0 and "no release"

- **Severity**: P1
- **State**: DOC-ONLY
- **Location**: `SECURITY.md:9-24`; `SUPPORT.md:7-8`; `CONTRIBUTING.md:4,12-19`;
  `docs/architecture.md:3`; `docs/mcp-tools.md:3`; `docs/mcp-conformance.md:3`;
  `docs/database-design.md:3`; `docs/security/security-review.md:3-5`;
  `docs/integrations/magento.md:3`, `cloudflare.md:3`, `ga4.md:3`;
  `docs/development/handoff.md:24-28`.
- **Evidence**: `SECURITY.md:9` — "The project is in Phase 0 (design). No release
  exists yet, so no version is currently supported in production"; the supported
  versions table (`:20-24`) says "No artifacts have been published".
  `SUPPORT.md:7` — "There is no runnable server yet". `CONTRIBUTING.md:12` —
  "Phase 0 is for design, not code"; `:19` — "Feature pull requests are not
  accepted until the Phase 0 design is approved". `architecture.md:3` — "not yet
  implemented". Meanwhile `git tag` ends at `v0.11.0`, the CHANGELOG has
  sections for 19 of the 20 tags, and `docs/releases/v0.11.0.md` says "released 2026-11-20".
- **Trigger**: a vulnerability reporter reads SECURITY.md; a contributor reads
  CONTRIBUTING.md; an operator reads the architecture status.
- **Impact**: reporters are told their report has no supported version;
  contributors are told not to send code; operators cannot tell what is shipped.
  This is the single largest source of reader confusion in the repository.
- **Proposed fix**: replace every Phase 0 status line with one shared status
  block (version, supported surface, link to the compatibility table). Keep
  historical Phase 0 documents under `docs/reviews/` or mark them "historical".
  `SECURITY.md` must state that v0.11.0 is the supported pre-release and that
  fixes target the latest v0.x minor.
- **Test requirement**: a docs lint that fails when a current document contains
  "Phase 0", "no release", or "not yet implemented" without a `historical`
  marker; plus a status test asserting the version in `SECURITY.md`,
  `compatibility.md`, `README.md` and `CHANGELOG.md` is identical.
- **Verified by**: (independent review)

### DOC-03: README says the security properties are "none of which is implemented yet"

- **Severity**: P1
- **State**: DOC-ONLY
- **Location**: `README.md:297-319`.
- **Evidence**: the section opens "The design specifies the following
  properties, none of which is implemented yet:" and then lists read-only
  default, confirmation, audit, least-privilege tokens and no escape hatches.
  The same README says "Production-grade Go gateway" (`:68`), "Writes are
  two-step human-approved" (`:12`), and documents the shipped confirmation flow
  (`:174-189`). The code implements read-only default, confirmation and audit
  (with the gaps recorded in SEC-06/SEC-11). This sentence was written during
  Phase 0 and never removed; it is the same contradiction as DOC-02 but inside
  the primary document.
- **Trigger**: an operator skims the security section of the README before
  deciding whether the gateway is safe to expose.
- **Impact**: the README simultaneously claims the product is production-grade
  and that none of its security controls exist. Either reading is unsafe: one
  over-trusts, the other concludes the project is vapourware.
- **Proposed fix**: rewrite as a status table: Implemented / Partial / Planned,
  with links to the finding IDs that qualify the partial rows (SEC-01, SEC-06,
  SEC-11, SEC-12). Do not use "none of which".
- **Test requirement**: a docs test that forbids the phrase "none of which is
  implemented" in current documents.
- **Verified by**: (independent review)

### DOC-04: The threat model's security "Proof" sections and test plan cite 55 files that do not exist

- **Severity**: P1
- **State**: DOC-ONLY
- **Location**: `docs/security/threat-model.md:257-259, 271-273, 286-288,
  301-303, 316-318, 330-332, 345-347, 360-362, 374-376, 389-391, 403-405,
  417-419, 431-433, 446-448, 461-463, 476-478, 634-668`.
- **Evidence**: every "**Proof.**" paragraph and every row of §9 names a file.
  A path scan (§2.4) found **0 of 55** exist. Examples:
  `tests/security/injection_test.go`, `tests/security/ssrf_test.go`,
  `tests/security/tenant_isolation_test.go`, `internal/egress/ipguard.go`,
  `internal/mcp/strictjson.go`, `internal/quota/cost.go`,
  `internal/ratelimit/window.go`, `internal/redact/untrusted.go`,
  `internal/registry/descriptions.go`, `internal/jobs/idempotency.go`.
  The directory `tests/security/` does not exist; `tests/contract/` is empty.
  `docs/audit/README.md:18-19` requires mocked coverage to be labelled `CONTRACT`
  and only a real instance to earn `INTEGRATION`; the threat model claims
  adversarial and integration suites that were never written.
- **Trigger**: a security reviewer or customer reads §5/§9 to decide whether the
  controls are proven.
- **Impact**: the document's assurance is fictional. This is worse than a missing
  test: it is a claim that the test exists. The threat model is cited by
  `SECURITY.md:197` and `docs/index.md:77` as the security authority.
- **Proposed fix**: either implement the suites or rewrite every Proof/§9 row to
  `PLANNED — no test exists`, with the control's real location (for example JWT
  verification is `internal/security/jwt`, not `internal/auth/jwt.go`). Add a
  meta-test that extracts every backticked path from the threat model and fails
  when a cited file is missing; that prevents recurrence.
- **Test requirement**: `TestThreatModelCitesExistingFiles` (same pattern as
  `TestImplementedToolsDocsMatchRegistry`), plus the real security suites before
  any row is marked `Verified`.
- **Verified by**: (independent review)

### DOC-05: Audit integrity, retention and fail-closed behaviour are documented as implemented but absent

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `docs/security/threat-model.md:566-578`; `docs/adr/0012-observability-and-audit.md:61`;
  `docs/operations/backup-restore.md:46-48`;
  `internal/storage/audit.go:39-46`; `cmd/server/main.go:105-125`.
- **Evidence**: §6.6 lists `params_hash`, `intent_id`, `prev_hash`, `entry_hash`,
  `tool_schema_version`, `params_summary`, `response_hash` and a two-row
  intent/outcome protocol; ADR-0012 says an audit write failure fails writes
  closed. The actual INSERT is
  `(event_id, occurred_at, subject_hash, identity_class, tool, instance_id, scope, decision, phase, state, error_code, request_id)`
  — no `params_hash`, no hash chain, no intent link. `main.go:121` ignores the
  error: `_ = auditor.Record(...)`. There are no partitions in the migrations
  (`grep -n PARTITION internal/storage/migrations/*.sql` → no matches) and no
  audit retention job. The code-side defect is **SEC-06 / REL-16**; this finding
  records the documentation half and the retention claims that no other report
  owns.
- **Trigger**: an operator relies on the audit trail for non-repudiation, or
  plans retention/partition maintenance from `backup-restore.md`.
- **Impact**: tamper evidence and fail-closed audit are claimed but absent; the
  documented 365-day partition procedure cannot be performed because the table
  is not partitioned.
- **Proposed fix**: mark §6.6 and ADR-0012's unimplemented rows `PLANNED`, and
  rewrite `backup-restore.md` to the actual schema (`audit_events` is a plain
  table; retention is not enforced). If the code fix lands, restore the claims
  with the migration that adds the columns.
- **Test requirement**: after the code fix, a chain-verification test and a
  retention test; before then, a docs test asserting no current document claims
  `entry_hash`/`prev_hash` (or a `PLANNED` marker next to it).
- **Verified by**: (independent review)

### DOC-06: The shipped example configuration and the README quickstart exit non-zero

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `configs/config.example.yaml:38-73`; `README.md:126-141`;
  `docs/configuration.md:68-83`; `cmd/server/main.go:345-348`.
- **Evidence**: reproduced exactly as shipped (§2.2):
  `MAGE2MCP_AUTH_ENABLED=false go run ./cmd/server -config configs/config.example.yaml`
  → `mage2mcp: bindings require both cloudflare and ga4 modules to be enabled`,
  exit 1. The example declares a `bindings` entry (`:38`) while all three
  modules are `enabled: false` (`:46,65,72`). `registerModules` treats any
  non-empty `bindings` as requiring both services (`main.go:345-348`), and the
  services are only built for enabled modules. A second, independent failure:
  with two instances per module and a binding, startup also fails with the same
  message (overlay test §2.1) even though `docs/configuration.md:57-60` says all
  three modules accept several instances and `:80-83` documents bindings for
  them. No test loads the shipped example (`grep -rn config.example --include='*_test.go'` → none).
- **Trigger**: the documented quickstart: copy the example, set
  `auth.enabled: false`, run.
- **Impact**: a new operator cannot start the gateway by following the README.
  The multi-instance+bindings combination is documented as supported and cannot
  start at all.
- **Proposed fix**: comment out the `bindings` block in the example (or ship a
  second `config.example.bindings.yaml`), and add a startup test that loads every
  file in `configs/`. For multi-instance+bindings, either fix the wiring
  (`main.go:345` should select a resolver-backed service per binding member) or
  document the limitation in `configuration.md` and reject it with an actionable
  message. `docs/configuration.md` must not claim a configuration that cannot
  start.
- **Test requirement**: `TestShippedConfigsLoad` (load + `registerModules` for
  each file in `configs/`); a multi-instance+bindings startup test.
- **Verified by**: (independent review)

### DOC-07: Release identity is inconsistent across README, Chinese README and docs index

- **Severity**: P1
- **State**: DOC-ONLY
- **Location**: `README.md:5,15,77-80,103`; `README.zh-CN.md:5,13,56-58,81`;
  `docs/index.md:9-13`; `CHANGELOG.md:10`; tag `v0.11.0`.
- **Evidence**: the static badge says "Latest release v0.9.0" but links
  `.../releases/tag/v0.11.0`; `README.md:77` says "The latest release is
  **v0.9.0**" and links the v0.11.0 tag; `:103` labels the v0.11.0 row with
  `docs/releases/v0.9.0.md`; `docs/index.md:9` says v0.9.0 is current while
  `:46` links the v0.11.0 notes; the Chinese README repeats all three. The
  release table also omits v0.10.0 and v0.9.0. `CHANGELOG.md` and the tags are
  consistent at v0.11.0.
- **Trigger**: an operator follows the README to the release notes or downloads
  artifacts.
- **Impact**: the wrong release notes are read for the shipped version; the
  release table contradicts the changelog and the tag.
- **Proposed fix**: make the version a single source (v0.11.0), fix the badge
  label and all three links in both READMEs and the index, add the missing
  v0.10.0/v0.9.0 rows, and link `docs/releases/v0.11.0.md`.
- **Test requirement**: a version-consistency script (badge label == latest tag
  == CHANGELOG top == `docs/index.md` == `compatibility.md`), wired into the docs
  job.
- **Verified by**: (independent review)

### DOC-08: "112 tools" is the maximum surface, not the surface an operator gets

- **Severity**: P2
- **State**: DOC-ONLY
- **Location**: `README.md:15,36-44,117`; `README.zh-CN.md:13,30,94`;
  `docs/implemented-tools.md:3,10-16`; `docs/index.md:35`.
- **Evidence**: the registry count depends on three switches (§2.1):

  | Configuration | Magento | Cloudflare | GA4 | Unified | Platform | Total |
  |---|---:|---:|---:|---:|---:|---:|
  | default (no writes, no DB, no bindings) | 43 | 3 | 6 | 0 | 1 | **53** |
  | + bindings | 43 | 3 | 6 | 1 | 1 | **54** |
  | writes, no DB, no bindings | 95 | 3 | 6 | 0 | 1 | **105** |
  | writes, no DB, + bindings | 95 | 3 | 6 | 1 | 1 | **106** |
  | read-only + database, no bindings | 49 | 3 | 6 | 0 | 1 | **59** |
  | writes + database, no bindings | 101 | 3 | 6 | 0 | 1 | **111** |
  | writes + database + bindings (max) | 101 | 3 | 6 | 1 | 1 | **112** |

  "112 tools (101 Magento)" is true only in the last row, which also requires
  single-instance Cloudflare/GA4 modules (multi-instance + bindings fails,
  DOC-06). `implemented-tools.md:3` calls the list "the tools the gateway
  registers today" and qualifies only writes (`:12-13`), not the database and
  bindings conditions.
- **Trigger**: an operator plans a read-only deployment and expects 112 tools;
  a security reviewer scopes the surface from the headline.
- **Impact**: the tool inventory is misstated by a factor of two for the default
  deployment; the `platform.info` tool would report the real number if wired
  (DOC-20).
- **Proposed fix**: replace the headline with "up to 112 tools; 53 in the default
  read-only configuration" and add the table above to `implemented-tools.md`
  (or generate it from the registry).
- **Test requirement**: extend the docs/registry test to all modules and assert
  the four counts; fail on a hard-coded total that does not match.
- **Verified by**: (independent review)

### DOC-09: `implemented-tools.md` says one instance per module and that `instance` does not select credentials

- **Severity**: P2
- **State**: DOC-ONLY
- **Location**: `docs/implemented-tools.md:15-16`; contradicted by
  `docs/configuration.md:57-60`.
- **Evidence**: "This release supports one instance per module; the value does
  not select credentials yet." The code routes per instance for **reads and
  writes** in all three modules:
  `internal/magento/resolver.go:75-90` (`RegisterWithResolver` → `resolver.lookup()`),
  `internal/magento/write_tools.go:269-276` (`RegisterWritesWithResolver`),
  `internal/cloudflare/tools.go:261-300`, `internal/ga4/resolver.go:28-40`.
  `cmd/server/main.go:258-282` wires the resolver for both. Tests:
  `internal/magento/resolver_test.go`, `internal/cloudflare/resolver_tools_test.go`,
  `internal/ga4/resolver_test.go`. The only true statement in the paragraph is
  that the `instance` field is schema-validated; it is in fact required by every
  schema, so the "empty instance" fast path is not reachable through MCP.
- **Trigger**: an operator reads the implemented-tools page and concludes
  multi-instance is unsupported; the same repository's configuration reference
  says the opposite.
- **Impact**: the two operator-facing references contradict each other; a
  multi-instance deployment is either avoided unnecessarily or configured from
  the wrong document.
- **Proposed fix**: replace with the actual semantics: `instance` is required in
  every schema; the resolver maps it to a per-instance credential; unknown id is
  `not_found`; the empty value is accepted only by direct Go callers with one
  instance. Add a cross-link to `configuration.md` and ADR-0018.
- **Test requirement**: a docs test that greps for the stale sentence and fails;
  the resolver tests already prove the behaviour.
- **Verified by**: (independent review)

### DOC-10: `mcp-tools.md` is a Phase 0 catalogue with stale scopes, fields and status

- **Severity**: P2
- **State**: DOC-ONLY (design text), with four implementation divergences
- **Location**: `docs/mcp-tools.md:3,115-117,210-217,227,271-273,408,412,444-453`.
- **Evidence**:
  - `:213` marks `magento.import.start` **destructive**; code registers it
    `write` (`internal/magento/import_jobs.go` metadata; verified in the
    permission dump). Same for `import.resume` (`:215`) and
    `inventory.unassign` (`:202`); `configurable.remove_child` (`:188`) is
    `write` in code.
  - `:214` says import status takes a `batch_id`; the schema requires `job_id`
    (`internal/magento/import_jobs.go:212-214`).
  - `:210` says order search supports a "customer email hash"; the schema has
    `status`, `state`, `increment_id`, `created_from/to`, `store_id`,
    `customer_id` only (`internal/magento/tools.go:278-289`).
  - `:211` says order summary returns "revenue, orders, AOV, top SKUs"; the tool
    returns daily count/revenue/quantity (`implemented-tools.md:97` is correct).
  - `:115-117` describes an 8 KiB inline cap and `resource_link`; neither exists.
  - `:321-372` shows `idempotency_key` as **required** for
    `magento.configurable.create`; the real schema rejects that property
    (`internal/magento/write_tools.go:105`, `additionalProperties: false`), and
    no write schema except `import.start` has the field.
  - `:227-228` lists `platform.job.get/cancel` in §7.5a, while `:445` lists them
    under "Not planned". `scripts/check-contracts.mjs:102-107` uses the former
    to pass a guard that therefore proves nothing.
  - `:412` is a table row outside any table (`| Storefront verification | 60 s | ...`).
  - `:448-453` says the remaining names are "planned but not built", but several
    rows in the catalog are marked "Implemented" and several implemented tools
    appear only in `implemented-tools.md`; the two catalogues are not
    cross-checked.
- **Trigger**: an agent/client is built from `mcp-tools.md` (the document the
  README calls the "design catalog with implementation status").
- **Impact**: clients send rejected fields and wrong identifiers; scope-based
  policy decisions use the wrong class; the "Implemented" markers cannot be
  trusted.
- **Proposed fix**: regenerate the implemented rows from registry metadata (name,
  scope, timeout, confirmation) and keep only genuinely planned rows in a
  separate section. Fix the stray table row. Add a docs test that every row
  marked "Implemented" exists in the registry with the same scope.
- **Test requirement**: `TestMcpToolsImplementedRowsMatchRegistry` (all modules,
  scope + confirmation); link/table lint for the stray row.
- **Verified by**: (independent review)

### DOC-11: `mcp-conformance.md` claims Redis, event stores, progress, resource links and `/livez` that do not exist

- **Severity**: P2
- **State**: DOC-ONLY
- **Location**: `docs/mcp-conformance.md:3,21,25-26,30-32,40-42,64,66,130-133`.
- **Evidence**: S3 says "Redis-backed session store with a 30-minute idle TTL";
  S4 says "Optional `EventStore`"; T9 says `platform.job.cancel` exists; T10/T11
  say `resource_link` and an 8 KiB cap are adopted; P2 says
  `tools/list_changed` is emitted; P4 names `/livez`. None has code:
  `grep -rn 'EventStore|progressToken|list_changed' internal cmd` → no matches;
  `internal/mcp/server/server.go` has no session binding and no `/livez`. S3 also
  contradicts ADR-0019 (Redis removed) and `compatibility.md:14`. §6 says "The
  gateway is implemented and released ... Rows marked `Implemented` have passing
  tests"; T5's row says "Conformance test parses both and compares", but
  `toolerr_test.go` asserts `StructuredContent` only and never compares the
  `TextContent` fallback. Row id `T6` is duplicated (`:26` and `:27`).
- **Trigger**: an MCP client implementer or reviewer reads the matrix as a
  conformance statement.
- **Impact**: the document claims protocol behaviours and tests that do not
  exist; the one "Implemented" row with a claimed test has no such test.
- **Proposed fix**: set every unimplemented row to `PLANNED` with the ADR that
  schedules it (ADR-0017), delete the Redis wording, dedupe T6, and either add
  the text-fallback assertion or change T5's status. §6 should state which rows
  are proven and by which test file.
- **Test requirement**: the text-fallback equality test; a docs test that every
  `Implemented` row names an existing test.
- **Verified by**: (independent review)

### DOC-12: README and architecture claim Magento GraphQL, R2 and ClickHouse components that do not exist

- **Severity**: P2
- **State**: DOC-ONLY
- **Location**: `README.md:213,244,331,356,362,404`; `docs/architecture.md:16,41,45-56,72-73,114`;
  `docs/index.md:69`; `docs/integrations/magento.md:20,67,76,345,471`.
- **Evidence**: the only GraphQL client in the tree is Cloudflare's
  (`internal/cloudflare/client.go:203`); `grep -rin graphql internal/magento` →
  no matches. ADR-0022:20-28 explicitly decides "No storefront GraphQL calls.
  Variant verification through the storefront is not implemented." The README
  compatibility row says "GraphQL is used for read-only storefront verification"
  and the roadmap says "GraphQL verification"; the integration document builds
  verification flows on `products`/`stock_status` queries. For raw logs,
  `internal/logs/` exists but is empty; no R2, ClickHouse, loader, or query tool
  exists, while README:281/362 and architecture:45-56/72-73 present them as
  components and `docs/mcp-tools.md:250-251` defines `cloudflare.logs.*`.
- **Trigger**: an operator provisions R2/ClickHouse or expects storefront
  verification; a reviewer treats the architecture diagram as as-built.
- **Impact**: documented components with no implementation; the integration
  document's "success definition" cannot be met by the gateway.
- **Proposed fix**: remove GraphQL from the Magento surface (or mark it
  "superseded by ADR-0022") and remove R2/ClickHouse from the component tables,
  or move them to a clearly labelled "target architecture" document. Update the
  integration doc's verification section to the admin-REST checks the code
  actually offers.
- **Test requirement**: docs test that no current document claims a Magento
  GraphQL client or a `cloudflare.logs.*` tool without a `PLANNED` marker.
- **Verified by**: (independent review)

### DOC-13: README says CI runs on every PR and that a docs test catches catalog drift

- **Severity**: P2
- **State**: DOC-ONLY
- **Location**: `README.md:24,69`; `.github/workflows/ci.yml:6-16`;
  `docs/release-checklist.md:5-6`; `internal/magento/tool_contract_test.go:63`.
- **Evidence**: `ci.yml` is `on: workflow_dispatch` only ("Local-first workflow:
  day-to-day commits are verified with scripts/local-ci.sh. GitHub runs this
  workflow only when a maintainer dispatches it before a release"). The release
  checklist correctly says "GitHub CI is manual and release-time only". README:69
  says the suite runs "on every PR". README:24 says "a docs test fails if the
  catalog drifts"; the test exists but covers only Magento
  (`TestImplementedToolsDocsMatchRegistry` reads only table rows whose first
  column starts with `magento.`), while `scripts/check-contracts.mjs` parses only
  `docs/mcp-tools.md` and never the registry. Cloudflare, GA4, unified and
  `platform.info` drift is unchecked.
- **Trigger**: a contributor expects PR CI; a maintainer trusts the "docs test"
  to catch a Cloudflare/GA4 rename.
- **Impact**: false CI expectation and a real drift hole outside Magento.
- **Proposed fix**: say "CI is a manual pre-release workflow; local CI is
  `scripts/local-ci.sh`", and extend the docs test to every module (or say
  explicitly that only Magento is pinned).
- **Test requirement**: extend `TestImplementedToolsDocsMatchRegistry` to the
  full registry; add a README statement test if the claim stays.
- **Verified by**: (independent review)

### DOC-14: The configuration reference omits two environment overrides and overstates secret handling

- **Severity**: P2
- **State**: DOC-ONLY
- **Location**: `docs/configuration.md:5,25-26,85-91`;
  `internal/core/config/config.go:134-162,224-226`; `cmd/server/main.go:53-60,177`.
- **Evidence**: `applyEnv` reads nine variables; the reference documents seven.
  Missing: `MAGE2MCP_AUTH_ENABLED` (`config.go:150-152`) and
  `MAGE2MCP_DEV_INSECURE` (`main.go:59`, the flag that permits auth-off on a
  non-loopback listener; `configs/config.docker.yaml:5` and the README Docker
  command use it). `server.public_url` is documented as "Public base URL behind
  the Cloudflare Tunnel" but is read only in `config.go:139` and never used by
  the server. The page says "Secrets are always references, never literals"
  (`:5`; ADR-0011:32 says literal secrets "are rejected"), but validation only
  checks that `credential` is non-empty (`config.go:224-226`); `bearer:literal`
  is accepted. The page does not mention that `/metrics` is served on the same
  unauthenticated listener (`server.go:258`, SEC-12) while `monitoring.md:11`
  tells operators to keep it private. `.env.example` compounds this: it says
  "Copy to .env (git-ignored) and fill in real values", but the server has no
  dotenv loader (`grep -rn godotenv internal cmd` → none), Compose declares no
  `env_file`, and none of the vendor credentials it lists
  (`MAGENTO_US_CREDENTIAL`, `CLOUDFLARE_*`, `GA4_SERVICE_ACCOUNT`) is passed
  into the container by either Compose file. It also says writes are "pending"
  confirmation, which shipped. The template only affects `go run` if the
  operator exports the variables manually.
- **Trigger**: an operator uses `MAGE2MCP_DEV_INSECURE` in production or commits
  a literal credential believing validation rejects it.
- **Impact**: undocumented switches; a false security guarantee; an unauthenticated
  metrics endpoint on the public listener.
- **Proposed fix**: document all nine variables (plus the CLI flags), either
  implement secret-reference validation or soften the claim to "credential
  values are references by convention; the loader does not reject literals",
  remove `public_url` or use it, and document the metrics exposure.
- **Test requirement**: a config-key coverage test that reflects over
  `config.Config` and the `applyEnv` literals and compares them with the tables
  in `configuration.md`.
- **Verified by**: (independent review)

### DOC-15: Deployment, backup and monitoring docs describe a stack that no longer exists

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `docs/operations/deployment.md:5,13,15,18-19,26-27,36`;
  `docs/operations/backup-restore.md:46-48`; `docs/operations/monitoring.md:53`;
  `docker-compose.prod.yml`; `configs/config.docker.yaml:30-36`.
- **Evidence**:
  - `deployment.md:36` exports `REDIS_PASSWORD`; Redis was removed by ADR-0019
    and `compatibility.md:14` says so.
  - `:13` says the worker does "Imports, reports, log ingestion"; there is no log
    ingestion (empty `internal/logs`).
  - `:18-19` says "Without PostgreSQL ... writes stay disabled". Writes are
    registered whenever `tools.writes: true`, with or without a database (overlay
    test: 105/106 tools); the confirmation store falls back to memory, and the
    admin CLI refuses to run without `database.url` (`cmd/admin/main.go:42-44`),
    so writes are *registered but unapprovable*, not disabled.
  - `:26-27` says the baked config means "no file mount is required"; the baked
    config enables no data sources (`configs/config.docker.yaml:30-36`), so a
    container without a config mount serves only `platform.info`. The
    "production-shaped" Compose stack (`docker-compose.prod.yml`) inherits that
    config and has no volume or build path for a real one.
  - `backup-restore.md:46-48` claims 365-day audit retention and partition drops;
    no partitions and no retention job exist.
  - `monitoring.md:53` tells operators to watch quota headroom; no quota metric
    exists (`internal/observability/metrics.go` has no `quota_remaining`).
- **Trigger**: an operator follows the deployment guide for a real deployment.
- **Impact**: wrong environment variable, a gateway with no data sources, a
  mistaken belief that writes are off without a database, and retention guidance
  that cannot be executed.
- **Proposed fix**: remove Redis, drop log ingestion, explain the actual
  write/DB relationship (and make the server refuse `tools.writes` without a
  database, or warn loudly), document how to mount a real config into the
  container, and rewrite retention/monitoring to what exists.
- **Test requirement**: a Compose smoke test that starts the production override
  and asserts the expected tool count; a startup test for writes without a
  database.
- **Verified by**: (independent review)

### DOC-16: `database-design.md` is presented as normative but contradicts the shipped migrations

- **Severity**: P2
- **State**: DOC-ONLY
- **Location**: `docs/database-design.md:3,19,161,165-600,893`;
  `internal/storage/migrations/000001..000005`; `internal/storage/storage.go:87-150`.
- **Evidence**: the document says "Normative for Phase 1 migrations" and "Phase 1
  writes migrations directly from §4.2". Comparing its `CREATE TABLE` set with
  the migrations: nine documented tables do not exist (`tenants`, `users`,
  `instance_bindings`, `instance_credentials`, `sku_mappings`, `job_attempts`,
  `ingest_manifest`, `role_capabilities`, `import_items`), and one shipped table
  is undocumented (`import_batch_failures`, migration 000004). Migration
  numbering and content differ (doc `000001 — tenants, identities, ...` vs
  shipped `000001_init`; doc `000002 — jobs and attempts` vs shipped
  `000002_confirmation`). The doc says `schema_migrations` is created by
  `golang-migrate` with a dirty flag (`:161,893`); the code creates
  `schema_migrations(version text primary key, applied_at timestamptz)` with a
  custom embedded runner (`storage.go:103-107`) and `go.mod` has no
  golang-migrate dependency. No table is partitioned, contrary to §6.5.
- **Trigger**: an engineer or operator uses the document as the schema reference
  or plans a migration/retention procedure from it.
- **Impact**: a different schema is designed from the doc than the one shipped;
  backup/retention and role guidance are invalid.
- **Proposed fix**: mark the document `PLANNED — target schema` and add an
  as-built schema reference generated from the migrations (or rewrite §4.2 to the
  four shipped migrations and document the missing tables as future work).
- **Test requirement**: a schema test that compares the tables in the doc with
  `information_schema` after `Migrate`; or a generated schema dump checked in CI.
- **Verified by**: (independent review)

### DOC-17: The architecture document is stale and its configuration example does not load

- **Severity**: P2
- **State**: DOC-ONLY (mostly), one reproducible config failure
- **Location**: `docs/architecture.md:3,16,114-115,176-207,194,196,213-249,300,317,320`.
- **Evidence**: status "Phase 0 ... not yet implemented" (`:3`). The §7 example
  uses `base_url` (`:231`), `auth.mode`/`auth.token` (`:233-235`),
  `credential_profile` (`:242`) and `credentials` (`:248`); the loader uses
  `KnownFields(true)` (`config.go:123`) and accepts only `endpoint`, `credential`,
  `store_code`, `account_id`, `zone_id`, `property_id`, so the example fails at
  startup with unknown fields. §6 claims capability-gated registration and
  `/readyz` degradation (`:194`), `tools/list_changed` (`:196`), an SDK
  `EventStore` (`:300`), a `mage2mcp_quota_remaining` metric (`:317`), `/livez`
  (`:320`), and `internal/logs`/`internal/admin` packages (`:114-115`); the
  packages are empty directories and none of the behaviours exists. The
  `core/datasource` contract (`:176-187`) is declared but no module implements
  `HealthCheck`/`Capabilities` (`grep -rn 'func .* HealthCheck' internal` → no
  matches), so registration is module-enablement based, not capability based.
- **Trigger**: an engineer copies the architecture config or relies on the
  component/registration claims.
- **Impact**: a non-loadable example, and a design document that describes a
  different system than the one shipped.
- **Proposed fix**: split "target architecture" from "as-built"; fix the config
  example to the real keys; mark the unimplemented behaviours planned and link
  the ADRs that schedule them.
- **Test requirement**: a test that loads every YAML example in `docs/`; a docs
  test for the unimplemented capability-registration claim.
- **Verified by**: (independent review)

### DOC-18: The evidence chain is broken for the headline validation claim, and a customer account ID is still published

- **Severity**: P2
- **State**: DOC-ONLY
- **Location**: `docs/evidence/magento.md:5-7`; `README.md:15-17`;
  `docs/operations/production-acceptance.md:1-17`; `docs/evidence/ga4.md:3`.
- **Evidence**: the Magento evidence log says "**Result:** not executed — no
  credentials or reachable instance were provided, so every behavioural row is
  `[U]`", dated 2026-11-18. The README claims validation "against a live Magento
  production store: 23 read-only checks plus a 30-day business drill-down (924
  orders)" on 2026-11-20, and `production-acceptance.md` documents that run. The
  evidence log was never updated, so the project's evidence chain (evidence file
  → integration doc → README) has a hole under its most prominent claim. The GA4
  evidence file still publishes `accounts/102070720` (`:3`), which the baseline
  rules forbid and the prior documentation review flagged as D-20.
- **Trigger**: a reviewer checks the evidence trail for the production-validation
  claim; a privacy reviewer reads the GA4 evidence.
- **Impact**: the central "every claim traces to evidence" property fails for the
  release headline; a customer account identifier is published.
- **Proposed fix**: append the 2026-11-20 acceptance run to
  `docs/evidence/magento.md` (target, scope, 23 checks, link to the report,
  write-disabled statement), or change the README claim to point at the evidence
  file; remove the account ID from the GA4 evidence and integration doc.
- **Test requirement**: a link test that every `[V-live]` claim resolves to an
  evidence section; a secret/PII scanner over `docs/evidence/`.
- **Verified by**: (independent review)

### DOC-19: "Production-grade" and "validated" claims exceed the tested scope

- **Severity**: P2
- **State**: DOC-ONLY
- **Location**: `README.md:15-18,64-73,349-352`; `README.zh-CN.md:13-18,38-51`;
  `docs/compatibility.md:3-14`; `docs/operations/production-acceptance.md:7-11`;
  `docs/development/magento-write-verification.md:5-12`.
- **Evidence**: the status block attaches "Validated against a live Magento
  production store" to "112 tools (101 Magento)". The acceptance run registered
  37 read-only Magento tools plus `platform.info` and states "no write,
  destructive, or confirmation-gated tool was available or called"
  (`production-acceptance.md:7-11`). Write verification was a separate run on a
  disposable demo that resets every six hours, using the v0.8.1 build
  (`magento-write-verification.md:5-12`), and several rows failed (media update,
  customer address, order place). 55 write/destructive tools exist; none was
  validated against the production store. The README also says the compatibility
  matrix is "target combinations for v0.x, not tested configurations" (`:351`),
  contradicting the validation sentence. "Production-grade" (`:68`) is not a
  testable claim.
- **Trigger**: an operator or reviewer uses the status line to judge production
  readiness.
- **Impact**: overstates validation. This is exactly the input to the release
  verdict, so it must be precise.
- **Proposed fix**: split the claim: "Read-only acceptance against a live
  Magento store (37 tools, 23 checks); write verification against a disposable
  demo (v0.8.1); no live validation of write/destructive tools." Remove
  "production-grade" or define it.
- **Test requirement**: none (prose), but the release-readiness report should
  quote the corrected sentence.
- **Verified by**: (independent review)

### DOC-20: `platform.info` is documented as reporting instances and the write flag, but the wiring never passes them

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `docs/implemented-tools.md:22`; `internal/mcp/server/server.go:197-219`;
  `cmd/server/main.go:105-125,193-212`.
- **Evidence**: the tool returns `Instances: opts.Instances` and
  `WritesEnabled: opts.WriteTools` (`server.go:215-216`), but the production
  `server.NewMCP` call passes neither (`main.go:107-124`), so `instances` is
  omitted and `writes_enabled` is always `false`. `configuredInstances`
  (`main.go:194-212`) exists solely for `main_test.go:137`; it is dead in
  production. The unit test constructs `Options` by hand and therefore passes.
- **Trigger**: any call to `platform.info` on a real deployment.
- **Impact**: agents and operators use `platform.info` for capability discovery
  and receive false information about the configured instances and write mode.
- **Proposed fix**: pass `Instances: configuredInstances(cfg)` and
  `WriteTools: cfg.Tools.Writes` in `run()`, and add an end-to-end test through
  `run()`/`registerModules` rather than `NewMCP` directly.
- **Test requirement**: an integration test that starts the wired server with
  two instances and writes enabled and asserts `platform.info` reports both.
- **Verified by**: (independent review)

### DOC-21: CHANGELOG structure and the README release table disagree with the changelog

- **Severity**: P3
- **State**: DOC-ONLY
- **Location**: `CHANGELOG.md:10-62`; `README.md:77-105`.
- **Evidence**: the `[0.11.0]` entry has two separate `### Fixed` sections
  (`:31` and `:52`) and describes write-safety guidance plus the admin
  credential form. The README calls v0.11.0 "the write-verification release: it
  fixes W1–W6, W9, W11, and W12" — that is the v0.9.0 entry (`CHANGELOG.md:63-80`).
  The README table lists only v0.11.0, v0.8.1, v0.8.0 and omits v0.10.0/v0.9.0,
  while `docs/index.md:46-48` lists them. There is no `[0.10.0]` section in
  `CHANGELOG.md` at all (19 version sections for 20 tags), even though
  `docs/releases/v0.10.0.md` exists.
- **Trigger**: a user reads the changelog or the README release table to decide
  what changed.
- **Impact**: the wrong change description for the current release; missing
  releases in the table.
- **Proposed fix**: merge the duplicate `### Fixed` sections, describe v0.11.0
  accurately (write-safety + admin credential + product-copy fixes), and rebuild
  the README table from the tags.
- **Test requirement**: Keep-a-Changelog lint (one section per type per version);
  a test that every tag has a changelog section and vice versa.
- **Verified by**: (independent review)

### DOC-22: ADR statuses and claims are stale relative to the code

- **Severity**: P3
- **State**: DOC-ONLY
- **Location**: `docs/adr/0018-multi-instance-routing.md:3`;
  `docs/adr/0011-configuration-and-multi-instance.md:32,35-37`;
  `docs/adr/0017-mcp-conformance.md:38-41`; `docs/adr/0012-observability-and-audit.md:61`;
  `docs/adr/0022-magento-rest-admin-scope.md:20-28`.
- **Evidence**: ADR-0018 is "Accepted (implementation in progress)" but reads and
  writes are routed (DOC-09) and the only missing piece is bindings with
  multi-instance (DOC-06). ADR-0011 says literal secrets in YAML "are rejected"
  and that an invalid instance is disabled individually with `/readyz` degraded;
  neither is implemented (DOC-14/DOC-17). ADR-0017 accepts progress/cancel,
  `platform.job.cancel`, `resource_link` and session binding, none implemented
  (DOC-11), and says prompts are "Post-1.0" although the `write_safety` prompt
  ships. ADR-0012's fail-closed audit is not implemented (DOC-05). ADR-0022 is
  correct but ignored by the README and integration doc (DOC-12).
- **Trigger**: an engineer treats an accepted ADR as as-built.
- **Impact**: decisions cannot be distinguished from aspirations.
- **Proposed fix**: add an `Implementation:` line to every ADR (`as-built`,
  `partial: <what>`, `planned`) and correct the stale statements; link the
  finding IDs.
- **Test requirement**: an ADR lint that requires the `Implementation:` field.
- **Verified by**: (independent review)

### DOC-23: Development docs describe a workflow and features that have moved on

- **Severity**: P3
- **State**: DOC-ONLY
- **Location**: `docs/development/handoff.md:18-20,24-28`;
  `docs/development/ci-cd.md:78-80`; `docs/development/magento-write-verification.md:70`;
  `docs/development/testing-strategy.md:311`.
- **Evidence**: handoff's "Next steps" #1 is "Add a `magento:import` job ... expose
  `magento.import.status/cancel/report`", all implemented in
  `import_jobs.go`/`cmd/worker`. Its CI list (docs, dco, go 1.26/1.27, gosec,
  govulncheck, gitleaks, dependency-review, integration, coverage, release dry
  run) does not match `ci.yml` (verify, integration, docker, e2e, gate) or the
  removed `release-dry-run.yml`. `ci-cd.md:78-80` still describes `detect`,
  `docs`, `dco`, `go`, `external-links` jobs. `magento-write-verification.md:70`
  says automatic admin-token acquisition "is not implemented" although v0.11.0
  ships `AdminTokenSigner` (`internal/magento/admin_token.go`). `testing-strategy.md:311`
  still lists Redis/Testcontainers.
- **Trigger**: the next contributor or agent follows the handoff/CI docs.
- **Impact**: work is repeated on done items; CI expectations are wrong.
- **Proposed fix**: mark these documents historical or refresh them; keep one
  current "state of the project" page.
- **Test requirement**: docs lint that current docs do not mention removed
  workflows (Redis, release-dry-run) without a historical marker.
- **Verified by**: (independent review)

### DOC-24: Compatibility and security docs disagree about supported versions and credential forms

- **Severity**: P3
- **State**: DOC-ONLY
- **Location**: `docs/compatibility.md:9,12,41-64`; `SECURITY.md:15-24`;
  `docs/configuration.md:64`; `CHANGELOG.md:16-23`.
- **Evidence**: compatibility lists Magento credentials as "bearer or OAuth 1.0a"
  and omits the `admin:<username>:<password>` form that v0.11.0 added and
  `configs/config.example.yaml:58-63` documents. It says "older clients are
  rejected" for MCP; the SDK negotiates and no test asserts rejection. Its
  version table lists v0.11.0 as "Supported pre-release", while SECURITY.md says
  no version is supported. The Redis row is correct.
- **Trigger**: an operator chooses a credential form or checks support status.
- **Impact**: a supported credential form looks unsupported; support status is
  contradictory.
- **Proposed fix**: add the admin form, soften or test the MCP rejection claim,
  and align the support table with SECURITY.md.
- **Test requirement**: a credential-form docs test (every form accepted by
  `ParseCredential` is documented); a version-table consistency test.
- **Verified by**: (independent review)

### DOC-25: Release and approval documentation overstates the controls around publishing and approval

- **Severity**: P3
- **State**: CONFIRMED (admin CLI trust model), DOC-ONLY (release wording)
- **Location**: `docs/release-checklist.md:28-38`; `README.md:174-189`;
  `cmd/admin/main.go:36-90`; `internal/security/confirm/confirm.go:115-123`.
- **Evidence**: the checklist says the release workflow verifies "that the tagged
  commit's checks all passed" (`:30-31`); `release.yml` runs its own
  `gofmt`/`vet`/`go test`/`govulncheck` and verifies the tag is an ancestor of
  `main`, but does not consult prior check runs. It also says "The `release`
  environment requires maintainer approval" — a GitHub setting that cannot be
  verified from the repository. The admin CLI takes the approver identity from
  `-as` (`cmd/admin/main.go:38,79`) and the store only compares it with the
  requester hash; there is no authentication of the human (SEC-04). The README
  correctly says a different identity must approve, but does not state that the
  CLI trusts the operator's assertion, so the guarantee depends on OS/DB access
  control.
- **Trigger**: an operator relies on the approval identity as proof of a second
  human; a release manager relies on the "checks passed" statement.
- **Impact**: the four-eyes property is weaker than the README implies; the
  release checklist describes a gate that is not implemented as written.
- **Proposed fix**: document the CLI trust model explicitly ("the approver string
  is self-asserted; protect the database and the host"), and correct the release
  checklist to what `release.yml` actually verifies.
- **Test requirement**: an admin CLI test (none exists today) covering
  pending/approve/self-approval; a release-workflow doc test if the wording stays.
- **Verified by**: (independent review)

### DOC-26: `docs/index.md` is a stale entry point

- **Severity**: P3
- **State**: DOC-ONLY
- **Location**: `docs/index.md:3-13,91,118-124`.
- **Evidence**: it calls v0.9.0 the current release while linking v0.11.0 notes;
  it says "Sanitized records of Phase 0 live verification" for evidence that
  predates the production acceptance run; it has no row for `docs/audit/` (the
  programme this report belongs to) and no row for `docs/operations/` release
  evidence. It does correctly state "past the Phase 0 design baseline" (`:3-5`),
  which contradicts the module docs (DOC-02).
- **Trigger**: a reader starts at the documentation entry point.
- **Impact**: the first page gives the wrong version and hides the audit and
  operations evidence.
- **Proposed fix**: regenerate the index after remediation (version, audit
  reports, operations evidence, historical section).
- **Test requirement**: link check plus the version-consistency script from
  DOC-07.
- **Verified by**: (independent review)

---

## 5. Cross-references to sibling reports (do not double-count)

| Documentation claim | Code-side finding already recorded |
|---|---|
| A5 read-only sales; no order-mutating tools (DOC-01) | [SEC-05](security-audit-report.md) (P0), [MAG-25](magento-integration-review.md) |
| Audit hash chain, fail-closed writes, `params_hash` (DOC-05) | [SEC-06](security-audit-report.md), [REL-16](reliability-review.md) |
| Idempotency ledger and per-write idempotency keys (DOC-01/05) | [REL-02](reliability-review.md), [REL-13](reliability-review.md), [ARCH-04](captain-findings.md) |
| Per-tool scope enforcement (DOC-11/A3) | [SEC-01](security-audit-report.md), [ARCH-01](captain-findings.md) |
| Rate limiting (DOC-05) | [SEC-11](security-audit-report.md) |
| `/metrics` private (DOC-14) | [SEC-12](security-audit-report.md) |
| IdP-group role mapping (DOC-11) | [SEC-09](security-audit-report.md) |
| `Cf-Access-Jwt-Assertion` (DOC-11) | [SEC-08](security-audit-report.md) |
| Approval surface identity (DOC-25) | [SEC-04](security-audit-report.md) |
| No automated Magento integration test (DOC-19) | [MAG-23](magento-integration-review.md) |
| Threat model describes a ledger that does not exist (DOC-04/05) | [REL-21](reliability-review.md) |

This report does **not** claim the code defects as new; it records that the
documentation still asserts the opposite and must be rewritten whichever way the
code findings are resolved.

---

## 6. Documents that must be rewritten before release (priority order)

**Tier 1 — release-blocking, rewrite in full**

1. `docs/security/threat-model.md` — A5, §5 Proofs, §6.6 audit, Redis/session,
   rate limiting, egress, media, idempotency; every `Proof` row either backed by
   a test or marked PLANNED. Do not ship a threat model that cites nonexistent
   files.
2. `SECURITY.md` — supported versions, reporting channels, release state,
   signing status.
3. `README.md` + `README.zh-CN.md` — status block, tool count, release identity,
   security posture, CI statement, GraphQL/R2 claims, validation scope.
4. `CONTRIBUTING.md` + `SUPPORT.md` — current project state, what is accepted,
   CI reality, Redis removal.
5. `docs/implemented-tools.md` — conditional tool-count matrix, instance
   routing, remove "Not implemented yet".
6. `docs/mcp-tools.md` — regenerate implemented rows, scopes, fields; split
   planned rows; fix `platform.job.*` contradiction and the stray table row.
7. `docs/mcp-conformance.md` — status per row, remove Redis, add test evidence,
   dedupe T6.
8. `docs/architecture.md` — as-built vs target; fix the config example; remove
   `/livez`, EventStore, quota metric, empty packages.
9. `docs/database-design.md` — mark target, add as-built schema or rewrite §4.2.
10. `docs/configuration.md` — all env vars, real secret handling, metrics
    exposure, multi-instance+bindings limitation.
11. `docs/operations/deployment.md`, `backup-restore.md`, `monitoring.md` —
    Redis, log ingestion, writes/DB, config mount, retention, quotas.
12. `docs/index.md` — version, audit/reports rows, historical section.

**Tier 2 — rewrite in the same pass (cheap, high signal)**

1. `configs/config.example.yaml` — remove/relocate `bindings`; add a startup
   test. `docs/configuration.md` cross-link.
2. `docs/compatibility.md` — admin credential, support table alignment, MCP
   negotiation claim.
3. `docs/evidence/magento.md`, `docs/evidence/ga4.md` — acceptance run, remove
   account ID.
4. `CHANGELOG.md` — merge duplicate sections; align with README table.
5. `docs/release-checklist.md` — what `release.yml` actually verifies.
6. ADRs 0011, 0012, 0017, 0018 — `Implementation:` line and corrections.
7. `docs/development/{handoff,ci-cd,testing-strategy,magento-write-verification}.md`
   — mark historical or refresh.

**Tier 3 — update after remediation lands**

1. `docs/operations/production-acceptance.md` — add the post-fix re-run.
2. `docs/development/mcp-gaps.md` — close G3/G10/G11/G13 or mark accepted.
3. `docs/releases/v0.11.0.md` — correct the verification checklist once CI is
   reproducible.
4. `docs/module-boundaries.md` — capability contract as-built (not in this
   audit's scope but cited by architecture).

---

## 7. Documentation that will be stale after the remediation phase

The remediation work already in flight (source files and new migrations)
will invalidate these passages unless they are updated in the same change:

| Remediation area | Documents that will change |
|---|---|
| RBAC scope enforcement (SEC-01/ARCH-01) | `threat-model.md` §6.2, `mcp-conformance.md` A3, `README.md` security posture, `security-review.md` Z1 |
| Audit integrity (SEC-06/REL-16) | `threat-model.md` §6.6, `adr/0012`, `monitoring.md` audit queries, `backup-restore.md` retention |
| Idempotency ledger (REL-02/13/ARCH-04) | `threat-model.md` §5.12, `mcp-tools.md` schemas, `implemented-tools.md` notes, `database-design.md` `idempotency_keys` |
| Storage fencing/lease changes (REL-01..REL-20) | `architecture.md` §8, `database-design.md` jobs/attempts, `upgrade.md` migration list, `release-checklist.md` |
| Media upload changes (MAG-14/15) | `threat-model.md` §5.9, `implemented-tools.md` media rows, `integrations/magento.md` media section |
| Configurable/create fixes (MAG-05..09) | `implemented-tools.md` configurable notes, `write-safety.md` inventory/configurable section, `integrations/magento.md` lifecycle |
| Order/money write controls (SEC-05/MAG-01..04) | `threat-model.md` A5/§5.12, `implemented-tools.md` order rows, `mcp-tools.md` import/order scopes, `README.md` status |
| Multi-instance/bindings fix (DOC-06) | `configuration.md`, `implemented-tools.md`, `adr/0018`, `README.md` multi-instance bullet |
| `platform.info` wiring (DOC-20) | `implemented-tools.md:22`, `architecture.md` observability, `README.md` |
| New migration 000005 | `database-design.md`, `upgrade.md`, `backup-restore.md` |
| CI changes (if CI becomes PR-triggered) | `README.md:69`, `ci-cd.md`, `release-checklist.md`, `CONTRIBUTING.md:242` |
| Version bump after fixes | every file in Tier 1 that names v0.11.0, `CHANGELOG.md`, `compatibility.md`, `SECURITY.md` |

---

## 8. Appendix A — configuration key and environment matrix

### A.1 YAML keys (all exist in `config.go`; all documented)

| Key | Type | Default | Validation | Documented |
|---|---|---|---|---|
| `server.listen` | string | `127.0.0.1:8080` | required | yes |
| `server.public_url` | string | — | none (unused) | yes |
| `server.shutdown_grace` | duration | `10s` | ≥ 0 | yes |
| `server.max_body_bytes` | int64 | `1048576` | > 0 | yes |
| `auth.enabled` | bool | `true` | non-loopback requires dev flag | yes |
| `auth.issuer` | string | — | https when enabled | yes |
| `auth.audience` | string | — | required when enabled | yes |
| `auth.jwks_url` | string | — | https when enabled | yes |
| `log.level` | string | `info` | debug/info/warn/error | yes |
| `log.format` | string | `json` | json/text | yes |
| `tools.writes` | bool | `false` | none | yes |
| `database.url` | string | — | postgres:// or postgresql:// | yes |
| `database.max_conns` | int32 | `10` | > 0 when URL set | yes |
| `datasources.*.enabled` | bool | `false` | needs ≥ 1 instance | yes |
| `datasources.*.instances[].id` | string | — | required, globally unique | yes |
| `...endpoint/store_code/account_id/zone_id/property_id` | string | — | module-specific, not validated | yes |
| `...credential` | string | — | non-empty only | yes |
| `bindings[].{id,magento,cloudflare,ga4}` | string | — | unique, refs must exist | yes |

### A.2 Environment variables

| Variable | Read by | Documented |
|---|---|---|
| `MAGE2MCP_LISTEN` | `config.go:135` | yes |
| `MAGE2MCP_PUBLIC_URL` | `config.go:138` | yes |
| `MAGE2MCP_AUTH_ISSUER` | `config.go:141` | yes |
| `MAGE2MCP_AUTH_AUDIENCE` | `config.go:144` | yes |
| `MAGE2MCP_AUTH_JWKS_URL` | `config.go:147` | yes |
| `MAGE2MCP_AUTH_ENABLED` | `config.go:150` | **no** (DOC-14) |
| `MAGE2MCP_LOG_LEVEL` | `config.go:153` | yes |
| `MAGE2MCP_TOOLS_WRITES` | `config.go:156` | yes |
| `MAGE2MCP_DATABASE_URL` | `config.go:159` | yes |
| `MAGE2MCP_DEV_INSECURE` | `cmd/server/main.go:59` | **no** (DOC-14) |
| `MAGE2MCP_ENV` | — (only threat-model:517) | **not implemented** |
| `MAGE2MCP_SANDBOX` | — (only testing-strategy:237) | **not implemented** |

## 9. Appendix B — deployment matrix

| Artifact | Documented | Code | Match |
|---|---|---|---|
| Docker build arg `VERSION` | README:157 | `Dockerfile:7,24`; Compose `args.VERSION` | yes |
| Port 8080 | README:158 | `Dockerfile:37`, Compose `127.0.0.1:8080:8080` | yes |
| Distroless non-root | deployment:24 | `Dockerfile:22,36` | yes |
| Healthcheck | deployment:27-28 | `Dockerfile:39-40` | yes |
| Baked config path | deployment:25-26 | `Dockerfile:35` (`/etc/mage2mcp/config.yaml`) | yes |
| Dev stack auth off | README:152-154 | `docker-compose.yml:35-36` | yes |
| Prod stack env required | README:165-169 | `docker-compose.prod.yml:5-14` | yes (but no data sources) |
| Redis | deployment:36 | removed by ADR-0019 | **no** (DOC-15) |
| `.env.example` | "copy to `.env`" | no dotenv loader; Compose has no `env_file`; vendor vars not passed | **no** (DOC-14) |
| Migrations | upgrade:16-17 | `storage.go:87-150`, 4 migrations | yes |
| Migration count | database-design:165-600 | 4 shipped vs 6+ designed | **no** (DOC-16) |

## 10. Appendix C — finding index

| ID | Severity | State | Title |
|---|---|---|---|
| DOC-01 | P0 | CONFIRMED | Threat model A5 is false; used to omit replay controls |
| DOC-02 | P1 | DOC-ONLY | Phase 0 / no release status across policy and design docs |
| DOC-03 | P1 | DOC-ONLY | README says no security property is implemented |
| DOC-04 | P1 | DOC-ONLY | Threat model cites 55 nonexistent control/test files |
| DOC-05 | P1 | CONFIRMED | Audit integrity/retention claims unimplemented |
| DOC-06 | P1 | CONFIRMED | Shipped example config and quickstart exit non-zero |
| DOC-07 | P1 | DOC-ONLY | Release identity v0.9.0 vs v0.11.0 |
| DOC-08 | P2 | DOC-ONLY | "112 tools" is the maximum, not the default surface |
| DOC-09 | P2 | DOC-ONLY | False one-instance / no-credential-selection claim |
| DOC-10 | P2 | DOC-ONLY | `mcp-tools.md` stale scopes, fields, statuses |
| DOC-11 | P2 | DOC-ONLY | `mcp-conformance.md` claims Redis/EventStore/progress/links |
| DOC-12 | P2 | DOC-ONLY | Magento GraphQL and R2/ClickHouse claims unsupported |
| DOC-13 | P2 | DOC-ONLY | README CI and docs-test claims false/incomplete |
| DOC-14 | P2 | DOC-ONLY | Config reference omissions and secret overstatement |
| DOC-15 | P2 | CONFIRMED | Deployment/backup/monitoring docs stale |
| DOC-16 | P2 | DOC-ONLY | Database design contradicts shipped migrations |
| DOC-17 | P2 | DOC-ONLY | Architecture doc stale; config example fails to load |
| DOC-18 | P2 | DOC-ONLY | Evidence chain broken; GA4 account ID published |
| DOC-19 | P2 | DOC-ONLY | Production/validation claims exceed tested scope |
| DOC-20 | P2 | CONFIRMED | `platform.info` instances/writes never wired |
| DOC-21 | P3 | DOC-ONLY | CHANGELOG structure and README release table |
| DOC-22 | P3 | DOC-ONLY | ADR statuses/claims stale |
| DOC-23 | P3 | DOC-ONLY | Development docs describe a moved-on workflow |
| DOC-24 | P3 | DOC-ONLY | Compatibility/security version and credential drift |
| DOC-25 | P3 | CONFIRMED | Release/admin approval controls overstated |
| DOC-26 | P3 | DOC-ONLY | Documentation index stale |

## 11. Appendix D — overlay test source (tool surface and bindings)

This is the exact file used for §2.1, reproduced so the count can be re-derived
without modifying the repository. It targets the baseline `registerModules`
signature; if that signature changes during remediation, update the calls, not
the expectation.

```go
package main

import (
    "context"
    "fmt"
    "sort"
    "strings"
    "testing"

    "github.com/shuaiZend/mage2mcp/internal/core/config"
    "github.com/shuaiZend/mage2mcp/internal/jobs"
    "github.com/shuaiZend/mage2mcp/internal/mcp/registry"
)

type fakeQueue struct{}

func (fakeQueue) Enqueue(context.Context, jobs.Task) (string, error) { return "job-1", nil }
func (fakeQueue) Cancel(context.Context, string) error             { return nil }
func (fakeQueue) GetJob(context.Context, string) (*jobs.JobRecord, error) {
    return &jobs.JobRecord{}, nil
}

type fakeBatches struct{}

func (fakeBatches) GetImportBatchByJob(context.Context, string) (*jobs.ImportBatch, error) {
    return nil, nil
}
func (fakeBatches) GetImportBatch(context.Context, string) (*jobs.ImportBatch, error) {
    return nil, nil
}
func (fakeBatches) ListImportBatches(context.Context, string, int) ([]jobs.ImportBatch, error) {
    return nil, nil
}

func auditCfg(writes bool) config.Config {
    return config.Config{
        Modules: config.Modules{
            Magento: config.Module{Enabled: true, Instances: []config.Instance{
                {ID: "store-us", Endpoint: "https://shop.example.com", StoreCode: "default", Credential: "bearer:tok"},
            }},
            Cloudflare: config.Module{Enabled: true, Instances: []config.Instance{
                {ID: "zone-us", AccountID: "acct", ZoneID: "zone", Credential: "cf-token"},
            }},
            GA4: config.Module{Enabled: true, Instances: []config.Instance{
                {ID: "giztop", PropertyID: "354542598", Credential: "static-token"},
            }},
        },
        Bindings: []config.Binding{{ID: "b1", Magento: "store-us", Cloudflare: "zone-us", GA4: "giztop"}},
        Tools:    config.Tools{Writes: writes},
    }
}

func TestAuditToolSurface(t *testing.T) {
    for _, tc := range []struct {
        name   string
        writes bool
        db     bool
    }{
        {"read-only, no database", false, false},
        {"writes, no database", true, false},
        {"read-only, database", false, true},
        {"writes, database", true, true},
    } {
        reg := registry.New()
        var err error
        if tc.db {
            err = registerModules(auditCfg(tc.writes), reg, importWiring{Queue: fakeQueue{}, Batches: fakeBatches{}})
        } else {
            err = registerModules(auditCfg(tc.writes), reg)
        }
        if err != nil {
            t.Fatalf("%s: %v", tc.name, err)
        }
        byModule := map[string]int{}
        for _, tool := range reg.List() {
            byModule[strings.SplitN(tool.Name, ".", 2)[0]]++
        }
        fmt.Printf("AUDIT %-24s registry=%d (+platform.info=1) byModule=%v\n", tc.name, reg.Len(), byModule)
    }

    // Full surface name list, writes + database.
    reg := registry.New()
    if err := registerModules(auditCfg(true), reg, importWiring{Queue: fakeQueue{}, Batches: fakeBatches{}}); err != nil {
        t.Fatal(err)
    }
    names := make([]string, 0, reg.Len())
    for _, tool := range reg.List() {
        names = append(names, tool.Name)
    }
    sort.Strings(names)
    fmt.Println("AUDIT_TOOL_NAMES_BEGIN")
    for _, n := range names {
        fmt.Println(n)
    }
    fmt.Println("AUDIT_TOOL_NAMES_END")

    multi := auditCfg(false)
    multi.Modules.Magento.Instances = append(multi.Modules.Magento.Instances,
        config.Instance{ID: "store-eu", Endpoint: "https://eu.example.com", StoreCode: "default", Credential: "bearer:tok2"})
    multi.Modules.Cloudflare.Instances = append(multi.Modules.Cloudflare.Instances,
        config.Instance{ID: "zone-eu", AccountID: "acct2", ZoneID: "zone2", Credential: "cf2"})
    multi.Modules.GA4.Instances = append(multi.Modules.GA4.Instances,
        config.Instance{ID: "ga4-eu", PropertyID: "42", Credential: "tok3"})
    multi.Bindings = []config.Binding{{ID: "b1", Magento: "store-us", Cloudflare: "zone-us", GA4: "giztop"}}
    reg2 := registry.New()
    err := registerModules(multi, reg2)
    fmt.Printf("AUDIT multi-instance-with-bindings err=%v registry=%d\n", err, reg2.Len())

    multi.Bindings = nil
    reg3 := registry.New()
    err = registerModules(multi, reg3)
    byModule := map[string]int{}
    for _, tool := range reg3.List() {
        byModule[strings.SplitN(tool.Name, ".", 2)[0]]++
    }
    fmt.Printf("AUDIT multi-instance-no-bindings err=%v registry=%d byModule=%v\n", err, reg3.Len(), byModule)
}
```
