-- Manual rollback for 000009_audit_approval_event.
--
-- This file lives in a subdirectory on purpose: the migrator embeds and applies
-- only migrations/*.sql, so a *.down.sql beside the up file would be treated as
-- a forward migration (and, sorted first, would run before the up file). Run
-- this script by hand when rolling the release back.
--
-- Rolling back removes the three values the migration added, so any approval row
-- written since then must go first. They are audit records: export them before
-- running the DELETE if the trail has to survive the rollback.

DELETE FROM audit_events WHERE decision = 'approve' OR phase = 'approval' OR state = 'approved';

ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_decision_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_decision_check
    CHECK (decision IN ('allow', 'deny', 'confirmation_required', 'error', 'pending'));

ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_phase_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_phase_check
    CHECK (phase IN ('intent', 'outcome'));

ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_state_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_state_check
    CHECK (state IN ('pending', 'succeeded', 'failed', 'denied', 'outcome_unknown', 'degraded'));
