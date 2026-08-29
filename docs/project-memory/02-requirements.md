# Requirements
> Purpose: testable statements of what the system must do and how well
> Project: pulsewatch (public)
> Last updated: 2026-08-29
> Depth: **baseline** — per `docs/SDLC-EVIDENCE.md` / `00a-ledger-confirmation.md`,
> this repo's two deep phases are Release & Deployment and Operations &
> Maintenance, not this one. Requirements Analysis is already demonstrated
> deeply in `privacy-forge`; this document is solid and testable but does not
> duplicate that ceremony (no ABAC policy matrix, no article-traceability
> table — there is no comparable external framework this repo answers to).

This document formalizes `00-project-brief.md` and `01-scope-and-non-goals.md`
into testable requirements. It does not reopen any Session 1 decision (plain
Postgres over TimescaleDB, the MVP boundary, the non-goals table) — where a
requirement below needs a numeric default that Session 1 deliberately left
open (check interval, failure threshold, retention window), this session
proposes one with stated reasoning, flagged as adjustable in Architecture
(Session 3) rather than re-litigated here.

Shorthand used in "Verified by" columns: `US-0xx` = user story below;
`SM#` = success metric # from `00-project-brief.md`.

## Roles and permissions matrix

v1 is single-operator by design (`01-scope-and-non-goals.md` business
assumption; multi-user auth/role separation is explicitly deferred to
backlog, not needed to prove this v1's success metrics). The only other
identity in the system is the agent, which is a machine credential, not a
second human role.

| Role | Can | Cannot |
|---|---|---|
| **Operator** (human, single account in v1) | Register/edit/delete check targets and their interval and failure threshold; view the dashboard (status, uptime%, incident history); configure alert channels (webhook/email) and their credentials; view agent registration status | N/A within the product — v1 has no lesser-privileged human role. No public/anonymous viewer role exists (public status pages are an explicit non-goal at v1); no second operator account or role separation exists (deferred to backlog) |
| **Agent** (machine, one service credential per agent instance) | Authenticate to the server with its own credential; report check results and heartbeats only for the targets it has been assigned | Register, edit, or delete targets; read another agent's assignments or results; read the operator's alert-channel credentials; access the dashboard or any operator-facing endpoint |

## User stories with acceptance criteria

### Registering checks

**US-001 — Register an HTTP(S) check**
As the operator, I want to register an HTTP(S) check against a target URL
with a configured interval and failure threshold, so pulsewatch begins
tracking that target's availability.
**Acceptance criteria:**
- Given a valid URL, an interval ≥ the configured minimum (10s), and a
  consecutive-failure threshold ≥ 1, when I submit the config, then a target
  is created in "awaiting first check" state and is picked up by the
  scheduler within one scheduler tick.
- Given an interval below the minimum, an interval above the maximum (24h),
  or a missing required field, when I submit the config, then the API
  rejects it with a field-level validation error and no target is created.
- Given an optional response-body-match pattern is configured, when a check
  executes, then both the status code and the body-match result are
  recorded as part of that check's outcome.

**US-002 — Register a TCP check**
As the operator, I want to register a raw TCP reachability check against a
host:port, so I can monitor targets that don't speak HTTP (e.g. a database
container).
**Acceptance criteria:**
- Given a host and port, when I submit the config, then a target of type TCP
  is created and scheduled the same way as an HTTP target.
- Given the TCP connection attempt times out or is refused, when the check
  executes, then the failure is recorded with the specific reason (timeout
  vs. connection refused) captured, not a generic "failed."

### Running checks and recording results

**US-003 — A check runs on schedule and records a result**
As the operator, I want a due check to execute automatically and its result
persisted, so history and SLO math have real data to work from.
**Acceptance criteria:**
- Given a target's next-due time (computed from persisted last-checked-at +
  interval) has passed, when the scheduler tick runs, then exactly one check
  executes for that target and its result (outcome, latency, timestamp,
  status code or TCP outcome) is persisted before that target is considered
  again.
