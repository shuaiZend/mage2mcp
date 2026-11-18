# Unified analytics

**Status:** Phase 0 design baseline. Defines the semantics every `unified.*` tool must implement; not yet implemented.
**Module:** `internal/unified`. **Related:** [architecture.md](architecture.md) §4,
[module-boundaries.md](module-boundaries.md) §5.4, [mcp-tools.md](mcp-tools.md) §7.8,
[database-design.md](database-design.md) §5.6, [ADR-0005](adr/0005-ga4-integration.md).
**Tags:** `[V-live]` observed live, `[V-docs]` vendor documentation, `[U]` unverified with the probe stated.

## 1. Purpose, scope, and non-goals

mage2mcp exposes three populations that are routinely mistaken for one another: Cloudflare counts HTTP
requests at the edge, GA4 counts sessions after client-side tagging, Magento counts orders. Each has its
own grain, clock, and failure mode. Unified analytics makes cross-source comparisons explicit,
reproducible, and auditable — it does not produce a single blended "truth" number.

**Unified analytics produces comparisons with declared semantics.** Every result names its sources,
formulas, windows, zones, currencies, quality flags, and caveats. It never emits an unqualified total that
mixes sources, and never silently substitutes one source for another.

In scope: aligned-window traffic comparisons; bot and human characterisation within what the plan
exposes; WAF action impact on sessions; GA4 item funnel against Magento sales for mapped SKUs; channel
revenue reconciliation; per-SKU performance with mapping confidence; the SKU mapping read path.

Non-goals: a blended metric of any kind; per-request joins without Logpush and ClickHouse; user- or
session-level identity; causal attribution; implicit currency conversion; writing to any source;
maintaining SKU mappings (a separate write path, §8); re-deriving a source's own metric.

## 2. The semantic model

An **entity** exists in one source at one grain; a unified result joins entity-level **aggregates**, never
instances. Identity follows from grain: where a source exposes no row identity, the entity is addressable
only as an aggregate cell. A Cloudflare request has identity (`RayID`) only in Logpush. A GA4 session
exposes no `sessionId` in report output, so it is addressable only as
`(property, date_local, dimension tuple)`. A Magento order is addressable by `increment_id` and is
revisable after creation. A security event is not a request: one request can fire several rules `[V-docs]`.

| Entity | Source | Grain | Primary key | Time zone | Currency | Latency | Sampling |
|---|---|---|---|---|---|---|---|
| Traffic event | Cloudflare Logpush `http_requests` | 1 request | `RayID` (Logpush only) | UTC | none | minutes; gaps permanent | none, if delivered |
| Traffic aggregate | Cloudflare GraphQL | bucket × dimension tuple | `(dataset, window, tuple)` | UTC | none | no documented SLA `[U]` | adaptive; `avg.sampleInterval` |
| Security event | Cloudflare `firewallEventsAdaptive` | 1 event | request id unconfirmed `[U]` (OQ-4) | UTC | none | 31-day retention | adaptive |
| Session | GA4 Data API | session-scoped aggregate | `(property, date_local, tuple)` | property zone | property currency | 24–48 h | thresholding |
| User | GA4 Data API | user-scoped aggregate | not exposed | property zone | property currency | 24–48 h | thresholding; non-additive across days |
| Item interaction | GA4 Data API | event × item | `itemId` | property zone | property currency | 24–48 h | thresholding |
| Order | Magento REST | 1 order | `increment_id` | store zone | store / base currency | near-real-time, revisable | none |
| Order line | Magento `/V1/orders/items` | order × SKU | `(increment_id, sku)` | store zone | store / base currency | near-real-time, revisable | none |
| SKU mapping | Gateway PostgreSQL | `(instance, normalized_item_id)` | `id` | UTC | n/a | immediate | none |

Two consequences drive the rest of this document. The only key shared by all three sources is a calendar day plus an instance, because no identifier is common to a request, a session, and an order. And a "user" exists only in GA4: Cloudflare `uniq.uniques` is an edge-side approximation and Magento `customer_id` is a commerce account, neither being a GA4 user.

## 3. Source-of-truth matrix

| Metric | Authoritative source | Authoritative for | Comparison-only | Never substituted by |
|---|---|---|---|---|
| Requests | Cloudflare `httpRequests*`; Logpush for exact counts | Edge request volume | — | GA4 sessions |
| Sessions | GA4 `sessions` | Client-side tagged visits | — | Cloudflare `uniq.uniques` or `sum.visits`; request counts |
| Users | GA4 `totalUsers` (period totals only) | Distinct tagged users | — | `uniq.uniques`; Magento customers |
| Page views | GA4 `screenPageViews` | Tagged page views | — | Cloudflare requests (assets, bots) |
| Orders | Magento order count | Booked orders | GA4 `ecommercePurchases` | GA4 transactions |
| Revenue | Magento `base_grand_total` (base) / `grand_total` (store) | Booked revenue | GA4 `purchaseRevenue` (attributed revenue) | A blended figure |
| AOV | Magento revenue ÷ Magento orders | Booked average order value | GA4 `averagePurchaseRevenue` | — |
| Conversion rate | Magento orders ÷ GA4 sessions, labelled cross-source | Orders per tagged session | — | GA4 `sessionKeyEventRate` |
| Add to cart | GA4 `addToCarts` / `itemsAddedToCart` | Cart activity | — | Nothing; Magento carts are out of scope `[V-docs]` |
| Items purchased | Magento order-line `qty_ordered` | Units sold | GA4 `itemsPurchased` (tagged) | GA4 item counts |
| Bot share | None on Pro; Logpush + Bot Management on Enterprise; otherwise a bound from security events | Bot pressure, as an interval | GA4's implicit bot exclusion (unquantified) | `1 − bot_share` as "human share" |
| WAF actions | Cloudflare `firewallEventsAdaptive*` | Rule and action counts | — | `sum.threats`; status-code counts |

