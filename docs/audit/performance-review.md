# Performance and scalability review (PERF)

- **Audit**: v0.11.0 production-readiness programme
- **Owner**: Performance engineer (Agent 5)
- **Baseline commit**: `66786a5` (`docs: record CI local-first timings (#149)`)
- **Scope**: `internal/magento/{client,product,product_bulk,catalog,order,sales_analysis,order_geography,import,import_jobs,inventory_read,configurable,product_write,order_documents}.go`,
  `internal/storage/*.go` + `internal/storage/migrations/*.sql`, `cmd/worker/main.go`, `internal/jobs/jobs.go`,
  `internal/mcp/{server,registry,toolerr}`, `cmd/server/main.go`, `internal/core/config/config.go`
- **Method**: code-level derivation + a reproducible probe harness
  (`docs/audit/perf-probe/`) that drives the **real** `internal/magento` client against a fake
  Magento REST server and the **real** storage layer against a disposable PostgreSQL 16 database.
  No live Magento instance was contacted, so every Magento number is `CONTRACT` (wire-shape
  faithful, latency synthetic); every PostgreSQL number is measured on a real server.
- **Code changes**: none to production code. The only files added are the probe harness and this report.

> **Concurrent remediation note.** While this audit ran, other agents were fixing security and
> Magento-integration defects in the same tree (`cmd/server/main.go`, `internal/mcp/server/server.go`,
> `internal/magento/{configurable,media}.go`, `internal/security/rbac/*`). Every line reference below
> is anchored to baseline `66786a5`; in those files the current tree is offset by up to ~30 lines
> while the code shape audited here is unchanged (re-checked: no tuned `http.Transport`, no
> semaphore/limiter and no tool-deadline enforcement was added). One behavioural difference affects
> PERF-16 only: the Magento engineer's `CreateConfigurable` fix adds a source-item read-back
> (`observeParentStock`, `configurable.go:804`), i.e. +⌈children/20⌉ requests, and makes the parent
> stock write conditional on what that read observes. Re-measured against the current tree: 32
> requests for a 10-child create when the read-back finds no stock (33 when children report stock),
> versus 32 at baseline — so the 50-child maximum is 154–155 rather than 152. The conclusion
> (N+1, unbounded by any limiter) is unchanged.

---

## 1. Executive summary

The gateway is correct but **serial and unbounded in the places that matter**. Three properties
dominate everything else:

1. **Every bulk operation is a sequential N+1 over Magento.** There is no batching, no bounded
   concurrency, and no rate limiter anywhere in the tree. A 5,000-row import is 10,000 sequential
   HTTPS round-trips (measured 2 per row); a 50-SKU bulk update is 50; a 25-SKU stock batch is 25;
   a 50-child configurable create is 152.
2. **The worker cannot finish the work it is given.** The default task timeout is 5 minutes
   (`jobs.WorkerOptions.TaskTimeout`, not exposed in config), while a maximal import needs 8m20s at
   50 ms RTT and 13m20s at 80 ms RTT. Worse, the 2-minute lease is **never extended** — `Extend` is
   implemented, declared in the `Queue` interface, and called by nobody — so any task that runs past
   2 minutes is re-queued and **executed a second time concurrently**.
3. **Declared limits are not enforced limits.** `registry.Tool.Timeout` (30 s read / 60 s write /
   120 s report) is validated at registration and then never used; `docs/architecture.md` §9 claims
   tuned per-instance connection pools, per-instance semaphores and token-bucket limiters, none of
   which exist. A paginating tool can therefore run for 40 × 30 s = 20 minutes inside one MCP call.

Two limits deserve separate mention because they are *correctness* limits disguised as scale limits:
`magento.sales.summary` silently stops at **1,000 orders** (a 20,000-order window is 5 % covered),
and `magento.orders.geography` stops at **10,000**. Both report `truncated: true`, so nothing is
silent — except that `sales.summary`'s flag is *sticky* and reports `truncated: true` even when it
scanned every order (measured: 924 of 924 orders scanned, `truncated: true`).

### Severity counts

| Severity | Count | IDs |
|---|---|---|
| **P0** | 0 | — |
| **P1** | 2 | PERF-01, PERF-02 |
| **P2** | 12 | PERF-03 … PERF-14 |
| **P3** | 9 | PERF-15 … PERF-23 |

State of the 23 findings: **22 CONFIRMED**, **1 DOC-ONLY** (PERF-23). Nothing was found
`ALREADY-FIXED`; no prior performance note in the repository was contradicted by the code.

### Remediation status (task t17, Magento client and read/analysis paths)

Seven findings were fixed in one pass after this review was published. Each row
names the guard and the test that now proves it; the finding text below is kept
as written so the original evidence stays auditable.

| Finding | Status | Guard / proof |
|---|---|---|
| PERF-04 (default transport) | **FIXED** | `client.go` `NewTransport()`: `MaxIdleConns=100`, `MaxIdleConnsPerHost=16`, `MaxConnsPerHost=32`, `IdleConnTimeout=90s`, `TLSHandshakeTimeout=10s`; the admin-token client shares it. `TestNewClientInstallsTunedTransport`, `TestClientReusesConnections` — measured **8 connections for 160 requests** (8 workers × 20) where the audit measured 41–64. |
| PERF-07 (summary caps at 1,000) | **PARTIAL** | The cap remains a documented budget, but the response now reports `total_available`, `scanned_orders`, and `excluded_orders` (`TestSalesSummaryReportsExcludedOrders`). Raising the cap is a separate decision (cost: 41 requests at 20k orders). |
| PERF-08 (sticky `truncated`) | **FIXED** | Flag computed after the scan from `total_available - scanned_orders`; a complete 924-order scan now reports `truncated=false, excluded_orders=0` (`TestSalesSummaryTruncationIsNotSticky`). |
| PERF-09 (offset paging) | **FIXED (caveat made machine-readable)** | Deterministic `(created_at, entity_id)` sort, duplicates dropped by `entity_id`, and a `paging` object carrying `method`, `order_by`, `snapshot_consistent=false`, `duplicates_skipped`, `caveat` (`TestAnalyzeSalesPagingIsExplicit`, `TestAnalyzeSalesDropsDuplicateOrders`). A true snapshot needs Magento-side support. |
| PERF-10 (geography cap) | **FIXED** | `unscanned_orders` and `duplicates_skipped` reported; `truncated` is exactly `unscanned_orders > 0` (`TestOrderGeographyTruncation`, `TestOrderGeographyCompleteScanReportsNoRemainder`). |
| PERF-11 (silent 8 MiB truncation) | **FIXED** | The client reads one byte past the cap and returns `*ResponseTooLargeError` (typed cause) plus `limit_bytes` / `observed_bytes` / `overage_bytes` details (`TestResponseTooLargeIsExplicit`, including the exactly-at-cap boundary). |
| PERF-12 (import limit vs body cap) | **FIXED (capability preserved)** | The first fix cut the ceiling to 512 KiB, which was a 16× capability reduction and was reverted (t27). Final state: the documented **8 MiB / 5,000 rows** ceiling is restored and the transport is sized to honour it — `server.max_body_bytes` defaults to 16 MiB (12 MiB floor with `tools.writes: true`), so a maximum import needs **11,188,908 of 16,777,216 bytes**. Schema `maxLength` (11184812), the decoder limit and the body cap are pinned together by `TestImportLimitsAgreeAcrossPackages`; a realistic 5,000-row CSV (1,698,942 bytes → 2,265,413-byte request body) is accepted end to end with HTTP 200 (`TestRealisticImportPayloadPassesBothLimits`), and one byte over the cap is a `413` naming the limit (`TestHTTPBodyCapRejectsOversizeRequest`). |

Known follow-ups left outside that task's file scope: `internal/magento/write_tools.go`
still describes `magento.import.execute` as "at most 8 MiB" with
`maxLength: 11184812`, and `docs/{magento-fields,integrations/magento,adr/0021,releases/v0.1.0,security/threat-model}.md`
plus `CHANGELOG.md` still state the old import ceiling.

