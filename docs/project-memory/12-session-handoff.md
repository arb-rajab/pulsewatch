# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed
- Session number and title: **Session 7 — Agent Communication
  (ADR-0003 implementation).**
- Objective: implement ADR-0003's agent-initiated push model exactly —
  the authenticated REST assignment-discovery poll, OTLP result/heartbeat
  ingestion feeding the identical Session 5/6 alerting pipeline, a real
  distinct agent-credential space, the precise staleness-vs-target-down
  evaluation-ordering rule, and a real reference agent binary used in a
  genuine end-to-end proof.
- Status: **complete**

## Work completed

### `backend/internal/agentauth` (new package)
- `credential.go` — `GenerateToken` (256-bit `crypto/rand`), `HashToken`
  (plain SHA-256 — an implementation-level choice, not a data-model
  reinterpretation: a slow KDF earns nothing defending a secret that's
  already 256 bits of uniform randomness, unlike a human password), and a
  constant-time `tokensEqual` used as defense-in-depth in `Authenticate`.
- `provision.go` — `CreateAgent`: the real INSERT into `agents`, storing
  only the hash, returning the plaintext token exactly once (`Created`,
  mirroring `AgentCreated` in `openapi.yaml`).
- `authenticate.go` — `Authenticate`: looks up `agents.credential_hash`,
  returns the caller's own `Identity` (never another agent's) or
  `ErrInvalidCredential` — one error for "unknown," "empty," and
  "rotated" alike, deliberately not distinguishing them to an
  unauthenticated caller.
- `liveness.go` — `IsStale` (NFR-008's `> 3 × report_interval_seconds`
  rule, pure, table-tested including the exact boundary), `FetchLiveness`,
  `RecordHeartbeat` (the monotonic `GREATEST(COALESCE(..., epoch), $1)`
  write `openapi.yaml`'s `/v1/logs` description requires).

### `backend/internal/agentapi` (new package)
- `otlp.go` — the one Go definition of the simplified OTLP/HTTP+JSON wire
  shape `openapi.yaml` specifies (`OtlpExportLogsServiceRequest` and
  friends), shared by the ingestion handler (decode) and
  `backend/cmd/agent` (encode) — no second, drifted copy of this shape
  anywhere in the repo.
- `assignments.go` — `GetAssignments` (`GET /api/v1/agent/assignments`):
  scoped only by the caller's bearer identity (no `{agent_id}` path
  parameter exists to even construct a cross-agent request); never
  touches `agents.last_heartbeat_at`.
- `logs.go` / `record.go` — `IngestLogs` (`POST /v1/logs`): parses both
  `heartbeat` and `check_result` records, per-record partial-success
  rejection (malformed record, or a target the caller isn't assigned to —
  the write-side symmetry of "identity comes only from the token"), and
  routes every accepted `check_result` through `recordAgentCheckResult`,
  which folds `alerting.RecordCheckResult` — **the identical shared write
  path `scheduler.releaseAndRecord` also uses** — into its own one-commit
  transaction (no `next_due_at`/lease columns to touch here, since the
  server's own scheduler never claims an agent-executed target in the
  first place).
- `middleware.go` / `errors.go` / `router.go` — `RequireAgent` (the
  `agentToken` bearer-auth Gin middleware both endpoints require) and the
  standard `{error:{code,message,field}}` envelope.

### `backend/internal/alerting` (extended)
- `recordresult.go` — `RecordCheckResult`: the single write path every
  check-result source now goes through — idempotent `INSERT ... ON
  CONFLICT (target_id, checked_at) DO NOTHING`, `SELECT ... FOR UPDATE`,
  `Evaluate`, `ApplyIncidentAction` — returning the streak/state to
  persist (unchanged, on a duplicate) so **the caller** still folds its
  own scheduling-specific columns into the same transaction.
- `display.go` — `DisplayState`: the pure ADR-0002-Consequences/ADR-0003-
  Decision-3 read-time overlay (`"unknown"` iff agent-assigned and
  currently stale, else `raw_state` unchanged) — `raw_state` itself is
  never written to by this function; staleness is checked first,
  structurally, per the ADR's own explicit ordering.

