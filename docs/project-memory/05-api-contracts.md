# API / Event Contracts
> Purpose: the interface others depend on
> Project: pulsewatch (public)
> Last updated: 2026-08-29
> Depth: baseline (Architecture & Design's own depth marker,
> `docs/SDLC-EVIDENCE.md`) — this document is the API-contracts pass
> `12-session-handoff.md` flagged as required before Implementation could
> meaningfully start, written as a short intermediate session between
> Architecture (Session 3) and Session 4, not a reopening of any of the
> four ADRs. It resolves every shape ADR-0003 named but didn't fully
> specify at the wire-format level; it does not redesign anything ADR-0001,
> ADR-0002, ADR-0003, or ADR-0004 already decided.

The authoritative, validated contract is
[`docs/architecture/openapi.yaml`](../architecture/openapi.yaml) (OpenAPI
3.1) — this document is the prose rationale for the decisions that spec
encodes, matching `lexicon`'s own `05-api-contracts.md` convention of
pairing a real, validated spec with a decisions-and-why document, and
`privacy-forge`'s Session 3 practice of validating that spec with a real
tool rather than assuming hand-written YAML is correct.

## Style and rationale

REST over JSON, versioned under `/api/v1/`, for the same reason `lexicon`
gives for its own single-consumer surface: no GraphQL, no gRPC, no general
third-party API product — the only planned consumers are pulsewatch's own
SvelteKit dashboard and its own agent binary (`03-architecture.md`'s
container diagram), and neither needs anything REST/JSON can't already
express cleanly. The one exception is the agent's telemetry-reporting
path, which uses OTLP/HTTP — not because REST was rejected for it, but
because OTLP is the already-frozen transport choice for agent→server
telemetry (`00a-ledger-confirmation.md`), and ADR-0003 fixes that the
server's own ingestion endpoint is where the OTel Collector's exporter
pipeline forwards that traffic. This document treats that endpoint as
part of the contract precisely because ADR-0003 named it as required
follow-through, not because this session is expanding OTel's scope beyond
what's already decided.

Two authenticated surfaces, two different actors, matching the roles
matrix in `02-requirements.md`:

- **Operator-facing REST**: register/list/update/delete checks, view live
  status and rolling-window SLO, configure alert channels — all under
  `/api/v1/`, `operatorSession` auth.
- **Agent-facing**: an authenticated REST poll for assignment discovery
  (`GET /api/v1/agent/assignments`) plus the OTLP/HTTP ingestion endpoint
  (`POST /v1/logs`, outside the `/api/v1` prefix on purpose — see below) —
  both `agentToken` auth.

## Authentication and authorisation model

**Operator auth — stateless, HMAC-signed session cookie.** `POST
/api/v1/auth/login` issues an `HttpOnly; Secure; SameSite=Strict` cookie
containing a server-signed token (operator ID + expiry); there is no
server-side session store. This was a deliberate choice against two
alternatives:

- A server-side session table would be the "obvious" answer, but adds a
  new piece of durable state with no correctness requirement asking for
  it (`02-requirements.md`'s roles matrix commits to exactly one operator
  account) — pure overhead for a session mechanism this simple.
- Storing sessions in Redis was rejected for the same underlying reason
  ADR-0001 rejected Redis for scheduling leases, one level down in
  stakes: it would make login/logout availability depend on a component
  `03-architecture.md` explicitly scopes as "not load-bearing" for
  anything today. A stateless signed cookie has no such dependency.

Revocation is by rotating the server's signing secret (invalidates every
outstanding session at once), not per-session — acceptable for v1's
single-operator, self-hosted deployment model (`01-scope-and-non-goals.md`)
where "log out this one compromised session while leaving others valid"
isn't a scenario that exists (there is only ever one operator identity).
This is a real trade-off, not an oversight — flagged as a revisit trigger
below.

