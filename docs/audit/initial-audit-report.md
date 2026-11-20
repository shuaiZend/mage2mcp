# Initial audit report — repository discovery and baseline

| Field | Value |
|---|---|
| Document | Initial audit report (Phase 1: repository discovery) |
| Project | mage2mcp — MCP gateway for Magento 2 / Adobe Commerce |
| Commit audited | `66786a5d10c1f78d00e08a5b42d01fcc2da37a3f` (`main`) |
| Tag at HEAD | `v0.11.0` |
| Audit date | 2026-11-20 |
| Owner | Chief Engineer |
| Method | Direct source inspection, reproducible commands, real test execution |

This report records **only what was verified against the working tree**. Claims
from earlier audits are treated as investigation leads, not findings. The
per-domain findings live in the sibling reports; §8 indexes them.

---

## 1. Scope

The audit covers the whole repository: the three binaries (`server`, `worker`,
`admin`), the 21 internal packages, the SQL migrations, the tool surface, the
security controls, the job/durability layer, and the documentation set.

Out of scope, and stated as such everywhere: the security of Magento itself,
Cloudflare's edge, the operator's IdP, and the agent runtime.

## 2. Repository state

```text
HEAD            66786a5d10c1f78d00e08a5b42d01fcc2da37a3f
Branch          main
Latest tag      v0.11.0
Working tree    clean at audit start (only untracked docs/audit/ and .audit-tmp/)
Go toolchain    go1.26.0 darwin/arm64
Go directive    go 1.26.0
```

The repository is a single Go module, `github.com/shuaiZend/mage2mcp`, with a
deliberately small dependency set: `golang-jwt/jwt/v5`, `google/jsonschema-go`,
`jackc/pgx/v5`, `modelcontextprotocol/go-sdk`, `prometheus/client_golang`, and
`go.yaml.in/yaml/v3`. There is no Redis dependency; the PostgreSQL `jobs` table
is the queue (ADR-0019).

## 3. Build and test baseline

Executed at the audited commit, before any change:

| Gate | Command | Result |
|---|---|---|
| Formatting | `gofmt -l ./cmd ./internal ./test` | clean (no output) |
| Vet | `go vet ./...` | clean |
| Unit tests | `go test ./...` | pass, all packages |
| Race detector | `go test -race ./...` | pass, all packages |
| Lint | `golangci-lint run` (CI, not run locally) | see §9 |

So the baseline is **green**. Any regression introduced during remediation is
therefore attributable to the remediation, not to a pre-existing failure. This
is the single most useful property of this baseline and it is why it was
captured first.

### 3.1 Coverage, and what it hides

`go test -cover ./internal/...` at the baseline:

| Package | Coverage | Comment |
|---|---|---|
| `internal/storage` | **3.4%** | Every test is gated on `MAGE2MCP_TEST_DATABASE_URL` and `t.Skip()`s when unset |
| `internal/mcp/toolerr` | 66.7% | |
| `internal/mcp/registry` | 70.8% | |
| `internal/security/confirm` | 71.1% | |
| `internal/magento` | 74.7% | Largest package; write paths are the risk |
| `internal/core/config` | 81.4% | |
| `internal/jobs` | 81.7% | |
| `internal/core/errors` | 82.5% | |
| `internal/unified` | 83.3% | |
| `internal/cloudflare` | 83.5% | |
| `internal/ga4` | 86.8% | |
| `internal/mcp/server` | 88.1% | |
| `internal/security/audit` | 89.7% | |
| `internal/security/jwt` | 89.9% | |
| `internal/security/auth` | 97.9% | |
| `internal/observability` | 100.0% | |
| `internal/security/rbac` | 100.0% | |
| `internal/core/datasource` | no test files | 23 LOC |

**The 3.4% figure is the most important number in this report.** `internal/storage`
is 863 lines and owns every durability guarantee the project makes: audit
persistence, atomic single-use confirmation, `FOR UPDATE SKIP LOCKED` job
claiming, lease recovery, idempotent enqueue, and import-batch bookkeeping.
A coverage tool reports 3.4% because the tests *skip*, and a skipped test is not
a passing test.

To distinguish "untested" from "broken", a real PostgreSQL 16.15 instance was
provisioned and the same suite was re-run:

