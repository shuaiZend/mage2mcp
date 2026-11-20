# Backup and restore

## What to back up

| Data | Store | Frequency | Notes |
|---|---|---|---|
| Audit events | PostgreSQL | continuous / daily | evidence; retention policy applies |
| Confirmations | PostgreSQL | daily | short-lived; losing them only cancels pending approvals |
| Jobs and the operation ledger | PostgreSQL | daily | losing the ledger may re-run a non-idempotent mutation |
| Instance rows and role tables | PostgreSQL | daily | instance rows are a projection of YAML; the role tables are not read at runtime |
| Configuration and secrets | operator repository / secret manager | on change | never in the database backup |

Raw Cloudflare request logs are not implemented; there is no R2/ClickHouse
component to back up. PostgreSQL never stores raw logs.

## PostgreSQL backup

```bash
pg_dump --format=custom --no-owner --no-acl \
  "$MAGE2MCP_DATABASE_URL" > mage2mcp-$(date +%F).dump
```

Store backups encrypted and off-host. Test a restore monthly:

```bash
createdb mage2mcp_restore
pg_restore --no-owner --no-acl -d mage2mcp_restore mage2mcp-2026-11-19.dump
MAGE2MCP_DATABASE_URL=postgres://.../mage2mcp_restore \
  ./bin/mage2mcp-server -config config.yaml   # runs migrations idempotently
```

## Restore checklist

1. Stop the server and worker so no writes race the restore.
2. Restore into a new database, not over the live one.
3. Run the server once against the restored database: migrations are
   idempotent and recorded in `schema_migrations`.
4. Verify audit continuity: `SELECT count(*), min(occurred_at), max(occurred_at) FROM audit_events;`
5. Start the server once so it applies any missing migrations, then start the
   worker, and check `/readyz`.
6. Re-request any confirmation that was pending before the restore; do not
   resurrect consumed or expired ones.

## Retention

Import batch history is pruned by the worker after 30 days (failure rows
cascade). Audit events, jobs, confirmations, and the operation ledger have **no
retention job** and **no partitions**; the table is a single table. If you need
retention or tamper evidence, export to an immutable sink and add an operator
procedure. A hash chain and retention partitions are PLANNED.
