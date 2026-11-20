# Security audit — mage2mcp v0.11.0

**Auditor:** security-auditor (independent of the implementation).
**Baseline:** commit `66786a5` (`docs: record CI local-first timings (#149)`), working tree
clean except this report and the untracked `docs/audit/` directory at audit start.
**Scope:** `cmd/`, `internal/security/**`, `internal/mcp/**`, `internal/storage/**`,
`internal/jobs/**`, `internal/magento/**`, `internal/cloudflare/**`, `internal/ga4/**`,
`internal/unified/**`, `internal/core/**`, `internal/observability/**`, migrations, and
`docs/security/threat-model.md` line by line.
**Method:** adversarial code reading, six throwaway probe tests in four files executed against
the real code paths (including a real PostgreSQL 16 instance for the confirmation store), and
the two mandated test commands. Probe files were deleted after capturing output; no tracked
file was modified (`git diff --stat` is empty).

---

## 1. Executive summary

| Severity | CONFIRMED | ALREADY-FIXED | DOC-ONLY | PLANNED |
|---|---|---|---|---|
| **P0** | 4 | 0 | 0 | 0 |
| **P1** | 5 | 0 | 0 | 0 |
| **P2** | 7 | 0 | 0 | 0 |
| **P3** | 3 | 0 | 0 | 0 |
| **Total** | **19** | 0 | 0 | 0 |

**Verdict for this task's scope: NOT production-ready.** Four P0 findings are reproducible in
this commit:

1. **SEC-01** — the authorization layer never consults the tool's declared permission. Every
   tool, including the 43 write and 9 destructive tools, is authorized at `read`. A token with
   the `viewer` role (documented "Read-only access") is authorized to call
   `magento.product.delete`, `magento.order.create` and `magento.import.start`. Proven by
   probe, not inferred.
2. **SEC-02** — the asynchronous import tools (`magento.import.status|cancel|resume|batch`)
   accept only a job/batch UUID, and the storage layer has no `instance_id` or `subject`
   predicate on those reads. Any authenticated caller who learns a UUID can read another
   tenant's job result, cancel their import, or resume it (which re-enqueues the *other*
   tenant's payload and writes into *their* Magento instance).
3. **SEC-03** — an approval is bound to a requester and a parameter hash only. The stored
   `tool` and `instance_id` are never compared on consume. An approval issued for
   `magento.import.cancel` was accepted for `magento.import.resume` in a live probe.
4. **SEC-05** — assumption **A5 is false**: order- and money-mutating tools ship
   (`magento.order.create`, `magento.invoice.create`, `magento.invoice.capture`,
   `magento.creditmemo.create`, `magento.order.cancel`, plus customer writes), and none of
   them accepts an idempotency key. The threat model's structural argument that "order-mutating
   tools are absent in v1, which removes double-order operations structurally" does not hold,
   and the duplicate-charge control it promises does not exist.

The strongest parts of the implementation are the JWT verifier (algorithm
allowlist enforced twice, key type checked against the header alg, redirects disabled, no
`jku`/`x5u`, exact `iss`, `aud` membership, `exp` required, 60 s leeway, 8 KiB cap) and the
confirmation store's single-use compare-and-set (20 concurrent `Consume` calls on one
approval: exactly one succeeded). The weakest part is that the RBAC ladder, the confirmation
bindings, and the audit decision field are all *declared* in the threat model and *not wired*
in `cmd/server/main.go`.

**Documentation status.** `docs/security/security-review.md:5` is honest — "Nothing here is
implemented yet" — but `docs/security/threat-model.md` is written in the present tense and
names packages that do not exist (`internal/auth`, `internal/httpx`, `internal/registry`,
`internal/config`, `internal/cache`, `internal/egress`, `internal/quota`, `internal/ratelimit`,
`internal/upstream`, `internal/media`, `internal/redact`, `internal/secret`, `internal/log`,
`internal/metrics`, `internal/store`, `internal/modules/*`). The reconciliation in §3 marks
every control, and the honest reading is: the code implements a narrower, sounder subset, and
the document describes a system that was not built.

---

## 2. Findings

**Deployment preconditions.** Write and destructive tools exist only when `tools.writes: true`
(`cmd/server/main.go:272-276`); the asynchronous import tools exist only when `database.url` is
set (`cmd/server/main.go:74-91,277-281`). Both are documented production configurations — the
write profile is the mode the confirmation system exists for, and a durable database is required
for audit and approvals. Findings that need one of these preconditions say so in **Trigger**;
none of them requires a misconfiguration beyond enabling a documented feature.

### SEC-01: RBAC scope ladder is never enforced — every tool is authorized at `read`

- **Severity**: P0
- **State**: CONFIRMED
- **Location**: `cmd/server/main.go:236` (the only production `Authorize` call site),
  `internal/mcp/server/server.go:36` (the `Authorize` signature carries no scope),
  `internal/mcp/server/server.go:95` (call site), `internal/mcp/registry/registry.go:144-169`
  (permissions validated but never consumed), `internal/security/rbac/rbac.go:75-100`.
- **Evidence**: `authorizeFunc` hardcodes the required scope:

  ```go
  // cmd/server/main.go:231-237
  return func(ctx context.Context, tool, instance string) error {
      id, ok := auth.IdentityFromContext(ctx)
      if !ok { return errs.New(errs.CodeUnauthenticated, "no verified identity") }
      return policy.Authorize(id, tool, instance, rbac.ScopeRead)
  }
  ```

  The middleware never receives `registry.Tool.Permission`, so it cannot pass it. A probe
  built the production policy (`viewer` → `*` at `read`, exactly `cmd/server/main.go:101-104`)
  and called the production closure:

  ```text
  PROBE RESULT: viewer ALLOWED magento.product.delete (authorized at scope=read)
  PROBE RESULT: viewer ALLOWED magento.order.create (authorized at scope=read)
  PROBE RESULT: viewer ALLOWED magento.import.start (authorized at scope=read)
  PROBE RESULT: identity with no roles denied: permission_denied: permission denied
  ```

  A tool inventory built from `registerModules` with `tools.writes: true`:

  ```text
  PROBE RESULT: registered tools=95 by permission=map[destructive:9 read:43 write:43]
  ```

  Default-deny still holds (an identity with no role is denied), but the ladder
  `read < write < destructive < admin` and the per-tool permission are decorative.
- **Trigger**: any caller with a valid token whose `roles` claim contains `viewer` or
  `analyst` (the only two roles the production policy maps), in a deployment with
  `tools.writes: true`. No special access beyond a normal agent token.
- **Impact**: Privilege escalation. A read-only agent can invoke product deletion, category
  deletion, inventory transfer, order cancellation, order creation, invoice capture and
  credit-memo creation. The only remaining gate is the confirmation middleware, which is
  itself bypassable (SEC-03) and whose approver is unverified (SEC-04). The RBAC matrix in
  `threat-model.md:519-540` (operator + confirm for writes) is not implemented.
- **Proposed fix**: change `server.Options.Authorize` to
  `func(ctx, tool, instance, requiredScope string) error`; in `installPolicy` look up
  `registry.Lookup(tool)` and pass `meta.Permission` (deny unknown tools); in `main.go` pass
  that scope to `policy.Authorize`. Add `operator`/`admin` grants. Add a registry test that
  every registered tool's permission is reachable only through a grant of at least that scope.
- **Test requirement**: table test over the full role × tool matrix asserting
  `viewer`+`analyst` are denied `write`/`destructive`/`admin` tools and `operator` is denied
  `destructive`; an HTTP-level test that `magento.product.delete` returns `permission_denied`
  for a viewer token before the confirmation step.
- **Verified by**: (independent review)

### SEC-02: Cross-instance job and import-batch access (no tenant predicate)

- **Severity**: P0
- **State**: CONFIRMED
- **Location**: `internal/magento/import_jobs.go:155-170` (`import.status`),
  `:172-185` (`import.cancel`), `:243-296` (`import.resume`), `:357-378` (`import.batch`);
  `internal/storage/jobqueue.go:214-242` (`GetJob`), `:198-206` (`Cancel`);
  `internal/storage/import_batches.go:136-171` (`GetImportBatch`);
  `internal/mcp/server/server.go:165-175` (`instanceArgument` only reads a field named
  `instance`, which these tools do not have).
- **Evidence**: the tools take only a UUID and call the store with no tenant argument:

  ```go
  // internal/magento/import_jobs.go:161
  record, err := queue.GetJob(ctx, in.JobID)
  ```

  ```sql
  -- internal/storage/jobqueue.go:222-225
  SELECT id::text, kind, state, attempt_count, max_attempts,
         last_error_code, result_ref, created_at, finished_at, payload
  FROM jobs WHERE id = $1::uuid
  ```

  ```sql
  -- internal/storage/import_batches.go:144-147
  SELECT ... FROM import_batches WHERE id = $1::uuid
  ```

  There is no `instance_id`, no `subject_hash`, and no ownership check. `resume` reads the
  other tenant's payload and re-enqueues it with that tenant's instance:

  ```go
  // internal/magento/import_jobs.go:283-287
  jobID, err := queue.Enqueue(ctx, jobs.Task{Kind: jobs.TypeMagentoImport,
      Queue: jobs.QueueImport, InstanceID: payload.Instance, ...})
  ```

  Because these tools carry no `instance` field, `instanceArgument` returns `""`, so RBAC is
  evaluated against the empty instance — a per-instance grant can never match, and the
  wildcard grants shipped in `main.go:102-103` always do.
