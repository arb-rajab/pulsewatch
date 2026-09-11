package pushprovider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// apnsServer is a mock that speaks the *real* APNs provider protocol,
// including its transport: HTTP/2 over TLS. httptest's EnableHTTP2 is what
// makes that real rather than asserted — TestAPNsClient_SendUsesHTTP2 below
// fails outright if this client ever falls back to HTTP/1.1, which a real
// APNs endpoint refuses.
//
// A real APNs endpoint cannot be exercised from this repo's CI or from the
// sandbox this channel was built in: it needs an Apple Developer account,
// a .p8 signing key, and a registered bundle id. See ADR-0007's "What is
// and isn't verified".
type apnsServer struct {
	*httptest.Server
	requests atomic.Int32

	lastPath          atomic.Value // string
	lastAuthorization atomic.Value // string
	lastTopic         atomic.Value // string
	lastPushType      atomic.Value // string
	lastPriority      atomic.Value // string
	lastCollapseID    atomic.Value // string
	lastProto         atomic.Int32
	lastPayload       atomic.Value // map[string]any

	handler func(attempt int32) (status int, body string)
}

func newAPNsServer(t *testing.T, handler func(attempt int32) (int, string)) *apnsServer {
	t.Helper()
	s := &apnsServer{handler: handler}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := s.requests.Add(1)
		s.lastPath.Store(r.URL.Path)
		s.lastAuthorization.Store(r.Header.Get("Authorization"))
		s.lastTopic.Store(r.Header.Get("apns-topic"))
		s.lastPushType.Store(r.Header.Get("apns-push-type"))
		s.lastPriority.Store(r.Header.Get("apns-priority"))
		s.lastCollapseID.Store(r.Header.Get("apns-collapse-id"))
		s.lastProto.Store(int32(r.ProtoMajor))

		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		s.lastPayload.Store(payload)

		status, body := s.handler(attempt)
		w.WriteHeader(status)
		if body != "" {
			_, _ = io.WriteString(w, body)
		}
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	s.Server = srv
	t.Cleanup(srv.Close)
	return s
}

func newTestAPNsClient(t *testing.T, srv *apnsServer) *APNsClient {
	t.Helper()
	_, ecKey := testKeys(t)
	client, err := NewAPNsClient(APNsCredential{
		TeamID:     "TEAM123456",
		KeyID:      "KEY1234567",
		PrivateKey: pkcs8PEM(t, ecKey),
		Topic:      "com.example.pulsewatch",
		BaseURL:    srv.URL,
	}, srv.Client())
	if err != nil {
		t.Fatalf("NewAPNsClient: %v", err)
	}
	return client
}

func apnsAccept(int32) (int, string) { return http.StatusOK, "" }