### The four concrete scenarios from the audit objective

Assumption for every "wall @ RTT" column: **80 ms** Magento round-trip, requests issued strictly
sequentially (the code has no parallelism), plus measured local processing. Request counts are
measured; only the latency column is derived.

| Scenario | Code path | Requests (measured / derived) | Wall @ 80 ms RTT | Memory | Verdict |
|---|---|---|---|---|---|
| **924 orders**, `magento.sales.summary` | `order.go:176` loop, pageSize 200 | **5** (measured) | 0.4 s | trivial | Works, but reports `truncated: true` (PERF-08) |
| **924 orders**, `magento.sales.analysis` | `sales_analysis.go:230` loop, pageSize 500 + 1 shipments page | **3** (measured) | 0.24 s | 12.1 MB alloc / call (benchmark) | Good — the intended path for this dataset |
| **20,000 orders**, `sales.analysis` (max) | 40 order pages + 1 shipments page | **41** (measured) | 3.3 s | 88 MB total alloc, 48 MB peak heap (measured, synthetic 1.05 KB/order) | Bounded but memory-heavy; real 4–8 KB orders ⇒ ~150–230 MB alloc (derived: 3.4 KB fixed + payload per order) |
| **20,000 orders**, `sales.summary` | `maxSummaryOrders = 1000` | **5** (measured) | 0.4 s | trivial | **Wrong by construction**: scans 1,000 of 20,000 (5 %) |
| **20,000 orders**, `orders.geography` | `maxGeographyOrders = 10000` | **20** (measured) | 1.6 s | trivial | Scans 10,000 of 20,000 (50 %), `truncated: true` |
| **Large SKU import**, 5,000 rows | `import.go:348` — 1 GET + 1 POST/PUT per row | **10,000** (measured 2/row) | **13 m 20 s** | 7.1 MB parse + 8 MiB payload + job row copy | **Cannot complete**: killed at 5 min, partially applied, then retried |
| **Many products**, 624 SKUs via `products.get` | `product.go:280` BatchGet, 50 SKUs/request | **13** (derived) | 1.04 s | 13 × page | Good — the batch endpoint is the right shape |
| 50-child configurable create | `configurable.go:123–152` | **152** (derived from measured 32 for 10) | 12.2 s | — | Sequential; no partial-success budget |
| 50-SKU `inventory.status_batch` | `inventory_read.go:126` | **50** (measured 25 for 25) | 4 s | 354 KB / 25 SKUs (benchmark) | Sequential by necessity, parallelisable |
| 924 shipments, `shipment.list` per order | `order_documents.go:157` requires `order_id` | **924** (derived; docs/development/mcp-gaps.md G4 recorded exactly this) | 74 s | — | Design N+1 for window-wide fulfillment |

---

## 2. How these numbers were produced

Probe harness (audit evidence, not production code):
`docs/audit/perf-probe/probe_test.go` (fake Magento) and
`docs/audit/perf-probe/probe_db_test.go` (disposable PostgreSQL).

```text
# Magento wire-level request counts, pagination, truncation, allocation, connections
go test ./docs/audit/perf-probe/ -run TestPerf -v -count=1

# Micro-benchmarks (per-call cost and allocation)
go test ./docs/audit/perf-probe/ -bench . -benchmem -run '^$' -count=1

# PostgreSQL plans and write costs (disposable database)
createdb -h 127.0.0.1 -p 55440 -U mage2mcp mage2mcp_perf
MAGE2MCP_TEST_DATABASE_URL='postgres://mage2mcp:mage2mcp-dev@127.0.0.1:55440/mage2mcp_perf?sslmode=disable' \
  go test ./docs/audit/perf-probe/ -run TestPerfDB -v -count=1

# Required regression subset
go test ./internal/magento/... ./internal/storage/... -count=1
```

Recorded output (trimmed to the `PROBE` lines; every line below is verbatim):

```text
PROBE import create 10 rows                  total_requests=20 GET /products/{sku} (404)=10 POST /products=10
PROBE import update 10 rows                  total_requests=20 GET /products/{sku}=10 PUT /products/{sku}=10
PROBE stock statuses 25 SKUs                 total_requests=25 GET /stockItems/{sku}=25
PROBE bulk update 50 SKUs                    total_requests=50 PUT /products/{sku}=50
PROBE bulk attribute options 10 labels       total_requests=10 POST /products/attributes/{code}/options=10
PROBE create configurable 10 children        total_requests=32 POST /configurable-products/{sku}/child=10 POST /inventory/source-items=10 POST /products=11 PUT /products/{sku}/stockItems=1
PROBE sales_summary_924                      total_requests=5 GET /orders=5
PROBE summary scanned_orders=924 total_orders=924 truncated=true days=24
PROBE sales_summary_20000                    total_requests=5 GET /orders=5
PROBE summary scanned_orders=1000 total_orders=1000 truncated=true days=26
PROBE analysis_924                           total_requests=3 GET /orders=2 GET /shipments=1
PROBE analysis orders=924 total_available=924 truncated=false top_skus=50
PROBE analysis_20000                         total_requests=41 GET /orders=40 GET /shipments=1
PROBE analysis orders=20000 total_available=20000 truncated=false top_skus=50
PROBE geography_20000_cap10000               total_requests=20 GET /orders=20
PROBE geography orders_scanned=10000 total_available=20000 truncated=true
PROBE response 8387570 bytes -> ok, name_len=8387540
PROBE response 8389618 bytes -> error: upstream_unavailable: Magento returned an unreadable product payload
PROBE conn churn default-transport requests=160 conns=62 elapsed=41ms
PROBE conn churn tuned-transport   requests=160 conns=8 elapsed=28ms
PROBE conn churn handshake_overhead_per_request=33%
PROBE import throughput rows=50 requests=100 created=50 elapsed=250ms per_row=5.01ms
PROBE import extrapolation latency=50ms rows=5000 requests=10000 wall=8m20s
PROBE import extrapolation latency=80ms rows=5000 requests=10000 wall=13m20s
PROBE import extrapolation latency=150ms rows=5000 requests=10000 wall=25m0s
PROBE analysis alloc orders=20000 requests=41 payload=20988166 bytes total_alloc=88179328 bytes peak_heap=47878456 bytes elapsed=145ms
PROBE import payload rows=5000 csv_bytes=1693942 base64_bytes=2258592
PROBE import payload default_http_body_limit=1048576 fits_http=false
PROBE import parse rows=5000 valid=5000 total_alloc=7068944 bytes peak_heap=7822600 bytes
PROBE db claim backlog=20000 claimed=20000 total=1m44.103s per_claim=5.205ms
PROBE db import-failure write rows=100 elapsed=43ms per_row=427µs
PROBE db import-failure write rows=1000 elapsed=497ms per_row=497µs
PROBE db audit list rows=5000 returned=50 elapsed=2ms
PROBE db audit list filtered returned=50 elapsed=1ms
```

Benchmarks (Apple M6, Go 1.26.0, fake server on loopback, zero synthetic latency):

```text
BenchmarkStockStatuses25-12            1077        994048 ns/op      354851 B/op        4665 allocs/op
BenchmarkAnalyzeSales924-12             133       8585824 ns/op    12134645 B/op       52867 allocs/op
```

Regression subset (required by the task contract):

```text
$ go test ./internal/magento/... ./internal/storage/... -count=1
ok      github.com/shuaiZend/mage2mcp/internal/magento    0.616s
ok      github.com/shuaiZend/mage2mcp/internal/storage    0.143s
```

Re-run at the end of the audit, after the concurrent edits landed: `internal/storage` still passes;
`internal/magento` fails two tests (`TestCreateConfigurableRequestSequence`,
`TestCreateConfigurableAssignsChildQuantity`) with
`stage stock_status: Magento has no such resource or route`. That is the Magento engineer's
in-progress `configurable.go` change (new `observeParentStock` read-back) against a test fake that
does not yet serve `/inventory/source-items`; it is unrelated to this audit, which touched no
production code. The probe suite still passes against the current tree (re-measured: 32 requests for
a 10-child configurable create).

