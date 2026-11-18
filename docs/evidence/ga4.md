# GA4 evidence log

**Property:** `properties/354542598` ("Giztop - GA4"), account `accounts/102070720`.
**Date:** 2026-11-18 (property time zone `America/Los_Angeles`; queries issued from
`Asia/Hong_Kong`).
**Method:** read-only Data API calls through the already-configured GA4 MCP server
(`mcp__analytics__*`). No Admin API call, no write, no configuration change.
**Companion document:** [`docs/integrations/ga4.md`](../integrations/ga4.md).

This file records what was asked, what came back structurally, and whether the result
matched the expectation. It is the source for every `[V-live]` tag in the integration
document.

## Redaction rules

| Rule | Why |
|---|---|
| No full response payloads | A report body is customer data; only field names, row counts, types, and aggregate totals are reproduced |
| No session or user counts tied to a customer identity | Counts appear only at property, channel, or event level, never per person or per order |
| No credentials, tokens, project IDs, or service-account emails | The evidence file is public; credentials are never in scope of a report response |
| No `transactionId` or `itemId` values | Order and product identifiers are business data, not verification evidence; only row counts are reported |
| Aggregate totals only where needed | Period figures such as 370,599 sessions are published because they are already public traffic scale and are needed to explain the totals rule |

## Probe summary

| Probe | Expected | Observed | Date | Status | Tag |
|---|---|---|---|---|---|
| Property metadata | Time zone, currency, service level | `America/Los_Angeles`, `USD`, `GOOGLE_ANALYTICS_STANDARD`; `custom_dimensions: []`, `custom_metrics: []` | 2026-11-18 | Pass | `[V-live]` |
| Key event list | At least `purchase` | 4 key events: `purchase` (standard), `manual_event_PURCHASE`, `checkouts`, `generate_lead` (custom) | 2026-11-18 | Pass | `[V-live]` |
| Schema: traffic, acquisition, event fields | 17 fields exist | All 17 present, plus per-key-event metrics | 2026-11-18 | Pass | `[V-live]` |
| Schema: ecommerce fields | 17 fields exist | 16 present; `itemSku` absent | 2026-11-18 | Partial | `[V-live]` |
| Traffic report, 7 days | Rows with sessions and users | 7 rows, 370,599 sessions, 348,529 users, engagement 0.128 | 2026-11-18 | Pass | `[V-live]` |
| Channel report | Channel groups | 9 rows, Direct dominant, `AI Assistant` present | 2026-11-18 | Pass | `[V-live]` |
| Event report, top 18 | Event names with counts | 18 rows, no ecommerce events | 2026-11-18 | Pass | `[V-live]` |
| Ecommerce event filter (11 events) | Some rows if tracked | 0 rows | 2026-11-18 | Fail (absent) | `[V-live]` |
| Ecommerce metrics by date | Non-zero revenue if tracked | 0 rows over 7 days | 2026-11-18 | Fail (absent) | `[V-live]` |
| Item report and funnel shape | Rows if tracked | 0 rows each; both schemas accepted | 2026-11-18 | Fail (absent) | `[V-live]` |
| Realtime | Rows with active users / counts | 211 screens (top 1,709 active users), 142 country×device rows, 9 event rows including `(other)` = 2 | 2026-11-18 | Pass | `[V-live]` |
| Quota inspection | Six buckets | 200,000/day, 40,000/hour, 14,000/project/hour, 10 concurrent, 10 server errors/hour, 120 thresholded/hour | 2026-11-18 | Pass | `[V-live]` |
| Compatibility probes | Reproduce documented rules | ~25 probes, 18 recorded pairs; 2 documented rules contradicted | 2026-11-18 | See below | `[V-live]` |
| `itemSku` schema search | Field exists | No dimensions or metrics matched `sku` | 2026-11-18 | Fail | `[V-live]` |
| `developers.google.com` | Reachable for citations | Unreachable (`fetch failed`) | 2026-11-18 | Fail (expected, F22) | `[V-live]` |
| `batchRunReports`, `runPivotReport`, `getMetadata` | Callable | No MCP surface exposes them | 2026-11-18 | Not run | `[U]` |

