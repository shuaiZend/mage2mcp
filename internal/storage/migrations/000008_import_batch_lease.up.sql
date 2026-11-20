-- REL-12: an import batch opened with state='running' and no lease stays
-- 'running' forever when the worker dies, because FinishImportBatch is the only
-- exit and a crashed process never calls it. magento.import.batches then reports
-- a live import that does not exist, and nothing can tell the two apart.
--
-- The batch now carries the same lease idea as jobs: the worker that opened it
-- owns it until lease_expires_at, and a reaper marks an expired batch failed once
-- no live job still claims it.

ALTER TABLE import_batches
    ADD COLUMN IF NOT EXISTS lease_expires_at timestamptz NULL,
    ADD COLUMN IF NOT EXISTS attempt integer NOT NULL DEFAULT 0;

-- The reaper scans expired running batches; a partial index keeps that cheap and
-- covers only the rows it can act on.
CREATE INDEX IF NOT EXISTS import_batches_lease_idx
    ON import_batches (lease_expires_at)
    WHERE state = 'running';