Note: `internal/storage` tests **skip** without `MAGE2MCP_TEST_DATABASE_URL` (see
`internal/storage/storage_test.go:21`), so the green run above is the compile-plus-skip path.
The PostgreSQL findings below were measured separately against a real server (§2, `TestPerfDB`).

---

## 3. Findings

### PERF-01: A maximal import cannot finish inside the worker task timeout, and retries re-run it from row 0

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `internal/magento/import.go:348-410` (row loop), `internal/jobs/jobs.go:98-99`
  (`TaskTimeout` default 5 min), `cmd/worker/main.go:82` (worker constructed with `Logger` only),
  `internal/magento/import_jobs.go:143` (`MaxAttempts: 2`)
- **Evidence**: `executeImportRows` issues **one `GET /products/{sku}` plus one `POST`/`PUT` per row**,
  strictly sequentially (`import.go:350`, `:369`, `:395`). Measured: 10 rows → 20 requests, 50 rows →
  100 requests (probe). The worker wraps the handler in `context.WithTimeout(ctx, w.opts.TaskTimeout)`
  (`jobs.go:177`) with `TaskTimeout` defaulting to `5 * time.Minute` (`jobs.go:98-99`); `cmd/worker`
  never overrides it and `internal/core/config` has no worker section, so an operator cannot raise it
  without a code change. Extrapolation from the measured 2 requests/row: 5,000 rows = 10,000 requests
  = **8 m 20 s @ 50 ms RTT, 13 m 20 s @ 80 ms, 25 m @ 150 ms**. Break-even against the 5-minute budget
  is ≈1,875 rows at 80 ms RTT (3,000 rows at 50 ms).
- **Trigger**: any import larger than ~1,875 rows (well inside the accepted 5,000-row limit), or a
  slower store, or a Magento under load.
- **Impact**: the import job is cancelled mid-flight at 5 minutes; rows already written stay in the
  catalog (`ExecuteImportWithProgress` documents partial success), the batch is recorded `failed`,
  and the job retries once — replaying the whole 2N request sequence, re-reading every row and
  re-writing the ones that failed. Operators see `failed` batches with hundreds of applied rows and
  no way to complete the batch in one pass. Bulk catalog load is the primary write use case.
- **Proposed fix**: (a) make `TaskTimeout`, `Concurrency`, `Lease` and `PollInterval` configuration
  (`worker:` block) and set the import task timeout from the batch size (rows × measured per-row
  budget); (b) call `queue.Extend` on a ticker while the handler runs (see PERF-02); (c) on retry,
  resume from `import_batch_failures` instead of row 0 (the resume plumbing already exists:
  `ExecuteImportRows` + `OnlyRows`); (d) batch the existence check — one `products?searchCriteria
  [filter_groups][0][filters][0][field]=sku&condition_type=in` per 50 SKUs instead of one GET per row
  (see PERF-06).
- **Test requirement**: a fake-server test that runs a 200-row import under a 1-second task timeout
  and asserts (i) the timeout is honoured, (ii) the batch records exactly the rows applied, and
  (iii) the retry issues only the failed rows (request count = 1 + failed rows, not 2N).
- **Verified by**: —

### PERF-02: The job lease is never extended, so any task over 2 minutes runs twice

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `internal/jobs/jobs.go:70` (`Extend` in the `Queue` interface), `:170-192`
  (`execute`, no `Extend` call), `internal/storage/jobqueue.go:187-195` (implementation),
  `:122-126` (recovery update in `Claim`)
- **Evidence**: `Worker.execute` calls the handler and then `Complete` or `Fail`; it never calls
  `queue.Extend`. `JobQueue.Claim` begins every claim with
  `UPDATE jobs SET state='queued', lease_expires_at=NULL WHERE state='running' AND lease_expires_at < now()`
  (`jobqueue.go:122-126`). Default lease is `2 * time.Minute` (`jobs.go:95-97`). `grep -rn "\.Extend("`
  returns only the interface declaration and the implementation — zero callers.
- **Trigger**: any task whose handler runs longer than 2 minutes — i.e. exactly the imports in
  PERF-01, and any future long report job.
- **Impact**: at t+2 min the lease expires; the next `Claim` by **any** worker (including another
  replica) flips the job back to `queued` and hands it out again, so two workers execute the same
  import against the same Magento instance concurrently, writing the same SKUs with last-write-wins
  semantics and doubling upstream load. When the first worker finishes it calls `Complete`, marking
  `succeeded` while the duplicate is still running. This is the "duplicated job execution" P1 in the
  shared ladder; it also silently defeats the at-least-once/idempotency story.
- **Proposed fix**: in `Worker.execute`, start a renew goroutine that calls `queue.Extend(taskCtx,
  id, lease)` at `lease/3` until the handler returns (and log a warning when a renewal fails); make
  the lease configurable and ≥ 2× the task timeout.
- **Test requirement**: a fake queue that records `Extend` calls; a handler that sleeps past a short
  lease and asserts at least ⌈duration/(lease/3)⌉ renewals and that `Claim` never returns the same
  task twice. A PostgreSQL integration test that seeds a running job with an expired lease and
  asserts recovery happens only for genuinely dead workers.
- **Verified by**: —

### PERF-03: Per-tool timeouts are declared and never enforced; a paginating tool can hold a session for 20 minutes

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/mcp/registry/registry.go:30` (field), `:139` (validated only),
  `internal/magento/tools.go:20` (`toolTimeout = 30s`), `:1428`, `:1838`, `:1899` (`120s`),
  `internal/magento/write_tools.go:18` (`writeToolTimeout = 60s`), `:1946`, `:2214`, `:2259`,
  `:2549`, `:2612`; `cmd/server/main.go:145-149`
- **Evidence**: `registry.Validate` checks `t.Timeout <= 0 || t.Timeout > maxTimeout` and stores the
  value. Nothing reads it afterwards: `grep -rn "\.Timeout" internal/mcp/` returns only that
  validation line, and the only `context.WithTimeout` on a tool path is the per-HTTP-request client
  deadline (`internal/magento/client.go:99-103`, default 30 s). The MCP server is built with
  `ReadHeaderTimeout: 10 * time.Second` and **no** `WriteTimeout`/`ReadTimeout`/`IdleTimeout`
  (`cmd/server/main.go:145-149`).
- **Trigger**: any call that pages: `sales.analysis` (40 pages), `orders.geography` (20),
  `sales.summary` (5), an import (10,000 requests), or a single request against a slow upstream.
- **Impact**: the declared 30 s/60 s/120 s budget is fiction. Worst case a `sales.analysis` call
  takes 41 × 30 s = **20.5 minutes** (each request may consume its full client deadline) before
  returning, while the MCP session, the HTTP goroutine and the caller's slot are pinned. Because
  there is also no server-side write timeout, a client that disconnects mid-call leaves the work
  running until the upstream finally answers. The tool contract in `docs/mcp-tools.md:63` ("Default
  30 s; max 120 s synchronous") is therefore DOC-ONLY behaviour.
- **Proposed fix**: enforce the declared budget at registration: wrap the handler in
  `context.WithTimeout(ctx, t.Timeout)` inside `toolerr.Add`/`registry` (one place), return
  `upstream_timeout` with a `narrow the window` hint, and set `http.Server.WriteTimeout` to
  `maxToolTimeout + grace`. Give paging tools a budget that matches their page count, or move them to
  jobs.
- **Test requirement**: a tool registered with a 50 ms timeout whose handler sleeps 500 ms must
  return `upstream_timeout` in <200 ms; a paginating tool must stop issuing requests once the
  deadline fires (assert request count on a fake server).
- **Verified by**: —

### PERF-04: The Magento client uses `http.DefaultTransport` — no tuned pool, `MaxIdleConnsPerHost = 2`

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/client.go:84-87` (client built without `Transport`),
  `internal/magento/admin_token.go:339-357` (`adminTokenRetryTransport` with `base == nil` ⇒
  `http.DefaultTransport`), `internal/magento/credential.go:55-63` (production wiring never sets
  `HTTPClient` for OAuth 1.0a and never sets `Timeout`)
