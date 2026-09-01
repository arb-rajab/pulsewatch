package rollup

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultTestDatabaseURL matches every other package's own test convention
// (backend/internal/scheduler/testdb_test.go, backend/internal/operatorapi/testdb_test.go).
const defaultTestDatabaseURL = "postgres://pulsewatch:pulsewatch@localhost:5432/pulsewatch?sslmode=disable"

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = defaultTestDatabaseURL
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("skipping: could not create postgres pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("skipping: postgres not reachable at %s: %v", url, err)
	}

	t.Cleanup(pool.Close)
	return pool
}

// insertTestTarget creates a real target + target_schedule row via the real
// schema (not a mock), backdated via created_at so a test can place its
// fixture check_results inside fully-completed hour buckets deterministically.
func insertTestTarget(t *testing.T, pool *pgxpool.Pool, urlOrHost string, intervalSeconds int, createdAt time.Time) string {
	t.Helper()
	ctx := t.Context()

	var targetID string
	err := pool.QueryRow(ctx, `
INSERT INTO targets (type, url_or_host, interval_seconds, timeout_seconds, created_at)
VALUES ('http', $1, $2, 5, $3)
RETURNING id::text`,
		urlOrHost, intervalSeconds, createdAt,
	).Scan(&targetID)
	if err != nil {
		t.Fatalf("insert test target: %v", err)
	}

	_, err = pool.Exec(ctx, `
INSERT INTO target_schedule (target_id, next_due_at)
VALUES ($1::uuid, now())`, targetID)
	if err != nil {
		t.Fatalf("insert test target_schedule: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// check_results and check_rollups_hourly both reference targets with
		// a plain REFERENCES (no ON DELETE CASCADE, deliberately — real
		// historical data shouldn't silently vanish if a target is ever hard-
		// deleted). Every test in this package writes both via RunOnce, so —
		// unlike scheduler/alerting's own equivalent fixtures, which accept
		// a best-effort leaked row on the (already-loosely-tolerated)
		// check_results side alone — leaving this cleanup best-effort would
		// leave a target that internal/rollup's own hourly job keeps
		// re-selecting and refreshing a rollup row for indefinitely, not just
		// a one-time leaked row. Delete child rows first so the target delete
		// actually succeeds.
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM check_rollups_hourly WHERE target_id = $1::uuid`, targetID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM check_results WHERE target_id = $1::uuid`, targetID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM targets WHERE id = $1::uuid`, targetID)
	})

	return targetID
}

func insertCheckResult(t *testing.T, pool *pgxpool.Pool, targetID string, checkedAt time.Time, success bool, latencyMS int) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `
INSERT INTO check_results (target_id, checked_at, success, latency_ms)
VALUES ($1::uuid, $2, $3, $4)
ON CONFLICT (target_id, checked_at) DO NOTHING`,
		targetID, checkedAt, success, latencyMS,
	)
	if err != nil {
		t.Fatalf("insert test check_result: %v", err)
	}
}

type rollupRow struct {
	expectedChecks int
	successCount   int
	failureCount   int
	unknownCount   int
	p50            *int
	p95            *int
}

// equal compares by value, not pointer identity — fetchRollup allocates a
// fresh *int on every call, so two reads of the identical underlying row
// never share a pointer even when the stored value hasn't changed.
func (r rollupRow) equal(other rollupRow) bool {
	return r.expectedChecks == other.expectedChecks &&
		r.successCount == other.successCount &&
		r.failureCount == other.failureCount &&
		r.unknownCount == other.unknownCount &&
		intPtrEqual(r.p50, other.p50) &&
		intPtrEqual(r.p95, other.p95)
}

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func fetchRollup(t *testing.T, pool *pgxpool.Pool, targetID string, hourBucket time.Time) (rollupRow, bool) {
	t.Helper()
	var row rollupRow
	err := pool.QueryRow(t.Context(), `
SELECT expected_checks, success_count, failure_count, unknown_count, p50_latency_ms, p95_latency_ms
FROM check_rollups_hourly
WHERE target_id = $1::uuid AND hour_bucket = $2`,
		targetID, hourBucket,
	).Scan(&row.expectedChecks, &row.successCount, &row.failureCount, &row.unknownCount, &row.p50, &row.p95)
	if err != nil {
		return rollupRow{}, false
	}
	return row, true
}

// TestRunOnce_ComputesRealCountsForACompletedHour proves the job's own
// arithmetic against a fully-controlled fixture: a target checked every 60s,
// with a known number of real successes/failures placed inside one
// fully-completed hour bucket two hours ago (so "now" never races the bucket
// boundary the job itself uses), and one check deliberately omitted so
// unknown_count has to be nonzero and checkable.
func TestRunOnce_ComputesRealCountsForACompletedHour(t *testing.T) {
	pool := testPool(t)

	hourBucket := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	createdAt := hourBucket.Add(-1 * time.Hour) // target existed for the whole bucket

	targetID := insertTestTarget(t, pool, "http://rollup-test.invalid/completed-hour", 60, createdAt)

	// 60s interval over a full hour = 60 expected checks. Insert 55 real
	// results (50 success, 5 failure) at distinct minute offsets, leaving 5
	// minutes with no check_results row at all — the real, structural
	// "unknown" case 04-data-model.md describes (not a flag, an absence).
	successCount, failureCount := 50, 5
	for i := 0; i < successCount; i++ {
		insertCheckResult(t, pool, targetID, hourBucket.Add(time.Duration(i)*time.Minute), true, 100+i)
	}
	for i := successCount; i < successCount+failureCount; i++ {
		insertCheckResult(t, pool, targetID, hourBucket.Add(time.Duration(i)*time.Minute), false, 0)
	}
	// Minutes successCount+failureCount .. 59 (5 of them) are left empty.

	if _, err := RunOnce(t.Context(), pool, 30*time.Second); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	row, ok := fetchRollup(t, pool, targetID, hourBucket)
	if !ok {
		t.Fatalf("expected a check_rollups_hourly row for target=%s hour_bucket=%s, found none", targetID, hourBucket)
	}
	if row.expectedChecks != 60 {
		t.Fatalf("expected expected_checks=60 (3600s / 60s interval), got %d", row.expectedChecks)
	}
	if row.successCount != successCount {
		t.Fatalf("expected success_count=%d, got %d", successCount, row.successCount)
	}
	if row.failureCount != failureCount {
		t.Fatalf("expected failure_count=%d, got %d", failureCount, row.failureCount)
	}
	wantUnknown := 60 - successCount - failureCount
	if row.unknownCount != wantUnknown {
		t.Fatalf("expected unknown_count=%d (60 expected - %d success - %d failure), got %d", wantUnknown, successCount, failureCount, row.unknownCount)
	}
	if row.p50 == nil || row.p95 == nil {
		t.Fatalf("expected non-nil p50/p95 latency with %d successful checks, got p50=%v p95=%v", successCount, row.p50, row.p95)
	}
}

// TestRunOnce_IsIdempotentAndSelfHealsLateData proves two real properties
// the pre-aggregation decision depends on: running the job twice with no new
// data produces byte-identical rollup rows (ON CONFLICT DO UPDATE, not DO
// NOTHING — it's a real overwrite, not a lucky no-op), and a check_results
// row that arrives AFTER the first rollup pass (the documented staleness
// case: a late agent-reported OTLP result landing in an already-rolled-up
// hour) is correctly picked up by the next pass.
func TestRunOnce_IsIdempotentAndSelfHealsLateData(t *testing.T) {
	pool := testPool(t)

	hourBucket := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	createdAt := hourBucket.Add(-1 * time.Hour)
	targetID := insertTestTarget(t, pool, "http://rollup-test.invalid/late-data", 3600, createdAt)

	insertCheckResult(t, pool, targetID, hourBucket.Add(10*time.Minute), true, 50)

	if _, err := RunOnce(t.Context(), pool, 30*time.Second); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	first, ok := fetchRollup(t, pool, targetID, hourBucket)
	if !ok {
		t.Fatalf("expected a rollup row after the first pass")
	}
	if first.successCount != 1 {
		t.Fatalf("expected success_count=1 after the first pass, got %d", first.successCount)
	}

	if _, err := RunOnce(t.Context(), pool, 30*time.Second); err != nil {
		t.Fatalf("second RunOnce (no new data): %v", err)
	}
	second, ok := fetchRollup(t, pool, targetID, hourBucket)
	if !ok {
		t.Fatalf("expected a rollup row after the second pass")
	}
	if !second.equal(first) {
		t.Fatalf("expected an unchanged re-run to be idempotent: first=%+v second=%+v", first, second)
	}

	// Late-arriving data: a second real check_results row lands in the same,
	// already-rolled-up hour (the exact scenario the "self-heals late data"
	// design decision names).
	insertCheckResult(t, pool, targetID, hourBucket.Add(20*time.Minute), false, 0)

	if _, err := RunOnce(t.Context(), pool, 30*time.Second); err != nil {
		t.Fatalf("third RunOnce (late data arrived): %v", err)
	}
	third, ok := fetchRollup(t, pool, targetID, hourBucket)
	if !ok {
		t.Fatalf("expected a rollup row after the third pass")
	}
	if third.successCount != 1 || third.failureCount != 1 {
		t.Fatalf("expected the late failure to be picked up (success=1, failure=1), got %+v", third)
	}
}

// TestRunOnce_TargetCreatedMidHourGetsNoPhantomDeficit proves
// 04-data-model.md's explicit invariant: a target that didn't exist for the
// whole bucket must not show a deficit for the portion of the hour before it
// was created — expected_checks must be computed from
// max(bucket_start, created_at), not bucket_start alone.
func TestRunOnce_TargetCreatedMidHourGetsNoPhantomDeficit(t *testing.T) {
	pool := testPool(t)

	hourBucket := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	// Created 15 minutes into the bucket: only 45 of the 60 minutes were
	// ever "expected" at a 60s interval.
	createdAt := hourBucket.Add(15 * time.Minute)
	targetID := insertTestTarget(t, pool, "http://rollup-test.invalid/mid-hour-create", 60, createdAt)

	if _, err := RunOnce(t.Context(), pool, 30*time.Second); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	row, ok := fetchRollup(t, pool, targetID, hourBucket)
	if !ok {
		t.Fatalf("expected a rollup row even with zero check_results, found none")
	}
	if row.expectedChecks != 45 {
		t.Fatalf("expected expected_checks=45 (45 remaining minutes after a 15-minute-late creation), got %d", row.expectedChecks)
	}
	if row.unknownCount != 45 {
		t.Fatalf("expected unknown_count=45 (no check_results at all), got %d", row.unknownCount)
	}
}
