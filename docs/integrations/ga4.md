# GA4 integration

**Status:** Phase 0 design, read-only. **Baseline:**
[`research-baseline.md`](../development/research-baseline.md) (2026-11-18).
**Evidence:** [`docs/evidence/ga4.md`](../evidence/ga4.md). **Decision:** ADR-0005.

Tags: `[V-live]` verified live during Phase 0, `[V-docs]` verified against vendor
documentation or released source, `[U]` unverified with an explicit verification step.
`developers.google.com` is unreachable from this workstation (F22), so
documentation-derived claims are `[U]` even where a citation is given.

## 1. Scope and non-goals

| In scope | Out of scope |
|---|---|
| Read-only Data API: `runReport`, `batchRunReports`, `runPivotReport`, `runRealtimeReport`, `properties.getMetadata` | Every Admin API method, read or write. No property, stream, key-event, audience, or custom-dimension mutation |
| Aggregate traffic, acquisition, event, and ecommerce reporting | User-level records: no `userId`, `clientId`, or `sessionId` in any output |
| Property metadata: display name, time zone, currency, schema | Property configuration changes of any kind |
| Capability probing: does this property actually have ecommerce data? | Guarantees of completeness or unsampled results |
| Per-property quota, caching, and cost control | Billing management or quota increases |
| Documenting the BigQuery boundary | An embedded SQL executor or BigQuery client |

Read-only by construction: the credential has no write scope and no tool maps to an Admin
API write. Aggregates only, so raw-event questions belong to BigQuery (§11). No
configuration authority — a property without ecommerce tracking is reported as such, not
repaired — and requests are logged as query shapes, not results.

## 2. The Data API surface

### 2.1 Request and response shape

| Request field | Purpose | Gateway default |
|---|---|---|
| `dateRanges[]` | `{startDate, endDate}` pairs; relative forms such as `7daysAgo` accepted | Required, max 2 |
| `dimensions[]` / `metrics[]` | Grouping keys and aggregated values | Metrics ≥ 1; dimensions optional |
| `dimensionFilter` / `metricFilter` | `FilterExpression` trees: `andGroup`, `orGroup`, `notExpression`, `filter` | Passed through after validation |
| `orderBys[]` | Dimension or metric ordering | Always injected (§2.2) |
| `limit` / `offset` | Row window | Default 1000, gateway cap 10,000 |
| `metricAggregations[]` | `TOTAL`, `MAXIMUM`, `MINIMUM` | `TOTAL` when a dimension splits rows |
| `keepEmptyRows` / `returnPropertyQuota` | All-zero rows; quota state | `false`; `true` on a sampled basis (§9) |

The response carries typed headers (all four types observed `[V-live]`), `rows`,
`totals`/`maximums`/`minimums`, `rowCount`, `metadata` (`currencyCode`, `timeZone`,
`samplingMetadatas`, `dataLossFromOtherRow`, `dataTruncationReasons`,
`schemaRestrictionResponse`), and `propertyQuota` `[V-live]`.

### 2.2 Pagination and ordering

**`rowCount` is the match count, not the page size:** `limit=15` returned 15 rows with
`rowCount=18`; `limit=5` returned 5 rows with `rowCount=211` `[V-live]`. Page on
`offset + len(rows) < rowCount`, never on `len(rows) < limit`.

**Default order is not guaranteed.** With no `orderBys`, output came back ordered by the
first metric descending rather than by `date` `[V-live]`, so the gateway always injects an
explicit order.

The 250,000 row cap is per request `[U]` — documented but not reached in Phase 0; verify at
`limit=250000` on a high-cardinality dimension. The gateway's own 10,000-row cap keeps one
call from exhausting a token budget, and oversized requests are rejected, never truncated.

### 2.3 Totals and filters

`metricAggregations` totals are computed over the whole range and are not a column sum: the
7-day total was **370,599 sessions** against a seven-row sum of **368,883**, a 0.46% delta
`[V-live]`. The gateway returns `totals` as the period figure and never sums rows.

`dimensionFilter` and `metricFilter` are independent trees applied separately `[V-live]`; an
`inList` filter on `eventName` over 11 standard ecommerce events returned 0 rows (§7.5).
Per-branch conditions cannot be expressed in one report, so the gateway rejects them with a
suggestion to split `[U]` for the exact error text.

### 2.4 Method matrix

