# ADR-0002 — Alert-Suppression State Machine

- **Date:** 2026-08-29
- **Status:** accepted

## Context

`02-requirements.md` commits to a 3-failure hysteresis threshold (NFR-011)
and states its consequences as testable acceptance criteria: a blip below
threshold must not fire an alert and must not carry partial credit into the
next streak (US-006); a real breach must fire exactly one "opened" alert
and, on recovery, exactly one resolution notification (US-005, FR-015,
FR-016); both properties must survive a restart with zero duplicates
(US-008, NFR-004). None of this is safe as an implicit "if streak >=
threshold" check scattered through the check-processing code path — that
shape's correctness depends entirely on the caller invoking it exactly
once per completed check, forever, including across bugs, retries, and any
future code path this evaluation might be reachable from. This ADR makes
the state model and the exactly-once guarantee explicit and enforced by the
data itself, not by caller discipline.

## Options considered

### A — Ad hoc inline counter check (no named states, no write-side guard)

"If streak reaches threshold and no incident is currently known to be open,
open one and send an alert." The default shape this would take if nobody
stopped to design it.

**Rejected.** Correctness rests entirely on the check-evaluation code path
never running twice for the same completed check (a partial failure after
dispatch but before the "incident opened" write, a retry, a future code
path reusing this logic) without an idempotency check at the persistence
layer itself. This is exactly the class of implicit design this session was
asked not to accept.

### B — Explicit 3-state machine with conditional, guarded persistence writes (chosen)

Named states (`Healthy`, `Suspect`, `Alerting`), a pure transition function
computed from persisted state, and incident open/close performed only via
an atomically-conditional write whose own success or failure — not the
caller's belief about whether it already ran — decides whether a
notification is dispatched.

### C — Full append-only transition ledger (`target_state_transitions`)

Record every single state change as its own row, mirroring
`privacy-forge`'s tamper-evident audit-log rigor.

**Rejected as over-scoped for this repo.** `privacy-forge`'s audit log
exists to satisfy a compliance/legal defensibility requirement this repo's
own data classification explicitly has none of (`02-requirements.md`:
"Lawful basis: N/A" on every row, single operator, no external audience).
The persisted `incidents` table (open/closed, opened-at/closed-at) already
**is** the durable record of every alerting-state transition that matters —
an incident's own lifecycle is the alerting history. A separate ledger
would duplicate it for a debugging convenience no FR/NFR asks for. Left as
a possible backlog nicety, not built now.

## Decision

**Option B.** States: `Healthy` (streak = 0), `Suspect` (1 ≤ streak <
threshold N), `Alerting` (streak ≥ N, one open `incidents` row). A fourth
condition, `Unknown`, is deliberately **not** a fourth state of this
machine — see below.

Given `previous_streak`, `previous_state`, threshold `N`, and a completed
check `outcome ∈ {Success, Failure}`:

- **Success, previous_state ∈ {Healthy, Suspect}** → `streak = 0`,
  `state = Healthy`. No incident write — there was none open.
- **Success, previous_state = Alerting** → `streak = 0`,
  `state = Healthy`, and attempt to close:
  `UPDATE incidents SET closed_at = now() WHERE target_id = $1 AND closed_at IS NULL RETURNING id`.
  A returned row means dispatch exactly one resolution notification
  referencing that incident's ID. Zero rows returned (already closed by a
  prior evaluation) means dispatch nothing. **Exactly one resolution
  notification is a property of this conditional write, not of the caller
  only ever invoking it once.**
- **Failure, `previous_streak + 1 < N`** → `streak += 1`,
  `state = Suspect`. No `incidents` write at all — this is US-006's
  transient-blip zone, deliberately silent: a `Suspect` streak that later
  resets to `Healthy` leaves no trace requiring cleanup.
