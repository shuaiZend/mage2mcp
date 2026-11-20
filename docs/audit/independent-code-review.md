# Independent code review — review records

**Append-only.** One section per review task, newest at the bottom. Each section is
written by a reviewer who is not the author of the change under review. The finding
format is defined in [README.md](README.md); review finding ids use the `AREA-RNN`
form so they cannot collide with the audit ids.

---

## t15 — review of t10: MAG-14, MAG-06, MAG-08, MAG-09

| Field | Value |
|---|---|
| Review task | `t15` (round 1) |
| Reviewed task | `t10`, author: magento-engineer |
| Reviewer | qa-engineer (not the author) |
| Change under review | working tree at `66786a5` + `internal/magento/media.go`, `internal/magento/configurable.go`, `internal/magento/media_upload_test.go`, `internal/magento/configurable_test.go` |
| Method | isolated copy of the tree, 13 independent probe tests (43 cases), four revert experiments, adapted end-to-end run |
| Verdict | **PASS** — four claims reproduced, every new test fails when its fix is reverted, no test deleted or weakened. One required follow-up (`MAG-R01`) and four P3 findings. |

### 1. Scope of the change

`git diff --stat -- internal/magento/` for the reviewed change is four files:
`configurable.go` (+127/−26), `configurable_test.go` (+283/−9), `media.go` (+38/−8),
`media_upload_test.go` (+66/−11). Everything in the diff belongs to MAG-14 (media entry
id), MAG-06 (completed-SKU truncation), MAG-08 (swallowed stock-status write) and MAG-09
(parent stock derivation). No unrelated file, package, or behaviour is touched. The
`ConfigurableProduct.ParentStockSource` field and the `stock_status` stage constant are
additive and used only by these paths.

Note on the review environment: other remediation tasks were editing sibling files
(`write_tools.go`, `inventory.go`, `client.go`, …) while this review ran. The review was
performed on an isolated copy taken when `configurable.go` and `media.go` matched the
current tree byte for byte (`diff -q` → identical), so the probes below describe exactly
the code that is in the tree now.

### 2. Method

1. Copied the tree to `/tmp/qa-t15` (excluding `.git`, `node_modules`, `docs/audit`).
2. Wrote 13 independent probe tests (43 cases including subtests) in
   `internal/magento/zz_qa_review_test.go` (not part of the
   change; never copied back). They drive the real `Service` through `httptest` fake
   Magento servers and call `decodeMediaEntryID`, `boundedSKUs` and `observeParentStock`
   directly.
3. Reverted each fix in a throwaway copy and ran the new tests to prove they guard the
   fix (§5).
4. Ran the adapted end-to-end smoke (§4, question 5 and MAG-R02).
5. Ran the required commands on the reviewed state: `go build ./...` → exit 0;
   `go test -count=1 ./internal/magento/...` → `ok 0.606s`;
   `go test -race -count=1 ./internal/magento/...` → `ok 2.821s`.

### 3. Claim-by-claim verification

#### 3.1 MAG-14 — media entry id: **reproduced**

The pre-fix code decoded only a JSON string; Magento's real contract
(`ProductAttributeMediaGalleryManagementInterface::create(): int`) returns a bare
integer, so every successful upload became `upstream_unavailable: Magento returned an
unreadable media payload`. Reverting the fix reproduces exactly that (§5, revert 1).

Independent decoder probe (all shapes, verbatim result):

```text
integer 9              -> id=9   scalar=true   err=nil
numeric string "9"     -> id=9   scalar=true   err=nil
padded " 9 "           -> id=9   scalar=true   err=nil
"+9"                   -> id=9   scalar=true   err=nil
0 / -3                 -> err=upstream_unavailable (code checked)
9.5 / 1e3              -> err=upstream_unavailable
99999999999999999999   -> err=upstream_unavailable (no int overflow)
"garbage"              -> err=upstream_unavailable
null                   -> err=upstream_unavailable
"" / true / [] / {"id":9} -> scalar=false (falls through to the full-entry path)
```

End-to-end through `UploadMedia` against a fake Magento:

```text
post body `9`            -> success, entry.ID=9, gallery GET performed, File="/w/i/front.jpg"
post body `"9"`          -> success, entry.ID=9
post body `{"id":9,...}` -> success, entry.ID=9 (no gallery read needed)
post body `"garbage"`    -> upstream_unavailable: Magento returned an unreadable media id
post body `true`         -> upstream_unavailable: Magento returned an unreadable media payload
post body `0` / `-3`     -> upstream_unavailable: Magento returned an unreadable media id
```

The bounded-payload guarantees are intact and pre-existing: request payload is checked
before base64 decoding (`maxMediaBytes = 5 MiB`, `media.go:207-209`), MIME/filename/label/
position/types are validated before any bytes are sent, and the client bounds every
response at `maxResponseBytes = 8 MiB` (`client.go:218-224`). `TestUploadMediaRejectsOversizedPayload`
still exists and still passes. The decoder does not accept a float, exponent, negative,
zero, overflow, or garbage scalar.

Two adjacent gaps are recorded as `MAG-R04`: the object branch and the empty-body branch
still accept a response with no usable id.

#### 3.2 MAG-06 — completed-SKU truncation: **reproduced**

`boundedSKUs` now keeps whole SKUs and returns `(joined, omitted)`. Independent boundary
probe (direct call, verbatim):

```text
0 SKUs                       -> "" , omitted 0
1 SKU                        -> 1 entry, omitted 0
exactly 20 entries (79 B)    -> 20 entries, omitted 0
21 entries                   -> 20 entries, omitted 1
joined exactly 900 bytes     -> 2 entries, omitted 0 (equality kept)
joined 901 bytes             -> 1 entry, omitted 1
single SKU 901 bytes         -> 0 entries, omitted 1
20 entries, joined 900 bytes -> 20 entries, omitted 0
```

In every case `entries + omitted == len(input)` and every emitted token is a complete
input SKU. End-to-end over the cap (30 children, last one fails): `completed_skus` has 20
whole tokens, `completed_total=29`, `completed_omitted=9`.

The old behaviour is real, not hypothetical: with 40 × 61-byte SKUs the reverted function
returns a 900-byte string whose last token is `CFG-LLLLLLLLLLLLLLLLLLLLLLLLLLLL` — a
truncated SKU that names no product (§5, revert 4).

#### 3.3 MAG-08 — swallowed stock-status write: **reproduced**

Forcing the parent `PUT …/stockItems/{id}` to 500 produces, verbatim:

```text
message="Magento write failed at stage stock_status: Magento is unavailable; completed writes are not rolled back and require compensation"
details=map[completed_skus:PHONE-01-BLK,PHONE-01-WHT,PHONE-01 failed_sku:PHONE-01 stage:stock_status]
```

The caller is told (error, not success), the error names the stage, the parent is the
`failed_sku`, and the completed set contains both children and the parent — the
compensation record is intact. Reverting to the old silent success makes the new test fail
(§5, revert 2).

#### 3.4 MAG-09 — parent in-stock derivation: **reproduced**

Independent `observeParentStock` probes:

```text
empty children (direct call) -> inStock=false, source="source_items: 0 of 0 children in stock, 0 units observed", 0 HTTP calls
45 children                  -> 3 GET calls (20+20+5), no hang, source="source_items: 0 of 45 children in stock, 0 units observed"
observed stock               -> inStock=true,  source="source_items: 1 of 2 children in stock, 6.5 units observed"
zero stock                   -> inStock=false
100-row page                 -> err=insufficient_data: the child source-item read hit its page cap; the parent stock status cannot be derived
```

End-to-end through `CreateConfigurable` with the legacy stock `PUT` body captured:

```text
observed stock -> parent_in_stock=true,  body {"stockItem":{"is_in_stock":true}}
zero stock     -> parent_in_stock=false, body {"stockItem":{"is_in_stock":false}}
```

The reported value matches the observed child state and the derivation string is attached
to the service result. Chunking is `ceil(children/20)` with `maxConfigurableChildren = 50`,
so at most three reads per create; the page-cap check turns an incomplete read into an
explicit `insufficient_data` observation instead of a silent truncation. The empty list is
unreachable through `CreateConfigurable` (`validateConfigurableInput` requires at least one
child, `configurable.go:343-345`); called directly it makes no HTTP request and reports
`0 of 0`.

The derivation is documented on the service result, but **not** on the agent-facing tool
result — see `MAG-R01`.

### 4. Answers to the review questions

1. **Does each fix change behaviour, and would the new test fail if reverted?** Yes to
   both, for all four. §5 lists the four revert experiments and the exact failure each
   produces.
2. **MAG-09 large child sets and empty list.** Bounded: 20 SKUs per `GET /inventory/source-items`,
   at most 50 children → at most 3 calls; each call is bounded by the client timeout; the
   100-row page cap is detected and reported as `insufficient_data` rather than truncated.
   Empty list: rejected by validation on the public path; direct call returns
   `false, "0 of 0", nil` with no HTTP call.
3. **MAG-14 hostile/oversized payload and over-acceptance.** Request bounded at 5 MiB
   before decode plus MIME/filename/label/position/type validation; response bounded at
   8 MiB by the client. The decoder rejects zero, negative, fractional, exponent, overflow,
   garbage, and `null` scalars. It still accepts two shapes it arguably should not: an
   object without a usable id, and an empty body, both reported as success with entry id 0
   (`MAG-R04`).
4. **MAG-06 omitted count at the cap boundary.** Correct: exactly 20 entries → omitted 0;
   21 → omitted 1; exactly 900 bytes → kept; 901 → one omitted; `entries + omitted` equals
   the input length in every probed case.
5. **New network-call pattern.** Yes: `CreateConfigurable` now issues one to three
   `GET /inventory/source-items` calls between linking and the stock write (it previously
   issued none). Cost: +1..3 round trips per create. New failure mode: if that read fails
   (403, 5xx, page cap, malformed payload) the create still returns **success** with
   `parent_in_stock=false`, writes no stock status, and records `parent_stock_source =
   "unobserved: <code>"`. That is defensible at the service layer, but the field is
   dropped at the MCP boundary, so the agent sees a success indistinguishable from an
   observed out-of-stock (`MAG-R01`). The end-to-end fake also models the new call
   incorrectly (`MAG-R02`).
6. **Anything outside the stated scope?** No. Only the two source files and their tests
   changed; the additional read and the `stock_status` stage are inherent to MAG-08/09.
   The one thing the change did **not** update is the tool description and the write-safety
   doc (`MAG-R05`), and the e2e fake (`MAG-R02`).

### 5. Revert experiment — the new tests guard the fixes

Each fix was reverted behaviourally in a throwaway copy (signatures kept so the new tests
still compile), then the new tests were run.

| Revert | Reverted behaviour | Result |
|---|---|---|
| 1 | `decodeMediaEntryID` back to string-only | `--- FAIL: TestUploadMedia` — `integer entry id must be accepted: upstream_unavailable: Magento returned an unreadable media payload` |
| 2 | stock-status error swallowed at the call site | `--- FAIL: TestCreateConfigurableFailsAtStockStage` — result is non-nil with `ParentInStock:true`, want nil on a failed stock write |
| 3 | `body.StockItem.IsInStock = true` (forced in stock) | `--- FAIL: TestCreateConfigurableRequestSequence` — `request body = {"stockItem":{"is_in_stock":true}}, want {"stockItem":{"is_in_stock":false}}` |
| 4 | `boundedSKUs` back to mid-token truncation | `--- FAIL: TestCreateConfigurableCompletedSKUsWholeTokens/byte_budget_binds` — `completed_skus contains "", which is not a complete input SKU` |

At least one new test fails for every reverted fix; all four do.

### 6. Regression safety

- **No test deleted:** `git diff -- internal/magento/*_test.go | grep '^-func Test'` → none.
  Six tests were added.
- **No assertion weakened:** the removed `t.Fatalf` lines are replaced by stronger
  expectations. `TestCreateConfigurableRequestSequence` changes the expected request count
  from 6 to 7 because a real read was added, and adds assertions on the stock body and the
  result. `TestUploadMedia` is rewritten around the real integer contract and now also
  asserts the gallery read and the resolved `file`. `TestUploadMediaRejectsOversizedPayload`
  is untouched.
- **No unrelated behaviour changed:** the only behavioural deltas are the four claimed
  fixes plus the `parent.ID == 0` reporting string (the stock write was already skipped in
  that case). The `ConfigurableProduct` and error-detail additions are additive.
- **Required commands on the reviewed state:** `go build ./...` exit 0;
  `go test -count=1 ./internal/magento/...` ok; `go test -race -count=1 ./internal/magento/...`
  ok. The same commands on the current working tree (which also carries other tasks'
  edits) are also green.

### 7. Findings

#### MAG-R01: `parent_stock_source` is dropped at the MCP boundary, so an unobserved stock state is indistinguishable from an observed out-of-stock

- **Severity**: P2
- **State**: CONFIRMED (this is the follow-up the author flagged; it is still open)
- **Location**: `internal/magento/write_tools.go:239-243` (`configurableCreateOutput`), `internal/magento/write_tools.go:465-467` (construction), `internal/magento/configurable.go:90-91` (field)
- **Evidence**: the service result carries `ParentStockSource`, and four service-level
  tests assert it (`configurable_test.go:127,504,916,959`). The tool output struct has only
  `parent`, `children`, `parent_in_stock`; the constructor forwards only those three. An
  adapted end-to-end run on the current tree returns exactly
  `{"children":…,"parent":…,"parent_in_stock":false}` — no derivation field — while
  `E2E PASS`. No test asserts the field at the tool layer.
- **Trigger**: `magento.configurable.create` when the child source-item read fails (403,
  5xx, malformed payload, page cap) or when the parent has no product id.
- **Impact**: in the unobserved case the create returns **success**, writes no parent stock
  status, and reports `parent_in_stock: false`. The agent cannot tell that from a genuine
  observed out-of-stock, which is exactly the "guessed rather than reported" behaviour
  MAG-09 set out to remove. It may proceed to verify or publish a product whose stock
  state was never established.
- **Proposed fix**: add a `ParentStockSource` field to `configurableCreateOutput` with the
  JSON tag `parent_stock_source,omitempty`, forward `result.ParentStockSource`, and add a
  tool-level test (through the in-memory transport, like
  `TestWriteToolsThroughInMemoryTransport`) covering both the observed and the unobserved
  cases.
- **Test requirement**: the tool-level test above must exist before this is closed.
- **Verified by**: qa-engineer (service probes + e2e payload inspection).

#### MAG-R02: the e2e fake returns a bare array for the new source-item read, so the end-to-end smoke silently lost the stock-path coverage

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `test/e2e/fakemagento/main.go:183-186`, `test/e2e/client/main.go:329`
- **Evidence**: the fake answers every `/inventory/source-items` request with `[]`.
  `SourceItems` expects `{"items":[…]}` and returns
  `upstream_unavailable: Magento returned an unreadable source-item payload` (probe
  reproduces it). The create therefore takes the unobserved branch and skips the stock
  write. Before the change the same e2e printed `parent_in_stock: true`; on the reviewed
  tree it prints `parent_in_stock: false` and still ends with `E2E PASS`. The e2e asserts
  only `CREATED CONFIGURABLE`, not the stock field.
- **Trigger**: every `make e2e` run.
- **Impact**: the only full-stack test no longer exercises the parent stock write it used
  to exercise; a regression in MAG-08/MAG-09 would not be caught end to end. It also
  models the new upstream call incorrectly, so the fake and the real API disagree.
- **Proposed fix**: return `{"items":[…],"total_count":N}` for the created children (one
  row per child, status 1, quantity ≥ 0) and assert `parent_in_stock` in the e2e client.
- **Test requirement**: the e2e must fail if the parent stock write is skipped or written
  with the wrong value.
- **Verified by**: qa-engineer (adapted e2e before/after).

#### MAG-R03: the derivation comment states the error direction backwards

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/magento/configurable.go:796-804`
- **Evidence**: the comment says the value "can under-report (a child whose source is not
  linked to the parent's stock still counts) but it never reports stock that no source
  holds". A child whose source is not linked to the parent's stock *still counting* is the
  over-report direction: the parent can be reported in stock while its salable quantity is
  zero. Ignoring reservations has the same optimistic effect.
- **Trigger**: any reader relying on the comment to bound the risk.
- **Impact**: the documented derivation — which MAG-09 claims as a deliverable — understates
  the risk; an operator may treat the value as conservative when it can be optimistic.
- **Proposed fix**: state that the value can over-report salability when a child's source is
  not in the parent's stock or when reservations exist, and that it never reports stock
  when no enabled source holds a positive quantity.
- **Test requirement**: documentation review; no test change needed.
- **Verified by**: qa-engineer.

#### MAG-R04: the object and empty-body media responses are still accepted with entry id 0

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/magento/media.go:105` (empty body), `internal/magento/media.go:118-127` (object branch)
- **Evidence**: probe results: `post body {"media_type":"image"}` → success, `entry.ID=0`;
  `post body ""` → success, `entry.ID=0`. The scalar path rejects id ≤ 0, the object path
  does not validate the id at all.
- **Trigger**: an upstream build or proxy returning an entry object without an id, or an
  empty body.
- **Impact**: the caller receives a success it cannot use — entry id 0 addresses no gallery
  entry — and the same class of defect MAG-14 fixed for scalars survives for objects.
- **Proposed fix**: after decoding the object require `created.ID > 0`, otherwise return
  `upstream_unavailable`; treat an empty body the same way.
- **Test requirement**: table cases in `TestUploadMediaRejectsInvalidEntryID`.
- **Verified by**: qa-engineer.

