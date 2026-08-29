# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed
- Session number and title: **Session 5 — Scheduler and Leasing
  (ADR-0001/ADR-0004 implementation).**
- Objective: implement the first real feature — the scheduler core (ADR-0001's
  atomic Postgres row-leasing claim/release mechanism) running on ADR-0004's
  bounded worker pool, with real proofs of exclusivity, restart recovery, and
  graceful shutdown, plus basic HTTP/TCP check execution (Session 2's MVP
  boundary). Explicitly not alert dispatch (ADR-0002) or agent communication
  (ADR-0003) — those are later sessions.
- Status: **complete**

## Work completed

### `backend/internal/scheduler` (new package)
- `config.go` — `Config` (worker-pool size, tick interval, lease safety
  margin, hard shutdown deadline) with `DefaultConfig()` matching ADR-0004's
  own reasoned defaults (20 / 1s / 5s / 30s) and `ConfigFromEnv()` reading
  `SCHEDULER_WORKER_POOL_SIZE`/`_TICK_INTERVAL`/`_LEASE_SAFETY_MARGIN`/
  `_HARD_SHUTDOWN_DEADLINE` — ADR-0004 Consequences requires these be
  configuration, not hardcoded constants.
- `owner.go` — per-process lease-owner ID (`hostname-pid-randomhex`), the
  label ADR-0001 Consequences names for self-observability.
