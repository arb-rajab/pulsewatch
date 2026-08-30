package agentauth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrInvalidCredential is returned by Authenticate for any presented token
// that doesn't verify — no token, an unknown token, or a rotated/revoked
// one. Deliberately one error for all three: telling a caller *which* of
// those applies would let it distinguish "this agent never existed" from
// "this agent's credential was rotated," information an unauthenticated
// caller has no legitimate use for.
var ErrInvalidCredential = errors.New("invalid or missing agent credential")

// Identity is what a verified bearer token resolves to — the agent's own
// identity, never another agent's (05-api-contracts.md: "an agent's
// identity comes from its own token").
type Identity struct {
	AgentID string
}

// Authenticate verifies a presented bearer token against agents.
// credential_hash (04-data-model.md) and returns the agent it belongs to.
// This is the real verification half of ADR-0003's distinct machine-
// credential space — every agent-facing request (GET /agent/assignments,
// POST /v1/logs) goes through this, never through operatorSession's cookie
// mechanism.
//
// credential_hash carries a unique index (04-data-model.md's indexing
// strategy), so at most one row can ever match a given hash — this looks up
// by hash directly rather than scanning every agent and comparing in Go,
// which is both simpler and lets Postgres's own index do the work.
func Authenticate(ctx context.Context, pool *pgxpool.Pool, token string) (Identity, error) {
	if token == "" {
		return Identity{}, ErrInvalidCredential
	}
	hash := HashToken(token)

	const stmt = `SELECT id::text, credential_hash FROM agents WHERE credential_hash = $1`
	var id, storedHash string
	err := pool.QueryRow(ctx, stmt, hash).Scan(&id, &storedHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Identity{}, ErrInvalidCredential
		}
		return Identity{}, fmt.Errorf("look up agent credential: %w", err)
	}

	// The WHERE clause already matched by equality; this second comparison
	// is defense in depth against a future refactor that loosens the query
	// (e.g. a prefix or case-insensitive lookup) without also revisiting
	// this check — constant-time so it never becomes the one comparison in
	// this path that isn't.
	if !tokensEqual(hash, storedHash) {
		return Identity{}, ErrInvalidCredential
	}

	return Identity{AgentID: id}, nil
}
