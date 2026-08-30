package operatorapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/agentauth"
)

const defaultReportIntervalSeconds = 60

type agentCreateRequest struct {
	Name                  string `json:"name" binding:"required"`
	ReportIntervalSeconds *int   `json:"report_interval_seconds"`
}

type agentCreatedResponse struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	ReportIntervalSeconds int       `json:"report_interval_seconds"`
	RegisteredAt          time.Time `json:"registered_at"`
	Token                 string    `json:"token"`
}

// CreateAgent is POST /api/v1/agents (ADR-0003): the real, gated HTTP path
// this session builds — 12-session-handoff.md's own flagged gap
// (agentauth.CreateAgent existed since Session 7, exposed only via
// cmd/seed-agent because no operatorSession mechanism existed to gate an
// HTTP handler). Same mechanism, same one-time-shown token guarantee, now
// reachable over HTTP behind RequireOperator.
func CreateAgent(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req agentCreateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, "malformed_request", "name is required")
			return
		}
		reportInterval := defaultReportIntervalSeconds
		if req.ReportIntervalSeconds != nil {
			if *req.ReportIntervalSeconds < 1 {
				writeFieldError(c, http.StatusUnprocessableEntity, "validation_error", "report_interval_seconds must be at least 1", "report_interval_seconds")
				return
			}
			reportInterval = *req.ReportIntervalSeconds
		}

		created, err := agentauth.CreateAgent(c.Request.Context(), pool, req.Name, reportInterval)
		if err != nil {
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not create agent")
			return
		}
		c.JSON(http.StatusCreated, agentCreatedResponse{
			ID: created.ID, Name: created.Name,
			ReportIntervalSeconds: int(created.ReportInterval.Seconds()),
			RegisteredAt:          created.RegisteredAt,
			Token:                 created.Token,
		})
	}
}

// agentResponse mirrors openapi.yaml's Agent schema — no credential/token
// property, matching AlertChannel's identical write-once-secret shape.
type agentResponse struct {
	ID                    string     `json:"id"`
	Name                  string     `json:"name"`
	ReportIntervalSeconds int        `json:"report_interval_seconds"`
	LastHeartbeatAt       *time.Time `json:"last_heartbeat_at"`
	IsStale               bool       `json:"is_stale"`
	AssignedTargetCount   int        `json:"assigned_target_count"`
	RegisteredAt          time.Time  `json:"registered_at"`
}

func scanAgent(row pgx.Row, now time.Time) (agentResponse, error) {
	var a agentResponse
	if err := row.Scan(&a.ID, &a.Name, &a.ReportIntervalSeconds, &a.LastHeartbeatAt, &a.AssignedTargetCount, &a.RegisteredAt); err != nil {
		return agentResponse{}, err
	}
	a.IsStale = agentauth.IsStale(a.ReportIntervalSeconds, a.LastHeartbeatAt, now)
	return a, nil
}

const selectAgentColumns = `
a.id::text, a.name, a.report_interval_seconds, a.last_heartbeat_at,
(SELECT count(*) FROM targets t WHERE t.agent_id = a.id AND t.deleted_at IS NULL),
a.registered_at
FROM agents a`

// ListAgents is GET /api/v1/agents.
func ListAgents(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		now := time.Now()
		rows, err := pool.Query(c.Request.Context(), `SELECT `+selectAgentColumns+` ORDER BY a.id`)
		if err != nil {
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not list agents")
			return
		}
		defer rows.Close()

		agents := []agentResponse{}
		for rows.Next() {
			a, scanErr := scanAgent(rows, now)
			if scanErr != nil {
				writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read agents")
				return
			}
			agents = append(agents, a)
		}
		if err := rows.Err(); err != nil {
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read agents")
			return
		}
		c.JSON(http.StatusOK, agents)
	}
}

// GetAgent is GET /api/v1/agents/{agent_id}.
func GetAgent(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		row := pool.QueryRow(c.Request.Context(), `SELECT `+selectAgentColumns+` WHERE a.id = $1::uuid`, c.Param("agent_id"))
		a, err := scanAgent(row, time.Now())
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(c, http.StatusNotFound, "not_found", "agent not found")
				return
			}
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read agent")
			return
		}
		c.JSON(http.StatusOK, a)
	}
}

// DeleteAgent is DELETE /api/v1/agents/{agent_id} — blocked with 409 while
// the agent still has assigned targets (05-api-contracts.md: falling those
// back to server-executed automatically would be actively wrong for a
// target the server structurally cannot reach, the entire reason the agent
// exists). Enforced here via the same foreign-key relationship
// targets.agent_id already carries — no separate application-level count
// check racing against a concurrent target assignment, since the DELETE
// itself fails at the database level if any target still references this
// agent.
func DeleteAgent(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var id string
		err := pool.QueryRow(c.Request.Context(),
			`DELETE FROM agents WHERE id = $1::uuid RETURNING id::text`, c.Param("agent_id"),
		).Scan(&id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(c, http.StatusNotFound, "not_found", "agent not found")
				return
			}
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
				writeError(c, http.StatusConflict, "agent_has_assigned_targets", "agent still has assigned targets")
				return
			}
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not delete agent")
			return
		}
		c.Status(http.StatusNoContent)
	}
}

type agentCredentialRotatedResponse struct {
	AgentID   string    `json:"agent_id"`
	Token     string    `json:"token"`
	RotatedAt time.Time `json:"rotated_at"`
}

// RotateAgentCredential is POST /api/v1/agents/{agent_id}/credential/rotate.
// Deliberately not idempotency-key-guarded — see agentauth.RotateCredential
// and 05-api-contracts.md for why that asymmetry with agent creation is
// intentional.
func RotateAgentCredential(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		rotated, err := agentauth.RotateCredential(c.Request.Context(), pool, c.Param("agent_id"))
		if err != nil {
			if errors.Is(err, agentauth.ErrAgentNotFound) {
				writeError(c, http.StatusNotFound, "not_found", "agent not found")
				return
			}
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not rotate agent credential")
			return
		}
		c.JSON(http.StatusOK, agentCredentialRotatedResponse{
			AgentID: rotated.AgentID, Token: rotated.Token, RotatedAt: rotated.RotatedAt,
		})
	}
}
