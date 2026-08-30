package alerting

// DisplayState is ADR-0002 Consequences' / ADR-0003 Decision 3's read-time
// overlay: target_schedule.state (State, this package) is never "unknown"
// itself — an agent-assigned target's state reads as "unknown" only while
// its agent is currently stale, computed fresh at read time from
// agents.last_heartbeat_at (see agentauth.IsStale), never a maintained flag
// that could itself drift out of sync with the fact it represents.
//
// ADR-0003 Decision 3's explicit evaluation-ordering rule, made concrete
// here: staleness is checked first (agentAssigned && agentStale), and only
// then does rawState pass through — a fresh agent's target shows exactly
// whatever Evaluate decided, including correctly reaching Alerting if the
// target itself is really down. This is what keeps "agent unreachable"
// (display "unknown", raw_state untouched underneath) structurally distinct
// from "target down" (a real Failure outcome that reached Alerting) — the
// dashboard/alerting code must never read a stale agent's last-known result
// as still current, and this function is the one place that decision is
// made, not duplicated per caller.
func DisplayState(rawState State, agentAssigned bool, agentStale bool) string {
	if agentAssigned && agentStale {
		return "unknown"
	}
	return string(rawState)
}