- **Trigger**: a deployment with `database.url` set (the production configuration), any
  authenticated caller with a role that has a wildcard grant (both shipped roles), and a job or
  batch UUID. UUIDs are not enumerable, but they are returned in
  `import.batches` output (`job_id` is serialised, `internal/jobs/jobs.go:240`) and in logs;
  `import.batches` itself takes an `instance` argument and will list any configured instance.
  Chain: `magento.import.batches{instance:"tenant-b"}` → `job_id` →
  `magento.import.batch{batch_id}` (SKUs + row errors) or
  `magento.import.resume{job_id}` (writes tenant B's rows into tenant B's Magento).
- **Impact**: Cross-tenant data leak (SKUs, per-row validation errors, instance ids, job
  results) and cross-tenant write/availability actions (cancel another tenant's running
  import; re-run it). This is the P0 "cross-instance data leak" class in the audit README.
- **Proposed fix**: add a required `instance` field to all four tools, thread it to the store,
  and add `AND instance_id = $n` to `GetJob`, `Cancel`, `GetImportBatch`; for non-admin roles
  also add `AND subject_hash = $m`. Resolve the instance through the same `Resolver` before
  the store call. Alternatively make `GetJob` take an `(instance, id)` pair and reject a
  mismatch with `not_found` (never `permission_denied`, per `threat-model.md:302`).
- **Test requirement**: two-instance fixture; caller on A calls each of the four tools with
  B's job/batch id and asserts `not_found` with no B data in the response; a storage test
  asserting every job/batch repository method rejects a cross-instance id; a test that
  `import.resume` cannot enqueue for an instance the caller has no grant on.
- **Verified by**: (independent review)

### SEC-03: Confirmation is not bound to the tool or the instance — cross-tool replay

- **Severity**: P0
- **State**: CONFIRMED
- **Location**: `internal/mcp/server/server.go:148` (`Require(..., tool, "", ...)` — instance
  hardcoded empty), `:156` (`Consume` receives no tool or instance);
  `internal/security/confirm/confirm.go:126-132` (`Consume` signature);
  `internal/storage/confirm.go:115-129` (UPDATE has no `tool`/`instance_id` predicate).
- **Evidence**: the `confirmation_tokens` row stores `tool` and `instance_id`
  (`internal/storage/confirm.go:38-43`), and neither is ever read on consume:

  ```sql
  -- internal/storage/confirm.go:116-121
  UPDATE confirmation_tokens
  SET state = 'consumed', consumed_at = $4
  WHERE id = $1::uuid AND state = 'approved' AND expires_at > $4
    AND requester_hash = $2 AND params_hash = decode($3,'hex')
  ```

  A probe drove the real `server.Options.enforceConfirmation` with a real confirmation
  service: it requested approval for `magento.import.cancel`, approved it as a different
  subject, and then replayed the id against `magento.import.resume` with the same arguments:

  ```text
  PROBE: tool A (magento.import.cancel) issued confirmation_id=f52a5dc1-aee8-4679-afa3-5383016f4721
  PROBE RESULT: cross-tool replay ACCEPTED — approval issued for "magento.import.cancel" consumed by "magento.import.resume"
  ```

  A storage probe confirmed the row is consumed with the original tool/instance still recorded
  (`tool="magento.category.delete" instance="store-a" state="consumed"`), i.e. the binding
  exists in the schema and is simply not enforced.
- **Trigger**: any pair of confirmation-requiring tools whose canonical argument hash matches,
  and a human who approves one of them. `magento.import.cancel`/`magento.import.resume`
  (`{"job_id": ...}`) is a real pair in the shipped catalog; other pairs share `{"instance",
  "sku"}` shapes across product/category/media destructive tools.
- **Impact**: An approval for one destructive action authorises a different destructive action
  the approver never saw. This defeats the single control that stands between an agent and
  every write tool (SEC-01 makes that the *only* control). `threat-model.md:546` claims the
  row is keyed with `(requester_hash, tool, instance_id or binding_id, params_hash)` and
  verified on consume; only two of the four are.
- **Proposed fix**: extend `confirm.Service.Consume` and `Store.Consume` to take `tool` and
  `instance`; add `AND tool = $5 AND instance_id IS NOT DISTINCT FROM NULLIF($6,'')` to the
  UPDATE; pass `meta`-derived values from `enforceConfirmation` (and stop hardcoding `""` for
  the instance — use `instanceArgument(call.Params.Arguments)`).
- **Test requirement**: an approval for tool A cannot be consumed by tool B with identical
  arguments (`confirmation_param_mismatch` or `not_found`); the same for instance A vs B; a
  storage test that the UPDATE rejects a tool/instance mismatch; a regression test that the
  legitimate same-tool flow still passes (extend `TestConfirmationFlowThroughHTTP`).
- **Verified by**: (independent review)

### SEC-04: The approval surface trusts a self-asserted approver string

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `cmd/admin/main.go:32` (`-as` flag), `:80-86` (approve path);
  `internal/storage/confirm.go:76-82` (`requester_hash <> $2`);
  `internal/security/confirm/confirm.go:115-123`; no `confirmation:approve` capability exists
  anywhere in the code (`grep -rn "confirmation:approve"` → only the threat model).
- **Evidence**: the only approval surface is a CLI that takes the approver's identity as a
  flag and writes `sha256(flag value)`:

  ```go
  // cmd/admin/main.go:83
  if err := svc.Approve(ctx, flag.Arg(1), as); err != nil { ... }
  ```

  A probe approved an agent's request as the literal string `"x"`:

  ```text
  PROBE RESULT: approved by unverified string "x"; state="approved" (requester was "agent-a")
  ```

  The "different human" rule is therefore `requester_hash != sha256(any other string)`.
  The CLI has no authentication, no role check, no audit row, and no second factor; it only
  needs the database URL from the config file.
- **Trigger**: an actor that can execute the binary or reach PostgreSQL — for example an agent
  running on the same host, a compromised CI job with the database URL, or any operator.
- **Impact**: self-approval is bypassed by choosing any string other than the requester's
  subject. Combined with SEC-03, one approval can be replayed onto a different tool. The
  threat model's "approval requires the `confirmation:approve` permission and a different
  human `sub`" (`threat-model.md:548`) is not implemented.
- **Proposed fix**: authenticate the approver (Cloudflare Access assertion verified with the
  same verifier, an mTLS client cert, or an OS user mapped to a subject), store the *verified*
  subject, and require a configured approver role. Add an audit row for every approval.
- **Test requirement**: an approval whose identity is not a verified subject is rejected; an
  approval by the requester's own subject is rejected; an approval by a subject without the
  approver role is rejected; approval writes an audit row.
- **Verified by**: (independent review)

### SEC-05: Assumption A5 is false — order/money-mutating tools ship with no idempotency

- **Severity**: P0
- **State**: CONFIRMED
- **Location**: `internal/magento/write_tools.go:1421-1535` (`magento.order.create`),
  `:801-955` (`invoice.create`, `shipment.create`, `creditmemo.create`), `:1298-1355`
  (`invoice.capture`), `:1739-1825` (`order.cancel`), `:1631-1733` (`customer.create/update`),
  `:277-300` (registration list); `docs/security/threat-model.md:32` (A5),
  `:407-419` (§5.12).
- **Evidence**: `docs/security/threat-model.md:32` states "Orders and customers are
  **read-only** in v1; no order-mutating tool ships", and `:409` relies on that assumption:
  "Order-mutating tools are absent in v1 (A5), which removes double-order operations
  structurally". The probe inventory shows otherwise:

  ```text
  PROBE RESULT: registered tools=95 by permission=map[destructive:9 read:43 write:43]
  PROBE RESULT: magento.order.create         registered=true
  PROBE RESULT: magento.invoice.create       registered=true
  PROBE RESULT: magento.creditmemo.create    registered=true
  PROBE RESULT: magento.invoice.capture      registered=true
  PROBE RESULT: magento.order.cancel         registered=true
  ```

  `magento.order.create` performs a multi-call admin cart flow (create cart → one call per item
  → shipping information → `POST /V1/carts/{id}/order`, `internal/magento/order_create.go:62-95`);
  a failure leaves the cart behind and a retry starts a second cart. No
  write tool except `magento.import.start` accepts an `idempotency_key`
  (`grep -rn "idempotency_key" internal/magento` → `import_jobs.go` only); there is no
  `internal/jobs/idempotency.go` or `ledger.go`, and no read-back/`suspect` marking.
