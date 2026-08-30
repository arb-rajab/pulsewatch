package agentauth

import (
	"testing"
	"time"
)

// TestIsStale covers NFR-008/ADR-0003's staleness rule as a pure function,
// table-driven, including the exact boundary (> 3x, not >=) and the
// no-heartbeat-yet case.
func TestIsStale(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	reportInterval := 60 // seconds -> 180s (3x) staleness threshold

	cases := []struct {
		name            string
		lastHeartbeatAt *time.Time
		want            bool
	}{
		{
			name:            "never heartbeated is stale",
			lastHeartbeatAt: nil,
			want:            true,
		},
		{
			name:            "well within threshold is fresh",
			lastHeartbeatAt: ptr(now.Add(-30 * time.Second)),
			want:            false,
		},
		{
			name:            "exactly at 3x threshold is NOT stale (strictly greater-than rule)",
			lastHeartbeatAt: ptr(now.Add(-180 * time.Second)),
			want:            false,
		},
		{
			name:            "one second past 3x threshold is stale",
			lastHeartbeatAt: ptr(now.Add(-181 * time.Second)),
			want:            true,
		},
		{
			name:            "far in the past is stale",
			lastHeartbeatAt: ptr(now.Add(-24 * time.Hour)),
			want:            true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsStale(reportInterval, tc.lastHeartbeatAt, now)
			if got != tc.want {
				t.Fatalf("IsStale() = %v, want %v", got, tc.want)
			}
		})
	}
}

func ptr(t time.Time) *time.Time { return &t }

// TestRecordHeartbeat_RealMonotonicUpdate proves the real GREATEST-based
// write against Postgres: a first heartbeat sets last_heartbeat_at from
// NULL, a later one advances it, and an out-of-order (older) one is a
// no-op — never regresses, exactly as docs/architecture/openapi.yaml's
// /v1/logs description requires.
func TestRecordHeartbeat_RealMonotonicUpdate(t *testing.T) {
	pool := testPool(t)
	agentID, _ := insertTestAgent(t, pool, "test-agent-heartbeat", 60)

	_, initial, err := FetchLiveness(t.Context(), pool, agentID)
	if err != nil {
		t.Fatalf("fetch initial liveness: %v", err)
	}
	if initial != nil {
		t.Fatalf("expected a freshly created agent to have no heartbeat yet, got %v", initial)
	}

	first := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	if err := RecordHeartbeat(t.Context(), pool, agentID, first); err != nil {
		t.Fatalf("record first heartbeat: %v", err)
	}
	_, got, err := FetchLiveness(t.Context(), pool, agentID)
	if err != nil {
		t.Fatalf("fetch liveness after first heartbeat: %v", err)
	}
	if got == nil || !got.Equal(first) {
		t.Fatalf("expected last_heartbeat_at=%v, got %v", first, got)
	}

	later := first.Add(5 * time.Minute)
	if err := RecordHeartbeat(t.Context(), pool, agentID, later); err != nil {
		t.Fatalf("record later heartbeat: %v", err)
	}
	_, got, err = FetchLiveness(t.Context(), pool, agentID)
	if err != nil {
		t.Fatalf("fetch liveness after later heartbeat: %v", err)
	}
	if got == nil || !got.Equal(later) {
		t.Fatalf("expected last_heartbeat_at to advance to %v, got %v", later, got)
	}

	// An out-of-order, older heartbeat must never regress last_heartbeat_at.
	stale := first
	if err := RecordHeartbeat(t.Context(), pool, agentID, stale); err != nil {
		t.Fatalf("record out-of-order heartbeat: %v", err)
	}
	_, got, err = FetchLiveness(t.Context(), pool, agentID)
	if err != nil {
		t.Fatalf("fetch liveness after out-of-order heartbeat: %v", err)
	}
	if got == nil || !got.Equal(later) {
		t.Fatalf("expected last_heartbeat_at to remain %v after an older out-of-order report, got %v", later, got)
	}
}
