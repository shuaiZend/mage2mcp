# Reliability review — job queue, leases, idempotency, operation ledger

**Area**: `REL-*` · **Owner**: Reliability engineer · **Baseline commit**: `66786a5`
**Scope**: `internal/jobs/*.go`, `internal/storage/{jobqueue,storage,audit,confirm,import_batches}.go`,
`internal/storage/migrations/*.sql`, `cmd/worker/main.go`, `internal/magento/import_jobs.go`,
plus the call sites that decide whether an upstream mutation is safe to retry.
**Method**: code reading against a real PostgreSQL 16.15 instance, plus probe tests that
assert the property the system *should* have (`internal/storage/probe_reliability_test.go`,
`internal/jobs/probe_reliability_test.go`, build tag `probe`, excluded from CI).

---

## 1. Executive summary

The queue is a competent *at-least-once* work distributor. It is **not** a
*crash-safe mutation engine*, and the objective ("financial and inventory mutations
safe under crash and concurrency") is not met.

Three facts dominate this review:

1. **There is no fencing.** `Claim()` stamps `attempt_count`, but `Complete()`,
   `Fail()`, `Extend()` and `Cancel()` are unguarded single-row `UPDATE`s keyed only
   on `id` (`internal/storage/jobqueue.go:155-206`). A worker whose lease expired
   keeps writing to a row another worker now owns. This is reproduced below
   (REL-01): a stale attempt 1 marked a job `succeeded` while attempt 2 was still
   running, and the live worker's heartbeat then silently affected 0 rows.
2. **There is no ledger and no UNKNOWN state.** The closest thing to a ledger is
   `audit_events`, whose `state` column *allows* `outcome_unknown`
   (`000001_init.up.sql:72-73`) and which **nothing ever writes**; the MCP
   middleware collapses every error to `failed` (`internal/mcp/server/server.go:110-114`)
   even though the Magento client's own error says *"the call may have completed"*
   (`internal/magento/client.go:153`). `idempotency_key` exists only on `jobs`
   (`000001_init.up.sql:100,116-118`); the `idempotency_keys` table
   (`000001_init.up.sql:122-130`) has no writer and no reader.
3. **Retries are blind.** The queue cannot distinguish "failed before the upstream
   write" from "failed after it", so a retry re-issues the mutation (REL-06,
   reproduced: 2 upstream writes for one job). `magento.order.create` is a 4-step
   saga (`internal/magento/order_create.go:67-93`) whose final step
   `POST /carts/:id/order` on timeout deletes the cart and returns an error — the
   order may exist, the cart is gone, and a retry creates a second order.

**What is genuinely correct**, and should not be re-litigated: `FOR UPDATE SKIP
LOCKED` gives disjoint claims (probe: 40 jobs, 8 workers, 40 distinct claims, 0
duplicates); confirmation `Consume` is a true compare-and-set (probe: 8 concurrent
consumers, exactly 1 succeeded); every `rows` handle in the in-scope packages is
closed and `rows.Err()`-checked (5/5 sites); migrations are idempotent under an
advisory lock.

### Finding counts

| Severity | Count | IDs | Fixed in t11 |
|---|---|---|---|
| **P0** | **2** | REL-01, REL-02 | 1 (REL-01) |
| **P1** | **6** | REL-03 … REL-08 | 4 (REL-03/04/05/08) |
| **P2** | **8** | REL-09 … REL-16 | 4 (REL-09/10/13/14) |
| **P3** | **4** | REL-17 … REL-20 | 4 (REL-17/18/19/20) |
| *duplicate of SEC-05 (not counted)* | *1* | *REL-21* | — |
| **Total unique** | **20** | | **13 + REL-15** |

**Status after t11.** Every queue-integrity finding is fixed and has a permanent
regression test against real PostgreSQL: REL-01, REL-03, REL-04, REL-05, REL-08,
REL-09, REL-10, REL-13, REL-14, REL-17, REL-18, REL-19, REL-20, plus REL-15 (test
isolation, fixed as a prerequisite for a green run). The operation-ledger findings
remain open and are the next task: **REL-02** (no ledger, no UNKNOWN state),
**REL-06** (blind retry re-issues the upstream mutation), **REL-07** (import
cancel/timeout swallowed as per-row failure), **REL-11** (duplicate import batch
rows), **REL-12** (no batch recovery), **REL-16** (un-transacted audit
intent/outcome). REL-21 is documentation, tracked under SEC-05.

### Release verdict from the reliability layer

**The queue is now crash-safe; the mutation layer is not yet.** As of t11 the
queue can be trusted to execute a task at most once per attempt, to fence a stale
worker out, to honour cancellation, to survive a deploy, and to dead-letter a
poison-pill job. What it still cannot do is tell a retry whether the previous
attempt already changed the upstream system — that is REL-02/REL-06, and until the
operation ledger lands, **any write capability that moves money or inventory
remains not production-ready**. Read paths are unaffected. Catalog-only payloads
(`platform:noop`, `magento.import`) may ship: the duplicate-execution and
lost-cancellation paths that made them unsafe are closed, though a retried import
is still re-executed from the top (REL-06) and can create a second batch row
(REL-11).

---

## 2. Findings

### REL-01: No fencing token — a stale worker's Complete/Fail/Extend wins over the live attempt

- **Severity**: P0
- **State**: ALREADY-FIXED
- **Fixed by** (t11): Migration `000005_job_fencing.up.sql` adds `jobs.lease_token` plus the `jobs_lease_token_chk` invariant; every outcome write shares `jobLeaseGuard` (`internal/storage/jobqueue.go:49`) and returns `ErrLeaseLost`/`conflict` when it matches no row. Tests: `TestJobQueueStaleAttemptCannotWrite`, `TestWorkerLeaseLostDiscardsResult`.
- **Location**: `internal/storage/jobqueue.go:155-163` (`Complete`), `:167-184` (`Fail`),
  `:187-195` (`Extend`), `:198-206` (`Cancel`); `internal/storage/migrations/000003_job_lease.up.sql:5`
  (no lease/attempt token column); `internal/jobs/jobs.go:68-71` (interface has no attempt argument)
- **Evidence**: The queue increments `attempt_count` at claim (`jobqueue.go:142-147`)
  and returns it on the `Task` (`jobs.go:59`), but no write path ever checks it:

  ```sql
  -- Complete(), jobqueue.go:156-158
  UPDATE jobs SET state = 'succeeded', finished_at = now(), lease_expires_at = NULL, updated_at = now()
   WHERE id = $1::uuid
  ```

  There is no `AND state = 'running'`, no `AND attempt_count = $2`, no lease token.
  The same is true of `Fail` and `Extend` (`Extend` has `AND state='running'`, which
  protects the *new* worker's row, not the old worker's claim). All four methods
  also ignore `RowsAffected`.
- **Trigger**: any handler that runs longer than its lease — a GC pause, a blocked
  syscall, a 5000-row import, a CPU-starved container. The default lease is 2
  minutes and the default task timeout is 5 minutes (`jobs.go:95-100`), so this is
  the *normal* case for a large import, not an edge case.
- **Impact**: two workers execute the same mutation; whichever finishes first
  records the terminal state, and the second worker's write silently overwrites it
  (lost update on `state`, `finished_at`, `last_error_code`). If the stale worker
  completes, the live worker's `Extend` no longer matches a `running` row, so its
  heartbeat is dead and it will itself be recovered and re-executed. For a money
  mutation this is a duplicate charge with a "succeeded" audit record.
- **Reproduced** (`TestProbeStaleWorkerCannotWriteAfterLeaseRecovery`):

  ```text
  === RUN   TestProbeStaleWorkerCannotWriteAfterLeaseRecovery
      probe_reliability_test.go:70: job 1f2622fb-… claimed twice: A attempt=1, B attempt=2
      probe_reliability_test.go:80: after stale Complete: state=succeeded attempt_count=2
      probe_reliability_test.go:82: FENCING DEFECT: stale worker (attempt 1) marked job 1f2622fb-… succeeded
          while attempt 2 is still running; the live worker's Extend()/Fail() now apply to a finished row
  --- FAIL: TestProbeStaleWorkerCannotWriteAfterLeaseRecovery (0.45s)
  ```

  Independently confirmed with the raw SQL from `jobqueue.go` against PostgreSQL
  16.15: after worker A's stale `Complete`, worker B's `Extend` affected **0 rows**
  and returned `nil`.
- **Proposed fix**: add a per-claim fencing token and require it on every write.
  1. `ALTER TABLE jobs ADD COLUMN lease_token uuid NULL;`
  2. `Claim()` sets `lease_token = gen_random_uuid()` in the same `UPDATE`, returns
     it on `Task`.
  3. `Complete(ctx, id, token)`, `Fail(ctx, id, token, …)`, `Extend(ctx, id, token, …)`
     add `AND lease_token = $2 AND state = 'running'`, and return
     `(applied bool, err error)`; `applied == false` means "you lost the lease" and
     the worker must log and discard its result rather than retry.
  4. `Cancel` keeps its `state IN ('queued','running')` guard but must also clear
     `lease_token`.
- **Test requirement**: `TestJobQueueRejectsStaleComplete` — claim with a short
  lease, expire it, claim again, then assert the first claim's
  `Complete/Fail/Extend` return `applied=false` and leave `state`/`attempt_count`
  untouched. (The probe file already encodes this assertion.)
- **Cross-reference**: SEC-19 files the same unguarded statements as a P2
  task-state-integrity defect. This finding is the lease-expiry path, which needs no
  privileged access; merge the two under REL-01.
- **Verified by**: *pending independent review*

---

### REL-02: No operation ledger and no UNKNOWN state — a timed-out money mutation is recorded as "failed" and is unsafe to retry

- **Severity**: P0
- **State**: CONFIRMED
- **Location**: `internal/storage/migrations/000001_init.up.sql:72-73,100,122-130`;
  `internal/mcp/server/server.go:110-114`; `internal/magento/client.go:152-156`;
  `internal/storage/audit.go:26-51`; `internal/storage/migrations/000004_import_batches.up.sql:10-11`
- **Evidence**: Three separate gaps, none of which alone is fatal, together
  producing an unrecoverable state:

  1. The schema *has* an `outcome_unknown` audit state and nothing writes it:

     ```sql
     state text NOT NULL DEFAULT 'pending'
           CHECK (state IN ('pending','succeeded','failed','denied','outcome_unknown','degraded'))
     ```

     (`grep -rn 'outcome_unknown' --include=*.go internal` → no hits.)
  2. The MCP middleware maps every error to `failed`:

     ```go
     state, code := "succeeded", ""
     if err != nil {
         state, code = "failed", string(errs.CodeOf(err))
     }
     ```

     (`server.go:110-114`), including `CodeUpstreamTimeout`, whose constructor text is
     *"Magento did not answer before the deadline; the call may have completed"*
     (`client.go:153`).
  3. `idempotency_key` exists only on `jobs`. The `idempotency_keys` table
     (`000001_init.up.sql:122-130`) has neither a writer nor a reader
     (`grep -rn idempotency_keys --include=*.go` → only the migration).
