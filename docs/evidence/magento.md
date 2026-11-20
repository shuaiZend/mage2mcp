# Magento evidence log

**Status:** **NOT EXECUTED** — no live Magento instance or credential was
available. This file records what would be probed and why the result is absent;
it is not evidence of live behaviour.

**Date of record:** 2026-11-18, re-stated for the remediation tree on
2026-11-20. **Scope:** read-only probes against a Magento 2.4.x instance.
**Related:** [Magento integration](../integrations/magento.md),
[known limitations](../audit/known-limitations.md) §1.1.

## 1. Vocabulary

| Label | Meaning |
|---|---|
| **IMPLEMENTED** | Code exists in the tree. |
| **TESTED (CONTRACT)** | Exercised against the documented REST contract and `httptest` fakes. |
| **TESTED (PostgreSQL)** | Exercised against a real PostgreSQL instance. |
| **`[V-docs]`** | Vendor documentation or released source, not a live probe. |
| **`[U]`** | Unverified; a probe is stated. |
| **NOT EXECUTED** | The probe was not run because no instance was reachable. |
| **PLANNED** | Designed, not implemented. |

`INTEGRATION_TESTED` is not used anywhere in this repository. There is no live
Magento integration test, and the fake-Magento e2e harness is labelled
**E2E-with-fake-Magento**, not integration.

## 2. Why nothing was executed

The audit environment had no Magento 2.4.x instance, no admin/integration
credential, and no reachable staging URL. Minting an admin token
(`POST /V1/integration/admin/token`) is itself a write-shaped request and was
out of scope. As a result:

- every **behavioural** Magento claim in the documentation is `TESTED (CONTRACT)`
  or `[U]`, never live-verified;
- the endpoint map and payload shapes are `[V-docs]`, checked against the 2.4.9
  admin OpenAPI schema and released `webapi.xml` files;
- Magento 2.4.9 compatibility is **unverified**, not "verified".

This is the same limitation recorded in
[known-limitations.md](../audit/known-limitations.md) §1.1. The consequence is
explicit there: a field name, id type, extension attribute, or workflow-route
behaviour can still be wrong until a real sandbox runs the contract.

## 3. Probe plan (not run)

These are the read-only checks that would upgrade the claims. They are listed so
the gap is actionable, not to imply they ran.

| Probe | What it would establish | Expected | Result |
|---|---|---|---|
| P1 store views | Auth works; store code is visible | 200, non-empty `items` | NOT EXECUTED |
| P2 REST schema | The instance's own path/method surface | 200, Swagger with `paths` | NOT EXECUTED |
| P3 configurable children/options | Parent/child/option shape | 200; children ≥ 1, options ≥ 1 | NOT EXECUTED |
| P4 attribute set/attribute/options | `frontend_input`, `scope`, option ids | 200; fields present | NOT EXECUTED |
| P5 media roles | Role names for the attribute set | 200, non-empty | NOT EXECUTED |
| P6 MSI sources/stocks/source-items | MSI topology and source-item shape | 200; ≥ 1 source/stock | NOT EXECUTED |
| P7 salable quantity / legacy stock | Aggregated vs source quantity | 200; numeric | NOT EXECUTED |
| P8 orders page | Order search shape and projection | 200; bounded `items` | NOT EXECUTED |
| P9 tier-price read | POST-as-read shape | 200; array | NOT EXECUTED |
| P10 import path presence | Commerce-only routes | present on Commerce, absent on Open Source | `[V-docs]` only |
| P11 invoice workflow route | `POST /V1/order/{orderId}/invoice` honours `capture` | 200; invoice created with capture | NOT EXECUTED |

The gateway does **not** fetch the schema at startup and does not perform
capability detection. P2/P10 are an operator review procedure, not a runtime
feature; see [integrations/magento.md](../integrations/magento.md) §13.

## 4. What is verified without a live instance

| Claim | Evidence |
|---|---|
| Endpoint map and payload shapes | 2.4.9 admin OpenAPI schema and released `webapi.xml` (`[V-docs]`) |
| Client request construction, auth header, timeouts, response cap | `internal/magento/client_test.go` (`TESTED (CONTRACT)`) |
| Tool schemas, error envelopes, write disclosure | `internal/magento/tools_test.go`, `write_tools_test.go` (`TESTED (CONTRACT)`) |
| Configurable staged workflow and resume | `internal/magento/configurable_test.go` (`TESTED (CONTRACT)`) |
| MSI transfer outcome and read-back | `internal/magento/inventory_test.go` (`TESTED (CONTRACT)`) |
| Money-tool idempotency/ledger contracts | `internal/magento/invoice_links_test.go`, `order_create_test.go`, `document_write_test.go` (`TESTED (CONTRACT)`) |
| Import ceiling and HTTP body cap | `internal/magento/import_test.go` (`TESTED (CONTRACT)`) |
| Queue/ledger durability | `internal/storage/*_test.go` against PostgreSQL (`TESTED (PostgreSQL)`) |

Contract tests prove the code matches the documented contract; they do not prove
Magento accepts the request. That distinction is the point of this log.

## 5. How to close the gap

1. Provision a Magento 2.4.9 sandbox with an integration credential scoped to the
   read manifest.
2. Run the read-only probes in §3 and record status, structural fields, counts,
   and timestamps; never record tokens, customer data, or full bodies.
3. Re-run the `test/e2e` harness against the sandbox (it currently uses a fake
   Magento) and label the result **E2E-with-real-Magento**, not
   `INTEGRATION_TESTED` unless the repository adopts that label.
4. Update this file's status and the claims in
   [known-limitations.md](../audit/known-limitations.md) §1.1; do not silently
   upgrade a `CONTRACT` claim to `INTEGRATION_TESTED`.

Until then, the honest position is: **no live Magento evidence; contract and
vendor documentation only.**
