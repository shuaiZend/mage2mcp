# Regression test report — QA baseline, inventory, blind spots, verification plan

| Field | Value |
|---|---|
| Document | Regression test report (QA baseline and independent verification plan) |
| Project | mage2mcp — MCP gateway for Magento 2 / Adobe Commerce |
| Commit audited | `66786a5d10c1f78d00e08a5b42d01fcc2da37a3f` (`v0.11.0-3-g66786a5`), tree `a3bab9943096973622011e5fe6e646ca9f50a8ae` |
| Audit date | 2026-11-20 (UTC) |
| Owner | QA engineer |
| Method | Real command execution on a pristine export of the audited commit, plus a real PostgreSQL 16 instance |
| Scope | Measurement and planning only. No production code and no existing test was modified. |

This report records **what was actually executed**, on which environment, and what the
result means.

> **Update (t35):** the §6 verification plan is now built and green. See §8 for the
> as-built independent suite, the exact commands, verbatim results, and what remains a
> blind spot. A test that did not run is reported as NOT RUN, never as passing. Mocked
coverage is labelled `CONTRACT`; only a real Magento instance could earn `INTEGRATION`,
and no such instance was available (see §5).

---

## 0. How the baseline was measured

The audit commit is `66786a5`. The working tree gained untracked probe tests from other
audit agents while this baseline was being taken (`internal/jobs/probe_reliability_test.go`,
`internal/storage/probe_reliability_test.go`, `.audit-tmp/`). Those files are not part of
the commit and would have contaminated the measurement, so every number in §1–§3 was
produced on a pristine export:

```bash
rm -rf /tmp/mage2mcp-clean && mkdir -p /tmp/mage2mcp-clean
git archive HEAD | tar -x -C /tmp/mage2mcp-clean
```

The export contains exactly the 77 test files tracked at `66786a5`; no probe file, no
`docs/audit/`, no `.audit-tmp/`. At measurement time `git status --porcelain` on the real
repository showed only untracked files, so the audited tree and the exported tree were
identical for every `.go` file. Tracked remediation edits appeared in the working tree
afterwards; that is precisely why this baseline is pinned to the commit hash and every
number below is reproducible from `git archive 66786a5` alone.

Evidence labels used throughout:

| Label | Meaning | Can be produced here? |
|---|---|---|
| `UNIT` | Pure in-process test, no network, no database | Yes |
| `CONTRACT` | `httptest` fake upstream (Magento/Cloudflare/GA4/JWKS) | Yes |
| `PROTOCOL` | MCP client against an in-process or `httptest` MCP server | Yes |
| `INTEGRATION-PG` | Needs a live PostgreSQL (`MAGE2MCP_TEST_DATABASE_URL`) | Yes (see §5) |
| `E2E` | Whole stack against a **fake** Magento | Yes, with an environment patch (§5.3) |
| `INTEGRATION` | Real Magento / Adobe Commerce instance | **No** |

---

## 1. Baseline

### 1.1 Environment

| Fact | Value |
|---|---|
| Commit | `66786a5d10c1f78d00e08a5b42d01fcc2da37a3f` |
| `git describe --tags` | `v0.11.0-3-g66786a5` (HEAD is 3 commits after the `v0.11.0` tag) |
| `go.mod` directive | `go 1.26.0` |
| `go` binary | `/opt/homebrew/bin/go` |
| `go version` | `go version go1.26.0 darwin/arm64` |
| GOROOT | `/Users/shuaizeng/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.0.darwin-arm64` |
| `go env GOOS GOARCH GOVERSION` | `darwin`, `arm64`, `go1.26.0` |
| OS | `Darwin MacBook-Pro-3.local 27.0.0 Darwin Kernel Version 27.0.0 ... arm64` |
| CI Go version (`.github/workflows/ci.yml:15`) | `1.27.x` (default input) — **different from the pinned toolchain** |
| Local toolchains also in the module cache | `go1.25.0`, `go1.26.0`, `go1.26.3`, `go1.26.11`, `go1.27.4` |
| PostgreSQL | 16.15, `postgres://mage2mcp:mage2mcp-dev@127.0.0.1:55440/mage2mcp_qa` (per-agent instance provided by the Chief Engineer) |
| Docker | 29.8.2 (daemon reachable) |
| Network to Docker Hub | **blocked** (`Bad Gateway` on `docker pull postgres:17-alpine`) |
| Real Magento | **none** (`MAGENTO_*` unset, no store reachable) |

### 1.2 Required commands, verbatim

All five commands were run from `/tmp/mage2mcp-clean` (pristine `HEAD`). In the quoted
output below, tab characters emitted by `go test` are rendered as four spaces so the
report passes the repository's markdownlint gate; only whitespace differs from the
terminal output, no character of any result was changed.

#### `gofmt -l ./cmd ./internal ./test`

```text
[no output, exit 0]
```

#### `go vet ./...`

```text
[no output, exit 0]
```

#### `go test ./...`

```text
?       github.com/shuaiZend/mage2mcp/cmd/admin    [no test files]
ok      github.com/shuaiZend/mage2mcp/cmd/server    0.371s
ok      github.com/shuaiZend/mage2mcp/cmd/worker    0.359s
ok      github.com/shuaiZend/mage2mcp/internal/cloudflare    0.541s
ok      github.com/shuaiZend/mage2mcp/internal/core/config    0.316s
?       github.com/shuaiZend/mage2mcp/internal/core/datasource    [no test files]
ok      github.com/shuaiZend/mage2mcp/internal/core/errors    0.306s
ok      github.com/shuaiZend/mage2mcp/internal/ga4    0.740s
ok      github.com/shuaiZend/mage2mcp/internal/jobs    0.344s
ok      github.com/shuaiZend/mage2mcp/internal/magento    0.800s
ok      github.com/shuaiZend/mage2mcp/internal/mcp/registry    0.348s
ok      github.com/shuaiZend/mage2mcp/internal/mcp/server    0.369s
ok      github.com/shuaiZend/mage2mcp/internal/mcp/toolerr    0.355s
ok      github.com/shuaiZend/mage2mcp/internal/observability    0.348s
ok      github.com/shuaiZend/mage2mcp/internal/security/audit    0.321s
ok      github.com/shuaiZend/mage2mcp/internal/security/auth    0.333s
ok      github.com/shuaiZend/mage2mcp/internal/security/confirm    0.316s
ok      github.com/shuaiZend/mage2mcp/internal/security/jwt    1.520s
ok      github.com/shuaiZend/mage2mcp/internal/security/rbac    0.333s
ok      github.com/shuaiZend/mage2mcp/internal/storage    0.351s
ok      github.com/shuaiZend/mage2mcp/internal/unified    0.352s
?       github.com/shuaiZend/mage2mcp/test/e2e/client    [no test files]
?       github.com/shuaiZend/mage2mcp/test/e2e/fakemagento    [no test files]
[exit 0]
```

`internal/storage` reports `ok` in 0.351 s. It is **not** a passing durability suite: 11 of
its 13 tests skipped (see §2.3). A skip is reported by `go test` as success.

#### `go test -race ./...`

```text
?       github.com/shuaiZend/mage2mcp/cmd/admin    [no test files]
ok      github.com/shuaiZend/mage2mcp/cmd/server    1.540s
ok      github.com/shuaiZend/mage2mcp/cmd/worker    1.458s
ok      github.com/shuaiZend/mage2mcp/internal/cloudflare    1.666s
ok      github.com/shuaiZend/mage2mcp/internal/core/config    1.381s
?       github.com/shuaiZend/mage2mcp/internal/core/datasource    [no test files]
ok      github.com/shuaiZend/mage2mcp/internal/core/errors    1.370s
ok      github.com/shuaiZend/mage2mcp/internal/ga4    2.216s
ok      github.com/shuaiZend/mage2mcp/internal/jobs    1.436s
ok      github.com/shuaiZend/mage2mcp/internal/magento    2.728s
ok      github.com/shuaiZend/mage2mcp/internal/mcp/registry    1.363s
ok      github.com/shuaiZend/mage2mcp/internal/mcp/server    1.425s
ok      github.com/shuaiZend/mage2mcp/internal/mcp/toolerr    1.379s
ok      github.com/shuaiZend/mage2mcp/internal/observability    1.377s
ok      github.com/shuaiZend/mage2mcp/internal/security/audit    1.333s
ok      github.com/shuaiZend/mage2mcp/internal/security/auth    1.349s
ok      github.com/shuaiZend/mage2mcp/internal/security/confirm    1.328s
ok      github.com/shuaiZend/mage2mcp/internal/security/jwt    3.340s
ok      github.com/shuaiZend/mage2mcp/internal/security/rbac    1.325s
ok      github.com/shuaiZend/mage2mcp/internal/storage    1.336s
ok      github.com/shuaiZend/mage2mcp/internal/unified    1.351s
?       github.com/shuaiZend/mage2mcp/test/e2e/client    [no test files]
?       github.com/shuaiZend/mage2mcp/test/e2e/fakemagento    [no test files]
[exit 0]
```

#### `go test -count=1 ./...`

```text
?       github.com/shuaiZend/mage2mcp/cmd/admin    [no test files]
ok      github.com/shuaiZend/mage2mcp/cmd/server    0.161s
ok      github.com/shuaiZend/mage2mcp/cmd/worker    0.158s
ok      github.com/shuaiZend/mage2mcp/internal/cloudflare    0.306s
ok      github.com/shuaiZend/mage2mcp/internal/core/config    0.109s
?       github.com/shuaiZend/mage2mcp/internal/core/datasource    [no test files]
ok      github.com/shuaiZend/mage2mcp/internal/core/errors    0.098s
ok      github.com/shuaiZend/mage2mcp/internal/ga4    0.695s
ok      github.com/shuaiZend/mage2mcp/internal/jobs    0.163s
ok      github.com/shuaiZend/mage2mcp/internal/magento    0.575s
ok      github.com/shuaiZend/mage2mcp/internal/mcp/registry    0.127s
ok      github.com/shuaiZend/mage2mcp/internal/mcp/server    0.149s
ok      github.com/shuaiZend/mage2mcp/internal/mcp/toolerr    0.133s
ok      github.com/shuaiZend/mage2mcp/internal/observability    0.127s
ok      github.com/shuaiZend/mage2mcp/internal/security/audit    0.145s
ok      github.com/shuaiZend/mage2mcp/internal/security/auth    0.149s
ok      github.com/shuaiZend/mage2mcp/internal/security/confirm    0.123s
ok      github.com/shuaiZend/mage2mcp/internal/security/jwt    1.002s
ok      github.com/shuaiZend/mage2mcp/internal/security/rbac    0.120s
ok      github.com/shuaiZend/mage2mcp/internal/storage    0.136s
ok      github.com/shuaiZend/mage2mcp/internal/unified    0.121s
?       github.com/shuaiZend/mage2mcp/test/e2e/client    [no test files]
?       github.com/shuaiZend/mage2mcp/test/e2e/fakemagento    [no test files]
[exit 0]
real    0m1.674s
```