- **Trigger**: `POST /invoices/{id}/capture` (`invoice_write.go:109`),
  `POST /creditmemos` (`order_documents_write.go:108`), `POST /orders/{id}/cancel`
  (`order_status_write.go:105`), `POST /inventory/source-items`
  (`inventory.go:63`), or `POST /carts/{id}/order` (`order_create.go:93`) exceeds the
  30 s client deadline after Magento accepted the request. The operator sees
  `upstream_timeout`, the audit row says `failed`, and the only way to find out what
  happened is to log into Magento by hand.
- **Impact**: the operator's rational response — retry — duplicates the charge,
  refund, cancellation or inventory movement. There is no record from which a
  reconciler could later determine the truth, and no idempotency key Magento could
  honour (Magento's REST API has no idempotency header, so a ledger plus a
  read-back reconciler is the only viable design). `magento.order.create` is worse:
  on a `place_order` timeout it deletes the cart (`order_create.go:95`) and returns
  an error, destroying the correlation handle while the order may exist.
- **Proposed fix**: implement the ledger in §3 and route every money/inventory/
  order mutation through it. Minimum viable slice: the `operations` table, the
  `PENDING→EXECUTING→{SUCCEEDED,FAILED,UNKNOWN}` transitions with a fence guard, and
  the `UNKNOWN→RECONCILING` worker for `invoice.capture`, `creditmemo.create`,
  `order.cancel` and `inventory.*`.
- **Test requirement**: §4 rows F1–F8. In particular a fault-injection test that
  makes the upstream return 200 and then kills the process before the local write,
  asserting the operation is `UNKNOWN` (not `FAILED`) and that a second call with the
  same idempotency key is refused until reconciliation completes.
- **Verified by**: *pending independent review*

---

### REL-03: Lease (2 min) is shorter than the task timeout (5 min) and nothing renews the lease — long handlers always execute twice

- **Severity**: P1
- **State**: ALREADY-FIXED
- **Fixed by** (t11): `WorkerOptions.defaults` forces `Lease > TaskTimeout` (default 6m vs 5m) and derives a heartbeat interval; `Worker.heartbeat` renews the lease while the handler runs and stops it if the lease is lost. Tests: `TestWorkerLeaseOutlivesTaskTimeout`, `TestWorkerRenewsLeaseWhileHandlerRuns`.
- **Location**: `internal/jobs/jobs.go:95-100` (defaults), `:170-192` (`execute` never
  calls `Extend`); `internal/storage/jobqueue.go:187-195` (`Extend` exists, no caller)
- **Evidence**:

  ```go
  if o.Lease <= 0 { o.Lease = 2 * time.Minute }
  if o.TaskTimeout <= 0 { o.TaskTimeout = 5 * time.Minute }
  ```

  `cmd/worker/main.go:82` passes only `Logger`, so both defaults are live.
  `Worker.execute` wraps the handler in a 5-minute context and calls
  `Complete`/`Fail` when it returns — it never calls `Extend`. `grep -rn '\.Extend('
  --include=*.go internal cmd` → **no call sites** (only the interface declaration,
  the implementation, and the test fake).
- **Trigger**: any handler slower than 2 minutes. The import path processes up to
  5000 rows with one `GET` and one `POST/PUT` per row (`internal/magento/import.go:348-408`);
  at 25 ms/row that is already 2 minutes.
- **Impact**: guaranteed concurrent execution of the same job — the exact
  precondition for REL-01's lost update and REL-06's duplicate writes. For imports
  it means two workers creating/updating the same SKUs simultaneously, duplicate
  `import_batches` rows (REL-11) and Magento "URL key already exists"-class failures
  attributed to the wrong attempt.
- **Proposed fix**: start a heartbeat goroutine per claimed task
  (`Extend(ctx, id, token)` every `lease/3`); if `Extend` reports `applied=false`,
  cancel the handler context immediately — the worker has lost the lease and its
  work will be re-done. Enforce at startup that `Lease > TaskTimeout` **or** that
  the heartbeat is enabled, so the invariant cannot regress by configuration.
- **Test requirement**: F9 — a handler that sleeps 3× the lease must be extended
  and must complete exactly once with no second claim.
- **Verified by**: *pending independent review*

---

### REL-04: `Fail(retry=true)` has no state guard and resurrects a canceled job

- **Severity**: P1
- **State**: ALREADY-FIXED
- **Fixed by** (t11): `failSQL` re-queues only from `state='running'` with budget left and maps a `canceling` row to `canceled`, so a late `Fail(retry=true)` can never resurrect a canceled job. Tests: `TestJobQueueCancelIsTerminal`, `TestJobQueueFailNeverRequeuesBeyondMaxAttempts`.
- **Location**: `internal/storage/jobqueue.go:167-184`; `internal/jobs/jobs.go:186-191`
- **Evidence**:

  ```sql
  UPDATE jobs SET state = $2, last_error_code = NULLIF($3,''), lease_expires_at = NULL,
         scheduled_at = now() + $4::interval,
         finished_at = CASE WHEN $2 = 'failed' THEN now() ELSE NULL END, updated_at = now()
   WHERE id = $1::uuid
  ```

  `$2` is `'queued'` when `retry` is true. No `AND state IN ('running')`, no
  attempt guard.
- **Trigger**: operator calls `magento.import.cancel` while the import runs; the
  worker's handler then fails (or times out) and the worker calls
  `Fail(..., retry=true)`.
- **Impact**: the cancel is silently undone, `finished_at` is cleared, and the job
  runs again — a business operation the operator explicitly aborted.
- **Reproduced** (`TestProbeCancelIsNotUndoneByStaleFail`):

  ```text
      probe_reliability_test.go:114: after Cancel: state=canceled
      probe_reliability_test.go:124: after stale Fail(retry=true): state=queued
      probe_reliability_test.go:126: CANCEL DEFECT: job 99487cea-… was canceled but a stale
          Fail(retry=true) moved it to "queued", so it will execute again
  --- FAIL: TestProbeCancelIsNotUndoneByStaleFail (0.02s)
  ```

- **Proposed fix**: `AND state = 'running' AND lease_token = $2` on `Fail`; a fenced
  `Fail` returns `applied=false` and the worker logs "result discarded, lease lost".
  Additionally, `Fail` must never move a row out of a terminal state.
- **Test requirement**: covered by the same fencing test as REL-01 plus an explicit
  cancel-then-fail case.
- **Verified by**: *pending independent review*

---

### REL-05: `Complete` overwrites `canceled`, and cancellation never reaches the handler

- **Severity**: P1
- **State**: ALREADY-FIXED
- **Fixed by** (t11): Two-phase cancel: `cancelSQL` moves `queued→canceled` and `running→canceling`; the heartbeat reports the cancel (`LeaseStatus.CancelRequested`) and cancels the handler context; `Complete`/`Fail` are fenced and `succeeded`/`failed`/`canceled` are terminal. Tests: `TestJobQueueCancelIsTerminal`, `TestWorkerCancelRequestStopsHandler`.
- **Location**: `internal/storage/jobqueue.go:155-163`, `:198-206`;
  `internal/jobs/jobs.go:170-192`; `internal/magento/import_jobs.go:172-185`
- **Evidence**: `Cancel` sets `state='canceled'` for `queued`/`running` rows, but the
  running handler is never told: no context cancellation, no `canceling` transition,
  no polling of the row. When the handler finishes, `execute` calls `Complete`, whose
  `WHERE id = $1::uuid` matches the canceled row and flips it to `succeeded`. The
  `canceling` value in the state CHECK (`000001_init.up.sql:95`) is written by
  nothing (`grep -rn "'canceling'"` → only the migration).
- **Trigger**: `magento.import.cancel` on a running import; the import then runs to
  completion.
- **Impact**: the operator's cancel is a lie in the API response *and* in the final
  state; a 5000-row import continues to write to the catalog after being canceled.
  `import.cancel` also returns `canceled:true` unconditionally, even for a terminal
  job (REL-14).
- **Reproduced** (`TestProbeCompleteDoesNotOverwriteCanceled`):

  ```text
      probe_reliability_test.go:156: after Cancel then Complete: state=succeeded
      probe_reliability_test.go:158: CANCEL DEFECT: canceled job 97f6d6ab-… reported "succeeded"
          after the worker completed; the operator's cancel was silently overwritten
  --- FAIL: TestProbeCompleteDoesNotOverwriteCanceled (0.02s)
  ```

- **Proposed fix**: two-phase cancel. `Cancel` moves `running`→`canceling` and sets a
  `cancel_requested_at`; the heartbeat (REL-03) observes `canceling` and cancels the
  handler context; the handler returns a typed `ErrCanceled`; `Fail(..., 'canceled')`
  moves `canceling`→`canceled` under the fence guard. `Complete` must require
  `state='running'`.
- **Test requirement**: F10 — cancel mid-handler must end in `canceled` with no
  further upstream writes after the cancel is observed.
- **Verified by**: *pending independent review*

---

### REL-06: Retries re-issue the upstream mutation; a local write failure after a successful import retries the whole import

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `cmd/worker/main.go:93-146`; `internal/jobs/jobs.go:186-191`;
  `internal/storage/jobqueue.go:167-184`
- **Evidence**: the import handler performs the upstream work, finalizes the batch,
  then ends with `return queue.SetResult(ctx, task.ID, string(raw))`
  (`main.go:145`). If that last `UPDATE jobs SET result_ref` fails (transient
  connection error, failover, pool exhaustion), the handler returns an error, the
  worker computes `retry := task.Attempts < task.MaxAttempts` (`jobs.go:186`) and
  calls `Fail(retry=true)` — re-running the *entire* import, including every create
  and update, even though the batch row already says `succeeded`.
- **Trigger**: a `SetResult` error after a successful 5000-row import; or any handler
  error raised after the upstream write.
- **Impact**: duplicate upstream writes on every retry; for the import path, creates
  are only "convergent" because `Create` is guarded by a preceding `Get`
  (`import.go:348-360`) — which is a read-then-write with no lock, so two concurrent
  attempts (REL-03) can both see `NotFound` and both create. For a money tool there
  is no such convergence at all.
- **Reproduced** (`TestProbeRetryReappliesBusinessEffect`):

  ```text
      probe_reliability_test.go:296: after first failed attempt: state=queued attempts=1
      probe_reliability_test.go:304: retry claimed: id=99487cea-… attempt=2
      probe_reliability_test.go:308: upstream writes performed for job e0ae93e6-…: 2
      probe_reliability_test.go:310: DUPLICATE WRITE: job e0ae93e6-… performed 2 upstream mutations;
          nothing in the queue or storage records that attempt 1 may already have succeeded upstream
  --- FAIL: TestProbeRetryReappliesBusinessEffect (5.24s)
  ```