| Method | Use case | Limits | Gateway policy |
|---|---|---|---|
| `properties.runReport` | One aggregate report; the workhorse for traffic, acquisition, events, ecommerce | One date-range set, 250k rows, dimension/metric caps `[U]` | **Exposed.** Default and only reporting tool in v1 `[V-live]` |
| `properties.batchRunReports` | Up to 5 requests per round trip; cuts latency and concurrency pressure | Per-request limits plus a batch cap `[U]` | **Not exposed.** Internal only, so one MCP call cannot become unbounded fan-out `[U]` |
| `properties.runPivotReport` | Pivot tables: two dimensions on rows/columns, up to 4 pivots | Stricter compatibility than `runReport` `[U]` | **Not exposed.** `runReport` with two dimensions answers the same question with one validator surface |
| `properties.runRealtimeReport` | Last-30-minute activity | 30-minute window, restricted dimensions, no custom metrics, separate quota | **Exposed with restrictions** (§8) `[V-live]` |
| `properties.getMetadata` | Discover valid dimensions and metrics, including custom ones | Metadata only | **Internal** to the validator (§6); agents use the fixed catalog `[U]` for transport |

## 3. Authentication models

| Aspect | Service account (recommended) | OAuth 2.0 |
|---|---|---|
| Principal | Service account added as a property viewer | Delegated human consent |
| Fit | Server, container, CI, unattended gateway | Desktop, per-user context, interactive tooling |
| Material | JSON key or workload identity; mounted secret, never baked into an image | Refresh token in an encrypted store |
| Scope | `.../auth/analytics.readonly` | Same scope; request nothing broader |
| Rotation | New key with overlap, then retire the old one; log the rotation | Re-consent or rotate the refresh token; revoke on offboarding |
| Failure | `PERMISSION_DENIED` when not a viewer | `invalid_grant` when revoked or expired |
| Multi-property | One account can view many properties; the gateway still scopes per instance | Access follows the consenting user |

`analytics.readonly` and nothing else makes the read-only guarantee mechanical: a bug cannot
write because the token cannot write. Credentials are per instance, not per property (§12);
none appears in a response, log, or error string, and a missing credential fails closed.

**Why the Admin API is out of scope.** It manages accounts, properties, streams, custom
dimensions, key events, and audiences — none of which answers a reporting question, all of
which is mutating. Excluding it removes an entire incident class at zero functional cost;
configuration stays a human task.

## 4. Property configuration model

| Field | Source | Rule |
|---|---|---|
| `id` | Operator config, cross-checked against the API | Numeric, immutable; a mismatch disables the property |
| `display_name` | API | Presentation only, never a key |
| `time_zone`, `currency` | API | Property-level; all `date` values and every `TYPE_CURRENCY` metric use them, echoed on every result |
| `capability` | Probe (§7.4) plus config | `ecommerce: configured \| absent \| unknown`, plus `realtime` and `custom_dimensions` flags |

Time zone and currency are property-level, not request-level, and they change the meaning
of every date and money value. Observed: `America/Los_Angeles`, `USD` `[V-live]`. Both are
copied into every envelope so an agent cannot misread a "yesterday" boundary. A currency
that differs from the Magento store currency is flagged (§7.3).

## 5. Dimension and metric catalog

Names checked against the live property schema `[V-live]`. Type is the value observed in
`metricHeaders`; scope is the gateway's internal classification, validated per pair in
§6.

### 5.1 Traffic

| Field | API name | Type | Scope | Compatibility notes |
|---|---|---|---|---|
| Sessions | `sessions` | INTEGER | session | Combines with session and user dimensions; accepted even with `itemId` (§6.1) |
| Total users | `totalUsers` | INTEGER | user | Do not sum across days for a period figure; use totals |
| Active users | `activeUsers` | INTEGER | user | Distinct-user metric; the practical realtime metric (§8) |
| New users | `newUsers` | INTEGER | user | Subset of `totalUsers`; never summed with it |
| Views | `screenPageViews` | INTEGER | event | Page views, not sessions; rejected with `itemId` `[V-live]` |
| Engagement rate | `engagementRate` | FLOAT 0–1 | session | Ratio; recompute from totals, never average rows |
| Avg session duration | `averageSessionDuration` | SECONDS | session | Ratio; rejected with `itemId` `[V-live]` |
| Bounce rate | `bounceRate` | FLOAT 0–1 | session | Ratio; rejected with item dimensions `[V-live]` |

### 5.2 Acquisition

| Field | API name | Type | Scope | Compatibility notes |
|---|---|---|---|---|
| Default channel group | `sessionDefaultChannelGroup` | STRING | session | Stable rollup; prefer for agent answers |
| Source | `sessionSource` | STRING | session | High cardinality; observed with `eventCount` (§6.1) |
| Medium | `sessionMedium` | STRING | session | Pair with source |
| Campaign | `sessionCampaignName` | STRING | session | Empty for non-campaign traffic; `(not set)`/`(direct)` expected |
| Landing page | `landingPage` | STRING | session | Query-string variant is `landingPagePlusQueryString` |

The live 7-day breakdown included `Direct`, `Organic Search`, `Unassigned`, `Referral`,
`AI Assistant`, `Organic Video`, `Organic Social`, `Organic Shopping`, and `Paid Search`
`[V-live]`. `AI Assistant` is a current channel group, so a hardcoded allowlist drops it.

