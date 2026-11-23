# Database design

**Status:** as-built for v0.11.10
(2026-11-20). This document describes the PostgreSQL schema that ships in
`internal/storage/migrations/000001..000006`. It is not a design proposal.

**Source of truth.** The six embedded SQL files are authoritative. If this
document and the SQL disagree, the SQL wins and this document is the defect. The
verification command in §2 checks the table and column inventory mechanically.

**Target:** PostgreSQL 16+ (the test environment is 16.15). The store uses
`pgx/v5`; there is no golang-migrate dependency and no Redis.

## 1. Migration mechanism

`internal/storage.Migrate` is the only migration runner. It is embedded in the
binary (`//go:embed migrations/*.sql`), idempotent, and safe to run on every
start:

1. Acquire one pooled connection and take `pg_advisory_lock(0x6d616765326d6370)`
   so the server and worker cannot race.
2. Create `schema_migrations (version text PRIMARY KEY, applied_at timestamptz
   NOT NULL DEFAULT now())` if it does not exist.
3. Read the embedded `.sql` files, sort by filename, and skip a version already
   present in `schema_migrations`.
4. Apply each new file and insert its filename into `schema_migrations` **in the
   same transaction**, then commit.
5. Release the advisory lock.

There are **no down migrations**, no `dirty` flag, and no per-migration checksum.
A failed file rolls back its transaction and stops startup. Forward fixes are new
migrations. The runner begins the transaction on the connection that holds the
advisory lock, so a single-connection pool cannot self-deadlock.

## 2. Verification

The table and column inventory in this document is checked against the SQL:

```bash
python3 - <<'PY'
import re, pathlib
sql = "\n".join(p.read_text() for p in sorted(pathlib.Path("internal/storage/migrations").glob("*.sql")))
tables = {}
for m in re.finditer(r"CREATE TABLE (?:IF NOT EXISTS )?(\w+)\s*\((.*?)\n\);", sql, re.S):
    name, body = m.group(1), m.group(2)
    cols = re.findall(
        r"^\s{4}([a-z_]+)\s+(?:text|uuid|bigint|integer|smallint|boolean|jsonb|timestamptz|bytea|text\[\])",
        body, re.M)
    tables[name] = cols
for m in re.finditer(r"ALTER TABLE (\w+) ADD COLUMN IF NOT EXISTS (\w+)", sql):
    tables.setdefault(m.group(1), []).append(m.group(2))
doc = pathlib.Path("docs/database-design.md").read_text()
missing = [f"{t}.{c}" for t, cols in tables.items() for c in cols if f"`{c}`" not in doc]
print(f"tables={len(tables)} columns={sum(len(c) for c in tables.values())} missing_in_doc={len(missing)}")
print("missing:", missing)
PY
```

The expected result is `missing_in_doc=0`. The same script can be inverted to
find a table named in this document that is not created by the migrations.

## 3. Tables

### 3.1 `datasource_instances` (000001)

One row per instance referenced by a job or an operation. The runtime
configuration is YAML; this table is a referential anchor, not the config store.

| Column | Type | Null | Default | Constraint |
|---|---|---|---|---|
| `id` | text | no | — | primary key; `^[a-z0-9][a-z0-9-]{0,63}$` |
| `module` | text | no | — | `magento`, `cloudflare`, `ga4` |
| `display_name` | text | no | `''` | |
| `endpoint` | text | yes | — | |
| `external_id` | text | no | `''` | |
| `config` | jsonb | no | `'{}'` | |
| `capabilities` | text[] | no | `'{}'` | |
| `write_profile` | boolean | no | `false` | |
| `status` | text | no | `'active'` | `active`, `disabled`, `degraded` |
| `created_at` | timestamptz | no | `now()` | |
| `updated_at` | timestamptz | no | `now()` | |

Runtime use: `INSERT ... ON CONFLICT (id) DO NOTHING` on job enqueue and ledger
begin, with `(id, module)`. The rest of the columns are not written by the code.

### 3.2 `roles` (000001)

| Column | Type | Null | Default | Constraint |
|---|---|---|---|---|
| `name` | text | no | — | primary key |
| `description` | text | no | `''` | |
| `rank` | smallint | no | `0` | |
| `created_at` | timestamptz | no | `now()` | |

Seeded with `viewer` (rank 10), `analyst` (20), `operator` (30), `admin` (40).
Runtime use: **none.** RBAC uses the compiled default policy in
`internal/security/rbac`, not this table.

### 3.3 `role_assignments` (000001)

