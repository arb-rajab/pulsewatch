# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed
- Session number and title: **Session 2 — Requirements Analysis**
- Objective: Turn the MVP boundary and continuous-operation properties named in Session 1 into concrete, testable functional and non-functional requirements (`02-requirements.md`).
- Status: **complete**

## Work completed
- Filled in the existing `02-requirements.md` template in full: roles/permissions matrix, 10 user stories with Given/When/Then acceptance criteria, 23 functional requirements, 12 non-functional requirements with numeric targets, data classification, integration requirements, and constraints.
- Wrote 10 user stories (US-001–US-010) covering every core continuous-operation flow named for this session: registering an HTTP(S) check (US-001) and a TCP check (US-002) — the two check types this repo's MVP boundary actually commits to per `01-scope-and-non-goals.md` (ICMP/DNS/cert-expiry are backlog, not MVP); a check running on schedule and recording a result (US-003); rolling-window SLO evaluation including the pulsewatch-downtime "unknown" exclusion (US-004); alert firing on a real threshold breach (US-005) and NOT firing on a sub-threshold blip (US-006); both restart-safety properties from Session 1 as explicit, testable stories rather than design notes — no lost in-flight scheduling state (US-007) and no double-alerting (US-008); dashboard viewing (US-009); and private-target monitoring via the agent (US-010).
- Wrote functional requirements (FR-001–FR-023) with one row per MVP boundary checklist item from `01-scope-and-non-goals.md`, each mapped to the user story or success metric that verifies it — confirmed every MVP boundary checklist item and every continuous-operation property from Session 1 has at least one corresponding FR, per this session's definition of done.
- Wrote non-functional requirements (NFR-001–NFR-012) with real numeric targets specific to continuous operation, not generic request-latency numbers: check-interval accuracy (±2s jitter bound), alert latency budget (≤5s from threshold breach to dispatch attempt), restart-recovery time (≤10s for ≤100 targets), restart correctness (0 duplicate alerts), rollup-job timing and retention correctness, concurrency safety (0 overlapping checks under a slow-target test), agent staleness threshold, and dashboard read performance (reads rollups, not raw history). Also proposed the three numeric defaults Session 1 explicitly deferred to this session — default check interval (30s), default consecutive-failure threshold (3), default rollup retention (400 days) — each with stated reasoning tied to the brief's measured-scale assumptions, flagged as adjustable in Architecture (Session 3), not re-litigating Session 1.
- Wrote a roles/permissions baseline sized to this repo's actual v1 scope: a single human **Operator** role (no ABAC, no privacy-forge-style multi-role matrix) plus a machine **Agent** role (its own service credential, scoped to reporting only its assigned targets) — explicitly noted that no viewer/status-page role exists at v1, since both public status pages and multi-user role separation are non-goals/backlog items per `01-scope-and-non-goals.md`, not omissions.
- Wrote data classification covering the four categories this repo actually has sensitive data in: target host/URL/port configs (Confidential — reveals private infrastructure topology), alert-channel credentials (Secret — webhook URLs/email-SMS provider credentials, must be encrypted at rest and write-only after creation, formalized as FR-023), check results (confirmed low-sensitivity, with one flagged caveat for Session 3 about body-match fragment capture potentially leaking monitored-target response content), and agent/operator authentication credentials (Secret). Marked "Lawful basis" `N/A` explicitly on every row rather than leaving it blank or inventing GDPR relevance this repo doesn't have — pulsewatch processes no personal data about third parties.
- Made the private-target-reachability requirement concrete rather than leaving it as Discovery-phase justification: FR-017/018/019 and US-010 require the agent to execute checks from a location that has real network reachability, require agent→server communication to be agent-initiated/outbound-only (so monitoring a private target never requires opening an inbound port), and require the server to distinguish "agent unreachable" from "target down" (mirroring the pulsewatch-restart "unknown" SLO carve-out) via NFR-008. Deliberately left *how* an agent learns its assigned target list (push vs. poll vs. static config) open for Architecture — Requirements states the property the mechanism must satisfy, not the mechanism itself.
- Updated `docs/SDLC-EVIDENCE.md`'s Phase 2 (Requirements Analysis) row with real evidence links, keeping the depth marker `light` per this repo's own ledger declaration.

## Files created or changed
- `docs/project-memory/02-requirements.md` — filled in from template to a complete, testable requirements document.
- `docs/SDLC-EVIDENCE.md` — Phase 2 row populated with evidence links.
- `docs/project-memory/12-session-handoff.md` — this file.

