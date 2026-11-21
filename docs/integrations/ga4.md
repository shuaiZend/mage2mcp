# Google Analytics 4 integration

**Status:** as-built for v0.11.5
(2026-11-20). The shipped module is a read-only GA4 Data API client with six
registered tools. Acquisition, funnel, landing-page, event, cohort, audience,
and channel-revenue reports are **not implemented**; they are listed as PLANNED
or absent below.

**Verification boundary.** No live GA4 property was available for this audit.
Behaviour is verified against the Data API v1beta contract and `httptest` fakes
(`CONTRACT`). There is no live integration test and nothing here is
`INTEGRATION_TESTED`.

**Module:** `internal/ga4`. **Evidence:** [ga4.md](../evidence/ga4.md) (the live
probe log; see its status note). **Configuration:**
[configuration.md](../configuration.md#datasources).

## 1. Scope

The module answers aggregate questions about one property:

- daily traffic totals and per-date rows;
- the property's available dimension and metric names;
- whether the property tracks ecommerce at all;
- a fixed 30-minute realtime window;
- daily purchases/revenue and item-level performance when ecommerce is tracked.

It never returns user-level data, never writes to GA4, and never manages
accounts, properties, data streams, or admin settings. It does not export to
BigQuery and does not query the GA4 UI.

## 2. Registered tools

These six tools are the entire GA4 surface. All are read-only, all require
`instance`, and all are registered when the GA4 module is enabled with an
instance.

| Tool | Inputs | Output | Limits / defaults |
|---|---|---|---|
| `ga4.analytics.traffic` | `instance`, optional `start_date`/`end_date`, `metrics[]` | `rows[]` grouped by `date` with the requested metrics, `totals`, `time_zone`, `currency`, `sampled` | Date range defaults to `7daysAgo`..`yesterday`; metrics from the closed allowlist below; row limit 1000 |
| `ga4.property.metadata` | `instance` | `dimensions[]`, `metrics[]` (including custom fields) | No date range; 30 s timeout |
| `ga4.property.capabilities` | `instance` | `ecommerce_tracked`, `currency`, `time_zone`, `checked_at`, `evidence[]` | Probes the last 7 complete days; two aggregate-only reports; row limit 100 |
| `ga4.analytics.realtime` | `instance`, optional `dimensions[]`, `metrics[]`, `limit` | `rows[]`, `time_zone`, `currency` | Fixed last-30-minutes window; dimensions from 4-name allowlist; metrics from 3-name allowlist; `limit` 1–100, default 100 |
| `ga4.ecommerce.overview` | `instance`, optional `start_date`/`end_date` | `overview` object: `rows[]` by date, `totals`, `time_zone`, `currency`, `sampled`, `capability_check` | Defaults to `7daysAgo`..`yesterday`; metrics `ecommercePurchases`, `purchaseRevenue`, `averagePurchaseRevenue`; row limit 1000; requires ecommerce capability |
| `ga4.ecommerce.products` | `instance`, optional `start_date`/`end_date`, `limit` | `performance` object: `products[]` with `item_id`, `items_viewed`, `items_added_to_cart`, `items_purchased`, `item_revenue`; currency/time zone/sampled/capability | Defaults to `7daysAgo`..`yesterday`; limit default 50, values outside 1–200 fall back to 50; ordered by `itemRevenue` descending; requires ecommerce capability |

### 2.1 Traffic metric allowlist

`ga4.analytics.traffic` accepts exactly:

`sessions`, `totalUsers`, `newUsers`, `screenPageViews`, `engagementRate`,
`averageSessionDuration`, `bounceRate`, `eventCount`, `keyEvents`.

An unknown metric is `invalid_argument` naming the allowed set. The tool does not
accept dimensions; it always groups by `date`. Period totals are GA4's
`metricAggregations: TOTAL`, never the sum of the returned rows.

### 2.2 Realtime allowlists

- Dimensions: `eventName`, `country`, `deviceCategory`, `unifiedScreenName`.
- Metrics: `activeUsers`, `eventCount`, `screenPageViews`.

Realtime is a fixed 30-minute window. Counts are volatile and are not
comparable to daily report totals; the result carries the property time zone and
currency so a caller does not assume UTC or USD.

### 2.3 Ecommerce capability gate

`ga4.ecommerce.overview` and `ga4.ecommerce.products` call
`ga4.property.capabilities` first. The probe looks for a positive count on any of
the eleven standard ecommerce events over the last seven complete days:

`view_item`, `add_to_cart`, `begin_checkout`, `purchase`, `refund`,
`add_payment_info`, `add_shipping_info`, `view_cart`, `remove_from_cart`,
`select_item`, `view_item_list`.

If none fired, the tools return `ecommerce_not_tracked` rather than a table of
zeros that looks like real revenue. An absent result is a fact about the
property, not proof that zero sales occurred. The capability result includes the
evidence strings the probe used, so an operator can see why it decided.

## 3. Authentication and instances

| Item | As built |
|---|---|
| Credential | Service-account JSON (`${GA4_SERVICE_ACCOUNT}`) or a static bearer token for development; the credential is never logged |
| Scope | One property per instance (`property_id`, numeric); the tool's `instance` selects it |
| Routing | Per-instance resolver; several GA4 instances can be configured and are selected by `instance` |
| Base URL | `https://analyticsdata.googleapis.com/v1beta`; tests use a loopback `httptest` server |
| Timeout | 60 s for reports, 30 s for metadata |
| Response cap | 8 MiB; a larger response is `upstream_unavailable` |
| Retries | None. `rate_limited` and `upstream_unavailable` carry `retryable`; the caller decides |
| Quotas | Vendor-side Data API token quotas. The gateway does **not** account for them or expose a quota metric; that is PLANNED |

The module has no Admin API client. It cannot create or modify properties,
data streams, custom dimensions, key events, or links.

## 4. Request and response semantics

- `runReport` is `POST properties/{property}:runReport`; realtime uses
  `properties/{property}:runRealtimeReport`.
- Date ranges are inclusive `startDate`/`endDate` strings. The tools accept
  `YYYY-MM-DD` or GA4 relative forms (`today`, `yesterday`, `NdaysAgo`); the
  default is the last seven complete days.
- The property time zone governs date boundaries. Results state `time_zone` and
  `currency`; the gateway does not convert either.
- Totals come from GA4 (`metricAggregations: TOTAL`), not from summing rows.
- `sampled` reflects GA4 metadata. A sampled result is an estimate; the tools do
  not silently rescale it.
- No user-level identifiers, client IDs, or PII are requested. All reports are
  aggregate-only.
- `ga4.property.metadata` is the authority for which dimension and metric names
  exist, including custom fields. The traffic and realtime tools still enforce
  their own closed allowlists; metadata does not widen them.

## 5. Errors

| Condition | Gateway code |
|---|---|
| Unknown metric/dimension, bad date, bad limit | `invalid_argument` |
| Missing or invalid credential | `permission_denied` |
| Property not found or no access | `not_found` / `permission_denied` |
| Ecommerce tools on a property with no ecommerce events | `ecommerce_not_tracked` |
| 429 | `rate_limited` (retryable) |
| 5xx, malformed body, response over 8 MiB | `upstream_unavailable` |
| Deadline | `upstream_timeout` |

## 6. Not implemented

| Capability | Status |
|---|---|
| Acquisition, channel, source/medium, landing-page, event, and funnel reports | **PLANNED**; no tool |
| Cohort, audience, demographic, and user-level reports | Absent by design |
| BigQuery export / raw-event query | Absent; documented boundary |
| Admin API (properties, streams, custom dimensions, key events) | Absent |
| Quota accounting, caching, and request coalescing | **PLANNED** |

If a report is not in the six-tool table above, the gateway cannot produce it.
Do not infer a capability from the presence of a GA4 field name in metadata.

## 7. Verification

| Test | What it proves |
|---|---|
| `TestTrafficReport`, `TestTrafficCustomRange`, `TestTrafficRejectsInvalidQuery`, `TestTrafficDeduplicatesMetrics` | Date-grouped traffic, defaults, allowlist, totals |
| `TestMetadata`, `TestMetadataErrorMapping` | Dimension/metric metadata and errors |
| `TestCapabilitiesTracked`, `TestCapabilitiesAbsent`, `TestCapabilitiesPurchasesWithoutEvents` | Ecommerce capability gate and evidence |
| `TestEcommerceOverviewAbsentCapability`, `TestEcommerceOverviewTracked`, `TestTopProductsTracked` | Ecommerce overview/products and the no-zeros rule |
| `TestRealtimeReport`, `TestRealtimeRowCap`, `TestRealtimeRejectsInvalidCombination`, `TestRealtimeRejectsUnsupportedFields` | Realtime window, allowlists, row cap |
| `TestRegisterMetadata`, `TestExtraToolSchemas`, `TestRegisterExtraMetadata` | Registry metadata and closed schemas |
| `TestResolverMultipleInstancesRequiresName` | Per-instance routing |

No live property was available; the tests use `httptest` and the documented Data
API contract.

## 8. Related

- [Configuration](../configuration.md#datasources) — property id, credential, routing.
- [MCP tools](../mcp-tools.md) — tool contract and error envelope.
- [Known limitations](../audit/known-limitations.md) §1.2 — no live GA4.
- [Unified analytics](../unified-analytics.md) — the one cross-source comparison
  that ships; the other scenarios are PLANNED.
