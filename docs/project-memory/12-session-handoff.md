# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed
- Session number and title: **Session 9 — Live Status Reads and the First
  Dashboard Screen.**
- Objective: build one real, read-only dashboard-status endpoint
  (`GET /api/v1/targets/{target_id}/status`) reusing Sessions 6/7's already-
  built `display_state` overlay, and one real, login-gated SvelteKit screen
  consuming it — nothing else (no SLO, no rollup job, no incidents endpoint,
  no write path, no new alert-machine states).
- Status: **complete**, with one real structural gap found and explicitly
  named rather than routed around (see "Open questions and risks").

## Work completed

### `backend/internal/operatorapi/status.go` (new)
`GetTargetStatus` is `GET /api/v1/targets/{target_id}/status`
(`05-api-contracts.md`/`openapi.yaml`'s `TargetStatus` schema, verbatim
field-for-field): a pure read joining `targets`/`target_schedule` for the
base row, calling `alerting.DisplayState` (ADR-0002 Consequences' read-time
staleness overlay — untouched, exactly as Session 7 left it) and
`agentauth.IsStale`/`FetchLiveness` (ADR-0003/NFR-008, also untouched) when
the target is agent-assigned, and reading `incidents` directly only when
`raw_state = "alerting"` for `open_incident`. No new computation was
written — this handler is entirely composition of functions Sessions 6/7
already built and tested; no ADR reopened. Wired into
`backend/internal/operatorapi/router.go` behind the same `RequireOperator`
middleware every other operator-facing route uses, and added to
`middleware_test.go`'s exhaustive unauthenticated-rejection audit table so
that table stays a genuine guarantee, not a snapshot that silently goes
stale as routes are added.

### `backend/internal/operatorapi/status_test.go` (new)
Four tests against real Postgres:
- `TestGetTargetStatus_ServerExecutedHealthyTarget` — a fresh target reads
  `healthy`/`healthy`, nil `agent_id`/`agent_stale`, no `open_incident`.
- `TestGetTargetStatus_UnknownIdReturnsNotFound`.
- `TestGetTargetStatus_OpenIncidentSurfaced` — a real `incidents` row with
  `raw_state='alerting'` is reflected in the response's `open_incident`.
- `TestGetTargetStatus_StaleAgentDisplaysUnknownWithoutMutatingRawState` —
  this session's own "prove it has teeth" case: a target assigned to a real
  agent whose `last_heartbeat_at` is pushed an hour into the past (report
  interval 60s, well past the `3×` staleness threshold) reads
  `display_state="unknown"` while `target_schedule`'s `state`/`streak`/
  `last_checked_at` are asserted byte-for-byte identical before and after
  the HTTP request — the direct, automated proof that the overlay never
  writes back over `raw_state`.