**Agent auth — a distinct machine credential, never the operator's.**
Each agent gets its own bearer token at registration
(`POST /api/v1/agents`), checked against `agents.credential_hash`
(`04-data-model.md`) — a completely separate credential space from
`operatorSession`, the same way `privacy-forge` keeps connector auth
(HMAC signature) distinct from staff auth (cookie session) rather than
reusing one scheme for two different kinds of actor. Concretely:

- **Provisioning**: `POST /api/v1/agents` returns the plaintext token
  exactly once, in a response schema (`AgentCreated`) that exists nowhere
  else in the spec — no `GET` endpoint's schema (`Agent`) has a token
  property. This is the same structural pattern FR-023 requires for
  alert-channel credentials, applied to the second secret this system
  handles.
- **Rotation**: `POST /api/v1/agents/{agent_id}/credential/rotate`
  invalidates the previous token and returns a new one, again exactly
  once (`AgentCredentialRotated`, also never reachable from a `GET`).
  Deliberately **not** idempotency-key-guarded — see Idempotency below
  for why that asymmetry with agent *creation* is intentional, not an
  inconsistency.
- **Decommissioning**: `DELETE /api/v1/agents/{agent_id}` is blocked
  (`409`) while the agent still has assigned targets. Falling those
  targets back to server-executed automatically would be actively wrong
  for a target the server structurally cannot reach — the entire reason
  the agent exists (`00b-build-vs-alternatives.md`, ADR-0003) — so
  reassignment or deletion of its targets has to be an explicit prior
  operator action, never an implicit side effect of removing the agent.

**No ABAC, and specifically no third role, was built.** The task framing
that produced this document explicitly warned against over-building a
policy matrix this repo's single-operator baseline doesn't need
(`02-requirements.md`: "v1 has no lesser-privileged human role"). The
`operatorSession`/`agentToken` split is the only authorization boundary
that exists, and it maps exactly onto the two identities the roles matrix
already names — nothing was added beyond that.

**A `403 Forbidden` response is deliberately absent from the spec.**
Because `operatorSession` (cookie) and `agentToken` (bearer) are
different credential *shapes*, not different privilege levels checked
against the same credential, an agent's bearer token can't even satisfy
an operator endpoint's security requirement — that request fails at
`401` (missing the required credential shape), never reaches a `403`
authorization check. There is no cross-role request shaped like "correctly
authenticated, but not allowed" in this system, because there are only
ever two disjoint identity types, each with endpoints scoped structurally
to itself (`/agent/assignments` has no `{agent_id}` path parameter for
exactly this reason — an agent's identity comes from its own token, so
there is no request shape for "read a different agent's assignments" to
even construct, let alone reject with `403`).

## Endpoints / schema summary

Full request/response schemas are in `openapi.yaml`; this table is the
navigation index, grouped to match the four things this session's DoD
named.

### Check registration and configuration (FR-001–FR-004, US-001, US-002)

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/v1/targets` | Register an HTTP or TCP check |
| `GET` | `/api/v1/targets` | List targets (unpaginated — bounded scale) |
| `GET` | `/api/v1/targets/{target_id}` | Get configuration |
| `PATCH` | `/api/v1/targets/{target_id}` | Update interval/threshold/timeout/body-match/agent assignment — identity fields (`type`/`url`/`host`/`port`) are immutable, delete-and-recreate to change what's monitored |
| `DELETE` | `/api/v1/targets/{target_id}` | Soft delete |

### Live status and rolling-window SLO (US-004, US-009, FR-010, FR-011, FR-019, FR-020–022, NFR-009)

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/v1/targets/{target_id}/status` | Live state — see "Alert-suppression states at the API layer" below |
| `GET` | `/api/v1/targets/{target_id}/slo?window_days=&slo_target_pct=` | Rolling-window uptime% and error budget, read from `check_rollups_hourly` only |
| `GET` | `/api/v1/targets/{target_id}/incidents` | Incident history, open vs. resolved |

