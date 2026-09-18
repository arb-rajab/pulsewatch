package alerting

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultTestDatabaseURL mirrors scheduler.defaultTestDatabaseURL exactly —
// the CI backend job's Postgres service container. Locally, without that
// service running, tests skip rather than fail; the properties they prove
// still get proved in CI on every push.
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

// insertTestTargetRow creates a real targets row — the minimum incidents'
// own FK requires — for tests that exercise OpenIncident/CloseIncident
// directly, without going through the full scheduler pipeline.
func insertTestTargetRow(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()

	var targetID string
	err := pool.QueryRow(t.Context(), `
INSERT INTO targets (type, url_or_host, interval_seconds, timeout_seconds)
VALUES ('http', 'http://example.invalid/alerting-test', 60, 5)
RETURNING id::text`).Scan(&targetID)
	if err != nil {
		t.Fatalf("insert test target: %v", err)
	}

	t.Cleanup(func() { deleteTargetCascade(pool, targetID) })

	return targetID
}

// deleteTargetCascade removes a target and every row that a plain
// REFERENCES-only child table (no ON DELETE CASCADE) still holds against
// it, in dependency order, before deleting the target itself.
//
// Root cause of B-006 (this package's own contribution): a bare `DELETE
// FROM targets` here silently failed (a foreign-key violation whose error
// this fixture discarded via `_, _ =`) whenever a test called
// OpenIncident/RecordCheckResult directly against the fixture target —
// exactly what dispatch_test.go and incident_concurrency_test.go do —
// leaving the target (and everything still pointing at it) orphaned in the
// shared test Postgres instead of failing loudly, matching scheduler's own
// identical fixture and internal/rollup.insertTestTarget's already-correct
// child-first ordering. This was the "accepted looseness" the old comment
// here named; it's no longer accepted, since the fix is no harder than
// scheduler/agentapi's own version of the same helper. target_schedule
// needs no explicit delete: it's the one child table with ON DELETE
// CASCADE (see its own migration's comment).
func deleteTargetCascade(pool *pgxpool.Pool, targetID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = pool.Exec(ctx, `DELETE FROM alert_dispatches WHERE incident_id IN (SELECT id FROM incidents WHERE target_id = $1::uuid)`, targetID)
	_, _ = pool.Exec(ctx, `DELETE FROM incidents WHERE target_id = $1::uuid`, targetID)
	_, _ = pool.Exec(ctx, `DELETE FROM check_results WHERE target_id = $1::uuid`, targetID)
	_, _ = pool.Exec(ctx, `DELETE FROM check_rollups_hourly WHERE target_id = $1::uuid`, targetID)
	_, _ = pool.Exec(ctx, `DELETE FROM targets WHERE id = $1::uuid`, targetID)
}

func countOpenIncidents(t *testing.T, pool *pgxpool.Pool, targetID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM incidents WHERE target_id = $1::uuid AND closed_at IS NULL`, targetID).Scan(&count); err != nil {
		t.Fatalf("count open incidents: %v", err)
	}
	return count
}

// insertTestTargetWithSchedule creates a real target + target_schedule row
// — RecordCheckResult reads/writes target_schedule.streak/state, which
// insertTestTargetRow's bare targets-only fixture doesn't provide. Mirrors
// scheduler.insertTestTarget's own shape, scoped to this package's own
// recordresult_test.go.
func insertTestTargetWithSchedule(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	targetID := insertTestTargetRow(t, pool)

	_, err := pool.Exec(t.Context(), `
INSERT INTO target_schedule (target_id, next_due_at)
VALUES ($1::uuid, now())`, targetID)
	if err != nil {
		t.Fatalf("insert test target_schedule: %v", err)
	}
	return targetID
}

func fetchStreakState(t *testing.T, pool *pgxpool.Pool, targetID string) (streak int, state string) {
	t.Helper()
	if err := pool.QueryRow(t.Context(), `SELECT streak, state FROM target_schedule WHERE target_id = $1::uuid`, targetID).Scan(&streak, &state); err != nil {
		t.Fatalf("fetch streak/state: %v", err)
	}
	return streak, state
}

// persistStreakState writes back the streak/state RecordCheckResult
// computed — RecordCheckResult deliberately leaves this write to its
// caller (see its own doc comment), so tests that call it directly (rather
// than through scheduler.releaseAndRecord, which does this as part of its
// own larger UPDATE) have to do it themselves to exercise a realistic
// multi-call sequence.
func persistStreakState(t *testing.T, pool *pgxpool.Pool, targetID string, streak int, state State) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `UPDATE target_schedule SET streak = $1, state = $2 WHERE target_id = $3::uuid`, streak, string(state), targetID)
	if err != nil {
		t.Fatalf("persist streak/state: %v", err)
	}
}

func countCheckResultsFor(t *testing.T, pool *pgxpool.Pool, targetID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM check_results WHERE target_id = $1::uuid`, targetID).Scan(&count); err != nil {
		t.Fatalf("count check_results: %v", err)
	}
	return count
}
