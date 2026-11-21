# Configuration reference

Configuration is YAML with `${VAR}` expansion and a small set of environment
overrides. Unknown keys are rejected at startup (`KnownFields(true)`), so a typo
fails loudly instead of silently disabling a control. Validation reports every
problem it finds, not only the first.

Three configuration files ship with the repository:

| File | Use |
|---|---|
| [`configs/config.example.yaml`](../configs/config.example.yaml) | Development: loopback listener, authentication disabled, no data sources. Copy and run; it validates with no environment at all. |
| [`configs/config.production.template.yaml`](../configs/config.production.template.yaml) | Production template: authentication enabled; PostgreSQL and data sources are expected to be filled in. It does **not** validate until the `MAGE2MCP_AUTH_*` values exist — that is deliberate. |
| [`configs/config.docker.yaml`](../configs/config.docker.yaml) | Baked into the container image at `/etc/mage2mcp/config.yaml`. Exposes only `platform.info` until a real configuration is mounted over it. |

The development quickstart is exactly:

```bash
go build -o bin/mage2mcp ./cmd/server
cp configs/config.example.yaml config.yaml
./bin/mage2mcp -config config.yaml
curl -s localhost:8080/healthz
curl -s localhost:8080/readyz
```

The gateway refuses to start with authentication disabled on a non-loopback
listener unless `-dev-insecure` or `MAGE2MCP_DEV_INSECURE=true` is set.

`-healthcheck` is the container liveness probe, not a configuration validator:
it reads `MAGE2MCP_LISTEN` (default `127.0.0.1:8080`) and requests `/healthz`
from an already-running process. It does not load `-config`. To validate a
configuration file, start the server normally; `config.Load` runs before the
listener is opened.

## `server`

| Key | Type | Default | Validation / effect |
|---|---|---|---|
| `listen` | string | `127.0.0.1:8080` | Required. `host:port` the HTTP server binds. A non-loopback address with authentication disabled is refused at startup. |
| `shutdown_grace` | duration | `10s` | Must not be negative. Drain window on `SIGTERM`. |
| `max_body_bytes` | int64 | `16777216` (16 MiB) | Must be positive. Maximum MCP request body; also passed to the Streamable HTTP handler, which answers `413 request body exceeds N bytes` beyond it. With `tools.writes: true` it must also be at least `12582912` (12 MiB) — see below. |

### Why the body cap is 16 MiB

The cap is sized for the largest supported operation, not the median one: a
maximum-size product import. An import travels base64-encoded inside the MCP
request body, and the import ceiling is a documented capability of 8 MiB
decoded / 5,000 rows.

| Quantity | Bytes |
|---|---|
| Import ceiling, decoded | `8388608` (8 MiB) |
| Same payload, base64 | `11184812` |
| JSON envelope (`instance`, `format`, `data_base64`, `update_existing`, `idempotency_key`) | ~`162` measured |
| Minimum body cap that fits | `11188908` |
| Shipped default | `16777216` (16 MiB) — 33% headroom |
| Minimum accepted with `tools.writes: true` | `12582912` (12 MiB) |

Raising the cap is a deliberate trade, not an oversight:

- The listener is authenticated by default (`auth.enabled: true`), and a
  non-loopback listener with authentication disabled is refused at startup.
- Every import **write** additionally requires a two-step human confirmation, so
  an unauthenticated caller cannot drive the import path at all.
- The cap is a hard ceiling, not a buffer that grows: the handler reads at most
  this many bytes and answers `413` beyond it, so a hostile client cannot make
  the process allocate more than one body per in-flight request.
- Peak memory for one in-flight maximum import is roughly 42 MB: 16 MiB body +
  11.2 MiB base64 string + 8 MiB decoded + ~7 MB of parsed rows (the last two
  measured on a 5,000-row CSV).
- Deployments that never import can lower it. A **write-enabled** deployment
  that lowers it below 12 MiB fails validation at startup with the exact numbers
  rather than rejecting a large import at runtime with a `413`; a read-only
  deployment (`tools.writes: false`) may set any positive value, and a
  `magento.import.validate` call larger than the cap then gets the transport's
  `413` instead of being silently truncated.

The three limits — the JSON-Schema `maxLength`, the decoder's limit, and this
body cap — are pinned to each other by
`TestImportLimitsAgreeAcrossPackages`, so raising the import ceiling without
raising the cap fails the test suite.

`server.public_url` and `MAGE2MCP_PUBLIC_URL` were **removed**. The server never
read them, so a key that could not change behaviour was deleted rather than
kept as a decoy. A configuration that still sets `public_url` now fails with an
unknown-field error; delete the key. This is an intentional pre-1.0 breaking
change.

## `auth`