### 5.3 Events

| Field | API name | Type | Scope | Compatibility notes |
|---|---|---|---|---|
| Event name | `eventName` | STRING | event | Works with `eventCount`, `keyEvents`; also accepted with `sessions` (§6.1) |
| Event count | `eventCount` | INTEGER | event | Rejected with `itemId` `[V-live]`; accepted with `ecommercePurchases` `[V-live]` |
| Key events | `keyEvents` | FLOAT | event | Formerly "conversions"; float because counting may be per-session |
| Key event rate | `sessionKeyEventRate` | FLOAT | session | Ratio; session-scope only |

Per-key-event metrics also exist and are property-specific: `keyEvents:purchase`,
`keyEvents:checkouts`, `keyEvents:generate_lead`, `keyEvents:manual_event_PURCHASE`
`[V-live]`. They must be discovered from metadata, never hardcoded.

### 5.4 Ecommerce

| Field | API name | Type | Scope | Compatibility notes |
|---|---|---|---|---|
| Purchases | `ecommercePurchases` | INTEGER | event | Zero without ecommerce tracking; accepted with `eventCount` `[V-live]` |
| Purchase revenue | `purchaseRevenue` | CURRENCY | event | GA4-attributed revenue, not Magento order revenue (§7.3) |
| Avg purchase revenue | `averagePurchaseRevenue` | CURRENCY | event | Ratio; recompute from totals |
| Add to carts | `addToCarts` | INTEGER | event | Funnel stage 2 |
| Checkouts | `checkouts` | INTEGER | event | Funnel stage 3; also a key event on this property |
| Items viewed | `itemsViewed` | INTEGER | item | Item-scope; pairs with `itemId` |
| Items added to cart | `itemsAddedToCart` | INTEGER | item | Item-scope |
| Items purchased | `itemsPurchased` | INTEGER | item | Item-scope |
| Item revenue | `itemRevenue` | CURRENCY | item | **Rejected with `ecommercePurchases`** `[V-live]`; query separately |
| Item ID | `itemId` | STRING | item | Rejected with `eventCount`, `screenPageViews`, `averageSessionDuration`, `bounceRate` `[V-live]` |
| Item SKU | `itemSku` | — | — | **Does not exist in the schema** `[V-live]`; use `itemId` |
| Item name | `itemName` | STRING | item | Same restrictions as `itemId` |
| Item category | `itemCategory` | STRING | item | Levels 2–5 exist as `itemCategory2`…`itemCategory5` |
| Item brand | `itemBrand` | STRING | item | Item-scope |
| Cart-to-view rate | `cartToViewRate` | FLOAT | item | Derived ratio; fails inside purchase-revenue bundles (§6.1) |
| Purchase-to-view rate | `purchaseToViewRate` | FLOAT | item | Derived ratio; same restriction |
| Transaction ID | `transactionId` | STRING | event | Order grain; one row per order, so cap rows and range |

`itemSku` is the one brief field that does not exist: the schema search for `sku`
returned nothing `[V-live]`. Mapping Magento SKU to `itemId` is a data-collection
concern, not a reporting one.

## 6. Compatibility validation

GA4 rejects incompatible dimension/metric combinations at query time with a generic HTTP
400, and the space is far too large to enumerate. The validator has three layers.

| Layer | Check | Failure it prevents | Cost |
|---|---|---|---|
| A — curated rules | Pairs refused before a request is issued, seeded from §6.1 | Known-bad combinations reaching the API | Zero; static, and every entry carries its observation date so the live API can correct it |
| B — live metadata | Every field must exist in the property's metadata, including custom fields | Typos, renamed fields, and property-specific metrics such as `keyEvents:checkouts` | One metadata read, cached for the process lifetime |
| C — capability probe | Runs §7.4 when ecommerce is requested and capability is `unknown` | A structurally valid query returning an empty table that an agent reports as "zero sales" | One cached probe per 24 h |

### 6.1 Observed compatibility matrix

Live results on `properties/354542598`, 7-day range, 2026-11-18 `[V-live]`. "Accepted"
means a 200-shaped response, possibly with zero rows.

