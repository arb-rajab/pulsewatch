package agentapi

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/arb-rajab/pulsewatch/backend/internal/agentauth"
	"github.com/arb-rajab/pulsewatch/backend/internal/alerting"
)

func buildOtlpRequest(agentID string, records ...OtlpLogRecord) []byte {
	req := OtlpExportLogsServiceRequest{
		ResourceLogs: []OtlpResourceLogs{
			{
				Resource:  OtlpResource{Attributes: []OtlpKeyValue{KV(AttrAgentID, StringValue(agentID))}},
				ScopeLogs: []OtlpScopeLogs{{LogRecords: records}},
			},
		},
	}
	b, err := json.Marshal(req)
	if err != nil {
		panic(err) // test-only helper; a marshal failure here is a bug in the test itself
	}
	return b
}

func heartbeatRecord(at time.Time) OtlpLogRecord {
	return OtlpLogRecord{
		TimeUnixNano: strconv.FormatInt(at.UnixNano(), 10),
		Attributes:   []OtlpKeyValue{KV(AttrEventType, StringValue(EventTypeHeartbeat))},
	}
}

func checkResultRecord(at time.Time, targetID string, success bool, latencyMS int64) OtlpLogRecord {
	outcome := OutcomeFailure
	if success {
		outcome = OutcomeSuccess
	}
	return OtlpLogRecord{
		TimeUnixNano: strconv.FormatInt(at.UnixNano(), 10),
		Attributes: []OtlpKeyValue{
			KV(AttrEventType, StringValue(EventTypeCheckResult)),
			KV(AttrTargetID, StringValue(targetID)),
			KV(AttrOutcome, StringValue(outcome)),
			KV(AttrLatencyMS, IntValue(latencyMS)),
		},
	}
}

// checkResultRecordWithStatus is checkResultRecord plus an explicit
// pulsewatch.status_code attribute, for exercising that conversion's
// boundaries independently of latency_ms.
func checkResultRecordWithStatus(at time.Time, targetID string, success bool, latencyMS, statusCode int64) OtlpLogRecord {
	rec := checkResultRecord(at, targetID, success, latencyMS)
	rec.Attributes = append(rec.Attributes, KV(AttrStatusCode, IntValue(statusCode)))
	return rec
}

func TestIngestLogs_RequiresValidBearerToken(t *testing.T) {
	pool := testPool(t)
	r := testRouter(pool, &spyDispatcher{}, nil)

	body := buildOtlpRequest("00000000-0000-0000-0000-000000000000", heartbeatRecord(time.Now()))

	w := doRequest(t, r, http.MethodPost, "/v1/logs", "", body)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no token, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(t, r, http.MethodPost, "/v1/logs", "this-token-was-never-issued", body)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with an unknown token, got %d: %s", w.Code, w.Body.String())
	}
}

func TestIngestLogs_MalformedBodyReturns422(t *testing.T) {
	pool := testPool(t)
	_, token := insertTestAgent(t, pool, "test-logs-malformed", 60)
	r := testRouter(pool, &spyDispatcher{}, nil)

	w := doRequest(t, r, http.MethodPost, "/v1/logs", token, []byte("not json"))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for a malformed body, got %d: %s", w.Code, w.Body.String())
	}
}

