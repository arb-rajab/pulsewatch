-- 04-data-model.md: CHECK_RESULTS — raw per-check outcomes (FR-007), the
-- input to rollups. Declarative range-partitioned by day (checked_at),
-- per the "Rollup and retention strategy" section: retention drops whole
-- partitions instead of row-by-row DELETE.
--
-- UNIQUE (target_id, checked_at): the additive constraint flagged by
-- Session 3's API-contracts session (05-api-contracts.md /
-- 12-session-handoff.md) — 04-data-model.md previously specified this pair
-- only as a plain index. It backs the OTLP duplicate-result idempotency
-- guarantee in docs/architecture/openapi.yaml's /v1/logs endpoint: a
-- resubmission carrying the identical (target_id, checked_at) pair must be
-- a no-op that never re-enters ADR-0002's transition function.
--
-- A partitioned table's PRIMARY KEY / UNIQUE constraints must include the
-- partition key (checked_at) — a Postgres mechanic, not a data-model
-- change. `id` stays a single globally-increasing bigserial sequence, so
-- (id, checked_at) remains effectively globally unique in practice even
-- though Postgres only enforces it per-partition.
CREATE TABLE check_results (
    id bigserial NOT NULL,
    target_id uuid NOT NULL REFERENCES targets (id),
    checked_at timestamptz NOT NULL,
    success boolean NOT NULL,
    latency_ms integer NOT NULL,
    status_code integer,
    failure_reason text CHECK (failure_reason IN ('timeout', 'refused', 'status_mismatch', 'body_mismatch')),
    body_match_fragment text,
    body_match_fragment_truncated boolean NOT NULL DEFAULT false,
    PRIMARY KEY (id, checked_at),
    UNIQUE (target_id, checked_at)
) PARTITION BY RANGE (checked_at);

-- Catch-all partition so inserts never fail for a date with no dedicated
-- day partition yet — the Session 5+ rollup/retention background job
-- (04-data-model.md) is what creates future day partitions in steady
-- state; this bootstrap only has to make a fresh database usable today.
CREATE TABLE check_results_default PARTITION OF check_results DEFAULT;

-- Bootstrap a rolling window of concrete day partitions (yesterday through
-- 6 days out) so routine inserts land in a real dated partition rather
-- than the default one from day one of a fresh environment.
DO $$
DECLARE
    day date;
BEGIN
    FOR day IN
        SELECT generate_series(current_date - INTERVAL '1 day', current_date + INTERVAL '6 days', INTERVAL '1 day')::date
    LOOP
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS check_results_%s PARTITION OF check_results FOR VALUES FROM (%L) TO (%L)',
            to_char(day, 'YYYY_MM_DD'),
            day,
            day + INTERVAL '1 day'
        );
    END LOOP;
END $$;