### 3.1 Non-equivalences every relevant result must state

| Non-equivalence | Why it is dangerous |
|---|---|
| Requests ≠ Sessions | A request is an edge event for any asset; a session is a tagged visit. The ratio is not traffic quality and not a bounce proxy. |
| GA4 Purchase Revenue ≠ Magento Revenue | GA4 revenue comes from `purchase` event `value`/`currency` attributed to a GA4 session `[V-live]`; Magento revenue is the booked total including tax, shipping, and discounts. |
| GA4 Users ≠ Magento Customers | GA4 users are device-scoped and thresholded; Magento customers are accounts, and guest checkout yields an order with no customer. |
| Cloudflare bot-classified requests ≠ GA4 bot-filtered sessions | Cloudflare classifies at the edge before JavaScript; GA4 filters a different list after tagging. The sets are neither equal nor nested. |
| `itemId` ≠ SKU | `itemId` is client-supplied and often, but not always, the SKU. Equality is a mapping claim with a confidence. |
| GA4 transactions ≠ Magento orders | One order can yield several `purchase` events (retries, multiple sessions) and vice versa (consent denied, ad blocker, app order). |
| `uniq.uniques` ≠ `totalUsers` | Different population, identifier, and clock. |
| Cloudflare `sum.visits` ≠ GA4 sessions | An edge-side visit model versus a tagged session affected by consent, ad blockers, and client-side timeouts. Neither identifier nor clock agrees. |
| Security event count ≠ request count | One request can fire several rules; events are never additive with requests `[V-docs]`. |
| `sum.threats` ≠ WAF actions | A legacy mitigated-request counter with no per-action or per-rule identity. |

## 4. Time model

All timestamps are stored and transported in UTC (`core/models.TimeRange`); local rendering is a property
of the result, never of storage. Each source keeps its own calendar:

| Source | Day boundary | Configurable | Consequence |
|---|---|---|---|
| Cloudflare | UTC | no | A local day is an arbitrary UTC interval, not a UTC calendar day. |
| GA4 | property zone (`America/Los_Angeles` for `properties/354542598` `[V-live]`) | no | `dateRanges` accept `YYYY-MM-DD` only, so GA4 is queryable only over whole property-local days. |
| Magento | store zone (instance configuration) | yes | Date filters use `Y-m-d H:i:s` in the store zone; the gateway converts UTC inputs and says so. |

**T1 — GA4 sets the alignment unit.** A comparison including GA4 must use a whole number of GA4
property-local days, with UTC bounds derived from those boundaries; a partial GA4 day cannot be requested.
**T2 — GA4-free comparisons** may use any declared zone: the Magento store zone by default, UTC on request.
**T3 — Alignment must hold in every participating zone**, else the offending source carries
`incomplete_window` plus its actual covered interval.
**T4 — A day is not always 86,400 seconds**: `rate_per_24h = value × 86400 / duration_seconds(window)`.
Local days are compared as local days, never as hours.
**T5 — Compare like windows**: equal local-day count, same grain, same weekday composition
(`same_weekday_last_week`, not "previous 7 days" for a weekly question), both past the common watermark
(§6), and no rule or plan change inside either window. A violation is labelled, never silently computed.
**T6 — Half-open intervals** `[start, end)`. Cloudflare already filters `datetime_geq`/`datetime_lt`
`[V-docs]`; Magento and GA4 bounds are converted so a boundary order is counted once.

```text
Property-local day 2026-11-16 (PST, UTC-8)  [2026-11-16T08:00:00Z, 2026-11-17T08:00:00Z)   86,400 s
Fall-back day 2026-11-01 (25 h)            [2026-11-01T07:00:00Z, 2026-11-02T08:00:00Z)   90,000 s
Spring-forward day 2026-03-08 (23 h)       [2026-03-08T08:00:00Z, 2026-03-09T07:00:00Z)   82,800 s
7 local days 2026-10-26..2026-11-01 (contains the fall-back day)
  duration 169 h = 608,400 s; previous 7 local days = 168 h = 604,800 s
  requests_per_24h = R x 86400 / 608400
```

