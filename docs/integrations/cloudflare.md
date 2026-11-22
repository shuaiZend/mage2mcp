# Cloudflare integration

**Status:** as-built for v0.11.7
(2026-11-20). The shipped module is a read-only Cloudflare GraphQL Analytics
client with three registered tools. Raw-log ingestion is **PLANNED** and is not
in the tree.

**Verification boundary.** No live Cloudflare zone was available. Behaviour is
verified against the GraphQL contract and `httptest` fakes (`CONTRACT`). There is
no live integration test and nothing here is `INTEGRATION_TESTED`.

**Module:** `internal/cloudflare`. **Evidence:**
[cloudflare.md](../evidence/cloudflare.md). **Decision:**
[ADR-0004](../adr/0004-cloudflare-log-storage.md) (the R2/ClickHouse half is
PLANNED).

## 1. Scope

The module answers two questions for one zone:

- **Traffic:** hourly request, byte, and cache aggregates grouped by hostname,
  country, or edge status.
- **Security events:** sampled WAF/bot/challenge event counts by action, and a
  bounded page of individual events without client IPs.

It does not mutate Cloudflare. There is no DNS, WAF, cache, ruleset, Logpush, or
zone-settings write path, and no raw GraphQL passthrough.

## 2. Registered tools

These three tools are the entire Cloudflare surface. They are read-only and are
registered whenever the Cloudflare module is enabled with an instance.

| Tool | Upstream dataset | Window | Output | Limits |
|---|---|---|---|---|
| `cloudflare.analytics.traffic` | `httpRequests1hGroups` | ≤ 31 days, half-open `[since, until)` | Rows keyed by the chosen dimension with `requests`, `bytes`, `cached_requests`, `cached_bytes`; `sampled=false` (pre-aggregated) | `group_by` is `hostname`, `country`, or `status`; `limit` 1–1000, default 100 |
| `cloudflare.security.summary` | `firewallEventsAdaptiveGroups` | ≤ 30 days, half-open | `actions` map of action → count, `total`, `sampled=true`, `retention_days=31` | No caller-supplied dimensions; fixed action grouping |
| `cloudflare.security.events` | `firewallEventsAdaptive` | ≤ 30 days, half-open | Newest-first page of `time`, `action`, `rule_id`, `country`, `path`, `user_agent`; `sampled=true`, `retention_days=31`, `truncated` | `limit` 1–1000, default 100 |

The traffic response is a set of groups, not a time series with a timestamp
field: the schema projects the selected dimension and the window sums. If the
same dimension appears in more than one hourly bucket, the current response does
not expose the bucket, so callers should not treat the rows as a unique
per-dimension series. A per-hour projection is **PLANNED** (a schema change).

The security tools count **events**, not requests. One request can produce
several events when several rules fire, so `security.summary.total` is never
additive with `analytics.traffic.requests`.

## 3. Authentication and instances

| Item | As built |
|---|---|
| Credential | Cloudflare API token, sent as `Authorization: Bearer …`; the token is never logged or returned in an error |
| Scope | One zone per instance (`zone_id` required); `account_id` is accepted in configuration but no registered tool uses it |
| Base URL | `https://api.cloudflare.com/client/v4` in production; `http` is accepted only for loopback test servers |
| Routing | The `instance` argument selects the configured zone; per-instance routing is implemented through the resolver |
| Timeout | 30 s per upstream request |
| Response cap | 8 MiB; a larger response is `upstream_unavailable`, never truncated silently |
| Retries | None. `rate_limited` and `upstream_unavailable` carry `retryable`; the caller decides |

The token should be a read-only Analytics token (`Zone → Analytics → Read`).
The module never needs `Logs: Write`; the log-inspection profile described in the
original design does not exist.

## 4. Query construction