// TestAPNsClient_SendSignsRealProviderTokenAndPostsRealRequest is the
// protocol-level proof: a genuine ES256 provider token Apple could verify,
// on the real /3/device/{token} path, with the headers APNs requires and
// the payload shape it defines (custom keys as siblings of "aps", not
// nested inside it).
func TestAPNsClient_SendSignsRealProviderTokenAndPostsRealRequest(t *testing.T) {
	srv := newAPNsServer(t, apnsAccept)
	client := newTestAPNsClient(t, srv)
	_, ecKey := testKeys(t)

	if err := client.Send(context.Background(), "abcdef0123456789", testNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got, _ := srv.lastPath.Load().(string); got != "/3/device/abcdef0123456789" {
		t.Fatalf("expected APNs' /3/device/{token} path, got %q", got)
	}

	authorization, _ := srv.lastAuthorization.Load().(string)
	if !strings.HasPrefix(authorization, "bearer ") {
		t.Fatalf("expected a lowercase `bearer ` provider token per Apple's docs, got %q", authorization)
	}
	header, claims, signingInput, sig := decodeJWT(t, strings.TrimPrefix(authorization, "bearer "))
	if header["alg"] != "ES256" {
		t.Fatalf("expected alg=ES256, got %v", header["alg"])
	}
	if header["kid"] != "KEY1234567" {
		t.Fatalf("expected kid to be the .p8 key id, got %v", header["kid"])
	}
	if claims["iss"] != "TEAM123456" {
		t.Fatalf("expected iss to be the team id, got %v", claims["iss"])
	}
	if _, ok := claims["iat"]; !ok {
		t.Fatal("expected an iat claim — Apple derives provider-token expiry from it")
	}
	verifyES256(t, &ecKey.PublicKey, signingInput, sig)

	if got, _ := srv.lastTopic.Load().(string); got != "com.example.pulsewatch" {
		t.Fatalf("expected apns-topic to be the bundle id, got %q", got)
	}
	if got, _ := srv.lastPushType.Load().(string); got != "alert" {
		t.Fatalf("expected apns-push-type=alert, got %q", got)
	}
	if got, _ := srv.lastPriority.Load().(string); got != "10" {
		t.Fatalf("expected apns-priority=10 for an outage alert, got %q", got)
	}
	if got, _ := srv.lastCollapseID.Load().(string); got != "opened-42" {
		t.Fatalf("expected apns-collapse-id, got %q", got)
	}

	payload, _ := srv.lastPayload.Load().(map[string]any)
	aps, ok := payload["aps"].(map[string]any)
	if !ok {
		t.Fatalf("expected a reserved `aps` dictionary, got %#v", payload)
	}
	alert, _ := aps["alert"].(map[string]any)
	if alert["title"] != "pulsewatch: incident opened" {
		t.Fatalf("expected the alert title, got %v", alert["title"])
	}
	// The deep-link fields must be siblings of aps — nesting them inside it
	// is the classic APNs payload mistake, and the app would never see them.
	if payload["incident_id"] != "42" || payload["kind"] != "opened" {
		t.Fatalf("expected the deep-link keys alongside `aps`, got %#v", payload)
	}
}

// TestAPNsClient_SendUsesHTTP2 proves the transport, not just the payload:
// APNs' provider API is HTTP/2 only, so an HTTP/1.1 request here would fail
// against the real endpoint no matter how correct its body was.
func TestAPNsClient_SendUsesHTTP2(t *testing.T) {
	srv := newAPNsServer(t, apnsAccept)
	client := newTestAPNsClient(t, srv)

	if err := client.Send(context.Background(), "abcdef0123456789", testNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := srv.lastProto.Load(); got != 2 {
		t.Fatalf("expected the request to be carried over HTTP/2 (APNs speaks nothing else), got HTTP/%d", got)
	}
}

// TestAPNsClient_ProviderTokenIsReusedAcrossSends proves this client does
// not re-sign per notification. Apple rate-limits provider-token
// regeneration (TooManyProviderTokenUpdates) — re-signing per device in a
// fan-out would be a self-inflicted outage.
func TestAPNsClient_ProviderTokenIsReusedAcrossSends(t *testing.T) {
	srv := newAPNsServer(t, apnsAccept)
	client := newTestAPNsClient(t, srv)

	var first string
	for i := 0; i < 3; i++ {
		if err := client.Send(context.Background(), "abcdef0123456789", testNotification()); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		got, _ := srv.lastAuthorization.Load().(string)
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatal("expected the same cached provider token across sends within its lifetime")
		}
	}
}

// TestAPNsClient_ClassifiesRealRejectionReasons is the dead-token proof at
// the APNs layer, over Apple's published reason strings.
func TestAPNsClient_ClassifiesRealRejectionReasons(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		reason   string
		wantKind ErrorKind
	}{
		{"uninstalled app", http.StatusGone, "Unregistered", KindDeadToken},
		{"malformed or wrong-environment token", http.StatusBadRequest, "BadDeviceToken", KindDeadToken},
		{"token belongs to another app", http.StatusBadRequest, "DeviceTokenNotForTopic", KindDeadToken},
		{"our provider token expired", http.StatusForbidden, "ExpiredProviderToken", KindCredential},
		{"our provider token is wrong", http.StatusForbidden, "InvalidProviderToken", KindCredential},
		{"apns rate limited us", http.StatusTooManyRequests, "TooManyRequests", KindTransient},
		{"apns is down", http.StatusServiceUnavailable, "ServiceUnavailable", KindTransient},
		{"apns internal error", http.StatusInternalServerError, "InternalServerError", KindTransient},
		{"payload too large", http.StatusRequestEntityTooLarge, "PayloadTooLarge", KindPermanent},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newAPNsServer(t, func(int32) (int, string) {
				return tc.status, `{"reason":"` + tc.reason + `"}`
			})
			client := newTestAPNsClient(t, srv)

			err := client.Send(context.Background(), "abcdef0123456789", testNotification())
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := KindOf(err); got != tc.wantKind {
				t.Fatalf("expected kind %q for reason %q, got %q (%s)", tc.wantKind, tc.reason, got, err)
			}
			if strings.Contains(err.Error(), "abcdef0123456789") {
				t.Fatalf("FR-023: the device token must never appear in an error reason, got %q", err.Error())
			}
		})
	}
}

