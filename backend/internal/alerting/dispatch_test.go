package alerting

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// insertTestAlertChannel creates a real alert_channels row, encrypting
// destination with testEncryptionKey. All assertions in this file scope by
// the returned channel id (never by "how many rows are in the table"),
// since alert_channels is a global table this session's tests don't get an
// isolated view of.
func insertTestAlertChannel(t *testing.T, pool *pgxpool.Pool, channelType, destination string) string {
	t.Helper()

	encrypted, err := EncryptDestination(destination, testEncryptionKey)
	if err != nil {
		t.Fatalf("encrypt test destination: %v", err)
	}

	var channelID string
	err = pool.QueryRow(t.Context(), `
INSERT INTO alert_channels (type, destination_encrypted) VALUES ($1, $2) RETURNING id::text`,
		channelType, encrypted,
	).Scan(&channelID)
	if err != nil {
		t.Fatalf("insert test alert_channel: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Best-effort: alert_dispatches rows referencing this channel (plain
		// REFERENCES, no ON DELETE CASCADE) will block this delete — same
		// accepted looseness as scheduler.insertTestTarget's own cleanup.
		_, _ = pool.Exec(ctx, `DELETE FROM alert_channels WHERE id = $1::uuid`, channelID)
	})

	return channelID
}

// TestLoadChannels_DecryptsConfiguredChannel proves LoadChannels' one real
// job: read a channel's encrypted destination and hand back the correct
// plaintext. Scoped to the specific channel this test created (search the
// result for its id) rather than asserting on the returned slice's length —
// alert_channels is a global table shared with whatever else this session's
// suite leaves behind, so "exactly N rows" is not a safe assertion here.
func TestLoadChannels_DecryptsConfiguredChannel(t *testing.T) {
	pool := testPool(t)
	const destination = "https://hooks.example.invalid/T00/B00/load-channels-test-token"
	channelID := insertTestAlertChannel(t, pool, "webhook", destination)

	channels, err := LoadChannels(t.Context(), pool, testEncryptionKey)
	if err != nil {
		t.Fatalf("LoadChannels: %v", err)
	}

	var found *Channel
	for i := range channels {
		if channels[i].ID == channelID {
			found = &channels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected channel %s among LoadChannels' results (got %d channels total)", channelID, len(channels))
	}
	if found.Type != "webhook" {
		t.Fatalf("expected type=webhook, got %q", found.Type)
	}
	if found.destination != destination {
		t.Fatalf("expected decrypted destination %q, got %q", destination, found.destination)
	}
}

// TestNotifyChannels_DispatchesRecordsDeliveryAndNeverLogsPlaintext is the
// FR-023-against-the-stub proof this session's scope requires: NotifyChannels
// decrypts a real channel's destination (LoadChannels) and hands it to a
// real Dispatcher (LogDispatcher) — but the plaintext destination must never
// appear in anything LogDispatcher writes, even though decrypting it was
// real, not skipped. Also proves the alert_dispatches bookkeeping row is
// written with the right kind/channel/delivery_confirmed.
func TestNotifyChannels_DispatchesRecordsDeliveryAndNeverLogsPlaintext(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)

	const secretDestination = "https://hooks.example.invalid/T00/B00/must-never-appear-in-logs"
	channelID := insertTestAlertChannel(t, pool, "webhook", secretDestination)

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil {
		t.Fatalf("OpenIncident (setup): %v", err)
	}
	if req == nil {
		t.Fatal("OpenIncident (setup): expected a dispatch request")
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	NotifyChannels(t.Context(), pool, NewLogDispatcher(logger), testEncryptionKey, *req, logger)

	if strings.Contains(logBuf.String(), secretDestination) {
		t.Fatalf("FR-023 violation: plaintext destination appeared in dispatch logs:\n%s", logBuf.String())
	}

	var kind string
	var confirmed bool
	err = pool.QueryRow(t.Context(), `
SELECT kind, delivery_confirmed FROM alert_dispatches
WHERE incident_id = $1 AND alert_channel_id = $2::uuid`,
		req.IncidentID, channelID,
	).Scan(&kind, &confirmed)
	if err != nil {
		t.Fatalf("fetch alert_dispatches row for channel %s: %v", channelID, err)
	}
	if kind != "opened" {
		t.Fatalf("expected kind=opened, got %q", kind)
	}
	if !confirmed {
		t.Fatal("expected delivery_confirmed=true: the stub's Dispatch call returned no error")
	}
}