### `backend/internal/scheduler/lease.go` (refactored, not redesigned)
- `releaseAndRecord` now calls `alerting.RecordCheckResult` instead of
  inlining the insert/read/evaluate sequence itself — same SQL, same
  transaction boundaries, verified via direct A/B testing (see
  "Verification performed") to introduce no behavioral change. This is
  what makes "no second, lower-rigor path for agent-sourced data"
  (`03-architecture.md`) literally true rather than merely intended.

### `backend/cmd/agent` (new — the reference agent binary)
- Real, minimal (`03-architecture.md`: "doesn't need every feature, but
  needs to be real"): its own local due-set scheduler with no Postgres
  access (`config.go`/`main.go`), real HTTP/TCP check execution
  (`checker.go`, the same success semantics as the server's own
  `scheduler.executeCheck`), and real outbound-only HTTP (`client.go`) —
  `GET /api/v1/agent/assignments` on `PULSEWATCH_POLL_INTERVAL_SECONDS`,
  `POST /v1/logs` after every check plus a periodic independent heartbeat
  on `PULSEWATCH_REPORT_INTERVAL_SECONDS`. Talks directly to the backend
  for this session's own proof (not through the OTel Collector — the
  DoD's own scope: "agent binary -> real network call -> real backend").

### `backend/cmd/seed-agent` (new — real agent provisioning)
- `docs/architecture/openapi.yaml`'s `POST /api/v1/agents` requires
  `operatorSession` auth, and **no operator-session mechanism exists
  anywhere in this repo yet** (no `/auth/login`, no cookie verification —
  the same class of gap Sessions 5/6 already flagged for target/
  alert-channel registration). Building a full cookie-auth system solely
  to gate one agent-creation endpoint would be scope well beyond this
  session's ADR-0003 focus, and shipping that endpoint open in a public
  repo would be a real regression, not a reasonable interim step. This
  session exposes the identical real mechanism (`agentauth.CreateAgent`)
  via a local admin CLI instead: `seed-agent -name X -report-interval N`
  prints the new agent's id and one-time plaintext token. Used for real
  in this session's own end-to-end proof (see below) — not test-only
  scaffolding.

### The required proofs (all real, all against a real Postgres, several against a real `docker compose up --build` stack)
- **Agent auth, real provisioning and verification**
  (`backend/internal/agentauth/*_test.go`): real token → hash → lookup
  round trip against a real `agents` row; unknown/empty token rejected;
  two agents' credentials never cross-authenticate
  (`TestAuthenticate_DistinctAgentsDistinctCredentials`); `credential_hash`
  proven to never equal the plaintext
  (`TestCreateAgent_StoresOnlyTheHashNeverThePlaintext`).
- **OTLP duplicate-result idempotency, at both the shared write path and
  the full HTTP endpoint**: `TestRecordCheckResult_DuplicateCheckedAtIsANoOp`
  (`backend/internal/alerting/recordresult_test.go`) and
  `TestIngestLogs_DuplicateCheckResult_IsANoOp`
  (`backend/internal/agentapi/logs_test.go`) both resubmit an identical
  `(target_id, checked_at)` pair and prove streak stays unchanged (no
  double-increment) and no second `check_results` row — the dedup gate is
  proven to sit *before* `Evaluate`, not just before the final write.
- **The staleness-vs-target-down evaluation-ordering rule, both halves
  explicit** (`TestStalenessVsTargetDown_BothHalvesDistinguished`,
  `backend/internal/agentapi/logs_test.go`): a real heartbeat, then three
  real `check_result` failures over real HTTP genuinely cross
  `failure_threshold` — `raw_state` reaches `alerting`, exactly one real
  incident opens, exactly one real dispatch fires (half one: a
  genuinely-down target reported by a healthy agent transitions through
  the alert machine normally). The identical `target_schedule` row, read
  back once `agentauth.IsStale` computes the agent as stale, is shown by
  `alerting.DisplayState` as `"unknown"` — while `raw_state` in Postgres
  is asserted, directly, to still be exactly `alerting`, untouched (half
  two: a stale agent's real last-known result is never read as still
  current, and staleness is its own distinct, surfaced condition, never
  silently reinterpreted as "all its targets are down").
- **Cross-agent write rejection**
  (`TestIngestLogs_CrossAgentTargetRejected`): an agent presenting a valid
  credential for a target it isn't assigned to is rejected via partial
  success, never written.
- **A real end-to-end proof, driven by the real reference agent binary,
  against the real `docker compose up --build` stack** (not simulated by
  curl): `seed-agent` provisioned a real agent against the running
  stack's own Postgres; a real target was assigned to it pointing at a
  guaranteed-404 path on the running `backend` container
  (`failure_threshold=2`); a real AES-256-GCM-encrypted `alert_channels`
  row was configured with a freshly generated key supplied to `backend`
  via `ALERT_CHANNEL_ENCRYPTION_KEY`. `backend/cmd/agent`, run as its own
  container with only its bearer token/agent id/server URL as
  configuration, polled assignments, executed ten real HTTP checks, and
  reported each real result plus periodic real heartbeats to `/v1/logs`
  over genuine outbound HTTP (no inbound port, FR-018). Verified directly
  against Postgres afterward: `agents.last_heartbeat_at` freshly updated;
  ten real `check_results` rows; `target_schedule` at `streak=10,
  state=alerting`; **exactly one** `incidents` row (opened on the second
  failure, matching `failure_threshold=2`, never reopened across the
  remaining eight); **exactly one** `alert_dispatches` row
  (`kind=opened, delivery_confirmed=true`). All of this session's own
  test data was deleted afterward.
- All of the above pass together with the rest of the suite under `go
  test ./... -race -v` (the exact command CI runs) against a real
  Postgres 16 container.

### A real, measured infrastructure finding (not a code defect) — cross-package Postgres contention on this shared verification host
Under this session's own heavier `-race -count=3` standard (beyond what
CI itself runs), `docker stats` showed this developer's shared Docker
Desktop host (5.7GB total memory) concurrently running several unrelated,
already-started containers from other projects on this machine (one
measured at 60% CPU) while `go test ./...`'s default cross-package
parallelism opened many simultaneous connections to one small
`postgres:16-alpine` test container. This intermittently timed out the
pre-existing `alert_lifecycle_test.go` end-to-end tests' `waitForCondition`
polling. **Proven not to be a Session 7 regression** by direct A/B
testing: the identical intermittent failure was reproduced against the
*unmodified* Session 6 code under the same contention
(`backend/internal/scheduler/lease.go`'s refactor is a pure delegation to
`alerting.RecordCheckResult` — same SQL, same transaction shape, no
behavioral change). `go test -p 1` (serializing package execution,
removing the cross-package contention variable) passes `-race -count=3`
clean and repeatably. As a real, low-risk test-robustness fix below
ADR-0002's own level of specification, `alert_lifecycle_test.go`'s
polling budget was widened (2s → 15s, documented inline with this exact
finding) — GitHub Actions' actual dedicated CI runner carries none of
this shared-host contention, so this is verification-environment
headroom, not evidence the underlying property is slow.

