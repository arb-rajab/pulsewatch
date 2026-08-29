# Project Brief
> Purpose: the single source of truth for what this project is and why it exists.
> Project: pulsewatch (public)
> Last updated: 2026-08-29
> Status: **final** (Session 1 — Discovery & Planning)

## One-line description
A self-hosted uptime, SLO, and alerting service with a lightweight agent —
built to genuinely monitor this developer's own portfolio infrastructure,
and to keep telling the truth about it continuously, not just at the moment
someone happens to check.

## The framing that governs this whole document

Every other flagship in this portfolio (`privacy-forge`, `laravel-consent-
guard`, `lexicon`) is a request/response system: a request arrives, the
system does work, an answer goes out, and correctness is evaluated on that
one exchange. pulsewatch is not that. It is a process that is supposed to
still be correctly doing its job an hour from now, a day from now, and after
its own container has been restarted three times in between. A bug that only
manifests after 40 hours of continuous operation, or only on the second
scheduled run after a crash, is a real pulsewatch bug in a way it couldn't
even exist as a category for the other flagships. Session 0 named this
correctly as the reason Release & Deployment and Operations & Maintenance
are this repo's two deep SDLC phases (`00a-ledger-confirmation.md`); this
session's job is to carry that framing all the way through the problem
statement, the build decision, and the success metrics, rather than
defaulting into "another CRUD app with a health-check feature."

## Problem statement

This developer runs several self-hosted flagship repositories
(`privacy-forge`, `laravel-consent-guard`, `bookslot`, `lexicon`) with live
or demo deployments, plus whatever personal infrastructure sits alongside
them. Nobody is watching a dashboard for these all day. The actual, current
problem is: **if one of those deployments goes down — a demo instance dies,
a database container doesn't come back after a host reboot, a cert expires
— there is currently no honest, continuously running answer to "is it
actually up right now," and no mechanism that tells this developer the
moment it isn't.**

The options this developer would otherwise reach for each fail for a
specific, nameable reason, not a vague "it's not good enough":

- **A cron job + a curl script.** This is genuinely what an experienced
  engineer reaches for first, and it is not wrong to try. It fails to solve
  the actual problem because it has no memory: a single failed curl exit
  code doesn't distinguish a real outage from one dropped packet, so it
  either alerts on every transient blip (and gets muted/ignored within a
  week) or nobody bothers wiring alerting to it at all and it silently
  becomes a log nobody reads. It also has no answer for "what happened while
  the host running the cron job was itself down" — there is no persisted
  state to reconcile against on recovery.
- **A big-name hosted uptime SaaS.** This one is not a strawman — see
  `00b-build-vs-alternatives.md` for the honest version of this comparison.
  The short version: it would probably work fine for the pure "ping a public
  URL, alert on failure" slice of this problem, which is exactly why it
  doesn't get dismissed lightly. It falls short of the actual need because
  several of this developer's targets (a database container's health,
  an internal service reachable only from the same Docker network) aren't
  publicly reachable, and because it produces zero portfolio value — see
  the next section.
- **An enterprise observability platform (self-hosted Grafana + Prometheus,
  as-is).** Also a real, mature answer for a much larger problem — dozens
  of services, a team, high-cardinality metrics. Deployed as-is against this
  developer's actual ~5–10 targets, it is a large operational surface (a
  scrape target config, exporters, Alertmanager routing, PromQL, Grafana
  dashboard authoring) adopted wholesale rather than built, for a
  correctness problem — "did we correctly detect this outage, did we
  correctly not alert on that blip" — that a much smaller, purpose-fit
  system can answer just as well at this scale.

## Why this is real dogfooding, not a hypothetical persona

Unlike `bookslot`'s tattoo-studio framing, which was necessarily
hypothetical (this developer does not run a tattoo studio), pulsewatch's
primary use case is not invented for the sake of having a persona. **The
primary user is this developer, and the infrastructure being monitored is
this developer's own**: the live/demo deployments of `privacy-forge`,
`laravel-consent-guard`, `bookslot`, and `lexicon`, plus pulsewatch's own
server monitoring itself. This is a bounded, currently-existing set of
services, not a projection of what some imagined third party might want.
That has a direct, practical consequence for this brief: the requirements
below aren't sized against a market segment, they're sized against what
this developer's own deployments actually need — a handful of HTTP(S)
health endpoints and a couple of TCP-reachable data stores, checked often
enough to catch a real outage before a portfolio reviewer clicks a dead
demo link, not thousands of targets across many tenants.

## Target users and stakeholders

- **Primary user (the actual, current one): this developer**, operating
  pulsewatch against their own self-hosted portfolio infrastructure. There
  is no separate "buyer persona" to imagine here — the person configuring
  targets, receiving alerts, and reading the SLO dashboard is the same
  person in every case, which is itself a property this brief should not
  paper over by inventing a fictitious second stakeholder.
- **Secondary role, same person: "on-call."** When an alert fires, it needs
  to reach this developer somewhere other than a browser tab that isn't
  open — a webhook or email, arriving with enough context (which target,
  since when, against which threshold) to act on without opening the
  dashboard first. This is a different *mode* of use (interrupt-driven,
  away from a screen), not a different *user*.