An additional `go test -race -count=1 ./...` (cache-bypassing, race-enabled) also exited 0
in 4.968 s. The `-race` result above was executed, not cached (cached `-race` results were
observed first and deliberately bypassed with `-count=1`).

### 1.3 `go test -cover ./...` — the pinned toolchain fails this command

On the repository's own pinned toolchain (`go 1.26.0`), the documented coverage command
exits **1**, even though every test passes:

```text
$ go test -cover ./...
# github.com/shuaiZend/mage2mcp/cmd/admin
go: no such tool "covdata"
ok      github.com/shuaiZend/mage2mcp/cmd/server    0.391s    coverage: 37.4% of statements
...
# github.com/shuaiZend/mage2mcp/test/e2e/client
go: no such tool "covdata"
# github.com/shuaiZend/mage2mcp/test/e2e/fakemagento
go: no such tool "covdata"
ok      github.com/shuaiZend/mage2mcp/internal/storage    0.388s    coverage: 3.4% of statements
ok      github.com/shuaiZend/mage2mcp/internal/unified    0.325s    coverage: 83.3% of statements
[exit 1]
```

The cause is environmental, not a repository defect: the downloaded `go1.26.0` toolchain
directory contains `asm cgo compile cover fix link preprofile vet` and **no `covdata`**.
The same command with the CI toolchain succeeds:

```text
$ GOTOOLCHAIN=go1.27.4 go test -cover ./...
    github.com/shuaiZend/mage2mcp/cmd/admin        coverage: 0.0% of statements
ok      github.com/shuaiZend/mage2mcp/cmd/server    0.518s    coverage: 37.4% of statements
...
    github.com/shuaiZend/mage2mcp/test/e2e/fakemagento        coverage: 0.0% of statements
[exit 0]
```

Consequence: `make cover` (which runs `go test -covermode=atomic -coverprofile=coverage.out
./...`) also exits 1 on the pinned toolchain, because it includes `cmd/admin`,
`test/e2e/client`, and `test/e2e/fakemagento` (main packages with no test files).

### 1.4 With a real PostgreSQL 16 instance

`MAGE2MCP_TEST_DATABASE_URL='postgres://mage2mcp:mage2mcp-dev@127.0.0.1:55440/mage2mcp_qa?sslmode=disable'`

`go test -race -count=1 ./...` with that variable set on a virgin schema:

```text
?       github.com/shuaiZend/mage2mcp/cmd/admin    [no test files]
ok      github.com/shuaiZend/mage2mcp/cmd/server    1.270s
ok      github.com/shuaiZend/mage2mcp/cmd/worker    1.312s
ok      github.com/shuaiZend/mage2mcp/internal/cloudflare    1.394s
ok      github.com/shuaiZend/mage2mcp/internal/core/config    1.130s
?       github.com/shuaiZend/mage2mcp/internal/core/datasource    [no test files]
ok      github.com/shuaiZend/mage2mcp/internal/core/errors    1.109s
ok      github.com/shuaiZend/mage2mcp/internal/ga4    1.957s
ok      github.com/shuaiZend/mage2mcp/internal/jobs    1.178s
ok      github.com/shuaiZend/mage2mcp/internal/magento    2.454s
ok      github.com/shuaiZend/mage2mcp/internal/mcp/registry    1.155s
ok      github.com/shuaiZend/mage2mcp/internal/mcp/server    1.200s
ok      github.com/shuaiZend/mage2mcp/internal/mcp/toolerr    1.164s
ok      github.com/shuaiZend/mage2mcp/internal/observability    1.154s
ok      github.com/shuaiZend/mage2mcp/internal/security/audit    1.094s
ok      github.com/shuaiZend/mage2mcp/internal/security/auth    1.098s
ok      github.com/shuaiZend/mage2mcp/internal/security/confirm    1.082s
ok      github.com/shuaiZend/mage2mcp/internal/security/jwt    2.843s
ok      github.com/shuaiZend/mage2mcp/internal/security/rbac    1.094s
ok      github.com/shuaiZend/mage2mcp/internal/storage    1.326s
ok      github.com/shuaiZend/mage2mcp/internal/unified    1.131s
?       github.com/shuaiZend/mage2mcp/test/e2e/client    [no test files]
?       github.com/shuaiZend/mage2mcp/test/e2e/fakemagento    [no test files]
[exit 0]
real    0m4.689s
```

All 13 storage tests pass on a **virgin** database (verbose run, §1.5). The durability
layer works. The problem is that it is not exercised by default and that it is not
repeatable (QA-03 / REL-15).

### 1.5 Storage suite, verbose, virgin database

```text
=== RUN   TestMigrateIsIdempotent
--- PASS: TestMigrateIsIdempotent (0.15s)
=== RUN   TestAuditRecorderRoundTrip
--- PASS: TestAuditRecorderRoundTrip (0.01s)
=== RUN   TestAuditRejectsMissingFields
--- PASS: TestAuditRejectsMissingFields (0.01s)
=== RUN   TestConfirmStoreLifecycle
--- PASS: TestConfirmStoreLifecycle (0.01s)
=== RUN   TestJobQueueLifecycle
--- PASS: TestJobQueueLifecycle (0.01s)
=== RUN   TestJobQueueRecoversExpiredLease
--- PASS: TestJobQueueRecoversExpiredLease (0.01s)
=== RUN   TestJobQueueResultAndLookup
--- PASS: TestJobQueueResultAndLookup (0.01s)
=== RUN   TestJobQueueIdempotentEnqueue
--- PASS: TestJobQueueIdempotentEnqueue (0.01s)
=== RUN   TestImportBatchLifecycle
--- PASS: TestImportBatchLifecycle (0.01s)
=== RUN   TestListAndGetImportBatch
--- PASS: TestListAndGetImportBatch (0.01s)
=== RUN   TestPruneImportBatches
--- PASS: TestPruneImportBatches (0.01s)
=== RUN   TestPruneImportBatchesNotOpen
--- PASS: TestPruneImportBatchesNotOpen (0.00s)
=== RUN   TestImportBatchStoreClosed
--- PASS: TestImportBatchStoreClosed (0.00s)
PASS
ok      github.com/shuaiZend/mage2mcp/internal/storage    0.397s
[exit 0]
```

### 1.6 Additional gates

| Gate | Command | Result |
|---|---|---|
| Lint | `GOTOOLCHAIN=go1.27.4 go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run` | `0 issues.` exit 0 |
| Lint, pinned toolchain | same without `GOTOOLCHAIN` | fails before linting: `requires go >= 1.26.0 (running go 1.21.4)` / `module lookup disabled by GOPROXY=off` — cannot run offline on the pinned toolchain |
| Docs contracts | `npm run test:contracts` | `Design contracts OK: 75 tools, 22 ADRs` exit 0 |
| CI coverage floor replica (no DB) | `go test -race -count=1 -covermode=atomic -coverprofile=... $(go list ./... \| grep -v /test/e2e/)` | exit 0, total **71.7 %** (floor is 70 %) |
| CI coverage floor replica (with DB) | same + `MAGE2MCP_TEST_DATABASE_URL` | exit 0, total **74.3 %**, `internal/storage` 61.6 % |

### 1.7 Baseline verdict

The baseline is **green on the default path and incomplete**. Precisely:

- `gofmt`, `go vet`, `go test`, `go test -race`, `go test -count=1` all pass at `66786a5`.
  Any regression introduced during remediation is therefore attributable to the
  remediation.
- The green result does **not** cover the durability layer: 12 tests (11 in
  `internal/storage`, 1 in `cmd/worker`) are skipped without a database, and the
  documented pre-push gate `scripts/local-ci.sh:22` never sets the variable.
- The green result does **not** cover any real Magento instance, any concurrency between
  identities, any worker failover, any database outage, or any ambiguous money operation
  (§4).

---

## 2. Test inventory

76 of the 77 tracked test files contain `Test` functions; 1
(`internal/magento/import_fake_test.go`) is a helper-only file. Total: **451 test
functions**, 51 `t.Run` subtest sites, 0 benchmarks, 0 fuzz targets, 0 build-tagged
tests.

### 2.1 Per-package inventory