- **Evidence**: `NewClient` builds `&http.Client{CheckRedirect: ...}` with no `Transport`, so every
  request inherits `http.DefaultTransport`: `MaxIdleConns = 100` but **`MaxIdleConnsPerHost = 2`**,
  `IdleConnTimeout = 90 s`, no per-host cap, no `ResponseHeaderTimeout`. `NewResolverFromSettings`
  passes `ClientConfig{BaseURL, StoreCode, Signer}` only — `Timeout` is zero, so
  `DefaultTimeout = 30 s` per request applies.
  Measured (probe, TLS server, 8 concurrent workers × 20 requests with 1 ms think time):
  **41–64 TCP+TLS connections for 160 requests** with the default transport (four runs: 64, 62, 58,
  41; median 60), versus **8 connections** with `MaxIdleConnsPerHost: 64` — i.e. **20–35 % of
  requests pay a fresh TCP+TLS handshake**.
- **Trigger**: any concurrency above 2 toward one Magento host — a single worker replica already runs
  4 goroutines, and each MCP server process adds its own pool.
- **Impact**: every churned request pays a TCP handshake plus TLS negotiation instead of reusing a
  warm connection. A fresh connection costs 1 TCP RTT + 1–2 TLS RTT, i.e. ~20–30 ms at 10 ms RTT, so
  the measured churn adds ~0.3 s to a 41-request analysis and **~75 s to a 10,000-request import**
  (derived: 30 % of requests × 25 ms). It also inflates connection churn and TLS session load
  on the Magento edge, which is exactly what rate limiters punish.
- **Proposed fix**: build one `*http.Transport` per instance in `NewResolverFromSettings` with
  `MaxIdleConns: 100`, `MaxIdleConnsPerHost: 16–32`, `IdleConnTimeout: 90 s`,
  `TLSHandshakeTimeout: 10 s`, `ResponseHeaderTimeout` bounded by the client timeout, and share it
  across the REST client and the admin-token signer.
- **Test requirement**: a probe-style test asserting that 160 requests from 8 workers open ≤ 12
  connections against an `httptest` server, plus a unit test that `NewClient` rejects a nil
  `Transport` on a supplied `HTTPClient` or installs the tuned default.
- **Verified by**: —

### PERF-05: No outbound rate limiting, no concurrency cap, no per-instance serialization

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: whole tree — `grep -rn "semaphore\|rate\.Limiter\|x/time/rate" internal/ cmd/`
  returns nothing outside `internal/jobs/jobs.go:135` (a worker-done channel);
  `docs/architecture.md:282-288` claims the opposite
- **Evidence**: there is no semaphore, no token bucket, and no per-instance mutex anywhere.
  `golang.org/x/time` and `golang.org/x/sync` appear in `go.mod` only as indirect dependencies
  (neither is imported by this module). Effective concurrency toward one Magento instance is
  **agents × in-flight tools × worker goroutines**, unbounded: 4 worker goroutines
  (`jobs.go:88-91`) plus every concurrent MCP `tools/call`, each of which may itself issue 25–10,000
  sequential requests. `docs/architecture.md` §9 states "Per-instance concurrency semaphores and
  token-bucket rate limiters, sized from configuration" and "imports are serialized per instance and
  SKU range to avoid write races" — neither exists (`grep -rn "import.*serial" internal/` → none).
- **Trigger**: two agents calling `sales.analysis` while an import runs, or two import jobs for the
  same instance (nothing prevents this: `Enqueue` only dedupes on an explicit idempotency key).
- **Impact**: (a) a Magento instance can be hit with hundreds of concurrent requests by one gateway,
  triggering the 429/503 the taxonomy already models but nothing prevents; (b) two imports for the
  same instance run on two worker goroutines and interleave writes to the same SKUs — last-write-wins
  on catalog state, with per-row "success" reported for both; (c) no fairness between a long import
  and interactive tools.
- **Proposed fix**: add a per-instance `golang.org/x/sync/semaphore` (or a buffered channel) sized
  from config, acquired around `Client.do`; add a per-instance import lock (`SELECT ... FOR UPDATE`
  on a `datasource_instances` row, or an advisory lock keyed by instance id) so imports serialize;
  add an optional token bucket (`x/time/rate`) honouring `429` + `Retry-After`.
- **Test requirement**: a fake server that records concurrent in-flight requests; assert the observed
  maximum never exceeds the configured limit with 20 concurrent callers, and that two enqueued
  imports for one instance never overlap in time.
- **Verified by**: —

### PERF-06: Import executes 2 sequential Magento requests per row (no batching, no existence pre-check batch)

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/import.go:348-410`
- **Evidence**: for each row the loop calls `s.Get(ctx, row.SKU)` (`:350`) to decide create-vs-update,
  then `s.Update` (`:369`) or `s.Create` (`:395`). Measured: 10 rows → 20 requests; 50 rows → 100
  requests; 2.00 requests/row exactly. No `products.get`-style batch (`BatchGet` already exists and
  handles 50 SKUs per request, `product.go:280-321`), no Magento async bulk endpoint
  (`/products/bulk` or `/async/bulk/V1/products`), no pipelining.
- **Trigger**: any import.
- **Impact**: N=5,000 ⇒ 10,000 requests where ~200 would suffice (one `sku in (...)` search per 50
  rows + one bulk POST per 100 rows + one bulk source-items call), i.e. a ~50× request reduction and
  the difference between 13 minutes and ~15 seconds. This is also the root cause of PERF-01.
- **Proposed fix**: pre-fetch existence with `BatchGet` in chunks of 50 (`condition_type=in`), then
  use `POST /products` (or the async bulk endpoint) for creates and `PUT` only for real updates;
  batch `AssignSourceItems` for all created rows in one call (already supported,
  `inventory.go:52-65`).
- **Test requirement**: a fake-server test asserting a 100-row import issues ≤ 6 requests
  (2 × `BatchGet` + 2 × bulk create + 1 source-items + 1 slack) and reports identical per-row
  outcomes to today's implementation.
- **Verified by**: —

### PERF-07: `magento.sales.summary` silently caps at 1,000 orders — a 20,000-order window is 5 % covered

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/order.go:24` (`maxSummaryOrders = 1000`), `:176` (loop bound),
  `:221-223` (flag)
- **Evidence**: `for offset := 0; offset < maxSummaryOrders; offset += limit` with `limit = 200`.
  Measured with 20,000 orders in the window: 5 requests, `scanned_orders = 1000`,
  `total_orders = 1000`, `truncated = true`. Revenue, AOV and per-day totals are computed from 5 % of
  the window.
- **Trigger**: any window with >1,000 orders — a single busy week for a mid-size store.
- **Impact**: the tool returns plausible, precisely formatted revenue numbers that are simply wrong
  (5 % of reality) and only a boolean to signal it. An agent that ignores `truncated` reports a 20×
  understatement. This is a **correctness limit implemented as a scale limit**; the ladder's P1
  "wrong business flow" is avoided only because the flag exists.
- **Proposed fix**: either make `magento.sales.summary` an alias for the (already correct,
  20,000-order) `sales.analysis` path, or return `revenue: null` plus `truncated: true` and a
  `complete: false` field when the scan is incomplete, and document that callers must slice windows.
  Better: add keyset pagination (`created_at > last_seen`) so the cap becomes a time bound, not a
  row bound.
- **Test requirement**: with 2,000 synthetic orders, either assert the summary covers all of them, or
  assert the response carries `complete: false` and null aggregates; a property test that
  `sum(by_day.revenue) == total_revenue` for every scan.
- **Verified by**: —

