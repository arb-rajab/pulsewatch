# ADR-0001 — Scheduler Restart-Safety and Run-Exclusivity via Postgres Row Leasing

- **Date:** 2026-08-29
- **Status:** accepted

## Context

`02-requirements.md` names two distinct safety properties the scheduler must
hold, both with real numeric targets: per-target run exclusivity — never two
overlapping checks against the same target (FR-005, NFR-007, US-003) — and
restart safety — scheduling state persists across a process restart and
recovers within a bounded time with zero duplicate alerts (FR-006, NFR-003,
NFR-004, US-007, US-008). These read as one requirement but are actually two
distinct race conditions that need one mechanism to close both, not two:

1. **Same-process race.** A target's check takes longer than its own
   interval (a slow or hanging target); the next scheduler tick must not
   start a second concurrent check against it.
2. **Restart-boundary race.** The server process dies (crash, deploy,
   restart) and a fresh process starts. `00a-ledger-confirmation.md` names
   Release & Deployment as one of this repo's two deep phases specifically
   because the system must "handle agent/server version skew during a
   rolling upgrade" and keep monitoring through its own releases. ADR-0004
   (Go concurrency) deliberately keeps an old process's in-flight workers
   alive briefly during a graceful shutdown so a check isn't corrupted
   mid-request — which means, by construction, there can be a real (if
   brief) window where two processes are both live. Whatever mechanism
   enforces exclusivity has to be correct in that window, not just in the
   steady-state single-process case.

v1 is explicitly single-instance (`01-scope-and-non-goals.md`) — this is not
a horizontal-scaling problem. But "single instance in steady state" and
"exactly one process ever executing a given check's request at a time" are
different guarantees, and the graceful-shutdown design in ADR-0004 means the
second one has to hold even during a restart.

## Options considered

### A — Pure in-memory tracking (mutex/set per target, inside the process)

A `sync.Map` (or similar) of currently-running target IDs, checked before a
worker starts a check. Trivial to implement, zero infrastructure cost.

**Rejected as the sole mechanism.** It fails exactly the restart-boundary
race: an in-memory set is empty in a fresh process by definition, so a new
process has no way to know "the old process's worker for target X might
still be running its HTTP request" during the graceful-drain overlap window
ADR-0004 creates — it would immediately reclaim and re-check every target
the instant it starts. It also gives no durable answer for a hard crash (no
graceful shutdown at all, e.g. OOM-killed): nothing persists the fact that
target X was mid-check, so the next process has nothing to reason about
beyond "well, it's due now." Retained only as a same-process fast-path
inside a single tick (a worker won't put a target it's already holding back
onto its own channel) — not what enforces the actual guarantee.

### B — Redis-based distributed lock (`SET NX PX`, TTL lease)

Redis is already in the stack, so it costs nothing against the learning
budget.

**Rejected.** This introduces a second system of record for scheduling
state that must stay consistent with Postgres's persisted `next_due_at` /
`last_checked_at` — a Redis eviction under memory pressure, a restart
without durable persistence enabled, or simple operational drift would
silently drop lease state with no relationship to the Postgres-side
schedule. There is no way to atomically read "is this target due" (a
Postgres fact) and "is this target currently leased" (a Redis fact) in one
consistent operation without a cross-system transaction, which Postgres and
Redis together cannot provide. Keeping the guard in the same database that
already holds the fact being guarded removes this whole class of
cross-system race by construction, rather than by discipline.

### C — `pg_advisory_lock` (session-scoped Postgres advisory locks)

**Rejected.** Advisory locks are tied to the database *session*
(connection) that acquired them, not to an explicit, durable, inspectable
row. If a connection pool transparently returns a connection without the
code path explicitly unlocking (a bug, or a client library swallowing a
context cancellation), the lock's actual lifetime becomes coupled to
connection-pool behavior in a way that's hard to reason about and invisible
to a normal `SELECT` (only `pg_locks` introspection shows it). It also
carries no owner/expiry pair that self-observability (OTel, scoped in
`03-architecture.md`) can cheaply export as a metric.

### D — Explicit Postgres lease row (chosen)

A dedicated `target_schedule` row per target carries `lease_owner` (a
process-instance identifier) and `lease_expires_at`. A worker claims a
target with a single atomic conditional statement:

```sql
UPDATE target_schedule
SET lease_owner = $1, lease_expires_at = now() + $2
WHERE target_id = $3
  AND next_due_at <= now()
  AND (lease_expires_at IS NULL OR lease_expires_at < now())
RETURNING target_id;
```

