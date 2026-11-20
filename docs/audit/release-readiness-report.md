# Release readiness report

| Field | Value |
|---|---|
| Document | Release readiness — final verdict and per-capability assessment |
| Baseline commit | `66786a5d10c1f78d00e08a5b42d01fcc2da37a3f` |
| Remediation branch | working tree on `main` (not pushed) |
| Owner | Chief Engineer |
| Verdict | **CONDITIONALLY READY** — see §6 |

This report answers one question: **may this software be used, and for what?**
It is deliberately narrower than "is it good". Every claim below is backed by a
command in `regression-test-report.md`, a review in `independent-code-review.md`,
or a limitation in `known-limitations.md`. Where evidence is missing, the verdict
says so rather than rounding up.

---

## A. Fix statistics

### Findings by area

| Area | Findings | P0 | P1 | P2 | P3 |
|---|---|---|---|---|---|
| Security (`SEC`) | 19 | 4 | 5 | 7 | 3 |
| Magento (`MAG`) | 26 | 2 | 11 | 9 | 4 |
| Reliability (`REL`) | 21 | 2 | 6 | 8 | 5 |
| Architecture (`ARCH`) | 8 | 1 | 2 | 3 | 2 |
| MCP (`MCP`) | 16 | 1 | 1 | 9 | 5 |
| Performance (`PERF`) | 23 | 0 | 2 | 12 | 9 |
| Documentation (`DOC`) | 26 | 1 | 6 | 13 | 6 |
| **Raw total** | **139** | **11** | **33** | **61** | **34** |

Eight of the eleven raw P0s were duplicates of one another. **Eight distinct P0s**
were confirmed: `SEC-01`, `SEC-02`, `SEC-03`, `SEC-05`, `MAG-01`, `MAG-04`,
`REL-01`, `REL-02`.

### Outcome

| Category | Count | Notes |
|---|---|---|
| P0 confirmed | 8 | All eight fixed and independently verified |
| P0 still open | **0** | |
| P1 confirmed | 26 (after de-duplication) | |
| P1 fixed and verified | 23 | |
| P1 deferred with accepted risk | 3 | `SEC-04`, `MAG-23` (now partially closed by a live run — see below), plus the documentation-level P1s, which are now fixed |
| P2 confirmed | ~50 | 31 fixed, 19 deferred |
| P3 confirmed | ~30 | 12 fixed, 18 deferred |

### The four defects that mattered most

The audit's real finding was not a list. It was that four contracts were declared
correctly, unit-tested correctly, and **never consulted by their only real
consumer**:

| Contract | How it was ignored |
|---|---|
| RBAC scope ladder | `Authorize` had no scope parameter; production passed `ScopeRead` for every tool, so a `viewer` could invoke `magento.invoice.capture` |
| Retryability (`RetryableError`) | Called only from tests; the worker retried on attempt count alone |
| Confirmation binding | Bound to the argument hash but not to the tool or the instance, so an approval for `order.hold` could consume `order.unhold` |
| Idempotency | The `idempotency_keys` table was dead schema and `idempotency_key_reuse` was unreachable |

All four are now wired, and each has a test that fails when the wiring is reverted.

### What independent review caught that the authors' tests did not

Two findings justify the review gate on their own:

- **t21** found the SEC-03 fix correct but the shipped SQL CAS untested — deleting
  the tool or instance predicate would have passed CI. `t31` added a DB-backed
  test; `t32` proved it non-vacuous by mutation.
- **t38** found that the ledger gate executed writes on `DecisionInProgress` and
  `DecisionRetry` because only `DecisionReplay` was handled — a reachable
  duplicate-mutation path in code that had already passed the author's tests and
  my own architecture review.

A third, **t43**, found that a *retried* operation never reached a terminal state
and sat in `executing` forever.

## B. Code changes

| Measure | Value |
|---|---|
| Files modified | 109 |
| New files | 21 |
| Insertions / deletions | 16,169 / 6,392 |
| New migrations | 3 (`000005_job_fencing`, `000006_operation_ledger`, `000007_audit_pending_decision`) |
| New third-party dependencies | **0** |
| Files deleted | 0 |

### Main architectural changes

1. **Operation ledger** (`internal/storage/operation_ledger.go` + migration
   `000006`). A durable record of every business mutation with states
   `pending → executing → {succeeded | failed | unknown}` plus
   `reconciling → {succeeded | failed}`, a fencing token per attempt, an
   append-only transition log, and replay semantics. This is the primitive that
   closes the money-safety P0s.