### Alert channels, write-once credentials (FR-013, FR-014, FR-023)

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/v1/alert-channels` | Configure a webhook/email channel |
| `GET` | `/api/v1/alert-channels` | List (never includes the destination) |
| `GET` | `/api/v1/alert-channels/{id}` | Detail (never includes the destination) |
| `PUT` | `/api/v1/alert-channels/{id}/secret` | Replace the destination/credential — `204`, no body |
| `DELETE` | `/api/v1/alert-channels/{id}` | Delete |

### Agent registration and machine auth (ADR-0003, roles matrix)

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/v1/agents` | Register — returns the token once |
| `GET` | `/api/v1/agents` | List (never includes a credential) |
| `GET` | `/api/v1/agents/{id}` | Detail incl. `last_heartbeat_at`, `is_stale` |
| `POST` | `/api/v1/agents/{id}/credential/rotate` | Rotate — returns a new token once |
| `DELETE` | `/api/v1/agents/{id}` | Decommission — `409` while targets remain assigned |

### Agent-facing surface (ADR-0003)

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/v1/agent/assignments` | Authenticated REST poll — assignment discovery |
| `POST` | `/v1/logs` | OTLP/HTTP log ingestion — check results + heartbeat |

## The credential-handling structural property (FR-023)

FR-023 requires alert-channel credentials to be "never returned in
plaintext by any API after initial creation." The spec makes this a
structural property of the schemas, not a convention a handler author has
to remember to honor:

- `AlertChannel` — the **only** schema every read path (`GET` list,
  `GET` detail, and the `201` response from `POST`'s echo) uses — has no
  `destination` property in its JSON Schema definition at all. There is
  no field for a value to accidentally leak into, not merely a field the
  server chooses not to populate.
- The only write path for a channel's secret, `PUT
  .../{id}/secret`, returns `204 No Content` — no response body, so there
  is no schema in that direction either that could carry the value back.
- The identical pattern is applied to the second secret this system
  handles, the agent credential: `Agent` (every `GET` schema) has no
  token property; only `AgentCreated` and `AgentCredentialRotated` — each
  reachable from exactly one endpoint, never from a `GET` — carry a
  plaintext token, and only once.

This is checkable directly against `openapi.yaml`: grep the file for
`destination` or `token` and confirm every match sits inside a
create/rotate-only schema, never inside `AlertChannel` or `Agent`.

## Alert-suppression states at the API layer (ADR-0002, ADR-0003)

`GET /targets/{target_id}/status` returns both:

- `raw_state` — `target_schedule.state` verbatim: `healthy` / `suspect` /
  `alerting`. Per ADR-0002, `unknown` is never a value this column holds.
- `display_state` — the ADR-0002-Consequences-mandated read-time overlay:
  `unknown` if the target is agent-assigned and that agent is currently
  stale, else `raw_state` unchanged. This is computed at request time from
  `agents.last_heartbeat_at`, exactly as ADR-0003 specifies (`now() -
  last_heartbeat_at > 3 × report_interval`), never a maintained flag that
  could itself drift out of sync with the fact it represents.

The response also carries `open_incident` (id + `opened_at`) when
`raw_state = alerting`, giving the dashboard everything US-009/FR-022
needs without a second round-trip.

**A precise point this contract had to get right, not gloss over:** a
live "unknown" from pulsewatch's own downtime (FR-011) cannot appear in
this endpoint's response, structurally — the API can't be queried while
the server that serves it is down. That cause of "unknown" is therefore
retrospective by nature and only ever surfaces in the `/slo` endpoint's
`unknown_count` (already unified with agent-staleness `unknown_count` at
the rollup layer per `04-data-model.md`). `display_state` on the live
status endpoint only ever needs to check agent staleness — this isn't an
omission of the pulsewatch-downtime case, it's a consequence of which
endpoint can observe which cause.

## Error model

Standard HTTP status codes, one JSON error envelope:
`{ "error": { "code": string, "message": string, "field": string|null } }`.

| Status | Used for |
|---|---|
| `400` | Malformed request body |
| `401` | Missing/invalid credential — either auth scheme |
| `404` | Resource not found (or not visible to the caller) |
| `409` | `Idempotency-Key` reuse with a different body; deleting an agent that still has assigned targets |
| `422` | Well-formed but semantically invalid (interval outside `[10s, 24h]`, missing `url` for an `http` target, etc.) |
| `503` | Postgres unavailable — surfaced explicitly rather than hanging or a bare `500`, matching `03-architecture.md`'s stated degradation mode ("the scheduler degrades to doing nothing rather than doing something incorrect") extended to the read/write API path fronting the same database |

No `403` — see Authentication and authorisation model above for why that
isn't a gap.

## Idempotency, pagination, rate limits

**Idempotency — the four cases this session's DoD named:**

1. **Retried check registration.** `POST /targets` accepts an optional
   `Idempotency-Key` header (client-generated UUID). A retry with the
   same key and an identical body replays the original cached response
   (including its `201` body) instead of creating a second target; the
   same key with a *different* body is a `409` (key-reuse conflict, not a
   resource conflict). Same mechanism applies to `POST /alert-channels`
   and `POST /agents` — the latter matters specifically because its
   response carries a one-time credential, so the idempotency cache (kept
   for the same window, 24h) is the one deliberate, time-bounded
   exception to "never retrievable again": it exists purely to make a
   retry of the *original* request safe, not a second read path.
2. **Duplicate agent result submission (OTLP path).** A completed check's
   own execution timestamp (`timeUnixNano` on its OTLP log record) *is*
   `checked_at` — never duplicated into a separate custom attribute — and
   doubles as the natural idempotency key together with `target_id`. A
   resubmission carrying an identical `(target_id, checked_at)` pair is a
   no-op: no new `check_results` row, and — this is the part that isn't
   obvious from ADR-0001/ADR-0002 alone — the ADR-0002 transition
   function must **not** be re-evaluated for it either. ADR-0001's
   "re-running a check is idempotent" argument is about re-*executing* a
   check (a claim retry producing a fresh, independent result); it says
   nothing about the *same already-completed result* arriving twice over
   OTLP, and naively re-running the transition function on a duplicate
   would double-increment `streak` even though ADR-0002's conditional
   `INSERT`/`UPDATE` guards would still prevent a duplicate *dispatch*.
   The contract therefore requires the dedup check to gate entry to the
   transition function, not just the final write. This needs a
   uniqueness constraint on `check_results (target_id, checked_at)` that
   `04-data-model.md` currently specifies only as a plain (non-unique)
   index — flagged here as a required additive index for whoever
   implements this endpoint, not a redesign of that document's schema.
3. **Duplicate `POST /agents/{id}/credential/rotate`.** Deliberately
   **not** idempotency-key-guarded, unlike agent creation. The risk
   idempotency keys exist to prevent — a network retry silently creating
   a second resource, or silently burning a secret the caller already
   saw — doesn't apply here: if the first rotation's response never
   reached the caller, the caller never learned that token, so a second
   rotation minting a fresh one is harmless (nothing valid gets
   invalidated out from under a caller who's actually holding it).
4. **Alert-channel secret rotation and agent decommissioning** follow
   ordinary `PUT`/`DELETE` semantics (naturally idempotent for `DELETE`;
   `PUT .../secret` replacing a value is safe to retry by construction).

**Pagination:** none in v1. Every list endpoint (`targets`, `agents`,
`alert-channels`, `incidents` per target) is bounded by this project's own
measured scale (~5–10 targets, `00-project-brief.md`) the same way
`lexicon` reasons about its own small, bounded document/corpus lists —
revisit only if a target's incident count or the agent/target counts
themselves grow enough to matter in practice, which nothing in this
project's stated scale suggests.

**Rate limits:** none. No NFR in `02-requirements.md` asks for one — this
system's cost/abuse profile (a single operator's own dashboard and a
handful of trusted agents, no third-party consumer, no per-request
external-provider cost like `lexicon`'s LLM calls) gives no real
motivation to invent a limit unasked for.

## Versioning and deprecation policy

`/api/v1/` now. `/v1/logs` (the OTLP ingestion path) is deliberately
**outside** that prefix — it's the OTel Collector's standard receiver
path convention (ADR-0003), not a resource of this product's own REST API,
and versioning it under `/api/v1` would misrepresent it as one. No
deprecation window is defined yet (matching `lexicon`'s same reasoning):
the only consumers are this repository's own dashboard and agent binary,
which ship and version together with the server in this project's actual
release model (`03-architecture.md`) — there is no external integrator
whose breakage this document needs to plan around yet.

## Events published/consumed

No internal job queue or event-bus contract exists to document here — 
unlike `lexicon`'s Redis-backed ingestion job, pulsewatch's background
work (rollup computation, retention, scheduler ticks) is entirely
Postgres-state-driven per ADR-0001/ADR-0004, not event-triggered. The one
outbound "event" this system produces is alert dispatch (webhook `POST` /
email, FR-013/FR-014) — its payload shape and delivery semantics
(`alert_dispatches.delivery_confirmed`) are an Implementation (Session 4)
concern per ADR-0002 Consequences, not fixed at the wire level in this
document, since no external consumer contract depends on a fixed webhook
body shape today (the receiving endpoint is the operator's own choice of
webhook target, configured, not standardized).

## Validation

Validated with [`@redocly/cli`](https://redocly.com/docs/cli) `lint`
(OpenAPI 3.1-aware; used in place of `openapi-spec-validator` because this
environment has no working Python 3 interpreter — `@redocly/cli` is the
equivalent, actively maintained tool for the same job, run the same way:
a real linter against the real spec file, not a hand-inspection):

```
$ npx --yes @redocly/cli lint docs/architecture/openapi.yaml

