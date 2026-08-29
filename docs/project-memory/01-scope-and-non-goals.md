# Scope and Non-Goals
> Purpose: prevent scope creep by writing down what this will never do
> Project: pulsewatch (public)
> Last updated: 2026-08-29
> Status: **final** (Session 1 — Discovery & Planning)

## MVP boundary (in scope)

Sized against the target set this developer's infrastructure will have once
it exists (~5–10 self-hosted services in shape — the other flagships'
eventual live/demo deployments plus pulsewatch's own instance), per
`00-project-brief.md`. As of this writing, the only real target is
pulsewatch itself — see that brief's "Why this is real dogfooding, not a
hypothetical persona — and what's actually monitorable today" section for
why none of the other four flagships currently has a live deployment; this
sizing describes the shape of the eventual target set, not four services
already waiting to be checked. A reviewer should be able to check each item
off directly:

- [ ] HTTP(S) check type — request a configured URL, evaluate status code
      and (optionally) response-body match, record latency.
- [ ] TCP check type — raw port-reachability check, for targets (e.g. a
      database container) that don't speak HTTP.
- [ ] Per-target configurable check interval and consecutive-failure
      alert threshold (hysteresis) — the mechanism behind success metrics
      1 and 2 in `00-project-brief.md`.
- [ ] Scheduler with per-target run exclusivity (never two overlapping
      checks against the same target) and restart-safe persisted
      scheduling state (last-checked-at survives a process restart) —
      success metrics 3 and 5.
- [ ] Check history persisted in PostgreSQL, with a scheduled rollup job
      (hourly/daily aggregates) and a retention policy that drops raw rows
      past the configured window — success metric 4; see
      `00-project-brief.md` §3 for why this is plain Postgres, not
      TimescaleDB.
- [ ] SLO / error-budget calculation over a rolling window (e.g. 30 days),
      excluding periods pulsewatch itself was down from each target's
      calculation (not counted as "down").
- [ ] Persisted incident state so an alert already firing before a
      restart is recognized as still-open afterward, with no duplicate
      alert on recovery.
- [ ] Alerting via generic webhook (HTTP POST) and email — matching the
      business assumption of integrating with existing channels rather
      than building a paging product.
- [ ] Lightweight Go agent, for targets not directly reachable from the
      pulsewatch server (the private-network dogfooding case),
      reporting over an OpenTelemetry pipeline.
- [ ] Minimal SvelteKit dashboard: target list with current status,
      uptime % over a selectable window, and incident history.
- [ ] v1 is considered complete only once it has run continuously against
      real targets for a sustained period without manual intervention —
      see "Definition of v1 complete" below.

## Explicit non-goals

| Non-goal | Why excluded | Would reconsider if |
|---|---|---|
| APM / distributed tracing of user applications | pulsewatch watches from the outside via checks; instrumenting what happens *inside* a monitored application is a different product with a different instrumentation model entirely (in-process tracing SDKs, span propagation) — not an extension of check-based monitoring. | Never for this repo — this would need its own ledger row, not a feature added here. |
| A log aggregation product | Log shipping, indexing, and search is a different scaling problem (storage/search over log volume) unrelated to check results and telemetry this system already handles. | N/A — structurally a different product. |
| On-call scheduling / paging escalation trees | pulsewatch fires alerts (webhook/email); it does not manage rotations, escalation policies, or acknowledgement workflows — that's a distinct, mature product category with its own reliability requirements. | Never for this repo — integrate with a dedicated paging tool via the generic webhook instead. |
| Synthetic browser monitoring (headless-browser scripted user journeys) | A materially different check execution model (a browser runtime, script maintenance) than HTTP/TCP checks; this is endpoint/service health from the outside, not UX simulation. | If a specific dogfooding need emerges to verify a full client-rendered flow — not currently the case for any monitored flagship, and would need its own new-technology budget slot (currently at cap). |
| Multi-tenant SaaS billing / plans | Single self-hosted instance for one operator, per the business assumptions in `00-project-brief.md`; multi-tenancy, quotas, and billing are a different product shape (isolation, payment integration). | Never for this repo — pulsewatch is explicitly not being productized as a hosted SaaS (`00b-build-vs-alternatives.md`). |
| A third new technology (e.g. TimescaleDB, Prometheus/Grafana, a message queue) | Rule D3 froze this repo's learning budget at exactly two new technologies (Go concurrency, OTel pipelines) in Session 0 (`00a-ledger-confirmation.md`); the retention/rollup decision in `00-project-brief.md` §3 already reasons through why plain Postgres is chosen instead of TimescaleDB specifically. | If a future session's *measured* data volume shows the hand-rolled rollup job genuinely can't keep up — against real evidence, not preemptively. |
| Multi-region / geographically distributed probing | Agents run on infrastructure the operator controls (business assumption); this is not a SaaS-style probe network checking from multiple PoPs simultaneously. | If dogfooding reveals a real need to distinguish "down for everyone" from "down from one network" — not currently a demonstrated need. |
| Arbitrary custom-metrics ingestion (becoming a general metrics platform) | pulsewatch ingests structured check results (up/down, latency, status code) and its own agent's OTel telemetry — not arbitrary application-defined metrics from monitored services, which would blur into the APM/log-aggregation territory already excluded above. | N/A — structurally a different product surface. |
| Public-facing status pages | A real feature of hosted SaaS competitors (Option A, `00b-build-vs-alternatives.md`), deliberately traded away in the build decision; not needed for a single-operator dogfooding use case where the dashboard itself is the only audience. | If this developer wants to publicly demonstrate uptime for a specific flagship's demo as part of that repo's own README — a small, separable feature, not a reason to reopen this list generally. |

## Deferred to backlog

Real, plausible v2+ features — not rejected, just not required to prove
the properties this brief's success metrics care about:

- ICMP/ping and DNS-resolution check types.
- TLS-certificate-expiry checking (a genuinely useful check type for this
  developer's own real deployments, but not required to demonstrate the
  continuous-operation properties this v1 is scoped around).
- Additional native alert-channel integrations beyond generic
  webhook/email (Slack, Discord) — achievable today by pointing a webhook
  at a bridge, so not blocking.
- Adaptive/anomaly-based alert thresholds beyond a fixed consecutive-
  failure count.
- Multi-user auth / role separation on the dashboard (today: single
  operator, matching the actual use case).

## Definition of "v1 complete"

All items in the MVP boundary checklist above are implemented **and**:

- All 5 success metrics in `00-project-brief.md` are demonstrated against
  real test scenarios (a stopped target, an injected transient blip, a
  server restart mid-incident, an elapsed retention window, a slow
  target) — not just implemented, verified.
- pulsewatch has run continuously against its own real, currently-
  monitorable targets (at minimum: its own health/liveness endpoints and
  scheduler process, plus a local docker-compose stack if one is stood up
  for this purpose) for a sustained period (proposed: 2+ weeks) without
  manual intervention or a missed real outage — the actual dogfooding proof
  this brief is built around, not a demo run once and screenshotted.
- Extending that same proof to the other portfolio flagships' live/demo
  deployments (`privacy-forge`, `laravel-consent-guard`, `bookslot`,
  `lexicon`) is **explicitly aspirational and not required for v1-complete**,
  because none of them currently has a real live deployment to monitor —
  per each repo's own recorded decisions: `privacy-forge`'s Session 24
  real-infrastructure-spend descoping, `lexicon`'s local-only deployment
  proof, `bookslot`'s D-0045 local-only MVP checkpoint, and
  `laravel-consent-guard` being a package never deployed as a running
  service at all. If and when any of those repos' own future sessions
  stand up a real live deployment, extending pulsewatch's dogfooding to it
  is a natural, low-effort follow-on — but v1-complete must not be gated on
  infrastructure decisions this repository doesn't own.