- **Trigger**: a deployment with `tools.writes: true`, and a client timeout or agent retry after
  `magento.order.create` (or `invoice.create`, `invoice.capture`, `creditmemo.create`) succeeded
  upstream but the response was lost. `internal/magento/client.go:153` returns `upstream_timeout` with the text "the call
  may have completed", which is exactly the case in which a model retries.
- **Impact**: duplicate order, duplicate invoice, duplicate capture or duplicate refund —
  the P0 "duplicate charge/refund" class. The threat model's mitigation (idempotency key +
  fingerprint ledger + read-back) is entirely absent, and the assumption that made the risk
  acceptable is false.
- **Proposed fix**: for v0.11, either remove the order/invoice/creditmemo tools from the
  default write profile and document them as out of scope, or implement the §5.12 controls:
  a required `idempotency_key` on every money/order write, a `(instance, subject, tool, key)`
  unique row with a stored result, and a read-back comparison that marks a mismatch `suspect`.
  Correct A5 in the threat model either way.
- **Test requirement**: same `order.create` request twice with one idempotency key produces one
  order and a `replayed: true` second response; two concurrent `order.create` calls with one
  key produce one order under `-race`; a create whose read-back differs is marked `suspect`;
  a document test asserting A5 matches the registered tool set.
- **Verified by**: (independent review)

### SEC-06: Denied calls are audited as `decision=allow`; no hash chain; audit is in-memory by default

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `cmd/server/main.go:70-71` (`audit.NewMemory()` default),
  `:114-125` (the audit closure hardcodes `Decision: "allow"`);
  `internal/mcp/server/server.go:96-99` (denial calls `Audit` with `state=denied`);
  `internal/storage/audit.go:38-46` (INSERT has no hash columns);
  `internal/storage/migrations/000001_init.up.sql` (`audit_events` has no
  `prev_hash`/`entry_hash`/`signature` columns).
- **Evidence**:

  ```go
  // cmd/server/main.go:121-124
  _ = auditor.Record(ctx, audit.Event{
      Subject: subject, IdentityClass: class, Tool: tool, Instance: instance,
      Decision: "allow", Phase: phase, State: state, ErrorCode: code,
  })
  ```

  The middleware invokes this closure on the denial path (`server.go:96-99`), so every denied
  call is stored with `decision='allow', state='denied'`. The database CHECK permits `'deny'`
  (`000001_init.up.sql`), and nothing ever writes it. There is no `prev_hash`, `entry_hash`,
  Ed25519 checkpoint, or verification job anywhere in the tree (`grep -rn "prev_hash\|entry_hash\|Ed25519"` → no matches).
  With no `database.url`, the process silently falls back to `audit.NewMemory()` and the
  warning is the only signal (`main.go:90`).
- **Trigger**: any authorization denial (routine), any restart without a database, any actor
  with `UPDATE` rights on `audit_events`.
- **Impact**: the "denials are audited" control is inverted — a SIEM query for `decision='deny'`
  returns nothing; the threat model's tamper-evidence claim (§6.6) has no implementation; the
  default deployment loses its whole audit trail on restart. Note also that the audit event
  carries no `params_hash`, `scope`, `confirmation_id`, `intent_id`, or `request_id`
  (`internal/security/audit/audit.go:23-37`), so intent and outcome rows cannot be correlated.
- **Proposed fix**: pass a `decision` argument through `server.Options.Audit` (or derive it
  from `state`), write `deny` on the denial path, add `params_hash`/`confirmation_id`/`scope`,
  add `prev_hash`/`entry_hash` columns with an append-only chain, and refuse to start in
  production without `database.url`.
- **Test requirement**: a denied call inserts exactly one row with `decision='deny'`; an
  outcome row references its intent row; mutating one row makes chain verification fail and
  name the sequence; a startup test asserts the process refuses to run with auth enabled and
  no durable audit store when `MAGE2MCP_ENV=production`.
- **Verified by**: (independent review)

### SEC-07: The approver sees a tool title, not the arguments being approved

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `internal/mcp/server/server.go:148` (`meta.Title` is passed as `summary`);
  `cmd/admin/main.go:70-74` (pending list prints `ID TOOL SUMMARY EXPIRES`).
- **Evidence**: `Require(ctx, tool, "", meta.Permission, meta.Title, requester, args)` stores
  the tool's display title as the summary. `mage2mcp-admin pending` therefore prints, for a
  destructive call, only e.g. `Delete a product`. The parameter hash is stored but never
  shown, and the validated arguments are discarded.
- **Trigger**: every destructive/write approval.
- **Impact**: blind approval. The human cannot see which SKU, which order, which instance, or
  which price is being changed, so "content-bound approval" is a hash the approver cannot
  interpret. This is the human-factors half of AB-6 and it makes SEC-03 and SEC-04
  materially worse.
- **Proposed fix**: store a bounded, redacted canonical summary of the validated arguments
  (the same canonical form used for the hash) and render it in `pending`; show the instance
  and the tool.
- **Test requirement**: a pending confirmation for `magento.product.delete{sku:"X"}` displays
  the SKU and the instance; restricted fields are masked; the summary is length-capped.
- **Verified by**: (independent review)

### SEC-08: The documented Cloudflare Access assertion header is not supported

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `internal/security/auth/auth.go:115-128` (`Authorization: Bearer` only);
  `docs/security/threat-model.md:38` (A11), `:489` (header table), `:505` (service tokens).
- **Evidence**: `bearerToken` reads `r.Header.Values("Authorization")` and requires the
  `Bearer` scheme. `grep -rn "Cf-Access" --include=*.go .` returns no Go match; the string
  exists only in the threat model. The threat model requires exactly one
  `Cf-Access-Jwt-Assertion`, an ingress check against Cloudflare ranges, an origin-lockdown
  startup check, and a header allowlist; none exists.
- **Trigger**: a deployment that follows the threat model and puts Cloudflare Access in front
  of the gateway (the documented only-ingress).
- **Impact**: two failure modes. Either every request is 401 because Access does not set
  `Authorization: Bearer` (availability, and operators "fix" it by disabling auth), or the
  operator adds a header-rewrite rule at the edge that is itself unauthenticated — which
  recreates the origin-reachability hole A2 exists to close. The code's own OIDC bearer path
  is sound; the *documented* path is missing. This is prior finding F-03, still open in code.
- **Proposed fix**: accept exactly one `Cf-Access-Jwt-Assertion` (reject duplicates with
  `duplicate_assertion`), keep `Authorization` as the OIDC fallback, and either implement the
  origin-lockdown startup check or delete the claim from the threat model.
- **Test requirement**: a request with one assertion header authenticates; two are rejected;
  a request with neither is 401; a request with the client-id/secret pair and no assertion is
  401 and the pair is never logged.
- **Verified by**: (independent review)

### SEC-09: Roles come from the token claim, not an operator-controlled mapping

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `internal/security/jwt/jwt.go:170-178` (`roles` claim → `Identity.Roles`),
  `cmd/server/main.go:101-104` (grants keyed by role name).
- **Evidence**: `parseRoles(c.Roles)` reads a `roles` array straight out of the token and puts
  it on the identity; `rbac.Authorize` then matches `id.Roles` against the grant table. There
  is no `map[verifiedSubject][idpGroup]` (as `threat-model.md:286` specifies) and no
  `service_identities` table. A service token is classified by `common_name != "" || type ==
  "service"` (`jwt.go:174-177`), but its `roles` claim is trusted identically to a human's.
- **Trigger**: an IdP whose `roles`/group claim a user can influence (self-service group
  creation, a custom claim mapped from a user-editable attribute), or any IdP that issues a
  `roles` claim for a service token. With Cloudflare Access, which does not issue `roles`, the
  opposite failure occurs: every caller has no roles and every call is denied.
- **Impact**: the gateway delegates role assignment to the IdP claim rather than to operator
  configuration, so the security boundary moves outside the deployment. It also makes the
  documented role matrix (operator/admin) unreachable, which is why only two read-only roles
  exist in `main.go` — the mapping was never built.
- **Proposed fix**: add a configured subject/group → role map (or a `role_assignments` table,
  which the migration already defines at `000001_init.up.sql` and nothing reads), resolve
  roles from it after verification, and treat the token claim as a lookup key only.
- **Test requirement**: a token whose `roles` claim says `admin` but whose configured mapping
  says `viewer` gets `viewer`; an unmapped subject is 403 `no_role`; a service token can never
  resolve to `admin`.
- **Verified by**: (independent review)

