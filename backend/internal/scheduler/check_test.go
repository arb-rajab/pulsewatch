package scheduler

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestExecuteHTTPCheck_SuccessOn2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	job := CheckJob{Type: "http", URLOrHost: srv.URL, TimeoutSeconds: 5}
	outcome := executeCheck(t.Context(), job)

	if !outcome.success {
		t.Fatalf("expected success, got failure reason %v", outcome.failureReason)
	}
	if outcome.statusCode == nil || *outcome.statusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %v", outcome.statusCode)
	}
}

func TestExecuteHTTPCheck_StatusMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	job := CheckJob{Type: "http", URLOrHost: srv.URL, TimeoutSeconds: 5}
	outcome := executeCheck(t.Context(), job)

	if outcome.success {
		t.Fatal("expected failure on 500")
	}
	if outcome.failureReason == nil || *outcome.failureReason != "status_mismatch" {
		t.Fatalf("expected status_mismatch, got %v", outcome.failureReason)
	}
}

func TestExecuteHTTPCheck_BodyMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("something else entirely"))
	}))
	defer srv.Close()

	pattern := "expected-fragment"
	job := CheckJob{Type: "http", URLOrHost: srv.URL, TimeoutSeconds: 5, BodyMatchPattern: &pattern}
	outcome := executeCheck(t.Context(), job)

	if outcome.success {
		t.Fatal("expected failure on body mismatch")
	}
	if outcome.failureReason == nil || *outcome.failureReason != "body_mismatch" {
		t.Fatalf("expected body_mismatch, got %v", outcome.failureReason)
	}
	if outcome.bodyMatchFragment == nil || *outcome.bodyMatchFragment != "something else entirely" {
		t.Fatalf("expected captured fragment, got %v", outcome.bodyMatchFragment)
	}
}

func TestExecuteHTTPCheck_BodyMatchSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("healthy: yes"))
	}))
	defer srv.Close()

	pattern := "healthy"
	job := CheckJob{Type: "http", URLOrHost: srv.URL, TimeoutSeconds: 5, BodyMatchPattern: &pattern}
	outcome := executeCheck(t.Context(), job)

	if !outcome.success {
		t.Fatalf("expected success, got failure reason %v", outcome.failureReason)
	}
}

func TestExecuteHTTPCheck_TimeoutReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	job := CheckJob{Type: "http", URLOrHost: srv.URL, TimeoutSeconds: 1}
	outcome := executeCheck(ctx, job)

	if outcome.success {
		t.Fatal("expected timeout failure")
	}
	if outcome.failureReason == nil || *outcome.failureReason != "timeout" {
		t.Fatalf("expected timeout, got %v", outcome.failureReason)
	}
}

func TestExecuteTCPCheck_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()

	host, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	port32 := int32(port)
	job := CheckJob{Type: "tcp", URLOrHost: host, Port: &port32, TimeoutSeconds: 5}
	outcome := executeCheck(t.Context(), job)

	if !outcome.success {
		t.Fatalf("expected TCP dial success, got failure reason %v", outcome.failureReason)
	}
}

func TestExecuteTCPCheck_Refused(t *testing.T) {
	// Port 1 is reserved and never listening in this test environment.
	port := int32(1)
	job := CheckJob{Type: "tcp", URLOrHost: "127.0.0.1", Port: &port, TimeoutSeconds: 2}
	outcome := executeCheck(t.Context(), job)

	if outcome.success {
		t.Fatal("expected TCP dial failure")
	}
	if outcome.failureReason == nil {
		t.Fatal("expected a failure reason")
	}
}
