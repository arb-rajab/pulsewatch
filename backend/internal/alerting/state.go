// Package alerting implements ADR-0002's 3-state alert-suppression state
// machine (Healthy/Suspect/Alerting) and the guarded, exactly-once
// incident-open/close writes and notification dispatch that ride on top of
// it.
//
// "Unknown" (agent stale, or the server's own scheduler wasn't running,
// ADR-0003/FR-011/FR-019) is deliberately not a fourth state anywhere in
// this package: there is no UnknownState constant, no branch for it in
// Evaluate, and no code path that calls Evaluate for a period with no
// completed check outcome. Per ADR-0002: "the transition function above is
// not invoked at all for this period... it's the function simply not
// running." A caller (the scheduler) simply never calls Evaluate when there
// is no real check result to evaluate — streak/state then pass through
// unchanged by construction, not by a branch that has to remember to leave
// them alone.
package alerting

// State mirrors the three values target_schedule.state may hold
// (04-data-model.md: CHECK (state IN ('healthy', 'suspect', 'alerting'))).
// "unknown" is never a value this type or column holds — see the package
// doc.
type State string

// The three states ADR-0002 names — "unknown" is deliberately absent, see
// the package doc above.
const (
	StateHealthy  State = "healthy"
	StateSuspect  State = "suspect"
	StateAlerting State = "alerting"
)

// IncidentAction is what a Transition requires of the incidents table.
// ADR-0002's edge-transition-only rule (FR-016) is structural here: only
// IncidentActionOpen and IncidentActionClose ever touch incidents — staying
// in Suspect or staying in Alerting is always IncidentActionNone.
type IncidentAction int

// The three actions a Transition can require of the incidents table.
const (
	IncidentActionNone IncidentAction = iota
	IncidentActionOpen
	IncidentActionClose
)

// Transition is the pure result of evaluating one completed check outcome
// against the previously persisted streak/state. It has no side effects of
// its own — OpenIncident/CloseIncident (incident.go) perform the guarded
// writes this describes, and only after a caller decides to.
type Transition struct {
	Streak int
	State  State
	Action IncidentAction
}

// Evaluate is ADR-0002's transition function, verbatim in its branches:
// given the previously persisted streak/state, one completed check's
// success/failure, and the target's configured consecutive-failure
// threshold (targets.failure_threshold, NFR-011), compute the next
// streak/state and which incidents-table action (if any) applies.
//
// Pure: no I/O, no error return. There is no invalid input a real caller can
// produce — previousState is schema-constrained to the three State values
// above, and threshold is a positive column value (targets.failure_threshold
// NOT NULL DEFAULT 3) — so this function has nothing to fail on.
func Evaluate(previousStreak int, previousState State, success bool, threshold int) Transition {
	if success {
		if previousState == StateAlerting {
			return Transition{Streak: 0, State: StateHealthy, Action: IncidentActionClose}
		}
		return Transition{Streak: 0, State: StateHealthy, Action: IncidentActionNone}
	}

	nextStreak := previousStreak + 1
	switch {
	case nextStreak < threshold:
		// US-006's blip zone: absorbed silently, no incidents write at all.
		return Transition{Streak: nextStreak, State: StateSuspect, Action: IncidentActionNone}
	case nextStreak == threshold:
		// Crossing the threshold this check — the one edge that opens an incident.
		return Transition{Streak: nextStreak, State: StateAlerting, Action: IncidentActionOpen}
	default: // nextStreak > threshold: already Alerting, stays Alerting.
		return Transition{Streak: nextStreak, State: StateAlerting, Action: IncidentActionNone}
	}
}
