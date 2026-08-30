package operatorapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestLogin_RealCredentialIssuesAWorkingSession(t *testing.T) {
	pool := testPool(t)
	insertTestOperator(t, pool, "test-login-e2e@example.invalid", "a-real-password")
	r := testRouter(pool)

	w := doRequest(t, r, http.MethodPost, "/api/v1/auth/login", "", []byte(`{"email":"test-login-e2e@example.invalid","password":"a-real-password"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp loginResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Email != "test-login-e2e@example.invalid" {
		t.Fatalf("expected email echoed back, got %q", resp.Email)
	}

	cookies := w.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "pulsewatch_session" {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected a pulsewatch_session cookie to be set")
	}
	if !sessionCookie.HttpOnly {
		t.Fatal("expected the session cookie to be HttpOnly")
	}
	if !sessionCookie.Secure {
		t.Fatal("expected the session cookie to be Secure")
	}
	if sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("expected SameSite=Strict, got %v", sessionCookie.SameSite)
	}

	// Prove the issued cookie actually authenticates a real gated request.
	w2 := doRequest(t, r, http.MethodGet, "/api/v1/targets", sessionCookie.Value, nil)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected the freshly issued session to authenticate a gated request, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestLogin_WrongPasswordRejected(t *testing.T) {
	pool := testPool(t)
	insertTestOperator(t, pool, "test-login-wrongpw-http@example.invalid", "a-real-password")
	r := testRouter(pool)

	w := doRequest(t, r, http.MethodPost, "/api/v1/auth/login", "", []byte(`{"email":"test-login-wrongpw-http@example.invalid","password":"nope"}`))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestLogin_MalformedBodyRejected(t *testing.T) {
	pool := testPool(t)
	r := testRouter(pool)

	w := doRequest(t, r, http.MethodPost, "/api/v1/auth/login", "", []byte(`{"email":"only-email@example.invalid"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing password field, got %d: %s", w.Code, w.Body.String())
	}
}

func TestLogin_NonJsonContentTypeRejected(t *testing.T) {
	pool := testPool(t)
	r := testRouter(pool)

	req := mustRequest(t, http.MethodPost, "/api/v1/auth/login", []byte(`email=x&password=y`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := recordRequest(r, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415 for a non-JSON content type (CSRF mitigation), got %d: %s", w.Code, w.Body.String())
	}
}

// TestLogin_RateLimitedAfterRepeatedFailures proves the login rate limiter
// has real teeth: enough failed attempts against the same email eventually
// produce 429, not an unbounded number of 401s.
func TestLogin_RateLimitedAfterRepeatedFailures(t *testing.T) {
	pool := testPool(t)
	insertTestOperator(t, pool, "test-login-ratelimit@example.invalid", "a-real-password")
	r := testRouter(pool)

	body := []byte(`{"email":"test-login-ratelimit@example.invalid","password":"wrong"}`)
	var lastCode int
	sawTooManyRequests := false
	for i := 0; i < loginFailureLimit+2; i++ {
		w := doRequest(t, r, http.MethodPost, "/api/v1/auth/login", "", body)
		lastCode = w.Code
		if w.Code == http.StatusTooManyRequests {
			sawTooManyRequests = true
			break
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for a wrong password attempt, got %d", w.Code)
		}
	}
	if !sawTooManyRequests {
		t.Fatalf("expected to eventually observe 429 after %d failed attempts, last code was %d", loginFailureLimit+2, lastCode)
	}
}

func TestLogout_RequiresValidSession(t *testing.T) {
	pool := testPool(t)
	r := testRouter(pool)

	w := doRequest(t, r, http.MethodPost, "/api/v1/auth/logout", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for logout with no session, got %d: %s", w.Code, w.Body.String())
	}
}

func TestLogout_ClearsTheCookie(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-logout@example.invalid", "a-real-password")
	r := testRouter(pool)

	cookie := realSessionCookie(t, operatorID)
	w := doRequest(t, r, http.MethodPost, "/api/v1/auth/logout", cookie, nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var cleared *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "pulsewatch_session" {
			cleared = c
		}
	}
	if cleared == nil {
		t.Fatal("expected logout to set a clearing Set-Cookie")
	}
	if cleared.MaxAge >= 0 {
		t.Fatalf("expected a negative Max-Age to clear the cookie, got %d", cleared.MaxAge)
	}
}
