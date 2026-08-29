# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed
- Session number and title: **Session 1 — Project Discovery & Business Framing**
- Objective: Validate every assumption in the draft brief with real reasoning, and produce the finalised `00-project-brief.md` plus `01-scope-and-non-goals.md` — framing the problem around continuous operation and real infrastructure monitoring, not a request/response product.
- Status: **complete**

## Work completed
- Rewrote `00-project-brief.md` in full — no "draft" markers remain. Reframed the problem statement around genuine dogfooding: the primary user is this developer, and the monitored infrastructure is this developer's own live/demo deployments of `privacy-forge`, `laravel-consent-guard`, `bookslot`, and `lexicon`, plus pulsewatch monitoring itself — a bounded, currently-existing target set (~5–10 services), not a hypothetical persona the way `bookslot`'s tattoo-studio framing necessarily was.
- Named a "the framing that governs this whole document" section up front, explicitly contrasting pulsewatch's continuous-operation correctness model against every other flagship's request/response model, and carried that framing through every subsequent section rather than defaulting into a CRUD-feature-list treatment.
- Added a new **Continuous operation requirements** section decomposing "runs continuously and stays correct" into four concrete, individually verifiable properties: (1) restart mid-check-cycle must not corrupt scheduling truth, (2) a check must never overlap its own still-running previous run, (3) retention/rollup over weeks of data, (4) SLO/alerting correctness as a rolling-window property rather than a single-check property (hysteresis, excluding pulsewatch's own downtime from target SLO math, restart-safe alert de-duplication).
- Made the deferred **TimescaleDB vs. plain PostgreSQL** decision, with real reasoning tied directly to property (3) above: plain PostgreSQL with a hand-rolled Go rollup/retention job, because (a) TimescaleDB would be a third new technology and break the learning budget frozen at exactly two in `00a-ledger-confirmation.md` (Rule D3), (b) the actual measured-scale data volume (~10 targets, 30–60s intervals, ~10M rows/year worst case) doesn't need it, (c) hand-rolling the rollup job is more aligned with this repo's own Operations & Maintenance deep phase and Go concurrency learning objective than delegating it to an extension would be. Recorded as a real decision, explicitly flagged for Session 3 to formalize as ADR-0001 if it wants to, since `00a-ledger-confirmation.md` records that formal ADRs begin at Session 3, not this one.
- Wrote `docs/project-memory/00b-build-vs-alternatives.md` — a genuine options-considered comparison (hosted uptime SaaS / self-hosted Grafana+Prometheus+Alertmanager as-is / purpose-built pulsewatch), matching the rigor of `lexicon/docs/project-memory/00b-rag-vs-alternatives.md`. Stated the real trade-off honestly: for the narrow "alert on a public URL going down" goal, a hosted SaaS is genuinely less effort and probably more reliable on day one — the justification for building pulsewatch instead is reachability of private/internal targets a SaaS structurally cannot reach, honoring (not exceeding) the learning budget already frozen in Session 0, and real dogfooding value that adopting either alternative as-is cannot produce by construction. Also gave the honest counter-case for Option B (Grafana+Prometheus is likely the *right* call at real multi-team scale — rejected for this deployment's scale and learning-budget constraints, not rejected universally).
- Wrote `docs/project-memory/01-scope-and-non-goals.md`: an MVP boundary as a checkable bullet list (HTTP/TCP checks, per-target hysteresis, restart-safe per-target-exclusive scheduling, Postgres history + rollup/retention job, rolling-window SLO math excluding pulsewatch's own downtime, persisted incident state, webhook/email alerting, the lightweight agent, a minimal dashboard); an explicit non-goals table (8 rows, each with a reason and a reconsider-trigger) formalizing the README's Session-0 preview and adding new ones this session's analysis surfaced (a third new technology, multi-region probing, public status pages); a "deferred to backlog" list; and a "definition of v1 complete" that requires the 5 success metrics to be demonstrated against real scenarios *and* a sustained real dogfooding period (proposed 2+ weeks), not just "features implemented."
- Defined 5 success metrics in `00-project-brief.md`, each stated as what will be measured (not a fabricated number), specific to what a continuously-operated system needs to prove: bounded real-outage detection time, no alert on a sub-threshold transient blip, no lost state / no double-alert across a server restart mid-incident, correct (not just non-crashing) rollup/retention, and per-target concurrent-check-safety under a slow target.
- Updated `docs/SDLC-EVIDENCE.md`'s Phase 1 (Discovery & Planning) row with real evidence links into the three files above, keeping the depth marker `light` (per this repo's own ledger declaration — Phases 6/7 are the deep ones here, not this one).

## Files created or changed
- `docs/project-memory/00-project-brief.md` — rewritten in full; no draft markers remain.
- `docs/project-memory/00b-build-vs-alternatives.md` — new; the build-vs-SaaS-vs-Grafana/Prometheus comparison.
- `docs/project-memory/01-scope-and-non-goals.md` — written in full; MVP boundary, non-goals table, backlog, definition of v1 complete.
- `docs/SDLC-EVIDENCE.md` — Phase 1 row populated with evidence links.
- `docs/project-memory/12-session-handoff.md` — this file.

## Decisions made
- **Plain PostgreSQL, not TimescaleDB**, for check-history storage and rollups — hand-rolled Go retention/rollup job instead of a database extension. Reasoning: honors the learning budget frozen at exactly two new technologies in `00a-ledger-confirmation.md`; the project's actual measured-scale data volume doesn't need TimescaleDB's value proposition; hand-rolling the job is more aligned with the Operations & Maintenance deep phase and the Go concurrency learning objective than delegating it would be. Not yet a formal ADR (ADRs begin Session 3 per `00a-ledger-confirmation.md`); Session 3 should reference `00-project-brief.md` §3 rather than re-derive this.
- **Build pulsewatch rather than adopt a hosted SaaS or self-hosted Grafana+Prometheus as-is** — full reasoning in `00b-build-vs-alternatives.md`. The trade-off is stated honestly: a SaaS is less effort for the narrow "alert on a public outage" goal; the decision to build is justified by reachability requirements a SaaS cannot meet, the already-frozen learning budget, and genuine dogfooding value, not asserted as the universally superior engineering choice.
- **MVP scope is checks + hysteresis + restart-safe scheduling + rollup/retention + rolling-window SLO math + webhook/email alerting + a minimal dashboard + the agent** — no status pages, no multi-region probing, no native paging-escalation product at v1 (`01-scope-and-non-goals.md`).
- **"v1 complete" requires a sustained real dogfooding period**, not just feature completion — because the actual point of this repository is genuine continuous operation against this developer's own infrastructure, and that can only be demonstrated by actually running it, not by a one-time demo.

## Validation performed
- Cross-checked every new claim in `00-project-brief.md` and `00b-build-vs-alternatives.md` against `00a-ledger-confirmation.md` (frozen record) to confirm nothing here silently reopens the frozen framework allocation, the two-technology learning budget, or the two-deep-SDLC-phase commitment — none were touched; the TimescaleDB rejection and the Grafana/Prometheus rejection are both derived *from* the frozen budget, not in tension with it.
- Read `lexicon/docs/project-memory/00b-rag-vs-alternatives.md` directly (cross-repo, read-only) before writing `00b-build-vs-alternatives.md`, to match its structure (Context / Options considered — for/against/verdict / Decision / Trade-offs accepted / Consequences / Revisit triggers) and its standard of honesty about the rejected options' real strengths, rather than writing a one-sided justification.
- Confirmed no implementation code, dependency, or config file was touched this session — this session is discovery/planning documentation only, per the ground rules; `privacy-forge`, `laravel-consent-guard`, `bookslot`, and `lexicon` were not modified (read-only reference to `lexicon`'s file only).
- Re-read the finished `00-project-brief.md`, `00b-build-vs-alternatives.md`, and `01-scope-and-non-goals.md` end to end to confirm no "draft"/"TBD"/stub language remains and every section is framed around continuous operation rather than a single request/response cycle.

## Open questions and risks
- **Open question:** exact check-interval and consecutive-failure-threshold defaults are not fixed yet — deliberately left for Requirements Analysis (Session 2) / Architecture (Session 3) rather than invented here without a real basis.
- **Risk (carried forward):** Go concurrency patterns and OTel collector pipelines are both genuinely new; timebox a learning spike before Session 3 architecture work if either proves a bigger lift than expected.
- **Risk (carried forward, now mitigated by this session's framing but not yet by any code):** the actual restart-safety and overlapping-run-prevention properties named in `00-project-brief.md` §3 still have to be designed correctly in Architecture (Session 3+) and then actually verified — naming them in Discovery reduces the risk of them being skipped, but doesn't yet prove they'll be built correctly.
- **No blockers.** Session 2 (Requirements Analysis) can start immediately.

## Next recommended session
- Proposed session title: **Session 2 — Requirements Analysis**
- Single objective: Turn the MVP boundary and continuous-operation requirements from this session into concrete functional and non-functional requirements (`02-requirements.md`) — including explicit, testable requirements for the four continuous-operation properties named in `00-project-brief.md` §3 (restart safety, overlapping-run prevention, retention/rollup, rolling-window SLO correctness), since Requirements Analysis is intentionally a baseline (not deep) phase here and must not silently become the place where those properties get re-litigated instead of formalized.
- Inputs required: this handoff; `00-project-brief.md`; `00b-build-vs-alternatives.md`; `01-scope-and-non-goals.md`.
- Expected deliverables: `02-requirements.md` with functional requirements (per MVP boundary item) and non-functional requirements (per continuous-operation property), each testable.
- Definition of done: every MVP boundary checklist item and every continuous-operation property has at least one corresponding requirement; no requirement introduces scope beyond `01-scope-and-non-goals.md`'s boundary without flagging it back to this session's scope decision first.

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Repository state:** branch `main`, unreleased (pre-v0.1.0), Session 1 complete

**Problem being solved:** This developer runs several self-hosted flagship deployments (`privacy-forge`, `laravel-consent-guard`, `bookslot`, `lexicon`) with no continuously running, honest answer to "is it actually up right now, and did I meet my SLO" — and no mechanism that tells them the moment it isn't. This is real dogfooding of this developer's own infrastructure, not a hypothetical buyer persona. Unlike this portfolio's other flagships, correctness here is about the system continuing to tell the truth over hours and days — surviving its own restarts, never double-alerting, never missing a real outage — not a single request's output.

**Users:** Primary and effectively only user — this developer, operating pulsewatch against their own bounded set of real deployments (~5–10 targets). "On-call" is the same person in an interrupt-driven mode (webhook/email), not a separate stakeholder.

**Current stack:**
- Frontend: SvelteKit
- Backend: Go 1.25 (Gin)
- Data: PostgreSQL (plain — TimescaleDB explicitly rejected this session, see Decisions made), Redis
- Infra: Docker Compose, GitHub Actions, OpenTelemetry Collector (planned)
- Testing: Go's `testing` package, Vitest

**Architecture decisions that must not be reversed:**
- Licence is AGPL-3.0 (hostable app, not a library).
- Primary frontend/backend framework pair is fixed (Go + Gin, SvelteKit) — frozen against the portfolio-wide framework allocation ledger.
- Exactly two deep SDLC phases: Release & Deployment, Operations & Maintenance.
- Exactly two new technologies for the learning budget: Go concurrency patterns, OpenTelemetry collector pipelines — **plain PostgreSQL, not TimescaleDB, is now an explicit Session-1 decision derived from this cap**, not an oversight to revisit casually.

**Implementation state:**
- Done: repository skeleton, licence, governance docs, minimal real Go/Gin backend and SvelteKit frontend with passing tests and clean lint, real CI, working `docker compose up`, and now — finalised project brief, build-vs-alternatives comparison, and scope/non-goals document (Session 1).
- In progress: nothing mid-flight.
- Not started: everything product-related — no monitoring logic, agent code, scheduler, or alerting exists yet; that begins at Requirements/Architecture (Sessions 2–3).

**Constraints and non-goals:**
- Max 2 new technologies for this repo — already at cap (Go concurrency, OTel pipelines); TimescaleDB, Prometheus/Grafana, and any other third technology are explicit non-goals for this reason (`01-scope-and-non-goals.md`).
- Full non-goals table (8 rows, with reconsider-triggers) is in `01-scope-and-non-goals.md`: no APM/tracing of user apps, no log aggregation product, no on-call paging/escalation, no synthetic browser monitoring, no multi-tenant SaaS billing, no third new technology, no multi-region probing, no public status pages at v1.

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning (this session — deep in `lexicon`) and Verification & Testing (deep in `lexicon`), Requirements Analysis and Retirement & Handover (deep in `privacy-forge`)

**Task for this session (single objective) — now complete:**
Conduct project discovery: validate the draft problem statement, users, and assumptions; identify real risks and success metrics; define the MVP boundary and non-goals — explicitly reasoning about this as a continuously-operated system, not a request/response product. **Done — see Work completed above.**

**Definition of done — met:**
- `00-project-brief.md` rewritten with no "draft" markers, every section validated with actual reasoning.
- `01-scope-and-non-goals.md` produced with an explicit non-goals table (reason for exclusion + reconsider trigger).
- 5 concrete, checkable success metrics defined, specific to continuous operation.
- MVP boundary stated as a bullet list a reviewer could tick off.

**Files to attach or paste for Session 2:**
- `docs/project-memory/00-project-brief.md`
- `docs/project-memory/00b-build-vs-alternatives.md`
- `docs/project-memory/01-scope-and-non-goals.md`
- `docs/project-memory/12-session-handoff.md` (this file)

**Ground rules:** Do not change the stack. Do not introduce a third new technology (TimescaleDB now explicitly excluded, not just undecided). Do not expand the deep-SDLC-phase count beyond two. Session 2 (Requirements Analysis) is intentionally baseline-depth — formalize requirements from this session's work, don't silently re-litigate the discovery decisions already made here.