## Decisions made
- **Proposed numeric defaults for check interval (30s), consecutive-failure threshold (3), and rollup retention (400 days)** — see `02-requirements.md` NFR-010/011/012. These are Session 2's answer to the open question Session 1 explicitly deferred ("exact check-interval and consecutive-failure-threshold defaults are not fixed yet"). Reasoning: 30s matches the brief's stated measured-scale assumption (30–60s interval); a threshold of 3 absorbs a single dropped check (US-006) while still bounding real-outage detection to (3 × interval) + the alert-latency budget (SM1); 400 days of rollup retention keeps a year-over-year comparison alive even if a month is skipped. Flagged as adjustable in Architecture (Session 3) against real implementation constraints, not frozen dogma — but Session 3 should treat "no numeric target existed" as resolved, not reopen the question from scratch.
- **No viewer/status-page role at v1** — confirmed against `01-scope-and-non-goals.md` rather than invented: public status pages are an explicit non-goal, and multi-user role separation is explicitly backlog, so the roles matrix is deliberately just Operator + machine Agent, not a placeholder for a future ABAC system.
- **Alert-channel credentials must be encrypted at rest and write-only after creation** (FR-023) — a new requirement this session's data-classification pass surfaced (webhook URLs commonly embed bearer-equivalent tokens, e.g. Slack incoming webhooks), not present in Session 1's scope documents. This is a small, necessary security requirement for MVP, not scope creep — flagged here so Session 3/Architecture and the eventual security threat model (`06-security-threat-model.md`) treat it as already-decided rather than re-deriving it.

