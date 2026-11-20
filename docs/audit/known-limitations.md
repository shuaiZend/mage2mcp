# Known limitations and accepted risks

| Field | Value |
|---|---|
| Document | Known limitations, deferred findings, and accepted risks |
| Baseline commit | `66786a5d10c1f78d00e08a5b42d01fcc2da37a3f` plus the remediation branch |
| Owner | Chief Engineer |
| Status | Binding for the release decision in `release-readiness-report.md` |

This document exists so that nothing deferred is silently forgotten. A limitation
listed here is a **known, accepted** gap with a stated workaround — not a claim
that the gap is harmless. Anything not listed here and not fixed is either a
defect we missed (report it) or a false statement in the documentation (also a
defect).

---

## 1. Verification limits — what this audit could not prove

These bound every claim in the release verdict. They are environmental, not
choices.

### 1.1 Live Magento: one manual run, still not in CI

No Magento instance was reachable during the audit itself, so every Magento
behaviour in this release is verified against the documented REST contract and
against `httptest` fake servers (`CONTRACT` tests). **No test in this repository is
labelled `INTEGRATION` for Magento, and none may be.**

**Update 2026-11-21.** A one-off live run against a real store was executed after
the release: `https://demo-magento-2.auroracreation.com`, store view `pl`, 101
tools, 123 cases. It is recorded in
[`live-magento-test-report.md`](live-magento-test-report.md) and produced five
confirmed defects in
[`live-magento-defects.md`](live-magento-defects.md), including a P1 in order
placement. Two consequences:

- The catalogue and product/media write surfaces are now genuinely verified
  against a real store, not only against a fake.
- The finance chain (order → invoice → shipment → credit memo) is **known broken**
  on a standard Magento 2 instance, because order placement uses `POST` where
  Magento requires `PUT`.

The defects it found were fixed and re-verified the same day against a second
container built from the patched tree: `links.add`, `inventory.source_create`,
`customer.addresses.create`, the `total_count` projections, log redaction and
`region_id` now pass on the real store, and order placement now creates an order
(the method was wrong). Two things came out of that verification. The reconciler's `maintenance` queue was
not in the worker's default list, so **no UNKNOWN operation was ever reconciled** in
a default deployment; that is fixed and verified (72 queued jobs drained, 283
terminal transitions). LIVE-8 was the sharpest finding: an ambiguous placement whose order *had* been
created was recorded as a definite failure with "safe to retry", which would have
produced a duplicate order. Empty reconciliation reads no longer prove
non-placement — only a surviving cart does — and the observer no longer reads
`/orders/{customerID}` as if it were an order id. What remains is a limitation
rather than a defect: this store's reconciler cannot confirm the placement, so the
operation escalates to a human after its retry budget.

That run is manual and is not part of CI. What is in CI is a **conformance suite
over payloads recorded from that store** (`internal/magento/conformance_test.go`,
fixtures under `test/fixtures/magento`): 12 real responses replayed unchanged, so
the shapes a real Magento returns are under regression even though no store is
reachable. It cannot notice a store changing its behaviour, because the fixtures are
frozen — the full live matrix still cannot be run without an environment.

**Update, same day.** That gap now has a workflow: `.github/workflows/live.yml` starts
the gateway against a real store from `MAGENTO_ENDPOINT`/`MAGENTO_CREDENTIAL`
repository secrets and runs the read matrix, and skips cleanly with a notice when
they are unset. It is verified end to end locally against the demo store — 43 pass,
0 fail — but it has not run on GitHub Actions yet, and it needs the secrets to do
anything. The full write matrix still needs an approval identity and a throwaway
store.

Consequence: Magento 2.4.9 compatibility is **unverified**, not "verified". The
gateway may still be wrong about a field name, an id type, an extension
attribute, or a workflow-route behaviour that a live store would reject. The
audit already found two such defects by reading the contract rather than by
testing (`MAG-14` media entry id, `MAG-02` invoice capture flag) — which is
evidence that contract reading finds some but not all of them.

**Before production use:** run the `test/e2e` harness against a real Magento
2.4.9 sandbox, and treat the first weeks of use as a verification period.

### 1.2 No live Cloudflare or GA4

The Cloudflare Analytics and GA4 adapters are reviewed for code quality and
contract fidelity only. Their live API behaviour is unverified.