| Dimension(s) | Metrics | Result | Error text |
|---|---|---|---|
| `date` | `ecommercePurchases`, `purchaseRevenue` | Accepted, 0 rows | — |
| `date` | `ecommercePurchases`, `eventCount` | **Accepted**, 7 rows | — |
| `date` | `ecommercePurchases` + `itemsPurchased` / `addToCarts` / `checkouts` / `itemsViewed` / `averagePurchaseRevenue` | Accepted, 0 rows | — |
| `date` | `ecommercePurchases`, `itemRevenue` | **Rejected** | "Please remove ecommercePurchases to make the request compatible for example." |
| `date` | `itemsViewed`, `addToCarts`, `checkouts`, `ecommercePurchases`, `purchaseRevenue` | Accepted, 0 rows | — |
| `sessionDefaultChannelGroup` / `transactionId` | `ecommercePurchases`, `purchaseRevenue` | Accepted, 0 rows | — |
| `itemId` | `itemsViewed`, `itemsAddedToCart`, `itemsPurchased`, `itemRevenue` | Accepted, 0 rows | — |
| `itemId` | `sessions` / `totalUsers` | **Accepted**, 0 rows | — |
| `itemId` | `eventCount` | **Rejected** | "Please remove eventCount to make the request compatible for example." |
| `itemId` | `sessions` + `screenPageViews` | **Rejected** | "Please remove screenPageViews to make the request compatible for example." |
| `itemId` | `averageSessionDuration` | **Rejected** | generic incompatibility message |
| `itemName` | `bounceRate`, `averageSessionDuration` | **Rejected** | generic incompatibility message |
| `sessionSource`, `sessionMedium` | `eventCount` | **Accepted**, 154 rows | — |
| `eventName` | `sessions` | **Accepted**, 18 rows | — |
| `date` | `cartToViewRate`, `purchaseToViewRate` | Accepted, 0 rows | — |
| realtime `unifiedScreenName` | `activeUsers` | Accepted, 211 rows | — |
| realtime `eventName` | `activeUsers` | **Rejected** | "Selected dimensions and metrics cannot be queried together." |
| realtime `eventName` | `eventCount` | Accepted, 9 rows | — |

### 6.2 Known-invalid combinations and replacements

| Combination | Status | Why | Replacement |
|---|---|---|---|
| `itemId` + `eventCount` | Invalid `[V-live]` | Item dimension with event counter | `itemId` + `itemsViewed`; `eventName` + `eventCount` for volume |
| `itemRevenue` + `ecommercePurchases` | Invalid `[V-live]` | Item metric with event purchase counter | Two reports, or `purchaseRevenue` for the order-level figure |
| `itemId` + `screenPageViews` / `averageSessionDuration` / `bounceRate` | Invalid `[V-live]` | Page and session metrics are not item-attributable | Item metrics only |
| `itemId` + `sessionDefaultChannelGroup` | Invalid `[U]` | Session dimension with item rows | One probe, recorded in evidence |
| `transactionId` + `sessions` | Invalid `[U]` | Order grain with session metric | `transactionId` + `ecommercePurchases` |
| realtime `eventName` + `activeUsers` | Invalid `[V-live]` | Realtime restricts the pairing | Realtime `eventName` + `eventCount` |

"Invalid" means *the API returns 400*, not *the number would be meaningless*: `itemId` +
`sessions` is accepted but yields an item × session cross-product with no interpretation, so
Layer A has a second category — **discouraged but accepted** — that warns rather than errors.

### 6.3 Why scope-based prediction fails

The intuitive model — event, session, user, and item scopes never mix — does not hold.
`sessionSource` + `eventCount` succeeded with 154 rows, `eventName` + `sessions` with 18,
`ecommercePurchases` + `eventCount` with 7; `itemId` + `sessions` succeeded while `itemId` +
`screenPageViews` failed `[V-live]`. Compatibility is enforced per field pair, not per scope
class, so scope arithmetic both over-rejects valid queries and under-rejects invalid ones.

### 6.4 Actionable errors

An incompatible request returns `ga4_incompatible_combination` with `detail`,
`offending_field`, `suggested_replacement`, ready-to-run `alternatives`, the `evidence` date,
and a docs link — for `itemId` + `eventCount`: `offending_field: eventCount`,
`suggested_replacement: {eventCount: itemsViewed}`. It names the offending field (GA4's own
message often does — "Please remove eventCount…" `[V-live]`). When the 400 names no field,
the validator bisects by dropping metrics one at a time and caches the smallest offending
subset as a Layer A rule.

## 7. Ecommerce analysis

### 7.1 The funnel

`itemsViewed` → `addToCarts` → `checkouts` → `ecommercePurchases`. The stages are **not** a
strict user-level sequence: each is an independent event count, so a user who adds to cart
without a `view_item` in the window still contributes. `checkouts` reflects
`begin_checkout`, so a store emitting `add_payment_info` but not `begin_checkout` shows a
hole that reads as drop-off.

This shape is structurally valid and returns 0 rows on the target property `[V-live]`;
`itemRevenue` must be removed from it (§6.1).

```json
{"dimensions": ["date"],
 "metrics": ["itemsViewed", "addToCarts", "checkouts", "ecommercePurchases", "purchaseRevenue"]}
```

### 7.2 Item-level reporting

