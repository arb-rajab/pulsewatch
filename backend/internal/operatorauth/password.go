package operatorauth

import "golang.org/x/crypto/bcrypt"

// bcryptCost is the standard library-recommended default. Unlike
// agentauth.HashToken's deliberate choice of a fast hash for a 256-bit
// random token, an operator's password is a human-chosen secret from a
// comparatively small effective keyspace — exactly the case a slow,
// adaptive KDF exists to defend (02-requirements.md's data classification:
// "Password hashed (not reversibly encrypted)"). bcrypt is the standard,
// battle-tested choice; this is an implementation-level algorithm choice
// this package fills in, the same way agentauth filled in SHA-256 for its
// own, differently-shaped secret.
const bcryptCost = bcrypt.DefaultCost

// HashPassword one-way transforms a plaintext operator password for storage
// in operators.password_hash.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// VerifyPassword reports whether password matches hash. bcrypt's own
// comparison is already constant-time with respect to the password content;
// no additional defense-in-depth compare is needed here the way
// agentauth.tokensEqual adds one after an equality-matched SQL lookup, since
// there is no separate "did the WHERE clause already decide this" step —
// every candidate row's hash must be compared through bcrypt regardless.
func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
