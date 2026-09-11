package alerting

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests exercise the real PushDispatcher against a real Postgres and
// a mock server speaking the real FCM HTTP v1 protocol — the same shape
// Session 17's webhook tests took (real httptest.Server, real database,
// real retry timing scaled down), extended to the two things push adds:
// a fan-out over many destinations, and a permanent per-destination failure
// that must be recorded rather than retried.
//
// FCM is used rather than APNs for the database-level tests purely because
// it needs no TLS/HTTP2 test server; both providers' protocol handling is
// covered exhaustively in internal/pushprovider's own tests.

var (
	pushKeyOnce sync.Once
	pushRSAKey  *rsa.PrivateKey
)

func testServiceAccountPEM(t *testing.T) string {
	t.Helper()
	pushKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		pushRSAKey = key
	})
	der, err := x509.MarshalPKCS8PrivateKey(pushRSAKey)
	if err != nil {
		t.Fatalf("marshal test service-account key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// mockFCM is a server speaking the real FCM HTTP v1 protocol. sendHandler
// decides each send's outcome; sends records every (attempt, device token)
// pair so a test can assert exactly which devices were reached and how many
// times.
type mockFCM struct {
	*httptest.Server
	mu       sync.Mutex
	sentTo   []string
	handler  func(attempt int, deviceToken string) (status int, body string)
	attempts atomic.Int32
}

func newMockFCM(t *testing.T, handler func(attempt int, deviceToken string) (int, string)) *mockFCM {
	t.Helper()
	m := &mockFCM{handler: handler}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"test-access-token","expires_in":3600}`)
	})
	mux.HandleFunc("/v1/projects/pulsewatch-test/messages:send", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message struct {
				Token string `json:"token"`
			} `json:"message"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		attempt := int(m.attempts.Add(1))
		m.mu.Lock()
		m.sentTo = append(m.sentTo, body.Message.Token)
		m.mu.Unlock()

		status, respBody := m.handler(attempt, body.Message.Token)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	})
	m.Server = httptest.NewServer(mux)
	t.Cleanup(m.Close)
	return m
}

func (m *mockFCM) sendsTo(token string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, sent := range m.sentTo {
		if sent == token {
			n++
		}
	}
	return n
}

func fcmAccepted(int, string) (int, string) {
	return http.StatusOK, `{"name":"projects/pulsewatch-test/messages/1"}`
}

func fcmErrorBody(rpcStatus, errorCode string) string {
	return `{"error":{"code":400,"message":"test","status":"` + rpcStatus + `","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"` + errorCode + `"}]}}`
}

// insertTestPushChannel creates a real "push" alert_channels row whose
// encrypted destination is a real FCM service-account credential pointed at
// srv. Nothing about the channel is special-cased for tests: this is
// exactly the row CreateAlertChannel writes for a real operator.
func insertTestPushChannel(t *testing.T, pool *pgxpool.Pool, srv *mockFCM) string {
	t.Helper()
	credential, err := json.Marshal(map[string]string{
		"provider":       "fcm",
		"project_id":     "pulsewatch-test",
		"client_email":   "pulsewatch@pulsewatch-test.iam.gserviceaccount.com",
		"private_key":    testServiceAccountPEM(t),
		"private_key_id": "test-key-id",
		"token_uri":      srv.URL + "/token",
		"base_url":       srv.URL,
	})
	if err != nil {
		t.Fatalf("marshal test push credential: %v", err)
	}
	return insertTestAlertChannel(t, pool, "push", string(credential))
}

// insertTestOperatorRow creates the operators row device_tokens' FK needs.
// A bare INSERT is used rather than operatorauth.CreateOperator because
// importing operatorauth here would be an import cycle in waiting — this
// package is the one operatorauth's callers depend on, not the reverse.
func insertTestOperatorRow(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var operatorID string
	err := pool.QueryRow(t.Context(), `
INSERT INTO operators (email, password_hash) VALUES ($1, 'not-a-real-hash') RETURNING id::text`,
		"push-test-"+randomSuffix(t)+"@example.invalid",
	).Scan(&operatorID)
	if err != nil {
		t.Fatalf("insert test operator: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// device_tokens has ON DELETE CASCADE (migration 000011), so this
		// cleanup genuinely completes — unlike the pre-existing fixtures
		// B-006 tracks.
		_, _ = pool.Exec(ctx, `DELETE FROM operators WHERE id = $1::uuid`, operatorID)
	})
	return operatorID
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("read random suffix: %v", err)
	}
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 0, 16)
	for _, b := range buf {
		out = append(out, hexDigits[b>>4], hexDigits[b&0x0f])
	}
	return string(out)
}