#### MAG-R05: the tool description and write-safety doc were not updated for the new read and the conditional write

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/magento/write_tools.go:55-61` (`configurableCreateToolDescription`), `docs/write-safety.md:38-41`
- **Evidence**: the description says "the parent stock status is set after linking" and
  lists the write calls only; it does not mention the `GET /inventory/source-items`
  read, that the parent is written in stock only when a child holds stock, that the status
  may be left unset, or the new `stock_status` stage. `docs/write-safety.md` says
  "attempts to set the parent stock status and reports `parent_in_stock`".
- **Trigger**: an agent or operator reading the tool contract before a write.
- **Impact**: the documented call pattern understates the round trips and hides the
  unobserved outcome; combined with MAG-R01 the agent has no way to learn it.
- **Proposed fix**: update the description and the write-safety bullet to name the read,
  the derivation, the unobserved outcome, and `stock_status`.
- **Test requirement**: the docs contract check covers the tool inventory, not this text;
  a review of the diff is sufficient.
- **Verified by**: qa-engineer.

### 8. Verdict

**PASS** for t10. The four claims are independently reproduced:

- MAG-14: an integer entry id from the fake Magento yields a success result carrying that
  id; garbage and non-positive scalars still fail with `upstream_unavailable`.
- MAG-06: over-cap lists emit only complete input SKUs with an accurate omitted count, at
  and around both the 20-entry and 900-byte boundaries.
- MAG-08: a forced parent stock-status failure returns an error that names `stock_status`,
  with the completed set intact.
- MAG-09: zero-stock and observed-stock cases report the observed value, the derivation is
  attached to the service result, chunking is bounded, and a truncated read is reported as
  `insufficient_data` instead of guessed.

Every new test fails when its fix is reverted; no existing test was deleted or weakened;
no unrelated behaviour changed. The review is conditional on one follow-up before release:
**MAG-R01** (forward `parent_stock_source` into `configurableCreateOutput` with a
tool-level test), because without it the agent-facing contract cannot distinguish
"observed out of stock" from "could not observe". MAG-R02..R05 are P3 and can be batched
with the same change.

---

## t14 — review of t8: SEC-01 / ARCH-01 RBAC scope-ladder enforcement

| Field | Value |
|---|---|
| Review task | `t14` (round 1) |
| Reviewed task | `t8`, author: security-auditor |
| Reviewer | mcp-engineer (not the author) |
| Change under review | working tree at `66786a5` + the `Authorize`/`installPolicy`/`DefaultPolicy` hunks in `internal/mcp/server/server.go`, `internal/mcp/server/server_test.go`, `cmd/server/main.go`, `cmd/server/main_test.go`, `internal/security/rbac/rbac.go`, `internal/security/rbac/rbac_test.go` |
| Method | isolated pre-fix copy at `HEAD`, isolated post-fix copy, independent probe tests, three revert experiments, required commands re-run |
| Verdict | **PASS** — the original defect is reproduced pre-fix and denied post-fix; the new tests fail when their fix is reverted; no test was deleted or weakened; the shipped binary is safe. Six P3 follow-ups are recorded in §7 and the residual risks in §8. |

### 1. Scope of the change

The t8 hunks are:

- `internal/mcp/server/server.go` — `Options.Authorize` gains `requiredScope`;
  `serverOwnedTools` + `requiredScope` + `authorizeCall`; `installPolicy` resolves
  the scope and fails closed; `cmd/server`'s authorizer now receives it.
- `cmd/server/main.go` — `rbac.DefaultPolicy()` replaces the inline viewer/analyst
  policy; `authorizeFunc` forwards `requiredScope` and warns loudly when
  `auth.enabled: false`.
- `internal/security/rbac/rbac.go` — `DefaultPolicy()` (viewer/analyst read,
  operator write, admin admin).
- Tests: `TestAuthorizationReceivesRequiredScope`,
  `TestAuthorizationFailsClosedForUnregisteredTool`,
  `TestAuthorizationCoversPlatformInfo`,
  `TestShippedPolicyEnforcesScopeLadder`,
  `TestRealToolPermissionsMatchFixture`,
  `TestAuthorizeFuncPassesRequiredScope`,
  `TestAuthorizeFuncWarnsWhenAuthDisabled`,
  `TestDefaultPolicyExpressesTheLadder`,
  `TestDefaultPolicyDenyStillBeatsAllow`.

**Interleaving caveat.** `server.go` and `server_test.go` also contain the
in-progress t9 confirmation-binding changes (`enforceConfirmation` takes
`instance`, `confirm.Summarize`, `Consume` gains tool/instance). This review
isolates and judges the t8 hunks only; t9 is a separate task and is not approved
here. The t8 tests use `Confirm: nil`, so the t9 code does not affect their
result. Re-run this review after t9 lands.

### 2. Method

1. `git archive HEAD` to `/tmp/mage2mcp-prefix` — a clean pre-fix tree.
2. Copied the current tree (without `.git`, `node_modules`, `docs/audit`) to
   `/tmp/mage2mcp-current` for post-fix probes and revert experiments.
3. Wrote independent probe tests in the throwaway copies (never copied back):
   - `TestPrefixViewerCanWrite` (pre-fix): production wiring + the pre-fix
     viewer-read policy, calling `magento.invoice.capture`.
   - `TestPostFixProbe` (post-fix): the same call under the shipped
     `DefaultPolicy`, plus the ladder matrix.
   - `TestNilAuthorizerProbe`, `TestEmptyToolNameProbe` (edge paths).
4. Reverted each fix in a throwaway copy and ran the new tests to prove they
   guard it (§5).
5. Ran the required commands on the reviewed working tree (§4).

### 3. Claim-by-claim verification

#### 3.1 The original attack: **reproduced pre-fix, denied post-fix**

Pre-fix (`/tmp/mage2mcp-prefix`, `HEAD`), with the pre-fix shipped policy
(`viewer` → `*` at read) and the production `authorizeFunc`:

```text
PREFIX RESULT: isError=false structured={"ok":true}
DEFECT REPRODUCED: viewer allowed magento.invoice.capture at pre-fix HEAD
```

Post-fix, same production wiring (`server.NewMCP` + `authorizeFunc` +
`rbac.DefaultPolicy` + auth middleware), verbatim probe output:

```text
POSTFIX role=viewer tool=magento.product.get isError=false structured={"ok":true}
POSTFIX role=viewer tool=magento.invoice.capture isError=true structured={"code":"permission_denied","message":"permission_denied: permission denied"}
POSTFIX role=viewer tool=magento.product.delete isError=true structured={"code":"permission_denied","message":"permission_denied: permission denied"}
POSTFIX role=operator tool=magento.invoice.capture isError=false structured={"ok":true}
POSTFIX role=operator tool=magento.product.delete isError=true structured={"code":"permission_denied","message":"permission_denied: permission denied"}
POSTFIX role=admin tool=magento.product.delete isError=false structured={"ok":true}
```

The pre-fix attack used a fixture tool named `magento.invoice.capture` with
`PermissionWrite`; `TestRealToolPermissionsMatchFixture` proves the real catalog
still labels `magento.product.get` read, `magento.invoice.capture` write, and
`magento.product.delete` destructive, so the fixture mirrors the real tool. The
authorization decision is independent of the handler, so the fixture is a valid
reproduction of SEC-01.

#### 3.2 Fail-closed paths

| Path | Behaviour | Evidence |
|---|---|---|
| Unregistered tool | `authorizeCall` returns `permission_denied` **without consulting the authorizer**; the authorizer-call counter stays false | `TestAuthorizationFailsClosedForUnregisteredTool` passes |
| Empty tool name | `requiredScope("")` is unknown → `permission_denied`; authorizer not called | independent `TestEmptyToolNameProbe`: `EMPTY-NAME err=permission_denied: permission denied code=permission_denied authorizerCalled=false` |
| Unknown required scope | `rbac.Authorize` rejects any scope not in the ladder before matching grants | pre-existing `TestUnknownRequiredScopeDenies` passes |
| `platform.info` | declared read in `serverOwnedTools`; authorization receives `read`; removal of the map entry makes the call deny | `TestAuthorizationCoversPlatformInfo`; revert experiment §5.3 |
| `auth.enabled: false` | `authorizeFunc` returns nil (no authorizer) and logs `AUTHORIZATION DISABLED` at Warn | `TestAuthorizeFuncWarnsWhenAuthDisabled` passes |
| nil authorizer at `server.NewMCP` | **fail-open**: `installPolicy` skips authorization and does not log | independent `TestNilAuthorizerProbe`: `NIL-AUTHORIZER isError=false structured=map[ok:true]`; see MCP-R01 |

The nil-authorizer row is the only path that is neither fail-closed nor logged.
It is not reachable from the shipped binary: `cmd/server` is the only
`server.NewMCP` caller, and it passes a non-nil authorizer whenever auth is
enabled; when auth is disabled it returns nil **and logs**. The package is
`internal/`, so there is no external library consumer. It remains a P3
follow-up for future in-repo callers (MCP-R01).

#### 3.3 `DefaultPolicy` ladder

`TestDefaultPolicyExpressesTheLadder` and the independent post-fix probe agree:

```text
viewer   read=allow  write=deny   destructive=deny   admin=deny
analyst  read=allow  write=deny   destructive=deny
operator read=allow  write=allow  destructive=deny   admin=deny
admin    read=allow  write=allow  destructive=allow  admin=allow
intruder read=deny
```

`DefaultPolicy` has no deny grants, so the shipped policy cannot demonstrate
deny-beats-allow by itself; `TestDefaultPolicyDenyStillBeatsAllow` builds a
custom operator policy with an explicit deny on `magento.product.delete` and
proves the deny wins over the covering `*` write allow. The mechanism in
`Policy.Authorize` is unchanged and still returns on the first matching deny.

Wildcard matching: every shipped grant is `Tool: "*"` with `Instance: ""`.
A grant covers every tool name, and the scope rank is the only gate:
`viewer`/`analyst` rank 1 cannot reach `write` rank 2; `operator` rank 2 cannot
reach `destructive` rank 3; `admin` rank 4 covers all. No read-only role holds a
wildcard above `read`. The guarantee depends on the registry labeling every write
tool as `write`/`destructive` (see MCP-R04).

#### 3.4 `platform.info` seam

`registry.nameRE` requires at least three dot-separated segments, so
`platform.info` cannot be registered. The author's `serverOwnedTools` map is a
single explicit exception at `read`. It is **acceptable for this change**:
`requiredScope` checks the registry first, the map second; a missing map entry
fails closed (proved by removing it); the tool is read-only; and the exception is
tested. It is still a single-source-of-truth hole: a future server-owned tool
must be added to the map or it is denied, and `platform.info` remains absent from
`registry.List()` (the inventory issue MCP-02 in the MCP review). MCP-R02 records
the follow-up.

#### 3.5 Backward compatibility

`grep -rn "Authorize:" --include=*.go .` finds only `cmd/server` and the server
tests; all use the new four-argument signature. `grep -rn "authorizeFunc("`
finds only `cmd/server` and its tests. `go build ./...` passes. The three
pre-existing server tests changed only their callback signatures; their
assertions are unchanged and still pass. The change is a deliberate internal API
break (the package is `internal/`), not an external compatibility break.

### 4. Required commands (verbatim)

```console
$ go build ./...
(no output; exit 0)

$ go test -count=1 ./internal/mcp/server/... ./internal/security/rbac/... ./cmd/server/...
ok      github.com/shuaiZend/mage2mcp/internal/mcp/server    0.163s
ok      github.com/shuaiZend/mage2mcp/internal/security/rbac    0.309s
ok      github.com/shuaiZend/mage2mcp/cmd/server    0.395s

$ go test -race -count=1 ./internal/mcp/server/... ./internal/security/rbac/...
ok      github.com/shuaiZend/mage2mcp/internal/mcp/server    1.217s
ok      github.com/shuaiZend/mage2mcp/internal/security/rbac    1.295s
```

Broader affected set, for regression confidence:

```console
$ go test -count=1 ./internal/security/... ./internal/mcp/... ./cmd/server/...
ok      internal/security/audit    0.316s
ok      internal/security/auth    0.285s
ok      internal/security/confirm    0.082s
ok      internal/security/jwt    1.627s
ok      internal/security/rbac    0.085s
ok      internal/mcp/registry    0.112s
ok      internal/mcp/server    0.133s
ok      internal/mcp/toolerr    0.106s
ok      cmd/server    0.385s
```

### 5. Revert experiments — the new tests guard the fix

1. **Scope-resolution revert.** Replaced `authorizeCall` with
   `opts.Authorize(ctx, tool, instance, registry.PermissionRead)` (the pre-fix
   hard-coded read, no registry check):

   ```text
   --- FAIL: TestAuthorizationReceivesRequiredScope
       server_test.go:535: magento.invoice.capture authorized at scope "read", want "write"
   --- FAIL: TestAuthorizationFailsClosedForUnregisteredTool
       server_test.go:559: call magento.not.registered: calling "tools/call": unknown tool "magento.not.registered"
   ```

2. **Policy revert.** Replaced `DefaultPolicy` with the pre-fix viewer/analyst
   read-only grants:

   ```text
   --- FAIL: TestShippedPolicyEnforcesScopeLadder
       main_test.go:345: role=operator tool=magento.invoice.capture: allowed=false want=true
       main_test.go:345: role=admin tool=magento.product.delete: allowed=false want=true
   ```

3. **`platform.info` seam revert.** Emptied `serverOwnedTools`:

   ```text
   --- FAIL: TestAuthorizationCoversPlatformInfo
       server_test.go:594: platform.info denied
   ```

All three fail as expected, so the tests are sensitive to the code they claim to
guard. The unregistered-tool test fails at the transport layer when the fix is
reverted (the SDK rejects the unknown tool after the permissive authorizer); it
still fails, which is the property that matters.

### 6. Regression safety

- `git diff --numstat` for the test files: `cmd/server/main_test.go` 227/0,
  `internal/security/rbac/rbac_test.go` 80/0, `internal/mcp/server/server_test.go`
  172/3. The only deletions are the three `Authorize` callback signatures adapted
  to the new interface. No test function removed, no assertion relaxed.
- Existing tests `TestAuthorizationDeniesToolCall`,
  `TestAuthorizationReceivesInstance`, `TestCrossInstanceAuthorizationIsEnforced`
  still pass with their original assertions.
- No production caller outside `cmd/server` exists; the worker and admin binaries
  do not build an MCP server.
- Race detector: clean on both changed packages.

### 7. Findings and residual risks

#### MCP-R01: nil authorizer is still a silent fail-open at `server.NewMCP`

- **Severity**: P3 (pre-existing; not reachable from the shipped binary)
- **State**: CONFIRMED
- **Location**: `internal/mcp/server/server.go:105-107` (`installPolicy` returns
  early when `Authorize`, `Audit`, and `Confirm` are all nil; it never logs) and
  `:123-125` (authorization is skipped whenever `Authorize` is nil).
- **Evidence**: independent probe: `NIL-AUTHORIZER isError=false
  structured=map[ok:true]` for a `PermissionWrite` tool with
  `NewMCP(Options{Registry: reg})`. `cmd/server` is the only caller and always
  passes a non-nil authorizer when auth is enabled; when auth is disabled it
  returns nil and logs `AUTHORIZATION DISABLED` (tested). The package is
  `internal/`, so there is no external consumer.
- **Trigger**: a future in-repo caller constructs `server.NewMCP` without
  `Authorize` (or with a nil one) and without `Audit`/`Confirm`.
- **Impact**: every tool call is allowed with no role check and no log. Not a
  production hole today, but a silent trap for the next caller.
- **Proposed fix**: log a warning (or return an error) in `NewMCP` when
  `Authorize == nil`, or require an explicit `AllowUnauthenticated` option for
  the dev/test path.
- **Test requirement**: `NewMCP` with nil `Authorize` logs a warning or fails
  unless the explicit opt-in is set.

#### MCP-R02: `serverOwnedTools` is a hard-coded single-source-of-truth exception

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/mcp/server/server.go:76-105`
- **Evidence**: `platform.info` is declared in a package-level map because
  `registry.nameRE` requires three segments. The map is checked after the
  registry; a missing entry denies (safe). It is tested only for
  `platform.info`.
- **Trigger**: a new server-owned tool is added without updating the map; it is
  denied at runtime (fail-closed but surprising).
- **Impact**: the "registry is the only tool source" invariant has one exception;
  the inventory and permission remain outside the registry (MCP-02 in
  `mcp-protocol-review.md`).
- **Proposed fix**: allow two-segment `platform.*` names in `registry.nameRE`, or
  add a validated `RegisterServerOwned` API; add a test that every server-owned
  tool is read-only and registered.
- **Test requirement**: server-owned tool registration/consistency test.

#### MCP-R03: denied results drop the denial reason and the tool name

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/mcp/server/server.go:236-243` (`deniedResult` builds
  only `code` and `message`); `authorizeCall` (`:154-163`) adds
  `reason=unregistered_tool` and `tool` details that are discarded.
- **Evidence**: post-fix probe shows
  `{"code":"permission_denied","message":"permission_denied: permission denied"}`
  for both a viewer write denial and an unregistered tool; the operator cannot
  distinguish them from the response.
- **Trigger**: any denial.
- **Impact**: diagnosability, not authorization; a missing registration looks
  like a missing grant.
- **Proposed fix**: include bounded `details` (reason/tool) in `deniedResult`, or
  log them in the audit record.
- **Test requirement**: denial body contains the reason for an unregistered tool.

#### MCP-R04: the wildcard policy relies on every write tool being labelled correctly

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/security/rbac/rbac.go:96-101` (`DefaultPolicy` grants
  `Tool: "*"` at each rung); `cmd/server/main_test.go:364-390`
  (`TestRealToolPermissionsMatchFixture` checks only three tools).
- **Evidence**: `DefaultPolicy` cannot grant a read-only role a write **if** the
  tool's registry `Permission` is `write`/`destructive`. A mislabelled write tool
  would be reachable by `viewer` at read. The fixture test pins
  `magento.product.get`, `magento.invoice.capture`, and
  `magento.product.delete`, but not the other 52 write tools.
- **Trigger**: a new write tool is registered with `PermissionRead` (or an
  existing one is mislabelled).
- **Impact**: the policy is only as correct as the registry metadata.
- **Proposed fix**: add a full-catalog permission matrix test (or generate the
  fixture from the registry) so every write/destructive tool is pinned; the MCP
  audit's inventory can be the source.
- **Test requirement**: all tools' permission classes asserted.

#### MCP-R05: auth-disabled is loudly logged but still allows destructive tools

- **Severity**: P3
- **State**: CONFIRMED (by design per t8 requirement 5)
- **Location**: `cmd/server/main.go:229-240`
- **Evidence**: `authorizeFunc` returns nil and logs `AUTHORIZATION DISABLED`
  when `cfg.Auth.Enabled` is false; the server then allows every tool, including
  destructive tools (confirmation still applies if `Confirm` is configured).
  `TestAuthorizeFuncWarnsWhenAuthDisabled` pins the warning.
- **Trigger**: a deployment with `auth.enabled: false` (loopback/dev or
  `-dev-insecure`).
- **Impact**: expected for local development; risky if the warning is not
  monitored. The requirement says preserve + warn, so this is not a defect
  against t8.
- **Proposed fix**: optionally require `-dev-insecure` for a non-loopback
  listener (already enforced by `buildAuth`) and surface the mode in
  `/healthz`/`platform.info`.
- **Test requirement**: end-to-end auth-disabled smoke (currently only the
  callback is tested).

#### MCP-R06: t9's in-progress confirmation changes share the reviewed files

- **Severity**: P3 (process)
- **State**: CONFIRMED
- **Location**: `internal/mcp/server/server.go:164-205`,
  `internal/mcp/server/server_test.go` (order-state/confirmation tests)
- **Evidence**: the working tree contains t9's `enforceConfirmation`/`Consume`
  changes in the same file as t8. This review isolates the t8 hunks; the required
  commands exercise the combined tree and pass, but t9 is not approved here.
- **Trigger**: merging/reviewing before t9 lands.
- **Impact**: a future t9 change could invalidate the combined test run; the t8
  verdict should be re-confirmed after t9.
- **Proposed fix**: re-run `go test -count=1 ./internal/mcp/server/...
  ./internal/security/rbac/... ./cmd/server/...` after t9 completes; review t9
  separately.
- **Test requirement**: n/a (process).

### 8. Verdict

**PASS** for t8. The original SEC-01 defect is reproduced against `HEAD` (a
`viewer` is allowed `magento.invoice.capture`) and is denied after the change
(`permission_denied`), with the operator/admin rungs working as specified. The
new tests fail when the scope resolution, the shipped policy, or the
`platform.info` seam is reverted; no existing test was deleted or weakened; the
required build, unit, and race commands pass.

**Safe to ship** for the shipped `cmd/server` binary, subject to these residual
risks:

1. **MCP-R01** — nil `Authorize` at the `server.NewMCP` boundary is still a
   silent fail-open. Not reachable from `cmd/server`; fix before adding another
   in-repo MCP server or exposing the package.
2. **MCP-R02** — `platform.info`'s `serverOwnedTools` exception is an acceptable
   short-term seam but must not grow silently; follow up with a registry-level
   two-segment `platform.*` rule or a validated server-owned registration.
3. **MCP-R04** — the wildcard `DefaultPolicy` is only as safe as the registry
   permission labels; add a full-catalog permission matrix test.
4. **Operational** — `operator`/`admin` roles are now required for writes.
   Deployments whose tokens only carry `viewer`/`analyst` will correctly lose
   write access; assign the seeded roles before enabling `tools.writes`.
5. **Cross-task** — the write gate (MCP-03 in `mcp-protocol-review.md`) still
   registers `magento.import.start/.cancel/.resume` with `tools.writes: false`;
   the new policy correctly denies a viewer, but an operator can still queue an
   import in a read-only deployment. That is a separate fix.

---

## t25 — review of t16: MAG-10, MAG-11 MSI transfer lost-update detection and partial-failure reporting

| Field | Value |
|---|---|
| Review task | `t25` (round 1) |
| Reviewed task | `t16`, author: magento-engineer |
| Reviewer | qa-engineer (not the author) |
| Change under review | `internal/magento/inventory.go` (+317/−28), `internal/magento/inventory_test.go` (+305/−31), `internal/magento/write_tools.go` (+17/−5) |
| Method | isolated copy of the tree, 14 independent probe tests (26 cases), tool-level calls through the in-memory MCP transport, race detector, scripted fake MSI endpoint |
| Verdict | **PASS** — both claims reproduced, no test deleted or weakened, `write_tools.go` edit confirmed mechanical and confined. One P2 finding (`MAG-R06`) and two P3 findings (`MAG-R07`, `MAG-R08`). |

### 1. Scope, including the out-of-scope `write_tools.go` edit

The declared change is the MSI transfer in `inventory.go` and its tests. The author also
edited `internal/magento/write_tools.go`, which was outside the declared scope. I reviewed
that diff line by line. It is three things and nothing else:

1. `inventoryTransferDescription` (lines 1576–1583): the old text is replaced by one that
   names the re-read, the read-back, the `transfer_id`, the observed before/after
   quantities, the outcome, and the no-retry/no-compensation rule.
2. `inventoryTransferOutput` (lines 1597–1610): six new fields — `transfer_id`,
   `from_before`, `from_after`, `to_before`, `to_after`, `outcome`.
3. The tool handler (lines 1622–1636): adapts to the new `(*TransferResult, error)`
   signature and maps the result into the output, with
   `Transferred: result.Outcome == TransferApplied`.

No other tool, metadata, schema, timeout, or annotation is touched. The change is
mechanical and confined to `magento.inventory.transfer`.

After this review was written, another task (MCP-06 disclosure) touched the same file: the
transfer description is now passed through `discloseWrite(...)` and the metadata is built by
`destructiveToolMetadata` with explicit annotations. That wrapper is not part of t16. I
re-extracted the transfer description, the output struct and the handler from the current
file and diffed them against the reviewed copy: description **same**, output struct
**same**, handler **differs only** by the `discloseWrite(...)` call around the unchanged
description. The t16 content is intact.

`internal/magento/README.md` also shows as modified in the working tree, but its one-line
diff (import payload size) belongs to another task, not t16.

### 2. Method

1. Copied the tree to `/tmp/qa-t25`; confirmed `inventory.go`, `inventory_test.go` and
   `write_tools.go` are byte-identical between the copy and the working tree
   (`diff -q` → same) after the review.
2. Wrote 14 independent probe tests (26 cases including subtests) in
   `internal/magento/zz_qa_review_test.go` (not part of the change; never copied back). They drive the real `Service` through a scripted fake MSI
   endpoint where the reviewer controls exactly what each of the three reads observes, and
   they call the MCP tool through the in-memory transport.
3. Ran every probe under `-race`, plus the package's own tests.

### 3. Claim verification (verbatim probe output)

#### 3.1 MAG-10 — lost update is detected (conflict, no write)

Another writer commits `7/3` between the first read (`10/0`) and the compare-before-write
re-read:

