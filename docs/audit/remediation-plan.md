# Remediation plan

| Field | Value |
|---|---|
| Document | Remediation plan — every finding, its owner, its fix, its status |
| Baseline commit | `66786a5d10c1f78d00e08a5b42d01fcc2da37a3f` |
| Owner | Chief Engineer |
| Source reports | `security-audit-report.md`, `magento-integration-review.md`, `reliability-review.md`, `mcp-protocol-review.md`, `performance-review.md`, `documentation-consistency.md`, `captain-findings.md` |

A finding is **closed** only when its fix is merged, its regression test exists and
fails without the fix, and a non-author reviewer has passed it. "Reported" is not
"fixed". "Fixed" is not "verified".

---

## 1. Status vocabulary

| Value | Meaning |
|---|---|
| **FIXED** | Code and test merged; awaiting or holding independent review. |
| **VERIFIED** | Fixed *and* passed by a non-author reviewer on the current bytes. |
| **DEFER** | Real, acknowledged, not fixed; recorded in `known-limitations.md`. |
| **DOC** | Code correct or unchanged; the document was wrong and is corrected. |
| **DUP** | Duplicate; tracked once. |

## 2. Outcome summary

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

After de-duplication (`ARCH-01`=`SEC-01`=`MCP-01`, `ARCH-03`=`SEC-02`,
`ARCH-06`/`ARCH-08`=`SEC-03`, `ARCH-04`=`REL-02`, `SEC-19`=`REL-01`,
`DOC-01`=`SEC-05`, `PERF-02`=`REL-03`, `PERF-03`=`MCP-05`) the distinct defect
count is **eight P0s**:

`SEC-01`, `SEC-02`, `SEC-03`, `SEC-05`, `MAG-01`, `MAG-04`, `REL-01`, `REL-02` —
of which `SEC-05`, `MAG-01`, `MAG-04` and the money half of `REL-02` share one
root cause and one fix (the operation ledger).

### What actually changed

The audit's headline result is not the defect list. It is that **four of the
most important contracts were declared correctly, unit-tested correctly, and
then never consulted by their only real consumer**:

| Declared contract | Where it was ignored |
|---|---|
| RBAC scope ladder (`read < write < destructive < admin`) | `Authorize` had no scope parameter; production passed `ScopeRead` for every tool |
| Retryability (`RetryableError`) | Called only from tests; the worker retried on attempt count alone |
| Confirmation binding | Bound to the argument hash but not to the tool or the instance |
| Idempotency (`idempotency_keys` table, `idempotency_key_reuse` error) | The table was dead schema and the error code unreachable |

All four are now wired, and each has an end-to-end test that fails when the
wiring is reverted.

## 3. Security findings

| ID | Sev | Status | Fix | Review |
|---|---|---|---|---|
| SEC-01 | P0 | **VERIFIED** | `t8` — scope resolved from the registry, fail-closed, real `DefaultPolicy` | `t14` pass; `t26` re-pass at current bytes |
| SEC-02 | P0 | **FIXED** | `t13` — `instance` required, tenant+subject predicates, resume re-checks the instance | final review `t36` |
| SEC-03 | P0 | **VERIFIED** | `t9`+`t31` — tool+instance in the CAS, DB-backed regression, summary redaction | `t21` needs_revision → `t31` repair → `t32` pass (mutation-tested) |
| SEC-04 | P1 | DEFER | Approval CLI trusts self-asserted `-as` | see limitations |
| SEC-05 | P0 | **FIXED** | `t12`+`t23`+`t30` — ledger, idempotency keys, pre-flight state checks | final reviews `t36`/`t37` |
| SEC-06 | P1 | **FIXED** | `t30` — denied audited as `deny`; transition+audit share a transaction | final review `t36` |
| SEC-07 | P1 | **FIXED** | `t9` — approver sees instance and a redacted argument summary | `t32` pass |
| SEC-08 | P1 | **FIXED** | `t22` — `Cf-Access-Jwt-Assertion` via the same verifier, explicit precedence | final review `t36` |
| SEC-09 | P1 | **FIXED** | `t22` — role provenance documented; unknown role fails closed | final review `t36` |
| SEC-10 | P2 | DEFER | Bounded upstream text reaches the agent | limitations |
| SEC-11 | P2 | **FIXED** | Instance-aware rate limiting and concurrency cap (`internal/magento/limits.go`, config `limits:`) | covered by `limits_test.go` + config validation |
| SEC-12 | P2 | DEFER | `/metrics` and `/healthz` unauthenticated on the public listener | limitations |
| SEC-13 | P2 | **FIXED** | `t20` — non-loopback + auth disabled is refused; startup warns loudly | `t20` |
| SEC-14 | P2 | **FIXED** | `t9` — canonical hashing and integer precision preserved | `t32` |
| SEC-15 | P2 | DEFER | Session not bound to identity | limitations |
| SEC-16 | P3 | **FIXED** | `t19` — `platform.info` is a first-class registry tool | final review `t39` |
| SEC-17 | P3 | DEFER | Inline credential strings, no redacting type | limitations |
| SEC-18 | P3 | **FIXED** | `t17`/`t27` — size checked before decode | `t39` |
| SEC-19 | P2 | **FIXED** (merged) | `t11` — every job transition is guarded | `t38` pass on fencing |

