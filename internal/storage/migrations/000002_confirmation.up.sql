-- Two-step confirmation requests (ADR-0014). Subjects are stored hashed.

CREATE TABLE confirmation_tokens (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tool          text        NOT NULL,
    instance_id   text        NULL,
    scope         text        NOT NULL DEFAULT 'destructive',
    params_hash   bytea       NOT NULL,
    requester_hash bytea      NOT NULL,
    approver_hash bytea       NULL,
    summary       text        NOT NULL DEFAULT '',
    state         text        NOT NULL DEFAULT 'pending'
                              CHECK (state IN ('pending','approved','consumed','expired','revoked')),
    expires_at    timestamptz NOT NULL,
    approved_at   timestamptz NULL,
    consumed_at   timestamptz NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT confirmation_hash_len CHECK (
        octet_length(params_hash) = 32 AND octet_length(requester_hash) = 32
        AND (approver_hash IS NULL OR octet_length(approver_hash) = 32)),
    CONSTRAINT confirmation_ttl CHECK (expires_at > created_at
        AND expires_at <= created_at + interval '15 minutes'),
    CONSTRAINT confirmation_no_self_approval CHECK (
        approver_hash IS NULL OR approver_hash <> requester_hash),
    CONSTRAINT confirmation_state_times CHECK (
        (approver_hash IS NOT NULL) = (approved_at IS NOT NULL)
        AND (state = 'consumed') = (consumed_at IS NOT NULL)
        AND (state IN ('pending','expired','revoked') OR approver_hash IS NOT NULL)),
    CONSTRAINT confirmation_summary_len CHECK (length(summary) <= 2048)
);
CREATE INDEX confirmation_pending_idx ON confirmation_tokens (created_at DESC) WHERE state = 'pending';
CREATE INDEX confirmation_expiry_idx ON confirmation_tokens (expires_at);