```text
outcome=conflict writes=0 from=10->7 to=0->3 observed=true retryable=false
reads = 2 (read + compare re-read, no write)
error details: {"after_observed":"true","applied_rows":"none","from_observed":"before=10 after=7 intended=7","outcome":"conflict","transfer_id":"tr_…"}
```

A change to only the `from` row and a change to only the `to` row are both detected
(`conflict`, 0 writes). Two concurrent same-process transfers of 3 units from `10/0`
produce `4/6`, not `7/3` twice:

```text
final state = map[default:4 warehouse:6] (want default=4 warehouse=6)
```

The residual window is real and the comment admits it. A writer that commits **after** the
compare re-read is overwritten by the absolute write, and the transfer reports success:

```text
outcome=applied detail="" from=7 to=3   # the concurrent +5 on `to` is lost
```

The code comment (`inventory.go:163–176` and `:480–484`) states that the in-process guard
does not survive a second replica, that the re-read is what detects the cross-replica lost
update, and that "even it leaves the read-to-write window open. Closing that window needs
the operation ledger and a compare-and-set upstream (tracked separately)". The tool
description says "re-read to detect a concurrent writer" and does not claim atomicity or
cross-replica safety. It does not overclaim.

#### 3.2 MAG-11 — mid-batch failure is reported, not retried or compensated

The batch lands the `from` row and fails the `to` row:

```text
outcome=partial applied=from writes=1 retryable=false
detail="only the from row was applied; the to row was not (from 7, intended 7; to 0, intended 3)"
error details: applied_rows=from, transfer_id=tr_… (same id as the result)
message: "… only the from row was applied; no compensation was attempted, verify both sources"
```

The read-back unreadable (write 500 + read-back 500):

```text
outcome=needs_verification applied=none after_observed=false writes=1 retryable=false
detail="the post-write read failed (upstream_unavailable); verify both source quantities before retrying"
error details: transfer_id=tr_…, outcome=needs_verification, after_observed=false
```

One write in both cases — no retry, no compensating write.

#### 3.3 The retryable flag

Go-level `errs.RetryableError` and the agent-facing envelope:

| Outcome | retryable (Go) | `retryable` in MCP envelope |
|---|---|---|
| applied | false (nil error) | n/a |
| conflict | false | absent |
| verification_mismatch | false | absent |
| partial | false | absent |
| needs_verification | false | absent |
| timeout with unchanged state | false (needs_verification) | absent |
| not_applied | **true** | `"retryable": true` |

Only `not_applied` is retryable. `needs_verification` is not, at either layer, so a retry
cannot double-decrement.

#### 3.4 Page-capped reads cannot drive a write

A 100-row page (the `pageSize`) at each stage:

```text
first read capped   -> result=<nil> err=insufficient_data: … writes=0
re-read capped      -> result=<nil> err=insufficient_data: … writes=0
read-back capped    -> outcome=needs_verification after_observed=false writes=1 err=conflict (unknown outcome)
```

A write is never derived from a truncated observation.

#### 3.5 Read-back comparison

The read-back compares the observed post-state against the intended post-state
(`FromApplied = fromAfter == IntendedFrom`, `ToApplied = toAfter == IntendedTo`). Exact for
integer quantities: an off-by-one read-back (`8/3` against intended `7/3`) yields
`verification_mismatch` with `from_applied=false, to_applied=true`. Fractional quantities
are truncated before the comparison (see `MAG-R07`).

#### 3.6 Transfer id

Format `tr_` + 16 hex characters; 500 consecutive generations produced 500 distinct ids.
The id in the result equals the id in the error details for conflict, partial and
needs_verification. Pre-write failures (validation, insufficient source, first-read error)
return no id because no transfer was attempted.

#### 3.7 Tool-level contract

```text
success:  {"from_after":7,"from_before":10,"from_source":"default","outcome":"applied","quantity":3,"sku":"SKU-1","to_after":3,"to_before":0,"to_source":"warehouse","transfer_id":"tr_…","transferred":true}
conflict: {"code":"conflict","details":{…"outcome":"conflict","transfer_id":"tr_…"},"message":"… no write was issued"}
```

### 4. Answers to the review questions

1. **Does the re-read narrow the race, and is the residual window honest?** Yes. The
   compare-before-write re-read turns a cross-replica interleaving into `conflict` with no
   write (probe 3.1). The residual read-to-write window remains, is demonstrated by probe
   `TestQA_MAG10_ResidualWindow`, and is stated explicitly in the code comment; the tool
   description does not claim otherwise. The in-process guard is documented as not
   surviving multiple replicas.
2. **Is `needs_verification` ever retryable?** No — false at the Go layer and absent from
   the MCP envelope; only `not_applied` is retryable (probe 3.3).
3. **Can `readSourcePair` derive a write from a page-capped read?** No. A 100-row page
   returns `insufficient_data` and no write is issued at the first read or the re-read; a
   capped read-back yields `needs_verification` (probe 3.4).
4. **Does the read-back compare observed against intended, exactly for integers?** Yes.
   Exact at integer granularity; fractional quantities are truncated (MAG-R07).
5. **Is the transfer id unique and stable?** Yes: `tr_` + 16 hex, 500/500 unique, identical
   in the result and the error details.
6. **The out-of-scope `write_tools.go` edit?** Mechanical and confined to the transfer tool
   (§1).
7. **Do the three original transfer tests still assert their original behaviour?** Yes:
   `TestTransferSourceQty` still asserts exactly one batch write with `from=7/status=1` and
   `to=3/status=1`; `TestTransferSourceQtyInsufficient` still asserts `invalid_argument`
   and no write; `TestTransferSourceQtyValidation` still asserts `invalid_argument` for all
   four invalid inputs. No test function was removed (`git diff` shows only the signature
   adaptation), and every removed assertion line is superseded by a stronger one.

### 5. Regression safety and verify commands

- No test function deleted or weakened; three tests added
  (`…TransferIDIsUnique`, `…Outcomes`, `…ConcurrentSameProcess`) and one adapted
  (`TestTransferSourceQty`).
- Reviewed files byte-identical to the isolated copy where the full package suite and all
  19 probes pass under `-race`.
- `go build ./internal/magento/...` → exit 0.
- The probes were re-run against a fresh copy of the final tree (which includes the
  MCP-06 wrapper): `go test -count=1 ./internal/magento/` → `ok 1.253s`.
- Final verify commands on the working tree, verbatim:
  `go build ./...` → exit 0;
  `go test -count=1 ./internal/magento/...` → `ok … 0.789s`;
  `go test -race -count=1 ./internal/magento/...` → `ok … 4.127s`.
  (During the review these commands transiently failed on two other tasks' in-flight
  edits — `ImportQueue.Cancel` in `import_jobs_test.go` and `storage.JobQueue` in
  `cmd/worker` — both resolved before this record was finalised; neither was related to
  the transfer.)

### 6. Findings

#### MAG-R06: `applied_rows` reports "none" for `needs_verification`, where the applied state is unknown

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/inventory.go:226-239` (`AppliedRows`), `:395-399` (error detail), `internal/magento/inventory_test.go` (`wantApplied: "none"` for the needs-verification case)
- **Evidence**: when the read-back fails, `FromApplied`/`ToApplied` are never computed and
  stay false, so `AppliedRows()` returns `"none"`. The MCP error envelope for a
  needs-verification transfer contains
  `"applied_rows":"none"` together with `"after_observed":"false"` and
  `"outcome":"needs_verification"`. The same conflation does not occur for `conflict`
  (no write was issued, so "none" is true) or `not_applied` (both rows were observed
  unchanged).
- **Trigger**: the post-write read-back fails (5xx, 403, page cap, malformed payload) after
  the write was issued.
- **Impact**: the machine-readable field states that no row was applied when the truth is
  unknown — exactly the false precision MAG-11 set out to remove. An agent that keys on
  `applied_rows` rather than `outcome`/`after_observed` can conclude the transfer was a
  no-op and retry, which is the double-decrement risk the non-retryable flag exists to
  prevent. The tool description ("… an error naming the rows that were applied") also
  promises a fact the gateway does not have.
- **Proposed fix**: make `AppliedRows()` return `"unknown"` when `!AfterObserved` (or when
  the outcome is `needs_verification`), keep `"none"` only when the read-back proved it,
  change the corresponding test expectation to `"unknown"`, and reword the tool
  description to "names the rows observed to be applied, or reports the state as
  unverified".
- **Test requirement**: the outcomes test must assert `applied_rows=unknown` for the
  needs-verification case and `none` for conflict/not_applied.
- **Verified by**: qa-engineer (probe `TestQA_MAG11_NeedsVerification` + MCP envelope).

#### MAG-R07: the read-back comparison is integer-truncated, so a fractional mismatch is reported as applied

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/magento/inventory.go:459-477` (`readSourcePair`, `int(item.Quantity)`), `:318-320` (`FromApplied`/`ToApplied`)
- **Evidence**: `readSourcePair` converts `float64` quantities with `int(...)`. A row that
  holds `7.9` reads as `7` and matches the intended `7`, so the transfer is reported
  `applied`; the same truncation makes `from_before` read `10` from `10.9`. Probe:
  `result from=10->7 to=0->3 intended=7/3 applied=true/true outcome=applied`.
- **Trigger**: any Magento source item with a fractional quantity (MSI supports decimals),
  including another writer changing a row by a fraction between the read and the read-back.
- **Impact**: the read-back cannot confirm the intended post-state at sub-unit granularity;
  a mismatch smaller than one unit is invisible and the transfer is reported verified. The
  transfer also writes integers, so the actual movement differs from the requested quantity.
  This is the pre-existing MAG-12 truncation, now inherited by the verification claim.
- **Proposed fix**: compare the raw `float64` quantities (or reject fractional source
  quantities explicitly with a typed error) instead of truncating before the comparison;
  document the integer-only contract if fractional quantities remain unsupported.
- **Test requirement**: a fractional read-back case asserting either an exact comparison or
  an explicit unsupported-quantity error.
- **Verified by**: qa-engineer (probe `TestQA_FractionalTruncation`).

#### MAG-R08: the transfer documentation still describes the pre-fix flow

- **Severity**: P3
- **State**: CONFIRMED (the author flagged these)
- **Location**: `docs/magento-fields.md:75`, `docs/implemented-tools.md:128`
- **Evidence**: both rows still say "reads both rows … upserts both in one batch; not
  transactional" / "refuses an insufficient source; not transactional". They do not
  mention the compare-before-write re-read, the conflict outcome, the read-back, the
  `transfer_id`/`outcome`/before-after fields, or that only `not_applied` is retryable.
- **Trigger**: an operator or agent reading the documented contract before a transfer.
- **Impact**: the documented call pattern and result shape are stale; the conflict and
  needs-verification semantics are invisible outside the tool description.
- **Proposed fix**: update both rows to: "Reads both sources, re-reads to detect a
  concurrent writer (conflict → no write), upserts both rows in one batch, then reads them
  back; the result carries a transfer_id, the observed before/after quantities and an
  outcome (applied | conflict | verification_mismatch | partial | not_applied |
  needs_verification). Only not_applied is safe to retry; nothing is compensated
  automatically."
- **Test requirement**: documentation review; the docs contract check does not cover this
  text.
- **Verified by**: qa-engineer.

### 7. Verdict

**PASS** for t16. Both claims are independently reproduced:

- MAG-10: a concurrent writer between the read and the compare-before-write re-read is
  detected and the transfer returns `conflict` with the observed state and **no write**;
  two same-process concurrent transfers leave `4/6`, not a lost update. The residual
  read-to-write window is real, demonstrated, and honestly documented.
- MAG-11: a mid-batch failure returns `partial` naming the applied row, or
  `needs_verification` when the read-back is unreadable; both carry the `transfer_id` in
  the error details and neither retries nor compensates. Only `not_applied` is retryable,
  at both the Go and MCP layers.

The out-of-scope `write_tools.go` edit is mechanical and confined to the transfer tool. No
existing test was deleted or weakened. The review is conditional on one P2 follow-up before
release: **MAG-R06** (`applied_rows` must say `unknown`, not `none`, when the read-back
failed), because it is a false statement in the machine-readable state report. `MAG-R07`
and `MAG-R08` are P3 and can be batched with the same change.

---

## t26 — re-review of t8 at the current revision (SEC-09 delta)

| Field | Value |
|---|---|
| Review task | `t26` (round 2) |
| Reviewed task | `t8`, author: security-auditor |
| Reviewer | mcp-engineer (not the author) |
| Reviewed revision | working tree at `66786a5` + the t8 hunks and the later SEC-09 doc-comment/test delta; t19 changes are present but are not part of this t8 re-review |
| Method | byte-level delta inspection, fresh current-tree copy, role-matrix probe, two revert experiments, required commands re-run |
| Verdict | **PASS holds** — the SEC-09 delta is comment/test-only and not behavioural; the t8 scope ladder still denies/allow exactly as round 1 found. |

### 1. Exact delta since the round-1 review

`internal/security/rbac/rbac.go`

- Added a `Policy` doc comment (lines 49-55) stating that the identity's roles are the
  IdP's verified claim, the package does not map subjects to roles, and an unknown role
  is simply a role with no grants.
- Added an `Authorize` doc comment (lines 102-105) stating that roles are additive,
  a role matching no grant contributes nothing, and an explicit deny wins.
- No executable change. `New`, `DefaultPolicy`, `Authorize`, `matchesTool`,
  `matchesInstance`, and `scopeRank` are byte-identical to the round-1-reviewed code
  apart from those comments.

`internal/security/rbac/rbac_test.go`

- Added `TestUnknownRoleCannotWidenAccess` (test-only). It asserts the existing
  behaviour: an unknown role alone cannot read or destroy, no roles cannot read, an
  unknown role adds nothing to a viewer (read allowed, write denied), and padded,
  case-different, or privileged-sounding role names are not normalised into a grant.
- `TestDefaultPolicyExpressesTheLadder` and `TestDefaultPolicyDenyStillBeatsAllow`
  are unchanged.

**Is any of it behavioural?** No. The two rbac.go edits are comments; the only code
change since round 1 is the added test, which exercises the unchanged `Authorize`
implementation. The test is still useful as a SEC-09 guard: it would fail if role
matching became case-insensitive, trimmed whitespace, treated `role:admin` as a
wildcard, or treated an unknown role as a grant.

**Other current-tree changes, explicitly out of t8:** `internal/mcp/server/server.go`
and `cmd/server/main.go` also contain t19 changes (platform.info as a first-class
registry entry, the registry write gate, timeout enforcement, uniform error
envelopes). Those are behavioural but belong to t19; the t8-specific
`Options.Authorize`/`authorizeCall`/`requiredScope` contract and the
`DefaultPolicy`/`authorizeFunc` wiring are unchanged in substance. t19 removed the
`serverOwnedTools` side map, which strengthens rather than weakens the t8
single-source-of-truth rule; `TestAuthorizationCoversPlatformInfo` still passes
because platform.info is now a registry entry.

### 2. Role-matrix re-confirmation (fresh current-tree copy)

```text
POSTFIX role=viewer tool=magento.product.get isError=false structured={"ok":true}
POSTFIX role=viewer tool=magento.invoice.capture isError=true structured={"code":"permission_denied","details":{"reason":"no matching grant"},"message":"permission denied"}
POSTFIX role=viewer tool=magento.product.delete isError=true structured={"code":"permission_denied","details":{"reason":"no matching grant"},"message":"permission denied"}
POSTFIX role=operator tool=magento.invoice.capture isError=false structured={"ok":true}
POSTFIX role=operator tool=magento.product.delete isError=true structured={"code":"permission_denied","details":{"reason":"no matching grant"},"message":"permission denied"}
POSTFIX role=admin tool=magento.product.delete isError=false structured={"ok":true}
```

The shipped test also covers analyst: `TestShippedPolicyEnforcesScopeLadder` has
`analyst` read allowed and `invoice.capture` denied. The denial now carries the
t19/t18 `details.reason` field; that is an envelope improvement, not a change to the
authorization decision.

### 3. Fail-closed and unknown-scope re-confirmation

- `TestAuthorizationFailsClosedForUnregisteredTool` — PASS. An unregistered tool is
  denied with `permission_denied` and the injected authorizer is never called.
- `TestUnknownRequiredScopeDenies` — PASS. A required scope outside the
  `read < write < destructive < admin` ladder is denied before grant matching.
- `TestAuthorizationCoversPlatformInfo` — PASS. platform.info resolves `read` from
  the registry (first-class after t19).
- `TestUnknownRoleCannotWidenAccess` — PASS (all eight subtests).

### 4. Revert experiments at the current bytes

1. **Scope-resolution revert.** Replaced `authorizeCall` with the pre-fix
   `opts.Authorize(ctx, tool, instance, registry.PermissionRead)` (no registry
   lookup):

   ```text
   --- FAIL: TestAuthorizationReceivesRequiredScope
       server_test.go:550: magento.invoice.capture authorized at scope "read", want "write"
   --- FAIL: TestAuthorizationFailsClosedForUnregisteredTool
       server_test.go:574: call magento.not.registered: calling "tools/call": unknown tool "magento.not.registered"
   ```

2. **Policy revert.** Replaced `DefaultPolicy` with the pre-fix viewer/analyst-only
   read grants:

   ```text
   --- FAIL: TestShippedPolicyEnforcesScopeLadder
       main_test.go:345: role=operator tool=magento.invoice.capture: allowed=false want=true
       main_test.go:345: role=admin tool=magento.product.delete: allowed=false want=true
   ```

Both experiments fail exactly where they should. The round-1 third experiment
(empty `serverOwnedTools`) no longer applies because t19 removed that map; the
equivalent current guard is `TestAuthorizationFailsClosedForUnregisteredTool` plus
`TestAuthorizationCoversPlatformInfo`, both passing.

### 5. Required commands (verbatim)

```console
$ go build ./...
(no output; exit 0)

$ go test -count=1 ./internal/mcp/server/... ./internal/security/rbac/... ./cmd/server/...
ok      github.com/shuaiZend/mage2mcp/internal/mcp/server    0.417s
ok      github.com/shuaiZend/mage2mcp/internal/security/rbac    0.092s
ok      github.com/shuaiZend/mage2mcp/cmd/server    0.426s

$ go test -race -count=1 ./internal/mcp/server/... ./internal/security/rbac/...
ok      github.com/shuaiZend/mage2mcp/internal/mcp/server    1.627s
ok      github.com/shuaiZend/mage2mcp/internal/security/rbac    1.125s
```

Focused t8 tests:

```text
--- PASS: TestAuthorizationFailsClosedForUnregisteredTool
--- PASS: TestUnknownRequiredScopeDenies
--- PASS: TestDefaultPolicyExpressesTheLadder
--- PASS: TestUnknownRoleCannotWidenAccess (8/8 subtests)
--- PASS: TestShippedPolicyEnforcesScopeLadder
--- PASS: TestAuthorizeFuncPassesRequiredScope
```

### 6. Exact file hashes reviewed

```text
d07cd2fb5ddf255a1022c43b7c99a5766bac655d08a05829eb51741bee4174dc  internal/mcp/server/server.go
84a4538323e458b9faf8efb45a60fdceb7173b34f9020436480f88b5a805ec5c  internal/mcp/server/server_test.go
1c0dab668a140cf43eb3bad5d36f74e0bbc0428eec752d6512c8a05c6e967beb  cmd/server/main.go
85e425b17026a80da5176baf9cca6da4d7a6da19c0011cbac0f692bee174b8e6  cmd/server/main_test.go
4980cb4f0160822f97425331a51ddfe0b38ff4ab5c8e14dd8909e0f744d0a754  internal/security/rbac/rbac.go
1dc3b19768555671e4f46b26447cdf292a6ba988c4b04ab9a1fb80ecccdf2814  internal/security/rbac/rbac_test.go
```

### 7. Verdict

**PASS holds for t8 at the current revision.** The SEC-09 delta is comments plus one
test; it is not behavioural. The t8 scope ladder still produces the exact role matrix
from round 1, fail-closed for an unregistered tool, and denies an unknown required
scope. Both decisive revert experiments still fail at the current bytes, so the tests
continue to guard the fix.

Residual risks from the round-1 review remain unchanged (nil authorizer at the
`server.NewMCP` library boundary, and the operational requirement to assign
operator/admin roles before enabling writes). The t19 changes in the same files
(platform.info first-class, timeout enforcement, uniform envelopes, registry write
gate) are separately owned and are not approved by this t8 re-review.

## t37 — final Magento review: money safety, MSI transfer, configurable resume

| Field | Value |
|---|---|
| Review task | `t37` (round 1, final Magento review) |
| Reviewed tasks | `t10`, `t16`, `t23`, `t24`/`t29`, `t28` (author: magento-engineer) |
| Reviewer | security-auditor (not the author) |
| Reviewed revision | working tree at `66786a5` plus the Magento remediation hunks; hashes pinned in §1 |
| Method | hash-pinned code reading, one revert experiment, two state-changing replay probes, adversarial reconciliation walk, three required commands |
| Verdict | **NEEDS_REVISION** — one P1 (wrong-entity order reconciliation); the MAG-01 and MAG-03 money paths themselves hold. |

### 1. Exact file hashes reviewed

Production files (all still matched at the time this record was written):

