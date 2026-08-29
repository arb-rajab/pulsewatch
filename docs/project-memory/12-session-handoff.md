# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed
- Session number and title: **Session 4 — Environment, Repository Setup,
  Standards, and CI Baseline.**
- Objective: make the actual dev environment match the real architecture
  designed in Sessions 3–3.5 (four ADRs, `04-data-model.md`,
  `05-api-contracts.md`/`openapi.yaml`) — real migrations, the OTel
  Collector wired into `docker-compose.yml`, coding-standards/lint config
  reflecting real project conventions, and CI extended to cover it all.
  Explicitly not a rebuild of Session 0's CI pipeline (lint, vet, test,
  govulncheck, gitleaks, CodeQL), and explicitly no business-logic
  implementation (scheduler, worker pool, alert dispatch code) — that is
  Session 5+.
- Status: **complete**

## Work completed

### Migrations (`backend/migrations/`, golang-migrate, plain SQL up/down pairs)
- Nine sequential migrations implementing every table in
  `04-data-model.md`'s ERD, in FK-dependency order: `operators`, `agents`,
  `targets`, `target_schedule`, `check_results`, `check_rollups_hourly`,
  `incidents`, `alert_channels`, `alert_dispatches`.
- **ADR-0001** (leasing): `target_schedule.lease_owner` /
  `lease_expires_at`, plus the `next_due_at` index the due-set scan
  depends on.
- **ADR-0002** (alert-suppression state machine): `target_schedule.streak`
  / `state` (CHECK-constrained to `healthy`/`suspect`/`alerting`); `incidents`
  with the partial unique index `incidents_one_open_per_target ON incidents
  (target_id) WHERE closed_at IS NULL` — copied verbatim from
  `04-data-model.md`'s own Invariants section, not reworded.
- **Session-3.5-flagged addition**: `UNIQUE (target_id, checked_at)` on
  `check_results`, backing the OTLP duplicate-result idempotency guarantee
  `docs/architecture/openapi.yaml`'s `/v1/logs` endpoint defines.
- **`check_results` is genuinely partitioned** (`PARTITION BY RANGE
  (checked_at)`), matching `04-data-model.md`'s "declarative range
  partitioning by day, dropped not deleted" design — not a placeholder
  flat table. The migration bootstraps a rolling window of concrete day
  partitions (yesterday through +6 days) plus a `DEFAULT` catch-all
  partition so a fresh database is immediately usable; the Session 5+
  hourly rollup/retention background job (`04-data-model.md`) is what
  creates future day partitions in steady state, per that document's own
  design — this migration only had to make day one work, not reimplement
  the retention job.
- **One Postgres mechanic worth flagging explicitly, not a data-model
  gap**: a partitioned table's PRIMARY KEY/UNIQUE constraints must include
  the partition key. `check_results.id` (a `bigserial`, per the ERD) alone
  can't be the sole primary key on a partitioned table — the migration
  uses `PRIMARY KEY (id, checked_at)` instead. `id` stays a single,
  globally-increasing sequence, so `(id, checked_at)` remains effectively
  globally unique in practice even though Postgres only enforces the
  constraint per-partition. This changes no semantics `04-data-model.md`
  or any ADR depends on (nothing reads `check_results.id` as a sole key
  today) — noted here so it isn't mistaken for silent scope drift later.
