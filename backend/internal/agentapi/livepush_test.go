package agentapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/arb-rajab/pulsewatch/backend/internal/livefeed"
)

// TestIngestLogs_ThresholdCrossing_PublishesLiveEvents is the agent-reported
// (OTLP) half of ADR-0010's real end-to-end proof — the server-executed
// half lives in internal/scheduler/livepush_test.go. Three real OTLP
// check_result submissions over the real authenticated HTTP endpoint (the
// identical sequence TestIngestLogs_ThresholdCrossing_RealPipeline already
// proves reaches streak=3/state=alerting/one open incident) must also
// publish a real target_status event per submission and a real incident
// event on the threshold-crossing edge, to a Hub subscriber that never
// touches Postgres directly — proving recordAgentCheckResult's live-push
// side effect, not just its database commit.
func TestIngestLogs_ThresholdCrossing_PublishesLiveEvents(t *testing.T) {
	pool := testPool(t)
	agentID, token := insertTestAgent(t, pool, "test-logs-livepush", 60)
	targetID := insertAssignedTarget(t, pool, agentID, defaultTestTargetOpts())

	hub := livefeed.NewHub()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	r := testRouterWithHub(pool, &spyDispatcher{}, nil, hub)
	base := time.Now().UTC()

	for i := range 3 {
		body := buildOtlpRequest(agentID, checkResultRecord(base.Add(time.Duration(i)*time.Second), targetID, false, 42))
		w := doRequest(t, r, http.MethodPost, "/v1/logs", token, body)
		if w.Code != http.StatusOK {
			t.Fatalf("iteration %d: expected 200, got %d: %s", i, w.Code, w.Body.String())
		}
	}

	var sawAlertingStatus, sawOpenedIncident bool
	deadline := time.After(5 * time.Second)
	for !sawAlertingStatus || !sawOpenedIncident {
		select {
		case ev := <-events:
			if ev.TargetID != targetID {
				continue
			}
			if ev.Type == livefeed.EventTargetStatus && ev.State == "alerting" && ev.Streak == 3 {
				sawAlertingStatus = true
			}
			if ev.Type == livefeed.EventIncident && ev.Kind == "opened" {
				sawOpenedIncident = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for live-push events from OTLP ingestion: sawAlertingStatus=%v sawOpenedIncident=%v", sawAlertingStatus, sawOpenedIncident)
		}
	}
}