| Package | Files | Funcs | Kind | What is actually asserted | Runs by default |
|---|---:|---:|---|---|---|
| `cmd/admin` | 0 | 0 | — | Nothing. 91-line approval CLI (`pending`, `approve`) has no test file. | n/a |
| `cmd/server` | 1 | 11 | UNIT | Config→module wiring, signer forms, multi-instance registration, dev-insecure guard. Asserts registry lookups and constructed signers, not serving behaviour. | Yes |
| `cmd/worker` | 1 | 3 | UNIT 2 + INTEGRATION-PG 1 | Import-result→batch mapping (all failures kept); prune loop stops on cancel and logs errors. The DB-gated test only asserts the prune loop exits after cancellation. | 2 yes, 1 **SKIP** |
| `internal/cloudflare` | 6 | 33 | CONTRACT 19, PROTOCOL 9, UNIT 5 | GraphQL request method/path/variables, pagination, rate-limit and error mapping, tool schemas via MCP. | Yes |
| `internal/core/config` | 1 | 10 | UNIT | Defaults, overrides, env expansion, unknown-field rejection, auth/instance/binding validation. | Yes |
| `internal/core/datasource` | 0 | 0 | — | Interface declarations only (23 LOC); no runtime code to test. | n/a |
| `internal/core/errors` | 1 | 6 | UNIT | Code coercion, detail bounds, cause unwrapping, retryable classification. | Yes |
| `internal/ga4` | 10 | 51 | CONTRACT 15, PROTOCOL 11, UNIT 25 | Report request/response mapping, dimension/metric validation, service-account token, realtime, compatibility. | Yes |
| `internal/jobs` | 1 | 4 | UNIT | Worker completes / fails without handler / retries while attempts remain, against a fake queue. Nothing about leases, panics, cancellation mid-task, or two workers. | Yes |
| `internal/magento` | 42 | 237 | CONTRACT 197, PROTOCOL 28, UNIT 12 | HTTP method/path/query/body for every tool, response projection, PII omission, error mapping tables, partial-success accounting, confirmation-argument validation, OAuth1 vector. | Yes |
| `internal/mcp/registry` | 1 | 5 | UNIT | Tool validation, duplicate rejection, sorted listing, input validator wiring. | Yes |
| `internal/mcp/server` | 2 | 9 | PROTOCOL (httptest HTTP) | `tools/list`, `platform.info`, health/ready 200, authorization denial, audit intent+outcome, one confirmation flow, cross-instance denial. | Yes |
| `internal/mcp/toolerr` | 1 | 3 | PROTOCOL | Structured error/success envelopes, invalid-argument envelope. | Yes |
| `internal/observability` | 2 | 11 | CONTRACT 4, UNIT 7 | Logger redaction, metrics registration/concurrency. | Yes |
| `internal/security/audit` | 1 | 6 | UNIT | Field validation, defaults, copy semantics, cancelled context, concurrent in-memory recording. | Yes |
| `internal/security/auth` | 1 | 8 | CONTRACT (httptest) | Middleware rejects missing/malformed headers, accepts valid token, maps verifier reasons, never leaks token. | Yes |
| `internal/security/confirm` | 1 | 9 | UNIT | Canonical hash, full lifecycle, approval required, argument binding, wrong requester, expiry, self-approval, UUID shape. Sequential only. | Yes |
| `internal/security/jwt` | 1 | 15 | CONTRACT (httptest JWKS) | RS256/ES256, claim/alg/kid rejections, oversized/malformed tokens, JWKS rate limit, redirect rejection, response size, context cancellation, TTL refetch, no token logging. | Yes |
| `internal/security/rbac` | 1 | 8 | UNIT | Default deny, exact/prefix allow, instance scoping, deny-overrides, scope hierarchy, nil identity, unknown scope. | Yes |
| `internal/storage` | 1 | 13 | INTEGRATION-PG | Migrations, audit round-trip, confirm lifecycle, job queue lifecycle/lease recovery/idempotent enqueue, import batches. 11 of 13 skip without a database. | 2 yes, 11 **SKIP** |
| `internal/unified` | 2 | 9 | UNIT 8, PROTOCOL 1 | Compare happy path, zero-denominator ratios, unknown binding, window validation, provider error propagation, concurrent fetch. | Yes |
| `test/e2e/client`, `test/e2e/fakemagento` | 0 | 0 | — | Harness programs (`main`), not `go test` targets. Exercised only by `test/e2e/run.sh`. | n/a |

Kind totals: **CONTRACT 267**, **UNIT 117**, **PROTOCOL 52**, **INTEGRATION-PG 15**.

### 2.2 Skipped, gated, or environment-dependent tests — all of them

There are **no build-tagged tests** (`grep '^//go:build' --include='*_test.go'` → 0) and
**no fuzz targets**. The only gating mechanism is one environment variable, used in two
files:

| Test | File | Gate | Effect when unset |
|---|---|---|---|
| `TestMigrateIsIdempotent` | `internal/storage/storage_test.go:36` | `testStore` → `MAGE2MCP_TEST_DATABASE_URL` | SKIP |
| `TestAuditRecorderRoundTrip` | `internal/storage/storage_test.go:47` | same | SKIP |
| `TestAuditRejectsMissingFields` | `internal/storage/storage_test.go:79` | same | SKIP |
| `TestConfirmStoreLifecycle` | `internal/storage/storage_test.go:87` | same | SKIP |
| `TestJobQueueLifecycle` | `internal/storage/storage_test.go:113` | same | SKIP |
| `TestJobQueueRecoversExpiredLease` | `internal/storage/storage_test.go:136` | same | SKIP |
| `TestJobQueueResultAndLookup` | `internal/storage/storage_test.go:160` | same | SKIP |
| `TestJobQueueIdempotentEnqueue` | `internal/storage/storage_test.go:193` | same | SKIP |
| `TestImportBatchLifecycle` | `internal/storage/storage_test.go:222` | same | SKIP |
| `TestListAndGetImportBatch` | `internal/storage/storage_test.go:252` | same | SKIP |
| `TestPruneImportBatches` | `internal/storage/storage_test.go:289` | same | SKIP |
| `TestPruneImportBatchesStopsOnCancel` | `cmd/worker/main_test.go:36` | same | SKIP |

Verbatim, without the variable (`go test -count=1 -v ./internal/storage/...`):

```text
=== RUN   TestMigrateIsIdempotent
--- SKIP: TestMigrateIsIdempotent (0.00s)
=== RUN   TestAuditRecorderRoundTrip
--- SKIP: TestAuditRecorderRoundTrip (0.00s)
=== RUN   TestAuditRejectsMissingFields
--- SKIP: TestAuditRejectsMissingFields (0.00s)
=== RUN   TestConfirmStoreLifecycle
--- SKIP: TestConfirmStoreLifecycle (0.00s)
=== RUN   TestJobQueueLifecycle
--- SKIP: TestJobQueueLifecycle (0.00s)
=== RUN   TestJobQueueRecoversExpiredLease
--- SKIP: TestJobQueueRecoversExpiredLease (0.00s)
=== RUN   TestJobQueueResultAndLookup
--- SKIP: TestJobQueueResultAndLookup (0.00s)
=== RUN   TestJobQueueIdempotentEnqueue
--- SKIP: TestJobQueueIdempotentEnqueue (0.00s)
=== RUN   TestImportBatchLifecycle
--- SKIP: TestImportBatchLifecycle (0.00s)
=== RUN   TestListAndGetImportBatch
--- SKIP: TestListAndGetImportBatch (0.00s)
=== RUN   TestPruneImportBatches
--- SKIP: TestPruneImportBatches (0.00s)
=== RUN   TestPruneImportBatchesNotOpen
--- PASS: TestPruneImportBatchesNotOpen (0.00s)
=== RUN   TestImportBatchStoreClosed
--- PASS: TestImportBatchStoreClosed (0.00s)
PASS
ok      github.com/shuaiZend/mage2mcp/internal/storage    0.164s
```

and `go test -count=1 -v ./cmd/worker/...`:

```text
--- PASS: TestImportBatchResultKeepsAllFailures (0.00s)
--- SKIP: TestPruneImportBatchesStopsOnCancel (0.00s)
--- PASS: TestPruneImportBatchesLogsAndStops (0.00s)
```

**12 tests are NOT RUN in the baseline.** They are counted as skipped, not passed.

### 2.3 Assertion quality

An automated scan of every test function (438 of 451 parsed by brace matching; the 13
unparsed are brace-in-string cases that the manual review covered) found:

- **0** tests that assert nothing. The two initially flagged
  (`TestVerifyClaimRejections`, `TestVerifyRejectsMalformedTokens`) assert through the
  `requireReason(t, …)` helper, which calls `t.Fatalf`.
- **3** tests whose only condition is `err == nil` / `err != nil`
  (`TestValidateModuleInstance`, `TestVerifyJWKSResponseTooLarge`,
  `TestVerifyContextCancellationAbortsFetch`); each is a legitimate negative test.
- The suite is assertion-dense: 267 contract tests assert method, path, query, body, and
  error code; `TestErrorMapping` additionally asserts that the upstream body is never
  leaked.

So the problem is not vacuous tests. The problem is **what is not tested at all** (§4) and
the two test-infrastructure defects QA-01/QA-03.

---

## 3. Coverage

Measured with the CI toolchain (`GOTOOLCHAIN=go1.27.4`, because the pinned 1.26.0 cannot
run coverage — §1.3). Per-package numbers are each package's **own** tests only, exactly
as `go test -cover ./...` and the CI profile report them; cross-package usage (for
example `cmd/server` tests calling `magento.RegisterWithResolver`) is not credited to the
callee.

### 3.1 Per-package coverage

| Package | No database (default path) | With PostgreSQL 16 | Risk | Note |
|---|---:|---:|---|---|
| `internal/storage` | **3.4 %** | **63.6 %** (61.6 % in CI `-race` profile) | **Critical** | Durability, job queue, confirmations, audit. 11/13 tests skip by default. |
| `cmd/worker` | **15.5 %** | 15.5 % | **High** | Worker wiring, prune loop; DB test is 1 of 3. |
| `cmd/server` | **37.4 %** | 37.4 % | **High** | `main`, `run`, `authorizeFunc` are 0 %. |
| `cmd/admin` | **0.0 %** (no tests) | 0.0 % | **High** | Human approval CLI, no test. |
| `internal/mcp/toolerr` | 66.7 % | 66.7 % | Medium | Envelope formatting. |
| `internal/mcp/registry` | 70.8 % | 70.8 % | Medium | `SetInputValidator` 0 %. |
| `internal/security/confirm` | 71.1 % | 71.1 % | **High** | Confirmation protocol; below its 90 % target. |
| `internal/magento` | 74.7–74.8 % | 74.8 % | **High** | All write paths; 237 tests but no concurrency/failover/money-ambiguity cases. |
| `internal/core/config` | 81.4 % | 81.4 % | Medium | |
| `internal/jobs` | 81.7 % | 81.7 % | **High** | Worker loop; `Extend` never called by the worker (REL-03). |
| `internal/core/errors` | 82.5 % | 82.5 % | Low | |
| `internal/unified` | 83.3 % | 83.3 % | Medium | |
| `internal/cloudflare` | 83.5 % | 83.5 % | Low | |
| `internal/ga4` | 86.8 % | 86.8 % | Low | |
| `internal/mcp/server` | 88.1 % | 88.1 % | **High** | Authorization/audit/confirm call path. |
| `internal/security/audit` | 89.7 % | 89.7 % | **High** | Just below its 90 % target. |
| `internal/security/jwt` | 89.9 % | 89.9 % | **High** | |
| `internal/security/auth` | 97.9 % | 97.9 % | **High** | |
| `internal/observability` | 100.0 % | 100.0 % | Low | |
| `internal/security/rbac` | 100.0 % | 100.0 % | **High** | 100 % of a policy that is not consulted for scope (ARCH-01). |
| `internal/core/datasource` | 0.0 % (no tests) | 0.0 % | n/a | Interface declarations only. |
| `test/e2e/client`, `test/e2e/fakemagento` | 0.0 % (no tests) | 0.0 % | Low | Harness programs. |
| **Repository total** | **71.7 %** | **74.3 %** | | CI floor is 70 %. |