- **Verified by actually running them, not just written**:
  - Full `up` → `down -all` → `up` round-trip against a fresh Postgres 16
    Docker container — `down -all` left only golang-migrate's own
    `schema_migrations` bookkeeping table, `up` reapplied cleanly.
  - Functional inserts into every one of the nine tables with real FK
    references (`operators` → `agents` → `targets` → `target_schedule` /
    `check_results` / `incidents` → `alert_dispatches` /
    `check_rollups_hourly`), confirmed with a real `SELECT`.
  - **ADR-0002's exactly-once guard, proven, not just present**: a second
    `INSERT INTO incidents (target_id) VALUES (...)` for a target with
    an already-open incident fails with
    `duplicate key value violates unique constraint
    "incidents_one_open_per_target"` — Postgres itself rejects it.
  - **ADR-0001's atomic claim, proven**: ADR-0001's own claim `UPDATE`
    statement, run twice in a row against the same target row, returns
    one row the first time and zero rows the second — the exact "claim
    failed (0 rows) = normal, silent skip" behavior the ADR and
    `03-architecture.md`'s sequence diagram specify.
  - **OTLP idempotency constraint, proven**: a second `check_results`
    insert with the identical `(target_id, checked_at)` pair fails with
    a duplicate-key error against
    `check_results_2026_08_29_target_id_checked_at_key` (the per-partition
    unique index), matching `openapi.yaml`'s "resubmission is a no-op"
    requirement at the schema level.
  - Also verified against the real `docker compose up --build` stack (see
    below), not only the throwaway test container.

### OTel Collector (`otel-collector-config.yaml`, `docker-compose.yml`)
- Image: `otel/opentelemetry-collector-contrib:0.159.0` — contrib, not
  core, specifically because the `headers_setter` extension (needed below)
  only ships in contrib; confirmed present via the image's own `components`
  subcommand before committing to it.