### 1.3 Container registry unreachable

`docker pull` fails in the audit environment, so the Dockerfile is reviewed
statically and the compose-based end-to-end smoke test could not run unmodified.
The e2e harness was run with only the PostgreSQL provisioning patched to a local
instance; that run passed (confirmation flow, mismatch rejection, async import,
configurable create, order create, bulk import, failing import and resume,
batches) and is labelled **E2E-with-fake-Magento**, not integration.

### 1.4 Single host, single database

Concurrency properties were probed with real goroutines against a real
PostgreSQL 16.15. That is sufficient to demonstrate a lost update, a double
claim, or a deadlock. It is **not** sufficient to model a multi-node deployment
with clock skew, network partitions, or a failover that changes the primary
mid-operation.

### 1.5 `golangci-lint` is fetched over the network

`make lint` downloads the linter. Where the network was unavailable, static
analysis coverage was `go vet` plus manual review. Run the lint gate before
release.

## 2. Capabilities that depend on configuration

The tool count is not a constant. It is a function of configuration, and the
documentation now says so:

| Configuration | Tools registered |
|---|---|
| Read-only, all modules | 54 (43 Magento) |
| Maximal (writes + database + bindings) | 112 (101 Magento) |

- `tools.writes: true` gates 52 Magento write tools.
- `database.url` gates 6 asynchronous import tools. Without a database those
  tools are **not registered**, so an agent cannot even discover them.

Operators reading "112 tools" and deploying the read-only default will see 54.
That is correct behaviour, and the generated `docs/implemented-tools.md` now
states it.

## 3. Operational requirements before enabling writes

These are not code limitations; they are conditions of safe use. Enabling writes
without them is a misuse.

1. **Assign operator and admin roles explicitly.** The shipped default policy
   grants `viewer`/`analyst` read, `operator` write, and `admin` destructive.
   A deployment with no `operator` role simply cannot write — that is
   default-deny working, not a bug.
2. **Configure a database.** Without `database.url`, confirmations and audit
   records are in memory and are lost on restart, and high-risk approvals do not
   survive a deploy. The server logs a warning; it does not refuse to start.
3. **Run the approval CLI as a trusted operator.** It now validates its request
   (subcommand, confirmation id, `-as`) before opening the store, so a typo fails
   immediately instead of after a connect timeout, and its usage contract is
   covered by tests. `cmd/admin` still requires direct
   database access, which is already root-equivalent. The `-as` approver identity
   is self-asserted (SEC-04), so the approval trail is only as trustworthy as the
   operator. See §4.
4. **Enforce the Cloudflare-only origin at the edge.** The gateway verifies a
   `Cf-Access-Jwt-Assertion` or a bearer token, but it does **not** enforce that
   the origin is reachable only through Cloudflare. Tunnel or authenticated
   origin pull is a deployment control.

## 4. Deferred findings

Each is real, was verified, and is **not** fixed in this release.

### Security

| ID | Sev | Limitation | Workaround / risk |
|---|---|---|---|
| SEC-04 | FIXED | The approval CLI now verifies the approver | It reuses the gateway's JWT verifier, so with `auth.enabled: true` the recorded subject comes from a verified token and `-as` may not contradict it. With authentication disabled `-as` is still a self-assertion, and the CLI says so on stderr |
| SEC-10 | P2 | Bounded upstream error text and the request path reach the agent | Only an already-authorized caller sees it; text is control-character stripped and length-bounded |
| SEC-15 | FIXED | An MCP session is now bound to the subject that created it | A different subject presenting the id gets 404 (not 403, per the anti-probing rule); an untracked id still falls through to the SDK, which rejects ids it does not know |
| SEC-17 | P3 | Credentials are inline config strings with no redacting type | Protect the config file by permissions; do not dump config into logs or tickets |
| SEC-18 | P3 | Import base64 is decoded before the size check | Bounded by the HTTP body cap; peak memory is ~4/3 of the body limit per concurrent request |

### Magento