### 3.2 Lowest coverage × highest risk

The ranking that matters for production readiness, not the raw percentage:

1. `internal/storage` — 3.4 % by default. Every durable guarantee (job lease, idempotent
   enqueue, confirmation single-use, audit persistence) lives here.
2. `cmd/worker` — 15.5 %. The process that actually executes mutations and can be killed
   mid-task is almost untested.
3. `cmd/server` — 37.4 %; `authorizeFunc` (`cmd/server/main.go:227`) is 0 %, so the
   production authorization decision has no test at all.
4. `cmd/admin` — 0 %, no test file. It is the human approval step of the write protocol.
5. `internal/security/confirm` (71.1 %) and `internal/mcp/server` (88.1 %) — the protocol
   is tested sequentially only; concurrent single-use is untested (§4, scenario 12).

### 3.3 Zero-coverage functions in the risky packages

`go tool cover -func` at the audited commit:

```text
internal/storage/confirm.go:51:        Get            0.0%
internal/storage/confirm.go:158:    ListPending        0.0%
internal/storage/jobqueue.go:167:    Fail            0.0%
internal/storage/jobqueue.go:187:    Extend            0.0%
internal/storage/jobqueue.go:198:    Cancel            0.0%
cmd/server/main.go:41:    main            0.0%
cmd/server/main.go:48:    run            0.0%
cmd/server/main.go:227:    authorizeFunc        0.0%
cmd/server/main.go:368:    Traffic            0.0%
cmd/server/main.go:384:    Sessions        0.0%
cmd/server/main.go:407:    runHealthcheck        0.0%
cmd/worker/main.go:33:    main            0.0%
cmd/worker/main.go:40:    run            0.0%
internal/security/confirm/confirm.go:254:    Get        0.0%
internal/mcp/registry/registry.go:110:    SetInputValidator    0.0%
internal/unified/unified.go:112:    Bindings    0.0%
```

`JobQueue.Fail`, `Extend`, and `Cancel` are 0 % — the retry, lease-renewal, and
cancellation paths have no direct test. The worker never calls `Extend`
(`internal/jobs/jobs.go:170-192`), which is REL-03; nothing tests that a long task is not
re-executed.

### 3.4 Coverage targets vs measured

`docs/development/testing-strategy.md:98-104` sets per-scope targets. Measured:

| Scope | Target | Measured | Verdict |
|---|---:|---:|---|
| `internal/security` (auth, RBAC, confirmation, audit) | 90 % | auth 97.9, rbac 100, audit 89.7, **confirm 71.1** | **Miss** (confirm, audit) |
| `internal/core`, `internal/mcp` | 80 % | config 81.4, errors 82.5, server 88.1, **registry 70.8**, **toolerr 66.7** | **Miss** (registry, toolerr) |
| Overall repository | 70 % | 71.7 % (no DB) / 74.3 % (DB) | Pass |
| Fuzz, sandbox, real, load suites | described | 0 fuzz targets, 0 build tags, `tests/` empty | **Absent** |

The 70 % overall floor is met only because `internal/storage` is small in statement count;
the security-confirmation package that guards every write is at 71.1 %.

---

## 4. Blind spots

### 4.1 The twelve required scenarios

| # | Scenario | Verdict | Evidence in the current tree | What is missing |
|---:|---|---|---|---|
| 1 | Two users with different permissions operating concurrently | **NONE** | `internal/security/rbac/rbac_test.go` is sequential unit testing; `internal/mcp/server/server_test.go:155,367` uses a single injected identity; `cmd/server/main.go:227` (`authorizeFunc`) is 0 % covered | No concurrent multi-identity test; no test that a `viewer` is denied a write tool (ARCH-01 requires exactly this test) |
| 2 | Two different Magento instances | **PARTIAL** | `internal/magento/resolver_test.go` (routing, empty-instance error, instance list); `cmd/server/main_test.go:164,182` (registration with two instances) | No test with two distinct fake endpoints/credentials asserting requests never cross; no concurrent multi-instance call; no per-instance authorization on a write |
| 3 | The same idempotent request submitted twice | **PARTIAL** | `internal/storage/storage_test.go:193` (`TestJobQueueIdempotentEnqueue`, DB-gated, skipped by default); `internal/magento/import_jobs_test.go:72` asserts only that the key reaches the queue | The tool-level dedupe (same key → same job id, one execution) is never exercised against the real queue; the fake queue ignores idempotency; `idempotency_keys` table is unused (ARCH-04/REL-13) |
| 4 | Network drop mid-request | **NONE** | `internal/magento/client_test.go:306` covers a slow server (timeout) only; no `CloseClientConnections`, no hijack, no truncated-body case | No test for connection reset after headers, truncated body, or a response lost after the upstream applied the write |
| 5 | Magento API returning 500 | **PARTIAL** | `internal/magento/client_test.go:258` maps 500/502/503/429 for a read; `internal/magento/configurable_test.go` covers a mid-sequence failure; `internal/magento/product_write_test.go:214` covers inventory failure after product create | No 500 on a money path (capture/refund/order create); no test that a 500 on a non-idempotent write is not blindly retried (MAG-18) |
| 6 | PostgreSQL unavailable mid-operation | **NONE** | Only nil-store guards (`internal/storage/import_batches.go:18` and siblings) | No test that terminates a backend, closes the pool mid-call, or asserts the typed error and readiness behaviour |
| 7 | Worker forced exit | **NONE** | `TestJobQueueRecoversExpiredLease` simulates a dead worker with a SQL `UPDATE` (DB-gated, skipped by default); `internal/jobs/jobs_test.go` only cancels the context | No process kill, no mid-handler `SIGKILL`, no assertion that the in-flight task is re-executed exactly once and that its partial upstream effect is accounted for |
| 8 | Two workers claiming the same job | **NONE** | `Claim` uses `FOR UPDATE SKIP LOCKED` (`internal/storage/jobqueue.go:108-152`), but no test calls `Claim` concurrently | No two-worker race test; no exactly-once accounting |
| 9 | Product batch create partial failure | **PARTIAL** | `internal/magento/import_test.go:146` asserts created/updated/failed counts for an import batch; `internal/magento/product_write_test.go:214` asserts partial success when inventory fails after create | No bulk *create* tool exists (only `bulk_update`); the per-row failure payload is not asserted; no resume-after-partial test at tool level |
| 10 | Inventory transfer partial failure | **NONE** | `TestTransferSourceQtyInsufficient` refuses before writing; `TestAssignSourceItemsErrorMapping` maps one status for the whole request | No multi-item partial-write case, no compensation assertion, no concurrent lost-update test (MAG-10/MAG-11) |
| 11 | Payment capture timeout | **NONE** | `internal/magento/client_test.go:306` gives a generic timeout mapping; `TestCaptureInvoice` asserts only method/path | No capture-specific ambiguous-outcome test, no second-attempt protection (MAG-01/MAG-02) |
| 12 | Concurrent use of one approval token | **NONE** | `internal/security/confirm/confirm_test.go:51` and `internal/storage/storage_test.go:87` are sequential; the SQL `UPDATE … WHERE state='approved'` (`internal/storage/confirm.go:115`) is atomic in principle but untested under concurrency | No N-goroutine consume test asserting exactly one success; no test that an approval is bound to its tool (ARCH-08) |

### 4.2 Additional gaps found while measuring

| Gap | Evidence | Consequence |
|---|---|---|
| Readiness never reflects a broken dependency | `/readyz` returns 200 unconditionally (`internal/mcp/server/server.go:251-255`); the only readiness test (`internal/mcp/server/server_test.go:132`) asserts 200 | A dead PostgreSQL is invisible to the orchestrator; no test can fail readiness |
| Production authorization is untested | `cmd/server/main.go:227` `authorizeFunc` 0 %; no test builds the production policy with a write tool | The P0 ARCH-01 defect could regress unnoticed |
| Job handler panic is not recovered | `internal/jobs/jobs.go:170-192` has no `recover()` | A panicking handler kills the worker process; no test |
| Non-retryable failures are retried | `internal/jobs/jobs.go:186`: `retry := task.Attempts < task.MaxAttempts` | ARCH-02; no test for a `validation_failed` handler with attempts remaining |
| Storage suite is not repeatable | `go test -count=2 ./internal/storage/...` fails on a virgin DB (verbatim below) | It cannot gate regressions; REL-15, independently reproduced here |
| `make e2e` cannot run unmodified here | `docker pull postgres:17-alpine` → `Bad Gateway` | The only full-stack test is not runnable without a registry (§5) |
| No load, fuzz, sandbox, or real-environment suites | 0 fuzz, 0 benchmarks, `tests/` empty (not in git) | The testing strategy describes suites that do not exist (QA-08) |

Verbatim non-repeatability evidence (`-count=2` runs the suite twice in one command):