### `frontend/src/lib/server/backend.ts` (new)
The one module every backend call goes through — always server-side (a
`+page.server.ts` load function or form action), never from browser-side
`<script>`. Reads the backend's base URL from `PUBLIC_API_URL`
(docker-compose's existing var) via `$env/dynamic/public`, **not**
`$env/dynamic/private`, which SvelteKit's own env-module split explicitly
excludes any `PUBLIC_`-prefixed variable from regardless of where it's
read — a real bug this session hit and fixed during its own live
verification (see "Decisions made"). Carries `operatorSession` as a plain
`Cookie` header on the outbound backend request (`backendFetch`); parses
the real `Set-Cookie` the backend issues on login (`parseSessionCookie`,
via `res.headers.getSetCookie()`) rather than forwarding it to the browser
verbatim — the backend's cookie targets the backend's own origin, which the
browser in this design never talks to directly (see "Decisions made" on
why: no CORS, and the backend enables none by design).

### `frontend/src/routes/login/` and `frontend/src/routes/dashboard/` (new)
This repository's first application code beyond the Session 0 skeleton.
`login/+page.server.ts`'s form action calls the real `POST
/api/v1/auth/login`, and on success re-issues an identical-shaped
(`HttpOnly`, `Secure`, `SameSite=Strict`, matching `Max-Age`) cookie from
the frontend's own origin. `dashboard/+page.server.ts`'s load function
calls the real `GET /targets` then the real `GET /targets/{id}/status` per
target (unpaginated — `05-api-contracts.md`'s own bounded-scale reasoning
at this project's actual size, not a new bulk endpoint the spec doesn't
define) — a **401 from the backend**, not merely a missing cookie, is what
actually redirects to `/login`; gating is decided by Session 8's real auth
on every request, never assumed from cookie presence alone. A `logout`
form action calls the real `POST /api/v1/auth/logout` and clears the
frontend's own cookie. `frontend/src/routes/+page.svelte` was updated
(previously said "Session 0 — ... no monitoring UI yet," which is no
longer true) to link to `/login`.

### `frontend/eslint.config.js` (fixed — a real, previously-latent gap)
No `.svelte` file in this repository had ever used `lang="ts"` before this
session, so `svelte.configs['flat/recommended']`'s TypeScript-in-Svelte
parsing had never actually been exercised — it silently mis-parsed `import
type`/`$props()` type annotations as plain JS (`"Unexpected token
{"`, not a real lint finding). Fixed with the documented
`languageOptions.parserOptions.parser: tseslint.parser` mapping for
`**/*.svelte`. `npm run lint` is clean project-wide after the fix, not
merely on this session's own new files.

## Decisions made

- **The Secure-cookie/no-TLS gap was named to the user, not routed around.**
  `05-api-contracts.md`'s `operatorSession` cookie is `Secure` by design,
  but no prior session gave this project any TLS termination story —
  `docker-compose.yml` serves both `backend` and `frontend` over plain
  HTTP, and `08-deployment-and-operations.md`/`06-security-threat-model.md`
  are silent on it. A real browser refuses to store a `Secure` cookie
  received over `http://`, so this session's own verification standard
  ("prove it works in a real browser") could not be met against the stack
  as it stands. The task's own ground rules warned specifically against
  "temporarily" weakening auth to make a screen work — flipping `Secure` off
  would have been exactly that shortcut. Instead this was surfaced directly
  to the user as a genuine three-way choice (self-signed HTTPS for
  verification only / curl-only proof matching Session 8's own method / add
  a real reverse proxy to `docker-compose.yml` now); the user chose
  self-signed-HTTPS-for-verification-only. See "Verification performed" for
  what that looked like and "Open questions and risks" for the real gap
  this leaves for a deployment-focused session.
- **The frontend re-issues its own session cookie rather than forwarding
  the backend's `Set-Cookie` header verbatim.** The backend's cookie is
  scoped to the backend's own origin/port; in this design the browser only
  ever talks to the frontend (no CORS is enabled on the backend by design,
  matching `operatorapi/middleware.go`'s own reasoning — a direct
  browser-to-backend fetch across origins would have its response blocked
  from being read anyway). So the frontend's `+page.server.ts` action reads
  the backend's `Set-Cookie` server-side (a plain Node `fetch`, not subject
  to browser cookie policy), extracts the token value and `Max-Age`, and
  sets its own cookie — same name, value, and lifetime, different origin.
  This is the standard SSR/BFF pattern for exactly this shape of API, not a
  deviation from Session 8's cookie design.
- **`PUBLIC_API_URL` is read via `$env/dynamic/public`, not
  `$env/dynamic/private` — found the hard way, during this session's own
  live verification, not by reading docs first.** SvelteKit's env-module
  split partitions by variable name prefix regardless of which module reads
  it: `$env/dynamic/private` explicitly *excludes* any variable starting
  with the configured public prefix (`PUBLIC_` by default), even when the
  code reading it never exposes the value to the client. The first
  attempt used `$env/dynamic/private` (reasoning: "this is read server-side
  only, so private is more correct") and silently fell through to the
  hardcoded fallback URL every time, producing a same-shaped
  `ECONNREFUSED` failure that took real debugging (direct `/proc/<pid>/environ`
  inspection to first rule out an environment-propagation problem) to find.
  Fixed by importing from `$env/dynamic/public` instead — the value still
  never reaches client-side code, since this module is never imported from
  one.
- **No ADR reopened, no new status computation written.** `GetTargetStatus`
  calls `alerting.DisplayState`/`agentauth.IsStale`/`FetchLiveness` exactly
  as they exist — Session 7 left them "ready and untouched" for exactly this
  use, and they needed no change to be reused from HTTP. This is the
  positive case `12-session-handoff.md` (Session 8's own version) flagged
  as worth reporting if it turned out otherwise; it did not turn out
  otherwise.
- **Dashboard scope held to exactly what the task named.** No SLO/uptime
  numbers, no historical charts, no alert acknowledgment/silencing, no new
  target/agent onboarding UI, no styling beyond legible-and-functional. The
  "Next recommended session" list below is deliberately the same shape
  Session 8's own handoff proposed for status/SLO/incidents, now narrowed
  to what's left after this session closed the status piece.

## Verification performed (all real, not description)

- **The new endpoint, proven against real Postgres**
  (`backend/internal/operatorapi/status_test.go`, described above under
  "Work completed") — all four new tests pass.
- **`TestEveryOperatorFacingEndpoint_RejectsUnauthenticatedRequests`**
  (`middleware_test.go`) extended with the new route and still passes,
  keeping that audit table exhaustive against `router.go`.
- **Full backend suite**: `go test ./... -race -v` via an ephemeral
  `golang:1.25` container against a real, isolated `postgres:16-alpine`
  container (this environment has no native Go toolchain, matching every
  prior session) — **113 passed, 2 failed** on the first run.
  The 2 failures (`internal/alerting`'s `TestRecordCheckResult_DuplicateCheckedAtIsANoOp`/
  `_ThresholdCrossingOpensIncidentDispatch`) are in a package this session
  never touched; re-run in isolation (`-run TestRecordCheckResult -count=3`)
  passed cleanly nine times running. This reads as shared-host test
  contention under this verification host's full concurrent `-race` load
  against one small Postgres container — the same class of finding
  Session 7's own evidence entry already documented for a different test
  file — not a regression this session introduced or a confirmed code
  defect. Reported as a real, open risk (`10-risk-register.md` R-002), not
  silently ignored or "fixed" by touching `internal/alerting` out of scope.
  `go vet ./...`: clean.
- **A real, previously-latent bug found and fixed during live
  verification, unrelated to the dashboard's own logic**: the demo/test
  Postgres instance used for the full `go test ./...` run above accumulates
  orphaned `targets`/`target_schedule`/`incidents` rows across sessions,
  because several packages' own `t.Cleanup` deletes are best-effort and
  silently no-op when a plain `REFERENCES` FK (no `ON DELETE CASCADE`)
  blocks the delete. Discovered when this session's first live dashboard
  screenshot showed dozens of unrelated "Alerting" targets from the test
  suite's own leftover fixtures. Not a pulsewatch code bug (this is a
  documented, accepted test-cleanup looseness — see e.g.
  `internal/alerting/testdb_test.go`'s own comment on it) — worked around
  for this session's own live demo by using a second, freshly-migrated
  Postgres instance never touched by `go test`, not by changing any
  cleanup code. Noted here so a future session isn't surprised by the same
  thing.
- **The live status/staleness/no-mutation proof, against the actual
  compiled binary and real Postgres** (`backend/Dockerfile`'s image, run as
  its own container against the second, clean Postgres instance above): a
  real target assigned to a real agent, `target_schedule` set to
  `state='suspect', streak=2` and the agent's `last_heartbeat_at` pushed an
  hour into the past via direct SQL — `SELECT state, streak, last_checked_at
  FROM target_schedule` read byte-for-byte identical before and after `curl
  GET /targets/{id}/status`, while the response itself showed
  `display_state="unknown"`, `raw_state="suspect"` (verbatim, unmutated),
  `agent_stale=true`. A second real target (server-executed, pointed at a
  nonexistent domain) was left running against the actual scheduler inside
  the compiled binary and organically crossed its real failure threshold
  into a real `alerting` state with a real open incident during
  verification — the dashboard is rendering genuinely live,
  scheduler-computed state, not a canned fixture. Unauthenticated `GET
  /targets/{id}/status` → real `401`, both directly against the backend and
  through the frontend's own gated `/dashboard` route (→ real `303` to
  `/login`).
- **The dashboard screen, end-to-end, over a real HTTPS connection** — the
  method the user chose for the Secure-cookie gap above: a locally
  generated self-signed certificate, a `caddy:2-alpine` TLS terminator
  reverse-proxying to the actual `vite dev` process (nothing committed to
  `docker-compose.yml`/`.env.example`; this verification infrastructure —
  the cert, the Caddy container, the two extra Postgres/backend instances —
  was fully torn down at the end of the session). Driven with `curl`'s own
  cookie jar (which does honor the `Secure` attribute the same way a
  browser does): `POST /login` over `https://` → a real `Set-Cookie:
  pulsewatch_session=...; HttpOnly; Secure; SameSite=Strict` issued by the
  frontend's own origin; that cookie replayed on `GET /dashboard` renders
  the real target rows with correct status badges (`Alerting`, `Unknown
  (agent stale)`); `POST /dashboard?/logout` clears it (`Max-Age=0`) and a
  subsequent `/dashboard` request redirects to `/login` again;
  unauthenticated `GET /dashboard` also redirects to `/login` (a real
  `303`, not a client-side-only hide).
- **Frontend's own checks**: `npm run test` (1 passed, pre-existing health
  test), `npm run check` (215 files, the one remaining finding —
  `vite.config.ts`'s vitest `test` key — predates this session and is
  unrelated to any file touched here), `npm run lint` (clean project-wide
  after the `eslint.config.js` fix above).
- Confirmed `privacy-forge`, `laravel-consent-guard`, `bookslot`, and
  `lexicon` were not touched. All of this session's own verification
  infrastructure (two isolated Postgres containers, a dedicated Docker
  network, a locally-built `pulsewatch-backend-test` image, Go module/build
  cache volumes, the Caddy TLS terminator and its self-signed cert, the
  `vite dev` process) was created fresh for this session and fully removed
  at the end — the real `docker-compose.yml` stack's own persistent volume
  was never started or touched.

## Open questions and risks

- **R-001 (new, real, unresolved): no TLS termination story exists for
  this project.** See "Decisions made" above and `10-risk-register.md`.
  This is a genuine blocker for the operator-facing product being usable
  as currently deployable — the `Secure` session cookie cannot work in a
  real browser against a plain-HTTP deployment. Needs a real
  deployment-focused session (reverse proxy in `docker-compose.yml`, real
  or self-signed cert story documented in `08-deployment-and-operations.md`),
  not a code change to `operatorauth`/`operatorapi`.
- **R-002 (new, real, unresolved): 2 pre-existing `internal/alerting` tests
  failed once under this session's own full-suite `-race` run, passed
  clean in isolation.** See `10-risk-register.md`. Not fixed here (out of
  this session's scope), not silently ignored either.
- **The dashboard-read surface still has `/slo` and `/incidents`
  unbuilt.** This session closed `/status` only, per its own bounded
  scope. `/slo` still needs FR-008's hourly rollup job (no session has
  built it yet); `/incidents` is a simple direct `incidents` read, no new
  computation, same shape as this session's `/status` handler.
- **No idempotency-key replay-cache mechanism exists** — unchanged from
  Session 8's handoff, still a real, examined scope trim, not an oversight.
- **No operator password-reset/rotation path exists** — unchanged from
  Session 8's handoff.
- **`docker-compose.yml`'s `otel-collector-config.yaml` integration for the
  reference agent (flagged since Session 7) remains unverified** —
  unrelated to this session's scope, restated only so it isn't lost across
  handoffs.

## Next recommended session

- Proposed session title: **Session 10 — TLS/Deployment Story, or SLO +
  Rollup Job (pick one; do not silently do both half-finished).**
  Two real candidates, deliberately not combined into one session:
  - **Option A — close R-001**: a real reverse proxy (e.g. Caddy) added to
    `docker-compose.yml` with a documented cert story (self-signed for
    self-hosted default, real-cert instructions for anyone exposing this
    publicly), `08-deployment-and-operations.md`'s currently-empty TLS
    section filled in for real. This unblocks the dashboard actually being
    usable in a real deployment, not just in a torn-down verification
    harness.
  - **Option B — FR-008's hourly rollup job + `GET /targets/{id}/slo`**:
    the next dashboard-read piece `05-api-contracts.md` already specifies,
    per `04-data-model.md`'s already-designed `check_rollups_hourly`
    schema (no session has written to it yet).
  - Either way: investigate R-002 if it recurs (try `go test -p 1` first,
    per the Session 7 precedent already in `docs/SDLC-EVIDENCE.md`).
  - Do **not** reopen ADR-0001–0004.
- Inputs required: this handoff; `10-risk-register.md` (R-001/R-002);
  `11-backlog.md` (B-001/B-002/B-003); for Option B,
  `docs/architecture/openapi.yaml`'s `TargetSlo`/`Incident` schemas and
  `04-data-model.md`'s rollup/retention section.
- Expected deliverables: either a real, documented TLS/reverse-proxy
  deployment story, or a real rollup job + `/slo` endpoint — not a partial
  version of both.
- Definition of done (Option A): an operator can reach the dashboard over
  a real HTTPS connection from `docker compose up`, with the cert story
  documented, not just proven in a session-local, torn-down harness.
  Definition of done (Option B): `check_rollups_hourly` has real rows
  written by a real hourly job, and `/slo` reads them per
  `05-api-contracts.md`'s already-specified contract.

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Repository state:** branch `main`, unreleased (pre-v0.1.0). Architecture,
API contracts, environment/schema/CI baseline, the scheduler/leasing core,
real alert dispatch, real agent communication (ADR-0003), real operator
authentication and the full mutating/registration REST surface, and now
the first dashboard-read endpoint (`/status`) plus the first real
SvelteKit screen are all complete. `/slo`/`/incidents` and a real TLS
deployment story remain the two largest open gaps.

**Problem being solved:** This developer maintains several self-hosted
flagship repositories (`privacy-forge`, `laravel-consent-guard`, `bookslot`,
`lexicon`), none of which currently has a real, live deployment, plus
pulsewatch's own future instance. There is no continuously running, honest
answer to "is it actually up right now, and did I meet my SLO" for whatever
of this does run. See `00-project-brief.md` for full framing.

**Users:** Single operator (this developer) plus the machine "Agent" role — v1 has no other identity (`02-requirements.md`).

**Current stack:**
- Frontend: SvelteKit (Svelte 5, runes) — now has real application code:
  `/login` and `/dashboard`, both server-rendered, gated by a real
  frontend-issued session cookie carrying Session 8's backend session
  token. No new npm dependency added this session.
- Backend: Go 1.25 (Gin); `github.com/jackc/pgx/v5` (`pgxpool`) — no new
  dependency this session; `GetTargetStatus` uses only existing packages.
- Data: PostgreSQL (plain, TimescaleDB rejected) — no new migration this
  session (`/status` reads existing columns only); Redis (still not
  load-bearing for anything).
- Infra: Docker Compose — **unchanged this session** (the TLS verification
  harness used a separate, temporary Caddy container never added to
  `docker-compose.yml` — see R-001). GitHub Actions (unchanged). OpenTelemetry
  Collector (unchanged).
- Testing: Go's `testing` package (`operatorapi/status_test.go` is new, 4
  tests); Vitest (frontend, still just the one pre-existing health test —
  this session's new SvelteKit routes were verified by real HTTP
  request/response against a real running dev server, not new unit tests,
  matching the task's own "prove it against real data" verification
  standard rather than adding component-test scaffolding this session
  wasn't asked to build).

**Architecture decisions that must not be reversed:**
- Licence AGPL-3.0; Go + Gin / SvelteKit frozen; exactly two deep SDLC
  phases (Release & Deployment, Operations & Maintenance); exactly two new
  technologies (Go concurrency, OTel pipelines) — still at cap, this
  session added none.
- ADR-0001 (Postgres row leasing), ADR-0002 (3-state alert-suppression
  machine), ADR-0003 (agent-initiated push), ADR-0004 (bounded worker pool)
  — untouched this session.
- **The stateless HMAC-signed operator-session cookie design
  (`05-api-contracts.md`) is unchanged.** The frontend's own cookie (see
  "Decisions made") is a new, separate cookie on a different origin
  carrying the same token value — it does not replace, weaken, or bypass
  the backend's own session mechanism. Do not add a session table or
  Redis-backed session store without a new, real measured reason.