## Structural findings

**Quota.** Two `returnPropertyQuota` reads, before and after 14 probes, returned the same limits
with consumption rising from 0 to 136 tokens per day, hour, and project-hour; concurrency stayed
at 0 of 10. Cost is token-based, roughly 10 tokens per small report.

**`rowCount` semantics.** A `date` report with `limit=15` returned 15 rows and `rowCount=18`; a
realtime report with `limit=5` returned 5 rows and `rowCount=211`. It is the match count, not the
page size.

**Totals vs row sum.** The 7-day total from `metricAggregations` was 370,599; summing the seven
daily rows gives 368,883, a 0.46% difference — row summation is not the period figure.

**Ecommerce absence.** Four probes agree: the 11 standard ecommerce events return 0 rows,
`ecommercePurchases` and `purchaseRevenue` return 0 rows, an item report returns 0 rows, and the
top-18 event list has no ecommerce event. The property defines a `purchase` key event, so the
event exists without volume — consistent with F21.

**Metadata envelope.** Every report returned `data_loss_from_other_row: false`,
`sampling_metadatas: []`, and `data_truncation_reasons: []`; reports touching ecommerce or item
metrics also returned `schema_restriction_response.active_metric_restrictions: []`.

**Realtime `(other)`.** A realtime `eventName` report returned a literal `(other)` row with 2
users, so the mechanism is visible in realtime responses.

## Compatibility probe results

| Dimension(s) | Metrics | Result |
|---|---|---|
| `date` | `ecommercePurchases`, `purchaseRevenue` | Accepted, 0 rows |
| `date` | `ecommercePurchases`, `eventCount` | **Accepted, 7 rows** (amended F18) |
| `date` | `ecommercePurchases` + `itemsPurchased` / `addToCarts` / `checkouts` / `itemsViewed` / `averagePurchaseRevenue` | Accepted, 0 rows each |
| `date` | `ecommercePurchases`, `itemRevenue` | 400: "Please remove ecommercePurchases…" |
| `date` | `itemsViewed`, `addToCarts`, `checkouts`, `ecommercePurchases`, `purchaseRevenue` | Accepted, 0 rows |
| `sessionDefaultChannelGroup`, `transactionId` | `ecommercePurchases`, `purchaseRevenue` | Accepted, 0 rows |
| `itemId` | `itemsViewed`, `itemsAddedToCart`, `itemsPurchased`, `itemRevenue` | Accepted, 0 rows |
| `itemId` | `sessions`, `totalUsers` | **Accepted, 0 rows** (amended F18) |
| `itemId` | `eventCount`; `sessions` + `screenPageViews` | 400: "Please remove eventCount…" / "…remove screenPageViews…" |
| `itemId`, `itemName` | `averageSessionDuration`, `bounceRate` | 400: generic incompatibility |
| `sessionSource`, `sessionMedium` | `eventCount` | **Accepted, 154 rows** (contradicts the skill library) |
| `eventName` | `sessions` | **Accepted, 18 rows** (contradicts the skill library) |
| `date` | `cartToViewRate`, `purchaseToViewRate` | Accepted, 0 rows |
| realtime `unifiedScreenName` / `country` + `deviceCategory` | `activeUsers` | Accepted, 211 and 142 rows |
| realtime `eventName` | `activeUsers` | 400: "Selected dimensions and metrics cannot be queried together." |
| realtime `eventName` | `eventCount` | Accepted, 9 rows |

The 400 responses quote the API verbatim. When the API names a field it names the one to
remove; otherwise the message is generic — which is why the validator bisects rather than
parsing.

## Replayable requests

