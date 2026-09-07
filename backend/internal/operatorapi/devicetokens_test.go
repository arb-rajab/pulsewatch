package operatorapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/arb-rajab/pulsewatch/backend/internal/alerting"
)

// registerDeviceToken drives the real HTTP handler and returns the decoded
// record. Every test here goes through the router (session cookie,
// RequireJSONContentType, the real handler), never the alerting package
// directly — that layer has its own tests.
func registerDeviceToken(t *testing.T, r *gin.Engine, cookie, provider, platform, token string) (int, alerting.DeviceTokenRecord) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"provider": provider, "platform": platform, "token": token})
	if err != nil {
		t.Fatalf("marshal register body: %v", err)
	}
	w := doRequest(t, r, http.MethodPost, "/api/v1/device-tokens", cookie, body)

	var record alerting.DeviceTokenRecord
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &record); err != nil {
			t.Fatalf("decode register response: %v (body %s)", err, w.Body.String())
		}
	}
	return w.Code, record
}

// TestDeviceTokenRoutes_RequireAnOperatorSession proves the mobile app's new
// endpoints are behind the same operatorSession gate as every other
// operator route — ADR-0007 added a client, not a second identity type.
func TestDeviceTokenRoutes_RequireAnOperatorSession(t *testing.T) {
	pool := testPool(t)
	r := testRouter(pool)

	body := []byte(`{"provider":"fcm","platform":"android","token":"t"}`)
	for _, tc := range []struct {
		method, path string
		body         []byte
	}{
		{http.MethodPost, "/api/v1/device-tokens", body},
		{http.MethodGet, "/api/v1/device-tokens", nil},
		{http.MethodDelete, "/api/v1/device-tokens/0f6b6f5e-0000-4000-8000-000000000001", nil},
	} {
		w := doRequest(t, r, tc.method, tc.path, "", tc.body)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: expected 401 without a session, got %d", tc.method, tc.path, w.Code)
		}
	}
}

// TestRegisterDeviceToken_RegistersUpsertsAndNeverEchoesTheToken is the
// happy path the mobile app depends on, plus the re-registration case it
// performs on every launch.
func TestRegisterDeviceToken_RegistersUpsertsAndNeverEchoesTheToken(t *testing.T) {
	pool := testPool(t)
	r := testRouter(pool)
	operatorID := insertTestOperator(t, pool, "device-token-register@example.invalid", "correct-horse-battery-staple")
	cookie := realSessionCookie(t, operatorID)
	token := "fcm-token-api-" + randomHex(t)

	status, record := registerDeviceToken(t, r, cookie, "fcm", "android", token)
	if status != http.StatusOK {
		t.Fatalf("expected 200 (registration is an upsert, not a create), got %d", status)
	}
	if record.ID == "" || record.Provider != "fcm" || record.Platform != "android" {
		t.Fatalf("unexpected record: %+v", record)
	}

	// Re-registering the same token is the normal case and must hit the
	// same row rather than creating a duplicate the fan-out would notify
	// twice.
	statusAgain, again := registerDeviceToken(t, r, cookie, "fcm", "android", token)
	if statusAgain != http.StatusOK {
		t.Fatalf("expected 200 on re-registration, got %d", statusAgain)
	}
	if again.ID != record.ID {
		t.Fatalf("expected re-registration to update the same row, got %s then %s", record.ID, again.ID)
	}

	// The registration response is the one place the token could plausibly
	// be echoed back. It is not.
	w := doRequest(t, r, http.MethodPost, "/api/v1/device-tokens", cookie, mustJSON(t, map[string]string{
		"provider": "fcm", "platform": "android", "token": token,
	}))
	if strings.Contains(w.Body.String(), token) {
		t.Fatalf("the device token must never be echoed back, got body %s", w.Body.String())
	}

	cleanupDeviceToken(t, pool, record.ID)
}