- **Failure, `previous_streak + 1 == N`** (crossing the threshold this
  check) → `streak = N`, `state = Alerting`, and attempt to open:
  `INSERT INTO incidents (target_id, opened_at) SELECT $1, now() WHERE NOT EXISTS (SELECT 1 FROM incidents WHERE target_id = $1 AND closed_at IS NULL) RETURNING id`.
  A returned row means dispatch exactly one "opened" alert referencing that
  incident's ID. This is additionally backed by a schema-level guarantee,
  not only the `NOT EXISTS` guard: `incidents` carries a partial unique
  index, `UNIQUE (target_id) WHERE closed_at IS NULL` (see
  `04-data-model.md`) — even a bug that bypasses the application-level
  guard cannot produce two simultaneously-open incidents for one target;
  Postgres itself rejects the second `INSERT` with a constraint violation
  rather than silently allowing it.
- **Failure, `previous_streak + 1 > N`** (already `Alerting`) →
  `streak += 1`, `state` stays `Alerting`. **No `incidents`-table write, no
  dispatch at all** — the code path for "already alerting" only increments
  a counter; it never re-evaluates the open-incident condition. This is
  FR-016 made structural: an `incidents` row is only ever written on the
  two edge transitions (crossing into `Alerting`, or leaving it), never on
  staying in it.
- **Unknown** (agent stale, or the server's own scheduler wasn't running —
  ADR-0003, FR-011, FR-019): the transition function above is **not
  invoked at all** for this period — there is no completed `outcome` to
  evaluate. `streak`/`state` pass through unchanged into whatever the next
  real (non-`Unknown`) check produces. This is the precise mechanism behind
  "pauses, doesn't reset": it is not a third branch of the function, it's
  the function simply not running. An already-open incident stays open and
  persisted through an `Unknown` period the exact same way it stays open
  across a restart (US-008) — in both cases, nothing happened to change the
  row, so the row didn't change.

**Storage:** `streak` and `state` live on the same `target_schedule` row
ADR-0001 already defines (not a separate table). Both ADR-0001's
lease-release write and this ADR's transition write happen in the same
transaction as the check-result insert, so a crash between "recorded the
result" and "evaluated the alert state" is not observable as a distinct,
inconsistent state either.

**Restart safety for this state machine specifically** (US-008, NFR-004):
`streak`, `state`, and any open `incidents` row are persisted columns, not
in-memory — a fresh process reads them back exactly as the prior process
left them, and the transition function is pure given those values, so there
is no separate "reconstruct alerting state after restart" logic beyond
"read the current row" — mirroring ADR-0001's "no special-cased recovery
path" property.

## Trade-offs accepted

- The silent `Suspect` zone means a transient blip is genuinely invisible
  in the `incidents` history, by design. An operator wanting "how often
  does this target almost breach" would need raw/rollup check-result data,
  not the incidents table. Accepted: the incidents table's purpose is
  alerting history, not near-miss analytics, and near-miss tracking wasn't
  asked for by any FR/NFR.
- Two guarded writes (conditional open/close, backed by a partial unique
  index) add real complexity over the naive if-statement version —
  accepted because it converts "duplicate alert" from an invariant the
  caller must never violate into an invariant the database enforces even
  if the caller does.

## Consequences

- `04-data-model.md` defines `target_schedule.streak` (int),
  `target_schedule.state` (enum: `healthy`/`suspect`/`alerting` — `unknown`
  is never a persisted value here, see below), and `incidents` (id,
  target_id, opened_at, closed_at nullable, with the partial unique index).
- The dashboard (US-009, FR-022) reads `target_schedule.state` directly for
  healthy/suspect/failing, and separately checks agent freshness
  (ADR-0003) to overlay "unknown" at read time — "unknown" is a
  display/SLO-math concept computed from agent staleness, never a value
  written into `target_schedule.state`. This keeps the alerting state
  machine itself to 3 states with no unknown-handling transitions of its
  own to get wrong.
- Alert dispatch (webhook/email, FR-013/FR-014) is triggered only after the
  conditional write above actually returns a row. This ADR fixes that
  dispatch is gated on a successful conditional write; the delivery
  mechanism's own retry/outbox semantics are an Implementation (Session 4)
  concern, deliberately left there.

## Revisit triggers

- If a future backlog item (adaptive/anomaly thresholds — explicitly
  deferred, `01-scope-and-non-goals.md`) replaces the fixed
  consecutive-failure count, only the Failure branch's threshold
  comparison changes. The conditional-write guard pattern for exactly-once
  open/close survives that change unmodified.
