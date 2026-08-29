-- 04-data-model.md: INCIDENTS — durable alerting-state history (FR-012);
-- also ADR-0002's exactly-once dispatch guard: the partial unique index
-- below is a hard schema constraint, not only the conditional-INSERT
-- application guard — a bug that bypasses the guard still cannot create a
-- second concurrently-open incident for one target.
CREATE TABLE incidents (
    id bigserial PRIMARY KEY,
    target_id uuid NOT NULL REFERENCES targets (id),
    opened_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz
);

-- "At most one open incident per target" (US-005, FR-016) — verbatim from
-- 04-data-model.md's Invariants section.
CREATE UNIQUE INDEX incidents_one_open_per_target ON incidents (target_id) WHERE closed_at IS NULL;

-- Supporting index for incident-history queries (US-009, FR-022).
CREATE INDEX incidents_target_id_opened_at_idx ON incidents (target_id, opened_at);