| Column | Type | Null | Default | Constraint |
|---|---|---|---|---|
| `id` | uuid | no | `gen_random_uuid()` | primary key |
| `user_id` | text | no | — | |
| `role_name` | text | no | — | FK `roles(name)` `ON DELETE CASCADE` |
| `instance_id` | text | yes | — | FK `datasource_instances(id)` `ON DELETE CASCADE` |
| `created_at` | timestamptz | no | `now()` | |
| `revoked_at` | timestamptz | yes | — | |

Index: `role_assignments_uq (user_id, role_name, instance_id) NULLS NOT DISTINCT
WHERE revoked_at IS NULL`. Runtime use: **none.**

### 3.4 `tool_permissions` (000001)

| Column | Type | Null | Default | Constraint |
|---|---|---|---|---|
| `id` | bigint | no | identity | primary key |
| `role_name` | text | no | — | FK `roles(name)` `ON DELETE CASCADE` |
| `tool_pattern` | text | no | — | no `*` allowed |
| `scope_kind` | text | no | — | `tool`, `capability_prefix` |
| `instance_id` | text | yes | — | FK `datasource_instances(id)` `ON DELETE CASCADE` |
| `effect` | text | no | `'allow'` | `allow`, `deny` |
| `scope` | text | no | — | `read`, `write`, `destructive`, `admin` |
| `requires_confirmation` | boolean | no | `false` | |
| `updated_at` | timestamptz | no | `now()` | |

Constraint `tool_permissions_no_wildcard` rejects `*` in `tool_pattern`. Index:
`tool_permissions_uq (role_name, tool_pattern, instance_id) NULLS NOT DISTINCT`.
Runtime use: **none.**

### 3.5 `audit_events` (000001)

| Column | Type | Null | Default | Constraint |
|---|---|---|---|---|
| `seq` | bigint | no | identity | primary key |
| `event_id` | uuid | no | — | |
| `occurred_at` | timestamptz | no | `now()` | |
| `subject_hash` | bytea | yes | — | 32 bytes when present |
| `identity_class` | text | yes | — | `human`, `service`, `anonymous` |
| `role_name` | text | yes | — | |
| `instance_id` | text | yes | — | |
| `binding_id` | text | yes | — | |
| `tool` | text | no | — | |
| `scope` | text | yes | — | |
| `decision` | text | no | — | `allow`, `deny`, `confirmation_required`, `error` |
| `phase` | text | no | — | `intent`, `outcome` |
| `state` | text | no | `'pending'` | `pending`, `succeeded`, `failed`, `denied`, `outcome_unknown`, `degraded` |
| `params_hash` | bytea | yes | — | 32 bytes when present; **not written by the code** |
| `error_code` | text | yes | — | |
| `request_id` | text | yes | — | |
| `confirmation_id` | uuid | yes | — | |
| `intent_id` | uuid | yes | — | |

Indexes: `audit_events_subject_idx (subject_hash, occurred_at DESC)`,
`audit_events_tool_idx (tool, occurred_at DESC)`,
`audit_events_instance_idx (instance_id, occurred_at DESC)`.

Runtime use: `internal/storage.AuditRecorder` inserts one row per event, and
`RecordInTx` inserts inside the operation-gate transaction. There is no hash
chain, no retention job, and no partition; `params_hash` is unused (the canonical
hash lives in `confirmation_tokens` and `operations`).

### 3.6 `jobs` (000001, altered by 000003 and 000005)

| Column | Type | Null | Default | Constraint |
|---|---|---|---|---|
| `id` | uuid | no | `gen_random_uuid()` | primary key |
| `instance_id` | text | yes | — | FK `datasource_instances(id)` `ON DELETE RESTRICT` |
| `binding_id` | text | yes | — | |
| `module` | text | no | — | `magento`, `cloudflare`, `ga4`, `unified`, `platform` |
| `tool` | text | no | — | |
| `kind` | text | no | — | |
| `state` | text | no | `'queued'` | `queued`, `running`, `canceling`, `succeeded`, `failed`, `canceled` |
| `queue` | text | no | `'default'` | `critical`, `default`, `import`, `report`, `maintenance` |
| `priority` | smallint | no | `0` | |
| `payload` | jsonb | no | `'{}'` | |
| `idempotency_key` | text | yes | — | |
| `subject_hash` | bytea | no | — | |
| `attempt_count` | smallint | no | `0` | |
| `max_attempts` | smallint | no | `1` | |
| `last_error_code` | text | yes | — | |
| `result_ref` | text | yes | — | |
| `scheduled_at` | timestamptz | no | `now()` | |
| `started_at` | timestamptz | yes | — | |
| `finished_at` | timestamptz | yes | — | |
| `created_at` | timestamptz | no | `now()` | |
| `updated_at` | timestamptz | no | `now()` | |
| `lease_expires_at` | timestamptz | yes | — | added 000003 |
| `lease_token` | uuid | yes | — | added 000005 |
| `cancel_requested_at` | timestamptz | yes | — | added 000005 |