func insertTestDeviceToken(t *testing.T, pool *pgxpool.Pool, operatorID, provider, platform, token string) string {
	t.Helper()
	record, err := RegisterDeviceToken(t.Context(), pool, operatorID, provider, platform, token)
	if err != nil {
		t.Fatalf("RegisterDeviceToken: %v", err)
	}
	return record.ID
}

func fetchDeviceTokenRow(t *testing.T, pool *pgxpool.Pool, id string) (deadAt *time.Time, deadReason *string, lastDeliveredAt *time.Time) {
	t.Helper()
	err := pool.QueryRow(t.Context(),
		`SELECT dead_at, dead_reason, last_delivered_at FROM device_tokens WHERE id = $1::uuid`, id,
	).Scan(&deadAt, &deadReason, &lastDeliveredAt)
	if err != nil {
		t.Fatalf("fetch device_tokens row %s: %v", id, err)
	}
	return deadAt, deadReason, lastDeliveredAt
}

// fastPushDispatcher is the real PushDispatcher with backoff scaled to
// milliseconds — real retry logic, no real wall-clock seconds.
func fastPushDispatcher(pool *pgxpool.Pool, srv *mockFCM, logger *slog.Logger) *PushDispatcher {
	return &PushDispatcher{
		Pool:              pool,
		Client:            srv.Client(),
		MaxAttempts:       3,
		BackoffBase:       5 * time.Millisecond,
		BackoffMax:        20 * time.Millisecond,
		PerAttemptTimeout: 2 * time.Second,
		Logger:            logger,
	}
}

// pushRouter is what production wires (NewDefaultDispatcher), with the two
// real dispatchers' test-speed backoff. Tests go through NotifyChannels and
// this router rather than calling Dispatch directly, so the alert_dispatches
// write, the channel decryption, and the type routing are all real.
func pushRouter(pool *pgxpool.Pool, srv *mockFCM, logger *slog.Logger) *ChannelRouter {
	return NewChannelRouter(map[string]Dispatcher{
		"webhook": fastDispatcher(),
		"push":    fastPushDispatcher(pool, srv, logger),
	})
}

// TestNotifyChannels_PushDeliversToEveryLiveTokenAndRecordsDispatch is the
// end-to-end success proof: a real push channel, two real registered
// devices, a real FCM protocol exchange for each, one alert_dispatches row
// recording the aggregate, and per-device delivery recorded in
// device_tokens — with neither the credential nor any device token ever
// reaching a log line (FR-023).
func TestNotifyChannels_PushDeliversToEveryLiveTokenAndRecordsDispatch(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)
	operatorID := insertTestOperatorRow(t, pool)

	srv := newMockFCM(t, fcmAccepted)
	channelID := insertTestPushChannel(t, pool, srv)

	phoneToken := "fcm-token-phone-" + randomSuffix(t)
	tabletToken := "fcm-token-tablet-" + randomSuffix(t)
	phoneID := insertTestDeviceToken(t, pool, operatorID, "fcm", "android", phoneToken)
	tabletID := insertTestDeviceToken(t, pool, operatorID, "fcm", "android", tabletToken)

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil {
		t.Fatalf("OpenIncident (setup): %v", err)
	}
	if req == nil {
		t.Fatal("OpenIncident (setup): expected a dispatch request")
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	NotifyChannels(t.Context(), pool, pushRouter(pool, srv, logger), testEncryptionKey, *req, logger)

	if got := srv.sendsTo(phoneToken); got != 1 {
		t.Fatalf("expected exactly 1 send to the phone, got %d", got)
	}
	if got := srv.sendsTo(tabletToken); got != 1 {
		t.Fatalf("expected exactly 1 send to the tablet, got %d", got)
	}

	confirmed, attempts, lastError := fetchDispatchRow(t, pool, req.IncidentID, channelID)
	if !confirmed {
		t.Fatalf("expected delivery_confirmed=true, got last_error %q", lastError)
	}
	if attempts != 2 {
		t.Fatalf("expected attempts=2 (one per live device token), got %d", attempts)
	}
	if lastError != "" {
		t.Fatalf("expected an empty last_error when every device was reached, got %q", lastError)
	}

	for name, id := range map[string]string{"phone": phoneID, "tablet": tabletID} {
		deadAt, _, lastDelivered := fetchDeviceTokenRow(t, pool, id)
		if deadAt != nil {
			t.Fatalf("%s: expected a live token to stay live", name)
		}
		if lastDelivered == nil {
			t.Fatalf("%s: expected last_delivered_at to be recorded", name)
		}
	}

	// FR-023: neither the device tokens nor the provider credential may
	// appear anywhere in the log output.
	logged := logs.String()
	for _, secret := range []string{phoneToken, tabletToken, "BEGIN PRIVATE KEY", "test-access-token"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("FR-023: %q leaked into log output:\n%s", secret, logged)
		}
	}
}

