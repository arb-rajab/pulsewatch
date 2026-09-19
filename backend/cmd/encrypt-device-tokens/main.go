// Command encrypt-device-tokens is the real-data half of migration 000012
// (B-012 security audit, 06-security-threat-model.md T-09): it backfills
// token_encrypted/token_hash for every device_tokens row a pre-000012
// deployment left holding only a plaintext token column, using the exact
// same AES-256-GCM helper (alerting.EncryptDestination) and HMAC-SHA256
// blind index (alerting.HashDeviceToken) RegisterDeviceToken now writes for
// every new registration.
//
// It exists as a separate command, run once between deploying 000012 (which
// only adds the new columns) and 000013 (which enforces NOT NULL on them
// and drops the plaintext column for good), because a SQL migration file
// has no access to ALERT_CHANNEL_ENCRYPTION_KEY — encryption is an
// application-level concern here, the same way FR-023 always treated it for
// alert_channels.destination_encrypted. This is an expand/contract
// migration pair with a real data-migration step in the middle, not a
// single schema-only rewrite: a fresh database (this project's own CI, or
// any deployment created after 000013) has no plaintext rows and this
// command has nothing to do.
//
// Usage:
//
//	DATABASE_URL=... ALERT_CHANNEL_ENCRYPTION_KEY=... go run ./cmd/encrypt-device-tokens
//
// Safe to run more than once (idempotent: it only selects rows where
// token_encrypted IS NULL) and safe to run against a database that has
// already had 000013 applied (zero rows match, since the plaintext token
// column no longer exists — a missing-column error, not silent
// no-op, in that specific case, which is why 000013 must not run until this
// has completed at least once against a database that had 000012 but not
// yet 000013).
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/alerting"
)

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "encrypt-device-tokens:", err)
		os.Exit(1)
	}
}

func run(out *os.File) error {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	key, err := alerting.EncryptionKeyFromEnv()
	if err != nil {
		return fmt.Errorf("ALERT_CHANNEL_ENCRYPTION_KEY: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer pool.Close()

	n, err := backfill(ctx, pool, key)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "encrypted %d device_tokens row(s)\n", n)
	return err
}

// backfill encrypts every row 000012 left with only a plaintext token.
// Row-at-a-time (not a bulk UPDATE) because each row's ciphertext needs its
// own fresh random nonce (EncryptDestination) — the same reason
// alert_channels never had a bulk-encrypt migration either.
func backfill(ctx context.Context, pool *pgxpool.Pool, key []byte) (int, error) {
	rows, err := pool.Query(ctx, `SELECT id::text, token FROM device_tokens WHERE token_encrypted IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("query unencrypted device_tokens rows: %w", err)
	}
	type row struct{ id, token string }
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.token); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan device_tokens row: %w", err)
		}
		pending = append(pending, r)
	}
	closeErr := rows.Err()
	rows.Close()
	if closeErr != nil {
		return 0, fmt.Errorf("query unencrypted device_tokens rows: %w", closeErr)
	}

	for _, r := range pending {
		encrypted, err := alerting.EncryptDestination(r.token, key)
		if err != nil {
			return 0, fmt.Errorf("encrypt device token %s: %w", r.id, err)
		}
		hash := alerting.HashDeviceToken(r.token, key)
		if _, err := pool.Exec(ctx,
			`UPDATE device_tokens SET token_encrypted = $1, token_hash = $2 WHERE id = $3::uuid`,
			encrypted, hash, r.id,
		); err != nil {
			return 0, fmt.Errorf("update device_tokens row %s: %w", r.id, err)
		}
	}
	return len(pending), nil
}
