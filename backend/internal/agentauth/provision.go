package agentauth

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// provisionDB is the minimal surface CreateAgent needs — satisfied by
// *pgxpool.Pool (the only real caller today: cmd/seed-agent).
type provisionDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Created is the one-time-shown result of registering a new agent —
// mirrors AgentCreated in docs/architecture/openapi.yaml: the plaintext
// Token is returned here and nowhere else. Nothing in this package, or any
// caller, persists it beyond this call.
type Created struct {
	ID             string
	Name           string
	ReportInterval time.Duration
	RegisteredAt   time.Time
	Token          string
}

// CreateAgent is ADR-0003/05-api-contracts.md's real agent-provisioning
// mechanism: generate a high-entropy credential, persist only its hash
// (agents.credential_hash), and return the plaintext exactly once. This is
// the same INSERT docs/architecture/openapi.yaml's POST /api/v1/agents
// operation describes — 12-session-handoff.md's own "no operatorSession
// auth exists yet to gate that endpoint" gap (see cmd/seed-agent's package
// doc) means this session exposes the mechanism as a real, tested Go
// function and a local admin CLI rather than an unauthenticated HTTP
// handler, not that the mechanism itself is a stub.
func CreateAgent(ctx context.Context, db provisionDB, name string, reportIntervalSeconds int) (Created, error) {
	token, err := GenerateToken()
	if err != nil {
		return Created{}, err
	}
	hash := HashToken(token)

	const stmt = `
INSERT INTO agents (name, credential_hash, report_interval_seconds)
VALUES ($1, $2, $3)
RETURNING id::text, registered_at`

	var id string
	var registeredAt time.Time
	if err := db.QueryRow(ctx, stmt, name, hash, reportIntervalSeconds).Scan(&id, &registeredAt); err != nil {
		return Created{}, fmt.Errorf("insert agent %q: %w", name, err)
	}

	return Created{
		ID:             id,
		Name:           name,
		ReportInterval: time.Duration(reportIntervalSeconds) * time.Second,
		RegisteredAt:   registeredAt,
		Token:          token,
	}, nil
}
