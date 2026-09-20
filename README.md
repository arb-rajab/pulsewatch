# pulsewatch

> **Status:** 🚧 Session 22 complete — full incident lifecycle, three alert dispatch
> channels (webhook, push, email), device-list dashboard, Kubernetes deployment
> verified against real infrastructure, and agent-initiated monitoring.
> See [`docs/project-memory/12-session-handoff.md`](docs/project-memory/12-session-handoff.md)
> for current state and roadmap. Two items are permanently blocked or descoped
> (see Constraints below).

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

This repository is built through a session-based workflow. **Current phase:
Session 22 (Backlog Cleanup — test-fixture ordering and security/testing
documentation).** Previous sessions completed:

- **Sessions 5–7:** Scheduler with Postgres leasing (restart-safe, no duplicates),
  alert dispatch state machine, agent-initiated monitoring.
- **Session 8:** Operator authentication and CRUD endpoints (targets, channels, agents).
- **Session 9:** First dashboard screen and real target status endpoint.
- **Sessions 10–22:** TLS termination, real webhook/email/push dispatch, mobile-push
  device management, device-list dashboard, Kubernetes deployment (real-cluster
  verified), agent stale-detection overlay, and security/testing documentation.

**Two explicitly out-of-scope blockers:**
- **B-013:** Terraform apply + cert-manager real-cloud verification (sandbox
  egress-blocked — requires external cloud account).
- **B-016:** Real FCM/APNs delivery with live provider credentials (permanently
  descoped by design — no public CI can hold production credentials; both
  protocols implemented and mock-verified, never delivered to real devices).

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
workflow. This boots the real schema with a complete incident-detection and
alerting pipeline: automated check execution via an embedded scheduler, alert
dispatch to webhooks/email/mobile-push, and an agent-initiated remote-monitoring
path for private infrastructure.

## Documentation

- [`docs/project-memory/`](docs/project-memory/) — brief, requirements,
  architecture, security, testing, operations, decisions, risks, backlog,
  handoff, release notes, maintenance/retirement plan
- [`docs/adr/`](docs/adr/) — architecture decision records, including
  [`ADR-0009`](docs/adr/ADR-0009-serverless-otlp-normalizer.md) (a
  serverless AWS Lambda function, [`serverless/otlp-normalizer/`](serverless/otlp-normalizer/),
  as a third, additive deployment target for stateless OTLP payload
  validation — alongside, not instead of, the Kubernetes-deployed backend)
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
