package scheduler

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// waitForCondition polls cond until it returns true or timeout elapses,
// failing the test on timeout. Used for asserting on the effects of async
// worker-pool processing without coupling tests to internal timing.
func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %s", timeout)
	}
}

// nopLogger discards logs so integration tests stay quiet on the happy path.
func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// upTarget starts a real HTTP server that always returns 200, for tests that
// only care about the leasing/scheduling behavior, not check-execution
// nuance.
func upTarget() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}