// TestPushDispatcher_DeadTokenIsMarkedNotRetriedAndSkippedNextTime is the
// central behavioural difference from ADR-0006's webhook retry policy: an
// UNREGISTERED token gets exactly one attempt, is recorded dead with the
// provider's own reason, and costs nothing at all on the next incident —
// while the operator's other device is still genuinely notified.
func TestPushDispatcher_DeadTokenIsMarkedNotRetriedAndSkippedNextTime(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)
	operatorID := insertTestOperatorRow(t, pool)

	deadToken := "fcm-token-uninstalled-" + randomSuffix(t)
	liveToken := "fcm-token-live-" + randomSuffix(t)

	srv := newMockFCM(t, func(_ int, deviceToken string) (int, string) {
		if deviceToken == deadToken {
			return http.StatusNotFound, fcmErrorBody("NOT_FOUND", "UNREGISTERED")
		}
		return fcmAccepted(0, deviceToken)
	})
	channelID := insertTestPushChannel(t, pool, srv)

	deadID := insertTestDeviceToken(t, pool, operatorID, "fcm", "android", deadToken)
	insertTestDeviceToken(t, pool, operatorID, "fcm", "ios", liveToken)

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil || req == nil {
		t.Fatalf("OpenIncident (setup): %v (req=%v)", err, req)
	}
	NotifyChannels(t.Context(), pool, pushRouter(pool, srv, logger), testEncryptionKey, *req, logger)

	// 1. Exactly one attempt for the dead token — never the 3 a transient
	//    failure would have earned.
	if got := srv.sendsTo(deadToken); got != 1 {
		t.Fatalf("expected exactly 1 attempt for an UNREGISTERED token (never retried), got %d", got)
	}

	// 2. The token is recorded dead, with the provider's own reason.
	deadAt, deadReason, _ := fetchDeviceTokenRow(t, pool, deadID)
	if deadAt == nil {
		t.Fatal("expected the UNREGISTERED token to be marked dead")
	}
	if deadReason == nil || !strings.Contains(*deadReason, "UNREGISTERED") {
		t.Fatalf("expected dead_reason to name the provider's rejection, got %v", deadReason)
	}
	if deadReason != nil && strings.Contains(*deadReason, deadToken) {
		t.Fatal("FR-023: dead_reason must never contain the device token itself")
	}

	// 3. The dispatch is still confirmed — the operator's other device was
	//    genuinely notified — but the shortfall is recorded, not hidden.
	confirmed, attempts, lastError := fetchDispatchRow(t, pool, req.IncidentID, channelID)
	if !confirmed {
		t.Fatalf("expected delivery_confirmed=true: one live device was reached (last_error %q)", lastError)
	}
	if attempts != 2 {
		t.Fatalf("expected attempts=2 (one per token, no retry for the dead one), got %d", attempts)
	}
	if !strings.Contains(lastError, "delivered to 1 of 2") || !strings.Contains(lastError, "1 marked dead") {
		t.Fatalf("expected last_error to record the partial delivery, got %q", lastError)
	}

	// 4. The next incident does not pay for the dead token at all.
	sendsBefore := srv.attempts.Load()
	if _, err := CloseIncident(t.Context(), pool, targetID); err != nil {
		t.Fatalf("CloseIncident (setup): %v", err)
	}
	secondReq, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil || secondReq == nil {
		t.Fatalf("OpenIncident (second): %v (req=%v)", err, secondReq)
	}
	NotifyChannels(t.Context(), pool, pushRouter(pool, srv, logger), testEncryptionKey, *secondReq, logger)

	if got := srv.attempts.Load() - sendsBefore; got != 1 {
		t.Fatalf("expected the second incident to cost exactly 1 send (the dead token excluded), got %d", got)
	}
	if got := srv.sendsTo(deadToken); got != 1 {
		t.Fatalf("expected no further attempts against the dead token, got %d total", got)
	}
	_, secondAttempts, secondLastError := fetchDispatchRow(t, pool, secondReq.IncidentID, channelID)
	if secondAttempts != 1 {
		t.Fatalf("expected attempts=1 on the second incident, got %d", secondAttempts)
	}
	if secondLastError != "" {
		t.Fatalf("expected a clean last_error once the dead token is out of the fan-out, got %q", secondLastError)
	}
}

