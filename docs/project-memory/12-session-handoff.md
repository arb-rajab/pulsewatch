# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed
- Session number and title: **API Contracts** — a short intermediate
  session between Architecture (Session 3) and Session 4, exactly as
  Session 3's own handoff proposed ("optionally preceded by a short
  API-contracts pass").
- Objective: Write `05-api-contracts.md`, the piece Session 3 explicitly
  deferred — a real, validated API contract for both the operator-facing
  REST surface and the agent-facing surface (REST assignment poll + OTLP
  result ingestion), matching every shape the four ADRs and
  `04-data-model.md` already committed to, with no redesign of any of
  them.
- Status: **complete**

## Work completed
- Wrote a real OpenAPI 3.1 spec, `docs/architecture/openapi.yaml` — 22
  operations across operator auth, target CRUD, live status, rolling-window
  SLO, alert channels, agent registration/credential lifecycle, the
  agent assignment poll, and OTLP log ingestion. Validated with
  `@redocly/cli lint` (see Validation performed) to zero warnings/errors.
- Wrote `docs/project-memory/05-api-contracts.md` — the prose rationale
  paired with that spec, matching `lexicon`'s own `05-api-contracts.md`
  convention (a validated spec plus a decisions-and-why document) and
  `privacy-forge`'s Session 3 practice of validating with a real tool.
- **Operator auth**: a stateless, HMAC-signed session cookie (no
  server-side session store, no new Redis dependency) — reasoned against
  a session table and against Redis-backed sessions, both rejected as
  unneeded weight for a single-operator baseline.
- **Agent auth**: a distinct bearer-token credential space
  (`agents.credential_hash`), provisioned and rotated with the exact same
  write-once/never-read-back structural pattern FR-023 already requires
  for alert-channel credentials — the response schema that carries a
  plaintext token (`AgentCreated`, `AgentCredentialRotated`) is never the
  schema any `GET` endpoint uses.
- **FR-023 made structural, not conventional**: `AlertChannel` (every read
  path) has no `destination` property in its schema at all — there is no
  field for a value to leak into. The only write path for the secret
  (`PUT .../secret`) returns `204` with no body — no response shape in
  that direction exists either. The same pattern applies to the agent
  credential.
- **ADR-0002 states + ADR-0003 staleness overlay surfaced precisely**:
  `GET /targets/{id}/status` returns both `raw_state` (the literal
  `target_schedule.state` — never `unknown`, per ADR-0002) and
  `display_state` (the read-time `unknown` overlay when an assigned
  agent is stale, per ADR-0002 Consequences/ADR-0003's evaluation-ordering
  rule). Documented explicitly why this endpoint can only ever show the
  agent-staleness cause of "unknown," never the pulsewatch-downtime cause
  (FR-011) — that cause is structurally retrospective, since the API
  can't be queried while the server serving it is down; it only surfaces
  in the `/slo` endpoint's rollup-derived `unknown_count`.
- **Idempotency defined for all four cases this session's DoD named**:
  retried check/agent/alert-channel registration (`Idempotency-Key`
  header, cached-response replay); duplicate agent-result submission over
  OTLP (the log record's own `timeUnixNano` *is* `checked_at`, doubling as
  the dedup key with `target_id` — and the dedup check must gate entry to
  ADR-0002's transition function, not just the final insert, since a
  naive re-run would double-increment `streak` even though the
  conditional incident write would still prevent a duplicate dispatch);
  agent-credential rotation deliberately left *not* idempotency-guarded,
  with the reasoning for that asymmetry stated explicitly.
- **One real, small, flagged consequence for `04-data-model.md`** (not one
  of the four ADRs, and not silently redesigned): `check_results` needs a
  `UNIQUE (target_id, checked_at)` constraint to back the OTLP-duplicate
  idempotency guarantee above — currently specified there only as a plain
  index. Named as required follow-through for Session 4, exactly the way
  ADR-0003 itself named `05-api-contracts.md` as required follow-through
  without writing it in the same session.
- **No gap found in any of the four ADRs.** Confirmed by design: every
  API-layer decision this session made was a wire-level concretization of
  something ADR-0001–0004 or `04-data-model.md` already committed to, not
  evidence of a missing decision at the architecture level.
- Updated `docs/SDLC-EVIDENCE.md`'s Phase 3 row with the API-contracts
  evidence (spec path, validation tool/result, and the FR-023/ADR-0002/
  ADR-0003/idempotency properties it encodes).

