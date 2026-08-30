# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed
- Session number and title: **Session 6 — Alert Dispatch
  (ADR-0002 implementation).**
- Objective: implement ADR-0002's 3-state alert-suppression state machine
  (Healthy → Suspect → Alerting), wired to Session 5's real check-execution
  results, with exactly-once dispatch enforced by the `incidents` table's
  partial unique index at the database level — not caller discipline — plus
  a clearly-labeled stub notification dispatcher (no real webhook/email/SMS
  provider integration this session).
- Status: **complete**

## Work completed

### `backend/internal/alerting` (new package)
- `state.go` — `State` (`healthy`/`suspect`/`alerting`), `IncidentAction`
  (`None`/`Open`/`Close`), and `Evaluate`: ADR-0002's pure transition
  function, verbatim in its branches. "Unknown" has no representation
  anywhere in this file — no constant, no case in `Evaluate`, nothing — per
  the package doc: the caller (the scheduler) simply never calls `Evaluate`
  when there is no completed check outcome, which is the literal mechanism
  behind "pauses, doesn't reset."
- `incident.go` — `OpenIncident` (the guarded `INSERT ... WHERE NOT
  EXISTS ... RETURNING id`, backed by `incidents_one_open_per_target`),
  `CloseIncident` (the guarded `UPDATE ... WHERE closed_at IS NULL
  RETURNING id`), and `ApplyIncidentAction` (dispatches to either per a
  `Transition`'s `Action`). `OpenIncident` runs its INSERT inside its own
  nested transaction (`db.Begin`, which pgx implements as a `SAVEPOINT` when
  `db` is already an in-progress `pgx.Tx`) — see "Decisions made" below for
  why this isn't optional.
- `channel.go` — `Channel`, `EncryptionKeyFromEnv` (reads
  `ALERT_CHANNEL_ENCRYPTION_KEY`, base64, must decode to 32 bytes),
  `EncryptDestination`/`decryptDestination` (AES-256-GCM, fresh random nonce
  per encryption, FR-023's app-level "encrypted at rest" requirement), and
  `LoadChannels` (reads `alert_channels`, decrypts each destination — zero
  rows requires no key at all, matching the current production reality that
  no channel-registration API exists yet).
- `dispatch.go` — `DispatchRequest`, the `Dispatcher` interface, and
  `LogDispatcher`: this session's stub notification sender (a clearly
  labeled stand-in, matching this portfolio's established stub-tier pattern
  from bookslot/lexicon — no real webhook/email/SMS provider integration is
  in scope this session). FR-023's credential-handling contract is enforced
  even against the stub: `LoadChannels` genuinely decrypts a channel's
  destination and `LogDispatcher` genuinely receives it, but never logs it —
  proven by `TestNotifyChannels_DispatchesRecordsDeliveryAndNeverLogsPlaintext`
  asserting the exact secret string never appears in the dispatcher's log
  output. `NotifyChannels` records one `alert_dispatches` row per configured
  channel per dispatch attempt, with `delivery_confirmed` reflecting only
  whether the `Dispatcher.Dispatch` call itself reported success.

### `backend/internal/scheduler` (extended, not redesigned)
- `lease.go` — `releaseAndRecord` now performs ADR-0002's transition and
  guarded incidents write inside the *same* transaction as the check-result
  insert and lease release, exactly as the ADR specifies ("Both ADR-0001's
  lease-release write and this ADR's transition write happen in the same
  transaction"): it reads `target_schedule.streak`/`state` via
  `SELECT ... FOR UPDATE`, calls `alerting.Evaluate`, calls
  `alerting.ApplyIncidentAction`, then folds the new `streak`/`state` into
  the same `UPDATE` that already advances `next_due_at` and clears the
  lease. Returns a `*alerting.DispatchRequest` only when the whole
  transaction actually commits and the incidents write actually returned a
  row.
- `scheduler.go` — `Scheduler` gained a `dispatcher` field (defaults to
  `alerting.NewLogDispatcher`, overridable via the new `SetDispatcher`
  method) and a `channelKey` field (`alerting.EncryptionKeyFromEnv`, read
  once at construction; a missing key logs a warning and degrades
  gracefully rather than failing to start — there is nothing to decrypt
  until a channel-registration API exists). `handleJob` calls
  `alerting.NotifyChannels` after a successful release, only when
  `releaseAndRecord` returned a non-nil `DispatchRequest`.
- `models.go` — `CheckJob` gained `FailureThreshold` (from
  `targets.failure_threshold`, NFR-011's per-target, operator-configurable
  value — `Evaluate` needs it fresh per job, not as a scheduler-wide
  constant), scanned by `dueTargets`' existing query.

### The required proofs (all real, all against a real Postgres)
- **Exactly-once dispatch under real concurrency, enforced by the database**
  (`TestOpenIncident_ExactlyOneWinnerUnderRealConcurrency`,
  `backend/internal/alerting/incident_concurrency_test.go`): 20 real,
  independent transactions racing to open an incident for the identical
  target via a shared start barrier. Exactly one dispatch, every run.
  **Verified to have real teeth**
  (`TestOpenIncident_WithoutUniqueIndex_DuplicatesOccur_ThenRestored`): the
  partial unique index is dropped, then the race is rerun against ADR-0002's
  own explicitly-rejected "Option A" shape — a naive check-then-insert, with
  a deliberately widened gap between the two statements. (`OpenIncident`'s
  real SQL is a single `INSERT ... WHERE NOT EXISTS` statement — one round
  trip — whose own race window proved too narrow, microseconds, to
  reproduce a duplicate deterministically in initial testing; Option A's
  two-statement shape, with the gap widened by a small sleep, reproduces the
  fault reliably instead.) With the index absent, 13–14 of 20 concurrent
  attempts opened an incident in observed runs. The index is then restored
  inline (not only via a `t.Cleanup` safety net), and the same test
  immediately reproves the real `OpenIncident`'s exactly-once guarantee is
  back. `TestCloseIncident_ExactlyOneWinnerUnderRealConcurrency` proves the
  same property on the resolution edge.
- **Blip suppression, proven end-to-end through the real scheduler**
  (`TestEndToEnd_BlipBelowThreshold_NoIncidentNoDispatch`,
  `backend/internal/scheduler/alert_lifecycle_test.go`): a real target,
  driven through the real `Scheduler.Run` tick/claim/execute/release cycle,
  fails twice against the default `failure_threshold` of 3 — `streak=2,
  state=suspect`, zero `incidents` rows, zero dispatch calls. `Evaluate`
  itself is additionally covered exhaustively in isolation
  (`TestEvaluate`, `backend/internal/alerting/state_test.go`, 8 table-driven
  cases covering every branch, including the exact `nextStreak ==
  threshold` edge and a `threshold=1` boundary case).
- **Resolution, and the full pipeline, proven together end-to-end**
  (`TestEndToEnd_ThresholdCrossing_DispatchesOnce_ThenResolvesOnRecovery`,
  same file): the real scheduler drives one target through three failures
  (crossing the threshold: `state=alerting`, exactly one `opened` dispatch
  to a real, configured, decrypted channel, `alert_dispatches` recorded)
  and then a real recovery (`state=healthy`, the incident's `closed_at` set,
  exactly one `resolved` dispatch) — proving scheduler → check result →
  state transition → dispatch together, not scheduler and alerting tested
  in isolation from each other.
- All of the above pass together with the rest of the suite under
  `go test ./... -race -count=3` against a real Postgres 16 container.

### A real bug found and fixed by the concurrency proof itself
`OpenIncident`'s first implementation caught a Postgres unique-violation
error (the losing side of a real race) and returned `(nil, nil)` as if
nothing had happened — but a failed statement leaves the *whole enclosing
transaction* aborted in Postgres; no further statement in it succeeds until
it's rolled back. Since `OpenIncident` is called from inside
`releaseAndRecord`'s own larger transaction (check-result insert, this
write, the `target_schedule` update — one commit), a lost race would have
silently poisoned that transaction: the subsequent `target_schedule` update
would fail too, `releaseAndRecord` would return an error, and the
already-inserted `check_results` row would be rolled back along with
everything else — losing a legitimately-processed check result purely
because its incident-open attempt lost a race it was always going to lose
correctly. `TestOpenIncident_WithoutUniqueIndex_DuplicatesOccur_ThenRestored`'s
own restore-and-reverify step caught this directly (`tx.Commit` failing with
"commit unexpectedly resulted in rollback" for the race's losers). Fixed by
having `OpenIncident` run its INSERT inside its own nested transaction via
`db.Begin` — a real, independent transaction when `db` is a bare pool, or a
`SAVEPOINT`-backed pseudo-transaction (pgx's built-in nested-transaction
support) when `db` is already an in-progress `pgx.Tx` — so a lost race is
contained to just that one guarded write and never reaches the caller's
outer transaction. No ADR was reopened by this fix: it's an
implementation-level correctness detail neither ADR-0002 nor
`04-data-model.md` specified at the SQL-mechanics level, exactly like
Session 5's own claim-statement parameter-encoding adaptations.

### `docker-compose.yml` / `.env.example`
- Both gained `ALERT_CHANNEL_ENCRYPTION_KEY` (empty by default), matching
  this repo's existing `${VAR:-default}` pattern. Left empty deliberately —
  no channel-registration API exists yet to populate `alert_channels`
  (flagged by Session 5's own handoff and still true after this session), so
  an unset key breaks nothing in a fresh environment; the scheduler logs a
  warning and degrades gracefully rather than failing to start.

### Verification performed (all real, not description)
- `go vet ./...`, `golangci-lint run ./...` (pinned CI version v2.13.2):
  clean, `0 issues`.
- `go test ./... -race -count=3` against a real Postgres 16 container (run
  via ephemeral Docker containers mirroring CI exactly, since this
  environment has no native Go toolchain): all tests pass, repeatably,
  including the proofs above.
- `govulncheck ./...`: `0 vulnerabilities` reachable from this repo's own
  code. No new external dependencies were added this session — the new
  `alerting` package uses only the standard library (`crypto/aes`,
  `crypto/cipher`, `crypto/rand`) plus the already-present `pgx/v5`.
- Full `docker compose up --build` against this real repo, twice-verified:
  (1) a manually inserted failing HTTP target with **no** `alert_channels`
  configured crossed the threshold, opened exactly one incident, and
  correctly dispatched to nothing (zero channels) — the startup log showed
  the graceful `ALERT_CHANNEL_ENCRYPTION_KEY not configured` warning, not a
  crash. (2) A second manually inserted failing target, with a real
  AES-256-GCM-encrypted `alert_channels` row and a real key supplied via
  environment, crossed the threshold and dispatched exactly once through
  the stub (`alert_dispatches`: `kind=opened, delivery_confirmed=true`), and
  the channel's real destination string was confirmed absent from the
  container's logs. Torn down cleanly with `docker compose down` afterward.
- Confirmed `privacy-forge`, `laravel-consent-guard`, `bookslot`, and
  `lexicon` were not modified, and no other flagship's running containers on
  this shared host were touched. All ephemeral test infrastructure this
  session created (a throwaway `pulsewatch-s6-net` Docker network, a
  `pulsewatch-s6-pg` test Postgres container, and two Go module/build-cache
  volumes) was created and torn down independently of the real
  `docker-compose.yml` stack and is removed at the end of this session.

## Decisions made
- **AES-256-GCM as FR-023's concrete encryption algorithm, with a fresh
  random nonce per encryption** — neither ADR-0002 nor `04-data-model.md`
  names a specific algorithm for "encrypted at rest (app-level)"; this
  session filled that gap with a standard, well-reviewed authenticated
  cipher, matching the sensitivity FR-023/the data-classification table
  assign to alert-channel credentials. An Implementation-level default, not
  a data-model or ADR reinterpretation — flagged here for a future
  channel-registration-API session to build on rather than re-decide.
- **`delivery_confirmed` reflects only whether `Dispatcher.Dispatch` itself
  reported success, nothing about real external delivery** — `04-data-model.md`
  names the column ("record of each attempted notification... for
  exactly-once verification and debugging") without specifying its exact
  semantics against a stub. For `LogDispatcher`, "success" means the log
  write succeeded; a real provider `Dispatcher` would give this column its
  intended meaning without any change to `NotifyChannels`.
- **`OpenIncident` reads/writes inside its own nested transaction
  (`db.Begin`, a real transaction standalone or a `SAVEPOINT` inside an
  existing `pgx.Tx`)** — not a design reopening ADR-0002, but a real,
  necessary fix to a genuine bug this session's own concurrency proof
  surfaced (see "A real bug found and fixed" above). Documented explicitly
  because it changes `OpenIncident`'s required call contract slightly: `db`
  must support `Begin`, which both `*pgxpool.Pool` and `pgx.Tx` do, but a
  narrower interface would not have.
- **Resolution-notification behavior was not actually ambiguous in
  ADR-0002** — this session's own instructions anticipated needing to
  resolve an ambiguity here "with real reasoning... rather than guessing
  silently," but ADR-0002's Decision section already specifies it exactly:
  "a returned row means dispatch exactly one resolution notification...
  zero rows returned means dispatch nothing." No new decision was required;
  `CloseIncident`/`ApplyIncidentAction` implement that sentence verbatim.
  Recorded here so a future session doesn't mistake the absence of a new
  decision-log entry for an oversight.
- **Naive "Option A" shape (a deliberately widened check-then-insert),
  isolated to one test file, used to prove the teeth of the partial unique
  index** — `OpenIncident`'s real single-statement guard has too narrow a
  race window to reproduce a duplicate deterministically in a fast
  container-to-container round trip; ADR-0002's own rejected Option A
  (`incident_concurrency_test.go`'s `openIncidentNaive`) reproduces it
  reliably instead. This is test-only scaffolding, never part of the
  production `OpenIncident` path, and is documented in the test file's own
  comments to avoid a future reader mistaking it for a second real
  implementation.
- **A single fixed, non-secret 32-byte test encryption key
  (`testEncryptionKey`), duplicated verbatim across the `alerting` and
  `scheduler` packages' test files, rather than a random key per test** —
  `LoadChannels` decrypts *every* row in the global `alert_channels` table,
  and `go test ./...` can run different packages' test binaries
  concurrently against the same shared test Postgres instance; two packages
  using different random keys would spuriously fail to decrypt each other's
  leftover rows. All DB-scoped assertions in both packages' new tests are
  additionally scoped by the specific channel/target/incident ID each test
  created, never by "how many rows are in the table," for the same reason.

## Validation performed
See "Verification performed" above — `go vet`, `golangci-lint`, `go test
-race` (×3), `govulncheck`, and a real `docker compose up --build` run
(twice: no-channel and real-channel paths), all executed this session
against real infrastructure, not described.

## Open questions and risks
- **No ADR reopened.** ADR-0002 was implemented as specified; the one real
  implementation-level bug found (`OpenIncident`'s transaction-poisoning
  issue) was a SQL-mechanics correctness detail below the ADR's own level of
  specification, not a gap in the ADR's design.
- **No blockers for Session 7.** `check_results`/`target_schedule` writes,
  the alert-suppression machine, and dispatch are all real and proven
  end-to-end. ADR-0003 (agent-initiated push reporting and assignment
  discovery) remains designed but not implemented — the natural next
  session, per Session 5's own handoff and unchanged by this one.
- **Still no target- or channel-registration REST API.** Both this
  session's and Session 5's manual verification inserted rows directly via
  `psql` because no handler implements `docs/architecture/openapi.yaml` yet.
  Not a gap in this session's own scope (alert dispatch only, per this
  session's ground rules) — flagged again so a future session doesn't
  mistake the psql-based verification above for "the API works," which it
  still doesn't.
- **`ALERT_CHANNEL_ENCRYPTION_KEY` has no real value anywhere in this
  repo** — by design (`.env.example`'s own "no real secrets in this file"
  rule) and by necessity (nothing yet creates a real `alert_channels` row in
  production). Whichever future session adds the registration API should
  also decide how an operator is expected to generate/rotate this key in a
  real deployment (`.env.example` currently just points at `openssl rand
  -base64 32`) — not resolved here, since it wasn't reachable without that
  API existing first.

## Next recommended session
- Proposed session title: **Session 7 — Agent-Initiated Push Reporting and
  Assignment Discovery (ADR-0003 implementation).**
  - Implement the agent-facing REST poll (`GET /api/v1/agent/assignments`)
    and OTLP/HTTP result ingestion (`POST /v1/logs`) `docs/architecture/openapi.yaml`
    already defines, wired to the same `check_results`/`target_schedule`
    write path Sessions 5–6 built (an agent-reported result should flow
    through the identical `alerting.Evaluate`/dispatch pipeline a
    server-executed check does — no second state machine).
  - Implement the agent-staleness computation ADR-0003/FR-019 specify
    (`now() - last_heartbeat_at > 3 × report_interval`) and the
    `display_state` read-time overlay `05-api-contracts.md` already
    contracts for `GET /targets/{target_id}/status` — "Unknown" stays a
    display-layer concept computed at read time, never a persisted
    `target_schedule.state` value, exactly as ADR-0002 Consequences already
    fixes and this session's `alerting` package already assumes.
  - A real test proving OTLP duplicate-result idempotency: resubmitting the
    identical `(target_id, checked_at)` pair is a no-op that never re-enters
    `alerting.Evaluate` a second time (the `UNIQUE (target_id, checked_at)`
    constraint on `check_results`, already schema-enforced since Session 4,
    is the backstop — analogous to this session's own
    partial-unique-index proof).
  - Do **not** build the target/channel-registration REST API as part of
    this unless agent assignment discovery genuinely requires a minimal
    slice of it (e.g. reading which targets are assigned to an agent) — full
    CRUD registration is a separable concern, flagged but not scheduled.
- Inputs required: this handoff; `docs/adr/ADR-0003-agent-push-reachability-model.md`;
  `docs/architecture/openapi.yaml` (the agent-facing paths);
  `backend/internal/scheduler/` and `backend/internal/alerting/` (this
  session's and Session 5's write path, the extension point).
- Expected deliverables: a real Go package (or extension to `scheduler`)
  implementing agent result ingestion and assignment discovery, with a
  passing OTLP-duplicate-idempotency proof and a real
  staleness/display-state computation.
- Definition of done: an agent-reported check result reaches the identical
  `alerting.Evaluate`/dispatch pipeline a server-executed check does; a
  stale agent's targets read back as `display_state=unknown` without ever
  writing `unknown` into `target_schedule.state`; a resubmitted duplicate
  result is proven to be a no-op.

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Repository state:** branch `main`, unreleased (pre-v0.1.0). Architecture,
API contracts, environment/schema/CI baseline, the scheduler/leasing core,
and now real alert dispatch are all complete; agent communication does not
exist yet.

**Problem being solved:** This developer maintains several self-hosted
flagship repositories (`privacy-forge`, `laravel-consent-guard`, `bookslot`,
`lexicon`), none of which currently has a real, live deployment, plus
pulsewatch's own future instance. There is no continuously running, honest
answer to "is it actually up right now, and did I meet my SLO" for whatever
of this does run. See `00-project-brief.md` for full framing.

**Users:** Single operator (this developer) plus the machine "Agent" role — v1 has no other identity (`02-requirements.md`).

**Current stack:**
- Frontend: SvelteKit (ESLint + Prettier baseline, Session 4) — unchanged this session.
- Backend: Go 1.25 (Gin); `github.com/jackc/pgx/v5` (`pgxpool`), Session 5's
  addition, still the only Postgres driver; **no new external dependency
  this session** — the new `internal/alerting` package uses only the
  standard library's `crypto/*` plus the already-present `pgx/v5`.
- Data: PostgreSQL (plain, TimescaleDB rejected) — real schema
  (`backend/migrations/`, `golang-migrate`, Session 4), **now with real
  alert-suppression state (`target_schedule.streak`/`state`) and incident/
  dispatch bookkeeping (`incidents`, `alert_channels`, `alert_dispatches`)
  all genuinely read/written**, not just schema-present; Redis (available,
  not load-bearing for scheduling/leasing/alerting/auth).
- Infra: Docker Compose (**now also `ALERT_CHANNEL_ENCRYPTION_KEY`, empty by
  default**, this session's addition), GitHub Actions (unchanged this
  session — no CI step changes were needed), OpenTelemetry Collector
  (unchanged this session).
- Testing: Go's `testing` package (now including real Postgres integration
  tests for exactly-once incident dispatch, blip suppression, and the full
  scheduler→state-machine→dispatch pipeline), Vitest (frontend, unchanged
  this session). This environment has no native Go toolchain — all
  verification this session ran through ephemeral Docker containers
  (`golang:1.25`, `golangci/golangci-lint:v2.13.2`, `postgres:16-alpine`,
  `migrate/migrate:v4.19.1`) mirroring CI, torn down at session end.

**Architecture decisions that must not be reversed:**
- Licence AGPL-3.0; Go + Gin / SvelteKit frozen; exactly two deep SDLC
  phases (Release & Deployment, Operations & Maintenance); exactly two new
  technologies (Go concurrency, OTel pipelines) — still at cap, this session
  added none.
- ADR-0001 (Postgres row leasing) and ADR-0004 (bounded worker pool) —
  implemented Session 5, untouched this session beyond the minimal
  extension point ADR-0002 itself specifies (folding streak/state into the
  same release transaction).
- ADR-0002 (3-state alert-suppression machine, DB-enforced exactly-once) —
  **now implemented, not just designed**, in `backend/internal/alerting/`
  and wired into `backend/internal/scheduler/`, with real proofs (see Work
  completed above). Not reopened; the one real bug found during
  implementation was an SQL-mechanics detail below the ADR's own level of
  specification, not a design gap.
- ADR-0003 (agent-initiated push only, both directions) remains designed
  but **not yet implemented** — Session 7's job.
- The API contract in `docs/architecture/openapi.yaml` — unchanged this
  session; still no handler implements it.
- The real schema in `backend/migrations/` (Session 4) — unchanged this
  session; the alerting package reads/writes `target_schedule.streak`/
  `state`, `incidents`, `alert_channels`, and `alert_dispatches` exactly as
  `04-data-model.md` specifies, no migration was needed.

**Implementation state:**
- Done: repository skeleton, licence, governance docs, minimal real Go/Gin
  backend and SvelteKit frontend (Session 0); project brief/scope (Session
  1); requirements (Session 2); architecture — four ADRs, diagrams, data
  model (Session 3); API contract (Session 3.5); Postgres schema, OTel
  Collector, CI baseline (Session 4); scheduler/leasing core with proven
  exclusivity/restart-recovery/graceful-shutdown, basic HTTP/TCP check
  execution (Session 5); **now real alert-suppression state evaluation,
  database-enforced exactly-once incident open/close, and stub notification
  dispatch, wired end-to-end into the real scheduler (this session).**
- In progress: nothing mid-flight.
- Not started: agent communication (ADR-0003), any REST/OTLP handler
  implementing `openapi.yaml` (including target/channel registration), the
  dashboard, any real notification provider integration (webhook/email/SMS
  — the stub `LogDispatcher` is a deliberate placeholder for this).

**Constraints and non-goals:**
- Max 2 new technologies, already at cap. v1 is single-operator; no
  multi-user auth/role separation or public status page. Full non-goals
  table in `01-scope-and-non-goals.md`.

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning, Requirements Analysis, Verification & Testing, Retirement & Handover
**Baseline-depth, real rigor on its own merits:** Architecture & Design
(Session 3 + API-contracts session); Implementation is baseline depth
overall, but this session's alert-dispatch work — like Sessions 4 and 5's
own work — was verified by actual execution and real concurrency proofs,
not description. See `00a-ledger-confirmation.md`.

**Task for this session (single objective) — now complete:**
Implement ADR-0002's 3-state alert-suppression machine, wired to Session
5's real check-execution results, with database-enforced exactly-once
incident open/close and dispatch, plus a clearly-labeled stub notification
sender. **Done — see Work completed above.**

**Definition of done — met:**
- The 3-state machine matches ADR-0002 exactly, including "Unknown is not a
  state" (no fourth state, no branch for it — `Evaluate` is simply never
  called for an Unknown period).
- Exactly-once dispatch is proven under real concurrency with a test that
  demonstrably has teeth: it fails (produces real duplicates) when the
  partial unique index is absent, and passes once it's restored.
- Blip suppression and resolution are both proven with real, end-to-end
  tests driven through the actual `Scheduler.Run` pipeline, not the
  transition function in isolation.
- The full pipeline (scheduler → check result → state transition →
  dispatch) is proven end-to-end in a dedicated test, and again manually
  against the real `docker compose up --build` stack.
- Full test suite passes (`-race`, `-count=3`), `golangci-lint` is clean,
  `govulncheck` reports `0` vulnerabilities reachable from this repo's code.
- `docs/SDLC-EVIDENCE.md` and this handoff are updated with real evidence
  citations (specific test names/files), handing off to Session 7 (agent
  communication, ADR-0003).

**Files to attach or paste for Session 7:**
- `docs/adr/ADR-0003-agent-push-reachability-model.md`
- `docs/architecture/openapi.yaml` (the agent-facing paths:
  `/api/v1/agent/assignments`, `/v1/logs`)
- `docs/project-memory/05-api-contracts.md` (the `display_state` overlay
  contract for `GET /targets/{target_id}/status`)
- `backend/internal/scheduler/lease.go` and `backend/internal/alerting/`
  (this session's write path — an agent-reported result needs to reach the
  identical pipeline)
- `docs/project-memory/12-session-handoff.md` (this file)

**Ground rules:** Do not change the stack. Do not introduce a third new
technology. Do not expand the deep-SDLC-phase count beyond two. Do not
reopen ADR-0001, ADR-0002, or ADR-0004 without new measured evidence per
their own Revisit triggers. Implement agent communication to match ADR-0003
exactly — if real implementation work reveals it is genuinely wrong, report
it explicitly rather than silently drifting the code away from the
documented design. Do not touch `privacy-forge`, `laravel-consent-guard`,
`bookslot`, or `lexicon`.