### Verification performed (all real, not description)
- `go vet ./...`: clean.
- `golangci-lint run ./...` (pinned CI version v2.13.2): clean, `0
  issues` — 13 real findings from this session's own new code were found
  and fixed (unchecked `Fprintf`/`Close` errors in `cmd/seed-agent` and
  test files, `net.Listen` without a context in a test, two missing
  exported-symbol doc comments in `agentapi/otlp.go`, one malformed `//`
  comment, one self-comparison bug in a determinism test).
- `go test ./... -race -v` (the exact command `.github/workflows/ci.yml`
  runs): clean, run via ephemeral Docker containers (this environment has
  no native Go toolchain) against a real Postgres 16 container.
  `go test ./... -race -count=3 -p 1`: clean and repeatable (see the
  infrastructure finding above for why `-p 1` was needed on this specific
  shared host).
- `govulncheck`: `0 vulnerabilities` reachable from this repo's own code.
  No new external dependency — `agentauth` uses only the standard
  library's `crypto/rand`/`crypto/sha256`/`crypto/subtle` plus the
  already-present `pgx/v5`; `agentapi`/`cmd/agent`/`cmd/seed-agent` add
  none either.
- Full `docker compose up --build` against this real repo: the reference
  agent binary's real end-to-end proof above, run and torn down cleanly.