// TestPushDispatcher_RetriesTransientFailureThenSucceeds proves the retry
// half of the policy is real and reaches a real success — the same property
// ADR-0006 proved for webhooks, re-proved here because the retry loop is a
// different one.
func TestPushDispatcher_RetriesTransientFailureThenSucceeds(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)
	operatorID := insertTestOperatorRow(t, pool)

	srv := newMockFCM(t, func(attempt int, deviceToken string) (int, string) {
		if attempt <= 2 {
			return http.StatusServiceUnavailable, fcmErrorBody("UNAVAILABLE", "UNAVAILABLE")
		}
		return fcmAccepted(attempt, deviceToken)
	})
	channelID := insertTestPushChannel(t, pool, srv)

	token := "fcm-token-flaky-" + randomSuffix(t)
	tokenID := insertTestDeviceToken(t, pool, operatorID, "fcm", "android", token)

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil || req == nil {
		t.Fatalf("OpenIncident (setup): %v (req=%v)", err, req)
	}

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	NotifyChannels(t.Context(), pool, pushRouter(pool, srv, logger), testEncryptionKey, *req, logger)

	if got := srv.sendsTo(token); got != 3 {
		t.Fatalf("expected 3 real attempts (two UNAVAILABLE, then a success), got %d", got)
	}
	confirmed, attempts, lastError := fetchDispatchRow(t, pool, req.IncidentID, channelID)
	if !confirmed {
		t.Fatalf("expected delivery_confirmed=true after a successful retry, got last_error %q", lastError)
	}
	if attempts != 3 {
		t.Fatalf("expected attempts=3, got %d", attempts)
	}
	if lastError != "" {
		t.Fatalf("expected an empty last_error on an eventually-successful delivery, got %q", lastError)
	}
	if deadAt, _, lastDelivered := fetchDeviceTokenRow(t, pool, tokenID); deadAt != nil || lastDelivered == nil {
		t.Fatal("expected a transiently-failing token to stay live and record its eventual delivery")
	}
}

// TestPushDispatcher_ExhaustedTransientFailureIsUnconfirmedAndTokenStaysLive
// proves the other half of that distinction: a provider outage must not be
// mistaken for a dead device.
func TestPushDispatcher_ExhaustedTransientFailureIsUnconfirmedAndTokenStaysLive(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)
	operatorID := insertTestOperatorRow(t, pool)

	srv := newMockFCM(t, func(int, string) (int, string) {
		return http.StatusServiceUnavailable, fcmErrorBody("UNAVAILABLE", "UNAVAILABLE")
	})
	channelID := insertTestPushChannel(t, pool, srv)

	token := "fcm-token-outage-" + randomSuffix(t)
	tokenID := insertTestDeviceToken(t, pool, operatorID, "fcm", "android", token)

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil || req == nil {
		t.Fatalf("OpenIncident (setup): %v (req=%v)", err, req)
	}

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	NotifyChannels(t.Context(), pool, pushRouter(pool, srv, logger), testEncryptionKey, *req, logger)

	if got := srv.sendsTo(token); got != 3 {
		t.Fatalf("expected MaxAttempts=3 attempts against a persistently unavailable provider, got %d", got)
	}
	confirmed, attempts, lastError := fetchDispatchRow(t, pool, req.IncidentID, channelID)
	if confirmed {
		t.Fatal("expected delivery_confirmed=false when no device was reached")
	}
	if attempts != 3 {
		t.Fatalf("expected attempts=3, got %d", attempts)
	}
	if !strings.Contains(lastError, "delivered to 0 of 1") || !strings.Contains(lastError, "UNAVAILABLE") {
		t.Fatalf("expected last_error to record the failure and its provider reason, got %q", lastError)
	}
	if deadAt, _, _ := fetchDeviceTokenRow(t, pool, tokenID); deadAt != nil {
		t.Fatal("a provider outage must never mark a device token dead — that would silently mute a working phone")
	}
}