- The API contract in `docs/architecture/openapi.yaml` — unchanged this
  session; `/status` is now genuinely implemented exactly as specified.
- The real schema in `backend/migrations/` — unchanged this session.

**Implementation state:**
- Done: repository skeleton, licence, governance docs (Session 0);
  project brief/scope (Session 1); requirements (Session 2); architecture
  (Session 3); API contract (Session 3.5); Postgres schema, OTel Collector,
  CI baseline (Session 4); scheduler/leasing core (Session 5); alert
  dispatch (Session 6); agent communication (Session 7); operator-session
  authentication and the full mutating/registration operator-facing REST
  surface (Session 8); **now the first dashboard-read endpoint
  (`GET /targets/{id}/status`) and the first real SvelteKit screen
  (login + dashboard), proven end-to-end including the staleness-overlay
  no-mutation property, both via Go integration tests and live
  verification against the actual compiled binary and a real HTTPS
  connection (this session).**
- In progress: nothing mid-flight.
- Not started: `/slo` + the hourly rollup job (FR-008), `/incidents`, any
  real TLS/reverse-proxy deployment story (R-001), any real notification
  provider integration, the idempotency-key replay-cache mechanism, an
  operator password-reset/rotation path.

**Constraints and non-goals:**
- Max 2 new technologies, already at cap. v1 is single-operator; no
  multi-user auth/role separation or public status page. Full non-goals
  table in `01-scope-and-non-goals.md`.

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning, Requirements Analysis, Verification & Testing, Retirement & Handover
**Baseline-depth, real rigor on its own merits:** Architecture & Design;
Implementation is baseline depth overall, but this session's work — like
every prior implementation session — was verified by actual execution,
real Postgres integration tests, and a real end-to-end proof against the
actual compiled binary and a real HTTPS connection, not description. See
`00a-ledger-confirmation.md`.