```text
MAGE2MCP_TEST_DATABASE_URL='postgres://…@127.0.0.1:55440/mage2mcp?sslmode=disable' \
  go test -count=1 -v ./internal/storage/...
```

All 13 tests passed on a **freshly migrated database**: `TestMigrateIsIdempotent`,
`TestAuditRecorderRoundTrip`, `TestAuditRejectsMissingFields`,
`TestConfirmStoreLifecycle`, `TestJobQueueLifecycle`,
`TestJobQueueRecoversExpiredLease`, `TestJobQueueResultAndLookup`,
`TestJobQueueIdempotentEnqueue`, `TestImportBatchLifecycle`,
`TestListAndGetImportBatch`, `TestPruneImportBatches`,
`TestPruneImportBatchesNotOpen`, `TestImportBatchStoreClosed`.

**Correction, established during the reliability audit and re-verified here.**
That 13/13 result holds *only* on a virgin schema. Running the same suite a
second time against the same database **fails**:

```text
--- FAIL: TestImportBatchLifecycle (0.01s)
    storage_test.go:248: stored id = "6e909c1d-…"
--- FAIL: TestListAndGetImportBatch (0.01s)
    storage_test.go:270: list = [{…store-list-test…}, {…}, {…}]   # 3 leftover rows
```

The suite is not repeatable, so it cannot gate regressions: it passes once on a
clean database and fails forever after. This is tracked as `REL-15`. The honest
statement is therefore narrower than the first draft of this report: the
durability layer *functions*, its tests *pass once*, and the suite is *not
idempotent* — so it cannot serve as a regression gate until it cleans up after
itself or isolates per test.

With the database connected, real coverage of `internal/storage` is
**61.6–64.2%**, not 3.4%. The 3.4% figure is what a developer sees by default,
because every test skips.

**Correct classification: the durability layer is functional but not exercised
by default CI, and its suite is not repeatable.** Two consequences follow, and
both are actionable:

1. CI's 70% coverage floor is satisfied *without* the storage layer, so the floor
   does not protect the most critical code. Coverage of a skipped suite is not a
   safety net.
2. Every existing storage test is a happy-path or single-threaded test. None of
   them proves the properties the objective cares about: two workers racing for
   one job, a stale worker writing after lease expiry, concurrent consumption of
   one approval, or a crash between upstream success and local commit. Those are
   tracked as `REL-*` findings.

### 3.2 What CI actually runs

`.github/workflows/ci.yml` is `workflow_dispatch` only — a manual pre-release
gate, not a push or pull-request gate. Day-to-day verification is delegated to
`scripts/local-ci.sh`. The workflow does enforce gofmt, `go mod verify`, vet,
golangci-lint, `go test -race` with a coverage profile, a 70% total coverage
floor, markdown/link linting, contract tests, and SHA-pinned actions.

The gap is structural: a green GitHub check does not exist for ordinary commits,
and the coverage floor is measured over a suite that skips its most important
package. This is recorded as a documentation/process finding, not a code defect.

## 4. Structure and module dependency graph

Three binaries and 21 internal packages, ~37,000 lines of Go (≈20,000 non-test,
≈15,000 test).

```text
cmd/server   → config, cloudflare, ga4, magento, mcp/{registry,server},
                observability, security/{audit,auth,confirm,jwt,rbac},
                storage, unified, core/errors
cmd/worker   → config, jobs, magento, observability, storage, core/errors
cmd/admin    → config, security/confirm, storage

internal/magento    → core/errors, jobs, mcp/registry, mcp/toolerr
internal/cloudflare → core/errors, mcp/registry, mcp/toolerr
internal/ga4        → core/errors, mcp/registry, mcp/toolerr
internal/unified    → core/errors, mcp/registry, mcp/toolerr
internal/storage    → core/errors, jobs, security/audit, security/confirm
internal/mcp/server → core/errors, mcp/registry, mcp/toolerr, security/auth, security/confirm
internal/security/* → core/errors (jwt and rbac also → security/auth)
```

The graph has **no cycles** and, critically, **no module imports another module**:
`magento`, `cloudflare`, `ga4`, and `unified` are siblings that only meet through
`mcp/registry` (the tool contract) and `core/errors` (the error contract). That is
the modular boundary ADR-0001 claims, and it holds in code.

Size distribution (non-test LOC):

