package scheduler

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/alerting"
)

// testEncryptionKey mirrors alerting's own testEncryptionKey exactly (a
// fixed, non-secret 32-byte key, sequential bytes, not randomly generated).
// Kept identical across packages deliberately: alert_channels is a global
// table, and LoadChannels decrypts every row in it with whatever key it's
// given — two packages' tests using different random keys against the same
// shared test Postgres would spuriously fail to decrypt each other's
// leftover rows.
var testEncryptionKey = []byte{
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
	16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31,
}

// spyDispatcher records every Dispatch call it receives, keyed by channel
// id, so tests can assert on calls for a channel they themselves created
// without being thrown off by unrelated leftover alert_channels rows in the
// shared test database (see insertTestAlertChannel below).
type spyDispatcher struct {
	mu    sync.Mutex
	calls []spyCall
}

type spyCall struct {
	channelID string
	req       alerting.DispatchRequest
}

func (s *spyDispatcher) Dispatch(_ context.Context, channel alerting.Channel, req alerting.DispatchRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, spyCall{channelID: channel.ID, req: req})
	return nil
}

func (s *spyDispatcher) callsFor(channelID string) []alerting.DispatchRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []alerting.DispatchRequest
	for _, c := range s.calls {
		if c.channelID == channelID {
			out = append(out, c.req)
		}
	}
	return out
}

// newToggleServer returns an HTTP test server whose success/failure can be
// flipped live via the returned *atomic.Bool (true = respond 500).
func newToggleServer() (*httptest.Server, *atomic.Bool) {
	failing := &atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	return srv, failing
}

// forceDueNow pushes next_due_at back into the past so the next tick claims
// this target immediately, without waiting out its real interval_seconds
// (schema-constrained to >=10s) between forced check cycles.
func forceDueNow(t *testing.T, pool *pgxpool.Pool, targetID string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `UPDATE target_schedule SET next_due_at = now() - INTERVAL '1 second' WHERE target_id = $1::uuid`, targetID); err != nil {
		t.Fatalf("force due: %v", err)
	}
}

func fetchAlertState(t *testing.T, pool *pgxpool.Pool, targetID string) (streak int, state string) {
	t.Helper()
	if err := pool.QueryRow(t.Context(), `SELECT streak, state FROM target_schedule WHERE target_id = $1::uuid`, targetID).Scan(&streak, &state); err != nil {
		t.Fatalf("fetch alert state: %v", err)
	}
	return streak, state
}

func countIncidents(t *testing.T, pool *pgxpool.Pool, targetID string) (open, closed int) {
	t.Helper()
	err := pool.QueryRow(t.Context(), `
SELECT count(*) FILTER (WHERE closed_at IS NULL), count(*) FILTER (WHERE closed_at IS NOT NULL)
FROM incidents WHERE target_id = $1::uuid`, targetID,
	).Scan(&open, &closed)
	if err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	return open, closed
}

// insertTestAlertChannel creates a real alert_channels row encrypted with
// testEncryptionKey. Cleanup is best-effort, matching insertTestTarget's own
// accepted looseness (an alert_dispatches row referencing this channel
// blocks the delete; harmless for test correctness, since every assertion
// in this file scopes by this channel's own id).
func insertTestAlertChannel(t *testing.T, pool *pgxpool.Pool, destination string) string {
	t.Helper()
	encrypted, err := alerting.EncryptDestination(destination, testEncryptionKey)
	if err != nil {
		t.Fatalf("encrypt test destination: %v", err)
	}
	var channelID string
	err = pool.QueryRow(t.Context(), `
INSERT INTO alert_channels (type, destination_encrypted) VALUES ('webhook', $1) RETURNING id::text`, encrypted,
	).Scan(&channelID)
	if err != nil {
		t.Fatalf("insert test alert_channel: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM alert_channels WHERE id = $1::uuid`, channelID)
	})
	return channelID
}

