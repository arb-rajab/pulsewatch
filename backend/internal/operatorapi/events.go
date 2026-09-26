package operatorapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/arb-rajab/pulsewatch/backend/internal/livefeed"
)

// heartbeatInterval bounds how long an idle SSE connection goes without any
// bytes on the wire. Comment-only frames (": ping\n\n" below) are not
// dispatched as events at all — they exist purely so an intermediary
// (browser, reverse proxy, corporate outbound proxy) that times out an
// HTTP response with no traffic never closes this connection just because
// no real target/incident event happened to occur in that window.
//
// sseWriteTimeout bounds how long any single write of this handler's (event
// frame or heartbeat ping) is allowed to take before the connection is
// considered stalled and torn down. A real, healthy dashboard connection
// only ever needs a write to actually leave this process and land in the
// kernel's send buffer — that completes in microseconds even over a slow
// network, since TCP acking happens independently of the SSE payload's
// eventual delivery. A connection that can't even manage that (client
// vanished, half-open TCP after a network drop, a proxy silently swallowing
// the socket) is exactly the stalled-but-uncleaned connection that keeps
// its sseConnLimiter slot forever without this: every write attempt —
// forced at least once per heartbeatInterval even with zero real events —
// now has to complete within sseWriteTimeout or the handler returns,
// releasing the slot via its unsubscribe/limiter.release defers.
//
// Both are vars, not consts, purely so tests can shrink them to keep a
// stalled-connection regression test fast rather than waiting out
// production-sized windows.
var (
	heartbeatInterval = 20 * time.Second
	sseWriteTimeout   = 15 * time.Second
)

// StreamEvents is GET /api/v1/events (ADR-0010): a Server-Sent Events
// stream of livefeed.Hub's real target-status/incident events, gated by the
// identical RequireOperator session-cookie auth every other operatorapi
// route uses — this is not a second identity type or a parallel auth
// mechanism, just a long-lived response body on the same authenticated
// surface.
//
// This handler never decides what counts as a state change; it only
// formats and relays whatever hub.Publish already received from the real
// write path (scheduler.releaseAndRecord, agentapi.recordAgentCheckResult).
//
// limiter caps concurrent streams per operator and globally (sseConnLimiter)
// so that RequireOperator's session-cookie auth alone can't be used to open
// unbounded long-lived connections against this process.
func StreamEvents(hub *livefeed.Hub, limiter *sseConnLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		operatorID, ok := OperatorIDFrom(c)
		if !ok {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		if !limiter.acquire(operatorID) {
			c.AbortWithStatus(http.StatusTooManyRequests)
			return
		}
		defer limiter.release(operatorID)

		events, unsubscribe := hub.Subscribe()
		defer unsubscribe()

		rc := http.NewResponseController(c.Writer)

		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no") // disable nginx-style proxy buffering of the stream
		if !setSSEWriteDeadline(rc) {
			return
		}
		c.Writer.WriteHeader(http.StatusOK)
		c.Writer.Flush()

		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()

		ctx := c.Request.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				if !writeEvent(c, rc, event) {
					return
				}
			case <-ticker.C:
				if !setSSEWriteDeadline(rc) {
					return
				}
				if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil {
					return
				}
				c.Writer.Flush()
			}
		}
	}
}

// setSSEWriteDeadline arms sseWriteTimeout on the connection underlying c's
// ResponseWriter ahead of the write that follows it, so that write — event
// frame, heartbeat ping, or the initial header flush — is guaranteed to
// either complete or fail within sseWriteTimeout rather than block on a
// stalled client indefinitely. Every write path in this handler calls it
// immediately before writing, matching net.Conn's "deadline applies to the
// next call" semantics. It reports false (handler should return) only if
// the underlying connection refuses deadlines outright, which never
// happens for a real HTTP/1.1 connection — gin's ResponseWriter is always
// backed by one here — and exists only so a non-conforming ResponseWriter
// (as in a unit test using httptest.NewRecorder) fails closed instead of
// silently never timing out.
func setSSEWriteDeadline(rc *http.ResponseController) bool {
	return rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)) == nil
}

// writeEvent formats one livefeed.Event as a single SSE frame (event name
// plus a JSON data line) and flushes it immediately — SSE requires each
// event be flushed as it's produced, not buffered until the handler
// returns, since the whole point is a client seeing it as it happens.
func writeEvent(c *gin.Context, rc *http.ResponseController, event livefeed.Event) bool {
	payload, err := json.Marshal(event)
	if err != nil {
		// Our own data failed to marshal — a bug, not a client problem. Skip
		// this one event rather than tearing down an otherwise-healthy
		// connection over it.
		return true
	}
	if !setSSEWriteDeadline(rc) {
		return false
	}
	if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event.Type, payload); err != nil {
		return false
	}
	c.Writer.Flush()
	return true
}
