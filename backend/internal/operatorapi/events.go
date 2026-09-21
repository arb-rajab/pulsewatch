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
const heartbeatInterval = 20 * time.Second

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
func StreamEvents(hub *livefeed.Hub) gin.HandlerFunc {
	return func(c *gin.Context) {
		events, unsubscribe := hub.Subscribe()
		defer unsubscribe()

		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no") // disable nginx-style proxy buffering of the stream
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
				if !writeEvent(c, event) {
					return
				}
			case <-ticker.C:
				if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil {
					return
				}
				c.Writer.Flush()
			}
		}
	}
}

// writeEvent formats one livefeed.Event as a single SSE frame (event name
// plus a JSON data line) and flushes it immediately — SSE requires each
// event be flushed as it's produced, not buffered until the handler
// returns, since the whole point is a client seeing it as it happens.
func writeEvent(c *gin.Context, event livefeed.Event) bool {
	payload, err := json.Marshal(event)
	if err != nil {
		// Our own data failed to marshal — a bug, not a client problem. Skip
		// this one event rather than tearing down an otherwise-healthy
		// connection over it.
		return true
	}
	if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event.Type, payload); err != nil {
		return false
	}
	c.Writer.Flush()
	return true
}
