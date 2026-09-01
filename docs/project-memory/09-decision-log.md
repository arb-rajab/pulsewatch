# Decision Log
> Purpose: why things are the way they are, so decisions are not silently undone
> Project: pulsewatch (public)
> Last updated: 2026-08-29 (Session 3)

Full reasoning for each ADR lives in `docs/adr/`. This log is the
short-form index — read it first, open the linked ADR for the trade-off
detail.

## ADR-0001 — Scheduler Restart-Safety and Run-Exclusivity via Postgres Row Leasing
- **Date:** 2026-08-29 · **Status:** accepted · [Full ADR](../adr/ADR-0001-scheduler-restart-safety-leasing.md)
- **Decision:** an atomic conditional `UPDATE ... RETURNING` lease claim on
  a per-target `target_schedule` row (owner + expiry), not in-memory
  tracking, a Redis lock, or `pg_advisory_lock`.
- **Must not be silently reversed because:** it's the single mechanism
  that satisfies both FR-005/NFR-007 (never two overlapping checks) and
  FR-006/NFR-003/NFR-004 (restart recovery, zero duplicate alerts) with no
  separate "restart recovery" code path — a fresh process's first tick
  runs the identical claim logic as every other tick.

## ADR-0002 — Alert-Suppression State Machine
- **Date:** 2026-08-29 · **Status:** accepted · [Full ADR](../adr/ADR-0002-alert-suppression-state-machine.md)
- **Decision:** an explicit 3-state machine (Healthy/Suspect/Alerting)
  whose edge transitions (open/close an incident) are guarded by an
  atomically-conditional write, backed by a partial unique index
  (`incidents (target_id) WHERE closed_at IS NULL`) — not an inline
  counter check.
- **Must not be silently reversed because:** it's what makes "exactly one
  opened alert, exactly one resolution notification" (US-005/FR-015/
  FR-016) a database-enforced guarantee rather than a caller-discipline
  assumption, including across a restart (US-008).

## ADR-0003 — Agent-Initiated Push Reporting and Assignment Discovery
- **Date:** 2026-08-29 · **Status:** accepted · [Full ADR](../adr/ADR-0003-agent-push-reachability-model.md)
- **Decision:** agent-initiated outbound reporting only (results via OTLP
  through the OTel Collector; assignment list via an authenticated REST
  poll from the agent) — never server-pull, never a manually-distributed
  static config file.
- **Must not be silently reversed because:** it's the concrete mechanism
  that makes FR-018's "no inbound port on the agent's network" real, and
  the direct fulfillment of the private-target-reachability justification
  `00b-build-vs-alternatives.md` names for building pulsewatch at all.

## ADR-0004 — Bounded Worker-Pool Scheduler with Context-Based Graceful Drain
- **Date:** 2026-08-29 · **Status:** accepted · [Full ADR](../adr/ADR-0004-go-concurrency-worker-pool.md)
- **Decision:** a long-lived, fixed-size worker pool (default 20) fed by a
  blocking channel, 1s scheduler tick, per-check `context.WithTimeout`, and
  a bounded hard-shutdown deadline after which an abandoned in-flight
  check's lease is safely left to expire (ADR-0001) rather than force
  anything to complete.
- **Must not be silently reversed because:** the worker-pool bound is what
  satisfies US-007's "not an unbounded synchronized burst" after a
  mass-restart, and the graceful-drain design is only safe because of
  ADR-0001's durable leasing — the two are one connected design, not two
  independent choices.

## Session 12 — Hourly Rollup Job: Recompute-Everything, Not Bounded-Lookback
- **Date:** 2026-08-31 · **Status:** accepted (not a new ADR — implements a
  pre-aggregation decision `04-data-model.md` already made in Session 3/4) ·
  [Full write-up](../project-memory/04-data-model.md#session-12-addendum-the-hourly-rollup-job-real-implemented)
- **Decision:** `internal/rollup`'s hourly job recomputes and
  `ON CONFLICT ... DO UPDATE`s every `(target, hour_bucket)` row since each
  target's `created_at` on every tick, rather than a bounded lookback plus a
  separate one-time backfill pass.
- **Must not be silently reversed because:** it's what makes the job
  self-healing for late-arriving `check_results` (an agent-reported OTLP
  result landing after its own hour already rolled up) with no separate
  dirty-bucket tracking table — replacing it with a bounded lookback removes
  that self-healing property for anything outside the lookback window unless
  a real, separate reconciliation mechanism replaces it. Revisit only
  against measured tick duration exceeding NFR-005's 5-minute bound (this
  session measured 350ms on real data), not by default habit.