```text
550cb4533943f2859c045972c3fda7d18b5290d3002d63389e8ddf48c984994b  internal/magento/media.go
0cff38f0f2d714c4d16709bd0e2b484325c7c281f2c8af87395f94d0d1e7257b  internal/magento/configurable.go
d0faf8928f397c542a8629ff43cb80ffc6d80e0fd1775230b3f7f82460dbe8c7  internal/magento/inventory.go
49cb0833d8b5726abac126c34cdf95f59b70674537fdcabc3a80500584183e59  internal/magento/invoice_write.go
a7264fb90f2f9f62398a47b1c67a51d5f068a0d185022a598942f054598032cb  internal/magento/order_documents_write.go
a5b03a12c7e7e20c60000318d55ac64d4742e22b826424fc792a055890506336  internal/magento/order_create.go
70b35d3212f4b19791d28879113b6da0113a28e5a89e496c6f6efea6eadb15bd  internal/magento/order_status_write.go
035d9637d5f128bc12bb9211841977f78cea91773c36bc7c8035c1198977c2f2  internal/magento/operations.go
84f21daf33f4e3a7b5ff52fd6262041974e10c034485e4e72799683a96fe3e41  internal/magento/write_tools.go
```

Test files relied on as evidence:

```text
492663cce4ebfdae127940158b6bf5efb88ecf2c8074c8ba507ae0aab1264492  internal/magento/invoice_capture_test.go
ee1da351795c3c410a72ace97e89b153c2ec98275384a1b6366ce9b9f38fd73a  internal/magento/order_create_test.go
fb19b92067f76f8e6e5d102e4f38e3f32e38d60026ca331455866a8bcaf94e63  internal/magento/document_write_test.go
d18f5138708d42db1f70775aee8a9d3a6fb37428a588d68b811f2689e00ab06c  internal/magento/configurable_test.go
2f4db86513931fad85ab8ca84454ea4673cd40c16584d88cfb917d39aee5e547  internal/magento/inventory_test.go
```

### 2. Method

1. Read every file above in full, then read the guarding tests rather than trusting
   them: a test that passes because its fake does not model the real state change is
   not evidence.
2. One revert experiment on the highest-consequence guard (MAG-01 paid-invoice
   pre-flight), with the file restored byte-identically afterwards.
3. Two replay probes against **state-changing** fakes: a successful capture moves the
   invoice to paid, and a successful credit memo increments `qty_refunded` — exactly
   what Magento does and what the shipped fakes do not model.
4. An adversarial walk of both reconciliation heuristics named in the brief.
5. The three required commands, run last, on the restored tree.

### 3. Acceptance verification

| Item | Result | Evidence |
|---|---|---|
| MAG-01 second capture of a paid invoice: no POST, typed conflict | **PASS** | `invoice_write.go:224-239` returns `conflict` before `ops.Begin`; revert experiment in §4 |
| MAG-01 timeout during capture → UNKNOWN, non-retryable | **PASS** | `TestCaptureInvoiceTimeoutIsUnknown`: `operation_unknown`, `RetryableError == false`, one POST, second call sends no second POST |
| MAG-04 retried order create places no second order | **PASS** | `TestCreateOrderReplaysSameKey`: `replayed=true`, place calls stay 1, no second cart |
| MAG-04 ambiguous place keeps the cart and reconciles | **PASS (mechanism)** | `TestCreateOrderAmbiguousReconciles`: `deletes == 0`, totals + search read once each, reconciled result recorded. Correctness caveat: MAG-R02 |
| MAG-04 more than one match → UNKNOWN | **PASS** | `TestCreateOrderAmbiguousMultipleMatches`; zero matches → recorded failure (`TestCreateOrderAmbiguousNoMatch`) |
| MAG-03 over-refund rejected before any upstream call | **PASS** | `checkRefundable` at `order_documents_write.go:138` runs before `ops.Begin` at `:149`; test asserts 0 POSTs and 0 ledger begins |
| MAG-03 amount/currency in integer minor units | **PASS** | ledger assertions: 12999/USD (capture), 12998/USD (invoice, 2 × 64.99), 2500/USD (refund); `MoneyFromDecimal` is the only decimal parser |
| MAG-02 invoice route honours the capture flag | **PASS** | `POST /order/{id}/invoice` (workflow route) with a top-level `capture`; `Captured` is derived from the returned invoice state; the repository route is not used |
| MAG-10/MAG-11 transfer detection, partial naming, unknown rows, no compensation | **PASS** | re-read before write aborts with `conflict`; `AppliedRows()` returns `"unknown"` when `needs_verification` or the read-back failed; partial names the applied row; no compensating write exists |
| MAG-05/MAG-07 resumable configurable with operation_id | **PASS** | `operation_id` is in the schema and the result; precheck re-reads upstream, resume reuses children/parent and repairs links; a fresh run refuses a pre-existing SKU; `orphan_skus`/`failed_stage`/`completed_stages` are asserted |
| At least one P0/P1 fix validated by reverting it | **PASS** | §4 |
| Reconciliation false-positive assessment | **FAIL (finding)** | §7 / MAG-R02 |

### 4. Revert experiment — the MAG-01 guard is what the test proves

The paid-invoice guard in `invoice_write.go` was changed from `case invoiceStatePaid:`
to an unreachable case, the test run, then the original file restored from a copy:

```text
$ go test -count=1 -run TestCaptureInvoice$ -v ./internal/magento/   # guard disabled
=== RUN   TestCaptureInvoice/refuses_an_already_paid_invoice
    invoice_capture_test.go:93: err = <nil>, want conflict
=== RUN   TestCaptureInvoice/refuses_a_canceled_invoice
=== RUN   TestCaptureInvoice/validation
--- FAIL: TestCaptureInvoice (0.00s)
    --- PASS: TestCaptureInvoice/captures_an_open_invoice (0.00s)
    --- FAIL: TestCaptureInvoice/refuses_an_already_paid_invoice (0.00s)
    --- PASS: TestCaptureInvoice/refuses_a_canceled_invoice (0.00s)
    --- PASS: TestCaptureInvoice/validation (0.00s)
FAIL    github.com/shuaiZend/mage2mcp/internal/magento    0.396s
```

```text
$ go test -count=1 -run TestCaptureInvoice$ -v ./internal/magento/   # restored
--- PASS: TestCaptureInvoice (0.00s)
    --- PASS: TestCaptureInvoice/captures_an_open_invoice (0.00s)
    --- PASS: TestCaptureInvoice/refuses_an_already_paid_invoice (0.00s)
    --- PASS: TestCaptureInvoice/refuses_a_canceled_invoice (0.00s)
    --- PASS: TestCaptureInvoice/validation (0.00s)
PASS    github.com/shuaiZend/mage2mcp/internal/magento    0.241s
```

Restored hash equals the reviewed hash: `49cb0833d8b5726abac126c34cdf95f59b70674537fdcabc3a80500584183e59`.

### 5. Reproduced defect — recorded replay is unreachable for the pre-flighted money tools

Probe: a fake whose state changes after a successful mutation (capture → invoice
state 2; credit memo → `qty_refunded` 2). Both tools are called twice with the same
`idempotency_key` and identical arguments.

```text
=== RUN   TestProbeReplayAfterSuccess/capture
    PROBE: second capture with the same key -> conflict: the invoice is already paid;
    no capture request was sent (code=conflict); POSTs=1
=== RUN   TestProbeReplayAfterSuccess/creditmemo
    PROBE: second full-quantity refund with the same key -> invalid_argument: order item 1
    has 0 refundable units, cannot refund 2 (code=invalid_argument); POSTs=1
```

The ledger's `DecisionReplay` path is never reached because the state pre-flight runs
before `ops.Begin`. `magento.order.cancel` has the same shape (`conflict: already
canceled`). The machine-checked disclosures at `write_tools.go:436-438` say these
repeats replay the recorded result; they do not. See MAG-R01.

### 6. Findings

| ID | Severity | P-ladder | Location | Problem | Required fix |
|---|---|---|---|---|---|
| MAG-R02 | **high** | **P1** | `order_create.go:214-223`, `order.go:31-33` | Order reconciliation matches customer + 15-min window + grand total only; the order projection never requests `quote_id`. A same-customer identical-total order inside the window is recorded as this operation's success (`reconciled=true`, ledger `succeeded`, foreign `upstream_ref`) and returned to the agent, which may then invoice or ship the wrong order. | Match on the cart→order link (`quote_id`) exactly; if the instance exposes no cart handle, return `operation_unknown` with candidate ids instead of a heuristic success. Test: matching-total foreign order must yield UNKNOWN. |
| MAG-R01 | medium | P2 | `invoice_write.go:224`, `order_status_write.go:136`, `order_documents_write.go:130`; `write_tools.go:436-438` | Replay is unreachable for capture, cancel, and full-quantity refunds because the pre-flight precedes `ops.Begin`; the disclosures claim a replay that cannot happen. No duplicate mutation; the error misleads an agent retrying after a lost response. | Check the ledger before the pre-flight (or move `Begin` above it and `Fail` on a terminal-state conflict). Test: state-changing fake, second call must return `replayed=true` with zero extra mutations; otherwise correct the disclosure strings. |
| MAG-R03 | medium | P2 | `order_create.go:231-243` | A single empty reconciliation read is declared "the order was not placed" and returned as retryable. On a lagging read replica that becomes a duplicate order. | Require two agreeing reads (bounded delay) before `failed`; otherwise keep UNKNOWN. |
| MAG-R04 | low | P3 | `inventory.go:311-345` | Transfer concurrency detection is optimistic (read2 → POST window) and the per-SKU lock is per-process; a writer that lands in the window and produces the intended values is masked as `applied`. Bounded by the post-write read-back and documented as a mitigation. | State the residual in the tool description, or accept and document as a known limitation. |
| MAG-R05 | low | P3 | `operations.go:239-242` (`operation_ledger.go:283-296`) | An explicit idempotency key is not caller-scoped: a second subject presenting the same key and arguments receives the first subject's recorded result (`replayed=true`) and its operation id. No second mutation; matches the approved §6.3 shape. | Scope the key by subject in the ledger, or document that keys must be unguessable and are not caller-scoped. |
| MAG-R06 | low | P3 | `configurable.go:186-188`, `inventory.go:339-343` | (a) A comment claims a ledger-backed resume that does not exist — resume is the upstream read-back and the `cfg_` id is a caller-carried handle. (b) `from_after`/`to_after` are emitted as 0 when the read-back failed; an agent ignoring `after_observed` reads "0 units". | (a) Correct the comment; (b) omit the fields when `AfterObserved` is false. |

### 7. Adversarial reconciliation assessment

**Orders (MAG-R02).** Match keys are customer + `created_at >= now-15m` + total
(±0.005) + currency. False-positive construction: customer 7 places a 19.98 order
through the storefront at 12:03; the agent's `magento.order.create` for customer 7 at
19.98 times out at 12:04; the reconciliation search at 12:05 returns the storefront
order as the single match → `reconciled=true`, ledger `succeeded`, and the agent is
handed an order it did not place. The real placement may have failed, so the intended
order is silently never created, and any follow-on invoice/ship acts on the wrong
entity. A reconciliation that reports success for the wrong entity is worse than
UNKNOWN, which is exactly why this is the blocking finding.

**Credit memos.** There is no credit-memo reconciliation: an ambiguous refund is
recorded `unknown` and never matched by invoice/amount. The concern does not apply;
the safe behaviour is the implemented one.

### 8. Required commands (verbatim)

```text
$ go build ./...
exit 0

$ go test -count=1 ./internal/magento/...
ok      github.com/shuaiZend/mage2mcp/internal/magento    1.438s

$ go test -race -count=1 ./internal/magento/...
ok      github.com/shuaiZend/mage2mcp/internal/magento    5.950s
```

The temporary probe file was deleted; no production file is left modified.

### 9. Severity summary and release implication

**No P0.** One **P1** (MAG-R02, order reconciliation wrong entity), three **P2**
(MAG-R01 replay contract, MAG-R03 single-read "not placed", MAG-R04 transfer
concurrency residual — the last is bounded and accepted-by-design), two **P3**
(MAG-R05 key scoping, MAG-R06 comment/zero-value precision).

The MAG-01 capture path and the MAG-03 refund path have **no P0/P1**: their
money-safety claims hold, including the no-second-POST and UNKNOWN-on-timeout
properties. The P1 sits in **order creation's reconciliation** (MAG-04): the mechanism
is present and tested, but it can attribute a foreign order to the operation.

Therefore: **order creation must be NOT READY** until MAG-R02 is fixed; capture,
refund and invoice creation may be **CONDITIONALLY READY**. If the release gate treats
"order/payment operations" as one surface, the combined verdict for that surface is
NOT READY.

### 10. Verdict

**NEEDS_REVISION.** Six findings, one P1. The money-movement primitives (integer minor
units, ledger intent before the call, UNKNOWN on ambiguity, no automatic retry, no
automatic compensation) are sound and revert-proven; the defect is in the
reconciliation heuristic that turns an ambiguous order placement into a success, and
in an idempotency disclosure that promises a replay the pre-flights prevent.
MAG-R02 and MAG-R01 are the repair targets; MAG-R03 should at least stop calling a
single read "proven"; MAG-R04/R05/R06 are hardening and can ride along.

**Routing note.** None of the findings is a `docs/` defect: MAG-R06(a) is a code
comment in `internal/magento/configurable.go` and MAG-R05's documentation half is a
property description in `internal/magento/write_tools.go` — both owned by the Magento
workstream, not the docs workstream. They should ride along with the repair (or be
dropped) rather than consume a docs task.

---

## t38 — final reliability review: fencing, ledger terminality, atomicity, retry gate, reconciler

| Field | Value |
|---|---|
| Review task | `t38` (round 2) |
| Reviewed tasks | `t11` (job fencing/lease/recovery/retry), `t12` (operation ledger state machine), `t30` (middleware gate, UNKNOWN, retry gate, atomic confirmation+ledger, reconciler, audit decision) |
| Reviewer | mcp-engineer (not the author) |
| Reviewed revision | working tree at `66786a5`; hashes below |
| Method | independent PostgreSQL probes in `/tmp/mage2mcp-t38`, pre/post-fix revert experiment, DB race suite, source inspection; authors' tests were not trusted |
| Verdict | **NEEDS_REVISION** — one P0 blocker in the middleware gate; REL-01 holds, the atomic transaction holds, but REL-02 does not hold as stated and crash recovery is incomplete. |

### 1. Exact file hashes reviewed

```text
8d0b1cb0e42fc61fd09602c5311f7f0bd35a62f65898f46fa2ba8f0e7a68d952  internal/jobs/jobs.go
c6df34d916b1026cbf3def9a954e2d2aeb3ca6123dc5a4742094f7d52504f2bd  internal/storage/jobqueue.go
a9e29264d7d919cd96f91d543eb745d92c9f609f38d9e5a5d22fc8ca0263fd24  internal/storage/migrations/000005_job_fencing.up.sql
edbca96b834112dd42b91c09e791445d11a9ef9bc7850ecd437bd9e760f3d7a2  internal/storage/operation_ledger.go
9cf17b4a20b1fceee1eaa3c16dce8600d4bf21b00a400b326dd1e418601f518f  internal/storage/migrations/000006_operation_ledger.up.sql
483fc2a9a0695e57d5c039fd06fad77723f13454396364b2232bb0ba9556b669  internal/storage/confirm.go
1aaab7937ee0094f9d39145f654302e282f261c34e68de693965d7dd2aaab808  internal/storage/audit.go
f687dcdfd2554b8a46ddac7c9c0195f8def9cac5687e4182dda76eb759797e74  cmd/worker/main.go
f9f9eeadfef71e1ffe4b7bf6d66157d774ea226f7b4433381705428b76482323  internal/mcp/server/server.go
0eac569f15d30a2c0bed4487f4a8404a24a1c5d9b8b2faf982cb57c7009f93b6  internal/magento/client.go
```

**Post-review tree movement (t42 repair in progress).** `internal/storage/operation_ledger.go`
has moved since this review: at review time it was
`edbca96b834112dd42b91c09e791445d11a9ef9bc7850ecd437bd9e760f3d7a2`; the current
tree is `668df8dc8620b5b1e31e3af0787a3229beeb5234a3644b4d9580f33c8fc7b2f7`. The
findings and verdict above apply to the reviewed bytes. t43 must re-bind to fresh
hashes and must not reuse this section's hashes for the post-repair verdict.

### 2. Interleavings reproduced

**REL-01 fencing (PASS).** After lease expiry and re-claim with a new token, the old
worker's `Complete`, `Fail`, `Extend` and `SetResult` all affected zero rows and
returned `jobs.ErrLeaseLost`/`code=conflict`; the row stayed `running`, attempts `2`,
with the new token. A live `Complete` with the new token succeeded. Two workers racing
one queued job produced exactly one claim.

**REL-03 lease (PASS).** With `Lease=400ms > TaskTimeout=200ms` and a handler sleeping
`700ms` (longer than one lease), the worker renewed the lease 14 times, ran the handler
exactly once, and completed exactly once.

**REL-16/SEC-06 atomicity (PASS for the stated failure window).** A database failure
after the confirmation `Consume` (NUL in `entity_id`) rolled back both effects:
confirmation stayed `approved`, zero operations. A failing audit write rolled the
ledger transition back; the row stayed `pending`. The unknown outcome wrote the
transition and the `audit_events` row together.

**REL-16-R (confirmed as documented).** The five self-ledgered tools
(`magento.invoice.create`, `magento.invoice.capture`, `magento.creditmemo.create`,
`magento.order.create`, `magento.order.cancel`) contain no audit writes; the
middleware writes their outcome audit after the tool commits. This is the documented
REL-16-R limitation and is bounded as stated.

**REL-02 middleware gate (BROKEN — P0).** `BeginCall` returns `DecisionInProgress`
(operation executing) and `DecisionRetry` (failed non-ambiguous) with a nil error
(`operation_ledger.go:309-318`). `server.go:209-214` handles only `DecisionReplay`, so
the handler runs in both cases. Probes: both decisions returned `isError=false`,
`structured={"ok":true}`. A real second `BeginCall` while executing returned
`decision=in_progress err=<nil>`. `FinishCall` then sees `state != pending`, writes an
audit `allow`, and leaves the ledger executing/failed.

**Ledger recovery (BROKEN — P1).** An `executing` row with an expired lease stayed
`executing` after `ClaimReconcilable`; no T5 recovery exists. A `reconciling` row with
an expired lease stayed `reconciling`; a second claim found nothing and `Abort` returned
`invalid_argument: operation cannot be aborted from that state`.

**Retry/ambiguity edges (BROKEN — P2).** `Retry` reopened a manually seeded
`failed/upstream_timeout` row (`applied=true`). `retryAllowed` returned `true` for an
idempotent task with `CodeUpstreamUnavailable`, `Retryable=true`, `outcome=unknown`.

**Two-path guard (counts correct, escape possible — P2).** The real catalog is
`55 write / 50 gated / 5 self-ledgered`. Adding `magento.product.create` to
`selfLedgeredTools()` made the guard pass with `49 gated / 6 self-ledgered`, even
though `product.create` does not self-ledger. The test only checks that a declared
name is a write tool, not that it writes an operations row.

### 3. Findings

