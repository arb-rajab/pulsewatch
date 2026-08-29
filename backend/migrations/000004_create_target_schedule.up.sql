-- 04-data-model.md: TARGET_SCHEDULE — hot, frequently-updated scheduling +
-- alerting-evaluation state, 1:1 with targets. Carries ADR-0001's leasing
-- columns (lease_owner, lease_expires_at) and ADR-0002's alert-suppression
-- state machine columns (streak, state) on the same row, per both ADRs'
-- explicit "same transaction, same row" design.
--
-- ON DELETE CASCADE here (and only here): target_schedule has no
-- independent existence apart from its target (PK IS the FK, a strict 1:1
-- "has one" relationship per the ERD) — this is referential-integrity
-- hygiene for a component row, not a data-retention policy choice.
CREATE TABLE target_schedule (
    target_id uuid PRIMARY KEY REFERENCES targets (id) ON DELETE CASCADE,
    next_due_at timestamptz NOT NULL,
    last_checked_at timestamptz,
    lease_owner text,
    lease_expires_at timestamptz,
    streak integer NOT NULL DEFAULT 0,
    state text NOT NULL DEFAULT 'healthy' CHECK (state IN ('healthy', 'suspect', 'alerting'))
);

-- Indexing strategy: "index on next_due_at (the due-set scan ADR-0001 and
-- ADR-0004 both depend on)."
CREATE INDEX target_schedule_next_due_at_idx ON target_schedule (next_due_at);
