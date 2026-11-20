# Magento integration review (`MAG-*`)

**Owner:** Magento integration engineer
**Baseline commit:** `66786a5` (`docs: record CI local-first timings (#149)`)
**Package under review:** `internal/magento/` (catalog, MSI inventory, sales documents), plus
the tool wiring in `internal/magento/write_tools.go` and the confirmation gate in
`internal/mcp/server/server.go`
**Format:** [`docs/audit/README.md`](README.md)
**Date:** 2026-11-20

---

## 1. Executive summary

The Magento gateway is a careful *input validator* wrapped around a *bare sequence of REST
calls*. Most values that leave the process are checked (bounds, regexes, enum membership,
duplicate detection) and the HTTP client is bounded (30 s timeout, 8 MiB response cap, no
redirect chasing, closed error taxonomy). That part is genuinely good and it is why the
defect list below contains almost no injection or resource-exhaustion items.

What is missing is everything that happens *after* the first byte is sent: there is no
pre-flight state read, no idempotency key, no operation ledger, no compensation, no
verification read-back, and no state machine. Every multi-call workflow (configurable
create, variations, source transfer, order create) is a straight-line sequence whose
failure mode is "some writes happened, you work out which". Because Magento's write
endpoints are not idempotent and its bulk endpoints are not documented as atomic, the
straight-line design converts ordinary transport failures (timeout, 5xx, 429) into
duplicate charges, duplicate orders, phantom stock, and orphan catalog rows.

Two findings reach **P0** because a retry after an ambiguous failure can move money twice:

- `magento.invoice.capture` is a single unguarded `POST`, and the gateway *tells the agent
  the call is retryable* when Magento returns 5xx (`retryable: true` in the tool error
  envelope). Reproduced: two capture requests reach Magento from one logical operation.
- `magento.order.create` places an order through four cart calls with no idempotency key
  and no post-failure lookup, so a retry after a timeout places a second order (and a
  second authorisation/capture for payment methods that capture at placement).

Eleven **P1** findings cover the catalog and inventory half: `completed_skus` — the only
compensation record the configurable workflow produces — is truncated at 20 entries / 900
bytes and can cut a SKU in half; a failed child link leaves a permanent orphan that a retry
reports as `exists` and never repairs; `TransferSourceQty` is a textbook lost-update
read-modify-write; a failed parent stock-status write is swallowed and success is returned.

Finally, **there is no integration test anywhere in the repository**. `docs/evidence/magento.md`
records every behavioural probe as `[U]` (not run — no credentials), and the two fakes in the
tree (`httptest` handlers, `test/e2e/fakemagento`) encode the gateway's own assumptions. That
is how a P1 contract defect survived: `magento.product.media.upload` rejects the integer
entry id that the repository's own integration document says Magento returns, so **every
successful image upload is reported as a failure** and every retry adds a duplicate gallery
entry.

| Severity | Count | IDs |
|---|---|---|
| **P0** | 2 | MAG-01, MAG-04 |
| **P1** | 11 | MAG-02, MAG-03, MAG-05, MAG-06, MAG-07, MAG-08, MAG-09, MAG-10, MAG-11, MAG-14, MAG-23 |
| **P2** | 9 | MAG-12, MAG-13, MAG-15, MAG-16, MAG-17, MAG-18, MAG-19, MAG-20, MAG-21 |
| **P3** | 4 | MAG-22, MAG-24, MAG-25, MAG-26 |
| **Total** | **26** | |

State distribution: **CONFIRMED** 22, **DOC-ONLY** 2 (MAG-24, MAG-25), **PLANNED** 2 (MAG-15, MAG-22).
No finding in this report is `ALREADY-FIXED` or `NOT-REPRODUCIBLE`; every CONFIRMED item was
reproduced against the current commit, either by reading the executed code path or by an
injected audit test (§3.2).

