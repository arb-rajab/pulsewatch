# Deployment and Operations
> Purpose: how this runs, and how someone else keeps it running
> Project: pulsewatch (public)
> Last updated: 2026-08-31

## Environments
Single environment today: local/self-hosted, brought up via `docker compose
up` from the repo root. No staging/production split exists yet — v1 is a
single-operator, self-hosted product (`01-scope-and-non-goals.md`); this is
the same instance an operator would run on their own machine or server.

## Build and release pipeline
`backend/Dockerfile` (multi-stage Go 1.25 build → `alpine:3.20` runtime) and
`frontend/Dockerfile` (multi-stage Node 22 build → Node 22 runtime) are built
by `docker compose build`/`up --build`. No CD pipeline publishes images
anywhere yet — GitHub Actions (`.github/`) runs tests/lint only.

## Deployment procedure
1. `cp .env.example .env`, fill in `SESSION_SIGNING_SECRET` (`openssl rand
   -base64 32` — the backend refuses to start without it) and any other
   values you want to override.
2. `docker compose up -d --build`.
3. Bootstrap the first (and, per v1's single-operator design, only) operator
   account — there is no API endpoint that could do this, since every
   operator-facing endpoint requires an operator session already:
   ```
   echo 'a-real-password' | docker run --rm -i \
     --network pulsewatch_default \
     -v "$(pwd)/backend:/app" -w /app \
     -e DATABASE_URL="postgres://pulsewatch:pulsewatch@postgres:5432/pulsewatch?sslmode=disable" \
     golang:1.25-alpine sh -c 'go run ./cmd/provision-operator -email you@example.com'
   ```
   (`backend/cmd/provision-operator`'s own package doc explains why this
   isn't shipped as a container of its own yet, and why there is no
   password-reset path — a known, named gap, not an oversight.) This only
   ever needs to run once: it refuses if an operator already exists.
4. Open `https://localhost:8443` (or whatever host/port you've mapped
   `proxy`'s `8443:443` to) and log in.

## TLS termination (R-001 — real, as of this session)

**What changed:** a `proxy` service (`caddy:2-alpine`) was added to
`docker-compose.yml`, configured by the checked-in `./Caddyfile`, and is now
the *only* way a browser reaches this deployment. `frontend` no longer
publishes a host port at all (`expose: 3000`, internal-only) — there is no
plain-HTTP path to the login/dashboard flow left to accidentally use instead
of the real one. `backend` still publishes its own host port (`8020:8080`)
in plain HTTP; that is unchanged and intentional (see "What didn't change /
residual gap" below).

**Routing:** `Caddyfile` sends `/api/*` to `backend:8080` and everything else
to `frontend:3000`, both reachable through the same `https://localhost:8443`
origin. The browser only ever actually calls the `frontend` path in this
design — `frontend/src/lib/server/backend.ts`'s own server-side calls to the
backend stay on the plain-HTTP internal Docker network, which never needs
TLS — but the `/api/*` route is wired up too, so a real HTTPS URL exists for
anyone who wants to call the API directly (e.g. with `curl`) with the same
cert story as the dashboard, matching this session's own task framing of
"terminating TLS in front of `backend`/`frontend`."

**The cert choice — explicit, named, and why:** this deployment uses
`tls internal` — Caddy's own local self-signed CA, generated once and
persisted in the `caddy_data` volume (so it survives `docker compose
restart`/`up` without being regenerated; verified this session by
restarting the `proxy` container and confirming Caddy logged "root
certificate is already trusted by system" rather than minting a new one).
This is a **deliberate, accepted trade-off for the self-hosted/local default
this project ships as**, not an oversight or a placeholder: a real domain
and a public CA (Let's Encrypt) were not something this session chose to
pay for or set up, and R-001 explicitly permits naming that choice rather
than silently faking a "real" cert setup. The honest consequence: **a real
browser will show a not-trusted/private-connection warning** the first time
it hits `https://localhost:8443`, because "Caddy Local Authority" is not a
publicly trusted root (confirmed this session via `openssl x509`: `Issuer:
CN=Caddy Local Authority - ECC Intermediate`). That warning is the expected,
accepted state for this deployment shape — not a bug to silently work
around by disabling `Secure` or weakening the cookie. An operator who
clicks through it once (or imports Caddy's root CA, printed to the `proxy`
container's own logs / found at `/data/caddy/pki/authorities/local/root.crt`
inside the volume) gets a normal green-padlock experience after that, on
that machine.

Each individual leaf certificate `tls internal` issues is short-lived (12h,
Caddy's own internal-issuer default) — Caddy renews it automatically from
the same persisted local CA well before expiry; this is normal operation,
not a sign of instability, and requires no operator action.

**If you want a real, publicly trusted certificate instead** (e.g. exposing
this on a real domain rather than `localhost`): replace `Caddyfile`'s
`localhost, 127.0.0.1 { tls internal ... }` site block's address with your
real domain and drop the `tls internal` line entirely — Caddy's automatic
HTTPS will then request a real Let's Encrypt certificate for that domain on
first request (needs port 80/443 reachable from the internet for the ACME
HTTP-01 challenge, and `auto_https disable_redirects` at the top of the file
removed so the plain-HTTP→HTTPS redirect Let's Encrypt's own challenge flow
expects can work). This project has not built or tested that path this
session — it's the documented upgrade route, not a claim that it's already
verified working.

**What didn't change / residual gap — named, not silently left implicit:**
`backend`'s own directly-published port (`8020:8080`) is still plain HTTP.
This is intentional and out of this session's scope: agents authenticate to
`backend` directly (`GET /api/v1/agent/assignments`, OTLP ingestion —
ADR-0003), and changing that transport is an agent-facing change this
session's own ground rules excluded ("do not touch... agent auth... any
ADR-governed subsystem"). It is a real, distinct residual gap from R-001
(which was specifically about the *browser-facing* `Secure` operator-session
cookie, now fixed) — tracked separately, see `10-risk-register.md` R-003.

## Migration and rollback procedure
`migrate` (image `migrate/migrate:v4.19.1`) runs `backend/migrations` against
`postgres` on every `docker compose up`, before `backend` starts
(`depends_on: condition: service_completed_successfully`). No rollback
tooling beyond `migrate ... down` exists; not exercised this session (no
new migration was added).

## Configuration and secrets
See `.env.example` for the full list. `SESSION_SIGNING_SECRET` is the one
required-with-no-default secret (backend refuses to start without it, by
design — see docker-compose.yml's own comment on it). TLS/proxy config lives
in `./Caddyfile`, not `.env` — there's nothing secret in it (`tls internal`
needs no credentials).

## Observability: logs, metrics, traces, health checks
`docker compose logs <service>` for each service, including `proxy` (Caddy's
own structured JSON access/error log). OTel Collector unchanged this
session. `backend`/`frontend` both still expose `/health`, reachable
directly on their own ports and through the proxy.

## Dashboards and alerts (each links a runbook)
The operator dashboard (`/dashboard`, Session 9) is the first read surface;
now reachable over real HTTPS as of this session. No alert-routing
dashboards exist yet.

## Runbooks
None written yet.

## Backup and restore (last verified: NEVER — update this)
Not built this session — `postgres_data` is a named Docker volume with no
backup procedure yet.

## Capacity and cost notes
Single-host `docker compose` deployment; no capacity planning done yet.