- Given a target's previous check is still in flight when its next due time
  arrives, when the scheduler tick runs, then no second check is started for
  that target (per-target run exclusivity — SM5).
- Given a check's own configured timeout elapses with no response, when this
  happens, then the result is recorded as a failure with reason "timeout,"
  never left pending indefinitely.

### SLO / error-budget evaluation

**US-004 — Rolling-window SLO evaluation**
As the operator, I want to see a target's uptime% over a rolling window
(e.g. 30 days), so I can tell whether it's meeting its SLO.
**Acceptance criteria:**
- Given at least one rollup period has completed for a target, when I
  request its rolling-window uptime%, then the value is computed from
  rollup aggregates, not a full raw-row scan, over exactly the requested
  window.
- Given pulsewatch itself was down for some interval within the window (no
  checks could run), when the uptime% is computed, then that interval is
  excluded from both the numerator and the denominator — counted as
  "unknown," not silently folded into either "up" or "down."
- Given the requested window's boundary falls mid-rollup-period, when the
  metric is computed, then the result reflects the actual elapsed *observed*
  time, not an off-by-one-rollup-period error.

### Alerting

**US-005 — Alert fires on a real threshold breach**
As the operator, I want an alert fired the moment a target's
consecutive-failure count reaches its configured threshold, so I learn about
a real outage promptly.
**Acceptance criteria:**
- Given a target's threshold is N, when the Nth consecutive failing check
  completes, then an incident is opened and a webhook/email alert is
  dispatched within the alert-latency budget (NFR-002).
- Given an incident is already open for a target, when subsequent checks
  continue to fail, then no duplicate "opened" alert is sent for the same
  incident.
- Given a target recovers (a check succeeds while an incident is open), when
  this happens, then the incident is closed exactly once and exactly one
  resolution notification is sent, referencing the same incident.

**US-006 — No alert on a transient blip**
As the operator, I want a single failed check below the configured threshold
to NOT fire an alert, so a dropped packet doesn't spam me.
**Acceptance criteria:**
- Given a target's threshold is N (N > 1) and fewer than N consecutive
  failures have occurred, when a check fails, then no incident is opened and
  no alert is dispatched.
- Given a failure streak shorter than N is broken by a subsequent success,
  when this happens, then the consecutive-failure counter resets to zero —
  no partial credit carries into the next streak.

### Restart safety

**US-007 — No lost in-flight scheduling state across a restart**
As the operator, I want pulsewatch to resume correct scheduling immediately
after a restart, so a deploy or crash doesn't blind the scheduler or spam
every target with a simultaneous re-check.
**Acceptance criteria:**
- Given the server process is killed and restarted, when it comes back up,
  then it reconstructs every target's next-due time from persisted
  last-checked-at + interval — not from a blank slate.
- Given a target was already overdue at the moment of restart, when the
  scheduler resumes, then that target's check runs at the next tick, not
  after waiting a full fresh interval from restart time.
- Given many targets were simultaneously overdue at restart, when the
  scheduler resumes, then it does not fire all of them as one unbounded
  synchronized burst — fan-out per tick stays within the worker pool's
  configured concurrency limit.

**US-008 — No double-alerting across a restart**
As the operator, I want an incident that was already open before a restart
to still be recognized as open afterward, so recovery doesn't silently drop
the incident or re-fire a duplicate alert.
**Acceptance criteria:**
- Given an incident is open (persisted) at the moment of restart, when the
  server restarts, then the incident loads from persisted state as
  still-open, and subsequent check results are evaluated against that
  existing incident, not a fresh one.
- Given the incident was already open before restart, when the first check
  after restart also fails, then no new duplicate "opened" alert is sent.
- Given the incident was already open before restart and the next check
  succeeds, when this happens, then the incident is closed exactly once and
  exactly one resolution notification is sent.

### Dashboard

**US-009 — View target status and history**
As the operator, I want a dashboard listing every target's current status,
uptime% over a selectable window, and incident history, so I can check on
everything without querying the database directly.
**Acceptance criteria:**
- Given at least one target is registered, when I open the dashboard, then I
  see its current state (healthy/failing/unknown), its uptime% for the
  selected window, and a list of past incidents with start/end timestamps.
