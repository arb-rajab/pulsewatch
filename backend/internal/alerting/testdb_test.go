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

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Best-effort: a target with incidents/check_results still
		// referencing it (plain REFERENCES, no ON DELETE CASCADE) won't
		// actually delete — same accepted looseness as
		// scheduler.insertTestTarget's own cleanup.
		_, _ = pool.Exec(ctx, `DELETE FROM targets WHERE id = $1::uuid`, targetID)
	})

	return targetID
}

func countOpenIncidents(t *testing.T, pool *pgxpool.Pool, targetID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM incidents WHERE target_id = $1::uuid AND closed_at IS NULL`, targetID).Scan(&count); err != nil {
		t.Fatalf("count open incidents: %v", err)
	}
	return count
}