- **Scoped to exactly the two pipelines `03-architecture.md`'s "Where
  OpenTelemetry actually fits" section specifies, nothing more:**
  - `logs` pipeline (agent telemetry, ADR-0003): `otlp` receiver →
    `batch` processor → `otlphttp/pulsewatch` exporter targeting
    `http://backend:8080` (otlphttp appends the standard `/v1/logs` path
    itself, matching `openapi.yaml` exactly). The `headers_setter`
    extension (`from_context: Authorization` → `upsert` on the outgoing
    request) preserves the agent's bearer token end-to-end, the concrete
    requirement ADR-0003 Consequences and `openapi.yaml`'s `/v1/logs`
    description both name explicitly for this session. Requires
    `include_metadata: true` on the OTLP HTTP receiver so the incoming
    header is actually available in context for `headers_setter` to read.
  - `traces` / `metrics` pipelines (self-observability, ADR-0001/ADR-0002/
    ADR-0004's future instrumentation): same `otlp` receiver → `batch` →
    the `debug` exporter only. **No Grafana/Prometheus backend** — exactly
    what `03-architecture.md` says is explicitly out of scope for v1
    (would reintroduce the excluded third technology,
    `01-scope-and-non-goals.md`).
- Config validated with the collector's own `validate --config` subcommand
  (exit 0) — both manually and as a new CI job.
- **No Docker-level `healthcheck:` block for this service — a deliberate,
  verified decision, not an oversight.** Checked directly: neither the
  core nor the contrib collector image ships a shell, `wget`, or `curl`
  (confirmed by attempting to exec `sh` in each — `exec: "sh": executable
  file not found in $PATH`), so a `CMD`/`CMD-SHELL` healthcheck cannot run
  inside the container. The `health_check` extension's HTTP endpoint
  (`:13133`, mapped to host `13143`) is exposed and verified from the host
  instead — confirmed responding `{"status":"Server available",...}`
  against the real running stack.

### docker-compose.yml
- New `migrate` service (`migrate/migrate:v4.19.1`, official image):
  applies every migration in `backend/migrations/` once, on every
  `docker compose up`, then exits. `backend` now `depends_on: migrate:
  condition: service_completed_successfully` — a fresh `docker compose up`
  always starts from the real, current schema; there is no separate manual
  migration step for local dev.
- New `otel-collector` service, described above.
- Verified end-to-end against this actual repo (not a description of what
  it should do): `docker compose up --build` → `migrate` exits `0` with
  all nine migrations applied and logged → Postgres/Redis/backend/frontend
  all report `healthy` → `backend`'s `/health` and `frontend`'s `/health`
  both return `{"status":"ok"}` → the real schema (all nine tables, eight
  `check_results` day partitions, the `check_results_default` catch-all,
  golang-migrate's own `schema_migrations`) confirmed present via `psql
  \dt` against the compose-managed Postgres itself, not a separate
  container. Torn down cleanly afterward (`docker compose down -v`).

### Coding standards
- **`backend/.golangci.yml`**: added `contextcheck`, `containedctx`,
  `fatcontext`, `noctx`, and `bodyclose` — justified inline by comment,
  not just enabled silently: ADR-0004's entire graceful-shutdown design
  depends on every blocking operation actually observing context
  cancellation, and ADR-0001's lease-safety property depends on check
  execution never outliving its own context. `bodyclose` added alongside
  as the natural companion for a process making outbound HTTP checks
  continuously for days/weeks. Verified against the real (minimal)
  codebase with the pinned CI version (`golangci-lint:v2.13.2`, matching
  `.github/workflows/ci.yml`): found one genuine, real finding (`noctx` on
  `main_test.go`'s `http.NewRequest`), fixed it
  (`http.NewRequestWithContext(t.Context(), ...)`), re-ran clean
  (`0 issues`). Also re-verified `go vet ./...` and `go test ./... -race
  -v` still pass (Go 1.25, matching CI's `setup-go` version) after that
  fix.
- **Frontend formatting baseline** (Session 0 had ESLint but no
  formatter): added Prettier 3.9.6 + `eslint-config-prettier` 10.1.8 +
  `prettier-plugin-svelte` 4.1.1, `.prettierrc.json` (tabs, single quotes,
  matching the existing skeleton files' actual style so `prettier --write`
  changed nothing but `package.json`'s own indentation), wired into
  `eslint.config.js` (via `eslint-config-prettier`, to disable stylistic
  rules that would otherwise conflict) and `package.json` (`npm run lint`
  now runs `eslint . && prettier --check .`; new `npm run format` runs
  `prettier --write .`). Verified: `npm run lint` passes clean against the
  reformatted codebase.

### CI (`.github/workflows/ci.yml`) — extended, not rebuilt
- Backend job: added a `postgres:16-alpine` service container plus three
  new steps — install the `migrate` CLI (`go install -tags 'postgres'
  .../migrate/v4/cmd/migrate@v4.19.1`), then `up` → `down -all` → `up`
  against that fresh service-container Postgres. A migration that doesn't
  reverse cleanly now fails CI, not just local dev — directly answering
  this session's own "confirm CI exercises what this session adds, don't
  let it silently drift out of coverage" requirement for the schema.
- New `otel-collector-config` job: validates `otel-collector-config.yaml`
  with the collector's own `validate` subcommand in a plain `docker run`
  step — same coverage requirement, applied to the Collector config.
- Everything Session 0 already built (lint, vet, test, govulncheck,
  gitleaks, CodeQL) is untouched, not rebuilt.

### CONTRIBUTING.md and README.md
- `CONTRIBUTING.md`: real, verified setup instructions — `.env` copy step,
  what `docker compose up --build` actually brings up and in what
  dependency order, concrete health-check and schema-verification commands
  (including the OTel Collector's host-side curl check and *why* it isn't
  a Docker healthcheck), a documented manual-migration command, and a
  documented `otel-collector-config.yaml` validation command. Every
  command in this file was actually run against this repo this session,
  not written from description.
- `README.md`: status banner, project-status section, and Quickstart
  updated to match reality (Session 4 complete, real schema/OTel Collector
  boot behavior, correct host ports) instead of the stale Session-1
  placeholder text.

### docs/SDLC-EVIDENCE.md
- Phase 4 (Implementation) row filled in with this session's evidence —
  explicitly framed as environment/schema/tooling, not business-logic
  implementation (that's still Phase 4's remaining, larger piece, starting
  Session 5).

## Files created or changed
- `backend/migrations/000001_create_operators.{up,down}.sql` through
  `000009_create_alert_dispatches.{up,down}.sql` — new (18 files).
- `docker-compose.yml` — `migrate` and `otel-collector` services added;
  `backend`'s `depends_on` extended.
- `otel-collector-config.yaml` — new.
- `backend/.golangci.yml` — five linters added, each with an inline
  rationale comment.
- `backend/main_test.go` — one-line `noctx` conformance fix
  (`NewRequestWithContext`), a consequence of turning that linter on, not
  a business-logic change.
- `frontend/.prettierrc.json`, `frontend/.prettierignore` — new.
- `frontend/eslint.config.js` — `eslint-config-prettier` wired in.
- `frontend/package.json`, `frontend/package-lock.json` — Prettier
  toolchain added; `lint`/`format` scripts.
- `.github/workflows/ci.yml` — backend job: Postgres service container +
  migration up/down/up verification. New `otel-collector-config` job.
- `CONTRIBUTING.md` — real setup/verification instructions.
- `README.md` — status, project-status, Quickstart updated to current
  reality.
- `docs/SDLC-EVIDENCE.md` — Phase 4 row filled in.
- `docs/project-memory/12-session-handoff.md` — this file.

## Decisions made
- **`golang-migrate` over `goose`** — the tooling choice
  `04-data-model.md`'s "Migration approach" section deliberately left
  open. Chosen for plain-SQL up/down pairs (matching that section's
  "paired down-migration" language literally, with no Go-code migration
  layer to maintain) and a real, official Docker image
  (`migrate/migrate`) that drops cleanly into `docker-compose.yml` as a
  one-shot service — no reason to prefer `goose`'s Go-function-migration
  model here, since this schema needs no migration logic beyond SQL DDL.
- **`otel/opentelemetry-collector-contrib`, not the smaller core image** —
  the core distribution has no `headers_setter` extension (or equivalent),
  and preserving the agent's `Authorization` header end-to-end through the
  Collector's exporter pipeline is a concrete, named requirement (ADR-0003
  Consequences, `openapi.yaml`'s `/v1/logs` description), not optional
  polish. Confirmed the extension's presence directly in the contrib
  image before committing to it, rather than assuming.
- **No Docker-level healthcheck for `otel-collector`** — both collector
  images (core and contrib) ship no shell or HTTP client binary; verified
  directly rather than assumed. Health is confirmed via the `health_check`
  extension's own HTTP endpoint, exposed to the host and documented in
  `CONTRIBUTING.md`, not silently skipped.
- **Non-default host ports for the Collector** (`4327`/`4328`/`13143` →
  container `4317`/`4318`/`13133`), matching this repo's existing pattern
  of offsetting host ports (`5435`, `6382`, `8020`, `3003`) so this stack
  can run alongside this developer's other flagship repos' own compose
  stacks on the same host without port collisions.
- **`PRIMARY KEY (id, checked_at)` on `check_results`, not bare `id`** — a
  Postgres partitioning mechanic (a partitioned table's unique constraints
  must include the partition key), not a reinterpretation of
  `04-data-model.md`'s ERD. `id` remains a single global sequence, so this
  changes no semantics anything currently depends on — flagged explicitly
  above so it reads as a documented implementation detail, not silent
  schema drift.
- **Existing `main_test.go` fixed for `noctx`, not excluded/suppressed** —
  turning on a real linter and then immediately carving out an exception
  for the one file it flags would defeat the point; the fix was a genuine,
  trivial, one-line conformance change, not a design change.

## Validation performed
- Migrations: full `up` → `down -all` → `up` round-trip against a fresh
  Postgres 16 container; functional inserts into all nine tables with
  real FK chains; the ADR-0002 partial-unique-index guard proven to reject
  a second open incident; the ADR-0001 atomic claim statement proven to
  return zero rows on a second concurrent claim; the OTLP
  `(target_id, checked_at)` idempotency constraint proven to reject a
  duplicate. Re-verified against the actual `docker compose up --build`
  stack's own Postgres (`psql \dt`), not only the throwaway container.
- OTel Collector config: `otelcol-contrib validate --config=...` → exit 0,
  both manually and as a new CI job (`otel-collector-config`).
- `docker compose up --build` (real repo, real `.env` from
  `.env.example`): `migrate` exits `0`; Postgres, Redis, backend, frontend
  all report `healthy`; `otel-collector` runs (no healthcheck, by the
  documented decision above) and its `:13143` health endpoint responds
  `200` with `{"status":"Server available",...}`; `backend`'s and
  `frontend`'s `/health` both return `{"status":"ok"}`. Torn down cleanly
  with `docker compose down -v` afterward.
- Backend: `go vet ./...` (0 issues), `go test ./... -race -v` (pass, Go
  1.25 — matching CI's `setup-go` version, in a container with cgo
  available since `-race` requires it), `golangci-lint run` at the pinned
  CI version `v2.13.2` with the five new linters enabled (`0 issues` after
  the one real `noctx` fix above).
- Frontend: `npm run lint` (`eslint .` + `prettier --check .`) passes
  clean after `prettier --write .` reformatted only `package.json`'s own
  indentation — every other existing file already matched the chosen
  Prettier config, confirming the config was chosen to fit this repo's
  actual style rather than imposing a new one.
- Confirmed `privacy-forge`, `laravel-consent-guard`, `bookslot`, and
  `lexicon` were not modified, and that no other flagship's running
  containers (visible via `docker ps` on this shared host) were touched,
  started, or stopped by any command this session ran.

## Open questions and risks
- **Resolved before Session 5 starts (was flagged here as an open
  question):** the ERD's `OPERATORS ||--o{ TARGETS: "registers"` and
  `OPERATORS ||--o{ ALERT_CHANNELS: "configures"` arrows implied an
  `operator_id` column that no attribute table ever listed and no
  migration ever added. Confirmed this session that the migrations were
  correct and the arrows were the mistake: `02-requirements.md`'s roles
  matrix states v1's single-operator shape as "the actual v1 shape," and
  `01-scope-and-non-goals.md` lists multi-user auth/role separation as a
  non-goal matching the actual use case, not a deferred-but-planned
  feature — so an `operator_id` FK would be speculative schema, not a
  documented gap. `04-data-model.md`'s ERD has been corrected to remove
  both arrows; no migration was needed. Session 5 can proceed without
  this in its open-questions list.
- **No ADR reopened.** Every schema/tooling decision this session made
  (migration tool, Collector image/pipeline shape, lint rules, CI
  extension) is either explicitly named as deferred-to-this-session by an
  ADR/`04-data-model.md`/`05-api-contracts.md`, or ordinary
  infrastructure/tooling work with no architectural weight — none of it
  revisits a Decision or Consequences section in
  `docs/adr/ADR-0001`–`ADR-0004`.
- **No blockers.** Session 5 can start directly against the real schema
  (`backend/migrations/`) and the real API contract (`openapi.yaml`) —
  every table the scheduler/leasing/alerting code needs already exists,
  verified working, in a running Postgres reachable via
  `docker compose up`.

## Next recommended session
- Proposed session title: **Session 5 — Scheduler and Leasing
  (ADR-0001/ADR-0004 implementation).** Recommended starting point because
  every other real feature (alert-state machine, agent ingestion, the
  dashboard) depends on the scheduler existing first: the worker pool,
  the tick loop, and the atomic lease-claim statement this session already
  proved works correctly at the SQL level
  (`UPDATE target_schedule SET lease_owner = ... WHERE next_due_at <=
  now() AND (lease_expires_at IS NULL OR lease_expires_at < now())
  RETURNING target_id`) are the concrete Session 5 deliverable per
  ADR-0001 and ADR-0004's own Decision sections.
  - Implement the long-lived, bounded worker pool (size 20,
    operator-configurable) and 1s tick loop exactly as ADR-0004 specifies,
    including the graceful-shutdown/hard-deadline behavior.
  - Implement the lease claim/release cycle exactly as ADR-0001 specifies,
    against the real `target_schedule` table this session created —
    reuse the same claim statement already verified working in this
    session's validation, don't re-derive it.
  - Wire `context.WithTimeout(rootCtx, target.Timeout)` per check
    (ADR-0004) and confirm the new `contextcheck`/`fatcontext`/`noctx`
    linters this session enabled catch a real regression if that wiring
    is done wrong — a natural first real test of whether this session's
    lint additions earn their keep.
  - Do **not** implement the alert-state machine (ADR-0002) or agent
    ingestion (ADR-0003) yet unless the scheduler work naturally requires
    a minimal stub of one — those are more naturally their own session(s)
    once the scheduler exists to call into.
- Inputs required: this handoff; `docs/adr/ADR-0001` and `ADR-0004`;
  `03-architecture.md` (Normal check execution and Restart-recovery
  sequence diagrams); `backend/migrations/000004_create_target_schedule.up.sql`.
- Expected deliverables: a real Go scheduler package (tick loop, bounded
  worker pool, lease claim/release, per-check context timeout, graceful
  shutdown) with passing tests exercising the exclusivity and
  restart-recovery properties NFR-003/NFR-007/US-007/US-008 name.
- Definition of done: the scheduler runs against the real `docker compose
  up` Postgres, correctly claims/releases leases with zero overlapping
  checks against the same target, and a simulated restart (kill and
  restart the process) recovers within NFR-003's ≤10s budget with no
  special-cased recovery code path — matching `03-architecture.md`'s own
  "there is no distinct restart recovery code path" property.

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Repository state:** branch `main`, unreleased (pre-v0.1.0). Architecture,
API contracts, and the environment/schema/CI baseline are all complete; no
business logic exists yet.

**Problem being solved:** This developer maintains several self-hosted
flagship repositories (`privacy-forge`, `laravel-consent-guard`,
`bookslot`, `lexicon`), none of which currently has a real, live
deployment, plus pulsewatch's own future instance. There is no
continuously running, honest answer to "is it actually up right now, and
did I meet my SLO" for whatever of this does run. See `00-project-brief.md`
for full framing.

**Users:** Single operator (this developer) plus the machine "Agent" role — v1 has no other identity (`02-requirements.md`).

**Current stack:**
- Frontend: SvelteKit (ESLint + Prettier baseline, this session)
- Backend: Go 1.25 (Gin); `golangci-lint` extended this session with
  context-propagation linters (`contextcheck`, `containedctx`,
  `fatcontext`, `noctx`, `bodyclose`)
- Data: PostgreSQL (plain, TimescaleDB rejected) — **real schema now
  exists and is migration-managed** (`backend/migrations/`,
  `golang-migrate`); Redis (available, not load-bearing for
  scheduling/leasing/auth)
- Infra: Docker Compose (now includes a one-shot `migrate` service and the
  OTel Collector), GitHub Actions (now verifies migrations and the
  Collector config), OpenTelemetry Collector (`otel/opentelemetry-
  collector-contrib:0.159.0`, two pipelines: agent-telemetry logs →
  backend `/v1/logs` with `Authorization` header preserved via
  `headers_setter`; self-observability traces/metrics → `debug` exporter
  only, no visualization backend)
- Testing: Go's `testing` package, Vitest

**Architecture decisions that must not be reversed:**
- Licence AGPL-3.0; Go + Gin / SvelteKit frozen; exactly two deep SDLC
  phases (Release & Deployment, Operations & Maintenance); exactly two new
  technologies (Go concurrency, OTel pipelines).
- ADR-0001 (Postgres row leasing), ADR-0002 (3-state alert-suppression
  machine, DB-enforced exactly-once), ADR-0003 (agent-initiated push only,
  both directions), ADR-0004 (bounded worker pool, 1s tick, context-based
  graceful drain) — see `docs/adr/` and `09-decision-log.md`.
- The API contract in `docs/architecture/openapi.yaml` (auth-scheme split,
  write-once credential schemas, `(target_id, checked_at)` OTLP-idempotency
  design) — the wire-level spec real endpoint implementations must match.
- **New this session, must not be silently reversed:** the real schema in
  `backend/migrations/` (nine tables, exactly as `04-data-model.md`
  specifies, plus the one additive `UNIQUE (target_id, checked_at)`
  constraint already flagged before this session started) is what
  Session 5+'s actual Go code must read/write against — not a sketch to
  redesign in passing. The OTel Collector's two-pipeline shape
  (agent-telemetry vs. self-observability, no visualization backend) is
  likewise fixed until a future session makes an explicit, evidenced
  decision to change it.

**Implementation state:**
- Done: repository skeleton, licence, governance docs, minimal real
  Go/Gin backend and SvelteKit frontend with passing tests and clean
  lint, real CI (Session 0); finalised project brief/scope (Session 1);
  complete requirements (Session 2); complete, decided architecture — four
  ADRs, diagrams, rollup/retention data model (Session 3); a validated,
  complete API contract for both the operator and agent surfaces (Session
  3.5); **now a real, verified Postgres schema (migrations), the OTel
  Collector wired into `docker-compose.yml` with its two pipelines,
  context-propagation-aware lint config, a frontend formatting baseline,
  and CI extended to verify all of it (this session).**
- In progress: nothing mid-flight.
- Not started: all actual business-logic implementation — no monitoring
  logic, agent code, scheduler, worker pool, leasing, alert state machine,
  or REST/OTLP handlers exist in Go yet. The schema and infrastructure
  they'll run against now does.

**Constraints and non-goals:**
- Max 2 new technologies, already at cap. v1 is single-operator; no
  multi-user auth/role separation or public status page. Full non-goals
  table in `01-scope-and-non-goals.md`.

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning, Requirements Analysis, Verification & Testing, Retirement & Handover
**Baseline-depth, real rigor on its own merits:** Architecture & Design (Session 3 + API-contracts session); Implementation is baseline depth but this session's schema/CI work was verified by actual execution, not description — see `00a-ledger-confirmation.md`.

**Task for this session (single objective) — now complete:**
Make the dev environment match the real architecture: real migrations for
every ADR-0001/ADR-0002/data-model table (including the flagged
`UNIQUE (target_id, checked_at)`), the OTel Collector wired into
`docker-compose.yml` scoped exactly to `03-architecture.md`'s
self-observability section, coding standards reflecting real project
conventions (context propagation given ADR-0004), a proven
`docker compose up`, and real CONTRIBUTING.md instructions. **Done — see
Work completed above.**

**Definition of done — met:**
- Real migrations exist and were actually run against a fresh Postgres
  (up/down/up round-trip, plus functional proofs of the ADR-0001 claim
  statement, the ADR-0002 exactly-once partial index, and the OTLP
  idempotency constraint) — not just written.
- The OTel Collector boots and is scoped to exactly what
  `03-architecture.md` specifies — verified via the contrib image's own
  `components` list (for `headers_setter`) and `validate` subcommand, with
  no scope creep into a visualization backend.
- `docker compose up --build` proven working end-to-end against this real
  repo with the real schema — migrate exits 0, every service reports
  healthy or (for the Collector, by documented design) responds 200 on
  its own health endpoint.
- `CONTRIBUTING.md` has real, verified setup instructions — every command
  in it was actually run this session.
- `docs/SDLC-EVIDENCE.md`'s Phase 4 row and this handoff are updated,
  handing off to Session 5 (scheduler/leasing implementation).

**Files to attach or paste for Session 5:**
- `docs/adr/ADR-0001-scheduler-restart-safety-leasing.md`
- `docs/adr/ADR-0004-go-concurrency-worker-pool.md`
- `docs/project-memory/03-architecture.md`
- `backend/migrations/000004_create_target_schedule.up.sql`
- `backend/migrations/000005_create_check_results.up.sql`
- `docs/project-memory/12-session-handoff.md` (this file)

**Ground rules:** Do not change the stack. Do not introduce a third new
technology. Do not expand the deep-SDLC-phase count beyond two. Do not
reopen any ADR, the API contract, or the schema this session verified
without new measured evidence per their own Revisit triggers. Implement
the scheduler to match ADR-0001/ADR-0004 exactly — if real implementation
work reveals one is genuinely wrong, report it explicitly rather than
silently drifting the code away from the documented design. Do not touch
`privacy-forge`, `laravel-consent-guard`, `bookslot`, or `lexicon`.