Constraints:

- `jobs_scope_chk`: `unified` needs `binding_id` and no `instance_id`;
  `platform` needs neither; `magento`/`cloudflare`/`ga4` need `instance_id` and
  no `binding_id`.
- `jobs_lease_token_chk` (000005): `(state IN ('running','canceling')) =
  (lease_token IS NOT NULL)`. A terminal or queued row cannot hold a token.

Indexes (final set):

- `jobs_idempotency_uq (instance_id, binding_id, subject_hash, tool,
  idempotency_key) NULLS NOT DISTINCT WHERE idempotency_key IS NOT NULL`
- `jobs_instance_created_idx (instance_id, created_at DESC)`
- `jobs_recovery_idx (queue, priority DESC, scheduled_at) WHERE state = 'queued'`
- `jobs_claim_idx (queue, priority DESC, scheduled_at) WHERE state = 'queued'`
  (000003; a duplicate of `jobs_recovery_idx`, kept because the migration is
  forward-only)
- `jobs_lease_recovery_idx (lease_expires_at, id) WHERE state IN
  ('running','canceling') AND lease_expires_at IS NOT NULL` (000005; replaced
  `jobs_lease_idx`)

Runtime use: `internal/storage.JobQueue` enqueues, claims with
`FOR UPDATE SKIP LOCKED`, extends the lease, completes/fails with the lease
token, and recovers expired leases. `lease_token` fences a worker whose lease
expired. `canceling` is written by `Cancel` for a running job and resolved by the
worker or lease recovery.

### 3.7 `idempotency_keys` (000001)

| Column | Type | Null | Default | Constraint |
|---|---|---|---|---|
| `instance_id` | text | no | — | primary key (with `idem_key`) |
| `idem_key` | text | no | — | primary key (with `instance_id`) |
| `subject_hash` | bytea | no | — | |
| `response_body` | jsonb | yes | — | |
| `created_at` | timestamptz | no | `now()` | |
| `expires_at` | timestamptz | no | — | |

Index: `idempotency_keys_expiry_idx (expires_at)`. Runtime use: **none.** The
operation ledger is the replay guard; this table is provisioned but unused
(ARCH-04).

### 3.8 `confirmation_tokens` (000002)

| Column | Type | Null | Default | Constraint |
|---|---|---|---|---|
| `id` | uuid | no | `gen_random_uuid()` | primary key |
| `tool` | text | no | — | |
| `instance_id` | text | yes | — | |
| `scope` | text | no | `'destructive'` | |
| `params_hash` | bytea | no | — | 32 bytes |
| `requester_hash` | bytea | no | — | 32 bytes |
| `approver_hash` | bytea | yes | — | 32 bytes when present |
| `summary` | text | no | `''` | ≤ 2048 bytes |
| `state` | text | no | `'pending'` | `pending`, `approved`, `consumed`, `expired`, `revoked` |
| `expires_at` | timestamptz | no | — | `> created_at`, `<= created_at + 15 min` |
| `approved_at` | timestamptz | yes | — | |
| `consumed_at` | timestamptz | yes | — | |
| `created_at` | timestamptz | no | `now()` | |

Constraints: `confirmation_hash_len` (hash lengths), `confirmation_ttl` (15 min
cap), `confirmation_no_self_approval` (`approver_hash <> requester_hash`),
`confirmation_state_times` (state and timestamps agree), `confirmation_summary_len`.
Indexes: `confirmation_pending_idx (created_at DESC) WHERE state = 'pending'`,
`confirmation_expiry_idx (expires_at)`.

Runtime use: `internal/storage.ConfirmStore` creates, approves, consumes, and
lists pending confirmations. The consume is a compare-and-set on `state`,
`requester_hash`, and `params_hash`; it is executed in the operation-gate
transaction for gated writes.

### 3.9 `import_batches` (000004)

