package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arb-rajab/pulsewatch/backend/internal/agentapi"
)

func httpAssignment(url string, bodyMatch *string) agentapi.AssignmentListItem {
	return agentapi.AssignmentListItem{
		TargetID:         "test-target",
		Type:             "http",
		URL:              &url,
		BodyMatchPattern: bodyMatch,
		IntervalSeconds:  60,
		TimeoutSeconds:   2,
	}
}

func TestExecuteCheck_HTTPSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	outcome := executeCheck(t.Context(), httpAssignment(srv.URL, nil))
	if !outcome.success {
		t.Fatalf("expected success, got failure with reason %v", outcome.failureReason)
	}
	if outcome.statusCode == nil || *outcome.statusCode != 200 {
		t.Fatalf("expected statusCode=200, got %v", outcome.statusCode)
	}
}

func TestExecuteCheck_HTTPStatusMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	outcome := executeCheck(t.Context(), httpAssignment(srv.URL, nil))
	if outcome.success {
		t.Fatal("expected failure for a 500 response")
	}
	if outcome.failureReason == nil || *outcome.failureReason != "status_mismatch" {
		t.Fatalf("expected failureReason=status_mismatch, got %v", outcome.failureReason)
	}
}

func TestExecuteCheck_HTTPBodyMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	pattern := "goodbye"
	outcome := executeCheck(t.Context(), httpAssignment(srv.URL, &pattern))
	if outcome.success {
		t.Fatal("expected failure when body_match_pattern doesn't match")
	}
	if outcome.failureReason == nil || *outcome.failureReason != "body_mismatch" {
		t.Fatalf("expected failureReason=body_mismatch, got %v", outcome.failureReason)
	}
}

func TestExecuteCheck_HTTPBodyMatchSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	pattern := "hello"
	outcome := executeCheck(t.Context(), httpAssignment(srv.URL, &pattern))
	if !outcome.success {
		t.Fatalf("expected success when body_match_pattern matches, got failure reason %v", outcome.failureReason)
	}
}

func TestExecuteCheck_TCPSuccess(t *testing.T) {
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	host := "127.0.0.1"
	port := int32(addr.Port)
	item := agentapi.AssignmentListItem{
		TargetID:        "test-tcp-target",
		Type:            "tcp",
		Host:            &host,
		Port:            &port,
		IntervalSeconds: 60,
		TimeoutSeconds:  2,
	}

	outcome := executeCheck(t.Context(), item)
	if !outcome.success {
		t.Fatalf("expected success, got failure with reason %v", outcome.failureReason)
	}
}

func TestExecuteCheck_TCPRefused(t *testing.T) {
	// Bind and immediately close to get a real port nothing is listening on.
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	_ = ln.Close()

	host := "127.0.0.1"
	port := int32(addr.Port)
	item := agentapi.AssignmentListItem{
		TargetID:        "test-tcp-refused",
		Type:            "tcp",
		Host:            &host,
		Port:            &port,
		IntervalSeconds: 60,
		TimeoutSeconds:  2,
	}

	outcome := executeCheck(t.Context(), item)
	if outcome.success {
		t.Fatal("expected failure connecting to a closed port")
	}
	if outcome.failureReason == nil {
		t.Fatal("expected a non-nil failureReason")
	}
}
