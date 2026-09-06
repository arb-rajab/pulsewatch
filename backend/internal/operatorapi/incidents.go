package operatorapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// incidentResponse mirrors openapi.yaml's Incident schema verbatim. status
// is derived from closed_at at read time ("open" vs "resolved") rather than
// stored — ADR-0002's own schema has no status column, only opened_at/
// closed_at, so this handler is the one place that turns "closed_at IS
// NULL" into the wire-level enum the contract promises.
type incidentResponse struct {
	ID       int64      `json:"id"`
	TargetID string     `json:"target_id"`
	OpenedAt time.Time  `json:"opened_at"`
	ClosedAt *time.Time `json:"closed_at"`
	Status   string     `json:"status"`
}

// GetTargetIncidents is GET /api/v1/targets/{target_id}/incidents
// (05-api-contracts.md, openapi.yaml's Incident schema): incident history
// for a target, open and resolved alike, newest first. A pure read of the
// durable incidents table ADR-0002's OpenIncident/CloseIncident already
// write — this handler adds no new computation, matching B-002's own
// backlog framing ("same pattern as Session 9's /status endpoint and
// Session 12's /slo endpoint"). Unpaginated in v1, per the contract's own
// documented reasoning (bounded by real-outage frequency).
func GetTargetIncidents(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		targetID := c.Param("target_id")

		var exists bool
		checkErr := pool.QueryRow(ctx, `SELECT true FROM targets WHERE id = $1::uuid AND deleted_at IS NULL`, targetID).Scan(&exists)
		if checkErr != nil {
			if errors.Is(checkErr, pgx.ErrNoRows) {
				writeError(c, http.StatusNotFound, "not_found", "target not found")
				return
			}
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read target")
			return
		}

		const stmt = `
SELECT id, target_id::text, opened_at, closed_at
FROM incidents
WHERE target_id = $1::uuid
ORDER BY opened_at DESC`

		rows, queryErr := pool.Query(ctx, stmt, targetID)
		if queryErr != nil {
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read incidents")
			return
		}
		defer rows.Close()

		incidents := make([]incidentResponse, 0)
		for rows.Next() {
			var inc incidentResponse
			if err := rows.Scan(&inc.ID, &inc.TargetID, &inc.OpenedAt, &inc.ClosedAt); err != nil {
				writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read incidents")
				return
			}
			if inc.ClosedAt == nil {
				inc.Status = "open"
			} else {
				inc.Status = "resolved"
			}
			incidents = append(incidents, inc)
		}
		if err := rows.Err(); err != nil {
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read incidents")
			return
		}

		c.JSON(http.StatusOK, incidents)
	}
}
