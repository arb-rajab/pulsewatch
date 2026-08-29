# ADR-0003 — Agent-Initiated Push Reporting and Assignment Discovery

- **Date:** 2026-08-29
- **Status:** accepted

## Context

`00b-build-vs-alternatives.md` names private-target reachability as real,
load-bearing justification for building an agent at all: several of this
developer's actual targets are not reachable from a third-party SaaS
prober or, in some deployments, even from the pulsewatch server process
itself. `02-requirements.md` turns that into concrete requirements —
FR-017 (agent executes checks locally and reports results), FR-018
(agent-to-server communication is agent-initiated/outbound only; no inbound
port on the agent's network), FR-019 and NFR-008 (the server must
distinguish "the agent stopped reporting" from "the agent's target is
down") — and deliberately leaves two things open for this session: *how* an
agent learns its assigned target list, and the concrete mechanism behind
the staleness-vs-target-down distinction.

## Decision 1 — Direction: agent-initiated push, not server-polls-agent

### Option A — Server polls each agent (pull)

The server periodically calls out to a listening endpoint on each agent to
request a check run or result.

**Rejected outright.** This requires the agent to run a network service
reachable *from* the server — an inbound port on the agent's network. That
is precisely the exposure FR-018 and the entire agent-based justification
in `00b-build-vs-alternatives.md` exist to avoid: if the agent must accept
inbound connections, it has the exact same reachability problem a hosted
SaaS prober already has for that network, which defeats the reason the
agent exists at all.

### Option B — Agent-initiated outbound push (chosen)

The agent runs its own local scheduler (executing checks against its
assigned targets on their configured intervals — the same per-check
scheduling shape as the server's own scheduler, ADR-0004) and reports
outcomes to the server. The server never needs a network path to the agent
or its targets. Directly satisfies FR-018.

## Decision 2 — Assignment discovery mechanism

The open question `12-session-handoff.md` (Session 2) deliberately left
for this session: how does an agent learn *which* targets it's responsible
for?

### Option A — Static config file, manually distributed per agent

**Rejected.** Zero coupling to server availability is a real advantage, but
every target-list change (add/remove/edit a target assigned to that agent)
requires manually redistributing a file to that specific agent's host — a
real, not hypothetical, operational cost at this project's actual
single-operator homelab scale. Worse, it silently diverges from the
server's own database as the system of record for target configuration:
`02-requirements.md` (US-001/US-002) already has the operator register
targets through the server API — a static file would mean a second,
disconnected step to make an assigned agent aware of a target the operator
just registered through the dashboard, undermining the single-source-of-
truth framing the rest of the requirements assume.

### Option B — Agent polls the server for its assignment list (chosen)

An authenticated REST `GET` from the agent to the server, on a fixed
interval, using the agent's own service credential
(`02-requirements.md` roles matrix). Still agent-initiated, still outbound
only, still FR-018-compliant — only the *direction* of the connection
matters for the inbound-port constraint, not whether the agent asks versus
is told.

**For:** single source of truth — the server's own `targets` table, the
same one US-001/US-002 already write to. An operator assigning a target to
an agent through the dashboard takes effect automatically within one poll
interval, with no manual distribution step. Reuses infrastructure that
already has to exist for every other server API (Gin REST handlers, the
same agent authentication scheme) rather than inventing a second channel.

**Against:** assignment-list staleness bounded by the poll interval — an
accepted, small lag (see Trade-offs).

### Option C — Server pushes assignments over the OTel/OTLP channel

Treat OTLP, already used for telemetry (below), as a bidirectional control
channel and push assignment changes down to the agent through it.

**Rejected.** OTLP's receiver/exporter model, and the OTel Collector's
pipeline architecture generally, is built around one-directional telemetry
export — an SDK exports data outward to a receiver; it is not designed as
a general request/response mechanism for a collector or backend to push
arbitrary application data back down to the exporting agent. Forcing a
config-push use case through a telemetry-shaped protocol means fighting the
tool's actual design, and blurs the OTel learning objective's actual scope
(`00a-ledger-confirmation.md`: "agent → server telemetry," not a general
control-plane protocol). Option B is a plain authenticated REST poll the
Gin backend already needs for everything else — it costs nothing extra
against the learning budget either.

## Decision 3 — Telemetry transport and the staleness/target-down distinction

**Transport (already frozen as a technology choice, fixed here as a
concrete shape):** after each locally-executed check, the agent exports
the result as OTLP telemetry (target ID, outcome, latency, timestamp,
failure reason) to the OTel Collector. Independently of any individual
target's check interval, the agent also emits a periodic heartbeat signal
every `report_interval` — an agent with zero assigned targets still
heartbeats, so "the agent process is alive" is observable even before it
has anything to check. This is what NFR-008's staleness threshold (> 3×
report interval) is measured against. The OTel Collector's exporter
pipeline forwards agent OTLP traffic to the pulsewatch server's own
ingestion endpoint; **the server, not the collector, writes to Postgres** —
agent-sourced results go through the identical result-recording path
(ADR-0001/ADR-0002) the server's own directly-executed checks use, they
just arrive via the collector instead of the in-process worker pool. This
keeps "the server is the sole writer of persisted state" true regardless of
which path a result took to get there.

**Staleness vs. target-down (FR-019, NFR-008), stated as an explicit
ordering rule:** for any target assigned to an agent, agent staleness is
evaluated *before* that target's own alerting state machine (ADR-0002).
Staleness is computed at read time — `now() - agents.last_heartbeat_at >
3 × agents.report_interval` — rather than maintained as a separately
updated flag that could itself drift out of sync with the fact it
represents.

- If the agent is stale: the target's state is `Unknown` for that period,
  full stop — ADR-0002's transition function is **not evaluated at all**
  during it. This is the identical "no evaluation happened" mechanism
  ADR-0002 already defines generically for `Unknown` — agent staleness is
  one concrete cause of it, a gap in the server's own scheduler (pulsewatch
  itself was down) is another; both use the same mechanism, not two
  separate unknown-handling code paths.
