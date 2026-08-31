# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed
- Session number and title: **Session 10 — Real TLS Termination.**
- Objective: add real, persistent TLS termination to the `docker-compose.yml`
  deployment shape so the operator-facing dashboard/login flow (Session 9)
  actually works end-to-end in a real browser, over a real HTTPS connection —
  replacing Session 9's temporary, torn-down self-signed harness, not
  supplementing it. No new application feature; deployment/proxy config and
  documentation only.
- Status: **complete.** R-001 closed with an honestly-named residual
  trade-off (self-signed cert); a distinct, separate residual gap (R-003,
  agent transport) opened rather than folded into R-001 or silently ignored.

## Work completed

### `Caddyfile` (new)
A `caddy:2-alpine` reverse proxy terminating HTTPS on `:443` for the site
address `localhost, 127.0.0.1` (not a bare `:443` — see "A real bug found
and fixed" below for why that distinction matters), routing `/api/*` to
`backend:8080` and everything else to `frontend:3000`. Uses `tls internal`
— Caddy's own local CA, persisted in the `caddy_data` volume so it survives
restarts. `auto_https disable_redirects` because no port-80 listener is
published (nothing to redirect from).

### `docker-compose.yml` (edited)
- New `proxy` service (`caddy:2-alpine`), publishing `8443:443`, mounting
  `./Caddyfile` and two new named volumes (`caddy_data`, `caddy_config`).
- `frontend` no longer publishes a host port (`ports: 3003:3000` →
  `expose: 3000`) — the browser now reaches it only through `proxy`, so
  there is exactly one TLS story, not a real one alongside a leftover plain-
  HTTP bypass.
- `frontend`'s `PUBLIC_API_URL` default changed from `http://localhost:8020`
  to `http://backend:8080` — see "A real bug found and fixed" below; this
  was necessary for the frontend's own server-side backend calls to work at
  all inside the real containerized stack, not a TLS-specific change.
- `backend`'s own host port (`8020:8080`) is unchanged — still plain HTTP,
  deliberately, for agent traffic (ADR-0003, untouched this session). See
  R-003.

### `.env.example` (edited)
`PUBLIC_API_URL`'s default and comment updated to match the
`docker-compose.yml` fix above, with an explanation of why it must be the
Docker-network backend address, not a host-facing URL (the browser never
calls it directly).

### `docs/project-memory/08-deployment-and-operations.md` (filled in for real)
Was an empty skeleton since Session 0. Now documents environments, the build
pipeline, the real deployment procedure (including bootstrapping the first
operator account via `cmd/provision-operator`, run from an ephemeral
`golang:1.25-alpine` container against the Compose network — no such
container is added to `docker-compose.yml` itself, matching the tool's own
one-time/bootstrap-only nature), and a full "TLS termination" section
covering the cert choice, why it's self-signed, what a real browser will
show, the real-domain/Let's Encrypt upgrade path, and the R-003 residual gap.

### `docs/project-memory/10-risk-register.md` (edited)
R-001 moved to "Closed risks" with the real resolution and verification
evidence cited. R-002 carried forward unchanged (untouched this session,
review date moved to Session 11). New R-003 opened: `backend`'s
agent-facing port remains plain HTTP — a real, distinct, separately-tracked
gap this session's TLS work does not cover, named rather than silently
folded into R-001's closure.

## Decisions made

- **`tls internal` (self-signed) was the deliberate, named choice — a real
  domain/Let's Encrypt cert was not something this session chose to pay for
  or set up.** The task's own ground rules explicitly permitted this as long
  as it's named honestly rather than faked as a "real" cert setup, and R-001
  is updated to say exactly that (mitigated, not silently closed as if no
  trade-off exists).
- **`backend`'s own agent-facing port stays plain HTTP, unchanged.** Routing
  agent traffic (ADR-0003) through TLS too would be a real, defensible next
  step, but it's an agent-transport change the task's ground rules excluded
  ("do not touch... agent auth... any ADR-governed subsystem"). Opened as
  R-003 instead of silently leaving it undocumented or conflating it with
  R-001's now-closed browser-cookie scope.
- **`frontend`'s host port was removed entirely, not left running alongside
  `proxy`.** The task's own framing ("this replaces, not supplements,
  Session 9's harness... don't leave two competing TLS setups") applies with
  equal force to "one real HTTPS path vs. a real HTTPS path plus a leftover
  plain-HTTP one" — a `curl http://localhost:3003` bypass sitting next to
  the new HTTPS entrypoint would undercut the same "actually operable" claim
  R-001 is about, even though today's app code would already refuse to set
  a `Secure` cookie back over that plain-HTTP path anyway.
- **`/api/*` is also routed through `proxy`, even though the browser never
  calls `backend` directly in this design** (`frontend/src/lib/server/
  backend.ts`'s own server-side calls stay on the internal Docker network,
  unaffected). Added because the task named "in front of `backend`/
  `frontend`" explicitly, and because it gives anyone using `curl` directly
  against the API the same real HTTPS story as the dashboard — genuinely
  useful, not scope creep, since it required no application code change.

## Real bugs found and fixed during this session's own verification

Per this session's own "bring up the full stack via `docker-compose.yml` as
a normal person would run it" standard — this uncovered that **`docker
compose up` against this repo's own `docker-compose.yml` had apparently
never actually been run successfully before** (every prior session's own
verification used ephemeral, hand-assembled containers instead — Session
9's own handoff says as much for its TLS harness, and the two bugs below
would have surfaced immediately on the very first real `docker compose up`
had one been attempted):

1. **A YAML parse error in `docker-compose.yml` itself**, pre-existing
   since Session 8: `SESSION_SIGNING_SECRET:
   ${SESSION_SIGNING_SECRET:?SESSION_SIGNING_SECRET is required — generate
   one with: openssl rand -base64 32}` — the unquoted colon after "with"
   inside the flow-scalar value is parsed by YAML as a second mapping
   separator, not a literal character (`go-yaml load error in scanner:
   mapping values are not allowed in this context`). `docker compose up`
   failed immediately, before creating a single container. Fixed by quoting
   the whole value.
2. **`frontend`'s `PUBLIC_API_URL` default (`http://localhost:8020`) was
   wrong for the real containerized stack.** `frontend/src/lib/server/
   backend.ts`'s server-side calls run *inside* the `frontend` container in
   this deployment shape, where `localhost` resolves to the `frontend`
   container itself, not `backend` — every backend call would have failed
   with a connection error the first time anyone actually ran the full
   stack via Compose (as opposed to Session 9's own verification, which ran
   `vite dev` directly on a host, where `localhost:8020` correctly reached
   the separately-exposed `backend` port). Fixed to `http://backend:8080`,
   the Docker-network address — the only address that's actually correct
   for a server-side call from inside that container, browser-facing or
   not.
3. **`Caddyfile`'s first draft used a bare `:443` site address with `tls
   internal`, which does not work** — confirmed by a full TLS handshake
   failure (`SSL alert number 80: internal error`, both from the host via
   Windows' Schannel and from another container on the same Docker network,
   ruling out a client-side/OS-networking explanation) and Caddy's own
   debug log: `"no certificate available for '<SNI>'"`. A portless/hostless
   site address gives `tls internal` no name to pre-provision a certificate
   for, so it has nothing to offer for any incoming SNI. Fixed by giving the
   site block explicit hostnames (`localhost, 127.0.0.1`) matching how this
   is actually reached.

None of these three are related to R-001's actual TLS-termination logic
once fixed — they're pre-existing or newly-introduced deployment-config bugs
this session's own "prove it against the real stack" standard was what
actually caught. Reported here rather than silently fixed and left
unmentioned, matching this project's own stated discipline (see e.g.
Session 9's `PUBLIC_API_URL` `$env/dynamic/public` finding, same class of
issue).

## Verification performed (all real, not description)

- **Full stack brought up via the actual, unmodified deployment path**:
  `docker compose up -d --build` from a clean checkout of this session's own
  `docker-compose.yml`/`Caddyfile`/`.env` (`.env` created from
  `.env.example` plus a freshly generated `SESSION_SIGNING_SECRET`, no
  secrets committed). All six services (`postgres`, `redis`, `migrate`,
  `otel-collector`, `backend`, `frontend`, `proxy`) started; `postgres`,
  `redis`, `backend`, `frontend` report `healthy`; `migrate` exited 0.
- **Real TLS handshake, not a stub**: `openssl s_client -connect
  localhost:8443 -servername localhost` completes a real TLSv1.3 handshake;
  `openssl x509 -noout -issuer` on the served certificate reads `Issuer:
  CN=Caddy Local Authority - ECC Intermediate` — a real, non-public-CA
  certificate, exactly the named self-signed trade-off, not a fake/expired/
  missing one. Restarting the `proxy` container and re-checking confirmed
  the same CA is reused (Caddy logged "root certificate is already trusted
  by system," not "installing root certificate") — the persistence claim
  R-001's fix depends on, not assumed.
- **First operator account bootstrapped for real**, via
  `backend/cmd/provision-operator` run from an ephemeral
  `golang:1.25-alpine` container attached to the Compose network (see
  `08-deployment-and-operations.md`'s deployment procedure) — this is the
  real, persistent `postgres_data` volume this Compose stack has always
  declared (confirmed pre-existing target rows from Sessions 8/9 were still
  present, dated `2026-08-29`/`2026-08-30` — this volume was *not* freshly
  created this session, contrary to Session 9's own note that "the real
  `docker-compose.yml` stack's own persistent volume was never started or
  touched" — apparently something did touch it between Session 9 and now,
  or that note was itself slightly imprecise; flagged here rather than
  silently smoothed over, though it doesn't change this session's own
  findings). **The operator password chosen for this account is not
  recorded in any committed file** (used only via shell command line this
  session) — see this session's own chat transcript / the human operator
  for the actual credential; `provision-operator` refuses to run again now
  that one exists, and no reset path exists yet (unchanged, known gap).
- **The full login → Secure cookie → persistence → logout round trip, over
  the real HTTPS connection, against the real running stack** (not a
  torn-down harness):
  - `POST https://localhost:8443/login` (with a matching `Origin` header —
    SvelteKit's own CSRF check requires this for any non-GET action, an
    unrelated real behavior this session's verification had to account for,
    not a bug) → real `Set-Cookie: pulsewatch_session=...; Max-Age=86399;
    Path=/; HttpOnly; Secure; SameSite=Strict`, captured by `curl`'s cookie
    jar (which honors `Secure` the same way a real browser does).
  - That cookie, replayed via a **separate, later `curl` invocation** (not
    the same connection or process — this is what actually distinguishes
    "persists across a reload" from "worked once, in-flight") against
    `GET /dashboard`, rendered the real dashboard HTML twice in a row,
    including real target rows and status badges (`Healthy`, `Alerting`)
    sourced from the real database — the specific claim R-001 raised
    ("dashboard would never actually stay authenticated") demonstrated
    false-when-fixed, not merely asserted.
  - Unauthenticated `GET /dashboard` (no cookie) → real `303` to `/login`.
  - `POST https://localhost:8443/dashboard?/logout` → real `Set-Cookie:
    pulsewatch_session=; Max-Age=0; ...`; a subsequent `GET /dashboard` with
    the now-cleared cookie jar → real `303` to `/login` again.
  - `GET https://localhost:8443/api/v1/targets` (unauthenticated, via the
    `/api/*` proxy path) → real `401` JSON from `backend` — confirming the
    proxy's path-based routing to `backend` works too, not just to
    `frontend`.
