# Google Analytics 4 module

Read-only, aggregate-only access to the GA4 Data API v1 for one property per
instance.

## Public surface

- `NewClient(Config)` / `NewService(Config)` — report client with a token
  source (service account or static token for development).
- `Traffic`, `Metadata`, `Capabilities`, `Realtime` — reports and property
  discovery.
- `Validate` / `ValidateRealtime` — pair-wise dimension/metric compatibility.
- `NewServiceAccountTokenSource` — RS256 JWT-bearer with cached, single-flight
  token refresh.
- `Register(reg, svc)` / `RegisterExtra(reg, svc)` — read tools.

## Behavior notes

- The Data API is aggregate-only; raw events belong to a BigQuery export.
- Ecommerce is probed, never assumed: `Capabilities` returns evidence.
- Results carry the property time zone and currency; incompatible field pairs
  are rejected before the HTTP call with an actionable message.

## Tests

`go test -race ./internal/ga4/...` — fake Data API, token endpoint with a
generated RSA key, compatibility table, capability probe, tool calls.
