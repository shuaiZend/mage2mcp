# Live test defect list and fix proposals

| Field | Value |
|---|---|
| Source | [`live-magento-test-report.md`](live-magento-test-report.md) — v0.11.1 against a real Magento 2 store, 2026-11-21 |
| Scope | Defects found by exercising the tools against `https://demo-magento-2.auroracreation.com` (store view `pl`) |
| Status | Designs only — no code was changed while testing |

Every defect below was reproduced against the live store. Where the gateway's own
error message blamed the store, the direct call is recorded so the attribution is
not taken on trust.

---

## Summary

| ID | Severity | Area | Defect | Fix size | Status |
|---|---|---|---|---|---|
| LIVE-1 | **P1** | order | Order placement uses `POST`; Magento requires `PUT` | one word | **fixed, verified live** |
| LIVE-2 | P2 | catalog | `product.links.add` sends `entity`; the contract wants `items` | small | **fixed, verified live** |
| LIVE-3 | P2 | inventory | `inventory.source_create` cannot parse a successful `[]` response | small | **fixed, verified live** |
| LIVE-4 | P2 | customer | `customer.addresses.create` gives up instead of using the working `PUT /customers/{id}` fallback | medium | **fixed, verified live** |
| LIVE-5 | **P1** | reporting | `total_count` is omitted from three `fields` projections, so `total` is always 0 and `sales.summary` never reports truncation | small | **fixed, verified live** |
| LIVE-6 | P3 | observability | Log redaction hides non-secret fields (`authorization_enabled`) | small | **fixed, verified live** |
| LIVE-7 | **P1** | reliability | The reconciler enqueues to the `maintenance` queue, but the worker's default queues did not include it, so no UNKNOWN operation was ever reconciled | one line | **fixed, verified live** |
| LIVE-8 | **P1** | order | An ambiguous placement that was retried-empty was recorded as a definite `failed` (`reconciled_not_applied`) for an order that existed; and the observer read `/orders/{customerID}`, which can report an unrelated order as applied | medium | **fixed, verified live** |
| MAG-20 | **P1** | order | Addresses could not express `region_id`; Magento rejects Polish addresses without it | small | **fixed, verified live** |

LIVE-1 and LIVE-5 are the two that matter: one blocks the entire money path, the
other makes completeness claims wrong.

---

## LIVE-1 (P1): order placement uses POST instead of PUT

- **Location**: `internal/magento/order_create.go:163`
- **Code**: `s.client.do(ctx, http.MethodPost, "/carts/"+strconv.Itoa(cartID)+"/order", nil, orderBody)`
- **Evidence** (direct calls, same token, same cart):

  ```text
  POST /rest/pl/V1/carts/9/order  -> 404 {"message":"Wniosek nie odpowiada żadnej ścieżce."}
  PUT  /rest/pl/V1/carts/9/order  -> 400 {"message":"Some addresses can't be used ..."}
  ```

  A 404 on POST and a *business* 400 on PUT is conclusive: the route exists only as
  `PUT`. Magento's `salesOrderManagementV1` defines both `PUT /V1/carts/mine/order`
  and `PUT /V1/carts/{cartId}/order`.
- **Impact**: `magento.order.create` cannot place an order on a standard Magento 2
  instance. The whole downstream chain — invoice, capture, shipment, credit memo —
  is unreachable, which is why 21 cases in the live matrix were skipped. The error
  the gateway returns is `unsupported_capability: this Magento instance does not
  expose the cart place-order route`, which blames the store for the gateway's own
  wrong method. That makes the defect worse than a plain failure: an operator would
  conclude their store is at fault.
- **Fix**: change `http.MethodPost` to `http.MethodPut` on that call. Nothing else
  changes: the body, the ledger integration and the reconciliation logic are
  correct.
- **Regression test**: a CONTRACT test asserting the method and path
  (`PUT /carts/{id}/order`) — the existing test suite asserted the sequence but not
  the verb, which is exactly how this survived. Add an assertion on the recorded
  method per step of the cart flow.

## LIVE-2 (P2): `product.links.add` sends the wrong body shape

- **Location**: `internal/magento/product_links.go:76` and `:104`
- **Code**: `body := productLinkBody{Entity: in}` where
  `type productLinkBody struct { Entity ProductLinkInput \`json:"entity"\` }`
