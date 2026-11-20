# Configuration reference

Configuration is YAML with `${VAR}` expansion and a small set of environment
overrides. Unknown keys are rejected at startup, so a typo fails loudly instead
of silently disabling a control. Secrets are always references, never literals.

## `server`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `listen` | string | `127.0.0.1:8080` | HTTP listener. Non-loopback requires authentication. |
| `public_url` | string | — | Public base URL behind the Cloudflare Tunnel. |
| `shutdown_grace` | duration | `10s` | Drain window on SIGTERM. |
| `max_body_bytes` | int | `1048576` | Maximum MCP request body. |

## `auth`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `enabled` | bool | `true` | Cloudflare Access JWT verification. |
| `issuer` | string | — | Expected `iss`; must be an https URL. |
| `audience` | string | — | Expected `aud`; the Access application audience. |
| `jwks_url` | string | — | JWKS endpoint; must be https. |

Environment overrides: `MAGE2MCP_AUTH_ISSUER`, `MAGE2MCP_AUTH_AUDIENCE`,
`MAGE2MCP_AUTH_JWKS_URL`.

## `log`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `level` | string | `info` | `debug`, `info`, `warn`, `error`. |
| `format` | string | `json` | `json` or `text`. |

## `tools`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `writes` | bool | `false` | Registers write tools. Every write requires two-step human approval. |

Environment override: `MAGE2MCP_TOOLS_WRITES=true`.

## `database`

Asynchronous import tools (`magento.import.*`) are registered only when
`database.url` is set: the job queue lives in PostgreSQL.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `url` | string | — | `postgres://` or `postgresql://` URL. Enables durable audit and confirmations. |
| `max_conns` | int | `10` | Connection pool size. |

Environment override: `MAGE2MCP_DATABASE_URL`.

## `datasources`

Each module has `enabled` and `instances`. All three modules accept several
instances: every tool call must then name an `instance`, and an unknown or
missing instance is rejected. Every write still requires two-step human approval; the store is selected by the
`instance` argument.

| Module | Instance keys |
|---|---|
| `magento` | `id`, `endpoint`, `store_code`, `credential` (`bearer:<token>`, `oauth1:<key>:<secret>:<token>:<token-secret>`, or `admin:<username>:<password>` for automatic token acquisition/refresh) |
| `cloudflare` | `id`, `account_id`, `zone_id`, `credential` (API token) |
| `ga4` | `id`, `property_id`, `credential` (service-account JSON or a static token for development) |

## `bindings`

Declares a Magento/Cloudflare/GA4 triple for unified tools.

```yaml
bindings:
  - id: store-us-analytics
    magento: store-us
    cloudflare: zone-us
    ga4: giztop
```

Validation: instance ids are globally unique across modules (audit rows, jobs, and
bindings reference them without a module qualifier); binding ids are unique;
every referenced instance must exist in an enabled module; a binding requires
both Cloudflare and GA4 to be enabled.

## Environment-only settings

| Variable | Effect |
|---|---|
| `MAGE2MCP_LISTEN` | Overrides `server.listen`. |
| `MAGE2MCP_PUBLIC_URL` | Overrides `server.public_url`. |
| `MAGE2MCP_LOG_LEVEL` | Overrides `log.level`. |

See [configs/config.example.yaml](../configs/config.example.yaml) for a complete
example and [operations/deployment.md](operations/deployment.md) for deployment
guidance.
