# ADR-0010 — Live Dashboard Push: Server-Sent Events, Not WebSocket

- **Date:** 2026-09-21
- **Status:** accepted

## Context

Every other real-time-feeling surface in this developer's portfolio
(`notifyhub`) uses a GraphQL subscription over WebSocket, and pulsewatch's
own dashboard (`/dashboard`, `/dashboard/devices`) has had no live-push
option at all up to this session — an operator only ever sees a target's
current `display_state`/incident/device-token status at the moment
SvelteKit's server `load` function ran, which is once per navigation or
manual refresh. For a monitoring/uptime tool, an operator watching the
dashboard while an outage is actively unfolding is exactly the scenario
this project exists for, and it is currently the one scenario the
dashboard serves worst: they find out a target went `alerting` only by
refreshing the page.

This session closes that gap. The question is which transport, not whether
to build one — `notifyhub`'s WebSocket precedent does not, by itself,
answer that for pulsewatch, because the two systems have different traffic
shapes.

## Decision — Server-Sent Events (SSE), not WebSocket

### What the dashboard actually needs to send

Every real trigger this session hooks into is **server-to-client only**:

- `alerting.RecordCheckResult` persisting a new `target_schedule.state`
  (healthy/suspect/alerting) after a real check result — the same write
  `scheduler.releaseAndRecord` and `agentapi.recordAgentCheckResult` already
  commit.