- **Evidence**:

  ```text
  POST /products/WJ01/links {"entity":{"sku":"WJ01","link_type":"related","linked_product_sku":"WJ02","position":1}}
    -> 400 {"message":"\"%fieldName\" is required ...","parameters":{"fieldName":"items"}}
  POST /products/WJ01/links {"items":[{"sku":"WJ01","link_type":"related","linked_product_sku":"WJ02","position":1}]}
    -> 200 true
  ```

- **Impact**: adding a related/upsell/cross-sell link always fails with
  `validation_failed`. `product.links.bulk` shares the same shape and fails
  identically. Reading links (`product.links`) and removing them work.
- **Fix**: send `{"items": [ {sku, link_type, linked_product_sku, position} ]}`.
  Keep the single-item input and wrap it; do not change the tool schema.
- **Regression test**: assert the request body key is `items` and that a
  one-element array is sent. The current CONTRACT fixtures encode the gateway's own
  assumption, which is why the mismatch survived.

## LIVE-3 (P2): `inventory.source_create` cannot parse a successful response

- **Location**: the source-creation response decode in `internal/magento/inventory.go`
  (the `CreateSource` path)
- **Evidence**:

  ```text
  POST /rest/pl/V1/inventory/sources {"source":{...}} -> 200 []
  ```

  Magento returns an empty JSON array for a successful create. The gateway reports
  `upstream_unavailable: Magento returned an unreadable inventory-source payload`.
- **Impact**: an MSI source cannot be created through the gateway, and the error
  says the upstream payload was unreadable when in fact the operation succeeded.
  Worse than a plain failure: the caller cannot tell that the source now exists.
- **Fix**: treat an empty array — and an empty body — as success for a create
  operation, then read the source back with `GET /inventory/sources/{code}` to
  return a populated object. Do not attempt to decode a single object from a
  response that is a list.
- **Regression test**: a CONTRACT test whose fake returns `[]` with `200`, asserting
  the tool reports success and the read-back is attempted.

## LIVE-4 (P2): `customer.addresses.create` does not use the working fallback

- **Location**: the customer address creation path in `internal/magento/customer.go`
- **Evidence**:

  ```text
  POST /rest/pl/V1/customers/1/addresses -> 404 no route
  POST /rest/pl/V1/addresses            -> 404 no route
  PUT  /rest/pl/V1/customers/1 {customer:{...,addresses:[...]}} -> 200
  ```

  The gateway returns `unsupported_capability: this Magento instance does not
  expose customer address creation; update the customer with an addresses array or
  use the admin UI`. The advice in that message is correct — and unimplemented.
- **Impact**: `customer.addresses.create` cannot create an address on this store,
  although a supported route exists. The tool tells the caller what to do manually
  instead of doing it.
- **Fix**: implement the fallback the error already recommends — on a 404 from the
  address routes, `PUT /customers/{customerId}` with the new address appended to
  `addresses[]`, then read the customer back and return the address the server
  assigned. Keep the `unsupported_capability` error only when both paths fail.
- **Regression test**: a CONTRACT test where the address POST 404s and the customer
  PUT succeeds, asserting the tool returns a created address.

## LIVE-5 (P1): `total_count` is dropped by the `fields` projection

- **Location**: `internal/magento/order.go:31` (`orderSearchField`),
  `internal/magento/order.go:114` and `:193`, `internal/magento/customer.go:83`
- **Code**:

  ```go
  orderSearchField = "items[entity_id,increment_id,…]"   // no total_count
  query.Set("fields", "items[id,group_id,…]")             // no total_count
  ```

  The response is decoded into a struct with a `total_count` JSON field, but the
  projection tells Magento not to send that field, so it is always zero.
- **Evidence**:

  ```text
  GET /orders?fields=items[entity_id]&pageSize=2            -> keys ['items'], total_count None
  GET /orders?fields=items[entity_id],total_count&pageSize=2 -> total_count 2
  ```

  Observed through the gateway: `magento.order.search` returns 2 orders with
  `"total": 0`; `magento.customer.search` returns `"total": 0`; `product.search`
  (which uses no projection) correctly returns `"total": 2062`.
