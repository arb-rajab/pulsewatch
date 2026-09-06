package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

// fetchDispatchRow reads back the one alert_dispatches row this test's
// channel/incident pair produced, for asserting on delivery_confirmed/
// attempts/last_error (ADR-0006).
func fetchDispatchRow(t *testing.T, pool *pgxpool.Pool, incidentID int64, channelID string) (confirmed bool, attempts int, lastError string) {
	t.Helper()
	var lastErr *string
	err := pool.QueryRow(t.Context(), `
SELECT delivery_confirmed, attempts, last_error FROM alert_dispatches
WHERE incident_id = $1 AND alert_channel_id = $2::uuid`,
		incidentID, channelID,
	).Scan(&confirmed, &attempts, &lastErr)
	if err != nil {
		t.Fatalf("fetch alert_dispatches row for channel %s: %v", channelID, err)
	}
	if lastErr != nil {
		lastError = *lastErr
	}
	return confirmed, attempts, lastError
}

// TestLoadChannels_DecryptsConfiguredChannel proves LoadChannels' one real
// job: read a channel's encrypted destination and hand back the correct
// plaintext.
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

// fastDispatcher returns a WebhookDispatcher tuned for tests: real retry
// logic, but backoff scaled down to milliseconds so retry tests don't spend
// real wall-clock seconds waiting out ADR-0006's production backoff.
func fastDispatcher() *WebhookDispatcher {
	return &WebhookDispatcher{
		Client:            &http.Client{},
		MaxAttempts:       3,
		BackoffBase:       5 * time.Millisecond,
		BackoffMax:        20 * time.Millisecond,
		PerAttemptTimeout: 2 * time.Second,
	}
}

// TestNotifyChannels_WebhookDeliversRecordsAttemptAndNeverLogsDestination is
// the FR-023-plus-ADR-0006 proof this session's real webhook delivery
// requires: NotifyChannels decrypts a real channel's destination
// (LoadChannels), a real WebhookDispatcher POSTs to it (a real
// httptest.Server, not a fake), the server's 200 is what makes
// delivery_confirmed true — but the destination itself (here, the server's
// own URL, standing in for a real webhook URL that might embed a bearer
// token) must never appear in any log line NotifyChannels or the
// dispatcher produce.
func TestNotifyChannels_WebhookDeliversRecordsAttemptAndNeverLogsDestination(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)

	var received webhookPayload
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	channelID := insertTestAlertChannel(t, pool, "webhook", srv.URL)

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil {
		t.Fatalf("OpenIncident (setup): %v", err)
	}
	if req == nil {
		t.Fatal("OpenIncident (setup): expected a dispatch request")
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	NotifyChannels(t.Context(), pool, fastDispatcher(), testEncryptionKey, *req, logger)

	if strings.Contains(logBuf.String(), srv.URL) {
		t.Fatalf("FR-023 violation: channel destination appeared in dispatch logs:\n%s", logBuf.String())
	}

	if gotContentType != "application/json" {
		t.Fatalf("expected Content-Type: application/json, got %q", gotContentType)
	}
	if received.Kind != "opened" || received.IncidentID != req.IncidentID || received.TargetID != targetID {
		t.Fatalf("unexpected webhook payload received by test server: %+v", received)
	}

	confirmed, attempts, lastError := fetchDispatchRow(t, pool, req.IncidentID, channelID)
	if !confirmed {
		t.Fatalf("expected delivery_confirmed=true: the test server returned 200 (last_error=%q)", lastError)
	}
	if attempts != 1 {
		t.Fatalf("expected attempts=1 for an immediate success, got %d", attempts)
	}
	if lastError != "" {
		t.Fatalf("expected no last_error on a confirmed delivery, got %q", lastError)
	}
}

// TestWebhookDispatcher_RetriesTransientFailureThenSucceeds proves the
// actual retry/backoff behavior ADR-0006 commits to: a channel whose
// endpoint fails twice (a real 500 response, not a simulated error) then
// succeeds on the third attempt is recorded as confirmed, with attempts=3 —
// the retry genuinely happened, driven through NotifyChannels end to end,
// not asserted against WebhookDispatcher.Dispatch in isolation.
func TestWebhookDispatcher_RetriesTransientFailureThenSucceeds(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	channelID := insertTestAlertChannel(t, pool, "webhook", srv.URL)

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil {
		t.Fatalf("OpenIncident (setup): %v", err)
	}
	if req == nil {
		t.Fatal("OpenIncident (setup): expected a dispatch request")
	}

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	NotifyChannels(t.Context(), pool, fastDispatcher(), testEncryptionKey, *req, logger)

	if got := calls.Load(); got != 3 {
		t.Fatalf("expected exactly 3 HTTP attempts against the test server, got %d", got)
	}

	confirmed, attempts, lastError := fetchDispatchRow(t, pool, req.IncidentID, channelID)
	if !confirmed {
		t.Fatalf("expected delivery_confirmed=true after the 3rd attempt succeeded (last_error=%q)", lastError)
	}
	if attempts != 3 {
		t.Fatalf("expected attempts=3 (2 failures then a success), got %d", attempts)
	}
}

