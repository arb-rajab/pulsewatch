package operatorapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/agentauth"
)

// createTestTarget inserts a real target (and its target_schedule row, via
// the real CreateTarget handler) — the same path a real operator uses, not
// a hand-rolled fixture row.
func createTestTarget(t *testing.T, r *gin.Engine, pool *pgxpool.Pool, cookie, url string) targetResponse {
	t.Helper()
	body := []byte(`{"type":"http","url":"` + url + `","interval_seconds":30,"timeout_seconds":5}`)
	w := doRequest(t, r, http.MethodPost, "/api/v1/targets", cookie, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating test target, got %d: %s", w.Code, w.Body.String())
	}
	var created targetResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created target: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(t.Context(), `DELETE FROM targets WHERE id = $1::uuid`, created.ID)
	})
	return created
}

func TestGetTargetStatus_ServerExecutedHealthyTarget(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-status-healthy@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	created := createTestTarget(t, r, pool, cookie, "http://example.invalid/status-healthy")

	w := doRequest(t, r, http.MethodGet, "/api/v1/targets/"+created.ID+"/status", cookie, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var status targetStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if status.RawState != "healthy" || status.DisplayState != "healthy" {
		t.Fatalf("expected a freshly-created target to read healthy/healthy, got raw=%q display=%q", status.RawState, status.DisplayState)
	}
	if status.AgentID != nil || status.AgentStale != nil {
		t.Fatalf("expected nil agent_id/agent_stale for a server-executed target, got agent_id=%v agent_stale=%v", status.AgentID, status.AgentStale)
	}
	if status.OpenIncident != nil {
		t.Fatalf("expected no open_incident for a healthy target, got %+v", status.OpenIncident)
	}
}

func TestGetTargetStatus_UnknownIdReturnsNotFound(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-status-notfound@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	w := doRequest(t, r, http.MethodGet, "/api/v1/targets/"+placeholderID+"/status", cookie, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestGetTargetStatus_StaleAgentDisplaysUnknownWithoutMutatingRawState is
// this session's "prove it has teeth" case (Session 9 prompt): a real
// target assigned to a real agent whose last_heartbeat_at is old enough to
// be stale (agentauth.IsStale, NFR-008) must display_state="unknown" while
// raw_state and every underlying target_schedule column are read back byte-
// for-byte identical before and after the request — proving the ADR-0003
// staleness overlay is genuinely computed at read time, never written back
// over target_schedule.state (ADR-0002 Consequences' load-bearing
// separation this session was told not to touch).
func TestGetTargetStatus_StaleAgentDisplaysUnknownWithoutMutatingRawState(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-status-stale@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	agent, err := agentauth.CreateAgent(t.Context(), pool, "test-status-stale-agent", 60)
	if err != nil {
		t.Fatalf("create test agent: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(t.Context(), `DELETE FROM agents WHERE id = $1::uuid`, agent.ID) })

	// A last_heartbeat_at far enough in the past to be stale under
	// agentauth.IsStale's fixed 3x-report-interval rule (60s here, so
	// anything past 180s ago is stale) — 1 hour is comfortably past that.
	staleHeartbeat := time.Now().Add(-1 * time.Hour)
	if _, err := pool.Exec(t.Context(), `UPDATE agents SET last_heartbeat_at = $1 WHERE id = $2::uuid`, staleHeartbeat, agent.ID); err != nil {
		t.Fatalf("set stale heartbeat: %v", err)
	}

	created := createTestTarget(t, r, pool, cookie, "http://example.invalid/status-stale")

	// Assign the target to the stale agent, and put its raw alert state at
	// something other than the zero-value "healthy" so a bug that
	// accidentally reads/writes the wrong column would be visible.
	assignBody := []byte(`{"agent_id":"` + agent.ID + `"}`)
	wPatch := doRequest(t, r, http.MethodPatch, "/api/v1/targets/"+created.ID, cookie, assignBody)
	if wPatch.Code != http.StatusOK {
		t.Fatalf("expected 200 assigning target to agent, got %d: %s", wPatch.Code, wPatch.Body.String())
	}
	if _, err := pool.Exec(t.Context(), `UPDATE target_schedule SET state = 'suspect', streak = 2 WHERE target_id = $1::uuid`, created.ID); err != nil {
		t.Fatalf("set raw state: %v", err)
	}

	type rawRow struct {
		State         string
		Streak        int
		LastCheckedAt *time.Time
	}
	readRaw := func() rawRow {
		var row rawRow
		if err := pool.QueryRow(t.Context(), `SELECT state, streak, last_checked_at FROM target_schedule WHERE target_id = $1::uuid`, created.ID).
			Scan(&row.State, &row.Streak, &row.LastCheckedAt); err != nil {
			t.Fatalf("read raw target_schedule row: %v", err)
		}
		return row
	}

	before := readRaw()

	w := doRequest(t, r, http.MethodGet, "/api/v1/targets/"+created.ID+"/status", cookie, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var status targetStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status response: %v", err)
	}

	after := readRaw()
	if before != after {
		t.Fatalf("expected target_schedule row untouched by a read-only status request: before=%+v after=%+v", before, after)
	}

	if status.RawState != "suspect" {
		t.Fatalf("expected raw_state to pass through target_schedule.state verbatim ('suspect'), got %q", status.RawState)
	}
	if status.DisplayState != "unknown" {
		t.Fatalf("expected display_state='unknown' for a target assigned to a stale agent, got %q", status.DisplayState)
	}
	if status.AgentStale == nil || !*status.AgentStale {
		t.Fatalf("expected agent_stale=true, got %v", status.AgentStale)
	}
	if status.AgentID == nil || *status.AgentID != agent.ID {
		t.Fatalf("expected agent_id=%s, got %v", agent.ID, status.AgentID)
	}
}

// TestGetTargetStatus_OpenIncidentSurfaced proves open_incident is populated
// exactly when raw_state is "alerting" — a direct incidents-table read, no
// new computation.
func TestGetTargetStatus_OpenIncidentSurfaced(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-status-incident@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	created := createTestTarget(t, r, pool, cookie, "http://example.invalid/status-incident")

	if _, err := pool.Exec(t.Context(), `UPDATE target_schedule SET state = 'alerting', streak = 3 WHERE target_id = $1::uuid`, created.ID); err != nil {
		t.Fatalf("set raw state: %v", err)
	}
	var incidentID int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO incidents (target_id, opened_at) VALUES ($1::uuid, now()) RETURNING id`, created.ID).Scan(&incidentID); err != nil {
		t.Fatalf("insert test incident: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(t.Context(), `DELETE FROM incidents WHERE id = $1`, incidentID) })

	w := doRequest(t, r, http.MethodGet, "/api/v1/targets/"+created.ID+"/status", cookie, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var status targetStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if status.RawState != "alerting" {
		t.Fatalf("expected raw_state='alerting', got %q", status.RawState)
	}
	if status.OpenIncident == nil || status.OpenIncident.ID != incidentID {
		t.Fatalf("expected open_incident.id=%d, got %+v", incidentID, status.OpenIncident)
	}
}