2. **Ledger gate at the middleware choke point** (`internal/mcp/server/server.go`).
   Every write tool passes through one gate that switches on the ledger decision.
   Only `proceed` runs the tool; `replay` returns the recorded result;
   `in_progress`, `retry` and `unknown` are refused without an upstream call.
   Five money tools are explicitly *self-ledgered* because their safety depends on
   pre-flight reads and reconciliation the middleware cannot perform; a
   registry-derived test fails if a write tool is on neither path.
3. **Job queue fencing** (`internal/storage/jobqueue.go` + migration `000005`).
   A lease token per claim, a single `jobLeaseGuard` predicate on every outcome
   write, lease renewal, bounded ordered recovery, and terminal-state guards.
4. **Authorization carries the required scope.** `Authorize` gained a
   `requiredScope` parameter resolved from the registry; `rbac.DefaultPolicy()`
   expresses the ladder.
5. **Confirmation bound to tool and instance** in both the memory store and the
   SQL CAS, with a redacted argument summary for the human approver.
6. **Registry as the single source of truth.** Write gate at registration, schema
   compilation, enforced per-tool timeouts, and a generated
   `docs/implemented-tools.md` that a test fails to let drift.

### Deleted files

None. No capability was removed to make a defect go away. The one attempt to do
so — cutting the import ceiling from 8 MiB to 512 KiB to fit the HTTP body cap —
was rejected and reversed in `t27` by raising the body cap to 16 MiB instead.

## C. Test results

Environment: `go1.26.0 darwin/arm64`; PostgreSQL 16.15 in Docker on
`127.0.0.1:55440`; no live Magento, Cloudflare or GA4.

| Gate | Command | Result |
|---|---|---|
| Formatting | `gofmt -l ./cmd ./internal ./test ./scripts ./docs` | clean |
| Static analysis | `go vet ./...` | clean |
| Build | `go build ./...` | exit 0 |
| Unit + contract | `go test -count=1 ./...` | **0 failures**, 22 packages |
| Race | `go test -count=1 -race ./...` | **0 failures**, verified over four consecutive runs |
| Storage (real PostgreSQL) | `go test -count=1 -race ./internal/storage/...` | pass, **repeatable on a dirty database** |
| Independent QA suite | `./scripts/qa-independent.sh` | **18 passed, 0 skipped, 0 failed** |
| Documentation | `npx markdownlint-cli2 "**/*.md"` | 106 files, **0 issues** |
| Links | `npm run link-check` | exit 0 |

**A test-isolation defect found during the final gate, and fixed.** `go test -race
./...` runs packages in parallel; when a database was configured, database-backed
packages shared one schema — one package's `DELETE FROM jobs` destroyed another's
fixtures mid-test, and one package's reconcile backlog filled another's claim
window. A second test used a fixed sleep that was timing-dependent under load.
Every database-backed package (`internal/storage`, `internal/magento`,
`cmd/worker`) now connects with its own `search_path`, and the stale-write test
polls for the observable condition instead of sleeping. Verified by **six
consecutive parallel `-race` runs with zero failures**; no `-p 1` workaround is
required and the gate is deterministic.

### Coverage

| Package | Default | With database |
|---|---|---|
| `internal/storage` | 3.4% (all tests skip) | ~64% |
| `internal/magento` | ~75% | ~75% |
| `internal/security/*` | 71–100% | 71–100% |
| repository total | ~72% | ~74% |

The 3.4% figure is the honest default number: the storage suite requires
`MAGE2MCP_TEST_DATABASE_URL` and skips without it. CI's 70% floor is met without
the durability layer, which is why the floor is not a safety net for it. See
`regression-test-report.md` for the per-package table.

### Test blind spots

Still untested, and stated as such:

- **No live Magento.** Every Magento claim is `CONTRACT` (fake server). No test is
  labelled `INTEGRATION_TESTED`, and the vocabulary is now explicit about that.
- Single gateway process: no multi-replica race testing.
- Worker forced exit is simulated by abandoning a claim, not by `SIGKILL`.
- Database outage is simulated by closing the pool.
- Network drop is simulated on the capture path only.
- No load or latency assertions.

## D. Security results

