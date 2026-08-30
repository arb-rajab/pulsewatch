package alerting

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// CheckResultInput is one completed check outcome to record — the shared
// shape every source of a check result (the scheduler's own server-executed
// checks, and agent-reported OTLP results) feeds into the identical write
// path below (03-architecture.md: "no separate, lower-rigor path for
// agent-sourced data").
type CheckResultInput struct {
	TargetID                   string
	CheckedAt                  time.Time
	Success                    bool
	LatencyMS                  int
	StatusCode                 *int32
	FailureReason              *string
	BodyMatchFragment          *string
	BodyMatchFragmentTruncated bool
}

// Recorded is what RecordCheckResult produces.
type Recorded struct {
	// Inserted is false when (TargetID, CheckedAt) was already present in
	// check_results — docs/architecture/openapi.yaml's OTLP
	// duplicate-idempotency contract: a resubmission is a no-op that never
	// re-enters Evaluate a second time. Streak/State are always the value
	// to persist back to target_schedule regardless of Inserted — the
	// previously persisted value, unchanged, when it's a duplicate — so a
	// caller never needs its own branch to decide what to write.
	Inserted bool
	Streak   int
	State    State
	Dispatch *DispatchRequest
}

// recordDB is the minimal surface RecordCheckResult needs — satisfied by
// both *pgxpool.Pool and pgx.Tx, the same shape incidentDB (incident.go)
// already uses, since a caller runs this inside its own transaction
// alongside whatever scheduling-specific columns (next_due_at, lease_owner,
// ...) it still has to write in the same commit.
type recordDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// RecordCheckResult is the single write path every completed check outcome
// goes through, regardless of where it came from: it inserts the
// check_results row (idempotently, via the ON CONFLICT below against the
// UNIQUE (target_id, checked_at) constraint 04-data-model.md's
// check_results migration carries), reads the previously persisted
// streak/state FOR UPDATE, evaluates ADR-0002's transition function, and
// applies whatever incidents-table action that transition calls for — all
// against whatever db is handed in, so a caller can fold it into a larger
// transaction that also writes its own scheduling-specific columns
// (scheduler.releaseAndRecord's next_due_at/lease clearing; the OTLP
// ingestion path has no such columns to write).
//
// A duplicate (target_id, checked_at) pair short-circuits before Evaluate
// is ever called — docs/architecture/openapi.yaml's /v1/logs description:
// "the dedup check must gate entry to the transition function, not just
// the final write," since a streak increment isn't reversible after the
// fact even if a later guarded incidents write would still prevent a
// duplicate dispatch.
func RecordCheckResult(ctx context.Context, db recordDB, in CheckResultInput, failureThreshold int) (Recorded, error) {
	const insertResult = `
INSERT INTO check_results
    (target_id, checked_at, success, latency_ms, status_code, failure_reason, body_match_fragment, body_match_fragment_truncated)
VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (target_id, checked_at) DO NOTHING`

	tag, err := db.Exec(ctx, insertResult,
		in.TargetID, in.CheckedAt, in.Success, in.LatencyMS,
		in.StatusCode, in.FailureReason, in.BodyMatchFragment, in.BodyMatchFragmentTruncated,
	)
	if err != nil {
		return Recorded{}, fmt.Errorf("insert check_results for target %s: %w", in.TargetID, err)
	}

	// FOR UPDATE: whichever caller holds this row exclusively for the
	// duration of its own transaction (the scheduler's ADR-0001 lease, or
	// the OTLP handler's per-target-assignment ownership) — cheap insurance
	// that Evaluate always runs against the value this same transaction is
	// about to overwrite, never a stale read.
	var previousStreak int
	var previousStateRaw string
	const selectSchedule = `SELECT streak, state FROM target_schedule WHERE target_id = $1::uuid FOR UPDATE`
	if scanErr := db.QueryRow(ctx, selectSchedule, in.TargetID).Scan(&previousStreak, &previousStateRaw); scanErr != nil {
		return Recorded{}, fmt.Errorf("read target_schedule streak/state for target %s: %w", in.TargetID, scanErr)
	}

	if tag.RowsAffected() == 0 {
		// Duplicate (target_id, checked_at): pass previous streak/state
		// through unchanged, exactly as ADR-0002 already does generically
		// for a period Evaluate is never invoked for.
		return Recorded{Inserted: false, Streak: previousStreak, State: State(previousStateRaw)}, nil
	}

	transition := Evaluate(previousStreak, State(previousStateRaw), in.Success, failureThreshold)
	dispatchReq, applyErr := ApplyIncidentAction(ctx, db, in.TargetID, transition.Action)
	if applyErr != nil {
		return Recorded{}, fmt.Errorf("apply incident action for target %s: %w", in.TargetID, applyErr)
	}

	return Recorded{Inserted: true, Streak: transition.Streak, State: transition.State, Dispatch: dispatchReq}, nil
}