## 4. Magento findings

| ID | Sev | Status | Fix | Review |
|---|---|---|---|---|
| MAG-01 | P0 | **FIXED** | `t23` — pre-flight invoice read, zero POST when paid, UNKNOWN on timeout | `t37` pass (revert-proven) |
| MAG-02 | P1 | **FIXED** | `t23` — workflow route honours `capture`; state reported truthfully | `t37` pass |
| MAG-03 | P1 | **FIXED** | `t23` — invoice linkage, refundable-quantity check, minor units | `t37` pass |
| MAG-04 | P0 | **VERIFIED** | `t23`+`t40` — ledger, cart retained, exact `quote_id` reconciliation | `t37` found **MAG-R02 (P1)**; `t40` fixed it; `t41` PASS with a revert experiment |
| MAG-05 | P1 | **FIXED** | `t24` — staged workflow, precheck, verification, honest partial | `t37` pass |
| MAG-06 | P1 | **FIXED** | `t10` — whole SKUs only, accurate omitted count | `t15` pass |
| MAG-07 | P1 | **FIXED** | `t24` — orphan detection and repair on resume | `t37` pass |
| MAG-08 | P1 | **FIXED** | `t10` — stock-status failure surfaced with the stage | `t15` pass |
| MAG-09 | P1 | **FIXED** | `t10` — parent stock derived from observed children | `t15` pass |
| MAG-10 | P1 | **FIXED** | `t16` — compare-before-write, read-back verification | `t25` pass |
| MAG-11 | P1 | **FIXED** | `t16`+`t28` — partial named, `applied_rows=unknown`, fractional refused | `t25` pass + `t28` |
| MAG-12 | P2 | **FIXED** | `t28` — fractional quantity refused with a typed error | `t28` |
| MAG-13 | P2 | **FIXED** | `t16` — page-capped reads cannot drive a write | `t25` pass |
| MAG-14 | P1 | **FIXED** | `t10` — integer entry id accepted; fixtures corrected | `t15` pass |
| MAG-15 | P2 | DEFER | Media dedupe/ledger | limitations |
| MAG-16 | P2 | **FIXED** | Fallback no longer fabricates an option: an unconfirmed write returns `conflict`, an unreadable read-back returns `upstream_unavailable` | `TestCreateProductOptionDoesNotFabricateOnMissingReadBack`, `TestCreateProductOptionReadBackFailureIsNotSuccess` |
| MAG-17 | P2 | FIXED | Stage errors now carry `upstream_status`, `upstream_message` (bounded) and `store_code`, and a stage failure is never retryable | `TestStageErrorPreservesUpstreamDiagnostics` |
| MAG-18 | P2 | **FIXED** | `t30` — 5xx on a non-idempotent write is UNKNOWN, not retryable | `t38` |
| MAG-19 | P2 | **FIXED** | `store_id` is refused with `unsupported_capability` instead of being silently dropped; the store is selected by the instance's `store_code` | `TestCreateOrderRejectsStoreID` (asserts zero upstream calls) |
| MAG-20 | P1 | FIXED | Addresses carry `region_id`; Magento rejects Polish addresses without it, so order creation could not succeed | `TestOrderAddressCarriesRegionID` |
| MAG-21 | P2 | FIXED | `writableTypeIDs` and the schema enum now offer only `simple`/`virtual`; the other types are refused with the missing capability named | `TestCreateProductRejectsBundleAndDownloadable` |
| MAG-22 | P3 | DEFER | Grouped product creation | limitations |
| MAG-23 | P1 | DEFER | No Magento integration coverage | limitations §1.1 — the largest residual risk |
| MAG-24 | P3 | DOC | Configurable price relationship | `t34` |
| MAG-25 | P3 | DOC | Capability matrix said sales read-only | `t33` |
| MAG-26 | P3 | DEFER | Numeric attribute option id | limitations |