- **No screenshot was taken** (this environment has no GUI browser) — per
  the task's own verification standard, `curl -v`/`openssl s_client` output
  showing the real TLS handshake and `Set-Cookie: ...Secure...` actually
  being honored was used instead, which the task explicitly names as an
  acceptable alternative to a screenshot.
- Confirmed `privacy-forge`, `laravel-consent-guard`, `bookslot`, and
  `lexicon` were not touched. No backend or frontend Go/Svelte application
  code was changed this session — only `docker-compose.yml`, `Caddyfile`,
  `.env.example`, and `docs/project-memory/`. `internal/alerting`,
  `operatorauth`, `agentauth`, and ADR-0001–0004 were not reopened.

## Open questions and risks

- **R-003 (new, real, unresolved): `backend`'s agent-facing port
  (`8020:8080`) is still plain HTTP.** Deliberately out of this session's
  scope (agent transport is an ADR-0003-adjacent change). Real exposure
  only once an agent actually crosses a real network boundary — not yet the
  case for any agent registered today. See `10-risk-register.md`.
- **R-002 (carried forward, unchanged): the two `internal/alerting` test
  flakes from Session 9 were not investigated this session** — out of
  scope (no backend code touched). See `10-risk-register.md`.
- **The self-signed cert means every browser will show a security warning
  on first visit.** This is the named, accepted trade-off, not a defect —
  but it does mean this deployment isn't yet suitable for showing to anyone
  who isn't the operator themselves without also handing them the CA cert
  or the explanation. A real domain + Let's Encrypt (documented as the
  upgrade path in `08-deployment-and-operations.md`, not yet built or
  tested) is the fix if that ever matters.
