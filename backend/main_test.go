package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestHealthReturns200OK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := setupRouter()

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
