# Deployment

mage2mcp runs as a server (`mage2mcp-server`, the MCP endpoint), a worker
(`mage2mcp-worker`, background jobs), and a human-only admin CLI
(`mage2mcp-admin`). PostgreSQL holds audit, confirmations, the job queue, import
batches, and the operation ledger (ADR-0019; no Redis). The admin CLI must never
run inside an agent's execution path.

## Components

| Component | Required | Purpose |
|---|---|---|
| `mage2mcp-server` | yes | MCP endpoint, auth, RBAC, confirmation, tool execution |
| `mage2mcp-worker` | with async imports or reconciliation | Imports, operation reconciliation, batch retention |
| `mage2mcp-admin` | for writes | Human approval surface for confirmations |
| PostgreSQL 16+ | required for confirmations and imports | Audit, confirmations, jobs, import batches, operation ledger |
| — | — | The job queue lives in PostgreSQL (ADR-0019); no Redis |

Without PostgreSQL the server keeps audit and confirmations in memory, the
async import tools are not registered, the five self-ledgered money tools fail
closed (no ledger), and writes cannot be approved (the admin CLI requires the
database). Audit is lost on restart. That mode is for local development only.

**Hard requirements for a write-enabled deployment:** `database.url` must be set,
and `server.max_body_bytes` must be at least 12 MiB (the default is 16 MiB). The
gateway fails configuration validation at startup if writes are enabled with a
smaller body cap, because a maximum import (8 MiB decoded, 11,184,812 bytes
base64) would otherwise be rejected at the transport.

## Docker

The image is a multi-stage build producing static server, worker, and admin
binaries on a distroless non-root base. It bakes
`configs/config.docker.yaml` at `/etc/mage2mcp/config.yaml` and declares a
`HEALTHCHECK` that runs `mage2mcp-server -healthcheck` (the image has no shell or
curl). The baked config enables no data source, so the container exposes only
`platform.info`; mount a real configuration over `/etc/mage2mcp/config.yaml` to
serve Magento/Cloudflare/GA4 tools.

```bash
# Local development stack (auth disabled, loopback port, dev passwords)
docker compose up --build
curl -s localhost:8080/healthz

# Production-shaped stack: every secret and Access value is required
export POSTGRES_PASSWORD=...
export MAGE2MCP_AUTH_ISSUER=https://team.cloudflareaccess.com
export MAGE2MCP_AUTH_AUDIENCE=...
export MAGE2MCP_AUTH_JWKS_URL=https://team.cloudflareaccess.com/cdn-cgi/access/certs
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
```

Both stacks bind the server to `127.0.0.1:8080`, run it read-only with
`no-new-privileges`, and wire PostgreSQL with a health check. The
development stack must never be exposed to a network: it disables authentication
and uses fixed placeholder passwords.

For a single container:

```bash
docker build -t mage2mcp .
docker run --rm -p 127.0.0.1:8080:8080 \
  -e MAGE2MCP_AUTH_ENABLED=false -e MAGE2MCP_DEV_INSECURE=true mage2mcp
```

## Cloudflare Tunnel

Production deployments publish the MCP endpoint through a Cloudflare Tunnel and
a Cloudflare Access application. Keep `auth.enabled: true`; the server refuses
to start with authentication disabled on a non-loopback listener. The tunnel
hostname is a deployment/DNS setting, not a gateway configuration key (there is
no `public_url`); the Access application audience is `auth.audience`, and
`auth.jwks_url` points at the team domain's certificates. The gateway does not
verify that traffic came from Cloudflare; configure a Tunnel or authenticated
origin pull.

## Configuration and secrets

- YAML holds structure; `${VAR}` references hold values. Unknown keys are
  rejected at startup.
- Secrets come from the environment or a secret manager, never from the image
  or the repository.
- `server.listen` defaults to loopback. In a container set it to `0.0.0.0:8080`
  and keep the host port bound to loopback.

## Health and shutdown

| Endpoint | Meaning |
|---|---|
| `GET /healthz` | process alive |
| `GET /readyz` | the process can serve MCP; returns the registry tool count, including `platform.info` (a read-only deployment reports `tools:1`) |
| `GET /metrics` | Prometheus metrics (same listener; not authenticated by the gateway) |

`SIGTERM` drains in-flight requests for `server.shutdown_grace` (default 10s)
before closing the database pool.

## Capacity notes

- One process handles all tool calls; scale vertically first. The SDK session
  is stateful, so multi-replica deployments require sticky routing. The gateway
  does not implement a rate limiter; limit concurrency at the agent or edge.
- The worker is stateless apart from PostgreSQL and can run as a
  separate replica.
