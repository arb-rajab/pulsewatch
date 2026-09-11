package alerting

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This file is the device_tokens read/write surface ADR-0007 adds: the
// registration path operatorapi exposes to the mobile app, and the fan-out
// and dead-marking paths PushDispatcher uses. Both live here, in the
// package that owns alerting's data, for the same reason EncryptDestination
// does: operatorapi holds HTTP concerns, not this table's invariants.

// Provider/platform vocabularies, kept in one place and matched by
// device_tokens' own CHECK constraints (migration 000011). Validated in Go
// as well as in the schema so a bad registration is a 422 with a useful
// message, not a raw constraint-violation 503.
const (
	ProviderFCM  = "fcm"
	ProviderAPNs = "apns"
)

// ValidPushProvider reports whether provider is one this repo can deliver
// to (see internal/pushprovider).
func ValidPushProvider(provider string) bool {
	return provider == ProviderFCM || provider == ProviderAPNs
}

// ValidDevicePlatform reports whether platform is one device_tokens accepts.
// Platform is recorded for operator visibility and never used to route a
// send — provider is what decides which client can deliver a token, since
// FCM can itself deliver to iOS devices.
func ValidDevicePlatform(platform string) bool {
	return platform == "ios" || platform == "android"
}

// maxDeviceTokenLength bounds what this API will store. Real FCM
// registration tokens are ~160-260 characters and APNs tokens are 64 hex
// characters; 4096 is far above both and far below "an authenticated
// operator can fill a table with megabyte rows".
const maxDeviceTokenLength = 4096

// ErrInvalidDeviceToken is returned for a registration this package rejects
// before it ever reaches Postgres.
var ErrInvalidDeviceToken = errors.New("invalid device token registration")

// DeviceTokenRecord is one registered device, as the operator API reports
// it. The token value itself is deliberately absent: it is accepted on
// write and used on dispatch, and there is no read path that hands it back
// out — the same structural discipline alertChannelResponse applies to a
// channel's destination, applied to the one other value in this schema that
// can be used to reach an operator's device.
type DeviceTokenRecord struct {
	ID               string     `json:"id"`
	Provider         string     `json:"provider"`
	Platform         string     `json:"platform"`
	CreatedAt        time.Time  `json:"created_at"`
	LastRegisteredAt time.Time  `json:"last_registered_at"`
	LastDeliveredAt  *time.Time `json:"last_delivered_at"`
	DeadAt           *time.Time `json:"dead_at"`
	DeadReason       *string    `json:"dead_reason"`
}

// liveDeviceToken is the dispatch-side view: the token value plus the id
// needed to mark it dead or delivered. Unexported and never returned by any
// API, matching Channel.destination's own treatment.
type liveDeviceToken struct {
	id    string
	token string
}

// RegisterDeviceToken upserts one (provider, token) pair for operatorID.
//
// Re-registering an existing token is the normal case, not an error: a
// mobile app re-registers on every launch and after every provider-side
// token refresh. The upsert deliberately clears revoked_at, dead_at and
// dead_reason — a device that has just told us it holds this token is,
// by direct evidence, live again, and refusing to resurrect it would mean
// one transient dead-marking silently muted an operator's phone forever.
func RegisterDeviceToken(ctx context.Context, pool *pgxpool.Pool, operatorID, provider, platform, token string) (DeviceTokenRecord, error) {
	token = strings.TrimSpace(token)
	switch {
	case !ValidPushProvider(provider):
		return DeviceTokenRecord{}, fmt.Errorf(`%w: provider must be "fcm" or "apns"`, ErrInvalidDeviceToken)
	case !ValidDevicePlatform(platform):
		return DeviceTokenRecord{}, fmt.Errorf(`%w: platform must be "ios" or "android"`, ErrInvalidDeviceToken)
	case token == "":
		return DeviceTokenRecord{}, fmt.Errorf("%w: token must not be empty", ErrInvalidDeviceToken)
	case len(token) > maxDeviceTokenLength:
		return DeviceTokenRecord{}, fmt.Errorf("%w: token exceeds %d characters", ErrInvalidDeviceToken, maxDeviceTokenLength)
	}

	const stmt = `
INSERT INTO device_tokens (operator_id, provider, platform, token)
VALUES ($1::uuid, $2, $3, $4)
ON CONFLICT (provider, token) DO UPDATE SET
    operator_id        = EXCLUDED.operator_id,
    platform           = EXCLUDED.platform,
    last_registered_at = now(),
    revoked_at         = NULL,
    dead_at            = NULL,
    dead_reason        = NULL
RETURNING id::text, provider, platform, created_at, last_registered_at, last_delivered_at, dead_at, dead_reason`

	var rec DeviceTokenRecord
	err := pool.QueryRow(ctx, stmt, operatorID, provider, platform, token).Scan(
		&rec.ID, &rec.Provider, &rec.Platform, &rec.CreatedAt,
		&rec.LastRegisteredAt, &rec.LastDeliveredAt, &rec.DeadAt, &rec.DeadReason,
	)
	if err != nil {
		return DeviceTokenRecord{}, fmt.Errorf("upsert device_tokens row: %w", err)
	}
	return rec, nil
}