| Package | LOC | Note |
|---|---|---|
| `internal/magento` | 13,694 | 74 files; `write_tools.go` alone is 137 KB |
| `internal/ga4` | 1,902 | |
| `internal/cloudflare` | 972 | |
| `internal/storage` | 863 | Every durability guarantee; 3.4% tested |
| `internal/security/jwt` | 454 | |
| `cmd/server` | 449 | |
| all others | < 350 each | |

**Architectural observation.** The layering is sound but the Magento module has
become a monolith: 13,694 lines across 74 files, with `write_tools.go` (137 KB)
mixing tool registration, input validation, business logic, and Magento REST
calls. The separation that exists everywhere else in the repository does not
exist inside this package. This is not a correctness defect, but it is why three
different agents would collide if they edited the same file, and it is the main
reason the remediation plan assigns file-level ownership rather than
package-level ownership.

## 5. Capability inventory (as measured, not as documented)

| Measure | Value | How measured |
|---|---|---|
| Packages | 23 (21 internal + 3 cmd, minus overlap) | `go list ./...` |
| Go files | 150 | `find -name '*.go'` |
| Total Go LOC | 36,996 | `wc -l` |
| Test LOC | 15,069 | `wc -l` on `*_test.go` |
| SQL migrations | 4 | `internal/storage/migrations/` |
| Magento tool-name literals | 118 unique | `grep` over `tools.go` + `write_tools.go` |
| Registered tools (verified) | **112** (Magento **101**) | registry enumeration + `tools/list`, `mcp-protocol-review.md` Appendix A |
| README claim | "112 tools (101 Magento)" | `README.md:15`, `README.md:36` |

**Resolved.** The 118-versus-112 gap was an artifact of this report's own grep,
which counts string literals including namespace and error-code references rather
than registered tools. The MCP engineer enumerated the registry mechanically and
the README's "112 tools (101 Magento)" is **accurate for the maximal
configuration** (writes enabled and a database configured): 101 Magento, 3
Cloudflare, 6 GA4, 1 unified, 1 platform. The read-only all-modules default is
54 tools (43 Magento); 52 Magento writes are gated on `tools.writes`, and 6
asynchronous import tools are gated on `database.url`.

So the documentation is not wrong about the count. What the MCP audit does
establish is that the count is *configuration-dependent* and the README does not
say so, and that the inventory is hand-maintained rather than generated from the
registry (`MCP-15`, `MCP-16`). The objective's Task A — a single source of truth
that emits the count — is still the correct fix; it is just a maintainability
defect rather than a factual error.

## 6. Verification environment

| Capability | Available | Consequence |
|---|---|---|
| Go 1.26.0 | yes | Full unit/race suite runs |
| PostgreSQL 16.15 (Docker, cached image) | yes, provisioned on `127.0.0.1:55440` | Storage/confirm/audit/lease tests run for real |
| Docker image registry | **no** (registry unreachable) | Cannot pull `postgres:17-alpine` or a Magento image |
| Real Magento 2.4.9 instance | **no** | No test may be labelled `INTEGRATION` for Magento |
| Network to Magento upstream | not applicable | All Magento tests use `httptest` fakes (`CONTRACT`) |

This is the honest boundary of what this audit can prove. Magento behaviour is
verified against the REST contract and the fake server, never against a live
store. The e2e harness (`test/e2e/`) exists and can build a fake Magento, but
`docker pull` failures prevent the full compose-based smoke test. Every report
must label Magento coverage `CONTRACT`, and `known-limitations.md` must say so
without hedging.

## 7. Preliminary assessment of the ten prior-audit leads

These are **leads under investigation**, not conclusions. Each names the report
that will settle it.

