-- Initial schema for the gateway state store.
-- Raw Cloudflare request logs never live here; they belong in R2/ClickHouse.

CREATE TABLE datasource_instances (
    id            text        PRIMARY KEY,
    module        text        NOT NULL CHECK (module IN ('magento','cloudflare','ga4')),
    display_name  text        NOT NULL DEFAULT '',
    endpoint      text        NULL,
    external_id   text        NOT NULL DEFAULT '',
    config        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    capabilities  text[]      NOT NULL DEFAULT '{}',
    write_profile boolean     NOT NULL DEFAULT false,
    status        text        NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled','degraded')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT datasource_instances_id_fmt CHECK (id ~ '^[a-z0-9][a-z0-9-]{0,63}$')
);

CREATE TABLE roles (
    name        text        PRIMARY KEY,
    description text        NOT NULL DEFAULT '',
    rank        smallint    NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now()
);

INSERT INTO roles (name, description, rank) VALUES
    ('viewer',   'Read-only access', 10),
    ('analyst',  'Read-only analytics access', 20),
    ('operator', 'Read and write access', 30),
    ('admin',    'Full access including destructive operations', 40);

CREATE TABLE role_assignments (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     text        NOT NULL,
    role_name   text        NOT NULL REFERENCES roles(name) ON DELETE CASCADE,
    instance_id text        NULL REFERENCES datasource_instances(id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    revoked_at  timestamptz NULL
);
CREATE UNIQUE INDEX role_assignments_uq
    ON role_assignments (user_id, role_name, instance_id) NULLS NOT DISTINCT
    WHERE revoked_at IS NULL;

CREATE TABLE tool_permissions (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    role_name   text        NOT NULL REFERENCES roles(name) ON DELETE CASCADE,
    tool_pattern text       NOT NULL,
    scope_kind  text        NOT NULL CHECK (scope_kind IN ('tool','capability_prefix')),
    instance_id text        NULL REFERENCES datasource_instances(id) ON DELETE CASCADE,
    effect      text        NOT NULL DEFAULT 'allow' CHECK (effect IN ('allow','deny')),
    scope       text        NOT NULL CHECK (scope IN ('read','write','destructive','admin')),
    requires_confirmation boolean NOT NULL DEFAULT false,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tool_permissions_no_wildcard CHECK (position('*' in tool_pattern) = 0)
);
CREATE UNIQUE INDEX tool_permissions_uq
    ON tool_permissions (role_name, tool_pattern, instance_id) NULLS NOT DISTINCT;

CREATE TABLE audit_events (
    seq            bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id       uuid        NOT NULL,
    occurred_at    timestamptz NOT NULL DEFAULT now(),
    subject_hash   bytea       NULL,
    identity_class text        NULL CHECK (identity_class IN ('human','service','anonymous')),
    role_name      text        NULL,
    instance_id    text        NULL,
    binding_id     text        NULL,
    tool           text        NOT NULL,
    scope          text        NULL,
    decision       text        NOT NULL CHECK (decision IN ('allow','deny','confirmation_required','error')),
    phase          text        NOT NULL CHECK (phase IN ('intent','outcome')),
    state          text        NOT NULL DEFAULT 'pending'
                               CHECK (state IN ('pending','succeeded','failed','denied','outcome_unknown','degraded')),
    params_hash    bytea       NULL,
    error_code     text        NULL,
    request_id     text        NULL,
    confirmation_id uuid       NULL,
    intent_id      uuid       NULL,
    CONSTRAINT audit_events_hash_len CHECK (
        (subject_hash IS NULL OR octet_length(subject_hash) = 32)
        AND (params_hash IS NULL OR octet_length(params_hash) = 32))
);
CREATE INDEX audit_events_subject_idx  ON audit_events (subject_hash, occurred_at DESC);
CREATE INDEX audit_events_tool_idx     ON audit_events (tool, occurred_at DESC);
CREATE INDEX audit_events_instance_idx ON audit_events (instance_id, occurred_at DESC);

CREATE TABLE jobs (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id    text        NULL REFERENCES datasource_instances(id) ON DELETE RESTRICT,
    binding_id     text        NULL,
    module         text        NOT NULL CHECK (module IN ('magento','cloudflare','ga4','unified','platform')),
    tool           text        NOT NULL,
    kind           text        NOT NULL,
    state          text        NOT NULL DEFAULT 'queued'
                               CHECK (state IN ('queued','running','canceling','succeeded','failed','canceled')),
    queue          text        NOT NULL DEFAULT 'default'
                               CHECK (queue IN ('critical','default','import','report','maintenance')),
    priority       smallint    NOT NULL DEFAULT 0,
    payload        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    idempotency_key text       NULL,
    subject_hash   bytea       NOT NULL,
    attempt_count  smallint    NOT NULL DEFAULT 0,
    max_attempts   smallint    NOT NULL DEFAULT 1,
    last_error_code text       NULL,
    result_ref     text        NULL,
    scheduled_at   timestamptz NOT NULL DEFAULT now(),
    started_at     timestamptz NULL,
    finished_at    timestamptz NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT jobs_scope_chk CHECK (
        (module = 'unified' AND binding_id IS NOT NULL AND instance_id IS NULL)
     OR (module = 'platform' AND instance_id IS NULL AND binding_id IS NULL)
     OR (module IN ('magento','cloudflare','ga4') AND instance_id IS NOT NULL AND binding_id IS NULL))
);
CREATE UNIQUE INDEX jobs_idempotency_uq
    ON jobs (instance_id, binding_id, subject_hash, tool, idempotency_key)
    NULLS NOT DISTINCT WHERE idempotency_key IS NOT NULL;
CREATE INDEX jobs_recovery_idx ON jobs (queue, priority DESC, scheduled_at) WHERE state = 'queued';
CREATE INDEX jobs_instance_created_idx ON jobs (instance_id, created_at DESC);

CREATE TABLE idempotency_keys (
    instance_id   text        NOT NULL,
    idem_key      text        NOT NULL,
    subject_hash  bytea       NOT NULL,
    response_body jsonb       NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    PRIMARY KEY (instance_id, idem_key)
);
CREATE INDEX idempotency_keys_expiry_idx ON idempotency_keys (expires_at);