Item reports use `itemId`, `itemName`, `itemBrand`, `itemCategory` with item-scope metrics
only: "which products were viewed, added, and bought", never "which channel drove the item",
because acquisition dimensions are session-scoped and do not join to item rows. They are a
distinct tool so an agent cannot build the invalid mix by accident; cardinality forces an
`itemId` filter or `limit ≤ 1000`.

### 7.3 Revenue semantics

`purchaseRevenue` is **GA4-attributed revenue**: computed from `purchase` events and their
`value`/`currency` parameters, attributed to the session and channel GA4 recorded. It is
not Magento order revenue.

| Question | Correct source | Why |
|---|---|---|
| GA4-attributed revenue by channel | `purchaseRevenue` | Channel attribution exists only in GA4 |
| Order total, tax, shipping, refunds | Magento REST `/V1/orders` | GA4 `value` is whatever the storefront sent |
| Cross-system reconciliation | Both, as an explicit reconciliation report | Differences are expected, not a bug |
| Currency conversion | Neither without an FX source | GA4 uses the property currency; Magento may not |

The gateway never joins the two implicitly: a tool returning both labels each figure with
its source and refuses to compute a single "revenue" number. A property currency that
differs from the Magento base currency warns on every revenue result.

### 7.4 Capability profile

| State | Meaning | Detection | Surfaced as |
|---|---|---|---|
| `configured` | Ecommerce events and revenue present and non-zero | Probe returns ≥ 1 row for any of 11 standard ecommerce events, or non-zero `ecommercePurchases` | Ecommerce tools enabled; revenue included |
| `absent` | No ecommerce tracking, or tracking that never fires | 0 rows for all 11 events **and** `ecommercePurchases` = 0 over the window | Tools disabled by default; funnel returns `ecommerce_not_tracked`, not a table of zeros |
| `unknown` | Probe not run, or inconclusive (e.g. no traffic at all) | Probe not run, or window has no traffic | Tools enabled, every response carries `capability: unknown` |

Detection is one `runReport` on `eventName` filtered to the 11 standard ecommerce events plus
a metrics probe for `ecommercePurchases` and `purchaseRevenue`, over the last 7 complete days,
cached 24 hours. The distinction is mandatory: `absent` is a fact about the property, `unknown`
a fact about our knowledge, and collapsing them lets an agent report zero sales when nobody
instrumented the store.

### 7.5 Live finding

The target property is **`ecommerce: absent`** `[V-live]`. Over 2026-11-11 to 2026-11-17
it served 370,599 sessions and 348,529 users and returned:

| Probe | Result |
|---|---|
| `eventName` filtered to 11 standard ecommerce events | 0 rows |
| `ecommercePurchases`, `purchaseRevenue` by date | 0 rows (7 days) |
| `itemId` + item metrics | 0 rows |
| Top 18 events by count | `page_view`, `session_start`, `first_visit`, `user_engagement`, `scroll`, `form_start`, `form_submit`, `view_search_results`, `generate_lead`, `click`, plus vendor events — no ecommerce events |

The property defines a `purchase` key event (standard, non-deletable) and a custom
`manual_event_PURCHASE` key event, but neither has volume; `generate_lead` (430 events) is
the only key event with data `[V-live]`. This is the F21 failure mode: a high-traffic
property where a naive `purchaseRevenue` query returns a clean, plausible, wrong zero.

## 8. Realtime analytics

| Aspect | Behaviour | Gateway policy |
|---|---|---|
| Window | Last 30 minutes; `minuteRanges` can narrow it | Fixed 30 minutes; no historical realtime |
| Dimensions | Restricted: `unifiedScreenName`, `eventName`, `country`, `deviceCategory`, `platform`, `minutesAgo` and similar `[V-live]` for the first four | Allowlist only |
| Metrics | `activeUsers`, `eventCount`, and a small set; no custom metrics `[U]` for the full list | Allowlist only |
| Compatibility | Stricter than standard reports: `eventName` + `activeUsers` rejected `[V-live]` | Realtime rule table, not the standard one |
| Quota | Separate from standard reporting `[U]` — not inspected in Phase 0 | Treated as scarce; cache 30–60 s |
| `(other)` rows | Present: realtime `eventName` returned `(other)` with 2 users `[V-live]` | Flagged in the envelope |

| Exposure | Items | Reason |
|---|---|---|
| Safe | `activeUsers` alone; `unifiedScreenName` + `activeUsers`; `eventName` + `eventCount` | Each answers a question the 30-minute window genuinely supports |
| Restricted or annotated | Realtime revenue; any comparison against daily totals; `activeUsers` read as `totalUsers` | Different attribution, no ecommerce guarantee, and volatile counts — two calls a minute apart can differ by an order of magnitude |

Every result is labelled `window: "last_30_minutes"` with a `not_comparable_to_daily`
flag.