- `alerting.OpenIncident`/`CloseIncident` returning a row — the same
  guarded write that already gates `NotifyChannels` (ADR-0002's "dispatch
  is triggered only after the conditional incidents write actually returned
  a row").

Nothing a dashboard viewer does needs to travel back to the server over
this channel. The dashboard already has a full, separate, authenticated
REST surface (`operatorapi`) for every operator-initiated action — creating
a target, registering an alert channel, revoking a device token. There is
no "operator clicks acknowledge and the server needs to hear about it over
the same live connection" requirement anywhere in `02-requirements.md`, and
this session does not invent one (see Scope below) — the real incident
lifecycle this codebase has is open/resolved (`incidents.opened_at`/
`closed_at`), not a three-state open/acknowledged/resolved one, and this
session does not add an acknowledge concept to reach for a bidirectional
justification that doesn't otherwise exist.

### Option A — WebSocket (rejected for this use case)

A WebSocket gives a bidirectional pipe pulsewatch's dashboard has no real
use for today. Taking it on here would mean:

- A second protocol to authenticate (the dashboard's session cookie model
  is plain HTTP; a WS upgrade handshake still carries it, but every
  disconnect/reconnect is then a full handshake this project would have to
  test in both directions for no traffic that ever flows client-to-server).
- A subscription/topic layer of pulsewatch's own design, since this
  project has no GraphQL layer the way `notifyhub` does — `notifyhub`'s
  WebSocket usage rides on GraphQL subscriptions already being that
  system's query layer; pulsewatch's operator API is plain REST, so
  reaching for WebSocket here would mean bolting on a bidirectional
  transport with no existing query/mutation layer underneath it to justify
  the extra direction.
- Reconnection/backoff logic pulsewatch would still have to hand-roll
  either way (browsers implement no native WebSocket reconnection), with
  none of SSE's compensating simplicity.

Rejected: it is more transport than the actual data flow (one-directional,
server→client, small JSON payloads, infrequent — an operator's ~5-10
targets do not check more than once every `interval_seconds`, schema
floor 10s) needs, and it would not reuse anything pulsewatch's stack
already has (no GraphQL layer, unlike `notifyhub`).

### Option B — Server-Sent Events (chosen)

SSE is plain HTTP: `GET /api/v1/events` with `Content-Type:
text/event-stream`, held open, authenticated by the exact same
`RequireOperator` session-cookie middleware every other `operatorapi` route
already uses — no separate handshake, no separate auth story, no new
dependency (Go's `net/http`/Gin already stream a response body; the
browser's native `EventSource` already reconnects on its own with a
server-suggested `retry:` backoff, which this session also layers a
capped exponential backoff on top of client-side, since `EventSource`'s own
default retry is fixed-interval — see Consequences).

**For:**
- Matches the actual traffic shape exactly: one direction, small discrete
  events (`target_status`, `incident`), no client-to-server payload ever
  needed on this channel.
- Reuses `operatorapi`'s existing session-cookie auth verbatim — the SSE
  route sits behind the same `RequireOperator(sessionSecret)` middleware
  every other route in `router.go` does, not a parallel auth mechanism.
- Native browser reconnection (`EventSource`) is a real, load-bearing
  simplification `notifyhub`'s WebSocket-based transport doesn't get for
  free — the client-side code in this session only has to add backoff
  *tuning* on top of it, not build a from-scratch reconnect loop.
- Ordinary HTTP: the same reverse-proxy path (`Caddyfile`, R-001) that
  already terminates every other `operatorapi` request handles a
  long-lived `text/event-stream` response with no new proxy configuration.

**Against:** SSE is one-directional and text-only (UTF-8 event framing,
not raw binary). Accepted — see "What the dashboard actually needs to
send" above; neither limitation is a real constraint against the traffic
this session actually pushes.

## Decision — hook point: the same real write path, not a new detector

The one new component this session adds is `internal/livefeed`: a small
in-process pub/sub hub (`Hub.Publish`/`Hub.Subscribe`), no message broker,
no new persistence. It is wired in at exactly two points, both already the
single place a state change becomes durable:

1. `scheduler.releaseAndRecord` — after its existing commit, using the
   `alerting.Recorded` value that commit already produced (state, streak,
   and — unchanged — the same `*DispatchRequest` `NotifyChannels` already
   consumes).
2. `agentapi.recordAgentCheckResult` — same `alerting.Recorded` value, same
   commit, for agent-reported results.

Neither call site's SQL, transaction shape, or return contract changes;
`livefeed.Publish` runs after the commit has already succeeded, mirroring
exactly how `NotifyChannels` itself is only ever invoked after the
guarded incidents write already returned a row. A dashboard event is
never the trigger for a dispatch, and a dispatch is never gated on a
dashboard event being deliverable — the two consumers of `alerting.Recorded`
are independent and neither can block the other (see Consequences: hub
delivery is non-blocking/best-effort).

## Scope

- **In scope:** real state transitions (`healthy`/`suspect`/`alerting`)
  and real incident open/resolve events, pushed to already-open dashboard
  sessions, additive to the existing SSR `load` fetch (initial load /
  full-refresh fallback, unchanged).
- **Out of scope, deliberately:** an "acknowledged" incident state. This
  codebase's incident lifecycle is two edges (open, resolve —
  `incidents.opened_at`/`closed_at`, ADR-0002), not three; adding an
  acknowledge concept would be new incident-lifecycle surface, which this
  session's own ground rules exclude ("do not touch the core incident
  lifecycle"). The live-push event vocabulary (`kind: "opened" |
  "resolved"`) matches the real lifecycle exactly, not a hypothetical
  richer one.

## Consequences

- `internal/livefeed.Hub.Publish` is fire-and-forget and non-blocking: a
  slow or gone subscriber's buffered channel filling up means that one
  subscriber drops the event (and, on next reconnect, gets the dashboard's
  existing SSR `load` path as a full resync) — it never backs up into, or
  slows down, the scheduler tick loop or the OTLP ingestion path that
  produced the event. Silence on this channel is never treated as a
  incident/state signal on its own; the REST endpoints remain the
  source of truth an SSE client resyncs against on reconnect.
- The dashboard's existing polling/initial-load path
  (`+page.server.ts`'s `load`) is untouched and stays the fallback: a
  client with JavaScript disabled, or one that never establishes/loses its
  SSE connection, still sees correct (if not live) data on every
  navigation or manual refresh.
- `GET /api/v1/events` is additive to `docs/architecture/openapi.yaml`'s
  existing `operatorSession`-gated surface, not a replacement for any
  existing route.

## Revisit triggers

- If a genuine operator-initiated action ever needs to ride the same live
  channel back to the server (e.g., a real acknowledge feature added in a
  future session with its own ADR), reconsider whether that one feature
  justifies a bidirectional transport for just that path — SSE plus the
  existing REST surface for the write, not a wholesale WebSocket
  migration, since every other event this ADR pushes stays one-directional
  regardless.
- If dashboard scale grows enough that many concurrent long-lived
  `GET /api/v1/events` connections become a real resource concern on
  `operatorSrv` (unlikely at this project's single-operator scale), revisit
  connection limits/heartbeat interval tuning before reconsidering the
  transport itself.
