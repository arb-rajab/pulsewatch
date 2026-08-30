package operatorapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// foreignKeyViolation is Postgres's SQLSTATE for a violated FK constraint —
// used to turn a nonexistent agent_id into a real 422 field error instead of
// a bare 503/500.
const foreignKeyViolation = "23503"

type targetCreateRequest struct {
	Type             string  `json:"type" binding:"required"`
	URL              string  `json:"url"`
	Host             string  `json:"host"`
	Port             *int32  `json:"port"`
	BodyMatchPattern *string `json:"body_match_pattern"`
	IntervalSeconds  int     `json:"interval_seconds" binding:"required"`
	FailureThreshold *int    `json:"failure_threshold"`
	TimeoutSeconds   int     `json:"timeout_seconds" binding:"required"`
	AgentID          *string `json:"agent_id"`
}

type targetResponse struct {
	ID               string    `json:"id"`
	Type             string    `json:"type"`
	URL              *string   `json:"url"`
	Host             *string   `json:"host"`
	Port             *int32    `json:"port"`
	BodyMatchPattern *string   `json:"body_match_pattern"`
	IntervalSeconds  int       `json:"interval_seconds"`
	FailureThreshold int       `json:"failure_threshold"`
	TimeoutSeconds   int       `json:"timeout_seconds"`
	AgentID          *string   `json:"agent_id"`
	CreatedAt        time.Time `json:"created_at"`
}

// defaultFailureThreshold is NFR-011's proposed default.
const defaultFailureThreshold = 3