## 9. Quotas, concurrency, and cost control

### 9.1 Verified envelope

Observed via `returnPropertyQuota` `[V-live]`, consistent with F20:

| Bucket | Limit | Observed after 14 probes |
|---|---|---|
| Tokens per day | 200,000 | 199,864 remaining (136 consumed) |
| Tokens per hour | 40,000 | 39,864 remaining |
| Tokens per project per hour | 14,000 | 13,864 remaining |
| Concurrent requests | 10 | 0 in use, 10 available |
| Server errors per project per hour | 10 | 0 used |
| Potentially thresholded requests per hour | 120 | 0 used |

Cost is charged in tokens, not requests: fourteen probes consumed 136 tokens, roughly **10
tokens per small report**. Large multi-dimensional reports cost substantially more; the
cost function is unpublished `[U]`. Verify by diffing `tokensPerDay.consumed` across known
shapes.

### 9.2 Cost model

The gateway budgets conservatively as `rows × columns × cardinality_factor`.

| Term | Meaning | Control |
|---|---|---|
| `rows_returned` | Rows the API materialises, bounded by `limit` | Hard cap per tool, default 10,000 |
| `columns` | Dimensions + metrics | Warn above 5 dimensions + 8 metrics |
| `cardinality_factor` | High-cardinality fields (`pagePath`, `transactionId`, `itemId`, `sessionSource`) multiply cost | Per-field weight; such fields require a filter or a short range |

The estimate is computed before the call and checked against the remaining daily budget; a
request exceeding the per-property share is refused with the estimate, not executed.

### 9.3 Budget, concurrency, and backoff

Each property gets a share of the daily token budget (default equal split), enforced in the
gateway rather than discovered by hitting a 429; an exhausted share returns
`ga4_budget_exhausted` with the reset time. A per-property semaphore caps in-flight requests
at 4 — below the observed limit of 10 — with a bounded queue and a fast retry-after hint.

| Condition | Action |
|---|---|
| HTTP 429 | Exponential backoff with jitter, honour `Retry-After`, max 3 attempts |
| HTTP 5xx | Retry twice; the server-error bucket is only 10/hour, so a retry storm is worse than a failure |
| HTTP 400 (incompatibility) | **Never retry.** Feed the validator, cache the rule, return an actionable error |
| HTTP 401/403, timeout | No retry for auth errors; one retry for timeouts, then a partial-result hint |

### 9.4 Caching

| Report shape | TTL | Rationale |
|---|---|---|
| Realtime | 30–60 s | The window is 30 minutes; fresher caching buys nothing |
| Today / intraday | 5 min | Data still settling |
| Yesterday and earlier, no filter | 15 min | Stable within the day |
| Historical, ending ≥ 7 days ago | 24 h | Immutable in practice |
| Schema, capability probe | Process lifetime; 24 h | Both change rarely |
| Compatibility failures | Until restart | A 400 today is a 400 tomorrow for the same pair |

Cache keys include property ID, the full request shape, and the property's time zone and
currency, because the same numeric request means different things across properties.

### 9.5 When quota is exhausted

Return the last cached result if within TTL, labelled `stale: true` with its age — never as
fresh. With no cache entry, return `ga4_quota_exhausted` naming the bucket, reset time, and
consumption. Do not retry, queue indefinitely, or silently degrade to realtime; log once per
property per window.

## 10. Data freshness and completeness

| Report type | Expected latency | Basis |
|---|---|---|
| Standard reports | 24–48 h to full completeness; most data within hours | `[U]` — documented, not measured. Verify by comparing D-1 and D-2 across consecutive days and watching for revision |
| Intraday / realtime | 4–8 h for the intraday table where applicable; realtime is a 30-minute window | `[U]` — same method |

Phase 0 did not measure revision behaviour, so the gateway claims no completeness guarantee;
it attaches metadata and warns when a range includes the last 48 hours.

| Signal | Where it appears | Handling |
|---|---|---|
| `(other)` rows | A dimension value literally `(other)` | Kept, flagged, never summed silently. Observed in realtime `eventName` `[V-live]` |
| `dataLossFromOtherRow` | `metadata` | `false` on all Phase 0 reports `[V-live]`; `true` marks the result incomplete |
| `samplingMetadatas[]`, `dataTruncationReasons[]` | `metadata` | Empty on all Phase 0 reports `[V-live]`; non-empty means sampled or dropped rows, and must be labelled |
| `schemaRestrictionResponse.activeMetricRestrictions[]` | `metadata` | Empty on all Phase 0 reports `[V-live]`; non-empty means a metric was restricted |
| Thresholding | Not directly visible | If a small segment returns no row where one is expected, the gateway notes thresholding as a possible cause rather than asserting zero |