### SEC-10: Upstream error text and the request path reach the agent

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/client.go:183-190` (`upstream_message` detail),
  `:141` (`WithOp(method + " " + path)`), `internal/mcp/server/server.go:177-186`
  (`Details` copied into the tool result), `internal/core/errors/errors.go:120-132`
  (`WithDetail` performs no redaction despite its comment).
- **Evidence**: `statusError` extracts Magento's `message` field, strips control characters,
  caps it at 300 runes, and attaches it as `upstream_message`. `toolErrorResult` serialises
  `typed.Details` into `structuredContent.details`, so the agent receives Magento's raw
  validation text (which echoes SKUs, attribute codes, and ACL resource names). The `Op`
  string embeds the request path, e.g. `magento GET /products/SECRET-SKU: not_found: ...`.
- **Trigger**: any upstream 4xx/5xx.
- **Impact**: SKU existence oracle and ACL/route disclosure (prior finding F-16, still open).
  Not cross-tenant, but it violates the stated error contract ("upstream bodies never reach
  the agent") and aids catalog enumeration.
- **Proposed fix**: keep `upstream_message` server-side (log with the request id), return only
  `{code, message, request_id}`, and stop putting the path in `Op` when the path contains an
  identifier.
- **Test requirement**: a 404/422 upstream response yields `not_found`/`validation_failed`
  with no SKU and no ACL name in any field of the tool result; the detail is present in the
  server log.
- **Verified by**: (independent review)

### SEC-11: No rate limiting exists

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: absent. The only `429` handling is the mapping of an *upstream* rate limit
  (`internal/magento/client.go:174-175`, `internal/cloudflare/client.go:180-181`,
  `internal/ga4/client.go:249-250`). No `internal/ratelimit`, no per-subject counter, no
  GA4 budget, no semaphore.
- **Evidence**: `grep -rni "ratelimit" internal cmd` returns only the JWT JWKS refresh
  limiter (`internal/security/jwt/jwt.go:55`). The threat model's §6.7 table (read 60/min,
  export 5/min, write 10/min, import 1/10 min, admin 5/min, instance 600/min, GA4 25 %
  reserve, Cloudflare 150/5 min, 64 in-flight) has no implementation.
- **Trigger**: an agent loop or a hostile caller issuing wide GA4/Cloudflare queries.
- **Impact**: cost amplification and quota exhaustion shared with human analysts (AB-2, AB-3,
  T-25). Reads are the exposed class; writes are slowed only by the approval gate.
- **Proposed fix**: add a per-subject + per-instance limiter in front of the tool handler,
  keyed on the verified subject (never the IP or session), enforced before the upstream call,
  failing closed for write/import.
- **Test requirement**: 1 000 calls with rotating `X-Forwarded-For` and session ids converge on
  one counter; the 61st read in a minute returns `429` with `Retry-After`; no upstream call is
  made after exhaustion.
- **Verified by**: (independent review)

### SEC-12: `/metrics` and `/healthz` are unauthenticated on the public listener

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/mcp/server/server.go:246-259`.
- **Evidence**: only `/mcp` is wrapped by `opts.AuthMiddleware`; `/healthz`, `/readyz` and
  `/metrics` are mounted directly on the mux. `/metrics` exposes the gateway instruments
  (`internal/observability/metrics.go`) including per-tool call counts and the Go/process
  collectors. `/readyz` reports the tool count.
- **Trigger**: any network position that can reach the listener.
- **Impact**: reconnaissance (tool inventory, version via `build_info`, traffic volume). No
  credentials or tenant data are in the labels.
- **Proposed fix**: serve `/metrics` on a separate loopback listener, or require the same
  auth for it.
- **Test requirement**: an unauthenticated GET to `/metrics` is 404/401 on a public bind; the
  loopback metrics listener still works.
- **Verified by**: (independent review)

### SEC-13: `-dev-insecure` disables authentication and authorization on a non-loopback listener

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `cmd/server/main.go:53` (flag), `:59-61` (`MAGE2MCP_DEV_INSECURE` env var),
  `:174-181` (`buildAuth` returns nil middleware), `:227-238` (`authorizeFunc` returns nil).