Set `PROPERTY=354542598`; each body goes to
`POST https://analyticsdata.googleapis.com/v1beta/properties/$PROPERTY:runReport` (or
`:runRealtimeReport`). All are read-only.

**Property metadata.** Read through the MCP server's property-details tool; the Data API
equivalent for schema discovery is `GET /v1beta/properties/$PROPERTY/metadata`. Admin API
property reads are out of scope for the gateway.

**Traffic report.**

```json
{"dateRanges":[{"startDate":"2026-11-11","endDate":"2026-11-17","name":"last7d"}],
 "dimensions":[{"name":"date"}],
 "metrics":[{"name":"sessions"},{"name":"totalUsers"},{"name":"newUsers"},{"name":"screenPageViews"},
            {"name":"engagementRate"},{"name":"averageSessionDuration"},{"name":"bounceRate"}],
 "returnPropertyQuota":true}
```

**Channel report.**

```json
{"dateRanges":[{"startDate":"2026-11-11","endDate":"2026-11-17"}],
 "dimensions":[{"name":"sessionDefaultChannelGroup"}],
 "metrics":[{"name":"sessions"},{"name":"totalUsers"},{"name":"engagementRate"},{"name":"bounceRate"}],
 "orderBys":[{"metric":{"metricName":"sessions"},"desc":true}],"limit":10}
```

**Event report.**

```json
{"dateRanges":[{"startDate":"2026-11-11","endDate":"2026-11-17"}],
 "dimensions":[{"name":"eventName"}],
 "metrics":[{"name":"eventCount"},{"name":"keyEvents"}],
 "orderBys":[{"metric":{"metricName":"eventCount"},"desc":true}],
 "limit":15}
```

**Ecommerce capability probe — the decisive one.**

```json
{"dateRanges":[{"startDate":"2026-11-11","endDate":"2026-11-17"}],
 "dimensions":[{"name":"eventName"}],
 "metrics":[{"name":"eventCount"}],
 "dimensionFilter":{"filter":{"fieldName":"eventName",
   "inListFilter":{"values":["view_item","view_item_list","select_item","add_to_cart",
     "remove_from_cart","view_cart","begin_checkout","add_payment_info",
     "add_shipping_info","purchase","refund"]}}},
 "limit":20}
```

Expected 11 rows on a property with unified ecommerce; observed 0 rows.

**Ecommerce metrics.**

```json
{"dateRanges":[{"startDate":"2026-11-11","endDate":"2026-11-17"}],
 "dimensions":[{"name":"date"}],
 "metrics":[{"name":"ecommercePurchases"},{"name":"purchaseRevenue"}]}
```

**Item-level report.**

```json
{"dateRanges":[{"startDate":"2026-11-11","endDate":"2026-11-17"}],
 "dimensions":[{"name":"itemId"}],
 "metrics":[{"name":"itemsViewed"},{"name":"itemsAddedToCart"},{"name":"itemsPurchased"},{"name":"itemRevenue"}],
 "limit":10}
```

**Realtime reports.** `POST .../properties/$PROPERTY:runRealtimeReport` with
`{"dimensions":[{"name":"unifiedScreenName"}],"metrics":[{"name":"activeUsers"}]}` or
`{"dimensions":[{"name":"eventName"}],"metrics":[{"name":"eventCount"}]}`.

**Compatibility probes.** Reuse the ecommerce-metrics body, swapping dimensions and metrics per
row of the table above; a 400 is the expected result for the rejected pairs.

## Notes and limits of this evidence

- One property, one day, one credential. Compatibility rules are API-level, but the field *set*
  is property-specific: no custom dimensions exist here. No probe exercised `keepEmptyRows`,
  `batchRunReports`, `runPivotReport`, or `getMetadata`, so those claims remain `[U]`.
- The ecommerce finding is about *tracking*, not *sales*: the store may transact through Magento
  while GA4 receives no ecommerce events, in which case Magento order data is the only revenue
  source.
- Quota numbers are point-in-time and reset on Google's schedule, not the gateway's.
