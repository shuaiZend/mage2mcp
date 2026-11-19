# Deployment

mage2mcp runs as two processes — `mage2mcp-server` (MCP endpoint) and
`mage2mcp-worker` (background jobs) — with PostgreSQL for durable state and
Redis for the job queue. The admin CLI (`mage2mcp-admin`) is a human-only tool
and must never run inside an agent's execution path.

## Components

| Component | Required | Purpose |
|---|---|---|
| `mage2mcp-server` | yes | MCP endpoint, auth, RBAC, confirmation, tool execution |
| `mage2mcp-worker` | with async jobs | Imports, reports, log ingestion |
| `mage2mcp-admin` | for writes | Human approval surface for confirmations |
| PostgreSQL 16+ | recommended | Audit, confirmations, jobs, instances |
| Redis 7+ | with worker | Asynq queues, rate limits, cache |

Without PostgreSQL the server keeps audit and confirmations in memory: writes
stay disabled and audit is lost on restart. That mode is for local development
only.

## Docker

The image is a multi-stage build producing static server, worker, and admin
binaries on a distroless non-root base. It bakes
`configs/config.docker.yaml` at `/etc/mage2mcp/config.yaml`, so no file mount is
required, and declares a `HEALTHCHECK` that runs `mage2mcp-server -healthcheck`
(the image has no shell or curl).

```bash
# Local development stack (auth disabled, loopback port, dev passwords)
docker compose up --build
curl -s localhost:8080/healthz

# Production-shaped stack: every secret and Access value is required
export POSTGRES_PASSWORD=... REDIS_PASSWORD=...
export MAGE2MCP_AUTH_ISSUER=https://team.cloudflareaccess.com
export MAGE2MCP_AUTH_AUDIENCE=...
export MAGE2MCP_AUTH_JWKS_URL=https://team.cloudflareaccess.com/cdn-cgi/access/certs
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
```

Both stacks bind the server to `127.0.0.1:8080`, run it read-only with
`no-new-privileges`, and wire PostgreSQL and Redis with health checks. The
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
hostname is the `public_url`; the Access application audience is
`auth.audience`, and `auth.jwks_url` points at the team domain's certificates.

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
| `GET /readyz` | at least one tool registered |
| `GET /metrics` | Prometheus metrics |

`SIGTERM` drains in-flight requests for `server.shutdown_grace` (default 10s)
before closing the database pool.

## Capacity notes

- One process handles all tool calls; scale vertically first. Multi-replica
  deployments require sticky routing for stateful MCP sessions or stateless
  mode.
- The worker is stateless apart from Redis and PostgreSQL and can run as a
  separate replica.