- **Indirect beneficiary, not a user of pulsewatch itself:** anyone
  evaluating this portfolio who clicks through to a live demo. pulsewatch
  being correct is part of why those demos stay up; that's a motivation for
  building this honestly, not a stakeholder pulsewatch needs to serve
  directly (they never see pulsewatch's UI).

## Business assumptions

- The target deployment is a single self-hosted instance monitoring a
  bounded set of services this developer controls (their own hosts,
  containers, or network-reachable endpoints) — not a multi-tenant hosted
  product. This is a direct, verified fact of the actual use case, not a
  simplifying assumption made for scope's sake.
- Agents run on infrastructure the operator controls — the lightweight
  agent exists specifically because some real targets (a database
  container's internal port, a service only reachable from inside a Docker
  network) are not reachable from a third-party SaaS prober; a self-hosted
  agent is the only way to check them at all, which is itself part of why a
  hosted SaaS cannot fully solve this developer's actual problem (see
  `00b-build-vs-alternatives.md`).
- v1 alerting integrates with existing notification channels (webhook,
  email) rather than building a dedicated paging/escalation product — this
  developer already has channels that can receive a webhook; building a
  second on-call rotation tool is out of scope (`01-scope-and-non-goals.md`).

## Continuous operation requirements

This is the section every other flagship's brief did not need, and the one
this session must not shortchange. "Runs continuously and stays correct" is
not one requirement, it decomposes into four concrete properties this
design must satisfy, each with a real failure mode if it's skipped.

### 1. Restart mid-check-cycle must not corrupt truth

pulsewatch's own process will restart — a deploy, a crash, a host reboot.
On restart it must not (a) treat every target as freshly unknown and wait a
full check interval before its first real check, (b) fire an immediate
duplicate check against every target the instant it comes back regardless
of when each was last actually checked, or (c) confuse "pulsewatch itself
was down" with "the target was down." This requires scheduling state — at
minimum, last-checked-at per target — to be persisted in PostgreSQL, not
held only in an in-memory scheduler, so a fresh process can reconstruct
"what's due, and when it's actually due" from durable state rather than
starting blind.

### 2. A check must never overlap with its own still-running previous run

If a target is slow or hanging (its check takes longer than its own
configured interval), the scheduler must not start a second concurrent
check against the same target while the first is still outstanding — that
produces unbounded concurrent requests against one already-struggling
target, which is a real bug, not a hypothetical edge case, the moment any
target's check timeout is close to its interval. This is where the Go
concurrency learning objective (worker pools, context cancellation) is
load-bearing rather than decorative: per-target run exclusivity (skip-if-
already-running, not queue-unboundedly) has to be a designed property of
the scheduler, verified directly (see success metric 5 below), not an
assumed side effect of "we used goroutines."

### 3. Retention and rollup over weeks — and the TimescaleDB decision

