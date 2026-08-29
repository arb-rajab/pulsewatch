# pulsewatch

> **Status:** 🚧 Session 4 complete — environment, schema, and CI baseline.
> No monitoring logic, agent code, or alerting exists yet. See
> [`docs/project-memory/12-session-handoff.md`](docs/project-memory/12-session-handoff.md)
> for current state and next steps.

A self-hosted uptime, SLO, and alerting service with a lightweight agent —
for teams who want real observability into whether their infrastructure is
up, degraded, or breaching its error budget, without adopting a full SaaS
observability platform.

## What this demonstrates

- **Release & Deployment (deep):** safe rollout of a service that must keep
  monitoring while it deploys — migrations, agent/server compatibility, and
  zero-downtime release discipline for a system that cannot simply go quiet
  during a deploy.
- **Operations & Maintenance (deep):** this is the portfolio's first
  genuinely continuous, operated system. It runs over hours and days, not
  just within a single request — so runbooks, backup/restore, capacity
  planning, and "what happens when the thing that watches everything else
  goes down" are first-class concerns, not an afterthought.
- Go concurrency patterns (worker pools, context cancellation, graceful
  shutdown) and an OpenTelemetry collector pipeline for agent telemetry.

Stack: Go 1.25 (Gin) · SvelteKit · PostgreSQL · Redis.

## Project status

This repository is built through a session-based workflow. Current phase:
**Session 4 (Environment, Repository Setup, Standards, and CI Baseline) —
complete.** Next: Session 5 (feature implementation, starting with the
scheduler/leasing mechanism).

Full portfolio context: this is a flagship repository in a broader
public/private software portfolio. See `docs/project-memory/` for the
complete project memory pack, and `docs/SDLC-EVIDENCE.md` for the
phase-by-phase evidence map.

## Quickstart

```bash
docker compose up --build
```

This boots Postgres (with every migration applied — `targets`,
`target_schedule`, `check_results`, `check_rollups_hourly`, `incidents`,
`alert_channels`, `alert_dispatches`, `agents`, `operators`), Redis, the
OTel Collector, the backend, and the frontend.

- Backend health check: `http://localhost:8020/health`
- Frontend: `http://localhost:3003`
- OTel Collector health: `http://localhost:13143/`

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the full setup and migration
workflow. This currently boots the real schema and infrastructure with no
monitoring, agent, or alerting business logic yet — that begins with
Session 5.

## Documentation

- [`docs/project-memory/`](docs/project-memory/) — brief, requirements,
  architecture, security, testing, operations, decisions, risks, backlog,
  handoff, release notes, maintenance/retirement plan
- [`SECURITY.md`](SECURITY.md) — vulnerability disclosure policy

## Non-goals

pulsewatch monitors whether *this developer's own* infrastructure is up and
meeting its SLOs — it is built to genuinely monitor this portfolio's other
flagships (`privacy-forge`, `laravel-consent-guard`, `bookslot`, `lexicon`)
and itself, not a hypothetical third-party persona. It deliberately does
**not** aim to become:

- **APM / distributed tracing of user applications.** This watches uptime
  and SLOs from the outside; it does not instrument or trace what happens
  inside the applications it monitors.
- **A log aggregation product.** No log shipping, indexing, or search — that
  is a different product with a different scaling problem.
- **An on-call scheduling / paging escalation system.** pulsewatch fires
  alerts; it does not manage who is on-call, rotations, or escalation trees
  (integrate with a dedicated paging tool for that).
- **Synthetic browser monitoring.** No headless-browser scripted user
  journeys — this is endpoint/service health and SLOs, not UX simulation.
- **A multi-tenant SaaS billing product.** This is a self-hosted service for
  one operator to run for themselves, not a hosted product with tenants,
  plans, or billing.
- **A third new technology** (TimescaleDB, Prometheus/Grafana, a message
  queue). The learning budget for this repo is frozen at exactly two
  (Go concurrency patterns, OpenTelemetry pipelines) — see
  `docs/project-memory/00-project-brief.md`.

Full rationale and reconsideration conditions for each non-goal, plus the
MVP boundary, are in
[`docs/project-memory/01-scope-and-non-goals.md`](docs/project-memory/01-scope-and-non-goals.md).
The reasoning behind building this instead of a hosted SaaS or a self-hosted
Grafana+Prometheus stack is in
[`docs/project-memory/00b-build-vs-alternatives.md`](docs/project-memory/00b-build-vs-alternatives.md).

## Licence

AGPL-3.0 — see [`LICENSE`](LICENSE). Rationale: this is a hostable
application, not a library; AGPL ensures modifications to a hosted version
remain shareable.
