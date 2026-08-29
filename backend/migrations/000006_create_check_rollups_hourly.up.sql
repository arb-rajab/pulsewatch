-- 04-data-model.md: CHECK_ROLLUPS_HOURLY — hourly aggregates driving
-- rolling-window SLO math (FR-008/FR-010), retained far longer (400 days)
-- than raw data. A single hourly tier, deliberately not split into a
-- second coarser daily tier (see "Rollup and retention strategy").
CREATE TABLE check_rollups_hourly (
    target_id uuid NOT NULL REFERENCES targets (id),
    hour_bucket timestamptz NOT NULL,
    expected_checks integer NOT NULL,
    success_count integer NOT NULL,
    failure_count integer NOT NULL,
    unknown_count integer NOT NULL,
    p50_latency_ms integer,
    p95_latency_ms integer,
    PRIMARY KEY (target_id, hour_bucket)
);