Every result carries an envelope with `property`, `time_zone`, `currency`, `date_range`,
`freshness` (`as_of`, `includes_settling_days`), `completeness` (`sampled`,
`data_loss_from_other_row`, `truncated`), `row_count`, and `rows_returned`.

## 11. BigQuery boundary

| Question | Why the Data API cannot answer it | BigQuery export |
|---|---|---|
| Which events did user X fire, in order? | No event rows, no identifier in output | `events_*` with `user_pseudo_id`, `event_timestamp` |
| What is the most common path to purchase? | No sequence, no session reconstruction | Ordered events via `STRING_AGG` |
| Cohort retention by first-purchase month | Cohort dimensions are coarse and pre-aggregated | User-level cohort SQL |
| Funnel with strict sequence and time window | Stages are independent counts | Window functions over ordered events |
| Unsampled data for very large ranges | Sampling may apply and cannot be disabled | Raw export, no sampling |
| Custom SQL or joins to Magento data | No SQL surface | Native BigQuery joins |
| Attribution modelling beyond GA4's models | Fixed attribution in API and UI | Recompute from raw touch data |

| Aspect | Implication |
|---|---|
| Setup | Property-level, human, one-time console action |
| Cost | Storage plus query cost billed to the project; one careless `SELECT *` over a year of events can exceed a year of Data API usage |
| Freshness and retention | Export latency differs from the API; intraday export is a separate paid option `[U]`; retention follows the dataset's own expiry |
| Access control | Dataset IAM, separate from GA4 property permissions |

**The gateway will not embed a SQL executor.**

| Reason | Argument |
|---|---|
| Blast radius | Arbitrary SQL against a billing-enabled project is an unbounded-cost, unbounded-scan operation triggered by an LLM |
| Security | It turns a read-only analytics gateway into a general query surface over customer data, with its own injection and exfiltration risks |
| Scope discipline | The value here is a small, validated, auditable set of report shapes; a SQL passthrough discards every guarantee in §6 and §13 |
| Correct boundary | Teams needing event-level analysis should query their own project with their own tooling and cost controls |

The gateway *routes* the question instead: a request needing event-level data returns
`requires_bigquery_export` with the reason and a pointer, rather than a plausible-looking
aggregate that answers a different question.

## 12. Multi-property and instance model

| Concept | Definition | Isolation |
|---|---|---|
| Instance | One deployment with one credential set and one property list | Process boundary; separate config, cache, audit log |
| Property | One property ID within an instance | Namespaced cache keys, per-property budget and semaphore, per-property capability |
| Tool surface | Generated per property, or takes a validated selector | A tool bound to one property cannot be repointed by argument injection |

| Rule | Implementation |
|---|---|
| Property ID is validated | A first-class argument checked against the configured list, so an unlisted ID is rejected before any API call and a prompt-injected ID cannot exfiltrate another tenant's analytics |
| Isolation is per property | Capability profiles, budgets, caches, and semaphores are per property; a noisy property cannot exhaust another's tokens, and one without ecommerce cannot share a description that promises revenue |
| Results are attributed | Every result names its property (display name plus ID), so a multi-property answer cannot be misattributed |
| No cross-property aggregation | Summing `totalUsers` across properties double-counts people, so v1 declines rather than producing a number that looks authoritative and is not |
| Permission scoping | The bearer-token layer maps a caller to a set of allowed properties; the GA4 credential is shared, so that allowlist is the only thing between caller A and property B, and the check is logged |

## 13. Security and privacy

| Control | Implementation |
|---|---|
| No PII, no user-level data | Only aggregate dimensions and metrics from §5. No `userId`, `clientId`, `sessionId`, age bracket, gender, or interest dimensions; `transactionId` is capped and flagged as order-grain |
| Data minimisation | A fixed catalog rather than arbitrary field selection; high-cardinality fields require a filter |
| Credential handling | Secrets from file or environment, never logged, never echoed in errors or results |
| Audit log | Timestamp, caller, property ID, dimensions, metrics, filter structure, date range, row count, token cost, outcome |
| Log exclusion | Response rows are **not** logged — the trail records that a report ran and how big it was, not what it contained |
| Error hygiene | Upstream bodies truncated and stripped of request IDs before reaching a caller |
| Injection surface | Property IDs and field names validated against allowlists; no free-form field name reaches the API |

Omitting result data is deliberate: a trail storing every row becomes a second, less-governed
copy of the analytics data. The query shape answers "who asked what, when, and did it succeed".

## 14. Verification log

Environment: `properties/354542598` ("Giztop - GA4"), account `accounts/102070720`, via the
configured GA4 MCP server, 2026-11-18. Replayable request JSON is in
[`docs/evidence/ga4.md`](../evidence/ga4.md).