- **Impact**:
  - `magento.order.search` and `magento.customer.search` always report `total: 0`,
    so an agent cannot paginate or know how many matches exist.
  - **`magento.sales.summary` is worse.** `SalesSummary` only adopts
    `payload.TotalCount` when it is `> 0`, then clamps `TotalAvailable` up to
    `ScannedOrders`. With `total_count` always zero, `excluded_orders` is always 0
    and `truncated` is always `false`. The audit's PERF-08 fix — "the truncated flag
    must reflect reality" — is defeated by this projection, so a capped scan now
    claims to be complete.
- **Fix**: append `total_count` to both projections
  (`orderSearchField + ",total_count"`, and the customer `fields` string). One line
  each. Then add a CONTRACT test whose fake omits `total_count` to prove the tool
  does not silently treat "absent" as "zero" — either assert it is requested, or
  distinguish `absent` from `0` and report `total_available` as unknown.
- **Regression test**: `sales.summary` over a window larger than the page size must
  report `truncated=true` and a non-zero `excluded_orders`.

## LIVE-6 (P3): log redaction hides non-secret fields

- **Location**: `internal/observability/logger.go` (key-fragment redaction)
- **Evidence** (startup log of the deployed gateway):

  ```json
  {"level":"WARN","msg":"AUTHORIZATION DISABLED: …","auth_enabled":false,"authorization_enabled":"[REDACTED]"}
  ```

  `authorization_enabled` is a boolean, not a secret, but its name contains the
  fragment `auth` so the value is replaced.
- **Impact**: an operator cannot see whether authorization is on from the structured
  field; they must read the message text. Low, but it is the kind of thing that
  makes an audit trail harder to read.
- **Fix**: redact by value shape and by a curated key list rather than a substring
  match on `auth`, or exempt known boolean fields (`*_enabled`, `*_required`).
- **Regression test**: a logger test asserting `authorization_enabled` survives while
  `authorization` and `auth_token` are redacted.

## Deployment documentation gap (P3)

Not a code defect, but it cost a deployment cycle: the config mounted over
`/etc/mage2mcp/config.yaml` must be world-readable inside the container because the
image runs as non-root. A `0600` mount produces `permission denied` and a restart
loop. `docs/operations/deployment.md` should state the required mode (`0644` when
the file holds only environment-variable references).

---

## LIVE-7 (P1, fixed): the reconciler's queue was never polled

- **Location**: `internal/jobs/jobs.go:192` — `WorkerOptions.defaults`
- **Evidence**: `enqueueReconcileLoop` enqueues `platform:reconcile` on the
  `maintenance` queue, but the worker's default queue list was
  `critical, default, import, report`. On the live deployment **70 reconcile jobs
  sat `queued` on `maintenance` and were never claimed**, so no UNKNOWN operation
  was ever resolved.
- **Impact**: this is the P1 that mattered most in practice. The whole
  UNKNOWN → reconciling → resolved design — the thing that makes an ambiguous money
  operation safe — was inert in a default deployment. An operator would see
  `operation_unknown` forever, and the ledger's retry and escalation paths would
  never run.
- **Fix**: add `maintenance` to the default queue list.
- **Verified live**: after deploying the fix, the 70+ queued jobs drained
  (`72 succeeded`), `283` terminal transitions were written, and the previously
  stuck operations resolved — seven to `failed` (the store confirmed nothing was
  placed) and one to `reconciliation_inconclusive`.
- **Regression test**: `TestDefaultQueuesIncludeMaintenance`.

## LIVE-8 (P1, fixed): a placed order was recorded as definitely not placed

The live ledger contained the worst possible row for a money operation:

```text
entity_id 8 | state failed | error_code reconciled_not_applied | reconcile_count 1
```

while order `3` existed on the store, created about one second after the failed
call. The gateway told the caller "the order was not placed … safe to retry with a
new idempotency key" — an instruction that would create a duplicate order.

**Two separate faults, both fixed:**

1. **An empty reconciliation read was treated as proof of non-placement.** The
   window is a race: a real store commits after the HTTP response is lost. Empty
   reads now prove nothing. The discriminator that does hold is the cart — Magento
   consumes it on a successful placement — so a cart that is still readable is
   positive evidence nothing was placed, and only then is `failed` recorded. A
   consumed cart leaves the operation `unknown`.

2. **The observer read an unrelated order.** `magento.order.create` records the
   *customer* id as the entity id (the order does not exist yet), and
   `ObserveOperation` did `GET /orders/{customerID}`. On a store where that order
   id happens to exist, the observer would read someone else's order and could
   report the placement as applied. It now returns inconclusive for this operation
   by construction, without any upstream read.

