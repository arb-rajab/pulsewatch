package operatorapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
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