- **Proposed fix**: separate "the work failed" from "recording the result failed".
  A result-recording failure must not re-run the handler: log it and leave the job
  `running` so the lease expiry path can *reconcile* rather than *re-execute*
  (which requires REL-02's ledger to be meaningful). Long term: the handler writes
  the ledger outcome and the job result in one transaction; the queue state becomes
  a projection of the ledger.
- **Test requirement**: F11 — a queue stub whose `SetResult`/`Complete` fails once
  must not cause a second upstream call.
- **Verified by**: *pending independent review*

---

### REL-07: Import cancellation and timeout are swallowed as per-row failures — the job reports success

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `internal/magento/import.go:348-408` (loop and `default:` branch at
  `:402-404`), `cmd/worker/main.go:123-145`
- **Evidence**: the per-row loop never checks `ctx.Err()`. A canceled/deadline-exceeded
  context makes `s.Get` return a context error, which falls into:

  ```go
  default:
      outcome.Action, outcome.Status, outcome.Error = "lookup", "failed", importErrorMessage(getErr)
      result.Failed++
  ```

  and the loop **continues**, doing one doomed HTTP call per remaining row. The
  function then returns `result, nil` (`import.go:407`), so the worker finalizes the
  batch as `partial` and returns `nil` → `Complete` → job `succeeded`.
- **Trigger**: `TaskTimeout` (5 min) or `Cancel` on a long import. (Note that Cancel
  does not cancel the context at all — REL-05 — so today this fires on timeout and
  on worker shutdown.)
- **Impact**: a timed-out import is reported as `succeeded` with every remaining row
  marked failed; no retry is attempted; the operator's only signal is a "partial"
  batch whose failure reason is `internal error` (`importErrorMessage` maps
  non-`errs.Error` to that string, discarding the context error). With 5000 rows and
  a dead upstream, this also burns the remaining task budget on doomed calls.
- **Proposed fix**: check `ctx.Err()` at the top of the loop and return
  `(result, ctx.Err())`; the worker then records `unknown`/`failed` rather than
  `succeeded`. Distinguish `context.Canceled` (operator) from `DeadlineExceeded`
  (timeout) in the batch `error_code`.
- **Test requirement**: F12 — a context canceled after N rows must return an error,
  mark the batch `failed`/`canceled`, and stop issuing upstream calls.
- **Verified by**: *pending independent review*

---

### REL-08: Graceful shutdown cannot record completion — every deploy re-executes the in-flight task

- **Severity**: P1
- **State**: ALREADY-FIXED
- **Fixed by** (t11): The handler runs on `context.WithoutCancel(ctx)` bounded by `TaskTimeout`, and terminal writes use a detached 5s context, so a shutdown can no longer lose the outcome. Tests: `TestWorkerShutdownRecordsCompletion`, `TestWorkerShutdownRecordsFailure`.
- **Location**: `internal/jobs/jobs.go:177-191` (`Complete`/`Fail` are called with the
  signal context `ctx`, not a fresh one), `cmd/worker/main.go:54,151`
- **Evidence**: `main` derives `ctx` from `signal.NotifyContext`; `worker.Run(ctx)`
  passes it down; `execute` calls `w.queue.Complete(ctx, task.ID)` and
  `w.queue.Fail(ctx, task.ID, …)` with that same already-canceled context. pgx
  returns `context canceled` for any statement on a canceled context, so both writes
  fail. The worker only logs (`jobs.go:182,190`).
- **Trigger**: `SIGTERM` (rolling deploy, `docker compose down`, Kubernetes
  eviction) while a task is in flight — i.e. every single deployment.
- **Impact**: the row stays `running` with a live lease; after `lease_expires_at` the
  next worker recovers and re-executes the task. The handler has already performed
  part of the work (and, for imports, left a `running` batch row that nothing ever
  finalizes — REL-12). `go test -tags probe` proves it:
- **Reproduced** (`TestProbeShutdownLosesCompletion`):

  ```text
  === RUN   TestProbeShutdownLosesCompletion
  2026/11/20 21:29:30 WARN job failed job_id=j1 kind=platform:noop attempt=1 max_attempts=3 retry=true err="context canceled"
  2026/11/20 21:29:30 ERROR job failure record failed job_id=j1 err="context canceled"
      probe_reliability_test.go:83: after shutdown: complete=0 fail=0 (a terminal write was required)
      probe_reliability_test.go:85: SHUTDOWN DEFECT: neither Complete nor Fail was recorded for the
          in-flight task; the row stays 'running' until lease_expires_at, then the task is
          re-executed (complete=0 fail=0)
  --- FAIL: TestProbeShutdownLosesCompletion (0.00s)
  ```

- **Proposed fix**: use a short, independent context for terminal writes
  (`context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)`); if it still
  fails, `Fail(retry=false, 'shutdown')` is not safe either — leave the lease in place
  and let reconciliation decide. Do **not** silently abandon the write.
- **Test requirement**: F13 — cancel `Run` while a handler is in flight; assert
  exactly one terminal write reaches the queue, on a fresh context.
- **Verified by**: *pending independent review*

---

### REL-09: The recovery `UPDATE` in `Claim` is unbounded and unordered — reproducible deadlock (40P01) and cross-queue lock coupling

- **Severity**: P2
- **State**: ALREADY-FIXED
- **Fixed by** (t11): `recoverExpiredLeasesSQL` is bounded (`LIMIT 64`), ordered (`lease_expires_at, id`), non-blocking (`FOR UPDATE SKIP LOCKED`) and runs as its own statement outside the claim transaction. Tests: `TestJobQueueClaimContention` (8×40, no 40P01), `TestJobQueueClaimDoesNotWaitOnOtherQueues`, `TestJobQueueRecoveryBounded`, `TestJobQueueRecoveryPersistsWithoutClaimableWork`.
- **Location**: `internal/storage/jobqueue.go:121-126`
- **Evidence**:

  ```sql
  UPDATE jobs SET state = 'queued', lease_expires_at = NULL
   WHERE state = 'running' AND lease_expires_at IS NOT NULL AND lease_expires_at < now()
  ```

  No `LIMIT`, no `ORDER BY`, no `SKIP LOCKED`, and no restriction to the queues this
  worker serves. It runs at the head of **every** `Claim`, i.e. once per poll
  interval per worker goroutine.
- **Trigger (deadlock)**: two workers run `Claim` concurrently over a backlog of
  expired leases while their plans differ (one sequential scan, one index scan —
  realistic after an `ANALYZE`, a rolling deploy that straddles statistics, or
  differing `random_page_cost`). Each locks the rows it has already updated and
  waits for a row the other holds. Reproduced twice out of two attempts against
  PostgreSQL 16.15 with 3000 expired rows, using the exact statement from the code:

  ```text
  [B idxscan ] ERROR:  deadlock detected
  [B idxscan ] DETAIL:  Process 200 waits for ShareLock on transaction 1168; blocked by process 201.
  [B idxscan ] Process 201 waits for ShareLock on transaction 1169; blocked by process 200.
  [B idxscan ] CONTEXT:  while updating tuple (201,26) in relation "jobs"

  [A seqscan ] ERROR:  deadlock detected
  [A seqscan ] DETAIL:  Process 205 waits for ShareLock on transaction 1174; blocked by process 204.
  [A seqscan ] Process 204 waits for ShareLock on transaction 1173; blocked by process 205.
  [A seqscan ] CONTEXT:  while updating tuple (369,5) in relation "jobs"
  ```

  Two transactions (`1163`/`1164` in the first run) each hold row locks the other
  needs; PostgreSQL aborts one, so `Claim` returns `storage: recover expired
  leases: …` and the worker sleeps for a poll interval. Not corruption, but a
  spurious failure path on every deploy that restarts workers with work in flight.
- **Trigger (coupling)**: any backend holding a row lock on *any* expired running
  job blocks claims for *every* queue. Reproduced
  (`TestProbeClaimBlocksOnUnrelatedExpiredLease`): a lock on an expired `import` job
  made `Claim([]string{"report"})` wait **1.21 s**, even though a `report` job was
  claimable — `SKIP LOCKED` cannot help because the blocking statement is the
  recovery `UPDATE`, not the claim `SELECT`.
- **Impact**: claim latency and spurious errors that scale with the expired-lease
  backlog (worst exactly when the system is already unhealthy); deadlock errors in
  logs that look like infrastructure faults.
- **Proposed fix**: make recovery bounded and ordered, and scope it to this worker's
  queues:

  ```sql
  UPDATE jobs j SET state = 'queued', lease_expires_at = NULL
  FROM (
      SELECT id FROM jobs
       WHERE state = 'running' AND lease_expires_at < now() AND queue = ANY($1)
       ORDER BY lease_expires_at
       LIMIT 64
       FOR UPDATE SKIP LOCKED
  ) expired
  WHERE j.id = expired.id;
  ```

  Better still: move recovery out of the claim hot path into the same loop as the
  reconciler, or use `FOR UPDATE SKIP LOCKED` in the subquery so concurrent
  recoverers never contend.
- **Test requirement**: F14 — two concurrent claims over 1000 expired rows must both
  succeed, with no lock wait above a threshold and no 40P01; a lock on a row in
  queue A must not delay a claim for queue B.
- **Verified by**: *pending independent review*

---

### REL-10: Lease recovery ignores `max_attempts` — a poison-pill job is retried without bound

- **Severity**: P2
- **State**: ALREADY-FIXED
- **Fixed by** (t11): Recovery dead-letters a row whose `attempt_count >= max_attempts` as `failed`/`lease_expired` instead of re-queueing it. Tests: `TestJobQueueRecoveryRespectsMaxAttempts`, `TestJobQueueFailNeverRequeuesBeyondMaxAttempts`.
- **Location**: `internal/storage/jobqueue.go:121-135`; `internal/jobs/jobs.go:186`
- **Evidence**: the recovery `UPDATE` re-queues any expired `running` row regardless
  of `attempt_count`, and the claim `SELECT` has no `attempt_count < max_attempts`
  predicate. The only code that honours `max_attempts` is the worker's in-process
  retry decision, which is never reached when the worker *dies* instead of
  returning an error.
- **Trigger**: a job that kills its worker every time — OOM on a large payload,
  panic outside a recover, `SIGKILL` from the orchestrator's memory limit.
- **Impact**: the job loops `running → expired → queued → running` forever,
  incrementing `attempt_count` past `max_attempts`, never reaching a terminal state,
  and repeatedly taking down a worker. There is no dead-letter state and no alert.
- **Reproduced** (`TestProbeRecoveryIgnoresMaxAttempts`):

  ```text
      probe_reliability_test.go:445: claim 1: attempt=1 max_attempts=1
      probe_reliability_test.go:445: claim 2: attempt=2 max_attempts=1
      probe_reliability_test.go:445: claim 3: attempt=3 max_attempts=1
      probe_reliability_test.go:448: POISON PILL: job 7cf5990b-… was claimed at attempt 3 with
          max_attempts=1; a job whose worker dies every time is retried without bound and never
          reaches a terminal state
  ```

- **Proposed fix**: in the recovery statement, split the set — rows with
  `attempt_count >= max_attempts` go to `state='failed', last_error_code='lease_expired'`
  (a dead-letter state); the rest are re-queued. Add an `exhausted` terminal reason
  and an alert counter.
- **Test requirement**: F15 — expire a lease `max_attempts` times and assert the job
  ends `failed` with `last_error_code='lease_expired'` and is never claimed again.
- **Verified by**: *pending independent review*

---

### REL-11: Every retry creates a second `import_batches` row; `GetImportBatchByJob` returns an arbitrary one

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `cmd/worker/main.go:106` (`CreateImportBatch` on every attempt);
  `internal/storage/import_batches.go:64-100` (`GetImportBatchByJob`);
  `internal/storage/migrations/000004_import_batches.up.sql:5-26` (no unique
  constraint on `job_id`)
- **Evidence**: the handler runs `store.CreateImportBatch(ctx, payload.Instance, task.ID, …)`
  unconditionally at the top of every attempt. The lookup then does

  ```go
  err := s.pool.QueryRow(ctx, `SELECT … FROM import_batches WHERE job_id = $1::uuid`, jobID).Scan(…)
  ```

  `QueryRow` with a multi-row result returns an arbitrary row (PostgreSQL returns
  whatever the plan yields first) — there is no `ORDER BY` and no `LIMIT`.
- **Trigger**: any import that fails after `CreateImportBatch` and is retried
  (`MaxAttempts: 2` for imports, `import_jobs.go:143`), or the double execution from
  REL-03.
- **Impact**: `magento.import.resume` reads `batch.Failures` from an arbitrary
  attempt (`import_jobs.go:257-263`), so it can resume the wrong row set; the batch
  list shows two rows for one job with contradictory counts; `import.batch` by id
  shows one of them.
- **Reproduced** (`TestProbeRetryCreatesSecondImportBatch`):

  ```text
      probe_reliability_test.go:413: job 7d5f487d-… owns 2 batch rows (a1749dda-…, 1eec9ad6-…)
      probe_reliability_test.go:418: GetImportBatchByJob returned a1749dda-…
      probe_reliability_test.go:420: BATCH DUPLICATION: one job owns 2 import_batches rows;
          GetImportBatchByJob selects one of them with no ORDER BY, so import.resume may resume
          from the wrong attempt's failure set
  --- FAIL: TestProbeRetryCreatesSecondImportBatch (0.02s)
  ```

- **Proposed fix**: `UNIQUE (job_id)` on `import_batches` (one batch per job) and
  `INSERT … ON CONFLICT (job_id) DO UPDATE` in `CreateImportBatch` so a retry
  *resumes* the same batch; add `ORDER BY created_at DESC LIMIT 1` as a defensive
  measure for legacy rows.
- **Test requirement**: F16 — two attempts for one job produce one batch row; resume
  reads the failure set of the surviving attempt.
- **Verified by**: *pending independent review*

---

### REL-12: `import_batches` has no lease or recovery — a crashed worker leaves a batch `running` forever

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/storage/migrations/000004_import_batches.up.sql:10-11,22`;
  `internal/storage/import_batches.go:17-30` (`state='running'` at insert, never
  recovered); `cmd/worker/main.go:130,138-140`
- **Evidence**: `CreateImportBatch` inserts `state='running'` immediately; the only
  exit is `FinishImportBatch`, called from the handler. On crash, `SIGKILL`, or
  shutdown (REL-08) it is never called. The error path calls it but ignores the
  result: `_ = store.FinishImportBatch(ctx, batchID, …, "failed", …)`
  (`main.go:130`); the success path logs and continues
  (`main.go:138-140`). No query ever selects `import_batches` by state, so nothing
  notices.
- **Trigger**: worker crash or restart during an import.
- **Impact**: `magento.import.batches` permanently reports `running` for an import
  that is not running; there is no way to distinguish it from a live import, and no
  reconciliation. Combined with REL-11, the stale row may also be the one `resume`
  reads.
- **Proposed fix**: give `import_batches` the same lease semantics as `jobs`
  (`lease_expires_at`, `attempt`), or derive batch state from the job state and make
  `running` a projection rather than an independently written fact. At minimum add a
  `updated_at`-based reaper that marks `running` batches with a dead job as `failed`.
- **Test requirement**: F17 — kill the worker mid-import; a reaper must move the
  orphan batch to `failed` within one lease period.
- **Verified by**: *pending independent review*

---

### REL-13: Idempotency is scoped to `(instance, tool)` with a hardcoded subject, and the duplicate lookup does not match the unique index

- **Severity**: P2
- **State**: ALREADY-FIXED
- **Fixed by** (t11): `Enqueue` uses `ON CONFLICT (instance_id, binding_id, subject_hash, tool, idempotency_key) WHERE idempotency_key IS NOT NULL` and a fallback lookup over exactly those columns, inside one transaction. Test: `TestJobQueueIdempotentEnqueueMatchesIndex`.
- **Location**: `internal/storage/jobqueue.go:55` (`subject := sha256.Sum256([]byte("system"))`),
  `:92-101` (lookup); `internal/storage/migrations/000001_init.up.sql:116-118` (index)
- **Evidence**: every job is inserted with the same `subject_hash` — the literal
  string `"system"` — so the unique index
  `(instance_id, binding_id, subject_hash, tool, idempotency_key)` degenerates to
  `(instance_id, tool, idempotency_key)`. Two different MCP callers of the same
  instance using the same key collapse into one job. Separately, the conflict lookup
  does not use the index's columns:

  ```sql
  SELECT id::text FROM jobs
   WHERE kind = $1 AND idempotency_key = $2 AND ($3::text IS NULL OR instance_id = $3::text)
   ORDER BY created_at LIMIT 1
  ```

  It filters on `kind` where the index has `tool`, omits `subject_hash` and
  `binding_id`, and — when `$3` is NULL — matches rows of *any* instance.
- **Trigger**: two operators (or two agents) of one store submit imports with the
  same `idempotency_key`; or, once write tools use the queue, two tenants.
- **Impact**: caller B receives caller A's job id and `import.resume` can re-run
  caller A's import. Cross-caller job-id disclosure; incorrect dedup semantics that
  contradict the documented scope `(instance, subject, tool)`
  (`docs/security/threat-model.md:417`). Latent today because the only idempotent
  producer is `magento.import.start`; it becomes a live cross-tenant defect the
  moment write tools are queued.
- **Proposed fix**: thread the caller's `subject_hash` through `jobs.Task` and insert
  it; replace the ad-hoc lookup with
  `INSERT … ON CONFLICT (instance_id, binding_id, subject_hash, tool, idempotency_key)
  DO UPDATE SET updated_at = now() RETURNING id` so the predicate is the index by
  construction.
- **Test requirement**: F18 — same key from two subjects yields two jobs; same key
  from one subject yields one job; the lookup never crosses an instance boundary.
- **Verified by**: *pending independent review*

---

### REL-14: `Cancel` reports success when it canceled nothing

- **Severity**: P2
- **State**: ALREADY-FIXED
- **Fixed by** (t11): `Cancel` checks `RowsAffected` and returns `not_found` or `illegal_transition` (wrapping `jobs.ErrNotCancelable`) when nothing transitioned. Test: `TestJobQueueCancelReportsWhatHappened`.
- **Location**: `internal/storage/jobqueue.go:198-206` (ignores `RowsAffected`);
  `internal/magento/import_jobs.go:176-181` (`Canceled: true` unconditionally)
- **Evidence**:

  ```go
  _, err := q.store.pool.Exec(ctx, `UPDATE jobs SET state='canceled' … WHERE id=$1 AND state IN ('queued','running')`, id)
  if err != nil { return … }
  return nil
  ```

  `import.cancel` then returns `{job_id, canceled: true}` regardless.
- **Trigger**: cancel a `succeeded`, `failed` or `canceled` job, or a nonexistent id.
- **Impact**: the tool lies to the operator and to the agent; an agent that trusts
  `canceled:true` will report a stopped import that is still running (which it may
  be, per REL-05).
- **Proposed fix**: return `(canceled bool, err error)` from `Cancel`, `false` when
  `RowsAffected()==0`; map to `canceled:false` in the tool (or `conflict` when the
  job is terminal).
- **Test requirement**: F19 — cancel a terminal job returns `canceled:false` and does
  not change the row.
- **Verified by**: *pending independent review*

---

### REL-15: The storage integration suite only passes on a virgin database, so it cannot gate regressions

- **Severity**: P2
- **State**: ALREADY-FIXED
- **Fixed by** (t11): The two import-batch tests now clear their own instance rows before asserting, and every queue test in `jobqueue_transitions_test.go` resets the `jobs` table; the suite passes `-count=2` against a persistent database. Fixed as a prerequisite for a green verification run; QA should confirm the broader isolation policy.
- **Location**: `internal/storage/storage_test.go:244` (`SELECT id FROM import_batches
  WHERE instance_id = 'store-us'` via `QueryRow`), `:265-271` (`len(list) != 1`)
- **Evidence**: with `MAGE2MCP_TEST_DATABASE_URL` set, run 1 on an empty schema
  passes; runs 2 and 3 on the same database fail because the tests neither clean up
  nor scope their queries:

  ```text
  === run 1 (clean DB) ===
  ok      github.com/shuaiZend/mage2mcp/internal/storage    0.347s
  === run 2 (same DB, no cleanup) ===
      storage_test.go:248: stored id = "ee0a0ebf-3757-44e0-8afe-394e8b193ba3"
  --- FAIL: TestListAndGetImportBatch (0.01s)
      storage_test.go:270: list = [{…store-list-test…} {…store-list-test…}]
  FAIL
  ```

  Coverage of the package that holds every durability guarantee: **3.4%** without the
  environment variable (the default CI path), **64.2%** with it.
- **Trigger**: any second run against a persistent database — a developer's local
  PostgreSQL, a reused CI service container, or the audit database used here.
- **Impact**: the suite is green only in the exact conditions where it proves least;
  a regression that only shows up on a dirty database is invisible, and the audit's
  own "all 13 pass" claim is true only for a fresh schema. `TestImportBatchLifecycle:244`
  is also the same `QueryRow`-over-many-rows hazard as REL-11, in test form.
- **Proposed fix**: truncate the tables in `TestMain`, or scope every assertion to a
  per-run UUID (`instance_id = 'store-' + runID`); add a `-race -count=2` CI job so
  re-runnability is enforced.
- **Test requirement**: `go test -count=2 ./internal/storage/...` with the database
  URL must pass.
- **Verified by**: *pending independent review*

---

### REL-16: Audit intent and outcome are separate, un-transacted inserts; the outcome can be lost and `outcome_unknown` is never used

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/storage/audit.go:26-51` (single-row `INSERT`, no
  transaction, no dedup); `internal/mcp/server/server.go:89-115` (two independent
  `Audit` calls); `000001_init.up.sql:70-73` (no unique constraint on `event_id`)
- **Evidence**: `installPolicy` records `intent/pending` before the call and
  `outcome/<state>` after it, as two unrelated `INSERT`s. If the process dies between
  the upstream mutation and the outcome insert, the audit trail contains an intent
  row with `state='pending'` and nothing else — forever. There is no
  `outcome_unknown` writer and no `event_id` uniqueness constraint, so a retried
  outcome insert can also duplicate.
- **Trigger**: crash or connection loss between the tool call and the outcome insert.
- **Impact**: the audit trail cannot answer "did this money mutation happen?" — it
  shows an unresolved intent. Any reconciliation or compliance query built on
  `audit_events` is unreliable precisely in the cases that matter.
- **Proposed fix**: the ledger (`operations`) becomes the authoritative record; the
  audit outcome is written in the same transaction as the ledger transition (see
  §3.4). Add `UNIQUE (event_id)` and a sweeper that closes `pending` intents older
  than the timeout as `outcome_unknown`.
- **Test requirement**: F20 — kill the process after the upstream write but before
  the outcome insert; the sweeper must produce `outcome_unknown`, never `failed`.
- **Verified by**: *pending independent review*

---

### REL-17: "Bounded exponential backoff" is a fixed 5-second delay

- **Severity**: P3
- **State**: ALREADY-FIXED
- **Fixed by** (t11): `failSQL` computes `LEAST(300, 5 * 2^(attempt-1)) * (0.75 + random()*0.5)` seconds. Test: `TestJobQueueRetryBackoffGrows`.
- **Location**: `internal/storage/jobqueue.go:165-179`
- **Evidence**: the doc comment says *"after a bounded exponential backoff"*, but
  `backoff = 5 * time.Second` is a constant and `attempt_count` is not consulted. No
  jitter.
- **Trigger**: any retry.
- **Impact**: retries of many jobs that failed together (upstream outage) fire in
  lockstep 5 s later, re-creating the thundering herd the comment claims to avoid;
  a genuinely transient outage that lasts longer than one attempt gets only two
  tries, 5 s apart.
- **Proposed fix**: `scheduled_at = now() + LEAST(max_backoff, base * 2^attempt) * jitter`;
  document the actual policy.
- **Test requirement**: F21 — attempts 1..5 produce strictly increasing delays within
  bounds.
- **Verified by**: *pending independent review*

---

### REL-18: `Enqueue` performs two statements outside a transaction

- **Severity**: P3
- **State**: ALREADY-FIXED
- **Fixed by** (t11): `Enqueue` registers the instance and inserts the job in one transaction. Test: `TestJobQueueEnqueueIsTransactional`.
- **Location**: `internal/storage/jobqueue.go:73-88`
- **Evidence**: `INSERT INTO datasource_instances … ON CONFLICT DO NOTHING` and then
  the `INSERT INTO jobs` run on the pool, not in a `tx`. If the job insert fails, the
  instance registration is already committed.
- **Trigger**: job insert failure (constraint violation, serialization error,
  connection loss) for a previously unseen instance.
- **Impact**: an orphan `datasource_instances` row for a job that does not exist;
  benign today, but it makes the registry assert an instance the operator never
  configured.
- **Proposed fix**: wrap both statements in a single transaction, or register
  instances out-of-band at configuration load.
- **Test requirement**: F22 — force the job insert to fail and assert no instance row
  is left behind.
- **Verified by**: *pending independent review*

---

### REL-19: `canceling` is a declared state with no writer

- **Severity**: P3
- **State**: ALREADY-FIXED
- **Fixed by** (t11): `cancelSQL` writes `canceling` for a running job; the worker (or lease recovery, for a dead worker) resolves it to `canceled`. Tests: `TestJobQueueCancelIsTerminal`, `TestJobQueueCancelThenRecoverResolvesToCanceled`.
- **Location**: `internal/storage/migrations/000001_init.up.sql:94-95`
- **Evidence**: `CHECK (state IN ('queued','running','canceling','succeeded','failed','canceled'))`
  but `grep -rn "'canceling'" --include=*.go` returns nothing.
- **Trigger**: reading the schema as documentation.
- **Impact**: an operator or future implementer infers a two-phase cancel that does
  not exist; `known-limitations.md` and the schema disagree.
- **Proposed fix**: implement the two-phase cancel from REL-05 (the state is already
  there) or drop it from the CHECK.
- **Test requirement**: n/a — resolved by REL-05's test.
- **Verified by**: *pending independent review*

---

### REL-20: `Migrate` holds a pooled connection and then begins a transaction from the same pool

- **Severity**: P3
- **State**: ALREADY-FIXED
- **Fixed by** (t11): `Migrate` runs each migration transaction on the connection that holds the advisory lock (`conn.Begin`), so a one-connection pool cannot deadlock. Test: `TestMigrateWithSingleConnection`.
- **Location**: `internal/storage/storage.go:92-96,133`
- **Evidence**: `conn, _ := s.pool.Acquire(ctx)` (held for the whole migration, with
  the advisory lock) followed by `tx, _ := s.pool.Begin(ctx)` inside the loop.
- **Trigger**: `database.max_conns: 1`.
- **Impact**: `pool.Begin` blocks until the context deadline and the process fails to
  start with a confusing "acquire" error. Requires a specific misconfiguration, hence
  P3.
- **Proposed fix**: run the migration transaction on the acquired connection
  (`conn.Begin`) instead of a second pool checkout, or reject `MaxConns < 2` at
  config load.
- **Test requirement**: F23 — `Open`+`Migrate` with `MaxConns: 1` must either succeed
  or fail fast with a clear message.
- **Verified by**: *pending independent review*

---

### REL-21: The threat model describes an idempotency and ledger implementation that does not exist

> **De-duplication**: this is the documentation half of **SEC-05**
> (`security-audit-report.md:267`), which already cites the same missing files and the same
> false assumption A5. It is recorded here only so the ledger design in §3 has a matching
> documentation action; **count it once, under SEC-05**. It is excluded from the totals above.

- **Severity**: P3
- **State**: DOC-ONLY
- **Location**: `docs/security/threat-model.md:417`; `docs/database-design.md:103,532-546`;
  `internal/storage/migrations/000001_init.up.sql:122-130`
- **Evidence**: the threat model asserts *"`internal/jobs/idempotency.go` — every write
  tool accepts an optional `idempotency_key` … a replay returns the stored result with
  `replayed: true`. `internal/jobs/ledger.go` — a fingerprint of
  `(instance, tool, sha256(JCS(args)))` is recorded for every write … `internal/confirm/store.go`"*.
  None of those files exists (`ls internal/jobs` → `jobs.go`, `jobs_test.go`;
  `internal/confirm` does not exist; confirmation lives in
  `internal/security/confirm`). The `idempotency_keys` table has no writer and no
  reader. The claim "every write tool accepts an optional `idempotency_key`" is false:
  only `magento.import.start` and `magento.import.resume` do
  (`internal/magento/import_jobs.go:88,138,285`).
- **Trigger**: a reader (auditor, customer, operator) trusting the threat model.
- **Impact**: the security posture is overstated in the document that is supposed to
  describe it; this is the documentation half of REL-02 and should be fixed with it.
- **Proposed fix**: mark the ledger/idempotency controls as *planned* in
  `known-limitations.md` and remove or re-scope the paragraph in `threat-model.md`;
  after §3 lands, update both to describe what actually exists.
- **Test requirement**: n/a (documentation).
- **Verified by**: *pending independent review*

### 2.1 Cross-references and de-duplication

| REL | Overlaps | Resolution |
|---|---|---|
| REL-01 | **SEC-19** (`security-audit-report.md:625`, P2 "job state transitions have no guard") | Same statements, different trigger. SEC-19 rests on "requires worker or DB access"; the lease-expiry path (REL-03) needs no adversary, which is why REL-01 is P0. **Merge into REL-01**; keep SEC-19's illegal-transition matrix as its test. |
| REL-02 | **SEC-05** (no idempotency on write tools), **MAG-01/03/04** (per-tool duplicate capture / no creditmemo idempotency / duplicate order) | Complementary, not duplicates: SEC-05 is the requirement, MAG-0x are the per-tool defects, REL-02 is the missing state machine and the ledger design that fixes all of them. Keep all four; §3 is the shared remediation. |
| REL-13 | **SEC-02** (cross-instance job/batch access) | Different mechanism: SEC-02 is the missing tenant predicate on reads; REL-13 is the enqueue lookup returning another row's id. Fix both. |
| REL-15 | **MAG-23** (no INTEGRATION test), QA regression report | REL-15 is specifically the re-runnability defect and the 3.4% default coverage; hand the numbers to QA. |
| REL-21 | **SEC-05** | Duplicate — count once, under SEC-05. |

---

## 3. Operation ledger design

### 3.1 What exists today

| Requirement | Today |
|---|---|
| Durable record of an intent to mutate | `audit_events` intent row, `state='pending'` — not linked to an outcome row |
| State machine for the mutation | none (`audit_events.state` allows `outcome_unknown`, never written) |
| Idempotency for write tools | none — `idempotency_key` exists only on `jobs` (`000001_init.up.sql:100`); `idempotency_keys` is dead schema |
| Fencing | none (REL-01) |
| Reconciliation of ambiguous outcomes | none |
| Money/inventory/order call sites using it | none |

### 3.2 Schema (migration `000005_operation_ledger.up.sql`)

```sql
-- One row per intended business mutation. This is the authority for "did it happen".
CREATE TABLE operations (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key  text        NOT NULL,
    operation        text        NOT NULL,           -- tool name, e.g. magento.invoice.capture
    instance_id      text        NOT NULL REFERENCES datasource_instances(id),
    subject_hash     bytea       NOT NULL,           -- who asked (32 bytes)
    entity_type      text        NOT NULL CHECK (entity_type IN ('order','invoice','creditmemo','shipment','product','stock','category','customer')),
    entity_id        text        NOT NULL,           -- order/invoice id, SKU, ...
    amount_minor     bigint      NULL,               -- money, integer minor units
    currency         text        NULL,
    args_hash        bytea       NOT NULL,           -- sha256(JCS(args)) == confirmation params_hash
    confirmation_id  uuid        NULL,               -- the approval that authorised this
    state            text        NOT NULL DEFAULT 'pending'
                     CHECK (state IN ('pending','executing','succeeded','failed','unknown','reconciling')),
    attempt          integer     NOT NULL DEFAULT 0,
    fence            uuid        NOT NULL DEFAULT gen_random_uuid(),  -- fencing token for the current attempt
    lease_expires_at timestamptz NULL,
    upstream_ref     text        NULL,               -- Magento entity id / increment id
    error_code       text        NULL,
    reconcile_after  timestamptz NULL,
    reconcile_count  integer     NOT NULL DEFAULT 0,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    finished_at      timestamptz NULL,
    CONSTRAINT operations_idem_uq UNIQUE (instance_id, operation, idempotency_key),
    CONSTRAINT operations_money_chk  CHECK ((amount_minor IS NULL) = (currency IS NULL)),
    CONSTRAINT operations_terminal_chk CHECK ((state IN ('succeeded','failed')) = (finished_at IS NOT NULL)),
    CONSTRAINT operations_fence_chk CHECK (octet_length(subject_hash) = 32 AND octet_length(args_hash) = 32)
);
CREATE INDEX operations_unknown_idx  ON operations (reconcile_after) WHERE state = 'unknown';
CREATE INDEX operations_executing_idx ON operations (lease_expires_at) WHERE state = 'executing';
CREATE INDEX operations_entity_idx   ON operations (instance_id, entity_type, entity_id, created_at DESC);
CREATE INDEX operations_subject_idx  ON operations (instance_id, subject_hash, created_at DESC);

-- Append-only history: every transition, with the fence that caused it.
CREATE TABLE operation_transitions (
    seq          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    operation_id uuid        NOT NULL REFERENCES operations(id) ON DELETE RESTRICT,
    from_state   text        NOT NULL,
    to_state     text        NOT NULL,
    fence        uuid        NOT NULL,
    attempt      integer     NOT NULL,
    reason       text        NOT NULL,
    occurred_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX operation_transitions_op_idx ON operation_transitions (operation_id, seq);
```

Notes on the schema:

- `fence` is regenerated on every `pending→executing` and `unknown→reconciling`
  transition. It is the *only* way a worker may write a terminal state, which makes
  the ledger immune to the REL-01 class of lost update.
- `UNIQUE (instance_id, operation, idempotency_key)` is the replay guard. It is
  deliberately not scoped to `subject_hash`: an idempotency key is a caller-supplied
  promise, and reusing one across subjects must be a *conflict*, not a silent
  cross-subject replay. Callers that do not supply a key get
  `idempotency_key = sha256(subject || args_hash)` derived in the tool layer, which
  gives exactly-once-by-arguments semantics.
- `amount_minor`/`currency` are stored so reconciliation and daily reconciliation
  reports can be reconciled against Magento totals without re-parsing args.
- Every transition is a *conditional* `UPDATE … WHERE id=$1 AND fence=$2 AND state=$3`
  returning `RowsAffected`. That is the same compare-and-set shape as
  `ConfirmStore.Consume`, which the probe proves is exactly-once under 8-way
  concurrency (§5.5), so the pattern is known-good in this codebase.
- `operations.instance_id` references `datasource_instances`; the lazy instance
  registration currently done outside a transaction in `Enqueue`
  (`jobqueue.go:76-81`, REL-18) must move into the same transaction as T1.

### 3.3 Transition table

Every transition is a single conditional `UPDATE` that returns `applied bool`. No
transition is allowed to move a row out of a terminal state.

| # | From → To | Trigger | Guard (WHERE clause) | Side effects |
|---|---|---|---|---|
| T1 | ∅ → `pending` | write tool called | `INSERT … ON CONFLICT (instance_id, operation, idempotency_key) DO NOTHING` | if conflict, load the existing row and branch on its state (see 3.5) |
| T2 | `pending` → `executing` | worker/executor claims it | `id=$1 AND state='pending' AND (lease_expires_at IS NULL OR lease_expires_at < now())` | `fence=gen_random_uuid()`, `attempt=attempt+1`, `lease_expires_at=now()+lease`, insert `operation_transitions` |
| T3 | `executing` → `succeeded` | upstream 2xx | `id=$1 AND fence=$2 AND state='executing'` | `upstream_ref`, `finished_at`, clear lease; audit outcome `succeeded` in the same tx |
| T4 | `executing` → `failed` | upstream error proven *not applied* (400/401/403/404/409, or 5xx **before** the request was sent) | `id=$1 AND fence=$2 AND state='executing'` | `error_code`, `finished_at` |
| T5 | `executing` → `unknown` | timeout, transport error after send, 5xx after send, ctx canceled after send, or lease expiry while `executing` | `id=$1 AND fence=$2 AND state='executing'` | `error_code`, `reconcile_after=now()+backoff`, clear lease; audit outcome `outcome_unknown` |
| T6 | `unknown` → `reconciling` | reconciler claims | `id=$1 AND state='unknown' AND reconcile_after<=now()` via `FOR UPDATE SKIP LOCKED` | new `fence`, `reconcile_count+1`, `lease_expires_at=now()+lease` |
| T7 | `reconciling` → `succeeded` | upstream read proves applied | `id=$1 AND fence=$2 AND state='reconciling'` | `upstream_ref`, `finished_at` |
| T8 | `reconciling` → `failed` | upstream read proves not applied | `id=$1 AND fence=$2 AND state='reconciling'` | `error_code`, `finished_at` |
| T9 | `reconciling` → `unknown` | upstream read inconclusive / upstream still down | `id=$1 AND fence=$2 AND state='reconciling'` | `reconcile_after=now()+min(2^n, 1h)`, `reconcile_count+1` |
| T10 | `pending`/`executing`/`unknown` → `failed` | **operator abort**, admin role, explicit reason | `id=$1 AND state=$2 AND $3='admin_abort'` | only permitted after a reconciliation read proves not-applied; recorded in `operation_transitions.reason` |
| T11 | `failed` → `pending` | explicit retry by the same subject with the same key | `id=$1 AND state='failed' AND error_code NOT IN ('unknown','ambiguous')` | resets `finished_at`; **never** from `unknown`/`reconciling` |

Per-capability transition conditions:

| Capability | `SUCCEEDED` requires | `UNKNOWN` triggers | Reconciliation read | `FAILED` only when |
|---|---|---|---|---|
| **Money — `magento.invoice.capture`** | 2xx from `POST /invoices/{id}/capture` | any transport error / timeout after send | `GET /invoices/{id}` → `state` in (`paid`,`complete`) ⇒ succeeded | 4xx (invoice not found, not capturable) — proven not applied |
| **Money — `magento.creditmemo.create`** | 2xx with `entity_id` | as above | `GET /creditmemos?searchCriteria[filter_groups][0][filters][0][field]=order_id&…` and match amount+created_at window; exactly one match ⇒ succeeded | 4xx validation error |
| **Money — `magento.invoice.create`** (capture=false) | 2xx with `entity_id` | as above | invoices for the order; match amount ⇒ succeeded | 4xx |
| **Order — `magento.order.cancel`** | 2xx | timeout after send | `GET /orders/{id}` → `status == 'canceled'` ⇒ succeeded; status unchanged and `state` still processing ⇒ failed (safe to retry) | 4xx |
| **Order — `magento.order.hold` / `unhold`** | 2xx | timeout | `GET /orders/{id}` → `status == 'holded'` / not holded | 4xx |
| **Order — `magento.order.create`** (4-step saga) | 2xx from `POST /carts/{cart}/order` with `entity_id` | timeout on any step after cart creation; **must not delete the cart** | search orders by customer + created_at ± 10 min + total; exactly one ⇒ succeeded, none ⇒ failed, more than one ⇒ `unknown` + manual | never automatic after `place_order` was sent |
| **Inventory — `magento.inventory.assign` / `unassign` / `transfer`** | 2xx | timeout | `GET /inventory/source-items?sku=` → qty delta matches the intent | 4xx |
| **Inventory — `magento.inventory.source_create` / `stock_source_link`** | 2xx | timeout | `GET /inventory/sources` / `stock-source-links` → exists | 4xx |
| **Catalog — `magento.import.*`** | batch finalized with counts | worker death / timeout | per-row `GET /products/{sku}`; rows are already convergent (create skips existing) | validation error |

**The rule that makes this safe**: no code path may retry an operation whose state is
`unknown` or `reconciling`. Retry is only permitted from `failed`, and `failed` is
only reachable when the upstream definitively did not apply the mutation.

### 3.4 Integration points (exact call sites)

| Where | Change |
|---|---|
| `internal/storage/operation_ledger.go` (new) | `Begin`, `Execute`, `Succeed`, `Fail`, `Unknown`, `ClaimReconcilable`, `Resolve` — each a single conditional `UPDATE`/`INSERT … RETURNING`, each returning `applied bool`. Mirror `JobQueue`'s style: pool methods, `fmt.Errorf("storage: …")` wrapping, `errs.New(errs.CodeConflict, …)` on fence loss. |
| `internal/mcp/server/server.go:76-121` (`installPolicy`) | The middleware already computes the tool, instance, identity and (via `enforceConfirmation`) the args hash. Create the `operations` row at intent and drive T3/T4/T5 from the returned error code. This is the single choke point that guarantees *every* write tool is covered — no per-tool opt-in. |
| `internal/magento/client.go:145-160` | Add an `errs.Error` detail or a typed sentinel (`errs.CodeUpstreamTimeout` already exists) that the middleware maps to `UNKNOWN` rather than `FAILED`: `errors.Is(err, errs.CodeUpstreamTimeout)` → T5. The client's message already says "may have completed"; give it machine-readable meaning. |
| `internal/storage/confirm.go:115-129` (`Consume`) | `Consume` and T1/T2 must share one transaction: burn the approval **and** create the `operations` row with `confirmation_id` atomically. Today a crash between `Consume` and the tool call burns the approval with no record (and the operator must re-approve, which is a second money-mutation authorisation). |
| `internal/magento/invoice_write.go:102-110` (`CaptureInvoice`), `:25-60` (invoice create) | T1 before the call, T3/T4/T5 on return, with `entity_type='invoice'`, `amount_minor`, `currency`. |
| `internal/magento/order_documents_write.go:79,108` (shipment, creditmemo) | Same, `entity_type='creditmemo'`/`'shipment'`; creditmemo carries `amount_minor`. |
| `internal/magento/order_status_write.go:41,53,65,105` (comment, hold, unhold, cancel) | Same; cancel is the canonical `UNKNOWN`-capable operation. |
| `internal/magento/order_create.go:67-95` | Replace `cleanupCart` on `place_order` failure with T5 (`unknown`) plus the cart id in `upstream_ref`; the reconciler searches for the order. |
| `internal/magento/inventory.go:63,129,243,270,314` | Same, `entity_type='stock'`, `entity_id=sku`. |
| `cmd/worker/main.go:93-146` (import handler) | One `operations` row per batch (`operation='magento.import'`, `entity_id=batch id`) and a per-row child operation keyed `(job_id, row_number)` so a resumed import can skip rows already `succeeded`. `SetResult` moves into the same transaction as T3. |
| `cmd/worker/main.go` (new handler `platform:reconcile`) | Claims `unknown` rows via T6 and applies T7/T8/T9 using the per-capability reads above; escalates to `unknown` + alert after `reconcile_count > 8`. |
| `internal/storage/audit.go:26-51` | Write the audit outcome in the same transaction as the ledger transition (REL-16). |
| `internal/storage/jobqueue.go:155-206` | Fence the queue writes (REL-01) so the job state is a projection of the ledger, not a second source of truth. |

### 3.5 Replay semantics at the tool boundary

Given an idempotency key, a repeated call must behave as follows:

| Existing operation state | Response |
|---|---|
| `pending` | `409 conflict` / `in_progress` — the first call has not started yet |
| `executing` | `202` with the operation id — poll |
| `succeeded` | replay the stored result, `replayed: true` (this is the behaviour `threat-model.md:417` already promises) |
| `failed` | allow the retry (T11) |
| `unknown` / `reconciling` | **`409 operation_unknown`** with the operation id and the last reconciliation time. This is the case that prevents duplicate charges; the caller is told to poll, never to retry. |

---

## 4. Fault-injection tests required to prove each property

Every row must exist before the corresponding finding is closed. `P` = probe test
(authored, build tag `probe`), `R` = permanent regression test.

| ID | Property | Injection | Assertion | Type |
|---|---|---|---|---|
| F1 | Stale worker cannot write (REL-01) | claim, expire lease, claim again, stale `Complete/Fail/Extend` | `applied=false`; `state`/`attempt_count` unchanged | P+R |
| F2 | Exactly-once claim under contention | 8 workers × 40 jobs | 40 distinct claims, 0 duplicates | P (passes) → R |
| F3 | Crash between upstream success and local commit (REL-02) | fake upstream returns 200; kill the process before the ledger write | row is `unknown`, **not** `failed`; no auto-retry; reconciler resolves it | P+R |
| F4 | Replay is refused while `unknown` | same idempotency key twice, first outcome `unknown` | second call returns `409 operation_unknown`, upstream called once | R |
| F5 | Money transition guards | attempt T3/T7 with a stale fence | 0 rows affected, `applied=false`, no state change | R |
| F6 | Reconciliation resolves `unknown` | upstream now reports applied / not applied | T7 / T8 respectively; `operation_transitions` has the full history | R |
| F7 | Confirmation and ledger are atomic | crash between `Consume` and T1 | either both happen or neither; never "approval burned, no operation" | R |
| F8 | Ledger covers every write tool | enumerate the registry; for each write tool assert one `operations` row per call | no write tool bypasses the middleware | R |
| F9 | Lease heartbeat (REL-03) | handler sleeps 3× lease | `Extend` called ≥3×, one execution, one claim | R |
| F10 | Cancel is honoured (REL-05) | cancel while the handler is mid-batch | final state `canceled`; upstream calls stop within one row | R |
| F11 | Result-write failure does not re-execute (REL-06) | `Complete`/`SetResult` fails once | handler invoked once | R |
| F12 | Cancellation is not swallowed (REL-07) | cancel after N rows | handler returns error; batch `failed`; no further upstream calls | R |
| F13 | Shutdown records a terminal state (REL-08) | cancel `Run` mid-handler | exactly one terminal write on a fresh context | P → R |
| F14 | Claim has no cross-queue coupling / deadlock (REL-09) | 1000 expired rows, two concurrent claims, divergent plans; plus a lock on queue A | both claims succeed; claim for queue B is not delayed | P → R |
| F15 | Poison pill is dead-lettered (REL-10) | expire the lease `max_attempts` times | final `failed`/`lease_expired`, never claimed again | P → R |
| F16 | One batch per job (REL-11) | two attempts for one job | one `import_batches` row; resume uses it | P → R |
| F17 | Orphan batch is reaped (REL-12) | kill the worker mid-import | batch moves to `failed` within one lease period | R |
| F18 | Idempotency scoping (REL-13) | same key, two subjects / two instances | two jobs for two subjects; never a cross-instance id | R |
| F19 | Cancel reports the truth (REL-14) | cancel a terminal job | `canceled=false`, row unchanged | R |
| F20 | Unresolved intent is swept (REL-16) | kill after upstream, before outcome insert | sweeper writes `outcome_unknown`, never `failed` | R |
| F21 | Backoff grows (REL-17) | 5 consecutive failures | strictly increasing delays within bounds | R |
| F22 | Enqueue is atomic (REL-18) | force the job insert to fail | no orphan `datasource_instances` row | R |
| F23 | Migration with `MaxConns: 1` (REL-20) | `Open`+`Migrate` with `MaxConns: 1` | succeeds or fails fast with a clear error | R |
| F24 | Suite is re-runnable (REL-15) | `go test -count=2` with a database URL | passes twice | R |

Harness requirements for the above: a PostgreSQL service in CI (the current default
CI path skips every storage test, 3.4% coverage); a fake Magento that can be told to
"apply then time out", "apply then 500", "not apply then time out"; and a process
kill hook (the ledger test needs a real `os.Exit`/subprocess, not a mocked error).

---

## 5. Verification

Environment: `go1.26.0 darwin/arm64`, PostgreSQL 16.15, baseline `66786a5`.
No production code was modified. Two probe files were added, both behind the `probe`
build tag so they are invisible to the default build and to CI:

- `internal/storage/probe_reliability_test.go`
- `internal/jobs/probe_reliability_test.go`

### 5.1 Required commands — default (no database, the CI path)

```text
$ go test ./internal/jobs/... ./internal/storage/... ./cmd/worker/...
ok      github.com/shuaiZend/mage2mcp/internal/jobs    0.146s
ok      github.com/shuaiZend/mage2mcp/internal/storage    0.127s
ok      github.com/shuaiZend/mage2mcp/cmd/worker    0.129s
```

### 5.2 Required commands — with a real PostgreSQL 16 instance (clean schema)

```text
$ MAGE2MCP_TEST_DATABASE_URL='postgres://mage2mcp:…@127.0.0.1:55440/mage2mcp_rel?sslmode=disable' \
    go test -count=1 ./internal/jobs/... ./internal/storage/... ./cmd/worker/...
ok      github.com/shuaiZend/mage2mcp/internal/jobs    0.146s
ok      github.com/shuaiZend/mage2mcp/internal/storage    0.238s
ok      github.com/shuaiZend/mage2mcp/cmd/worker    0.131s
```

### 5.3 `-race`

```text
$ go test -race -count=1 ./internal/jobs/... ./internal/storage/... ./cmd/worker/...
ok      github.com/shuaiZend/mage2mcp/internal/jobs    1.377s
ok      github.com/shuaiZend/mage2mcp/internal/storage    1.361s
ok      github.com/shuaiZend/mage2mcp/cmd/worker    1.392s

$ MAGE2MCP_TEST_DATABASE_URL='…' go test -race -count=1 ./internal/jobs/... ./internal/storage/... ./cmd/worker/
ok      github.com/shuaiZend/mage2mcp/internal/jobs    1.156s
ok      github.com/shuaiZend/mage2mcp/internal/storage    1.366s
ok      github.com/shuaiZend/mage2mcp/cmd/worker    1.185s
```

Both `-race` runs pass **on a clean schema**. Run the database-backed suite a second
time against the same database and it fails — REL-15, not a data race:

```text
$ MAGE2MCP_TEST_DATABASE_URL='…' go test -count=1 ./internal/storage/    # second run, no cleanup
    storage_test.go:248: stored id = "ee0a0ebf-3757-44e0-8afe-394e8b193ba3"
--- FAIL: TestImportBatchLifecycle (0.02s)
    storage_test.go:270: list = [{…store-list-test…} {…store-list-test…}]
--- FAIL: TestListAndGetImportBatch (0.01s)
FAIL    github.com/shuaiZend/mage2mcp/internal/storage
```

`go build ./...` succeeds with the probe files present; the `probe` build tag keeps
them out of every default build and out of CI.

### 5.4 Coverage of the package that holds every durability guarantee

```text
$ go test -count=1 -cover ./internal/storage/
ok  …/internal/storage  0.345s  coverage: 3.4% of statements     # no MAGE2MCP_TEST_DATABASE_URL (CI default)

$ MAGE2MCP_TEST_DATABASE_URL='…' go test -count=1 -cover ./internal/storage/
ok  …/internal/storage  0.286s  coverage: 61.6% of statements    # 61.6–64.2% across runs, with a database
```

### 5.5 Probe results (verbatim)

```text
$ MAGE2MCP_TEST_DATABASE_URL='…' go test -tags probe -count=1 -v -run TestProbe ./internal/storage/ ./internal/jobs/

    probe_reliability_test.go:70: job 6c51b3c9-… claimed twice: A attempt=1, B attempt=2
    probe_reliability_test.go:80: after stale Complete: state=succeeded attempt_count=2
    probe_reliability_test.go:82: FENCING DEFECT: stale worker (attempt 1) marked job 6c51b3c9-… succeeded
        while attempt 2 is still running; the live worker's Extend()/Fail() now apply to a finished row
--- FAIL: TestProbeStaleWorkerCannotWriteAfterLeaseRecovery (0.71s)

    probe_reliability_test.go:114: after Cancel: state=canceled
    probe_reliability_test.go:124: after stale Fail(retry=true): state=queued
    probe_reliability_test.go:126: CANCEL DEFECT: job f06d9248-… was canceled but a stale Fail(retry=true)
        moved it to "queued", so it will execute again
--- FAIL: TestProbeCancelIsNotUndoneByStaleFail (0.10s)

    probe_reliability_test.go:156: after Cancel then Complete: state=succeeded
    probe_reliability_test.go:158: CANCEL DEFECT: canceled job daf849b6-… reported "succeeded" after the
        worker completed; the operator's cancel was silently overwritten
--- FAIL: TestProbeCompleteDoesNotOverwriteCanceled (0.05s)

    probe_reliability_test.go:213: jobs enqueued=40 distinct claimed=40 duplicate claims=0
--- PASS: TestProbeConcurrentClaimExactlyOnce (0.30s)

    probe_reliability_test.go:255: concurrent Consume: 1 of 8 succeeded
--- PASS: TestProbeConcurrentConsumeExactlyOnce (0.16s)

    probe_reliability_test.go:296: after first failed attempt: state=queued attempts=1
    probe_reliability_test.go:304: retry claimed: id=b48f6bba-… attempt=2
    probe_reliability_test.go:308: upstream writes performed for job b48f6bba-…: 2
    probe_reliability_test.go:310: DUPLICATE WRITE: job b48f6bba-… performed 2 upstream mutations;
        nothing in the queue or storage records that attempt 1 may already have succeeded upstream
--- FAIL: TestProbeRetryReappliesBusinessEffect (5.42s)

    probe_reliability_test.go:371: Claim(report) took 1.20557675s while an unrelated 'import' row was locked
    probe_reliability_test.go:374: CLAIM COUPLING: claiming the 'report' queue waited 1.20557675s on a row
        in the 'import' queue (held for 1.2s); the recovery UPDATE in Claim() is unbounded across queues
--- FAIL: TestProbeClaimBlocksOnUnrelatedExpiredLease (1.28s)

    probe_reliability_test.go:413: job 7992a27a-… owns 2 batch rows (04caa24a-…, fbc5b6c0-…)
    probe_reliability_test.go:418: GetImportBatchByJob returned 04caa24a-…
    probe_reliability_test.go:420: BATCH DUPLICATION: one job owns 2 import_batches rows;
        GetImportBatchByJob selects one of them with no ORDER BY, so import.resume may resume from
        the wrong attempt's failure set
--- FAIL: TestProbeRetryCreatesSecondImportBatch (0.04s)

    probe_reliability_test.go:445: claim 1: attempt=1 max_attempts=1
    probe_reliability_test.go:445: claim 2: attempt=2 max_attempts=1
    probe_reliability_test.go:445: claim 3: attempt=3 max_attempts=1
    probe_reliability_test.go:448: POISON PILL: job 388bcea1-… was claimed at attempt 3 with
        max_attempts=1; a job whose worker dies every time is retried without bound and never
        reaches a terminal state
--- FAIL: TestProbeRecoveryIgnoresMaxAttempts (0.05s)

FAIL    github.com/shuaiZend/mage2mcp/internal/storage    8.272s

    probe_reliability_test.go:83: after shutdown: complete=0 fail=0 (a terminal write was required)
    probe_reliability_test.go:85: SHUTDOWN DEFECT: neither Complete nor Fail was recorded for the
        in-flight task; the row stays 'running' until lease_expires_at, then the task is re-executed
        (complete=0 fail=0)
--- FAIL: TestProbeShutdownLosesCompletion (0.00s)

FAIL    github.com/shuaiZend/mage2mcp/internal/jobs    0.114s
```

The two `PASS` results are deliberate positive controls: they establish that the
defects above are not an artefact of a broken harness — the same harness proves
`SKIP LOCKED` disjointness and confirmation compare-and-set exactly-once.

### 5.6 Deadlock reproduction (raw SQL from `Claim`, PostgreSQL 16.15, 3000 expired rows)

```text
[A seqscan ] ERROR:  deadlock detected
[A seqscan ] DETAIL:  Process 205 waits for ShareLock on transaction 1174; blocked by process 204.
[A seqscan ] Process 204 waits for ShareLock on transaction 1173; blocked by process 205.
[A seqscan ] CONTEXT:  while updating tuple (369,5) in relation "jobs"
```

Reproduced 2/2 with the statement at `internal/storage/jobqueue.go:122-124`, one
session forced to a sequential scan and the other to the lease index scan. With
identical plans the two sessions serialise instead of deadlocking — hence P2, not P1.

### 5.7 What could not be verified

- No real Magento instance was available, so every statement about Magento's
  behaviour under a timeout (does `POST /invoices/{id}/capture` apply before the
  response is lost?) is **CONTRACT**, inferred from the REST semantics and from the
  client's own error text. The ledger's reconciler must be validated against a real
  instance before it is trusted.
- The Magento HTTP calls were not fault-injected here (no fake server was built in
  this task); the duplicate-write evidence (F-REL-06) uses a counting handler at the
  queue boundary, which is where the retry decision is made.
- Lock-wait statistics for the recovery `UPDATE` under production-sized backlogs were
  not measured; the 1.21 s figure is a single-row lock with a 1.2 s hold, chosen to
  prove the coupling mechanism, not to estimate production latency.

---

## 6. Remediation status (t11)

Scope: `internal/jobs/`, `internal/storage/{jobqueue,storage}.go`,
`internal/storage/migrations/000005_job_fencing.up.sql`, `cmd/worker/main.go`.
No production behaviour outside the queue was changed; the operation ledger is
deliberately **not** implemented here.

| Finding | Change | Proof |
|---|---|---|
| REL-01 | `jobs.lease_token` (migration 000005), `jobLeaseGuard` shared by every outcome write, `ErrLeaseLost`/`conflict` on 0 rows | `TestJobQueueStaleAttemptCannotWrite`, `TestWorkerLeaseLostDiscardsResult` |
| REL-03 | `Lease > TaskTimeout` enforced (6m default), heartbeat renews every ≤15s | `TestWorkerLeaseOutlivesTaskTimeout`, `TestWorkerRenewsLeaseWhileHandlerRuns` |
| REL-04 | `failSQL` re-queues only from `running` with budget; `canceling` always ends `canceled` | `TestJobQueueCancelIsTerminal`, `TestJobQueueFailNeverRequeuesBeyondMaxAttempts` |
| REL-05 | Two-phase cancel; `CancelRequested` delivered via the heartbeat; terminal states final | `TestJobQueueCancelIsTerminal`, `TestWorkerCancelRequestStopsHandler` |
| REL-08 | Handler on `WithoutCancel` + `TaskTimeout`; terminal writes on a detached 5s context | `TestWorkerShutdownRecordsCompletion`, `TestWorkerShutdownRecordsFailure` |
| REL-09 | Bounded, ordered, `SKIP LOCKED` recovery as its own statement | `TestJobQueueClaimContention`, `TestJobQueueClaimDoesNotWaitOnOtherQueues`, `TestJobQueueRecoveryBounded`, `TestJobQueueRecoveryPersistsWithoutClaimableWork` |
| REL-10 | Recovery dead-letters at `max_attempts` (`failed`/`lease_expired`) | `TestJobQueueRecoveryRespectsMaxAttempts` |
| REL-13 | `ON CONFLICT` on the real unique index + matching lookup, in one transaction | `TestJobQueueIdempotentEnqueueMatchesIndex` |
| REL-14 | `Cancel` reports `not_found`/`illegal_transition` when nothing transitioned | `TestJobQueueCancelReportsWhatHappened` |
| REL-15 | Import-batch tests clear their own rows; queue tests reset the table; suite passes `-count=2` | full suite run twice |
| REL-17 | `LEAST(300, 5·2^(attempt-1)) · jitter` | `TestJobQueueRetryBackoffGrows` |
| REL-18 | Instance registration + job insert in one transaction | `TestJobQueueEnqueueIsTransactional` |
| REL-19 | `canceling` is written by `Cancel(running)` and resolved by worker or recovery | `TestJobQueueCancelIsTerminal`, `TestJobQueueCancelThenRecoverResolvesToCanceled` |
| REL-20 | `Migrate` uses `conn.Begin` on the advisory-lock connection | `TestMigrateWithSingleConnection` |

Schema invariant added: `jobs_lease_token_chk` — a row in `running`/`canceling` must
carry a token, and a terminal or queued row must not. `TestJobQueueLeaseTokenInvariant`
fails if the invariant is ever relaxed.

### Behaviour changes an operator or reviewer should know about

1. **`magento.import.cancel` can now return an error.** `jobs.Queue.Cancel` reports a
   typed `not_found`/`illegal_transition` instead of always succeeding (REL-14). The
   tool in `internal/magento/import_jobs.go` already propagates the error, so
   cancelling a finished import now fails loudly instead of lying.
2. **A cancelled running job ends `canceled`, not `succeeded`.** If the handler had
   already finished successfully, the outcome is `succeeded` and the cancel is
   recorded in `jobs.cancel_requested_at`.
3. **Shutdown waits for the in-flight handler** (bounded by `TaskTimeout`, 5m) instead
   of cancelling it. A container killed before that window is recovered by lease
   expiry; because recovery now respects `max_attempts`, a job that is repeatedly
   killed at shutdown is dead-lettered rather than looping.
4. **`Complete`/`Fail`/`Extend`/`SetResult` now require the lease token.** Any
   out-of-tree `jobs.Queue` implementation must be updated; the in-tree ones are
   `internal/storage.JobQueue` and the test fakes.

### Verification (t11)

```text
$ go build ./...                                                    # ok
$ go vet ./...                                                      # ok
$ MAGE2MCP_TEST_DATABASE_URL='postgres://…@127.0.0.1:55440/mage2mcp_rel?sslmode=disable' \
    go test -count=1 -race ./internal/jobs/... ./internal/storage/... ./cmd/worker/...
ok  github.com/shuaiZend/mage2mcp/internal/jobs     1.881s
ok  github.com/shuaiZend/mage2mcp/internal/storage  3.770s
ok  github.com/shuaiZend/mage2mcp/cmd/worker        1.444s
```

The storage suite was run twice against the same database to prove re-runnability
(`-count=2` equivalent), which it did not have before t11.

---

## 7. The two-path write model (t30)

The ledger is wired in at the middleware, so a write tool is recorded without any
per-tool code. There is exactly one exception, and it is deliberate.

| Path | Which tools | Who opens the operation | Who records the outcome |
|---|---|---|---|
| **Middleware-gated** (default) | 50 of the 55 write tools | `installPolicy`, before the handler runs | the middleware, in one transaction with the audit row |
| **Self-ledgered** (declared) | `magento.invoice.create`, `magento.invoice.capture`, `magento.creditmemo.create`, `magento.order.create`, `magento.order.cancel` | the tool, through `internal/magento/operations.go` | the tool |

**Why the five money tools are not middleware-gated.** Their safety depends on
work that must happen *around* the upstream call and that only they know how to
do: `CaptureInvoice` reads the invoice before capturing, and `CreateOrder`
reconciles against cart totals and the customer's order window rather than
replaying a recorded result. A middleware-owned operation would either create a
second row for the same call or lose that reconciliation. They therefore record a
*richer* operation (entity, money in minor units, upstream reference) than the
middleware could derive — this is not an opt-out from the ledger.

**The guard.** `TestEveryWriteToolIsGatedOrDeclaredSelfLedgered` (cmd/server)
enumerates the **real catalog** through `scripts/toolinventory` and fails if any
write tool is on neither path, if a declaration names a tool that no longer
exists, or if a tool's declared permission is not one the middleware gate covers.
Current counts: 55 write tools, 50 middleware-gated, 5 self-ledgered. The set
lives in `selfLedgeredTools()` in `cmd/server/main.go` and should shrink as those
tools gain a way to accept an externally owned operation.

**Known limitation (REL-16, accepted for this release).** Audit-outcome and
ledger-transition atomicity holds for the middleware-gated path: `FinishCall`
writes the transition and the `audit_events` row in one transaction, and a failing
audit rolls the transition back (`TestFinishCallRollsBackTransitionWhenAuditFails`).
It does **not** hold for the five self-ledgered tools: they write no audit event
inside their own transaction (there are no `audit_events` writes anywhere in
`internal/magento`), so the middleware's outcome audit row for those calls is
written after the tool has already committed. A crash in that window leaves the
operation row correct but its outcome audit row missing. Closing it requires the
money tools to write their audit outcome in the same transaction as their own
transition, which needs `internal/magento/{invoice_write,order_create,
order_documents_write,order_status_write}.go` in scope.

**Execution vs business outcome.** The job queue remains the durable record of
*execution* (attempts, lease, terminal job state); the ledger is the authority on
*business outcome*. `FinishCall` deliberately never touches the job row, so a
retried job and its operation are related but not merged.

**Decision handling (t42).** The middleware switches on every gate decision and
only `proceed` runs the tool: `replay` returns the recorded result, `in_progress`
returns `conflict`/`operation_in_progress`, `unknown`/`reconciling` returns
`operation_unknown`, `retry` is only honoured when the ledger re-opens and claims
the row inside the same transaction (so the caller always runs under a fence),
and an unrecognised decision fails closed. The approval is consumed **only** for a
decision that will execute; every other path rolls the transaction back, so a
replay or a conflict can no longer burn an approval.

**Crash recovery (t42).** Before each reconciliation pass the ledger recovers
abandoned rows in one bounded, ordered, `SKIP LOCKED` statement: `executing` with
an expired lease becomes `unknown`/`lease_expired`, `reconciling` with an expired
lease becomes `unknown`/`reconcile_lease_expired`, and a `pending` row older than
the staleness window becomes `failed`/`abandoned_pending`. Recovery never
re-executes an ambiguous outcome, and `Abort` is available from `reconciling` once
a human has proved the mutation was not applied.

**Escalation is terminal (t42).** An operation escalated after the bounded number
of reconciliation attempts is left `unknown` with `error_code =
needs_manual_reconciliation` and `reconcile_after = 'infinity'`, so it is never
claimed or re-escalated again; it is surfaced to an operator by that error code.
