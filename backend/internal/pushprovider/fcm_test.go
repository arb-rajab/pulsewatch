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

// fcmServer is a mock that speaks the *real* FCM HTTP v1 protocol: the
// OAuth 2 JWT-bearer token exchange on /token, and
// POST /v1/projects/{project}/messages:send with a bearer access token.
// It is a mock of Google's server, not of this package's own client — every
// byte the client sends is a byte a real FCM endpoint would receive, which
// is the only kind of test that can catch a wrong signing algorithm, a
// wrong grant type, or a wrong request path.
//
// A real FCM project cannot be exercised from this repo's CI or from the
// sandbox this channel was built in (it needs a Firebase project and a
// service-account key, which are real credentials nobody should commit) —
// so the protocol is verified here, exhaustively, against the published
// request and error shapes. See ADR-0007's "What is and isn't verified".
type fcmServer struct {
	*httptest.Server
	tokenRequests atomic.Int32
	sendRequests  atomic.Int32

	// lastAssertion is the signed JWT the token endpoint received.
	lastAssertion atomic.Value // string
	// lastSend is the decoded body of the most recent send.
	lastSend atomic.Value // map[string]any
	// lastAuthorization is the Authorization header of the most recent send.
	lastAuthorization atomic.Value // string

	// sendHandler decides each send's response. Returning ok==true means
	// "202 accepted".
	sendHandler func(attempt int32) (status int, body string)
}

