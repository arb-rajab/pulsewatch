# Session 1 — Why Build This, Not a Hosted SaaS or Grafana+Prometheus

> Purpose: the explicit options-considered comparison this repository's
> central "why does this need to exist" question requires (per `00a-ledger-
> confirmation.md`'s Discovery deep-phase rationale, and matching the rigor
> of `lexicon/docs/project-memory/00b-rag-vs-alternatives.md`). Written at
> Session 1 (Discovery), deliberately kept out of `docs/adr/` and unnumbered
> — `00a-ledger-confirmation.md` records that formal ADRs begin at Session 3
> (Architecture). If Session 3 needs to formalize or revise this as an
> architecture ADR, it should reference this file rather than re-run the
> comparison from scratch.
>
> Project: pulsewatch (public) · Last updated: 2026-08-29 · Status: decided

## Context

`00-project-brief.md` establishes the actual problem: this developer runs a
handful of self-hosted flagship deployments and needs an honest, continuous
answer to "is it up, and did I meet my SLO," with correctness measured over
hours and days rather than a single request. Three real mechanisms could
plausibly answer that: a hosted uptime SaaS, a self-hosted enterprise
observability platform (Grafana + Prometheus + Alertmanager) adopted as-is,
and a purpose-built tool. This file compares them against that specific
problem and this specific developer's constraints — not against either
alternative's general reputation, and not as a foregone conclusion dressed
up as analysis.

## Options considered

### A — A big-name hosted uptime SaaS

A third-party service periodically requests each configured URL from its
own infrastructure and alerts on failure; history, dashboards, and status
pages are hosted for you.

**For, stated honestly:** for the pure "ping a public HTTPS endpoint, alert
on failure" slice of this problem, a mature SaaS is genuinely easier and
more reliable than anything built in a portfolio timebox. It has years of
production hardening this project will not match: global probe locations,
mature notification integrations, a polished dashboard, zero infrastructure
to operate. This is not a strawman comparison — if the *only* goal were
"get alerted when a public URL goes down," this is probably the objectively
better engineering choice, and this document should not pretend otherwise.

**Against, specific to this developer's actual targets, not SaaS in
general:**
- **Reachability.** Several of this developer's real targets are not
  publicly routable — a database container's internal port, a service only
  reachable from inside a Docker network on the host running the other
  flagships. A third-party prober physically cannot reach those; only
  something running on infrastructure this developer controls can, which is
  exactly why the project's business assumptions (`00-project-brief.md`)
  commit to a self-hosted agent rather than "just point a SaaS at
  everything."
- **Zero portfolio/skill-proof value.** Subscribing to a SaaS product
  demonstrates nothing about this developer's own engineering ability, and
  exercises neither of the two learning objectives already frozen in
  `00a-ledger-confirmation.md` (Go concurrency, OTel pipelines). For a
  portfolio flagship, that's not a minor gap — it's the entire point of the
  repository existing.
- **Ongoing cost for a personal, indefinite deployment**, and a third
  party holding this developer's own infrastructure's uptime history —
  neither is a large problem on its own, but neither is offset by anything
  a SaaS gives back for *this* use case once reachability is already broken
  for part of the target set.

**Verdict:** rejected as the sole answer. It would have solved a narrower
version of the problem than the one that actually exists (public-only
targets, no learning objective, no portfolio artifact) — not the version
`00-project-brief.md` describes.

### B — Self-hosted Grafana + Prometheus + Alertmanager, adopted as-is

Prometheus scrapes metrics (via `blackbox_exporter` for HTTP/TCP probing),
Alertmanager routes alerts, Grafana renders dashboards — all mature,
production-grade, free open-source software, self-hosted and fully under
this developer's control.

**For, stated honestly:** this is the real, professional answer for an
organization with dozens of services and a team operating them. It handles
retention, rollups, and alert routing far more robustly than anything this
project will build from scratch, for free, today. If pulsewatch's actual
target set were 50 services instead of ~10, this would likely be the
correct call over building something bespoke — that's a genuine trade-off,
not a strawman weakness invented to make Option C look better.

**Against, specific to this developer's constraints:**
- **It would blow the frozen learning budget.** Operating this stack
  correctly for even a handful of targets means learning
  `blackbox_exporter` configuration, Prometheus's scrape/service-discovery
  model, PromQL, Alertmanager routing rules, and Grafana dashboard
  authoring — several new technologies, not one. `00a-ledger-
  confirmation.md` (Rule D3) already froze this repository's learning
  budget at exactly two new technologies. Adopting this stack as-is isn't
  compatible with that governance decision, and this session is not the
  place to quietly reopen it.
- **Zero original code, zero portfolio artifact.** Deployed as-is, the
  deliverable is a docker-compose bundle of other people's software with
  some YAML configuration. That has real operational value but demonstrates
  none of this developer's own backend/systems engineering — which is what
  a flagship repository in this portfolio exists to show.
