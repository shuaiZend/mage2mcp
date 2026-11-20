# Upgrade

## Versioning

Pre-1.0 releases follow Semantic Versioning with an explicit caveat: breaking
changes are allowed in minor releases when an ADR and a changelog entry record
them. From 1.0, tool names, schemas, and configuration keys are stable.

## Procedure

1. Read `CHANGELOG.md` for the target version and note breaking changes,
   configuration additions, and migration notes.
2. Back up PostgreSQL (see [backup and restore](backup-restore.md)).
3. Pull the new image or build the new binaries.
4. Stop the worker, then the server (graceful shutdown drains in-flight calls).
5. Start the new server once: the six embedded migrations run automatically
   under an advisory lock and are recorded in `schema_migrations` (there are no
   down migrations; rollback is a database restore).
6. Start the worker.
7. Verify `/healthz`, `/readyz`, and one read-only tool call.
8. Check the changelog's verification section and the release notes for
   post-upgrade checks.

## Rollback

- Migrations are forward-only. A rollback requires restoring the pre-upgrade
  database backup; the application never downgrades a schema in place.
- Keep the previous image tag available until the new version has served
  traffic for a full business cycle.
- Confirmations created by the new version are invalid after a rollback; they
  must be re-requested.

## Configuration changes

Unknown configuration keys are rejected, so an old config file fails loudly
rather than silently ignoring a new control. Add new keys from
`configs/config.production.template.yaml` before upgrading. `server.public_url`
was removed; delete it if present. Secrets are never part of the config file;
rotate them through the secret manager.

Two requirements are enforced at startup:

- `database.url` is required for confirmations and async imports; without it the
  import tools are not registered and the money tools fail closed.
- A write-enabled deployment needs `server.max_body_bytes` ≥ 12 MiB (default
  16 MiB); a smaller cap fails validation.

## Compatibility

See [compatibility.md](../compatibility.md) for the supported component matrix,
interface stability promises, and version support. In short: Go 1.26/1.27,
PostgreSQL 16+, Magento 2.4.x, the current Cloudflare v4/GraphQL APIs, the GA4
Data API v1beta, MCP `2025-11-25`/`2026-07-28`, and no Redis.
