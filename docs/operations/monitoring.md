# Monitoring and alerting

## Health endpoints

| Endpoint | Use |
|---|---|
| `GET /healthz` | liveness probe; process is up |
| `GET /readyz` | readiness probe; at least one tool is registered |
| `GET /metrics` | Prometheus scrape |

Keep `/metrics` on a private listener or behind Access. It exposes no secrets
and no tool arguments, but it does reveal traffic patterns.

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

Structured JSON on stderr. One event per tool call with `tool`, `instance`,
`identity_id`, `permission`, `outcome`, `duration_ms`, and `param_hash`.
Parameters, tokens, and response bodies are never logged. Ship logs to the
operator's log store; do not rely on container stdout retention for audit —
audit lives in PostgreSQL.

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

- Quota headroom for Cloudflare GraphQL (300 queries / 5 min) and GA4 tokens
  (200,000 / day per property).
- Job queue depth and retry counts; a growing `import` queue means upstream
  throttling or an oversized batch.
- PostgreSQL connection saturation and audit table growth.
- Certificate and JWKS reachability for Cloudflare Access.
