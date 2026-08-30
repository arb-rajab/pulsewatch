package operatorapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/agentapi"
	"github.com/arb-rajab/pulsewatch/backend/internal/alerting"
)

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

// agentSurfaceRouter builds the real internal/agentapi router against the
// same pool — used only to prove an agent token minted through
// operatorapi's POST /api/v1/agents actually works on the real agent-facing
// surface, end to end across both packages.
func agentSurfaceRouter(pool *pgxpool.Pool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	agentapi.RegisterRoutes(r, pool, alerting.NewLogDispatcher(nopLogger()), testEncryptionKey, nopLogger())
	return r
}

// TestCreateAgent_TokenWorksOnTheRealAgentFacingSurface is this session's
// direct proof that retiring cmd/seed-agent as the *only* provisioning path
// is justified: an agent created through the real, gated HTTP handler this
// session built produces a credential that is genuinely interchangeable
// with one minted by cmd/seed-agent, because both call the identical
// agentauth.CreateAgent function underneath.
func TestCreateAgent_TokenWorksOnTheRealAgentFacingSurface(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-agents-e2e@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	wCreate := doRequest(t, r, http.MethodPost, "/api/v1/agents", cookie, []byte(`{"name":"test-agents-e2e-agent","report_interval_seconds":45}`))
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", wCreate.Code, wCreate.Body.String())
	}
	var created agentCreatedResponse
	if err := json.Unmarshal(wCreate.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(t.Context(), `DELETE FROM agents WHERE id = $1::uuid`, created.ID)
	})
	if created.Token == "" {
		t.Fatal("expected a plaintext token in the create response")
	}
	if created.ReportIntervalSeconds != 45 {
		t.Fatalf("expected report_interval_seconds=45, got %d", created.ReportIntervalSeconds)
	}

	agentR := agentSurfaceRouter(pool)
	req := mustRequest(t, http.MethodGet, "/api/v1/agent/assignments", nil)
	req.Header.Set("Authorization", "Bearer "+created.Token)
	w := recordRequest(agentR, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected the operator-provisioned agent's token to authenticate on the real agent-facing surface, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAgents_ListGetDelete_ThroughRealGatedHTTP(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-agents-crud@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	wCreate := doRequest(t, r, http.MethodPost, "/api/v1/agents", cookie, []byte(`{"name":"test-agents-crud-agent"}`))
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", wCreate.Code, wCreate.Body.String())
	}
	var created agentCreatedResponse
	if err := json.Unmarshal(wCreate.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(t.Context(), `DELETE FROM agents WHERE id = $1::uuid`, created.ID)
	})
	if created.ReportIntervalSeconds != defaultReportIntervalSeconds {
		t.Fatalf("expected the default report_interval_seconds=%d, got %d", defaultReportIntervalSeconds, created.ReportIntervalSeconds)
	}

	wGet := doRequest(t, r, http.MethodGet, "/api/v1/agents/"+created.ID, cookie, nil)
	if wGet.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wGet.Code, wGet.Body.String())
	}
	var got agentResponse
	if err := json.Unmarshal(wGet.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if !got.IsStale {
		t.Fatal("expected a freshly created agent with no heartbeat yet to read as stale (NFR-008: never-heartbeated counts as stale)")
	}
	if got.AssignedTargetCount != 0 {
		t.Fatalf("expected 0 assigned targets, got %d", got.AssignedTargetCount)
	}

	wList := doRequest(t, r, http.MethodGet, "/api/v1/agents", cookie, nil)
	if wList.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wList.Code, wList.Body.String())
	}

	wDelete := doRequest(t, r, http.MethodDelete, "/api/v1/agents/"+created.ID, cookie, nil)
	if wDelete.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", wDelete.Code, wDelete.Body.String())
	}
	wGetAfterDelete := doRequest(t, r, http.MethodGet, "/api/v1/agents/"+created.ID, cookie, nil)
	if wGetAfterDelete.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", wGetAfterDelete.Code)
	}
}

