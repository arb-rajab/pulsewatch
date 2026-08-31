# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed
- Session number and title: **Session 11 — Close the Cross-Port
  Session-Cookie Leak.**
- Objective: eliminate the cross-port session-cookie exposure found closing
  out Session 10 — a real operator session cookie must never be usable over
  the plain-HTTP agent-facing port, while agent traffic (ADR-0003) keeps
  working unchanged. A genuine security fix, not a documentation
  correction.
- Status: **complete.** R-004 (the cross-port cookie leak itself) opened
  and closed within this session, with real before/after evidence. R-003
  narrowed to its own original, still-real, still-open scope (agent bearer
  tokens/telemetry in plaintext) — not silently folded into R-004's
  closure.

## Work completed

### `backend/main.go` (edited — the real fix)
Replaced the single shared `setupRouter` (one `*gin.Engine`, one
`http.Server`, both `agentapi` and `operatorapi` mounted on it) with two
independent ones:
- `setupOperatorRouter(pool, sessionSecret, channelKey)` — `/health` +
  every `operatorapi` route. Served by a new `operatorSrv` (`:8080` inside
  the container).
- `setupAgentRouter(pool, dispatcher, channelKey, logger)` — `/health` +
  every `agentapi` route. Served by a new `agentSrv` (`:8081` inside the
  container).

`run()` now starts both `http.Server`s as independent goroutines, treats
either's fatal `ListenAndServe` error as a reason to begin the same
graceful-shutdown path the single server used before, and shuts both down
on the same `schedCfg.HardShutdownDeadline`-bounded context. No
`operatorapi` route exists anywhere on `agentSrv`, and no `agentapi` route
exists anywhere on `operatorSrv` — this is a structural absence, not
routing logic that happens to reject a cookie.

### `backend/main_test.go` (edited)
`TestHealthReturns200OK` split into `TestOperatorRouterHealthReturns200OK`/
`TestAgentRouterHealthReturns200OK` (one per new router). Two new
regression tests added as this session's own proof the split is real, not
just observed working once: `TestOperatorRouterHasNoAgentRoutes` asserts
`GET /api/v1/agent/assignments` is a genuine `404` on the operator router;
`TestAgentRouterHasNoOperatorRoutes` asserts `GET /api/v1/targets` is a
genuine `404` on the agent router.

### `docker-compose.yml` (edited)
`backend`'s published port changed from `8020:8080` to `8020:8081` — the
external address agents/README/CONTRIBUTING already document (`8020`) is
unchanged; only its internal target moved to the new agent-only listener.
Container port `8080` (the operator listener) is no longer published at
all — reachable only over the internal Docker network, by `proxy`/Caddy or
another container. `healthcheck:` now checks both `:8080/health` and
`:8081/health`, so the container only reports `healthy` if both listeners
are actually serving, not just whichever one happens to be published.