// CreateTarget is POST /targets (FR-001/FR-002, US-001/US-002): validates
// the http-vs-tcp identity-field shape TargetCreateRequest's oneOf in
// openapi.yaml specifies, then inserts both the targets row and its 1:1
// target_schedule row (next_due_at = now()) in one transaction — the
// scheduler's own due-set scan (scheduler.go) only ever looks at
// target_schedule.next_due_at, so a target with no schedule row would never
// be picked up at all, silently violating US-001's "picked up by the
// scheduler within one tick."
func CreateTarget(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req targetCreateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, "malformed_request", "malformed request body")
			return
		}

		urlOrHost, field, verr := validateTargetIdentity(req)
		if verr != "" {
			writeFieldError(c, http.StatusUnprocessableEntity, "validation_error", verr, field)
			return
		}
		if req.IntervalSeconds < 10 || req.IntervalSeconds > 86400 {
			writeFieldError(c, http.StatusUnprocessableEntity, "validation_error", "interval_seconds must be between 10 and 86400", "interval_seconds")
			return
		}
		if req.TimeoutSeconds < 1 {
			writeFieldError(c, http.StatusUnprocessableEntity, "validation_error", "timeout_seconds must be at least 1", "timeout_seconds")
			return
		}
		failureThreshold := defaultFailureThreshold
		if req.FailureThreshold != nil {
			if *req.FailureThreshold < 1 {
				writeFieldError(c, http.StatusUnprocessableEntity, "validation_error", "failure_threshold must be at least 1", "failure_threshold")
				return
			}
			failureThreshold = *req.FailureThreshold
		}

		ctx := c.Request.Context()
		tx, err := pool.Begin(ctx)
		if err != nil {
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not create target")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		const insertTarget = `
INSERT INTO targets (type, url_or_host, port, body_match_pattern, interval_seconds, failure_threshold, timeout_seconds, agent_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::uuid)
RETURNING id::text, created_at`
		var id string
		var createdAt time.Time
		err = tx.QueryRow(ctx, insertTarget, req.Type, urlOrHost, req.Port, req.BodyMatchPattern, req.IntervalSeconds, failureThreshold, req.TimeoutSeconds, req.AgentID).
			Scan(&id, &createdAt)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
				writeFieldError(c, http.StatusUnprocessableEntity, "validation_error", "agent_id does not reference an existing agent", "agent_id")
				return
			}
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not create target")
			return
		}

		if _, err := tx.Exec(ctx, `INSERT INTO target_schedule (target_id, next_due_at) VALUES ($1::uuid, now())`, id); err != nil {
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not create target")
			return
		}

		if err := tx.Commit(ctx); err != nil {
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not create target")
			return
		}

		resp := targetResponse{
			ID: id, Type: req.Type, BodyMatchPattern: req.BodyMatchPattern,
			IntervalSeconds: req.IntervalSeconds, FailureThreshold: failureThreshold,
			TimeoutSeconds: req.TimeoutSeconds, AgentID: req.AgentID, CreatedAt: createdAt,
		}
		if req.Type == "http" {
			resp.URL = &urlOrHost
		} else {
			resp.Host = &urlOrHost
			resp.Port = req.Port
		}
		c.JSON(http.StatusCreated, resp)
	}
}

// validateTargetIdentity enforces TargetCreateRequest's if/then/else shape
// (openapi.yaml): http requires url, tcp requires host+port.
func validateTargetIdentity(req targetCreateRequest) (urlOrHost, field, errMsg string) {
	switch req.Type {
	case "http":
		if strings.TrimSpace(req.URL) == "" {
			return "", "url", "url is required for an http target"
		}
		return req.URL, "", ""
	case "tcp":
		if strings.TrimSpace(req.Host) == "" {
			return "", "host", "host is required for a tcp target"
		}
		if req.Port == nil || *req.Port < 1 || *req.Port > 65535 {
			return "", "port", "port is required for a tcp target and must be between 1 and 65535"
		}
		return req.Host, "", ""
	default:
		return "", "type", "type must be either \"http\" or \"tcp\""
	}
}

const selectTargetColumns = `id::text, type, url_or_host, port, body_match_pattern, interval_seconds, failure_threshold, timeout_seconds, agent_id::text, created_at`

func scanTarget(row pgx.Row) (targetResponse, error) {
	var t targetResponse
	var targetType, urlOrHost string
	var port *int32
	var agentID *string
	if err := row.Scan(&t.ID, &targetType, &urlOrHost, &port, &t.BodyMatchPattern, &t.IntervalSeconds, &t.FailureThreshold, &t.TimeoutSeconds, &agentID, &t.CreatedAt); err != nil {
		return targetResponse{}, err
	}
	t.Type = targetType
	t.AgentID = agentID
	if targetType == "http" {
		t.URL = &urlOrHost
	} else {
		t.Host = &urlOrHost
		t.Port = port
	}
	return t, nil
}

// ListTargets is GET /targets — unpaginated (05-api-contracts.md: bounded,
// single-operator scale).
func ListTargets(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		rows, err := pool.Query(ctx, `SELECT `+selectTargetColumns+` FROM targets WHERE deleted_at IS NULL ORDER BY id`)
		if err != nil {
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not list targets")
			return
		}
		defer rows.Close()

		targets := []targetResponse{}
		for rows.Next() {
			t, scanErr := scanTarget(rows)
			if scanErr != nil {
				writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read targets")
				return
			}
			targets = append(targets, t)
		}
		if err := rows.Err(); err != nil {
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read targets")
			return
		}
		c.JSON(http.StatusOK, targets)
	}
}

// GetTarget is GET /targets/{target_id}.
func GetTarget(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		row := pool.QueryRow(ctx, `SELECT `+selectTargetColumns+` FROM targets WHERE id = $1::uuid AND deleted_at IS NULL`, c.Param("target_id"))
		t, err := scanTarget(row)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(c, http.StatusNotFound, "not_found", "target not found")
				return
			}
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read target")
			return
		}
		c.JSON(http.StatusOK, t)
	}
}

type targetUpdateRequest struct {
	IntervalSeconds  *int    `json:"interval_seconds"`
	FailureThreshold *int    `json:"failure_threshold"`
	TimeoutSeconds   *int    `json:"timeout_seconds"`
	BodyMatchPattern *string `json:"body_match_pattern"`
	AgentID          *string `json:"agent_id"`
}

// UpdateTarget is PATCH /targets/{target_id} (05-api-contracts.md: only
// interval/threshold/timeout/body-match/agent assignment are mutable —
// type/url/host/port are identity fields, immutable after creation). A
// field is only ever changed when its JSON key is actually present in the
// request body — body_match_pattern and agent_id are both legitimately
// nullable (clear the pattern; unassign the agent back to server-executed),
// so "key present with value null" must be distinguishable from "key
// absent," which a plain pointer field alone cannot do (Go's JSON decoder
// leaves both cases as a nil pointer) — hence the separate raw-key presence
// check below.
func UpdateTarget(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, err := io.ReadAll(c.Request.Body)
		if err != nil {
			writeError(c, http.StatusBadRequest, "malformed_request", "could not read request body")
			return
		}
		var presence map[string]json.RawMessage
		if err := json.Unmarshal(raw, &presence); err != nil {
			writeError(c, http.StatusBadRequest, "malformed_request", "malformed request body")
			return
		}
		var req targetUpdateRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			writeError(c, http.StatusBadRequest, "malformed_request", "malformed request body")
			return
		}

		var sets []string
		var args []any
		argN := 1
		addSet := func(column string, value any, cast string) {
			sets = append(sets, fmt.Sprintf("%s = $%d%s", column, argN, cast))
			args = append(args, value)
			argN++
		}

		if req.IntervalSeconds != nil {
			if *req.IntervalSeconds < 10 || *req.IntervalSeconds > 86400 {
				writeFieldError(c, http.StatusUnprocessableEntity, "validation_error", "interval_seconds must be between 10 and 86400", "interval_seconds")
				return
			}
			addSet("interval_seconds", *req.IntervalSeconds, "")
		}
		if req.FailureThreshold != nil {
			if *req.FailureThreshold < 1 {
				writeFieldError(c, http.StatusUnprocessableEntity, "validation_error", "failure_threshold must be at least 1", "failure_threshold")
				return
			}
			addSet("failure_threshold", *req.FailureThreshold, "")
		}
		if req.TimeoutSeconds != nil {
			if *req.TimeoutSeconds < 1 {
				writeFieldError(c, http.StatusUnprocessableEntity, "validation_error", "timeout_seconds must be at least 1", "timeout_seconds")
				return
			}
			addSet("timeout_seconds", *req.TimeoutSeconds, "")
		}
		if _, ok := presence["body_match_pattern"]; ok {
			addSet("body_match_pattern", req.BodyMatchPattern, "")
		}
		if _, ok := presence["agent_id"]; ok {
			addSet("agent_id", req.AgentID, "::uuid")
		}

		ctx := c.Request.Context()
		targetID := c.Param("target_id")

		if len(sets) == 0 {
			row := pool.QueryRow(ctx, `SELECT `+selectTargetColumns+` FROM targets WHERE id = $1::uuid AND deleted_at IS NULL`, targetID)
			t, scanErr := scanTarget(row)
			if scanErr != nil {
				if errors.Is(scanErr, pgx.ErrNoRows) {
					writeError(c, http.StatusNotFound, "not_found", "target not found")
					return
				}
				writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read target")
				return
			}
			c.JSON(http.StatusOK, t)
			return
		}

		args = append(args, targetID)
		query := fmt.Sprintf(`UPDATE targets SET %s WHERE id = $%d::uuid AND deleted_at IS NULL RETURNING `+selectTargetColumns, strings.Join(sets, ", "), argN)
		row := pool.QueryRow(ctx, query, args...)
		t, err := scanTarget(row)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
				writeFieldError(c, http.StatusUnprocessableEntity, "validation_error", "agent_id does not reference an existing agent", "agent_id")
				return
			}
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(c, http.StatusNotFound, "not_found", "target not found")
				return
			}
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not update target")
			return
		}
		c.JSON(http.StatusOK, t)
	}
}

// DeleteTarget is DELETE /targets/{target_id} — soft delete
// (05-api-contracts.md), leaving history (check_results, incidents) intact.
// scheduler.go's own due-set query already filters `t.deleted_at IS NULL`,
// so a soft-deleted target simply stops being picked up starting the next
// tick, with no separate target_schedule cleanup required.
func DeleteTarget(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		var id string
		err := pool.QueryRow(ctx, `UPDATE targets SET deleted_at = now() WHERE id = $1::uuid AND deleted_at IS NULL RETURNING id::text`, c.Param("target_id")).Scan(&id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(c, http.StatusNotFound, "not_found", "target not found")
				return
			}
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not delete target")
			return
		}
		c.Status(http.StatusNoContent)
	}
}
