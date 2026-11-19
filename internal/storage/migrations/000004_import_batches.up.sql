-- Import batch history for asynchronous Magento imports (ADR-0021).
-- Failures are stored per row so resume can re-run them without the 100-row
-- report cap; the original payload is kept for the resume job.

CREATE TABLE IF NOT EXISTS import_batches (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id  text        NOT NULL,
    job_id       uuid        NULL REFERENCES jobs(id) ON DELETE SET NULL,
    format       text        NOT NULL CHECK (format IN ('csv', 'json')),
    state        text        NOT NULL DEFAULT 'queued'
                             CHECK (state IN ('queued','running','succeeded','partial','failed','cancelled')),
    total        integer     NOT NULL DEFAULT 0,
    processed    integer     NOT NULL DEFAULT 0,
    created      integer     NOT NULL DEFAULT 0,
    updated      integer     NOT NULL DEFAULT 0,
    skipped      integer     NOT NULL DEFAULT 0,
    failed       integer     NOT NULL DEFAULT 0,
    error_code   text        NULL,
    payload      bytea       NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz NULL
);

CREATE INDEX IF NOT EXISTS import_batches_job_idx ON import_batches (job_id);
CREATE INDEX IF NOT EXISTS import_batches_instance_idx ON import_batches (instance_id, created_at DESC);

CREATE TABLE IF NOT EXISTS import_batch_failures (
    batch_id   uuid    NOT NULL REFERENCES import_batches(id) ON DELETE CASCADE,
    row_number integer NOT NULL,
    sku        text    NOT NULL,
    action     text    NOT NULL,
    error      text    NOT NULL,
    PRIMARY KEY (batch_id, row_number)
);
