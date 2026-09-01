// Package rollup implements FR-008's hourly rollup job: the pre-aggregation
// step 04-data-model.md already designed (the "Rollup and retention
// strategy" section) and no prior session had written to. It reads
// check_results (raw per-check outcomes) and writes check_rollups_hourly
// (one row per (target_id, hour_bucket)) — the only table
// GET /targets/{id}/slo (05-api-contracts.md) is allowed to read, per its
// own "computed exclusively from check_rollups_hourly, never a raw-row
// scan" contract.
package rollup

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config holds the job's runtime tuning knobs, matching this repo's existing
// scheduler.Config convention (reasoned defaults, not hardcoded constants).
type Config struct {
	// TickInterval is how often the job recomputes rollups. NFR-005 requires
	// "a fixed hourly cadence" — the default below matches that exactly.
	TickInterval time.Duration
	// OpTimeout bounds each tick's single SQL statement. NFR-005 requires
	// each run to complete in <=5 minutes at this project's measured scale
	// (~10 targets, 30-60s interval, <=30k raw rows/day); the default is
	// that same bound, so a run that's genuinely stuck is canceled rather
	// than left to hang past the NFR it's supposed to satisfy.
	OpTimeout time.Duration
}

// DefaultConfig returns NFR-005's stated cadence/bound.
func DefaultConfig() Config {
	return Config{
		TickInterval: 1 * time.Hour,
		OpTimeout:    5 * time.Minute,
	}
}

// upsertRollupsSQL is the entire computation: one statement, atomic (commits
// or rolls back as a whole — a canceled/errored run leaves no partial rollup
// row behind), covering every target's every fully-completed hour since that
// target's own created_at, not just the most recent one.
//
// Design decision (see docs/project-memory/04-data-model.md's Session 12
// addendum for the full write-up): this recomputes and overwrites
// (ON CONFLICT ... DO UPDATE) every historical bucket on every tick, rather
// than a bounded lookback plus a separate one-time backfill pass. That is
// deliberately simple, not an oversight — 04-data-model.md's own reasoning
// for a single hourly tier already establishes this project's real rollup
// volume as "trivially small" for Postgres (<=9,600 rows/target over the
// full 400-day retention ceiling), and recomputing the whole history is what
// makes the job self-healing for ANY late-arriving check_results row
// (agent-reported OTLP results in particular can arrive after their own
// hour has already rolled up) with no extra "which buckets are dirty"
// bookkeeping. Revisit with a bounded-lookback design, against real measured
// tick duration, if that ever stops being true — the identical
// revisit-on-evidence discipline 04-data-model.md already applies to its own
// no-second-tier decision.
//
// buckets: every (target, hour_bucket) pair for a fully-completed hour
// (hour_bucket + 1h <= the current top-of-hour) from that target's own
// created_at forward — a target created mid-hour contributes no bucket for
// the hour it didn't fully exist in, matching expected_checks' own
// max(bucket_start, created_at) floor.
//
// expected: (min(bucket_end, now()) - max(bucket_start, created_at)) /
// interval_seconds, floored at 0 — 04-data-model.md's formula verbatim.
//
// agg: real counts/percentiles from check_results, grouped by the same
// per-target per-hour bucket.
//
// unknown_count = max(0, expected_checks - success_count - failure_count) —
// checks that should have happened (per the target's configured interval)
// but produced no check_results row at all (FR-011/FR-019's carve-out,
// concretized at the schema level).
const upsertRollupsSQL = `
WITH cutoff AS (
    SELECT date_trunc('hour', now()) AS current_hour
),
buckets AS (
    SELECT t.id AS target_id, t.interval_seconds, t.created_at, b.hour_bucket
    FROM targets t
    CROSS JOIN cutoff c
    CROSS JOIN LATERAL generate_series(
        date_trunc('hour', t.created_at),
        c.current_hour - interval '1 hour',
        interval '1 hour'
    ) AS b(hour_bucket)
    WHERE t.deleted_at IS NULL
),
agg AS (
    SELECT
        target_id,
        date_trunc('hour', checked_at) AS hour_bucket,
        count(*) FILTER (WHERE success)     AS success_count,
        count(*) FILTER (WHERE NOT success) AS failure_count,
        percentile_cont(0.5)  WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE success) AS p50_latency_ms,
        percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE success) AS p95_latency_ms
    FROM check_results
    GROUP BY target_id, date_trunc('hour', checked_at)
)
INSERT INTO check_rollups_hourly
    (target_id, hour_bucket, expected_checks, success_count, failure_count, unknown_count, p50_latency_ms, p95_latency_ms)
SELECT
    b.target_id,
    b.hour_bucket,
    expected.expected_checks,
    COALESCE(a.success_count, 0),
    COALESCE(a.failure_count, 0),
    GREATEST(0, expected.expected_checks - COALESCE(a.success_count, 0) - COALESCE(a.failure_count, 0)),
    round(a.p50_latency_ms)::int,
    round(a.p95_latency_ms)::int
FROM buckets b
CROSS JOIN LATERAL (
    SELECT GREATEST(0, FLOOR(
        EXTRACT(EPOCH FROM (
            LEAST(b.hour_bucket + interval '1 hour', now()) - GREATEST(b.hour_bucket, b.created_at)
        )) / NULLIF(b.interval_seconds, 0)
    ))::int AS expected_checks
) expected
LEFT JOIN agg a ON a.target_id = b.target_id AND a.hour_bucket = b.hour_bucket
ON CONFLICT (target_id, hour_bucket) DO UPDATE SET
    expected_checks = EXCLUDED.expected_checks,
    success_count   = EXCLUDED.success_count,
    failure_count   = EXCLUDED.failure_count,
    unknown_count   = EXCLUDED.unknown_count,
    p50_latency_ms  = EXCLUDED.p50_latency_ms,
    p95_latency_ms  = EXCLUDED.p95_latency_ms`