// ErrDeviceTokenNotFound is returned by RevokeDeviceToken for an id that
// does not exist or is not this operator's.
var ErrDeviceTokenNotFound = errors.New("device token not found")

// RevokeDeviceToken is the unregister path (a signed-out or reinstalled
// app). It sets revoked_at rather than deleting the row: the history of
// which devices were notified for which incidents is exactly what
// alert_dispatches exists to preserve, and a delete would strand it.
func RevokeDeviceToken(ctx context.Context, pool *pgxpool.Pool, operatorID, id string) error {
	const stmt = `
UPDATE device_tokens SET revoked_at = now()
WHERE id = $1::uuid AND operator_id = $2::uuid AND revoked_at IS NULL
RETURNING id`
	var revoked string
	err := pool.QueryRow(ctx, stmt, id, operatorID).Scan(&revoked)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDeviceTokenNotFound
		}
		return fmt.Errorf("revoke device_tokens row: %w", err)
	}
	return nil
}

// ListDeviceTokens returns operatorID's registrations, newest first,
// including revoked and dead ones — an operator debugging "why didn't my
// phone buzz" needs to see the dead row and its reason, which is the whole
// reason dead_reason is a column rather than only a log line.
func ListDeviceTokens(ctx context.Context, pool *pgxpool.Pool, operatorID string) ([]DeviceTokenRecord, error) {
	const stmt = `
SELECT id::text, provider, platform, created_at, last_registered_at, last_delivered_at, dead_at, dead_reason
FROM device_tokens
WHERE operator_id = $1::uuid
ORDER BY last_registered_at DESC`

	rows, err := pool.Query(ctx, stmt, operatorID)
	if err != nil {
		return nil, fmt.Errorf("query device_tokens: %w", err)
	}
	defer rows.Close()

	records := []DeviceTokenRecord{}
	for rows.Next() {
		var rec DeviceTokenRecord
		if err := rows.Scan(&rec.ID, &rec.Provider, &rec.Platform, &rec.CreatedAt,
			&rec.LastRegisteredAt, &rec.LastDeliveredAt, &rec.DeadAt, &rec.DeadReason); err != nil {
			return nil, fmt.Errorf("scan device_tokens row: %w", err)
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query device_tokens: %w", err)
	}
	return records, nil
}

// loadLiveDeviceTokens returns every non-revoked, non-dead token for one
// provider — the fan-out set for a push channel using that provider.
//
// Note this is not scoped by operator: v1 is single-operator by design
// (02-requirements.md's roles matrix), alert_channels is likewise a global
// table, and a push channel is an operator-wide destination exactly as a
// webhook channel is. device_tokens.operator_id exists so the registration
// API can be per-account without a schema change if that ever stops being
// true — the same "model the table properly, don't build the feature"
// discipline 04-data-model.md applies to the operators table itself.
func loadLiveDeviceTokens(ctx context.Context, pool *pgxpool.Pool, provider string) ([]liveDeviceToken, error) {
	const stmt = `
SELECT id::text, token FROM device_tokens
WHERE provider = $1 AND revoked_at IS NULL AND dead_at IS NULL
ORDER BY last_registered_at DESC`

	rows, err := pool.Query(ctx, stmt, provider)
	if err != nil {
		return nil, fmt.Errorf("query live device_tokens: %w", err)
	}
	defer rows.Close()

	var tokens []liveDeviceToken
	for rows.Next() {
		var tok liveDeviceToken
		if err := rows.Scan(&tok.id, &tok.token); err != nil {
			return nil, fmt.Errorf("scan live device_tokens row: %w", err)
		}
		tokens = append(tokens, tok)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query live device_tokens: %w", err)
	}
	return tokens, nil
}

// markDeviceTokenDead records a provider's permanent rejection of a token.
// reason comes from pushprovider's fixed vocabulary (never a raw provider
// response body and never the token itself), so it is safe to persist and
// to show back to an operator through ListDeviceTokens.
func markDeviceTokenDead(ctx context.Context, pool *pgxpool.Pool, id, reason string) error {
	const stmt = `
UPDATE device_tokens SET dead_at = now(), dead_reason = $2
WHERE id = $1::uuid AND dead_at IS NULL`
	if _, err := pool.Exec(ctx, stmt, id, reason); err != nil {
		return fmt.Errorf("mark device token dead: %w", err)
	}
	return nil
}

// markDeviceTokenDelivered records a confirmed delivery. Best-effort by
// design: a failure here must never turn a notification the provider
// genuinely accepted into a reported failure, so callers log and continue.
func markDeviceTokenDelivered(ctx context.Context, pool *pgxpool.Pool, id string) error {
	if _, err := pool.Exec(ctx, `UPDATE device_tokens SET last_delivered_at = now() WHERE id = $1::uuid`, id); err != nil {
		return fmt.Errorf("mark device token delivered: %w", err)
	}
	return nil
}