- If the agent is fresh: the target's state is whatever ADR-0002's machine
  says from its actually reported results — including, correctly, reaching
  `Alerting` if the target itself is really down.

This ordering is precisely what makes "agent unreachable" structurally
distinct from "target down": the former suppresses evaluation entirely
(`Unknown` — "we have no data"), the latter is a real `Failure` outcome fed
into the state machine (potentially reaching `Alerting` — "we have data and
it's bad"). The dashboard and alerting code must never conflate the two by,
say, treating a stale agent's last-known result as still current.

## Trade-offs accepted

- Assignment changes take up to one poll interval to reach an agent, not
  instant. Accepted: target assignment is a slow-moving configuration
  operation, not a latency-sensitive path like alerting (NFR-002's ≤5s
  budget applies to alert dispatch, which has no relationship to this
  interval). The poll interval is an implementation default (e.g. 60s),
  not a numeric requirement this document is inventing.
- Two distinct outbound channels from the agent — REST poll for
  assignments, OTLP for telemetry — instead of one unified channel.
  Accepted because each is the right shape for its own job (request/
  response config sync vs. one-way telemetry export); unifying them would
  mean distorting one into a shape it doesn't fit, per Option C's
  rejection above.

## Consequences

- `05-api-contracts.md` (not written this session) must define the
  agent-assignment REST endpoint and its auth, reusing the Agent role from
  `02-requirements.md`'s roles matrix — named here as required follow-
  through, not silently deferred.
- `03-architecture.md`'s container diagram shows the OTel Collector as a
  distinct container between agents and the server, with the server (not
  the collector) as the actual Postgres writer.
- Implementation (Session 4) must add an OTel Collector service to
  `docker-compose.yml` and wire its receiver/exporter pipeline config — not
  done this session per the ground rule against implementation code, named
  here so it is not a surprise.

## Revisit triggers

- If agent/target count grows enough that per-agent REST polling load
  becomes real (unlikely at this project's ~5–10 target scale), consider a
  longer poll interval or conditional-GET caching before considering a
  push-based redesign — tune the existing model, don't abandon it.
- If OTel Collector tooling matures a well-supported bidirectional
  config-distribution pattern that fits this repo's learning objective
  better, reconsider folding assignment discovery into that channel — not
  reconsidered now because no such well-trodden pattern exists today, per
  Option C's rejection above.
