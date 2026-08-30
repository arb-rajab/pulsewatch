package agentapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultPollIntervalSeconds is ADR-0003 Trade-offs' own example ("e.g.
// 60s") for the assignment-poll hint returned in AssignmentList — an
// implementation default, not a numeric requirement the ADR or contract
// invents.
const defaultPollIntervalSeconds = 60

// AssignmentListItem mirrors docs/architecture/openapi.yaml's
// AssignmentListItem schema exactly — deliberately omits failure_threshold
// (hysteresis evaluation is server-side only, ADR-0002; the agent's job is
// to execute and report, nothing more).
type AssignmentListItem struct {
	TargetID         string  `json:"target_id"`
	Type             string  `json:"type"`
	URL              *string `json:"url"`
	Host             *string `json:"host"`
	Port             *int32  `json:"port"`
	BodyMatchPattern *string `json:"body_match_pattern"`
	IntervalSeconds  int     `json:"interval_seconds"`
	TimeoutSeconds   int     `json:"timeout_seconds"`
}

// AssignmentList mirrors docs/architecture/openapi.yaml's AssignmentList
// schema.
type AssignmentList struct {
	AgentID             string               `json:"agent_id"`
	Assignments         []AssignmentListItem `json:"assignments"`
	PollIntervalSeconds int                  `json:"poll_interval_seconds"`
}

// GetAssignments is GET /api/v1/agent/assignments (ADR-0003 Decision 2):
// the caller's own assignment list, scoped by its bearer identity alone —
// there is no {agent_id} path parameter (05-api-contracts.md: "no request
// shape for reading a different agent's assignments to even construct").
// Deliberately does not touch agents.last_heartbeat_at — heartbeat is
// carried exclusively over the OTLP channel (logs.go), kept as the single
// liveness signal staleness is computed from.
func GetAssignments(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity := identityFromContext(c)
		ctx := c.Request.Context()

		const stmt = `
SELECT id::text, type, url_or_host, port, body_match_pattern, interval_seconds, timeout_seconds
FROM targets
WHERE agent_id = $1::uuid AND deleted_at IS NULL
ORDER BY id`
		rows, err := pool.Query(ctx, stmt, identity.AgentID)
		if err != nil {
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not load assignments")
			return
		}
		defer rows.Close()

		assignments := []AssignmentListItem{}
		for rows.Next() {
			var item AssignmentListItem
			var targetType, urlOrHost string
			var port *int32
			if scanErr := rows.Scan(&item.TargetID, &targetType, &urlOrHost, &port, &item.BodyMatchPattern, &item.IntervalSeconds, &item.TimeoutSeconds); scanErr != nil {
				writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read assignments")
				return
			}
			item.Type = targetType
			if targetType == "http" {
				item.URL = &urlOrHost
			} else {
				item.Host = &urlOrHost
				item.Port = port
			}
			assignments = append(assignments, item)
		}
		if err := rows.Err(); err != nil {
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read assignments")
			return
		}

		c.JSON(http.StatusOK, AssignmentList{
			AgentID:             identity.AgentID,
			Assignments:         assignments,
			PollIntervalSeconds: defaultPollIntervalSeconds,
		})
	}
}