// TestIngestLogs_HeartbeatUpdatesLastHeartbeatAt proves the real write side
// of ADR-0003's heartbeat channel — the one liveness signal staleness is
// computed from.
func TestIngestLogs_HeartbeatUpdatesLastHeartbeatAt(t *testing.T) {
	pool := testPool(t)
	agentID, token := insertTestAgent(t, pool, "test-logs-heartbeat", 60)
	r := testRouter(pool, &spyDispatcher{}, nil)

	at := time.Now().UTC().Truncate(time.Second)
	body := buildOtlpRequest(agentID, heartbeatRecord(at))

	w := doRequest(t, r, http.MethodPost, "/v1/logs", token, body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	got := fetchLastHeartbeat(t, pool, agentID)
	if got == nil || !got.Equal(at) {
		t.Fatalf("expected last_heartbeat_at=%v, got %v", at, got)
	}
}

// TestIngestLogs_ThresholdCrossing_RealPipeline is the "genuinely-down
// target reported by a healthy agent transitions through Session 6's alert
// machine normally" half of the staleness-vs-target-down proof: three real
// OTLP check_result submissions over HTTP, through the real
// authenticated endpoint, reach the identical ADR-0002 outcome the
// scheduler's own end-to-end test proves for a server-executed check —
// streak=3, state=alerting, exactly one open incident, exactly one
// "opened" dispatch.
func TestIngestLogs_ThresholdCrossing_RealPipeline(t *testing.T) {
	pool := testPool(t)
	agentID, token := insertTestAgent(t, pool, "test-logs-threshold", 60)
	targetID := insertAssignedTarget(t, pool, agentID, defaultTestTargetOpts())
	channelID := insertTestAlertChannel(t, pool, "https://hooks.example.invalid/otlp-threshold-test")

	spy := &spyDispatcher{}
	r := testRouter(pool, spy, testEncryptionKey)
	base := time.Now().UTC()

	for i := range 3 {
		body := buildOtlpRequest(agentID, checkResultRecord(base.Add(time.Duration(i)*time.Second), targetID, false, 42))
		w := doRequest(t, r, http.MethodPost, "/v1/logs", token, body)
		if w.Code != http.StatusOK {
			t.Fatalf("iteration %d: expected 200, got %d: %s", i, w.Code, w.Body.String())
		}
	}

	streak, state := fetchStreakState(t, pool, targetID)
	if streak != 3 || state != "alerting" {
		t.Fatalf("expected streak=3 state=alerting after crossing threshold via OTLP, got streak=%d state=%s", streak, state)
	}
	if count := countCheckResultsFor(t, pool, targetID); count != 3 {
		t.Fatalf("expected exactly 3 check_results rows, got %d", count)
	}
	if got := len(spy.callsFor(channelID)); got != 1 {
		t.Fatalf("expected exactly 1 dispatch call to this test's own channel, got %d", got)
	}
}

// TestIngestLogs_DuplicateCheckResult_IsANoOp is the full HTTP-level OTLP
// duplicate-idempotency proof (docs/architecture/openapi.yaml): resending
// the identical (target_id, timeUnixNano) pair over the real endpoint a
// second time inserts no second row and never re-enters Evaluate —
// complementing alerting.TestRecordCheckResult_DuplicateCheckedAtIsANoOp's
// proof of the same property one layer down, at the shared write path
// itself.
func TestIngestLogs_DuplicateCheckResult_IsANoOp(t *testing.T) {
	pool := testPool(t)
	agentID, token := insertTestAgent(t, pool, "test-logs-duplicate", 60)
	targetID := insertAssignedTarget(t, pool, agentID, defaultTestTargetOpts())

	spy := &spyDispatcher{}
	r := testRouter(pool, spy, nil)
	at := time.Now().UTC()
	body := buildOtlpRequest(agentID, checkResultRecord(at, targetID, false, 42))

	w := doRequest(t, r, http.MethodPost, "/v1/logs", token, body)
	if w.Code != http.StatusOK {
		t.Fatalf("first submission: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	streak, state := fetchStreakState(t, pool, targetID)
	if streak != 1 || state != "suspect" {
		t.Fatalf("expected streak=1 state=suspect after the first submission, got streak=%d state=%s", streak, state)
	}

	// Resubmit the identical record — a real network retry of the same
	// OTLP payload, not a second, independent check.
	w = doRequest(t, r, http.MethodPost, "/v1/logs", token, body)
	if w.Code != http.StatusOK {
		t.Fatalf("duplicate submission: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if count := countCheckResultsFor(t, pool, targetID); count != 1 {
		t.Fatalf("expected exactly 1 check_results row after a duplicate resubmission, got %d", count)
	}
	streak, state = fetchStreakState(t, pool, targetID)
	if streak != 1 || state != "suspect" {
		t.Fatalf("expected streak to remain 1 (not double-incremented to 2) after the duplicate, got streak=%d state=%s", streak, state)
	}
	if got := len(spy.calls); got != 0 {
		t.Fatalf("expected zero dispatches (streak=1 never crosses a threshold of 3), got %d", got)
	}
}

// TestIngestLogs_CrossAgentTargetRejected proves an agent cannot write a
// check result for a target it isn't assigned to — the write-side
// symmetry of "no {agent_id} in /agent/assignments" (05-api-contracts.md):
// identity comes only from the bearer token, never from a caller-supplied
// target_id claim.
func TestIngestLogs_CrossAgentTargetRejected(t *testing.T) {
	pool := testPool(t)
	agentA, tokenA := insertTestAgent(t, pool, "test-logs-cross-agent-a", 60)
	agentB, _ := insertTestAgent(t, pool, "test-logs-cross-agent-b", 60)
	targetOfB := insertAssignedTarget(t, pool, agentB, defaultTestTargetOpts())

	r := testRouter(pool, &spyDispatcher{}, nil)
	body := buildOtlpRequest(agentA, checkResultRecord(time.Now(), targetOfB, false, 42))

	w := doRequest(t, r, http.MethodPost, "/v1/logs", tokenA, body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with a partial-success rejection, got %d: %s", w.Code, w.Body.String())
	}

	var resp exportLogsServiceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.PartialSuccess == nil || resp.PartialSuccess.RejectedLogRecords != 1 {
		t.Fatalf("expected exactly 1 rejected log record, got %+v", resp.PartialSuccess)
	}
	if count := countCheckResultsFor(t, pool, targetOfB); count != 0 {
		t.Fatalf("expected zero check_results written for a target the caller isn't assigned to, got %d", count)
	}
}

// TestStalenessVsTargetDown_BothHalvesDistinguished is the direct proof of
// ADR-0003 Decision 3's evaluation-ordering rule end to end: a genuinely
// down target reported by a real, fresh agent reaches Alerting through the
// exact same OTLP pipeline as any other check result (half one); the
// identical target_schedule row, read back once the agent has gone stale,
// is surfaced as "unknown" by alerting.DisplayState — never conflating the
// stale agent's last real result (which really was Alerting) with "still
// current" (half two). raw_state itself is untouched by staleness either
// way — only the read-time overlay differs.
func TestStalenessVsTargetDown_BothHalvesDistinguished(t *testing.T) {
	pool := testPool(t)
	const reportIntervalSeconds = 60
	agentID, token := insertTestAgent(t, pool, "test-staleness-both-halves", reportIntervalSeconds)
	targetID := insertAssignedTarget(t, pool, agentID, defaultTestTargetOpts())
	channelID := insertTestAlertChannel(t, pool, "https://hooks.example.invalid/staleness-both-halves-test")

	spy := &spyDispatcher{}
	r := testRouter(pool, spy, testEncryptionKey)
	base := time.Now().UTC()

	// A real heartbeat first — the agent is genuinely alive and reporting.
	heartbeatAt := base
	hbBody := buildOtlpRequest(agentID, heartbeatRecord(heartbeatAt))
	if w := doRequest(t, r, http.MethodPost, "/v1/logs", token, hbBody); w.Code != http.StatusOK {
		t.Fatalf("heartbeat: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Three real failures, from the same fresh agent — the target is
	// genuinely down, not the agent.
	for i := range 3 {
		body := buildOtlpRequest(agentID, checkResultRecord(base.Add(time.Duration(i+1)*time.Second), targetID, false, 42))
		if w := doRequest(t, r, http.MethodPost, "/v1/logs", token, body); w.Code != http.StatusOK {
			t.Fatalf("check_result %d: expected 200, got %d: %s", i, w.Code, w.Body.String())
		}
	}

	_, rawState := fetchStreakState(t, pool, targetID)
	if rawState != string(alerting.StateAlerting) {
		t.Fatalf("expected the target to genuinely reach raw_state=alerting, got %s", rawState)
	}
	if got := len(spy.callsFor(channelID)); got != 1 {
		t.Fatalf("expected exactly 1 dispatch to this test's own channel for the real incident, got %d", got)
	}

	// Half one: while the agent is fresh, display_state must show the real
	// alerting state — never suppressed.
	reportInterval, lastHeartbeatAt, err := agentauth.FetchLiveness(t.Context(), pool, agentID)
	if err != nil {
		t.Fatalf("fetch liveness: %v", err)
	}
	freshNow := heartbeatAt.Add(10 * time.Second) // well within 3x60s
	if stale := agentauth.IsStale(reportInterval, lastHeartbeatAt, freshNow); stale {
		t.Fatalf("expected the agent to be fresh %v after its last heartbeat", freshNow.Sub(heartbeatAt))
	}
	if display := alerting.DisplayState(alerting.State(rawState), true, false); display != "alerting" {
		t.Fatalf("expected display_state=alerting while the agent is fresh, got %s", display)
	}

	// Half two: no further heartbeat arrives. Once more than 3x the report
	// interval has elapsed since the last one, the agent is stale — and
	// display_state must read "unknown", NOT the target's real last-known
	// alerting state, even though raw_state in the database is still
	// exactly what it was (this call never writes anything).
	staleNow := heartbeatAt.Add(time.Duration(3*reportIntervalSeconds+1) * time.Second)
	if stale := agentauth.IsStale(reportInterval, lastHeartbeatAt, staleNow); !stale {
		t.Fatalf("expected the agent to be stale %v after its last heartbeat", staleNow.Sub(heartbeatAt))
	}
	if display := alerting.DisplayState(alerting.State(rawState), true, true); display != "unknown" {
		t.Fatalf("expected display_state=unknown once the agent is stale, got %s (must not be conflated with the real raw_state=%s)", display, rawState)
	}

	// raw_state itself was never touched by any of the staleness
	// computation above — it's still the same real Alerting the incident
	// machine wrote.
	_, rawStateAfter := fetchStreakState(t, pool, targetID)
	if rawStateAfter != string(alerting.StateAlerting) {
		t.Fatalf("expected raw_state to remain alerting regardless of display-time staleness, got %s", rawStateAfter)
	}
}

// TestIngestLogs_LatencyMSBoundaries proves the fix for CodeQL's
// go/incorrect-integer-conversion finding on the latencyMS narrowing
// conversion (int64 -> int) in processCheckResult (logs.go): a value
// outside [0, math.MaxInt32] is rejected as invalid before any conversion
// happens, rather than silently truncated or handed to Postgres's
// `integer` (int4) column, and every value inside that range round-trips
// exactly.
func TestIngestLogs_LatencyMSBoundaries(t *testing.T) {
	pool := testPool(t)
	agentID, token := insertTestAgent(t, pool, "test-logs-latency-boundary", 60)
	r := testRouter(pool, &spyDispatcher{}, nil)

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
			targetID := insertAssignedTarget(t, pool, agentID, defaultTestTargetOpts())
			body := buildOtlpRequest(agentID, checkResultRecord(time.Now(), targetID, true, tc.latencyMS))

			w := doRequest(t, r, http.MethodPost, "/v1/logs", token, body)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}

			var resp exportLogsServiceResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			gotAccepted := resp.PartialSuccess == nil
			if gotAccepted != tc.accepted {
				t.Fatalf("latencyMS=%d: expected accepted=%v, got accepted=%v (partialSuccess=%+v)", tc.latencyMS, tc.accepted, gotAccepted, resp.PartialSuccess)
			}

			wantCount := 0
			if tc.accepted {
				wantCount = 1
			}
			if count := countCheckResultsFor(t, pool, targetID); count != wantCount {
				t.Fatalf("latencyMS=%d: expected %d persisted check_results rows, got %d", tc.latencyMS, wantCount, count)
			}
		})
	}
}

// TestIngestLogs_StatusCodeBoundaries proves the fix for CodeQL's second
// go/incorrect-integer-conversion finding on the statusCode narrowing
// conversion (int64 -> int32) in processCheckResult (logs.go). Unlike the
// identical-looking int32(resp.StatusCode) casts in
// internal/scheduler/check.go and cmd/agent/checker.go — where the source
// is Go's own net/http response and therefore already bounded — this
// value starts as an arbitrary agent-supplied decimal string, so it must
// be range-checked before conversion. An in-range value round-trips to
// Postgres exactly; an out-of-range one is rejected outright rather than
// silently wrapping to an unrelated status code.
func TestIngestLogs_StatusCodeBoundaries(t *testing.T) {
	pool := testPool(t)
	agentID, token := insertTestAgent(t, pool, "test-logs-status-boundary", 60)
	r := testRouter(pool, &spyDispatcher{}, nil)

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
			targetID := insertAssignedTarget(t, pool, agentID, defaultTestTargetOpts())
			body := buildOtlpRequest(agentID, checkResultRecordWithStatus(time.Now(), targetID, true, 42, tc.statusCode))

			w := doRequest(t, r, http.MethodPost, "/v1/logs", token, body)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}

			var resp exportLogsServiceResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			gotAccepted := resp.PartialSuccess == nil
			if gotAccepted != tc.accepted {
				t.Fatalf("statusCode=%d: expected accepted=%v, got accepted=%v (partialSuccess=%+v)", tc.statusCode, tc.accepted, gotAccepted, resp.PartialSuccess)
			}

			if !tc.accepted {
				if count := countCheckResultsFor(t, pool, targetID); count != 0 {
					t.Fatalf("statusCode=%d: expected 0 persisted rows for a rejected record, got %d", tc.statusCode, count)
				}
				return
			}

			got := fetchLatestStatusCode(t, pool, targetID)
			if got == nil || int64(*got) != tc.statusCode {
				t.Fatalf("statusCode=%d: expected it to round-trip exactly, got %v", tc.statusCode, got)
			}
		})
	}
}