## 5. Reliability findings

| ID | Sev | Status | Fix | Review |
|---|---|---|---|---|
| REL-01 | P0 | **VERIFIED** | `t11` — lease token, guarded transitions, recovery | `t38` pass (revert-proven) |
| REL-02 | P0 | **VERIFIED** | `t12` ledger + `t30` gate + `t42` decision switch + `t46` retry terminality | `t38` found **REL38-01 (P0)**; `t42` fixed it; `t43` found **REL43-01 (P1)**; `t46` fixed it; `t48` PASS; `t52` consolidated PASS |
| REL-03 | P1 | **VERIFIED** | `t11` — lease ≥ timeout, heartbeat renewal | `t38` |
| REL-04 | P1 | **VERIFIED** | `t11` — canceled cannot be resurrected | `t38` |
| REL-05 | P1 | **VERIFIED** | `t11` — cancel delivered to the handler | `t38` |
| REL-06 | P1 | **FIXED** | `t30` — retry gate: attempts ∧ idempotent ∧ retryable ∧ not-unknown | `t38` found ambiguity edges → `t42` |
| REL-07 | P1 | **FIXED** | `t11`/`t30` — cancel/timeout no longer reported as success | `t38` |
| REL-08 | P1 | **VERIFIED** | `t11` — shutdown-safe terminal writes | `t38` |
| REL-09 | P2 | **VERIFIED** | `t11` — bounded, ordered, `SKIP LOCKED` recovery | `t38` |
| REL-10 | P2 | **VERIFIED** | `t11` — recovery respects `max_attempts` | `t38` |
| REL-11 | P2 | **FIXED** | A retried job resumes its open batch instead of opening a second one; the by-job lookup orders by `created_at DESC, id DESC` | `TestCreateImportBatchResumesOpenBatch` |
| REL-12 | P2 | DEFER | `import_batches` has no lease | limitations |
| REL-13 | P2 | **VERIFIED** | `t11` — lookup matches the real unique index | `t38` |
| REL-14 | P2 | **VERIFIED** | `t11` — Cancel reports only real transitions | `t38` |
| REL-15 | P2 | **VERIFIED** | `t11` — suite is repeatable on a dirty database | `t38` (ran twice) |
| REL-16 | P2 | **PARTIAL** | `t30` — atomic for 50 gated tools; 5 self-ledgered tools remain (`REL-16-R`) | `t38` confirmed bounded |
| REL-17..20 | P3 | **VERIFIED** | `t11` — backoff, transactional enqueue, `canceling` state, migrate connection | `t38` |
| REL-21 | P3 | DOC | Threat model described a ledger that did not exist | `t33` |

## 6. Architecture findings

| ID | Sev | Status |
|---|---|---|
| ARCH-01 | P0 | **VERIFIED** → `SEC-01` |
| ARCH-02 | P1 | **FIXED** — retry honours retryability and idempotency (`t30`) |
| ARCH-03 | P0 | **FIXED** → `SEC-02` |
| ARCH-04 | P1 | **FIXED** → `REL-02`; `idempotency_keys` superseded by the ledger |
| ARCH-05 | P2 | **FIXED** — `operation_unknown` added; `idempotency_key_reuse` reachable |
| ARCH-06 | P2 | **VERIFIED** → `SEC-03` |
| ARCH-07 | P3 | **FIXED** — `NewMCP` refuses to start when a confirmation-requiring tool is registered and no service is supplied |
| ARCH-08 | P1 | **VERIFIED** → `SEC-03` |

