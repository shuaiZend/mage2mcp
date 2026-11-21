-- An approval is a first-class audit event (SEM-03 / SEC-02).
--
-- `mage2mcp-admin approve` records decision='approve', phase='approval',
-- state='approved'. The constraints from 000001 and 000007 rejected every such
-- row, so an approval had no audit trail at all: the confirmation flipped to
-- 'approved' and the CLI then failed on the insert.
--
-- The audit table is append-only. Rewriting the approval as decision='allow'
-- would erase the difference between "a call was authorized" and "a human
-- approved a pending operation", so the values are added instead.
--
-- Additive: every existing row keeps a value that is still in the set.

ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_decision_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_decision_check
    CHECK (decision IN ('allow', 'deny', 'confirmation_required', 'error', 'pending', 'approve'));

ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_phase_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_phase_check
    CHECK (phase IN ('intent', 'outcome', 'approval'));

ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_state_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_state_check
    CHECK (state IN ('pending', 'succeeded', 'failed', 'denied', 'outcome_unknown', 'degraded', 'approved'));