### `backend/internal/operatorapi/status.go` (edited — incidental)
One `gofmt`-only whitespace fix (`targetStatusResponse`'s struct-tag
alignment), pre-existing since Session 8, found via this session's own
`golangci-lint run ./...` verification pass (see "Real bugs found and
fixed" below) — no field, tag, or logic change.

### `Caddyfile` — unchanged
`/api/*` already routed to `backend:8080` (Session 10), which is exactly
the operator-only listener after this session's split — no edit was
needed for Caddy's own routing to keep working correctly.

### `docs/project-memory/10-risk-register.md` (edited)
New `R-004` opened and closed within this same session (found closing out
Session 10, fixed and verified in Session 11) — the cross-port
session-cookie leak itself, with full before/after evidence cited. `R-003`
narrowed: its text previously (Session 10) undersold the exposure as
"agent bearer tokens only" when the same port also carried the full
operator session flow — corrected, and its remaining scope narrowed to
exactly what's still real and unchanged: agent bearer tokens/OTLP
telemetry still cross the agent-facing port in plaintext. R-002 carried
forward unchanged (still out of scope, no backend logic touched beyond the
routing split), review date moved to Session 12.

### `docs/project-memory/08-deployment-and-operations.md` (edited)
"TLS termination" section extended to document the Session 11 router
split: which routes live on which listener, which is published as a host
port, why the router-split option was chosen over routing agent traffic
through Caddy too (real cost that option would add — see "Decisions made"
below), and the corrected/narrowed residual-gap framing matching R-003's
new text. The "Observability" section's health-check description updated
to reflect `backend` now running two independently health-checked
listeners instead of one.

## Decisions made

- **Split the routers (option (a)), not "route agent traffic through Caddy
  too" (option (b), R-003's own originally suggested mitigation).** Both
  would have eliminated the demonstrated leak. Option (b) was rejected
  for this session because it costs more than this session's actual,
  bounded objective needed: it would require removing `backend`'s direct
  host port entirely (funneling *all* traffic, agent included, through
  `proxy`) and the reference agent (`backend/cmd/agent`) trusting Caddy's
  `tls internal` self-signed CA — fine for this same-host `docker compose`
  deployment, but a real, undischarged cost for this developer's actual
  stated agent-deployment model (`00-project-brief.md`: agents on
  `privacy-forge`/`laravel-consent-guard`/`bookslot`/`lexicon`'s own
  separate hosts), which would need that CA certificate distributed to
  every such remote host. The router split fully closes the demonstrated
  leak with no agent-binary change and no new cross-host trust story, at
  the honestly-named cost of leaving R-003's narrower agent-transport gap
  open for a future session — not a half-measure, since it doesn't merely
  make the leak *harder* to demonstrate (the ground rules' explicit
  warning): the route is structurally absent, not conditionally rejected.
- **Did not change the cookie itself (`Path`/`Domain`/`SameSite`), and did
  not add a header/origin check aimed at making curl's specific replay
  fail without closing the actual gap.** The task's own ground rules
  named this as the exact failure mode to avoid, and it's correct: neither
  attribute carries port scoping (RFC 6265), so either would have been
  cosmetic against a real browser too, not just against curl.
- **Kept the external agent-facing address at `8020`, only moved its
  internal container-port target (8080 → 8081).** Every existing
  reference to `http://localhost:8020` (README, CONTRIBUTING,
  `frontend`'s historical `PUBLIC_API_URL` default, prior session
  handoffs) stays valid as "the agent-facing port" — nothing outside
  `docker-compose.yml`/`backend/main.go` needed to change to keep that
  true, since those docs only ever named the *external* `8020` address,
  never `8080` specifically.
- **R-003 was narrowed, not closed, and R-004 was opened and closed
  separately rather than reusing R-003's ID for the fix.** R-003's own
  original scope (agent bearer tokens/telemetry in plaintext) is still
  entirely real and unchanged — closing R-003 outright would have
  silently implied that gap was fixed too, which it isn't. R-004 exists so
  the newly-found-and-fixed component (the cross-port operator-cookie
  leak) has its own clean, fully-closed record, the same discipline
  Session 10 applied when it declined to fold R-003 into R-001's closure.

## Real bugs found and fixed during this session's own verification

One, pre-existing and unrelated to the routing fix: running
`golangci-lint run ./...` (matching CI's exact `v2.13.2` invocation, done
as this session's own "prove it passes what CI would check" step, the same
discipline Session 10 applied to `docker compose up`) surfaced a
`gofmt`-only failure in `backend/internal/operatorapi/status.go`
(`targetStatusResponse`'s struct-tag column alignment), present since
Session 8 (`a94b735`) — CI would have failed this lint check on `main`
regardless of this session's own changes, unnoticed until now because no
session since had run `golangci-lint` locally before pushing. Fixed with a
single `gofmt`-equivalent whitespace edit (no field, tag, or logic
change) rather than left broken or routed around; re-verified clean
(`0 issues`) and `go test ./...` still passes. Otherwise, this session's
own `docker compose up -d --build` against the edited
`docker-compose.yml`/`backend` came up clean on the first attempt — no new
deployment-config defect surfaced.

## Verification performed (all real, not description)

- **Build, vet, lint, and unit tests**, via ephemeral `golang:1.25-alpine`/
  `golangci/golangci-lint:v2.13.2` containers (no local Go toolchain in
  this environment) against the edited `backend/` module: `go build ./...`
  clean; `go vet ./...` clean; `golangci-lint run ./...` — one pre-existing
  `gofmt` failure found and fixed (see "Real bugs found and fixed" below),
  clean (`0 issues`) after; `go test ./...` — every package passes,
  including the two new regression tests
  (`TestOperatorRouterHasNoAgentRoutes`, `TestAgentRouterHasNoOperatorRoutes`)
  and the renamed health tests.
- **Full stack brought up via the actual, unmodified deployment path**:
  `docker compose up -d --build` from this session's edited
  `docker-compose.yml`, reusing the `.env` created in Session 10 (no
  secrets committed). `backend` container reports `healthy` (checked via
  the new dual-listener healthcheck) with ports
  `0.0.0.0:8020->8081/tcp` — confirming the agent-only listener, not
  `8080`, is what's actually published.
- **A real, validly-signed operator session cookie**, minted via the
  identical `operatorauth.IssueSession` the real `/auth/login` handler
  calls, using the real `SESSION_SIGNING_SECRET` from the running stack's
  own `.env` and the real, pre-existing operator row's id (this session
  did not have — and did not reset — that operator's actual password,
  which Session 10's own handoff records as deliberately never committed;
  minting via the same signing function the app itself uses is equivalent
  evidence to a real login for this specific test, without touching the
  `operators` table or that operator's real credential at all). Built as a
  throwaway `backend/cmd/_verify_mintcookie` program, run once via the
  ephemeral Go container, then deleted before this session's diff was
  finalized — never committed.
- **The leak demonstration itself, before/after:**
  - *Before* (cited, not re-broken to re-prove): Session 10's own closeout
    evidence — the identical class of cookie, replayed against
    `http://localhost:8020/api/v1/targets` prior to this session's fix,
    returned real target data over plaintext.
  - *After* (this session, live): the same minted cookie against
    `GET https://localhost:8443/api/v1/targets` (positive control, proving
    the cookie is genuinely valid) → real `200` with real target JSON. The
    identical cookie against `GET http://localhost:8020/api/v1/targets`
    (the leak port) → real `404 page not found` — the operator route is
    not mounted on that listener at all, not merely rejecting the cookie.
  - Additional checks: `GET http://localhost:8020/health` → `200` (agent
    listener itself still alive); the same operator cookie presented to
    `GET http://localhost:8020/api/v1/agent/assignments` (agent route, no
    bearer token) → `401` (RequireAgent correctly ignores an
    operator-shaped credential — unrelated identity spaces, unaffected by
    this fix); an unrelated local service on the host happened to answer
    on port `8080` directly (`X-Powered-By: PHP/8.5.10` — not pulsewatch,
    confirming `backend`'s own operator listener is genuinely unreachable
    from the host, not merely quiet).
- **Real agent traffic, end-to-end, against the now-agent-only port:** a
  fresh agent seeded via `cmd/seed-agent` (real token, real `agents` row),
  then the real `cmd/agent` reference binary run against
  `PULSEWATCH_SERVER_URL=http://backend:8081` (the internal agent
  listener, reached from another container the same way a real remote
  agent reaches the published `8020`) — `docker compose logs backend`
  shows real `200`s for `GET /api/v1/agent/assignments` and
  `POST /v1/logs`; the seeded agent's `agents.last_heartbeat_at` in
  Postgres updated to a real, current timestamp, confirming the OTLP
  ingestion path actually wrote through. Also confirmed directly against
  the host-published `8020` with the same bearer token (`200`, real
  assignment list) and without one (`401`). The throwaway agent row and
  its plaintext token were deleted from `agents` after verification — test
  data, not a credential meant to persist.
- Confirmed `privacy-forge`, `laravel-consent-guard`, `bookslot`, and
  `lexicon` were not touched. ADR-0001–0004, `operatorauth`, `agentauth`,
  and every handler in `operatorapi`/`agentapi` were not reopened — only
  `backend/main.go`'s router *wiring* and `docker-compose.yml`'s port
  publication changed.

## Open questions and risks

- **R-003 (narrowed, unchanged in substance): `backend`'s agent-facing
  listener is still plain HTTP.** Now a pure agent-transport gap — no
  operator-session data is reachable through it at all (R-004, closed).
  Real exposure only once a real remote agent reports over an untrusted
  network — not yet the case for any agent registered today. See
  `10-risk-register.md`.
- **R-002 (carried forward, unchanged): the two `internal/alerting` test
  flakes from Session 9 were not investigated this session** — out of
  scope (this session's backend changes were routing-only, no
  `internal/alerting` code touched). See `10-risk-register.md`.
- **The self-signed cert / operator password-reset gap (Session 10)
  remain unchanged** — this session did not touch `operatorauth`'s login
  mechanism or provision any real operator account of its own.
- **`/slo` and `/incidents` are still unbuilt** — deliberately deferred
  again. This was originally slated as Session 11's own objective (per
  Session 10's handoff); it is now **Session 12's**, because a real,
  demonstrated security gap (the cross-port cookie leak) took priority
  over a planned feature session. Reprioritized, not dropped.

## Next recommended session

- Proposed session title: **Session 12 — FR-008's hourly rollup job +
  `GET /targets/{id}/slo`.** This is the same next dashboard-read piece
  Session 10's handoff proposed for "Session 11" before this session's
  security finding took priority — `05-api-contracts.md` already specifies
  it, per `04-data-model.md`'s already-designed `check_rollups_hourly`
  schema (no session has written to it yet).
  - Also worth a look if time allows (not the primary objective): R-002 if
    it recurs (`go test -p 1`, Session 7 precedent), and/or R-003 (routing
    `backend`'s agent-facing listener through Caddy too, or otherwise
    securing it — a real, scoped extension, but explicitly an
    ADR-0003-adjacent transport change, not a rewrite of the ADR itself).
  - Do **not** reopen ADR-0001–0004.
- Inputs required: this handoff; `10-risk-register.md` (R-002, R-003);
  `11-backlog.md`; `docs/architecture/openapi.yaml`'s `TargetSlo`/`Incident`
  schemas; `04-data-model.md`'s rollup/retention section.
- Expected deliverables: a real hourly rollup job writing real rows to
  `check_rollups_hourly`, and `/targets/{id}/slo` reading them per
  `05-api-contracts.md`'s already-specified contract — verified against the
  real stack (now reachable over real HTTPS with the cross-port leak
  closed, so the dashboard-facing half of this can be verified the same
  honest way this session's own work was).
- Definition of done: `check_rollups_hourly` has real rows written by a real
  hourly job (not a one-off manual computation), and `/slo` reads them
  exactly as specified — proven by real Postgres integration tests and a
  real `curl`/live-binary check, not description.

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Repository state:** branch `main`, unreleased (pre-v0.1.0). Architecture,
API contracts, environment/schema/CI baseline, the scheduler/leasing core,
real alert dispatch, real agent communication (ADR-0003), real operator
authentication and the full mutating/registration REST surface, the first
dashboard-read endpoint and SvelteKit screen, real persistent TLS
termination, and now **a structural split between the operator-facing and
agent-facing HTTP listeners** (this session) are all complete. `/slo`/
`/incidents` remain the largest open feature gap; R-003 (agent transport
still plain HTTP, narrowed this session) is the largest open deployment
gap.

**Problem being solved:** This developer maintains several self-hosted
flagship repositories (`privacy-forge`, `laravel-consent-guard`, `bookslot`,
`lexicon`), none of which currently has a real, live deployment, plus
pulsewatch's own future instance. There is no continuously running, honest
answer to "is it actually up right now, and did I meet my SLO" for whatever
of this does run. See `00-project-brief.md` for full framing.

**Users:** Single operator (this developer) plus the machine "Agent" role — v1 has no other identity (`02-requirements.md`).

**Current stack:**
- Frontend: SvelteKit (Svelte 5, runes) — unchanged this session.
- Backend: Go 1.25 (Gin); `github.com/jackc/pgx/v5` (`pgxpool`) — **changed
  this session**: `backend/main.go` now runs two `*gin.Engine`s / two
  `http.Server`s (`setupOperatorRouter`/`setupAgentRouter`,
  `operatorSrv`/`agentSrv`) instead of one shared router; no
  `operatorapi`/`agentapi` handler logic itself changed.
- Data: PostgreSQL (plain, TimescaleDB rejected) — no new migration this
  session; Redis (still not load-bearing for anything).
- Infra: Docker Compose — **changed this session**: `backend`'s published
  port moved from `8020:8080` to `8020:8081` (agent-only listener now),
  `8080` (operator-only listener) no longer published at all; healthcheck
  now checks both listeners. `Caddyfile` unchanged (already targeted
  `backend:8080`, which is exactly the operator listener post-split).
  GitHub Actions (unchanged). OpenTelemetry Collector (unchanged).
- Testing: `backend/main_test.go` — two health tests split per-router, two
  new regression tests (`TestOperatorRouterHasNoAgentRoutes`,
  `TestAgentRouterHasNoOperatorRoutes`) proving the split is structural.
  Go's `testing` package and Vitest themselves unchanged.

**Architecture decisions that must not be reversed:**
- Licence AGPL-3.0; Go + Gin / SvelteKit frozen; exactly two deep SDLC
  phases (Release & Deployment, Operations & Maintenance); exactly two new
  technologies (Go concurrency, OTel pipelines) — still at cap, this
  session added none (splitting `net/http` listeners in an existing Go
  process is not a new technology).
- ADR-0001 (Postgres row leasing), ADR-0002 (3-state alert-suppression
  machine), ADR-0003 (agent-initiated push), ADR-0004 (bounded worker pool)
  — untouched this session. The router split changes *transport/routing*,
  never ADR-0003's actual mechanism (assignment polling, OTLP ingestion,
  bearer-token auth all unchanged).
- **The stateless HMAC-signed operator-session cookie design
  (`05-api-contracts.md`) is unchanged.** This session did not touch
  `operatorauth`'s issuance/verification logic at all — the fix is
  entirely about *which listener* `operatorapi`'s routes are mounted on.
- The API contract in `docs/architecture/openapi.yaml` — unchanged this
  session.
- The real schema in `backend/migrations/` — unchanged this session.

**Implementation state:**
- Done: repository skeleton, licence, governance docs (Session 0);
  project brief/scope (Session 1); requirements (Session 2); architecture
  (Session 3); API contract (Session 3.5); Postgres schema, OTel Collector,
  CI baseline (Session 4); scheduler/leasing core (Session 5); alert
  dispatch (Session 6); agent communication (Session 7); operator-session
  authentication and the full mutating/registration operator-facing REST
  surface (Session 8); the first dashboard-read endpoint and SvelteKit
  screen (Session 9); real, persistent TLS termination via a Caddy reverse
  proxy (Session 10); **now a structural split between the operator-facing
  and agent-facing HTTP listeners, closing the cross-port session-cookie
  leak found at Session 10's own closeout (this session).**
- In progress: nothing mid-flight.
- Not started: `/slo` + the hourly rollup job (FR-008), `/incidents`, TLS in
  front of `backend`'s agent-facing listener (R-003, narrowed but still
  open), any real notification provider integration, the idempotency-key
  replay-cache mechanism, an operator password-reset/rotation path.

**Constraints and non-goals:**
- Max 2 new technologies, already at cap. v1 is single-operator; no
  multi-user auth/role separation or public status page. Full non-goals
  table in `01-scope-and-non-goals.md`.

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning, Requirements Analysis, Verification & Testing, Retirement & Handover
**Baseline-depth, real rigor on its own merits:** this session's own work
was entirely Release & Deployment / Operations & Maintenance depth — a
real demonstrated leak, a real structural fix (not a cosmetic check), real
build/test/live verification against the running stack, real agent traffic
proven unaffected. See `00a-ledger-confirmation.md`.

**Task for this session (single objective) — now complete:**
Eliminate the cross-port session-cookie exposure found closing out
Session 10, without redesigning ADR-0003's agent protocol. **Done — see
Work completed above.** No new deployment-config bugs were found this
session (unlike Session 10's three); the fix itself is the router split
described above, chosen over routing agent traffic through Caddy too for
the real cost reasons named in "Decisions made."

**Definition of done — met:**
- The demonstrated leak no longer reproduces — real before/after evidence
  (same minted cookie, real `200` over HTTPS, real `404` over the
  agent-only port) — see Verification performed above.
- Agent communication (ADR-0003) still works, demonstrated live — real
  seeded agent, real `cmd/agent` binary, real `200`s and a real
  `last_heartbeat_at` update in Postgres.
- `10-risk-register.md`'s R-003 updated (narrowed, corrected prior
  understatement) and a new R-004 opened-and-closed for the specific leak,
  matching R-001's own evidence standard.
- `08-deployment-and-operations.md` updated so the documented port/routing
  story matches the real, current `docker-compose.yml`.
- `docs/project-memory/` handoff (this file) updated for Session 12, naming
  explicitly that the SLO/rollup work originally slated as "Session 11" is
  now Session 12, and why.
- Local HEAD confirmed to match `origin/main` after commit/push — see the
  actual `git log`/`git status` proof pasted into this session's own
  closing message (not restated here, since this file is written before
  that final push step).

**Files to attach or paste for Session 12:**
- `10-risk-register.md` (R-002, R-003) and `11-backlog.md` — next-step
  candidates
- `docs/architecture/openapi.yaml` (`TargetSlo`/`Incident` schemas)
- `04-data-model.md` (`check_rollups_hourly`'s existing, unwritten-to
  schema)
- `08-deployment-and-operations.md` (now reflects the real two-listener
  routing story — for context on how to verify Session 12's work against
  the actual running stack)
- `docs/project-memory/12-session-handoff.md` (this file)

**Ground rules:** Do not change the stack. Do not introduce a third new
technology. Do not expand the deep-SDLC-phase count beyond two. Do not
reopen ADR-0001–0004 without new measured evidence per their own Revisit
triggers. Do not touch `privacy-forge`, `laravel-consent-guard`, `bookslot`,
or `lexicon`.
