package alerting

import (
	"testing"
	"time"
)

func mkInput(targetID string, checkedAt time.Time, success bool) CheckResultInput {
	return CheckResultInput{
		TargetID:  targetID,
		CheckedAt: checkedAt,
		Success:   success,
		LatencyMS: 10,
	}
}

// TestRecordCheckResult_FirstFailureAdvancesStreakBelowThreshold proves the
// ordinary, non-duplicate path: one failure short of threshold is a real
// row, a real Evaluate call, streak=1/state=suspect, no incident.
func TestRecordCheckResult_FirstFailureAdvancesStreakBelowThreshold(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetWithSchedule(t, pool)

	result, err := RecordCheckResult(t.Context(), pool, mkInput(targetID, time.Now(), false), 3)
	if err != nil {
		t.Fatalf("RecordCheckResult: %v", err)
	}
	if !result.Inserted {
		t.Fatal("expected Inserted=true for a fresh (target_id, checked_at) pair")
	}
	if result.Streak != 1 || result.State != StateSuspect {
		t.Fatalf("expected streak=1 state=suspect, got streak=%d state=%s", result.Streak, result.State)
	}
	if result.Dispatch != nil {
		t.Fatalf("expected no dispatch below threshold, got %+v", result.Dispatch)
	}
	persistStreakState(t, pool, targetID, result.Streak, result.State)

	if count := countCheckResultsFor(t, pool, targetID); count != 1 {
		t.Fatalf("expected exactly 1 check_results row, got %d", count)
	}
	streak, state := fetchStreakState(t, pool, targetID)
	if streak != 1 || state != "suspect" {
		t.Fatalf("expected persisted streak=1 state=suspect, got streak=%d state=%s", streak, state)
	}
}

// TestRecordCheckResult_DuplicateCheckedAtIsANoOp is the OTLP
// duplicate-result idempotency proof docs/architecture/openapi.yaml's
// /v1/logs description requires at this exact layer: resubmitting the
// identical (target_id, checked_at) pair inserts no second check_results
// row and — the part that matters — never re-enters Evaluate a second
// time. Proven by driving a target right up to one check below the
// failure_threshold, then "resubmitting" that same failure: if Evaluate
// ran again, streak would double-increment to 2; the dedup gate must keep
// it at 1.
func TestRecordCheckResult_DuplicateCheckedAtIsANoOp(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetWithSchedule(t, pool)
	checkedAt := time.Now()

	first, err := RecordCheckResult(t.Context(), pool, mkInput(targetID, checkedAt, false), 3)
	if err != nil {
		t.Fatalf("first RecordCheckResult: %v", err)
	}
	if !first.Inserted || first.Streak != 1 {
		t.Fatalf("expected the first submission inserted with streak=1, got %+v", first)
	}
	persistStreakState(t, pool, targetID, first.Streak, first.State)

	// Resubmit the identical (target_id, checked_at) pair — a real network
	// retry of the same OTLP record, not a second, independent check.
	second, err := RecordCheckResult(t.Context(), pool, mkInput(targetID, checkedAt, false), 3)
	if err != nil {
		t.Fatalf("duplicate RecordCheckResult: %v", err)
	}
	if second.Inserted {
		t.Fatal("expected Inserted=false for a resubmitted (target_id, checked_at) pair")
	}
	// Streak must be passed through UNCHANGED from before this call, not
	// re-evaluated — if Evaluate had run again on the duplicate, this would
	// be 2, not 1.
	if second.Streak != 1 || second.State != StateSuspect {
		t.Fatalf("expected the duplicate to leave streak=1 state=suspect untouched, got streak=%d state=%s", second.Streak, second.State)
	}
	if second.Dispatch != nil {
		t.Fatalf("expected no dispatch from a duplicate resubmission, got %+v", second.Dispatch)
	}

	if count := countCheckResultsFor(t, pool, targetID); count != 1 {
		t.Fatalf("expected exactly 1 check_results row after a duplicate resubmission, got %d", count)
	}
	streak, state := fetchStreakState(t, pool, targetID)
	if streak != 1 || state != "suspect" {
		t.Fatalf("expected persisted streak=1 state=suspect (not double-incremented to 2), got streak=%d state=%s", streak, state)
	}
}

// TestRecordCheckResult_ThresholdCrossingOpensIncidentDispatch proves the
// shared write path reaches the identical ADR-0002 outcome
// TestEndToEnd_ThresholdCrossing_DispatchesOnce_ThenResolvesOnRecovery
// (scheduler package) proves through the scheduler's own lease-based
// path — three consecutive failures cross a threshold of 3 and open
// exactly one incident, with a "opened" DispatchRequest returned on the
// third.
func TestRecordCheckResult_ThresholdCrossingOpensIncidentDispatch(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetWithSchedule(t, pool)
	base := time.Now()

	var last Recorded
	for i := range 3 {
		var err error
		last, err = RecordCheckResult(t.Context(), pool, mkInput(targetID, base.Add(time.Duration(i)*time.Second), false), 3)
		if err != nil {
			t.Fatalf("RecordCheckResult iteration %d: %v", i, err)
		}
		// RecordCheckResult deliberately leaves persisting streak/state to
		// its caller (real callers fold it into a larger transaction that
		// also writes their own scheduling-specific columns) — mirror that
		// here so each iteration evaluates against the previous one's real
		// outcome, not a stale streak=0 read every time.
		persistStreakState(t, pool, targetID, last.Streak, last.State)
	}

	if last.State != StateAlerting || last.Streak != 3 {
		t.Fatalf("expected streak=3 state=alerting after crossing threshold, got streak=%d state=%s", last.Streak, last.State)
	}
	if last.Dispatch == nil || last.Dispatch.Kind != "opened" {
		t.Fatalf("expected an 'opened' dispatch on the threshold-crossing check, got %+v", last.Dispatch)
	}
	if open := countOpenIncidents(t, pool, targetID); open != 1 {
		t.Fatalf("expected exactly 1 open incident, got %d", open)
	}
}