Raw check results accumulate forever if nothing prunes them: at this
project's actual scale (~5–10 targets, checked on the order of every
30–60s) that's on the order of 10,000–30,000 rows/day system-wide, which
is not large in absolute terms but is unbounded, and the SLO/error-budget
question this system exists to answer ("did we meet target over the last
30 days") needs efficient rolling-window aggregation, not a full scan of
every raw row ever recorded.

**Decision: plain PostgreSQL, with retention and rollup implemented as an
explicit, testable Go background job — not TimescaleDB.**

Reasoning, not preference:
- **The learning budget is frozen, and this would break it.** `00a-ledger-
  confirmation.md` commits to exactly two new technologies (Go concurrency
  patterns, OpenTelemetry pipelines) under Rule D3. TimescaleDB would be a
  third. That's not a soft guideline being weighed against convenience —
  it's a governance decision already signed off in Session 0, and this
  session isn't the place to quietly reopen it.
- **The actual data volume doesn't need it.** TimescaleDB's real value —
  automatic time-partitioning, compression, continuous aggregates — pays
  off at high-cardinality, high-volume ingestion (many hosts, sub-second
  granularity). At ~10 targets and a 30–60s interval, even a full year of
  raw history is on the order of 10M rows: comfortably handled by plain
  PostgreSQL with an index on `(target_id, checked_at)` and a scheduled
  cleanup job. Reaching for TimescaleDB here would be solving a scale
  problem this deployment doesn't have.
- **Hand-rolling the rollup job is more aligned with this repo's actual
  point than outsourcing it would be.** A scheduled Go job that computes
  hourly/daily rollups (uptime %, p50/p95 latency) and deletes expired raw
  rows past the retention window is exactly the kind of "operable system"
  work Operations & Maintenance (this repo's other deep phase) is supposed
  to demonstrate, and it reuses the same worker-pool/context-cancellation
  patterns already committed to as the Go concurrency learning objective.
  Delegating that to a database extension would remove, not add to, the
  thing this repo is trying to prove.

This is a real design decision, made here because the continuous-operation
requirements above force it, but it is not yet a formal ADR — `00a-ledger-
confirmation.md` records that ADRs begin at Session 3 (Architecture). If
Session 3 wants to formalize this as ADR-0001, it should reference this
section rather than re-run the comparison from scratch.

**Revisit trigger:** if a later session's *measured* data volume (many more
targets added, or interval dropped well below 30s) shows the hand-rolled
rollup job genuinely can't keep up, or query latency on the dashboard
degrades in practice — reconsider TimescaleDB then, against real evidence,
not preemptively now.

### 4. SLO/alerting correctness is a rolling-window property, not a single check

- A single failed check must not, by itself, fire an alert — that's the
  cron+curl failure mode repeating itself inside pulsewatch. Alerting needs
  a consecutive-failure threshold (hysteresis), configured per target, so a
  genuinely transient blip is absorbed and a real outage still gets caught
  within a bounded number of check intervals.
- Time pulsewatch itself was down must not silently count as "target down"
  in that target's error-budget math — it wasn't observed, not failing, and
  conflating the two would corrupt every monitored target's SLO with
  pulsewatch's own outage history. It must be excluded from the rolling-
  window calculation as "unknown," not counted either way.
- Alert de-duplication must survive a restart: an incident already firing
  before a restart must be recognized as still-open afterward (persisted
  incident state, not an in-memory flag), so recovery doesn't either drop
  a real ongoing incident silently or re-fire a duplicate alert for the
  same one.

## Why this project exists in the portfolio

- **Technology/learning objective:** Go concurrency patterns (worker pools,
  context cancellation, graceful shutdown) for a scheduler that polls many
  targets concurrently with per-target run exclusivity and shuts down
  cleanly; OpenTelemetry collector pipelines for agent → server telemetry.
  Both are load-bearing in the continuous-operation requirements above, not
  bolted on for résumé value.
- **SDLC phases demonstrated deeply:** 6 (Release & Deployment — must keep
  monitoring or fail visibly through its own deploys), 7 (Operations &
  Maintenance — runbooks, backup/restore, and "what happens when the thing
  that watches everything else goes down").
- **Framework allocation:** Go + Gin (backend) + SvelteKit (frontend) —
  confirmed `UNIQUE` in `00a-ledger-confirmation.md`; frozen.
- **Portfolio significance:** the first genuinely continuous, operated
  system in the portfolio, and the only one whose primary use case is
  actual dogfooding of this developer's own live infrastructure rather than
  a persona built for the sake of having one.

## Success metrics

Framed around what a continuously-operated system actually needs to prove
— not "time to first response," which is the right metric for a
request/response product and the wrong one here. Each of these is stated as
what will be measured, not a number fabricated in advance of building
anything:

1. **Real-outage detection is bounded by the configured check interval.**
   Stop a monitored target; measure wall-clock time from actual downtime
   start to alert fired; confirm it is bounded by (consecutive-failure
   threshold × check interval) plus a small margin — not "eventually."
2. **A transient blip below threshold does not fire an alert.** Inject a
   single dropped check below the configured consecutive-failure threshold;
   confirm no alert fires and the target's state stays healthy.
3. **A server restart loses no in-flight check state and does not double-
   alert.** Kill the pulsewatch server process mid-check-cycle while an
   incident is actively firing; restart it; confirm scheduling resumes from
   persisted state (not a blind full-reset), the open incident is still
   recognized as open, and no duplicate alert is emitted for it.
4. **Rollup and retention are correct, not just non-crashing.** After the
   retention window elapses, confirm raw rows past that window are actually
   removed, and that a rollup aggregate (e.g. hourly uptime %) matches a
   hand-computed reference over the same underlying raw data.
5. **Concurrent-check safety holds under a slow target.** Configure a
   target whose check duration exceeds its own interval; confirm the
   scheduler never runs two overlapping checks against that same target at
   once, and confirm this does not starve or delay other targets' checks.

## Feasibility notes and key risks

- **Risk:** treating this like another request/response API and
  under-investing in the continuous-operation properties above. Mitigated
  by naming them explicitly in this brief rather than leaving them implicit
  until Architecture.
- **Risk:** Go concurrency patterns and OTel collector pipelines are both
  genuinely new; timebox a learning spike before Session 3 architecture
  work if either proves a bigger lift than expected.
- **Feasibility:** Gin, SvelteKit, PostgreSQL, and Redis are all
  well-understood pieces individually; the genuinely novel work is
  concurrency-safe, restart-safe scheduling and the OTel pipeline — which
  keeps the risk contained to two named spikes rather than the whole
  system.

## Elevator pitch (for the README)

"pulsewatch is what happens when you actually need to know your own
self-hosted portfolio is up, not just believe it is: real SLOs with an
honest error budget, a scheduler that survives its own restarts without
losing the plot, and alerts that fire on a real outage and stay quiet on a
blip — because it's monitoring this developer's own infrastructure, every
day, not a demo."

---
This brief is final for Session 1. See `00b-build-vs-alternatives.md` for
the full build-vs-buy comparison and `01-scope-and-non-goals.md` for the
MVP boundary and non-goals table.