- Given a target has an open incident, when I view it, then the dashboard
  visibly distinguishes "open" from "resolved" incidents.

### Private-target monitoring via the agent

**US-010 — Monitor a target unreachable from the server**
As the operator, I want to monitor a target that isn't reachable from the
pulsewatch server itself (e.g. a service on an internal Docker network only
the agent's host can reach), so I can monitor infrastructure a hosted SaaS
or the server process alone structurally cannot reach.
**Acceptance criteria:**
- Given a target is assigned to a specific agent, when that agent runs the
  check locally and reports the result, then the result is recorded against
  that target exactly as if the server had checked it directly.
- Given the agent hasn't reported within its expected heartbeat window, when
  that staleness threshold (NFR-008) is exceeded, then the target's state
  becomes "unknown" (not "down"), and that unknown period is excluded from
  the target's SLO/error-budget math — the same carve-out as US-004's
  pulsewatch-downtime exclusion.
- Given the agent's network requires no inbound access (agent-initiated
  reporting only), when the agent is deployed on a network the server
  cannot reach, then monitoring works without opening any inbound port on
  that network (FR-018).

## Functional requirements

| ID | Requirement | Priority | Verified by |
|---|---|---|---|
| FR-001 | Register an HTTP(S) check: URL, status-code check, optional response-body match, latency recorded per check | P0 | US-001 |
| FR-002 | Register a TCP check: host:port reachability, failure reason captured (timeout vs. refused) | P0 | US-002 |
| FR-003 | Per-target configurable check interval, bounded [10s, 24h] | P0 | US-001, US-002 |
| FR-004 | Per-target configurable consecutive-failure alert threshold (hysteresis) | P0 | US-005, US-006 |
| FR-005 | Scheduler enforces per-target run exclusivity — never two overlapping checks against the same target | P0 | US-003, SM5 |
| FR-006 | Scheduling state (last-checked-at, next-due) persisted in PostgreSQL and survives a process restart | P0 | US-007, SM3 |
| FR-007 | Check results persisted with timestamp, latency, outcome, and failure reason | P0 | US-003 |
| FR-008 | Scheduled rollup job computes hourly/daily aggregates (uptime%, p50/p95 latency) | P0 | US-004, SM4 |
| FR-009 | Retention job deletes raw check rows past the configured retention window | P0 | SM4 |
| FR-010 | Rolling-window SLO/uptime% is computed from rollup aggregates, not raw-row scans | P0 | US-004 |
| FR-011 | Periods where pulsewatch itself was down are excluded ("unknown") from a target's SLO/error-budget math | P0 | US-004 |
| FR-012 | Incident state (open/closed, opened-at, closed-at) is persisted and survives a restart | P0 | US-008, SM3 |
| FR-013 | Alert dispatch via generic webhook (HTTP POST) on incident open | P0 | US-005 |
| FR-014 | Alert dispatch via email on incident open | P0 | US-005 |
| FR-015 | Exactly one resolution notification is sent when an incident closes | P0 | US-005, US-008 |
| FR-016 | No duplicate "opened" alert is sent while an incident remains open, across any number of subsequent failing checks or a restart | P0 | US-005, US-008 |
| FR-017 | Lightweight Go agent executes HTTP/TCP checks locally against its assigned targets and reports results to the server | P0 | US-010 |
| FR-018 | Agent-to-server communication is agent-initiated (outbound only); no inbound port is required on the agent's network | P0 | US-010 |
| FR-019 | Server marks a target "unknown" (not "down") when its assigned agent misses its staleness threshold, and excludes that period from SLO math | P0 | US-010 |
| FR-020 | Dashboard shows a target list with current status (healthy/failing/unknown) | P0 | US-009 |
| FR-021 | Dashboard shows uptime% over a selectable window per target | P0 | US-009 |
| FR-022 | Dashboard shows incident history per target, distinguishing open vs. resolved | P0 | US-009 |
| FR-023 | Alert-channel credentials (webhook URL, email/SMS provider credentials) are stored encrypted at rest and are never returned in plaintext by any API after initial creation (write-only field) | P0 | Data classification, below |

