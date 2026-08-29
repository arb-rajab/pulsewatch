package scheduler

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testDatabaseURL matches the CI backend job's Postgres service container
// (postgres:16-alpine, published on localhost:5432 there) so these tests
// run unmodified in CI. Locally, without that service running, the tests
// skip rather than fail — the properties they prove still get proved in CI
// on every push.
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

// testTargetOpts configures the fixture insertTestTarget creates.
type testTargetOpts struct {
	timeoutSeconds  int
	intervalSeconds int
	nextDueAt       time.Time
	leaseOwner      *string
	leaseExpiresAt  *time.Time
}

func defaultTestTargetOpts() testTargetOpts {
	return testTargetOpts{
		timeoutSeconds:  5,
		intervalSeconds: 60,
		nextDueAt:       time.Now().Add(-1 * time.Minute), // already due
	}
}

// insertTestTarget creates a real target + target_schedule row (an
// http target pointed at urlOrHost) so tests exercise the actual schema
// Session 4 migrated, not a mock. Cleaned up via t.Cleanup.
func insertTestTarget(t *testing.T, pool *pgxpool.Pool, urlOrHost string, opts testTargetOpts) string {
	t.Helper()
	ctx := t.Context()

	var targetID string
	err := pool.QueryRow(ctx, `
INSERT INTO targets (type, url_or_host, interval_seconds, timeout_seconds)
VALUES ('http', $1, $2, $3)
RETURNING id::text`,
		urlOrHost, opts.intervalSeconds, opts.timeoutSeconds,
	).Scan(&targetID)
	if err != nil {
		t.Fatalf("insert test target: %v", err)
	}

	_, err = pool.Exec(ctx, `
INSERT INTO target_schedule (target_id, next_due_at, lease_owner, lease_expires_at)
VALUES ($1::uuid, $2, $3, $4)`,
		targetID, opts.nextDueAt, opts.leaseOwner, opts.leaseExpiresAt,
	)
	if err != nil {
		t.Fatalf("insert test target_schedule: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM targets WHERE id = $1::uuid`, targetID)
	})

	return targetID
}

func countCheckResults(t *testing.T, pool *pgxpool.Pool, targetID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM check_results WHERE target_id = $1::uuid`, targetID).Scan(&count); err != nil {
		t.Fatalf("count check_results: %v", err)
	}
	return count
}

func fetchScheduleRow(t *testing.T, pool *pgxpool.Pool, targetID string) (leaseOwner *string, leaseExpiresAt *time.Time, nextDueAt time.Time) {
	t.Helper()
	err := pool.QueryRow(t.Context(), `
SELECT lease_owner, lease_expires_at, next_due_at FROM target_schedule WHERE target_id = $1::uuid`, targetID,
	).Scan(&leaseOwner, &leaseExpiresAt, &nextDueAt)
	if err != nil {
		t.Fatalf("fetch target_schedule row for %s: %v", targetID, err)
	}
	return leaseOwner, leaseExpiresAt, nextDueAt
}

func mustParseOwner(n int) string {
	return fmt.Sprintf("test-owner-%d", n)
}