| Key | Type | Default | Validation / effect |
|---|---|---|---|
| `enabled` | bool | `true` | Enables Cloudflare Access JWT verification. When `false`, every request is anonymous; allowed only on a loopback listener unless the dev-insecure flag is set. |
| `issuer` | string | — | Required when enabled; must start with `https://`. Expected `iss` claim (the team domain). |
| `audience` | string | — | Required when enabled. Expected `aud` claim (the Access application AUD tag). |
| `jwks_url` | string | — | Required when enabled; must start with `https://`. JWKS endpoint used to fetch signing keys lazily. |

## RBAC and instance isolation

Authorization is compiled into the binary; there is no YAML policy file.
With authentication enabled, `cmd/server` builds
`rbac.DefaultPolicy(configuredInstances(cfg))` at startup from the same instance
map the resolvers and `platform.info` use, and that policy is the authorizer.
With authentication disabled no policy is built at all: `authorizeFunc` returns no
authorizer, so every call is anonymous and none of the rules below are consulted.

- **One instance per module is the shipped default.** The policy binds each
  configured module instance to the role ladder: `viewer`/`analyst` read,
  `operator` write, `admin` admin. A grant for one instance never authorizes
  another.
- **Every grant must name an instance.** `rbac.New` rejects an empty instance with
  `rbac: grant 0 has no instance; name one, or use "*" for every instance`.
  Empty string is **not** a wildcard.
- **Wildcard is explicit.** `matchesInstance` accepts an exact id or the literal
  `rbac.WildcardInstance` (`"*"`). `platform.info` is granted at `"*"` for every
  role because it returns only gateway metadata, never store data.
- **With authentication enabled, multiple instances in one module fail startup.**
  The default ladder cannot express "store A yes, store B no", so it refuses
  instead of silently granting every store:

  ```text
  mage2mcp: rbac: magento has 2 instances (demo, demo-alt); the default policy binds one instance per module. Grant per-instance policies explicitly, or call DefaultPolicyAllInstances() to treat them as one trust domain
  ```

  This is the SEM-02 security default, not a configuration fault: silently
  binding every store would recreate the cross-instance grant the policy exists to
  prevent. The check runs only when authentication is enabled, because that is the
  only mode where a policy is built and an authorizer consulted. With
  `auth.enabled: false` the gateway starts and every call is anonymous.

**How to resolve a multi-instance module.** Pick one:

1. Configure one instance per module (the normal shape: one Magento store, one
   Cloudflare zone, one GA4 property). No code change.
2. Write per-instance grants explicitly with `rbac.New` and pass that policy to
   the server. This is a code change; there is no YAML policy surface yet.
3. Opt in to one trust domain with `rbac.DefaultPolicyAllInstances()` instead of
   `rbac.DefaultPolicy(...)`. This is a code change in `cmd/server`; every grant
   becomes `"*"`, so **any role grant applies to every instance** and the
   cross-instance isolation is gone. Use it only when all configured instances
   belong to one tenant.

Disabling authentication does avoid the startup error — no policy is built, so the
multi-instance rule never runs — but it is not a supported way to run a
multi-store deployment: `auth.enabled: false` makes every tool anonymous and is
refused on a non-loopback listener. Treat it as a loopback-only development
shortcut, not a configuration.

## `log`

| Key | Type | Default | Validation / effect |
|---|---|---|---|
| `level` | string | `info` | One of `debug`, `info`, `warn`, `error`. |
| `format` | string | `json` | `json` or `text`. |

## `tools`

| Key | Type | Default | Validation / effect |
|---|---|---|---|
| `writes` | bool | `false` | Registers the write and destructive tools. Every write still requires two-step human approval; the approval surface (`mage2mcp-admin`) requires `database.url`, so a write-enabled deployment without PostgreSQL registers writes that cannot be approved. |

## `database`

The PostgreSQL store holds audit, confirmations, the job queue, and import
batches. With an empty URL, audit and confirmations are in memory and lost on
restart, and the asynchronous import tools (`magento.import.start/status/cancel/
batches/batch/resume`) are not registered.

| Key | Type | Default | Validation / effect |
|---|---|---|---|
| `url` | string | — | `postgres://` or `postgresql://` URL. Enables durable state and the async import tools. Migrations run automatically at startup under an advisory lock. |
| `max_conns` | int32 | `10` | Must be positive when `url` is set. Connection pool size. |

## `datasources`

Each module has `enabled` and `instances`. An enabled module needs at least one
instance; every instance needs a non-empty `id` and `credential`. Instance ids
are globally unique across the three modules because audit rows, jobs, and
bindings reference them without a module qualifier.

```yaml
datasources:
  magento:
    enabled: true
    instances:
      - id: "store-us"
        endpoint: "https://shop.example.com"
        store_code: "default"
        credential: "${MAGENTO_US_CREDENTIAL}"
```