// TestWebhookDispatcher_PermanentFailureRecordsUnconfirmedWithError proves
// the give-up path: an endpoint that always fails exhausts every retry,
// alert_dispatches ends up with delivery_confirmed=false, attempts equal to
// the configured max, and a non-empty last_error describing the HTTP status
// — never the destination itself (FR-023).
func TestWebhookDispatcher_PermanentFailureRecordsUnconfirmedWithError(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	channelID := insertTestAlertChannel(t, pool, "webhook", srv.URL)

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil {
		t.Fatalf("OpenIncident (setup): %v", err)
	}
	if req == nil {
		t.Fatal("OpenIncident (setup): expected a dispatch request")
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	dispatcher := fastDispatcher()
	NotifyChannels(t.Context(), pool, dispatcher, testEncryptionKey, *req, logger)

	confirmed, attempts, lastError := fetchDispatchRow(t, pool, req.IncidentID, channelID)
	if confirmed {
		t.Fatal("expected delivery_confirmed=false: every attempt returned 500")
	}
	if attempts != dispatcher.MaxAttempts {
		t.Fatalf("expected attempts=%d (every configured attempt exhausted), got %d", dispatcher.MaxAttempts, attempts)
	}
	if lastError == "" {
		t.Fatal("expected a non-empty last_error describing the failure")
	}
	if strings.Contains(lastError, srv.URL) {
		t.Fatalf("FR-023 violation: destination leaked into last_error: %q", lastError)
	}
	if strings.Contains(logBuf.String(), srv.URL) {
		t.Fatalf("FR-023 violation: destination leaked into logs:\n%s", logBuf.String())
	}
}

// TestWebhookDispatcher_NonRetryableStatusStopsImmediately proves that a
// 4xx (other than 429) is treated as a permanent rejection, not retried —
// attempts stays at 1 rather than climbing to MaxAttempts, and the server
// only ever sees one request.
func TestWebhookDispatcher_NonRetryableStatusStopsImmediately(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	channelID := insertTestAlertChannel(t, pool, "webhook", srv.URL)

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil {
		t.Fatalf("OpenIncident (setup): %v", err)
	}
	if req == nil {
		t.Fatal("OpenIncident (setup): expected a dispatch request")
	}

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	NotifyChannels(t.Context(), pool, fastDispatcher(), testEncryptionKey, *req, logger)

	if got := calls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 HTTP attempt for a non-retryable 400, got %d", got)
	}

	confirmed, attempts, lastError := fetchDispatchRow(t, pool, req.IncidentID, channelID)
	if confirmed {
		t.Fatal("expected delivery_confirmed=false for a 400 response")
	}
	if attempts != 1 {
		t.Fatalf("expected attempts=1 (no retry for a non-retryable status), got %d", attempts)
	}
	if lastError == "" {
		t.Fatal("expected a non-empty last_error")
	}
}

// TestNotifyChannels_EmailChannelReportedNotImplemented proves ADR-0006's
// explicit scope boundary: an "email" channel is never silently dropped or
// falsely confirmed — it comes back unconfirmed with a last_error saying
// plainly that email delivery isn't built this session.
func TestNotifyChannels_EmailChannelReportedNotImplemented(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)
	channelID := insertTestAlertChannel(t, pool, "email", "ops@example.invalid")

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil {
		t.Fatalf("OpenIncident (setup): %v", err)
	}
	if req == nil {
		t.Fatal("OpenIncident (setup): expected a dispatch request")
	}

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	NotifyChannels(t.Context(), pool, fastDispatcher(), testEncryptionKey, *req, logger)

	confirmed, attempts, lastError := fetchDispatchRow(t, pool, req.IncidentID, channelID)
	if confirmed {
		t.Fatal("expected delivery_confirmed=false: email delivery is not implemented this session")
	}
	if attempts != 1 {
		t.Fatalf("expected attempts=1 (not implemented is not retried), got %d", attempts)
	}
	if !strings.Contains(lastError, "not implemented") {
		t.Fatalf("expected last_error to say email is not implemented, got %q", lastError)
	}
}
