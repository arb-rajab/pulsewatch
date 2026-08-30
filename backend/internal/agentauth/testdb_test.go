package agentauth

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultTestDatabaseURL matches every other package's own test convention
// (backend/internal/scheduler/testdb_test.go, backend/internal/alerting/
// testdb_test.go) — the CI backend job's Postgres service container.
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

// insertTestAgent creates a real agent row via the real provisioning path
// (CreateAgent) — never a hand-rolled INSERT with a fake hash — so these
// tests exercise the exact same credential mechanism production uses.
// Returns the agent id and its one-time plaintext token.
func insertTestAgent(t *testing.T, pool *pgxpool.Pool, name string, reportIntervalSeconds int) (id, token string) {
	t.Helper()
	created, err := CreateAgent(t.Context(), pool, name, reportIntervalSeconds)
	if err != nil {
		t.Fatalf("create test agent: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM agents WHERE id = $1::uuid`, created.ID)
	})
	return created.ID, created.Token
}
