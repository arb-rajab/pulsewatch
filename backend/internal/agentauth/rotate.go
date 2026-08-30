package agentauth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrAgentNotFound is returned by RotateCredential when agentID doesn't name
// an existing agent.
var ErrAgentNotFound = errors.New("agent not found")

// Rotated is the one-time-shown result of rotating an agent's credential —
// mirrors AgentCredentialRotated in docs/architecture/openapi.yaml exactly,
// the same one-time-plaintext guarantee Created gives at creation.
type Rotated struct {
	AgentID   string
	Token     string
	RotatedAt time.Time
}

// RotateCredential mints a fresh high-entropy token and overwrites
// agents.credential_hash with its hash in a single statement — the previous
// token stops verifying the instant this commits (Authenticate looks up by
// exact hash equality, so the old hash simply no longer matches any row).
// 05-api-contracts.md deliberately does not idempotency-key-guard this
// operation: a client that never received this response never learned the
// new token, so a plain retry minting yet another one is harmless.
func RotateCredential(ctx context.Context, db provisionDB, agentID string) (Rotated, error) {
	token, err := GenerateToken()
	if err != nil {
		return Rotated{}, err
	}
	hash := HashToken(token)

	const stmt = `
UPDATE agents SET credential_hash = $1
WHERE id = $2::uuid
RETURNING now()`

	var rotatedAt time.Time
	err = db.QueryRow(ctx, stmt, hash, agentID).Scan(&rotatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Rotated{}, ErrAgentNotFound
		}
		return Rotated{}, fmt.Errorf("rotate credential for agent %s: %w", agentID, err)
	}

	return Rotated{AgentID: agentID, Token: token, RotatedAt: rotatedAt}, nil
}