- **No operator password-reset/rotation path exists** — unchanged from
  Session 8/9's handoffs, restated because this session made it concretely
  relevant by actually creating the one operator account that gap now
  applies to.
- **`/slo` and `/incidents` are still unbuilt** — unchanged from Session 9;
  this was a deployment-only session by design (Option A of Session 9's own
  proposed split), not a feature session.
- **The pre-existing `postgres_data` volume's history is slightly unclear**
  (see "Verification performed" above) — worth a future session confirming
  whether it's the same volume every session has been quietly accumulating
  state in, or something else; not a blocker, just an inconsistency in the
  record worth someone eventually resolving.

## Next recommended session

- Proposed session title: **Session 11 — FR-008's hourly rollup job +
  `GET /targets/{id}/slo`.** This is the next dashboard-read piece
  `05-api-contracts.md` already specifies, per `04-data-model.md`'s
  already-designed `check_rollups_hourly` schema (no session has written to
  it yet) — the same Option B Session 9's own handoff proposed, now that
  Session 10 has closed Option A (TLS) and the dashboard is reachable over
  real HTTPS to actually demo it against.
  - Also worth a look if time allows (not the primary objective): R-002 if
    it recurs (`go test -p 1`, Session 7 precedent), and/or R-003 (routing
    `backend`'s agent-facing traffic through TLS too — a real, scoped
    extension of this session's `Caddyfile` work, but explicitly an
    ADR-0003-adjacent transport change, not a rewrite of the ADR itself).
  - Do **not** reopen ADR-0001–0004.
