-- Fencing tokens and two-phase cancellation for the PostgreSQL job queue.
--
-- REL-01: every claim now carries a lease_token. Complete/Fail/Extend require it,
-- so a worker whose lease expired (and whose job was reclaimed) can no longer
-- overwrite the row another worker owns.
-- REL-19: 'canceling' is now a real state, written by Cancel for a running job and
-- resolved by the worker (or by lease recovery) into 'canceled'.

ALTER TABLE jobs ADD COLUMN IF NOT EXISTS lease_token uuid NULL;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS cancel_requested_at timestamptz NULL;

-- A running row that predates this migration has no token. Give it one nobody
-- holds so any still-running worker from the old binary is fenced out.
UPDATE jobs SET lease_token = gen_random_uuid()
 WHERE state IN ('running', 'canceling') AND lease_token IS NULL;

-- Terminal and queued rows must never carry a token: the token identifies the one
-- worker allowed to write an outcome.
UPDATE jobs SET lease_token = NULL
 WHERE state NOT IN ('running', 'canceling') AND lease_token IS NOT NULL;

ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_lease_token_chk;
ALTER TABLE jobs ADD CONSTRAINT jobs_lease_token_chk CHECK (
    (state IN ('running', 'canceling')) = (lease_token IS NOT NULL)
);

-- cancel_requested_at is an audit trail, not a state invariant: a cancel that
-- arrives after the handler already succeeded leaves the timestamp on a
-- 'succeeded' row, which is the truth of what happened.

-- Lease recovery scans this index. It is ordered by (lease_expires_at, id) so the
-- bounded recovery statement is deterministic, and it covers 'canceling' too so an
-- abandoned cancellation is resolved instead of stranded.
DROP INDEX IF EXISTS jobs_lease_idx;
CREATE INDEX IF NOT EXISTS jobs_lease_recovery_idx
    ON jobs (lease_expires_at, id)
    WHERE state IN ('running', 'canceling') AND lease_expires_at IS NOT NULL;