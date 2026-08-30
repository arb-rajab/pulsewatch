package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/arb-rajab/pulsewatch/backend/internal/agentapi"
)

// maxBodyReadBytes bounds how much of an HTTP response body this agent
// reads for body_match_pattern checking — mirrors
// internal/scheduler/check.go's own bound (the server-executed check path),
// so an agent-executed check behaves identically to a server-executed one
// from the operator's point of view.
const maxBodyReadBytes = 64 * 1024

// checkOutcome is one locally-executed check's result — this agent's own
// minimal equivalent of scheduler.checkOutcome, self-contained rather than
// importing internal/scheduler (whose CheckJob/checkOutcome types are
// shaped around the server's own Postgres-leased execution path, not a
// standalone binary with no database access at all, ADR-0003).
type checkOutcome struct {
	success       bool
	latency       time.Duration
	statusCode    *int32
	failureReason *string // "timeout" | "refused" | "status_mismatch" | "body_mismatch"
}

// executeCheck runs one assigned target's check locally — the agent's own
// side of "execute checks locally and reports results" (FR-017). Success
// criteria mirror the server's own executeCheck exactly (2xx status,
// checked before any body_match_pattern) so an operator sees identical
// semantics regardless of which side actually ran the check.
func executeCheck(ctx context.Context, item agentapi.AssignmentListItem) checkOutcome {
	checkCtx, cancel := context.WithTimeout(ctx, time.Duration(item.TimeoutSeconds)*time.Second)
	defer cancel()

	start := time.Now()
	if item.Type == "tcp" {
		return executeTCPCheck(checkCtx, item, start)
	}
	return executeHTTPCheck(checkCtx, item, start)
}

func executeHTTPCheck(ctx context.Context, item agentapi.AssignmentListItem, start time.Time) checkOutcome {
	url := ""
	if item.URL != nil {
		url = *item.URL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		reason := "refused"
		return checkOutcome{success: false, latency: time.Since(start), failureReason: &reason}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		latency := time.Since(start)
		reason := "refused"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "timeout"
		}
		return checkOutcome{success: false, latency: latency, failureReason: &reason}
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyReadBytes))
	latency := time.Since(start)
	statusCode := int32(resp.StatusCode) //nolint:gosec // HTTP status codes fit comfortably in int32

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		reason := "status_mismatch"
		return checkOutcome{success: false, latency: latency, statusCode: &statusCode, failureReason: &reason}
	}

	if item.BodyMatchPattern != nil && !strings.Contains(string(body), *item.BodyMatchPattern) {
		reason := "body_mismatch"
		return checkOutcome{success: false, latency: latency, statusCode: &statusCode, failureReason: &reason}
	}

	return checkOutcome{success: true, latency: latency, statusCode: &statusCode}
}

func executeTCPCheck(ctx context.Context, item agentapi.AssignmentListItem, start time.Time) checkOutcome {
	if item.Host == nil || item.Port == nil {
		reason := "refused"
		return checkOutcome{success: false, latency: time.Since(start), failureReason: &reason}
	}

	dialer := &net.Dialer{}
	addr := fmt.Sprintf("%s:%d", *item.Host, *item.Port)
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	latency := time.Since(start)
	if err != nil {
		reason := "refused"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "timeout"
		}
		return checkOutcome{success: false, latency: latency, failureReason: &reason}
	}
	_ = conn.Close()

	return checkOutcome{success: true, latency: latency}
}