## Validation performed
- Cross-checked every FR against `01-scope-and-non-goals.md`'s MVP boundary checklist — confirmed every checklist bullet has at least one corresponding FR, and no FR introduces a check type, feature, or scope item absent from that checklist (no ICMP/DNS/cert-expiry check, no multi-region probing, no paging-escalation product, no public status page).
- Cross-checked every user story's restart-safety and rolling-window-SLO acceptance criteria directly against `00-project-brief.md` §3's four continuous-operation properties and the corresponding success metrics — confirmed US-003/US-007 cover property 1 (restart mid-cycle) and SM3, US-003/NFR-007 cover property 2 (no overlapping runs) and SM5, US-004/FR-008-011 cover property 3 (retention/rollup) and SM4, and US-004/US-005/US-006/US-008 cover property 4 (rolling-window SLO correctness, hysteresis, restart-safe de-duplication) and SM1/SM2/SM3.
- Re-read the finished `02-requirements.md` end to end to confirm no requirement is stated only as a design note (every restart-safety and rolling-window property is a Given/When/Then acceptance criterion or a numbered FR/NFR, not prose), and that the roles/data-classification sections don't import privacy-forge's ABAC/GDPR-specific complexity into a repo that doesn't need it.
- Confirmed no implementation code, dependency, or config file was touched this session — Requirements Analysis only, per the ground rules; `privacy-forge`, `laravel-consent-guard`, `bookslot`, and `lexicon` were not modified (read-only reference to `privacy-forge`'s `02-requirements.md` for structural calibration only — to see what ceremony to *not* replicate, not to copy its content).

## Open questions and risks
- **Open question (for Architecture, Session 3):** the exact mechanism by which an agent learns its assigned target list (server push, agent poll, or static config file) is deliberately left open in `02-requirements.md`'s integration requirements section — Requirements states the property (agent-initiated, outbound-only reporting) the mechanism must satisfy, not the mechanism itself.
- **Open question (for Architecture, Session 3):** the exact cap on a response-body-match fragment's stored length (data classification caveat on check results) — flagged, not sized, this session.
- **Risk (carried forward from Session 1, unchanged):** Go concurrency patterns and OTel collector pipelines are both genuinely new; timebox a learning spike before Session 3 architecture work if either proves a bigger lift than expected.
- **Risk (narrowed by this session, not yet resolved):** this session committed to real numeric NFR targets (±2s scheduling jitter, ≤5s alert latency, ≤10s restart recovery) that Architecture must actually design the scheduler and alerting pipeline to meet — naming them precisely here makes them checkable in Verification & Testing later, but doesn't yet prove they're achievable with the chosen concurrency design.
- **No blockers.** Session 3 (Architecture & Design) can start immediately.

## Next recommended session
- Proposed session title: **Session 3 — Architecture & Design**
- Single objective: Design the system architecture (scheduler, worker-pool concurrency model, agent↔server protocol over the OTel pipeline, data model, API contracts) that satisfies every FR/NFR in `02-requirements.md` — this is a baseline-depth phase per this repo's ledger, but the first session where formal ADRs begin (per `00a-ledger-confirmation.md`), starting with formalizing the plain-Postgres-vs-TimescaleDB decision from Session 1 as ADR-0001 if desired.
- Inputs required: this handoff; `00-project-brief.md`; `00b-build-vs-alternatives.md`; `01-scope-and-non-goals.md`; `02-requirements.md`.
- Expected deliverables: `03-architecture.md`, `04-data-model.md`, `05-api-contracts.md`, and any ADRs the session produces.
- Definition of done: every FR/NFR in `02-requirements.md` is addressed by a concrete architectural decision (not left implicit); the two open questions above (agent target-assignment mechanism, body-match fragment cap) are resolved or explicitly deferred with reasoning; no requirement is silently dropped or expanded.

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Repository state:** branch `main`, unreleased (pre-v0.1.0), Sessions 1–2 complete

**Problem being solved:** This developer maintains several self-hosted flagship repositories (`privacy-forge`, `laravel-consent-guard`, `bookslot`, `lexicon`), none of which currently has a real, live deployment, plus pulsewatch's own future instance. There is no continuously running, honest answer to "is it actually up right now, and did I meet my SLO" for whatever of this does run — and no mechanism that tells this developer the moment it isn't. Correctness here is about the system continuing to tell the truth over hours and days — surviving its own restarts, never double-alerting, never missing a real outage — not a single request's output. See `00-project-brief.md` for full framing.

**Users:** Primary and effectively only user — this developer, operating pulsewatch against whatever real deployments actually exist. v1 is single-operator; the only other identity in the system is the machine "Agent" role (`02-requirements.md`).

**Current stack:**
- Frontend: SvelteKit
- Backend: Go 1.25 (Gin)
- Data: PostgreSQL (plain — TimescaleDB explicitly rejected in Session 1), Redis
- Infra: Docker Compose, GitHub Actions, OpenTelemetry Collector (planned)
- Testing: Go's `testing` package, Vitest

**Architecture decisions that must not be reversed:**
- Licence is AGPL-3.0 (hostable app, not a library).
- Primary frontend/backend framework pair is fixed (Go + Gin, SvelteKit) — frozen against the portfolio-wide framework allocation ledger.
- Exactly two deep SDLC phases: Release & Deployment, Operations & Maintenance.
- Exactly two new technologies for the learning budget: Go concurrency patterns, OpenTelemetry collector pipelines — plain PostgreSQL, not TimescaleDB, is an explicit Session-1 decision derived from this cap.
- **New this session:** proposed numeric defaults (30s check interval, 3-failure threshold, 400-day rollup retention) and the full NFR numeric target set in `02-requirements.md` — treat as resolved defaults for Architecture to design against, adjustable with reasoning, not blank slate.

**Implementation state:**
- Done: repository skeleton, licence, governance docs, minimal real Go/Gin backend and SvelteKit frontend with passing tests and clean lint, real CI, working `docker compose up`, finalised project brief/build-vs-alternatives/scope-and-non-goals (Session 1), and now a complete requirements document — user stories, FRs, NFRs, roles, data classification, integration requirements (Session 2).
- In progress: nothing mid-flight.
- Not started: everything product-related — no monitoring logic, agent code, scheduler, or alerting exists yet; architecture design begins at Session 3.

**Constraints and non-goals:**
- Max 2 new technologies for this repo — already at cap (Go concurrency, OTel pipelines); TimescaleDB, Prometheus/Grafana, and any other third technology are explicit non-goals.
- Full non-goals table (8 rows, with reconsider-triggers) is in `01-scope-and-non-goals.md`.
- v1 is single-operator; no multi-user auth/role separation or public status page exists at v1 (`02-requirements.md` roles matrix confirms this against the non-goals table).

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning (deep in `lexicon`), Requirements Analysis (this session — deep in `privacy-forge`), Verification & Testing (deep in `lexicon`), Retirement & Handover (deep in `privacy-forge`)

**Task for this session (single objective) — now complete:**
Turn the MVP boundary and continuous-operation properties from Session 1 into concrete functional and non-functional requirements. **Done — see Work completed above.**

**Definition of done — met:**
- User stories and acceptance criteria are concrete and testable, covering restart-safety and rolling-window-SLO properties as real Given/When/Then criteria, not narrative.
- NFRs have real numeric targets specific to continuous operation (alert latency, restart recovery, scheduling jitter, rollup timing).
- Roles/permissions and data classification are sized to this repo's actual scope, not copied at privacy-forge's complexity level.
- The private-target-reachability requirement is concrete (FR-017/018/019), not just restated justification.
- `docs/SDLC-EVIDENCE.md`'s Phase 2 row and this handoff are updated.

**Files to attach or paste for Session 3:**
- `docs/project-memory/00-project-brief.md`
- `docs/project-memory/00b-build-vs-alternatives.md`
- `docs/project-memory/01-scope-and-non-goals.md`
- `docs/project-memory/02-requirements.md`
- `docs/project-memory/12-session-handoff.md` (this file)

**Ground rules:** Do not change the stack. Do not introduce a third new technology. Do not expand the deep-SDLC-phase count beyond two. Session 3 (Architecture & Design) is baseline-depth but is where formal ADRs begin (per `00a-ledger-confirmation.md`) — design against `02-requirements.md`'s FR/NFR set, don't silently drop or expand it.