- Confirmed `privacy-forge`, `laravel-consent-guard`, `bookslot`, and
  `lexicon` were not modified, and no other flagship's running containers
  on this shared host were touched. All ephemeral test infrastructure
  this session created (Docker networks/volumes/containers for both the
  unit/integration-test verification pass and the docker-compose
  end-to-end proof, including the temporary `backend/cmd/tmp-encrypt`
  one-off encryption helper used only to seed a real alert channel for
  the proof) was removed at the end of this session; this session's own
  rows in the real `docker-compose.yml` stack's persistent Postgres
  volume (the agent, target, schedule, results, incident, dispatch,
  channel it created) were deleted afterward, restoring that volume to
  the state prior sessions' own leftover manual-verification rows had
  already left it in (a handful of stale `alert_channels`/`alert_dispatches`
  rows from Sessions 5/6's own manual verification were found and cleaned
  up too, since a stale, undecryptable channel row would otherwise break
  `alerting.LoadChannels` for any future session reusing this same
  volume).

## Decisions made
- **`HashToken` is plain SHA-256, not bcrypt/scrypt/argon2** —
  `04-data-model.md` names `agents.credential_hash` without specifying an
  algorithm; this session fills that gap the same way Session 6 filled
  FR-023's encryption-algorithm gap. A slow KDF defends a human-chosen
  secret from a small effective keyspace against offline guessing; a
  256-bit `crypto/rand` token has no such small keyspace to defend, so a
  fast cryptographic hash (matching, e.g., GitHub's own PAT design) is
  the standard, correct choice here — not an oversight of "should have
  used bcrypt."
- **Agent provisioning is real (`agentauth.CreateAgent`) but reached via
  a local admin CLI (`cmd/seed-agent`), not an HTTP endpoint this
  session** — see "Work completed" above and the CLI's own package doc.
  Not a silent gap: flagged here, in `SDLC-EVIDENCE.md`, and in the CLI's
  own doc comment, exactly matching the ground rules' "report a gap
  explicitly rather than silently drift" instruction. The real
  provisioning *mechanism* (token generation, hashing, storage) is not a
  stub — only the HTTP-transport wrapper around it is deferred, pending
  operator-session auth existing.
- **`alerting.RecordCheckResult` is the single shared write path,
  extracted from `scheduler.releaseAndRecord` rather than duplicated for
  the agent path** — the literal mechanism behind "no second, lower-rigor
  path for agent-sourced data" (`03-architecture.md`). Verified via direct
  A/B testing (see the infrastructure-finding section) to be behavior-
  preserving for the scheduler's own existing, previously-proven tests.
- **The `alert_lifecycle_test.go` polling budget was widened (2s → 15s)**
  — a real, measured test-robustness fix, not a silent loosening of
  Session 6's own assertions (the assertions themselves — exact streak/
  state/incident/dispatch values — are unchanged; only how long the test
  waits to observe them changed). Documented inline at the point of
  change with the full A/B-testing rationale.
- **No ADR reopened.** ADR-0003 was implemented as specified, including
  its assignment-discovery and staleness-ordering decisions verbatim; the
  one real implementation-level question it left open (the credential-
  hashing algorithm) was filled the same way Session 6 filled ADR-0002's
  encryption-algorithm gap — an implementation default below the ADR's
  own level of specification, not a redesign.