**Remediation status (t10).** MAG-06, MAG-08, MAG-09, and MAG-14 have code fixes with CONTRACT
regression tests in this working tree (see each finding's *Remediation (t10)* note). They stay
`CONFIRMED` in this report until the independent reviewer signs them off in
`independent-code-review.md`; the P0s (MAG-01, MAG-04) and the route-level sales findings (MAG-02,
MAG-03) are untouched and remain open.

**Release verdict from this area: NOT READY.** The two P0s are money-moving and require a
design change (ledger + verification), not a patch; the P1 catalog/inventory items need the
configurable and transfer workflows rebuilt around an operation record.

---

## 2. What was verified, and how

### 2.1 Verification commands (real output)

```text
$ go test ./internal/magento/... -count=1
ok      github.com/shuaiZend/mage2mcp/internal/magento    0.585s

$ go test ./internal/magento/... -count=1 -v | grep -c '^--- PASS'
237
$ go test ./internal/magento/... -count=1 -v | grep -c '^=== RUN   Test'
352
$ go test ./internal/magento/... -count=1 -v | grep -c '^--- FAIL'
0
```

(352 `RUN` lines = 237 top-level tests + 115 subtests; all pass.) The suite is fast and green;
that is precisely the problem — see MAG-23.

### 2.2 Audit reproduction harness (no repository changes)

The findings marked *reproduced* were reproduced with twelve audit-only tests injected through
`go test -overlay`, so `internal/magento/` was not modified (`git status --short internal/magento`
is empty). The harness is reproduced in §9 so the reviewers can re-run it verbatim.

```text
$ go test -overlay /tmp/magrepro/overlay.json ./internal/magento/ -count=1 -run TestRepro
ok      github.com/shuaiZend/mage2mcp/internal/magento    0.514s     (12/12 PASS)
```

### 2.3 Test inventory and labelling

`UNIT` = pure validation/decoding with no server; `CONTRACT` = fake HTTP server asserting the
request the gateway emits; `INTEGRATION` = a real Magento 2.4.x instance.

| Test file | Kind | Tests | What it actually proves | What it cannot prove |
|---|---|---|---|---|
| `configurable_test.go` | CONTRACT (5 fake servers) + UNIT | 19 | stage order, request bodies, stage/`completed_skus` details, validation-before-write | that Magento accepts the payloads, that the flow is resumable, that the parent becomes salable |
| `inventory_test.go` | CONTRACT (5) + UNIT | 11 | source-item bodies, absolute-quantity semantics, insufficient-source refusal, source/link bodies | MSI atomicity, concurrent transfers, fractional quantities, indexer behaviour |
| `inventory_read.go` coverage → `media_inventory_test.go` | CONTRACT (3) + UNIT | 4 | source-item `searchCriteria` shape, legacy stock item decode | that page 2 exists (no pagination test) |
| `invoice_write.go` coverage → `invoice_links_test.go`, `invoice_capture_test.go` | CONTRACT (4) + UNIT | 5 | `POST /V1/invoices` body, `POST /V1/invoices/9/capture` path | that the route is the *documented* invoice/capture route; capture flag semantics |
| `order_documents_write.go` coverage → `document_write_test.go`, `order_documents_test.go` | CONTRACT (8) + UNIT | 11 | shipment/creditmemo bodies, read projections, `flexBool` | refund execution, duplicate refunds, pre-flight state |
| `order_create_test.go` | CONTRACT (2) + UNIT | 3 | cart → items → shipping-information → order sequence | whether `store_id` is honoured (it is not), duplicate orders |
| `product_write.go` coverage → `product_write_test.go`, `product_seo_test.go`, `product_batch_test.go` | CONTRACT (10) + UNIT | 25 | payload shape, merge semantics, validation-before-write | attribute acceptance, bundle/grouped sellability |
| `product_test.go` | CONTRACT (3) + UNIT | 6 | projection decoding, search filter groups, pagination cursor | search behaviour against real EAV |
| `product_options_test.go` | CONTRACT (1) | 1 | `GET /products/{sku}/options` decode | the 404 fallback (untested) |
| `category_write_test.go` | CONTRACT (5) + UNIT | 6 | category create/update/assign/delete bodies | URL rewrite side effects |
| `media.go` coverage → `media_upload_test.go`, `media_inventory_test.go` | CONTRACT (6) + UNIT | 13 | base64 envelope, MIME/filename/label validation, **string** and **object** entry-id fixtures | **the integer entry id Magento actually returns** (MAG-14) |
| `catalog_test.go` | CONTRACT (8) + UNIT | 11 | tree flattening, attribute sets/attributes, option list/create | numeric option id (MAG-26) |
| `test/e2e/fakemagento/main.go` | CONTRACT (process-level fake) | 2 routes | end-to-end plumbing, confirmation flow, import | everything about Magento; the README itself calls it "a smoke test, not a substitute" |
| `docs/evidence/magento.md` §3 | **INTEGRATION — none** | 11 probes | nothing: every behavioural row is `[U]`, "not run — credentials not provided" | — |

**There is zero INTEGRATION coverage in this repository.** No finding in this report claims
integration evidence.

---

## 3. Findings

### MAG-01: `magento.invoice.capture` can capture the same invoice twice after an ambiguous failure

- **Severity**: P0
- **State**: CONFIRMED
- **Location**: `internal/magento/invoice_write.go:102` (the whole function), `internal/magento/client.go:178`, `internal/mcp/toolerr/toolerr.go:114`, `internal/mcp/server/server.go:156`
- **Evidence**: `CaptureInvoice` is one unguarded call:

  ```go
  _, err := s.client.do(ctx, http.MethodPost, "/invoices/"+strconv.Itoa(invoiceID)+"/capture", nil, nil)
  return err
  ```

  There is no `GetInvoice` pre-flight (the read primitive exists at
  `internal/magento/order_documents.go:132` and is never used by a write path), no operation
  record, no post-call verification, and no idempotency key. Three properties combine into the
  duplicate-charge path:

  1. A timeout is reported as `upstream_timeout: Magento did not answer before the deadline; the
     call may have completed` (`client.go:153`) — it explicitly admits the money may have moved.
  2. A 5xx is mapped to `CodeUpstreamUnavailable` with `e.Retryable = true` (`client.go:179`) and
     the tool envelope then serialises `"retryable": true` to the agent (`toolerr.go:120`).
     Magento returns 500 for business failures too, not only for transient ones.
  3. The confirmation token is consumed *before* the call executes (`server.go:156`), so it cannot
     be replayed; the only way to retry is a **fresh** approval, which a human will grant because
     the previous attempt reported a retryable/ambiguous failure. The confirmation protocol
     therefore gives no protection against a duplicate side effect — it only proves that some
     human approved *an* execution.

  Reproduced (`TestReproCaptureTimeoutThenRetry`): the fake upstream records the capture and then
  stalls past the 40 ms client deadline; the first call returns `upstream_timeout`; the second call
  reaches the upstream again — `captures_seen_by_magento=2`.
- **Trigger**: any failure after Magento (or the payment gateway behind it) has committed the
  capture: a 30 s client timeout on a slow gateway, a 502/504 from an intermediate proxy, a 500
  raised by the payment method after a successful authorisation, or a 429. Then any retry —
  by the agent, because the envelope said `retryable: true`, or by the operator, because the
  message said the call may have completed.
- **Impact**: the customer is charged twice for one invoice. There is no gateway-side record that
  would let an operator prove or reverse it, and no read-back to tell them whether the first call
  succeeded.
- **Proposed fix**: (a) make `CaptureInvoice` a three-step operation with an operation record:
  `intent` (persist `instance, tool, invoice_id, actor, confirmation_id`) → `send` → `observe`
  (`GetInvoice`, assert `state == 2` / `amount_paid == grand_total`, record `transaction_id`);
  (b) never set `Retryable` for a money-moving write, and introduce an explicit
  `outcome_unknown` code for timeouts/5xx after a write; (c) on `outcome_unknown`, refuse a
  second capture until `observe` has run and reported the invoice state.
- **Test requirement**: `TestCaptureInvoiceRejectsSecondCapture` (CONTRACT: two calls with the
  same operation id — the second must be rejected before any HTTP request, and the ledger must
  show one `sent`); `TestCaptureInvoiceTimeoutRecordsUnknownOutcome` (CONTRACT: a stalling
  upstream must yield `outcome_unknown` + a persisted ledger row, and `Retryable` must be false);
  `TestCaptureInvoiceVerifiesStateAfterSuccess` (CONTRACT: `GetInvoice` is called and a
  non-captured state is reported as a failure); `IT-CAPTURE-01` (INTEGRATION: capture once,
  then attempt a second capture against real Magento with a real payment method and assert the
  second is refused and `amount_paid` is unchanged).
- **Verified by**:

### MAG-02: sales documents are created through repository routes; the `capture` flag cannot be honoured

- **Severity**: P1
- **State**: CONFIRMED (client side) — the upstream consequence is contract-derived and needs `IT-INVOICE-01`
- **Location**: `internal/magento/invoice_write.go:44`, `internal/magento/order_documents_write.go:74`, `internal/magento/order_documents_write.go:102`
- **Evidence**: the gateway sends

  ```json
  {"entity": {"order_id": 42, "capture": true, "items": [{"order_item_id": 7, "qty": 1}]}}
  ```

  to `POST /rest/{store}/V1/invoices` (reproduced verbatim in `TestReproFinanceNoPreflight`).
  That route is the *repository* route (`InvoiceRepositoryInterface::save`, body parameter
  `entity`); the documented "invoice an order and capture" workflow is
  `InvoiceOrderInterface::execute($orderId, $capture, …)` at `POST /V1/order/{orderId}/invoice`,
  whose body is `{"capture": true, "items": [...]}` with no `entity` wrapper. `capture` is not a
  property of `InvoiceInterface`, so on the route the gateway uses it is at best stored as
  arbitrary model data and ignored. The same pattern applies to `POST /V1/creditmemos`
  (`CreditmemoRepositoryInterface::save`, `order_documents_write.go:108`) versus
  `POST /V1/order/{orderId}/refund` (`RefundOrderInterface`), and to `POST /V1/shipments`
  (`order_documents_write.go:79`) versus `POST /V1/order/{orderId}/ship` (`ShipOrderInterface`).
  `docs/implemented-tools.md:68` promises "`capture` defaults to false and does not charge",
  implying that `capture: true` *does* charge.
- **Trigger**: any call to `magento.invoice.create` with `capture: true`, or any credit memo /
  shipment creation.
- **Impact**: an operator believes money was taken when it was not (silent revenue loss), or an
  invoice exists that was never registered against the order (no totals, no transaction, order
  state unchanged) — which then makes MAG-03's refund accounting wrong as well.
- **Proposed fix**: switch to the workflow routes with their documented bodies, keep the
  repository routes only for genuine entity persistence, and add a read-back that asserts the
  order state/`total_invoiced` moved. If the workflow route is unavailable on an instance, return
  `unsupported_capability` instead of silently degrading.
- **Test requirement**: `TestCreateInvoiceUsesInvoiceOrderRoute` (CONTRACT: path
  `/V1/order/42/invoice`, body without `entity`, `capture` at top level);
  `TestCreateInvoiceVerifiesOrderState` (CONTRACT: after the write, `GET /orders/42` is read and a
  missing `total_invoiced` change fails the tool); `IT-INVOICE-01` (INTEGRATION: invoice+capture
  against real Magento with a test payment method, assert `amount_paid`, order state
  `processing`, and one row in `sales_invoice_grid`).
- **Verified by**:

### MAG-03: `magento.creditmemo.create` has no invoice linkage, no refundable-quantity check, and no idempotency

- **Severity**: P1 (P0 on an instance whose credit-memo route executes the refund)
- **State**: CONFIRMED
- **Location**: `internal/magento/order_documents_write.go:91`, `:146`, `:213`
- **Evidence**: the entity body carries only `order_id`, `items`, `adjustment_*`, `comment`,
  `notify`. It has no `invoice_id` (so the credit memo cannot be tied to the invoice it refunds),
  no `offline` flag, and no pre-flight read: `OrderCreditmemos` (`order_documents.go:219`),
  `OrderInvoices` (`:108`) and `GetInvoice` (`:132`) all exist and none is called. Nothing checks
  that the requested quantity is still refundable, so a second call for the same lines is
  accepted by the client and sent upstream. The tool description
  (`write_tools.go:863`) says "actual payment refunds depend on the payment method", which is an
  accurate hedge but not a control.
- **Trigger**: a retry after a timeout or 5xx on `magento.creditmemo.create`, or a well-meaning
  second call for the same order item.
- **Impact**: a second credit memo for the same quantity (duplicate refund record and, where the
  instance's route executes the refund, a duplicate money movement); finance reconciliation breaks
  because the document is not linked to an invoice.
- **Proposed fix**: pre-flight `OrderInvoices` + `OrderCreditmemos`, compute refundable qty per
  order item, refuse an over-refund before sending; send `invoice_id`; record the operation and
  verify with `GetCreditmemo` + `GetInvoice`.
- **Test requirement**: `TestCreateCreditmemoRefusesOverRefund` (CONTRACT: pre-flight reads show
  qty 1 already refunded, request for qty 2 must fail with `validation_failed` and zero writes);
  `TestCreateCreditmemoIsIdempotentPerOperation` (CONTRACT: the same operation id twice issues one
  POST); `IT-REFUND-01` (INTEGRATION: refund 1 of 2 units, retry, assert one credit memo and
  `total_refunded` unchanged).
- **Verified by**:

### MAG-04: `magento.order.create` places a duplicate order (and duplicate charge) on retry

- **Severity**: P0
- **State**: CONFIRMED
- **Location**: `internal/magento/order_create.go:60`, `:93`, `:95`
- **Evidence**: four sequential calls — `POST /customers/{id}/carts`, one
  `POST /carts/{id}/items` per line, `POST /carts/{id}/shipping-information`, then
  `POST /carts/{id}/order` (reproduced in `TestReproOrderStoreIDDropped`). There is no
  idempotency key (the queue's `IdempotencyKey` exists only for `internal/jobs` import tasks —
  `internal/jobs/jobs.go:57`, `internal/storage/jobqueue.go:69`), no lookup for an equivalent
  recent order, and no way to ask "did my order land?" after a timeout. On a `place_order`
  failure the code deletes the cart (`order_create.go:95`, `cleanupCart` at `:232`) and returns a
  stage error whose only extra detail is `cart_cleanup`; the message never states that an order
  may exist.
- **Trigger**: a timeout or 5xx on `POST /carts/{id}/order` after Magento committed the order, then
  a retry (new confirmation, same arguments).
- **Impact**: a second order for the same customer and items. With a payment method that
  authorises/captures at placement this is a second charge; without one it is a duplicate
  fulfilment and a stock reservation that the operator must find and cancel by hand.
- **Proposed fix**: persist an operation record before the first call and include it in the
  order's status history or a custom field so a retry can be matched; on `outcome_unknown`, look
  up `GET /orders?searchCriteria[...customer_id...]` within a bounded window and compare items and
  totals before allowing a second placement; never auto-delete the cart on an ambiguous outcome.
- **Test requirement**: `TestCreateOrderIsIdempotentPerOperation` (CONTRACT: same operation id →
  exactly one `POST /carts/{id}/order`); `TestCreateOrderTimeoutDoesNotDeleteCartAndReportsUnknown`
  (CONTRACT: stalling upstream → `outcome_unknown`, `cart_cleanup` absent, a reconciliation hint
  present); `IT-ORDER-01` (INTEGRATION: place an order, force a client timeout with a proxy delay,
  retry, assert exactly one order and one payment authorisation).
- **Verified by**:

### MAG-05: `CreateConfigurable` is a bare four-stage sequence with no precheck, resume, compensation, or verification

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `internal/magento/configurable.go:114` (children → parent → links → stock), `:147`
- **Evidence**: the function is a straight line of `Create` / `createConfigurableParent` /
  `AddChild` / `setParentStockStatus`. Before the first write there is no existence check of the
  parent or child SKUs (only syntactic validation, `validateConfigurableInput` at `:299`), so a
  half-created set from a previous attempt is discovered only as an upstream 400. There is no
  operation id, no journal, no compensating `DELETE`, no resume entry point, and no verification
  stage — `GetConfigurable`/`Children` (`:254`, `:280`) exist but are never called after a write.
  Reproduced (`TestReproConfigurableRerunAfterPartialFailure`): with one child left over from a
  failed attempt, the re-run returns
  `code=validation_failed stage=children completed="" failed="PHONE-01-BLK"` and never reaches the
  parent (`parent_attempts=0`). The operator gets no information about what the *previous* attempt
  created, and no supported way to remove it.
- **Trigger**: any upstream failure in the middle of a multi-child create (400 on one child, 500
  on the parent, a conflict on a link), followed by the documented recovery action ("run
  compensation before retrying").
- **Impact**: an operator cannot safely retry and cannot enumerate what to delete; the catalog is
  left with children that belong to no parent. With 50 children this is a manual, error-prone
  cleanup against a live catalog.
- **Proposed fix**: introduce the missing state machine —
  `PRECHECK` (GET parent + every child SKU, fail fast with the full free/taken list) →
  `PLAN` (deterministic stage list + idempotency key) → `APPROVAL` (existing confirmation) →
  `EXECUTION` (per-stage journal rows: `children[i]=created`, `parent=created`, `link[i]=created`)
  → `VERIFICATION` (`GetConfigurable` children == expected, options == expected, salable qty > 0)
  — plus a `magento.configurable.status`/`resume`/`compensate` trio keyed by the operation id.
- **Test requirement**: `TestCreateConfigurablePrecheckRejectsExistingSKU` (CONTRACT: parent or
  child already present → `conflict` with `existing_skus`, zero writes);
  `TestCreateConfigurableResumeAfterChildFailure` (CONTRACT: journal from a failed attempt →
  resume creates only the missing children and completes the links);
  `TestCreateConfigurableVerifiesResult` (CONTRACT: a parent whose `GET /children` does not match
  the plan fails verification); `IT-CONFIGURABLE-01` (INTEGRATION: full create + resume + delete
  against real Magento, assert GraphQL `variants` count and `stock_status`).
- **Verified by**:

### MAG-06: `completed_skus` — the only compensation record — is silently truncated and can cut a SKU in half

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `internal/magento/configurable.go:499` (`boundedSKUs`), `:483` (`writeStageError`)
- **Evidence**: `boundedSKUs` caps the list at 20 entries and then at 900 bytes
  (`joined = joined[:maxCompletedSKUsBytes]`), with no marker and no count. With 60-character SKUs
  the byte cap binds first: reproduced (`TestReproCompletedSKUsTruncation`) with 25 children and a
  failure at child 21 — `completed_skus: bytes=900 entries=16 truncated_marker=false
  last_entry_is_partial=true`. Four created children are absent from the list and the 16th entry is
  a partial token that matches no SKU.
- **Trigger**: a configurable create with more than ~15–20 children (the schema allows 50) that
  fails after the first 20 children are written.
- **Impact**: the compensation instruction in the error message ("completed writes are not rolled
  back and require compensation") cannot be followed: the operator does not know which children
  exist, and the truncated token invites deleting the wrong SKU.
- **Proposed fix**: return the full list as a structured field (the MCP result can carry an array;
  the 1024-byte detail cap is the constraint to remove, not the data), or return an operation id
  plus a read tool that lists the journal. At minimum: never cut mid-token, add
  `completed_truncated: true` and `completed_count`.
- **Test requirement**: `TestCompletedSKUsNeverTruncatesMidToken` (CONTRACT/UNIT: 50 children ×
  64-byte SKUs → every returned token parses as a SKU); `TestConfigurableFailureExposesFullJournal`
  (CONTRACT: the failure result carries all created SKUs, or a resumable operation id).
- **Remediation (t10)**: `boundedSKUs` now emits whole SKUs only and returns the omitted count;
  `writeStageError` adds `completed_total`/`completed_omitted` when the list is abbreviated, so the
  compensation list is never silently short or corrupt. Guard:
  `TestCreateConfigurableCompletedSKUsWholeTokens` (40 × 59-byte SKUs → 15 whole SKUs,
  `completed_omitted=24`, `completed_total=39`, every reported token an input SKU; plus the
  20-entry-cap case). The audit's own reproduction now yields `bytes=899 entries=15
  last_entry_is_partial=false` instead of a cut token. State flips to ALREADY-FIXED on reviewer
  sign-off.
- **Verified by**:

### MAG-07: a failed child link leaves an orphan that a retry reports as `exists` and never repairs

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `internal/magento/configurable.go:623` (skip-if-exists), `:648` (link failure), `:641`
- **Evidence**: `CreateVariations` creates the child, then links it. If the link fails the row is
  marked `failed` while the product exists, and the error text does not say the product was
  created. On the next call the existence check at `:623` short-circuits:

  ```go
  if _, err := s.Get(ctx, variant.SKU); err == nil {
      row.Status = "exists"
      result.Skipped++
      ...
      continue          // AddChild is never retried
  }
  ```

  Reproduced (`TestReproVariationLinkFailureOrphan`): first call
  `[{SKU:P-1-RED Status:failed ...stage links...}]`, second call `[{SKU:P-1-RED Status:exists}]`,
  `link_attempts=1` — the orphan is never linked.
- **Trigger**: a 5xx/409/timeout on `POST /configurable-products/{sku}/child` (the most likely
  failure in the whole flow, because Magento validates the child's variant values there).
- **Impact**: a simple product exists that belongs to no configurable parent — invisible on the
  storefront, unorderable, and permanently skipped by every retry. Catalog corruption that the
  tool actively conceals.
- **Proposed fix**: distinguish `created` from `created_unlinked` in the row status, and on the
  skip path verify the link (`Children(parentSKU)` contains the SKU) and repair it instead of
  skipping.
- **Test requirement**: `TestCreateVariationsRepairsOrphanOnRetry` (CONTRACT: child exists, link
  absent → the retry issues the link and reports `linked`, not `exists`);
  `IT-VARIATIONS-01` (INTEGRATION: force the link to fail, retry, assert the child is linked).
- **Verified by**:

### MAG-08: a failed parent stock-status write is swallowed and success is returned

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `internal/magento/configurable.go:147`
- **Evidence**:

  ```go
  inStock := false
  if parent.ID > 0 {
      if err := s.setParentStockStatus(ctx, in.Parent.SKU, parent.ID); err == nil {
          inStock = true
      }
  }
  return &ConfigurableProduct{... ParentInStock: inStock}, nil
  ```

  The error is discarded; no stage error, no warning, no detail. Reproduced
  (`TestReproParentStockStatusSwallowed`): the `PUT /products/{sku}/stockItems/{id}` returns 500
  and `CreateConfigurable` returns `err=<nil> parent_in_stock=false`.
- **Trigger**: any failure of the legacy stock-item write (permission scope, product id mismatch
  on a multi-website install, 5xx).
- **Impact**: the workflow reports success for a configurable that is not salable; whether it is
  noticed depends on the caller reading one extra boolean. This is "partial success reported as
  success" — the exact failure mode the task asked about.
- **Proposed fix**: return `writeStageError(stageStock, completed, sku, err)` so the caller sees a
  failure and the completed set, and add a verification stage that reads the salable quantity
  (see MAG-09) instead of trusting the write.
- **Test requirement**: `TestCreateConfigurableFailsAtStockStage` (CONTRACT: 500 on the stock write
  → error with `stage=stock_status` and the completed SKUs); `IT-CONFIGURABLE-02` (INTEGRATION:
  assert `is-product-salable` true after create).
- **Remediation (t10)**: a failed `setParentStockStatus` now returns
  `writeStageError(stageStock, completed, parentSKU, err)` — `stage=stock_status`, the parent in
  `failed_sku`, the full completed set — instead of being swallowed. When the child stock cannot be
  read back, no status is written and the result says `parent_stock_source: "unobserved: <code>"`.
  Guards: `TestCreateConfigurableFailsAtStockStage`, `TestCreateConfigurableReportsUnobservedStock`.
- **Verified by**:

### MAG-09: the parent is forced in stock regardless of child stock, and salable quantity is never read

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `internal/magento/configurable.go:745` (`IsInStock = true`), `internal/magento/configurable.go:156` (children get quantity only on the `default` source)
- **Evidence**: `setParentStockStatus` hard-codes `body.StockItem.IsInStock = true` with no
  reference to any child quantity. Children without a `quantity` get **no source item at all**
  (`createChild` passes `Quantity: child.Quantity`; nil means no `AssignSourceItems` call), and MSI
  treats a missing source item as 0/out of stock. The gateway has no way to read salable quantity:
  `GET /V1/inventory/get-product-salable-quantity/{sku}/{stockId}` is documented as a capability
  (`docs/integrations/magento.md:262`) but no Go code calls it (`grep -r salable internal/` matches
  only a comment and two tool descriptions).
- **Trigger**: creating a configurable without per-child quantities (the default, since `quantity`
  is optional), or on a multi-source install where children hold stock in a non-`default` source.
- **Impact**: storefront shows "In Stock" for a configurable with zero salable units; the customer
  discovers it at checkout. On MSI installs the parent's legacy `is_in_stock` and the aggregated
  salable quantity disagree, and no tool can tell the operator which one the storefront used.
- **Proposed fix**: read salable quantity (or `is-product-salable`) for the parent stock and report
  it in the result; set `is_in_stock` from the observed value rather than unconditionally; expose a
  `magento.inventory.salable` read tool so verification is possible at all.
- **Test requirement**: `TestCreateConfigurableReportsSalableQty` (CONTRACT: the salable-quantity
  read is called and a zero value produces `parent_in_stock=false` plus a warning);
  `IT-CONFIGURABLE-03` (INTEGRATION: zero-stock children → GraphQL `stock_status` is OUT_OF_STOCK).
- **Remediation (t10)**: the parent's legacy `is_in_stock` is written from an observation
  (`observeParentStock`: the children's MSI source items with status 1 and quantity > 0, summed per
  child; the parent is in stock when at least one child is) instead of being forced to true, and the
  result carries `parent_stock_source` describing the derivation. Reservations and the stock-source
  topology are still not applied, and a read that hits its page cap (MAG-13) yields
  `parent_stock_source: "unobserved: …"` with no stock write rather than a guessed value — those are
  the remaining conservative gaps. Guards: `TestCreateConfigurableRequestSequence` (no child stock →
  `is_in_stock=false`, `parent_in_stock=false`), `TestCreateConfigurableAssignsChildQuantity` (4 units
  observed → `is_in_stock=true`, `parent_in_stock=true`, "1 of 2 children in stock"),
  `TestCreateConfigurableReportsUnobservedStock`, and
  `TestCreateConfigurableUnobservedStockOnPageCap`.
- **Verified by**:

### MAG-10: `TransferSourceQty` loses updates — read-modify-write of absolute quantities

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `internal/magento/inventory.go:164`, `:180` (read), `:196` (write)
- **Evidence**: the transfer reads the two source rows, computes `from-qty` and `to+qty` in Go, and
  upserts both **absolute** values in one `POST /inventory/source-items`. Nothing serialises two
  transfers for the same SKU, and the read is not tied to the write by a version/etag. Reproduced
  (`TestReproTransferLostUpdate`): two transfers of 3 units from a snapshot of 10/0 both compute
  7/3 — `write 1: from=7 to=3`, `write 2: from=7 to=3`.
- **Trigger**: two concurrent transfers for the same SKU (two agents, a retried job, an operator
  double-click), or a read that returns a stale replica.
- **Impact**: 3 of the 6 requested units never arrive at the destination while the source shows 7
  instead of 4 — phantom stock that oversells, or understock that blocks sales. No record exists to
  reconstruct what happened.
- **Proposed fix**: serialise per SKU (a gateway-side lock keyed by `instance+sku`, or a
  compare-and-set on a re-read loop: read → compute → write → re-read → retry on mismatch), and
  record an operation row with `from_before/from_after/to_before/to_after`. On Commerce editions
  the documented `/inventory/bulk-product-source-transfer` route is a better primitive; on Open
  Source the re-read loop is the only option.
- **Test requirement**: `TestTransferSourceQtySerialisesConcurrentTransfers` (CONTRACT: two
  transfers with a shared snapshot → the second re-reads after the first write and computes 4/6,
  or is rejected with `conflict`); `TestTransferSourceQtyRecordsLedger` (CONTRACT: the operation row
  carries pre/post quantities); `IT-TRANSFER-01` (INTEGRATION: two concurrent transfers, assert the
  sum is conserved).
- **Verified by**:

### MAG-11: a mid-batch `AssignSourceItems` failure leaves one row written, with no compensation and no pre/post check

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `internal/magento/inventory.go:196` (two-item batch), `:52` (`AssignSourceItems`)
- **Evidence**: `TransferSourceQty` sends both rows in one call:

  ```go
  batch := []SourceItem{ {from, newFrom, statusFor(newFrom)}, {to, newTo, statusFor(newTo)} }
  return s.AssignSourceItems(ctx, batch)
  ```

  Magento's `SourceItemsSave` iterates the items and saves them one by one; the endpoint documents
  no atomicity and returns no per-item outcome (the code comment at `:49` admits "the response
  carries no per-item outcome"). On a non-2xx the client cannot know which row persisted. The
  returned error is the raw client error: reproduced (`TestReproTransferMidwayFailure`) with the
  upstream applying the first row and rejecting the second —
  `rows_applied=1`, `code=validation_failed`, `details=map[store_code upstream_message upstream_status]`
  — no SKU, no quantities, no stage, no compensation hint, and nothing that says the source was
  already decremented.
- **Trigger**: any per-item rejection inside the bulk endpoint (a source item validation error on
  the second row: disabled source, deleted source, quantity rule), or a 5xx mid-batch.
- **Impact**: stock is silently destroyed or created: the source was decremented and the
  destination never credited. The error tells the operator nothing, and a naive retry re-reads the
  already-decremented source and moves the *remaining* quantity — compounding the loss.
- **Proposed fix**: send one item per call with a journal (so the applied row is known), or keep the
  batch and re-read both rows after any failure, returning `observed_from`/`observed_to` and an
  `applied_unknown` marker; add a compensating write that restores the pre-transfer values when the
  second row is known to have failed.
- **Test requirement**: `TestTransferSourceQtyCompensatesMidBatchFailure` (CONTRACT: the fake
  applies row 1 and rejects row 2 → the error reports both observed quantities and the tool either
  restores row 1 or reports `outcome_unknown` with the ledger id);
  `IT-TRANSFER-02` (INTEGRATION: force a per-item rejection with a disabled second source and
  assert the sum of the two sources is unchanged).
- **Verified by**:

### MAG-12: source quantities are integers; fractional MSI quantities are truncated by a transfer

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/inventory.go:30` (`Quantity int`), `:188` (`fromQty = int(item.Quantity)`), `:199`; `internal/magento/inventory_read.go:36` (`Quantity float64`)
- **Evidence**: the read projection is `float64` (MSI stores `decimal(12,4)`), the write projection
  is `int`, and the transfer converts with a truncating cast. A source holding 10.5 units, asked to
  transfer 3, is written back as 7 — 0.5 units are destroyed, and the destination gets 3.
- **Trigger**: any product sold by weight/volume or any install that uses fractional source
  quantities.
- **Impact**: silent inventory loss of up to one unit per transfer, per source.
- **Proposed fix**: carry quantity as a decimal string (or a fixed-point type) end to end; validate
  the scale (≤4) instead of truncating.
- **Test requirement**: `TestTransferSourceQtyPreservesFractionalQuantities` (UNIT/CONTRACT: 10.5 − 3
  → `7.5` and `3.5` in the body).
- **Verified by**:

### MAG-13: source-item reads truncate at 100 rows and never check `total_count`

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/inventory_read.go:82` (`pageSize=100`), `:90` (only `items` decoded); same pattern at `:171` (`Sources`) and `:194` (`Stocks`)
- **Evidence**: `SourceItems` accepts up to 20 SKUs and sets `searchCriteria[pageSize]=100`, then
  decodes `items` and ignores `total_count`; there is no pagination loop and no truncation flag. 20
  SKUs × 6 sources = 120 rows, so a legal call silently returns a subset.
- **Trigger**: a multi-source install where `skus × sources > 100` (or > 100 sources/stocks for the
  list tools).
- **Impact**: `TransferSourceQty` treats a missing row as quantity 0 — so it either refuses a
  transfer that is actually possible ("source has 0 units") or writes an absolute quantity computed
  from a missing row. Read tools report a complete-looking list that is incomplete.
- **Proposed fix**: paginate until `total_count` is satisfied (or the cap is hit) and return a
  `truncated`/`total_count` field so the caller can tell.
- **Test requirement**: `TestSourceItemsPaginatesBeyondOnePage` (CONTRACT: two pages, `total_count`
  120 → 120 rows or an explicit `truncated: true`).
- **Verified by**:

### MAG-14: `magento.product.media.upload` rejects Magento's integer entry id — every successful upload is reported as a failure

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `internal/magento/media.go:104`, `:109` (string branch), `:125` (struct branch)
- **Evidence**: the decoder tries `string`, then the full entry object:

  ```go
  var idText string
  if err := json.Unmarshal(raw, &idText); err == nil && idText != "" { ... }
  if err := json.Unmarshal(raw, &created); err != nil {
      return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable media payload")
  }
  ```

  Magento returns the new gallery entry id as a JSON **integer**
  (`ProductAttributeMediaGalleryManagementInterface::create(): int`), which the repository's own
  document states: `docs/integrations/magento.md:426` — "the endpoint returns the new `entryId` as
  an integer". A bare `3422` fails both branches. Reproduced
  (`TestReproMediaIntegerEntryID`): `err=upstream_unavailable: Magento returned an unreadable media
  payload`. The two CONTRACT tests encode the *wrong* contract (`media_upload_test.go:28` returns an
  object; `:204` returns `"3422"` as a string), so CI cannot see it.
- **Trigger**: every upload against a real Magento instance.
- **Impact**: the image is stored and the gallery entry created, but the tool reports failure. The
  agent/operator retries (the operation is non-idempotent, `IdempotentHint: false`), producing
  duplicate gallery entries and duplicate files; the returned `media_id` is never available, so
  follow-up `media.update`/`media.delete` calls cannot be made.
- **Proposed fix**: decode `json.Number` first (int or float), then string, then object; keep the
  read-back that resolves the full entry.
- **Test requirement**: `TestUploadMediaIntegerEntryID` (CONTRACT: body `3422` → `entry.ID == 3422`
  and the gallery read-back is used); `IT-MEDIA-01` (INTEGRATION: upload against real Magento,
  assert one gallery entry and a usable `media_id`).
- **Remediation (t10)**: `decodeMediaEntryID` accepts the JSON integer first, then the numeric
  string, then the full entry object; a non-positive or unparsable scalar is `upstream_unavailable`.
  The CONTRACT fixture now encodes the real contract (POST → `9`, GET → the gallery entry) and the
  string/object forms keep their own tests. Guards: `TestUploadMedia`,
  `TestUploadMediaObjectResponse`, `TestUploadMediaScalarResponse`,
  `TestUploadMediaRejectsInvalidEntryID`.
- **Verified by**:

### MAG-15: media upload has no dedupe, no ledger, and no role handling, contrary to the design document

- **Severity**: P2
- **State**: PLANNED (the document describes an unimplemented design)
- **Location**: `internal/magento/media.go:79`; `docs/integrations/magento.md:438`
- **Evidence**: the document specifies "Decoded bytes are hashed (SHA-256) and tracked as
  `sku → hash → entryId`; a repeat with the same hash and roles is skipped … a crash between create
  and role assignment resumes from the recorded `entryId`" and "Assigning `image` to a second entry
  silently moves the role, so the gateway clears the role on the previous holder first". The code
  has no hashing, no record, no previous-holder handling, and no resume path.
- **Trigger**: a retried upload (see MAG-14), or a second image promoted to `image`.
- **Impact**: duplicate files/entries, and a silently demoted base image (the previous holder loses
  the role without the operator being told).
- **Proposed fix**: implement the documented design, or move it to `known-limitations.md` as
  PLANNED and stop implying it in the integration document.
- **Test requirement**: `TestUploadMediaDeduplicatesByHash` (CONTRACT: same bytes + roles twice →
  one POST, second call returns the recorded id); `TestUploadMediaClearsPreviousRoleHolder`
  (CONTRACT: the previous `image` entry is updated before the new role is assigned).
- **Verified by**:

### MAG-16: the custom-option 404 fallback fabricates a successful option

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/product_options.go:101`, `:128`, `:136`
- **Evidence**: on a 404 the code retries the option through `PUT /products/{sku}` and, if that
  returns 200, looks the option up by title; when the lookup finds nothing it returns a synthetic
  object with no error:

  ```go
  return &ProductOption{Title: in.Title, Type: in.Type, IsRequire: in.IsRequire, SortOrder: in.SortOrder}, nil
  ```

  Reproduced (`TestReproProductOptionFallbackFabricates`): POST → 404, PUT → 200, GET options → `[]`
  ⇒ `option=&{OptionID:0 ...} err=<nil>`. Magento's product save does not create custom options
  from an `options` array in the product payload, so the PUT is a no-op that reports success.
- **Trigger**: any instance where `POST /products/{sku}/options` is not exposed (the code's own
  premise for the fallback).
- **Impact**: the caller believes a gift-wrap/engraving option exists; the storefront has none. A
  fabricated `option_id: 0` also poisons later `option.update`/`option.delete` calls.
- **Proposed fix**: treat "PUT succeeded but the option is absent" as `unsupported_capability`, not
  success; only return a real option with a real id.
- **Test requirement**: `TestProductOptionFallbackFailsWhenOptionAbsent` (CONTRACT: PUT 200 + empty
  list → `unsupported_capability`, no fabricated object).
- **Verified by**:

### MAG-17: stage-wrapped errors discard Magento's message, status, and retryability

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/configurable.go:467` (`stageFailure`), `:478` (`writeStageError`), `:490` (`stageMessage`)
- **Evidence**: the wrapper builds a **new** `errs.Error` from `CodeOf(cause)` and the cause's
  `Message` only. `upstream_message`, `upstream_status`, `store_code`, `Op`, and `Retryable` are all
  dropped. Reproduced (`TestReproStageErrorDropsUpstreamMessage`): Magento answers 400 with
  `{"message":"The value of attribute \"color\" is incorrect."}`; the caller sees
  `"Magento write failed at stage parent: Magento rejected the request as invalid; verify
  store_code and field values; completed writes are not rolled back and require compensation"`
  with details `{completed_skus, failed_sku, stage}` — the actual reason is gone.
- **Trigger**: any Magento 400/422 inside a staged workflow (configurable create, order create).
- **Impact**: the operator is told "verify store_code and field values" when the real problem is a
  specific attribute value. Debugging a 50-child create becomes guesswork, and the retry that
  follows re-creates products.
- **Proposed fix**: copy the cause's `Details` (except where redaction is required), keep
  `upstream_status`, and propagate `Retryable` explicitly per stage (never for money-moving stages).
- **Test requirement**: `TestStageErrorPreservesUpstreamMessage` (CONTRACT: a 400 at the parent
  stage surfaces `upstream_message` and `upstream_status`).
- **Verified by**:

### MAG-18: every Magento 5xx is marked retryable, including on non-idempotent writes

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/client.go:178` (`case status >= 500: … retryable = true`), `internal/mcp/toolerr/toolerr.go:120`
- **Evidence**: `statusError` sets `e.Retryable = true` for any 5xx and `errorResult` serialises
  `"retryable": true` into the MCP error envelope, regardless of the tool's `IdempotentHint: false`.
  Magento raises 500 for business failures as well (localized exceptions escaping a save path,
  "Something went wrong", indexer locks), and a 500 after a capture/refund/order placement is
  exactly the ambiguous case where a retry is unsafe.
- **Trigger**: any 5xx on a write tool.
- **Impact**: the gateway actively instructs the agent to repeat a non-idempotent write — the
  mechanism behind MAG-01 and MAG-04.
- **Proposed fix**: never mark a write retryable; make retryability a property of the tool's
  annotation (read tools retryable, writes not) and add `outcome_unknown` for post-write ambiguity.
- **Test requirement**: `TestWriteTool5xxIsNotRetryable` (UNIT: `errorResult` for a write tool never
  contains `retryable`), `TestReadTool5xxIsRetryable` (UNIT: the read counterpart still does).
- **Verified by**:

### MAG-19: `store_id` is accepted by `magento.order.create` and silently dropped

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/order_create.go:45`, `:165`, `internal/magento/write_tools.go:1443`
- **Evidence**: `StoreID` is in the schema, in the input struct, and validated (`< 0` rejected) — and
  then never used. Reproduced (`TestReproOrderStoreIDDropped`): with `store_id: 5` the four requests
  are `POST /customers/7/carts`, `POST /carts/17/items`, `POST /carts/17/shipping-information`,
  `POST /carts/17/order` — no `store_id` anywhere, and no store segment in the path beyond the
  configured `default`.
- **Trigger**: any caller that passes `store_id` (the field is documented in the schema and in
  `docs/magento-fields.md`? — it is not documented there, which makes it worse).
- **Impact**: the order is placed in whatever store the cart API defaults to (the credential's
  store scope), not the requested store view: wrong storefront, possibly wrong currency/tax/
  shipping availability, and a `store_id` on the order that the caller did not ask for.
- **Proposed fix**: send the store where the API accepts it (the admin cart flow derives the store
  from the customer's store or the credential scope; if a per-request store is required, use
  `POST /V1/carts` with the store-scoped base path or reject `store_id` that differs from the
  configured store with `invalid_argument`).
- **Test requirement**: `TestCreateOrderHonoursStoreID` (CONTRACT: the cart creation carries the
  store, or the call is rejected when the store differs); otherwise
  `TestCreateOrderRejectsForeignStoreID` (UNIT).
- **Verified by**:

### MAG-20: addresses cannot express `region_id`

- **Severity**: P2
- **State**: CONFIRMED (code) — the exact upstream failure needs `IT-ORDER-02`
- **Location**: `internal/magento/order_create.go:25`, `:138`; `internal/magento/customer.go:320`
- **Evidence**: `OrderAddressInput` and `CustomerAddressInput` carry `Region string` only; there is no
  `region_id`, and no call to `GET /V1/directory/countries/{countryId}` to resolve it. Magento's
  address validation for countries with a region directory (US, CA, …) resolves a region by id, and
  the string is only a display value.
- **Trigger**: an order or customer address for a country with a region directory.
- **Impact**: the call is rejected ("Please enter a state") or the address is stored with an
  unvalidated region that breaks tax/shipping rate matching.
- **Proposed fix**: accept `region_id` (preferred) plus `region` as a fallback, and resolve/validate
  against the directory endpoint.
- **Test requirement**: `TestOrderAddressSendsRegionID` (CONTRACT: `region_id` in the payload);
  `IT-ORDER-02` (INTEGRATION: US address with region_id → order placed).
- **Verified by**:

### MAG-21: `type_id=bundle` (and `downloadable`) is accepted without the options/links that make the product sellable

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/product_write.go:35` (`writableTypeIDs`), `:374`; `internal/magento/write_tools.go:79` (`typeIDProp`)
- **Evidence**: `writableTypeIDs = {"simple","virtual","downloadable","bundle"}`. Creating a bundle
  performs only `POST /products`; no bundle-option or selection endpoint exists anywhere in the
  package, and no downloadable link/sample endpoint either. The design document expects the schema
  to reject this: `docs/integrations/magento.md:405` lists the intended error
  `invalid_argument: type_id 'bundle' requires bundle_options`.
- **Trigger**: `magento.product.create` with `type_id: bundle` or `downloadable`.
- **Impact**: an enabled, visible, unsellable product is reported as created; the storefront shows a
  bundle with no selections or a downloadable product with no file.
- **Proposed fix**: either implement the option/selection and link/sample endpoints with a
  verification stage, or restrict `writableTypeIDs` (and the schema enum) to `simple`/`virtual`
  until they exist.
- **Test requirement**: `TestCreateProductRejectsBundleWithoutOptions` (UNIT: `invalid_argument`);
  `IT-PRODUCT-BUNDLE-01` (INTEGRATION: if implemented, assert the storefront bundle has selections).
- **Verified by**:

### MAG-22: grouped product creation is not implemented

- **Severity**: P3
- **State**: PLANNED
- **Location**: `internal/magento/product_write.go:35`; `internal/magento/tools.go:47`
- **Evidence**: `grouped` is absent from `writableTypeIDs` and from the create schema enum, and
  `product_links.go` handles only `related`/`upsell`/`crosssell`. The read path *does* accept
  `type_id=grouped` as a filter (`tools.go:47`), so grouped products can be discovered but never
  created or populated.
- **Trigger**: an operator asking the gateway to create a grouped product.
- **Impact**: capability gap only; the failure is a clear `invalid_argument`.
- **Proposed fix**: implement (`type_id=grouped` + `POST /products/{sku}/links` with
  `link_type: "associated"`/`grouped`), or record it in `known-limitations.md`.
- **Test requirement**: `TestCreateProductRejectsGrouped` (UNIT) today;
  `IT-PRODUCT-GROUPED-01` if implemented.
- **Verified by**:

### MAG-23: there is no INTEGRATION test, and the fakes encode the gateway's own assumptions

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `docs/evidence/magento.md:5` ("every behavioural row is `[U]`"), `:97` (a representative row), `test/e2e/fakemagento/main.go:51` (2 routes), `internal/magento/media_upload_test.go:28`, `internal/magento/catalog_test.go:144`
- **Evidence**: the evidence log states "Result: not executed — no credentials or reachable instance
  were provided, so every behavioural row is `[U]`". The process-level fake implements two routes
  (`/rest/default/V1/products` and a catch-all). The unit fakes answer with hand-written JSON that
  matches the *client's* structs, not Magento's contract: the media upload fixture returns an object
  or a string where Magento returns an integer (MAG-14), the attribute-option fixture returns
  `{"label","value"}` where Magento returns an id (MAG-26), and `TestCreateInvoice`
  (`invoice_links_test.go:45`) asserts the repository route as if it were the invoicing route
  (MAG-02). Because the fixtures are the only oracle, a wrong contract is indistinguishable from a
  right one.
- **Trigger**: every release.
- **Impact**: the P0/P1 contract defects in this report are invisible to CI; the repository cannot
  detect a Magento upgrade that changes a response shape (media id, attribute option id, invoice
  route).
- **Proposed fix**: a gated INTEGRATION suite (build tag `integration`, `MAGENTO_BASE_URL` +
  `MAGENTO_ACCESS_TOKEN`) against a disposable Magento 2.4.x (docker) covering: product create with
  quantity, configurable create + children + salable qty, source transfer, media upload, invoice +
  capture, credit memo, order create, and the 400/409 business-error mapping. Record the observed
  contract in `docs/evidence/magento.md` and diff it in CI nightly.
- **Test requirement**: the suite itself is the requirement; minimum `IT-CONFIGURABLE-01`,
  `IT-TRANSFER-01`, `IT-CAPTURE-01`, `IT-INVOICE-01`, `IT-REFUND-01`, `IT-ORDER-01`, `IT-MEDIA-01`.
- **Verified by**:

### MAG-24: the configurable price relationship is documented backwards

- **Severity**: P3
- **State**: DOC-ONLY
- **Location**: `internal/magento/write_tools.go:56`, `docs/write-safety.md:42`
- **Evidence**: the tool description says "The parent price is derived from its children", and the
  write-safety prompt says "The configurable parent price is derived from its children; a requested
  parent price may read back as 0." The code does the opposite: a child with an empty price inherits
  the parent price (`configurable.go:157`), and the parent price is sent verbatim
  (`configurable.go:181`).
- **Trigger**: an operator pricing a configurable from the documentation.
- **Impact**: an agent may set only child prices and leave the parent at 0, which is exactly the
  outcome the doc warns about but the code does not produce.
- **Proposed fix**: correct both texts to "children inherit the parent price unless a child price is
  supplied; the parent price is sent as given".
- **Test requirement**: documentation review (DOC); the actual behaviour is already covered by
  `configurable_test.go:121`, which asserts the child inherits `699` from the parent.
- **Verified by**:

### MAG-25: the capability matrix calls sales read-only

- **Severity**: P3
- **State**: DOC-ONLY
- **Location**: `docs/integrations/magento.md:19`
- **Evidence**: "| Sales | Read-only orders, invoices, shipments, credit memos, aggregate analytics |"
  while the package ships twelve sales write tools (`write_tools.go:281`: invoice create/capture/
  email, shipment create/email, credit memo create/email, order create/comment/hold/unhold/cancel/
  email) and `docs/implemented-tools.md` documents them as write + confirmation. The write routes
  are absent from the route matrix entirely, which is how MAG-02 went unnoticed.
- **Trigger**: a reviewer or operator reading the integration document before enabling writes.
- **Impact**: the highest-risk tools in the product are undocumented in the document that is
  supposed to be the endpoint source of truth.
- **Proposed fix**: add a "sales writes" section with the workflow routes
  (`/V1/order/{id}/invoice`, `/V1/order/{id}/ship`, `/V1/order/{id}/refund`, `/V1/invoices/{id}/capture`, `/V1/invoices/{id}/void`, `/V1/creditmemos`),
  their idempotency and risk, and `[V-docs]`/`[U]` tags; correct the capability row.
- **Test requirement**: documentation review (DOC).
- **Verified by**:

### MAG-26: `CreateAttributeOption` rejects a numeric option id

- **Severity**: P3
- **State**: CONFIRMED (code) — needs `IT-ATTR-01` for the observed shape
- **Location**: `internal/magento/catalog.go:265`, `:269`; fixture `internal/magento/catalog_test.go:144`
- **Evidence**: the decoder tries `string`, then `{label,value}`. A bare numeric id (`123`) fails
  both. Magento's `ProductAttributeOptionManagementInterface::add` returns the new option id, whose
  JSON type varies by build (string in the interface docblock, integer in several deployments); the
  fixture only exercises the object form.
- **Trigger**: an instance that serialises the option id as a number.
- **Impact**: the option is created and the tool reports `upstream_unavailable`; the caller cannot
  learn the `value_index` needed to build a configurable variant, and a retry creates a second
  option.
- **Proposed fix**: decode `json.Number` first, then string, then object.
- **Test requirement**: `TestCreateAttributeOptionNumericID` (CONTRACT: body `123` → `Value == "123"`);
  `IT-ATTR-01` (INTEGRATION: create an option and read it back).
- **Verified by**:

---

## 4. The state-machine gaps, spelled out

The task asked for the configurable, inventory, and finance gaps as concrete interleavings. Each
timeline below is what the current code does, step by step.

### 4.1 Configurable create: partial failure, then the documented recovery

`CreateConfigurable` (`configurable.go:114`) executes: `children[0..n]` → `parent` →
`links[0..n]` → `stock`. There is no `PRECHECK` (nothing is read before the first write), no `PLAN`
(no operation id, no journal), no `VERIFICATION` (nothing is read after the last write), and no
`APPROVAL` beyond the generic confirmation, which is consumed before the first REST call.

**Interleaving C1 — failure at the parent stage.**

1. `POST /products` for children 1..3 succeeds. Magento now holds 3 simple products.
2. `POST /products` for the parent returns 400 (the attribute set does not contain the variant
   attribute, or the URL key collides).
3. The error says `stage=parent, completed_skus=CH-1,CH-2,CH-3, failed_sku=PARENT` and adds
   "completed writes are not rolled back and require compensation". Magento's own reason is
   **discarded** (MAG-17).
4. The operator fixes the attribute set and re-runs. `POST /products` for `CH-1` now returns 400
   ("SKU already exists") ⇒ `stage=children, completed_skus=""` (reproduced). The operator learns
   nothing about the 3 products that exist, and the tool offers no way to resume from step 4.
5. The only supported path is to delete the children by hand and start over — and if there were 50
   children, the list of what to delete was truncated at 20/900 bytes (MAG-06), with the 16th entry
   possibly a partial token.

**Interleaving C2 — link failure (the common case).**

1. Children 1..3 and the parent are created; `POST /configurable-products/PARENT/child` for child 1
   succeeds, for child 2 returns 409.
2. Error: `stage=links`, `completed_skus=CH-1,CH-2,CH-3,PARENT`, `failed_sku=CH-2`. This list does
   **not** say which links exist; it is derivable only because the loop is ordered.
3. Re-run aborts at child 1 with 400 (as in C1). The half-linked parent stays half-linked; the
   storefront shows a configurable with one option value missing.

**Interleaving C3 — `CreateVariations` link failure (MAG-07).**

1. Child `P-1-RED` is created; the link returns 500.
2. Row status `failed`, but the product exists (no `product_created` marker, unlike `Create`).
3. Retry: `GET /products/P-1-RED` succeeds ⇒ row status `exists`, `Skipped++`, `continue` — the
   link is never attempted again. Reproduced: `link_attempts=1`.

**Interleaving C4 — stock stage (MAG-08, MAG-09).**

1. Everything above succeeds; `PUT /products/PARENT/stockItems/{id}` returns 500.
2. The error is swallowed; the tool returns 200 with `parent_in_stock: false`.
3. No child had a quantity, so no source item exists for any child; MSI salable quantity is 0.
4. The storefront shows the parent in stock (the legacy flag was the only thing the gateway tried to
   set) or out of stock (if MSI wins) — and no tool can read salable quantity to tell which.

**Required machine** (proposal): `PRECHECK` → `PLAN` → `APPROVAL` → `EXECUTION` (journal) →
`VERIFICATION` (`Children`, options, salable quantity) with `resume`/`compensate`/`status` tools
keyed by an operation id. Until that exists, the honest description of the tool is "a best-effort
multi-call script that can leave the catalog in an unknown state".

### 4.2 MSI transfer: the lost-update interleaving, named

`TransferSourceQty` (`inventory.go:164`) is `read → compute → write absolute values`. The state is
two source rows: `A` (from) and `B` (to). Initial state `A=10, B=0`.

**Interleaving I1 — lost update (MAG-10).**

| time | T1 (transfer 3 A→B) | T2 (transfer 3 A→B) | durable state |
|---|---|---|---|
| t0 | `GET` ⇒ A=10, B=0 | | A=10, B=0 |
| t1 | | `GET` ⇒ A=10, B=0 | A=10, B=0 |
| t2 | computes A=7, B=3 | | A=10, B=0 |
| t3 | | computes A=7, B=3 | A=10, B=0 |
| t4 | `POST` A=7, B=3 | | A=7, B=3 |
| t5 | | `POST` A=7, B=3 | **A=7, B=3** |

Expected after two transfers of 3: `A=4, B=6`. Actual: `A=7, B=3`. Three units are phantom stock in
A; B is short by 3. Reproduced deterministically by making both reads observe the same snapshot
(`TestReproTransferLostUpdate`).

**Interleaving I2 — mid-batch partial write (MAG-11).**

| time | actor | action | durable state |
|---|---|---|---|
| t0 | client | `GET` ⇒ A=10, B=0 | A=10, B=0 |
| t1 | client | `POST /inventory/source-items` with `[{A,7},{B,3}]` | A=10, B=0 |
| t2 | Magento | saves item 1 (A=7) | A=7, B=0 |
| t3 | Magento | item 2 rejected (source disabled / rule violation) ⇒ 400 | **A=7, B=0** |
| t4 | client | returns the raw 400: no SKU, no quantities, no stage | A=7, B=0 |

Three units have vanished from A and never reached B, and the error does not say so. A retry now
reads A=7 and moves 3 more ⇒ `A=4, B=3`, i.e. the second attempt "succeeds" while 3 units are gone
for good.

**Interleaving I3 — stale read.** If `GET /inventory/source-items` is served from a lagging
replica, the same absolute write silently *restores* stock that another operation already consumed.

**Missing controls**: no per-SKU serialisation, no transfer id, no ledger, no pre/post read-back, no
compensation. The only protection is the confirmation prompt, which is not a concurrency control.

### 4.3 Finance: capture, refund, and order placement after an ambiguous outcome

**Interleaving F1 — double capture (MAG-01).**

| time | actor | action | durable state |
|---|---|---|---|
| t0 | agent | `magento.invoice.capture` with approval | approval consumed (`server.go:156`) |
| t1 | gateway | `POST /invoices/9/capture` | — |
| t2 | Magento | captures; the gateway/PSP is slow or the proxy returns 502 | **money moved** |
| t3 | gateway | 30 s deadline ⇒ `upstream_timeout: … the call may have completed` (or 5xx ⇒ `retryable: true`) | money moved |
| t4 | agent/operator | re-approves and retries (the tool said the call may have completed / is retryable) | — |
| t5 | gateway | `POST /invoices/9/capture` again | **second capture attempt** |

Reproduced: two capture requests reach the fake upstream from one logical operation. Whether Magento
rejects the second attempt depends on the payment method and its `canCapture()`/`amount_paid` state;
for any payment integration without its own idempotency key it is a second charge, and the gateway
has no way to know which happened.

**Interleaving F2 — duplicate credit memo (MAG-03).** Same shape with no `invoice_id` and no
refundable-quantity pre-check: the second POST creates a second credit memo (and, on instances whose
route executes the refund, a second refund). The client cannot detect it afterwards without manually
calling `OrderCreditmemos`.

**Interleaving F3 — duplicate order (MAG-04).** A timeout on `POST /carts/{id}/order` after Magento
committed the order leaves the gateway with a stage error and a deleted cart; a retry builds a new
cart and places a second order. Nothing in the response tells the operator that the first order
exists, and there is no lookup to reconcile against.

**Missing controls across all three**: an operation record (intent/attempt/observation), an
idempotency key passed to Magento where the route supports one, a pre-flight state read, a
post-write verification, and an `outcome_unknown` error class that blocks a blind retry. The read
primitives for verification already exist (`GetInvoice`, `OrderInvoices`, `OrderCreditmemos`,
`GetCreditmemo`, `OrderStatusHistory`) and are unused by every write path.

---

## 5. Contract-fidelity matrix

| Surface | Gateway behaviour | Magento contract | Verdict |
|---|---|---|---|
| Product create | `POST /products`, `custom_attributes[]`, `extension_attributes.{website_ids,category_links}` | `ProductRepositoryInterface::save`; extension attributes correct | OK |
| Product update | `PUT /products/{sku}`, only supplied fields | merge semantics; correct | OK |
| `type_id` | `simple`, `virtual`, `downloadable`, `bundle` | bundle/downloadable need options/links that the gateway cannot create | MAG-21 |
| Configurable create | `POST /products` with `extension_attributes.configurable_product_options` | supported (`ConfigurableProduct` extension attributes) | OK |
| Configurable links | `POST /configurable-products/{sku}/child` per child | `LinkManagementInterface::addChild` | OK, but no resume (MAG-05/07) |
| Configurable parent stock | derives `is_in_stock` from the children's source items, writes it both ways, reports `parent_stock_source` | legacy stock item plus MSI salable quantity | MAG-09 (fixed in t10; reservations not applied) |
| Variant `attribute_id` | string (`strconv.Itoa`) | `OptionInterface::getAttributeId(): string` | OK |
| `value_index` | int | int | OK |
| `category_links.category_id` | string | `CategoryLinkInterface::getCategoryId(): string` | OK |
| Category create/update | `POST` / `PUT /categories`, `custom_attributes` for `url_key` | OK; `include_in_menu` accepted as a top-level key | OK |
| Category product assign | `{"productLink": {...}}` | `CategoryLinkManagementInterface::assignProductToCategory($categoryId, $productLink)` | OK |
| Media upload body | `{"entry": {..., content:{base64_encoded_data,type,name}}}` | `ProductAttributeMediaGalleryManagementInterface::create` | OK |
| Media upload response | accepts the **integer** entry id, the numeric string, and the object | returns an **integer** entry id (`docs/integrations/magento.md:426`) | MAG-14 (fixed in t10) |
| Attribute option create | expects string or `{label,value}` | returns the option id (string or int by build) | MAG-26 |
| MSI source items | `POST /inventory/source-items`, absolute quantities | `SourceItemsSaveInterface`; per-item save, no documented atomicity | MAG-10/11 |
| MSI source quantity type | `int` on write, `float64` on read | `decimal(12,4)` | MAG-12 |
| MSI reads | `pageSize=100`, no pagination | `searchCriteria`, `total_count` | MAG-13 |
| Invoice create | `POST /invoices` `{entity:{order_id,capture,items}}` | repository save; capture belongs to `InvoiceOrderInterface` at `POST /order/{id}/invoice` | **MAG-02** |
| Credit memo create | `POST /creditmemos` `{entity:{...}}` | repository save; refund workflow at `POST /order/{id}/refund` | MAG-02/03 |
| Shipment create | `POST /shipments` `{entity:{...}}` | repository save; workflow at `POST /order/{id}/ship` | MAG-02 |
| Invoice capture | `POST /invoices/{id}/capture` | `InvoiceManagementInterface::capture` — correct route, no idempotency | MAG-01 |
| Order create | cart → items → shipping-information → order | correct flow; `store_id` unsupported on the cart route | MAG-04/19 |
| Address | `region` string only | `region_id` for directory countries | MAG-20 |
| `quote_id` | string | `CartItemInterface::getQuoteId(): string` | OK |
| Business errors | 400/422 → `validation_failed` + `upstream_message`; 409 → `conflict` | Magento sends `{message, parameters}`; `parameters` ignored | OK (message), see MAG-17 for staged flows |
| 5xx on a write | `upstream_unavailable`, `retryable: true`, surfaced to the agent | 5xx is used for business failures too | MAG-18 |
| Store scope | one store code per client (path); writes land in the default scope | Magento ignores the path store for writes | documented limitation; no per-store value writes exist (PLANNED) |
| Extension attributes (orders/quotes) | not used | not required by the flows implemented | OK |

---

## 6. Exact tests required to close each CONFIRMED P0/P1

`CONTRACT` = fake HTTP server, must run in CI. `INTEGRATION` = real Magento 2.4.x, gated by
`MAGENTO_BASE_URL`/`MAGENTO_ACCESS_TOKEN` and a `integration` build tag.

| Finding | Test | Kind | Assertion |
|---|---|---|---|
| MAG-01 | `TestCaptureInvoiceRejectsSecondCapture` | CONTRACT | one ledger `sent` row; second call fails before any HTTP request |
| MAG-01 | `TestCaptureInvoiceTimeoutRecordsUnknownOutcome` | CONTRACT | stalling upstream → `outcome_unknown`, `Retryable=false`, ledger row present |
| MAG-01 | `TestCaptureInvoiceVerifiesStateAfterSuccess` | CONTRACT | `GET /invoices/{id}` called; `state != 2` ⇒ failure |
| MAG-01 | `IT-CAPTURE-01` | INTEGRATION | capture once, retry, `amount_paid` unchanged, second attempt refused |
| MAG-02 | `TestCreateInvoiceUsesInvoiceOrderRoute` | CONTRACT | path `/V1/order/42/invoice`, `capture` at top level, no `entity` wrapper |
| MAG-02 | `TestCreateInvoiceVerifiesOrderState` | CONTRACT | post-write `GET /orders/42`; unchanged `total_invoiced` ⇒ failure |
| MAG-02 | `IT-INVOICE-01` | INTEGRATION | invoice+capture; order state `processing`, `amount_paid == grand_total`, one grid row |
| MAG-03 | `TestCreateCreditmemoRefusesOverRefund` | CONTRACT | pre-flight shows qty 1 refunded; request 2 ⇒ `validation_failed`, zero POSTs |
| MAG-03 | `TestCreateCreditmemoIsIdempotentPerOperation` | CONTRACT | same operation id twice ⇒ one POST |
| MAG-03 | `IT-REFUND-01` | INTEGRATION | refund 1 of 2, retry, one credit memo, `total_refunded` unchanged |
| MAG-04 | `TestCreateOrderIsIdempotentPerOperation` | CONTRACT | same operation id ⇒ one `POST /carts/{id}/order` |
| MAG-04 | `TestCreateOrderTimeoutDoesNotDeleteCartAndReportsUnknown` | CONTRACT | `outcome_unknown`, no `cart_cleanup`, reconciliation hint present |
| MAG-04 | `IT-ORDER-01` | INTEGRATION | delayed response, retry, exactly one order and one authorisation |
| MAG-05 | `TestCreateConfigurablePrecheckRejectsExistingSKU` | CONTRACT | existing parent/child ⇒ `conflict` + `existing_skus`, zero writes |
| MAG-05 | `TestCreateConfigurableResumeAfterChildFailure` | CONTRACT | journal → resume creates only missing children, completes links |
| MAG-05 | `TestCreateConfigurableVerifiesResult` | CONTRACT | `GET /children` mismatch ⇒ verification failure |
| MAG-05 | `IT-CONFIGURABLE-01` | INTEGRATION | create + resume + delete; GraphQL `variants` count and `stock_status` correct |
| MAG-06 | `TestCreateConfigurableCompletedSKUsWholeTokens` (**implemented, t10**) | CONTRACT | 40 × 59-byte SKUs and a 20-entry-cap case: every reported token is an input SKU, `completed_omitted`/`completed_total` stated |
| MAG-06 | `TestConfigurableFailureExposesFullJournal` (open — needs the MAG-05 journal) | CONTRACT | failure result carries all created SKUs or a resumable operation id |
| MAG-07 | `TestCreateVariationsRepairsOrphanOnRetry` | CONTRACT | child exists, link absent ⇒ retry links it, status `linked` |
| MAG-07 | `IT-VARIATIONS-01` | INTEGRATION | forced link failure then retry ⇒ child linked |
| MAG-08 | `TestCreateConfigurableFailsAtStockStage` (**implemented, t10**) | CONTRACT | 500 on stock write ⇒ error with `stage=stock_status` + completed SKUs |
| MAG-08 | `IT-CONFIGURABLE-02` | INTEGRATION | `is-product-salable` true after create |
| MAG-09 | `TestCreateConfigurableRequestSequence` + `TestCreateConfigurableAssignsChildQuantity` (**implemented, t10**) | CONTRACT | source items are read back; zero child stock ⇒ `is_in_stock=false`/`parent_in_stock=false`; observed stock ⇒ `is_in_stock=true` + `parent_stock_source` |
| MAG-09 | `IT-CONFIGURABLE-03` | INTEGRATION | zero-stock children ⇒ GraphQL `stock_status` OUT_OF_STOCK |
| MAG-10 | `TestTransferSourceQtySerialisesConcurrentTransfers` | CONTRACT | shared snapshot ⇒ 4/6 or `conflict`; never 7/3 twice |
| MAG-10 | `TestTransferSourceQtyRecordsLedger` | CONTRACT | ledger row with pre/post quantities |
| MAG-10 | `IT-TRANSFER-01` | INTEGRATION | two concurrent transfers, sum conserved |
| MAG-11 | `TestTransferSourceQtyCompensatesMidBatchFailure` | CONTRACT | row 1 applied, row 2 rejected ⇒ observed quantities reported and row 1 restored |
| MAG-11 | `IT-TRANSFER-02` | INTEGRATION | per-item rejection ⇒ sum of sources unchanged |
| MAG-14 | `TestUploadMedia` (+ `TestUploadMediaObjectResponse`, `TestUploadMediaScalarResponse`, `TestUploadMediaRejectsInvalidEntryID`) (**implemented, t10**) | CONTRACT | integer body ⇒ success with `entry.ID == 3422` and the gallery read-back; string and object still accepted; invalid scalar rejected |
| MAG-14 | `IT-MEDIA-01` | INTEGRATION | one gallery entry, usable `media_id` |
| MAG-23 | the INTEGRATION suite itself | INTEGRATION | CI job with a disposable Magento; nightly contract diff |

---

## 7. What could not be verified

Stated plainly, per the programme rules:

1. **No INTEGRATION evidence exists.** Nothing in this report is an observed Magento behaviour; the
   upstream half of MAG-02, MAG-20, MAG-21 and MAG-26 is derived from Magento's service contracts
   (`InvoiceOrderInterface`, `ProductAttributeMediaGalleryManagementInterface`, the admin OpenAPI
   schema) and from the repository's own `[V-docs]` claims, not from a live instance.
2. **MSI bulk atomicity is not documented by Adobe.** MAG-11's premise is that
   `SourceItemsSaveInterface` iterates items without a transaction; the client-side consequence (one
   row can persist, the error carries no per-item outcome) is certain from the code, the upstream
   iteration is `[V-docs]` and must be confirmed by `IT-TRANSFER-02`.
3. **Payment-method behaviour on a second capture** is integration-specific (offline methods are
   no-ops; gateways vary). The P0 stands because the gateway provides no protection and actively
   invites the retry, not because every method double-charges.
4. **Whether Magento rejects a second `POST /V1/invoices` or silently persists it** was not observed;
   both outcomes are bad (MAG-02).
5. **Store-scope semantics for writes** were not exercised; the code pins one store code per client
   and Magento applies writes at the default scope, so per-store value writes are not supported by
   this gateway (PLANNED, not counted as a finding).
6. **Fractional source quantities** (MAG-12) are certain at the Go boundary; whether a given install
   uses them is deployment-specific.

---

## 8. Scope compliance

Audit only. No file under `internal/` was modified (`git status --short internal/magento` is empty);
the reproduction tests live in `/tmp` and are injected with `-overlay`. The only file written by this
task is this report.

---

## 9. Appendix — reproduction harness

`/tmp/magrepro/repro_test.go` (`package magento`) plus:

```json
{"Replace": {"/Users/shuaizeng/Documents/mage2mcp/internal/magento/zz_audit_repro_test.go":
             "/tmp/magrepro/repro_test.go"}}
```

```bash
go test -overlay /tmp/magrepro/overlay.json ./internal/magento/ -count=1 -run TestRepro -v
```

Output (trimmed to the assertions):

```text
REPRO media int id: entry=<nil> err=upstream_unavailable: Magento returned an unreadable media payload
REPRO rerun: code=validation_failed stage=children completed="" failed="PHONE-01-BLK" parent_attempts=0
REPRO variations: first=[{SKU:P-1-RED Status:failed ...stage links...}] second=[{SKU:P-1-RED Status:exists}] link_attempts=1
REPRO transfer write 1: from=7 to=3
REPRO transfer write 2: from=7 to=3
REPRO midway: code=validation_failed details=map[store_code upstream_message upstream_status] rows_applied=1
REPRO capture 1: code=upstream_timeout retryable=false
REPRO capture 2: err=<nil> captures_seen_by_magento=2
REPRO parent stock: err=<nil> parent_in_stock=false
REPRO completed_skus: bytes=900 entries=16 truncated_marker=false last_entry_is_partial=true
REPRO finance request: POST /rest/default/V1/invoices {"entity":{"order_id":42,"capture":true,...}}
REPRO finance request: POST /rest/default/V1/creditmemos {"entity":{"order_id":42,...}}
REPRO order request: POST /rest/default/V1/customers/7/carts
REPRO order request: POST /rest/default/V1/carts/17/items
REPRO order request: POST /rest/default/V1/carts/17/shipping-information
REPRO order request: POST /rest/default/V1/carts/17/order
REPRO stage error: message="Magento write failed at stage parent: Magento rejected the request as invalid; verify store_code and field values; ..." details=map[completed_skus failed_sku stage]
REPRO option fallback: option=&{OptionID:0 Title:Gift wrap ...} err=<nil>
```

Twelve tests, all PASS (each asserts the defective behaviour is present, so a future fix turns the
test red and the test must be inverted into the regression suite listed in §6).