// TestAPNsClient_UnrecognisedReasonIsBounded proves a hostile or broken
// endpoint cannot inject arbitrary text into a log line or into
// alert_dispatches.last_error through the reason field.
func TestAPNsClient_UnrecognisedReasonIsBounded(t *testing.T) {
	hostile := `Bad" injected=\"value` + strings.Repeat("A", 300)
	srv := newAPNsServer(t, func(int32) (int, string) {
		body, err := json.Marshal(apnsErrorResponse{Reason: hostile})
		if err != nil {
			t.Errorf("marshal hostile reason: %v", err)
		}
		return http.StatusBadRequest, string(body)
	})
	client := newTestAPNsClient(t, srv)

	err := client.Send(context.Background(), "abcdef0123456789", testNotification())
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(err.Error()) > 128 {
		t.Fatalf("expected the reason to be bounded, got %d characters", len(err.Error()))
	}
	if strings.ContainsAny(err.Error(), `"\=`) {
		t.Fatalf("expected non-alphanumeric characters to be stripped from an unrecognised reason, got %q", err.Error())
	}
}

// TestNewAPNsClient_RejectsBadSigningKeyUpFront proves an unusable .p8 is a
// configuration error found at construction, not a mystery at 3am.
func TestNewAPNsClient_RejectsBadSigningKeyUpFront(t *testing.T) {
	rsaKey, _ := testKeys(t)
	_, err := NewAPNsClient(APNsCredential{
		TeamID: "TEAM123456", KeyID: "KEY1234567", Topic: "com.example.pulsewatch",
		PrivateKey: pkcs8PEM(t, rsaKey), // an RSA key where APNs requires ECDSA P-256
	}, nil)
	if err == nil {
		t.Fatal("expected an RSA key to be rejected as an APNs signing key")
	}
	if _, err := NewAPNsClient(APNsCredential{TeamID: "T"}, nil); err == nil {
		t.Fatal("expected a credential missing key_id/private_key/topic to be rejected")
	}
	if _, err := NewAPNsClient(APNsCredential{
		TeamID: "TEAM123456", KeyID: "KEY1234567", Topic: "com.example.pulsewatch",
		PrivateKey: "-----BEGIN PRIVATE KEY-----\nnot base64\n-----END PRIVATE KEY-----\n",
	}, nil); err == nil {
		t.Fatal("expected an unparseable PEM key to be rejected")
	}
}

// TestAPNsClient_SandboxEnvironmentSelectsSandboxHost proves the one
// configuration value that silently produces BadDeviceToken for every
// development build if it is wrong.
func TestAPNsClient_SandboxEnvironmentSelectsSandboxHost(t *testing.T) {
	_, ecKey := testKeys(t)
	client, err := NewAPNsClient(APNsCredential{
		TeamID: "TEAM123456", KeyID: "KEY1234567", Topic: "com.example.pulsewatch",
		PrivateKey: pkcs8PEM(t, ecKey), Environment: "sandbox",
	}, nil)
	if err != nil {
		t.Fatalf("NewAPNsClient: %v", err)
	}
	if client.baseURL != apnsSandboxBaseURL {
		t.Fatalf("expected the sandbox host, got %q", client.baseURL)
	}

	production, err := NewAPNsClient(APNsCredential{
		TeamID: "TEAM123456", KeyID: "KEY1234567", Topic: "com.example.pulsewatch",
		PrivateKey: pkcs8PEM(t, ecKey),
	}, nil)
	if err != nil {
		t.Fatalf("NewAPNsClient: %v", err)
	}
	if production.baseURL != apnsProductionBaseURL {
		t.Fatalf("expected the production host by default, got %q", production.baseURL)
	}

	if _, err := NewAPNsClient(APNsCredential{
		TeamID: "TEAM123456", KeyID: "KEY1234567", Topic: "com.example.pulsewatch",
		PrivateKey: pkcs8PEM(t, ecKey), Environment: "staging",
	}, nil); err == nil {
		t.Fatal("expected an unknown environment to be rejected")
	}
}