| # | Prior lead | Preliminary observation | Settled by |
|---|---|---|---|
| 1 | Docs inconsistent with code | Confirmed at least one hard contradiction: `docs/implemented-tools.md` states "the value does not select credentials yet", but `internal/magento/resolver.go` and `RegisterWritesWithResolver` route reads *and* writes per instance. | `DOC-*` |
| 2 | Multi-instance credential isolation incomplete | `Resolver` + `RegisterWithResolver` + `RegisterWritesWithResolver` exist and are wired in `cmd/server`. Isolation looks implemented; whether RBAC's instance matching actually prevents cross-tenant selection is the open question. | `SEC-*` |
| 3 | Configurable product partial success / orphans | `CreateConfigurable` runs children → parent → links as bare sequential REST calls and returns a completed-stage list on failure. There is no resumable state machine and no compensation. Confirmed as a design gap; severity to be set by reproduction. | `MAG-*` |
| 4 | MSI transfer lacks consistency guarantees | `TransferSourceQty` reads two source quantities, computes absolute new values, and writes both back via one bulk call. No ledger, no transfer id, no compare-and-swap. The lost-update interleaving is real by inspection. | `MAG-*` |
| 5 | Financial operations can double-execute | `CaptureInvoice` is a bare `POST /invoices/{id}/capture` with no pre-flight state check and no idempotency key. `IdempotencyKey` appears in the codebase **only** in the job queue. | `MAG-*`, `REL-*` |
| 6 | Threat model contains unimplemented controls | Assumption **A5** states "Orders and customers are read-only in v1; no order-mutating tool ships", yet order-mutating tools exist (`magento.order.create`, `magento.order.cancel`, `magento.order.hold`, `magento.invoice.create`, `magento.invoice.capture`, `magento.creditmemo.create`, …). A5 is falsified. | `SEC-*`, `DOC-*` |
| 7 | Too many tools for reliable selection | 112–118 tools with no workflow grouping. `magento.products.*`, `magento.categories.*`, `magento.attributes.*`, `magento.orders.*` sit beside singular namespaces. | `MCP-*` |
| 8 | N+1 Magento API queries | Not yet measured. Loops that issue one request per element exist; the `products.get` batch tool shows the team already knows the pattern. | `PERF-*` |
| 9 | Large-volume pagination / async / resume insufficient | Job queue supports enqueue/resume for imports only. No general resumable long-running report. A fixed row cap is suspected. | `PERF-*`, `REL-*` |
| 10 | Magento 2.4.9 compatibility unverified | No live instance is reachable. Cannot be verified in this environment; must be declared in `known-limitations.md`. | `MAG-*` |

## 8. Consolidated finding index

The audit is complete. Every finding is indexed in
[remediation-plan.md](remediation-plan.md), which is the authoritative tracker.

| ID range | Report | Owner | Findings |
|---|---|---|---|
| `SEC-*` | [security-audit-report.md](security-audit-report.md) | Security auditor | 19 |
| `MAG-*` | [magento-integration-review.md](magento-integration-review.md) | Magento engineer | 26 |
| `REL-*` | [reliability-review.md](reliability-review.md) | Reliability engineer | 21 |
| `MCP-*` | [mcp-protocol-review.md](mcp-protocol-review.md) | MCP engineer | 16 |
| `PERF-*` | [performance-review.md](performance-review.md) | Performance engineer | 23 |
| `DOC-*` | [documentation-consistency.md](documentation-consistency.md) | Documentation engineer | 26 |
| `ARCH-*` | [captain-findings.md](captain-findings.md) | Chief Engineer | 8 |
| `QA-*` | [regression-test-report.md](regression-test-report.md) | QA engineer | 10 |

**Result: 139 raw findings, 8 distinct P0s, all fixed and independently verified.**
The final verdict is in
[release-readiness-report.md](release-readiness-report.md); deferred items and
accepted risks are in [known-limitations.md](known-limitations.md).

## 9. Method limitations

Stated plainly so that no reader mistakes this audit for more than it is:

1. **No live Magento.** Every Magento claim is contract-verified against the REST
   API and the fake server. Magento 2.4.9 compatibility is unverified.
2. **No live Cloudflare or GA4.** Those adapters are reviewed for code quality
   and contract fidelity, not exercised against the real APIs.
3. **golangci-lint was not run locally** (it is fetched over the network by
   `make lint`). Static analysis coverage is therefore `go vet` plus manual
   reading. If the network permits, the lint gate should be run before release.
4. **Docker registry unreachable**, so the container image cannot be rebuilt and
   the compose-based e2e smoke test cannot run. `Dockerfile` is reviewed
   statically.
5. **Single-host, single-database testing.** Concurrency properties are probed
   with real goroutines against a real PostgreSQL, which is sufficient to
   demonstrate a lost update or a double-claim, but not sufficient to model a
   multi-node deployment with clock skew.

These limitations are carried forward into `known-limitations.md` and constrain
the final verdict in `release-readiness-report.md`.
