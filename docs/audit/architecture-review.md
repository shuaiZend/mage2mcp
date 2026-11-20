# Architecture review

| Field | Value |
|---|---|
| Document | Architecture review (Phase 3: architecture alignment) |
| Commit audited | `66786a5d10c1f78d00e08a5b42d01fcc2da37a3f` |
| Owner | Chief Engineer |
| Status | Binding for the remediation phase; interface decisions in §6 require no further approval |

This review answers one question: **does the architecture as built actually
enforce the contracts it declares?** It is not a style review. The finding that
matters most is that several contracts are declared correctly, unit-tested
correctly, and then not honored at the integration point where they matter.

---

## 1. Verified structure

The layering is genuinely sound and survives inspection:

```text
cmd/{server,worker,admin}
        │
        ├── internal/magento      ─┐
        ├── internal/cloudflare    ├── depend only on: core/errors,
        ├── internal/ga4           │   mcp/registry, mcp/toolerr
        └── internal/unified      ─┘
        │
        ├── internal/mcp/{registry,server,toolerr}
        ├── internal/security/{auth,jwt,rbac,confirm,audit}
        ├── internal/storage  → jobs, security/audit, security/confirm
        └── internal/jobs, core/{config,errors}, observability
```

Two properties hold in code, not just in ADR-0001:

1. **No cycles.** The import graph is acyclic.
2. **No module imports another module.** `magento`, `cloudflare`, `ga4`, and
   `unified` are siblings that meet only through the tool registry and the error
   taxonomy. This is the boundary the documentation claims and the code delivers.

The error taxonomy (`internal/core/errors`) is closed, typed, bounded, and
strips upstream detail — a genuinely good design. The tool registry
(`internal/mcp/registry`) validates names, permissions, timeouts, and schema
consistency at startup and refuses to boot on a contract violation. These are
the strongest parts of the codebase.

## 2. The structural weakness: contracts are declared, then not consumed

The recurring defect pattern in this codebase is not a missing abstraction. It is
an abstraction that exists, is tested in isolation, and is **not consulted by its
only real consumer**. Three instances follow, in descending severity.

### ARCH-01 (P0): the RBAC scope ladder is never applied to a real tool call

**This is the most serious defect found in the audit.**

`internal/security/rbac` implements a correct policy engine: default-deny, deny
beats allow, an ordered scope ladder `read < write < destructive < admin`, and
per-instance grants. It is 100% covered and its tests genuinely exercise the
ladder (`TestScopeHierarchy`, `TestInstanceScoping`, `TestDenyOverridesAllow`).

The authorization **callback signature cannot express a required scope**:

```go
// internal/mcp/server/server.go:95
if err := opts.Authorize(ctx, tool, instance); err != nil {
```

and the production wiring therefore hard-codes the weakest scope:

```go
// cmd/server/main.go:236
return policy.Authorize(id, tool, instance, rbac.ScopeRead)
```

The tool's declared permission is available at exactly that point
(`meta.Permission`, set to `read`/`write`/`destructive`/`admin` on every tool) and
is used for only two things: a string in the confirmation record
(`server.go:148`) and registry validation (`registry.go:144,157,168`). It is
**never** used to make an authorization decision.

Consequence, with authentication enabled: the shipped policy grants
`{Role:"viewer", Tool:"*", Scope:read}`. Every authenticated caller with the
`viewer` role therefore passes authorization for **every tool in the registry**,
including `magento.invoice.capture`, `magento.creditmemo.create`,
`magento.product.delete`, and every other write and destructive tool. The
confirmation gate still stands in front of high-risk writes and requires a
different human to approve, so this is not by itself an unauthenticated write
path — but it is a complete collapse of the authorization model: RBAC no longer
distinguishes read from destructive, and a read-only role can initiate
destructive operations.

The fix is a **public interface change** and is approved in §6.1.

### ARCH-02 (P1): the retryability contract is declared and never honored

`errs.Error` carries `Retryable`, and `errs.RetryableError` documents the rule:

> "A non-idempotent write must never be retried, which is the caller's
> responsibility to combine with the tool annotation."

The only caller of `RetryableError` in the entire repository is a **test**. The
worker's retry decision is:

```go
// internal/jobs/jobs.go
retry := task.Attempts < task.MaxAttempts
```

It consults neither the error's retryability nor the operation's idempotency. It
retries a definitively failed 400 exactly as readily as a transient 503.

