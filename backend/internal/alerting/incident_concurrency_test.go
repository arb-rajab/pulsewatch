package alerting

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestOpenIncident_ExactlyOneWinnerUnderRealConcurrency is the non-negotiable
// proof ADR-0002 exists for: even when multiple real, independent
// transactions attempt to cross the failure threshold for the identical
// target at the same instant — e.g. two near-simultaneous check-result
// evaluations racing to open an incident for one target — exactly one
// incidents row is opened, so exactly one dispatch is ever triggered. This
// uses real, independent goroutines, each through its own transaction out
// of a real pgxpool.Pool against a real Postgres, released at a shared
// start barrier — not a single-threaded test asserting the SQL "should" be
// atomic. Mirrors scheduler.TestClaimLease_ExactlyOneWinnerUnderRealConcurrency's
// own methodology exactly.
func TestOpenIncident_ExactlyOneWinnerUnderRealConcurrency(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)

	dispatched := raceOpenIncident(t, pool, targetID, 20)

	if dispatched != 1 {
		t.Fatalf("expected exactly 1 dispatch among 20 concurrent OpenIncident calls, got %d", dispatched)
	}
	if open := countOpenIncidents(t, pool, targetID); open != 1 {
		t.Fatalf("expected exactly 1 open incidents row, got %d", open)
	}
}

