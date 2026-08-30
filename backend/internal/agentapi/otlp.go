// Package agentapi implements ADR-0003's agent-facing HTTP surface exactly
// as docs/architecture/openapi.yaml specifies it: the authenticated
// assignment-discovery poll (GET /api/v1/agent/assignments) and the OTLP
// log-ingestion endpoint (POST /v1/logs) that carries both check-result and
// heartbeat reports. Every write this package performs for a check result
// goes through the identical shared path internal/scheduler's own
// server-executed checks use (internal/alerting.RecordCheckResult) — there
// is no second, agent-specific evaluation logic anywhere in this package.
package agentapi

import "strconv"

// The two record shapes docs/architecture/openapi.yaml's /v1/logs
// operation description names, distinguished by the pulsewatch.event_type
// attribute.
const (
	EventTypeCheckResult = "check_result"
	EventTypeHeartbeat   = "heartbeat"
)

// Attribute keys this endpoint's simplified OTLP/HTTP+JSON shape uses —
// named once here so the ingestion handler (logs.go) and the reference
// agent binary (backend/cmd/agent) share the exact same wire vocabulary,
// never two independently-typed copies of it.
const (
	AttrAgentID       = "agent.id" // resource-level attribute
	AttrEventType     = "pulsewatch.event_type"
	AttrTargetID      = "pulsewatch.target_id"
	AttrOutcome       = "pulsewatch.outcome" // "success" | "failure"
	AttrLatencyMS     = "pulsewatch.latency_ms"
	AttrStatusCode    = "pulsewatch.status_code"
	AttrFailureReason = "pulsewatch.failure_reason"
)

// OutcomeSuccess / OutcomeFailure are the two values AttrOutcome carries.
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
)

// OtlpValue is the simplified OTLP AnyValue this endpoint's schema
// supports — a string or an int (OTLP encodes int64 as a decimal string in
// its JSON mapping, never a bare JSON number, to avoid precision loss).
type OtlpValue struct {
	StringValue *string `json:"stringValue,omitempty"`
	IntValue    *string `json:"intValue,omitempty"`
}

// StringValue / IntValue construct an OtlpValue — used by both this
// package's own tests and the reference agent binary, so neither hand-rolls
// the {"stringValue": "..."} / {"intValue": "..."} shape itself.
func StringValue(s string) OtlpValue { return OtlpValue{StringValue: &s} }

// IntValue constructs an OtlpValue carrying an int64, encoded as OTLP's own
// decimal-string JSON mapping — see StringValue.
func IntValue(n int64) OtlpValue {
	s := strconv.FormatInt(n, 10)
	return OtlpValue{IntValue: &s}
}

func (v OtlpValue) asString() (string, bool) {
	if v.StringValue == nil {
		return "", false
	}
	return *v.StringValue, true
}

func (v OtlpValue) asInt() (int64, bool) {
	if v.IntValue == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(*v.IntValue, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// OtlpKeyValue is one OTLP attribute.
type OtlpKeyValue struct {
	Key   string    `json:"key"`
	Value OtlpValue `json:"value"`
}

// KV constructs one attribute — a small convenience so the reference agent
// binary doesn't hand-build OtlpKeyValue{Key: ..., Value: ...} literals.
func KV(key string, value OtlpValue) OtlpKeyValue { return OtlpKeyValue{Key: key, Value: value} }

func attrMap(attrs []OtlpKeyValue) map[string]OtlpValue {
	m := make(map[string]OtlpValue, len(attrs))
	for _, kv := range attrs {
		m[kv.Key] = kv.Value
	}
	return m
}

// OtlpResource carries the resource-level agent.id attribute
// (docs/architecture/openapi.yaml: "Must include agent.id (uuid).").
type OtlpResource struct {
	Attributes []OtlpKeyValue `json:"attributes"`
}

// OtlpLogRecord is one check_result or heartbeat record. TimeUnixNano *is*
// checked_at for a check_result — never duplicated into a separate custom
// attribute (docs/architecture/openapi.yaml) — encoded as a decimal string
// per the real OTLP JSON mapping for a fixed64 field.
type OtlpLogRecord struct {
	TimeUnixNano string         `json:"timeUnixNano"`
	Attributes   []OtlpKeyValue `json:"attributes"`
}

// OtlpScopeLogs groups log records under one instrumentation scope — this
// endpoint's simplified shape uses exactly one scope per resourceLogs
// entry.
type OtlpScopeLogs struct {
	LogRecords []OtlpLogRecord `json:"logRecords"`
}

// OtlpResourceLogs is one resource's worth of log records — the top-level
// grouping OtlpExportLogsServiceRequest carries.
type OtlpResourceLogs struct {
	Resource  OtlpResource    `json:"resource"`
	ScopeLogs []OtlpScopeLogs `json:"scopeLogs"`
}

// OtlpExportLogsServiceRequest mirrors
// docs/architecture/openapi.yaml's OtlpExportLogsServiceRequest schema
// exactly — the one Go definition of this wire shape in the repo, decoded
// by IngestLogs (logs.go) and encoded by backend/cmd/agent.
type OtlpExportLogsServiceRequest struct {
	ResourceLogs []OtlpResourceLogs `json:"resourceLogs"`
}
