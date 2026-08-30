package alerting

import "testing"

// TestEvaluate covers every branch of ADR-0002's transition function
// exhaustively — including the exact edge (nextStreak == threshold) and the
// two states the ADR names explicitly (crossing into Alerting vs. staying
// in it). "Unknown" has no case here at all — see state.go's package doc:
// Evaluate is simply never called for an Unknown period, so there is
// nothing for this test to exercise for it.
func TestEvaluate(t *testing.T) {
	cases := []struct {
		name           string
		previousStreak int
		previousState  State
		success        bool
		threshold      int
		wantStreak     int
		wantState      State
		wantAction     IncidentAction
	}{
		{
			name:           "success from Healthy stays Healthy, no incident",
			previousStreak: 0, previousState: StateHealthy, success: true, threshold: 3,
			wantStreak: 0, wantState: StateHealthy, wantAction: IncidentActionNone,
		},
		{
			name:           "success from Suspect resets to Healthy, no incident (US-006 full absorption)",
			previousStreak: 2, previousState: StateSuspect, success: true, threshold: 3,
			wantStreak: 0, wantState: StateHealthy, wantAction: IncidentActionNone,
		},
		{
			name:           "success from Alerting resolves: Healthy + close incident",
			previousStreak: 5, previousState: StateAlerting, success: true, threshold: 3,
			wantStreak: 0, wantState: StateHealthy, wantAction: IncidentActionClose,
		},
		{
			name:           "first failure from Healthy is a blip: Suspect, no incident",
			previousStreak: 0, previousState: StateHealthy, success: false, threshold: 3,
			wantStreak: 1, wantState: StateSuspect, wantAction: IncidentActionNone,
		},
		{
			name:           "second failure (still below threshold) stays Suspect, no incident (US-006 blip suppression)",
			previousStreak: 1, previousState: StateSuspect, success: false, threshold: 3,
			wantStreak: 2, wantState: StateSuspect, wantAction: IncidentActionNone,
		},
		{
			name:           "third failure crosses the threshold: Alerting, open incident",
			previousStreak: 2, previousState: StateSuspect, success: false, threshold: 3,
			wantStreak: 3, wantState: StateAlerting, wantAction: IncidentActionOpen,
		},
		{
			name:           "fourth failure while already Alerting: streak grows, no incident write, no dispatch (FR-016)",
			previousStreak: 3, previousState: StateAlerting, success: false, threshold: 3,
			wantStreak: 4, wantState: StateAlerting, wantAction: IncidentActionNone,
		},
		{
			name:           "threshold of 1: the very first failure both crosses and opens",
			previousStreak: 0, previousState: StateHealthy, success: false, threshold: 1,
			wantStreak: 1, wantState: StateAlerting, wantAction: IncidentActionOpen,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.previousStreak, tc.previousState, tc.success, tc.threshold)
			if got.Streak != tc.wantStreak || got.State != tc.wantState || got.Action != tc.wantAction {
				t.Fatalf("Evaluate(%d, %q, %v, %d) = %+v, want streak=%d state=%q action=%d",
					tc.previousStreak, tc.previousState, tc.success, tc.threshold, got, tc.wantStreak, tc.wantState, tc.wantAction)
			}
		})
	}
}
