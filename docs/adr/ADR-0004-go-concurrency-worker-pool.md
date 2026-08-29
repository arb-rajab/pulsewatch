# ADR-0004 — Bounded Worker-Pool Scheduler with Context-Based Graceful Drain

- **Date:** 2026-08-29
- **Status:** accepted

## Context

Go concurrency patterns (worker pools, context cancellation, graceful
shutdown) are one of this repo's two frozen learning-budget technologies
(`00a-ledger-confirmation.md`), and `00-project-brief.md` §3 is explicit
that this is "load-bearing rather than decorative" — the concurrency design
has to actually satisfy real numeric NFRs, not just "use goroutines" and
call it done: scheduling jitter ≤2s (NFR-001), restart recovery ≤10s at
≤100 targets (NFR-003), zero overlapping checks against the same target
(NFR-007), and fan-out after a mass-restart staying within a configured
concurrency limit rather than firing an unbounded synchronized burst
(US-007). It must also connect directly to ADR-0001: graceful shutdown has
to drain or safely abandon in-flight checks, not kill them in a way that
corrupts a result or defeats the leasing design that makes recovery safe.

## Options considered

### A — Unbounded goroutine-per-due-target

`go executeCheck(target)` fired directly from the tick loop for every due
target, no pool.

**Rejected.** No bound on simultaneous outbound network calls the process
makes — a mass-restart scenario with, say, 100 simultaneously-overdue
targets (NFR-003's own scale ceiling) would fire 100 concurrent HTTP/TCP
dials in one instant, directly violating US-007's "fan-out stays within a
configured concurrency limit." Also has no natural place to hang a
`sync.WaitGroup` for a bounded graceful-shutdown drain without extra,
separately-invented bookkeeping.

### B — `errgroup.Group` with `SetLimit(n)`, spawned per tick

A bounded semaphore of goroutines created fresh each tick.

Functionally close to Option C below, but goroutines are created and torn
down every tick rather than living for the process's lifetime. At the 1s
tick interval this design settles on (below), that is meaningful churn
over a process meant to run continuously for days or weeks — and
per-worker self-observability (item 7, `03-architecture.md`; e.g. "worker
#3's cumulative check count") has no stable identity to attach metrics to
across ticks. A reasonable variant, not a wrong answer — but it fits this
project's actual continuous-operation framing worse than a long-lived pool.

### C — Long-lived, fixed-size worker pool (chosen)

`N` goroutines started once at process startup, each looping on a shared
buffered channel of `CheckJob`s until the channel closes or the root
context is canceled. The scheduler tick's due-set query (ADR-0001) pushes
each due target onto that channel — a blocking send, which is itself the
backpressure mechanism: if all `N` workers are busy, the send blocks rather
than spawning a 101st concurrent goroutine, bounding fan-out to `N` without
a separate semaphore.

## Decision

**Option C.** Concrete design:

- **Tick interval: 1s.** Derived from NFR-001, not chosen first and
  justified after: a 1s poll grants roughly ±1s of pure polling-induced
  jitter, leaving real headroom under the 2s budget for the claim
  round-trip and dispatch latency. A coarser tick (e.g. 5s, which might
  look more "efficient") would already consume the entire jitter budget on
  polling granularity alone before any real work happens.
- **Worker pool size: 20, operator-configurable.** At the measured-scale
  assumption (~5–10 targets), this is already far more than steady-state
  needs (most ticks find 0–1 due targets) — it is sized against NFR-003's
  restart-recovery ceiling instead. A full-restart burst of up to 100
  simultaneously-overdue targets drains in at most 5 sequential batches
  through a 20-worker pool, each batch bounded by that batch's slowest
  check's own configured timeout — a small, bounded multiple of one check
  timeout, not instantaneous but predictably bounded, which is what
  US-007's "not an unbounded synchronized burst" language actually asks
  for.
- **Job dispatch:** a channel send only means "this target looked due when
  the tick ran." A worker pulling a job first attempts the ADR-0001 lease
  claim — this is still what enforces actual exclusivity, covering the
  case where a worker is mid-check on a target from a *previous* tick's
  dispatch when this tick's due-set query also returns it (a target whose
  check duration exceeds its interval — the exact slow-target scenario
  NFR-007 names). A failed claim (zero rows) is a silent, expected skip,
  not an error — it is the normal outcome of exactly the race NFR-007
  tests for.
- **Check execution:** each claimed check runs under
  `context.WithTimeout(rootCtx, target.Timeout)` — the operator-configured
  per-check timeout is always the first thing that can end a check's
  HTTP/TCP call. Graceful shutdown (below) is a second, independent bound
  layered on top, never a replacement for per-check timeouts.
- **Graceful shutdown (SIGTERM/SIGINT):** canceling the process's root
  lifecycle signal does two things, in order:
  1. The scheduler tick loop stops issuing new ticks/claims immediately —
     no new work is dispatched once shutdown begins.
  2. Already-claimed, in-flight workers are **not** immediately
     canceled — each check's own per-check timeout context (above)
     continues to govern it independently, so a check 2s into a 10s
     timeout gets to finish naturally or hit its own timeout, exactly as
     if no shutdown were happening, rather than being yanked mid-request
     and leaving a half-processed result.

  A `sync.WaitGroup` tracks currently-claimed workers; the main goroutine
  blocks on `wg.Wait()` up to a **hard shutdown deadline** — a shorter,
  separate context (e.g. 30s, chosen to comfortably exceed realistic
  per-check timeout defaults, which are a small fraction of FR-003's
  [10s, 24h] *interval* range and are a distinct config value from it).
  Past that deadline, any still-running worker's context is force-canceled
  and the process exits regardless — this is what makes shutdown itself
  bounded, so a deploy cannot hang forever on one slow check.

## The direct link to ADR-0001 (as this session requires)

Force-canceling at the hard deadline is only safe **because** ADR-0001
chose durable Postgres leasing over in-memory-only tracking. A check
in-flight when shutdown begins has exactly three possible outcomes, all
safe:

1. **Finishes naturally before the hard deadline** — result recorded,
   lease released, normal.
2. **Hits its own per-check timeout first** — recorded as a timeout
   failure (US-003), lease released, normal.
3. **Still running at the hard shutdown deadline, force-canceled** — no
   result is recorded, and the lease is simply abandoned. It expires on
   schedule (ADR-0001), and the next process — the very next start of this
   same service, since v1 is single-instance — reclaims and re-runs that
   one check on its first tick. Outcome 3 costs one delayed check for one
   target, never lost scheduling state and never a duplicate alert
   (ADR-0002's state machine only reacts to a completed outcome, and
   outcome 3 produces none). If leasing lived only in memory (ADR-0001
   Option A), this same force-cancel would instead be genuinely unsafe —
   the new process would have no record that target was ever claimed, but
   also no guaranteed correct alternative signal that it's now free either.
   The hard-deadline shutdown bound and the leasing design are the same
   trade-off applied at two different layers, not two independent choices.

## Trade-offs accepted

- Worker pool size and tick interval are process-startup configuration,
  not runtime-adaptive — a burst larger than the configured ceiling would
  take proportionally longer to drain. Accepted as sized correctly against
  this project's own measured-scale assumptions and NFR-003's explicit
  ceiling, not built to handle an unstated larger scale.
- The graceful-drain design means a deploy can legitimately take up to the
  hard shutdown deadline (tens of seconds) rather than exiting instantly —
  a real, small cost to release velocity. Accepted because Release &
  Deployment (a deep phase for this repo) explicitly cares more about not
  corrupting in-flight state during a deploy than about shutdown being
  instant.

## Consequences

- `03-architecture.md`'s restart-recovery sequence diagram shows this
  exact shutdown/startup shape, not a simplified "process stops, process
  starts."
- Implementation (Session 4) must expose worker-pool size, tick interval,
  and hard-shutdown-deadline as configuration (env vars, matching this
  repo's existing `${VAR:-default}` docker-compose pattern), not hardcoded
  constants — this ADR's numbers are reasoned defaults, not immutable
  ones.
- Self-observability (item 7, `03-architecture.md`) exports worker-pool
  utilization (busy/idle workers) and shutdown-drain duration as metrics —
  the concrete, scoped self-observability use OTel serves here, distinct
  from agent telemetry (ADR-0003).

## Revisit triggers

- If real dogfooding data (once pulsewatch monitors itself and,
  eventually, other flagships per the brief's aspirational framing) shows
  the default pool size or hard-shutdown deadline is measurably wrong,
  adjust the config defaults — expected tuning, not a sign the pool-based
  design itself needs to change.
