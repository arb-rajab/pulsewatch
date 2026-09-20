// Package main implements the otlp-normalizer serverless function: a
// stateless validator/normalizer for the same simplified OTLP/HTTP+JSON
// check_result and heartbeat shape the always-on backend accepts at
// POST /v1/logs (backend/internal/agentapi, docs/architecture/openapi.yaml).
//
// This file is a deliberate, self-contained reimplementation of that
// contract's validation rules — not an import of backend/internal/agentapi.
// The two are kept independent on purpose (see docs/adr/ADR-0009): this
// function has no database, no dispatcher, and no agent-identity concept,
// so it can only ever normalize a payload's shape, never decide whether a
// given agent is allowed to write a given target's result. Duplicating the
// ~80 lines of pure parsing logic here avoids forcing the real ingestion
// path to depend on a Lambda-shaped module, or forcing this Lambda to vendor
// backend's pgx/gin dependency tree just to reuse a few validation
// functions.
package main

import (
	"errors"
	"math"
	"strconv"
	"time"
)

// The same wire vocabulary docs/architecture/openapi.yaml and
// backend/internal/agentapi/otlp.go define — copied here verbatim, not
// imported (see the package doc comment).
const (
	eventTypeCheckResult = "check_result"
	eventTypeHeartbeat   = "heartbeat"

	attrEventType     = "pulsewatch.event_type"
	attrTargetID      = "pulsewatch.target_id"
	attrOutcome       = "pulsewatch.outcome"
	attrLatencyMS     = "pulsewatch.latency_ms"
	attrStatusCode    = "pulsewatch.status_code"
	attrFailureReason = "pulsewatch.failure_reason"

	outcomeSuccess = "success"
	outcomeFailure = "failure"
)

// otlpValue is the simplified OTLP AnyValue this shape supports — a string
// or an int (OTLP encodes int64 as a decimal string in its JSON mapping to
// avoid precision loss), identical to agentapi.OtlpValue.
type otlpValue struct {
	StringValue *string `json:"stringValue,omitempty"`
	IntValue    *string `json:"intValue,omitempty"`
}

func (v otlpValue) asString() (string, bool) {
	if v.StringValue == nil {
		return "", false
	}
	return *v.StringValue, true
}