```text
$ go test -count=2 ./internal/storage/...     # virgin database
--- FAIL: TestImportBatchLifecycle (0.01s)
    storage_test.go:248: stored id = "3902b4c6-1f34-4bf8-bde3-06307f78cf56"
--- FAIL: TestListAndGetImportBatch (0.01s)
    storage_test.go:270: list = [{ID:85e47637-773a-4f67-96e7-9a9e4b484749 InstanceID:store-list-test JobID: Format:csv State:partial Total:1 Processed:1 Created:0 Updated:0 Skipped:0 Failed:1 ErrorCode: Failures:[]} {ID:d33ab312-a5b8-4198-b6f6-fc60f74915ad InstanceID:store-list-test JobID: Format:csv State:partial Total:1 Processed:1 Created:0 Updated:0 Skipped:0 Failed:1 ErrorCode: Failures:[]}]
FAIL
FAIL    github.com/shuaiZend/mage2mcp/internal/storage    0.692s
[exit 1]
```

Root cause: `storage_test.go:244` selects `id FROM import_batches WHERE instance_id = 'store-us'`
with no `ORDER BY`/`LIMIT`, and `storage_test.go:270` asserts `len(list) == 1` for
`store-list-test`; both see rows left by the previous iteration. The CI job
(`.github/workflows/ci.yml:113`) runs the suite once against a fresh service container,
so the defect is invisible there.

---

## 5. Integration environment reality

### 5.1 What is actually available and was verified

| Capability | Status | Evidence |
|---|---|---|
| PostgreSQL 16.15 | **Available** | `SELECT version()` → `PostgreSQL 16.15 on aarch64-unknown-linux-musl`; dedicated DB `mage2mcp_qa` on `127.0.0.1:55440`, user `mage2mcp`, `CREATEDB` confirmed |
| Second PostgreSQL (shared) | Available but not used | `postgres16` container on `:5432` (user `artex`); reserved for the QA database only to avoid colliding with other projects |
| Docker daemon | **Available** | `docker version` server 29.8.2 |
| Docker Hub pull | **Blocked** | `docker pull postgres:17-alpine` → `failed to resolve reference "docker.io/library/postgres:17-alpine": … Bad Gateway` |
| Local `postgres:16-alpine` image | Present | `docker images` |
| Go toolchains offline | Available | module cache contains 1.25.0, 1.26.0, 1.26.3, 1.26.11, 1.27.4 |
| golangci-lint v2.15.0 offline | Available with `GOTOOLCHAIN=go1.27.4` | `0 issues.` |
| Real Magento / Adobe Commerce | **Not available** | no instance, no credentials, no reachable store |
| Cloudflare / GA4 live APIs | **Not available** | no credentials; not needed for this audit |

### 5.2 What can and cannot be run

**Can be run here, and was:**

- the full default suite (`go test`, `-race`, `-count=1`) — pass;
- the storage/worker durability suite against real PostgreSQL 16 — pass on a virgin DB,
  fail on a second run (QA-03);
- the CI coverage-floor replica with and without the database — 71.7 % / 74.3 %;
- `golangci-lint` (with `GOTOOLCHAIN=go1.27.4`) — 0 issues;
- the documentation contract check — 75 tools, 22 ADRs;
- the end-to-end smoke (`test/e2e/run.sh`) with **one environment patch**, see §5.3.

**Cannot be run here:**

- `make e2e` / `test/e2e/run.sh` **unmodified** — it provisions `postgres:17-alpine` via
  `docker compose`, and the image cannot be pulled (no registry network);
- any real-Magento test (`INTEGRATION` label) — no instance exists;
- sandbox tests (`tests/sandbox`) — the suite does not exist and there is no account;
- load tests (`tests/load`) — the suite does not exist;
- fuzz targets — none exist.

### 5.3 End-to-end smoke, adapted for this environment (labelled E2E, not INTEGRATION)

To avoid claiming an unexecutable test, the harness was run with the **only** change
needed for this machine: the `docker compose` PostgreSQL provisioning block was replaced
by the already-running PostgreSQL 16 instance (`mage2mcp_e2e` database), and the compose
teardown line was dropped. Everything else — binaries, fake Magento, confirmation flow,
worker, assertions — is the repository's own `test/e2e/run.sh`. The patch is recorded in
the run log and is not a repository change.

Result: **PASS**, 45 lines of output, exit 0:

```text
== build ==
== fake magento ==
== server ==
== worker ==
== tools/list ==
== product create through confirmation ==
approve(a88bd828-12ac-4e48-a04a-3df026524d6b): approved a88bd828-12ac-4e48-a04a-3df026524d6b by e2e-reviewer
CREATED {"product":{"id":1,"sku":"E2E-1", ...}}
== invalid write rejected before confirmation ==
INVALID WRITE CODE invalid_argument
== confirmation binds arguments ==
CONFIRMATION 6d9ffceb-a0b3-494e-b85d-597bdc48d8bf
approve(6d9ffceb-a0b3-494e-b85d-597bdc48d8bf): approved 6d9ffceb-a0b3-494e-b85d-597bdc48d8bf by e2e-reviewer
MISMATCH RETRY CODE confirmation_param_mismatch
== SEO audit ==
== import validation warnings ==
== asynchronous import ==
== configurable product ==
CREATED CONFIGURABLE {"parent":{...},"children":[{...}],"parent_in_stock":true}
== order creation ==
ORDER 99
== bulk product update ==
BULK updated=2 failed=0
== failing import and resume ==
JOB succeeded result={"total":2,"created":1,...,"failed":1,...}
RESUMED 14444d9f-cba0-44ca-9c7d-4b3c23258b6f rows=1
== import batch queries ==
BATCHES 3
BATCH 4497269e-e54a-434c-a8c0-dfd4ea035822 failed=1
BATCH_FAILURES 1
E2E PASS
```

What this proves: the server, worker, PostgreSQL, confirmation protocol, import job
execution, resume, and batch persistence work together against a **fake** Magento.
What it does not prove: any real Magento behaviour, any concurrency, any failure
injection, or any money path (the fake never rejects a capture).

Honest label: **E2E with a fake Magento**. It does not satisfy the README's `INTEGRATION`
definition and must not be reported as such.

---

## 6. Independent verification plan

> **Superseded for the as-built suite by §8.** This section is the plan written before
the remediation landed; the file names and expected states below are historical.

The suite below is what QA will build in the later verification task. It is independent
of the developers' tests: it drives the real binaries/packages through their public
surfaces, uses the real PostgreSQL instance, and is designed so that **a skip is a
failure**. Each file states what it proves and whether it is expected to be red today
(a regression test for an open finding) or green (a guard against regression).

Location: `test/qa/` (new package, same module, imports `internal/...` legally). The
files carry a `//go:build qa` tag so the default `go test ./...` neither compiles nor
silently skips them; the QA runner always passes `-tags qa`. This is deliberate: QA-01
is exactly the failure mode where a skipped suite is counted as green, and the new suite
must not repeat it.

### 6.1 Files, what each proves, expected state today

| File | Proves | Expected today |
|---|---|---|
| `test/qa/harness_test.go` | Shared harness: opens PostgreSQL from `MAGE2MCP_TEST_DATABASE_URL`, migrates, truncates, builds a real `server.Handler` with the production `authorizeFunc` policy, starts fake Magento/Cloudflare/GA4 `httptest` servers, and provides two identities (`viewer`, `operator`). Fails (not skips) when the variable is missing. | green (harness) |
| `test/qa/authz_concurrency_test.go` | Scenario 1: two identities with different roles call concurrently through the HTTP MCP surface; `viewer` is denied `magento.invoice.capture` / `magento.product.create`, `operator` is allowed `magento.product.get`; audit rows carry the right subject; race-clean. | **red** — closes ARCH-01 |
| `test/qa/instance_isolation_test.go` | Scenario 2: two Magento instances with distinct hosts and credentials; concurrent calls for both; each fake sees only its own credential and store code; per-instance authorization denies the instance outside the grant. | **red** (isolation is untested today; may pass, must be proven) |
| `test/qa/idempotency_test.go` | Scenario 3: the same `magento.import.start` payload with the same `idempotency_key` twice returns the same job id and executes once; a different key creates a second job; `idempotency_keys` is consulted. | **red** — ARCH-04 / REL-13 |
| `test/qa/network_faults_test.go` | Scenario 4/5: connection reset mid-body, truncated body, HTML error page, 500 on read and on a write; typed errors, no panic, no upstream-body leak, and no automatic retry of a non-idempotent write. | partly green (read), **red** (write 5xx retryability, MAG-18) |
| `test/qa/postgres_outage_test.go` | Scenario 6: `pg_terminate_backend` / pool close mid-operation; typed error not panic; `/readyz` turns non-200 while the database is down. | **red** — readiness is unconditional |
| `test/qa/worker_failover_test.go` | Scenarios 7/8: two workers on one queue execute a job exactly once; a worker killed mid-handler is recovered by lease expiry and the job is retried (or marked unknown per REL-02); a task shorter than the lease is never double-executed. | **red** — REL-01/03/08 |
| `test/qa/confirm_concurrency_test.go` | Scenario 12: 32 goroutines consume one approved token → exactly one success, 31 `confirmation_used`; approval is bound to its tool (another tool cannot use it); self-approval and changed arguments rejected. | partly green, **red** (ARCH-08) |
| `test/qa/partial_failure_test.go` | Scenarios 9/10: import batch partial failure persists per-row outcomes and reports `partial`; configurable create failure mid-sequence does not report success and leaves no orphan claim; a mid-batch `AssignSourceItems` failure is visible; concurrent `TransferSourceQty` does not lose an update. | **red** — MAG-05/07/10/11 |
| `test/qa/money_ambiguity_test.go` | Scenario 11: capture times out after the fake applied it; the second attempt must not capture twice; order create after an ambiguous outcome must not duplicate. | **red** — MAG-01/02/04, REL-02 |
| `test/qa/storage_repeatable_test.go` | QA-03: the durability flows run twice in the same database and produce the same result; asserts the storage suite is repeatable. | **red** — REL-15 |
| `scripts/qa-independent.sh` | Runs the suite with `-race -count=1 -v`, requires `MAGE2MCP_TEST_DATABASE_URL`, and **fails on any `--- SKIP`**. Prints the skip count. | n/a |

