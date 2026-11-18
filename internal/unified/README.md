# Unified analytics module

Compares data from several sources with declared semantics. It never produces a
single blended number.

## Public surface

- `Binding` — a declared Magento/Cloudflare/GA4 triple.
- `NewService(bindings, trafficProvider, sessionsProvider)`.
- `Compare(ctx, bindingID, from, to)` — Cloudflare requests vs GA4 sessions.
- `Register(reg, svc)` — `unified.traffic.compare`.

## Behavior notes

- Providers are fetched concurrently; the window is UTC and at most 31 days.
- `requests_per_session` is `null` when sessions are zero.
- Quality notes always state that requests are not sessions, plus sampling and
  missing-data flags.
- The module depends only on the narrow provider interfaces it declares; it
  never imports another module's package.

## Tests

`go test -race ./internal/unified/...` — fake providers, ratio and zero-data
cases, error propagation, config validation, in-memory MCP call.