## Non-functional requirements (numeric targets only)

Defaults below are this session's proposed values, reasoned from the
measured-scale assumptions in `00-project-brief.md` (~5–10 targets,
30–60s check interval, ~10M raw rows/year worst case). They are
operator-configurable per target where noted, and open to revision in
Architecture (Session 3) against real implementation constraints — they are
not re-litigating Session 1, they're the numeric commitment Session 1
deliberately deferred to this session.

| ID | Category | Requirement | Target | Verified by |
|---|---|---|---|---|
| NFR-001 | Scheduling accuracy | A due check starts within a bounded jitter of its computed due time under normal load (≤10 targets) | ±2s | US-003 |
| NFR-002 | Alert latency | Time from the Nth-consecutive-failure being recorded (threshold breach) to the alert dispatch attempt (webhook POST issued / email handed to send) | ≤5s | US-005, SM1 |
| NFR-003 | Restart recovery time | Wall-clock time from process start to the scheduler having reconstructed correct due/overdue state for all persisted targets (≤100 targets) | ≤10s | US-007, SM3 |
| NFR-004 | Restart correctness | Duplicate "opened" alerts fired across a restart for an incident already open, across all restart test runs | 0 | US-008, SM3 |
| NFR-005 | Rollup job timing | Rollup job runs on a fixed hourly cadence; each run completes at measured scale (~10 targets, 30–60s interval, ≤30k raw rows/day) | ≤5 min/run | SM4 |
| NFR-006 | Retention correctness | Raw rows past the configured retention window (default 30 days, operator-configurable) are removed within one rollup-job cycle of crossing the boundary | ≤1h after eligible | SM4 |
| NFR-007 | Concurrency safety | Overlapping checks against the same target under a slow-target test (check duration > interval) | 0 | US-003, SM5 |
| NFR-008 | Agent staleness | A target is marked "unknown" if its agent's report/heartbeat is late by more than a multiple of that agent's expected report interval | > 3× report interval | US-010 |
| NFR-009 | Dashboard read performance | Rolling-window uptime% query latency at measured scale (reads rollups, not raw history) | ≤500ms p95 | US-009 |
| NFR-010 | Default check interval | Proposed default when the operator doesn't override it, consistent with the brief's measured-scale assumption | 30s | US-001, US-002 |
| NFR-011 | Default failure threshold | Proposed default consecutive-failure count before alerting — absorbs a single dropped check (US-006) while bounding real-outage detection to (threshold × interval) + NFR-002 (SM1) | 3 consecutive failures | US-005, US-006, SM1 |
| NFR-012 | Default rollup retention | Proposed default retention for hourly/daily rollup aggregates (kept well past raw retention so a year-over-year comparison survives a skipped month) | 400 days | US-004 |

## Data classification