### PERF-08: `sales.summary` sets `truncated: true` even when it scanned every order

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/order.go:221-223`
- **Evidence**:

  ```go
  if payload.TotalCount > summary.ScannedOrders {
      truncated = true
  }
  ```

  evaluated inside the page loop, so the flag latches on the **first** page and is never cleared.
  Measured with 924 orders: `scanned_orders=924 total_orders=924 truncated=true` — a complete scan
  reported as truncated. Contrast `order_geography.go:128`, which assigns
  (`result.Truncated = result.TotalAvailable > result.OrdersScanned`) and is therefore correct.
- **Trigger**: any window with more than 200 orders (i.e. more than one page).
- **Impact**: the exact dataset from the project's own acceptance run (924 orders) is reported as
  truncated. Agents and operators either distrust a correct number or slice windows unnecessarily,
  multiplying upstream load. It also masks PERF-07: a genuinely truncated 20,000-order scan and a
  complete 924-order scan are indistinguishable in the payload.
- **Proposed fix**: compute the flag after the loop, from `TotalCount` captured on page 1:
  `summary.Truncated = totalCount > summary.ScannedOrders` (mirror the geography implementation).
- **Test requirement**: table test over 0/1/200/201/924/1000/1001 synthetic orders asserting
  `Truncated == (totalCount > scanned)`; the 924 case must be `false`.
- **Verified by**: —

### PERF-09: `sales.analysis` caps at 20,000 orders with no cursor, and pages with SQL OFFSET

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/sales_analysis.go:19-20` (default 10,000 / max 20,000),
  `:230-231` (page loop), `:449-462` (`analysisQuery` sets `currentPage`), `:625` (unbounded
  shipment loop)
- **Evidence**: measured: 924 orders → 3 requests (2 order pages + 1 shipments page); 20,000 orders →
  41 requests (40 + 1). `searchCriteria[currentPage]` becomes `LIMIT 500 OFFSET (page-1)*500` in
  Magento's SQL, so page *k* makes the database skip 500(k-1) rows before returning any:
  total skipped rows for a full N-order scan is Σ 500(k-1) ≈ **N²/1000** = **400,000 skipped rows for
  N = 20,000** (derived). Beyond 20,000 the tool refuses (`max_orders must be at most 20000`), which
  is the documented gap G11 in `docs/development/mcp-gaps.md`.
- **Trigger**: windows above 20,000 orders; deep pages on any large store.
- **Impact**: (a) quadratic page-skip cost inside Magento's database for large scans; (b) no cursor,
  so a 100,000-order month cannot be analysed at all (the caller must slice by hand — the docs
  confirm this); (c) OFFSET paging over `ORDER BY created_at ASC` is **not stable** when orders are
  inserted during the scan: a new row at the end shifts every subsequent page by one, duplicating an
  order across pages and double-counting its revenue (this is also a MAG/REL correctness note).
- **Proposed fix**: replace `currentPage` paging with keyset paging
  (`created_at >= last_created_at` with `sortOrders[created_at]=asc`, tie-break on `entity_id`), and
  expose a `cursor` in the response. Keep the 20,000 default as a *budget*, but let the caller
  continue from the cursor instead of re-slicing the window.
- **Test requirement**: a fake server asserting each subsequent request carries the last-seen
  `created_at` boundary rather than `currentPage > 1`; an integration test that inserts an order
  mid-scan and asserts no order is counted twice.
- **Verified by**: —

### PERF-10: `orders.geography` caps at 10,000 orders and drops the rest

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/order_geography.go:18` (`maxGeographyOrders = 10000`), `:60`
  (loop), `:128` (flag)
- **Evidence**: measured with 20,000 orders and `max_orders=10000`: 20 requests,
  `orders_scanned = 10000`, `total_available = 20000`, `truncated = true`. The flag is correct here;
  the coverage is not.
- **Trigger**: any window above 10,000 orders.
- **Impact**: half the revenue is attributed to the wrong country mix with a boolean as the only
  warning. Country mix is the kind of number that gets pasted into a board deck.
- **Proposed fix**: same keyset/cursor treatment as PERF-09; or return aggregates plus an explicit
  `coverage_ratio` and refuse to return `total_revenue` when coverage < 100 %.
- **Test requirement**: with 20,000 synthetic orders, assert either full coverage or
  `coverage_ratio == 0.5` and a null `total_revenue`.
- **Verified by**: —

### PERF-11: The 8 MiB response cap truncates silently, and analysis pages read unrestricted order objects

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/client.go:28` (`maxResponseBytes = 8 << 20`), `:143`
  (`io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))`), `sales_analysis.go:449-462` and
  `order_geography.go:61-73` (no `fields` restriction, unlike `SearchOrders` which sets
  `orderSearchField` at `order.go:31-34`)
- **Evidence**: `io.LimitReader` stops at 8 MiB and returns no error; `ReadAll` succeeds, `json.Unmarshal`
  then fails on the cut-off document. Measured: an 8,387,570-byte product body is accepted
  (`name_len = 8,387,540`); an 8,389,618-byte body fails with
  `upstream_unavailable: Magento returned an unreadable product payload`. Measured payload volume for
  the analysis path: 20,998,166 bytes for 20,000 synthetic orders at **1.05 KB/order**, i.e. ~1.05 MB
  per 500-order page. Real Magento order objects (items + extension_attributes + payment) are
  typically 3–8 KB, so a 500-order page is 1.5–4 MB and the 8 MiB cap is reached at **~16 KB/order**
  (derived) — plausible for orders with many line items.
- **Trigger**: a page of large orders on the analysis/geography paths (which request full objects),
  or any endpoint returning a large document.
- **Impact**: the call fails with a message that blames Magento ("unreadable payload") when the
  gateway truncated the response; there is no `payload_too_large` code, no size in the error, and no
  way for the caller to tell truncation from upstream corruption. It is also undetectable in
  aggregate: `truncated` refers to order-count coverage, not bytes.
- **Proposed fix**: read with `io.LimitReader(maxResponseBytes+1)` and return a distinct
  `upstream_response_too_large` error carrying the limit when the extra byte is present; add
  `fields=items[entity_id,created_at,status,grand_total,...,items[...]]` to the analysis and
  geography queries (the constant already exists for `SearchOrders`) so the payload shrinks by an
  order of magnitude; reduce `analysisPageSize` to 200 if the projection is still large.
- **Test requirement**: fake-server test with a body of `maxResponseBytes+1` asserting
  `upstream_response_too_large` (not `unreadable payload`); a test asserting every paged order query
  carries a `fields` parameter.
- **Verified by**: —

### PERF-12: The documented import ceiling (8 MiB / 5,000 rows) is unreachable behind the 1 MiB HTTP body limit

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/import.go:22-26` (`maxImportBytes = 8 << 20`,
  `maxImportRows = 5000`), `internal/mcp/server/server.go:231,238`
  (`maxBody = 1 << 20`, `MaxRequestBodyBytes`), `internal/core/config/config.go:104`
  (`MaxBodyBytes: 1 << 20` default)
- **Evidence**: the import payload travels base64-encoded inside the MCP request body
  (`import_jobs.go:21-28`, `import.go:148-157`). Measured: a realistic 5,000-row catalog CSV
  (300-character description per row) is **1,693,942 bytes**; base64 is **2,258,592 bytes**, which is
  **2.15× the default 1 MiB body limit** (`fits_http=false`). The effective ceiling at the default
  configuration is ≈786 KiB of CSV, i.e. **~2,300 rows** of that shape (derived), 46 % of the
  documented row limit and 9 % of the documented byte limit.
- **Trigger**: any import with descriptions, meta fields, or multi-attribute rows — i.e. every real
  catalog export.
- **Impact**: the tool rejects the payload at the transport layer (HTTP 413 or a transport error)
  before any gateway validation runs, so the error does not name the real constraint. Operators who
  read the tool description ("at most 5000 rows / 8 MiB") will conclude the gateway is broken. The
  8 MiB path also multiplies memory: 8 MiB decoded + 10.7 MiB base64 string + 7.1 MB parsed rows
  (measured for 5,000 rows) + an 11 MB `jsonb` job row in PostgreSQL.
- **Proposed fix**: make the two limits consistent — either raise `server.max_body_bytes` to
  `ceil(4/3 × maxImportBytes) + slack` (≈11 MiB) and document it, or lower `maxImportBytes` to
  `3/4 × max_body_bytes` and enforce it before base64 decode (a length check on the encoded string,
  which avoids the decode-then-reject allocation). Document the effective row limit per payload
  shape.
- **Test requirement**: a table test over payloads at 0.9× and 1.1× the body limit asserting the
  error is `invalid_argument` naming `max_body_bytes`/`max_import_bytes`, plus a startup validation
  that `max_body_bytes ≥ 4/3 × max_import_bytes`.
- **Verified by**: —

### PERF-13: The job-claim query degrades to a full scan + sort as the backlog grows

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/storage/jobqueue.go:108-152` (`Claim`), `:122-126` (recovery update),
  `internal/storage/migrations/000003_job_lease.up.sql` (`jobs_claim_idx`)
