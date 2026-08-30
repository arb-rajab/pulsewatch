package agentauth

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// staleFactor is NFR-008/ADR-0003's fixed multiple: an agent is stale once
// more than 3x its own report_interval has elapsed since its last
// heartbeat. Not operator-configurable — 05-api-contracts.md's Agent.
// is_stale doc comment and ADR-0003 Decision 3 both fix this as "3 ×
// agents.report_interval," a specific number, not a tunable.
const staleFactor = 3

// IsStale is NFR-008's staleness computation, pure and read-time only — it
// takes no dependency on "now" beyond what's passed in, so it's identical
// whether called from a request handler or a test. A nil lastHeartbeatAt
// (an agent that has never once reported in) counts as stale: "unknown" is
// the honest answer to "is this agent's data current" when there is no
// data yet, not a special case requiring its own branch downstream.
func IsStale(reportIntervalSeconds int, lastHeartbeatAt *time.Time, now time.Time) bool {
	if lastHeartbeatAt == nil {
		return true
	}
	threshold := time.Duration(staleFactor*reportIntervalSeconds) * time.Second
	return now.Sub(*lastHeartbeatAt) > threshold
}

// FetchLiveness reads the two columns IsStale needs for one agent. Returns
// pgx.ErrNoRows (wrapped) if the agent id doesn't exist — callers that
// already authenticated via Authenticate never hit that in practice, since
// the agent row backing a valid credential can't disappear mid-request (no
// DELETE /agents handler exists yet to race against, 12-session-handoff.md).
func FetchLiveness(ctx context.Context, pool *pgxpool.Pool, agentID string) (reportIntervalSeconds int, lastHeartbeatAt *time.Time, err error) {
	const stmt = `SELECT report_interval_seconds, last_heartbeat_at FROM agents WHERE id = $1::uuid`
	if scanErr := pool.QueryRow(ctx, stmt, agentID).Scan(&reportIntervalSeconds, &lastHeartbeatAt); scanErr != nil {
		return 0, nil, fmt.Errorf("fetch liveness for agent %s: %w", agentID, scanErr)
	}
	return reportIntervalSeconds, lastHeartbeatAt, nil
}

// RecordHeartbeat is the OTLP heartbeat record's write (ADR-0003: "an agent
// with zero assigned targets still heartbeats"). GREATEST makes this
// monotonic against out-of-order delivery — docs/architecture/openapi.yaml's
// /v1/logs description: "updates agents.last_heartbeat_at to max(current,
// timeUnixNano) — never regresses on a late-arriving older record."
// COALESCE against the epoch handles the first-ever heartbeat, where
// last_heartbeat_at starts NULL and GREATEST(NULL, x) would otherwise be
// NULL in SQL.
func RecordHeartbeat(ctx context.Context, pool *pgxpool.Pool, agentID string, at time.Time) error {
	const stmt = `
UPDATE agents
SET last_heartbeat_at = GREATEST(COALESCE(last_heartbeat_at, to_timestamp(0)), $1)
WHERE id = $2::uuid`
	if _, err := pool.Exec(ctx, stmt, at, agentID); err != nil {
		return fmt.Errorf("record heartbeat for agent %s: %w", agentID, err)
	}
	return nil
}
