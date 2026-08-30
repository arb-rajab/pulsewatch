// Command seed-agent is this session's real agent-provisioning mechanism
// (ADR-0003, 05-api-contracts.md's POST /api/v1/agents) exposed as a local
// admin CLI rather than an HTTP endpoint.
//
// docs/architecture/openapi.yaml's POST /api/v1/agents requires
// operatorSession auth — the stateless, server-HMAC-signed session cookie
// 05-api-contracts.md's "Authentication and authorisation model" section
// designs. That mechanism has no implementation in this repo yet: no
// session store, no /auth/login handler, nothing to verify a cookie
// against — the same class of gap Sessions 5/6 already flagged for
// target/alert-channel registration ("no target- or channel-registration
// REST API exists yet... because no handler implements
// docs/architecture/openapi.yaml"). Building a full cookie-auth system
// solely to gate one agent-creation endpoint would be scope well beyond
// this session's own ADR-0003 focus, and shipping that endpoint
// unauthenticated over HTTP in a public repo would be a real regression,
// not a reasonable interim step — so this session provisions agents the
// same way Sessions 5/6 provisioned targets and alert channels for their
// own proofs: directly, not through a REST handler that doesn't exist yet.
// The difference here is that a target/channel row is inert until a
// scheduler tick or dispatch touches it, while an agent row's whole
// purpose is to hold a live bearer credential — psql alone can't compute
// this package's own HashToken digest, so this real, tested Go binary
// exists to call agentauth.CreateAgent (the exact function real
// provisioning logic will still call once an operator-authenticated HTTP
// handler exists) rather than asking an operator to hand-roll a hash.
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