Today this is contained only because the worker registers exactly two handlers —
`platform:noop` and `magento:import` — so no financial operation runs through the
retry path yet. That containment is incidental, not designed. The moment an
invoice, capture, refund, or transfer is queued, this line duplicates money.

Note also the client is *aware* of the ambiguity it creates: a timeout produces

```go
errs.New(errs.CodeUpstreamTimeout,
    "Magento did not answer before the deadline; the call may have completed")
```

with `Retryable` left false — correct, and yet nothing downstream distinguishes
"may have completed" from "did not happen". There is no state for it.

### ARCH-03 (P2): the error taxonomy anticipates machinery that does not exist

The closed taxonomy declares codes that no code path ever produces:

| Code | Uses outside `errors.go` |
|---|---|
| `idempotency_key_reuse` | **0** |
| `illegal_transition` | **0** |
| `probe_sku_conflict` | 0 (probe-only, acceptable) |

`idempotency_key_reuse` and `illegal_transition` are precisely the errors an
operation ledger and a workflow state machine would emit. Their presence is
evidence that the intended design includes a ledger and a state machine, and
that the design was never built. The taxonomy is ahead of the implementation.

`idempotency_key` itself exists in exactly one place in the whole codebase: the
job queue (`internal/storage/jobqueue.go`, `internal/magento/import_jobs.go`).
No product, inventory, order, invoice, or refund operation accepts one.

## 3. The missing primitive: an operation ledger

Every one of the four business-critical gaps in this audit — configurable-product
partial success, MSI transfer consistency, financial idempotency, and job retry
safety — reduces to the same missing primitive. There is no durable record that
an operation is *in flight*, so the system cannot distinguish:

- never started,
- started and failed before any side effect,
- started and may have succeeded (the dangerous state),
- succeeded.

The error taxonomy already names the state the system lacks a word for. The
client already returns the sentence that describes it. What is missing is
persistence and a transition rule.

**Architectural decision: one ledger primitive, four consumers.** Building four
bespoke mechanisms would multiply the failure modes. The ledger is specified in
§6.3 and the concrete schema/transition design is owned by the reliability
engineer (`REL-*`); the Magento engineer consumes it for the three business
flows. This is the single most important cross-team contract in the remediation.

## 4. The Magento monolith

`internal/magento` is 13,694 non-test lines across 74 files. `write_tools.go`
alone is 137 KB and mixes four responsibilities in one file: tool registration,
JSON schema declaration, input validation, and Magento REST orchestration.

This is not a correctness defect and must not be "fixed" by a large refactor —
the objective forbids unrelated restructuring. It matters for two practical
reasons:

1. **It constrains parallel work.** Three agents need to touch write paths.
   Ownership is therefore assigned per *file*, not per package (§7).
2. **It hides coupling.** A tool's schema, its validation, and its side effect
   are hundreds of lines apart, which is how a `Permission` field can be set on
   every tool and read by no authorization decision (ARCH-01).

The remediation adds *new* files rather than restructuring existing ones:
ledger client code, workflow state machines, and idempotency helpers get their
own files. Existing files are edited only where a defect lives.

## 5. Confirmation model: correct, with two binding gaps

The two-step confirmation (ADR-0014) is well built: the requester never sees an
approval secret, the store enforces single use atomically in SQL, self-approval
is rejected by the database (`requester_hash <> $2`), the TTL is bounded, and the
arguments are bound by canonical hash with `confirmation_id` excluded.

Two gaps:

- **CONF binding (P2).** `Confirm.Require` is called with an empty instance
  (`server.go:148` passes `""`), so the human approval record carries no tenant
  context. The approval *is* still argument-bound (the instance appears in the
  hashed arguments), so cross-instance replay is prevented; what is missing is
  that the approver cannot see which store they are approving for.
- **Consume ordering.** The confirmation is consumed *before* the tool executes.
  This is the fail-safe direction (a crash burns the approval rather than
  permitting replay) and should be kept, but it must be documented, because it
  means a failed execution requires re-approval.

## 6. Interface contract decisions (binding)

These are the public-interface changes approved by the Chief Engineer. Every
consumer, test, and document must be updated in the same change.

### 6.1 Authorization carries the required scope

```go
// internal/mcp/server Options
Authorize func(ctx context.Context, tool, instance, requiredScope string) error
```

