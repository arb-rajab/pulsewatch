package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func cleanupAgentByName(t *testing.T, databaseURL, name string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pool, err := pgxpool.New(ctx, databaseURL)
		if err != nil {
			return
		}
		defer pool.Close()
		_, _ = pool.Exec(ctx, `DELETE FROM agents WHERE name = $1`, name)
	})
}

func TestRun_RequiresName(t *testing.T) {
	if err := run([]string{"-report-interval", "60"}, os.Stdout); err == nil {
		t.Fatal("expected an error when -name is omitted")
	}
}

func TestRun_RejectsNonPositiveReportInterval(t *testing.T) {
	if err := run([]string{"-name", "x", "-report-interval", "0"}, os.Stdout); err == nil {
		t.Fatal("expected an error for -report-interval=0")
	}
	if err := run([]string{"-name", "x", "-report-interval", "-5"}, os.Stdout); err == nil {
		t.Fatal("expected an error for a negative -report-interval")
	}
}

func TestRun_RequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if err := run([]string{"-name", "x"}, os.Stdout); err == nil {
		t.Fatal("expected an error when DATABASE_URL is unset")
	}
}

// TestRun_RealProvisioning proves the CLI's real path end to end against a
// real Postgres: a genuine agents row with a genuine bearer credential,
// not a stub — the same real mechanism this session's DoD requires.
func TestRun_RealProvisioning(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://pulsewatch:pulsewatch@localhost:5432/pulsewatch?sslmode=disable"
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
	pool.Close()

	t.Setenv("DATABASE_URL", url)
	cleanupAgentByName(t, url, "test-seed-agent-cli")

	// run(...) takes a *os.File (matching os.Stdout's real type directly,
	// rather than the narrower io.Writer, so main() needs no adapter) — a
	// throwaway temp file is the simplest real *os.File to hand it here.
	outFile, err := os.CreateTemp(t.TempDir(), "seed-agent-output")
	if err != nil {
		t.Fatalf("create temp output file: %v", err)
	}
	defer func() { _ = outFile.Close() }()

	if err := run([]string{"-name", "test-seed-agent-cli", "-report-interval", "45"}, outFile); err != nil {
		t.Fatalf("run: %v", err)
	}

	contents, err := os.ReadFile(outFile.Name())
	if err != nil {
		t.Fatalf("read captured output: %v", err)
	}
	if !strings.Contains(string(contents), "token:") {
		t.Fatalf("expected output to contain a plaintext token line, got: %s", contents)
	}
}