### 6.2 Exact commands

```bash
# one-time database (already provisioned for this audit)
export MAGE2MCP_TEST_DATABASE_URL='postgres://mage2mcp:mage2mcp-dev@127.0.0.1:55440/mage2mcp_qa?sslmode=disable'

# full independent suite (fails on skip, fails on race, no cache)
./scripts/qa-independent.sh

# equivalent direct command
go test -tags qa -race -count=1 -v ./test/qa/...

# one file / one scenario
go test -tags qa -race -count=1 -v -run 'TestAuthzConcurrency|TestConfirmConcurrency' ./test/qa/...

# default path must stay green and must stay database-free
go test -count=1 ./...

# regression guard for the repeatability defect
go test -count=2 ./internal/storage/...

# coverage with the CI toolchain (pinned 1.26.0 lacks covdata)
GOTOOLCHAIN=go1.27.4 go test -race -count=1 -covermode=atomic -coverprofile=coverage.out $(go list ./... | grep -v '/test/e2e/')
```

### 6.3 Gates the plan adds

1. **No-skip gate.** `qa-independent.sh` parses `--- SKIP` and exits non-zero. This is the
   direct answer to QA-01: a skipped durability test must never be counted as green.
2. **Repeatability gate.** `-count=2` on `internal/storage` (and on the QA durability
   flows) must pass. This is the direct answer to QA-03/REL-15.
3. **Concurrency gate.** Every concurrency test runs under `-race`; a `DATA RACE` fails
   the gate.
4. **Readiness gate.** `/readyz` must be non-200 while PostgreSQL is down; the test owns
   the database lifecycle.
5. **Expected-red quarantine.** The tests listed as red above encode open findings. Until
   the corresponding fix lands they run in `test/qa/expected_failures.txt` (a checked-in
   list consumed by the runner), so the suite is honest: red tests are reported as known
   failures, never skipped and never deleted.

### 6.4 Limits of the plan

- It still cannot test a real Magento. Every Magento assertion is `CONTRACT` against a
  fake whose behaviour is written by the same repository (MAG-23). A `sandbox` run with a
  real integration token remains the only way to validate vendor behaviour, and it is
  outside what this environment can do.
- It cannot test Magento-side idempotency or MSI salable quantity; those need the real
  API.
- Load and fuzz suites remain unimplemented; they are listed as future work, not claimed.

---

## 7. QA findings

Format per [`README.md`](README.md). IDs are `QA-NN`; the captain merges and de-duplicates
against `REL-*`, `ARCH-*`, and `MAG-*`.

### QA-01: The documented pre-push gate never executes the durability layer

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `scripts/local-ci.sh:22`, `.github/workflows/ci.yml:58-61`, `internal/storage/storage_test.go:19-22`, `cmd/worker/main_test.go:37-40`
- **Evidence**: `scripts/local-ci.sh` runs `go test -race -count=1 "${packages[@]}"` with no
  `MAGE2MCP_TEST_DATABASE_URL`. Every durability test calls `t.Skip` when the variable is
  unset. Measured: 11 of 13 `internal/storage` tests and 1 of 3 `cmd/worker` tests skip, and
  the command still prints `ok` and `local CI passed`. The CI `verify` job has the same
  gap; only the optional `integration` job (`.github/workflows/ci.yml:110-113`) sets the
  variable, and only for `./internal/storage/...` — the `cmd/worker` DB test is never run
  by any CI job.
- **Trigger**: A contributor runs the documented pre-push gate, or a maintainer dispatches
  CI without the `integration` job.
- **Impact**: A regression in job leasing, idempotent enqueue, confirmation single-use, or
  audit persistence passes the gate. The green baseline overstates the evidence.
- **Proposed fix**: Make `scripts/local-ci.sh` require a database (start one, or fail with
  a clear message when `MAGE2MCP_TEST_DATABASE_URL` is unset), run
  `./internal/storage/... ./cmd/worker/...` with it, and fail on any `--- SKIP`. Include
  `cmd/worker` in the CI integration job.
- **Test requirement**: a runner that greps `--- SKIP` from `-v` output and exits non-zero.
- **Verified by**: QA (independent), reproduced on the pristine export.

### QA-02: `go test -cover ./...` and `make cover` fail on the pinned Go toolchain

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `go.mod:3` (`go 1.26.0`), `Makefile:22-24` (`cover`), toolchain distribution
- **Evidence**: on `go1.26.0` the command exits 1 with
  `# github.com/shuaiZend/mage2mcp/cmd/admin` / `go: no such tool "covdata"` for
  `cmd/admin`, `test/e2e/client`, and `test/e2e/fakemagento`. The toolchain directory
  contains no `covdata` binary. The same command with `GOTOOLCHAIN=go1.27.4` exits 0.
- **Trigger**: any contributor using the pinned toolchain; `make cover`.
- **Impact**: the documented coverage command is red although all tests pass; coverage
  cannot be produced on the default toolchain.
- **Proposed fix**: align the local toolchain with CI (`go 1.27.x` in `go.mod` or a
  `toolchain` directive), or make `make cover` use `GOTOOLCHAIN=go1.27.4`; document the
  requirement.
- **Test requirement**: a CI step that runs `make cover` on the pinned toolchain.
- **Verified by**: QA.

### QA-03: The storage integration suite is not repeatable

- **Severity**: P2
- **State**: CONFIRMED (independent reproduction of REL-15)
- **Location**: `internal/storage/storage_test.go:244-249`, `internal/storage/storage_test.go:265-271`
- **Evidence**: on a virgin database the suite passes; a second run fails:
  `TestImportBatchLifecycle` reads `stored id = "<a different uuid>"` because the query has
  no `ORDER BY`/`LIMIT`, and `TestListAndGetImportBatch` gets two rows for
  `store-list-test`. `go test -count=2 ./internal/storage/...` fails in one command
  (verbatim in §4.2). The CI job runs it once against a fresh container
  (`.github/workflows/ci.yml:113`), so it is invisible there.
- **Trigger**: any second execution against the same database (developer loop, rerun,
  parallel agent, staging verification).
- **Impact**: the durability suite cannot gate a regression; a green run says nothing
  about the previous run's data.
- **Proposed fix**: scope every assertion to rows created by the test (unique instance id
  per run, `ORDER BY created_at DESC LIMIT 1`, or truncate at start). Do not weaken the
  assertions.
- **Test requirement**: `go test -count=2 ./internal/storage/...` in the required gate.
- **Verified by**: QA (pristine export, PostgreSQL 16.15).

### QA-04: The human approval CLI has no test

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `cmd/admin/main.go` (91 lines, `pending` and `approve` commands)
- **Evidence**: `cmd/admin` has no `_test.go`; coverage 0.0 %; `go test ./...` prints
  `[no test files]`. The only execution is the optional e2e smoke
  (`test/e2e/run.sh` `approve()`), which is not a required gate.
- **Trigger**: a change to argument parsing, config loading, or the approve path.
- **Impact**: the step that authorizes every write is untested; a broken `approve` is only
  detected by a manual release-time smoke.
- **Proposed fix**: add `cmd/admin/main_test.go` covering: missing config, unknown
  command, approve of a pending token, approve of an expired/consumed token, self-approval
  rejection, and exit codes.
- **Test requirement**: the above, run against the real PostgreSQL store.
- **Verified by**: QA.

### QA-05: The production authorization decision has no test

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `cmd/server/main.go:227-238` (`authorizeFunc`), `cmd/server/main.go:101-104`
  (shipped grants), `internal/mcp/server/server.go:95`
- **Evidence**: `authorizeFunc` is 0.0 % covered. The only authorization tests inject a
  stub `Authorize` (`internal/mcp/server/server_test.go:155`) or call `rbac.Authorize`
  directly (`internal/security/rbac/rbac_test.go`). No test builds the production policy
  and a real tool call together, and no test asserts that a `viewer` is denied a write
  tool. The shipped policy grants `{Role:"viewer", Tool:"*", Scope:read}` and
  `authorizeFunc` always passes `rbac.ScopeRead` (ARCH-01).
- **Trigger**: any refactor of the wiring; and today, any authenticated viewer calling a
  write tool.
- **Impact**: the P0 authorization defect can regress unnoticed, and a fix cannot be
  proven by the current suite.
- **Proposed fix**: add the end-to-end authorization test required by ARCH-01 (viewer
  denied `magento.invoice.capture`, allowed `magento.product.get`; operator allowed the
  write), driven through the HTTP surface with the production wiring.
- **Test requirement**: `test/qa/authz_concurrency_test.go` (planned §6).
- **Verified by**: QA.