`installPolicy` resolves `requiredScope` from `Registry.Lookup(tool).Permission`
and **fails closed** if the tool is unknown. `cmd/server/main.go` passes the scope
through instead of hard-coding `read`.

The shipped default policy must then actually express the ladder: a `viewer`
role gets read; a write-capable role gets write; destructive operations require
an explicit destructive grant. The default must not grant `"*"` at read scope and
call that authorization.

Acceptance: an end-to-end test proves a `viewer` identity is denied
`magento.invoice.capture`, and a `viewer` identity is allowed
`magento.product.get`.

### 6.2 Retry honours retryability and idempotency

```go
retry := task.Attempts < task.MaxAttempts &&
         errs.RetryableError(err) &&
         task.Idempotent
```

`Task` gains an explicit `Idempotent bool`; the zero value must be **false** so
that a task is never retried unless it opts in. `platform:noop` and
`magento:import` (which is idempotency-keyed) opt in; anything financial must not.

Acceptance: a worker test proves a non-retryable error fails the task on the
first attempt even when attempts remain, and a non-idempotent task is never
retried.

### 6.3 Operation ledger (shape owned by REL, integration owned by MAG)

Requirements the implementation must satisfy, regardless of final schema:

1. A unique operation id, plus a caller-supplied idempotency key unique per
   `(instance_id, tool, key)`; a reused key with different arguments is
   `idempotency_key_reuse`, never a silent second execution.
2. States `pending → executing → {succeeded | failed | unknown}`, plus
   `reconciling → {succeeded | failed}`, with every illegal transition rejected
   as `illegal_transition`.
3. A fencing token so a stale worker cannot overwrite a newer attempt's state.
4. `unknown` is entered on ambiguous upstream outcomes (timeout, 5xx after a
   non-idempotent write) and is resolved **only** by querying upstream state,
   never by retrying the mutation.
5. The ledger write and the state transition are transactional.

### 6.4 Confirmation records its instance

`Confirm.Require` receives the resolved instance instead of `""`, so the human
approval surface shows the tenant. Argument binding is unchanged.

## 7. File ownership for the remediation phase

Assigned to prevent the collision that the monolith otherwise guarantees:

| Area | Owner | Files |
|---|---|---|
| Security wiring, RBAC scope, confirmation binding | Security auditor | `internal/mcp/server/server.go`, `cmd/server/main.go`, `internal/security/**`, `internal/storage/confirm.go` |
| Catalog / MSI / finance business logic | Magento engineer | `internal/magento/{configurable,inventory,invoice_write,order_documents_write,order_create}.go` + new workflow files |
| Ledger, queue, worker, storage | Reliability engineer | `internal/jobs/**`, `internal/storage/**`, new ledger package |
| Tool registration, schemas, error envelopes | MCP engineer | `internal/magento/{tools,write_tools}.go`, `internal/mcp/registry/**`, `internal/mcp/toolerr/**` |
| Client transport, pooling, pagination | Performance engineer | `internal/magento/{client,catalog,product,product_bulk,sales_analysis,order_geography,import}.go` |
| Independent tests | QA engineer | new `*_test.go` files only; may not edit production code |
| Documentation | Docs engineer | `README*`, `docs/**`, `CHANGELOG.md`, `SECURITY.md`, `CONTRIBUTING.md` |

A file has exactly one owner. Where a change spans owners (e.g. the ledger is
built by REL and consumed by MAG), the provider lands first and the consumer
depends on the completed task.

## 8. What this review does not change

Deliberately out of scope, per the objective's constraints:

- No new third-party dependencies.
- No change to the MCP transport or protocol version.
- No restructuring of `internal/magento` beyond adding new files.
- No change to the Magento REST/GraphQL approach; the gateway does not touch the
  Magento database.
- No removal of any tool to reduce surface area.

## 9. Verdict of this review

The architecture is **fundamentally sound and unusually disciplined** for its
size: correct boundaries, a closed error taxonomy, a validated tool registry, and
a genuinely well-built confirmation protocol. It fails not by having the wrong
abstractions but by not wiring them to their consumers — the scope ladder is
never applied, the retry contract is never read, and the error taxonomy names a
ledger that was never built.

That is good news for remediation: the fixes are integration fixes plus one new
primitive, not a redesign. It is also the reason the final verdict cannot be
READY until ARCH-01 and ARCH-02 are closed and proven by end-to-end tests.