A caller asking for the UTC day `2026-11-16T00:00:00Z..2026-11-17T00:00:00Z` with GA4 participating gets
no GA4 figure: that UTC day covers local 2026-11-15 16:00 through 2026-11-16 16:00, which GA4 cannot
express. The tool returns `incomplete_window` with
`aligned_alternative: {start_utc: 2026-11-16T08:00:00Z, end_utc: 2026-11-17T08:00:00Z}`. Splitting a GA4
day pro rata is forbidden: it fabricates a number GA4 never reported.

**Labelling a non-aligned comparison.** `window.alignment` is `exact` (whole local days in every zone, one
UTC interval), `derived` (exact for every source but not a calendar day in at least one zone — typical of a
GA4-local day read against Cloudflare UTC reporting; flags `non_calendar_day`, arithmetic valid), or
`approximate` (a source cannot cover the interval exactly — GA4 partial day, retention clamp, filling
bucket; flags `incomplete_window`, `window_clamped` when clamped, and any derived ratio is
`directional_only`).

## 5. Currency model

Each instance declares one currency: Magento store currency from `GET /V1/store/storeConfigs`
(`base_grand_total` in base currency, `grand_total` in store currency); GA4 property currency (`USD`
`[V-live]`); Cloudflare none.

- **No implicit conversion, ever.** Money is `{amount, currency}` (`core/models.Money`, decimal string).
- **Conversion requires an explicit rate**: `rate`, `rate_date`, `rate_source`; the native value is always
  retained beside the converted one.
- **Mixed currencies are not summed**: `amount: null`, `mixed_currency: true`, per-currency parts.
- **A GA4/Magento currency mismatch blocks revenue ratios**: both native figures are returned with
  `currency_mismatch: true`, and no gap or share is computed.

```text
converted = {amount_native, currency_native, amount_converted, currency_target, rate, rate_date, rate_source}
mixed     = {amount: null, mixed_currency: true, parts: [{currency, amount}, ...]}
```

## 6. Latency and completeness

| Source | Freshness | Completeness signal | Revisable |
|---|---|---|---|
| Cloudflare GraphQL | no documented SLA `[U]`; last bucket filling | `complete_last_bucket: false`, `avg.sampleInterval` | yes, recent buckets restate |
| Cloudflare Logpush | minutes after delivery; gaps permanent, no backfill `[V-docs]` | ingest watermark, missing objects | no |
| GA4 | 24–48 h to completeness `[U]` | `samplingMetadatas`, `dataLossFromOtherRow`, `dataTruncationReasons`, thresholding | yes, D-1 revises |
| Magento | near-real-time | successful response, no truncation flag | yes: status, refunds, totals |

**Watermark rule.** `common_watermark = min(last_complete_day over participating sources)`. A window ending
after it is `partial` and may not be presented as a trend. **Asymmetric freshness.** The result is computed
on the older source's window and the fresher source is re-queried over that same window; reporting two
different windows as one comparison is forbidden.

| Flag | Trigger | Effect |
|---|---|---|
| `partial` | window end > watermark, or fewer rows than expected | ratios returned; trend language blocked in `caveats` |
| `sampled` | Cloudflare adaptive dataset, or non-empty GA4 `samplingMetadatas` | estimate labelled with `sample_interval` or confidence |
| `stale` | cache past TTL, or source `as_of` older than the freshness bound | `age_seconds` attached; never presented as current |
| `incomplete_window` | source cannot cover the exact interval | that source is `directional_only` |
| `mapping_incomplete` | SKU coverage below threshold (§8, coverage) | SKU figures suppressed; `unattributed` bucket returned |

Scenario-specific flags: `non_calendar_day`, `window_clamped`, `currency_mismatch`, `confounded` (rule,
plan, or ruleset changed inside the window), `capability_absent`, `event_grain_dedupe_unavailable`,
`bot_classification_unavailable`, `estimated`, `directional_only`.

## 7. Join strategy

```text
request (1 row)  --forbidden-->  session (1 row)  --forbidden-->  order (1 row)
    |                                |                               |
    v                                v                               v
request-day aggregate   <-->   session-day aggregate   <-->   order-day aggregate
(date, country, asn)           (date, country, channel)        (date, sku, status)
```

Aggregates join to aggregates of the same grain and window. The canonical keys are `(date_local,
instance)`, `(date_local, sku)`, `(date_local, channel)`, `(date_local, country)`. `date_local` is always
carried with its zone; a bare `date` is a defect, because the same string denotes different instants in
different zones. `instance` is a resolved **binding** — a declared `(ga4, magento, cloudflare)` triple with
the store view and zone that make the comparison meaningful — never a source name, and never guessed when
several are configured.

**Why per-request joins are forbidden.** No common identifier exists: Cloudflare has `RayID`, GA4 exposes
no `clientId` or `sessionId` in report output `[V-live]`, and Magento orders carry no request identifier.
GraphQL aggregates carry no request identity, and adaptive datasets expose sampled events rather than a
complete stream `[V-docs]`. Sampling rates differ per source and window, so a request-level join would
compare a 1% sample with a 10% sample and a census. Logpush is usage-billed and lossy with no backfill
`[V-docs]`, so a join across a gap silently changes the denominator. And one request can contribute to
several sessions or none, producing a fan-out that looks like data.