### QA-06: Readiness is unconditional and the test asserts that

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/mcp/server/server.go:251-255`, `internal/mcp/server/server_test.go:132-150`
- **Evidence**: `/readyz` always writes 200 with a tool count; it consults no dependency.
  `TestReadyzReportsWithoutBusinessTools` asserts 200 for a server with an empty registry.
  There is no test with a database, and none that makes a dependency fail.
- **Trigger**: PostgreSQL down, migrations not applied, or credentials rotated.
- **Impact**: the orchestrator keeps routing traffic to a gateway that cannot persist
  confirmations, audit rows, or jobs. Readiness provides no signal.
- **Proposed fix**: make `/readyz` probe the configured dependencies (database ping, at
  minimum) and return 503 when they fail; keep `/healthz` as liveness.
- **Test requirement**: `test/qa/postgres_outage_test.go` (planned §6): readyz non-200
  while PostgreSQL is down, 200 after recovery.
- **Verified by**: QA.

### QA-07: Coverage targets in the testing strategy are missed and unenforced

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `docs/development/testing-strategy.md:98-104`, `.github/workflows/ci.yml:62-70`
- **Evidence**: the document sets 90 % for `internal/security` and 80 % for
  `internal/core`/`internal/mcp`; measured `internal/security/confirm` 71.1 %,
  `internal/security/audit` 89.7 %, `internal/mcp/registry` 70.8 %,
  `internal/mcp/toolerr` 66.7 %. CI enforces only the 70 % repository total
  (71.7 % without a database, 74.3 % with one).
- **Trigger**: any change that lowers a per-package number; nothing fails.
- **Impact**: the documented per-package quality bar is not a gate; the confirmation
  package that guards every write is 19 points below its target.
- **Proposed fix**: either enforce the per-package targets in CI (a small script over the
  cover profile) or correct the document to the numbers actually enforced.
- **Test requirement**: a CI step that fails when a listed package is below its target.
- **Verified by**: QA.

### QA-08: The testing strategy describes suites that do not exist

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `docs/development/testing-strategy.md:15-27`, `:35-62`, `:98-104`; repository tree
- **Evidence**: the document specifies `tests/sandbox` (build tag `sandbox`), `tests/real`,
  `tests/security`, `tests/load`, `tests/fixtures/<module>/<scenario>`, per-module
  `contract`/`mocktest` packages, and fuzz targets. Measured: 0 build-tagged tests, 0
  fuzz targets, 0 benchmarks, and `tests/` contains no tracked file (the directory is not
  in git). `test/e2e` exists and is the only cross-cutting suite.
- **Trigger**: a reader or auditor trusting the document as a description of the current
  evidence.
- **Impact**: the stated test evidence overstates reality; "nightly sandbox" and "load"
  claims have no implementation.
- **Proposed fix**: mark the sections as planned with a phase/status table, or implement
  the suites; link `regression-test-report.md` §2 as the current inventory.
- **Test requirement**: documentation review; the contract checker
  (`scripts/check-contracts.mjs`) could assert referenced paths exist.
- **Verified by**: QA.

### QA-09: Two storage tests are mislabelled — they exercise a nil store, not a closed one

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/storage/storage_test.go:324-329` (`TestPruneImportBatchesNotOpen`), `internal/storage/storage_test.go:331-348` (`TestImportBatchStoreClosed`)
- **Evidence**: both tests declare `var store *Store` and call methods on the nil pointer.
  They prove the nil-receiver guards (`if s == nil || s.pool == nil`), not behaviour after
  `Close()`. No test opens a store, closes it, and then asserts the error.
- **Trigger**: a change to the nil guards, or a real use-after-close defect.
- **Impact**: the names claim coverage of the closed-store path that does not exist; a
  use-after-close regression would pass.
- **Proposed fix**: rename to `TestImportBatchNilStore` and add a real
  open→`Close()`→operate test against PostgreSQL.
- **Test requirement**: the real close test in the durability suite.
- **Verified by**: QA.

### QA-10: The job queue lifecycle test claims an arbitrary default-queue job

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/storage/storage_test.go:113-134`, `internal/storage/jobqueue.go:108-152`
- **Evidence**: `Claim(ctx, []string{"default"}, time.Minute)` has no task-id predicate, and
  the test asserts `task.ID == id`. It passes only while no other `default`-queue job is
  queued; a leftover job from a previous failed run is claimed instead and the assertion
  fails. The test also does not exercise two concurrent claims.
- **Trigger**: a previous run that left a queued default job (any failure before
  `Complete`), or a second test that enqueues to `default`.
- **Impact**: latent order dependence in the one test that covers claiming; the
  `SKIP LOCKED` guarantee is never exercised concurrently.
- **Proposed fix**: claim by unique queue name per test (or assert on the enqueued id set),
  and add a two-goroutine claim test that asserts exactly one winner.
- **Test requirement**: `test/qa/worker_failover_test.go` (planned §6).
- **Verified by**: QA.

---

---

## 8. Independent QA suite — as built and run (t35)

Status: **built, green, repeatable, skip-gated**. The plan in §6 is now
`test/qa/` (build tag `qa`) plus `scripts/qa-independent.sh`. It is independent of the
authors' tests: it drives the real gateway over its HTTP surface, the real PostgreSQL
store, the real job queue, the real confirmation service and the real operation ledger,
against fake Magento upstreams the reviewer controls. It is excluded from
`go test ./...` by the `qa` build tag, so it cannot slow or destabilise the default gate;
the runner always passes `-tags qa`.

**16 test functions, 18 cases** (two subtests), ~7 s under `-race`. Every test names the
audit finding it guards in its doc comment, so a reader can trace test to defect.

### 8.1 Coverage of the twelve blind spots

| # | Scenario | Test | Finding guarded |
|---:|---|---|---|
| 1 | Two users with different permissions, concurrently | `TestConcurrentViewerCannotWriteWhileOperatorReads` | SEC-01 / ARCH-01 |
| 2 | Two Magento instances | `TestInstanceRoutingNeverCrossesTenants` | SEC-02 |
| 3 | Same idempotent request twice | `TestIdempotentRequestReplaysRecordedOutcome` | REL-02 / ARCH-04 |
| 4 | Network drop mid-request | `TestMoneyPathAmbiguityEndsUnknown/network_drop_mid-request` | REL-02 / MAG-01 / MAG-18 |
| 5 | Magento 500 on a money path | `TestMoneyPathAmbiguityEndsUnknown/magento_500` | REL-02 / MAG-01 / MAG-18 |
| 6 | PostgreSQL unavailable mid-operation | `TestPostgresOutageFailsClosed` | REL-02 / REL-16 |
| 7 | Worker forced exit | `TestForcedWorkerExitRecoversTaskExactlyOnce` | REL-01 / REL-03 / REL-08 / REL-10 |
| 8 | Two workers claiming the same job | `TestTwoWorkersClaimDisjointJobs` | REL-01 / REL-09 |
| 9 | Product batch create partial failure | `TestImportBatchPartialFailureIsReported` | MAG-05 |
| 10 | Inventory transfer partial failure | `TestInventoryTransferPartialFailureIsHonest` | MAG-11 |
| 11 | Payment capture timeout | `TestCaptureTimeoutEndsUnknownAndDoesNotDoubleCapture` | MAG-01 / REL-02 |
| 12 | Concurrent use of one approval token | `TestConcurrentApprovalTokenYieldsOneSuccess` | SEC-03 / ARCH-08 |

Five additional tests cover adjacent properties:

| Test | Guards | What it proves |
|---|---|---|
| `TestUnknownRoleGrantsNothing` | SEC-01 | a role the policy does not know grants nothing; a caller cannot widen access by naming a privileged role |
| `TestAuthorizationIsFailClosedForUnregisteredTools` | SEC-01 | an unregistered tool is denied before the authorizer is consulted |
| `TestApprovalIsBoundToItsToolAndInstance` | SEC-03 / ARCH-08 | an approval for one tool cannot be replayed against another |
| `TestImportJobIsScopedToTheCallerSubject` | SEC-02 | a second caller on the same instance cannot read or cancel another caller's job |
| `TestInventoryTransferRefusesFractionalQuantities` | MAG-12 / MAG-R07 | the documented limitation: a fractional source quantity is refused with a typed error (`unsupported_capability`), never truncated or written |

### 8.2 What the decisive assertions are

- **Scenario 1.** Eight concurrent viewer `product.create` calls all return
  `permission_denied` and cause **zero** upstream POSTs while an operator reads
  concurrently; a final operator write succeeds (one POST), so the denials are
  authorization, not a broken fixture.
- **Scenario 2.** A read, a write and an async import started on instance A leave
  instance B's upstream at **zero requests**; `import.status` / `import.cancel` from B
  return `not_found` for A's job (the cancel path is approved first, so the tenant check —
  not the approval gate — is what rejects it).
- **Scenario 3.** Two separately approved submissions of the same request produce **one**
  upstream POST; the second response is the recorded product and is labelled `replayed`.
- **Scenarios 4/5/11.** The ledger row ends in state `unknown` (not `failed`), the envelope
  is not `retryable`, and a second, separately approved attempt is blocked as
  `operation_unknown` with the capture POST count still 1.
- **Scenario 6.** With the store closed after the approval, the write returns an error and
  the upstream is never mutated.
- **Scenario 7.** A claim abandoned with a 200 ms lease is recovered by a worker after the
  lease lapses: the handler runs **exactly once**, `attempts == 2`, the stale token's
  `Complete` returns `jobs.ErrLeaseLost`, and a succeeded task is not re-executed.
- **Scenario 8.** Twenty tasks claimed by two concurrent workers: every task runs exactly
  once, no task is claimed twice, no unknown task id appears.
- **Scenario 9.** A two-row batch where one row is rejected returns `created=1`, `failed=1`,
  and the failing row named per SKU; two upstream creates, no rollback.
- **Scenario 10.** A batch that lands only the `from` row returns `conflict` with
  `applied_rows=from`, a `transfer_id`, and exactly one write (no compensation, no retry).
- **Scenario 12.** Eight concurrent callers with one approval: exactly one success, seven
  `confirmation_used`, one upstream POST.

### 8.3 Exact commands and verbatim results

```bash
go build ./...
MAGE2MCP_TEST_DATABASE_URL='postgres://mage2mcp:mage2mcp-dev@127.0.0.1:55440/mage2mcp_qa?sslmode=disable' \
  go test -count=1 -race -tags qa ./test/qa/...
MAGE2MCP_TEST_DATABASE_URL='postgres://mage2mcp:mage2mcp-dev@127.0.0.1:55440/mage2mcp_qa?sslmode=disable' \
  ./scripts/qa-independent.sh
```

(Tab characters in the quoted `go test` output are rendered as four spaces so the report
passes the repository's markdownlint gate; only whitespace differs. The suite was run
against the working tree while other remediation tasks were still landing; it touches only
public surfaces, so their in-flight edits do not change what it asserts.)

```text
$ go build ./...
[exit 0]

$ MAGE2MCP_TEST_DATABASE_URL=... go test -count=1 -race -tags qa ./test/qa/...
ok      github.com/shuaiZend/mage2mcp/test/qa    7.479s
[exit 0]

$ MAGE2MCP_TEST_DATABASE_URL=... ./scripts/qa-independent.sh
...
PASS
ok      github.com/shuaiZend/mage2mcp/test/qa    7.109s