**Verified live** on the next run: the placement again timed out, the order was
created (`entity_id 4`, customer 3), and the ledger recorded
`unknown / upstream_unavailable` — the safe state. The earlier run's `failed` row
for a placed order is the evidence that the old behaviour was wrong.

**Residual (documented, not a defect):** this store cannot confirm the placement
from the reconciler, so the operation escalates to a human after
`maxReconcileAttempts`. That is the designed terminal state for an operation the
gateway refuses to guess about. Auto-resolving it would need the cart id in the
ledger, which is a schema change worth doing deliberately rather than in a
bug-fix pass.

- **Location**: `internal/magento/order_create.go` — `reconcilePlacedOrder` and the
  `reconcileReads` / `reconcileBackoff` window
- **Evidence** (fixed build, live store): the order **was created**
  (`entity_id 3`, `created_at 17:33:32`, `grand_total 29.5`) while the ledger row
  stayed `unknown` with `upstream_unavailable`, created at `17:33:30.79`. The
  placement call failed ambiguously about a second before the order materialised.
- **What is already correct**: the gateway refuses to guess. It records `unknown`,
  returns `operation_unknown`, and tells the caller to poll instead of retrying —
  that is exactly the designed behaviour and it prevented a duplicate order.
- **What is still wrong**: the reconciliation read does not reliably see an order
  that commits ~1 s after the failed call. The window was widened during this work
  from 3 reads × 250 ms to 6 × 500 ms (≈2.5 s) and the order *still* missed, which
  points at the match rather than the wait: the candidate list came back but no
  candidate carried this cart's `quote_id`, or the `created_at >= since` filter
  excluded the fresh row. The store does expose `quote_id` (verified: order 3
  reports `quote_id: 21`), so the projection is not the problem.
- **Impact**: a successful order is reported as unknown. The operation is safe — no
  duplicate is placed — but an operator must reconcile by hand, and the agent
  cannot proceed to invoice the order. On a store as slow as this demo the failure
  is reproducible.
- **Attempted fix, rejected by evidence**: reconciling through the cart is not
  possible — `GET /carts/{id}/order` is not a route (404), and the cart itself is
  gone after a successful placement (`404 No such entity with cartId`). The search
  path is the only one available.
- **Second fix hypothesis, also rejected by evidence**: filtering the order search
  on `quote_id`. Magento returns `200` with `total_count: 0` for
  `field=quote_id&condition_type=eq&value=21`, so `quote_id` is not a filterable
  attribute even though it is returned in the projection.
- **What was ruled out**: the date format is not the problem. Both
  `created_at gteq 2026-11-20T17:30:00Z` (RFC3339, what the code sends) and
  `created_at gteq 2026-11-20 17:30:00` (`Y-m-d H:i:s`) return the order, so the
  window filter is correct.
- **Remaining direction**: the search path is sound but its inputs are not. The
  observer should be verified against a placement whose cart id is known, and the
  `since` anchor should come from the ledger row rather than from the in-process
  attempt, so a reconciler running minutes later still covers the placement. This
  needs a dedicated contract test rather than another live guess.
- **Test**: a CONTRACT test where the placement returns a 5xx, the customer search
  returns no candidate, and `GET /carts/{id}/order` returns an order — the tool must
  reconcile to `succeeded` rather than `unknown`.

## Suggested order of work

1. **LIVE-1** — one word, unblocks the entire money path and the 21 skipped cases.
2. **LIVE-5** — one line each, restores pagination counts and the completeness claim.
3. **LIVE-2**, **LIVE-3**, **LIVE-4** — small, each with a CONTRACT test that
   asserts the *wire* shape rather than the gateway's assumption.
4. **LIVE-6** and the deployment note — polish.

After LIVE-1 and LIVE-5 land, the live matrix should be re-run: the order, invoice,
shipment and credit-memo domains become reachable, which is where the remaining
unverified risk sits.

## What this says about the existing test suite

Every defect above passed the audit's own contract tests. The reason is consistent:
the fakes encode the gateway's assumptions about the wire contract, so a wrong
method, a wrong body key, or a projection that omits a field all look correct in
the test and fail against a real store. The durable fix is not more assertions on
the same fakes — it is recording the request shape (method, path, body keys) in the
CONTRACT tests, which the review above recommends for each item.