// RunOnce executes exactly one rollup pass and reports how many
// (target, hour_bucket) rows it wrote or refreshed. Exported separately from
// Run so a caller (a test, or a future one-off "run it now" admin command)
// can trigger a single real pass without standing up the ticker loop.
func RunOnce(ctx context.Context, pool *pgxpool.Pool, timeout time.Duration) (int64, error) {
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tag, err := pool.Exec(opCtx, upsertRollupsSQL)
	if err != nil {
		return 0, fmt.Errorf("upsert check_rollups_hourly: %w", err)
	}
	return tag.RowsAffected(), nil
}

// cadenceGapExceeded reports whether gap — the real time elapsed since the
// previous tick actually ran — violates NFR-005's "fixed hourly cadence" by
// more than a 10% tolerance (scheduling jitter under load, not a real gap).
// Exported as its own function so the threshold logic is unit-testable
// without waiting for or faking an actual multi-hour gap.
func cadenceGapExceeded(cfg Config, gap time.Duration) bool {
	return gap > cfg.TickInterval+cfg.TickInterval/10
}

// Run starts the hourly rollup job and blocks until ctx is canceled, the
// same shutdown shape scheduler.Scheduler.Run already uses. It runs one pass
// immediately on startup — not just on the first tick an hour later — so a
// freshly started process (or one recovering from downtime) doesn't leave
// check_rollups_hourly stale for up to an hour before the dashboard's /slo
// reads reflect real recent history.
func Run(ctx context.Context, pool *pgxpool.Pool, cfg Config, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	var lastTickAt time.Time

	// time.Ticker never queues missed ticks: if this process's own clock
	// doesn't advance for a stretch — e.g. a dev machine's Docker VM pausing
	// while the host is idle — and then resumes, exactly one catch-up tick
	// fires with no error, no crash, and (before this) no signal that
	// NFR-005's cadence was actually violated in between. RunOnce's own
	// full-history recompute means no data is lost, but an operator reading
	// /slo has no way to know staleness briefly exceeded its bound unless
	// it's logged here.
	runTick := func() {
		start := time.Now()
		if !lastTickAt.IsZero() {
			if gap := start.Sub(lastTickAt); cadenceGapExceeded(cfg, gap) {
				logger.Warn("rollup job cadence gap exceeded expected interval — check_rollups_hourly was stale until this run", "expected_interval", cfg.TickInterval, "actual_gap", gap)
			}
		}
		lastTickAt = start

		rows, err := RunOnce(ctx, pool, cfg.OpTimeout)
		if err != nil {
			logger.Error("rollup job run failed", "error", err)
			return
		}
		logger.Info("rollup job run complete", "rows_written", rows, "duration", time.Since(start))
	}

	runTick()

	ticker := time.NewTicker(cfg.TickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			runTick()
		}
	}
}
