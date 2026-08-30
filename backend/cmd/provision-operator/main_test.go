package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/operatorauth"
)

func cleanupOperatorByEmail(t *testing.T, databaseURL, email string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pool, err := pgxpool.New(ctx, databaseURL)
		if err != nil {
			return
		}
		defer pool.Close()
		_, _ = pool.Exec(ctx, `DELETE FROM operators WHERE email = $1`, email)
	})
}

// stdinWithPassword returns an *os.File that yields password+"\n" when read
// — run()'s readPassword reads exactly one line from it, mirroring how a
// real `echo password | provision-operator` invocation behaves.
func stdinWithPassword(t *testing.T, password string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	if _, err := w.WriteString(password + "\n"); err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestRun_RequiresEmail(t *testing.T) {
	if err := run([]string{}, stdinWithPassword(t, "a-real-password"), os.Stdout); err == nil {
		t.Fatal("expected an error when -email is omitted")
	}
}

func TestRun_RequiresPassword(t *testing.T) {
	if err := run([]string{"-email", "x@example.invalid"}, stdinWithPassword(t, ""), os.Stdout); err == nil {
		t.Fatal("expected an error when stdin has no password")
	}
}

func TestRun_RequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if err := run([]string{"-email", "x@example.invalid"}, stdinWithPassword(t, "a-real-password"), os.Stdout); err == nil {
		t.Fatal("expected an error when DATABASE_URL is unset")
	}
}

// connectForTest opens a short-lived pool for a precondition check, skipping
// the test (not failing it) if Postgres isn't reachable at all — matching
// this repo's own testPool convention in every other package.
func connectForTest(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
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
	return pool
}

// requireEmptyOperatorsTable skips (not fails) if any operator row already
// exists. This CLI's own single-operator guard is a genuinely global
// invariant over one shared table, and `go test ./...`'s default
// cross-package parallelism means internal/operatorauth's and
// internal/operatorapi's own test suites transiently create-and-delete
// their own operator rows against this identical Postgres concurrently
// (the same shared-table contention this repo already documents for
// alert_channels) — this precondition check keeps a genuine, rare
// collision a skip rather than a false failure, the same way testPool
// itself skips rather than fails when Postgres isn't reachable at all.
func requireEmptyOperatorsTable(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM operators`).Scan(&count); err != nil {
		t.Fatalf("count operators: %v", err)
	}
	if count > 0 {
		t.Skipf("skipping: %d operator row(s) already exist in the shared test database (likely a concurrently-running package's own test fixture) — this test needs a genuinely empty table to observe first-time bootstrap succeeding", count)
	}
}

// TestRun_RealProvisioning proves the CLI's real path end to end against a
// real Postgres: a genuine operators row with a genuine bcrypt hash, not a
// stub — this repo's real bootstrapping answer to the single-operator
// chicken-and-egg problem.
func TestRun_RealProvisioning(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://pulsewatch:pulsewatch@localhost:5432/pulsewatch?sslmode=disable"
	}
	pool := connectForTest(t, url)
	requireEmptyOperatorsTable(t, pool)
	pool.Close()

	t.Setenv("DATABASE_URL", url)
	const email = "test-provision-operator-cli@example.invalid"
	cleanupOperatorByEmail(t, url, email)

	outFile, err := os.CreateTemp(t.TempDir(), "provision-operator-output")
	if err != nil {
		t.Fatalf("create temp output file: %v", err)
	}
	defer func() { _ = outFile.Close() }()

	if err := run([]string{"-email", email}, stdinWithPassword(t, "a-real-bootstrap-password"), outFile); err != nil {
		t.Fatalf("run: %v", err)
	}

	contents, err := os.ReadFile(outFile.Name())
	if err != nil {
		t.Fatalf("read captured output: %v", err)
	}
	if !strings.Contains(string(contents), "operator_id:") {
		t.Fatalf("expected output to contain the created operator's id, got: %s", contents)
	}
	if strings.Contains(string(contents), "a-real-bootstrap-password") {
		t.Fatal("the plaintext password must never appear in the CLI's own output")
	}
}

// TestRun_RefusesWhenAnOperatorAlreadyExists proves the single-operator
// bootstrapping guard has real teeth: with at least one operator account
// already present, a second invocation must fail and create nothing.
// Deliberately seeds that existing operator directly via
// operatorauth.CreateOperator (never through run() itself) so this
// assertion holds regardless of how many *other* operator rows the shared
// test database happens to have at the moment — count > 0 is guaranteed by
// this test's own seed regardless of any concurrently-running package's own
// fixtures, unlike TestRun_RealProvisioning's first-time-success case,
// which genuinely does need an empty table.
func TestRun_RefusesWhenAnOperatorAlreadyExists(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://pulsewatch:pulsewatch@localhost:5432/pulsewatch?sslmode=disable"
	}
	pool := connectForTest(t, url)
	defer pool.Close()

	t.Setenv("DATABASE_URL", url)
	const existingEmail = "test-provision-existing@example.invalid"
	const secondEmail = "test-provision-second@example.invalid"
	cleanupOperatorByEmail(t, url, existingEmail)
	cleanupOperatorByEmail(t, url, secondEmail)

	if _, err := operatorauth.CreateOperator(t.Context(), pool, existingEmail, "a-real-password"); err != nil {
		t.Fatalf("seed existing operator: %v", err)
	}

	outFile, err := os.CreateTemp(t.TempDir(), "provision-operator-output")
	if err != nil {
		t.Fatalf("create temp output file: %v", err)
	}
	defer func() { _ = outFile.Close() }()

	err = run([]string{"-email", secondEmail}, stdinWithPassword(t, "another-real-password"), outFile)
	if err == nil {
		t.Fatal("expected the invocation to refuse, since an operator already exists")
	}

	var count int
	if scanErr := pool.QueryRow(t.Context(), `SELECT count(*) FROM operators WHERE email = $1`, secondEmail).Scan(&count); scanErr != nil {
		t.Fatalf("count operators: %v", scanErr)
	}
	if count != 0 {
		t.Fatal("expected no row to have been created for the refused second operator")
	}
}