GraphQL is sent as `POST` to the fixed `/graphql` endpoint. The document is a
compiled template; the only caller-controlled variation is the traffic
dimension, which is selected from a closed allowlist (`hostname`, `country`,
`status`). There is no tool that accepts a GraphQL document, a field name, or a
raw filter.

Windows are parsed as RFC 3339 timestamps, converted to UTC, and sent as a
half-open range. An absent, reversed, or over-long window is `invalid_argument`
before any upstream call.

## 5. Data semantics

- **Traffic is pre-aggregated, not adaptive-sampled.** `httpRequests1hGroups`
  returns hourly aggregates; the response reports `sampled=false`. The current
  hour can be incomplete; treat the newest bucket as provisional.
- **Security data is adaptive-sampled.** Both security tools report
  `sampled=true` and `retention_days=31`. Counts are estimates, not a census.
- **Security retention is 31 days on every plan; the query ceiling is 30 days.**
  The tool enforces the ceiling before the call.
- **No client IPs.** `security.events` deliberately omits `clientIP`; event-level
  IPs are restricted data and are not exposed by this module.
- **Truncation is explicit.** `security.events` sets `truncated=true` when the
  page is full; it does not silently return a partial page as complete.
- **No complete request stream.** GraphQL Analytics cannot answer "every
  request"; only a raw-log pipeline could, and that pipeline is PLANNED.

## 6. Errors

| Upstream condition | Gateway code |
|---|---|
| 401/403, bad token or missing zone permission | `permission_denied` |
| 404 | `not_found` |
| 429 | `rate_limited` (retryable) |
| 5xx, reset, empty body, malformed JSON, response over 8 MiB | `upstream_unavailable` |
| GraphQL `errors` with a message | `upstream_unavailable` with a bounded message (200 bytes) |
| Invalid window, dimension, or limit | `invalid_argument` |
| Deadline | `upstream_timeout` |

## 7. Not implemented (PLANNED or absent)

| Capability | Status |
|---|---|
| Logpush job management, R2 archive, ClickHouse loader, Parquet | **PLANNED** (ADR-0004) |
| `cloudflare.logs.*` raw-log tools | **PLANNED** |
| Zone listing, status-code/top/cache/bot reports | **PLANNED** |
| Plan/entitlement detection at startup | **PLANNED**; the operator confirms the token's zone access |
| Raw GraphQL or REST passthrough | Absent by design |
| Any Cloudflare mutation | Absent by design |

The original document described a Logpush → R2 → ClickHouse inventory pipeline
with a separate write-capable log-inspection credential. None of that code
exists; it is retained only as the PLANNED design in ADR-0004.

## 8. Verification

| Test | What it proves |
|---|---|
| `TestTrafficMapsRows`, `TestTrafficValidation`, `TestTrafficEmptyZones` | Traffic rows, window/dimension/limit validation, empty zone |
| `TestSummaryAggregatesByAction`, `TestSummaryValidation` | Event counts by action and window validation |
| `TestEventsMapsRows`, `TestEventsValidation` | Event projection, ordering, limit validation |
| `TestDoResponseCap`, `TestDoTimeout`, `TestGraphQLErrors` | 8 MiB cap, deadline, bounded GraphQL errors |
| `TestRegisterMetadata`, `TestToolsListAndSchemas`, `TestMCPTrafficTool`, `TestMCPSecurityTool` | Registry metadata, closed schemas, MCP calls |
| `TestResolverMultipleZonesRequireName`, `TestRegisterWithResolverSelectsInstance` | Per-instance routing |

No live Cloudflare zone was available; the tests use `httptest` and the
documented GraphQL contract.

## 9. Related

- [Configuration](../configuration.md) — Cloudflare instance keys and routing.
- [MCP tools](../mcp-tools.md) — tool contract and error envelope.
- [ADR-0004](../adr/0004-cloudflare-log-storage.md) — raw-log decision, PLANNED.
- [Known limitations](../audit/known-limitations.md) §1.2 — no live Cloudflare.
