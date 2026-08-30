package alerting

import "testing"

// TestDisplayState covers ADR-0003 Decision 3's read-time overlay,
// proving the two halves the API contract calls out explicitly:
//  1. a genuinely-down target behind a fresh (or unassigned) agent shows
//     its real raw_state, including Alerting — never suppressed.
//  2. a stale agent's target shows "unknown" regardless of what raw_state
//     actually holds underneath — including when the real answer is
//     Alerting, proving the two are never conflated (a stale agent's
//     last-known result must never be read as still current).
func TestDisplayState(t *testing.T) {
	cases := []struct {
		name          string
		rawState      State
		agentAssigned bool
		agentStale    bool
		want          string
	}{
		{
			name:          "server-executed target always shows raw_state, healthy",
			rawState:      StateHealthy,
			agentAssigned: false,
			agentStale:    false,
			want:          "healthy",
		},
		{
			name:          "server-executed target always shows raw_state, alerting — agentStale is meaningless without an agent",
			rawState:      StateAlerting,
			agentAssigned: false,
			agentStale:    true,
			want:          "alerting",
		},
		{
			name:          "agent-assigned, fresh agent, genuinely down target — real alerting state, not suppressed",
			rawState:      StateAlerting,
			agentAssigned: true,
			agentStale:    false,
			want:          "alerting",
		},
		{
			name:          "agent-assigned, fresh agent, healthy target",
			rawState:      StateHealthy,
			agentAssigned: true,
			agentStale:    false,
			want:          "healthy",
		},
		{
			name:          "agent-assigned, stale agent, target was healthy — surfaced as unknown, not conflated",
			rawState:      StateHealthy,
			agentAssigned: true,
			agentStale:    true,
			want:          "unknown",
		},
		{
			name:          "agent-assigned, stale agent, target's last-known state was alerting — still surfaced as unknown, never read as still current",
			rawState:      StateAlerting,
			agentAssigned: true,
			agentStale:    true,
			want:          "unknown",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DisplayState(tc.rawState, tc.agentAssigned, tc.agentStale)
			if got != tc.want {
				t.Fatalf("DisplayState(%q, agentAssigned=%v, agentStale=%v) = %q, want %q",
					tc.rawState, tc.agentAssigned, tc.agentStale, got, tc.want)
			}
		})
	}
}
