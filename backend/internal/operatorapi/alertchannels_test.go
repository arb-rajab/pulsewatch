package operatorapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAlertChannels_FullLifecycle_ThroughRealGatedHTTP(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-channels-crud@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	createBody := []byte(`{"type":"webhook","destination":"https://hooks.invalid/real-secret-token"}`)
	wCreate := doRequest(t, r, http.MethodPost, "/api/v1/alert-channels", cookie, createBody)
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", wCreate.Code, wCreate.Body.String())
	}
	// FR-023: the create response body itself must never carry the
	// destination back, structurally (no field exists for it) — a raw
	// string search is the cheapest real proof the wire body genuinely
	// has no such field, not just that the Go struct doesn't expose one.
	if strings.Contains(wCreate.Body.String(), "real-secret-token") {
		t.Fatal("the create response must never echo the destination back")
	}

	var created alertChannelResponse
	if err := json.Unmarshal(wCreate.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(t.Context(), `DELETE FROM alert_channels WHERE id = $1::uuid`, created.ID)
	})

	// The destination really is encrypted at rest, not stored in plaintext.
	var storedEncrypted string
	if err := pool.QueryRow(t.Context(), `SELECT destination_encrypted FROM alert_channels WHERE id = $1::uuid`, created.ID).Scan(&storedEncrypted); err != nil {
		t.Fatalf("read destination_encrypted: %v", err)
	}
	if strings.Contains(storedEncrypted, "real-secret-token") {
		t.Fatal("destination_encrypted must not contain the plaintext destination")
	}

	// Get and list also never carry it.
	wGet := doRequest(t, r, http.MethodGet, "/api/v1/alert-channels/"+created.ID, cookie, nil)
	if wGet.Code != http.StatusOK || strings.Contains(wGet.Body.String(), "real-secret-token") {
		t.Fatalf("GET must succeed (200) and never leak the destination: code=%d body=%s", wGet.Code, wGet.Body.String())
	}
	wList := doRequest(t, r, http.MethodGet, "/api/v1/alert-channels", cookie, nil)
	if wList.Code != http.StatusOK || strings.Contains(wList.Body.String(), "real-secret-token") {
		t.Fatalf("LIST must succeed (200) and never leak the destination: code=%d body=%s", wList.Code, wList.Body.String())
	}

	// Rotate the secret — 204, no body.
	rotateBody := []byte(`{"destination":"https://hooks.invalid/rotated-secret-token"}`)
	wRotate := doRequest(t, r, http.MethodPut, "/api/v1/alert-channels/"+created.ID+"/secret", cookie, rotateBody)
	if wRotate.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", wRotate.Code, wRotate.Body.String())
	}
	if wRotate.Body.Len() != 0 {
		t.Fatalf("expected an empty body on secret rotation, got %q", wRotate.Body.String())
	}

	var rotatedEncrypted string
	if err := pool.QueryRow(t.Context(), `SELECT destination_encrypted FROM alert_channels WHERE id = $1::uuid`, created.ID).Scan(&rotatedEncrypted); err != nil {
		t.Fatalf("read rotated destination_encrypted: %v", err)
	}
	if rotatedEncrypted == storedEncrypted {
		t.Fatal("expected the encrypted destination to change after rotation")
	}

	// Delete.
	wDelete := doRequest(t, r, http.MethodDelete, "/api/v1/alert-channels/"+created.ID, cookie, nil)
	if wDelete.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", wDelete.Code, wDelete.Body.String())
	}
	wGetAfterDelete := doRequest(t, r, http.MethodGet, "/api/v1/alert-channels/"+created.ID, cookie, nil)
	if wGetAfterDelete.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", wGetAfterDelete.Code)
	}
}

func TestCreateAlertChannel_RejectsInvalidType(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-channels-badtype@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	w := doRequest(t, r, http.MethodPost, "/api/v1/alert-channels", cookie, []byte(`{"type":"carrier-pigeon","destination":"x"}`))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreateAlertChannel_AcceptsAPushChannel proves ADR-0007's channel type
// is genuinely creatable through the same operator API — and that a push
// credential is treated exactly like any other channel secret on the way
// out: the created channel is readable, and nothing in either response can
// carry the credential back.
func TestCreateAlertChannel_AcceptsAPushChannel(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-channels-push@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	const credential = `{"provider":"fcm","project_id":"p","client_email":"e@x.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\nnot-a-real-key\n-----END PRIVATE KEY-----\n"}`
	body := mustJSON(t, map[string]string{"type": "push", "destination": credential})

	w := doRequest(t, r, http.MethodPost, "/api/v1/alert-channels", cookie, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for a push channel, got %d: %s", w.Code, w.Body.String())
	}
	var created alertChannelResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Type != "push" {
		t.Fatalf("expected type=push, got %q", created.Type)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM alert_channels WHERE id = $1::uuid`, created.ID)
	})

	if strings.Contains(w.Body.String(), "PRIVATE KEY") {
		t.Fatalf("the push credential must never be echoed back, got %s", w.Body.String())
	}
	read := doRequest(t, r, http.MethodGet, "/api/v1/alert-channels/"+created.ID, cookie, nil)
	if read.Code != http.StatusOK {
		t.Fatalf("expected 200 reading the push channel back, got %d", read.Code)
	}
	if strings.Contains(read.Body.String(), "PRIVATE KEY") {
		t.Fatalf("the push credential must never be readable, got %s", read.Body.String())
	}
}

// TestCreateAlertChannel_AcceptsAnEmailChannel proves B-014's registration
// side was already real before this session touched anything: "email" has
// been a valid alert_channels.type since migration 000008 (Session 6), and
// CreateAlertChannel has never special-cased it — an email channel's
// destination is a plain recipient address, encrypted at rest exactly like
// a webhook URL, with no schema or endpoint change needed for
// alerting.EmailDispatcher (emaildispatch.go) to have a real row to read.
func TestCreateAlertChannel_AcceptsAnEmailChannel(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-channels-email@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	const recipient = "oncall+real-secret-address@example.invalid"
	body := mustJSON(t, map[string]string{"type": "email", "destination": recipient})

	w := doRequest(t, r, http.MethodPost, "/api/v1/alert-channels", cookie, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for an email channel, got %d: %s", w.Code, w.Body.String())
	}
	var created alertChannelResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Type != "email" {
		t.Fatalf("expected type=email, got %q", created.Type)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM alert_channels WHERE id = $1::uuid`, created.ID)
	})

	if strings.Contains(w.Body.String(), recipient) {
		t.Fatalf("the recipient address must never be echoed back, got %s", w.Body.String())
	}
	read := doRequest(t, r, http.MethodGet, "/api/v1/alert-channels/"+created.ID, cookie, nil)
	if read.Code != http.StatusOK {
		t.Fatalf("expected 200 reading the email channel back, got %d", read.Code)
	}
	if strings.Contains(read.Body.String(), recipient) {
		t.Fatalf("the recipient address must never be readable, got %s", read.Body.String())
	}
}
