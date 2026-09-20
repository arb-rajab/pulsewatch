package main

import (
	"math"
	"strconv"
	"testing"
	"time"
)

func strAttr(key, value string) otlpKeyValue {
	return otlpKeyValue{Key: key, Value: otlpValue{StringValue: &value}}
}

func intAttr(key string, value int64) otlpKeyValue {
	s := strconv.FormatInt(value, 10)
	return otlpKeyValue{Key: key, Value: otlpValue{IntValue: &s}}
}

func TestNormalizeRecord_Heartbeat(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	rec := otlpLogRecord{
		TimeUnixNano: strconv.FormatInt(at.UnixNano(), 10),
		Attributes:   []otlpKeyValue{strAttr(attrEventType, eventTypeHeartbeat)},
	}

	got := normalizeRecord(rec)
	if got.Error != "" {
		t.Fatalf("expected no error, got %q", got.Error)
	}
	if got.Heartbeat == nil || !got.Heartbeat.CheckedAt.Equal(at) {
		t.Fatalf("expected heartbeat at %v, got %+v", at, got.Heartbeat)
	}
	if got.CheckResult != nil {
		t.Fatalf("expected no check result for a heartbeat, got %+v", got.CheckResult)
	}
}

func TestNormalizeRecord_CheckResult_Valid(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	rec := otlpLogRecord{
		TimeUnixNano: strconv.FormatInt(at.UnixNano(), 10),
		Attributes: []otlpKeyValue{
			strAttr(attrEventType, eventTypeCheckResult),
			strAttr(attrTargetID, "11111111-1111-1111-1111-111111111111"),
			strAttr(attrOutcome, outcomeFailure),
			intAttr(attrLatencyMS, 342),
			intAttr(attrStatusCode, 503),
			strAttr(attrFailureReason, "connection refused"),
		},
	}

	got := normalizeRecord(rec)
	if got.Error != "" {
		t.Fatalf("expected no error, got %q", got.Error)
	}
	cr := got.CheckResult
	if cr == nil {
		t.Fatalf("expected a check result")
	}
	if cr.Success {
		t.Fatalf("expected Success=false for outcome=failure")
	}
	if cr.TargetID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("unexpected target id %q", cr.TargetID)
	}
	if cr.LatencyMS != 342 {
		t.Fatalf("expected latency 342, got %d", cr.LatencyMS)
	}
	if cr.StatusCode == nil || *cr.StatusCode != 503 {
		t.Fatalf("expected status code 503, got %v", cr.StatusCode)
	}
	if cr.FailureReason == nil || *cr.FailureReason != "connection refused" {
		t.Fatalf("expected failure reason to round-trip, got %v", cr.FailureReason)
	}
	if !cr.CheckedAt.Equal(at) {
		t.Fatalf("expected checkedAt %v, got %v", at, cr.CheckedAt)
	}
}

func TestNormalizeRecord_MissingEventType(t *testing.T) {
	rec := otlpLogRecord{TimeUnixNano: "0", Attributes: nil}
	got := normalizeRecord(rec)
	if got.Error == "" {
		t.Fatalf("expected a validation error for a missing event type")
	}
}

func TestNormalizeRecord_UnknownEventType(t *testing.T) {
	rec := otlpLogRecord{TimeUnixNano: "0", Attributes: []otlpKeyValue{strAttr(attrEventType, "something_else")}}
	got := normalizeRecord(rec)
	if got.Error != errUnknownEventType.Error() {
		t.Fatalf("expected unknown event type error, got %q", got.Error)
	}
}

func TestNormalizeRecord_MalformedTimestamp(t *testing.T) {
	rec := otlpLogRecord{TimeUnixNano: "not-a-number"}
	got := normalizeRecord(rec)
	if got.Error == "" {
		t.Fatalf("expected an error for a malformed timeUnixNano")
	}
}

