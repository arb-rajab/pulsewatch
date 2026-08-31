package operatorapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/agentauth"
	"github.com/arb-rajab/pulsewatch/backend/internal/alerting"
)

// openIncidentResponse mirrors openapi.yaml's TargetStatus.open_incident —
// present only when raw_state is currently "alerting" (05-api-contracts.md).
type openIncidentResponse struct {
	ID       int64     `json:"id"`
	OpenedAt time.Time `json:"opened_at"`
}

// targetStatusResponse mirrors openapi.yaml's TargetStatus schema verbatim.
type targetStatusResponse struct {
	TargetID      string                 `json:"target_id"`
	DisplayState  string                 `json:"display_state"`
	RawState      string                 `json:"raw_state"`
	Streak        int                    `json:"streak"`
	LastCheckedAt *time.Time             `json:"last_checked_at"`
	NextDueAt     time.Time              `json:"next_due_at"`
	AgentID       *string                `json:"agent_id"`
	AgentStale    *bool                  `json:"agent_stale"`
	OpenIncident  *openIncidentResponse  `json:"open_incident"`
}

// GetTargetStatus is GET /api/v1/targets/{target_id}/status
// (05-api-contracts.md, openapi.yaml's TargetStatus schema): the live-status
// read this session builds. It is a pure read composed entirely from
// functions earlier sessions already wrote and tested —
// alerting.DisplayState (ADR-0002 Consequences' read-time staleness overlay)
// and agentauth.IsStale/FetchLiveness (ADR-0003/NFR-008) — and never writes
// to target_schedule or agents. display_state is computed fresh on every
// call; raw_state is target_schedule.state read back unmodified, proving the
// overlay is genuinely read-only, not a value ever persisted over the real
// state (ADR-0002 Consequences).
func GetTargetStatus(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		targetID := c.Param("target_id")

		const stmt = `
SELECT t.agent_id::text, ts.state, ts.streak, ts.last_checked_at, ts.next_due_at
FROM targets t
JOIN target_schedule ts ON ts.target_id = t.id
WHERE t.id = $1::uuid AND t.deleted_at IS NULL`

		var agentID *string
		var rawState string
		var streak int
		var lastCheckedAt *time.Time
		var nextDueAt time.Time
		err := pool.QueryRow(ctx, stmt, targetID).Scan(&agentID, &rawState, &streak, &lastCheckedAt, &nextDueAt)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(c, http.StatusNotFound, "not_found", "target not found")
				return
			}
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read target status")
			return
		}

		resp := targetStatusResponse{
			TargetID:      targetID,
			RawState:      rawState,
			Streak:        streak,
			LastCheckedAt: lastCheckedAt,
			NextDueAt:     nextDueAt,
			AgentID:       agentID,
		}

		agentAssigned := agentID != nil
		var agentStale bool
		if agentAssigned {
			reportIntervalSeconds, lastHeartbeatAt, livenessErr := agentauth.FetchLiveness(ctx, pool, *agentID)
			if livenessErr != nil {
				writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read agent liveness")
				return
			}
			agentStale = agentauth.IsStale(reportIntervalSeconds, lastHeartbeatAt, time.Now())
			resp.AgentStale = &agentStale
		}

		resp.DisplayState = alerting.DisplayState(alerting.State(rawState), agentAssigned, agentStale)

		if rawState == string(alerting.StateAlerting) {
			var incidentID int64
			var openedAt time.Time
			incErr := pool.QueryRow(ctx, `SELECT id, opened_at FROM incidents WHERE target_id = $1::uuid AND closed_at IS NULL`, targetID).Scan(&incidentID, &openedAt)
			if incErr == nil {
				resp.OpenIncident = &openIncidentResponse{ID: incidentID, OpenedAt: openedAt}
			} else if !errors.Is(incErr, pgx.ErrNoRows) {
				writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read open incident")
				return
			}
		}

		c.JSON(http.StatusOK, resp)
	}
}
