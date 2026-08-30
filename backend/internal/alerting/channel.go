package alerting

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Channel is one operator-configured alert destination (FR-013/FR-014),
// decrypted for immediate use by a Dispatcher. destination is unexported
// deliberately: it must never leave this package, be logged, or be returned
// by any API — the same FR-023 structural guarantee 05-api-contracts.md
// applies to the AlertChannel schema, applied here on the read side too.
type Channel struct {
	ID          string
	Type        string // "webhook" | "email"
	destination string
}

// EncryptionKeyFromEnv loads the AES-256 key used for FR-023's app-level
// "encrypted at rest" requirement on alert_channels.destination_encrypted,
// from ALERT_CHANNEL_ENCRYPTION_KEY (base64-encoded, must decode to exactly
// 32 bytes). Not added to .env.example/docker-compose with a real value —
// this repo's own ".env.example: no real secrets belong in this file" rule
// — and there is no channel-registration API yet (12-session-handoff.md) to
// create a row this key would need to decrypt in production, so an unset
// key breaks nothing today. See LoadChannels for how a missing key is
// handled once a channel actually exists.
func EncryptionKeyFromEnv() ([]byte, error) {
	encoded := os.Getenv("ALERT_CHANNEL_ENCRYPTION_KEY")
	if encoded == "" {
		return nil, errors.New("ALERT_CHANNEL_ENCRYPTION_KEY not set")
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode ALERT_CHANNEL_ENCRYPTION_KEY: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("ALERT_CHANNEL_ENCRYPTION_KEY must decode to 32 bytes (AES-256), got %d", len(key))
	}
	return key, nil
}

// EncryptDestination is FR-023's app-level encryption-at-rest step for a
// channel's destination (a webhook URL, possibly with an embedded bearer
// token, or an email/SMS provider credential): AES-256-GCM, a fresh random
// nonce prepended to the ciphertext, base64-encoded for storage in
// destination_encrypted. Exported for the (not-yet-existing)
// channel-registration API to call once it exists, and for this session's
// own test fixtures — there is no other writer of this column today.
func EncryptDestination(plaintext string, key []byte) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decryptDestination reverses EncryptDestination. Unexported: the plaintext
// it returns must never leave this package's own dispatch path (dispatch.go)
// — never logged, never surfaced on any API response.
func decryptDestination(encoded string, key []byte) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode destination_encrypted: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(raw) < nonceSize {
		return "", errors.New("destination_encrypted too short to contain a nonce")
	}
	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt destination: %w", err)
	}
	return string(plaintext), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new GCM: %w", err)
	}
	return gcm, nil
}

// LoadChannels reads every configured alert channel and decrypts its
// destination for immediate, one-shot use by a Dispatcher. Zero rows — the
// current production reality, since no channel-registration API exists yet
// — returns an empty slice and requires no key at all. A nil/invalid key
// only matters once a channel actually exists to decrypt: that failure is
// returned to the caller to log and skip dispatch for (NotifyChannels),
// never a panic that would take the scheduler down.
func LoadChannels(ctx context.Context, pool *pgxpool.Pool, key []byte) ([]Channel, error) {
	rows, err := pool.Query(ctx, `SELECT id::text, type, destination_encrypted FROM alert_channels`)
	if err != nil {
		return nil, fmt.Errorf("query alert_channels: %w", err)
	}
	defer rows.Close()

	var channels []Channel
	for rows.Next() {
		var id, channelType, encrypted string
		if err := rows.Scan(&id, &channelType, &encrypted); err != nil {
			return nil, fmt.Errorf("scan alert_channels row: %w", err)
		}
		if key == nil {
			return nil, fmt.Errorf("channel %s exists but ALERT_CHANNEL_ENCRYPTION_KEY is not configured", id)
		}
		destination, err := decryptDestination(encrypted, key)
		if err != nil {
			return nil, fmt.Errorf("decrypt destination for channel %s: %w", id, err)
		}
		channels = append(channels, Channel{ID: id, Type: channelType, destination: destination})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query alert_channels: %w", err)
	}
	return channels, nil
}
