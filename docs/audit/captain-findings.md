# Chief Engineer working findings

Findings established directly by the Chief Engineer during architecture review.
These are tracked here so they are not lost, and each is cross-checked against the
domain reports when they land. Format follows [README.md](README.md).

---

## Findings

### ARCH-01: RBAC scope ladder is never applied to a tool call (P0)

- **State**: CONFIRMED
- **Location**: `internal/mcp/server/server.go:95`, `cmd/server/main.go:236`, `cmd/server/main.go:101`
- **Evidence**: `opts.Authorize(ctx, tool, instance)` has no scope parameter, and
  the production wiring calls `policy.Authorize(id, tool, instance, rbac.ScopeRead)`
  with a literal. `meta.Permission` — which is correctly set on every tool — is
  consumed only by the confirmation record and by registry validation. The shipped
  policy grants `{Role:"viewer", Tool:"*", Scope:read}`.
- **Trigger**: Authentication enabled and a caller holding the `viewer` role.
- **Impact**: Every authenticated viewer is authorized for every tool, including
  `magento.invoice.capture`, `magento.creditmemo.create`, and destructive tools.
  The read/write/destructive/admin ladder provides no protection at the call site.
  The confirmation gate still requires a second human, so this is not an
  unauthenticated write path — but authorization is effectively absent.
- **Proposed fix**: Change the callback to
  `Authorize func(ctx, tool, instance, requiredScope string) error`; resolve the
  scope from the registry in `installPolicy`; fail closed on an unknown tool; ship
  a default policy that actually distinguishes read from write from destructive.
- **Test requirement**: end-to-end test asserting a `viewer` is denied
  `magento.invoice.capture` and allowed `magento.product.get`.
- **Remediation (t8, security-auditor)**: fixed in `internal/mcp/server/server.go`,
  `cmd/server/main.go`, `internal/security/rbac/`.
  - `Options.Authorize` now takes `requiredScope`; `installPolicy` resolves it from
    `Registry.Lookup(tool).Permission` and **denies locally** (`permission_denied`,
    reason `unregistered_tool`) without consulting the authorizer when the tool has no
    declared permission. `platform.info` is registered by the server package itself, so
    it is declared explicitly in `serverOwnedTools` at `read` rather than falling back
    to a default.
  - `cmd/server/main.go` passes the scope through; the literal `rbac.ScopeRead` is gone
    from that file.
  - `rbac.DefaultPolicy()` is the shipped ladder: `viewer`/`analyst` → read,
    `operator` → write, `admin` → admin (which covers destructive). A write grant does
    not reach a destructive call, and no read-only role holds a wildcard above read.
  - With `auth.enabled: false` there is still no authorizer, but `authorizeFunc` now
    logs `AUTHORIZATION DISABLED: …` at Warn during startup.
  - Evidence: `TestShippedPolicyEnforcesScopeLadder` (HTTP end-to-end, viewer denied
    `magento.invoice.capture` and `magento.product.delete`, allowed
    `magento.product.get`; operator allowed `magento.invoice.capture`, denied
    `magento.product.delete`; admin allowed the destructive tool),
    `TestAuthorizationFailsClosedForUnregisteredTool`,
    `TestAuthorizationReceivesRequiredScope`, `TestAuthorizationCoversPlatformInfo`,
    `TestDefaultPolicyExpressesTheLadder`, `TestRealToolPermissionsMatchFixture`
    (guards the fixture against catalog drift), `TestAuthorizeFuncWarnsWhenAuthDisabled`.
  - Residual (not in this task): the authorizer still receives the instance from the
    request body only, so a tool with no `instance` field (for example
    `magento.import.status`) is evaluated against the empty instance and an
    instance-scoped grant cannot match it. That is SEC-02 / ARCH-03's scope.

### ARCH-02: worker retry ignores retryability and idempotency (P1)

- **State**: CONFIRMED
- **Location**: `internal/jobs/jobs.go` (`execute`), `internal/core/errors/errors.go:134`
- **Evidence**: `retry := task.Attempts < task.MaxAttempts`. `RetryableError` is
  called only from tests. `Task` has no idempotency declaration.
- **Trigger**: Any queued handler returning a non-retryable error while attempts
  remain — for example a `validation_failed` 400, or a timeout on a write whose
  outcome is unknown.
- **Impact**: A definitively failed request is retried. If a financial or
  inventory mutation is ever queued, this duplicates the side effect.
- **Proposed fix**: `retry := attempts < max && errs.RetryableError(err) && task.Idempotent`,
  with `Idempotent` defaulting to false.
- **Test requirement**: worker test proving a non-retryable error fails on the
  first attempt, and a non-idempotent task is never retried.

### ARCH-03: cross-tenant access to import jobs (P0)

- **State**: CONFIRMED
- **Location**: `internal/magento/import_jobs.go` (`newImportStatusTool`,
  `newImportCancelTool`, `newImportResumeTool`, `newImportBatchGetTool`),
  `internal/storage/jobqueue.go` (`GetJob`, `Cancel`, `Enqueue`)