- Inputs required: this handoff; `10-risk-register.md` (R-002, R-003);
  `11-backlog.md`; `docs/architecture/openapi.yaml`'s `TargetSlo`/`Incident`
  schemas; `04-data-model.md`'s rollup/retention section.
- Expected deliverables: a real hourly rollup job writing real rows to
  `check_rollups_hourly`, and `/targets/{id}/slo` reading them per
  `05-api-contracts.md`'s already-specified contract — verified against the
  real stack (now reachable over real HTTPS, so the dashboard-facing half of
  this, if any is added, can be verified the same honest way this session's
  own work was).
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
dashboard-read endpoint and SvelteKit screen, and now **real, persistent TLS
termination** (this session) are all complete. `/slo`/`/incidents` remain
the largest open feature gap; R-003 (agent transport still plain HTTP) is
the largest open deployment gap.

**Problem being solved:** This developer maintains several self-hosted
flagship repositories (`privacy-forge`, `laravel-consent-guard`, `bookslot`,
`lexicon`), none of which currently has a real, live deployment, plus
pulsewatch's own future instance. There is no continuously running, honest
answer to "is it actually up right now, and did I meet my SLO" for whatever
of this does run. See `00-project-brief.md` for full framing.

**Users:** Single operator (this developer) plus the machine "Agent" role — v1 has no other identity (`02-requirements.md`).

**Current stack:**
- Frontend: SvelteKit (Svelte 5, runes) — application code unchanged this
  session (`/login`, `/dashboard`); only its Compose deployment config
  (`PUBLIC_API_URL` default, host-port removal) changed.
- Backend: Go 1.25 (Gin); `github.com/jackc/pgx/v5` (`pgxpool`) — no
  application code changed this session.
- Data: PostgreSQL (plain, TimescaleDB rejected) — no new migration this
  session; Redis (still not load-bearing for anything).
- Infra: Docker Compose — **changed this session**: new `proxy` (Caddy)
  service, `frontend` host-port removed, `PUBLIC_API_URL` default fixed, a
  pre-existing YAML bug fixed. GitHub Actions (unchanged). OpenTelemetry
  Collector (unchanged).