**Task for this session (single objective) — now complete:**
Build one real, read-only dashboard-status endpoint reusing the existing
`display_state` overlay, and one real, login-gated SvelteKit screen
consuming it. **Done — see Work completed above.** One real structural
gap (R-001, the Secure-cookie/no-TLS mismatch) was found and named
explicitly rather than routed around, per the task's own ground rules.

**Definition of done — met:**
- Real Gin endpoint returning real target status from Postgres via the
  existing overlay — proven by Go integration tests and live `curl`
  against the actual compiled binary.
- Real SvelteKit page rendering that data, reachable only when
  authenticated — proven end-to-end over a real HTTPS connection (the
  method the user chose for the Secure-cookie gap), including
  unauthenticated rejection and logout.
- Demonstrated, with actual before/after DB state (both via Go test
  assertions and live `psql` queries), that staleness display doesn't
  corrupt `raw_state`.
- `docs/SDLC-EVIDENCE.md`, this handoff, `10-risk-register.md`, and
  `11-backlog.md` are updated with real evidence citations and the two
  real findings (R-001, R-002), handing off to Session 10.

**Files to attach or paste for Session 10:**
- `10-risk-register.md` (R-001, R-002) and `11-backlog.md` (B-001, B-002,
  B-003) — the concrete next-step candidates
- `docs/architecture/openapi.yaml` (`TargetSlo`/`Incident` schemas, for
  Option B)
- `04-data-model.md` (`check_rollups_hourly`'s existing, unwritten-to
  schema, for Option B)
- `08-deployment-and-operations.md` (currently an empty skeleton — for
  Option A)
- `docs/project-memory/12-session-handoff.md` (this file)

**Ground rules:** Do not change the stack. Do not introduce a third new
technology. Do not expand the deep-SDLC-phase count beyond two. Do not
reopen ADR-0001–0004 without new measured evidence per their own Revisit
triggers. Do not reopen the stateless-session-cookie design
(`05-api-contracts.md`) without a new, real measured reason — R-001's fix
is a deployment/infra addition (a reverse proxy), not a cookie redesign.
Do not touch `privacy-forge`, `laravel-consent-guard`, `bookslot`, or
`lexicon`.