- **The adopted skills don't compound with what's already committed.**
  Prometheus's pull-based scrape model and PromQL are a different skill
  investment than the Go concurrency and OTel pipeline work already frozen
  as this repo's learning objectives — adopting them would be redundant
  effort spent sideways, not additive to what Session 0 already committed
  to.
- **Overkill for the actual blast radius.** Prometheus's TSDB and query
  engine are built to handle thousands of high-cardinality series across
  many teams. Correctly computing an error budget over ~10 targets doesn't
  need that machinery; using it anyway multiplies operational surface
  against a problem size that doesn't require it.

**Verdict:** rejected for *this* deployment, not rejected in general — the
honest caveat above (Option B is the right call at real multi-team scale)
is recorded here so this isn't oversold as "Grafana+Prometheus is the wrong
tool," only "the wrong tool for this specific, smaller, learning-budget-
constrained problem."

### C — A purpose-built tool (pulsewatch)

A small Go service with its own scheduler, its own PostgreSQL-backed
history and rollups, and a lightweight agent for otherwise-unreachable
targets — built specifically for this developer's own bounded target set.

**Why this fits the actual problem better than A or B:**
- **Reachability solved directly.** The agent runs on infrastructure this
  developer controls, so private/internal targets that A structurally
  cannot reach are checkable — this is a requirement A cannot meet at any
  price, not a preference.
- **Learning budget honored, not spent sideways.** Go concurrency patterns
  and OTel pipelines are exactly the two technologies already committed to
  in `00a-ledger-confirmation.md`; building the scheduler and telemetry
  pipeline exercises both directly, rather than adopting a third
  (TimescaleDB, PromQL/Grafana) as B would require.
- **Genuine dogfooding.** It will actually run continuously against this
  developer's own live flagship deployments — its correctness gets tested
  by real operational use over weeks, not a demo script run once. That is
  the literal subject of this repository's two deep SDLC phases (Release &
  Deployment, Operations & Maintenance), and neither A nor B produces that
  proof: A is someone else's system running against your endpoints, B is
  someone else's system running unmodified on your infrastructure.
- **Sized to the real target count.** ~5–10 targets doesn't need a
  distributed TSDB or a scrape-config DSL; a scheduler with per-target run
  exclusivity and a Postgres-backed rollup job (`00-project-brief.md`,
  Continuous operation requirements) is proportionate to the actual
  problem size.

**The honest trade-off, stated plainly, not oversold:** for the narrow goal
of "get an alert when a public URL goes down," Option A is less total
effort and probably more reliable on day one than anything this project
will ship. Building pulsewatch is the right call given this portfolio's
actual goals — skill demonstration against two frozen learning objectives,
exact fit for infrastructure a SaaS can't fully reach, and real dogfooding
value — not because it's the objectively superior engineering choice in a
vacuum. A reviewer asking "why didn't you just use \[SaaS product\]" is
asking a fair question, and the answer is this list, not "because SaaS is
bad."

## Decision

**Option C — build pulsewatch**, purpose-built and scoped to this
developer's actual bounded target set, for the reasons above: reachability
requirements A cannot meet, a learning budget already frozen and honored
rather than exceeded, and dogfooding value neither alternative can produce
by construction.

## Trade-offs accepted

- More total build effort than subscribing to Option A, for a narrower
  feature set than Option A ships with on day one (no global probe
  locations, no polished status pages at launch — see
  `01-scope-and-non-goals.md`). Accepted because the reachability and
  portfolio-value gaps in A are real, not because C was assumed superior by
  default.
- Retention, rollup, and SLO math that Option B gets for free from mature
  software have to be built and verified by hand
  (`00-project-brief.md`, Continuous operation requirements §3–4).
  Accepted because building them is itself part of this repo's point, not
  incidental overhead.

## Consequences

- The success metrics in `00-project-brief.md` must actually verify the
  properties a mature alternative would have gotten for free (restart
  safety, no double-alerting, correct rollups) — there's no vendor SLA to
  lean on if these are wrong.
- `01-scope-and-non-goals.md` must explicitly exclude the features Option A
  ships with that this decision consciously trades away (status pages,
  multi-region probing, mature multi-channel notification integrations) so
  they aren't silently expected later.
- The TimescaleDB-vs-plain-Postgres decision in `00-project-brief.md` §3
  follows directly from this decision's "honor the frozen learning budget"
  constraint — it is the same reasoning applied one layer down.

## Revisit triggers

- If this developer's target count grows from ~10 into the dozens (a real
  team, not a solo portfolio), Option B's rejection above should be
  re-examined — its "overkill for the actual blast radius" argument weakens
  as the actual blast radius grows.
- If a future need arises to monitor purely public-facing endpoints with no
  reachability requirement at all (e.g., a status page for external users,
  which is itself a non-goal today — `01-scope-and-non-goals.md`), Option A
  could be reconsidered for that narrower slice specifically, without
  reopening this decision for pulsewatch's actual private-infrastructure
  use case.