qa-independent: PASS - 18 test(s) passed, 0 skipped, 0 failed
[exit 0]
```

Repeatability (the suite uses a fresh instance id per test and drains its dedicated queue
first, so a second run cannot be satisfied by the first run's rows):

```text
$ MAGE2MCP_TEST_DATABASE_URL=... go test -count=2 -race -tags qa ./test/qa/...
ok      github.com/shuaiZend/mage2mcp/test/qa    12.850s
[exit 0]
```

The existing repository suite still passes and does not compile the `qa` package:

```text
$ go test -count=1 ./...
?       github.com/shuaiZend/mage2mcp/cmd/admin    [no test files]
ok      github.com/shuaiZend/mage2mcp/cmd/server    0.196s
ok      github.com/shuaiZend/mage2mcp/cmd/worker    0.146s
ok      github.com/shuaiZend/mage2mcp/internal/jobs    0.903s
ok      github.com/shuaiZend/mage2mcp/internal/magento    2.231s
ok      github.com/shuaiZend/mage2mcp/internal/mcp/server    0.192s
ok      github.com/shuaiZend/mage2mcp/internal/security/confirm    0.065s
ok      github.com/shuaiZend/mage2mcp/internal/storage    0.097s
... (all packages ok)
[exit 0]
```

### 8.4 The skip gate is real (proved, not asserted)

A temporary probe test containing only `t.Skip("skip-gate probe")` was added, the runner
was executed, and the probe was then deleted. `go test` itself reported `PASS`; the runner
failed anyway:

```text
$ MAGE2MCP_TEST_DATABASE_URL=... ./scripts/qa-independent.sh
PASS
ok      github.com/shuaiZend/mage2mcp/test/qa    6.791s

qa-independent: FAIL - the suite skipped at least one test:
--- SKIP: TestSkipGateProbe (0.00s)
[exit 1]
```

The suite contains **no `t.Skip`**: a database-less run exits non-zero from `TestMain`
with a message instead of skipping, and the runner requires `MAGE2MCP_TEST_DATABASE_URL`
to be set.

### 8.5 What remains a blind spot

- **No real Magento.** Every upstream is an `httptest` fake. The `INTEGRATION` label is
  still unavailable (MAG-23): the suite proves gateway behaviour, not Magento's. Sandbox
  and storefront verification remain manual.
- **One gateway process.** Concurrent HTTP clients hit a single process. The RBAC,
  confirmation and ledger races are real, but multi-replica behaviour (the in-process
  transfer guard not surviving replicas, ledger row contention across processes) is not
  reproduced.
- **Worker forced exit is simulated** by abandoning a claim with a short lease, not by
  `SIGKILL` mid-handler. The recovery path is the same code, but an upstream write in
  flight at the moment of the kill is not covered.
- **PostgreSQL outage is simulated** by closing the pool after the approval, not by
  stopping the server; a failure exactly at commit (ambiguous commit) is not reproduced.
- **Network drop is exercised on the capture money path only**, not on every write tool.
- **No load or latency assertions.** The PERF findings rely on the probes in
  `docs/audit/perf-probe`, not on this suite.
- **The suite leaves rows in the QA database by design** (unique instance ids per run).
  It needs a disposable database and does not clean up after itself.

### 8.6 New defects found

None. The suite is green on the current tree, which independently confirms the remediated
properties it targets: SEC-01 (scope ladder), SEC-02 (tenant scoping), SEC-03 (approval
binding and single use), REL-01/03/08/09/10 (lease fencing and disjoint claims), REL-02/16
(UNKNOWN outcome, ledger state, fail-closed on database loss), MAG-01/18 (no retry of an
ambiguous money mutation), MAG-05 (honest partial batch result), MAG-11 (honest partial
transfer), MAG-12/MAG-R07 (fractional quantity refused). The remaining risks are the
blind spots listed in §8.5, not failures of the current code.

### 8.7 Post-t42 reconciliation: concurrent approval and worker retry (t44)

The suite was re-run after the t42 ledger repair. One test failed and the failure was
diagnosed, not silenced.

**Diagnosis.** `TestConcurrentApprovalTokenYieldsOneSuccess` expected one `ok` and seven
`confirmation_used`. After t42, `BeginCall` decides the ledger transition *before* it
consumes the approval (`internal/storage/operation_ledger.go`): the winner's operation is
created and the approval is burned in one transaction; a concurrent duplicate sees the
operation in progress and is refused with a typed `conflict` **without** consuming
anything; a duplicate that arrives after the winner commits receives the recorded result
as a labelled `replayed` response. The old expectation encoded the pre-t42 order, where the
approval was consumed before the operation row was examined.

**Which contract is right.** The new behaviour is correct, and stricter. There is still
exactly one winner, exactly one upstream mutation and exactly one consumption; the losers
are refused earlier with a more accurate instruction ("poll, do not retry" instead of "the
approval is gone"), and the approval is no longer touched by callers that cannot use it.

**The test is now about the invariant, not the error string.** It asserts:

- exactly one non-replay success (a `replayed` response is a success shape but not a
  second mutation, and is counted separately);
- zero `confirmation_used` among the concurrent callers — a loser that reached the consume
  step would violate the post-t42 ordering;
- `conflicts + replays == callers - 1`;
- **exactly one** upstream `POST /products` (counted, not inferred from response codes);
- the persisted approval row is `consumed` with `consumed_at` set;
- a direct second `Consume` of the same approval returns `confirmation_used`, so the token
  can never be used twice.

While strengthening it, the first version still classified replays as ordinary successes
and failed with `4 non-replay successes with one approval, want exactly 1` — the guard
firing on a real classification error in the test itself. The classification was fixed,
not the assertion.

**Negative controls (the guard is live).**

- Making the replay path re-execute the handler (`case DecisionReplay` falls through to the
  handler) makes `TestIdempotentRequestReplaysRecordedOutcome` fail deterministically:
  `the second submission mutated the upstream (2 POSTs)`.
- Allowing a consumed token to be consumed again (state predicate widened) makes the
  concurrency test fail at the final check:
  `the consumed approval could be consumed again: <nil>`.
- The two-success case is what the first run above caught (`ok=4`); a single-component
  revert cannot produce it because the ledger uniqueness and the confirmation CAS are
  independent guards.

**Worker retry (t42).** t42 stopped auto-retrying an idempotent import that fails with a
5xx (the batch row plus `import.resume` is the recovery path). No test in this suite
encoded the old auto-retry behaviour: the two worker tests use `platform:noop` handlers
that return nil and assert claim/lease invariants; the asynchronous import tests assert
status/cancel/resume/batch tenant scoping; the partial-failure test uses the synchronous
`magento.import.execute`. No change was required.

**Results after the reconciliation (verbatim).**

```text
$ MAGE2MCP_TEST_DATABASE_URL=... go test -count=1 -race -tags qa ./test/qa/...
ok      github.com/shuaiZend/mage2mcp/test/qa    7.340s

$ MAGE2MCP_TEST_DATABASE_URL=... ./scripts/qa-independent.sh
...
qa-independent: PASS - 18 test(s) passed, 0 skipped, 0 failed

$ MAGE2MCP_TEST_DATABASE_URL=... go test -count=1 -tags qa -run TestConcurrentApprovalTokenYieldsOneSuccess -v ./test/qa/
    confirm_concurrency_test.go: results: ok=1 conflict=5 replayed=2 confirmation_used=0
--- PASS: TestConcurrentApprovalTokenYieldsOneSuccess
```

## Appendix A — raw logs

The full command logs from this baseline are preserved at `/tmp/qa-baseline/` for the
duration of the audit session:

| File | Content |
|---|---|
| `test.txt`, `test-count1.txt`, `test-race.txt`, `test-race-count1.txt` | required test runs |
| `cover.txt`, `cover-withdb.txt` | coverage runs without/with PostgreSQL |
| `ci-nodb.out`, `ci-db.out` | CI coverage-floor replicas |
| `storage-nodb-v.txt` | verbatim skip output |
| `e2e-local.log` | adapted end-to-end run |
| `lint.log`, `lint2.log` | lint attempts |
| `storage-clean.cover` | storage function coverage with PostgreSQL |

Reproduction of the baseline from the repository:

```bash
rm -rf /tmp/mage2mcp-clean && mkdir -p /tmp/mage2mcp-clean
git archive 66786a5d10c1f78d00e08a5b42d01fcc2da37a3f | tar -x -C /tmp/mage2mcp-clean
cd /tmp/mage2mcp-clean
gofmt -l ./cmd ./internal ./test && go vet ./... && go test ./... && go test -race ./... && go test -count=1 ./...
export MAGE2MCP_TEST_DATABASE_URL='postgres://mage2mcp:mage2mcp-dev@127.0.0.1:55440/mage2mcp_qa?sslmode=disable'
go test -race -count=1 ./...          # durability layer now executes
go test -count=2 ./internal/storage/...   # fails: QA-03 / REL-15
```

## 9. Post-remediation note: test isolation (TEST-PAR)

During the final gate a flake appeared that the baseline could not have shown,
because the baseline never ran the durability layer at all.

`go test -race ./...` runs packages in parallel. With a database configured,
`internal/storage` and `internal/magento` shared one schema, and
`internal/storage/jobqueue_transitions_test.go` cleans up with a blanket
`DELETE FROM jobs`. That statement deleted rows `internal/magento`'s
`TestImportJobsCrossTenantIsolation` was still using, so the test failed
intermittently with either `owner GetJob: not_found` or `batch across subjects was
allowed`. It passed in isolation and under `-p 1`.

**This was a test-isolation defect, not a product defect** — the cross-tenant
behaviour it guards was correct throughout.

**Fix.** Each database-backed test package now connects with its own `search_path`
(`test_storage`, `test_magento`), created by the test helper before `Migrate`
runs, so one package's cleanup cannot see another package's rows. Verified with
four consecutive `go test -race ./...` runs, zero failures, no `-p 1` workaround.

**Consequence for the commands in this report.** Every database-backed command
above is now valid *without* `-p 1`. The `go test -count=2 ./internal/storage/...`
line in Appendix A records the pre-remediation REL-15 defect and should now pass;
it is retained as the baseline record, not as current guidance.