- **Evidence**:
  1. `importStatusSchema`, `importCancelSchema`, `importResumeSchema`, and
     `importBatchGetSchema` have **no `instance` field**. A systematic scan of
     every tool schema found exactly these four (plus one nested object that is
     not a tool).
  2. `GetJob(ctx, id)` and `Cancel(ctx, id)` filter on `id` only — no instance,
     no owner.
  3. `magento.import.resume` reads the original job's payload and enqueues a new
     import with `InstanceID: payload.Instance`, so it writes to the **original**
     tenant's Magento instance regardless of who called it.
  4. `Enqueue` hard-codes `subject := sha256.Sum256([]byte("system"))`, so every
     job shares one `subject_hash`. Job ownership is not modelled at all, even
     though the column exists.
- **Trigger**: A caller who obtains any job UUID. Job ids are UUIDv4 and not
  enumerable through a scoped tool, but they are returned in tool output, may be
  shared between operators, and appear in logs.
- **Impact**: Read another tenant's job status and failed rows; cancel another
  tenant's running import; and, worst, **trigger a write against another tenant's
  Magento instance** via `resume`. Combined with ARCH-01, a read-only role can
  initiate that write. The approval prompt shows the tool title with no tenant
  context, so the human approver cannot see which store they are approving.
- **Proposed fix**: add `instance` to all four schemas and require it; add a
  caller `Subject` to `jobs.Task` and hash the real subject in `Enqueue`; filter
  `GetJob`/`Cancel`/batch reads by `(instance_id, subject_hash)`; make `resume`
  refuse a job whose instance the caller is not authorized for. Bind the
  confirmation to the resolved instance (see ARCH-06).
- **Test requirement**: cross-tenant test — identity scoped to instance A cannot
  read, cancel, or resume a job belonging to instance B.

### ARCH-04: the idempotency ledger table exists and is unused (P1)

- **State**: CONFIRMED
- **Location**: `internal/storage/migrations/000001_init.up.sql` (`idempotency_keys`)
- **Evidence**: the table is created with
  `PRIMARY KEY (instance_id, idem_key)` and a `response_body jsonb` replay column.
  `grep -rn idempotency_keys --include=*.go` returns **zero** hits. No Go code
  reads or writes it.
- **Impact**: The database schema anticipates exactly the response-replay ledger
  the objective requires, and nothing uses it. Financial idempotency is therefore
  unimplemented despite the storage being ready.
- **Proposed fix**: implement the ledger against this table (or its successor),
  wired per §6.3 of the architecture review.
- **Test requirement**: same idempotency key + same arguments returns the recorded
  response without a second upstream call; same key + different arguments is
  `idempotency_key_reuse`.

### ARCH-05: `idempotency_key_reuse` and `illegal_transition` are dead codes (P2)

- **State**: CONFIRMED
- **Location**: `internal/core/errors/errors.go`
- **Evidence**: zero uses outside `errors.go` for both codes.
- **Impact**: The closed taxonomy advertises failure classes the system cannot
  produce, which misleads agent clients that branch on the code.
- **Proposed fix**: land with ARCH-04 so the codes become reachable.

### ARCH-06: confirmation records no instance (P2)

- **State**: CONFIRMED
- **Location**: `internal/mcp/server/server.go:148`
- **Evidence**: `o.Confirm.Require(ctx, tool, "", meta.Permission, ...)` passes an
  empty instance. The approval row therefore carries no tenant. Argument binding
  still prevents cross-instance replay because `instance` is inside the hashed
  arguments.
- **Impact**: The human approving a destructive write cannot see which store it
  targets. With ARCH-03 this becomes materially dangerous.
- **Proposed fix**: pass the resolved instance into `Require`.
- **Test requirement**: approval listing shows the instance for a multi-instance
  deployment.
- **Remediation (t9, security-auditor)**: fixed with ARCH-08. `enforceConfirmation`
  now passes the instance it authorized the call against
  (`instanceArgument(args)`) into `Require`, and `Consume` requires the same value.
  `confirm.WritePending` renders an `INSTANCE` column (`-` when the tool carries no
  instance), and `Summarize` stores a bounded argument summary in the same row.
  Evidence: `TestPendingShowsInstanceAndSummary`,
  `TestConfirmationStoresArgumentSummary`,
  `TestConfirmationBoundToToolAndInstanceThroughHTTP` (step 4 proves a store-a
  approval is rejected on store-b).
  Residual: a tool with no `instance` field is still bound to `""`; making the
  gateway resolve a default instance for single-instance deployments is SEC-02's
  instance plumbing, not this change.

### ARCH-08: a confirmation is not bound to its tool, so one approval authorizes another tool (P1)

- **State**: CONFIRMED
- **Location**: `internal/storage/confirm.go` (`Consume`), `internal/security/confirm/confirm.go`
  (`MemoryStore.Consume`), `internal/mcp/server/server.go` (`enforceConfirmation`)
