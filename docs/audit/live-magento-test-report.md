# Live Magento functional test report — v0.11.1

| Field | Value |
|---|---|
| Document | Live functional test of mage2mcp v0.11.1 against a real Magento store |
| Date | 2026-11-21 |
| Target server | `49.51.250.83` (Ubuntu, Docker 29.1.3, 2 vCPU / 1.9 GiB) |
| Gateway version | `v0.11.1`, image `ghcr.io/shuaizend/mage2mcp:v0.11.1` |
| Magento target | `https://demo-magento-2.auroracreation.com`, store view `pl` (PLN, pl_PL) |
| Store contents | 2 062 products, 2 orders, attribute sets 4 and 9–15 |
| Harness | [`test/live/`](../../test/live/) (build tag `live`) |
| Raw results | `test/live/out/results-20261121-005600.json` |

This run closes the gap the audit recorded as **MAG-23**: until now every Magento
claim was verified against the REST contract and a fake server, never against a
real store. It is a one-off manual run, not CI, and it should be read that way.

---

## 1. Deployment

### 1.1 What was deployed

The existing `/opt/mage2mcp` stack (v0.11.0, no data source configured) was
upgraded in place to v0.11.1, as agreed. Before the change the whole directory was
copied to `/opt/mage2mcp.bak.20261121102721`.

| Change | Value |
|---|---|
| Image | `ghcr.io/shuaizend/mage2mcp:v0.11.0` → `v0.11.1` |
| Config | new `config.yaml` mounted over the image's baked `/etc/mage2mcp/config.yaml` |
| Data source | `magento`, instance id `demo`, endpoint the demo store, store code `pl` |
| Credential | `admin:demo_admin:…` — the gateway mints and refreshes its own token |
| Writes | `tools.writes: true` |
| Limits | `requests_per_second: 20`, `burst: 40`, `max_concurrent: 4` — deliberately below the default because the target is a shared public demo |
| Auth | `auth.enabled: false` + `MAGE2MCP_DEV_INSECURE=true`, listener bound to `127.0.0.1` and reached only through an SSH tunnel |
| Body cap | 16 MiB (the write-enabled floor is 12 MiB) |

### 1.2 Startup verification

```console
$ curl -s http://127.0.0.1:8080/readyz
{"status":"ready","tools":102}
```

102 = 101 Magento tools + `platform.info`. This matches the count the registry
documentation predicts for "Magento only, writes enabled, database configured", so
the write gate and the module registration behave as documented on a real
deployment.

Two operational notes from the deployment itself:

- The mounted config must be world-readable inside the container: the image runs as
  a non-root user, so a `0600` file produced `permission denied` on startup. The
  file contains only an environment-variable reference, not a secret, so `0644` is
  the correct mode. Worth documenting in `docs/operations/deployment.md`.
- With authentication disabled the server logs both `AUTHORIZATION DISABLED: …`
  and `authentication disabled: development only`. The warning works.

## 2. Method

- A Go harness (`test/live/`) speaks MCP Streamable HTTP to the deployed gateway
  over an SSH tunnel. It never exposes the gateway port publicly.
- Write tools were driven the way a real client must: call → receive
  `confirmation_required` → a **different identity** (`live-reviewer`) approves
  through `mage2mcp-admin` → retry with the confirmation id. The approval path was
  exercised on every confirmed write, so confirmation binding and single use were
  tested incidentally throughout.
- Every case records tool, arguments, status, taxonomy code, latency and a detail
  string. Results are written to JSON so the numbers below are reproducible from
  the artefact rather than transcribed.
- Cases are ordered: a failed create leaves its identifier unset, so its dependants
  report `skipped` instead of cascading failures. That is why the skip count is
  high — it is a dependency signal, not 26 untested tools.

## 3. Coverage and results

123 cases across 101 distinct tools.

| Status | Count | Meaning |
|---|---|---|
| pass | 78 | Behaviour matched the contract |
| soft-fail | 17 | Failed, but the cause is environmental or a documented limitation — analysed individually in the defect report |
| fail | 2 | Failed and the cause is a gateway defect or an unimplemented capability |
| skipped | 26 | Prerequisite from an earlier failed case was missing |

### Per-domain

