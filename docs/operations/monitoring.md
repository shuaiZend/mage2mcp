# Monitoring and alerting

## Health endpoints

| Endpoint | Use |
|---|---|
| `GET /healthz` | liveness probe; process is up |
| `GET /readyz` | readiness probe; the process can serve MCP, the registry tool count, and (when configured) the durable store `ReadyCheck` (503 when it fails) |
| `GET /metrics` | Prometheus scrape (behind auth on a non-loopback listener; open on loopback) |

`/metrics` is served on the same listener as `/mcp`. On a non-loopback listener
the server puts it behind the same auth middleware as `/mcp`; a loopback
development listener leaves it open. Restrict it with the edge/Access policy or
a private network in either case. It exposes no secrets and no tool arguments,
but it does reveal traffic patterns.

## Key metrics

| Metric | Type | Alert idea |
|---|---|---|
| `mage2mcp_tool_calls_total{module,tool,outcome}` | counter | error ratio above 5% for 10 min |
| `mage2mcp_tool_duration_seconds` | histogram | p95 above the tool timeout budget |
| `mage2mcp_upstream_requests_total{module,status}` | counter | sustained 5xx or 429 |
| `mage2mcp_upstream_duration_seconds` | histogram | upstream latency regression |
| `mage2mcp_auth_failures_total{reason}` | counter | spike indicates probing or misconfiguration |
| `mage2mcp_jobs_total{queue,state}` | counter | failed jobs growing |

## Logs

Structured JSON on stderr with key-based redaction. Logs are operational, not
the audit record: the per-call fields (subject hash, tool, instance, scope,
decision, phase, state, error code, request id) live in `audit_events`.
Parameters, tokens, and response bodies are never logged. Ship logs to the
operator's log store; do not rely on container stdout retention for audit.

## Audit queries

```sql
-- Denied calls in the last hour
SELECT occurred_at, tool, error_code FROM audit_events
WHERE decision = 'deny' AND occurred_at > now() - interval '1 hour'
ORDER BY occurred_at DESC;

-- Tool usage by identity class
SELECT identity_class, tool, count(*) FROM audit_events
WHERE occurred_at > now() - interval '24 hours'
GROUP BY 1, 2 ORDER BY 3 DESC;

-- Pending confirmations older than five minutes
SELECT id, tool, created_at FROM confirmation_tokens
WHERE state = 'pending' AND created_at < now() - interval '5 minutes';
```

## What to watch operationally

- Cloudflare GraphQL (documented 300 queries / 5 min) and GA4 token quota
  headroom in the vendor console. The gateway does **not** account for quotas or
  expose a quota metric.
- Job queue depth and retry counts; a growing `import` queue means upstream
  throttling or an oversized batch.
- `operations` rows in `unknown`/`reconciling`, and `operation_transitions` for
  escalations ("reconciliation attempts exhausted"). A growing unknown set is
  the signal to inspect Magento and the reconciler.
- A **stable** unknown count can be a known baseline rather than an alarm: rows
  whose upstream has since been reset or deleted are permanently undecidable,
  and they stay `unknown` on purpose. Record the baseline (count and ids) so a
  *rise* above it is what pages someone. Do not dispose of them as
  `not_applied` to make the view empty: that writes a human judgement the
  evidence cannot support, and `not_applied` also releases the derived
  idempotency key, which allows a retry of an operation nobody can prove was
  never applied. When the upstream is gone, look for proof inside the ledger's
  own timeline (did the precondition exist at the time of the attempt?) instead
  of concluding "not applied" because a current read finds nothing.
- Pending confirmations: the query above finds the abandoned ones. Expired and
  unconsumed `pending` rows are real records, not garbage — review them and let
  them expire, or delete them as a deliberate, recorded cleanup. Never approve
  them to shorten the list: approving a stale request records a decision nobody
  made. On a long-lived or demo environment the count can reach hundreds; treat
  growth as a hygiene signal and clean up on a schedule.
- PostgreSQL connection saturation and audit table growth (no retention job or
  partitions; plan an export/retention procedure).
- Certificate and JWKS reachability for Cloudflare Access.
- `/metrics` is authenticated on non-loopback listeners; on loopback development
  it is open, so do not expose that listener off-host.
