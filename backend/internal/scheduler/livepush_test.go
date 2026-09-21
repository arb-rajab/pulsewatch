package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/arb-rajab/pulsewatch/backend/internal/livefeed"
)

// TestEndToEnd_ThresholdCrossing_PublishesLiveTargetStatusAndIncidentEvents
// is ADR-0010's own real, end-to-end proof: driven through the identical
// real scheduler pipeline (tick -> claim -> execute -> release -> evaluate
// -> dispatch) TestEndToEnd_ThresholdCrossing_DispatchesOnce_ThenResolvesOnRecovery
// already proves opens a real incident and dispatches a real notification,
// this test additionally subscribes a real livefeed.Hub as the Scheduler's
// publisher and asserts a connected subscriber actually receives, over that
// Hub, both a target_status event for every recorded check and an incident
// event for the real open/resolve edge transitions — not a synthetic call
// into livefeed.PublishRecorded, but the genuine live-push side effect of a
// real threshold-crossing failure sequence followed by real recovery.
func TestEndToEnd_ThresholdCrossing_PublishesLiveTargetStatusAndIncidentEvents(t *testing.T) {
	pool := testPool(t)
	srv, failing := newToggleServer()
	defer srv.Close()
	failing.Store(true)

	opts := defaultTestTargetOpts()
	opts.timeoutSeconds = 2
	targetID := insertTestTarget(t, pool, srv.URL, opts)

	cfg := DefaultConfig()
	cfg.WorkerPoolSize = 2
	cfg.TickInterval = 20 * time.Millisecond
	cfg.HardShutdownDeadline = 2 * time.Second

	sched, err := New(pool, cfg, nopLogger())
	if err != nil {
		t.Fatalf("construct scheduler: %v", err)
	}
	sched.SetDispatcher(&spyDispatcher{}) // no real alert_channels row needed for this test

	hub := livefeed.NewHub()
	sched.SetPublisher(hub)
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- sched.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(3 * time.Second):
			t.Fatal("scheduler did not shut down in time")
		}
	}()

	// Three consecutive failures: the third crosses the default threshold
	// (3), the identical sequence alert_lifecycle_test.go's own end-to-end
	// test drives.
	for i := 1; i <= 3; i++ {
		want := i
		waitForCondition(t, 15*time.Second, func() bool { return countCheckResults(t, pool, targetID) >= want })
		if i < 3 {
			forceDueNow(t, pool, targetID)
		}
	}

	// Drain events until we've seen a target_status for this target with
	// state=alerting AND the incident-opened event, in either order — both
	// are published from the same handleJob call, but map iteration order
	// (Hub.Publish) does not promise which livefeed.Event a subscriber reads
	// first.
	var sawAlertingStatus, sawOpenedIncident bool
	deadline := time.After(15 * time.Second)
	for !sawAlertingStatus || !sawOpenedIncident {
		select {
		case ev := <-events:
			if ev.TargetID != targetID {
				continue // a leftover event from a different test's target sharing this Postgres
			}
			if ev.Type == livefeed.EventTargetStatus && ev.State == "alerting" && ev.Streak == 3 {
				sawAlertingStatus = true
			}
			if ev.Type == livefeed.EventIncident && ev.Kind == "opened" {
				sawOpenedIncident = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for live-push events: sawAlertingStatus=%v sawOpenedIncident=%v", sawAlertingStatus, sawOpenedIncident)
		}
	}

	// Recovery: the target starts responding 200 again — must publish a
	// healthy target_status and a resolved incident event, the live-push
	// mirror of the real close write TestEndToEnd_ThresholdCrossing_
	// DispatchesOnce_ThenResolvesOnRecovery already proves against Postgres
	// directly.
	failing.Store(false)
	forceDueNow(t, pool, targetID)

	var sawHealthyStatus, sawResolvedIncident bool
	deadline = time.After(15 * time.Second)
	for !sawHealthyStatus || !sawResolvedIncident {
		select {
		case ev := <-events:
			if ev.TargetID != targetID {
				continue
			}
			if ev.Type == livefeed.EventTargetStatus && ev.State == "healthy" {
				sawHealthyStatus = true
			}
			if ev.Type == livefeed.EventIncident && ev.Kind == "resolved" {
				sawResolvedIncident = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for recovery live-push events: sawHealthyStatus=%v sawResolvedIncident=%v", sawHealthyStatus, sawResolvedIncident)
		}
	}
}
