package operatorapi

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arb-rajab/pulsewatch/backend/internal/livefeed"
)

// TestStreamEvents_RealHTTPClientReceivesALivePublishedEvent is ADR-0010's
// HTTP-level proof: a genuine net/http.Client holds open a real streaming
// GET /api/v1/events response (through a real httptest.Server, not a
// buffering httptest.ResponseRecorder, which cannot represent a connection
// that never finishes writing) behind the identical RequireOperator
// session-cookie auth every other operatorapi route uses, and actually
// receives, over that live TCP connection, an event published to the same
// Hub the real scheduler/agentapi write paths publish through
// (scheduler/livepush_test.go proves that half: a real threshold-crossing
// failure sequence genuinely produces these Publish calls). Together the
// two tests prove a real incident state change reaches a real connected
// client end to end.
func TestStreamEvents_RealHTTPClientReceivesALivePublishedEvent(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-events-live@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)

	hub := livefeed.NewHub()
	r := testRouterWithHub(pool, hub)
	srv := httptest.NewServer(r)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Cookie", fmt.Sprintf("%s=%s", "pulsewatch_session", cookie))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 opening the SSE stream, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected Content-Type text/event-stream, got %q", ct)
	}

	// Wait until the handler has actually subscribed before publishing —
	// otherwise a Publish could race ahead of Subscribe and the event would
	// correctly, but unhelpfully, be delivered to nobody.
	deadline := time.Now().Add(5 * time.Second)
	for hub.SubscriberCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("handler never subscribed to the hub")
		}
		time.Sleep(10 * time.Millisecond)
	}

	hub.Publish(livefeed.Event{
		Type:       livefeed.EventIncident,
		TargetID:   "11111111-1111-1111-1111-111111111111",
		IncidentID: 999,
		Kind:       "opened",
		OccurredAt: time.Now().UTC(),
	})

	scanner := bufio.NewScanner(resp.Body)
	var sawEventLine, sawDataLine bool
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "event: incident":
			sawEventLine = true
		case strings.HasPrefix(line, "data: ") && strings.Contains(line, `"incident_id":999`) && strings.Contains(line, `"kind":"opened"`):
			sawDataLine = true
		}
		if sawEventLine && sawDataLine {
			break
		}
	}
	if err := scanner.Err(); err != nil && !sawDataLine {
		t.Fatalf("reading SSE stream: %v", err)
	}
	if !sawEventLine || !sawDataLine {
		t.Fatalf("real HTTP client never received the published event over the stream (sawEventLine=%v sawDataLine=%v)", sawEventLine, sawDataLine)
	}
}

// TestStreamEvents_RejectsMissingSession proves GET /api/v1/events sits
// behind the identical RequireOperator gate as every other route — it is
// not a second, weaker auth surface.
func TestStreamEvents_RejectsMissingSession(t *testing.T) {
	pool := testPool(t)
	r := testRouterWithHub(pool, livefeed.NewHub())

	w := doRequest(t, r, http.MethodGet, "/api/v1/events", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no session cookie, got %d", w.Code)
	}
}

// TestStreamEvents_CapsConcurrentConnectionsPerOperator proves the
// sseConnLimiter wired into StreamEvents actually refuses a real operator's
// (sseConnsPerOperator+1)th concurrent stream rather than letting a single
// authenticated session open unbounded connections against the process —
// the gap this session's cap closes. Every request here is a genuine held-
// open net/http.Client connection through a real httptest.Server, not a
// buffering ResponseRecorder, since the limiter only matters while a
// connection is actually still open.
func TestStreamEvents_CapsConcurrentConnectionsPerOperator(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-events-cap@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)

	r := testRouterWithHub(pool, livefeed.NewHub())
	srv := httptest.NewServer(r)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	open := func() *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Cookie", fmt.Sprintf("%s=%s", "pulsewatch_session", cookie))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("open SSE stream: %v", err)
		}
		return resp
	}

	// Open exactly the allowed number of concurrent streams and keep them
	// open for the rest of the test — closing any of them would free a slot
	// and defeat the point of this test.
	for i := 0; i < sseConnsPerOperator; i++ {
		resp := open()
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d: expected 200 opening the SSE stream, got %d", i, resp.StatusCode)
		}
	}

	// The next stream from the same operator, while all sseConnsPerOperator
	// slots are still held, must be refused.
	resp := open()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 once the per-operator cap is reached, got %d", resp.StatusCode)
	}
}