- **Evidence**: measured on PostgreSQL 16.15 with 20,000 queued jobs:

  ```text
  Limit (actual time=5.959..5.960 rows=1)
    ->  LockRows
          ->  Sort  (Sort Key: priority DESC, scheduled_at; quicksort Memory: 2911kB)
                ->  Seq Scan on jobs (actual time=0.529..3.965 rows=20000)
                      Filter: ((state='queued') AND (queue = ANY('{critical,default,import,report}')) AND (scheduled_at <= now()))
  Buffers: shared hit=966   Execution Time: 6.008 ms
  ```

  The same query with a **single** queue value uses the index (`Index Scan using jobs_claim_idx`,
  0.122 ms), and with an empty queue it uses the index (0.665 ms). The `queue = ANY($1)` form defeats
  the index's ordering, so the planner scans and sorts the whole queued set. End-to-end in Go:
  **5.205 ms per claim** over 20,000 claims. The recovery `UPDATE` (`:122-126`) does use
  `jobs_lease_idx` (0.184 ms) but runs on **every** claim as an unconditional write.
- **Trigger**: a backlog of a few thousand queued jobs — e.g. a bulk catalog load enqueuing one job
  per SKU range, or a stalled Magento.
- **Impact**: claim cost grows with the backlog (2.9 MB sort for 20k rows; ~145 MB at 1 M rows, which
  will spill to disk at the default `work_mem`), so the worker spends its time sorting the queue
  instead of running tasks. With 4 workers polling at 1 Hz this is tolerable today and pathological
  under a backlog.
- **Proposed fix**: claim per queue instead of `ANY(array)` (loop over the four queues in priority
  order inside the same transaction, or add a `queue_priority` column and order by
  `(priority, scheduled_at)` with a single-value queue predicate), or keep `ANY` and add an index on
  `(priority DESC, scheduled_at) WHERE state='queued'`. Move the recovery sweep to a periodic
  background task (e.g. every 30 s) instead of every claim.
- **Test requirement**: `EXPLAIN`-based regression test asserting the claim plan contains no
  `Seq Scan` with 20,000 queued rows; a benchmark asserting p99 claim latency < 1 ms at that backlog.
- **Verified by**: —