**With Logpush + ClickHouse**, request counts become exact for the covered period (`sampling: none`) and
request-level dimensions (path, ASN, user agent, status, cache, WAF action, bot score on Enterprise)
become available; security events join requests on `RayID`; request volume can be attributed to a URL,
hence heuristically to a SKU through a URL→SKU mapping that does not exist in the schema today.
Request↔session and session↔order joins remain impossible, because the missing identifier is absent from
GA4's output contract, not from our storage. ClickHouse changes the Cloudflare side's resolution only.

## 8. SKU mapping

**Direction and normalization.** GA4 `itemId` → Magento SKU; `itemSku` does not exist `[V-live]`.
Normalization is a stored generated column, not application code, so the index cannot drift from the
function and there is no second implementation to test:

```sql
normalized_item_id = lower(regexp_replace(btrim(ga4_item_id), '[[:space:]]+', ' ', 'g'))
```

| `match_method` | Confidence | Meaning | May inform a write |
|---|---|---|---|
| `exact` | ≥ 0.95 (DB constraint) | raw `itemId` equals the SKU | yes — the only write-relevant path |
| `manual` | = 1.00 (DB constraint) | operator-confirmed | yes |
| `normalized` | ≤ 0.90 (DB constraint) | normalized keys collide | no — proposes only |
| `heuristic` | any | fuzzy proposal | no — proposes only |

A stored normalized key is a proposal at confidence < 1.0 and **requires confirmation** before it can
inform a write. Case folding makes `ABC-1` and `abc-1` collide; the confidence constraint turns that
collision into a visible low-confidence row rather than a silent exact match.

**Conflicts.** `UNIQUE (instance_id, normalized_item_id) WHERE status='active'` means one item resolves to
one active SKU, while many item IDs may map to one SKU (variants, bundles). A second active mapping is
rejected by that index. Resolution is a new manual row superseding the old one in one transaction
(`UPDATE old SET status='superseded', superseded_by=new.id`), so both stay in the trail; an auto-generated
row may never supersede a manual row, and a rejected mapping stays `rejected` so the same wrong guess is
not regenerated.

**Coverage.** Report every applicable number, never one blended figure; threshold `0.90` for any
SKU-attributed claim. Below it the result carries `mapping_incomplete`, per-SKU figures are suppressed, and
the residual is returned as an explicit `unattributed` bucket.

```text
coverage_views   = SUM(views for mapped itemIds)       / SUM(views for all itemIds)
coverage_revenue = SUM(itemRevenue for mapped itemIds) / SUM(itemRevenue for all itemIds)
coverage_units   = SUM(units for mapped SKUs)          / SUM(units for all SKUs)
```

**The invariance rule.** A mapping never changes a revenue or volume total; it only re-attributes an
already-aggregated metric. For every window and source:

```text
SUM(itemRevenue over mapped itemIds) + itemRevenue(unattributed) = itemRevenue(total)
SUM(itemsViewed over mapped itemIds) + itemsViewed(unattributed) = itemsViewed(total)
```

This is a testable invariant (§12) and the reason a mapping change can never make a report's total disagree with the source. Mapping affects where a number is attributed, never how much.

**Unmapped reporting.** The read path reports the top-N unmapped `itemId` values by `itemsViewed` or
`itemRevenue`, with a candidate SKU and confidence when a proposal exists. Accepting a proposal writes to
`sku_mappings` and is therefore a separate `write`-scope tool with confirmation and an audit record; no
`unified.*` tool in the current catalog performs that write (§13 Q3).

## 9. The eight scenarios

Tools: (a) and (b) `unified.traffic.compare` (`dimension: country|asn` for b); (c) `unified.traffic.bot_vs_human`; (d) `unified.security.waf_impact`; (e) and (f) `unified.ecommerce.product_funnel`; (g) `unified.ecommerce.channel_revenue`; (h) `unified.analytics.sku_performance`. All numbers are symbolic, not observed data.

### 9.1 Cloudflare requests vs GA4 sessions

**Q.** How does edge request volume relate to tagged visit volume in the same window?
**Sources.** Cloudflare `httpRequests1hGroups` (`sum.requests`); GA4 `sessions`.
**Join.** `(date_local, binding)`, zone = GA4 property zone. **Window.** Whole property-local days past the watermark. **Flags.** `non_calendar_day` (Cloudflare), `partial` at the watermark.

```text
R = cf.requests(zone, window_utc)          # sum.requests over the interval
S = ga4.sessions(property, window_local)   # metricAggregations TOTAL, never the row sum
ratio = R / S;  delta_ratio = (ratio_t1 - ratio_t0) / ratio_t0
```