func (v otlpValue) asInt() (int64, bool) {
	if v.IntValue == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(*v.IntValue, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

type otlpKeyValue struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

type otlpLogRecord struct {
	TimeUnixNano string         `json:"timeUnixNano"`
	Attributes   []otlpKeyValue `json:"attributes"`
}

// normalizeRequest is this function's own request shape: a batch of
// standalone log records with no resource/scope wrapper and no agent.id —
// this function has no agent identity to check them against, so it accepts
// exactly the part of the wire format it can actually validate.
type normalizeRequest struct {
	LogRecords []otlpLogRecord `json:"logRecords"`
}

// normalizedCheckResult is one record's validated, normalized output —
// ready for a caller to persist, but never persisted by this function
// itself.
type normalizedCheckResult struct {
	CheckedAt     time.Time `json:"checkedAt"`
	TargetID      string    `json:"targetId"`
	Success       bool      `json:"success"`
	LatencyMS     int       `json:"latencyMs"`
	StatusCode    *int32    `json:"statusCode,omitempty"`
	FailureReason *string   `json:"failureReason,omitempty"`
}

type normalizedHeartbeat struct {
	CheckedAt time.Time `json:"checkedAt"`
}

// normalizedRecord is a discriminated result: exactly one of CheckResult or
// Heartbeat is set on success; Error is set instead when the record failed
// validation.
type normalizedRecord struct {
	Index       int                    `json:"index"`
	EventType   string                 `json:"eventType,omitempty"`
	CheckResult *normalizedCheckResult `json:"checkResult,omitempty"`
	Heartbeat   *normalizedHeartbeat   `json:"heartbeat,omitempty"`
	Error       string                 `json:"error,omitempty"`
}

// normalizeResponse mirrors the shape of the real endpoint's partial-success
// reporting (exportLogsServiceResponse in backend/internal/agentapi/logs.go)
// closely enough that a caller already handling that response can read this
// one the same way, without being the same Go type.
type normalizeResponse struct {
	Accepted int                `json:"accepted"`
	Rejected int                `json:"rejected"`
	Records  []normalizedRecord `json:"records"`
}

func attrMap(attrs []otlpKeyValue) map[string]otlpValue {
	m := make(map[string]otlpValue, len(attrs))
	for _, kv := range attrs {
		m[kv.Key] = kv.Value
	}
	return m
}

var errUnknownEventType = errors.New("unknown pulsewatch.event_type")

// normalizeRecord validates and normalizes one log record — the exact same
// field-by-field rules backend/internal/agentapi/logs.go's processLogRecord
// and processCheckResult apply, minus the two things this function
// structurally cannot do: look up whether the target belongs to the caller,
// and persist or dispatch anything.
func normalizeRecord(rec otlpLogRecord) normalizedRecord {
	nanos, err := strconv.ParseInt(rec.TimeUnixNano, 10, 64)
	if err != nil {
		return normalizedRecord{Error: "timeUnixNano is not a valid integer"}
	}
	checkedAt := time.Unix(0, nanos).UTC()

	attrs := attrMap(rec.Attributes)
	eventType, ok := attrs[attrEventType].asString()
	if !ok {
		return normalizedRecord{Error: "missing pulsewatch.event_type"}
	}

	switch eventType {
	case eventTypeHeartbeat:
		return normalizedRecord{EventType: eventType, Heartbeat: &normalizedHeartbeat{CheckedAt: checkedAt}}
	case eventTypeCheckResult:
		cr, err := normalizeCheckResult(checkedAt, attrs)
		if err != nil {
			return normalizedRecord{EventType: eventType, Error: err.Error()}
		}
		return normalizedRecord{EventType: eventType, CheckResult: cr}
	default:
		return normalizedRecord{Error: errUnknownEventType.Error()}
	}
}

// normalizeCheckResult applies the identical range-checked int conversions
// backend/internal/agentapi/logs.go's processCheckResult uses for
// pulsewatch.latency_ms and pulsewatch.status_code: both start life as an
// agent-supplied decimal string that can carry any int64 on the wire, so
// each is bounds-checked against int32 before the narrowing conversion
// rather than trusting a bare cast.
func normalizeCheckResult(checkedAt time.Time, attrs map[string]otlpValue) (*normalizedCheckResult, error) {
	targetID, ok := attrs[attrTargetID].asString()
	if !ok {
		return nil, errors.New("missing pulsewatch.target_id")
	}
	outcome, ok := attrs[attrOutcome].asString()
	if !ok || (outcome != outcomeSuccess && outcome != outcomeFailure) {
		return nil, errors.New("missing or invalid pulsewatch.outcome")
	}
	latencyMS, ok := attrs[attrLatencyMS].asInt()
	if !ok || latencyMS < 0 || latencyMS > math.MaxInt32 {
		return nil, errors.New("missing or invalid pulsewatch.latency_ms")
	}

	var statusCode *int32
	if n, ok := attrs[attrStatusCode].asInt(); ok {
		if n < 0 || n > math.MaxInt32 {
			return nil, errors.New("pulsewatch.status_code out of range")
		}
		v := int32(n)
		statusCode = &v
	}
	var failureReason *string
	if s, ok := attrs[attrFailureReason].asString(); ok && s != "" {
		failureReason = &s
	}

	return &normalizedCheckResult{
		CheckedAt:     checkedAt,
		TargetID:      targetID,
		Success:       outcome == outcomeSuccess,
		LatencyMS:     int(latencyMS),
		StatusCode:    statusCode,
		FailureReason: failureReason,
	}, nil
}

// normalize runs normalizeRecord over a whole batch and tallies accepted vs
// rejected — the pure, side-effect-free core this function's Lambda handler
// (handler.go) wraps in an HTTP request/response shape.
func normalize(req normalizeRequest) normalizeResponse {
	resp := normalizeResponse{Records: make([]normalizedRecord, len(req.LogRecords))}
	for i, rec := range req.LogRecords {
		r := normalizeRecord(rec)
		r.Index = i
		resp.Records[i] = r
		if r.Error == "" {
			resp.Accepted++
		} else {
			resp.Rejected++
		}
	}
	return resp
}