## 7. MCP findings

| ID | Sev | Status |
|---|---|---|
| MCP-01 | P0 | **VERIFIED** → `SEC-01` |
| MCP-02 | P2 | **FIXED** (`t19`) — `platform.info` is a registry tool reporting truth |
| MCP-03 | P1 | **FIXED** (`t19`) — registry-level write gate; import tools split |
| MCP-04 | P2 | **FIXED** (`t19`) — schemas compiled and closed at registration |
| MCP-05 | P2 | **FIXED** (`t19`) — declared timeout enforced as a deadline |
| MCP-06 | P2 | **FIXED** (`t18`) — 55 write disclosures, test-enforced |
| MCP-07 | P2 | PARTIAL — documented; full schema polish deferred |
| MCP-08 | P2 | DEFER — no output-schema envelope |
| MCP-09 | P2 | **FIXED** (`t18`+`t19`) — one envelope shape, code once, no SQLSTATE leak |
| MCP-10 | P3 | DEFER — naming register recorded |
| MCP-11 | P2 | **FIXED** (`t19`) — `order.summary` is a distinct deprecated alias |
| MCP-12 | P2 | **FIXED** (`t18`) — server instructions/grouping |
| MCP-13 | P3 | DEFER — capability metadata |
| MCP-14 | P3 | **FIXED** (`t18`) — generated inventory |
| MCP-15 | P3 | DEFER — no provenance envelope |
| MCP-16 | P3 | DEFER — annotation hints |

**Workflow-tool decision.** All three proposed tools
(`configurable.create_complete`, `inventory.reconcile`, `order.fulfillment`)
are **rejected for this release**: the first duplicates an existing tool, and the
other two would present a multi-call money/inventory sequence as one atomic
operation. Recorded in `known-limitations.md`.

## 8. Performance findings

| ID | Sev | Status |
|---|---|---|
| PERF-01 | P1 | **FIXED** — lease/timeout alignment (`t11`) + configurable import timeout |
| PERF-02 | P1 | **VERIFIED** → `REL-03` |
| PERF-03 | P2 | **FIXED** → `MCP-05` |
| PERF-04 | P2 | **FIXED** (`t17`) — tuned `Transport`; 8 connections for 160 requests vs 41–64 |
| PERF-05 | P2 | **FIXED** — per-instance token bucket + concurrency cap (`t53`) |
| PERF-06 | P2 | DEFER — 2 requests per import row |
| PERF-07 | P2 | **FIXED** (`t17`) — 1,000-order cap now reported |
| PERF-08 | P2 | **FIXED** (`t17`) — `truncated` reflects reality |
| PERF-09 | P2 | **FIXED** (`t17`) — deterministic paging with an explicit caveat |
| PERF-10 | P2 | **FIXED** (`t17`) — unscanned remainder reported |
| PERF-11 | P2 | **FIXED** (`t17`) — explicit typed error instead of silent truncation |
| PERF-12 | P2 | **FIXED** (`t27`) — 8 MiB ceiling restored, body cap 16 MiB, drift guard |
| PERF-13 | P2 | **FIXED** (`t11`) — claim query indexed |
| PERF-14 | P2 | **FIXED** — the worker's retention loop prunes terminal jobs (`Store.PruneJobs`, 30-day window); queued and running jobs are never eligible |
| PERF-15..22 | P3 | DEFER |
| PERF-23 | P3 | DOC (`t34`) |

## 9. Documentation findings

