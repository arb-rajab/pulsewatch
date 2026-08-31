package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestOperatorRouterHealthReturns200OK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// /health doesn't touch Postgres, so a nil pool/key is safe here.
	router := setupOperatorRouter(nil, nil, nil)

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	const expected = `{"status":"ok"}`
	if w.Body.String() != expected {
		t.Fatalf("expected body %q, got %q", expected, w.Body.String())
	}
}

func TestAgentRouterHealthReturns200OK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// /health doesn't touch Postgres or the agentapi routes, so a nil pool
	// and nil dispatcher/key are safe here — they'd only matter if this
	// test actually invoked an agentapi route.
	router := setupAgentRouter(nil, nil, nil, nil)

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	const expected = `{"status":"ok"}`
	if w.Body.String() != expected {
		t.Fatalf("expected body %q, got %q", expected, w.Body.String())
	}
}

// TestOperatorRouterHasNoAgentRoutes is this session's own regression proof
// for the cross-port session-cookie leak found closing out Session 10:
// operatorapi's router must never carry an agentapi route, or a session
// cookie replayed against whatever listener serves this engine would reach
// a real handler again.
func TestOperatorRouterHasNoAgentRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := setupOperatorRouter(nil, nil, nil)

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/agent/assignments", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected /api/v1/agent/assignments to be unmounted (404) on the operator router, got %d", w.Code)
	}
}

// TestAgentRouterHasNoOperatorRoutes is the symmetric proof: the agent
// router must never carry an operatorapi route.
func TestAgentRouterHasNoOperatorRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := setupAgentRouter(nil, nil, nil, nil)

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/targets", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected /api/v1/targets to be unmounted (404) on the agent router, got %d", w.Code)
	}
}
