package operatorauth

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultTestDatabaseURL matches every other package's own test convention
// (agentauth/testdb_test.go, scheduler/testdb_test.go, ...).
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

// insertTestOperator creates a real operator row via the real provisioning
// path (CreateOperator) — never a hand-rolled INSERT with a precomputed
// hash — so these tests exercise the exact same mechanism
// cmd/provision-operator uses. email must be unique per test (the column
// carries a UNIQUE constraint).
func insertTestOperator(t *testing.T, pool *pgxpool.Pool, email, password string) string {
	t.Helper()
	created, err := CreateOperator(t.Context(), pool, email, password)
	if err != nil {
		t.Fatalf("create test operator: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM operators WHERE id = $1::uuid`, created.ID)
	})
	return created.ID
}