// TestRegisterDeviceToken_ValidatesItsInputs proves a bad registration is a
// useful 422 naming the offending field, not a 503 from a raw constraint
// violation.
func TestRegisterDeviceToken_ValidatesItsInputs(t *testing.T) {
	pool := testPool(t)
	r := testRouter(pool)
	operatorID := insertTestOperator(t, pool, "device-token-validate@example.invalid", "correct-horse-battery-staple")
	cookie := realSessionCookie(t, operatorID)

	cases := []struct {
		name               string
		provider, platform string
		token              string
		wantStatus         int
		wantField          string
	}{
		{"unknown provider", "sms", "android", "t", http.StatusUnprocessableEntity, "provider"},
		{"unknown platform", "fcm", "blackberry", "t", http.StatusUnprocessableEntity, "platform"},
		{"blank token", "fcm", "android", "   ", http.StatusUnprocessableEntity, "token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := mustJSON(t, map[string]string{"provider": tc.provider, "platform": tc.platform, "token": tc.token})
			w := doRequest(t, r, http.MethodPost, "/api/v1/device-tokens", cookie, body)
			if w.Code != tc.wantStatus {
				t.Fatalf("expected %d, got %d (body %s)", tc.wantStatus, w.Code, w.Body.String())
			}
			var envelope errorEnvelope
			if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode error envelope: %v", err)
			}
			if envelope.Error.Field == nil || *envelope.Error.Field != tc.wantField {
				t.Fatalf("expected the error to name field %q, got %+v", tc.wantField, envelope.Error)
			}
		})
	}

	// A missing field is a 400 from binding, before any validation runs.
	w := doRequest(t, r, http.MethodPost, "/api/v1/device-tokens", cookie, []byte(`{"provider":"fcm"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a body missing platform/token, got %d", w.Code)
	}
}

// TestListAndUnregisterDeviceTokens covers the remaining two operations and
// the id-enumeration property the 404 is chosen for.
func TestListAndUnregisterDeviceTokens(t *testing.T) {
	pool := testPool(t)
	r := testRouter(pool)
	operatorID := insertTestOperator(t, pool, "device-token-list@example.invalid", "correct-horse-battery-staple")
	strangerID := insertTestOperator(t, pool, "device-token-stranger@example.invalid", "correct-horse-battery-staple")
	cookie := realSessionCookie(t, operatorID)
	strangerCookie := realSessionCookie(t, strangerID)

	token := "apns-token-api-" + randomHex(t)
	status, record := registerDeviceToken(t, r, cookie, "apns", "ios", token)
	if status != http.StatusOK {
		t.Fatalf("register: expected 200, got %d", status)
	}
	cleanupDeviceToken(t, pool, record.ID)

	// List shows this operator's registration and never the token value.
	w := doRequest(t, r, http.MethodGet, "/api/v1/device-tokens", cookie, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), token) {
		t.Fatalf("list must never return the token value, got %s", w.Body.String())
	}
	var listed []alerting.DeviceTokenRecord
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != record.ID {
		t.Fatalf("expected exactly this operator's registration, got %+v", listed)
	}

	// Another operator's list does not include it, and their delete cannot
	// reach it — a 404, deliberately identical to "no such id".
	strangerList := doRequest(t, r, http.MethodGet, "/api/v1/device-tokens", strangerCookie, nil)
	if got := strangerList.Body.String(); strings.Contains(got, record.ID) {
		t.Fatalf("another operator must not see this registration, got %s", got)
	}
	if w := doRequest(t, r, http.MethodDelete, "/api/v1/device-tokens/"+record.ID, strangerCookie, nil); w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for another operator's device token, got %d", w.Code)
	}

	// The owner's unregister succeeds once, then reports not-found.
	if w := doRequest(t, r, http.MethodDelete, "/api/v1/device-tokens/"+record.ID, cookie, nil); w.Code != http.StatusNoContent {
		t.Fatalf("expected 204 from unregister, got %d", w.Code)
	}
	if w := doRequest(t, r, http.MethodDelete, "/api/v1/device-tokens/"+record.ID, cookie, nil); w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 from a second unregister, got %d", w.Code)
	}

	// The row survives revocation (alert_dispatches history depends on it)
	// and reports itself as revoked rather than vanishing.
	after := doRequest(t, r, http.MethodGet, "/api/v1/device-tokens", cookie, nil)
	if !strings.Contains(after.Body.String(), record.ID) {
		t.Fatal("expected a revoked registration to remain visible to its operator")
	}
}
