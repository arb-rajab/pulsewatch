package operatorapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultWindowDays/defaultSloTargetPct/min.../max... mirror
// openapi.yaml's /targets/{target_id}/slo query-parameter schema verbatim
// (05-api-contracts.md, already validated with @redocly/cli before this
// session): window_days default 30, range [1, 400]; slo_target_pct default
// 99.9, range [0, 100]. These are request-time query parameters, not stored
// per-target configuration — the identical "a lens applied over existing
// rollup data at read time" pattern the contract already uses for
// slo_target_pct, extended (by that same already-committed, already-linted
// contract, not by this session) to window_days too. This session's own
// "pick one fixed window" ground rule is what the dashboard does with this
// endpoint (see frontend/src/routes/dashboard/+page.server.ts: it always
// requests the default, never exposes a picker) — see
// docs/project-memory/04-data-model.md's Session 12 addendum for the full
// reasoning on why implementing anything narrower than the existing,
// previously-validated contract here was rejected.
const (
	defaultWindowDays = 30
	minWindowDays     = 1
	maxWindowDays     = 400

	defaultSloTargetPct = 99.9
	minSloTargetPct     = 0.0
	maxSloTargetPct     = 100.0
)

// targetSloResponse mirrors openapi.yaml's TargetSlo schema verbatim.
type targetSloResponse struct {
	TargetID               string    `json:"target_id"`
	WindowDays             int       `json:"window_days"`
	WindowStart            time.Time `json:"window_start"`
	WindowEnd              time.Time `json:"window_end"`
	ExpectedChecks         int64     `json:"expected_checks"`
	SuccessCount           int64     `json:"success_count"`
	FailureCount           int64     `json:"failure_count"`
	UnknownCount           int64     `json:"unknown_count"`
	UptimePct              float64   `json:"uptime_pct"`
	SloTargetPct           float64   `json:"slo_target_pct"`
	ErrorBudgetConsumedPct float64   `json:"error_budget_consumed_pct"`
}

// GetTargetSlo is GET /api/v1/targets/{target_id}/slo
// (05-api-contracts.md, openapi.yaml's TargetSlo schema): the rolling-window
// uptime/error-budget read this session builds on top of Session 12's own
// new check_rollups_hourly rollup job (internal/rollup). It is computed
// exclusively from check_rollups_hourly, per the contract's own explicit
// "never a raw-row scan" requirement — no query here ever touches
// check_results.
func GetTargetSlo(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		targetID := c.Param("target_id")

		windowDays, err := parseIntParam(c, "window_days", defaultWindowDays, minWindowDays, maxWindowDays)
		if err != nil {
			writeFieldError(c, http.StatusUnprocessableEntity, "validation_error", err.Error(), "window_days")
			return
		}
		sloTargetPct, err := parseFloatParam(c, "slo_target_pct", defaultSloTargetPct, minSloTargetPct, maxSloTargetPct)
		if err != nil {
			writeFieldError(c, http.StatusUnprocessableEntity, "validation_error", err.Error(), "slo_target_pct")
			return
		}

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

		// window_end is the top of the current hour, never the in-progress
		// hour itself — check_rollups_hourly only ever holds fully-completed
		// buckets (internal/rollup), so this is also exactly what
		// TargetSlo.window_start's schema description promises ("clipped to
		// whole completed hourly rollup buckets"). Computed in SQL, not Go,
		// so window_start/window_end and the SUM below share Postgres's own
		// now()/timezone, never risking a Go-vs-Postgres clock or TZ mismatch.
		const stmt = `
WITH w AS (
    SELECT date_trunc('hour', now()) AS window_end
)
SELECT
    w.window_end - ($2 || ' days')::interval AS window_start,
    w.window_end,
    COALESCE(SUM(r.expected_checks), 0),
    COALESCE(SUM(r.success_count), 0),
    COALESCE(SUM(r.failure_count), 0),
    COALESCE(SUM(r.unknown_count), 0)
FROM w
LEFT JOIN check_rollups_hourly r
    ON r.target_id = $1::uuid
   AND r.hour_bucket >= w.window_end - ($2 || ' days')::interval
   AND r.hour_bucket < w.window_end
GROUP BY w.window_end`

		var windowStart, windowEnd time.Time
		var expectedChecks, successCount, failureCount, unknownCount int64
		queryErr := pool.QueryRow(ctx, stmt, targetID, strconv.Itoa(windowDays)).
			Scan(&windowStart, &windowEnd, &expectedChecks, &successCount, &failureCount, &unknownCount)
		if queryErr != nil {
			writeError(c, http.StatusServiceUnavailable, "database_unavailable", "could not read rollup data")
			return
		}

		resp := targetSloResponse{
			TargetID:       targetID,
			WindowDays:     windowDays,
			WindowStart:    windowStart,
			WindowEnd:      windowEnd,
			ExpectedChecks: expectedChecks,
			SuccessCount:   successCount,
			FailureCount:   failureCount,
			UnknownCount:   unknownCount,
			SloTargetPct:   sloTargetPct,
		}
		resp.UptimePct = uptimePct(successCount, failureCount)
		resp.ErrorBudgetConsumedPct = errorBudgetConsumedPct(resp.UptimePct, sloTargetPct)

		c.JSON(http.StatusOK, resp)
	}
}

// uptimePct is success / (success + failure) * 100, unknown excluded from
// both terms (FR-011/FR-019, 04-data-model.md). A window with zero observed
// success-or-failure checks (e.g. before the target existed, or a window
// that's entirely "unknown") reports 100.0 — vacuously true: no failure was
// ever observed, so there is nothing to subtract a budget against. This is a
// documented choice, not a division-by-zero accident: TargetSlo.uptime_pct
// has no null variant in the schema, so some real number has to be returned.
func uptimePct(successCount, failureCount int64) float64 {
	total := successCount + failureCount
	if total == 0 {
		return 100.0
	}
	return float64(successCount) / float64(total) * 100.0
}

// errorBudgetConsumedPct is (100 - uptime_pct) / (100 - slo_target_pct) * 100
// per openapi.yaml's TargetSlo description; may exceed 100 (budget
// exhausted). slo_target_pct=100 is a real, in-range request value
// (schema maximum: 100) that makes the denominator zero — handled by
// saturating rather than emitting a JSON-illegal Infinity/NaN: a target
// actually at 100% uptime against a 100% target has consumed none of an
// (infinitely strict, zero-width) budget; anything less than 100% uptime
// against a 100% target has consumed all of it.
func errorBudgetConsumedPct(uptimePct, sloTargetPct float64) float64 {
	denominator := 100.0 - sloTargetPct
	if denominator <= 0 {
		if uptimePct >= 100.0 {
			return 0.0
		}
		return 100.0
	}
	return (100.0 - uptimePct) / denominator * 100.0
}

func parseIntParam(c *gin.Context, name string, def, lo, hi int) (int, error) {
	raw := c.Query(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < lo || v > hi {
		return 0, fmt.Errorf("%s must be between %d and %d", name, lo, hi)
	}
	return v, nil
}

func parseFloatParam(c *gin.Context, name string, def, lo, hi float64) (float64, error) {
	raw := c.Query(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < lo || v > hi {
		return 0, fmt.Errorf("%s must be between %g and %g", name, lo, hi)
	}
	return v, nil
}