### PERF-14: The `jobs` table is never pruned or archived

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/storage/jobqueue.go` (no delete path), `cmd/worker/main.go:149,183-204`
  (only `PruneImportBatches`), `internal/storage/import_batches.go:175-183`
- **Evidence**: `grep -rn "DELETE FROM" internal/storage/*.go` finds only
  `DELETE FROM import_batches`. Completed and failed jobs, their `payload jsonb` (which for imports
  contains the full base64 batch, up to ~11 MB) and their `result_ref` stay forever. Import batches
  are pruned at 30 days; jobs are not.
- **Trigger**: normal operation over months.
- **Impact**: the jobs table — and its TOAST payloads — grows without bound, which (a) inflates every
  `Seq Scan` in the claim path (PERF-13), (b) inflates backups and `pg_dump` time, and (c) keeps
  base64 catalog payloads (product descriptions, prices) in the database indefinitely, which is a
  data-retention concern for the security auditor. At 100 imports/day × 2 MB payload, that is
  ~6 GB/month of job rows alone.
- **Proposed fix**: a retention sweep in the worker (`DELETE FROM jobs WHERE state IN
  ('succeeded','failed','canceled') AND finished_at < now() - interval '30 days'` in bounded
  batches), plus a `jobs_created_idx`/partition strategy if volume is high; drop the payload from
  the job row once the batch is finalized (`import_batches` already stores it).
- **Test requirement**: integration test that a sweep removes exactly the terminal jobs older than
  the window and leaves queued/running ones; an assertion that a finalized import job's payload is
  nulled.
- **Verified by**: —

### PERF-15: `inventory.status_batch` issues one sequential request per SKU

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/magento/inventory_read.go:114-142` (loop at `:126`, call at `:134`)
- **Evidence**: measured: 25 SKUs → **25** requests, all `GET /stockItems/{sku}`. Benchmark:
  994 µs/call and **354,851 B / 4,665 allocs** per 25-SKU call on a zero-latency fake server — i.e.
  ~18.6 KB and ~187 allocations per SKU, almost all of it HTTP+JSON overhead.
- **Trigger**: `magento.inventory.status_batch` with the documented maximum (25).
- **Impact**: 25 × RTT (2 s at 80 ms) where a bounded parallel fan-out of 5 would take 0.4 s. It is
  correct and documented ("Magento has no batch endpoint for catalog inventory"), so this is a
  throughput opportunity rather than a defect.
- **Proposed fix**: run the per-SKU reads through an `errgroup` bounded by the per-instance semaphore
  from PERF-05 (concurrency 4–8), preserving the per-SKU error envelope.
- **Test requirement**: fake-server test asserting all 25 SKUs are read with ≤ 8 concurrent in-flight
  requests and that one failing SKU still returns its own error row.
- **Verified by**: —

### PERF-16: Bulk write paths are N sequential round-trips (50 PUTs, 152 requests for one configurable)

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/magento/product_bulk.go:52-71` (`BulkUpdateProducts`),
  `internal/magento/catalog.go:337-358` (`BulkCreateAttributeOptions`),
  `internal/magento/configurable.go:123-152` (children, links, stock),
  `internal/magento/configurable.go:578-659` (`CreateVariations`: Get + Create + AddChild per variant, max 50)
- **Evidence**: measured: 50-SKU bulk update → **50** requests; 10 attribute options → **10**
  requests; configurable with 10 children (each with a quantity) → **32** requests
  (`POST /products` × 11, `POST /inventory/source-items` × 10,
  `POST /configurable-products/{sku}/child` × 10, `PUT /products/{sku}/stockItems` × 1). Derived at
  the documented maximum of 50 children: 50 × 2 + 1 + 50 + 1 = **152** requests. `CreateVariations` is
  **3 requests per variant** (existence `Get`, `Create`, `AddChild`), i.e. up to 150 requests for its
  documented 50-variant maximum (`configurable.go:550`).
- **Trigger**: any bulk catalog operation.
- **Impact**: 4 s for a 50-SKU bulk update and 12 s for a 50-child configurable at 80 ms RTT, all
  inside a single MCP call whose declared budget is 60 s — fine, but linear and fragile. Partial
  success is already modelled (`BulkUpdateResult`), so bounded parallelism is safe.
- **Proposed fix**: bounded parallel fan-out (same semaphore as PERF-05) for per-SKU writes, and use
  Magento's async bulk endpoints (`/async/bulk/V1/products`) where the deployment supports them.
- **Test requirement**: fake-server test asserting 50-SKU bulk update completes with ≤ 8 concurrent
  requests and identical per-row outcomes.
- **Verified by**: —

### PERF-17: `FinishImportBatch` writes one round-trip per failed row

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/storage/import_batches.go:53-67`
- **Evidence**: the failure rows are inserted one `INSERT ... ON CONFLICT` per row inside a single
  transaction. Measured on local PostgreSQL (RTT ≈ 0.1 ms): **427 µs/row at 100 rows, 497 µs/row at
  1,000 rows** (497 ms for the batch). `pgx` offers `CopyFrom`/`SendBatch` for exactly this.
- **Trigger**: an import where many rows fail validation or hit Magento errors — the case the batch
  history exists for.
- **Impact**: 5,000 failed rows ≈ 2.5 s locally and ≈ 5 s at 1 ms RTT, inside the finalization path
  that also holds the job's lease. It is the second-largest DB cost after the claim path.
- **Proposed fix**: `pgx.CopyFrom` into a temp table + `INSERT ... SELECT ... ON CONFLICT`, or a
  single multi-row `INSERT` in chunks of 500.
- **Test requirement**: integration test asserting one round-trip per chunk (count via
  `pg_stat_statements` or a query-count hook) and identical failure rows after the change.
- **Verified by**: —

### PERF-18: Import status polling reads and discards the full payload TOAST column

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/storage/import_batches.go:70-78`
- **Evidence**: `SELECT id::text, instance_id, ..., payload FROM import_batches WHERE job_id = $1`
  scans `payload` into `var payload []byte` (`:72`, `:75`, `:78`) and never uses it —
  `jobs.ImportBatch` has no payload field. The column holds the raw CSV/JSON batch (up to 8 MiB).
- **Trigger**: every `magento.import.status` / `magento.import.batch` poll — which agents do in a
  loop while waiting for a job.
- **Impact**: each poll detaches and transfers a TOAST value of up to 8 MiB to discard it. A poll
  every 5 s for a 10-minute import = 120 polls × payload; at 2 MB that is 240 MB of needless I/O and
  network traffic per import.
- **Proposed fix**: drop `payload` from the `SELECT` (add a dedicated accessor for resume, which
  reads it once per resume).
- **Test requirement**: a query-shape test (or `EXPLAIN` assertion) that the status path does not
  touch `payload`; an integration test that resume still works.
- **Verified by**: —

### PERF-19: Retry policy is a fixed 5-second re-run of the whole task — no exponential, no jitter

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/storage/jobqueue.go:165-183` (`backoff = 5 * time.Second` at `:172`),
  `internal/jobs/jobs.go:186-191` (`retry := task.Attempts < task.MaxAttempts`)
- **Evidence**: `Fail` sets `scheduled_at = now() + $4::interval` with a constant `5s`; the doc
  comment above it says "after a bounded exponential backoff". There is no jitter and no growth.
  Retries are bounded per task (`max_attempts`, default 1, imports 2), and the Magento client itself
  performs exactly one attempt per request (plus one 401 re-sign via `adminTokenRetryTransport`), so
  there is no per-call retry storm today.
- **Trigger**: a Magento outage while several jobs fail.
- **Impact**: with N failing jobs, all retries fire at t+5 s simultaneously (thundering herd against
  an upstream that is already unhealthy) and each retry re-runs the **entire** task — for an import
  that is the full 2N request sequence again, not just the failed rows. A sustained outage produces
  load spikes every 5 s.
- **Proposed fix**: exponential backoff with full jitter (`5s × 2^attempt`, capped at 5 min,
  `rand` jitter), and resume-from-failure for imports (see PERF-01c). Also honour `429`
  `Retry-After` when scheduling the retry.
- **Test requirement**: unit test asserting `scheduled_at - now()` grows with the attempt number and
  is jittered (statistical bound over 100 samples); a test that an import retry re-issues only the
  failed rows.
- **Verified by**: —

### PERF-20: The admin-token mutex is held across network I/O, serializing every request on the instance

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/magento/admin_token.go:183-206` (`Token` holds `s.mu` for the whole
  `fetchToken` call), `:208-251` (`fetchToken` performs the HTTPS request)
- **Evidence**: `Token()` takes `s.mu.Lock()` and, on a cache miss, calls `fetchToken(ctx)` — a full
  HTTP POST — while still holding the lock (`defer s.mu.Unlock()`). Every `Sign` call goes through
  `Token`, so all concurrent requests to one instance block behind the token fetch.
- **Trigger**: token expiry (default TTL) or a revoked token, under concurrency.
- **Impact**: head-of-line blocking for up to the token timeout (10 s) on **all** in-flight requests
  of that instance; a slow token endpoint turns a 30 s request budget into a queue. It also means
  one hung token fetch stalls every worker goroutine touching that store.
- **Proposed fix**: single-flight the refresh (`golang.org/x/sync/singleflight` or a
  `refreshing chan struct{}` pattern) so waiters block only on the in-flight refresh and the mutex is
  held only to read/write the cached value; give the refresh its own short context.
- **Test requirement**: concurrency test with 20 goroutines and a token endpoint delayed 200 ms
  asserting exactly one token fetch and total elapsed < 400 ms.
- **Verified by**: —

### PERF-21: Invoice/shipment/credit-memo list queries stop at 100 rows with no pagination

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/magento/order_documents.go:287-294` (`orderSearchQuery` sets
  `searchCriteria[pageSize] = 100` and no `currentPage` loop), used at `:115`, `:158`, `:226`
- **Evidence**: the helper builds a single page of 100 and the callers return
  `envelope.Items` directly; there is no loop and no `total_count` check.
- **Trigger**: an order with more than 100 shipments/invoices/credit memos (bulk split shipments), or
  a per-order document list used as a window scan.
- **Impact**: silent truncation at 100 with no flag and no total. Low frequency, but the failure mode
  is invisible.
- **Proposed fix**: return `total_count` and a `truncated` flag, or page until exhausted with a
  bounded maximum.
- **Test requirement**: fake-server test with 150 documents asserting the caller sees 150 (or an
  explicit truncation flag).
- **Verified by**: —

### PERF-22: The import payload is parsed twice per execution

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/magento/import.go:319` (`ValidateImport`) and `:327` (`parseImportRows`)
- **Evidence**: `executeImportRows` first calls `s.ValidateImport(ctx, format, data)`, which parses
  the payload into `[]ImportRow`, then calls `parseImportRows(format, data)`, which parses it again.
  Measured parse cost for 5,000 rows: **7,068,944 bytes allocated, 7.8 MB peak heap**, per parse.
- **Trigger**: every import execution.
- **Impact**: ~2× parse CPU and ~2× transient allocation (≈14 MB for a 5,000-row batch) on a path
  that is already the slowest in the system; also doubles the window in which a large payload is
  resident.
- **Proposed fix**: have `ValidateImport` return the parsed rows (or add an internal
  `parseAndValidate` that returns `preview, rows`), and let `executeImportRows` reuse them.
- **Test requirement**: benchmark asserting one parse per execution (allocation budget in
  `-benchmem`) and unchanged validation outcomes.
- **Verified by**: —

### PERF-23: `docs/architecture.md` claims tuned per-instance pools, semaphores and token buckets that do not exist

- **Severity**: P3
- **State**: DOC-ONLY
- **Location**: `docs/architecture.md:282-288`
- **Evidence**: the document states: "One `http.Client` per upstream instance with bounded connection
  pools and explicit timeouts; never the default client" and "Per-instance concurrency semaphores and
  token-bucket rate limiters, sized from configuration. Cloudflare GraphQL defaults to a
  300-query/5-minute budget; GA4 uses the observed 10-concurrent-request quota" and "Worker
  concurrency is bounded per queue; imports are serialized per instance and SKU range to avoid write
  races". Code: the Magento client uses `http.DefaultTransport` (PERF-04), there is no semaphore or
  limiter anywhere (PERF-05), `jobs.WorkerOptions.Concurrency` is not configurable and there is no
  per-queue bound beyond a single flat `Concurrency`, and imports are not serialized per instance.
- **Trigger**: an operator or reviewer sizing capacity from the architecture document.
- **Impact**: capacity planning and threat modelling assume back-pressure that does not exist. This
  is a documentation defect, not a code defect: the code behaves as if the section were absent.
- **Proposed fix**: either implement the section (PERF-04/05) or rewrite §9 to describe what the code
  does today and move the target to `docs/known-limitations.md`.
- **Test requirement**: documentation review against the code; once implemented, the concurrency
  tests from PERF-04/05 become the citation for the section.
- **Verified by**: —

---

## 4. Checked and found acceptable (no finding)

Recording these so the absence of a finding is itself evidence.

| Check | Result |
|---|---|
| **Response buffering bound** | `client.go:143` caps every success body at 8 MiB and error bodies at 4 KiB (`maxDrainBytes`) — memory is bounded, only the *reporting* is wrong (PERF-11). |
| **Request body size** | Bounded by the MCP transport (`MaxRequestBodyBytes`, 1 MiB default) and by `maxImportBytes` for imports. |
| **Pagination loops terminate** | `sales_analysis.go:230`, `order_geography.go:60`, `order.go:176` all stop on a short page or the cap; no infinite loop on a well-behaved upstream. Exception: `sales_analysis.go:625` (`shipmentStats`) has no cap but does break on a short page, so it terminates whenever Magento honours `pageSize`. |
| **Order search projection** | `SearchOrders` restricts `fields` (`order.go:31-34`, `:114`), keeping the default page small. Only the analysis/geography paths are unrestricted (PERF-11). |
| **Product batch read** | `BatchGet` (50 SKUs/request, `product.go:280-321`) is the correct shape; 624 SKUs = 13 requests. |
| **DB indexes for the hot paths** | `jobs_claim_idx`, `jobs_lease_idx`, `jobs_idempotency_uq`, `import_batches_instance_idx`, `import_batch_failures` PK and the `audit_events`/`confirmation_tokens` indexes all match their queries. The claim path's problem is the `ANY(array)` predicate, not a missing index (PERF-13). |
| **Audit list query** | `ListAudit` with and without a tool filter measured 2 ms / 1 ms at 5,000 rows — acceptable at the current scale; the unfiltered variant cannot use `audit_events_tool_idx`, so revisit above ~1 M rows. |
| **Lease recovery index** | `UPDATE ... WHERE state='running' AND lease_expires_at < now()` uses `jobs_lease_idx`: 0.184 ms measured. |
| **Pool sizing** | `pgxpool` bounded by `database.max_conns` (default 10), `MaxConnLifetime` 30 min, `MaxConnIdleTime` 5 min (`storage.go:43-47`). |
| **Worker fan-out** | `jobs.Worker.Run` starts exactly `Concurrency` (default 4) goroutines and drains on shutdown (`jobs.go:131-146`); the `unified` comparison fan-out is 2 goroutines (`unified.go:140-155`). Both bounded. |
| **Migrations** | Serialized by `pg_advisory_lock` (`storage.go:97-102`), idempotent, each in its own transaction. No performance issue. |
| **`magento.product.media` / tier prices / links** | Single-request reads with `pageSize` bounds; no N+1. |

Cross-area observations handed to other auditors (not PERF findings):

- An import CSV row **without** a `type_id` column passes `magento.import.validate` (preview allows
  `""`, `import.go:256-260`) but fails `magento.import.execute` at `CreateProductInput` validation
  before the request is sent — 5,000 wasted `GET`s and a failed batch. Reproduced while building the
  probe (`created=0 failed=10`). → MAG/REL.
- `sales.analysis` OFFSET paging can double-count an order inserted mid-scan (PERF-09c). → MAG/REL.
- Job rows retain full base64 catalog payloads indefinitely (PERF-14). → SEC (retention).

---

## 5. Prioritised remediation list

Ordered by (severity × blast radius) / effort. "Cheap" means ≤ 1 file and ≤ 50 lines.

| # | Finding | Action | Effort | Why now |
|---|---|---|---|---|
| 1 | PERF-02 | Renew the lease in `Worker.execute` (ticker at `lease/3` calling `Extend`) | Cheap | Removes duplicate job execution; it is a P1 correctness bug, not a tuning knob |
| 2 | PERF-01a | Expose `worker.{concurrency,task_timeout,lease,poll_interval}` in config and size the import timeout from the row count | Cheap | Unblocks the primary write use case without touching Magento call shapes |
| 3 | PERF-01c | Resume imports from `import_batch_failures` instead of row 0 | Small | Turns a 2× amplification into a 1× retry |
| 4 | PERF-06 + PERF-01b | Batch existence checks (`BatchGet`, 50/request) and bulk creates; batch `AssignSourceItems` | Medium | ~50× fewer requests; the single largest scalability win in the repo |
| 5 | PERF-04 | One tuned `*http.Transport` per instance, shared with the token signer | Cheap | 20–35 % of requests stop paying a TLS handshake |
| 6 | PERF-05 | Per-instance semaphore + optional token bucket; per-instance import lock | Medium | Back-pressure toward Magento; makes the architecture doc true |
| 7 | PERF-03 | Enforce `registry.Tool.Timeout` in one wrapper; set `http.Server.WriteTimeout` | Cheap | Converts a declared contract into an enforced one; stops 20-minute calls |
| 8 | PERF-08 | Fix the sticky `truncated` flag (assign after the loop) | Trivial | The 924-order acceptance run currently reports a false truncation |
| 9 | PERF-07 | Make `sales.summary` cover the window (reuse `sales.analysis`) or return nulls + `complete:false` | Small | Removes a 20× revenue understatement |
| 10 | PERF-09/10 | Keyset (`created_at` cursor) paging for analysis and geography; keep the row cap as a budget | Medium | Removes the 20k/10k wall and the quadratic OFFSET cost |
| 11 | PERF-11 | Distinguish truncation from corruption (`+1` byte probe) and add `fields` to analysis/geography queries | Cheap | Turns an opaque failure into an actionable error; cuts payload ~10× |
| 12 | PERF-12 | Reconcile `max_body_bytes` with `max_import_bytes` (and validate at startup) | Cheap | The documented import ceiling is currently unreachable |
| 13 | PERF-13 | Claim per queue (or index `(priority DESC, scheduled_at) WHERE state='queued'`); move the recovery sweep off the hot path | Small | Keeps claim latency flat as the backlog grows |
| 14 | PERF-14 | Bounded retention sweep for terminal jobs; null the payload after finalize | Small | Bounds the jobs table and the retained catalog payloads |
| 15 | PERF-19 | Exponential backoff with jitter; honour `Retry-After` | Cheap | Prevents retry herds against an unhealthy Magento |
| 16 | PERF-15/16 | Bounded parallel fan-out for per-SKU reads/writes | Small | 4–6× throughput on batch tools |
| 17 | PERF-20 | Single-flight token refresh, mutex only around the cached value | Cheap | Removes head-of-line blocking on token expiry |
| 18 | PERF-17/18/21/22 | `CopyFrom` failure rows; drop `payload` from status polls; page document lists; parse once | Small each | Housekeeping; measurable I/O and allocation wins |
| 19 | PERF-23 | Rewrite `architecture.md` §9 to match the code (or cite items 5–6 once landed) | Trivial | Stops capacity planning against fiction |

**Suggested release gate**: items 1–9 must land (or be explicitly accepted in
`known-limitations.md`) before a production verdict of "ready with known limitations"; items 10–12
before any customer with >20,000 orders/month or >2,000-row imports is onboarded.

---

## 6. Limits of this audit

- **No live Magento was contacted.** Every Magento number is a request count, allocation, or
  latency derivation from a wire-shape-faithful fake server. The 80 ms RTT assumption is stated
  wherever it is used; the *request counts* (the part that scales) are measured.
- **`internal/storage` integration tests skip without `MAGE2MCP_TEST_DATABASE_URL`.** The PostgreSQL
  findings were measured separately (`TestPerfDB`) against PostgreSQL 16.15 on loopback, so the
  absolute milliseconds are a lower bound for a networked deployment (add ~1 ms per round-trip per
  1,000 km).
- **The 8 MiB truncation** was proven with a synthetic product document. Whether a real Magento order
  page exceeds 8 MiB depends on the store's line-item count; the derivation (~16 KB/order) is given
  so an operator can check it against their own data.
- **Concurrency beyond one process** was reasoned, not measured: no multi-replica harness exists in
  the repository. PERF-05's impact statement is a derivation from the absence of any shared limiter.
- **Worker throughput** was not load-tested end to end (that needs a live Magento); the per-task cost
  is measured and the queueing model is code-derived.
