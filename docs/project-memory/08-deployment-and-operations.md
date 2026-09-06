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

## TLS termination (R-001 — real, as of Session 10) and the operator/agent listener split (R-004 — Session 11)

**What changed (Session 10):** a `proxy` service (`caddy:2-alpine`) was added
to `docker-compose.yml`, configured by the checked-in `./Caddyfile`, and is
now the *only* way a browser reaches this deployment. `frontend` no longer
publishes a host port at all (`expose: 3000`, internal-only) — there is no
plain-HTTP path to the login/dashboard flow left to accidentally use instead
of the real one.

**What changed (Session 11 — closing the cross-port session-cookie leak
found at Session 10's own closeout):** closing out Session 10, a live test
showed that `backend`'s directly-published agent port did not actually
scope out operator traffic the way R-001's and R-003's own text claimed —
`agentapi` and `operatorapi` were registered on the identical `gin.Engine`/
`http.Server` (`backend/main.go`), so a real operator's `Secure`,
`HttpOnly` `pulsewatch_session` cookie, issued over `https://localhost:8443`,
was accepted in cleartext by that port and returned real target data.
`Secure` did not help: `localhost`/loopback is treated as a trustworthy
origin regardless of scheme, and `Path`/`Domain` cookie attributes don't
carry port scoping either (RFC 6265). See R-004 (closed) in
`10-risk-register.md` for the full before/after evidence.

**The fix — split the routers, not the TLS story:** `backend/main.go` now
builds two separate `*gin.Engine`s and runs two independent `http.Server`s
inside the single `backend` process/container:

- `operatorSrv` (`setupOperatorRouter`, container port `8080`) — every
  `operatorapi` route (session login/logout, targets, alert channels,
  agents), gated by `RequireOperator`. **Not published as a host port at
  all** — reachable only over the internal Docker network, by `proxy`/Caddy
  (`/api/*` → `backend:8080`, unchanged from Session 10) or another
  container.
- `agentSrv` (`setupAgentRouter`, container port `8081`) — every `agentapi`
  route (`GET /api/v1/agent/assignments`, `POST /v1/logs`), gated by
  `RequireAgent`. **This is the one published as a host port**
  (`docker-compose.yml`: `8020:8081` — the external `8020` address is
  unchanged from every prior session's docs/README, only its internal
  container-port target moved from `8080` to `8081`).

No `operatorapi` route is registered on `agentSrv`, and no `agentapi` route
is registered on `operatorSrv` — this is a structural absence, not a check
that happens to reject a replayed cookie (`backend/main_test.go`:
`TestOperatorRouterHasNoAgentRoutes`/`TestAgentRouterHasNoOperatorRoutes`
assert the routes are genuinely unmounted). A session cookie replayed
against `8020` now gets a real `404 page not found` — there is no route
there for it to be rejected *by*, because no operator-facing code is
reachable through that listener under any circumstance.

**Why this option, not "route agent traffic through Caddy too" (R-003's own
suggested alternative mitigation):** that alternative would also work, but
costs more for what this session actually needed. It would require (a)
removing `backend`'s direct host port entirely so *all* traffic, agent
included, funnels through `proxy`, and (b) the reference agent
(`backend/cmd/agent`) trusting Caddy's `tls internal` self-signed CA — fine
for this same-host `docker compose` deployment, but a real, undischarged
cost for this developer's actual stated agent-deployment model
(`00-project-brief.md`: agents on `privacy-forge`/`laravel-consent-guard`/
`bookslot`/`lexicon`'s own separate hosts), which would need that CA
certificate distributed to every such host — a cert-distribution problem
this session did not want to take on silently. The router split achieves
this session's actual, bounded objective (operator cookies can never reach
`operatorapi` over `8020`) with no agent-binary change and no new
cross-host trust story, at the cost of leaving R-003's narrower remaining
gap (agent bearer tokens/telemetry still plaintext) open for a future
session to pick up, named honestly rather than silently folded into this
one's "done."

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
`backend`'s agent-facing listener (published as `8020:8081`) is still plain
HTTP. This is intentional and out of Session 11's scope too: agents
authenticate to `backend` directly (`GET /api/v1/agent/assignments`, OTLP
ingestion — ADR-0003), and changing that transport is an agent-facing
change both sessions' ground rules excluded ("do not touch... agent
auth... any ADR-governed subsystem"). Unlike before Session 11, this is now
a *pure* agent-transport gap — no operator-session data is reachable
through this listener at all (R-004, closed) — tracked as R-003's own
narrowed scope in `10-risk-register.md`.

## Kubernetes (production deployment target — ADR-0005, Session 14)

Additive to, not a replacement for, the Docker Compose path above — see
ADR-0005 for the full reasoning behind adding it and
`../adr/ADR-0005-iac-terraform-and-kubernetes-deployment.md` for the
options considered. Files: `infra/terraform/` (platform layer) and
`deploy/k8s/` (application workload, Kustomize-based). Each directory's
own `README.md` has the mechanical how-to; this section is the
design-level contract — what "safe rollout while continuing to monitor"
actually means for this system, per `00a-ledger-confirmation.md`'s own
justification for Release & Deployment being a deep phase.

### Environments

Same single-environment reality as Compose: one operator, one real
deployment. Kubernetes does not introduce a staging/production split this
project doesn't otherwise need — it introduces a second *kind* of
deployment target (a real orchestrator instead of a single host running
`docker compose`), not a second environment tier.

### The rolling-update contract

`deploy/k8s/base/backend.yaml`'s Deployment runs `replicas: 1` — v1 is
still explicitly single-instance
(`../project-memory/01-scope-and-non-goals.md`) — but `strategy:
RollingUpdate` with `maxUnavailable: 0, maxSurge: 1` means every deploy
briefly runs the old and new pod together (the new pod must pass its
readiness probe before the old one is terminated). This is the exact
overlap window ADR-0001's Postgres row-leasing was designed to survive
from day one (that ADR's own "Restart-boundary race" and its revisit
trigger: "if a future session ever adds a second server instance for
real, this mechanism already generalizes without modification") — the
Kubernetes target is the first time that design is genuinely exercised
end-to-end rather than reasoned about, since `docker compose up --build`
replaces a service outright and never creates this overlap.

What makes this safe, concretely:

- **No monitoring gap.** The old pod keeps its scheduler ticking
  (ADR-0004) — issuing claims, executing checks, evaluating alert
  transitions — right up until Kubernetes sends it `SIGTERM`, which only
  happens after the new pod is `Ready`. `maxUnavailable: 0` is what
  enforces "never below one live scheduler," matching
  `00a-ledger-confirmation.md`'s framing that this system "must keep
  monitoring (or fail predictably and visibly) through its own releases."
- **No duplicate alerts, no double-checks.** Both pods' workers claim
  against the identical `target_schedule` leasing mechanism (ADR-0001) —
  whichever pod's worker wins a given target's lease claim executes that
  check; the loser's claim attempt returns zero rows and is silently
  skipped, the same routine behavior as any same-process contention.
  Alert-suppression state (ADR-0002) is likewise Postgres-resident, not
  per-pod in-memory state, so there is no risk of the old and new pod
  disagreeing about whether an incident is already open.
- **`terminationGracePeriodSeconds: 40` exceeds `SCHEDULER_HARD_SHUTDOWN_DEADLINE`'s
  default 30s (ADR-0004)** so Kubernetes only sends `SIGKILL` after the
  application's own graceful-drain logic has had its full, designed
  window to finish — not a race between two independent shutdown timers
  that happen to usually agree.

### Migrations during a rolling update

The real hazard a rolling update adds that a single-host restart doesn't:
for the surge window, **old and new backend code run against the same
schema simultaneously**. `deploy/k8s/base/migration-job.yaml` already
enforces migrations-complete-before-new-code-starts (via
`deploy/k8s/rollout.sh`'s two-phase apply — see that script and
`deploy/k8s/README.md`), which handles ordering, but ordering alone does
not make a migration safe under overlap: it only guarantees the *new*
pod never sees a stale schema. It says nothing about whether the *old*
pod, still running against the now-migrated schema for the duration of
the surge, keeps working.

**Migration policy for this project going forward: every migration must
be backward-compatible with the immediately-previous backend version for
the duration of one rolling update** (the expand/contract pattern) —
concretely:

- Adding a column: must be nullable or have a default, so the old pod's
  `INSERT`/`UPDATE` statements (which don't know about it) keep working.
- Renaming or dropping a column/table the old pod still reads or writes:
  not safe in a single migration during a live rolling update — split
  into an expand migration (this release: add the new shape alongside
  the old) and a contract migration (a *later* release, once no pod
  running the old code is left) instead.
- This is a real constraint on migration authoring, not yet exercised in
  anger — `backend/migrations/`'s existing nine migrations were all
  written before this deployment target existed and happen to already
  fit this pattern (each adds a new table), but no migration since has
  been *tested* under a real overlapping-pod window (R-008,
  `10-risk-register.md`).

### Agent/server version compatibility during a rolling update

ADR-0003's own design — agent-initiated, outbound-only polling (`GET
/api/v1/agent/assignments`) and OTLP push, never a persistent server-held
connection — is what makes this tractable at all. An agent never holds a
connection that a backend pod's termination could sever mid-operation the
way a long-lived TCP stream or a server-push model would; each agent
request is a discrete, short-lived HTTP call load-balanced (by
`backend-agent`'s Service) to whichever backend pod happens to be Ready
at that moment, old or new. The compatibility contract this deployment
target actually needs is therefore narrow and already how this project
develops its API by convention: **API changes must be additive
(new optional fields, new endpoints) for the duration of a rolling
update** — an agent built against the previous release's assignment-list
schema must not break when a response gains a field it doesn't recognize
(and Go's own JSON unmarshaling into a defined struct already silently
ignores unknown fields, so this is close to free rather than a new
discipline to enforce). A breaking agent-facing change (removing/
renaming a field or endpoint the agent binary depends on) needs the same
expand/contract treatment as a breaking migration: ship the new shape
alongside the old for one release, retire the old shape only once no
older agent binary is expected to still be polling it.

### TLS and the agent-transport gap (R-003) — narrowed further for this target

`cert-manager` (`infra/terraform/main.tf`) gives the Kubernetes target a
real, publicly-trusted Let's Encrypt certificate — the upgrade path
Session 10 documented but never exercised for Compose's Caddy setup
(`tls internal`'s self-signed local CA, still the Compose default above).
`deploy/k8s/base/ingress.yaml` routes **both** the operator-facing
(`/api`) and agent-facing (`/api/v1/agent`, `/v1/logs`) paths through this
same Ingress and its real certificate — unlike Compose's `Caddyfile`,
which only ever routes `/api/*` to the operator-facing listener and lets
agent traffic reach `backend`'s separately-published, plain-HTTP host
port directly. Session 11 explicitly considered and rejected routing
agent traffic through Caddy for Compose, specifically because of the cost
of distributing Caddy's self-signed local CA to every remote agent host
(`08-deployment-and-operations.md`'s "TLS termination" section, "Why this
option, not..."). That cost does not exist here: a real, publicly-trusted
certificate needs no CA distributed to anyone. **This is a real,
structural narrowing of R-003 for the Kubernetes target specifically** —
agent bearer tokens and OTLP telemetry travel over real TLS, not
plaintext — without touching ADR-0003's agent-facing logic at all (purely
an Ingress-routing decision). R-003 itself stays open in `10-risk-register.md`
because Compose's own plain-HTTP agent port is untouched and remains this
project's documented local/dev default; the register entry is updated to
name this narrower scope precisely.

### Rollout procedure

See `deploy/k8s/README.md` and `deploy/k8s/rollout.sh`'s own header
comment for the exact commands. Summary: (1) `infra/terraform apply` once
per cluster (idempotent thereafter — re-running it only reconciles drift,
it does not re-run per deploy); (2) `deploy/k8s/rollout.sh <namespace>
<backend-image> <frontend-image>` per release, which sets the image tags,
renders the manifests, runs the migration Job to completion, then applies
and waits on both Deployments' rollouts in order.

### What's not yet real (named, not hidden)

This entire Kubernetes path was authored and validated only with offline
tooling in a sandboxed session with no outbound access to the Terraform
provider registry and no live cluster or Docker daemon available —
`terraform fmt -check` (HCL syntax) and a real `kubectl kustomize` build
(base+overlay merge, image substitution, patches — confirmed correct by
inspecting the rendered output) passed, but no `terraform apply` and no
`kubectl apply` reaching a `Running` pod have been exercised. See R-008
(`10-risk-register.md`) and B-009 (`11-backlog.md`) — this is the same
honesty standard this project already held itself to for Session 10's
untested Let's Encrypt real-domain upgrade path, applied to a larger
surface. Backup/restore for Postgres in this target is also not yet
built (B-011, `11-backlog.md`) — the gap named below for Compose applies
here too, and is more load-bearing now that Kubernetes is a real
production target rather than a documented absence on a dev-only
deployment.

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
session. `frontend` still exposes `/health`, reachable through the proxy.
`backend` now exposes `/health` on *both* of its listeners independently
(Session 11: `setupOperatorRouter`/`setupAgentRouter` each register it) —
the container's own `healthcheck:` in `docker-compose.yml` checks both, so
the container only reports `healthy` if the operator-facing and
agent-facing servers are both actually serving.

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