| ID | Sev | Status |
|---|---|---|
| DOC-01 | P0 | **FIXED** (`t33`) — threat model A5 replaced with the real ledger controls |
| DOC-02 | P1 | **FIXED** (`t33`) — Phase 0 language removed |
| DOC-03 | P1 | **FIXED** (`t33`) |
| DOC-04 | P1 | **FIXED** (`t33`) — 31 cited paths and 84 cited tests verified present |
| DOC-05 | P1 | **FIXED** (`t30`) — audit integrity claims now match the code |
| DOC-06 | P1 | **FIXED** (`t20`) — quickstart works as shipped |
| DOC-07 | P1 | **FIXED** (`t33`) — version identity consistent, CHANGELOG 20/20 tags |
| DOC-08 | P2 | **FIXED** (`t18`) — generated inventory states per-configuration counts |
| DOC-09 | P2 | **FIXED** (`t18`) — stale one-instance claim replaced by generated truth |
| DOC-10 | P2 | IN PROGRESS (`t34`) |
| DOC-11 | P2 | IN PROGRESS (`t34`) |
| DOC-12 | P2 | IN PROGRESS (`t34`) |
| DOC-13 | P2 | **FIXED** (`t33`) |
| DOC-14 | P2 | **FIXED** (`t20`) |
| DOC-15 | P2 | IN PROGRESS (`t34`) |
| DOC-16 | P2 | IN PROGRESS (`t34`) |
| DOC-17 | P2 | IN PROGRESS (`t34`) |
| DOC-18 | P2 | **FIXED** (`t33` + `t34`) |
| DOC-19 | P2 | **FIXED** (`t33`) |
| DOC-20 | P2 | **FIXED** (`t19`) |
| DOC-21..26 | P3 | `t33` / `t34` |

## 10. Review gate

Every P0/P1 fix has been reviewed by a non-author, on bound file hashes, with at
least one revert experiment proving the guarding test is non-vacuous. The final
round is `t36` (security), `t37`→`t41` (Magento), `t38`→`t43` (reliability),
`t39` (MCP/config/performance). Verdicts and evidence live in
`independent-code-review.md`.

Two review rounds already changed the outcome materially and are worth recording:

- `t21` found that the SEC-03 fix was correct but that the shipped SQL CAS had no
  regression test — deleting the tool or instance predicate would have passed CI.
  `t31` added the DB-backed test; `t32` proved it non-vacuous by mutation.
- `t38` found that the ledger gate executed writes on `DecisionInProgress` and
  `DecisionRetry` because only `DecisionReplay` was handled — a reachable
  duplicate-mutation path in code that had already passed the authors' tests.
  `t42` repairs it.

Those two findings are the argument for the review gate existing at all.

## 11. Acceptance mapping

| # | Criterion | Findings | Status |
|---|---|---|---|
| 1 | No confirmed P0 | SEC-01..03, SEC-05, MAG-01, MAG-04, REL-01, REL-02 | **all eight VERIFIED**, re-proven together in `t52` |
| 2 | P0/P1 fixed or risk accepted | all P0/P1 | in progress |
| 3 | Multi-instance escalation tests pass | SEC-02, SEC-03 | yes (`t36`, `t32`, `t35`) |
| 4 | No duplicate charge or refund | SEC-05, MAG-01, MAG-03, MAG-04, REL-02, REL-06 | **verified**; `t52` re-proved capture, replay and retry safety together |
| 5 | Inventory transfer anomalies detectable | MAG-10, MAG-11 | yes (`t25`, `t28`, `t35`) |
| 6 | Configurable failure recoverable | MAG-05, MAG-06, MAG-07 | yes (`t24`, `t29`, `t37`) |
| 7 | Job recovers after worker crash | REL-01, REL-03, REL-08, REL-09 | yes (`t38`, `t35`) |
| 8 | MCP schema matches implementation | MCP-*, SEC-16 | yes (`t18`, `t19`, generated inventory) |
| 9 | Documentation matches reality | DOC-* | **done** — `t33`, `t34`, `t47`; threat model cites 31 real paths and 84 real tests, 0 missing |
| 10 | No unapproved regression | regression report | yes — full suite green, race clean |
| 11 | No new high-severity dependency | — | no dependency added at any point |
| 12 | Results reproducible | regression report | **yes** — per-package schema isolation; six consecutive parallel `-race` runs, zero failures |

## 12. Change log

| Date | Change |
|---|---|
| 2026-11-20 | Initial plan from the three completed domain audits. |
| 2026-11-20 | Full plan with all seven areas, outcomes, and the two review findings that changed the result. |
| 2026-11-20 | Final: all eight P0s verified, consolidated review `t52` PASS, test-isolation defect fixed, gate deterministic. |
