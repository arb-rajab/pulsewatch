package scheduler

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestClaimLease_ExactlyOneWinnerUnderRealConcurrency is the non-negotiable
// proof ADR-0001 exists for: never two claimants win the same lease, even
// under genuine race conditions. This uses real, independent goroutines,
// each holding its own connection out of a real pgxpool.Pool (so each
// claimant's UPDATE genuinely reaches Postgres as an independent backend,
// not serialized by sharing one Go-side connection or transaction), all
// released to race for the identical target_id's lease at the same instant
// via a shared start barrier — not a single-threaded test asserting the SQL
// "should" be atomic.
//
// Real OS-process-level concurrency (bookslot's proc_open pattern) is not
// needed here: the property under test is Postgres row-level atomicity of
// the UPDATE ... RETURNING statement itself, which is enforced by Postgres
// regardless of which process issues the request. Concurrent goroutines
// each round-tripping through a distinct pool connection already exercise a
// genuine race against the real external resource (Postgres) that the
// claim's WHERE clause has to resolve correctly — an OS-process boundary
// would add nothing this test doesn't already prove.
func TestClaimLease_ExactlyOneWinnerUnderRealConcurrency(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTarget(t, pool, "http://example.invalid/concurrency", defaultTestTargetOpts())

	const claimants = 20
	var (
		wg        sync.WaitGroup
		successes int64
		start     = make(chan struct{})
	)

	errs := make([]error, claimants)
	claimed := make([]bool, claimants)

	for i := range claimants {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release every goroutine at (as close as Go allows to) the same instant
			ok, err := claimLease(t.Context(), pool, targetID, mustParseOwner(i), 30*time.Second)
			claimed[i] = ok
			errs[i] = err
			if ok {
				atomic.AddInt64(&successes, 1)
			}
		}(i)
	}

	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("claimant %d: unexpected error: %v", i, err)
		}
	}

	if successes != 1 {
		t.Fatalf("expected exactly 1 winner among %d concurrent claimants, got %d", claimants, successes)
	}

	leaseOwner, leaseExpiresAt, _ := fetchScheduleRow(t, pool, targetID)
	if leaseOwner == nil {
		t.Fatal("expected lease_owner to be set after a successful claim")
	}
	if leaseExpiresAt == nil || !leaseExpiresAt.After(time.Now()) {
		t.Fatalf("expected lease_expires_at in the future, got %v", leaseExpiresAt)
	}

	var winner int
	winnerFound := false
	for i, ok := range claimed {
		if ok {
			winner = i
			winnerFound = true
			break
		}
	}
	if !winnerFound {
		t.Fatal("no winner recorded despite successes == 1")
	}
	if *leaseOwner != mustParseOwner(winner) {
		t.Fatalf("lease_owner %q does not match the recorded winner %q", *leaseOwner, mustParseOwner(winner))
	}
}