// TestNormalizeRecord_LatencyBoundaries mirrors
// backend/internal/agentapi/logs_test.go's TestIngestLogs_LatencyMSBoundaries
// exactly, proving this standalone reimplementation enforces the identical
// int32 range check on pulsewatch.latency_ms.
func TestNormalizeRecord_LatencyBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		latencyMS int64
		accepted  bool
	}{
		{"zero", 0, true},
		{"typical", 42, true},
		{"max_int32", math.MaxInt32, true},
		{"negative", -1, false},
		{"one_past_max_int32", math.MaxInt32 + 1, false},
		{"max_int64", math.MaxInt64, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := otlpLogRecord{
				TimeUnixNano: "1000000000",
				Attributes: []otlpKeyValue{
					strAttr(attrEventType, eventTypeCheckResult),
					strAttr(attrTargetID, "target-1"),
					strAttr(attrOutcome, outcomeSuccess),
					intAttr(attrLatencyMS, tc.latencyMS),
				},
			}
			got := normalizeRecord(rec)
			accepted := got.Error == ""
			if accepted != tc.accepted {
				t.Fatalf("latencyMS=%d: expected accepted=%v, got accepted=%v (error=%q)", tc.latencyMS, tc.accepted, accepted, got.Error)
			}
		})
	}
}

// TestNormalizeRecord_StatusCodeBoundaries mirrors
// TestIngestLogs_StatusCodeBoundaries in the same way.
func TestNormalizeRecord_StatusCodeBoundaries(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int64
		accepted   bool
	}{
		{"typical_2xx", 200, true},
		{"zero", 0, true},
		{"max_int32", math.MaxInt32, true},
		{"negative", -1, false},
		{"one_past_max_int32", math.MaxInt32 + 1, false},
		{"max_int64", math.MaxInt64, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := otlpLogRecord{
				TimeUnixNano: "1000000000",
				Attributes: []otlpKeyValue{
					strAttr(attrEventType, eventTypeCheckResult),
					strAttr(attrTargetID, "target-1"),
					strAttr(attrOutcome, outcomeSuccess),
					intAttr(attrLatencyMS, 42),
					intAttr(attrStatusCode, tc.statusCode),
				},
			}
			got := normalizeRecord(rec)
			accepted := got.Error == ""
			if accepted != tc.accepted {
				t.Fatalf("statusCode=%d: expected accepted=%v, got accepted=%v (error=%q)", tc.statusCode, tc.accepted, accepted, got.Error)
			}
		})
	}
}

func TestNormalizeRecord_MissingTargetOutcomeLatency(t *testing.T) {
	base := func() otlpLogRecord {
		return otlpLogRecord{TimeUnixNano: "1000000000", Attributes: []otlpKeyValue{strAttr(attrEventType, eventTypeCheckResult)}}
	}

	if got := normalizeRecord(base()); got.Error == "" {
		t.Fatalf("expected error for missing target_id/outcome/latency_ms")
	}

	withTarget := base()
	withTarget.Attributes = append(withTarget.Attributes, strAttr(attrTargetID, "t1"))
	if got := normalizeRecord(withTarget); got.Error == "" {
		t.Fatalf("expected error for missing outcome/latency_ms")
	}

	withOutcome := withTarget
	withOutcome.Attributes = append(withOutcome.Attributes, strAttr(attrOutcome, "not-a-real-outcome"))
	if got := normalizeRecord(withOutcome); got.Error == "" {
		t.Fatalf("expected error for invalid outcome value")
	}
}

func TestNormalize_TallyAcceptedAndRejected(t *testing.T) {
	req := normalizeRequest{
		LogRecords: []otlpLogRecord{
			{TimeUnixNano: "1000000000", Attributes: []otlpKeyValue{strAttr(attrEventType, eventTypeHeartbeat)}},
			{TimeUnixNano: "not-a-number"},
			{
				TimeUnixNano: "1000000000",
				Attributes: []otlpKeyValue{
					strAttr(attrEventType, eventTypeCheckResult),
					strAttr(attrTargetID, "t1"),
					strAttr(attrOutcome, outcomeSuccess),
					intAttr(attrLatencyMS, 10),
				},
			},
		},
	}

	resp := normalize(req)
	if resp.Accepted != 2 || resp.Rejected != 1 {
		t.Fatalf("expected 2 accepted / 1 rejected, got accepted=%d rejected=%d", resp.Accepted, resp.Rejected)
	}
	if len(resp.Records) != 3 {
		t.Fatalf("expected 3 records in response, got %d", len(resp.Records))
	}
	for i, r := range resp.Records {
		if r.Index != i {
			t.Fatalf("expected record %d to report Index=%d, got %d", i, i, r.Index)
		}
	}
}