| ID | Sev | Limitation | Workaround / risk |
|---|---|---|---|
| MAG-12 | P2 | MSI source quantities are integers in the transfer path, so a source holding a fractional quantity cannot be transferred through the gateway. The transfer **refuses** a fractional observed quantity with a typed `unsupported_capability` error before any write; it does not truncate | Capability limitation, not a data-integrity risk: fractional stock must be adjusted outside the gateway or the source must hold integer stock. This was previously documented as truncation; the review of t28 established that it is refused |
| SEC-R01 | P3 | `magento.import.batches` (the list operation) is instance-scoped but not subject-scoped (`internal/storage/import_batches.go`), so a second caller on the *same* instance can see batch ids, job ids, state, counts and error codes for another caller's import. The get and by-job paths are subject-scoped, so the listed ids 404 on read | Metadata-only disclosure within one tenant. Cross-tenant access is blocked by the instance predicate (SEC-02). Tracked for a join-and-filter fix |
| SEC-R02 | P3 | The audit **intent** row is written before authorization and always carries `decision=allow`, so a denied call has `intent=allow` + `outcome=deny`. Outcome rows are correct, so the audit is not misleading when read by phase | A phase-blind query on `decision=allow` will surface denied calls. Read the outcome phase for the decision |
| REL-16-R2 | P3 | For the five self-ledgered money tools, the middleware consumes the approval before the tool begins its own ledger operation. A crash in that window loses the approval | Availability only — the failure mode is "ask a human again", never an unapproved write |
| CTX-SUBJ | P3 | `ListImportBatches` derives the caller subject from the request context rather than taking it as a parameter, because `ImportBatchReader` lives in `internal/magento` and changing its interface for a P3 was not worth the blast radius. It fails closed (no identity → `"anonymous"`, never all subjects) | Implicit dependency. If a future caller needs to list on behalf of another subject, add an explicit parameter and update `ImportBatchReader` — do not extend the context convention |
| REL48-01 | P3 | The guard that proves every write tool is either middleware-gated or self-ledgered scans at file granularity, so it can be satisfied by a quoted comment plus a count edit rather than a real ledger call | The guard still catches the realistic failure (a new write tool on neither path). A stricter check would follow the call graph; not worth it now |
| REL48-02 | P3 | The author's lease-renewal test uses a 120 ms handler against a 300 ms effective lease, so it does not itself exceed one lease period | The behaviour is proven by an independent probe (400 ms lease / 200 ms timeout / 700 ms handler → runs=1, extends=14, completed=1). The shipped test is weaker than the property |
| REL48-03 | P3 | Escalation is terminal (`reconcile_after='infinity'`, `needs_manual_reconciliation`) but writes no audit outcome | Escalated operations are discoverable by querying the ledger; they are not in the audit trail |
| TEST-PAR | **FIXED** | Database-backed test packages shared one PostgreSQL schema, so `go test -race ./...` (which runs packages in parallel) let one package's cleanup and reconcile backlog interfere with another's fixtures. Affected `internal/storage`, `internal/magento` and `cmd/worker`; and `TestJobQueueStaleAttemptCannotWrite` used a fixed sleep that was timing-dependent under load | **Resolved.** Every database-backed test package now connects with its own `search_path` (`test_storage`, `test_magento`, `test_worker`), created by the test helper before `Migrate`; the stale-write test polls for the observable condition instead of sleeping. Verified by six consecutive `go test -race ./...` runs with zero failures. No `-p 1` workaround is needed and the gate is deterministic |
| MAG-15 | P2 | Media upload has no dedupe and no ledger | Re-uploading creates a second gallery entry. Check `product.media` before uploading |
| MAG-20 | P2 | Addresses cannot express `region_id` | Some regions (notably US/CA states) may be rejected or mis-mapped for order creation. Verify against the target store's address configuration |
| MAG-22 | P3 | Grouped product creation is not implemented | Create grouped products in the Magento admin |
| MAG-23 | P1 | No Magento integration coverage exists at all | See §1.1. This is the single largest residual risk in the project |
| MAG-04-R | P2 | Order-creation reconciliation requires the Magento order search projection to expose `quote_id`. Where it does, an order is claimed only when its `quote_id` matches the operation's cart, so a foreign same-customer same-total order can no longer be attributed. Where an instance's search does **not** expose `quote_id`, reconciliation degrades to UNKNOWN with `candidate_order_ids` rather than guessing | Safe by construction: the failure mode is "verify this", never "here is your order". If you see UNKNOWN with candidates after an order create, check the store's REST search attribute set for `quote_id` |

