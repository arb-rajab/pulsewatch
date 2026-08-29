# Project Brief
> Purpose: the single source of truth for what this project is and why it exists.
> Project: pulsewatch (public)
> Last updated: 2026-08-29
> Status: STUB — full brief is produced in Session 1 (Discovery & Business Framing)

## One-line description
A self-hosted uptime, SLO, and alerting service with a lightweight agent —
so a small team can know whether their infrastructure is up, degrading, or
burning its error budget, without adopting a full SaaS observability
platform.

## Problem statement (draft — refine in Session 1)
Small engineering teams either bolt together uptime checks from a handful of
disconnected tools (a status-page SaaS, a cron job pinging a health
endpoint, ad hoc Slack webhooks) or over-adopt a heavyweight observability
platform meant for a much larger org. Neither gives an honest, continuously
maintained answer to "is this service actually meeting its availability
target, and who gets told the moment it isn't." Unlike this portfolio's
other flagships, the thing being validated here isn't a single request's
output — it's whether the system keeps telling the truth about other
systems, hour after hour, day after day.

## Target users and stakeholders (draft)
- **Primary user:** an engineer or small SRE-adjacent team responsible for
  keeping a handful of self-hosted or cloud services up, who wants SLOs and
  alerting without a SaaS subscription or a Kubernetes-scale observability
  stack.
- **Secondary user:** the on-call engineer who receives an alert and needs
  enough context (what broke, since when, against which SLO) to act on it.
- **Stakeholder:** whoever owns the error budget conversation — the person
  who needs a credible, historical answer to "did we meet our SLO this
  quarter."

## Business assumptions (draft — validate in Session 1)
- The target deployment is a single self-hosted instance monitoring a
  bounded set of services/hosts, not a multi-tenant hosted product.
- Agents run on infrastructure the operator controls (their own hosts,
  containers, or network-reachable endpoints) — not a black-box SaaS probe
  network.
- v1 alerting integrates with existing notification channels (e.g. webhook,
  email) rather than replacing a dedicated paging/escalation product.

## Why this project exists in the portfolio
- **Technology/learning objective:** Go concurrency patterns (worker pools,
  context cancellation, graceful shutdown) for a service that must poll many
  targets concurrently and shut down cleanly; OpenTelemetry collector
  pipelines for agent → server telemetry.
- **SDLC phases demonstrated deeply:** 6 (Release & Deployment), 7
  (Operations & Maintenance).
- **Framework allocation:** Go + Gin (primary backend) + SvelteKit (primary
  frontend) — confirmed `UNIQUE` in `00a-ledger-confirmation.md`.
- **Portfolio significance:** the first genuinely continuous, operated
  system in the portfolio — every other flagship (`privacy-forge`,
  `laravel-consent-guard`, `lexicon`) is a request/response system where a
  request comes in and an answer goes out. pulsewatch runs over time,
  monitors real things, and its correctness is partly about behaviour across
  hours and days.

## Success metrics (draft — finalise in Session 1)
- A stranger can self-host, register a monitored target, and receive a real
  alert on a simulated outage, from the README, in under 15 minutes.
- SLO/error-budget calculations are verifiably correct against a
  hand-computed reference scenario in the test suite.
- The agent survives a server restart and a network partition without
  losing or double-reporting check results.
- Zero critical/high findings in CodeQL + `govulncheck`/`npm audit` at
  v1.0.0.

## Feasibility notes and key risks (draft)
- **Risk:** treating this like another request/response API and
  under-investing in the "runs continuously, must self-heal and be
  operable" concerns that are this repo's actual point. Mitigated by
  deliberately scoping Session 1 around continuous operation, not a CRUD
  feature list.
- **Risk:** Go concurrency patterns and OTel collector pipelines are both
  genuinely new; timebox a learning spike before Session 3 architecture work
  if either proves a bigger lift than expected.
- **Feasibility:** Gin, SvelteKit, PostgreSQL, and Redis are all
  well-understood pieces of the puzzle individually; the novel work is
  concurrency-safe scheduling/polling and the OTel pipeline, which keeps the
  risk contained to two clearly named spikes rather than the whole system.

## Elevator pitch (for the README — draft)
"pulsewatch is what you wish your uptime monitoring looked like: real SLOs
with an honest error budget, a lightweight agent that keeps reporting even
when things go sideways, and alerts that fire the moment you're actually at
risk — self-hosted, not another SaaS bill."

---
**Next session (Session 1) must:** validate every assumption above with the
user, replace "draft" sections with confirmed content, and produce
`01-scope-and-non-goals.md` alongside the finalised version of this file —
specifically framing the problem around continuous operation and real
infrastructure monitoring, not a request/response product like this
portfolio's other flagships.