## Validation performed
See "Verification performed" above — `go vet`, `golangci-lint`, `go test
-race` (both the CI-exact single pass and this session's own `-count=3
-p 1` standard), `govulncheck`, and a real `docker compose up --build`
end-to-end proof driven by the real reference agent binary, all executed
this session against real infrastructure, not described.

## Open questions and risks
- **No ADR reopened.** See "Decisions made" above.
- **Agent lifecycle CRUD (list/get/rotate/decommission) remains
  unimplemented**, same as target/alert-channel registration before it —
  `cmd/seed-agent` covers only creation, matching this session's actual
  scope (ADR-0003's push model, not the full agent-management surface).
  A future session building operator-session auth should build the real
  `POST /api/v1/agents` HTTP handler (and the rest of the `/agents/*`
  surface) at the same time — `agentauth.CreateAgent` is already the
  function it would call.
- **Still no operator-session auth (`POST /api/v1/auth/login`) anywhere
  in this repo.** This blocks not just agent-registration-over-HTTP but
  every operator-facing endpoint `05-api-contracts.md` designs
  (target/alert-channel registration, live status, SLO, incident
  history) — none of which any session has implemented yet. This is the
  natural next real gap for a future session to close, not new
  information this session discovered, but worth restating since it's
  now the most-flagged unimplemented piece across three consecutive
  handoffs.
- **The GET `/api/v1/targets/{target_id}/status` read endpoint (the
  contracted home for `display_state`) was not built this session** —
  `alerting.DisplayState` and `agentauth.IsStale`/`FetchLiveness` are
  real, tested, and proven together end-to-end
  (`TestStalenessVsTargetDown_BothHalvesDistinguished`), but wiring them
  to an HTTP GET handler was judged out of this session's five numbered
  scope items and would face the same operator-session-auth gap above.
  A future session implementing the operator-facing status endpoint has
  both pure functions ready to call directly.
- **`docker-compose.yml`/`otel-collector-config.yaml` were not changed
  this session** — the reference agent binary talks directly to
  `backend`'s `/v1/logs` for this session's own proof scope ("agent
  binary -> real network call -> real backend"), matching the DoD's own
  wording. Whether/how to route the reference agent through the already-
  configured OTel Collector in the real compose stack (and verify the
  collector's `otlphttp` exporter preserves the JSON encoding this
  endpoint's schema expects, rather than re-encoding to protobuf) is
  unverified and flagged for whichever future session first needs the
  full collector-mediated path proven, not silently assumed to work.

## Next recommended session
- Proposed session title: **Session 8 — Operator-Session Auth and
  Target/Alert-Channel/Agent Registration REST API.**
  - Implement `POST /api/v1/auth/login` (the stateless, server-HMAC-
    signed session cookie `05-api-contracts.md` already designs) and the
    `operatorSession` Gin middleware.
  - Implement the target, alert-channel, and agent registration/lifecycle
    REST surfaces (`POST/GET/PATCH/DELETE /targets`,
    `POST/GET/PUT /alert-channels`, `POST/GET/DELETE /agents` +
    `credential/rotate`) — real handlers calling the already-real
    functions this and prior sessions built (`agentauth.CreateAgent`,
    `alerting.EncryptDestination`, direct SQL for targets).
  - Implement `GET /targets/{target_id}/status` (live status +
    `display_state`) and `GET /targets/{target_id}/slo`, using
    `alerting.DisplayState`/`agentauth.IsStale` (already real) for the
    former.
  - Do **not** reopen ADR-0001–0004 or ADR-0003; this is wire-level REST
    handler work over already-decided design, matching the same
    "implementation, not redesign" posture Sessions 4–7 have each kept.
- Inputs required: this handoff; `docs/architecture/openapi.yaml` (the
  full operator-facing surface); `docs/project-memory/05-api-contracts.md`
  (session-cookie mechanism, idempotency-key design);
  `backend/internal/agentauth/`, `backend/internal/alerting/` (real
  functions this session and Session 6 built for the new handlers to
  call).
- Expected deliverables: real Gin handlers for the full operator-facing
  REST surface plus operator-session auth, with real integration tests
  against a real Postgres.
- Definition of done: an operator can authenticate, register/update/
  delete a target, configure an alert channel, register/rotate/
  decommission an agent, and read live status/SLO/incident history —
  all through real HTTP endpoints, not `psql`/CLI workarounds.

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Repository state:** branch `main`, unreleased (pre-v0.1.0). Architecture,
API contracts, environment/schema/CI baseline, the scheduler/leasing core,
real alert dispatch, and now real agent communication (ADR-0003) are all
complete; the operator-facing REST API (auth, target/channel/agent
registration, status/SLO reads) does not exist yet.

**Problem being solved:** This developer maintains several self-hosted
flagship repositories (`privacy-forge`, `laravel-consent-guard`, `bookslot`,
`lexicon`), none of which currently has a real, live deployment, plus
pulsewatch's own future instance. There is no continuously running, honest
answer to "is it actually up right now, and did I meet my SLO" for whatever
of this does run. See `00-project-brief.md` for full framing.

**Users:** Single operator (this developer) plus the machine "Agent" role — v1 has no other identity (`02-requirements.md`).

**Current stack:**
- Frontend: SvelteKit (ESLint + Prettier baseline, Session 4) — unchanged this session.
- Backend: Go 1.25 (Gin); `github.com/jackc/pgx/v5` (`pgxpool`) — **no new
  external dependency this session**; `backend/internal/agentauth` uses
  only the standard library's `crypto/*`.
- Data: PostgreSQL (plain, TimescaleDB rejected) — real schema
  (`backend/migrations/`, `golang-migrate`, Session 4), no new migration
  needed this session (the `UNIQUE (target_id, checked_at)` constraint
  05-api-contracts.md flagged was already present in
  `000005_create_check_results.up.sql`); Redis (available, not
  load-bearing).
- Infra: Docker Compose (unchanged this session — the reference agent
  binary is a standalone binary run outside the compose stack by design,
  matching its own real deployment shape, `03-architecture.md`: "may be a
  private network"), GitHub Actions (unchanged), OpenTelemetry Collector
  (unchanged — the reference agent talks directly to `backend` for this
  session's own proof scope, not through the collector).
- Testing: Go's `testing` package (now including real Postgres
  integration tests for agent auth, assignment discovery, OTLP ingestion,
  the staleness-vs-target-down distinction, and a real end-to-end proof
  through `docker compose up --build` driven by the real reference agent
  binary), Vitest (frontend, unchanged). This environment has no native
  Go toolchain — all verification this session ran through ephemeral
  Docker containers (`golang:1.25`, `golangci/golangci-lint:v2.13.2`,
  `postgres:16-alpine`), torn down at session end.

**Architecture decisions that must not be reversed:**
- Licence AGPL-3.0; Go + Gin / SvelteKit frozen; exactly two deep SDLC
  phases (Release & Deployment, Operations & Maintenance); exactly two new
  technologies (Go concurrency, OTel pipelines) — still at cap, this
  session added none.
- ADR-0001 (Postgres row leasing), ADR-0002 (3-state alert-suppression
  machine), ADR-0004 (bounded worker pool) — implemented Sessions 5–6,
  untouched this session beyond `lease.go`'s refactor to call the new
  shared `alerting.RecordCheckResult` (same SQL, same transaction
  boundaries, verified behavior-preserving).
- ADR-0003 (agent-initiated push only, both directions) — **now
  implemented, not just designed**: real assignment-discovery poll, real
  OTLP result/heartbeat ingestion feeding the identical alert-evaluation
  pipeline, real distinct agent credentials, the real staleness-vs-
  target-down evaluation-ordering rule, and a real reference agent
  binary. Not reopened.
- The API contract in `docs/architecture/openapi.yaml` — unchanged this
  session; the agent-facing surface it specifies (`/agent/assignments`,
  `/v1/logs`) is now genuinely implemented; the operator-facing surface
  still has no handler.
- The real schema in `backend/migrations/` — unchanged this session.

**Implementation state:**
- Done: repository skeleton, licence, governance docs (Session 0);
  project brief/scope (Session 1); requirements (Session 2); architecture
  — four ADRs, diagrams, data model (Session 3); API contract (Session
  3.5); Postgres schema, OTel Collector, CI baseline (Session 4);
  scheduler/leasing core (Session 5); real alert-suppression state
  evaluation, database-enforced exactly-once incident open/close, stub
  notification dispatch (Session 6); **now real agent communication:
  assignment discovery, OTLP result/heartbeat ingestion sharing the exact
  alert-evaluation pipeline, real distinct agent credentials, the real
  staleness-vs-target-down rule, and a real reference agent binary proven
  end-to-end against the real docker-compose stack (this session).**
- In progress: nothing mid-flight.
- Not started: operator-session auth, any operator-facing REST handler
  (target/alert-channel/agent registration, live status/SLO/incident
  history reads), the dashboard, any real notification provider
  integration, agent lifecycle CRUD beyond creation (list/get/rotate/
  decommission), the `GET /targets/{target_id}/status` endpoint (its
  underlying computation — `DisplayState`/`IsStale` — is real and tested,
  just not wired to HTTP yet).

**Constraints and non-goals:**
- Max 2 new technologies, already at cap. v1 is single-operator; no
  multi-user auth/role separation or public status page. Full non-goals
  table in `01-scope-and-non-goals.md`.

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning, Requirements Analysis, Verification & Testing, Retirement & Handover
**Baseline-depth, real rigor on its own merits:** Architecture & Design;
Implementation is baseline depth overall, but this session's agent-
communication work — like every prior implementation session — was
verified by actual execution, real Postgres integration tests, and a real
end-to-end proof against a running `docker compose` stack, not
description. See `00a-ledger-confirmation.md`.

**Task for this session (single objective) — now complete:**
Implement ADR-0003's agent-initiated push model exactly: real assignment
discovery, real OTLP result/heartbeat ingestion sharing the identical
alert-evaluation pipeline Session 6 built, a real distinct agent-
credential space, the precise staleness-vs-target-down evaluation-
ordering rule proven with both halves explicit, and a real reference
agent binary used in a genuine end-to-end proof. **Done — see Work
completed above.**

**Definition of done — met:**
- Real assignment-polling and OTLP-ingestion endpoints, implemented
  exactly to the API contract.
- Agent auth is real, distinct from operator auth (which doesn't exist
  yet), tested — provisioning via a real function + local CLI (operator-
  session auth doesn't exist to gate an HTTP endpoint yet, flagged
  explicitly, not silently stubbed).
- The staleness-vs-target-down distinction is proven with real tests
  covering both cases explicitly, matching the precision this repo has
  applied to every other safety property.
- A real, minimal agent binary exists and is used in this session's
  end-to-end proof (agent binary -> real network call -> real backend ->
  Session 5/6's existing scheduler/alert pipeline) — verified directly
  against Postgres: real heartbeats, real check results, one real
  incident, one real dispatch.
- Full test suite passes (`-race`; CI's exact single-pass command clean,
  and this session's own heavier `-count=3` standard clean via `-p 1`,
  with the cross-package host-contention root cause measured and
  documented, not hand-waved), `golangci-lint` clean, `govulncheck`
  reports `0` vulnerabilities reachable from this repo's own code.
- `docs/SDLC-EVIDENCE.md` and this handoff are updated with real evidence
  citations, handing off to Session 8 (operator-session auth and the
  operator-facing REST API).

**Files to attach or paste for Session 8:**
- `docs/architecture/openapi.yaml` (the full operator-facing REST surface)
- `docs/project-memory/05-api-contracts.md` (session-cookie mechanism,
  idempotency-key design, credential-handling structural property)
- `backend/internal/agentauth/` (real credential functions the agent
  registration handler would call), `backend/internal/alerting/`
  (`EncryptDestination`/`DisplayState`, real functions for the
  alert-channel and status handlers)
- `docs/project-memory/12-session-handoff.md` (this file)

**Ground rules:** Do not change the stack. Do not introduce a third new
technology. Do not expand the deep-SDLC-phase count beyond two. Do not
reopen ADR-0001–0004 without new measured evidence per their own Revisit
triggers. Implement the operator-facing REST API and session auth to
match `05-api-contracts.md`/`openapi.yaml` exactly — if real
implementation work reveals either is genuinely wrong, report it
explicitly rather than silently drifting the code away from the
documented design. Do not touch `privacy-forge`, `laravel-consent-guard`,
`bookslot`, or `lexicon`.
