package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Session 5 MVP boundary (02-requirements.md): success for an HTTP check is
// a 2xx status code, checked before any configured body-match pattern — no
// `expected_status_code` column exists on `targets` (04-data-model.md), so
// this is the simplest rule consistent with the schema's own
// `failure_reason` vocabulary ('status_mismatch' as distinct from
// 'body_mismatch'). body_match_pattern is matched as a literal substring,
// not a regex — the schema calls it a "pattern" but does not specify a
// syntax, and a literal substring is the simplest rule that satisfies
// US-001's acceptance criteria.
const (
	maxBodyReadBytes     = 64 * 1024
	bodyFragmentCapBytes = 512
)

// executeCheck runs the claimed check against its target and returns the
// outcome to be persisted. It has no side effect beyond its return value —
// ADR-0001's safety depends on check execution being idempotent to re-run.
func executeCheck(ctx context.Context, job CheckJob) checkOutcome {
	start := time.Now()
	switch job.Type {
	case "tcp":
		return executeTCPCheck(ctx, job, start)
	default: // "http" — targets.type is schema-constrained to http|tcp
		return executeHTTPCheck(ctx, job, start)
	}
}

func executeHTTPCheck(ctx context.Context, job CheckJob, start time.Time) checkOutcome {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, job.URLOrHost, nil)
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

	if job.BodyMatchPattern != nil && !strings.Contains(string(body), *job.BodyMatchPattern) {
		reason := "body_mismatch"
		fragment, truncated := capFragment(body)
		return checkOutcome{
			success: false, latency: latency, statusCode: &statusCode, failureReason: &reason,
			bodyMatchFragment: &fragment, bodyMatchFragmentTruncated: truncated,
		}
	}

	if job.BodyMatchPattern != nil {
		fragment, truncated := capFragment(body)
		return checkOutcome{
			success: true, latency: latency, statusCode: &statusCode,
			bodyMatchFragment: &fragment, bodyMatchFragmentTruncated: truncated,
		}
	}

	return checkOutcome{success: true, latency: latency, statusCode: &statusCode}
}

func executeTCPCheck(ctx context.Context, job CheckJob, start time.Time) checkOutcome {
	if job.Port == nil {
		reason := "refused"
		return checkOutcome{success: false, latency: time.Since(start), failureReason: &reason}
	}

	dialer := &net.Dialer{}
	addr := fmt.Sprintf("%s:%d", job.URLOrHost, *job.Port)
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

// capFragment caps a captured response-body fragment at bodyFragmentCapBytes
// (04-data-model.md: "512 bytes is enough to show an operator what did or
// didn't match without storing large fractions of a monitored target's
// response body").
func capFragment(body []byte) (fragment string, truncated bool) {
	if len(body) <= bodyFragmentCapBytes {
		return string(body), false
	}
	return string(body[:bodyFragmentCapBytes]), true
}