// TestPushDispatcher_CredentialRejectionAbortsFanOutWithoutKillingTokens
// proves an expired or wrong service-account key is diagnosed once, not
// repeated per registered device, and never blamed on the devices.
func TestPushDispatcher_CredentialRejectionAbortsFanOutWithoutKillingTokens(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)
	operatorID := insertTestOperatorRow(t, pool)

	srv := newMockFCM(t, func(int, string) (int, string) {
		return http.StatusUnauthorized, fcmErrorBody("UNAUTHENTICATED", "THIRD_PARTY_AUTH_ERROR")
	})
	channelID := insertTestPushChannel(t, pool, srv)

	var ids []string
	for i := 0; i < 3; i++ {
		ids = append(ids, insertTestDeviceToken(t, pool, operatorID, "fcm", "android", "fcm-token-cred-"+randomSuffix(t)))
	}

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil || req == nil {
		t.Fatalf("OpenIncident (setup): %v (req=%v)", err, req)
	}

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	NotifyChannels(t.Context(), pool, pushRouter(pool, srv, logger), testEncryptionKey, *req, logger)

	if got := srv.attempts.Load(); got != 1 {
		t.Fatalf("expected the fan-out to stop after the first credential rejection, got %d attempts across 3 devices", got)
	}
	confirmed, _, lastError := fetchDispatchRow(t, pool, req.IncidentID, channelID)
	if confirmed {
		t.Fatal("expected delivery_confirmed=false")
	}
	if !strings.Contains(lastError, "not attempted after a credential rejection") {
		t.Fatalf("expected last_error to say the remaining devices were skipped, got %q", lastError)
	}
	for _, id := range ids {
		if deadAt, _, _ := fetchDeviceTokenRow(t, pool, id); deadAt != nil {
			t.Fatal("a credential rejection must never mark a device token dead")
		}
	}
}

// TestPushDispatcher_NoLiveTokensIsHonestlyUnconfirmed proves the
// silent-success failure mode ADR-0006 avoided for email is avoided here
// too: a push channel with no registered device notified nobody, and says so.
func TestPushDispatcher_NoLiveTokensIsHonestlyUnconfirmed(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)
	operatorID := insertTestOperatorRow(t, pool)

	srv := newMockFCM(t, fcmAccepted)
	channelID := insertTestPushChannel(t, pool, srv)

	// A registered-then-unregistered device must not be notified.
	tokenID := insertTestDeviceToken(t, pool, operatorID, "fcm", "ios", "fcm-token-signed-out-"+randomSuffix(t))
	if err := RevokeDeviceToken(t.Context(), pool, operatorID, tokenID); err != nil {
		t.Fatalf("RevokeDeviceToken: %v", err)
	}

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil || req == nil {
		t.Fatalf("OpenIncident (setup): %v (req=%v)", err, req)
	}

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	NotifyChannels(t.Context(), pool, pushRouter(pool, srv, logger), testEncryptionKey, *req, logger)

	if got := srv.attempts.Load(); got != 0 {
		t.Fatalf("expected no provider calls with no live tokens, got %d", got)
	}
	confirmed, attempts, lastError := fetchDispatchRow(t, pool, req.IncidentID, channelID)
	if confirmed {
		t.Fatal("expected delivery_confirmed=false: nobody was notified")
	}
	if attempts != 0 {
		t.Fatalf("expected attempts=0, got %d", attempts)
	}
	if !strings.Contains(lastError, "no live device tokens") {
		t.Fatalf("expected last_error to say there were no live device tokens, got %q", lastError)
	}
}

// TestPushDispatcher_UnusableCredentialIsRecordedNotRetried proves a
// malformed push channel produces an honest, actionable alert_dispatches
// row rather than a stack trace or a silent skip.
func TestPushDispatcher_UnusableCredentialIsRecordedNotRetried(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)

	channelID := insertTestAlertChannel(t, pool, "push", `{"provider":"carrier-pigeon"}`)

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil || req == nil {
		t.Fatalf("OpenIncident (setup): %v (req=%v)", err, req)
	}

	srv := newMockFCM(t, fcmAccepted)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	NotifyChannels(t.Context(), pool, pushRouter(pool, srv, logger), testEncryptionKey, *req, logger)

	confirmed, _, lastError := fetchDispatchRow(t, pool, req.IncidentID, channelID)
	if confirmed {
		t.Fatal("expected delivery_confirmed=false for an unusable credential")
	}
	if !strings.Contains(lastError, "credential unusable") {
		t.Fatalf("expected last_error to name the credential as the problem, got %q", lastError)
	}
}