| Question | Answer | Evidence |
|---|---|---|
| Privilege escalation? | **No.** A `viewer` is denied write and destructive tools; `operator` is denied destructive; `admin` is required for destructive. | `t36` reproduced through the production wiring, revert-proven |
| Cross-instance data leak? | **No.** A job belonging to instance B is `not_found` for a caller scoped to A, for status, cancel, resume and batch. | `t36`, `t13` DB-backed test |
| Replay risk? | **No.** A confirmation is bound to tool, instance, requester and the canonical argument hash; single use is atomic; the failed attempt does not burn the approval. | `t32` mutation-tested, `t36` |
| Duplicate financial operation? | **No** for capture, refund and invoice: a paid invoice is refused with zero POSTs, ambiguity becomes UNKNOWN and is never retried, and a repeat with the same key replays the recorded outcome. **For order creation**, a retried create places no second order, and an order is only attributed when its `quote_id` matches the operation's cart. | `t37`, `t41` revert-proven |
| Sensitive information leakage? | **Reduced.** Error envelopes are uniform and untyped storage errors are sanitised. Credential-shaped values are redacted from approval summaries. Published GA4 account and property ids were removed from product docs and shipped configs. **Residual:** SEC-10 (bounded upstream text reaches an authorized caller), SEC-17 (inline credential strings). | `t18`, `t32`, `t39`, `t47` |
| Audit integrity? | **Improved.** Denied calls are audited `deny`; intent rows no longer assert a decision; the outcome row shares the ledger transaction for the 50 middleware-gated tools. **Residual:** the 5 self-ledgered money tools write their outcome audit after their own commit (`REL-16-R`). | `t30`, `t36`, `t45` |
| Rate limiting? | **Present for Magento.** Each instance has its own token bucket and concurrency cap (`limits:`; default 50 rps / burst 100 / 8 concurrent). An exhausted budget returns a retryable `rate_limited` without reaching the store. Cloudflare and GA4 do not honour it yet. | `internal/magento/limits.go`, `limits_test.go`, `docs/configuration.md` |

## E. Production assessment by capability

Each verdict is scoped to what was actually verified.

### Read-only production use — **READY, with the Magento caveat**

Read tools are default-deny authorized, audited, schema-validated, and bounded by
enforced timeouts. Pagination and completeness reporting are honest: a truncated
scan says how many orders were excluded rather than silently capping.

Caveat: no live Magento was available, so field-level compatibility with a real
2.4.9 store is unverified. Two contract defects were found by reading the API
(`MAG-14` media entry id, `MAG-02` invoice capture flag) — evidence that contract
reading catches some but not all of such defects.

### Product writes — **CONDITIONALLY READY**

Write tools require explicit confirmation by a different human, are gated by the
ledger, and are subject to a real write-enable flag. Configurable creation is a
staged workflow with precheck, link verification, resume via `operation_id`, and
orphan reporting; a partial run is never reported as success.

Caveat: media upload has no dedupe (MAG-15), and the configurable workflow's
resume state is upstream read-back rather than a durable ledger intent.

### Inventory writes — **CONDITIONALLY READY**

Transfers detect concurrent modification, verify their own write by reading back,
name the applied row on a partial failure, report `applied_rows=unknown` when the
outcome is genuinely unknown, and never compensate automatically. Fractional
source quantities are refused rather than truncated.

Caveat: Magento MSI has no conditional write, so the read-to-write window is only
narrowed, not closed, and the in-process guard does not survive multiple
replicas (MAG-R04). Do not run several gateway replicas against one source and
expect transfer atomicity.

### Order operations — **CONDITIONALLY READY**

Order creation records a ledger operation before placing the order, keeps the cart
when `place_order` is ambiguous, re-reads up to three times before declaring a
failure, and attributes an order only when its `quote_id` matches the operation's
cart.

Caveat: if the target store's REST order search does not expose `quote_id`,
reconciliation degrades to `operation_unknown` with candidate ids. That is
fail-safe — a human reconciles, and the system never claims a foreign order — but
"automatic reconciliation works" remains `CONTRACT` until verified on a live
store.

### Payment and refund operations — **CONDITIONALLY READY**

Invoice capture refuses an already-paid invoice with zero upstream calls, treats a
timeout as UNKNOWN and non-retryable, and replays a recorded outcome on a repeat
with the same idempotency key. Credit memos validate invoice linkage and
refundable quantity before any upstream call and carry money as integer minor
units end to end. Order cancellation reconciles against the order state.

Caveat: the gateway cannot make a payment gateway idempotent. The protection is
ledger-plus-read-back plus human reconciliation, not a gateway guarantee. Until
this has been exercised against a live Magento with a real payment method, treat
the first production captures as supervised.

