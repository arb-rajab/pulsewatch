package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/alerting"
)

// pgxQuerier is the minimal surface claimLease needs — satisfied by both
// *pgxpool.Pool and pgx.Tx, so the same claim statement works whether called
// standalone or inside a larger transaction.
type pgxQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// claimLease is ADR-0001's atomic conditional claim, verbatim in predicate
// and columns — only the interval literal's parameter encoding differs
// (seconds-as-float cast to INTERVAL, rather than a driver-native interval
// type) to fit pgx's parameter binding. Zero rows (claimed == false) means
// another owner already holds a live lease, or the target is no longer due:
// a normal, silent skip per ADR-0001/ADR-0004, never an error.
func claimLease(ctx context.Context, db pgxQuerier, targetID, ownerID string, leaseDuration time.Duration) (claimed bool, err error) {
	const stmt = `
UPDATE target_schedule
SET lease_owner = $1,
    lease_expires_at = now() + ($2 * INTERVAL '1 second')
WHERE target_id = $3::uuid
  AND next_due_at <= now()
  AND (lease_expires_at IS NULL OR lease_expires_at < now())
RETURNING target_id::text`

	var returned string
	scanErr := db.QueryRow(ctx, stmt, ownerID, leaseDuration.Seconds(), targetID).Scan(&returned)
	if scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("claim lease for target %s: %w", targetID, scanErr)
	}
	return true, nil
}

// releaseAndRecord is ADR-0001's release plus ADR-0002's transition,
// deliberately one transaction per both ADRs' own design ("Both ADR-0001's
// lease-release write and this ADR's transition write happen in the same
// transaction as the check-result insert" — ADR-0002): it records the check
// result, reads the target's previously persisted streak/state, evaluates
// ADR-0002's pure transition function, applies the guarded incidents write
// that transition calls for (if any), advances next_due_at/last_checked_at,
// clears the lease, and persists the new streak/state — all committed
// together. A crash anywhere before commit cannot be observed as a distinct,
// inconsistent state: the lease simply expires and the next tick re-claims
// and re-runs the check from the last successfully committed streak/state,
// which is what ADR-0001 and ADR-0002 both already require independently.
//
// The returned *alerting.DispatchRequest is non-nil only when the
// commit above actually succeeded and the incidents write inside it
// returned a row — the caller (Scheduler.handleJob) dispatches
// notifications only then, never speculatively before commit.
func releaseAndRecord(ctx context.Context, pool *pgxpool.Pool, job CheckJob, checkedAt time.Time, outcome checkOutcome) (*alerting.DispatchRequest, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin release transaction for target %s: %w", job.TargetID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	const insertResult = `
INSERT INTO check_results
    (target_id, checked_at, success, latency_ms, status_code, failure_reason, body_match_fragment, body_match_fragment_truncated)
VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8)`

	if _, execErr := tx.Exec(ctx, insertResult,
		job.TargetID, checkedAt, outcome.success, outcome.latency.Milliseconds(),
		outcome.statusCode, outcome.failureReason, outcome.bodyMatchFragment, outcome.bodyMatchFragmentTruncated,
	); execErr != nil {
		return nil, fmt.Errorf("insert check_results for target %s: %w", job.TargetID, execErr)
	}

	// FOR UPDATE: this row is already exclusively ours per ADR-0001's lease
	// (only the current owner ever writes target_schedule for this target),
	// so this never contends — it's cheap insurance that the transition
	// below is always computed from the value this same transaction is
	// about to overwrite, not a stale read.
	var previousStreak int
	var previousStateRaw string
	const selectSchedule = `SELECT streak, state FROM target_schedule WHERE target_id = $1::uuid FOR UPDATE`
	if scanErr := tx.QueryRow(ctx, selectSchedule, job.TargetID).Scan(&previousStreak, &previousStateRaw); scanErr != nil {
		return nil, fmt.Errorf("read target_schedule streak/state for target %s: %w", job.TargetID, scanErr)
	}

	transition := alerting.Evaluate(previousStreak, alerting.State(previousStateRaw), outcome.success, job.FailureThreshold)

	dispatchReq, applyErr := alerting.ApplyIncidentAction(ctx, tx, job.TargetID, transition.Action)
	if applyErr != nil {
		return nil, fmt.Errorf("apply incident action for target %s: %w", job.TargetID, applyErr)
	}

	const updateSchedule = `
UPDATE target_schedule
SET next_due_at = now() + ($1 * INTERVAL '1 second'),
    last_checked_at = now(),
    lease_owner = NULL,
    lease_expires_at = NULL,
    streak = $2,
    state = $3
WHERE target_id = $4::uuid`

	if _, execErr := tx.Exec(ctx, updateSchedule, job.IntervalSeconds, transition.Streak, string(transition.State), job.TargetID); execErr != nil {
		return nil, fmt.Errorf("release lease for target %s: %w", job.TargetID, execErr)
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		return nil, fmt.Errorf("commit release transaction for target %s: %w", job.TargetID, commitErr)
	}
	return dispatchReq, nil
}