// TestDeleteAgent_BlockedWhileTargetsAssigned proves 05-api-contracts.md's
// 409 rule end to end through the real HTTP surface: decommissioning must
// be blocked while any target still references the agent, and becomes
// possible again once that assignment is explicitly cleared — never an
// implicit side effect of the DELETE itself.
func TestDeleteAgent_BlockedWhileTargetsAssigned(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-agents-409@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	wCreateAgent := doRequest(t, r, http.MethodPost, "/api/v1/agents", cookie, []byte(`{"name":"test-agents-409-agent"}`))
	var agent agentCreatedResponse
	if err := json.Unmarshal(wCreateAgent.Body.Bytes(), &agent); err != nil {
		t.Fatalf("decode agent create response: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(t.Context(), `DELETE FROM agents WHERE id = $1::uuid`, agent.ID)
	})

	wCreateTarget := doRequest(t, r, http.MethodPost, "/api/v1/targets", cookie,
		[]byte(`{"type":"http","url":"http://example.invalid/409-target","interval_seconds":30,"timeout_seconds":5,"agent_id":"`+agent.ID+`"}`))
	if wCreateTarget.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", wCreateTarget.Code, wCreateTarget.Body.String())
	}
	var target targetResponse
	if err := json.Unmarshal(wCreateTarget.Body.Bytes(), &target); err != nil {
		t.Fatalf("decode target create response: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(t.Context(), `DELETE FROM targets WHERE id = $1::uuid`, target.ID)
	})

	wDeleteBlocked := doRequest(t, r, http.MethodDelete, "/api/v1/agents/"+agent.ID, cookie, nil)
	if wDeleteBlocked.Code != http.StatusConflict {
		t.Fatalf("expected 409 while a target is still assigned, got %d: %s", wDeleteBlocked.Code, wDeleteBlocked.Body.String())
	}

	// Unassign, then decommissioning succeeds.
	wUnassign := doRequest(t, r, http.MethodPatch, "/api/v1/targets/"+target.ID, cookie, []byte(`{"agent_id":null}`))
	if wUnassign.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wUnassign.Code, wUnassign.Body.String())
	}
	var unassigned targetResponse
	if err := json.Unmarshal(wUnassign.Body.Bytes(), &unassigned); err != nil {
		t.Fatalf("decode unassign response: %v", err)
	}
	if unassigned.AgentID != nil {
		t.Fatalf("expected agent_id to become null after unassignment, got %v", *unassigned.AgentID)
	}

	wDeleteNow := doRequest(t, r, http.MethodDelete, "/api/v1/agents/"+agent.ID, cookie, nil)
	if wDeleteNow.Code != http.StatusNoContent {
		t.Fatalf("expected 204 after unassigning its target, got %d: %s", wDeleteNow.Code, wDeleteNow.Body.String())
	}
}

func TestRotateAgentCredential_ThroughRealGatedHTTP(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-agents-rotate@example.invalid", "a-real-password")
	cookie := realSessionCookie(t, operatorID)
	r := testRouter(pool)

	wCreate := doRequest(t, r, http.MethodPost, "/api/v1/agents", cookie, []byte(`{"name":"test-agents-rotate-agent"}`))
	var created agentCreatedResponse
	if err := json.Unmarshal(wCreate.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(t.Context(), `DELETE FROM agents WHERE id = $1::uuid`, created.ID)
	})

	wRotate := doRequest(t, r, http.MethodPost, "/api/v1/agents/"+created.ID+"/credential/rotate", cookie, nil)
	if wRotate.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wRotate.Code, wRotate.Body.String())
	}
	var rotated agentCredentialRotatedResponse
	if err := json.Unmarshal(wRotate.Body.Bytes(), &rotated); err != nil {
		t.Fatalf("decode rotate response: %v", err)
	}
	if rotated.Token == created.Token {
		t.Fatal("expected a fresh token after rotation")
	}

	agentR := agentSurfaceRouter(pool)

	oldReq := mustRequest(t, http.MethodGet, "/api/v1/agent/assignments", nil)
	oldReq.Header.Set("Authorization", "Bearer "+created.Token)
	oldW := recordRequest(agentR, oldReq)
	if oldW.Code != http.StatusUnauthorized {
		t.Fatalf("expected the pre-rotation token to be rejected, got %d: %s", oldW.Code, oldW.Body.String())
	}

	newReq := mustRequest(t, http.MethodGet, "/api/v1/agent/assignments", nil)
	newReq.Header.Set("Authorization", "Bearer "+rotated.Token)
	newW := recordRequest(agentR, newReq)
	if newW.Code != http.StatusOK {
		t.Fatalf("expected the rotated token to authenticate, got %d: %s", newW.Code, newW.Body.String())
	}
}
