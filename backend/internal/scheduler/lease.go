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

	// The shared write path every check-result source goes through
	// (alerting.RecordCheckResult) — the identical insert/evaluate/apply
	// sequence an agent-reported OTLP result now also uses (internal/agentapi),
	// so this session's ADR-0003 work adds no second, lower-rigor path for
	// agent-sourced data (03-architecture.md).
	recorded, recordErr := alerting.RecordCheckResult(ctx, tx, alerting.CheckResultInput{
		TargetID:                   job.TargetID,
		CheckedAt:                  checkedAt,
		Success:                    outcome.success,
		LatencyMS:                  int(outcome.latency.Milliseconds()),
		StatusCode:                 outcome.statusCode,
		FailureReason:              outcome.failureReason,
		BodyMatchFragment:          outcome.bodyMatchFragment,
		BodyMatchFragmentTruncated: outcome.bodyMatchFragmentTruncated,
	}, job.FailureThreshold)
	if recordErr != nil {
		return nil, fmt.Errorf("record check result for target %s: %w", job.TargetID, recordErr)
	}

	// next_due_at/lease clearing are this path's own scheduling-specific
	// columns (ADR-0001) — folded into the same commit as the shared write
	// above, exactly as ADR-0002 requires ("the same transaction"). Persists
	// recorded.Streak/State regardless of recorded.Inserted: a duplicate
	// (target_id, checked_at) still needs its lease released and next_due_at
	// advanced, just with streak/state passed through unchanged (which is
	// exactly what RecordCheckResult already returns in that case).
	const updateSchedule = `
UPDATE target_schedule
SET next_due_at = now() + ($1 * INTERVAL '1 second'),
    last_checked_at = now(),
    lease_owner = NULL,
    lease_expires_at = NULL,
    streak = $2,
    state = $3
WHERE target_id = $4::uuid`

	if _, execErr := tx.Exec(ctx, updateSchedule, job.IntervalSeconds, recorded.Streak, string(recorded.State), job.TargetID); execErr != nil {
		return nil, fmt.Errorf("release lease for target %s: %w", job.TargetID, execErr)
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		return nil, fmt.Errorf("commit release transaction for target %s: %w", job.TargetID, commitErr)
	}
	return recorded.Dispatch, nil
}