| Column | Type | Null | Default | Constraint |
|---|---|---|---|---|
| `id` | uuid | no | `gen_random_uuid()` | primary key |
| `instance_id` | text | no | — | |
| `job_id` | uuid | yes | — | FK `jobs(id)` `ON DELETE SET NULL` |
| `format` | text | no | — | `csv`, `json` |
| `state` | text | no | `'queued'` | `queued`, `running`, `succeeded`, `partial`, `failed`, `cancelled` |
| `total` | integer | no | `0` | |
| `processed` | integer | no | `0` | |
| `created` | integer | no | `0` | |
| `updated` | integer | no | `0` | |
| `skipped` | integer | no | `0` | |
| `failed` | integer | no | `0` | |
| `error_code` | text | yes | — | |
| `payload` | bytea | yes | — | original payload for resume |
| `created_at` | timestamptz | no | `now()` | |
| `updated_at` | timestamptz | no | `now()` | |
| `finished_at` | timestamptz | yes | — | |

Indexes: `import_batches_job_idx (job_id)`,
`import_batches_instance_idx (instance_id, created_at DESC)`.

### 3.10 `import_batch_failures` (000004)

| Column | Type | Null | Default | Constraint |
|---|---|---|---|---|
| `batch_id` | uuid | no | — | FK `import_batches(id)` `ON DELETE CASCADE`; primary key (with `row_number`) |
| `row_number` | integer | no | — | primary key (with `batch_id`) |
| `sku` | text | no | — | |
| `action` | text | no | — | |
| `error` | text | no | — | |

Runtime use: the worker writes a batch and its failures after an import; the
resume tool reads every failure row (not the 100-row report cap) and re-queues
those rows. The worker prunes batches older than 30 days.

### 3.11 `operations` (000006)

| Column | Type | Null | Default | Constraint |
|---|---|---|---|---|
| `id` | uuid | no | `gen_random_uuid()` | primary key |
| `idempotency_key` | text | no | — | 1–256 chars; unique with `(instance_id, operation)` |
| `operation` | text | no | — | tool name |
| `instance_id` | text | no | — | FK `datasource_instances(id)` `ON DELETE RESTRICT` |
| `subject_hash` | bytea | no | — | 32 bytes |
| `entity_type` | text | no | — | `order`, `invoice`, `creditmemo`, `shipment`, `product`, `stock`, `category`, `customer` |
| `entity_id` | text | no | — | 1–256 chars |
| `amount_minor` | bigint | yes | — | integer minor units; paired with `currency` |
| `currency` | text | yes | — | `^[A-Z]{3}$` when present |
| `args_hash` | bytea | no | — | 32 bytes; same canonical hash as confirmation |
| `confirmation_id` | uuid | yes | — | the approval that authorised the mutation |
| `state` | text | no | `'pending'` | `pending`, `executing`, `succeeded`, `failed`, `unknown`, `reconciling` |
| `attempt` | integer | no | `0` | |
| `fence` | uuid | no | `gen_random_uuid()` | fencing token for the current attempt |
| `lease_expires_at` | timestamptz | yes | — | only `executing`/`reconciling` |
| `upstream_ref` | text | yes | — | Magento entity/increment id |
| `result` | jsonb | yes | — | replayable response on success |
| `error_code` | text | yes | — | |
| `reconcile_after` | timestamptz | yes | — | |
| `reconcile_count` | integer | no | `0` | |
| `created_at` | timestamptz | no | `now()` | |
| `updated_at` | timestamptz | no | `now()` | |
| `finished_at` | timestamptz | yes | — | |

Constraints:

- `operations_idem_uq UNIQUE (instance_id, operation, idempotency_key)` — the
  replay guard.
- `operations_money_chk`: `(amount_minor IS NULL) = (currency IS NULL)` and the
  currency is three uppercase letters.
- `operations_terminal_chk`: `(state IN ('succeeded','failed')) = (finished_at IS
  NOT NULL)`.
- `operations_hash_len`: `subject_hash` and `args_hash` are 32 bytes.
- `operations_lease_chk`: a lease is only on `executing`/`reconciling`.
- `operations_key_len` (1–256) and `operations_entity_len` (1–256).

Indexes: `operations_unknown_idx (reconcile_after, id) WHERE state = 'unknown'`,
`operations_executing_idx (lease_expires_at) WHERE state = 'executing'`,
`operations_entity_idx (instance_id, entity_type, entity_id, created_at DESC)`,
`operations_subject_idx (instance_id, subject_hash, created_at DESC)`,
`operations_state_idx (instance_id, state, created_at DESC)`.