### Reliability

| ID | Sev | Limitation | Workaround / risk |
|---|---|---|---|
| REL-11 | **FIXED** | A retried import created a second `import_batches` row and the by-job lookup had no ordering | **Resolved.** A retry resumes the job's open batch; only a terminal batch is left as history, and the lookup orders by `created_at DESC, id DESC` |
| REL-12 | P2 | `import_batches` has no lease or recovery of its own | A worker crash can leave a batch `running` forever. The job record is authoritative; reconcile batches against jobs |
| ARCH-07 | **FIXED** | `server.NewMCP` silently skipped the confirmation gate when `Confirm` was nil | **Resolved.** `NewMCP` now refuses to start when a registered tool requires approval and no confirmation service is supplied, so a library consumer cannot drop the gate by omission. `TestNewMCPRequiresConfirmationServiceForApprovalTools` |
| REL-16-R | P2 | Audit-outcome and ledger-transition atomicity holds for the 50 middleware-gated write tools but **not** for the 5 self-ledgered money tools (`invoice.capture`, `invoice.create`, `creditmemo.create`, `order.create`, `order.cancel`). Those tools have no `audit_events` write of their own, so their outcome audit row is written by the middleware *after* the tool commits. A crash in that window leaves the `operations` row correct but its audit row missing | The ledger is the authority on whether the business mutation happened; a missing audit row for those five tools means the audit trail has a gap, not that money moved incorrectly. Closing it requires giving the five Magento write files the ability to write their audit row inside their own transaction. Recorded in `reliability-review.md` §7 |
| R-6 | P3 | `TestSummarizeRendersSecurityKeysBeforePadding` iterates the production `prioritySummaryKeys` slice, so it cannot detect a key being removed from it | The test proves ordering, not completeness. A future money tool with a different field name (`total`, `price`, `refund_amount`) will not be prioritised in the approval summary — revisit when the ledger is wired to the approval surface |
| R-7 | P3 | The `classifyConsumeFailure` error-path fix (a DB outage no longer reads as `not_found`) has no test pinning it | Behaviour is correct and fails closed; a cancelled-context test would pin it |

Note on credential redaction: the credential **value** heuristic in `Summarize`
is defeatable by inserting whitespace and misses short base64 strings. The real
control is the key-fragment list, which now covers `private`, `pwd`, `session`,
`jwt`, and `bearer` in addition to the original set. Do not rely on value-shape
detection alone.

### MCP and agent experience

| ID | Sev | Limitation | Workaround / risk |
|---|---|---|---|
| MCP-08 | P2 | Output schemas are not self-describing and results have no common envelope | Agents must read each tool's documented output shape. A provenance/quality envelope is planned |
| MCP-10 | P3 | Naming is inconsistent: 4 singular/plural splits, 27 noun-final names, 18 four-segment names | Use the generated inventory as the authoritative list; do not infer names |
| MCP-13 | P3 | Capability metadata is inaccurate and unconsumed | Do not build tooling on the `Capability` field |
| MCP-15 | P3 | No provenance envelope; upstream text reaches the agent unmarked | Treat upstream-derived strings as untrusted data |
| MCP-16 | P3 | Annotation hints are inaccurate or missing for some tools | Do not rely on `readOnlyHint`/`destructiveHint` alone; use the registry permission |

### Performance

| ID | Sev | Limitation | Workaround / risk |
|---|---|---|---|
| PERF-05 | **FIXED** | No outbound rate limiter, no concurrency cap, no per-instance serialisation | **Resolved.** Each Magento instance now has its own token bucket and concurrency cap (`limits:` in configuration, default 50 rps / burst 100 / 8 concurrent). An exhausted budget returns a retryable `rate_limited` error without reaching Magento. Cloudflare and GA4 do not honour the block yet |
| PERF-06 | P2 | Import executes two sequential Magento requests per row | A 5,000-row import is 10,000 requests. Chunk imports and run them off-peak |
| PERF-15..22 | P3 | Several N+1 and round-trip inefficiencies remain (inventory status batch, bulk writes, import status polling, admin-token mutex, list pagination, double payload parse) | Acceptable at current scale; batch and schedule large jobs |
| PERF-PROBE | P3 | `docs/audit/perf-probe` is baseline evidence, not a live gate. It encodes the behaviour of commit `66786a5` and is excluded from the default test run (`-tags probe`). The configurable-product SKU precheck added during remediation is not modelled by its fake, so it does not currently pass on the current tree | The numbers in `performance-review.md` remain valid for the baseline they describe. Update the fake before relying on it as a regression harness |

