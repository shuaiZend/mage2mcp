# Cloudflare evidence log

**Purpose:** what was verified about the Cloudflare integration, how to re-verify it, and what a
live probe may print. **Date:** 2026-11-18. **Environment:** no Cloudflare credentials were
available, so live probes are `[U]` and documentation facts are `[V-docs]`. **Companion:**
[Cloudflare integration](../integrations/cloudflare.md).

## Credentials

| Variable | Used for | Required scope |
|---|---|---|
| `CLOUDFLARE_API_TOKEN` | Every probe | `Analytics Read` or `Account Analytics Read`, `Zone Read`; `Logs Write` only for P6 |
| `CLOUDFLARE_ACCOUNT_ID` / `CLOUDFLARE_ZONE_ID` | Account- and zone-scoped probes | Account the token belongs to, and one zone in it |

## Probe commands

Read-only `GET`s plus GraphQL `POST`s with fixed bodies; every probe projects structural facts through `jq`.

### P1 — token liveness

```bash
curl -sS "https://api.cloudflare.com/client/v4/user/tokens/verify" \
  -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
  | jq '{success, status: .result.status, has_expiry: (.result.not_before != null)}'
```

Expected `success: true`, `status: "active"` [V-docs](https://developers.cloudflare.com/fundamentals/api/troubleshooting/). Record the status only, never `.result.id`.

### P2 — zones and plan labels

```bash
curl -sS "https://api.cloudflare.com/client/v4/zones?per_page=50" \
  -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
  | jq '{count: (.result | length), plans: [.result[].plan.name] | unique, id_prefixes: [.result[].id[0:6]]}'
```

Expected: zone count, distinct plan names, six-character id prefixes. `plan.name` is only a label; the
authoritative plan is `rate_plan.id` from `GET /zones/{zone_id}/subscription`
[V-docs](https://developers.cloudflare.com/api/resources/zones/subresources/subscriptions/methods/get/).

### P3 — dataset availability and node limits

```bash
curl -sS "https://api.cloudflare.com/client/v4/graphql" \
  -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" -H "Content-Type: application/json" \
  --data '{"query":"query($z:string){viewer{zones(filter:{zoneTag:$z}){settings{
    httpRequests1mGroups{enabled maxDuration maxPageSize notOlderThan}
    httpRequests1hGroups{enabled maxDuration}
    httpRequestsAdaptiveGroups{enabled maxDuration maxPageSize}
    firewallEventsAdaptive{enabled maxDuration notOlderThan}
    firewallEventsAdaptiveGroups{enabled maxDuration}}}}}",
    "variables":{"z":"'"$CLOUDFLARE_ZONE_ID"'"}}' | jq '.data.viewer.zones[0].settings'
```

Expected: one object per node with `enabled: true` and numeric limits, which seed the gateway's
window and page-size clamps [V-docs](https://developers.cloudflare.com/analytics/graphql-api/features/discovery/settings/).

### P4 — bounded `httpRequests1hGroups`

```bash
END=$(date -u +%Y-%m-%dT%H:00:00Z); START=$(date -u -v-2H +%Y-%m-%dT%H:00:00Z 2>/dev/null || date -u -d '2 hours ago' +%Y-%m-%dT%H:00:00Z)
curl -sS "https://api.cloudflare.com/client/v4/graphql" \
  -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" -H "Content-Type: application/json" \
  --data '{"query":"query($z:string,$a:Time,$b:Time){viewer{zones(filter:{zoneTag:$z}){
    httpRequests1hGroups(limit:3,filter:{datetime_geq:$a,datetime_lt:$b},orderBy:[datetimeHour_ASC]){
    dimensions{datetimeHour} sum{requests bytes}}}}}}",
    "variables":{"z":"'"$CLOUDFLARE_ZONE_ID"'","a":"'"$START"'","b":"'"$END"'"}}' \
  | jq '{errors: (.errors // [] | length), rows: (.data.viewer.zones[0].httpRequests1hGroups | length),
         fields: (.data.viewer.zones[0].httpRequests1hGroups[0] // {} | keys)}'
```

Expected `errors: 0`, `rows <= 3`, `fields: ["dimensions","sum"]`; record those three facts only.

### P5 — bounded `firewallEventsAdaptiveGroups`

```bash
END=$(date -u +%Y-%m-%dT%H:00:00Z); START=$(date -u -v-2H +%Y-%m-%dT%H:00:00Z 2>/dev/null || date -u -d '2 hours ago' +%Y-%m-%dT%H:00:00Z)
curl -sS "https://api.cloudflare.com/client/v4/graphql" \
  -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" -H "Content-Type: application/json" \
  --data '{"query":"query($z:string,$a:Time,$b:Time){viewer{zones(filter:{zoneTag:$z}){
    firewallEventsAdaptiveGroups(limit:10,filter:{datetime_geq:$a,datetime_lt:$b},orderBy:[count_DESC]){
    count dimensions{action source} avg{sampleInterval}}}}}",
    "variables":{"z":"'"$CLOUDFLARE_ZONE_ID"'","a":"'"$START"'","b":"'"$END"'"}}' \
  | jq '{errors: (.errors // [] | length), rows: (.data.viewer.zones[0].firewallEventsAdaptiveGroups | length),
         actions: [.data.viewer.zones[0].firewallEventsAdaptiveGroups[].dimensions.action] | unique,
         sample_intervals: [.data.viewer.zones[0].firewallEventsAdaptiveGroups[].avg.sampleInterval] | unique}'
```

Expected `errors: 0`, `rows <= 10`, documented action values. Record row counts, field names, action
names and sample intervals only — never counts, byte totals, rule ids, paths, user agents or IPs.

### P6 — Logpush inventory and bot-field entitlement (requires `Logs Write`)

```bash
B="https://api.cloudflare.com/client/v4/zones/$CLOUDFLARE_ZONE_ID/logpush"
curl -sS "$B/jobs" -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
  | jq '{jobs: (.result | length), datasets: [.result[].dataset] | unique, enabled: [.result[].enabled] | unique}'
curl -sS "$B/datasets/http_requests/fields" -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
  | jq '{fields: (.result | length), bot_fields: ([.result[] | select(.name | test("^Bot|^JA[34]|^VerifiedBot"))] | length)}'
```

Expected: HTTP 200 with job summaries and a field list, or 403 without `Logs Write`
[V-docs](https://developers.cloudflare.com/logs/logpush/permissions/). Record job count, dataset names,
enabled flags, field count and the bot-field boolean — never `destination_conf` (it carries
credentials) or the full field list. Dataset ids are static and there is no "list datasets" endpoint
[V-docs](https://developers.cloudflare.com/api/resources/logpush/), so a 403 means a permission problem.

## Results

| Probe | Expected | Observed | Date | Status |
|---|---|---|---|---|
| P1 token liveness / P2 zones and plans | `status: "active"`; 200 with plan names | not run — credentials not provided | 2026-11-18 | `[U]` |
| P3 dataset settings | `enabled: true` for all five nodes | not run — credentials not provided | 2026-11-18 | `[U]` |
| P4 / P5 bounded GraphQL queries | 0 errors; ≤ 3 and ≤ 10 rows | not run — credentials not provided | 2026-11-18 | `[U]` |
| P6 Logpush jobs and fields | 200 with job summaries and field count | not run — credentials not provided | 2026-11-18 | `[U]` |
| Token verify shape; `Logs Write` requirement; `firewall_events` has no bot fields; Instant Logs Business+, Logpull Enterprise | documented | confirmed in docs | 2026-11-18 | `[V-docs]` |

## Redaction rules

| Never record | Instead record |
|---|---|
| Client or origin IP addresses; real customer hostnames | Nothing, or `zone-a`/`zone-b` aliases |
| Full zone or account ids | First six characters plus `…` |
| Tokens, `Authorization` headers, `destination_conf` | Presence or absence of the variable only |
| Full API or GraphQL responses; rule ids, paths, user agents, cookies | Row counts, field names, `enabled` flags, error counts, distinct action names |
| Free-text error bodies that may embed identifiers | Error class from the documented taxonomy |

Probe output is piped through `jq` before being written anywhere; raw responses are never redirected to a
file; and any probe that could return many rows is bounded with `limit` and a UTC window.

## Capability detection at startup

Run once per zone at boot and cache the result for 24 hours.

| Step | Call | Fact extracted | On failure |
|---|---|---|---|
| 1 | `GET /user/tokens/verify` | token active, expiry | Refuse to start |
| 2 | `GET /zones` | zone reachable by this token | Disable that zone, serve others |
| 3 | `GET /zones/{id}/subscription` | `rate_plan.id` | Assume the lowest plan, mark `plan_unknown` |
| 4 | GraphQL `settings` node | per-dataset `enabled`, `maxDuration`, `maxPageSize`, `notOlderThan` | Disable only the affected datasets |
| 5 | `GET .../logpush/jobs` and `GET .../datasets/http_requests/fields` (optional profile) | job presence, enabled flag, bot fields | Mark Logpush `unknown`; set `bot_score_available: false` |

Derived flags: `plan`, `graphql_available`, `security_events_available`, `logpush_entitled`, `bot_score_available`, `logpush_observed`; `instant_logs_available` is always `false` here.

### Surfacing degradation to agents

1. **Filter the tool list:** a tool whose dataset probe returned `enabled: false` is not registered.
2. **Annotate every response** with `capabilities` (`plan`, `bot_score_available`, `logpush_observed`) and a `degraded` array of `{capability, reason}`; `bot_score` is `null` with `bot_score_available: false`, never `0`.
3. **Fail loudly on configuration errors, softly on entitlement gaps:** a 403 is a startup error; a missing dataset is a degraded capability. Never retry a permission failure.
4. **Name the gap in the message:** `degraded: bot_score unavailable (plan)`, `degraded: logpush not configured`, `degraded: dataset disabled (firewallEventsAdaptive)`, or `window_clamped: true` when the window shrank.
