package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

// releaseAndRecord is ADR-0001's release: "one transaction records the check
// result, advances next_due_at, sets last_checked_at, and clears
// lease_owner/lease_expires_at." A crash between check completion and this
// call cannot be observed as a distinct state — the lease simply expires and
// the next tick re-claims and re-runs the check, which is safe because check
// execution has no side effect beyond producing this one result row.
func releaseAndRecord(ctx context.Context, pool *pgxpool.Pool, job CheckJob, checkedAt time.Time, outcome checkOutcome) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin release transaction for target %s: %w", job.TargetID, err)
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
		return fmt.Errorf("insert check_results for target %s: %w", job.TargetID, execErr)
	}

	const updateSchedule = `
UPDATE target_schedule
SET next_due_at = now() + ($1 * INTERVAL '1 second'),
    last_checked_at = now(),
    lease_owner = NULL,
    lease_expires_at = NULL
WHERE target_id = $2::uuid`

	if _, execErr := tx.Exec(ctx, updateSchedule, job.IntervalSeconds, job.TargetID); execErr != nil {
		return fmt.Errorf("release lease for target %s: %w", job.TargetID, execErr)
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		return fmt.Errorf("commit release transaction for target %s: %w", job.TargetID, commitErr)
	}
	return nil
}