State machine: `pending → executing → {succeeded, failed, unknown}`;
`unknown → reconciling → {succeeded, failed, unknown}`; `failed → pending` only
when a retry is proven safe. Every transition after `pending` is a conditional
update guarded by the current state and the fence.

### 3.12 `operation_transitions` (000006)

| Column | Type | Null | Default | Constraint |
|---|---|---|---|---|
| `seq` | bigint | no | identity | primary key |
| `operation_id` | uuid | no | — | FK `operations(id)` `ON DELETE RESTRICT` |
| `from_state` | text | no | — | `'none'` for the creating transition |
| `to_state` | text | no | — | |
| `fence` | uuid | no | — | the fence that caused the transition |
| `attempt` | integer | no | — | |
| `reason` | text | no | — | |
| `occurred_at` | timestamptz | no | `now()` | |

Index: `operation_transitions_op_idx (operation_id, seq)`. Append-only history;
the ledger's transitions are the audit trail a dispute is argued from.

### 3.13 `schema_migrations` (runner, not a migration)

| Column | Type | Null | Default | Constraint |
|---|---|---|---|---|
| `version` | text | no | — | primary key (the `.sql` filename) |
| `applied_at` | timestamptz | no | `now()` | |

Created by `Migrate`, not by a migration file. There is no `dirty` flag.

## 4. Invariants and state machines

| Invariant | Where enforced |
|---|---|
| Instance ids are globally unique and `module` is one of three values | `datasource_instances` primary key/CHECK; config validation |
| A job belongs to exactly one scope (instance, binding, or platform) | `jobs_scope_chk` |
| Only a running/canceling job holds a lease token | `jobs_lease_token_chk` |
| A confirmation cannot be self-approved and expires within 15 minutes | `confirmation_no_self_approval`, `confirmation_ttl` |
| A consumed confirmation must have an approver | `confirmation_state_times` |
| Terminal operations carry `finished_at`; non-terminal ones do not | `operations_terminal_chk` |
| Only a claimed operation holds a lease | `operations_lease_chk` |
| Money is integer minor units with a three-letter currency | `operations_money_chk` |
| An idempotency key is unique per `(instance, operation)` | `operations_idem_uq` |
| Every ledger transition is guarded by state and fence | `operation_ledger.go` conditional updates |

## 5. Runtime usage

| Table | Read/write at runtime |
|---|---|
| `datasource_instances` | upsert `(id, module)` on job/operation creation |
| `roles`, `role_assignments`, `tool_permissions` | **not read or written**; RBAC is compiled |
| `audit_events` | insert (standalone and in-transaction) |
| `jobs` | full queue lifecycle |
| `idempotency_keys` | **unused** |
| `confirmation_tokens` | create/approve/consume/list |
| `import_batches`, `import_batch_failures` | write after import, read for status/resume, prune after 30 days |
| `operations`, `operation_transitions` | ledger and reconciler |
| `schema_migrations` | migration runner |

## 6. Retention and operations

- **Import batches** are pruned by the worker after 30 days; failure rows cascade.
- **Audit events** have no retention job and no partition. A deployment that needs
  retention must add an operator procedure; a hash chain and signed checkpoints
  are **PLANNED**.
- **Jobs** are retained after completion; there is no retention job.
- **Confirmations** are retained after consumption/expiry; there is no pruning
  job.
- **Operation ledger** rows are retained; terminal rows are the record of what
  happened.
- **Backups:** a consistent `pg_dump` (or physical backup) covers audit,
  confirmations, jobs, import batches, and the ledger. Restore into a new
  database, run the server once so migrations are applied, then verify
  `schema_migrations`, `audit_events`, and `operations` counts. See
  [operations/backup-restore.md](operations/backup-restore.md).

## 7. Known gaps

| Gap | Consequence |
|---|---|
| `idempotency_keys` table is unused | Replay protection is the operation ledger, not this table. |
| `roles`/`tool_permissions` are not read | The database cannot override the compiled policy; changing a role requires a code change. |
| No audit hash chain/retention | Tamper evidence and retention are operator responsibilities (SEC-06). |
| No partitions | A large audit table is a single table; vacuum/index maintenance is the operator's job. |
| REL-16-R | The five self-ledgered money tools write their outcome audit row after the tool commits; a crash can leave the operation row correct and the audit row missing. |

## 8. Verification result

The §2 script reports the table and column inventory against the SQL. At the
time of writing: `tables=12 columns=143 missing_in_doc=0`. Any mismatch is a defect in this document, not in the
migrations.
