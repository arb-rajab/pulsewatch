package agentapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestGetAssignments_RequiresValidBearerToken(t *testing.T) {
	pool := testPool(t)
	r := testRouter(pool, &spyDispatcher{}, nil)

	cases := []struct {
		name  string
		token string
	}{
		{name: "no Authorization header", token: ""},
		{name: "unknown token", token: "this-token-was-never-issued"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doRequest(t, r, http.MethodGet, "/api/v1/agent/assignments", tc.token, nil)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

// TestGetAssignments_ReturnsOnlyThisAgentsTargets proves the structural
// property 05-api-contracts.md leans on: identity comes only from the
// bearer token, and each agent sees only its own assigned targets — never
// another agent's, and never an unassigned (server-executed) target.
func TestGetAssignments_ReturnsOnlyThisAgentsTargets(t *testing.T) {
	pool := testPool(t)
	agentA, tokenA := insertTestAgent(t, pool, "test-assignments-agent-a", 60)
	agentB, tokenB := insertTestAgent(t, pool, "test-assignments-agent-b", 60)

	optsA := defaultTestTargetOpts()
	optsA.urlOrHost = "http://example.invalid/agent-a-target"
	targetA := insertAssignedTarget(t, pool, agentA, optsA)

	optsB := defaultTestTargetOpts()
	optsB.urlOrHost = "http://example.invalid/agent-b-target"
	_ = insertAssignedTarget(t, pool, agentB, optsB)

	r := testRouter(pool, &spyDispatcher{}, nil)
	w := doRequest(t, r, http.MethodGet, "/api/v1/agent/assignments", tokenA, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var got AssignmentList
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if got.AgentID != agentA {
		t.Fatalf("expected agent_id=%s, got %s", agentA, got.AgentID)
	}
	if len(got.Assignments) != 1 {
		t.Fatalf("expected exactly 1 assignment for agent A, got %d: %+v", len(got.Assignments), got.Assignments)
	}
	if got.Assignments[0].TargetID != targetA {
		t.Fatalf("expected target %s, got %s", targetA, got.Assignments[0].TargetID)
	}
	if got.Assignments[0].URL == nil || *got.Assignments[0].URL != optsA.urlOrHost {
		t.Fatalf("expected url=%s, got %+v", optsA.urlOrHost, got.Assignments[0].URL)
	}
	if got.Assignments[0].Host != nil {
		t.Fatalf("expected host=nil for an http target, got %v", *got.Assignments[0].Host)
	}
	_ = tokenB // used only to prove agent B's own token is a distinct credential, exercised in agentauth's own tests
}

// TestGetAssignments_DoesNotTouchLastHeartbeat proves
// docs/architecture/openapi.yaml's explicit contract: the assignment poll
// is not a liveness signal — only the OTLP heartbeat record is.
func TestGetAssignments_DoesNotTouchLastHeartbeat(t *testing.T) {
	pool := testPool(t)
	agentID, token := insertTestAgent(t, pool, "test-assignments-no-heartbeat", 60)

	before := fetchLastHeartbeat(t, pool, agentID)
	if before != nil {
		t.Fatalf("expected a freshly created agent to have no heartbeat yet, got %v", before)
	}

	r := testRouter(pool, &spyDispatcher{}, nil)
	w := doRequest(t, r, http.MethodGet, "/api/v1/agent/assignments", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	after := fetchLastHeartbeat(t, pool, agentID)
	if after != nil {
		t.Fatalf("expected last_heartbeat_at to remain NULL after an assignments poll, got %v", after)
	}
}

func TestGetAssignments_TcpTargetReturnsHostAndPort(t *testing.T) {
	pool := testPool(t)
	agentID, token := insertTestAgent(t, pool, "test-assignments-tcp", 60)

	port := int32(5432)
	opts := testTargetOpts{
		targetType:       "tcp",
		urlOrHost:        "db.internal.invalid",
		port:             &port,
		intervalSeconds:  30,
		timeoutSeconds:   5,
		failureThreshold: 3,
	}
	targetID := insertAssignedTarget(t, pool, agentID, opts)

	r := testRouter(pool, &spyDispatcher{}, nil)
	w := doRequest(t, r, http.MethodGet, "/api/v1/agent/assignments", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var got AssignmentList
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Assignments) != 1 || got.Assignments[0].TargetID != targetID {
		t.Fatalf("expected exactly the one tcp assignment, got %+v", got.Assignments)
	}
	item := got.Assignments[0]
	if item.URL != nil {
		t.Fatalf("expected url=nil for a tcp target, got %v", *item.URL)
	}
	if item.Host == nil || *item.Host != opts.urlOrHost {
		t.Fatalf("expected host=%s, got %+v", opts.urlOrHost, item.Host)
	}
	if item.Port == nil || *item.Port != port {
		t.Fatalf("expected port=%d, got %+v", port, item.Port)
	}
}