// fullRouter wires all three real dispatchers (webhook, push, email) at
// test-speed backoff — what NewDefaultDispatcher wires in production as of
// B-014, now that email is a third real implementation rather than the
// permanently-"not implemented" case ADR-0006/ADR-0007 originally routed
// through.
func fullRouter(pool *pgxpool.Pool, pushSrv *mockFCM, emailSrv *fakeSMTPServer, logger *slog.Logger) *ChannelRouter {
	return NewChannelRouter(map[string]Dispatcher{
		"webhook": fastDispatcher(),
		"push":    fastPushDispatcher(pool, pushSrv, logger),
		"email":   fastEmailDispatcher(emailSrv),
	})
}

// TestChannelRouter_RoutesEachTypeToItsRealDispatcher proves ADR-0007's
// structural change to ADR-0006's dispatch path, completed by B-014: every
// channel type alert_channels' own CHECK constraint allows now has a real
// dispatcher, each channel reaches the right one in the same NotifyChannels
// call, and every one is genuinely delivered — not just routed.
func TestChannelRouter_RoutesEachTypeToItsRealDispatcher(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)
	operatorID := insertTestOperatorRow(t, pool)

	var webhookCalls atomic.Int32
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		webhookCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()

	pushSrv := newMockFCM(t, fcmAccepted)
	emailSrv := newFakeSMTPServer(t)
	emailSrv.start()
	emailRecipient := "ops-router-test-" + randomSuffix(t) + "@example.invalid"

	webhookChannelID := insertTestAlertChannel(t, pool, "webhook", webhookSrv.URL)
	pushChannelID := insertTestPushChannel(t, pool, pushSrv)
	emailChannelID := insertTestAlertChannel(t, pool, "email", emailRecipient)
	insertTestDeviceToken(t, pool, operatorID, "fcm", "android", "fcm-token-router-"+randomSuffix(t))

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil || req == nil {
		t.Fatalf("OpenIncident (setup): %v (req=%v)", err, req)
	}

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	NotifyChannels(t.Context(), pool, fullRouter(pool, pushSrv, emailSrv, logger), testEncryptionKey, *req, logger)

	if got := webhookCalls.Load(); got != 1 {
		t.Fatalf("expected the webhook channel to reach the webhook dispatcher exactly once, got %d", got)
	}
	if got := pushSrv.attempts.Load(); got != 1 {
		t.Fatalf("expected the push channel to reach the push dispatcher exactly once, got %d", got)
	}
	if got := emailSrv.attemptsFor(emailRecipient); got != 1 {
		t.Fatalf("expected the email channel to reach the email dispatcher exactly once, got %d", got)
	}

	if confirmed, _, _ := fetchDispatchRow(t, pool, req.IncidentID, webhookChannelID); !confirmed {
		t.Fatal("expected the webhook dispatch to be confirmed")
	}
	if confirmed, _, _ := fetchDispatchRow(t, pool, req.IncidentID, pushChannelID); !confirmed {
		t.Fatal("expected the push dispatch to be confirmed")
	}
	if confirmed, _, lastError := fetchDispatchRow(t, pool, req.IncidentID, emailChannelID); !confirmed {
		t.Fatalf("expected the email dispatch to be confirmed (last_error=%q)", lastError)
	}
}

// TestChannelRouter_UnknownTypeIsReportedNotImplemented proves the router's
// own defensive fallback still works for a channel type with no registered
// dispatcher. There is no way to produce this through a real row today
// (alert_channels' CHECK constraint only allows webhook/email/push, and all
// three have a real dispatcher as of B-014) — this calls Dispatch directly
// with a Channel the database could never hand back, the same "a mis-wired
// router should produce an honest unconfirmed outcome" guarantee
// ADR-0007 documents.
func TestChannelRouter_UnknownTypeIsReportedNotImplemented(t *testing.T) {
	router := NewChannelRouter(map[string]Dispatcher{
		"webhook": fastDispatcher(),
	})
	outcome := router.Dispatch(t.Context(), Channel{ID: "unknown-channel", Type: "sms"}, DispatchRequest{IncidentID: 1, TargetID: "t", Kind: "opened"})
	if outcome.Confirmed {
		t.Fatal("expected an unconfirmed outcome for a type with no registered dispatcher")
	}
	if !strings.Contains(outcome.LastError, "not implemented") {
		t.Fatalf("expected last_error to say the type is not implemented, got %q", outcome.LastError)
	}
}