| Domain | Pass | Soft | Fail | Skipped | Note |
|---|---|---|---|---|---|
| platform | 2 | 0 | 0 | 0 | tool count, self-listing, `writes_enabled`, instances |
| store | 2 | 0 | 0 | 0 | store views, tax classes |
| catalog-read | 7 | 0 | 0 | 0 | attribute sets/options, category tree/get/products/flat |
| product-read | 9 | 0 | 0 | 0 | get, batch get, search, SEO, tier prices, media, options, links |
| configurable-read | 1 | 0 | 0 | 0 | children of a real configurable product |
| inventory-read | 5 | 0 | 0 | 0 | sources, stocks, source items, status, status batch |
| customer-read | 2 | 0 | 0 | 0 | groups, search |
| sales-read | 10 | 0 | 0 | 0 | order search/get/history/summary, sales summary/analysis, geography, invoice/creditmemo/shipment lists |
| import-read | 2 | 0 | 0 | 0 | validate, batches |
| product-write | 5 | 0 | 0 | 0 | create with full SEO content, read-back, update, SEO audit, delete |
| media-write | 4 | 0 | 0 | 0 | upload, list, update, delete |
| tier-write | 2 | 0 | 0 | 0 | set and read back |
| bulk-write | 1 | 0 | 0 | 0 | bulk status update |
| category-write | 6 | 0 | 0 | 0 | create, update, assign, list, create+delete |
| attribute-write | 2 | 0 | 0 | 1 | option create, bulk create, delete |
| links-write | 2 | 2 | 0 | 0 | add/remove fail — see defect LIVE-2 |
| options-write | 1 | 1 | 1 | 2 | create fails — see §5 |
| configurable-write | 0 | 7 | 0 | 0 | blocked by the harness payload; see §6 |
| inventory-write | 3 | 4 | 0 | 0 | assign/unassign/transfer; source create — see defect LIVE-3 |
| customer-write | 5 | 0 | 1 | 2 | address create — see defect LIVE-4 |
| order-write | 0 | 1 | 0 | 8 | order create — see defect LIVE-1 |
| invoice-write | 0 | 0 | 0 | 5 | blocked by order create |
| shipment-write | 0 | 0 | 0 | 4 | blocked by order create |
| creditmemo-write | 0 | 0 | 0 | 4 | blocked by order create |
| import-write | 4 | 2 | 0 | 0 | execute, start, status, cancel/resume semantics |
| errors | 3 | 1 | 0 | 0 | unknown instance, unknown SKU, schema rejection |

### What passed and is worth stating plainly

- **The full read surface works against a real store.** All 43 read cases passed,
  including SEO audit, tier prices, media, custom options, related links, MSI
  source items, stock status, order search, sales summary and analysis, and
  geography. No read tool returned wrong data.
- **Product creation with full SEO content round-trips.** `product.create` wrote
  name, description, short description, `url_key`, `meta_title`,
  `meta_description`, `meta_keywords`, weight, tax class, website assignment and a
  custom attribute; `product.get` read the `meta_title` back unchanged.
- **Media upload works, including the integer entry id.** This was defect MAG-14
  in the audit; on a real store the upload returned `id: 3420` as an integer and
  the gateway parsed it.
- **Confirmation binding held throughout.** Every write went through
  `confirmation_required` → approve as `live-reviewer` → retry. No write executed
  without approval, and no approval was reused.
- **Error semantics are correct on the live path.** An unknown instance and an
  unknown SKU both returned `not_found`; a schema-invalid call returned
  `invalid_argument` before reaching Magento.

## 4. Decisive evidence for the routing defects

Three of the defects were confirmed by calling Magento directly, so they are not
inferences from the gateway's own error text.

| Route | Method the gateway uses | Live result | Verdict |
|---|---|---|---|
| `/carts/{id}/order` | `POST` | `404` "request does not match any route" | gateway is wrong |
| `/carts/{id}/order` | `PUT` | `400` business validation ("addresses can't be used") | route exists — the gateway should use PUT |
| `/products/{sku}/links` | `POST {"entity":{…}}` | `400` `"%fieldName" is required`, `fieldName: items` | gateway body is wrong |
| `/products/{sku}/links` | `POST {"items":[…]}` | `200 true` | correct shape |
| `/inventory/sources` | `POST` | `200 []` | success returns an **empty array** |
| `/customers/{id}/addresses` | `POST` | `404` no route | gateway is wrong |
| `/customers/{id}` | `PUT` with `addresses[]` | `200` | working alternative the gateway does not use |
| `/products/{sku}/options` | `POST` | `404` no route | store limitation, gateway honest |
| `/customers/{id}/carts` | `POST` | `200` (cart id) | works — so cart creation is fine, only placement is wrong |

## 5. Not testable on this store (environment, not defects)

| Capability | Why | Evidence |
|---|---|---|
| `magento.product.options.create` | The store does not expose `POST /products/{sku}/options` (404), and the product-update fallback is rejected. The gateway reports `unsupported_capability` with both failure paths, which is the honest answer. | §4 |
| Online capture (`invoice.capture`) | Blocked upstream: order creation fails, so no invoice exists to capture. Payment capture also needs a payment method that supports it; `checkmo` is offline. | order chain blocked |
| Inventory transfer between sources | The store exposes only the `default` source, so a real transfer has no valid destination. The same-source rejection was verified instead. | `magento.inventory.sources` returned one source |
| Emails (`*.email`) | Depend on the order/invoice/shipment chain that is blocked. | dependency |

## 6. Harness limitations (stated so the numbers are not over-read)

