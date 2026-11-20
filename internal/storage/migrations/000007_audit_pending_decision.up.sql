-- An audit intent row records that a call was attempted, not that it was
-- allowed: the authorization decision has not been made when the intent is
-- written. Add 'pending' so a phase-blind query for allowed calls cannot return
-- a call that was subsequently denied (SEC-R02).
--
-- Additive: every existing row keeps a value that is still in the set.

ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_decision_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_decision_check
    CHECK (decision IN ('allow', 'deny', 'confirmation_required', 'error', 'pending'));
