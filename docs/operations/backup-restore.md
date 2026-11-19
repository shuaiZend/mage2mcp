# Backup and restore

## What to back up

| Data | Store | Frequency | Notes |
|---|---|---|---|
| Audit events | PostgreSQL | continuous / daily | evidence; retention policy applies |
| Confirmations | PostgreSQL | daily | short-lived; losing them only cancels pending approvals |
| Jobs and idempotency keys | PostgreSQL | daily | losing them may re-run non-idempotent work |
| Instances and permissions | PostgreSQL | daily | projection of YAML; YAML is the source of truth |
| Configuration and secrets | operator repository / secret manager | on change | never in the database backup |

Raw Cloudflare request logs (when Logpush is enabled) live in R2/ClickHouse and
follow that system's own backup policy; they are never in PostgreSQL.

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
5. Start the worker, then the server, and check `/readyz`.
6. Re-approve any confirmation that was pending before the restore; do not
   resurrect consumed ones.

## Retention

Audit retention defaults to 365 days. Deleting old audit partitions is an
operator action performed after exporting to the external sink and recording the
archive digest; see the audit design in the architecture document.
