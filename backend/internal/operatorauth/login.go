package operatorauth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrInvalidCredential is returned by Login for any presented email/password
// pair that doesn't verify — unknown email or wrong password. Deliberately
// one error for both, the same reasoning agentauth.ErrInvalidCredential
// documents for its own distinct credential space: telling a caller "that
// email doesn't exist" vs. "wrong password" hands an attacker a free
// account-enumeration oracle for no legitimate benefit to a real operator,
// who already knows their own email.
var ErrInvalidCredential = errors.New("invalid email or password")

// dummyHash is compared against on an unknown-email login attempt so that
// Login takes roughly the same amount of time (one bcrypt comparison)
// whether or not the email exists — otherwise an attacker could distinguish
// "no such operator" from "wrong password" purely by response latency, the
// timing-side-channel equivalent of the enumeration ErrInvalidCredential's
// single error value already declines to hand out directly. This is a fixed,
// non-secret bcrypt hash of an unguessable placeholder string — it protects
// no real credential, it only exists to burn the same CPU cost.
const dummyHash = "$2a$10$C6UzMDM.H6dfI/f/IKcEeOgOo8ZzP6NgTBH5AtNBjqM2i.vLxJ2Eq"

// Login verifies an operator's email/password against operators.
// password_hash (04-data-model.md/02-requirements.md) and returns the
// operator's identity. This is the real verification half of
// 05-api-contracts.md's /auth/login — the caller (operatorapi.LoginHandler)
// issues a session token from the returned Identity.
func Login(ctx context.Context, pool *pgxpool.Pool, email, password string) (Identity, error) {
	const stmt = `SELECT id::text, password_hash FROM operators WHERE email = $1`
	var id, hash string
	err := pool.QueryRow(ctx, stmt, email).Scan(&id, &hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			VerifyPassword(dummyHash, password) // constant-time-ish: burn the same bcrypt cost as a real lookup
			return Identity{}, ErrInvalidCredential
		}
		return Identity{}, fmt.Errorf("look up operator by email: %w", err)
	}

	if !VerifyPassword(hash, password) {
		return Identity{}, ErrInvalidCredential
	}
	return Identity{OperatorID: id}, nil
}
