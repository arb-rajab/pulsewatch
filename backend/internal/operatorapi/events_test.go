package operatorapi

import (
	"bufio"
	"context"
	"fmt"
	"net"
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

// tinyBufferListener wraps a real TCP listener so every accepted connection
// has a minimal kernel send buffer — used only to make a "client stopped
// reading" stall reproducible fast in a test, without needing megabytes of
// unread data to actually fill a default-sized (megabyte-scale) socket
// buffer first.
type tinyBufferListener struct{ net.Listener }

func (l tinyBufferListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return conn, err
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetWriteBuffer(1)
	}
	return conn, nil
}

// TestStreamEvents_StalledConnectionIsClosedAndSlotFreed is the write/idle
// timeout regression: it proves a connection that stops being read by its
// client — a stalled network peer or a half-open TCP session, the exact gap
// this session's sseWriteTimeout closes — is actually torn down by the
// server, releasing both its livefeed.Hub subscription and its
// sseConnLimiter slot, rather than holding them indefinitely and eating
// into the connection cap the way it could before this change. It shrinks
// heartbeatInterval/sseWriteTimeout so the test doesn't have to wait out
// production-sized windows, and shrinks the accepted connection's kernel
// send buffer (tinyBufferListener) so a client that simply never reads
// causes the server's heartbeat-ping writes to block almost immediately,
// the same way a real stalled peer eventually would once OS buffers fill.
func TestStreamEvents_StalledConnectionIsClosedAndSlotFreed(t *testing.T) {
	origHeartbeat, origWriteTimeout := heartbeatInterval, sseWriteTimeout
	heartbeatInterval = 20 * time.Millisecond
	sseWriteTimeout = 100 * time.Millisecond
	t.Cleanup(func() {
		heartbeatInterval = origHeartbeat
		sseWriteTimeout = origWriteTimeout
	})

	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-events-stall@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)

	hub := livefeed.NewHub()
	r := testRouterWithHub(pool, hub)

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &httptest.Server{Listener: tinyBufferListener{ln}, Config: &http.Server{Handler: r}}
	srv.Start()
	defer srv.Close()

	var d net.Dialer
	conn, err := d.DialContext(t.Context(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(1)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Cookie", fmt.Sprintf("%s=%s", "pulsewatch_session", cookie))
	req.Host = ln.Addr().String()
	if err := req.Write(conn); err != nil {
		t.Fatalf("write request: %v", err)
	}

	// Read just enough (status line + headers) to confirm the stream
	// actually opened, then — deliberately — never read from conn again.
	// That's the stall: a real client that stopped consuming the stream.
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 opening the SSE stream, got %d", resp.StatusCode)
	}

	subDeadline := time.Now().Add(5 * time.Second)
	for hub.SubscriberCount() == 0 {
		if time.Now().After(subDeadline) {
			t.Fatal("handler never subscribed to the hub")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Push enough real, non-trivial-sized events (rather than relying on
	// the tiny heartbeat pings alone) that the handler's forwarding writes
	// quickly exceed the shrunk-to-minimum receive window nobody is
	// draining. livefeed's subscriber buffer holds up to 16, so publish
	// exactly that many ~8KB frames — comfortably more than the kernel's
	// smallest usable TCP window — to guarantee an in-flight write blocks
	// rather than depending on OS buffer autotuning specifics.
	largeTargetID := strings.Repeat("a", 8000)
	for i := 0; i < 16; i++ {
		hub.Publish(livefeed.Event{
			Type:       livefeed.EventTargetStatus,
			TargetID:   largeTargetID,
			State:      "down",
			Streak:     i,
			OccurredAt: time.Now().UTC(),
		})
	}

	// The server keeps trying to write into a connection nobody is
	// draining. Once the receive window nobody is servicing fills, those
	// writes block until sseWriteTimeout trips them and the handler
	// returns, which must unsubscribe from the hub and release its
	// limiter slot.
	cleanupDeadline := time.Now().Add(5 * time.Second)
	for hub.SubscriberCount() != 0 {
		if time.Now().After(cleanupDeadline) {
			t.Fatal("stalled SSE connection was never cleaned up (subscriber slot still held)")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The freed slot must actually be usable again — proving this
	// interacts correctly with the connection cap rather than merely
	// decrementing a counter nothing else reads.
	newCookie := realSessionCookie(t, operatorID)
	for i := 0; i < sseConnsPerOperator; i++ {
		req2, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/v1/events", nil)
		if err != nil {
			t.Fatalf("build request %d: %v", i, err)
		}
		req2.Header.Set("Cookie", fmt.Sprintf("%s=%s", "pulsewatch_session", newCookie))
		resp2, err := http.DefaultClient.Do(req2)
		if err != nil {
			t.Fatalf("open stream %d after cleanup: %v", i, err)
		}
		defer func() { _ = resp2.Body.Close() }()
		if resp2.StatusCode != http.StatusOK {
			t.Fatalf("stream %d after cleanup: expected 200, got %d", i, resp2.StatusCode)
		}
	}
}
