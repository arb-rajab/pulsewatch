# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed
- Session number and title: **Session 3 — Architecture & Design**
- Objective: Design the concrete architecture — scheduler restart-safety,
  alert-suppression, agent reachability, and Go concurrency model — that
  satisfies every FR/NFR in `02-requirements.md`, as real ADRs with
  options-considered reasoning, not defaulted implementations.
- Status: **complete**

## Work completed
- Wrote four ADRs (`docs/adr/`), each with real options considered and trade-offs stated, matching this portfolio's established ADR rigor (calibrated against `lexicon/docs/adr/ADR-0001` and `privacy-forge/docs/adr/ADR-0006`) despite this repo's baseline (not deep) Architecture phase:
  - **ADR-0001** — restart-safe, duplicate-proof scheduling via an atomic Postgres row-leasing claim (`target_schedule.lease_owner`/`lease_expires_at`), rejecting in-memory-only tracking, a Redis lock, and `pg_advisory_lock`. The key insight: there is no separate "restart recovery" code path — a fresh process's first tick runs the identical due-set query and claim statement as every other tick, because an abandoned lease with a past expiry is indistinguishable from an ordinary expired lease regardless of which process wrote it.
  - **ADR-0002** — an explicit 3-state alert-suppression machine (Healthy → Suspect → Alerting) with a precise transition function, where "exactly one opened alert" and "exactly one resolution notification" are enforced by an atomically-conditional database write (backed by a partial unique index, `incidents (target_id) WHERE closed_at IS NULL`) rather than by caller discipline. `Unknown` (agent stale, or pulsewatch itself was down) is modeled as the transition function simply not being invoked for that period — streak/state pass through unchanged — not a fourth state with its own transitions.
  - **ADR-0003** — agent-initiated outbound push only (never server-pull, never a manually-distributed static config file): check results + heartbeat via OTLP through the OTel Collector, assignment-list discovery via an authenticated REST poll from the agent. Resolves Session 2's open question (how an agent learns its assignments) and makes the agent-staleness-vs-target-down distinction concrete: agent freshness is evaluated *before* a target's own alert state machine, so a stale agent suppresses evaluation entirely (`Unknown`) rather than ever being fed into it as a `Failure`.
  - **ADR-0004** — a long-lived, fixed-size worker pool (default 20 workers, 1s scheduler tick — the tick interval is derived directly from NFR-001's ±2s jitter budget, not chosen first), fed by a blocking job channel for natural backpressure, with per-check `context.WithTimeout` and a bounded hard-shutdown deadline. Explicitly ties back to ADR-0001: force-canceling an in-flight check at the shutdown deadline is only safe because Postgres leasing (not in-memory tracking) makes an abandoned check self-healing, not lost.
- Wrote `03-architecture.md`: C4 system-context and container diagrams; three sequence diagrams (normal check execution — including the agent-executed variant converging on the same code path; restart recovery — showing there's no bespoke recovery routine; alert-fire-vs-suppress — showing every branch of ADR-0002's transition function); component responsibilities table; failure-handling/degradation modes for each component; backup/recovery scope (deferred concretely to `08-deployment-and-operations.md`, not invented here); and a concretely-scoped OpenTelemetry self-observability section (server-internal scheduler/alerting instrumentation via the same collector already required for agent telemetry, exported to a debug/logging exporter — explicitly not a Grafana/Prometheus visualization backend, which would reintroduce the excluded third technology).
- Wrote `04-data-model.md` with the concrete plain-Postgres rollup/retention design Session 1's TimescaleDB rejection defers to this session: `check_results` as a declaratively range-partitioned table (partitioned by day), retention enforced by **dropping** whole partitions (O(1), no per-row `DELETE`/bloat) rather than row deletion, gated so retention never races ahead of rollup; a single `check_rollups_hourly` tier retained the full 400 days (NFR-012) — deliberately not split into a second daily tier, since ~9,600 rows/target at that retention is trivially fast to scan directly at this project's scale; `unknown_count` computed as `expected_checks - success_count - failure_count` per hourly bucket, which is the concrete mechanism behind FR-011/FR-019's "excluded from both numerator and denominator" carve-out. Also resolved Session 2's second open question: response-body-match fragments are capped at 512 bytes, with a truncation flag.
- Wrote `09-decision-log.md` as the short-form ADR index (matching the `privacy-forge` pattern — full reasoning stays in `docs/adr/`, this is "read first, then open the linked ADR").
- Updated `docs/SDLC-EVIDENCE.md`'s Phase 3 row with evidence links, keeping the depth marker `baseline` per this repo's own ledger.

## Files created or changed
- `docs/adr/ADR-0001-scheduler-restart-safety-leasing.md` — new.
- `docs/adr/ADR-0002-alert-suppression-state-machine.md` — new.
- `docs/adr/ADR-0003-agent-push-reachability-model.md` — new.
- `docs/adr/ADR-0004-go-concurrency-worker-pool.md` — new.
- `docs/project-memory/03-architecture.md` — filled in from template.
- `docs/project-memory/04-data-model.md` — filled in from template.
- `docs/project-memory/09-decision-log.md` — filled in from template (index of the four ADRs above).
- `docs/SDLC-EVIDENCE.md` — Phase 3 row populated with evidence links.
- `docs/project-memory/12-session-handoff.md` — this file.

## Decisions made
- **Postgres row leasing (not Redis, not advisory locks, not in-memory) is the restart-safety/run-exclusivity mechanism** — ADR-0001. Chosen specifically because it keeps the "is this due" fact and the "is this claimed" guard in the same transactional system, closing a cross-system race class that a Redis-based lock would reopen.
- **Alert suppression is a database-enforced state machine, not an inline counter check** — ADR-0002. The exactly-once guarantee for incident open/close is backed by both a conditional write and a partial unique index — a defense-in-depth pattern, not single-layer trust in application code.
- **Agent communication is push-only in both directions that matter (results via OTLP, assignments via agent-initiated REST poll)** — ADR-0003. Rejected using the OTel/OTLP channel itself as a bidirectional control channel for assignment push (Option C) — OTLP's receiver/exporter model isn't shaped for that, and it would blur the OTel learning objective's actual scope.
- **Worker pool sizing and tick interval are derived from the NFRs, not picked by convention** — ADR-0004: the 1s tick comes directly from NFR-001's ±2s jitter budget; the 20-worker default comes directly from NFR-003's ≤100-target restart-recovery ceiling (a full burst drains in ~5 bounded batches).
- **Rollup/retention stays a single-tier hourly design on native Postgres partitioning** — `04-data-model.md`. No second daily rollup tier, no TimescaleDB (frozen, not reopened) — sized to this project's actual ~10-target scale, with an explicit revisit-on-evidence trigger if that assumption turns out wrong.
- **Response-body-match fragment cap: 512 bytes, with a truncation flag** — resolves the Session 2 data-classification caveat concretely.
- **`05-api-contracts.md` was not written this session** — this session's explicit definition of done (four ADRs, `03-architecture.md`, `04-data-model.md`, evidence/handoff updates) did not require it, and writing it well requires the agent-assignment endpoint and ingestion-endpoint shapes ADR-0003 names but doesn't fully specify at the wire-format level. Flagged as the natural next piece of work, not silently dropped — see Next recommended session.

## Validation performed
- Cross-checked every one of the four required decisions against the specific FR/NFR/US IDs it has to satisfy (cited inline in each ADR's Context section) — confirmed NFR-001/003/004/007 and US-003/007/008 are addressed by ADR-0001; FR-004/013-016 and US-005/006/008 by ADR-0002; FR-017/018/019 and US-010/NFR-008 by ADR-0003; NFR-001/003/007 and US-007 by ADR-0004.
- Confirmed the two Session 2 open questions (agent target-assignment mechanism; response-body-match fragment cap) are both resolved with stated reasoning, not left open again — ADR-0003 for the first, `04-data-model.md`'s entity description for the second.
- Confirmed the TimescaleDB-vs-Postgres decision was not reopened — `04-data-model.md`'s rollup/retention section is framed explicitly as "the concrete consequence of" Session 1's decision, and reasons from real Postgres-native features (declarative partitioning, partial unique indexes), not from re-litigating whether TimescaleDB should be used instead.
- Confirmed no requirement from `02-requirements.md` was silently dropped or expanded: re-read all 23 FRs and 12 NFRs against the four ADRs and `03-architecture.md`/`04-data-model.md` — every FR/NFR that names a mechanism (not just a number) now has a concrete architectural answer; NFR-009 (dashboard read performance) and NFR-005/006 (rollup/retention timing) are addressed by the single-tier hourly rollup design's stated scan cost, not left as an unaddressed target.
- Confirmed the four ADRs are internally consistent with each other where they interlock: ADR-0002 and ADR-0004 both read/write `target_schedule` in the same transaction as ADR-0001's lease release (checked across all three documents, not just asserted in one); ADR-0004's shutdown-safety argument explicitly cites ADR-0001 rather than restating leasing's rationale independently.
- Confirmed no implementation code, dependency, or `docker-compose.yml` change was made this session — Architecture & Design only, per the ground rules. The OTel Collector's eventual addition to `docker-compose.yml` is named as required future work (ADR-0003 Consequences) rather than done now. `privacy-forge`, `laravel-consent-guard`, `bookslot`, and `lexicon` were not modified — read-only reference to `lexicon/docs/adr/ADR-0001` and `privacy-forge/docs/adr/ADR-0006` for ADR-rigor calibration only.

## Open questions and risks
- **Resolved this session (were open after Session 2):** agent target-assignment mechanism (ADR-0003: authenticated REST poll) and the response-body-match fragment cap (`04-data-model.md`: 512 bytes).
- **New, explicitly flagged item for Session 4 (Implementation):** `05-api-contracts.md` does not exist yet — the agent-assignment REST endpoint's request/response shape and the server's OTLP-forwarded-telemetry ingestion endpoint both need a concrete contract before Implementation can start building either. Not a blocker for Session 3's own definition of done, but should be the first thing addressed before or at the start of Session 4.
- **New, named but not resolved (deliberately, per this session's ground rules):** exact worker-pool size (20), tick interval (1s), and hard-shutdown-deadline (~30s) defaults in ADR-0004 are reasoned from the stated NFRs but are unmeasured against a real implementation — flagged in the ADR's own Revisit triggers as expected tuning once real dogfooding data exists, not a design gap.
- **Carried forward from Session 2, still open:** Go concurrency patterns and OTel collector pipelines are both genuinely new implementation work — ADR-0004 and ADR-0003 give them a concrete design now, but neither has been built or measured yet; Session 4 is where the ±2s jitter, ≤10s restart recovery, and 0-duplicate-alert targets actually get tested against real code for the first time.
- **No blockers.** Session 4 (Implementation) can start once `05-api-contracts.md` is written (either as the first piece of Session 4, or as a short intermediate session).

## Next recommended session
- Proposed session title: **Session 4 — Implementation (Part 1: scheduler, leasing, alert state machine)** — optionally preceded by a short API-contracts pass to produce `05-api-contracts.md` first, since ADR-0003 names concrete endpoints it doesn't fully specify at the wire-format level.
- Single objective: Build the Go scheduler/worker pool (ADR-0004), the Postgres leasing claim/release (ADR-0001), and the alert-suppression state machine (ADR-0002) as real, tested code — this is the core continuous-operation machinery every other requirement depends on, and the four ADRs from this session are its exact specification.
- Inputs required: this handoff; `02-requirements.md`; `docs/adr/ADR-0001` through `ADR-0004`; `03-architecture.md`; `04-data-model.md`.
- Expected deliverables: `05-api-contracts.md` (if not done as a preceding short session); the `target_schedule`/`check_results`/`check_rollups_hourly`/`incidents` schema as real migrations; the scheduler, worker pool, leasing claim, and alert state machine as real Go code with tests exercising NFR-001 (jitter), NFR-003 (restart recovery), NFR-004 (0 duplicate alerts), and NFR-007 (0 overlapping checks) directly — not just implemented, verified, matching this repo's own "not just non-crashing" standard from `00-project-brief.md`'s success metrics.
- Definition of done: every mechanism named in ADR-0001/0002/0004 exists as real, tested code; the four ADRs' Consequences sections (e.g. "ADR-0004 must implement the claim as the first thing a worker does") are checked against the actual implementation, not left as unread prose.

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Repository state:** branch `main`, unreleased (pre-v0.1.0), Sessions 1–3 complete

**Problem being solved:** This developer maintains several self-hosted flagship repositories (`privacy-forge`, `laravel-consent-guard`, `bookslot`, `lexicon`), none of which currently has a real, live deployment, plus pulsewatch's own future instance. There is no continuously running, honest answer to "is it actually up right now, and did I meet my SLO" for whatever of this does run — and no mechanism that tells this developer the moment it isn't. Correctness here is about the system continuing to tell the truth over hours and days — surviving its own restarts, never double-alerting, never missing a real outage — not a single request's output. See `00-project-brief.md` for full framing.

**Users:** Primary and effectively only user — this developer, operating pulsewatch against whatever real deployments actually exist. v1 is single-operator; the only other identity in the system is the machine "Agent" role (`02-requirements.md`).

**Current stack:**
- Frontend: SvelteKit
- Backend: Go 1.25 (Gin)
- Data: PostgreSQL (plain — TimescaleDB explicitly rejected in Session 1), Redis (available, not load-bearing for scheduling — ADR-0001)
- Infra: Docker Compose, GitHub Actions, OpenTelemetry Collector (designed this session — ADR-0003; not yet added to `docker-compose.yml`, that's Session 4 work)
- Testing: Go's `testing` package, Vitest

**Architecture decisions that must not be reversed:**
- Licence is AGPL-3.0 (hostable app, not a library).
- Primary frontend/backend framework pair is fixed (Go + Gin, SvelteKit) — frozen against the portfolio-wide framework allocation ledger.
- Exactly two deep SDLC phases: Release & Deployment, Operations & Maintenance.
- Exactly two new technologies for the learning budget: Go concurrency patterns, OpenTelemetry collector pipelines — plain PostgreSQL, not TimescaleDB.
- **New this session, must not be silently reversed:** ADR-0001 (Postgres row leasing for restart-safety/exclusivity), ADR-0002 (explicit alert-suppression state machine with DB-enforced exactly-once transitions), ADR-0003 (agent-initiated push only, both for results and for assignment discovery), ADR-0004 (bounded worker pool, 1s tick, context-based graceful drain tied to ADR-0001's leasing) — see `docs/adr/` for full reasoning, `09-decision-log.md` for the short index.

**Implementation state:**
- Done: repository skeleton, licence, governance docs, minimal real Go/Gin backend and SvelteKit frontend with passing tests and clean lint, real CI, working `docker compose up`, finalised project brief/build-vs-alternatives/scope-and-non-goals (Session 1), a complete requirements document (Session 2), and now a complete, decided architecture — four real ADRs, system/container/sequence diagrams, and a concrete rollup/retention data model (Session 3).
- In progress: nothing mid-flight.
- Not started: all actual implementation — no monitoring logic, agent code, scheduler, worker pool, leasing, alert state machine, or migrations exist yet. Session 3 designed exactly what Session 4 needs to build; nothing has been built.

**Constraints and non-goals:**
- Max 2 new technologies for this repo — already at cap (Go concurrency, OTel pipelines); TimescaleDB, Prometheus/Grafana, and any other third technology are explicit non-goals — this session reaffirmed the OTel self-observability use stays scoped to server-internal instrumentation exported via the same collector, explicitly not a Grafana/Prometheus visualization backend.
- Full non-goals table (8 rows, with reconsider-triggers) is in `01-scope-and-non-goals.md`.
- v1 is single-operator; no multi-user auth/role separation or public status page exists at v1.

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning (deep in `lexicon`), Requirements Analysis (deep in `privacy-forge`), Verification & Testing (deep in `lexicon`), Retirement & Handover (deep in `privacy-forge`)
**Baseline-depth, but with real ADRs on their own merits:** Architecture & Design (this session) — see `00a-ledger-confirmation.md` for why phase-depth and ADR rigor are independent axes in this portfolio's convention.

**Task for this session (single objective) — now complete:**
Design the concrete architecture for scheduler restart-safety, alert suppression, agent reachability, and the Go concurrency model, as real ADRs with options-considered reasoning, plus a rollup/retention data model and tying diagrams. **Done — see Work completed above.**

**Definition of done — met:**
- Four real ADRs (`docs/adr/`), each with options considered and real trade-offs stated, matching this portfolio's established ADR format.
- `03-architecture.md` ties them together with system/container/sequence diagrams.
- `04-data-model.md` reflects the rollup/retention strategy concretely — real schema (declarative partitioning, partial unique index, single-tier hourly rollup), not just prose.
- `docs/SDLC-EVIDENCE.md`'s Phase 3 row and this handoff are updated.

**Files to attach or paste for Session 4:**
- `docs/project-memory/02-requirements.md`
- `docs/adr/ADR-0001-scheduler-restart-safety-leasing.md`
- `docs/adr/ADR-0002-alert-suppression-state-machine.md`
- `docs/adr/ADR-0003-agent-push-reachability-model.md`
- `docs/adr/ADR-0004-go-concurrency-worker-pool.md`
- `docs/project-memory/03-architecture.md`
- `docs/project-memory/04-data-model.md`
- `docs/project-memory/12-session-handoff.md` (this file)

**Ground rules:** Do not change the stack. Do not introduce a third new technology. Do not expand the deep-SDLC-phase count beyond two. Do not reopen the TimescaleDB-vs-Postgres decision, or any of the four ADRs from this session, without new measured evidence per their own Revisit triggers — design around them. Session 4 (Implementation) should treat `05-api-contracts.md` as a needed first step (or a short preceding session) before or alongside building the scheduler/leasing/alert-state-machine code the four ADRs specify.