## F. Final conclusion

**CONDITIONALLY READY**

**Why not READY.** Three reasons, each specific:

1. **No Magento integration test exists.** Every Magento behaviour is verified
   against the documented REST contract and a fake server. That is a real
   verification, but it is not the same as a verified store. `MAG-23` was
   partially closed after this release by a one-off live run: 101 tools against a
   real store, recorded in `live-magento-test-report.md`, which found five defects
   including a P1 in order placement (`live-magento-defects.md`). It is still not
   in CI.
2. **Order creation depends on an unverified field.** Exact reconciliation needs
   `quote_id` in the order search projection. If an instance omits it, the system
   degrades to UNKNOWN — safe, but not automatic.
3. **Residual audit and isolation gaps.** The five self-ledgered money tools write
   their outcome audit after their own commit (`REL-16-R`); Cloudflare and GA4
   have no outbound limits; `cmd/admin` has no tests.

**Why not NOT READY.** None of the conditions that would force it hold:

- No confirmed P0 remains open. All eight were fixed and independently verified,
  each with a test proven to fail when the fix is reverted.
- No security test passes because it was skipped. The independent suite fails on
  any skip, and that gate was itself proven by a temporary skip probe.
- No documented capability lacks implementing code. The threat model now cites 31
  real paths and 84 real tests, all verified present.
- No test was deleted or weakened to make a gate pass. The two assertions that
  changed were each verified as stricter by a reviewer.

**Conditions attached to this verdict**, in priority order:

1. Run `test/e2e` against a Magento 2.4.9 sandbox before first production write
   use, and confirm `quote_id` appears in the order search projection.
2. Enable writes only with an explicit `operator` role, a configured database, and
   a body cap of at least 12 MiB.
3. Supervise the first payment captures and refunds; reconcile any
   `operation_unknown` against Magento manually rather than retrying.
4. Do not run multiple gateway replicas if MSI transfer atomicity matters.
5. Set `limits:` per Magento instance to match the store's real capacity; the
default is generous and is not a substitute for knowing the store's limits.

**What would change this verdict to READY:** a passing integration suite against a
real Magento 2.4.9 store covering read, product write, inventory transfer, order
create, capture and refund; plus a live confirmation that `quote_id` is exposed.

**What would change it to NOT READY:** any regression that reopens a P0, any test
deleted or skipped to pass a gate, or a live Magento run that contradicts a
contract the gateway depends on.

## G. Review binding

This programme ran fourteen review rounds. Each verdict was bound to the sha256 of
the bytes it judged, and the review tooling **invalidates a verdict when any
reviewed file changes afterwards**. Because remediation continued after several
area reviews passed, those verdicts stopped binding the tree — which is a real
gap, not a bookkeeping detail.

**It was closed by a final consolidated review (`t52`)** covering the whole tree
rather than one area: the eight P0s reproduced through the production wiring, the
seams between them, six revert experiments, and the full gate re-run. Its 21
reviewed production files were hashed at the start and re-hashed at the end with
identical results, so the verdict provably covers the bytes it judged.

After `t52` the only changes were to **test files** — the two flaky tests it
flagged (`F1` per-package schema for `cmd/worker`, `F2` a timing-dependent sleep
replaced by a condition poll). No production file changed, so `t52`'s verdict
still binds the shipped code. The gate is now deterministic: six consecutive
parallel `-race` runs, zero failures.

| Review | Verdict | Binds the final production bytes? |
|---|---|---|
| `t36` security (area) | PASS, no P0/P1 | superseded by `t52` |
| `t41` Magento (area) | PASS | superseded by `t52` |
| `t48` reliability (area) | PASS | superseded by `t52` |
| `t50` MCP (area) | PASS | superseded by `t52` |
| **`t52` consolidated** | **PASS** | **yes** |

The team's automatic review loop escalated after `t43` reached its three-round
ceiling. That escalation was a bookkeeping limit, not a code state: `t43`'s
findings were fixed in `t46` and verified PASS in `t48`. It is recorded here
rather than suppressed.

With this closed, the verdict rests on two things only: the absent Magento
integration run, and the documented residual risks. That is why it remains
CONDITIONALLY READY rather than READY.

This software is materially better than it was at the baseline commit and is safe
to use for read-only production work today. It is not yet safe to call
"production-grade" for money movement without the supervised first run described
above, and this report will not use that phrase until the integration suite
exists.