- Testing: no new automated tests this session (deployment/infra only, no
  application code changed) — verification was live, against the real
  compiled Docker images and real Postgres, per this session's own
  verification standard (see above). Go's `testing` package and Vitest
  themselves unchanged.

**Architecture decisions that must not be reversed:**
- Licence AGPL-3.0; Go + Gin / SvelteKit frozen; exactly two deep SDLC
  phases (Release & Deployment, Operations & Maintenance); exactly two new
  technologies (Go concurrency, OTel pipelines) — still at cap, this
  session added none (Caddy is deployment/infra, not a new *application*
  technology in the sense this cap tracks — same category as Postgres/Redis
  themselves, not a third counted slot).
- ADR-0001 (Postgres row leasing), ADR-0002 (3-state alert-suppression
  machine), ADR-0003 (agent-initiated push), ADR-0004 (bounded worker pool)
  — untouched this session.
- **The stateless HMAC-signed operator-session cookie design
  (`05-api-contracts.md`) is unchanged.** This session did not touch
  `operatorauth`/`operatorapi` — R-001's fix was entirely a deployment/infra
  addition (a reverse proxy), exactly as prescribed, not a cookie redesign.
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
  screen (Session 9); **now real, persistent TLS termination via a Caddy
  reverse proxy in `docker-compose.yml`, with the operator-facing
  login/dashboard flow proven end-to-end over a real HTTPS connection
  against the actual running stack (this session).**
- In progress: nothing mid-flight.
- Not started: `/slo` + the hourly rollup job (FR-008), `/incidents`, TLS in
  front of `backend`'s agent-facing traffic (R-003), any real notification
  provider integration, the idempotency-key replay-cache mechanism, an
  operator password-reset/rotation path.

**Constraints and non-goals:**
- Max 2 new technologies, already at cap. v1 is single-operator; no
  multi-user auth/role separation or public status page. Full non-goals
  table in `01-scope-and-non-goals.md`.

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning, Requirements Analysis, Verification & Testing, Retirement & Handover
**Baseline-depth, real rigor on its own merits:** this session's own work
was entirely Release & Deployment / Operations & Maintenance depth — real
`docker compose up` against a real, previously-broken deployment path, real
bugs found and fixed as a direct result, real TLS/cookie verification
against the running stack, not description. See `00a-ledger-confirmation.md`.

**Task for this session (single objective) — now complete:**
Add real, persistent TLS termination to `docker-compose.yml` so the
operator-facing dashboard/login flow works end-to-end in a real browser,
replacing Session 9's torn-down harness. **Done — see Work completed
above.** Two real, pre-existing deployment bugs (a YAML parse error, a wrong
`PUBLIC_API_URL` default) and one real bug in this session's own first
Caddy config draft (bare `:443` + `tls internal`) were found and fixed
rather than routed around.

**Definition of done — met:**
- `docker-compose.yml` includes a real, working TLS-terminating reverse
  proxy as a normal part of bringing the stack up — proven by actually
  bringing the stack up via `docker compose up -d --build`, not description.
- `08-deployment-and-operations.md` documents the real setup, including an
  honest statement of the self-signed-cert choice and why.
- `10-risk-register.md`'s R-001 is closed with a real, honestly-scoped
  resolution (not silently closed); R-003 opened for the real residual gap
  this doesn't cover.
- Session cookie persistence demonstrated end-to-end with real evidence
  (separate-request replay, not same-connection assumption) — see
  Verification performed above.
- `docs/project-memory/` updated with this handoff for Session 11.

**Files to attach or paste for Session 11:**
- `10-risk-register.md` (R-002, R-003) and `11-backlog.md` — next-step
  candidates
- `docs/architecture/openapi.yaml` (`TargetSlo`/`Incident` schemas)
- `04-data-model.md` (`check_rollups_hourly`'s existing, unwritten-to
  schema)
- `08-deployment-and-operations.md` (now real — for context on how to
  verify Session 11's work against the actual running stack)
- `docs/project-memory/12-session-handoff.md` (this file)

**Ground rules:** Do not change the stack. Do not introduce a third new
technology. Do not expand the deep-SDLC-phase count beyond two. Do not
reopen ADR-0001–0004 without new measured evidence per their own Revisit
triggers. Do not touch `privacy-forge`, `laravel-consent-guard`, `bookslot`,
or `lexicon`.