- **Evidence**: `Consume(ctx, id, requester, paramsHash, now)` has no tool
  parameter, and the SQL predicate is
  `WHERE id=$1 AND state='approved' AND expires_at>$4 AND requester_hash=$2 AND params_hash=decode($3,'hex')`.
  There is no `tool = …` term, and the in-memory store likewise compares only
  state, requester, and params hash. `CanonicalHash` excludes only
  `confirmation_id`, so the hash is a function of the supplied arguments alone.
- **Trigger**: Any two confirmation-requiring tools that accept the same supplied
  argument object. Because the caller controls which optional fields to send and
  `additionalProperties:false` only forbids extra fields, many pairs collide. Three
  pairs collide by construction, sharing one schema constant:
  - `magento.order.hold` and `magento.order.unhold` (`orderHoldSchema`, both
    registered at `write_tools.go:334-335`) — semantically opposite operations.
  - `magento.product.update` and `magento.configurable.update` (`productUpdateSchema`).
  - `magento.invoice.email`, `magento.shipment.email`, `magento.creditmemo.email`
    (`documentEmailSchema`).
- **Concrete attack**: the agent calls `magento.order.hold` with
  `{"instance":"store-us","order_id":42}`; the server returns
  `confirmation_required` with id `C`; a human approves "Hold order" for order 42;
  the agent then calls `magento.order.unhold` with
  `{"instance":"store-us","order_id":42,"confirmation_id":"C"}`. The hash matches,
  the tool is never compared, the approval is consumed, and the order is
  un-held — an operation no human approved.
- **Impact**: The human approval gate is bypassable across tools. Severity is
  capped below P0 only because the colliding pairs found are not direct money
  movement; the class would be P0 the moment a financial tool's argument shape
  coincides with another tool's.
- **Proposed fix**: add `tool` (and the resolved `instance`) to the `Consume`
  signature and to both store predicates; reject a mismatch as
  `confirmation_param_mismatch`. This is a public interface change to the
  `confirm.Store` contract and is approved here.
- **Test requirement**: approving tool A cannot consume for tool B with identical
  arguments; a table test over every shared-schema group.
- **Remediation (t9, security-auditor)**: fixed.
  - `confirm.Store.Consume` is now
    `Consume(ctx, id, requester, tool, instance, paramsHash string, now time.Time)`;
    `confirm.Service.Consume` takes `tool, instance, args`. `MemoryStore.Consume`
    compares `Tool`, `Instance`, and `ParamsHash` and returns one generic
    `confirmation_param_mismatch` ("confirmation does not match this operation")
    so the caller cannot learn which binding failed.
  - `internal/storage/confirm.go` CAS predicate is now
    `... AND requester_hash = $2 AND tool = $3 AND COALESCE(instance_id,'') = $4
    AND params_hash = decode($6,'hex')`; `classifyConsumeFailure` reports the same
    generic mismatch for a tool, instance, or hash divergence.
  - The single-use compare-and-set is unchanged: it is still one `UPDATE ... WHERE
    state = 'approved'`, so exactly one of N concurrent consumes wins.
  - Evidence: `TestConsumeRejectsDifferentTool` (the `magento.order.hold` vs
    `magento.order.unhold` pair, byte-identical arguments, and the rejected attempt
    does not burn the approval), `TestConsumeRejectsDifferentInstance`,
    `TestConsumeSingleUseUnderConcurrency` (20 concurrent consumes → 1 success, 19
    refusals), `TestConfirmationBoundToToolAndInstanceThroughHTTP` (end-to-end
    through `installPolicy`), plus the pre-existing
    `TestConsumeRejectsChangedArguments`, `TestConsumeRejectsWrongRequester`,
    `TestFullLifecycle`.
  - `cmd/admin pending` now delegates to `confirm.WritePending`, which prints
    `ID TOOL INSTANCE SUMMARY EXPIRES`; the summary is the sanitised argument
    rendering from `confirm.Summarize` (sorted keys, credential-shaped values
    redacted, 20-item cap, 1024-rune cap, control characters stripped).
  - ARCH-07 (nil `Confirm` fail-open) is **not** part of this change and remains
    open.

### ARCH-07: confirmation is silently skipped when the service is nil (P3)

- **State**: CONFIRMED (not reachable from `cmd/server`, which always constructs a
  service over either PostgreSQL or the in-memory store)
- **Location**: `internal/mcp/server/server.go` (`enforceConfirmation`, first branch)
- **Evidence**: `if o.Confirm == nil { return nil }`.
- **Impact**: A library consumer that omits `Confirm` gets high-risk tools with no
  approval gate, silently. This is a fail-open default in a reusable package.
- **Proposed fix**: make `NewMCP` reject a configuration where any registered tool
  requires confirmation and `Confirm` is nil, or default to an in-memory store and
  log loudly.

---

## Cross-check log

| Captain finding | Domain report | Outcome |
|---|---|---|
| (pending audit completion) | | |