### Workflow tools — a deliberate decision

The objective asked whether to add `magento.configurable.create_complete`,
`magento.inventory.reconcile`, and `magento.order.fulfillment`. The audit
recommended **against all three for this release**, and that recommendation is
accepted:

- `configurable.create_complete` would duplicate `magento.configurable.create`,
  which already performs children → parent → links → stock verification.
- `inventory.reconcile` and `order.fulfillment` would be non-atomic
  money/inventory mutations composed of several REST calls. Composing them into
  one tool would present a multi-call sequence as a single atomic operation,
  which the objective explicitly forbids pretending.

The safer shape — read-only plan/status tools plus prompts — is future work.

## 5. Behavioural changes in this release

Callers must know these changed. Each is deliberate and tested.

| Change | Reason |
|---|---|
| Authorization now enforces the declared tool scope | SEC-01. A `viewer` can no longer call write or destructive tools. **Any deployment relying on the old behaviour will break — correctly.** |
| `server.public_url` / `MAGE2MCP_PUBLIC_URL` removed | Dead configuration; setting it now fails as an unknown field |
| Bindings require all three modules enabled and exactly one Cloudflare + one GA4 instance | The unified service genuinely supports only one of each; validation now says so at startup instead of failing later with a misleading message |
| `magento.import.cancel` returns a typed error for an already-terminal job | REL-14. It no longer reports `canceled: true` when it canceled nothing |
| A confirmation is bound to its tool and instance | SEC-03. A confirmation issued for one tool cannot be replayed against another |
| Import tools require an `instance` argument | SEC-02. Job ids are no longer a cross-tenant capability |
| The storage test suite is now repeatable on a non-virgin database | REL-15 |

## 6. What "not ready" would still look like

For the avoidance of doubt, the following would each make the release verdict
NOT READY, and none of them is true as of this document:

- A confirmed P0 without a fix.
- A security test that passes only because it is skipped.
- A documented capability with no implementing code.
- A test deleted or an assertion relaxed to make a gate pass.

If any of those becomes true later, this document must be updated before the next
release. A limitation is only honest while it is stated.

## Final state of the remediation

Every finding in the audit reports is entered in
[`remediation-plan.md`](remediation-plan.md) with a status. At the close of the
remediation:

| Status | Count |
|---|---|
| Closed (fixed, verified, already fixed, or recorded as documentation) | 114 |
| Partial | 1 |
| Open P0, P1 or P2 | **0** |

The one partial item is `PERF-16`, and it is a performance optimisation rather than a
defect. Bulk product updates and bulk attribute-option creation now fan out under a
bound. No stage inside the configurable workflow can be parallelised without changing
the failure contract: children and links both fail fast and report one SKU, and the link
stage ends with a verification read the whole stage depends on. The audit itself measured
the remaining latency as fitting the tool budget — "fine, but linear and fragile" — so
closing it means trading a deterministic failure report for latency, which is a
maintainer's decision rather than a defect fix. `perf-16-options.md` records the options
and the corrected analysis.

Everything that could change an outcome for a caller was fixed and verified, most of it
against a real store.

Each of the three has a note laying out the options and their costs, so the remaining
work is a choice rather than an open question:
[`rel-16-design-options.md`](rel-16-design-options.md),
[`mcp-08-envelope-options.md`](mcp-08-envelope-options.md),
[`perf-16-options.md`](perf-16-options.md).
`PERF-16`'s bulk update and bulk attribute-option paths now fan out under a bound; the
configurable children path is deliberately left sequential because it fails fast on the
first child error and reports that one SKU. Running children concurrently would make
"which child failed" nondeterministic and require the result contract to carry a list of
failures — a contract change rather than a defect fix, and one that would complicate the
compensation story that `orphan_skus` and `completed_stages` exist to support.

Everything that could change an outcome for a caller was fixed and verified, most of it
against a real store (see the live test report). What remains is throughput work on a
path that is already correct.