- **`configurable.create` payload**: the case returns `invalid_argument` from schema
  validation. The contract was corrected mid-run (`values[]` rather than
  `options[]`), but the run that produced these numbers still used a payload the
  schema rejects. The 7 configurable-write soft-fails are therefore **harness
  payload, not product behaviour**, and that domain is effectively untested.
- **`import.batch`** was passed the job id where a batch id is required; it is a
  harness bug, not a product finding.
- The harness does not retry transient network errors; a single failure is recorded.

## 7. Reproduction

```bash
# 1. Deploy (already done on the test server)
ssh ubuntu@49.51.250.83 'cd /opt/mage2mcp && sudo docker compose up -d'

# 2. Tunnel, do not expose the port
ssh -N -f -L 18080:127.0.0.1:8080 ubuntu@49.51.250.83

# 3. Drive the matrix
export MCP_LIVE=1
export MCP_ENDPOINT=http://127.0.0.1:18080/mcp
export MCP_INSTANCE=demo
export MCP_APPROVE_CMD="ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh {id}'"
go run -tags live ./test/live -mode=all
```

## 8. Verdict

The **read surface and the product/catalog/media write surface are confirmed
working against a real Magento 2 store**. The **order → invoice → shipment →
credit-memo chain could not be exercised at all**, because order placement uses the
wrong HTTP method (LIVE-1), and that is a P1 defect in the money path.

The live run therefore *narrows* the audit's residual risk rather than closing it:
catalog is now genuinely verified, and the finance chain is now known to be broken
in a specific, reproducible way that the contract tests could not see.

See [`live-magento-defects.md`](live-magento-defects.md) for the defect list and
fix proposals.

## 9. Fix verification (same day, second and third runs)

After the defects were fixed, the same harness was re-run against a **second
gateway container** on the same server (`m2m-fixed`, port `127.0.0.1:18081`,
same network, database, config and demo store) built from the patched tree. The
production container was left untouched.

| Run | Pass | Soft | Fail | Skipped |
|---|---|---|---|---|
| Baseline v0.11.1 | 78 | 17 | 2 | 26 |
| After the fixes | **81** | 17 | **1** | **24** |

Per-defect outcome:

| Defect | Before | After |
|---|---|---|
| LIVE-1 order placement method | `unsupported_capability`, no order created | **order created on the real store** (`entity_id 3`, `grand_total 29.5`); the call itself now reports `operation_unknown` — see LIVE-7 |
| LIVE-2 `links.add` body | `validation_failed` | **pass** |
| LIVE-3 `inventory.source_create` | `upstream_unavailable` | **pass** |
| LIVE-4 `customer.addresses.create` | `unsupported_capability` | **pass** |
| LIVE-5 `total_count` projections | `total: 0`, `truncated: false` | **`order.search` / `customer.search` / `sales.summary` pass** |
| LIVE-6 log redaction | `authorization_enabled: "[REDACTED]"` | **`authorization_enabled: false`** in the startup log |
| MAG-20 `region_id` | not expressible | **expressible**; verified as the only remaining blocker for PL addresses, then fixed |

The single remaining hard failure is `magento.product.options.create`, which is the
store limitation described in §5 (the demo exposes no custom-option route), not a
gateway defect.

**LIVE-8 changed the picture again.** The reconciler used to record a *definite
failure* for an ambiguous placement whose order had in fact been created — the
worst possible row for a money operation, because the message told the caller it
was safe to retry. Empty reconciliation reads no longer prove anything: only a
surviving cart does. Verified live afterwards: the placement timed out again, the
order was created (`entity_id 4`), and the ledger recorded `unknown` — the safe
state — instead of `failed`.

The operation still cannot be *confirmed* from this store's reconciler, so it
escalates to a human after `maxReconcileAttempts`. That is the designed terminal
state for an operation the gateway refuses to guess about, and it is why
`order-write` still shows 0 pass: the case asserts a confirmed order.

The 24 skipped cases remain dependants of that confirmation, so the invoice,
shipment and credit-memo domains are still unexercised. Fixing LIVE-7 is what
unlocks them.

### 9.1 A P1 the fix verification exposed

Re-running with a worker present revealed that the reconciler's queue was never
polled: `enqueueReconcileLoop` publishes `platform:reconcile` on the `maintenance`
queue, and the worker's default queue list omitted `maintenance`. On the live
deployment **70 reconcile jobs sat queued and were never claimed**, so no UNKNOWN
operation could ever resolve — the mechanism that makes an ambiguous money
operation safe was inert.

After adding `maintenance` to the default list and redeploying the worker: 72
reconcile jobs drained, 283 terminal transitions were written, and the stuck
operations resolved (seven to `failed`, one to `reconciliation_inconclusive`).

This is the strongest argument for running the live harness with a worker: the
unit tests, the contract tests and the first live run all passed without it.