**Caveats.** `R` includes bots and static assets, so `ratio` has no absolute meaning; only its change and cross-window consistency are interpretable. `S` is post-tagging and post-GA4-bot-filtration, and neither is a "visitor" count. GA4 row sums differ from period totals (368,883 vs 370,599 `[V-live]`), so `S` uses `totals`.
**Misleading.** "Requests fell 12% while sessions held flat, so engagement improved." A ratio change with a flat denominator is equally consistent with fewer asset requests or less bot traffic. The result carries `numerator_includes_assets: true` and `bot_classification_unavailable` and refuses an engagement label.
**Worked.** `R = 3,000,000`, `S = 120,000` → `ratio = 25.0`. Next window `R = 2,640,000`, same `S` → `ratio = 22.0`, `delta_ratio = (22.0 − 25.0)/25.0 = −0.12` (−12.0%).

### 9.2 ASN and country traffic vs GA4 traffic

**Q.** Do edge-side country and network shares match tagged-session shares?
**Sources.** Cloudflare `httpRequestsAdaptiveGroups` (`clientCountryName`; `clientAsn` where the zone's dataset exposes it `[U]`); GA4 `sessions` by `country`.
**Join.** `(date_local, country)`; ASN has no GA4 counterpart. **Window.** Whole property-local days, both series summed over the same days. **Flags.** `sampled`, plus `partial` if a bucket is incomplete.

```text
share_cf(c)    = CF.requests(country=c) / SUM_c CF.requests
share_ga4(c)   = GA4.sessions(country=c) / SUM_c GA4.sessions
delta_pp(c)    = 100 * (share_cf(c) - share_ga4(c));  share_ratio(c) = share_cf(c) / share_ga4(c)
```

**Caveats.** Country is resolved by different means (edge IP geolocation vs GA4's own IP handling), so a systematic offset is expected and the comparison is about relative share, not equality. GA4 has no ASN or ISP dimension, so ASN analysis characterises Cloudflare traffic only and cannot be attributed to sessions.
**Misleading.** "Germany is 8% of traffic but 3.2% of sessions, so German users bounce." Edge requests include assets and bots, so a high-request, low-session country is more likely automated. The result reports share deltas with `sampled: true`, never labels the delta a bounce rate, and marks ASN rows `ga4_counterpart: absent`.
**Worked.** `share_cf(DE) = 240,000/3,000,000 = 0.0800`; `share_ga4(DE) = 3,840/120,000 = 0.0320`; `delta_pp = +4.80 pp`; `share_ratio = 2.50`. US: `0.5000` vs `0.6500` → `−15.00 pp`.

### 9.3 Bot traffic vs human traffic

**Q.** What share of edge traffic is automated, and how does it relate to session volume?
**Sources.** Cloudflare `firewallEventsAdaptiveGroups` by `source`/`action`; `httpRequests*` for the denominator; GA4 `sessions`.
**Join.** `(date_local, binding)`; no bot dimension joins to GA4. **Window.** Whole days, one day minimum. **Flags.** `bot_classification_unavailable` (Pro), `sampled`, `event_grain_dedupe_unavailable`.

```text
bot_actioned_events = SUM(events where source IN bot_sources)   # events, not requests
bot_share_upper     = bot_actioned_events / cf.requests         # upper bound only
bot_share_interval  = [0, bot_share_upper];  human_share = null
```

**Caveats.** On Pro there is no bot score, bot tag, or verified-bot classification — GraphQL and Logpush bot fields require the Enterprise Bot Management add-on `[V-docs]` — so the gateway reports `bot_score: null`, never `0` `[V-docs]`. Security events count only requests a bot feature acted on, and count events rather than requests, so the bound is loose. GA4 filters known bots from a different list after tagging, unquantifiably.
**Misleading.** "8.67% of traffic is bots, so 91.33% is human." The denominator is events not requests, only actioned bots are counted, and the complement of a bound is not a bound. The result returns an interval, sets `human_share: null`, and names the plan and required add-on.
**Worked.** `cf.requests = 3,000,000`, `bot_actioned_events = 260,000` → `bot_share_upper = 0.0867` (8.67%) → `bot_share_interval = [0%, 8.67%]`.

### 9.4 WAF challenge and action impact on visits

**Q.** Did a change in WAF challenge volume coincide with a change in sessions?
**Sources.** Cloudflare `firewallEventsAdaptiveGroups` (`action`, `datetime`); GA4 `sessions`.
**Join.** `(date_local, binding)`, optionally `(date_local, country)`. **Window.** Equal-length before/after windows, same weekday composition, both settled. **Flags.** `event_grain_dedupe_unavailable`, `confounded` if a ruleset changed, `sampled`.

```text
delta_sessions   = S_after - S_before
delta_challenges = C_after - C_before                  # action in {challenge, managed_challenge}
slope            = delta_sessions / delta_challenges   # descriptive only; causal = false
```

**Caveats.** This is a temporal association between two aggregates, not a causal effect. One request can produce both a challenge-issued and a challenge-solved event, so the delta is an event delta with no request-level deduplication at group grain. A rule deployed mid-window makes the comparison `confounded`, and the tool then returns both series and declines the slope. On Pro, log-only rules are unavailable `[V-docs]`, so no rule is observable without enforcing it.
**Misleading.** "Each challenge costs 0.58 sessions, so the WAF is destroying 3,500 visits." Traffic mix, campaigns, and seasonality also moved inside the window. The result names the figure `descriptive_slope`, sets `causal: false`, and requires `confounded: false` before emitting it.
**Worked.** `S: 100,000 → 96,500` → `delta_sessions = −3,500` (−3.50%); `C: 20,000 → 26,000` → `delta_challenges = +6,000` (+30.0%); `slope = −3,500/6,000 = −0.583`.

### 9.5 Product views vs Magento orders

**Q.** For each mapped SKU, how many item views precede a unit sold?
**Sources.** GA4 `itemsViewed` by `itemId`; Magento order lines (`qty_ordered`).
**Join.** `(date_local, sku)`, where `sku` comes from the mapping, never from `itemId`. **Window.** Whole property-local days; Magento filtered on `created_at` in the store zone. **Flags.** `mapping_incomplete`, `partial`; on the target property `capability_absent`.

```text
V(sku) = SUM(ga4.itemsViewed for itemIds mapped to sku)          # mapped subset only
U(sku) = SUM(magento.qty_ordered for order lines with that sku)  # orders created in window
v2o(sku) = U(sku) / V(sku)                                       # units per item view
```

**Caveats.** `itemsViewed` counts events, not people. Units are counted at order creation, before shipment and refund. The populations differ: a view can occur with no order, and an order with no view (app, marketplace, re-order). Magento orders belong to the store-zone day, so a 23:30 local order is in that day.
**Misleading.** Dividing total units by total views across all item IDs mixes a SKU-side numerator with an item-side denominator and changes when someone edits a mapping. The result computes on the mapped subset only, returns the residual separately, and applies the §8 invariance rule.
**Worked.** `V_total = 40,000`, `U_total = 500` → naive `0.01250`. With 92% of views and 94% of units mapped: `V = 36,800`, `U = 470` → `v2o = 0.01277`; the naive figure understates by 2.2%. On the target property `itemsViewed` returns 0 rows `[V-live]`, so the scenario is defined but not numerically validated: the tool returns `ecommerce_not_tracked`.

### 9.6 Add to cart vs actual sales

**Q.** What fraction of units added to cart become units sold?
**Sources.** GA4 `itemsAddedToCart` (item scope) and `addToCarts` (event scope); Magento `qty_ordered`.
**Join.** `(date_local, sku)` for the item-scope version; no key exists for the event-scope version. **Window.** Whole property-local days. **Flags.** `mapping_incomplete`; `capability_absent` on the target property.

```text
A(sku) = SUM(ga4.itemsAddedToCart for mapped itemIds)   # units, item scope  <- correct denominator
E      = SUM(ga4.addToCarts)                            # events, event scope <- unit mismatch
U(sku) = SUM(magento.qty_ordered);  atc_to_sale(sku) = U(sku) / A(sku)
```

**Caveats.** `addToCarts` counts `add_to_cart` events while `itemsAddedToCart` counts items, so the two differ whenever one event adds several units. Funnel stages are independent event counts, not a strict user-level sequence `[V-live]`. Magento has no cart data in scope `[V-docs]`, so the ratio is single-source with no cross-check.
**Misleading.** "17.24% of add-to-carts convert", computed as `500/2,900` on the event count, when the unit-correct figure is 15.63%. The result requires item-scope metrics for SKU ratios and rejects `addToCarts` as a denominator in the SKU path.
**Worked.** `A = 3,200`, `E = 2,900`, `U = 500` → `atc_to_sale = 500/3,200 = 0.1563` (15.63%); the event-based `500/2,900 = 0.1724` overstates by 10.3%.

### 9.7 GA4 channel revenue vs Magento revenue

**Q.** How far does GA4-attributed revenue diverge from booked order revenue, and how is it distributed?
**Sources.** GA4 `sessionDefaultChannelGroup` × `purchaseRevenue`; Magento orders.
**Join.** `(date_local, binding)` for the reconciliation; `(date_local, channel)` exists only in GA4. **Window.** Whole property-local days, both settled. **Flags.** `currency_mismatch`, `partial`, `capability_absent` on the target property.

```text
R_ga4_total = SUM(ga4.purchaseRevenue);  R_ga4(c) = SUM(ga4.purchaseRevenue where channel=c)
R_mag = SUM(magento.base_grand_total) over included statuses
gap = R_ga4_total - R_mag;  gap_ratio = gap / R_mag;  share_ga4(c) = R_ga4(c) / R_ga4_total
```

**Caveats.** Magento exposes no marketing channel on the order resource, so there is no per-channel Magento revenue and none is synthesised; only the total reconciles. Differences are expected: tax and shipping are in `base_grand_total` and may be absent from the GA4 `value`; refunds, cancellations, consent denial, ad blockers, and cross-device sessions move the figures apart. Status inclusion is an explicit parameter; refunds are excluded by default (`refunds_excluded: true`).
**Misleading.** "GA4 says Direct drove 92,250, so Direct revenue fell 18% short." The 18% is a total-level gap and cannot be allocated to a channel. The result refuses per-channel Magento revenue, labels channel rows `attribution_source: ga4`, and attaches the gap only at total level.
**Worked.** `R_ga4_total = 205,000`, `R_mag = 250,000` → `gap = −45,000`, `gap_ratio = −0.180` (−18.0%). Shares: Direct 45% → 92,250; Organic 30% → 61,500; Paid 15% → 30,750; other 10% → 20,500; sum 205,000.

### 9.8 Product traffic decline vs sales decline

**Q.** Did a decline in product traffic coincide with a smaller or larger decline in units?
**Sources.** GA4 `itemsViewed`; Magento `qty_ordered`; both per mapped SKU.
**Join.** `(date_local, sku)` over the same key set. **Window.** Two equal-length, same-weekday, settled windows. **Flags.** `mapping_incomplete`, `partial`, `capability_absent` on the target property, `directional_only` if either window is approximate.

```text
dV = (V_t1 - V_t0) / V_t0        # relative change in item views
dU = (U_t1 - U_t0) / U_t0        # relative change in units sold
elasticity = dU / dV;  unit_conversion(t) = U_t / V_t   # only when |dV| and |dU| >= 0.05
```

**Caveats.** `elasticity` is descriptive and bounded by both windows' mapping coverage; a SKU present in only one window is reported separately, never imputed. A decline caused by tracking loss (tag change, consent banner, template change) is indistinguishable from a demand decline without a third source, so falling `V` with flat Cloudflare requests for the same paths is flagged `tracking_suspect` where path grouping is available.
**Misleading.** "Views fell 20%, sales fell 8%, so conversion improved 15%." That is arithmetically true within the mapped subset and causally unsupported. The result reports both rates with their coverage, labels `elasticity` `descriptive_only`, and requires coverage before either rate.
**Worked.** `V: 10,000 → 8,000` → `dV = −0.200`; `U: 500 → 460` → `dU = −0.080`; `elasticity = −0.080/−0.200 = 0.40`. `unit_conversion` moves `0.0500 → 0.0575` (+15.0%). On the target property only the Magento half is computable, so the tool returns `ecommerce_not_tracked` for the GA4 side.

## 10. Output contract

Every `unified.*` result carries these fields; a missing one is a contract violation, on the same footing as a missing `meta.source` in [mcp-tools.md](mcp-tools.md) §5.

| Field | Content |
|---|---|
| `sources[]` | per source: `source`, `instance`, `time_zone`, `currency`, `as_of`, `freshness_seconds`, `sampling`, `rows_returned`, `query_hash` |
| `window` | `declared_zone`, `start_utc`, `end_utc`, `duration_seconds`, `grain`, `alignment`, `per_source_windows[]` |
| `formulas` | the exact expressions used, keyed by output name |
| `quality.flags[]` | from §6, each with the trigger that fired |
| `mapping` | when SKU-attributed: `coverage_views`, `coverage_revenue`, `coverage_units`, `threshold`, `unmapped_items`, `method_counts` |
| `caveats[]` | every §3.1 non-equivalence that applies |
| `instance_bindings` | the resolved binding triple and the store view used |
| `meta.retrieved_at` | UTC |

```json
{
  "data": { "comparison": "requests_vs_sessions",
            "series": [{ "date_local": "2026-11-16", "requests": 3000000, "sessions": 120000,
                         "requests_per_session": 25.0 }],
            "summary": { "requests_per_session": 25.0, "delta_ratio": null } },
  "meta": {
    "instance_bindings": { "ga4": "giztop", "magento": "store-us", "cloudflare": "storefront-primary" },
    "sources": [
      { "source": "cloudflare", "instance": "storefront-primary", "time_zone": "UTC", "currency": null,
        "as_of": "2026-11-19T00:00:00Z", "freshness_seconds": 900,
        "sampling": { "sampled": true, "sample_interval": 10 }, "rows_returned": 24, "query_hash": "sha256:..." },
      { "source": "ga4", "instance": "giztop", "time_zone": "America/Los_Angeles", "currency": "USD",
        "as_of": "2026-11-18T22:00:00Z", "freshness_seconds": 7200,
        "sampling": { "sampled": false }, "rows_returned": 1, "query_hash": "sha256:..." } ],
    "window": { "declared_zone": "America/Los_Angeles",
                "start_utc": "2026-11-16T08:00:00Z", "end_utc": "2026-11-17T08:00:00Z",
                "duration_seconds": 86400, "grain": "day", "alignment": "derived",
                "per_source_windows": [{ "source": "cloudflare", "non_calendar_day": true }] },
    "formulas": { "requests_per_session": "cf.requests / ga4.sessions" },
    "quality": { "sampled": true, "partial": false, "freshness_seconds": 7200,
                 "flags": [{ "flag": "non_calendar_day", "source": "cloudflare" }], "notes": [] },
    "mapping": null,
    "caveats": ["requests include static assets and bots", "sessions are post-tagging and post-bot-filter"],
    "retrieved_at": "2026-11-19T00:05:00Z"
  }
}
```

## 11. Failure and degradation

The tool returns a typed outcome, never a plausible-looking number. A zero must be a verified zero: a
successful upstream response with no matching rows, distinguished from `unknown`.

| Condition | Returned instead of a number |
|---|---|
| GA4 ecommerce `absent` | `ecommerce_not_tracked` with the probe window and 11-event evidence; no funnel, no ratio |
| GA4 capability `unknown` | figures with `capability: unknown` and a warning; never "zero sales" |
| Logpush disabled, request-level question asked | `requires_logpush` with reason, volume estimate, and cost reference |
| Property with no traffic in window | `insufficient_data` with verified `sessions: 0` and `directional_only`; no ratios |
| Magento instance with no orders in window | `orders: 0`, `verified_zero: true`, `no_orders`; a failed or truncated query returns `unknown` |
| Mapping coverage below 0.90 | `mapping_incomplete`, per-SKU figures suppressed, `unattributed` bucket and coverage returned |
| One source unavailable | `partial` with remaining sources labelled; no ratio computed across a missing source |
| Currency mismatch | native figures plus `currency_mismatch`; no gap, no share |
| Window not expressible for a source | `incomplete_window` plus `aligned_alternative`; never a pro-rata split |

## 12. Testing strategy for unified analytics

Unified tests are hermetic: no test in `internal/unified` may open a socket, matching
[testing-strategy.md](development/testing-strategy.md) §2. Sources are exercised through module fakes,
never through recorded vendor responses at this layer.

| Layer | Proves | Shape |
|---|---|---|
| Golden files | each of the eight scenarios over synthetic daily aggregates, byte-compared | one directory per scenario: input aggregates, expected result, expected flags |
| Invariants | mapped + unattributed = total for revenue, views, units (§8, invariance rule) | property-style over generated mappings |
| Time zone and DST | local day ↔ UTC window round-trip; no gaps or overlaps when partitioning a range | property-based: random instants, random IANA zones, `America/Los_Angeles` DST edges |
| Duration | `duration_seconds` = 86,400 except 82,800 / 90,000 on transition days | table-driven with the dates in §4 |
| Alignment refusal | a UTC day with GA4 participating returns `incomplete_window`, never a split | negative test per GA4 scenario |
| Currency | no implicit conversion; mixed currencies yield `amount: null` and parts | table-driven |
| Coverage | threshold behaviour at 0.89 / 0.90 / 0.91; suppression and residual bucket | boundary tests |
| Determinism | fixed clocks, no `time.Now()` in assertions, stable ordering | suite-wide rule |

Fixtures are synthetic and labelled as such; a fixture resembling real customer data is a defect. Golden
files change only with a reviewed reason, because a silent golden update is how a semantic regression ships.

## 13. Open questions and decision record

| ID | Question | Why it matters | Resolution |
|---|---|---|---|
| Q1 | With store views in different zones, which zone is canonical? | alignment is declared once per binding | configuration review; propose the GA4 zone when GA4 participates |
| Q2 | Does the target Magento store zone equal `America/Los_Angeles`? | if not, every GA4 comparison is `derived`, not `exact` | read `GET /V1/store/storeConfigs` on staging |
| Q3 | Which tool owns the mapping write path? | Resolved: `unified.mapping.sku_propose` (write, proposal only) and `unified.mapping.sku_confirm` (destructive, requires confirmation) own it; all other `unified.*` tools are read-only | closed in ADR-0014 follow-up |
| Q4 | Does scenario (h) need a dedicated tool? | it currently rides `unified.analytics.sku_performance` | catalog review in Phase 5 |
| Q5 | How much do `(other)` rows and thresholding distort share comparisons? | §9.2 and §9.7 rest on shares | flagged vs unfiltered comparison on a high-cardinality dimension |
| Q6 | Does Cloudflare expose a path dimension on `httpRequestsAdaptiveGroups`? | asset share and `tracking_suspect` depend on it | `settings` probe per zone; Logpush is the fallback |
| Q7 | Which FX source is authoritative when conversion is requested? | conversion needs a declared source | operator decision; no default provider |

[ADR-0005](adr/0005-ga4-integration.md) already fixes three constraints this document depends on: Data API
only, a three-state ecommerce capability profile, and revenue never silently joined to Magento. The
decisions below are this document's own and require a future unified-analytics ADR — not yet written; the
the next free number is 0016 at the time of writing (0014 covers confirmation and bindings, 0015 covers jobs).

1. Unified results are comparisons with declared semantics; no blended metric exists anywhere in the API.
2. GA4's property-local day is the alignment unit whenever GA4 participates; pro-rata splitting is forbidden.
3. Aggregates join only to aggregates, on `(date_local, instance)`, `(date_local, sku)`, `(date_local, channel)`, or `(date_local, country)`.
4. Request-level joins are out of scope until Logpush plus ClickHouse exist, and even then only within the Cloudflare side.
5. Magento is authoritative for orders and revenue; GA4 `purchaseRevenue` is attributed revenue, labelled as such everywhere.
6. Bot share without Bot Management is an interval with `human_share: null`.
7. A SKU mapping never changes a total; it only re-attributes an aggregated metric.
8. Every result carries sources, formulas, windows, zones, currencies, quality flags, mapping coverage, and caveats, or it is not returned.