// openIncidentNaive is a deliberately naive, two-round-trip
// reimplementation of ADR-0002's explicitly-rejected "Option A" ("an ad hoc
// inline counter check... If streak reaches threshold and no incident is
// currently known to be open, open one" — no atomically-conditional write):
// a plain existence check, then a plain INSERT, as two separate statements
// with nothing tying them together. Used only by the test below.
//
// OpenIncident's own real SQL is a single INSERT...WHERE NOT EXISTS
// statement — one Postgres round trip — whose TOCTOU race window against
// another such statement is typically microseconds, too narrow to reproduce
// deterministically even under real concurrency. Option A's shape is the
// same class of race with a much wider, easily-forced window (two
// statements, with real work — and here, a deliberate small delay — able to
// happen between them), which is exactly why ADR-0002 rejected it rather
// than accepting "the caller only calls this once" as good enough. Racing
// this naive shape is what makes the "database constraint, not just careful
// code" property demonstrable on demand, instead of dependent on timing luck.
func openIncidentNaive(ctx context.Context, pool *pgxpool.Pool, targetID string) (opened bool, err error) {
	var alreadyOpen bool
	const checkStmt = `SELECT EXISTS(SELECT 1 FROM incidents WHERE target_id = $1::uuid AND closed_at IS NULL)`
	if err := pool.QueryRow(ctx, checkStmt, targetID).Scan(&alreadyOpen); err != nil {
		return false, err
	}
	if alreadyOpen {
		return false, nil
	}

	time.Sleep(20 * time.Millisecond) // deliberately widen the check-then-insert gap

	const insertStmt = `INSERT INTO incidents (target_id, opened_at) VALUES ($1::uuid, now())`
	if _, err := pool.Exec(ctx, insertStmt, targetID); err != nil {
		// Only reachable if the partial unique index is present (restored):
		// then this naive shape's second insert is finally rejected by the
		// database itself, exactly as ADR-0002 says a real bug bypassing the
		// application guard would be.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == postgresUniqueViolation {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// raceOpenIncidentNaive runs n real, independent openIncidentNaive calls
// for the identical targetID, released at a shared start barrier, and
// returns how many actually inserted a row.
func raceOpenIncidentNaive(t *testing.T, pool *pgxpool.Pool, targetID string, n int) int64 {
	t.Helper()

	var (
		wg    sync.WaitGroup
		count int64
		start = make(chan struct{})
	)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			opened, err := openIncidentNaive(t.Context(), pool, targetID)
			if err != nil {
				t.Errorf("openIncidentNaive: %v", err)
				return
			}
			if opened {
				atomic.AddInt64(&count, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	return count
}

// TestOpenIncident_WithoutUniqueIndex_DuplicatesOccur_ThenRestored is the
// "has real teeth" proof this session's own standard requires: temporarily
// drop the incidents_one_open_per_target partial unique index, race
// ADR-0002's explicitly-rejected "Option A" shape (openIncidentNaive above)
// against it, and confirm duplicates actually occur — proving a
// database-level constraint, not an application-level check, is what
// ADR-0002 actually depends on for "exactly one." The index is then
// restored — inline, so this same test immediately reproves the real
// OpenIncident's exactly-once guarantee is back, not just trusted to be —
// with a t.Cleanup safety net in case the test fails or panics first. No
// other test in this package is exposed to the weakened schema at any
// point: Go runs this package's tests sequentially (no t.Parallel anywhere
// in it).
func TestOpenIncident_WithoutUniqueIndex_DuplicatesOccur_ThenRestored(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)

	restoreIndex := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Duplicates from the race below must be cleared first: CREATE
		// UNIQUE INDEX validates existing data and would otherwise fail
		// against the very rows this test just proved can exist without it.
		if _, err := pool.Exec(ctx, `DELETE FROM incidents WHERE target_id = $1::uuid`, targetID); err != nil {
			t.Fatalf("clear duplicate incidents before restoring index: %v", err)
		}
		if _, err := pool.Exec(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS incidents_one_open_per_target ON incidents (target_id) WHERE closed_at IS NULL`); err != nil {
			t.Fatalf("restore partial unique index: %v", err)
		}
	}
	t.Cleanup(restoreIndex) // safety net: runs even if this test fails/panics before the inline restore below

	if _, err := pool.Exec(t.Context(), `DROP INDEX incidents_one_open_per_target`); err != nil {
		t.Fatalf("drop partial unique index: %v", err)
	}

	opened := raceOpenIncidentNaive(t, pool, targetID, 20)
	if opened <= 1 {
		t.Fatalf("expected the missing database constraint to let ADR-0002's rejected Option A shape create duplicates (>1) — got %d, meaning this race did not reproduce and this test would not catch a real regression", opened)
	}
	t.Logf("without the partial unique index, %d of 20 concurrent naive check-then-insert attempts opened an incident for one target — this is exactly the duplicate-alert bug ADR-0002's Option A discussion rejects, and exactly what the partial unique index exists to make impossible", opened)

	// Restore now (not only at t.Cleanup time) so this same test proves the
	// guarantee comes back, rather than trusting that it will.
	restoreIndex()

	freshTargetID := insertTestTargetRow(t, pool)
	dispatchedAfterRestore := raceOpenIncident(t, pool, freshTargetID, 20) // the real, guarded OpenIncident
	if dispatchedAfterRestore != 1 {
		t.Fatalf("expected exactly 1 dispatch via the real OpenIncident after restoring the partial unique index, got %d", dispatchedAfterRestore)
	}
}

// TestCloseIncident_ExactlyOneWinnerUnderRealConcurrency is the same proof
// on the other edge transition: once an incident is open, at most one
// concurrent CloseIncident call may actually close it and get back a
// resolution dispatch.
func TestCloseIncident_ExactlyOneWinnerUnderRealConcurrency(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)

	openReq, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil {
		t.Fatalf("OpenIncident (setup): %v", err)
	}
	if openReq == nil {
		t.Fatal("OpenIncident (setup): expected a dispatch request for the first open")
	}

	const claimants = 20
	var (
		wg    sync.WaitGroup
		count int64
		start = make(chan struct{})
	)

	for i := 0; i < claimants; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req, closeErr := CloseIncident(t.Context(), pool, targetID)
			if closeErr != nil {
				t.Errorf("CloseIncident: %v", closeErr)
				return
			}
			if req != nil {
				atomic.AddInt64(&count, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if count != 1 {
		t.Fatalf("expected exactly 1 dispatch among %d concurrent CloseIncident calls, got %d", claimants, count)
	}
	if open := countOpenIncidents(t, pool, targetID); open != 0 {
		t.Fatalf("expected 0 open incidents after close, got %d", open)
	}
}

// raceOpenIncident runs n real, independent transactions, each attempting
// OpenIncident for the identical targetID, released at a shared start
// barrier, and returns how many produced a non-nil dispatch request.
func raceOpenIncident(t *testing.T, pool *pgxpool.Pool, targetID string, n int) int64 {
	t.Helper()

	var (
		wg    sync.WaitGroup
		count int64
		start = make(chan struct{})
	)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			tx, err := pool.Begin(t.Context())
			if err != nil {
				t.Errorf("begin tx: %v", err)
				return
			}
			defer func() { _ = tx.Rollback(t.Context()) }()

			req, err := OpenIncident(t.Context(), tx, targetID)
			if err != nil {
				t.Errorf("OpenIncident: %v", err)
				return
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Errorf("commit: %v", err)
				return
			}
			if req != nil {
				atomic.AddInt64(&count, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	return count
}