## Files created or changed
- `docs/architecture/openapi.yaml` — new.
- `docs/project-memory/05-api-contracts.md` — filled in from the empty
  template Session 3 left behind.
- `docs/SDLC-EVIDENCE.md` — Phase 3 row extended with the API-contracts
  evidence (row's `baseline` depth marker unchanged).
- `docs/project-memory/12-session-handoff.md` — this file.

## Decisions made
- **Session-cookie, stateless-HMAC operator auth; distinct bearer-token
  agent auth** — the two credential spaces stay disjoint, mirroring
  `privacy-forge`'s connector-auth/staff-auth separation, and are the only
  authorization boundary this system has (no ABAC, matching the
  single-operator roles matrix's own baseline).
- **`403 Forbidden` is not part of the error model.** Because the two auth
  schemes are structurally different shapes (cookie vs. bearer), not two
  privilege levels checked against one credential, a cross-role request
  fails at `401` before any authorization check could produce a `403` —
  confirmed as a real property of the design, not an oversight.
- **`slo_target_pct` is a request-time query parameter, not new persisted
  state.** `04-data-model.md` has no stored per-target SLO-target
  percentage; rather than silently adding one, "error budget" is defined
  as a lens computed over existing `check_rollups_hourly` data at read
  time — resolves a real gap in what "error budget" could mean without
  touching the frozen data model.
- **The OTLP ingestion endpoint (`/v1/logs`) is deliberately outside the
  `/api/v1` prefix** — it's the OTel Collector's standard receiver path
  convention (ADR-0003), not a resource of this product's own REST API;
  versioning it under `/api/v1` would misrepresent what it is.
- **`@redocly/cli lint` used in place of `openapi-spec-validator`** — this
  environment has no working Python 3 interpreter (only the Windows Store
  App-execution-alias stub); Redocly's CLI is the equivalent, actively
  maintained OpenAPI-3.1-aware linter, run the same way (a real tool
  against the real file, output pasted as evidence), not a lesser
  substitute.

## Validation performed
- `npx --yes @redocly/cli lint docs/architecture/openapi.yaml` →
  zero warnings, zero errors, on the tool's `recommended` (strictest
  built-in) ruleset. Full command/output is pasted in
  `05-api-contracts.md`'s Validation section. Reached that state
  iteratively — an initial pass had 27 warnings (missing `operationId`s,
  missing `info.license.url`, an unused `Forbidden` response component,
  and a `then`/`else` conditional-schema pattern the linter couldn't
  resolve without redundant local `properties`) — all fixed in the
  committed file, none suppressed via linter config.
- Cross-checked every endpoint against the specific FR/US/ADR it serves
  (cited inline in `05-api-contracts.md`'s endpoint tables) — confirmed
  FR-001–004/US-001/US-002 (target CRUD), FR-010/FR-011/FR-019/US-004/
  US-009/US-010/NFR-009 (status + SLO endpoints), FR-013/FR-014/FR-023
  (alert channels), and ADR-0003's two decisions (push direction, REST-poll
  assignment discovery) each have a concrete endpoint, not just a
  paragraph gesturing at one.
- Confirmed the credential write-once/never-read-back property is
  structural by re-reading `openapi.yaml`'s own schemas: `grep`-checked
  that `destination` and `token` properties only ever appear in
  create/rotate-only schemas (`AlertChannelCreateRequest`,
  `AlertChannelSecretRotateRequest`, `AgentCreated`,
  `AgentCredentialRotated`), never in `AlertChannel` or `Agent` — the
  schemas every `GET` endpoint actually returns.
- Confirmed no ADR was reopened: re-read all four ADRs' Decision and
  Consequences sections against every endpoint this session added, and
  found each contract-layer decision traces to something already fixed
  there (or in `04-data-model.md`) rather than a new architectural choice
  made silently at the API layer.
- Confirmed `privacy-forge`, `laravel-consent-guard`, `bookslot`, and
  `lexicon` were not modified — `lexicon/docs/project-memory/
  05-api-contracts.md` was read for style/convention calibration only (its
  reusable-response and error-envelope pattern), the same read-only-
  reference discipline Session 3 applied to `lexicon`/`privacy-forge`'s
  ADR rigor.

## Open questions and risks
- **Resolved this session:** the two shapes ADR-0003 named but deferred
  (the agent-assignment REST endpoint, the OTLP-forwarded ingestion
  endpoint) are both now fully specified at the wire level in
  `openapi.yaml`.
- **New, flagged for Session 4 (not a blocker for this session's own DoD):**
  `04-data-model.md` needs one additive constraint —
  `UNIQUE (target_id, checked_at)` on `check_results` — to back the OTLP
  duplicate-result idempotency guarantee this contract defines. A schema
  migration detail for whoever writes the real Go migrations, not a
  design gap.
- **New, flagged for Session 4:** the OTel Collector's exporter-pipeline
  config must preserve the agent's `Authorization` bearer header
  end-to-end so `/v1/logs` can authenticate the forwarded request with the
  same scheme `/agent/assignments` uses — already named as required future
  work in ADR-0003 Consequences; this session made the concrete
  requirement explicit at the endpoint level.
- **Named, accepted trade-off, not a defect:** operator session revocation
  is by server-secret rotation (invalidates every session at once), not
  per-session — a real limitation, acceptable for v1's single-operator,
  self-hosted deployment model. Revisit only if this project's actual
  threat model ever changes (it's frozen as single-operator per
  `01-scope-and-non-goals.md`, so not expected to).
- **No blockers.** Session 4 can start directly against `openapi.yaml` and
  `05-api-contracts.md` as the concrete wire-level spec for every endpoint
  it needs to implement.

## Next recommended session
- Proposed session title: **Session 4 — Environment, Repository Setup,
  Standards, and CI Baseline.** This repo's Session 0 already shipped a
  real, working CI pipeline and a minimal real Go/Gin + SvelteKit skeleton
  (`12-session-handoff.md`'s own prior "Implementation state" note) — so
  this session is about the **dev-environment / `docker-compose.yml`
  baseline and coding standards**, not rebuilding CI from scratch. Concrete
  scope this session should cover, given everything decided so far:
  - Add the OTel Collector service to `docker-compose.yml` and wire a
    real receiver/exporter pipeline config (named as required in ADR-0003
    Consequences, still not done — this is the natural session for it,
    since the ingestion endpoint it forwards to is now fully specified in
    `openapi.yaml`).
  - Establish Go and SvelteKit coding-standards docs/lint config
    (`golangci-lint`, ESLint/Prettier or this repo's existing equivalents)
    consistent with whatever Session 0 already set up — this session
    should audit what already exists before adding anything, not assume a
    blank slate.
  - Decide and document the migration tool
    (`golang-migrate` vs. `goose`, explicitly left open by
    `04-data-model.md`'s Migration approach section) and write the first
    real migration set: `targets`, `target_schedule`, `check_results`
    (partitioned), `check_rollups_hourly`, `incidents`, `alert_channels`,
    `alert_dispatches`, `agents`, `operators` — including the
    `UNIQUE (target_id, checked_at)` addition this session flagged.
  - Confirm the existing CI pipeline runs against any new services this
    session adds to `docker-compose.yml` (the OTel Collector especially),
    rather than silently drifting out of CI's coverage.
- Inputs required: this handoff; `02-requirements.md`; `docs/adr/ADR-0001`
  through `ADR-0004`; `03-architecture.md`; `04-data-model.md`;
  `05-api-contracts.md`; `docs/architecture/openapi.yaml`.
- Expected deliverables: an updated `docker-compose.yml` (OTel Collector
  wired in); coding-standards documentation and lint config; the first
  real migration set; confirmation (not just an assumption) that CI
  exercises all of it.
- Definition of done: `docker compose up` brings up a working Collector
  pipeline alongside the existing services; migrations apply cleanly and
  match `04-data-model.md` (plus this session's one flagged addition)
  exactly; coding-standards docs exist and lint runs clean in CI.

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Repository state:** branch `main`, unreleased (pre-v0.1.0). Architecture
(Session 3) and the API-contracts intermediate session are both complete.

**Problem being solved:** This developer maintains several self-hosted
flagship repositories (`privacy-forge`, `laravel-consent-guard`,
`bookslot`, `lexicon`), none of which currently has a real, live
deployment, plus pulsewatch's own future instance. There is no
continuously running, honest answer to "is it actually up right now, and
did I meet my SLO" for whatever of this does run. See `00-project-brief.md`
for full framing.

**Users:** Single operator (this developer) plus the machine "Agent" role — v1 has no other identity (`02-requirements.md`).

**Current stack:**
- Frontend: SvelteKit
- Backend: Go 1.25 (Gin)
- Data: PostgreSQL (plain, TimescaleDB rejected), Redis (available,
  not load-bearing for scheduling or, per this session, auth either)
- Infra: Docker Compose, GitHub Actions, OpenTelemetry Collector (designed
  in ADR-0003, its ingestion endpoint now fully specified in
  `05-api-contracts.md`/`openapi.yaml` — still not wired into
  `docker-compose.yml`, that's Session 4's job)
- Testing: Go's `testing` package, Vitest

**Architecture decisions that must not be reversed:**
- Licence AGPL-3.0; Go + Gin / SvelteKit frozen; exactly two deep SDLC
  phases (Release & Deployment, Operations & Maintenance); exactly two new
  technologies (Go concurrency, OTel pipelines).
- ADR-0001 (Postgres row leasing), ADR-0002 (3-state alert-suppression
  machine, DB-enforced exactly-once), ADR-0003 (agent-initiated push only,
  both directions), ADR-0004 (bounded worker pool, 1s tick, context-based
  graceful drain) — see `docs/adr/` and `09-decision-log.md`.
- **New this session, must not be silently reversed:** the API contract
  in `docs/architecture/openapi.yaml` — specifically its auth-scheme
  split (session cookie vs. bearer token), the structural write-once
  credential schemas, and the `(target_id, checked_at)` OTLP-idempotency
  design — is the wire-level spec Session 4's actual endpoint
  implementations must match, not a sketch to redesign in passing.

**Implementation state:**
- Done: repository skeleton, licence, governance docs, minimal real
  Go/Gin backend and SvelteKit frontend with passing tests and clean
  lint, real CI, working `docker compose up` (Session 0); finalised
  project brief/scope (Session 1); complete requirements (Session 2);
  complete, decided architecture — four ADRs, diagrams, rollup/retention
  data model (Session 3); now a validated, complete API contract for both
  the operator and agent surfaces (this session).
- In progress: nothing mid-flight.
- Not started: all actual implementation — no monitoring logic, agent
  code, scheduler, worker pool, leasing, alert state machine, migrations,
  or Collector wiring exist yet.

**Constraints and non-goals:**
- Max 2 new technologies, already at cap. v1 is single-operator; no
  multi-user auth/role separation or public status page. Full non-goals
  table in `01-scope-and-non-goals.md`.

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning, Requirements Analysis, Verification & Testing, Retirement & Handover
**Baseline-depth, real rigor on its own merits:** Architecture & Design (Session 3 + this API-contracts session) — see `00a-ledger-confirmation.md`.

**Task for this session (single objective) — now complete:**
Write `05-api-contracts.md` (paired with a real, validated OpenAPI spec)
for both the operator-facing REST API and the agent-facing REST-poll +
OTLP-ingestion surface, matching the four ADRs and `04-data-model.md`
exactly, with FR-023's write-once credential handling made structural and
idempotency defined for every retry/duplicate case those ADRs' guarantees
depend on. **Done — see Work completed above.**

**Definition of done — met:**
- `05-api-contracts.md` exists, matches the ADRs precisely, and the
  credential write-once/never-read-back property is structural (verified
  by inspecting the schemas directly, not just described in prose).
- The spec validates with a real tool — `@redocly/cli lint`, zero
  warnings/errors; command and output shown in `05-api-contracts.md`.
- `docs/SDLC-EVIDENCE.md`'s Phase 3 row and this handoff are updated,
  handing off to Session 4 (Environment, Repository Setup, Standards, and
  CI baseline).

**Files to attach or paste for Session 4:**
- `docs/project-memory/02-requirements.md`
- `docs/adr/ADR-0001-scheduler-restart-safety-leasing.md` through `ADR-0004-go-concurrency-worker-pool.md`
- `docs/project-memory/03-architecture.md`
- `docs/project-memory/04-data-model.md`
- `docs/project-memory/05-api-contracts.md`
- `docs/architecture/openapi.yaml`
- `docs/project-memory/12-session-handoff.md` (this file)

**Ground rules:** Do not change the stack. Do not introduce a third new
technology. Do not expand the deep-SDLC-phase count beyond two. Do not
reopen the TimescaleDB decision or any of the four ADRs without new
measured evidence per their own Revisit triggers. Do not redesign
`openapi.yaml`'s auth/credential/idempotency shapes in passing while
implementing Session 4 — implement to match them, and if real
implementation work reveals one is genuinely wrong, report it explicitly
rather than silently drifting the code away from the documented contract.