// TestEndToEnd_BlipBelowThreshold_NoIncidentNoDispatch is the real,
// end-to-end proof of US-006/ADR-0002's blip suppression: two consecutive
// failures against the default failure_threshold of 3 (targets.failure_threshold
// DEFAULT 3, NFR-011) must never reach Alerting and must never dispatch —
// driven through the real Scheduler.Run tick/claim/execute/release cycle
// Session 5 built, not the transition function in isolation.
//
// This file's waitForCondition calls poll with a 15s budget (widened from
// Session 6's original 2s during Session 7's own verification): a direct
// A/B rerun of this exact test against the unmodified Session 6 code,
// interleaved with Session 7's own code, reproduced the identical
// intermittent "condition not met" timeout on BOTH versions — confirming
// this is a pre-existing polling-margin sensitivity, not a Session 7
// regression. `docker stats` during this session's own verification run
// showed the actual cause: this developer's shared Docker Desktop VM (a
// 5.7GB-total-memory host) was concurrently running several unrelated,
// heavy, already-started containers from this developer's other projects
// (a Laravel app, MySQL, multiple Postgres/Redis/MinIO instances, one
// container alone measured at 60% CPU) — real, external resource
// contention this session's own tests have no way to avoid and no
// business touching (ground rules: don't touch other projects). Even
// running this package in complete isolation (no other pulsewatch package
// concurrently) still measured one iteration at 12.9s under that load.
// GitHub Actions' actual CI runner has no such contention (a dedicated,
// single-tenant runner) — this widened budget is pure headroom for a
// noisy local verification host, not evidence the underlying property is
// actually slow. Widening it is a test-robustness fix below ADR-0002's
// own level of specification (the ADR fixes the alert-suppression
// semantics being proved, not this test file's polling interval), exactly
// like Session 6's own "OpenIncident's nested transaction" fix was
// implementation-level rather than a design change.
func TestEndToEnd_BlipBelowThreshold_NoIncidentNoDispatch(t *testing.T) {
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
	spy := &spyDispatcher{}
	sched.SetDispatcher(spy)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- sched.Run(ctx) }()

	waitForCondition(t, 15*time.Second, func() bool { return countCheckResults(t, pool, targetID) >= 1 })
	forceDueNow(t, pool, targetID)
	waitForCondition(t, 15*time.Second, func() bool { return countCheckResults(t, pool, targetID) >= 2 })

	// Give any (incorrect) dispatch a moment to happen before asserting its absence.
	time.Sleep(100 * time.Millisecond)

	cancel()
	select {
	case runErr := <-runDone:
		if runErr != nil {
			t.Fatalf("Run returned error: %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not shut down in time")
	}

	if count := countCheckResults(t, pool, targetID); count != 2 {
		t.Fatalf("expected exactly 2 check_results rows, got %d", count)
	}
	streak, state := fetchAlertState(t, pool, targetID)
	if streak != 2 || state != "suspect" {
		t.Fatalf("expected streak=2 state=suspect after 2 failures below threshold 3, got streak=%d state=%s", streak, state)
	}
	if open, closed := countIncidents(t, pool, targetID); open != 0 || closed != 0 {
		t.Fatalf("expected zero incidents for a blip below threshold, got open=%d closed=%d", open, closed)
	}
}

