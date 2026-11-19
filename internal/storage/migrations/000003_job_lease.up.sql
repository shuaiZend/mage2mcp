-- Lease-based claiming for the PostgreSQL job queue (ADR-0019).
-- A worker renews the lease while it runs a task; an expired lease returns the
-- task to the queue so a crashed worker does not strand it.

ALTER TABLE jobs ADD COLUMN IF NOT EXISTS lease_expires_at timestamptz NULL;

CREATE INDEX IF NOT EXISTS jobs_lease_idx
    ON jobs (lease_expires_at)
    WHERE state = 'running' AND lease_expires_at IS NOT NULL;

-- Claim index: pick the highest-priority runnable task per queue.
CREATE INDEX IF NOT EXISTS jobs_claim_idx
    ON jobs (queue, priority DESC, scheduled_at)
    WHERE state = 'queued';
