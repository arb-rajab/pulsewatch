// Package agentauth implements ADR-0003's distinct machine-credential space
// for agents: a bearer token generated once at registration, stored only as
// a hash (agents.credential_hash, 04-data-model.md), and verified against
// that hash on every agent-facing request (05-api-contracts.md's agentToken
// security scheme) — a completely separate identity space from any future
// operatorSession cookie, never reusable across the two.
package agentauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// tokenBytes is the random credential's entropy — 256 bits, comfortably
// beyond what a bearer token needs to resist brute force (contrast with a
// human password, where a slow KDF earns its cost against a small keyspace;
// a token this random gets nothing from one, see HashToken).
const tokenBytes = 32

// GenerateToken mints a new, high-entropy bearer credential (crypto/rand,
// never math/rand — this is a real secret, not a display id). Returned
// base64url-encoded (RawURLEncoding: no padding, URL/header-safe) so it can
// be dropped directly into an Authorization: Bearer header.
func GenerateToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate agent credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashToken is the one-way transform stored in agents.credential_hash and
// recomputed on every verification attempt (authenticate.go).
//
// Plain SHA-256, not bcrypt/scrypt/argon2 — an implementation-level decision
// this package fills in (04-data-model.md names the column, not an
// algorithm), the same way Session 6 picked AES-256-GCM for FR-023 without
// ADR-0002 naming a cipher. A slow password-hashing KDF earns its cost
// defending a human-chosen secret from a small effective keyspace against
// offline guessing; GenerateToken's output is already 256 bits of uniform
// randomness, so a second, deliberately expensive hash adds computational
// cost on every one of this agent's requests without shrinking an
// attacker's search space at all (they still have to search the full 256
// bits either way) — the standard practice for high-entropy API tokens
// (e.g. GitHub personal access tokens) is a fast cryptographic hash, not a
// KDF, and that's what this is.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// tokensEqual compares two hex-encoded hashes in constant time. Both sides
// here are already-hashed values (never the raw token), so this isn't
// defending against a timing attack that recovers the token itself — it's
// cheap, uncontroversial hygiene for any secret-derived comparison, applied
// the same way bookslot/lexicon compare signature hashes elsewhere in this
// developer's portfolio.
func tokensEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
