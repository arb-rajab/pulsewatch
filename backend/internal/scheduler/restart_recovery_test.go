package scheduler

import (
	"context"
	"testing"
	"time"
)

// TestClaimLease_ReclaimsLeaseAbandonedByAnotherOwner isolates the exact
// predicate ADR-0001's restart recovery depends on: a lease whose
// lease_expires_at is in the past is claimable regardless of which owner
// wrote it — "no separate recovery query," just the same WHERE clause every
// other claim uses.
func TestClaimLease_ReclaimsLeaseAbandonedByAnotherOwner(t *testing.T) {
	pool := testPool(t)

	crashedOwner := "old-process-crashed"
	pastExpiry := time.Now().Add(-1 * time.Minute)
	opts := defaultTestTargetOpts()
	opts.leaseOwner = &crashedOwner
	opts.leaseExpiresAt = &pastExpiry
	targetID := insertTestTarget(t, pool, "http://example.invalid/restart", opts)

	claimed, err := claimLease(t.Context(), pool, targetID, "new-process", 30*time.Second)
	if err != nil {
		t.Fatalf("claimLease: %v", err)
	}
	if !claimed {
		t.Fatal("expected the abandoned lease to be claimable by a new owner")
	}

	leaseOwner, _, _ := fetchScheduleRow(t, pool, targetID)
	if leaseOwner == nil || *leaseOwner != "new-process" {
		t.Fatalf("expected lease_owner = new-process, got %v", leaseOwner)
	}
}

// TestRestartRecovery_SameTickLogicReclaimsStaleLease is the literal,
// end-to-end proof: it creates a stale/abandoned lease simulating a crash
// mid-check, then runs Scheduler.Run — the exact same tick/claim/execute/
// release logic that governs every ordinary tick, never a special-cased
// "recover" function (grep this package: there is no Recover, no
// RunRecovery, nothing but Run and the tick it calls on every interval) —
// and confirms the stale lease is correctly reclaimed, the check runs
// again, and no duplicate check_results row is produced.
func TestRestartRecovery_SameTickLogicReclaimsStaleLease(t *testing.T) {
	pool := testPool(t)
	srv := upTarget()
	defer srv.Close()

	crashedOwner := "old-process-crashed"
	pastExpiry := time.Now().Add(-1 * time.Minute)
	opts := defaultTestTargetOpts()
	opts.leaseOwner = &crashedOwner
	opts.leaseExpiresAt = &pastExpiry
	targetID := insertTestTarget(t, pool, srv.URL, opts)

	cfg := DefaultConfig()
	cfg.WorkerPoolSize = 2
	cfg.TickInterval = 50 * time.Millisecond
	cfg.HardShutdownDeadline = 2 * time.Second

	sched, err := New(pool, cfg, nopLogger())
	if err != nil {
		t.Fatalf("construct scheduler: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- sched.Run(ctx) }()

	waitForCondition(t, 2*time.Second, func() bool {
		return countCheckResults(t, pool, targetID) >= 1
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

	if count := countCheckResults(t, pool, targetID); count != 1 {
		t.Fatalf("expected exactly 1 check_results row (no duplicate from the stale lease), got %d", count)
	}

	leaseOwner, leaseExpiresAt, nextDueAt := fetchScheduleRow(t, pool, targetID)
	if leaseOwner != nil {
		t.Fatalf("expected lease released (nil owner) after recovery, got %v", *leaseOwner)
	}
	if leaseExpiresAt != nil {
		t.Fatalf("expected lease_expires_at cleared after recovery, got %v", *leaseExpiresAt)
	}
	if !nextDueAt.After(time.Now()) {
		t.Fatalf("expected next_due_at advanced into the future after recovery, got %v", nextDueAt)
	}
}