| ID | Severity | P-ladder | File:line | Problem | Required fix |
|---|---|---|---|---|---|
| REL38-01 | **blocker** | **P0** | `internal/mcp/server/server.go:209-214`; `internal/storage/operation_ledger.go:309-318` | Middleware handles only `DecisionReplay`. `BeginCall` returns `DecisionInProgress` and `DecisionRetry` with nil error, so the write handler runs while another attempt owns the operation or after a definite failure. `FinishCall` writes an audit allow and leaves the ledger failed/executing; repeated calls repeat the unledgered execution. Reproduced with both decisions and a real executing row. | Switch on every `GateDecision`: replay→replay, proceed→run, retry→`Retry`+re-`Begin` (or typed conflict), in_progress→conflict/202 with operation_id, unknown→error. Do not return nil for non-proceed decisions. Add real-PostgreSQL tests for executing and failed rows. |
| REL38-02 | **high** | **P1** | `internal/storage/operation_ledger.go:963-983` | `BeginCall` consumes the confirmation before `beginInTx` and commits on any nil error. For retry/in_progress/replay the approval is burned with no new admitted mutation. Reproduced: failed row + fresh approval → `decision=retry`, `err=nil`, `confirmation=consumed`. | Consume only for `GateProceed`, or roll back on non-proceed. For retry, perform `Retry`+re-`Begin` in the same transaction after consuming. Test approval state for every non-proceed decision. |
| REL38-03 | **high** | **P1** | `internal/storage/operation_ledger.go:497-525,596-598`; migration `000006:66`; `cmd/worker/main.go:228-232` | `ClaimReconcilable` only selects `unknown`. There is no recovery for pending, executing or reconciling rows; T5 (`executing` + expired lease → `unknown`) is unimplemented, `operations_executing_idx` is unused, and `Abort` rejects `reconciling`. A crash after `BeginCall`/`Execute` or during reconciliation strands the operation and approval. | Add bounded recovery before claiming: executing+expired lease→unknown, reconciling+expired lease→unknown, stale pending→unknown/failed with reason. Allow `Abort` from reconciling after proof. Tests for each crash window; never auto-retry an ambiguous outcome. |
| REL38-04 | medium | **P2** | `internal/storage/operation_ledger.go:629-636,840-845` | `Retry` excludes only `unknown`/`ambiguous`; `isAmbiguousCode` also includes `upstream_timeout`. A failed/upstream_timeout row can be reopened. Reproduced `Retry(upstream_timeout) applied=true`. | Use the same `isAmbiguousCode` predicate in `Retry`; test every ambiguous code. |
| REL38-05 | medium | **P2** | `cmd/server/audit_decision_test.go:35-87`; `cmd/server/main.go:505-513` | The two-path guard only checks that declared self-ledgered names are write tools; it does not verify they write an operations row. The hand-maintained list is an escape hatch. Reproduced escape with `magento.product.create` (49/6, test passes). | Derive self-ledgered status from code/marker/interface, or add a per-tool integration test proving an operations row is created/transitioned. Assert the exact 55/50/5 set. |
| REL38-06 | medium | **P2** | `internal/jobs/jobs.go:340-350` | `retryAllowed`'s comment says ambiguous outcomes are not retried, but it only rejects `CodeOperationUnknown`; a Retryable `CodeUpstreamUnavailable` with `outcome=unknown` is retried. Reproduced `retryAllowed=true`. | Reject `outcome=unknown`/`upstream_timeout` in `retryAllowed`, or explicitly document and test the idempotent-import exemption and correct the comment. |
| REL38-07 | medium | **P2** | `internal/storage/operation_ledger.go:539-590`; `cmd/worker/main.go:260-285` | `Resolve` writes `operation_transitions` but no `audit_events`. The original audit stays `outcome_unknown` after reconciliation succeeds/fails, so the security audit never records the final outcome. | Accept an `audit.Event` in `Resolve` and write it in the same transaction (like `FinishCall`), or write a reconciliation audit event; test the resolved state. |
| REL38-08 | low | **P3** | `internal/jobs/jobs_test.go:230-275`; `internal/jobs/jobs.go:179-181` | `TestWorkerRenewsLeaseWhileHandlerRuns` sets `Lease=300ms` but defaults raise it to `TaskTimeout+1m`; the 300ms handler does not outlive the effective lease. The test proves heartbeat renewal, not the claimed "handler outlives one lease period". The implementation passes an independent 400ms/200ms/700ms probe. | Set `Lease > TaskTimeout` and sleep longer than the effective lease (or assert the effective options) so the test exercises expiry/renewal. |
| REL38-09 | low | **P3** | `cmd/worker/main.go:239-246`; `internal/storage/operation_ledger.go:1078-1099` | Escalation is not terminal: `Escalate` sets `unknown` with `reconcile_after=now()+24h`; the next claim sees `reconcile_count > max` and escalates again every 24h. No terminal escalated state or admin surface. | Add a terminal escalated state/flag or stop claiming above the bound; surface to an operator; test a second pass does not re-escalate. |

### 4. P0/P1 vs lower, and what actually blocks a capability claim

- **P0 (1):** REL38-01 — the middleware runs a write on `in_progress`/`retry` nil
  errors. This is a real duplicate-mutation/ledger-divergence path, not a coverage
  gap. It blocks the **REL-02 "UNKNOWN, never retried"** claim and the gate's
  idempotency claim for concurrent/retried calls.
- **P1 (2):** REL38-02 (approval burned on non-proceed) and REL38-03 (no
  pending/executing/reconciling recovery). These block the claim that a crash-safe
  write always either resolves or is recoverable; REL38-03 leaves an operation
  permanently in progress/unresolvable.
- **P2 (4):** REL38-04 (Retry ambiguity), REL38-05 (guard escape), REL38-06
  (worker ambiguity), REL38-07 (reconciler audit). These are edge/defence gaps; they
  do not by themselves prove a current duplicate charge, but they contradict stated
  invariants and must be fixed before citing those invariants.
- **P3 (2):** REL38-08 (test weakness) and REL38-09 (24h re-escalation). Hardening.

**REL-01 (fencing): not broken.** The stale-write and two-worker claims hold and are
revert-proven. REL38-08 is a test-quality issue only. The job-queue fencing layer can
be cited as PASS.

**REL-02 (UNKNOWN, never retried): broken as stated.** The ambiguous-outcome path
itself is correct — `Unknown` records `unknown`, `Begin` returns `operation_unknown`,
and an unknown row is not retried by the normal gate. But the gate executes on
`in_progress`/`retry`, and `Retry`/`retryAllowed` have ambiguity edges. This is not
merely missing coverage; it is a reachable execution path. The release wording must
say REL-02 is **NOT READY** until REL38-01/04/06 are fixed.

**Atomic confirmation+ledger: the transaction is not broken; its use is.** The
injected-failure test proves Consume+Begin and transition+audit roll back together.
REL38-02 is a lifecycle bug: the transaction commits and burns the approval on
decisions that should not proceed. So the atomicity claim can be cited for the
proceed/failure window, but not as "every BeginCall is atomic"; the gate decision
semantics must be fixed.

**Two-path completeness guard: counts are as documented, the escape-proof claim is
not.** 55/50/5 is correct and the current five declarations really self-ledger. The
guard does not prove a declared tool writes an operations row; REL38-05 is a real
escape hatch for a future write tool.

**REL-16-R: real and correctly bounded as documented.** The five self-ledgered tools
write no audit row of their own; the middleware writes the outcome audit after the
tool commit. That is the accepted limitation. REL38-07 (reconciler resolution has no
audit outcome) is a separate audit-completeness gap, not a reclassification of
REL-16-R.

### 5. Revert experiments (verbatim)

**Fencing guard revert.** Removed the `lease_token` predicate from `completeSQL` in a
throwaway copy (kept the parameter count so the failure is the fence, not SQL arity):

```text
=== RUN   TestJobQueueStaleAttemptCannotWrite
    jobqueue_transitions_test.go:134: stale Complete: expected ErrLeaseLost, got nil (a stale write was silently accepted)
--- FAIL: TestJobQueueStaleAttemptCannotWrite (0.43s)
```

**Server gate probes (current bytes, no fix):**

```text
=== RUN   TestT38GateDecisionsAreNotIgnored/in_progress
    t38_probe_test.go:19: decision=in_progress isError=false structured=map[ok:true]
    t38_probe_test.go:21: the tool ran despite a "in_progress" gate decision
=== RUN   TestT38GateDecisionsAreNotIgnored/retry
    t38_probe_test.go:19: decision=retry isError=false structured=map[ok:true]
    t38_probe_test.go:21: the tool ran despite a "retry" gate decision
--- FAIL: TestT38GateDecisionsAreNotIgnored
```

**Ledger probes (current bytes):**

```text
--- FAIL: TestT38BeginCallInProgressReturnsNilError
    t38_probe_test.go:97: second BeginCall while executing: decision=in_progress err=<nil>
    t38_probe_test.go:99: GateInProgress returned with a nil error; the middleware runs the tool on nil error

--- FAIL: TestT38BeginCallBurnsConfirmationOnNonProceed
    t38_probe_test.go:53: second BeginCall on failed row: decision=retry err=<nil> confirmation=consumed
    t38_probe_test.go:55: a non-proceed BeginCall (retry) burned the approval

--- FAIL: TestT38ExpiredExecutingRowIsRecovered
    t38_probe_test.go:34: ClaimReconcilable after executing lease expiry: applied=false rec=<nil> err=<nil> state=executing
    t38_probe_test.go:36: expired executing row is still executing; no recovery path moves it to unknown

--- FAIL: TestT38ReconcilingCrashIsStuck
    t38_probe_test.go:79: after reconciling lease expiry: secondClaim=false rec=<nil> err=<nil> state=reconciling abort=false abortErr=invalid_argument: operation cannot be aborted from that state
    t38_probe_test.go:81: a crashed reconciler leaves the row permanently reconciling and unabortable

--- FAIL: TestT38RetryRejectsAmbiguousFailedRow
    t38_probe_test.go:19: Retry(upstream_timeout): applied=true err=<nil>
    t38_probe_test.go:21: Retry reopened a failed row whose code is ambiguous (upstream_timeout)

--- FAIL: TestT38AmbiguousIdempotentTaskIsRetried
    t38_probe_test.go:16: retryAllowed(ambiguous idempotent) = true
    t38_probe_test.go:18: an ambiguous outcome was retried; the function comment says it must not be
```

**Passing probes (verbatim):**

```text
=== RUN   TestT38FencingStaleAttempt
    t38_fencing_probe_test.go:37: after stale writes: state=running attempts=2 token=0x518f16eec1b0
--- PASS: TestT38FencingStaleAttempt (0.04s)
=== RUN   TestT38TwoWorkersOneJob
    t38_fencing_probe_test.go:73: claims=1
--- PASS: TestT38TwoWorkersOneJob (0.02s)

=== RUN   TestT38HandlerOutlivesOneLeasePeriod
    t38_probe_test.go:45: runs=1 extends=14 completed=1
--- PASS: TestT38HandlerOutlivesOneLeasePeriod (1.20s)

=== RUN   TestT38FinishCallOnForeignExecutingRowLeavesItExecuting
    t38_probe_test.go:123: after FinishCall on foreign executing ticket: state=executing audit_succeeded=1
--- PASS (confirms the audit is written while the ledger remains executing)
```

**Completeness guard counts and escape:**

```text
write tools: 55 total, 50 middleware-gated, 5 self-ledgered
# after adding magento.product.create to selfLedgeredTools:
write tools: 55 total, 49 middleware-gated, 6 self-ledgered
--- PASS (the guard does not catch the escape)
```

### 6. Required commands (verbatim)

```text
$ go build ./...
exit 0

$ MAGE2MCP_TEST_DATABASE_URL='postgres://mage2mcp:mage2mcp-dev@127.0.0.1:55440/mage2mcp_qa?sslmode=disable' \
  go test -count=1 -race ./internal/storage/... ./internal/jobs/... ./internal/mcp/server/... ./cmd/worker/...
ok      github.com/shuaiZend/mage2mcp/internal/storage    4.162s
ok      github.com/shuaiZend/mage2mcp/internal/jobs    2.157s
ok      github.com/shuaiZend/mage2mcp/internal/mcp/server    1.873s
ok      github.com/shuaiZend/mage2mcp/cmd/worker    1.674s
```

Focused atomicity tests:

```text
--- PASS: TestBeginCallConsumesConfirmationAtomically
--- PASS: TestBeginCallRollsBackConfirmationOnLedgerFailure
--- PASS: TestFinishCallWritesAuditInTheSameTransaction
--- PASS: TestFinishCallRollsBackTransitionWhenAuditFails
--- PASS: TestFinishCallRecordsUnknownWithAudit
```

Focused reconciler tests:

```text
--- PASS: TestReconcileHandlerResolvesEachWay
--- PASS: TestReconcileHandlerDefersOnReadFailure
--- PASS: TestReconcileHandlerEscalates
```

### 7. Verdict and release wording

**NEEDS_REVISION. The reliability layer is not ready to be cited as release-safe.**

- **Job queue / fencing (REL-01, REL-03): PASS.** Stale terminal/progress writes
  are fenced, two workers get disjoint claims, lease renewal keeps a long handler to
  one execution, and reverting the fence makes the guard test fail. REL38-08 is a
  test-quality P3.
- **Ledger gate (REL-02): NOT READY.** REL38-01 is a P0 reachable execution path;
  REL38-02/03 are P1 lifecycle/recovery gaps; REL38-04/06 are ambiguity edges.
- **Atomic confirmation+ledger: transaction PASS, decision semantics FAIL.** The
  Consume+Begin and transition+audit transactions are proven atomic under injected
  failure; REL38-02 shows the approval is committed/burned on non-proceed decisions.
- **Reconciler: unknown resolution and escalation PASS; crash recovery and audit
  completeness FAIL (REL38-03/07/09).**
- **Coverage: counts as documented (55/50/5), guard not escape-proof (REL38-05).**
- **REL-16-R: as documented and bounded; the five self-ledgered tools write no audit
  row of their own.**

**Routing for t42/t43.** Repair REL38-01 first (gate decision handling + real-ledger
tests), then REL38-02/03. t43 must reproduce the defect pre-fix, confirm post-fix,
revert the new guard and watch its test fail, and re-bind every hash; do not carry the
hashes above into the post-repair verdict.

## t41 — re-review of t40: MAG-R02 exact reconciliation, MAG-R01 replay, MAG-R03 repeated reads