| Module | Instance keys |
|---|---|
| `magento` | `id`, `endpoint`, `store_code`, `credential`, `limits` |
| `cloudflare` | `id`, `account_id`, `zone_id`, `credential` |
| `ga4` | `id`, `property_id`, `credential` |

### Outbound limits per instance

`limits` bounds how hard the gateway may drive one upstream instance. It is per
instance because two stores have independent capacity, and it exists so a
runaway agent or a retry storm cannot saturate a store's PHP workers.

```yaml
datasources:
  magento:
    enabled: true
    instances:
      - id: "store-us"
        endpoint: "https://shop.example.com"
        store_code: "default"
        credential: "${MAGENTO_US_CREDENTIAL}"
        limits:
          requests_per_second: 50
          burst: 100
          max_concurrent: 8
```

| Key | Type | Default | Effect |
|---|---|---|---|
| `requests_per_second` | float | `50` | Sustained request rate for the instance. `0` disables the rate limit. |
| `burst` | int | `100` | Token-bucket capacity: how many requests may go back-to-back before the sustained rate applies. Requires `requests_per_second > 0`. |
| `max_concurrent` | int | `8` | In-flight request cap for the instance. `0` disables the cap. |

Omit the `limits` block and the module default above applies — an unconfigured
deployment is still protected. An explicit all-zero block means **unlimited**;
that is how a deployment opts out. A negative value, or `burst > 0` with
`requests_per_second: 0`, fails configuration validation at startup.

A request that cannot obtain budget before its tool deadline returns
`rate_limited` (retryable) instead of stalling, and the request never reaches
Magento. The wait is charged against the same deadline as the call, so a
tight tool timeout bounds how long a caller can queue.

Only the Magento client honours `limits` today; Cloudflare and GA4 accept the
block but ignore it.

`credential` accepts these forms (the loader does not inspect the value beyond
requiring it non-empty):

- Magento: `bearer:<integration-access-token>`, `oauth1:<key>:<secret>:<token>:<token-secret>`, or `admin:<username>:<password>` (mints and refreshes an admin token; a password may contain colons).
- Cloudflare: an API token.
- GA4: a service-account JSON document, or a static token for development.

Every data-source tool call names an `instance`; the resolver selects that
instance's credential. Unified tools name a `binding` instead. With exactly one configured instance the resolver also accepts an
empty value from direct Go callers, but the MCP schemas require the field, so
clients must always send it. An unknown id is `not_found`.

### Secrets are references by convention, not by enforcement

`${VAR}` is expanded before YAML parsing, so after expansion the loader sees
only a string; it cannot tell an environment reference from a literal. There is
no validation that rejects a literal secret. The shipped examples use
`${VAR}` references and the project treats a literal credential in a committed
file as a defect, but the guarantee is procedural. Keep credentials in the
environment or a secret manager.

## `bindings`

A binding names one Magento, one Cloudflare, and one GA4 instance for the
unified analytics tools. All three modules must be enabled, every referenced
instance must exist, and binding ids must be unique.

**Current limitation (ADR-0018).** The unified adapters bind a single
Cloudflare service and a single GA4 service, so a configuration with `bindings`
must have **exactly one Cloudflare instance and exactly one GA4 instance**.
Magento may have several stores. Any other shape is rejected at configuration
validation with an actionable message; it previously failed later during module
registration with a misleading "bindings require both ... to be enabled" error.
Multi-zone or multi-property bindings are not supported yet. Remove the
`bindings` block (and the unified tool disappears) until the adapters are
resolver-aware. ADR-0018 is updated (per-tool routing implemented; unified
bindings still single-instance), but its positive-consequence bullet still
overstates multi-instance bindings; this section is the as-built statement until
that bullet is corrected.

Working shape:

```yaml
datasources:
  magento:
    enabled: true
    instances:
      - id: "store-us"
        endpoint: "https://shop.example.com"
        store_code: "default"
        credential: "${MAGENTO_US_CREDENTIAL}"
      - id: "store-eu"
        endpoint: "https://eu.example.com"
        store_code: "default"
        credential: "${MAGENTO_EU_CREDENTIAL}"
  cloudflare:
    enabled: true
    instances:
      - id: "zone-us"
        account_id: "${CLOUDFLARE_ACCOUNT_ID}"
        zone_id: "${CLOUDFLARE_ZONE_ID}"
        credential: "${CLOUDFLARE_API_TOKEN}"
  ga4:
    enabled: true
    instances:
      - id: "giztop"
        property_id: "123456789"
        credential: "${GA4_SERVICE_ACCOUNT}"

bindings:
  - id: "store-us-analytics"
    magento: "store-us"
    cloudflare: "zone-us"
    ga4: "giztop"
```

## Environment overrides

The server reads these variables. They override the YAML value only when set;
an empty variable is ignored. There is no environment override for
`log.format`, `server.shutdown_grace`, `server.max_body_bytes`,
`database.max_conns`, or the `datasources`/`bindings` blocks.