pulsewatch does not process personal data about third parties (its users
are the operator and, indirectly, this developer's own infrastructure) —
the "Lawful basis" column is retained for template consistency with the
portfolio's other flagships but is not applicable to any row below; each
row states `N/A` explicitly rather than leaving it blank.

| Data element | Classification | Retention | Encryption | Lawful basis |
|---|---|---|---|---|
| Target host/URL/port and check config | Confidential — reveals this developer's private infrastructure topology (internal hostnames, ports, which services exist) | Lifetime of the check config; deleted when the target is deleted | TLS in transit (dashboard/API); at rest via standard DB/volume-level protection — no additional app-level encryption required at v1 scale | N/A — not personal data |
| Alert-channel credentials (webhook URL incl. any embedded bearer token, email/SMTP or SMS provider credentials) | Secret | Until rotated or replaced by the operator | MUST be encrypted at rest (app-level) and never returned in plaintext after initial creation — FR-023 | N/A |
| Check results (outcome, latency, status code/TCP reason, timestamp) | Internal, low sensitivity — confirmed: these are operational signals about pulsewatch's own checks, not personal data or credentials | Raw: 30 days default. Rollups: 400 days default. Both operator-configurable | Standard transport/at-rest protection; no special handling required. Caveat: if a body-match check's captured fragment could incidentally include something sensitive from the monitored target's response — cap the stored fragment length and never log full response bodies (a concrete constraint for Session 3/Architecture to size, not a new v1 feature) | N/A |
| Agent authentication credential (per-agent token/API key) | Secret | Until rotated or the agent is decommissioned | At rest: hashed or encrypted, never logged in plaintext | N/A |
| Operator account credential (login) | Secret | Until changed by the operator | Password hashed (not reversibly encrypted); sessions expire | N/A |

## Integration requirements

Session 1's build-vs-alternatives analysis named "reachability of private
targets" as part of the justification for building pulsewatch instead of
adopting a hosted SaaS. This section makes that concrete rather than
leaving it as abstract justification — see FR-017/018/019 and US-010 above
for the testable requirements; this section states the *why* each one
exists as an integration boundary, not a new mechanism.

- **The server process may have no network path to a private target.** A
  hosted SaaS's prober and, in some deployments, even pulsewatch's own
  server process cannot reach a target on an internal Docker network or a
  homelab-only segment. The agent exists specifically to run the check
  *from* a location that does have that reachability (FR-017) — this is the
  entire reason the agent is in MVP scope, not an optional extra.
- **Reachability must not require opening a new inbound path.** If
  monitoring a private target required opening an inbound port to the
  agent's network, it would reintroduce the exact exposure this developer is
  trying to avoid by not using a public SaaS prober. Agent→server
  communication is therefore agent-initiated/outbound only (FR-018) — this
  is what makes the private-target case operationally real, not just
  "an agent binary exists somewhere."
- **An agent outage must not corrupt SLO math for its assigned targets.**
  Mirroring the pulsewatch-restart "unknown" carve-out (US-004, FR-011), an
  agent that stops reporting doesn't mean its targets are down — it means
  they're unobserved. FR-019/NFR-008 require the server to tell these apart.
- **How an agent learns its assigned target list is deliberately left open
  here.** Requirements states the property the mechanism must satisfy
  (agent-initiated, outbound-only reporting; correct staleness detection);
  it does not fix whether the agent polls the server for its assignment
  list or reads a static config file — that is an Architecture (Session 3)
  decision, not a Requirements one.
- **Transport for agent→server telemetry is the OpenTelemetry collector
  pipeline**, per the learning objective already frozen in
  `00a-ledger-confirmation.md` — Requirements does not re-derive this
  choice, only requires that whatever the OTel pipeline carries satisfies
  FR-017/018/019 above.

## Constraints

- Exactly two new technologies for this repo's learning budget (Go
  concurrency patterns, OpenTelemetry collector pipelines) — frozen in
  `00a-ledger-confirmation.md`; no requirement above may imply a third
  (e.g. no requirement here needs TimescaleDB, Prometheus/Grafana, or a
  message queue to be satisfied).
- Stack is frozen: Go 1.25 (Gin) backend, SvelteKit frontend, PostgreSQL,
  Redis, Docker Compose, GitHub Actions.
- v1 is single-operator; no multi-user auth, role separation, or public
  status page exists at v1 (both deferred/excluded per
  `01-scope-and-non-goals.md`) — the roles matrix above is not a
  placeholder for a future ABAC system, it is the actual v1 shape.
- No requirement above expands the MVP boundary in `01-scope-and-non-goals.md`
  — every FR/NFR maps to an existing checklist item or an already-named
  continuous-operation property; none introduces a new check type (ICMP,
  DNS, cert-expiry — all backlog), multi-region probing, or a paging
  product.
- Numeric NFR defaults (NFR-010/011/012, and the bounds in FR-003) are this
  session's proposed values, not yet exercised against a real
  implementation — Architecture (Session 3) may adjust them with reasoning,
  but should treat "no numeric target existed before Session 2" as resolved
  by this document, not reopen the question from scratch.