- **Evidence**: with `-dev-insecure` or `MAGE2MCP_DEV_INSECURE=true`, a non-loopback listener
  starts with no auth middleware and `Authorize == nil`; every read tool is then callable by
  anyone who can reach the port. The write profile is *not* disabled, contrary to
  `threat-model.md:501` ("disables the write profile … rejected when any write tool is
  enabled"). The env-var activation means a stray variable in a container environment flips
  the gateway into this mode.
- **Trigger**: an operator who sets the env var "for local containers" and then exposes the
  listener, or a leaked/typo'd deployment manifest.
- **Impact**: unauthenticated read access to every configured Magento/Cloudflare/GA4 instance
  (all tenant data), plus write tools gated only by the confirmation CLI.
- **Proposed fix**: keep the flag only (drop the env var), refuse to start when
  `tools.writes` is true or `MAGE2MCP_ENV=production`, and report the mode in `/healthz`.
- **Test requirement**: `-dev-insecure` with writes enabled fails startup; the env var alone
  does not enable the mode; `/healthz` reports `insecure_origin: true`.
- **Verified by**: (independent review)

### SEC-14: Confirmation hashing decodes numbers as `float64`, losing integer precision

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/security/confirm/confirm.go:146-171` (`json.Unmarshal` into `any`,
  `case float64`), `:161-218` (custom canonicaliser, not RFC 8785).
- **Evidence**: `CanonicalHash` unmarshals into `any`, so every number becomes a `float64`.
  `{"customer_id": 9007199254740993}` and `{"customer_id": 9007199254740992}` canonicalise to
  the same string and therefore the same hash. The document claims RFC 8785 JCS
  (`threat-model.md:552`), which the custom encoder is not (it uses `encoding/json` number
  formatting and does not reject non-finite values, though `json.Unmarshal` already does).
- **Trigger**: a confirmation-requiring tool with an integer argument above 2^53 — currently
  none in practice (Magento ids are small), so this is latent.
- **Impact**: two distinct argument sets can share an approval hash, weakening the
  content-binding property that SEC-03 already undermines. Latent today, real if an id or a
  timestamp is ever compared as a large integer.
- **Proposed fix**: decode with `json.Decoder.UseNumber`, reject non-integral numbers for
  integer-typed fields, or hash the exact validated argument bytes instead of re-encoding.
- **Test requirement**: two payloads differing only above 2^53 produce different hashes; a
  payload with `1` and `1.0` produces the same hash; the canonical form matches a JCS test
  vector.
- **Verified by**: (independent review)

### SEC-15: The MCP session is not bound to the caller identity

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/mcp/server/server.go:233-244` (the SDK streamable handler is wrapped
  by auth but no session/identity check is added);
  `go-sdk@v1.8.0/mcp/streamable.go:555-576` (session lookup by `Mcp-Session-Id` only).
- **Evidence**: the SDK resolves a session purely from the header; the gateway's middleware
  verifies a token per request but never compares the session owner with the caller.
  `threat-model.md:498` claims "a session presented with a different subject is 404 (T-11)";
  no such check exists.
- **Trigger**: an attacker who obtains a session id (log, proxy, shared client) and has any
  valid token of their own.
- **Impact**: the attacker attaches to another caller's session and receives its notifications
  and in-flight state. It is *not* a privilege escalation because authorization uses the
  caller's own identity on every request, which is why this is P2 and not P0.
- **Proposed fix**: store the verified subject with the session at creation and reject a
  mismatched subject with 404 (not 403, per the threat model's anti-probing rule).
- **Test requirement**: two sessions with different subjects; presenting session A's id with
  subject B's token returns 404; the legitimate client is unaffected.
- **Verified by**: (independent review)

### SEC-16: `platform.info` bypasses the registry contract

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/mcp/server/server.go:63` (registered directly on the server),
  `:197-219` (definition has no `Permission`, `Capability`, or `Timeout`).
- **Evidence**: every other tool goes through `registry.Register`, which validates capability,
  permission, timeout, and annotation consistency (`internal/mcp/registry/registry.go:127-171`).
  `platform.info` is added with `toolerr.Add` before the loop and is absent from
  `registry.List()` — it is not in the 95-tool inventory and carries no declared permission.
  It is still authorized by the middleware at `read` (SEC-01), so it is not unauthenticated.
- **Trigger**: none directly; it is a contract gap, not an exploit.
- **Impact**: the "every registered tool has a required scope" invariant is false; a future
  change that trusts registry membership (e.g. a per-tool policy table) would silently skip it.
- **Proposed fix**: register `platform.info` through `registry` with `PermissionRead`,
  a capability, and a timeout.
- **Test requirement**: `registry.Len()` includes `platform.info`; a test asserts every tool
  in `tools/list` has a registry entry with a non-empty permission.
- **Verified by**: (independent review)

### SEC-17: Credentials are inline config strings with no redacting type

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/core/config/config.go:95` (`Credential string`), `:121`
  (`os.ExpandEnv` over the whole file), `internal/security/secrets/` (empty package).
- **Evidence**: `Instance.Credential` is a plain `string`; `Config` has no `String()`/`Format`
  method, so `fmt.Sprintf("%+v", cfg)` prints every bearer token, OAuth secret, admin password,
  and GA4 service-account JSON. The threat model claims a `Secret` type whose every formatting
  path redacts (`threat-model.md:330`) and `secret://` references resolved at startup
  (`:556`); neither exists. `os.ExpandEnv` means a credential value containing `$` is silently
  rewritten, and a secret can live in the config file as well as the environment.
- **Trigger**: a panic with the config in scope, a debug print, or a support bundle that
  includes the config.
- **Impact**: credential disclosure to logs/artifacts. The logger's key-based redaction
  (`internal/observability/logger.go:81-89`) only helps for slog attributes, not for `%+v` of
  a struct.
- **Proposed fix**: introduce a `Secret` type with `String`/`GoString`/`MarshalJSON` returning
  `[REDACTED]`, resolve `secret://` references, and refuse a config file whose mode is
  group/world-readable.
- **Test requirement**: `fmt.Sprintf("%+v", cfg)` contains no credential; a config with a
  `secret://` reference resolves from the environment; a 0644 config file is refused.
- **Verified by**: (independent review)

### SEC-18: Import base64 is fully decoded before the size check

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/magento/import.go:148-156`.
- **Evidence**: `base64.StdEncoding.DecodeString(encoded)` allocates the whole decoded buffer
  and only then checks `len(decoded) > maxImportBytes`. The threat model claims an
  `io.LimitReader` after decode with a length-mismatch rejection (`threat-model.md:352`).
  In practice the input is bounded by the JSON Schema `maxLength: 11184812` and the 1 MiB
  HTTP body cap (`cmd/server/main.go:142`), so the worst case is an ~11 MB allocation, not a
  bomb.
- **Trigger**: a caller who raises `max_body_bytes` and sends a maximal payload.
- **Impact**: bounded memory amplification only; no confidentiality or integrity impact.
- **Proposed fix**: decode through `base64.NewDecoder` + `io.LimitReader`, and reject a
  length mismatch.
- **Test requirement**: a payload that decodes to more than 8 MiB is rejected without
  allocating the full buffer (assert with a large input under a memory limit).
- **Verified by**: (independent review)

### SEC-19: Job state transitions have no guard

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/storage/jobqueue.go:155-163` (`Complete`), `:167-184` (`Fail`),
  `:198-206` (`Cancel`), `:245-255` (`SetResult`).
- **Evidence**: each statement updates by `id` alone:

  ```sql
  UPDATE jobs SET state = 'succeeded', ... WHERE id = $1::uuid
  ```

  There is no `AND state IN (...)` guard, no transition table, and no compare-and-set. A
  canceled job can be marked `succeeded` by a late worker; `SetResult` can overwrite the
  result of any job. `threat-model.md:428-433` claims an explicit transition table with a CAS
  `UPDATE`; the migration has the state CHECK but the writer does not enforce transitions.
- **Trigger**: a worker that finishes after an operator cancelled the job (the cancel path is
  exactly the race the threat model describes), or any actor with database access.
- **Impact**: task-state tampering — a cancelled import can be recorded as succeeded, and a
  failed job's result can be overwritten. Requires worker or DB access, hence P2.
- **Proposed fix**: add `AND state = 'running'` to `Complete`/`Fail`, `AND state IN
  ('queued','running')` to `Cancel` (already present), and make `SetResult` conditional on a
  running lease.
- **Test requirement**: complete-after-cancel is refused; an exhaustive illegal-transition
  matrix returns `illegal_transition`; a concurrent cancel/complete pair under `-race` has
  exactly one winner.
- **Verified by**: (independent review)

---

## 3. Threat-model reconciliation

Legend: **Implemented** (code does it, path:line cited), **Implemented-but-not-tested** (code
does it, no test proves it), **Planned** (not implemented), **N/A** (does not apply to this
architecture). "DOC" marks a document statement that is false of the code.

The threat model's own path names (`internal/auth`, `internal/httpx`, `internal/registry`,
`internal/config`, `internal/cache`, `internal/egress`, `internal/quota`, `internal/ratelimit`,
`internal/upstream`, `internal/media`, `internal/redact`, `internal/secret`, `internal/log`,
`internal/metrics`, `internal/store`, `internal/modules/*`) do not exist; the real packages are
`internal/security/*`, `internal/mcp/*`, `internal/storage`, `internal/magento`. Every
"Implemented" row below cites the real location.

### 3.1 Architectural assumptions (A1–A12)

| Assumption | Status | Evidence |
|---|---|---|
| A1 one deployment, many instances (tenancy = instance) | Implemented | `internal/magento/resolver.go:41-58`, `internal/cloudflare/resolver.go`, `internal/ga4/resolver.go`; multi-instance writes wired at `internal/magento/write_tools.go:269-274` |
| A2 Cloudflare Access is the only ingress; origin not publicly routable | Planned | no ingress/range check, no origin-lockdown startup check (`cmd/server/main.go:145-160` binds any `server.listen`); SEC-08 |
| A3 session stateless except an id | Planned | SDK keeps server-side sessions (`go-sdk/streamable.go:555`); no session/identity binding (SEC-15) |
| A4 no raw GraphQL passthrough | Implemented | `internal/cloudflare/tools.go` exposes fixed query templates only; no GraphQL text parameter |
| **A5 orders/customers read-only, no order-mutating tool** | **DOC — false** | `magento.order.create` (`write_tools.go:1489`), `invoice.create`/`capture`, `creditmemo.create`, `order.cancel`, `customer.create/update` are registered; probe shows 43 write + 9 destructive tools (SEC-05) |
| A6 per-instance Magento credentials | Implemented | `internal/magento/credential.go:48-74`, one `Service` per instance |
| A7 non-JSON upstream is a protocol error | Implemented | `json.Unmarshal` on every response; `internal/magento/client.go:143-147`; a 200 HTML body fails decode → `upstream_unavailable` |
| A8 no customer PII persisted | Implemented | `audit_events` stores only `subject_hash`; import batches store SKUs and error text, not customer rows (`internal/storage/import_batches.go:50-59`) |
| A9 tools statically registered | Implemented | `registry.Register` at startup only (`internal/mcp/registry/registry.go:72-83`) |
| A10 agent client untrusted | Implemented | input validation + bounded schemas; but authorization is broken (SEC-01) |
| A11 service tokens validated at the edge, assertion only | Planned | no assertion header support (SEC-08) |
| A12 R2/ClickHouse in scope | N/A | no R2 or ClickHouse code in the tree |

### 3.2 Threat analyses §5.1–§5.16

| Control (threat-model ref) | Status | Evidence / note |
|---|---|---|
| §5.2 duplicate-key / prototype rejection, `UseNumber`, depth/array caps, 1 MiB body | Planned | no `internal/mcp/strictjson.go`; the SDK decodes arguments and `jsonschema-go` validates; duplicate keys are accepted (last wins); the 1 MiB body cap exists (`cmd/server/main.go:142`) |
| §5.2 identifier grammar shared by schema and Go validator | Implemented-but-not-tested | `skuInputPattern`/`validateSKU` in `internal/magento/tools.go`; no contract test that the two agree (prior F-15/R-16 remain open) |
| §5.2 path building escapes and asserts `/V1/` prefix | Implemented-but-not-tested | `url.PathEscape` at every call site (`internal/magento/*.go`); no explicit `..`/`%2e` assertion after escaping |
| §5.3 role = `map[verifiedSubject][idpGroup]` | Planned | roles read from the token claim (SEC-09) |
| §5.3 instance from route + `aud`; disagreeing body instance is a deny | Planned | instance comes from the body argument only (`server.go:165-175`); `aud` is a single global value (`cmd/server/main.go:182-186`) |
| §5.3 registry `Register` rejects missing permission; `Decide` denies unknown tools | Implemented (validation) / Planned (decision) | `registry.go:127-171` validates; `Decide` does not exist and the permission is never consumed (SEC-01) |
| §5.3 per-instance credential references | Implemented | `internal/magento/credential.go:48-74` |
| §5.3 egress asserts the outbound host belongs to the instance | Planned | no egress layer; the client trusts `BaseURL` from config |
| §5.3 write tools absent unless the write profile is enabled | Implemented | `cmd/server/main.go:272-276`, `internal/core/config/config.go:41-43` |
| §5.4 cache key includes instance | N/A | no cache exists |
| §5.4 no process-global upstream client | Implemented | one `Service`/`Client` per instance (`internal/magento/credential.go:49-73`) |
| §5.4 store refuses a tenant query without an instance predicate | **Planned — contradicted** | `GetJob`/`GetImportBatch` have no instance predicate (SEC-02) |
| §5.4 upstream bodies never reach the agent | **Planned — contradicted** | `upstream_message` reaches the tool result (SEC-10) |
| §5.4 cross-instance lookups return 404 | Planned | `resolver.Resolve` returns 404 for an unknown instance (`resolver.go:54-57`), but the job/batch stores have no check (SEC-02) |
| §5.5 RS256/ES256 allowlist, `none`/HMAC rejected | Implemented | `internal/security/jwt/jwt.go:52,141-146,258-260`; tested `jwt_test.go:258` |
| §5.5 `kid` as lookup key only; no `jku`/`x5u` | Implemented | `jwt.go:261-290`; the JWKS struct has no `jku` field (`:368-377`) |
| §5.5 JWKS over TLS, redirects disabled, 3 s timeout | Implemented | `jwt.go:100-102` (https required), `:118` (redirects off), `:342` (timeout, default 5 s not 3 s) |
| §5.5 exact `iss`, `aud` = instance Access application | Implemented (global `aud`) / Planned (per instance) | `jwt.go:161-166`; one audience for all instances (`config.go:64`) |
| §5.5 `exp` required, `nbf`/`iat` honoured, ≤ 60 s skew | Implemented | `jwt.go:148-153`; tested `jwt_test.go:231` |
| §5.5 `exp - iat` ≤ 24 h | Planned | not checked anywhere |
| §5.5 8 KiB token cap | Implemented | `jwt.go:37,134-136`; tested `jwt_test.go:336` |
| §5.5 exactly one assertion header, raw header never logged | Implemented for `Authorization` / Planned for the assertion | `auth.go:115-128` rejects duplicates; token never logged (`auth_test.go:218`) |
| §5.5 replay cache `(token hash, session id)` | Planned | absent |
| §5.6 `Secret` type that redacts every formatting path | Planned | `internal/security/secrets/` is empty (SEC-17) |
| §5.6 exact-value + regex secret redaction in logs | Implemented (partial) | key-fragment + `Bearer` redaction in `internal/observability/logger.go:81-112`; no exact-value or base64/JWT regex |
| §5.6 response builder rejects secret-shaped strings | Planned | no such builder |
| §5.6 metrics label allowlist | Implemented | `internal/observability/metrics.go` labels are module/tool/outcome/status only |
| §5.6 crash hygiene | Planned | not configured in the Dockerfile/process |
| §5.7 no tool accepts a URL; media import takes bytes | Implemented | `mediaUploadSchema` takes `data_base64` (`write_tools.go:752-759`); no `from_url` tool |
| §5.7 outbound hosts from validated instance config only | Implemented | `internal/magento/client.go:58-89`; Cloudflare/GA4 base URLs are constants (`cmd/server/main.go:287`) |
| §5.7 IP guard (loopback/link-local/metadata/RFC1918), 4-in-6 | Planned | no `internal/egress`; only the scheme/host check at `client.go:64-70` |
| §5.7 resolve-once/dial-validated-IP, cross-host redirect refusal | Planned (redirect refusal: Implemented) | `client.go:86` refuses redirects; no IP pinning |
| §5.7 instance base URLs validated at startup | Implemented (scheme/host only) | `client.go:58-89`; a metadata IP is *not* rejected (only scheme/userinfo/path) |
| §5.8 strict base64, 8 MiB cap, CSV 64 cols / 5 000 rows | Implemented | `internal/magento/import.go:148-156,159-190` (decode-before-check: SEC-18) |
| §5.8 format allowlist csv/json/xlsx, zip/OLE2 rejected | Implemented (narrower) | only csv/json (`importStartSchema`, `import_jobs.go:203-210`); xlsx is not offered |
| §5.8 formula-injection neutralisation on export | N/A | no export tool ships |
| §5.8 filenames never used as paths | Implemented | `mediaFilenameRE` (`internal/magento/media.go:70,156`) |
| §5.8 import requires operator + confirmation with payload hash | Implemented (confirmation) / Planned (operator role) | `writeToolMetadata` sets confirmation; no operator role exists (SEC-01) |
| §5.9 image decode-and-re-encode, 40 MP cap, SVG rejected | Planned | media upload validates MIME + filename and forwards base64 unchanged (`media.go:80-100`); no decode, no re-encode, no dimension cap |
| §5.9 never serve image bytes back | Implemented | media tools return metadata only (`media.go:18-27`) |
| §5.10 parameterised query templates, no raw passthrough | Implemented | GA4/Cloudflare query builders; no raw query parameter |
| §5.10 static cost estimate + per-subject budget | Planned | absent |
| §5.10 caps: pageSize ≤ 200, 5 000 rows, 100 bulk items | Implemented (with one divergence) | schemas cap `limit` at 200 (`internal/magento/tools.go:53`), import rows at 5 000 (`import.go:24`); bulk updates cap at **50**, not the documented 100 (`internal/magento/product_bulk.go:12`) |
| §5.10 job status 2 s minimum poll interval | Planned | `import.status` polls the DB with no interval gate |
| §5.11 rate limit keyed on the verified subject | Planned | absent (SEC-11) |
| §5.12 idempotency key on every write, 24 h TTL | Planned | only `import.start` accepts one (SEC-05) |
| §5.12 duplicate-fingerprint ledger | Planned | absent |
| §5.12 confirmation CAS from `approved` to `consumed` | Implemented | `internal/storage/confirm.go:115-129`; probe: 20 concurrent consumes → 1 success |
| §5.12 confirmation bound to hash + identity + tool + instance | Partially implemented — **DOC** | hash + identity enforced; tool/instance not (SEC-03) |
| §5.12 read-back after create, `suspect` marking | Planned | absent |
| §5.12 30 s Redis lock per entity | Planned | no Redis anywhere |
| §5.13 explicit job transition table, status not client-writable | Planned | no transition guard (SEC-19); status is not client-writable (good) |
| §5.13 every job query includes instance/subject; foreign job 404 | Planned | absent (SEC-02) |
| §5.13 cooperative cancellation re-read before each batch | Planned | `import.status`/`cancel` only; the worker does not re-read state between batches (`cmd/worker/main.go:124-128`) |
| §5.13 result payload hash + audit sequence | Planned | absent |
| §5.14 only 2xx with an allowlisted content type | Implemented | `internal/magento/client.go:138-147`; Cloudflare/GA4 clients map status |
| §5.14 bounded response size (8 MiB) | Implemented | `client.go:28,143`; Cloudflare/GA4 have their own caps |
| §5.14 log injection neutralised | Implemented | `upstreamMessage` strips control characters (`client.go:206-211`); slog JSON encoding escapes the rest |
| §5.15 dependency supply chain (SHA-pinned actions, SBOM, provenance) | Planned for this audit's scope | not re-verified here; owned by the CI/architecture review |
| §5.16 R2/ClickHouse log stores | N/A | not implemented |

### 3.3 Control design §6.1–§6.8

| Control | Status | Evidence / note |
|---|---|---|
| §6.1 header: one `Cf-Access-Jwt-Assertion` | Planned | `Authorization` only (SEC-08) |
| §6.1 size ≤ 8 KiB | Implemented | `jwt.go:134-136` |
| §6.1 algorithm allowlist | Implemented | `jwt.go:52,141-146,258-260` |
| §6.1 key by `kid` only | Implemented | `jwt.go:261-290` |
| §6.1 signature over the exact serialization | Implemented | `golang-jwt` `ParseWithClaims` (`jwt.go:154`) |
| §6.1 exact issuer | Implemented | `jwt.go:161-163` |
| §6.1 audience per instance | Planned | one global audience |
| §6.1 time checks | Implemented | `jwt.go:148-153` |
| §6.1 `exp - iat` ≤ 24 h | Planned | absent |
| §6.1 `type` claim values confirmed | Planned | `type` is read but not validated (`jwt.go:175`); classification falls back to `common_name` |
| §6.1 replay cache / cross-session alert | Planned | absent |
| §6.1 identity from a configured group map | Planned | token claim (SEC-09) |
| §6.1 JWKS caching: 10 min TTL, single-flight, 1/60 s forced refresh, 30 s negative cache | Implemented | `jwt.go:37-38,293-338`; "stale-while-error 1 h" is **not** implemented (fails closed instead — safer) |
| §6.1 service-token identities | Planned | no service-identity config, no assertion header |
| §6.1 origin lockdown / `--dev-insecure-origin` semantics | Planned | `-dev-insecure` exists but disables auth rather than only writes (SEC-13) |
| §6.2 default deny | Implemented | `rbac.go:75-100`; `rbac_test.go:39` |
| §6.2 deny beats allow | Implemented | `rbac.go:88-90`; `rbac_test.go:73` |
| §6.2 scope ladder read<write<destructive<admin | Implemented in the engine, **not wired** | `rbac.go:42-47,91-93`; the only call site passes `read` (SEC-01) |
| §6.2 tool prefix matching | Implemented | `rbac.go:102-107` |
| §6.2 instance matching | Implemented (engine) | `rbac.go:109-111`; wildcard grants make it a no-op in production (SEC-02) |
| §6.2 role matrix viewer/analyst/operator/admin | Planned | only `viewer` and `analyst` exist, both read-only (`cmd/server/main.go:101-104`) |
| §6.2 instance scope intersected with `aud` | Planned | absent |
| §6.2 `confirmation:approve` capability | Planned | absent (SEC-04) |
| §6.2 `roles.rank` never read | Implemented | `rank` is a DB column only (`000001_init.up.sql`); no Go reference |
| §6.3 opaque id, never an approval secret | Implemented | `confirm.go:220-228` (random UUID, not v7 as documented) |
| §6.3 requester never approves | Implemented (string comparison only) | `confirm.go:280-282`, `storage/confirm.go:81`; bypassable (SEC-04) |
| §6.3 bound to `(tool, instance, params_hash)` | Partially implemented — **DOC** | only `params_hash` is checked (SEC-03) |
| §6.3 TTL 300 s default, 900 s cap | Implemented | `confirm.go:39-42,79-90` |
| §6.3 single use by compare-and-set | Implemented | `storage/confirm.go:115-129`; probe: 1 of 20 concurrent consumes succeeds |
| §6.3 approval surface is a human-only CLI, not an MCP tool | Implemented | `cmd/admin/main.go`; no approval tool in the catalog |
| §6.3 cap of 5 pending per subject/instance | Planned | absent |
| §6.3 reject → `revoked` | Planned | no reject command (`cmd/admin/main.go:60-90`) |
| §6.3 canonical JSON = RFC 8785 JCS | Planned | custom canonicaliser (SEC-14) |
| §6.3 approvals audited with both identities | Planned | approvals write no audit row |
| §6.4 `secret://` references, mounted file 0400, env with warning | Planned | inline config strings (SEC-17) |
| §6.4 no secret in logs/metrics/MCP response | Implemented (partial) | key-based log redaction; errors do not carry credentials (`internal/core/errors/errors.go:93-101`); `%+v` of config does (SEC-17) |
| §6.5 all egress through one package, https + 443, no env proxy, IP policy | Planned | per-client `http.Client`; https enforced except loopback (`client.go:64-67`); default transport honours `HTTP_PROXY` |
| §6.6 two-row intent/outcome audit | Partially implemented | intent + outcome rows are written (`server.go:91-115`), but with no correlation id and `decision` always `allow` (SEC-06) |
| §6.6 never record credentials/tokens/bodies | Implemented | `audit.Event` has no body/credential field (`audit.go:23-37`) |
| §6.6 `entry_hash` chain + Ed25519 checkpoint | Planned | no columns, no signing (SEC-06) |
| §6.6 fail closed for writes when the intent row cannot be written | Planned | `_ = auditor.Record(...)` ignores the error (`main.go:121`) |
| §6.6 retention 365 d, monthly partitions | Planned | single unpartitioned table |
| §6.7 rate limits (subject/instance/deployment) | Planned | absent (SEC-11) |
| §6.8 per-instance capability registration, `unsupported_capability` | Partially implemented | `Capabilities` exists on `datasource.DataSource` (`internal/core/datasource/datasource.go:22`) but no module implements it for registration; `unsupported_capability` is returned ad hoc (`order_create.go:97`, `customer.go:502`) |

### 3.4 Abuse cases §7 and residual risks §8

| Item | Status | Note |
|---|---|---|
| AB-1 identical-call dedupe (60 s cache, 429 on the 5th) | Planned | absent |
| AB-2 per-subject token bucket + 300 calls/session | Planned | absent |
| AB-3 static cost model + daily budget | Planned | absent |
| AB-4 restricted-read → write sequence alert | Planned | absent |
| AB-5 stored-content scanner / `untrusted_persisted` | Planned | absent |
| AB-6 approval bound to the exact parameter hash, single use, 300/900 s | Partially implemented | hash + single use + TTL hold; tool/instance binding (SEC-03) and a verified approver (SEC-04) do not |
| AB-7 generated filenames | Implemented | `mediaFilenameRE` |
| AB-8 response/row caps + 50 MiB session budget | Partially implemented | per-call caps exist; no session budget |
| R-1 prompt injection residual | N/A (risk) | the write path exists, so R-1's "structural controls remove the write path" rationale is **false** in this commit (SEC-05) |
| R-2 stolen JWT usable until expiry | N/A (risk) | no replay cache, so the "correlate and alert" mitigation is absent |
| R-3 audit/job share PostgreSQL | Implemented (as a risk) | one `Store` (`internal/storage/storage.go:30-32`) |
| R-4 quota shared with humans | N/A (risk) | no budget exists, so the mitigation is absent |
| R-5 Magento credential power depends on ACL | Implemented (as a risk) | per-instance credentials, no ACL probe |
| R-6 rate limiting fails open for reads | N/A (risk) | no limiter |
| R-7 restricted data in the agent runtime | N/A (risk) | outside the boundary |
| R-8 no reproducible build | N/A (risk) | not re-verified here |
| R-9 wildcard egress allowlist | N/A | no allowlist |
| R-10 operator-added modules bypass the v1 restriction | N/A | no plugin mechanism |
| R-11 raw-log stores outside the process | N/A | not implemented |

### 3.5 Prior-review reconciliation (F-01…F-33, R-01…R-19)

The Phase 0 reviews were written against documents with no code (`security-review.md:5`). They
are therefore not "findings in the current commit" and are not counted in §1; the table records
what the code now does.

| Prior ID | Current state | Evidence |
|---|---|---|
| F-01 (Cloudflare read-only guarantee) | NOT-REPRODUCIBLE as a code defect | the document withdrew the guarantee; no mutation code path exists |
| F-02 (Magento ACL manifest) | NOT-REPRODUCIBLE as a code defect; residual PLANNED | no startup ACL probe in the code |
| F-03 (two ingress paths) | **CONFIRMED in code** → SEC-08 | only `Authorization` is read |
| F-04 (replay semantics) | PLANNED | no replay cache |
| F-05 (GA4 tenancy) | PLANNED | `internal/ga4/resolver.go` keys on instance id; property-level grants do not exist |
| F-06 (XLSX/gzip bombs) | ALREADY-FIXED (narrowed) | xlsx/gzip are not accepted (`import_jobs.go:203-210`) |
| F-07 (audit immutability) | PLANNED | SEC-06 |
| F-08 (R2/ClickHouse) | N/A | not implemented |
| F-09/F-10 (supply chain) | out of scope for this report | see the architecture/CI review |
| F-11 (superseded confirmation mechanism) | ALREADY-FIXED | no HMAC/nonce; opaque random id (`confirm.go:220-228`) |
| F-12 (admin scope) | NOT-REPRODUCIBLE | no `admin`-permission tool ships |
| F-13 (`type` claim) | **CONFIRMED in code** → part of SEC-09 | `jwt.go:174-177` |
| F-14 (unconstrained attribute map) | ALREADY-FIXED | `attributesProp` uses `additionalProperties: {type: string, maxLength: 255}` and `attributeCodeInputPattern` constrains codes (`write_tools.go:85-86`) — still an arbitrary map, but bounded |
| F-15 (SKU grammar) | PARTIALLY FIXED | one pattern is used by schemas (`skuInputPattern`); `validateSKU` in Go matches it — verify with a contract test |
| F-16 (error leakage) | **CONFIRMED** → SEC-10 | `upstream_message` |
| F-19 (unbounded GA4 caches) | NOT-REPRODUCIBLE | no metadata cache in the tree |
| F-20 (`pii_unmask`) | N/A | no unmask flag ships |
| F-21 (unsalted `params_hash`) | N/A | `params_hash` is never written (`main.go:121-124` omits it) |
| F-25 (GraphQL `errors` array) | NOT-REPRODUCIBLE | no GraphQL client; Cloudflare uses the analytics REST/GraphQL template and maps `errors` |
| F-26 (http on localhost) | **CONFIRMED** (dev-only) | `client.go:64-67` |
| F-27 (admin self-grant) | N/A | no role administration surface |
| F-28 (read→write chain) | NOT-REPRODUCIBLE as stated | `product.update` builds the payload from caller arguments and PUTs without a prior read (`internal/magento/product_write.go:202-218`); `UpdateConfigurable` reads first but uses the result only as a type gate (`internal/magento/configurable.go:539-547`); no upstream field is copied into a write payload. The residual is SEC-07 (the approver sees no parameters), not a read→write taint |
| F-29 (WebP re-encode) | NOT-REPRODUCIBLE | no image decode path at all |
| F-30 (URL tool) | ALREADY-FIXED | no URL-accepting tool exists |
| F-31 (description hash) | N/A | not implemented, not claimed in code |
| R-02 (`confirmation:approve` naming) | **CONFIRMED** → SEC-04 | neither name exists in code |
| R-03 (deny row CHECK) | **CONFIRMED, different cause** → SEC-06 | the CHECK accepts `denied`; the writer never emits `deny` |
| R-05 (tag-pinning lint) | out of scope | see CI review |
| R-11 (error taxonomy) | ALREADY-FIXED | the four codes are in `internal/core/errors/errors.go:49-51,65` |
| R-15/R-16 (schema constraints) | ALREADY-FIXED | `write_tools.go:85-93` |

---

## 4. Security tests that must exist before a P0/P1 can be closed

| Finding | Required test (blocking) |
|---|---|
| **SEC-01** | `TestRBACScopeLadderEnforced`: 4 roles × 95 tools × 2 instances matrix; `viewer`/`analyst` denied every `write`/`destructive`/`admin` tool, `operator` denied `destructive`, `admin` allowed; HTTP-level test that `magento.product.delete` returns `permission_denied` for a viewer token *before* the confirmation step. |
| **SEC-01** | `TestEveryRegisteredToolHasEnforcedScope`: iterate `registry.List()` and assert the authorization layer receives a non-empty scope equal to `Tool.Permission`. |
| **SEC-02** | `TestCrossInstanceJobIsolation`: two instances, caller A, every one of `import.status/cancel/resume/batch` with B's id → `not_found`, no B field in any response, no new job enqueued for B. |
| **SEC-02** | `TestStorageJobQueriesRequireInstance`: a repository-level table test asserting every job/batch method rejects a mismatched `instance_id` (and `subject_hash` for non-admin). |
| **SEC-03** | `TestConfirmationBoundToToolAndInstance`: approval for tool A rejected for tool B with identical args; approval for instance A rejected for instance B; storage-level UPDATE test; legitimate same-tool flow still passes. |
| **SEC-05** | `TestOrderCreateIdempotent`: same request + same key → one order, second response `replayed: true`; two concurrent creates with one key → one order under `-race`; read-back mismatch → `suspect`. |
| **SEC-05** | `TestThreatModelA5MatchesToolSet`: parse the registered tool set and fail if any `magento.order.*`/`invoice.*`/`creditmemo.*`/`customer.*` write exists while the threat model asserts A5. |
| **SEC-04** | `TestApprovalRequiresVerifiedSubject`: unverified `-as` string rejected; self-subject rejected; subject without the approver role rejected; approval writes an audit row. |
| **SEC-06** | `TestDeniedCallAuditedAsDeny`: denied call inserts exactly one row with `decision='deny'`, `state='denied'`; intent/outcome rows share a correlation id. |
| **SEC-06** | `TestAuditChainTamperEvidence`: mutate one row → verification fails and names the sequence; the writer role has no `UPDATE` on `audit_events`. |
| **SEC-06** | `TestProductionRequiresDurableAudit`: auth enabled + no `database.url` + `MAGE2MCP_ENV=production` → startup fails. |
| **SEC-07** | `TestApprovalSummaryShowsParameters`: pending output for `product.delete{sku:"X"}` contains the SKU and the instance; restricted fields masked. |
| **SEC-08** | `TestAccessAssertionHeader`: one assertion authenticates, two are rejected, zero is 401, the client-id/secret pair is never read or logged. |
| **SEC-09** | `TestRoleMappingFromConfig`: token `roles` claim ignored; configured mapping wins; unmapped subject 403 `no_role`; service token never maps to `admin`. |

Existing tests that must be extended rather than replaced:
`internal/mcp/server/server_test.go:240` (`TestConfirmationFlowThroughHTTP` — add cross-tool and
cross-instance cases), `internal/security/rbac/rbac_test.go:87` (extend the matrix to the real
tool set), `internal/security/confirm/confirm_test.go:51` (add tool/instance binding),
`internal/storage/storage_test.go:120` (`TestConfirmStoreLifecycle` — add the cross-instance
case). No test may be deleted or weakened.

---

## 5. Verification evidence (verbatim)

Environment: `go version go1.26.0 darwin/arm64`; PostgreSQL 16 at
`127.0.0.1:55440/mage2mcp_sec` supplied for the storage tests.

### 5.1 Mandated command 1 — `go test ./internal/security/... ./internal/storage/...`

```text
$ go test -count=1 ./internal/security/... ./internal/storage/...
ok      github.com/shuaiZend/mage2mcp/internal/security/audit    0.078s
ok      github.com/shuaiZend/mage2mcp/internal/security/auth    0.095s
ok      github.com/shuaiZend/mage2mcp/internal/security/confirm    0.081s
ok      github.com/shuaiZend/mage2mcp/internal/security/jwt    1.322s
ok      github.com/shuaiZend/mage2mcp/internal/security/rbac    0.083s
ok      github.com/shuaiZend/mage2mcp/internal/storage    0.113s
```

Note: without `MAGE2MCP_TEST_DATABASE_URL`, the 13 storage tests `t.Skip()` — the package
reports `ok` with **zero executed tests**, so default CI does not exercise the confirmation or
audit persistence layer. With the variable set, all 13 pass:

```text
$ MAGE2MCP_TEST_DATABASE_URL='postgres://…/mage2mcp_sec?sslmode=disable' go test -count=1 -v ./internal/storage/...
=== RUN   TestMigrateIsIdempotent
--- PASS: TestMigrateIsIdempotent (0.09s)
=== RUN   TestAuditRecorderRoundTrip
--- PASS: TestAuditRecorderRoundTrip (0.01s)
=== RUN   TestAuditRejectsMissingFields
--- PASS: TestAuditRejectsMissingFields (0.00s)
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
ok      github.com/shuaiZend/mage2mcp/internal/storage    0.310s
```

### 5.2 Mandated command 2 — `go test -race ./internal/security/...`

```text
$ go test -race -count=1 ./internal/security/...
ok      github.com/shuaiZend/mage2mcp/internal/security/audit    1.100s
ok      github.com/shuaiZend/mage2mcp/internal/security/auth    1.110s
ok      github.com/shuaiZend/mage2mcp/internal/security/confirm    1.096s
ok      github.com/shuaiZend/mage2mcp/internal/security/jwt    2.743s
ok      github.com/shuaiZend/mage2mcp/internal/security/rbac    1.096s
```

No data races reported. This proves nothing about the P0s: none of these packages exercises the
authorization wiring, the job tools, or the order tools.

### 5.3 Probe tests (throwaway, deleted after capture)

| Probe | Purpose | Verbatim result |
|---|---|---|
| A (storage) | Is `Consume` bound to tool/instance? | `PROBE RESULT: consumed confirmation originally bound to tool="magento.category.delete" instance="store-a"; state="consumed"` |
| B (storage) | 20 concurrent consumes on one approval | `PROBE RESULT: concurrent Consume successes=1 refusals=19` → single-use holds |
| C (storage) | Is the approver verified? | `PROBE RESULT: approved by unverified string "x"; state="approved" (requester was "agent-a")` |
| D (server) | Cross-tool confirmation replay | `PROBE RESULT: cross-tool replay ACCEPTED — approval issued for "magento.import.cancel" consumed by "magento.import.resume"` |
| E (cmd/server) | Is the scope ladder enforced? | `PROBE RESULT: viewer ALLOWED magento.product.delete (authorized at scope=read)` (+ order.create, import.start) |
| F (cmd/server) | Tool inventory | `PROBE RESULT: registered tools=95 by permission=map[destructive:9 read:43 write:43]`; order/invoice/creditmemo tools registered |

Probe files (`internal/storage/zz_probe_security_audit_test.go`,
`internal/mcp/server/zz_probe_security_audit_test.go`,
`cmd/server/zz_probe_security_audit_test.go`, `cmd/server/zz_probe_tools_test.go`) were removed
after the output above was captured. `git status` shows no modification to tracked files.

---

## 6. What could not be verified, and why

1. **A live Cloudflare Access deployment.** No Access application, team domain, or JWKS was
   available, so the claim that Access forwards `Cf-Access-Jwt-Assertion` (and not
   `Authorization`) rests on the vendor documentation cited in the threat model. The code-side
   gap (SEC-08) is independent of that: the gateway has no code path that reads the header.
2. **A real Magento instance.** No integration environment was available. The order-creation
   flow, the ACL surface of the configured credential, and the actual duplicate-order behaviour
   of a retried `order.create` are `CONTRACT`-level claims from code reading, not `INTEGRATION`
   evidence. This is the single largest blind spot for SEC-05: the *absence* of an idempotency
   key is certain, the exact duplication rate under Magento's cart API is not measured.
3. **A live IdP token.** Whether the deployment's IdP issues a `roles` claim, and what a human
   token contains, could not be observed; SEC-09 describes both failure modes without deciding
   which one production hits.
4. **Multi-process rate limiting / Redis.** There is no limiter to test, so SEC-11 is an
   absence, not a demonstrated bypass.
5. **The worker in a multi-instance deployment.** `cmd/worker` was read, not run, against two
   configured instances; the cross-tenant resume path (SEC-02) was proven at the tool and store
   layer but not executed end-to-end into a second Magento.
6. **Supply chain and CI.** Out of scope for this report; `docs/audit/` assigns it elsewhere.
   The prior F-09/F-10/R-01/R-05 status was not re-verified here.
7. **TLS configuration.** The gateway serves plain HTTP and relies on Cloudflare Tunnel /
   an external terminator; no TLS policy exists in the process to test (T-02 is N/A in code).
8. **`internal/admin/`, `internal/mcp/transport/`, `internal/core/models/`,
   `internal/security/secrets/` are empty directories.** Any claim that a control lives there
   (admin HTTP surface, transport middleware, secret manager) is unverifiable because the
   package does not exist.

---

## 7. Auditor's note on the document set

`docs/security/security-review.md:5` says "Nothing here is implemented yet"; the threat model
does not, and it is the document a reviewer or an operator will read. The gap is not cosmetic:
A5 (SEC-05), the RBAC matrix (SEC-01), the confirmation bindings (SEC-03), the audit hash chain
(SEC-06), rate limiting (SEC-11), egress (SSRF), and the `secret://` inventory (SEC-17) are all
described in the present tense and all absent. Recommendation: add a status column to the
threat model's control tables (the same four-value legend used in §3 of this report) so a
reader can tell a design from a control. The `docs/audit/README.md` state ladder already
supports this; the threat model should adopt it.