validating docs\architecture\openapi.yaml...
docs\architecture\openapi.yaml: validated in 118ms

Woohoo! Your API description is valid. 🎉
```

Zero warnings, zero errors, on the `recommended` ruleset (the strictest
built into the tool) — the spec was iterated against this exact command
until it reached that state (an earlier pass had 27 warnings: missing
`operationId`s, a missing `info.license.url`, an unused `Forbidden`
response, and a `then`/`else` conditional-schema pattern the linter
couldn't resolve without redundant local `properties` — all fixed in the
committed file, not suppressed).

**Re-run this check in CI from whichever session first stands up backend
CI against real endpoint code** (Session 4 or later), so the contract
can't silently drift into an invalid state as endpoints are actually
implemented — the same practice `privacy-forge`'s own Session 3 flagged
for its equivalent spec.

## Notes for Session 4 and beyond (not gaps in the four ADRs — see below)

- `04-data-model.md` needs one additive index this contract's idempotency
  design depends on: `UNIQUE (target_id, checked_at)` on `check_results`
  (currently specified there only as a plain index). This is a schema
  *addition*, not a change to anything ADR-0001/ADR-0002 decided — flagged
  per this session's ground rules as something the contract work revealed
  is needed, not something silently redesigned.
- The OTel Collector's exporter pipeline config must preserve the agent's
  `Authorization` bearer header end-to-end so `/v1/logs` can authenticate
  the forwarded request with the same `agentToken` scheme
  `/agent/assignments` uses — a Collector-config detail for whichever
  session wires up `docker-compose.yml` (already named as required future
  work in ADR-0003 Consequences; this document just makes the auth
  requirement concrete at the endpoint level).
- **No gap was found in any of the four ADRs themselves.** Every shape
  this document had to invent (session-cookie mechanism, agent-credential
  provisioning/rotation, idempotency keys, the `(target_id, checked_at)`
  dedup key, the `slo_target_pct` request parameter) was a wire-level
  concretization of something already decided, not evidence that
  ADR-0001–0004 left a real hole. The one item above is a `04-data-model.md`
  addition, and `04-data-model.md` is not one of the four ADRs.