- `lease.go` — `claimLease` (ADR-0001's atomic conditional claim, verbatim in
  predicate and columns — only the interval literal's parameter encoding
  differs to fit pgx, using `now() + ($n * INTERVAL '1 second')` instead of a
  driver-native interval type) and `releaseAndRecord` (one transaction:
  insert `check_results`, advance `next_due_at`, clear the lease — exactly
  ADR-0001's release design).
- `check.go` — HTTP check (2xx = success; optional `body_match_pattern`
  matched as a literal substring, not regex; fragment capped at 512 bytes per
  `04-data-model.md`) and TCP check (dial success/refused/timeout). Kept
  deliberately simple per this session's own scope — the leasing/concurrency/
  restart correctness was the substance, not check-execution sophistication.
- `scheduler.go` — `Scheduler.Run`: the tick loop (ticker-driven due-set scan
  + dispatch), the long-lived worker pool (`N` goroutines started once,
  ranging over a channel until it closes), and `handleJob` (claim → execute
  under `context.WithTimeout(drainCtx, target.Timeout)` → release). Graceful
  shutdown implemented exactly as ADR-0004 specifies: the tick loop stops on
  root-context cancellation; already-claimed workers run under a **detached**
  drain context (`context.WithCancel(context.Background())`, intentionally
  not derived from the root context — annotated `//nolint:contextcheck` with
  the ADR-0004 rationale) so they are never yanked by shutdown itself, only
  by their own per-check timeout or the hard-shutdown-deadline force-cancel.
  `tick()` is the single scheduling pass both `Run`'s ticker loop and the
  restart-recovery test call — there is no separate `Recover`/`RunRecovery`
  function anywhere in the package.
- `models.go` — `CheckJob`, `checkOutcome`.

### The four required proofs (all real, all against a real Postgres)
- **Exclusivity under genuine concurrency**
  (`concurrency_test.go`, `TestClaimLease_ExactlyOneWinnerUnderRealConcurrency`):
  20 real, independent goroutines, each through its own connection out of a
  real `pgxpool.Pool`, released at a shared start barrier to race for the
  identical lease. Exactly one wins, every run (`-count=3`, `-race`).
  **Verified to have real teeth**: the claim was temporarily weakened to a
  read-then-write (SELECT then UPDATE, no atomic WHERE guard) and the same
  test then failed reliably — 13–14 of 20 goroutines "won" per run, across 5
  repeated runs — before the atomic implementation was restored and
  reverified passing. Goroutines with independent pool connections were used
  rather than real OS processes (bookslot's `proc_open` pattern): the
  property under test is Postgres row-level atomicity of the `UPDATE ...
  RETURNING` statement, enforced by Postgres itself regardless of which
  process issues the request — an OS-process boundary would add nothing this
  test doesn't already prove against the real external resource being raced
  over.
- **Restart recovery is literally the same code path**
  (`restart_recovery_test.go`): `TestClaimLease_ReclaimsLeaseAbandonedByAnotherOwner`
  isolates the claim predicate itself against a foreign, expired lease;
  `TestRestartRecovery_SameTickLogicReclaimsStaleLease` is the literal,
  end-to-end proof — a lease row is seeded with a foreign owner and a
  `lease_expires_at` in the past (simulating a crash mid-check), then a
  fresh `Scheduler.Run` (the exact method every ordinary tick uses) reclaims
  it, runs exactly one check, and releases cleanly (`next_due_at` advanced,
  lease cleared, exactly one `check_results` row — no duplicate).
- **Graceful shutdown, both outcomes** (`shutdown_test.go`):
  `TestGracefulShutdown_InFlightCheckFinishesNaturally` proves a claimed
  check is not yanked when the root context cancels — it keeps running under
  its own per-check timeout and records a real result.
  `TestGracefulShutdown_ForceCancelAtHardDeadline_LeavesLeaseAbandonedNotCorrupted`
  proves the other side: a check still running at a short, test-configured
  hard shutdown deadline is force-canceled, records **no** result (never a
  half-processed or duplicate one), and leaves the lease exactly as the claim
  set it — abandoned, not cleared, not corrupted — reclaimable later via the
  same ordinary claim predicate once it actually expires, no special
  shutdown-recovery path.
- All of the above pass together with the rest of the suite under
  `go test ./... -race -v -count=3` (repeated 3x for confidence against
  flakiness, not run once and assumed stable).

### `backend/main.go` — rewritten
- Wires a real `pgxpool.Pool`, `scheduler.New`, and the HTTP server together
  under one `signal.NotifyContext(SIGINT, SIGTERM)` root context. Refactored
  into `run() error` (was inline in `main()`) specifically to fix a real
  `gocritic exitAfterDefer` finding — the original had `defer stop()` before
  several `log.Fatal` calls, which `os.Exit` would have skipped, silently
  leaking the signal-notification registration on every one of those early-
  exit paths.
- Shutdown sequencing: blocks on root-context cancellation (or an
  unexpected HTTP server failure, which cancels the root context too), shuts
  the HTTP server down with its own bounded deadline, **then** waits for
  `Scheduler.Run` to actually return (already internally bounded by its own
  hard-shutdown-deadline) — the earlier draft of this file raced these two
  and could have let `main` return (killing the process) while the scheduler
  was still mid-drain; caught and fixed before this was ever committed.

### `backend/go.mod` / `go.sum`
- Added `github.com/jackc/pgx/v5` v5.10.0 (`pgxpool` for the connection pool;
  chosen over `database/sql` + a stdlib-wrapper driver since the worker
  pool's concurrent claim/release/check-execution traffic maps directly onto
  pgx's native pool and context-first API, with no separate driver-registration
  layer needed).
- `golang.org/x/text` bumped `v0.34.0` → `v0.39.0`: `govulncheck` found a
  real, reachable vulnerability (GO-2026-5970, infinite loop on invalid
  input) in the version pgx's dependency graph had pulled in transitively,
  reachable through `pgxpool.New`. Fixed, reverified `0 vulnerabilities`.

### `docker-compose.yml` / `.env.example`
- `backend` service and `.env.example` both gained
  `SCHEDULER_WORKER_POOL_SIZE`/`_TICK_INTERVAL`/`_LEASE_SAFETY_MARGIN`/
  `_HARD_SHUTDOWN_DEADLINE`, matching this repo's existing `${VAR:-default}`
  pattern, per ADR-0004's explicit "expose as configuration, not hardcoded
  constants" consequence.

### `.github/workflows/ci.yml` — reordered, not rebuilt
- **A real, necessary fix, not scope creep**: migrations now apply once
  *before* `go test`, in addition to the existing later up→down-all→up
  reversibility check. Session 4's tests never touched Postgres, so step
  order didn't matter yet; Session 5's integration tests do (they need real
  `targets`/`target_schedule`/`check_results` tables), and would have failed
  — or worse, silently skipped everything via the DB-unreachable `t.Skip`
  path — against a schema-less service-container Postgres without this.
  `DATABASE_URL` (pointing at the existing `postgres` service container) is
  now also set on the `go test` step itself.
- Everything else in the backend job (lint, vet, govulncheck, gitleaks,
  CodeQL, the otel-collector-config job) is untouched.

### Verification performed (all real, not description)
- `go vet ./...`, `golangci-lint run ./...` (pinned CI version v2.13.2):
  clean, `0 issues` — including one real, fixed finding during development
  (`contextcheck` on the intentional drain-context detach in `Run`, resolved
  with a `//nolint:contextcheck` annotation carrying the ADR-0004 rationale,
  not silently suppressed) and a real `gocritic exitAfterDefer` finding in
  `main.go` (fixed by the `run() error` refactor above).
- `go test ./... -race -v -count=3`: all tests pass, repeatably, including
  the four proofs above, against a real Postgres 16 container.
- `govulncheck ./...`: `0 vulnerabilities` after the `golang.org/x/text` fix
  above (was 1, real and reachable, before the fix).
- Full `docker compose up --build` against this real repo: backend boots
  cleanly with the scheduler running (no crash, no error logs). Manually
  inserted one HTTP target (`http://backend:8080/health`) and one TCP target
  (`redis:6379`) directly via `psql` against the compose-managed Postgres;
  both were claimed, checked, and recorded by the running `backend`
  container within one tick — `check_results.success = true` with a real
  status code (HTTP) / real latency (both), the lease correctly released
  (`lease_owner` cleared) and `next_due_at` advanced afterward. Torn down
  cleanly with `docker compose down` afterward.
- Confirmed `privacy-forge`, `laravel-consent-guard`, `bookslot`, and
  `lexicon` were not modified, and no other flagship's running containers on
  this shared host were touched, started, or stopped by any command this
  session ran. All ephemeral test infrastructure this session created (a
  throwaway `pulsewatch-s5-net` Docker network, `pulsewatch-s5-pg` and
  `pulsewatch-ci-dryrun-pg` test Postgres containers, and two Go build-cache
  volumes) was created and torn down independently of the real
  `docker-compose.yml` stack and is removed at the end of this session.

## Decisions made
- **`github.com/jackc/pgx/v5` (pgxpool) as the Postgres driver** — the
  concrete choice `04-data-model.md`'s "Migration approach" section didn't
  need to make (that was migration tooling, decided Session 4) but
  Implementation now does need for the scheduler's actual DB access. Chosen
  over `database/sql` + a pgx-stdlib shim because the worker pool's
  concurrent claim/release/execute traffic maps directly onto `pgxpool`'s
  native connection pool and context-first `QueryRow`/`Exec`/`Begin` API,
  with no separate driver-registration indirection — an ordinary
  Implementation-session tooling choice, not an architecture decision.
- **HTTP check success = 2xx status code, checked before any configured
  body-match pattern; `body_match_pattern` matched as a literal substring,
  not a regex** — neither is specified by `04-data-model.md` or
  `02-requirements.md` (no `expected_status_code` column exists on
  `targets`; "pattern" is used without a stated syntax). This session filled
  that specific gap with the simplest rule consistent with the schema's own
  `failure_reason` vocabulary (`status_mismatch` distinct from
  `body_mismatch`) and this session's own "keep check execution simple"
  scope — an Implementation-level default, not a data-model or ADR
  reinterpretation. Flagged here explicitly in case a future session (e.g.
  a real target-registration API) wants an explicit `expected_status_code`
  column instead.
- **Claim/release SQL uses `now() + ($n * INTERVAL '1 second')` and explicit
  `::uuid`/`::text` casts, rather than ADR-0001's illustrative SQL verbatim**
  — a driver parameter-binding adaptation only. The predicate, the columns
  touched, and the transaction boundaries are unchanged from the ADR; this
  is the same reasoning Session 4 already applied to
  `PRIMARY KEY (id, checked_at)` on `check_results` (a mechanic, not a
  redesign).
- **Goroutines with independent pool connections, not real OS processes,
  for the concurrency proof** — see the proof description above. Documented
  explicitly since this session's own instructions named OS-process-level
  concurrency (bookslot's `proc_open` pattern) as a fallback "if genuinely
  necessary"; it wasn't, because the race being proven lives entirely inside
  Postgres, not inside any one Go process's memory.
- **CI step reorder (migrations before `go test`) is a real fix, not scope
  creep** — see the CI section above. No ADR, schema, or prior session's
  decision is reopened; this is ordinary CI plumbing catching up to the fact
  that Session 5 is the first session with Postgres-dependent Go tests.
- **`main.go` refactored to `run() error`** — a real `gocritic
  exitAfterDefer` finding, not a style preference; see the main.go section
  above for the concrete bug it would otherwise have left in place (deferred
  signal-context cleanup skipped by early `log.Fatal` exits).

## Validation performed
See "Verification performed" above — golangci-lint, go vet, go test -race
(×3), govulncheck, and a real `docker compose up --build` run with manually
inserted HTTP and TCP targets, all executed this session, not described.

## Open questions and risks
- **No ADR reopened.** ADR-0001 and ADR-0004 were implemented as specified;
  no gap was found in either that required reporting back as a design
  problem. The two implementation-level gaps this session had to fill
  (HTTP success/body-match semantics, driver parameter encoding) are exactly
  the kind of decision `04-data-model.md`/the ADRs left to Implementation,
  not a sign either document was wrong.
- **No blockers for Session 6.** The scheduler runs correctly against the
  real schema; `target_schedule.streak`/`state` (ADR-0002's columns) exist
  in the schema already (Session 4) but are untouched by this session's
  code — Session 6 can build the alert-suppression state machine directly
  on top of the check-execution/release cycle this session produced,
  without needing to modify `handleJob`'s claim/execute/release shape, only
  extend what happens after a result is recorded.
- **No target-registration API exists yet** — this session's `docker
  compose up` verification inserted test targets directly via `psql`
  because there is no REST endpoint yet to do it through
  (`docs/architecture/openapi.yaml` defines the contract; no handler
  implements it). Not a gap in this session's own scope (scheduler/leasing/
  execution only, per this session's ground rules) — flagged so a future
  session doesn't mistake the psql-based verification above for "the API
  works," which it doesn't yet.

## Next recommended session
- Proposed session title: **Session 6 — Alert-Suppression State Machine
  (ADR-0002 implementation).**
  - Wire ADR-0002's streak/state transition function into the post-release
    point in `scheduler.handleJob` (after `releaseAndRecord` succeeds) — the
    natural extension point this session's claim/execute/release cycle
    already produces.
  - Implement the conditional incident open/close (`INSERT ... WHERE NOT
    EXISTS`/`UPDATE ... RETURNING`) and alert dispatch (webhook/email)
    exactly as ADR-0002 and `03-architecture.md`'s alert-fire-vs-suppress
    sequence diagram specify.
  - A real test proving the exactly-once dispatch guarantee under a
    concurrent race analogous to this session's lease-claim proof (e.g. two
    concurrent check-result paths for the same target attempting to open an
    incident simultaneously — the `incidents_one_open_per_target` partial
    unique index, already schema-enforced since Session 4, should reject the
    second).
  - Do **not** implement agent communication (ADR-0003) yet unless alert
    dispatch naturally requires a minimal stub — that's a later session.
- Inputs required: this handoff; `docs/adr/ADR-0002-alert-suppression-state-machine.md`;
  `docs/project-memory/03-architecture.md` (the alert-fire-vs-suppress
  sequence diagram); `backend/internal/scheduler/` (this session's
  claim/execute/release cycle, the extension point).
- Expected deliverables: a real Go package implementing the transition
  function and incident/dispatch persistence, with a passing exactly-once
  concurrency proof matching this session's own standard.
- Definition of done: a failing check advances streak/state correctly, an
  incident opens exactly once at the configured failure threshold and
  closes exactly once on recovery, and a concurrent-race test proves the
  exactly-once guarantee the same way this session proved lease-claim
  exclusivity.

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Repository state:** branch `main`, unreleased (pre-v0.1.0). Architecture,
API contracts, environment/schema/CI baseline, and now the scheduler/leasing
core are all complete; alert dispatch and agent communication do not exist
yet.

**Problem being solved:** This developer maintains several self-hosted
flagship repositories (`privacy-forge`, `laravel-consent-guard`, `bookslot`,
`lexicon`), none of which currently has a real, live deployment, plus
pulsewatch's own future instance. There is no continuously running, honest
answer to "is it actually up right now, and did I meet my SLO" for whatever
of this does run. See `00-project-brief.md` for full framing.

**Users:** Single operator (this developer) plus the machine "Agent" role — v1 has no other identity (`02-requirements.md`).

**Current stack:**
- Frontend: SvelteKit (ESLint + Prettier baseline, Session 4)
- Backend: Go 1.25 (Gin); **now also `github.com/jackc/pgx/v5`
  (`pgxpool`)**, this session's addition, for all scheduler Postgres access;
  `golangci-lint` context-propagation linters (Session 4) exercised for real
  this session (one genuine `contextcheck` finding, correctly annotated with
  its ADR-0004 rationale, not suppressed blind)
- Data: PostgreSQL (plain, TimescaleDB rejected) — real schema
  (`backend/migrations/`, `golang-migrate`, Session 4), **now with a real
  Go scheduler reading/writing `targets`/`target_schedule`/`check_results`
  against it**; Redis (available, not load-bearing for scheduling/leasing/
  auth)
- Infra: Docker Compose (migrate + OTel Collector services, Session 4; **now
  also the scheduler's four `SCHEDULER_*` env vars**, Session 5), GitHub
  Actions (**migration-before-test ordering fixed this session** — a real
  necessity once Postgres-dependent Go tests existed, not scope creep),
  OpenTelemetry Collector (unchanged this session)
- Testing: Go's `testing` package (now including real Postgres integration
  tests for the scheduler — concurrency, restart-recovery, graceful
  shutdown), Vitest (frontend, unchanged this session)

**Architecture decisions that must not be reversed:**
- Licence AGPL-3.0; Go + Gin / SvelteKit frozen; exactly two deep SDLC
  phases (Release & Deployment, Operations & Maintenance); exactly two new
  technologies (Go concurrency, OTel pipelines).
- ADR-0001 (Postgres row leasing) and ADR-0004 (bounded worker pool, 1s
  tick, context-based graceful drain) — **now implemented, not just
  designed**, in `backend/internal/scheduler/`, with real proofs (see Work
  completed above). Neither ADR was reopened; no gap was found requiring one
  to be revisited.
- ADR-0002 (3-state alert-suppression machine, DB-enforced exactly-once) and
  ADR-0003 (agent-initiated push only, both directions) remain designed but
  **not yet implemented** — Session 6+'s job, not this session's.
- The API contract in `docs/architecture/openapi.yaml` — unchanged this
  session; still no handler implements it (the scheduler consumes
  `target_schedule`/`targets` directly, not through the REST API).
- The real schema in `backend/migrations/` (Session 4) — unchanged this
  session; the scheduler reads/writes it exactly as `04-data-model.md`
  specifies, no migration was needed.

**Implementation state:**
- Done: repository skeleton, licence, governance docs, minimal real Go/Gin
  backend and SvelteKit frontend with passing tests and clean lint, real CI
  (Session 0); finalised project brief/scope (Session 1); complete
  requirements (Session 2); complete, decided architecture — four ADRs,
  diagrams, rollup/retention data model (Session 3); a validated, complete
  API contract (Session 3.5); a real, verified Postgres schema, the OTel
  Collector, context-propagation-aware lint config, and CI extended to
  verify all of it (Session 4); **now a real scheduler/leasing core
  (ADR-0001/ADR-0004) with proven exclusivity, restart-recovery, and
  graceful-shutdown properties, plus basic HTTP/TCP check execution (this
  session).**
- In progress: nothing mid-flight.
- Not started: alert-suppression state machine (ADR-0002), agent
  communication (ADR-0003), any REST/OTLP handler implementing
  `openapi.yaml`, the dashboard.

**Constraints and non-goals:**
- Max 2 new technologies, already at cap. v1 is single-operator; no
  multi-user auth/role separation or public status page. Full non-goals
  table in `01-scope-and-non-goals.md`.

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning, Requirements Analysis, Verification & Testing, Retirement & Handover
**Baseline-depth, real rigor on its own merits:** Architecture & Design
(Session 3 + API-contracts session); Implementation is baseline depth
overall, but this session's scheduler/leasing work — like Session 4's
schema/CI work — was verified by actual execution and real concurrency/
restart/shutdown proofs, not description. See `00a-ledger-confirmation.md`.

**Task for this session (single objective) — now complete:**
Implement the scheduler core (ADR-0001's leasing claim/release) running on
ADR-0004's bounded worker pool, with real proofs of exclusivity under
genuine concurrency, restart recovery via the identical tick code path (no
special-cased recovery), and graceful shutdown (both the natural-completion
and force-cancel outcomes), plus basic HTTP/TCP check execution. **Done —
see Work completed above.**

**Definition of done — met:**
- The scheduler runs against the real `docker compose up` Postgres,
  correctly claims/releases leases with zero overlapping checks against the
  same target (proven under real concurrency, not asserted) — verified both
  by the automated test suite and by a live `docker compose up --build` run
  with manually inserted HTTP/TCP targets.
- A simulated restart (a stale, foreign-owned, expired lease) recovers via
  the exact same `Scheduler.Run`/tick code every ordinary tick uses — no
  distinct recovery code path exists in the package, proven by a test that
  exercises exactly that.
- Graceful shutdown is tested, not assumed: both ADR-0004 outcomes (natural
  completion within the hard deadline; force-cancel past it, leaving the
  lease abandoned but never corrupted) have dedicated, passing tests.
- Full test suite passes (`-race`, `-count=3`), `golangci-lint` is clean,
  `govulncheck` is clean (after a real, required `golang.org/x/text` fix).
- `docs/SDLC-EVIDENCE.md` and this handoff are updated with real evidence
  citations (specific test names/files), handing off to Session 6
  (alert-suppression state machine).

**Files to attach or paste for Session 6:**
- `docs/adr/ADR-0002-alert-suppression-state-machine.md`
- `docs/project-memory/03-architecture.md` (the alert-fire-vs-suppress
  sequence diagram)
- `backend/internal/scheduler/scheduler.go` and `lease.go` (the
  claim/execute/release cycle Session 6 extends)
- `backend/migrations/000004_create_target_schedule.up.sql` and
  `000007_create_incidents.up.sql`
- `docs/project-memory/12-session-handoff.md` (this file)

**Ground rules:** Do not change the stack. Do not introduce a third new
technology. Do not expand the deep-SDLC-phase count beyond two. Do not
reopen ADR-0001 or ADR-0004, the API contract, or the schema without new
measured evidence per their own Revisit triggers. Implement the
alert-suppression state machine to match ADR-0002 exactly — if real
implementation work reveals it is genuinely wrong, report it explicitly
rather than silently drifting the code away from the documented design. Do
not touch `privacy-forge`, `laravel-consent-guard`, `bookslot`, or `lexicon`.