Zero rows returned means another worker (or a still-draining prior process)
already holds the lease, or the target is no longer due — a normal, silent
skip, not an error.

## Decision

**Option D.** Full mechanism:

- **Lease duration** (`$2`) = that target's configured check timeout plus a
  fixed safety margin (e.g. +5s): long enough that a legitimately-running
  check's own context-bounded execution (ADR-0004) can never outlive its
  lease under normal operation, short enough that a crashed worker's
  abandoned lease self-heals quickly.
- **Release**, on check completion (success, failure, or timeout): one
  transaction records the check result, advances `next_due_at = now() +
  interval`, sets `last_checked_at = now()`, and clears
  `lease_owner`/`lease_expires_at`. A crash between "check finished" and
  "release" cannot be observed as a distinct state — either the whole
  update lands, or the lease simply expires and the next tick re-claims and
  re-runs the check. This is safe because check execution has no side
  effect beyond producing a result row: re-running a check is not
  equivalent to re-sending an already-delivered alert. Preventing *that*
  class of duplication is ADR-0002's job, not this one's — this ADR only
  has to guarantee re-running a check is harmless, which it is by
  construction (idempotent by design, not by convention).
- **Restart recovery** (US-007, NFR-003) needs zero special-cased
  "recover in-flight checks" logic. A fresh process starts its scheduler
  loop immediately (ADR-0004); the very first tick's due-set query
  (`next_due_at <= now()`, indexed) already returns every target overdue at
  the moment of the crash, and the claim statement above already treats a
  lease with a past `lease_expires_at` as unclaimed, regardless of which
  process wrote it. There is no separate "restart recovery" code path
  distinct from "an ordinary due-set query" — the mechanism handling
  routine per-tick exclusivity and the mechanism handling restart-boundary
  recovery are the same code, not two systems that have to be kept
  consistent with each other.
- **Bounding recovery to ≤10s at ≤100 targets** (NFR-003): the due-set
  query is a single indexed scan returning at most 100 rows — low
  single-digit milliseconds on Postgres at this scale. The ≤10s budget is
  dominated by process boot and DB connection establishment, not by this
  query, so this design has wide headroom against the target rather than
  being a close call.

## Trade-offs accepted

- A worst-case abandoned lease (hard crash mid-check, no graceful shutdown)
  leaves that one target unclaimed for up to (timeout + margin) before
  self-healing — a bounded, small window affecting only the one target
  whose lease was held at the moment of the crash, not the whole scheduler.
- Every check now costs two small extra writes against Postgres (claim,
  release) beyond the check-result insert itself — negligible at this
  project's actual scale (~5–10 targets today, ≤100-target NFR-003 ceiling)
  but a real, stated cost that would need revisiting at orders-of-magnitude
  more targets.
- This ties scheduling correctness to Postgres availability during the
  claim/release window — accepted because Postgres is already the sole
  system of record for every other piece of durable state (targets,
  results, incidents, per FR-006/FR-012); there is no version of this
  design where Postgres being briefly unavailable wouldn't already stop the
  scheduler from doing anything useful.

## Consequences

- `04-data-model.md` defines `target_schedule` with the columns above and
  an index supporting the due-set scan.
- ADR-0004 (Go concurrency) must implement the claim as the first thing a
  worker does after pulling a job off the channel, and must treat "claim
  returned zero rows" as a normal, silent skip under routine operation, not
  a logged error.
- The lease-owner ID becomes a natural label for the self-observability
  metrics scoped in `03-architecture.md` (e.g. a gauge of currently-held
  leases per process — useful for confirming no more than one process is
  actively claiming during a deploy).
- Check execution must have no side effect beyond producing a result row
  (idempotent re-run) — a standing constraint on the check-execution code,
  since this design's safety depends on "re-running a check is harmless,"
  not merely "re-running a check is rare."

## Revisit triggers

- If target count ever grows enough that the claim/release write pair
  becomes measurably significant Postgres load (real evidence required,
  not assumed), consider batching claims for an entire due-set in one
  statement rather than one `UPDATE` per target — an optimization of the
  same mechanism, not a redesign.
- If a future session ever adds a second server instance for real (a
  genuine architecture change, out of v1's scope), this mechanism already
  generalizes without modification — the claim is already correct under
  concurrent claimants from multiple processes, since that is exactly what
  it protects against at the restart boundary today.
