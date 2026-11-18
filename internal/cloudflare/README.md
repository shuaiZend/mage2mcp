# Cloudflare module

Read-only access to Cloudflare analytics and security data through the official
v4 API and GraphQL Analytics API.

## Public surface

- `NewClient(Config)` — zone-bound client with a bearer token, 8 MiB cap.
- `NewService(*Client)` — `Traffic` (hourly aggregates), `Summary` (security
  events by action), `Events` (bounded sampled event page).
- `Register(reg, svc)` — the three read tools.

## Behavior notes

- One instance is exactly one zone; the zone tag is injected, never caller
  supplied.
- GraphQL documents are fixed templates; there is no raw passthrough.
- Security data is adaptive-sampled with 31-day retention and is disclosed as
  such. Client IPs are never requested.
- No mutation endpoint exists in the code path; the module cannot write.

## Tests

`go test -race ./internal/cloudflare/...` — fake GraphQL server asserting the
query and variables, error mapping, window/limit validation, tool calls.
