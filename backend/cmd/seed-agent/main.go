// Command seed-agent was Session 7's interim agent-provisioning mechanism,
// built because docs/architecture/openapi.yaml's POST /api/v1/agents
// requires operatorSession auth and no such mechanism existed yet in this
// repo. Session 8 built that mechanism for real (internal/operatorauth,
// internal/operatorapi) and POST /api/v1/agents is now a real, gated HTTP
// handler (operatorapi.CreateAgent) calling the identical
// agentauth.CreateAgent function this CLI always called — so this tool is
// no longer a stopgap standing in for a missing endpoint.
//
// It is kept, deliberately, as a genuine break-glass provisioning path: it
// needs only DATABASE_URL, never the backend HTTP server or an operator
// session, so it still works when Postgres is reachable but the API
// process itself is down, mid-incident, or not yet deployed — the same
// operational role a framework's own DB-direct admin CLI plays relative to
// its HTTP-authenticated admin UI. For every normal, day-to-day agent
// registration, prefer the real HTTP path
// (POST /api/v1/agents, operatorapi.CreateAgent) — it is exercised by real
// integration tests the same way this CLI's own tests exercise
// agentauth.CreateAgent directly, and going through it keeps agent
// provisioning auditable via the same operator-session identity every
// other operator action goes through.
//
// Usage:
//
//	seed-agent -name "homelab-agent-1" -report-interval 60
//
// Prints the new agent's id and its plaintext bearer token to stdout
// exactly once — the same one-time-shown guarantee AgentCreated (openapi.yaml)
// makes, just over a CLI instead of a JSON response body.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/agentauth"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "seed-agent:", err)
		os.Exit(1)
	}
}

func run(args []string, out *os.File) error {
	fs := flag.NewFlagSet("seed-agent", flag.ContinueOnError)
	name := fs.String("name", "", "human-readable name for the new agent (required)")
	reportInterval := fs.Int("report-interval", 60, "heartbeat/report interval in seconds (ADR-0003 Trade-offs default: 60)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("-name is required")
	}
	if *reportInterval <= 0 {
		return errors.New("-report-interval must be positive")
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer pool.Close()

	created, err := agentauth.CreateAgent(ctx, pool, *name, *reportInterval)
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}

	if _, err := fmt.Fprintf(out, "agent_id:  %s\n", created.ID); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if _, err := fmt.Fprintf(out, "name:      %s\n", created.Name); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if _, err := fmt.Fprintf(out, "token:     %s\n", created.Token); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if _, err := fmt.Fprintln(out, "This token is shown exactly once — store it now. It cannot be retrieved again; use rotation once that exists if it's lost."); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
