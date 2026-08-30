package agentapi

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/alerting"
)

// recordAgentCheckResult folds alerting.RecordCheckResult — the identical
// shared write path scheduler.releaseAndRecord itself uses — into its own
// one-transaction commit alongside the scheduling-specific column an
// agent-reported result still needs (last_checked_at). Unlike the
// scheduler's own release path, there is no next_due_at/lease_owner to
// touch here: the server's own scheduler never claims an agent-executed
// target in the first place (dueTargets excludes agent_id IS NOT NULL) —
// the agent runs its own local due-set, and next_due_at/lease_* on this
// target_schedule row are simply unused by this path, exactly as
// ADR-0003/03-architecture.md describe the agent's local scheduler as
// independent of the server's own.
func recordAgentCheckResult(ctx context.Context, pool *pgxpool.Pool, targetID string, failureThreshold int, in alerting.CheckResultInput) (alerting.Recorded, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return alerting.Recorded{}, fmt.Errorf("begin agent check-result transaction for target %s: %w", targetID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	recorded, err := alerting.RecordCheckResult(ctx, tx, in, failureThreshold)
	if err != nil {
		return alerting.Recorded{}, err
	}

	const updateSchedule = `UPDATE target_schedule SET last_checked_at = now(), streak = $1, state = $2 WHERE target_id = $3::uuid`
	if _, execErr := tx.Exec(ctx, updateSchedule, recorded.Streak, string(recorded.State), targetID); execErr != nil {
		return alerting.Recorded{}, fmt.Errorf("update target_schedule for target %s: %w", targetID, execErr)
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		return alerting.Recorded{}, fmt.Errorf("commit agent check-result transaction for target %s: %w", targetID, commitErr)
	}
	return recorded, nil
}