| Variable | Overrides | Accepted values and default |
|---|---|---|
| `MAGE2MCP_LISTEN` | `server.listen` | Any `host:port`. Default: the YAML value (`127.0.0.1:8080` from `Default()`). |
| `MAGE2MCP_AUTH_ISSUER` | `auth.issuer` | Any string; must be https when auth is enabled. |
| `MAGE2MCP_AUTH_AUDIENCE` | `auth.audience` | Any non-empty string when auth is enabled. |
| `MAGE2MCP_AUTH_JWKS_URL` | `auth.jwks_url` | Any string; must be https when auth is enabled. |
| `MAGE2MCP_AUTH_ENABLED` | `auth.enabled` | Only the exact lower-case `true` or `1` enables; **any other non-empty value disables authentication**, including `TRUE` and `yes`. Default when unset: the YAML value (`true` in `Default()` and the production template, `false` in the development example). |
| `MAGE2MCP_LOG_LEVEL` | `log.level` | `debug`, `info`, `warn`, `error`; an invalid value fails validation. Default: `info`. |
| `MAGE2MCP_TOOLS_WRITES` | `tools.writes` | Only the exact lower-case `true` or `1` enables writes. Any other value (including `TRUE`) is ignored and cannot disable a YAML-enabled write profile. Default: `false`. |
| `MAGE2MCP_DATABASE_URL` | `database.url` | `postgres://` or `postgresql://` URL. Default: empty (in-memory state). |
| `MAGE2MCP_DEV_INSECURE` | server flag `-dev-insecure` | Read by `cmd/server`, not by the config loader. Only the value `true` (case-insensitive) sets the flag; anything else leaves it off. Default: unset/false. |

### `MAGE2MCP_AUTH_ENABLED` and `MAGE2MCP_DEV_INSECURE`

These two control the authentication boundary and are easy to misuse.

- `MAGE2MCP_AUTH_ENABLED=false` disables Cloudflare Access verification: every
  MCP request is anonymous. The server refuses this on a non-loopback listener
  unless the dev-insecure flag is also set. Use it only for a loopback
  development run. **Use the exact lower-case value:** the parser compares
  literally against `true`/`1`, so `TRUE`, `yes`, or a typo also disables
  authentication. Check the startup log line `auth_enabled` after changing it.
- `MAGE2MCP_DEV_INSECURE=true` (or `-dev-insecure`) removes that refusal. It
  lets an unauthenticated gateway bind a non-loopback address.
- **Never set both in production.** The combination `MAGE2MCP_AUTH_ENABLED=false`
  plus `MAGE2MCP_DEV_INSECURE=true` exposes every tool — including
  write/destructive tools when `tools.writes: true` — without authentication.
  The only supported use is a local container on a loopback-published port
  (`docker run -p 127.0.0.1:8080:8080 ...`), and the development Compose stack
  does exactly that.

### `.env` files

The server does **not** read a `.env` file: there is no dotenv loader and the
Compose files declare no `env_file`. [`.env.example`](../.env.example) is a
reference list of variable names, not something the process loads. Export the
variables in the shell, use your process manager, or rely on Compose's own
`.env` interpolation for the values the Compose file references.

## Validation summary

Startup fails when any of these is true:

- `server.listen` is empty, `shutdown_grace` is negative, or `max_body_bytes` is not positive.
- `tools.writes` is enabled and `max_body_bytes` is below 12 MiB (a maximum-size import would not fit the request body).
- Authentication is enabled and `issuer` is not https, `audience` is empty, or `jwks_url` is not https.
- `log.level` or `log.format` is outside its enum.
- `database.url` is set but is not a `postgres://`/`postgresql://` URL, or `max_conns` is not positive.
- An enabled module has no instances, an instance has no `id` or `credential`, an instance id repeats within a module, or an id is reused across modules.
- A binding id is empty or duplicated, a binding member is empty or references an unknown instance, a binding is declared while a module is disabled, or a binding is declared with more than one Cloudflare or GA4 instance.
- The compiled RBAC policy cannot bind a module with more than one instance; startup fails with the `rbac: ... has N instances ...` error above (currently regardless of `auth.enabled`).

The regression test `TestShippedConfigsValidate` loads every YAML file under
`configs/` with the environment each template documents and fails if a shipped
example stops validating. Add a new example to its manifest when you add a file.

## Related

- [configs/config.example.yaml](../configs/config.example.yaml) — runnable development example.
- [configs/config.production.template.yaml](../configs/config.production.template.yaml) — production template and required environment.
- [ADR-0018](adr/0018-multi-instance-routing.md) — per-instance routing and the current unified-binding limitation.
- [operations/deployment.md](operations/deployment.md) — containers, tunnel, health, capacity.
- [compatibility.md](compatibility.md) — supported component versions.