func newFCMServer(t *testing.T, sendHandler func(attempt int32) (int, string)) *fcmServer {
	t.Helper()
	s := &fcmServer{sendHandler: sendHandler}
	mux := http.NewServeMux()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		s.tokenRequests.Add(1)
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got := r.PostForm.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Errorf("expected the RFC 7523 JWT-bearer grant type, got %q", got)
		}
		s.lastAssertion.Store(r.PostForm.Get("assertion"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"test-access-token","expires_in":3600,"token_type":"Bearer"}`)
	})

	mux.HandleFunc("/v1/projects/test-project/messages:send", func(w http.ResponseWriter, r *http.Request) {
		attempt := s.sendRequests.Add(1)
		s.lastAuthorization.Store(r.Header.Get("Authorization"))
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.lastSend.Store(body)

		status, respBody := s.sendHandler(attempt)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s %s — the client is not speaking FCM HTTP v1", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})

	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func acceptAll(int32) (int, string) {
	return http.StatusOK, `{"name":"projects/test-project/messages/1"}`
}

func newTestFCMClient(t *testing.T, srv *fcmServer) *FCMClient {
	t.Helper()
	rsaKey, _ := testKeys(t)
	client, err := NewFCMClient(FCMCredential{
		ProjectID:    "test-project",
		ClientEmail:  "pulsewatch@test-project.iam.gserviceaccount.com",
		PrivateKey:   pkcs8PEM(t, rsaKey),
		PrivateKeyID: "test-key-id",
		TokenURI:     srv.URL + "/token",
		BaseURL:      srv.URL,
	}, srv.Client())
	if err != nil {
		t.Fatalf("NewFCMClient: %v", err)
	}
	return client
}

func testNotification() Notification {
	return Notification{
		Title:      "pulsewatch: incident opened",
		Body:       "A monitored target has started failing its checks.",
		Data:       map[string]string{"kind": "opened", "incident_id": "42", "target_id": "0f6b6f5e-0000-4000-8000-000000000001"},
		CollapseID: "opened-42",
	}
}

// TestFCMClient_SendSignsRealAssertionAndPostsRealV1Message is the
// protocol-level proof: a real RS256 assertion the token endpoint can
// verify, exchanged for a real access token, used as a bearer on a real
// v1 messages:send with the token, notification and data FCM expects.
func TestFCMClient_SendSignsRealAssertionAndPostsRealV1Message(t *testing.T) {
	srv := newFCMServer(t, acceptAll)
	client := newTestFCMClient(t, srv)
	rsaKey, _ := testKeys(t)

	if err := client.Send(context.Background(), "device-token-abc", testNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// 1. The assertion is a genuine RS256 JWT signed by the service-account
	//    key, with Google's required claim set.
	assertion, _ := srv.lastAssertion.Load().(string)
	if assertion == "" {
		t.Fatal("token endpoint received no assertion")
	}
	header, claims, signingInput, sig := decodeJWT(t, assertion)
	if header["alg"] != "RS256" {
		t.Fatalf("expected alg=RS256, got %v", header["alg"])
	}
	if header["kid"] != "test-key-id" {
		t.Fatalf("expected kid to be the service account's private_key_id, got %v", header["kid"])
	}
	verifyRS256(t, &rsaKey.PublicKey, signingInput, sig)
	if claims["iss"] != "pulsewatch@test-project.iam.gserviceaccount.com" {
		t.Fatalf("expected iss to be client_email, got %v", claims["iss"])
	}
	if claims["scope"] != fcmScope {
		t.Fatalf("expected the firebase.messaging scope, got %v", claims["scope"])
	}
	if claims["aud"] != srv.URL+"/token" {
		t.Fatalf("expected aud to be the token endpoint, got %v", claims["aud"])
	}

	// 2. The send carried the access token the token endpoint issued.
	if got, _ := srv.lastAuthorization.Load().(string); got != "Bearer test-access-token" {
		t.Fatalf("expected the issued access token as a bearer, got %q", got)
	}

	// 3. The message body is a real v1 Message resource.
	body, _ := srv.lastSend.Load().(map[string]any)
	message, ok := body["message"].(map[string]any)
	if !ok {
		t.Fatalf("expected a v1 Message envelope, got %#v", body)
	}
	if message["token"] != "device-token-abc" {
		t.Fatalf("expected the device token in message.token, got %v", message["token"])
	}
	notification, _ := message["notification"].(map[string]any)
	if notification["title"] != "pulsewatch: incident opened" {
		t.Fatalf("expected the notification title, got %v", notification["title"])
	}
	data, _ := message["data"].(map[string]any)
	if data["incident_id"] != "42" || data["kind"] != "opened" {
		t.Fatalf("expected the deep-link data payload, got %#v", data)
	}
	android, _ := message["android"].(map[string]any)
	if android["priority"] != "high" {
		t.Fatalf("expected android.priority=high for an outage alert, got %v", android["priority"])
	}
	if android["collapse_key"] != "opened-42" {
		t.Fatalf("expected the collapse key, got %v", android["collapse_key"])
	}
}

// TestFCMClient_AccessTokenIsCachedAcrossSends proves the OAuth exchange is
// not repeated per notification — with a real fan-out over N devices, one
// token request per device would be N times the latency and would run
// straight into Google's own rate limits.
func TestFCMClient_AccessTokenIsCachedAcrossSends(t *testing.T) {
	srv := newFCMServer(t, acceptAll)
	client := newTestFCMClient(t, srv)

	for i := 0; i < 3; i++ {
		if err := client.Send(context.Background(), "device-token-abc", testNotification()); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}
	if got := srv.tokenRequests.Load(); got != 1 {
		t.Fatalf("expected exactly 1 token exchange across 3 sends, got %d", got)
	}
	if got := srv.sendRequests.Load(); got != 3 {
		t.Fatalf("expected 3 sends, got %d", got)
	}
}

// fcmError builds a real google.rpc.Status error body with an FcmError
// detail, exactly as FCM returns one.
func fcmError(status int, rpcStatus, errorCode string) string {
	return `{"error":{"code":` + itoa(status) + `,"message":"test","status":"` + rpcStatus + `","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"` + errorCode + `"}]}}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestFCMClient_ClassifiesRealErrorBodies is the dead-token proof at the
// provider layer: FCM's published error codes are mapped to the three
// decisions a caller can act on, and in particular UNREGISTERED and
// UNAVAILABLE — which look identical as "a failed HTTP request" — are
// mapped to opposite ones.
func TestFCMClient_ClassifiesRealErrorBodies(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		rpcStatus  string
		errorCode  string
		wantKind   ErrorKind
		wantReason string
	}{
		{"uninstalled app", http.StatusNotFound, "NOT_FOUND", "UNREGISTERED", KindDeadToken, "UNREGISTERED"},
		{"malformed token", http.StatusBadRequest, "INVALID_ARGUMENT", "INVALID_ARGUMENT", KindDeadToken, "INVALID_ARGUMENT"},
		{"token from another project", http.StatusForbidden, "PERMISSION_DENIED", "SENDER_ID_MISMATCH", KindDeadToken, "SENDER_ID_MISMATCH"},
		{"fcm overloaded", http.StatusServiceUnavailable, "UNAVAILABLE", "UNAVAILABLE", KindTransient, "UNAVAILABLE"},
		{"fcm internal error", http.StatusInternalServerError, "INTERNAL", "INTERNAL", KindTransient, "INTERNAL"},
		{"rate limited", http.StatusTooManyRequests, "RESOURCE_EXHAUSTED", "QUOTA_EXCEEDED", KindTransient, "QUOTA_EXCEEDED"},
		{"our apns key expired inside firebase", http.StatusUnauthorized, "UNAUTHENTICATED", "THIRD_PARTY_AUTH_ERROR", KindCredential, "THIRD_PARTY_AUTH_ERROR"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFCMServer(t, func(int32) (int, string) {
				return tc.status, fcmError(tc.status, tc.rpcStatus, tc.errorCode)
			})
			client := newTestFCMClient(t, srv)

			err := client.Send(context.Background(), "device-token-abc", testNotification())
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := KindOf(err); got != tc.wantKind {
				t.Fatalf("expected kind %q, got %q (reason %q)", tc.wantKind, got, err.Error())
			}
			if !strings.Contains(err.Error(), tc.wantReason) {
				t.Fatalf("expected the provider error code %q in the reason, got %q", tc.wantReason, err.Error())
			}
			if strings.Contains(err.Error(), "device-token-abc") {
				t.Fatalf("FR-023: the device token must never appear in an error reason, got %q", err.Error())
			}
		})
	}
}

// TestFCMClient_RejectedAssertionIsCredentialNotTransient proves a bad
// service-account key is never retried as if it were a blip.
func TestFCMClient_RejectedAssertionIsCredentialNotTransient(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rsaKey, _ := testKeys(t)
	client, err := NewFCMClient(FCMCredential{
		ProjectID:   "test-project",
		ClientEmail: "pulsewatch@test-project.iam.gserviceaccount.com",
		PrivateKey:  pkcs8PEM(t, rsaKey),
		TokenURI:    srv.URL + "/token",
		BaseURL:     srv.URL,
	}, srv.Client())
	if err != nil {
		t.Fatalf("NewFCMClient: %v", err)
	}

	sendErr := client.Send(context.Background(), "device-token-abc", testNotification())
	if sendErr == nil {
		t.Fatal("expected an error")
	}
	if got := KindOf(sendErr); got != KindCredential {
		t.Fatalf("expected KindCredential for a rejected assertion, got %q (%s)", got, sendErr)
	}
}

// TestNewFCMClient_RejectsIncompleteCredential proves configuration errors
// surface at construction, not on the first real incident.
func TestNewFCMClient_RejectsIncompleteCredential(t *testing.T) {
	if _, err := NewFCMClient(FCMCredential{ProjectID: "p"}, nil); err == nil {
		t.Fatal("expected a credential missing client_email and private_key to be rejected")
	}
}

// TestClientFromCredentialJSON_BuildsTheRightClient proves the one blob an
// operator pastes into a push channel's destination resolves to the right
// provider — and that an unknown provider is a named configuration error,
// never a silent no-op.
func TestClientFromCredentialJSON_BuildsTheRightClient(t *testing.T) {
	rsaKey, ecKey := testKeys(t)

	fcmJSON, err := json.Marshal(map[string]any{
		"provider": "fcm", "project_id": "p", "client_email": "e@x.iam.gserviceaccount.com",
		"private_key": pkcs8PEM(t, rsaKey),
	})
	if err != nil {
		t.Fatalf("marshal fcm credential: %v", err)
	}
	client, err := ClientFromCredentialJSON(string(fcmJSON), nil)
	if err != nil {
		t.Fatalf("ClientFromCredentialJSON(fcm): %v", err)
	}
	if client.Provider() != "fcm" {
		t.Fatalf("expected an fcm client, got %q", client.Provider())
	}

	apnsJSON, err := json.Marshal(map[string]any{
		"provider": "apns", "team_id": "TEAM123456", "key_id": "KEY1234567",
		"private_key": pkcs8PEM(t, ecKey), "topic": "com.example.pulsewatch",
	})
	if err != nil {
		t.Fatalf("marshal apns credential: %v", err)
	}
	client, err = ClientFromCredentialJSON(string(apnsJSON), nil)
	if err != nil {
		t.Fatalf("ClientFromCredentialJSON(apns): %v", err)
	}
	if client.Provider() != "apns" {
		t.Fatalf("expected an apns client, got %q", client.Provider())
	}

	if _, err := ClientFromCredentialJSON(`{"provider":"sms"}`, nil); err == nil {
		t.Fatal("expected an unknown provider to be rejected")
	}
	if _, err := ClientFromCredentialJSON(`not json`, nil); err == nil {
		t.Fatal("expected malformed credential JSON to be rejected")
	}
}
