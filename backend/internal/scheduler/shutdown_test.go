package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestGracefulShutdown_InFlightCheckFinishesNaturally proves ADR-0004
// outcome 1: a check already claimed when shutdown begins is not yanked —
// it keeps running under its own per-check timeout (never rootCtx), so a
// slow-but-healthy target still gets to finish and record a real result
// during the graceful-drain window.
func TestGracefulShutdown_InFlightCheckFinishesNaturally(t *testing.T) {
	pool := testPool(t)

	respond := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-respond:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	opts := defaultTestTargetOpts()
	opts.timeoutSeconds = 5
	targetID := insertTestTarget(t, pool, srv.URL, opts)

	cfg := DefaultConfig()
	cfg.WorkerPoolSize = 2
	cfg.TickInterval = 30 * time.Millisecond
	cfg.HardShutdownDeadline = 3 * time.Second // generous: the check finishes long before this

	sched, err := New(pool, cfg, nopLogger())
	if err != nil {
		t.Fatalf("construct scheduler: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- sched.Run(ctx) }()

	waitForCondition(t, 2*time.Second, func() bool {
		owner, _, _ := fetchScheduleRow(t, pool, targetID)
		return owner != nil
	})

	cancel() // SIGTERM arrives mid-check

	time.Sleep(100 * time.Millisecond) // shutdown is in progress; the check must not have been aborted by it
	close(respond)                     // now let the handler finish naturally

	select {
	case runErr := <-runDone:
		if runErr != nil {
			t.Fatalf("Run returned error: %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not shut down in time")
	}

	if count := countCheckResults(t, pool, targetID); count != 1 {
		t.Fatalf("expected the in-flight check to finish naturally and record exactly 1 result, got %d", count)
	}
	leaseOwner, _, _ := fetchScheduleRow(t, pool, targetID)
	if leaseOwner != nil {
		t.Fatalf("expected lease released after natural completion, got %v", *leaseOwner)
	}
}

// TestGracefulShutdown_ForceCancelAtHardDeadline_LeavesLeaseAbandonedNotCorrupted
// proves ADR-0004 outcome 3: a check still running at the hard shutdown
// deadline is force-canceled, but this is safe by construction — no result
// is recorded (never a half-processed or duplicate one), and the lease is
// simply abandoned (left exactly as the claim set it, not cleared, not
// corrupted) so it self-heals via the ordinary claim predicate once it
// actually expires — the same mechanism restart-recovery uses, not a
// special case for shutdown.
func TestGracefulShutdown_ForceCancelAtHardDeadline_LeavesLeaseAbandonedNotCorrupted(t *testing.T) {
	pool := testPool(t)

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never responds on its own — only a client-side abort ends this request
	}))
	defer srv.Close()

	opts := defaultTestTargetOpts()
	opts.timeoutSeconds = 2 // long enough that the per-check timeout itself never fires in this test
	targetID := insertTestTarget(t, pool, srv.URL, opts)

	cfg := DefaultConfig()
	cfg.WorkerPoolSize = 2
	cfg.TickInterval = 30 * time.Millisecond
	cfg.LeaseSafetyMargin = 500 * time.Millisecond
	cfg.HardShutdownDeadline = 200 * time.Millisecond // short — forces outcome 3, not outcome 1 or 2

	sched, err := New(pool, cfg, nopLogger())
	if err != nil {
		t.Fatalf("construct scheduler: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- sched.Run(ctx) }()

	waitForCondition(t, 2*time.Second, func() bool {
		owner, _, _ := fetchScheduleRow(t, pool, targetID)
		return owner != nil
	})
	claimedOwner, claimedExpiry, _ := fetchScheduleRow(t, pool, targetID)

	cancel() // SIGTERM while the check is mid-flight and will never respond on its own

	select {
	case runErr := <-runDone:
		if runErr != nil {
			t.Fatalf("Run returned error: %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not force-shut-down within a bounded time")
	}

	if count := countCheckResults(t, pool, targetID); count != 0 {
		t.Fatalf("expected no check_results row for a force-canceled check, got %d", count)
	}

	leaseOwner, leaseExpiresAt, _ := fetchScheduleRow(t, pool, targetID)
	if leaseOwner == nil || *leaseOwner != *claimedOwner {
		t.Fatalf("expected the original lease to remain in place untouched (abandoned, not cleared), got %v want %v", leaseOwner, claimedOwner)
	}
	if leaseExpiresAt == nil || !leaseExpiresAt.Equal(*claimedExpiry) {
		t.Fatalf("expected lease_expires_at unchanged from claim time (release never ran), got %v want %v", leaseExpiresAt, claimedExpiry)
	}

	// And the abandoned lease is reclaimable via the ordinary claim
	// predicate once it actually expires — no special shutdown-recovery path.
	waitForCondition(t, 5*time.Second, func() bool {
		claimed, claimErr := claimLease(t.Context(), pool, targetID, "next-process", 5*time.Second)
		if claimErr != nil {
			t.Fatalf("claimLease: %v", claimErr)
		}
		return claimed
	})
}