// TestEndToEnd_ThresholdCrossing_DispatchesOnce_ThenResolvesOnRecovery is
// the real, end-to-end proof of the other two required properties together:
// the third consecutive failure crosses the default threshold (3),
// producing exactly one "opened" dispatch to a real configured channel; a
// subsequent successful check resolves it, producing exactly one "resolved"
// dispatch — all driven through the real scheduler pipeline (tick -> claim
// -> execute -> release -> evaluate -> dispatch), not any component in
// isolation.
func TestEndToEnd_ThresholdCrossing_DispatchesOnce_ThenResolvesOnRecovery(t *testing.T) {
	pool := testPool(t)
	srv, failing := newToggleServer()
	defer srv.Close()
	failing.Store(true)

	// Scheduler.New reads this at construction time to decrypt alert_channels
	// rows for dispatch (alerting.EncryptionKeyFromEnv) — must be set before
	// New is called below.
	t.Setenv("ALERT_CHANNEL_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(testEncryptionKey))

	opts := defaultTestTargetOpts()
	opts.timeoutSeconds = 2
	targetID := insertTestTarget(t, pool, srv.URL, opts)
	channelID := insertTestAlertChannel(t, pool, "https://hooks.example.invalid/T00/B00/end-to-end-lifecycle-test")

	cfg := DefaultConfig()
	cfg.WorkerPoolSize = 2
	cfg.TickInterval = 20 * time.Millisecond
	cfg.HardShutdownDeadline = 2 * time.Second

	sched, err := New(pool, cfg, nopLogger())
	if err != nil {
		t.Fatalf("construct scheduler: %v", err)
	}
	spy := &spyDispatcher{}
	sched.SetDispatcher(spy)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- sched.Run(ctx) }()

	// Three consecutive failures: the third crosses the default threshold (3).
	for i := 1; i <= 3; i++ {
		want := i
		waitForCondition(t, 15*time.Second, func() bool { return countCheckResults(t, pool, targetID) >= want })
		if i < 3 {
			forceDueNow(t, pool, targetID)
		}
	}

	waitForCondition(t, 15*time.Second, func() bool {
		_, state := fetchAlertState(t, pool, targetID)
		return state == "alerting"
	})
	if streak, state := fetchAlertState(t, pool, targetID); streak != 3 || state != "alerting" {
		t.Fatalf("expected streak=3 state=alerting after crossing the threshold, got streak=%d state=%s", streak, state)
	}
	if open, closed := countIncidents(t, pool, targetID); open != 1 || closed != 0 {
		t.Fatalf("expected exactly 1 open incident, got open=%d closed=%d", open, closed)
	}

	waitForCondition(t, 15*time.Second, func() bool { return len(spy.callsFor(channelID)) == 1 })
	if kind := spy.callsFor(channelID)[0].Kind; kind != "opened" {
		t.Fatalf("expected the one dispatch to be kind=opened, got %s", kind)
	}
	waitForCondition(t, 15*time.Second, func() bool {
		var confirmed bool
		err := pool.QueryRow(t.Context(), `
SELECT delivery_confirmed FROM alert_dispatches
WHERE alert_channel_id = $1::uuid AND kind = 'opened'`, channelID,
		).Scan(&confirmed)
		return err == nil && confirmed
	})

	// Recovery: the target starts responding 200 again.
	failing.Store(false)
	forceDueNow(t, pool, targetID)
	waitForCondition(t, 15*time.Second, func() bool { return countCheckResults(t, pool, targetID) >= 4 })

	waitForCondition(t, 15*time.Second, func() bool {
		_, state := fetchAlertState(t, pool, targetID)
		return state == "healthy"
	})
	if streak, state := fetchAlertState(t, pool, targetID); streak != 0 || state != "healthy" {
		t.Fatalf("expected streak=0 state=healthy after recovery, got streak=%d state=%s", streak, state)
	}
	if open, closed := countIncidents(t, pool, targetID); open != 0 || closed != 1 {
		t.Fatalf("expected the incident closed (0 open, 1 closed), got open=%d closed=%d", open, closed)
	}

	waitForCondition(t, 15*time.Second, func() bool { return len(spy.callsFor(channelID)) == 2 })
	if kind := spy.callsFor(channelID)[1].Kind; kind != "resolved" {
		t.Fatalf("expected the second dispatch to be kind=resolved, got %s", kind)
	}
	waitForCondition(t, 15*time.Second, func() bool {
		var confirmed bool
		err := pool.QueryRow(t.Context(), `
SELECT delivery_confirmed FROM alert_dispatches
WHERE alert_channel_id = $1::uuid AND kind = 'resolved'`, channelID,
		).Scan(&confirmed)
		return err == nil && confirmed
	})

	cancel()
	select {
	case runErr := <-runDone:
		if runErr != nil {
			t.Fatalf("Run returned error: %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not shut down in time")
	}
}