| Field | Value |
|---|---|
| Review task | `t41` (round 2) |
| Reviewed task | `t40`, author: magento-engineer |
| Reviewer | security-auditor (not the author) |
| Reviewed revision | working tree after t40; hashes pinned in §1 |
| Method | hash-pinned delta reading, independent probes against state-changing fakes, one revert experiment, full suite re-run |
| Verdict | **PASS** — all six t37 findings are addressed; the one remaining risk is a deployment-verification assumption (quote_id must be returned by the instance's order API), and it degrades to UNKNOWN, never to a wrong success. |

### 1. Exact file hashes reviewed (post-t40)

```text
8ff12ddc580aa163620412df59c6d5837db707fd005a8b197824212b093ede57  internal/magento/order_create.go
7617505ad751a071491f27866cce782ec905ac05eaefbeb414c29f4d7f8299f9  internal/magento/invoice_write.go
0972ca50feeacf2d21ec8f93bfee8ec2e84ac70dcddd14f9ac3be4d3ec65e718  internal/magento/order_documents_write.go
33703b32e282960db43e377eb3ec8830432ea82f83d8a089393ba605fdd4358b  internal/magento/order_status_write.go
67d6ef5b16a51d079662e60f65a5d578bd13f15efced39e53e542b2578a4e0db  internal/magento/operations.go
b29c1042b1564661f733a4f500189a5d3af36002a85f7d0324fa05b56795b456  internal/magento/configurable.go
abc19486c237a13390a1d893744ef970024c360dc07b99ef3a8cb5130befdfe0  internal/magento/inventory.go
98186d6e0cc62a7a10a698c56ee562cd8f8fdbb2abfdbaccdd72dafadeeef982  internal/magento/write_tools.go
2153b6c00d2bba02ce8f707585a4c44939154f4440d3bca5af5f48efa09d75f7  internal/magento/order.go
4c8d660d103f1c9bf031ddeecd4298fedea7037f420a66b6ad943d17f8d48597  internal/magento/order_create_test.go
443a4d5429e30f7910cb02623b0ae93d305aab44da36c12bc1f8afa6c6143fa4  internal/magento/invoice_capture_test.go
5fc31f8ce004c493a68a7d04b534376e37f8a384b8b483331d920e90e39a719a  internal/magento/document_write_test.go
755f144bc877c31df1be1309845484ee93769d55a78ee6d2118b71cad68f9092  internal/magento/order_status_write_test.go
```

### 2. Delta since the t37 review, per finding

- **MAG-R02** — `reconcilePlacedOrder` no longer matches on customer + window + total.
  It requests `quote_id` in the search projection (`orderReconcileFields`,
  `order_create.go:295`), selects only candidates whose `QuoteID == cartID`
  (`:253`), reads the matched order back by id, and returns `operation_unknown`
  with `candidate_order_ids` when no candidate carries this cart's quote_id or when
  several do. The `cartTotals` read and the total comparison are gone.
- **MAG-R01** — `ops.Begin` now runs before the terminal-state pre-flight in
  `CaptureInvoice` (`invoice_write.go`), `CreateCreditmemo`
  (`order_documents_write.go`) and `CancelOrder` (`order_status_write.go`). A fresh
  attempt that hits a terminal state closes the row with `run.Fail(conflict)`, so a
  repeat returns the recorded outcome.
- **MAG-R03** — reconciliation repeats the search up to `reconcileReads` (3) with
  `reconcileBackoff` between reads and only records `failed` when every read is
  empty; cancellation requires two agreeing "still open" reads.
- **MAG-R04/R05** — the transfer description now states the cross-replica residual
  (`write_tools.go:1779`) and the `idempotency_key` property now states that the key
  is not caller-scoped and must be unguessable (`write_tools.go:77`).
- **MAG-R06** — the `CreateConfigurableInput.OperationID` comment now says the
  resume state is the upstream read-back and no ledger intent is recorded
  (`configurable.go:92-96`); `TransferResult.FromAfter/ToAfter` are `*int` with
  `omitempty` and are set only when the post-write read succeeded
  (`inventory.go:219-222`).

### 3. Independent reproduction (verbatim probe output)

State-changing fakes (capture → paid; cancel → canceled; refund → qty_refunded
spent), two calls with the same idempotency key:

```text
=== RUN   TestProbeR2ReplayAfterSuccess/capture
    PROBE: second capture replayed=true op=op-1; POSTs=1
=== RUN   TestProbeR2ReplayAfterSuccess/cancel
    PROBE: second cancel replayed=true op=op-1; POSTs=1
=== RUN   TestProbeR2ReplayAfterSuccess/creditmemo
    PROBE: second refund replayed=true op=op-1; POSTs=1
```

Foreign same-total order (quote_id 7777) for the same customer:

```text
PROBE: foreign candidate -> operation_unknown: the order placement outcome is unknown:
Magento did not confirm it, so the operation was recorded as unknown. Do not retry:
poll the operation until it is reconciled (code=operation_unknown); searches=1;
ledger unknown=1 failed=0
```

Own order (quote_id 42) alongside the foreign candidate:

```text
PROBE: own order reconciled=true entity=100 (foreign candidate present)
```

Repeated reads (MAG-R03):

```text
PROBE: empty-then-match reconciled=true entity=100; searches=2
PROBE: all reads empty -> code=upstream_timeout retryable=true searches=3 ledger failed=1 unknown=0
```

### 4. Revert experiment — the new test guards the MAG-R02 fix

The cart-link predicate was changed from `if candidate.QuoteID == cartID` to
`if true`, the author's regression test run, then the file restored:

```text
$ go test -count=1 -run TestCreateOrderReconcileIgnoresForeignMatchingTotal -v ./internal/magento/   # quote_id match disabled
=== RUN   TestCreateOrderReconcileIgnoresForeignMatchingTotal
    order_create_test.go:356: result = &{Order:{EntityID:99 ... GrandTotal:19.98 ...} OperationID:op-1 Replayed:false Reconciled:true}, want no order claimed
--- FAIL: TestCreateOrderReconcileIgnoresForeignMatchingTotal (0.00s)
FAIL    github.com/shuaiZend/mage2mcp/internal/magento    0.424s
```

Restored and re-run: `ok github.com/shuaiZend/mage2mcp/internal/magento 0.191s`; the file
hash is back to `8ff12ddc…`, matching §1.

### 5. Prior acceptance re-confirmation

| Item | Result | Note |
|---|---|---|
| MAG-01 paid invoice: no POST, typed conflict | **PASS** | `TestCaptureInvoice/refuses_an_already_paid_invoice` now asserts `POSTs=0` **and** `ledger begins=1 fails=1`: the rejection is recorded, and a repeat returns it. The no-upstream-call property is unchanged. |
| MAG-01 timeout → UNKNOWN, non-retryable | **PASS** | `TestCaptureInvoiceTimeoutIsUnknown` unchanged and green. |
| MAG-04 retried create places no second order | **PASS** | `TestCreateOrderReplaysSameKey` unchanged and green. |
| MAG-04 ambiguous keeps cart and reconciles | **PASS** | now by exact `quote_id`; probe above. |
| MAG-04 >1 match → UNKNOWN | **PASS** | `TestCreateOrderAmbiguousMultipleMatches`; probe shows a foreign candidate yields `operation_unknown`. |
| MAG-03 over-refund rejected before any upstream call | **PASS** | `POSTs=0` retained; the ledger now records the rejection (`begins=1 fails=1`), which is what makes the repeat honest. |
| MAG-03 minor units | **PASS** | unchanged (12999 / 12998 / 2500 + currency). |
| MAG-02 capture flag | **PASS** | unchanged. |
| MAG-10/11 transfer reporting | **PASS** | unchanged behaviour; `from_after`/`to_after` are now omitted when unobserved, which is stricter. |
| MAG-05/07 configurable resume | **PASS** | comment corrected; behaviour unchanged and green. |
| Revert experiment | **PASS** | §4. |
| Reconciliation false-positive assessment | **PASS** | §3 and §6. |

No test was deleted or weakened: the two assertion changes in the pre-flight tests
(`begins=0` → `begins=1 fails=1`) follow the new ordering, and both keep the decisive
`POSTs == 0` assertion.

### 6. Residual risks

1. **`quote_id` availability (deployment verification, not a code defect).** The exact
   match depends on the instance's `/V1/orders` search returning `quote_id`. If a
   Magento version omits it, every candidate reads `QuoteID == 0`, no order is ever
   tied to the cart, and every ambiguous placement returns `operation_unknown` — a
   fail-safe degradation (a human reconciles) rather than a wrong-entity success. The
   claim "ambiguous placements reconcile automatically" is therefore `CONTRACT` until
   verified against a live instance.
2. **MAG-R04** cross-replica transfer window remains (now stated in the tool
   description); **MAG-R05** keys remain unguessable-not-scoped (now stated).
3. The ledger's `(instance, tool, key)` uniqueness means a replay is still not
   subject-scoped; that is the approved §6.3 shape and is now documented.

### 7. Verdict

**PASS for t40.** All six t37 findings are closed with tests that fail when the fix is
reverted; the two substantive defects (wrong-entity order success, unreachable replay)
are gone and independently reproduced; the prior MAG-01/02/03/04/05/07/10/11 acceptance
items still hold. The only open item is the deployment-verification assumption in §6.1,
which fails safe.

The t37 verdict (**NEEDS_REVISION**) is superseded for the repaired scope. Order
creation can be re-assessed as conditionally ready once §6.1 is verified against a live
Magento; until then the honest label is "conditionally ready, auto-reconciliation
unproven on a live instance".

---

## t43 — review-round-3: reliability repair verification

| Field | Value |
|---|---|
| Review task | `t43` (round 3) |
| Reviewed task | `t42` (repair of t38 findings REL38-01…09), on top of t11/t12/t30 |
| Reviewer | mcp-engineer (not the author) |
| Reviewed revision | working tree at `66786a5`; hashes below |
| Method | fresh PostgreSQL probes in `/tmp/mage2mcp-t43`, pre/post-fix reproduction, one P0 revert, DB race suite, source inspection; authors' tests not trusted |
| Verdict | **NEEDS_REVISION** — the t38 P0 is fixed and revert-proven, but a retry now leaves the ledger in `executing` after a successful tool call (P1), and recovery/abort transitions still write no outcome audit (P2). |

### 1. Exact file hashes reviewed

```text
47c580ffba17d125daac5aed1c6bd72ded74f3617062f31b9068f97c27634b67  internal/jobs/jobs.go
c6df34d916b1026cbf3def9a954e2d2aeb3ca6123dc5a4742094f7d52504f2bd  internal/storage/jobqueue.go
a9e29264d7d919cd96f91d543eb745d92c9f609f38d9e5a5d22fc8ca0263fd24  internal/storage/migrations/000005_job_fencing.up.sql
7ba4d76fe969c4bf8f3eb22e33cfa5f77dfbdb3a4db268992783556ec78f5c05  internal/storage/operation_ledger.go
9cf17b4a20b1fceee1eaa3c16dce8600d4bf21b00a400b326dd1e418601f518f  internal/storage/migrations/000006_operation_ledger.up.sql
483fc2a9a0695e57d5c039fd06fad77723f13454396364b2232bb0ba9556b669  internal/storage/confirm.go
1aaab7937ee0094f9d39145f654302e282f261c34e68de693965d7dd2aaab808  internal/storage/audit.go
0ff344998a85bd52300a2d4abe52f7011ff12ba4a03ca768a045f5021eca3516  cmd/worker/main.go
bb344c0cf89f4ed08c48bedc16a8c611f5e8f81eee8e3c58deb41423a1a7396d  internal/mcp/server/server.go
0eac569f15d30a2c0bed4487f4a8404a24a1c5d9b8b2faf982cb57c7009f93b6  internal/magento/client.go
40205d864f8ea83cc3de91c60fef7ee9e70d9c1ed7b87a56d4b1bede97625624  cmd/server/main.go
f232954883b79b34f9352ad5d355e975672761092f9fba54c7b471c284052a40  cmd/server/audit_decision_test.go
4940a1b09d039a3d15deba87947b8f8007fc908c04f825abe42e198ac41c527a  internal/jobs/jobs_test.go
```

### 2. What t42 fixed (independently reproduced)

**REL38-01 gate decisions (P0) — fixed and revert-proven.** The middleware now
switches on every `Decision` and only `proceed` runs the tool; `replay` returns the
recorded result; `retry`/`in_progress` return conflicts; `unknown` returns
`operation_unknown`; an unrecognised decision fails closed. `TestEveryOperationDecisionIsHandled`
passes all six cases with a counting handler (only `proceed` calls it). Reverting the
switch to the old "only replay" shape in a throwaway copy produced:

```text
--- FAIL: TestEveryOperationDecisionIsHandled/retry
    server_test.go:1571: UPSTREAM CALLED 1 times for decision "retry"; only proceed may run the tool
--- FAIL: TestEveryOperationDecisionIsHandled/in_progress
    server_test.go:1571: UPSTREAM CALLED 1 times for decision "in_progress"; only proceed may run the tool
--- FAIL: TestEveryOperationDecisionIsHandled/unknown
    server_test.go:1571: UPSTREAM CALLED 1 times for decision "unknown"; only proceed may run the tool
--- FAIL: TestEveryOperationDecisionIsHandled/something-new
    server_test.go:1571: UPSTREAM CALLED 1 times for decision "something-new"; only proceed may run the tool
```

**REL38-02 approval lifecycle — fixed.** `BeginCall` decides before consuming:
`GateReplay` returns without consuming; `GateInProgress`/`GateUnknown` return typed
errors and roll back; `GateProceed` consumes; `GateRetry` does T11+T2 in the same
transaction and returns a fenced `proceed` with attempt 2. Probe:
`TestT43BeginCallNonProceedDoesNotConsume` → in_progress/unknown/replay all left the
approval `approved`; retry test `TestBeginCallRetryClaimsTheOperation` → `proceed`,
attempt 2, approval consumed.

**REL38-03 crash recovery — fixed for the tested windows.** `RecoverExpired` recovers
executing+expired → unknown/`lease_expired`, reconciling+expired →
unknown/`reconcile_lease_expired`, stale pending → failed/`abandoned_pending`, in one
bounded `SKIP LOCKED` statement. Probe `TestT43RecoverExpired` → recovered=3, states as
above; `Abort` now accepts reconciling (`TestAbortFromReconciling` passes).

**REL38-04 ambiguous Retry — fixed.** `Retry` refuses unknown/ambiguous/upstream_timeout
in Go before the update, with the SQL predicate as a second guard. Probe
`TestT43RetryAmbiguous` → `applied=false`, `conflict: the previous outcome is ambiguous`.

**REL38-05 completeness guard — strengthened, but not escape-proof (see REL43-03).**
Counts are now pinned: `55 write / 50 gated / 5 self-ledgered`, and
`TestSelfLedgeredToolsAreBackedByCode` scans `internal/magento` for a quoted tool name
plus `configuredOperations`. A new write tool not placed on a path changes the counts
and fails. However, the scan is file-level, not function-level; see REL43-03.

**REL38-06 worker ambiguity — fixed.** `retryAllowed` now calls `ambiguousOutcome`
(outcome detail, upstream_timeout, operation_unknown). Probe
`TestT43AmbiguousRetryDenied` → `retryAllowed=false`.

**REL38-07 reconciler audit — fixed for `Resolve`.** `Resolve` takes an
`audit.Event` and writes it in the same transaction as the transition;
`TestResolveWritesAuditInTheSameTransaction` passes and `cmd/worker` passes a
`reconcileAudit` event for succeeded/failed/inconclusive. Recovery and Abort remain
unaudited (REL43-02).

**REL38-08 lease test — partially improved, still weak.** The test now asserts the
effective lease exceeds the task timeout, but the handler sleeps 120ms while the
effective lease is 300ms; it never outlives one lease period. Independent probe
`TestT43HandlerOutlivesOneLeasePeriod` (Lease=400ms, TaskTimeout=200ms, handler=700ms)
passes: runs=1, extends=14, completed=1. The behavior is correct; the author test still
does not guard the claimed property (REL43-04).

**REL-16-R — unchanged and as documented.** No audit writes exist under
`internal/magento`; the five self-ledgered tools still have their outcome audit written
by the middleware after commit.

### 3. New findings

| ID | Severity | P-ladder | File:line | Problem | Required fix |
|---|---|---|---|---|---|
| REL43-01 | **high** | **P1** | `internal/storage/operation_ledger.go:1078-1098,1130-1157` | `BeginCall` GateRetry reopens and claims the row to `executing` and returns `proceed`; `FinishCall` then sees `state != pending`, takes the tool-managed branch, writes only the audit row and leaves the ledger `executing`. Reproduced: after a successful retry, `FinishCall` left state=`executing`, attempt=2, finished=nil while writing an audit `allow`. The retry outcome is never recorded; a later call conflicts until recovery, and replay is unavailable. This is a partial write (audit success, ledger non-terminal). | Make the middleware ticket carry its ownership (or use `ticket.Decision == proceed`) and let `FinishCall` transition `executing` with the matching fence, or have `BeginCall` retry return a pending reservation that `FinishCall` claims. Add tests for retry→succeeded, retry→failed and retry→unknown with audit atomicity. |
| REL43-02 | medium | **P2** | `internal/storage/operation_ledger.go:502-559,671-708` | `RecoverExpired` and `Abort` change ledger state but write no `audit_events` outcome. A crash-recovered or operator-aborted operation has only its intent audit row (or none if the crash preceded the outcome); the audit trail cannot show the final state. Probe `TestT43RecoveryAndAbortHaveOutcomeAudit` → outcome rows after recovery=0, after abort=0. | Accept an `audit.Event` (system for recovery, operator identity for abort) and write it in the same transaction as the transition; test both paths. |
| REL43-03 | low | **P3** | `cmd/server/audit_decision_test.go:102-134`; `cmd/server/main.go:505-513` | `selfLedgeredEvidence` is file-level: a quoted tool name in any `internal/magento` file that also contains `configuredOperations` satisfies it. Constructed escape: added `magento.product.create` to `selfLedgeredTools`, a quoted comment to `invoice_write.go`, and updated pinned counts to 49/6; both `TestEveryWriteToolIsGatedOrDeclaredSelfLedgered` and `TestSelfLedgeredToolsAreBackedByCode` passed. The guard is registry-derived and pinned, but not a function-level proof. | Parse the AST for a `configuredOperations` call in the function that handles the named tool, or add a per-tool runtime/integration test proving an operations row is created/transitioned. |
| REL43-04 | low | **P3** | `internal/jobs/jobs_test.go:230-275` | The repaired test asserts `Lease > TaskTimeout` but the handler sleeps 120ms against an effective lease of 300ms; it never exceeds one lease period. Independent 400/200/700ms probe passes, so this is a test-quality gap, not a production defect. | Sleep longer than the effective lease (or set the lease explicitly) so the test exercises expiry/renewal; keep the exactly-once assertion. |
| REL43-05 | low | **P3** | `cmd/worker/main.go:248-260`; `internal/storage/operation_ledger.go:1078-1099` | Escalation is not terminal: `Escalate` sets `unknown` with `reconcile_after=now()+24h`; the next claim sees `reconcile_count > max` and escalates again every 24h. No terminal escalated state or operator surface. | Add a terminal escalated state/flag or stop claiming above the bound; surface to an operator; test a second pass does not re-escalate. |

### 4. P0/P1 vs lower, and release wording

- **P0:** none remaining. The t38 P0 (REL38-01) is fixed and the guarding test fails when
  the switch is reverted.
- **P1 (1):** REL43-01 — retry outcome is not recorded; audit and ledger disagree.
  This blocks the claim that a retry is fully resolved.
- **P2 (1):** REL43-02 — recovery/abort transitions are not audited.
- **P3 (3):** REL43-03 (guard file-level escape), REL43-04 (lease test weakness),
  REL43-05 (24h re-escalation).

**REL-01 fencing: PASS.** Stale Complete/Fail/Extend/SetResult are zero/conflict; two
workers get one claim; independent probe pass.

**REL-03 lease: PASS.** Independent 400ms lease / 200ms timeout / 700ms handler ran
once, renewed 14 times, completed once. The author test is weak (REL43-04).

**REL-02 UNKNOWN/never retried: PASS for the gate and retry eligibility.** Ambiguous
outcomes become `unknown`; `Begin`/`BeginCall`/`Retry` refuse them; retry is permitted
only from a non-ambiguous failed row. The new P1 is the **outcome recording** of that
retry, not the retry gate itself.

**REL-16/SEC-06: PASS for middleware-gated tools; REL-16-R as documented.** Consume+Begin
and transition+audit are atomic under injected failure; non-proceed decisions do not
consume; the five self-ledgered tools still have the documented post-commit audit gap.
Recovery/abort audit is the separate P2.

**Ledger gate counts: PASS (55/50/5); escape-proof claim: FAIL (REL43-03).** The guard
is registry-derived and pinned, but a file-level quoted comment plus a count edit is
enough to declare a non-self-ledgered tool; a new write tool is otherwise gated.

**Reconciler: PASS for unknown resolution both ways and bound escalation; recovery
audit FAIL (REL43-02); escalation remains a 24h loop (REL43-05).**

**Atomic confirmation+ledger: PASS.** Injected failure after Consume rolls back both;
non-proceed decisions leave the approval approved; retry consumes only because it will
execute. The P1 above is the post-execution `FinishCall` transition, not the
confirmation transaction.

### 5. Verdict

**NEEDS_REVISION.** The t42 repair closes the t38 P0 and the gate/retry/atomicity
claims are now real and revert-proven. It is not release-ready because every retry that
reaches `proceed` leaves the ledger `executing` after `FinishCall` (REL43-01), and
recovery/abort still do not write outcome audit (REL43-02). The job-queue/fencing layer
(REL-01/REL-03) can be cited as PASS; the ledger gate decisions are PASS; the ledger
**outcome** and recovery-audit layer is not.

Repair order: REL43-01 first (retry→FinishCall with real-PostgreSQL tests), then
REL43-02; REL43-03/04/05 are hardening. t44 must reproduce the retry defect pre-fix,
confirm post-fix, revert the new guard and watch its test fail, and re-bind every hash.

## t39 — final MCP, configuration, and performance review: write gate, platform.info, schemas, timeouts, import ceiling

- **Task:** t39 (review round 1), attempt `217e4a21-d495-4dcc-9ce6-dab46f8418b5`.
- **Reviewed workstreams:** t18 (write disclosure, error envelopes, generated inventory),
  t19 (write gate, platform.info, schema compilation, timeouts, duplicate resolution,
  envelope delegation), t20 (shipped config and quickstart), t17/t27 (transport pooling,
  truncation, analysis caps, restored 8 MiB import ceiling).
- **Tree:** frozen remediation working tree at 2026-11-20, HEAD `66786a5` plus the
  uncommitted remediation. No source edits were made by this review; every temporary
  adversarial edit was reverted and the reviewed hashes below were re-checked after.
- **Independence:** t20 was authored by this reviewer (docs-engineer). Its config and
  quickstart checks are mechanical re-verification, not an independent approval. t18,
  t19, t17 and t27 were authored by others.
- **Verdict:** **NEEDS_REVISION** (two P1 documentation findings, four P2, four P3; no
  P0 and no code-level write-gate or import-ceiling defect).

### 1. Exact file hashes reviewed

Primary files named by the review contract (sha256, before review):

| File | sha256 |
|---|---|
| `internal/magento/write_tools.go` | `98186d6e0cc62a7a10a698c56ee562cd8f8fdbb2abfdbaccdd72dafadeeef982` |
| `internal/mcp/toolerr/toolerr.go` | `79f0de76046b42ff253f689072b2c9019c1701b7de657e65907391cf335f8189` |
| `internal/mcp/toolerr/toolerr_test.go` | `b4b88cbc3c2cb739782487046a6d8b4b31bf718d50618051511074c22a54925a` |
| `docs/implemented-tools.md` | `0f09ce83ed3e44b6435b0397403af36896d4ebb75db4ad7238c9da6ed61e9fb8` |
| `scripts/check-contracts.mjs` | `67ea6b46bd797790babf89cf96b7f5491fc20a2d717a05c679b8afb4787ab202` |
| `internal/mcp/server/server.go` | `bb344c0cf89f4ed08c48bedc16a8c611f5e8f81eee8e3c58deb41423a1a7396d` |
| `internal/mcp/server/server_test.go` | `186e2bcd758ea2ecba5701c439a0c36bc5b01df3be944db793e3946601d9044d` |
| `internal/mcp/registry/registry.go` | `ea3c33e50bc909f2d9f54c9424a1e604e36a15231a66928df1d6bb52f4003cb6` |
| `internal/mcp/registry/registry_test.go` | `5d5ef1ce606f7abc10be1b55875b612d7af6d5c5b224d95b1c37579e7990885a` |
| `cmd/server/main.go` | `1d2ebed05ae6d864b5b24b54f9f5e2aefb664c2d03ab0c4e5cdbbf8bc6b5f51b` |
| `internal/ga4/tools.go` | `0416fe294e736d5d338b7f5edb75ade4466172bd738b0823595007f6920bbffd` |
| `internal/core/config/config.go` | `a5724e44bdef11b4e6025d48857f4b9f617795dcfbd38e58967fc6646d6c6a6e` |
| `internal/core/config/config_test.go` | `1bd68dd1e05715a7e710daa4b3f4e1ef4dee7a2eeaadd815d0bc751b2f15f9bb` |
| `docs/configuration.md` | `bc4fea969dd1548f526e469c1b2152b778639be65be9ef3c926dfe8e63dc0d46` |
| `configs/config.example.yaml` | `3f5263926b4a783d3eb4813ae7b67eacb89aad0b313ad62bc34340ff72a97f7a` |
| `configs/config.production.template.yaml` | `a24f3334272f526cfd80ce8dc11d628437330119c7d3ef91e1afb6f3440a1c1d` |
| `configs/config.docker.yaml` | `dbd4ba8a60b635ccc8d6d39112fd8e27fd3795fbb760d6cca58455522daeb2db` |
| `internal/magento/client.go` | `0eac569f15d30a2c0bed4487f4a8404a24a1c5d9b8b2faf982cb57c7009f93b6` |
| `internal/magento/client_test.go` | `9ae4d5acee413e9caa029f7bdf887dae246a682cd02a5fe6d3c97f4f8b170494` |
| `internal/magento/sales_analysis.go` | `809bd3044a1bceeb322f491f9b6dfdadf77585fd2fcd5a2c99c9e5ee153f4e98` |
| `internal/magento/order_geography.go` | `bf5aeea96ff1217c82c21141a11b7322f1d4d2c6f23a695e2c4bd1dc7ac32569` |
| `internal/magento/import.go` | `1f2894ecbc6285c9b748f5242785745974cba356c6c5716b41138dcc2493182e` |
| `internal/magento/import_test.go` | `f11231296fa5c1097312f95628780b16df588ba336179af86b90ddd552be7f39` |

Additional files inspected for the findings (sha256):

| File | sha256 |
|---|---|
| `scripts/toolinventory/inventory.go` | `5a56dd73bba599088fc9c12e5a3b8ddab9a0fbd12f31a82d63ef02aa0f5ca51c` |
| `scripts/toolinventory/inventory_test.go` | `3c31650b3459b0484f7b0ffcaab4e0b0d31bcc245f4d1a8c26612ff149dc9646` |
| `scripts/tool-contract/write_disclosure_test.go` | `3c418e760091819175e8fbca338c90eaad861cb125674ba673ee936412d418a1` |
| `scripts/gen-implemented-tools/main.go` | `e503712b787025bbeb39b0d5ec31b646d5365a205cf61b381fcd1d2601167208` |
| `internal/magento/tools.go` | `730238ab6d3a60b290bbba50d172de4d388be07bb6f3ffc09fa0198f0c91a598` |
| `internal/magento/import_jobs.go` | `219f734370c6d715ba2fbe33fa322d55dff3136b12b312cf08d87307a9a399a7` |
| `docs/mcp-tools.md` | `85d482cd2f56a90f7f468af0fbbb735b7738a04ecdc8fe15cdcdad7bba817045` |
| `docs/mcp-conformance.md` | `31431a7385691a178bec6a27198487947e361e520a804643970c1ee642793311` |
| `docs/architecture.md` | `9f6c0e74d22e11afbaa942cf26c0e955b62e1b4c81d37f1fd2ea6456cc59aa55` |
| `docs/operations/deployment.md` | `652f2b2ab31b44743019aa4662e00df26d53616745d7824eec08ff2bb604f1ed` |
| `README.md` | `beaaa96661ba2788b61ccb24b29ab3daa5803233dcb8f6a7f46fd14ac53af1a5` |
| `README.zh-CN.md` | `cade559d27a93a34da4216cb0dcaaaadba89cba2ec8eac117ada9fc60f05be5e` |
| `docs/compatibility.md` | `69c6588dc4bde485844135fffc7935a804fc91b859b5f1f29b11cb310932676c` |
| `docs/unified-analytics.md` | `a3907789ea7d02e93f6f1ffcf3332f1ebf2461842986b3431e059ea0d0c7d0b5` |
| `docs/development/research-baseline.md` | `f1b838348e0d4bea475cfd41979a2c04d5b609e19b33809b6fc85b7f51985e6c` |

The primary hashes were re-checked after the review and are unchanged. In particular
`internal/magento/import.go` returned to `1f2894ec...` and
`docs/implemented-tools.md` to `0f09ce83...` after the revert experiments.

### 2. Reproduced evidence (verbatim)

**Build and package tests.** `go build ./...` exit 0. The contract test set exit 0:

```text
ok  github.com/shuaiZend/mage2mcp/internal/mcp/registry       0.106s
ok  github.com/shuaiZend/mage2mcp/internal/mcp/server         0.221s
ok  github.com/shuaiZend/mage2mcp/internal/mcp/toolerr        0.098s
ok  github.com/shuaiZend/mage2mcp/cmd/server                   0.293s
ok  github.com/shuaiZend/mage2mcp/internal/core/config         0.114s
ok  github.com/shuaiZend/mage2mcp/internal/magento             2.381s
ok  github.com/shuaiZend/mage2mcp/scripts/tool-contract        0.220s
ok  github.com/shuaiZend/mage2mcp/scripts/toolinventory        0.206s
```

`go test -race -count=1 ./internal/mcp/... ./cmd/server/...` exit 0 (registry 1.125s,
server 1.716s, toolerr 1.172s, cmd/server 1.760s).

**Write gate and platform.info, through the real `registerModules`.** An overlay test
built the registry with the same function `cmd/server` uses, with a fake queue/batches,
then called `platform.info` through an in-memory MCP session:

```text
REVIEW read-only no db no bindings       registry=53  writes=false
REVIEW read-only with db no bindings     registry=56  writes=false
REVIEW writes no db no bindings          registry=105 writes=true
REVIEW maximal writes+db+bindings        registry=112 writes=true
```

In the read-only+DB case the registry contains **zero non-read tools**; the three
`magento.import.status/.batches/.batch` read tools are present, and
`import.start/.cancel/.resume` are absent. `RegisterImportJobs` called with writes
disabled returns an error (`TestReadOnlyConfigurationRegistersNoWriteTools`). The
platform.info result listed itself, its `tools` length equalled the registry length,
`writes_enabled` matched `Registry.HasWrites()`, and `protocol_versions` matched
`mcp.SupportedProtocolVersions()`:

```text
protocol_versions=[2026-07-28 2025-11-25 2025-06-18 2025-03-26 2024-11-05]
```

**Every registered schema is closed.** An overlay listed all 112 tools from the maximal
server and called `toolerr.ValidateSchema` on each:

```text
REVIEW schemas validated=112 registry=112
```

`TestNewMCPRejectsOpenSchema` still fails startup with an `additionalProperties`
rejection when a `toolerr` tool is open.

**Timeout.** `TestDeclaredTimeoutFires` passes: a handler that waits on `ctx.Done()`
returns a typed `upstream_timeout` with "declared timeout". Independent probe with a
handler that ignores context and sleeps 250ms against a 20ms declared timeout:

```text
REVIEW ignoring-handler elapsed=252.001125ms isError=true err=<nil>
```

This is cooperative cancellation, not an early return while the handler keeps running:
the middleware waits for the handler and then classifies the deadline. It is recorded as
an observation, not a finding; all shipped I/O handlers pass the context to
`http.NewRequestWithContext`.

**Envelopes.** `TestDeniedAndHandlerEnvelopesMatch` passes: denied and handler errors
have identical `{code,message,op,details}` maps and the text contains
`permission_denied` exactly once.

**Write disclosure.** `TestWriteToolDescriptionsDiscloseSideEffects` passes for 55
write tools across both the registry metadata and a real MCP session; every description
contains "write operation", "Mutates:", "idempotent" and "confirmation".

**Import ceiling.** Verbatim from the tests:

```text
import ceiling=8388608 base64=11184812 required_body=11188908 default_body_cap=16777216 headroom=5588308
realistic import rows=5000 csv_bytes=1698942 base64_bytes=2265256 request_body_bytes=2265413 cap=16777216 http_status=200
```

`TestHTTPBodyCapRejectsOversizeRequest` sends 16 MiB + 1 byte and gets `413` naming
`16777216`. A temporary cut to `maxImportBytes = 512 << 10` failed the drift test:

```text
--- FAIL: TestImportLimitsAgreeAcrossPackages (0.00s)
    import_test.go:439: maxImportBytes = 524288, want the documented ceiling 8388608 (a capability cut is not a fix)
```

**Inventory drift.** A temporary change to one tool description made the staleness test
fail:

```text
--- FAIL: TestGeneratedDocumentIsCurrent (0.01s)
    inventory_test.go:77: docs/implemented-tools.md is stale; run `go run ./scripts/gen-implemented-tools -write`
```

The description was restored and `TestGeneratedDocumentIsCurrent` passes again.

**Quickstart.** Built `cmd/server`, copied `configs/config.example.yaml`, started it:

```text
healthz={"status":"ok"}
readyz={"status":"ready","tools":1}
healthcheck_exit=0
```

This is the decisive contradiction with the README's `"tools":0` claim (F3).

### 3. Findings

| ID | Severity | P-ladder | File:line | Problem | Required fix |
|---|---|---|---|---|---|
| MCP39-01 | **high** | **P1** | `docs/implemented-tools.md:34`; `scripts/toolinventory/inventory.go:245-251,316`; `scripts/toolinventory/inventory_test.go:49` | The generated inventory says `import.start/.cancel/.resume` are gated only by `database.url` and that a database-enabled read-only deployment still registers them. False: `cmd/server/main.go:305-310` calls `RegisterImportReadTools` when writes are false, registering only status/batches/batch. Real read-only+DB registry: 56 tools, zero non-read. The generator and its test encode the wrong gate model. | Split import read vs write gates; write import tools require both `database.url` and `tools.writes`; replace the note; add a generator test for read-only+DB through the real registration path; regenerate the doc. |
| MCP39-02 | **high** | **P1** | `docs/mcp-tools.md:202` | The conditional-count table says read-only+DB is 49 Magento / 59 total; the real registry is 46 Magento / 56 total (43 read + 3 read import; 3 Cloudflare + 6 GA4 + platform.info). The row overstates the surface by three write tools. | Correct the row to 46/56 and pin the read-only+DB count/permissions in a test. |
| MCP39-03 | medium | **P2** | `README.md:167`; `README.zh-CN.md:140`; `docs/architecture.md:177-179`; `docs/operations/deployment.md:91` | All four say platform.info is outside the registry and a fresh start reports `"tools":0`. t19 made it a first-class registry entry; the shipped quickstart reports `"tools":1`. | Update the four docs; keep the point that data-source tools appear only when enabled. |
| MCP39-04 | medium | **P2** | `internal/mcp/server/server_test.go:154` | `TestReadyzReportsWithoutBusinessTools` passes a different `registry.New()` to `Handler` and asserts only HTTP 200, so it cannot detect the readyz count or the README drift. | Use the same registry, decode the JSON, assert `tools == 1` and platform.info is the sole entry. |
| MCP39-05 | medium | **P2** | `docs/implemented-tools.md:15-16` | The preamble says "one instance per module" and "the value does not select credentials yet". Per-instance routing is implemented (ADR-0018; `registerModules` builds a resolver and `instance` selects it). The generator preserves the preamble, so staleness testing cannot catch it. | Replace with the as-built model; note the unified-binding limitation separately. |
| MCP39-06 | medium | **P2** | `internal/mcp/registry/registry.go:108,160`; `internal/mcp/toolerr/toolerr.go:32,79` | Schema closure is enforced only in `toolerr.Add`; `registry.Validate` has no schema. A tool whose `Register` calls `mcp.AddTool` directly can ship an open/invalid schema without failing startup. No committed test enumerates the real registry's schemas (the 112/112 check was an overlay). | Carry `InputSchema` on `registry.Tool` and validate it in `Validate`, or commit an enumeration test over the maximal server calling `toolerr.ValidateSchema`. |
| MCP39-07 | low | **P3** | `README.md:287`; `docs/compatibility.md:12`; `docs/mcp-conformance.md:78` | The docs say only `2025-11-25` and `2026-07-28`; platform.info truthfully reports the full SDK list of five versions, so older clients are accepted contrary to the docs. | Document all SDK-supported versions, or filter the output if only two are intended; update the conformance P1 evidence. |
| MCP39-08 | low | **P3** | `configs/config.example.yaml:70`; `configs/config.production.template.yaml:71`; `docs/configuration.md:212` | The real GA4 property id `354542598` remains in copyable config examples and the reference (also `docs/unified-analytics.md:93` and `docs/development/research-baseline.md:47`). DOC-18 removed the account id from the integration doc but not the property id from examples. | Use `<ga4-property-id>` or `123456` in config examples/reference; keep the real id only in dated evidence. |
| MCP39-09 | low | **P3** | `internal/magento/import_test.go:494` | PERF-12 is proven by arithmetic (11,188,908 needed vs 16,777,216 cap) and a 1.7 MB CSV / 2.27 MB body test. No test sends a decoded payload at/near 8 MiB through HTTP, so an envelope/escaping regression could break the documented maximum while tests stay green. | Add a near-ceiling HTTP test that asserts not 413; keep the cap+1 413 test. |
| MCP39-10 | low | **P3** | `cmd/server/main.go:207-222` | `configuredInstances` includes instances of disabled modules; platform.info reports `magento:["store-us"]` while no Magento tools exist (observed tools=10). No enabled/available flag. | Filter to enabled modules or add an enabled map to InfoOutput; test a disabled module with instances. |

### 4. Revert experiments

1. **Inventory drift guard.** Changed one tool description in
   `internal/magento/tools.go`, ran `go test ./scripts/toolinventory -run
   TestGeneratedDocumentIsCurrent`: FAIL, "docs/implemented-tools.md is stale". Restored
   the file; test passes; `docs/implemented-tools.md` hash still `0f09ce83...`.
2. **Import capability guard.** Changed `maxImportBytes = 8 << 20` to `512 << 10`, ran
   `go test ./internal/magento -run TestImportLimitsAgreeAcrossPackages`: FAIL, "want
   the documented ceiling 8388608 (a capability cut is not a fix)". Restored
   `import.go`; hash still `1f2894ec...`.

No other adversarial edits were left in the tree. The overlay tests used for the write
gate, platform.info, schema enumeration and timeout probes live only under `/tmp` and
are not part of the repository.

### 5. P0/P1 vs lower, and what blocks a capability claim

| Claim | Code status | Findings that touch it | Does anything block the code claim? |
|---|---|---|---|
| **MCP-03** write gate | PASS. Real read-only+DB registry has 56 tools and zero non-read; registry rejects non-read while the gate is false; `RegisterImportJobs` with writes disabled errors. | MCP39-01, MCP39-02 (both P1, **documentation only**). | **No code blocker.** The gate is correct. Do not publish the generated inventory's read-only claim until MCP39-01/02 are fixed; the docs currently say the opposite of the code. |
| **MCP-02** platform.info truthfulness | PASS in four configurations: self-listing, `tools` length, `writes_enabled`, `protocol_versions`, instances. | MCP39-03/04 (P2, stale docs/test), MCP39-07 (P3 protocol wording), MCP39-10 (P3 disabled-module instances). | **No blocker to the code claim.** Docs are stale and the disabled-module edge is misleading; fix before release notes repeat `tools:0`. |
| **MCP-04** schema compilation | PASS today: 112/112 registered schemas validate closed/resolvable; open schema via `toolerr` fails startup. | MCP39-06 (P2, structural/test gap). | **No current schema defect.** The guarantee is not structural: a direct `mcp.AddTool` can bypass startup validation. State the claim as "all 112 registered schemas are closed and tested" or fix the registry enforcement. |
| **MCP-05** timeout enforcement | PASS: ctx-respecting handler cancels at the deadline and returns typed `upstream_timeout`. | None. The ctx-ignoring 252ms probe is an observation, not a finding. | **No blocker.** The declared timeout fires and is typed. It is cooperative, not a hard wall-clock kill; do not claim otherwise. |
| **PERF-12** import ceiling | PASS: 8 MiB decoded, 11,184,812 base64, 11,188,908-byte envelope vs 16,777,216 cap; 512 KiB cut fails the guard; one byte over the body cap is 413. | MCP39-09 (P3, missing near-ceiling HTTP test). | **No blocker.** The capability is restored and guarded; the test gap is hardening. |

**P0:** none.

**P1 (2):** MCP39-01 and MCP39-02. Both are documentation/generator-level, not code
gate failures. They are P1 because they misstate the exposed write surface for the exact
MCP-03 configuration; the release report must not cite the generated inventory's
read-only claim until they are fixed.

**P2 (4):** MCP39-03/04 (stale platform.info/readyz docs and test), MCP39-05 (stale
one-instance preamble), MCP39-06 (schema-closure enforcement is toolerr-only).

**P3 (4):** MCP39-07 (protocol-version wording), MCP39-08 (real GA4 property id in
examples), MCP39-09 (near-ceiling HTTP test), MCP39-10 (disabled-module instances).

**Scope routing.** No finding is in `internal/magento/client.go`; the pooling and
import-ceiling code reviewed there is clean. MCP39-08 is config-example and docs
content, not the config layer in `internal/core/config`; route it to docs-engineer if
mcp-engineer's t49 scope excludes `configs/` and `docs/`. MCP39-09 is a test-only change
in `internal/magento/import_test.go`. MCP39-06 is registry/server, MCP39-10 is
`cmd/server`, MCP39-01/02 are scripts+docs, MCP39-03/04/05/07 are docs/tests.

### 6. Required commands (verbatim)

```bash
go build ./...
go test -count=1 ./internal/mcp/... ./cmd/server/... ./internal/core/config/... ./internal/magento/... ./scripts/...
go test -race -count=1 ./internal/mcp/... ./cmd/server/...
go test -count=1 -v ./internal/magento -run 'TestImportLimitsAgreeAcrossPackages|TestRealisticImportPayloadPassesBothLimits|TestHTTPBodyCapRejectsOversizeRequest'
go test -count=1 -v ./internal/mcp/server -run 'TestReadOnlyConfigurationRegistersNoWriteTools|TestPlatformInfoReportsRegistryTruth|TestNewMCPRejectsOpenSchema|TestDeclaredTimeoutFires|TestDeniedAndHandlerEnvelopesMatch|TestReadyzReportsWithoutBusinessTools'
go test -count=1 ./scripts/toolinventory ./scripts/tool-contract
```

All exited 0 in the final tree. The adversarial runs that must fail were run
separately and restored (section 4).

### 7. Verdict and release wording

**NEEDS_REVISION.** The MCP mechanics are real: the write gate is registry-enforced and
the read-only+DB registry has no non-read tools; platform.info is a truthful registry
tool in four configurations; all 112 schemas are closed; the timeout is typed and
cancels ctx-respecting work; the import ceiling is restored and guarded. No P0 and no
code-level blocker exists in MCP-03, MCP-02, MCP-04, MCP-05 or PERF-12.

The release report cannot yet cite the generated inventory or the quickstart's readyz
line as written: MCP39-01/02 (P1) say a read-only+DB deployment registers the three
write import tools, which the code does not, and MCP39-03/04 (P2) still say platform.info
is outside the registry and `/readyz` reports `tools:0` while it reports `tools:1`.
These are documentation/test fixes, not gate failures. Fix MCP39-01/02 and regenerate
the inventory first, then MCP39-03/04/05; MCP39-06 is the main structural hardening.
MCP39-07/08/09/10 are release-note polish and edge hardening.

For t50: reproduce the pre-fix read-only+DB count and the `tools:0` doc claim, confirm
the post-fix generator/doc/readyz test, revert the new gate/guard and show the relevant
test fails, and re-bind every hash above to the post-t49 tree.

## t50 — re-review of t49: write gate, platform.info, schemas, timeouts, import ceiling

- **Task:** t50 (review round 2), attempt `6e569209-fa9f-40e7-974a-5151d3b20565`.
- **Reviewed:** t49 repair of the t39 findings F1–F10, on the frozen remediation tree.
- **Verdict:** **PASS**, with two non-blocking findings (one P2 schema-guard wiring/test gap, one P3 doc wording) and the already-disclosed out-of-scope GA4 id.
- **Independence:** t20/t47 docs were authored by this reviewer; the config/doc checks below are mechanical re-verification, not independent approval. t18/t19/t49 were authored by others.

### 1. Hashes reviewed (post-t49)

| File | sha256 |
|---|---|
| `internal/magento/write_tools.go` | `98186d6e0cc62a7a10a698c56ee562cd8f8fdbb2abfdbaccdd72dafadeeef982` |
| `internal/mcp/toolerr/toolerr.go` | `79f0de76046b42ff253f689072b2c9019c1701b7de657e65907391cf335f8189` |
| `internal/mcp/toolerr/toolerr_test.go` | `b4b88cbc3c2cb739782487046a6d8b4b31bf718d50618051511074c22a54925a` |
| `docs/implemented-tools.md` | `e803b47236589b31efea644b4b26ca8672fa5431f64698f0ff94a892c5a54339` |
| `scripts/toolinventory/inventory.go` | `c9b48c2b1283f09fb2987fd1fe4dba2c547573f6935d11359d9f8248fabab181` |
| `scripts/toolinventory/inventory_test.go` | `14e3b2bed0569eea29a569d53f870a2e2a3ab487cacc03a0dd171778fe0f9cb4` |
| `scripts/tool-contract/write_disclosure_test.go` | `3c418e760091819175e8fbca338c90eaad861cb125674ba673ee936412d418a1` |
| `internal/mcp/server/server.go` | `db8022b2542f1a7b123d3128527fc65b8679f8b8ca0dd53bcdf9df129be99187` |
| `internal/mcp/server/server_test.go` | `fe08918330ab65a85d7d00ac6f70feca65dc08bcc8b2a7b988a8ebde425f2192` |
| `internal/mcp/registry/registry.go` | `ea3c33e50bc909f2d9f54c9424a1e604e36a15231a66928df1d6bb52f4003cb6` |
| `internal/mcp/registry/registry_test.go` | `5d5ef1ce606f7abc10be1b55875b612d7af6d5c5b224d95b1c37579e7990885a` |
| `cmd/server/main.go` | `f4ed4e1ca76d0d946b5d25276769618f95d60fbd26e04111a65d5c6ee86f33ab` |
| `cmd/server/main_test.go` | `03faa0187b7b7019f4f6d352dfc98827e5e97bedde4329003b138c7f284968eb` |
| `internal/ga4/tools.go` | `0416fe294e736d5d338b7f5edb75ade4466172bd738b0823595007f6920bbffd` |
| `internal/core/config/config.go` | `a5724e44bdef11b4e6025d48857f4b9f617795dcfbd38e58967fc6646d6c6a6e` |
| `internal/core/config/config_test.go` | `1bd68dd1e05715a7e710daa4b3f4e1ef4dee7a2eeaadd815d0bc751b2f15f9bb` |
| `docs/configuration.md` | `39049e63b3f299d58cd0f2797946ba919b9b6e01189de5da2f0e4c4aed7e45d4` |
| `docs/mcp-tools.md` | `3bea4f2a9e06062ba6f3e315d7990c6b13d1e26b282bd75f1583a7946554d845` |
| `docs/mcp-conformance.md` | `2ffa57c498dd4db0bcfd7e8e150d85634f980cf9573fc9d5d6519e8bca20a36b` |
| `docs/compatibility.md` | `209efe8f96de66c579b74a519c2ce227404668d9d2fee9249584cd7574c03857` |
| `configs/config.example.yaml` | `edd8ed3ead5f6c3d6485c8795eec6dd76d80c77a82649efac082c0439bb1b2d5` |
| `configs/config.production.template.yaml` | `8f59553dc8d9771e603dcc896e362e145930019171af9f989d8232fa4fcce1fd` |
| `configs/config.docker.yaml` | `dbd4ba8a60b635ccc8d6d39112fd8e27fd3795fbb760d6cca58455522daeb2db` |
| `internal/magento/client.go` | `0eac569f15d30a2c0bed4487f4a8404a24a1c5d9b8b2faf982cb57c7009f93b6` |
| `internal/magento/client_test.go` | `9ae4d5acee413e9caa029f7bdf887dae246a682cd02a5fe6d3c97f4f8b170494` |
| `internal/magento/sales_analysis.go` | `809bd3044a1bceeb322f491f9b6dfdadf77585fd2fcd5a2c99c9e5ee153f4e98` |
| `internal/magento/order_geography.go` | `bf5aeea96ff1217c82c21141a11b7322f1d4d2c6f23a695e2c4bd1dc7ac32569` |
| `internal/magento/import.go` | `1f2894ecbc6285c9b748f5242785745974cba356c6c5716b41138dcc2493182e` |
| `internal/magento/import_test.go` | `d2e5cd26028b441fab7e5e92ea58b37c7dc13040249681cd957aade650cfe367` |
| `README.md` | `0786b30b19570fca68dcf48129ba4f1d9e48aba9afe281505c4aac90a94f2cbd` |
| `README.zh-CN.md` | `d19d876b5b519e8c8c4429c3414aee42f57ff74a20b94840b42a54af1c06a1f8` |
| `docs/architecture.md` | `67170b7a6cab5a260bf9f32a351ab3574f57fe91e021ba8584dfbe8fff4b3832` |
| `docs/operations/deployment.md` | `c051a9eddbec77eaac2f062fd81d85de0c0141f868aba64d2247b3a094e71853` |

All hashes were re-checked after the revert experiments; the two files temporarily edited
(`cmd/server/main.go`, `scripts/toolinventory/inventory.go`, `internal/magento/import.go`)
returned to the hashes above.

### 2. Reproduced evidence

- `go build ./...` exit 0.
- Contract test set exit 0: `internal/mcp/{registry,server,toolerr}`, `cmd/server`,
  `internal/core/config`, `internal/magento`, `scripts/tool-contract`,
  `scripts/toolinventory`.
- `go test -race -count=1 ./internal/mcp/... ./cmd/server/...` exit 0.
- Real `registerModules` overlay: read-only no DB **53** tools/0 non-read;
  read-only + DB **56**/0 non-read; writes no DB **105**; maximal **112**.
  platform.info listed itself, `tools` length equalled the registry, `writes_enabled`
  matched `HasWrites`, `protocol_versions` matched the five-version SDK list.
- Every maximal wire schema validated: **112/112**.
- F9: `TestMaxImportPayloadPassesHTTPCap` built exactly **8,388,608** decoded bytes,
  request body **11,184,969**, cap **16,777,216**, HTTP **200**; one byte over the cap
  is **413** naming the limit.
- Quickstart: `healthz={"status":"ok"}`, `readyz={"status":"ready","tools":1}`,
  `-healthcheck` exit 0.
- F1 generator tests pass: maximal 112/101, read-only 54/43, read-only+DB 56/46,
  `DatabaseGate=3`, `CombinedGate=3`, `ReadOnlyDBNonRead=0`.
- F2 `TestMcpToolsReadOnlyDatabaseRowMatchesInventory` passes (46/56 row).
- F4 `TestReadyzReportsWithoutBusinessTools` uses the same registry and asserts `tools==1`.
- F5 `TestGeneratedDocumentIsCurrent` passes after the preamble edit.
- F6 `TestNewMCPValidatesDirectAddToolSchema` and
  `TestEveryRegisteredToolSchemaIsClosed` pass when `ValidateSchemas:true`.
- F7 docs list all five SDK versions in `mcp-conformance` P1, `compatibility`, README,
  and architecture.
- F8 no `354542598` in the three in-scope files; config tests pass.
- F10 `TestConfiguredInstancesOmitsDisabledModules` passes.

### 3. Revert experiments

1. **F1 generator gate.** Reverted `gateFor` so all import tools returned `database.url`:
   `TestInventoryCounts` failed with `database.url gate = 6, want 3` and
   `tools.writes+database.url gate = 0, want 3`; `TestImportGatesAreDerived` failed for
   start/cancel/resume; `TestGeneratedDocumentIsCurrent` failed. Restored.
2. **F9 import ceiling.** Set `maxImportBytes = 512 << 10`:
   `TestImportLimitsAgreeAcrossPackages` failed with `maxImportBytes = 524288, want the
   documented ceiling 8388608 (a capability cut is not a fix)`. Restored.
3. **F6 production wiring.** Removed `ValidateSchemas: true` from
   `cmd/server/main.go` and ran `go test ./cmd/server ./internal/mcp/server`: **both
   packages passed**. This is finding MCP50-01: the validator is tested only when a test
   opts in; the production flag is not bound by any test.
4. **F6 default constructor.** An overlay test registered a direct `mcp.AddTool` with an
   open schema: `NewMCP(Options{Registry: reg})` accepted it; only
   `ValidateSchemas: true` rejected it (`schema root must set additionalProperties:false`).

### 4. Findings

| ID | Severity | P-ladder | File:line | Problem | Required fix |
|---|---|---|---|---|---|
| MCP50-01 | medium | **P2** | `internal/mcp/server/server.go:42-45,126`; `cmd/server/main.go:123` | Schema validation is opt-in (`ValidateSchemas` defaults false). A direct `mcp.AddTool` with an open schema is accepted by the default `NewMCP`; the shipped `cmd/server` sets true, but no test binds that production flag (removing it leaves all tests green). MCP-04 is true for the shipped binary but not structurally enforced or revert-proven. | Default `ValidateSchemas` to true (or validate unconditionally) and add a test that the default constructor/production options reject a direct open schema; keep an explicit test-only opt-out if needed. |
| MCP50-02 | low | **P3** | `docs/mcp-conformance.md:8-10` | The References paragraph still says the SDK "negotiates `2025-11-25` through `2026-07-28`", while the P1 row and the other docs correctly list all five versions. | Reword to "all `mcp.SupportedProtocolVersions()` (currently five, 2024-11-05 through 2026-07-28)". |
| MCP50-03 | low | **P3** | `docs/unified-analytics.md:93`; `docs/development/research-baseline.md:47` | The real GA4 property id remains in two out-of-scope files (t47 disclosed this). It is not in the t50/t49 changed set and not in the release-critical configs, but it is still a disclosure gap. | Replace with a placeholder in a docs-only follow-up; keep the dated baseline if it must retain the observation. |

### 5. Verdict and release wording

**PASS** for t49. All ten t39 findings are fixed in the shipped path: the read-only+DB
registry has 56 tools and zero non-read; platform.info is a registry tool with truthful
self-listing/instances/writes/protocols; all 112 wire schemas are closed and validated in
the shipped startup; the timeout is typed; write descriptions are enumerated; envelopes
match; the inventory is generated and drift-tested; the 8 MiB import ceiling is HTTP-proven
with a 512 KiB-cut guard; configs and quickstart work.

The P2 is a hardening/test-binding gap, not a shipped-path failure: `cmd/server` sets
`ValidateSchemas: true` and the real maximal server passes the enumeration. Release notes
should not claim "the default constructor rejects open schemas"; the accurate claim is
"the shipped server validates every wire schema at startup." The P3s are wording/disclosure
cleanups.

---

## t52 — final consolidated review of the whole remediation (frozen tree)

| Field | Value |
|---|---|
| Review task | `t52` (consolidated, final) |
| Reviewer | qa-engineer — authored no production code in this programme; did not review `test/qa` |
| Tree reviewed | baseline `66786a5` plus the uncommitted remediation working tree; bound by the hashes in §1 |
| Method | 17 independent probes in a throwaway copy (package `main`, production wiring), six behavioural reverts, full gate re-run |
| Verdict | **PASS** on the eight P0s and the seams. `CONDITIONALLY READY` remains the right release verdict. Two P3 test-infrastructure findings (F1, F2); no new P0/P1. |

### 1. Revision binding

Every file this review relies on, sha256 at the start of the review. The same list was
re-computed at the end; it was **byte-identical** (`diff` of the two lists: no output), so
this verdict provably covers the bytes it judged.

```text
8acac6b920c1e99ff002c4b8bfea2f522a5ce67e879d59b96351aa379528f79b  internal/mcp/server/server.go
34d837cf99fe880f2f05239386edd9f560ae845a00f30e3063ef379d125ff357  cmd/server/main.go
4980cb4f0160822f97425331a51ddfe0b38ff4ab5c8e14dd8909e0f744d0a754  internal/security/rbac/rbac.go
219f734370c6d715ba2fbe33fa322d55dff3136b12b312cf08d87307a9a399a7  internal/magento/import_jobs.go
c6df34d916b1026cbf3def9a954e2d2aeb3ca6123dc5a4742094f7d52504f2bd  internal/storage/jobqueue.go
47c580ffba17d125daac5aed1c6bd72ded74f3617062f31b9068f97c27634b67  internal/jobs/jobs.go
dd76469ed91401b85a5a355c4f5039bd105b33da01896107ce1346ad8bca9bd4  internal/security/confirm/confirm.go
483fc2a9a0695e57d5c039fd06fad77723f13454396364b2232bb0ba9556b669  internal/storage/confirm.go
aaa54adb977ff879687de4ed03a3885187bbaddc0fef83d4e962faaa555f77f1  internal/storage/operation_ledger.go
67d6ef5b16a51d079662e60f65a5d578bd13f15efced39e53e542b2578a4e0db  internal/magento/operations.go
7617505ad751a071491f27866cce782ec905ac05eaefbeb414c29f4d7f8299f9  internal/magento/invoice_write.go
8ff12ddc580aa163620412df59c6d5837db707fd005a8b197824212b093ede57  internal/magento/order_create.go
0972ca50feeacf2d21ec8f93bfee8ec2e84ac70dcddd14f9ac3be4d3ec65e718  internal/magento/order_documents_write.go
33703b32e282960db43e377eb3ec8830432ea82f83d8a089393ba605fdd4358b  internal/magento/order_status_write.go
dfcd731e5bd96762eb85e16fa01c1d56e3c161d86e88f2cebd3974eddd68b338  internal/security/auth/auth.go
09a41f409d1e6ab29de87f02f108b536356a191ed435aa54067c51a40445ef9d  internal/security/jwt/jwt.go
ebd7fa3c886461984f6b3c8bc6565553a33c683d6e0e60bff49225581b5f5e1d  internal/storage/audit.go
563d6ba80b5fba225c805b45f1ccc48b925da08f01d4d400641bc31b8ba12772  internal/storage/import_batches.go
ea3c33e50bc909f2d9f54c9424a1e604e36a15231a66928df1d6bb52f4003cb6  internal/mcp/registry/registry.go
0ff344998a85bd52300a2d4abe52f7011ff12ba4a03ca768a045f5021eca3516  cmd/worker/main.go
e73b2137f7cc8436d2f82328c2e1ed818869f843df9c7887ab3836853c31c62d  internal/core/errors/errors.go
```

### 2. Method

The probes live only in a throwaway copy of the tree. They are in package `main` so they call
the **production wiring directly** — `authorizeFunc`, `operationBegin`/`operationFinish`,
`selfLedgeredTools`, `auditDecision`, `server.NewMCP` and the HTTP surface — against a real
PostgreSQL 16 ledger/confirmation/queue and reviewer-controlled fake Magento upstreams. They
do not use the authors' tests or the QA suite. Six fixes were then reverted one at a time in
fresh copies and the guarding probe re-run.

### 3. The eight P0s, end to end through the production wiring

**SEC-01 (RBAC scope ladder).** With `rbac.DefaultPolicy()` and the real `authorizeFunc`:
viewer `product.get` 200, viewer `product.create` and `product.delete` `permission_denied`
with **zero** upstream writes, **no** operation row and no `operation_id` in the response;
operator `product.create` (approved) 200 and `product.delete` `permission_denied`; admin
`product.delete` (approved) 200. One POST (operator) and one DELETE (admin) upstream.
*Revert* `requiredScope` to always-read → the viewer write reaches the confirmation gate and
the probe fails; the authors' `TestShippedPolicyEnforcesScopeLadder` fails too.

**SEC-02 (tenant ownership on jobs).** A job started by alice on instance A is `not_found`
for bob on A and for alice on B across `import.status`, `import.cancel` (approved),
`import.resume` (approved) and `import.batch` (a persisted batch linked to the job); the
owner reads status and batch. *Revert* the subject predicate → bob reads alice's job; *revert*
the instance predicate → alice reads B's job; both probes fail, as does the authors' DB-backed
cross-tenant test.

**SEC-03 (confirmation bound to tool and instance).** A `product.create` approval used for
`product.delete` is rejected `confirmation_param_mismatch`, no DELETE reaches the upstream,
and the legitimate create with the same id then succeeds (the failed attempt did not burn it).
The identical-arguments case `order.cancel` → `order.email` is also rejected and sends no
email. Cross-instance is rejected and the legitimate use succeeds; the instance is part of the
canonical hash. Under eight concurrent callers with one approval: exactly one non-replay
success, one upstream write, no `confirmation_used` (losers are refused at the ledger gate as
conflict or get a labelled replay), and the persisted approval row is `consumed` with a second
direct `Consume` returning `confirmation_used`. *Revert* the CAS tool/instance predicates →
the cancel approval sends an order email (`sent:true`) and the probe fails.

**SEC-05 / REL-02 (money safety and the ledger).** Capture under a Magento 500, a client
timeout and a mid-request connection drop each ends `operation_unknown`, with the ledger row
in state `unknown`, the envelope non-retryable, exactly one capture POST, and a second,
separately approved attempt blocked without a second POST. A successful capture is replayed
from the ledger (`replayed:true`) with one POST. A paid invoice is refused `conflict` before
any capture request and the ledger row is `failed`. A forced audit-insert failure (DB trigger)
rolls the terminal transition back with it: the operation stays non-terminal (`executing`), no
outcome audit row exists, and the caller is told. *Revert* the ambiguous branch to `run.Fail`
→ the probe sees `upstream_unavailable`/`upstream_timeout` instead of `operation_unknown` and
fails.

**MAG-01 (capture double-charge).** Covered by the capture probes above: ambiguous outcomes
never retry, a replay never re-charges, and a paid invoice never sends a capture request.

**MAG-04 (duplicate order and wrong-entity reconciliation).** An ambiguous placement is
reconciled only by the exact `quote_id` link: a candidate carrying our cart id resolves to
`reconciled:true`; a same-customer, same-total candidate with a foreign `quote_id` yields
`operation_unknown` with `candidate_order_ids` and is never claimed; agreeing empty reads
yield a definite failure that is explicitly retryable; the placement POST happens exactly
once. *Revert* the match to total-only → the foreign candidate is reported as our order and
the probe fails.

**REL-01 (job fencing).** After a reclaim, the stale attempt's `Complete`, `Fail` and
`Extend` all return `jobs.ErrLeaseLost`; the reclaim carries a new token and attempt 2; the
current attempt completes to `succeeded`; a retried task reaches `failed` and is never
claimed again. The worker retry rule holds: a non-idempotent task is not retried even for a
retryable error; an idempotent task is retried for a retryable error; an ambiguous outcome —
including a real Magento 5xx, which the client marks retryable **and** ambiguous — is never
retried (the batch row plus resume is the recovery path). *Revert* the lease-token guard →
the stale `Complete` is accepted and the probe fails.

### 4. The seams

- **Authorization → ledger gate → tool.** `installPolicy` authorizes before
  `prepareConfirmation` and before `BeginOperation`; the handler runs last. A denied caller
  creates no operation row and receives no `operation_id`, and cannot use a replay to learn
  that an operation exists. Verified by probe and by code order.
- **Confirmation consumed only for a decision that executes.** `BeginCall` returns replay,
  conflict or unknown before `consumeInTx`; only `Proceed` consumes. Probe: a replayed
  `product.create` with a freshly issued approval leaves that approval `approved`, and the
  upstream is not touched.
- **A retried operation reaches a terminal state.** `Fail(retry=true)` requeues; the second
  attempt's terminal `Fail` moves it to `failed`, and it is never claimed again.
- **The instance in the confirmation is the instance the resolver uses.** Both derive from
  the same `instance` argument; the value is part of the canonical argument hash (proven) and
  the SQL predicate is defence in depth.
- **An unauthenticated or unauthorized caller cannot learn whether an operation exists.** The
  denial precedes the gate, carries no `operation_id`, and never replays. An unauthenticated
  call is refused `unauthenticated` before the gate.

### 5. Final gate, re-run and recorded verbatim

```text
$ gofmt -l .
(no output)                                        exit 0
$ go vet ./...
(no output)                                        exit 0
$ go build ./...
(no output)                                        exit 0
$ go test -count=1 ./...
?       .../cmd/admin    [no test files]
ok      .../cmd/server    0.540s
ok      .../cmd/worker    0.361s
ok      .../docs/audit/perf-probe    0.283s
ok      .../internal/cloudflare    0.503s
ok      .../internal/core/config    0.303s
?       .../internal/core/datasource    [no test files]
ok      .../internal/core/errors    0.285s
ok      .../internal/ga4    1.003s
ok      .../internal/jobs    1.096s
ok      .../internal/magento    2.604s
ok      .../internal/mcp/registry    0.359s
ok      .../internal/mcp/server    0.485s
ok      .../internal/mcp/toolerr    0.354s
ok      .../internal/observability    0.321s
ok      .../internal/security/audit    0.295s
ok      .../internal/security/auth    0.284s
ok      .../internal/security/confirm    0.317s
ok      .../internal/security/jwt    1.409s
ok      .../internal/security/rbac    0.296s
ok      .../internal/storage    0.304s
ok      .../internal/unified    0.303s
?       .../scripts/gen-implemented-tools    [no test files]
ok      .../scripts/tool-contract    0.350s
ok      .../scripts/toolinventory    0.433s
?       .../test/e2e/client    [no test files]
?       .../test/e2e/fakemagento    [no test files]
                                                   exit 0
$ MAGE2MCP_TEST_DATABASE_URL=.../mage2mcp go test -count=1 -race ./...
ok      .../cmd/server    2.777s
ok      .../cmd/worker    1.370s
ok      .../docs/audit/perf-probe    1.291s
ok      .../internal/cloudflare    1.577s
ok      .../internal/core/config    1.305s
ok      .../internal/core/errors    1.284s
ok      .../internal/ga4    2.060s
ok      .../internal/jobs    2.087s
ok      .../internal/magento    9.200s
ok      .../internal/mcp/registry    1.389s
ok      .../internal/mcp/server    2.013s
ok      .../internal/mcp/toolerr    1.394s
ok      .../internal/observability    1.338s
ok      .../internal/security/audit    1.291s
ok      .../internal/security/auth    1.365s
ok      .../internal/security/confirm    1.292s
ok      .../internal/security/jwt    2.936s
ok      .../internal/security/rbac    1.286s
ok      .../internal/storage    4.655s
ok      .../internal/unified    1.345s
ok      .../scripts/tool-contract    1.777s
ok      .../scripts/toolinventory    1.685s
                                                   exit 0
$ MAGE2MCP_TEST_DATABASE_URL=.../mage2mcp_qa ./scripts/qa-independent.sh
qa-independent: PASS - 18 test(s) passed, 0 skipped, 0 failed    exit 0
$ npm run lint:md
Summary: 0 issues in 0 files                            exit 0
$ npm run lint:links
(no output)                                        exit 0
```

The `-race` suite ran **in parallel, without `-p 1`**. It is green on this run, but not
deterministic: across nine full parallel `-race ./...` runs during this review, two distinct
intermittent failures appeared, each once — findings F1 and F2 below.

### 6. Findings

#### F1: `cmd/worker` database tests still share the `public` schema, so the reconcile test is state-dependent across runs

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `cmd/worker/reconcile_test.go:37-63` (`reconcileStore` opens the raw
  `MAGE2MCP_TEST_DATABASE_URL`, no `search_path`), `cmd/worker/main_test.go:43`
- **Evidence**: the full parallel `-race ./...` run failed once with
  `--- FAIL: TestReconcileHandlerResolvesEachWay (0.08s)` /
  `reconcile_test.go:124: applied operation = unknown, want succeeded`. The test passes in
  isolation (3/3). The public schema held 50 accumulated `unknown` operations from earlier
  runs. Deterministic reproduction: inserting 25 due reconcilable operations into the public
  schema (`qa-seed-backlog`) makes the test fail in isolation with the same message, because
  the handler's `reconcileBatch = 20` fills with older rows and never reaches the test's
  fresh operations. The seeded rows were deleted afterwards.
- **Trigger**: enough earlier runs (or any activity) to leave more than 20 due reconcilable
  operations in `public` when the test runs.
- **Impact**: the release gate `go test -race ./...` is intermittently red on a
  test-isolation defect, not a product defect. `known-limitations.md` marks `TEST-PAR` as
  FIXED with per-package `search_path`, but that fix covers `internal/storage` and
  `internal/magento` only; `cmd/worker` is database-backed and still shares `public`.
- **Required fix**: give `cmd/worker`'s DB tests their own schema with the same
  `testSchemaDSN` helper the other packages use (or make the reconcile test seed and drain
  deterministically). Then update the `TEST-PAR` entry.

#### F2: `TestJobQueueStaleAttemptCannotWrite` is timing-dependent

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/storage/jobqueue_transitions_test.go:113-121` (300 ms lease,
  400 ms sleep, no polling)
- **Evidence**: one full parallel `-race ./...` run failed with
  `--- FAIL: TestJobQueueStaleAttemptCannotWrite (1.49s)` /
  `jobqueue_transitions_test.go:126: claim B = <nil>, want the reclaimed task at attempt 2`.
  The test passed 5/5 in isolation and on seven of the other eight full runs. Under load the
  fixed sleeps can race the lease expiry.
- **Impact**: the release gate is not deterministic; a red run costs a re-run to distinguish
  a real regression from this flake.
- **Required fix**: replace the fixed sleeps with polling (claim until the reclaimed task
  appears, with a deadline), or set the lease much shorter than the wait.

### 7. Known limitations — accuracy check

Every entry in `known-limitations.md` was read against these bytes. The following are
confirmed **accurately described**: SEC-04 (self-asserted `-as`), SEC-10 (bounded upstream
text), SEC-15 (session not identity-bound), SEC-17/SEC-18, SEC-R01 (instance-scoped batch
list; I re-confirmed it), MAG-12 (now correctly states that a fractional transfer quantity is
**refused**, not truncated), MAG-15, MAG-20, MAG-22, MAG-23 (no Magento integration — the
largest residual risk), MAG-04-R (reconciliation now exact by `quote_id`, degrading to
UNKNOWN), REL-11, REL-12, REL-16-R (self-ledgered audit row after commit) and REL-16-R2
(approval consumed before the self-ledgered tool's own ledger begin — I re-confirmed the
availability-only failure mode), ARCH-07, and the MCP/PERF entries.

One entry is **understated**: `TEST-PAR` says "Resolved … Verified by four consecutive
`go test -race ./...` runs with zero failures. No `-p 1` workaround is needed." The
storage/magento half is fixed, but `cmd/worker` was not covered and its reconcile test is
state-dependent (F1); a second, timing-dependent test also flakes (F2). The accurate statement
is: cross-package row interference is fixed for the two schema-scoped packages; the worker
package still shares `public`, and the gate can still flake for two independent reasons.

The release report's §G caveat — "no single verdict states that these final bytes were
independently reviewed" — is closed by this section. Its "final full gate is green" claim
should be read together with F1/F2: green on the recorded run, not deterministic.

### 8. Verdict

**PASS** for the eight P0s and the seams between them, on the hashes in §1. No new P0/P1 was
found. The two findings are test-infrastructure defects (P3), not product defects.

**`CONDITIONALLY READY` is still the right release verdict on these bytes.** It is not READY
for the three reasons the release report gives, all of which this review re-confirms: no
Magento integration coverage (`MAG-23`), order reconciliation depends on the instance
exposing `quote_id`, and the residual audit/isolation gaps (`REL-16-R`, `SEC-R01`, rate
limiting, untested `cmd/admin`). It is not NOT READY: all eight P0s are fixed and were
re-proven here through the production wiring, six of them by reverting the fix and watching
the guard fail. The two new P3s make the *gate* less deterministic, not the product less safe;
fixing F1 and F2 (both small) is recommended before the release is tagged, and the release
notes should not claim a flake-free gate until then.
