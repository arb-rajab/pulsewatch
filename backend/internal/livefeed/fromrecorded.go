package livefeed

import (
	"time"

	"github.com/arb-rajab/pulsewatch/backend/internal/alerting"
)

// PublishRecorded is the one place alerting.Recorded — the shared return
// value of alerting.RecordCheckResult, produced identically by
// scheduler.releaseAndRecord and agentapi.recordAgentCheckResult — turns
// into live-push events. Both call sites use this instead of each
// hand-rolling their own Event construction, so the two real sources of a
// check result (server-executed and agent-reported) can never drift into
// publishing differently-shaped events for the same underlying write.
//
// Callers are expected to have already checked recorded.Inserted (a
// duplicate (target_id, checked_at) produces no new information to push —
// ADR-0010 only ever broadcasts a state a commit actually just wrote).
func PublishRecorded(hub *Hub, targetID string, recorded alerting.Recorded) {
	if hub == nil {
		return
	}
	now := time.Now().UTC()

	hub.Publish(Event{
		Type:       EventTargetStatus,
		TargetID:   targetID,
		State:      string(recorded.State),
		Streak:     recorded.Streak,
		OccurredAt: now,
	})

	if recorded.Dispatch != nil {
		hub.Publish(Event{
			Type:       EventIncident,
			TargetID:   targetID,
			IncidentID: recorded.Dispatch.IncidentID,
			Kind:       recorded.Dispatch.Kind,
			OccurredAt: now,
		})
	}
}
