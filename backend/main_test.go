package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestHealthReturns200OK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// /health doesn't touch Postgres or the agentapi routes, so a nil pool
	// and nil dispatcher/key are safe here — they'd only matter if this
	// test actually invoked an agentapi route.
	router := setupRouter(nil, nil, nil, nil)

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
