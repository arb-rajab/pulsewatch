// Package livefeed is ADR-0010's live-push transport: a small in-process
// pub/sub hub that fans out real target-state and incident-lifecycle events
// to connected dashboard clients over Server-Sent Events. It holds no
// database connection and makes no write of its own — every event it ever
// carries is produced by, and only after, the same guarded writes
// alerting.RecordCheckResult/OpenIncident/CloseIncident already commit
// (scheduler.releaseAndRecord, agentapi.recordAgentCheckResult). This
// package never decides whether a state actually changed; it only
// broadcasts what a caller already persisted.
package livefeed

import (
	"sync"
	"time"
)

// EventType names the two shapes of event this session's hook points ever
// produce — see ADR-0010's "Scope" section for why there is no third
// ("acknowledged") kind.
type EventType string

const (
	// EventTargetStatus is published once per real (non-duplicate) recorded
	// check result — alerting.Recorded.Inserted true — regardless of whether
	// State actually differs from the previous value, so a client always has
	// a fresh Streak/State pair to reconcile against rather than having to
	// infer "no change" from silence.
	EventTargetStatus EventType = "target_status"
	// EventIncident is published only when the guarded incidents write
	// (alerting.OpenIncident/CloseIncident) actually returned a row — the
	// identical gate alerting.NotifyChannels already uses, so a dashboard
	// never sees an incident event that has no corresponding
	// alert_dispatches attempt (or vice versa).
	EventIncident EventType = "incident"
)

// Event is the one payload shape this package ever broadcasts, serialized
// as SSE `data:` JSON. Fields that don't apply to an EventType are left at
// their zero value (e.g. IncidentID/Kind are empty on an EventTargetStatus).
type Event struct {
	Type       EventType `json:"type"`
	TargetID   string    `json:"target_id"`
	OccurredAt time.Time `json:"occurred_at"`

	// State/Streak apply to EventTargetStatus — target_schedule.state/streak
	// exactly as alerting.Recorded returned them from the same commit.
	State  string `json:"state,omitempty"`
	Streak int    `json:"streak,omitempty"`

	// IncidentID/Kind apply to EventIncident — alerting.DispatchRequest's own
	// fields, verbatim ("opened" | "resolved").
	IncidentID int64  `json:"incident_id,omitempty"`
	Kind       string `json:"kind,omitempty"`
}

// subscriberBuffer is how many undelivered events a slow subscriber can
// accumulate before Publish starts dropping for it. Small and deliberate:
// this is a live-view convenience, not a durable queue — a dashboard that
// misses an event still has the correct picture on its next SSR load or
// reconnect (ADR-0010 Consequences), so there is no reason to let one slow
// reader hold events in memory indefinitely.
const subscriberBuffer = 16

// Hub is the fan-out point: any number of subscribers, each its own
// buffered channel. The zero value is not usable — construct with NewHub.
type Hub struct {
	mu          sync.Mutex
	subscribers map[chan Event]struct{}
}

// NewHub constructs an empty Hub, ready for Subscribe/Publish.
func NewHub() *Hub {
	return &Hub{subscribers: make(map[chan Event]struct{})}
}

// Subscribe registers a new subscriber and returns its event channel and an
// Unsubscribe function the caller must call exactly once (typically via
// defer) when done reading, closing the channel and removing it from the
// fan-out set. The returned channel is never closed by Publish itself —
// only Unsubscribe closes it — so a range over it blocks until Unsubscribe
// runs, never spuriously.
func (h *Hub) Subscribe() (events <-chan Event, unsubscribe func()) {
	ch := make(chan Event, subscriberBuffer)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subscribers, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
	return ch, unsub
}

// Publish fans an event out to every currently subscribed channel. Delivery
// is best-effort and non-blocking per subscriber (ADR-0010 Consequences): a
// subscriber whose buffer is already full simply doesn't receive this
// event, rather than Publish blocking the caller (the scheduler's own
// release path, or agent OTLP ingestion) on a slow or stalled dashboard
// connection.
func (h *Hub) Publish(event Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subscribers {
		select {
		case ch <- event:
		default:
			// Slow subscriber — drop for it, not for anyone else, and never
			// block the real write path that produced this event.
		}
	}
}

// SubscriberCount reports how many subscribers are currently registered —
// exposed for tests; production code has no need to branch on it.
func (h *Hub) SubscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subscribers)
}