| # | Check | Result | Tag |
|---|---|---|---|
| 1 | Property metadata | Time zone `America/Los_Angeles`, currency `USD`, standard service level | `[V-live]` |
| 2 | Custom dimensions and metrics | Both empty | `[V-live]` |
| 3 | Key events | `purchase` (standard), `manual_event_PURCHASE`, `checkouts`, `generate_lead` (custom) | `[V-live]` |
| 4 | Traffic report, 7 days | 7 rows; 370,599 sessions; 348,529 users; engagement 0.128 | `[V-live]` |
| 5 | Channel report | 9 channel groups including `AI Assistant`; Direct dominant | `[V-live]` |
| 6 | Event report | 18 events; no ecommerce events | `[V-live]` |
| 7 | Ecommerce probes (event filter, metrics, item, funnel) | 0 rows in every case | `[V-live]` |
| 8 | Realtime | 211 screens, 142 country×device rows, `(other)` row present | `[V-live]` |
| 9 | Quota inspection | 200k/day, 40k/hour, 10 concurrent, 14k/project/hour, 10 server errors/hour, 120 thresholded/hour | `[V-live]` |
| 10 | Compatibility matrix | 18 recorded pairs, from ~25 probes; results in §6.1 | `[V-live]` |
| 11 | `metricAggregations` totals | 370,599 total vs 368,883 row sum | `[V-live]` |
| 12 | `itemSku` existence; docs reachability | `itemSku` absent; `developers.google.com` unreachable, so citations are links | `[V-live]` |
| 13 | `batchRunReports`, `runPivotReport`, `getMetadata` | Not exercised through the available MCP surface | `[U]` |

### Findings that amended F18

F18 originally repeated the GA4 skills library's blanket scope rules. The live probes
below replaced them; the baseline now records the corrected, pair-wise position.

| Original F18 wording | Live observation | Tag |
|---|---|---|
| "`ecommercePurchases` cannot be combined with `eventCount`" | The pair returns 200 with 7 rows | `[V-live]` |
| "`itemId`/`itemSku` cannot be combined with session-scoped metrics" | `itemId` + `sessions` and `itemId` + `totalUsers` are accepted; `itemId` + `screenPageViews`, `averageSessionDuration`, `bounceRate`, and `eventCount` are rejected. The rule is per field pair, not per scope, and `itemSku` does not exist in the schema at all | `[V-live]` |

Corrected position: compatibility is pair-wise and empirically determined; scope arithmetic
is a heuristic that produces both false rejections and false acceptances.

## Open questions

| # | Question | Why it matters | How to resolve |
|---|---|---|---|
| Q1 | Does `keepEmptyRows=true` change compatibility or only the row set? | Decides whether an empty result means "no data" or "filtered out" | Run the funnel query with and without it on a sparse property |
| Q2 | What is the exact token cost function? | §9.2 is an estimate | Diff `tokensPerDay.consumed` across known shapes |
| Q3 | Is realtime quota separate, and what are its limits? | §8 assumes scarcity | Call `runRealtimeReport` with `returnPropertyQuota` |
| Q4 | How much does D-1 revise over 48 h here? | §10 is `[U]` | Snapshot the same range daily for a week and diff |
| Q5 | Do `itemId` + session *dimensions* fail like session *metrics*? | §6.2 has an `[U]` row | One probe, recorded in evidence |
| Q6 | Should `batchRunReports` back a composite dashboard tool? | Could cut latency and quota pressure | Measure a 5-widget dashboard as 5 calls vs 1 batch |

## Decision record

ADR-0005 (GA4 read-only Data API boundary) records:

1. **Read-only, Data API only.** No Admin API method, read or write; `analytics.readonly` and nothing broader.
2. **`runReport` and `runRealtimeReport` only in v1**; `batchRunReports`, `runPivotReport`, and `getMetadata` are deferred, with `getMetadata` serving the validator internally.
3. **Three-layer compatibility validator**, because scope-based prediction demonstrably fails (§6.3).
4. **Three-state capability profile** (`configured`, `absent`, `unknown`); the distinction changes what an agent may claim.
5. **Revenue is never silently joined to Magento**; GA4 and order revenue are separate labelled figures.
6. **No SQL executor**; event-level questions route to BigQuery rather than being answered approximately.
7. **Audit logs record query shapes, not results.**

## References

- Data API overview — <https://developers.google.com/analytics/devguides/reporting/data/v1>
- Schema — <https://developers.google.com/analytics/devguides/reporting/data/v1/api-schema>
- Dimensions and metrics explorer — <https://ga-dev-tools.web.app/ga4/dimensions-metrics-explorer/>
- Quotas — <https://developers.google.com/analytics/devguides/reporting/data/v1/quotas>
- Realtime schema — <https://developers.google.com/analytics/devguides/reporting/data/v1/realtime-api-schema>
- BigQuery export — <https://support.google.com/analytics/answer/9358801>
