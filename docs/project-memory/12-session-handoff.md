# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed

- Session number and title: **Session 16 — `GET /targets/{id}/incidents` (B-002).**
- Objective: implement B-002, the last small pure-read gap in the
  operator-facing incidents surface (`05-api-contracts.md`/`openapi.yaml`
  already specified the endpoint and its `Incident` schema; nothing about
  the contract needed inventing).
- Status: **done.** Before writing any code, this session verified — by
  reading `04-data-model.md`, `09-decision-log.md` (ADR-0002), and
  `backend/internal/alerting/incident.go` directly — that incident
  detection and the open/close state machine were **already fully
  implemented and tested** (ADR-0002, accepted Session 2/4-era, exercised
  by `internal/alerting`'s own concurrency-proof tests and wired into
  `scheduler.releaseAndRecord`'s transaction). This session's own task
  framing described B-002 as if incident detection, state transitions, and
  the endpoint all still needed to be built from scratch; that framing was
  wrong for this repo's actual state, and this session did not invent a
  second, parallel incident-detection path to match it — it read the real
  code first, confirmed the backlog's own "Small... simple direct
  `incidents` read, no new computation" framing was the accurate one, and
  implemented exactly that: the one genuinely missing piece, a read
  endpoint over the table ADR-0002 already writes.

### What was actually built this session
- `backend/internal/operatorapi/incidents.go`: `GetTargetIncidents`, mirroring
  `GetTargetStatus`/`GetTargetSlo`'s established handler shape exactly — a
  target-existence check (`404` if missing/soft-deleted), then a single
  `SELECT ... ORDER BY opened_at DESC` against `incidents`, with `status`
  (`open`/`resolved`) derived from `closed_at` at read time (the schema
  itself has no status column — see ADR-0002). Unpaginated, per the
  contract's own documented reasoning. No incidents-table write path was
  added or changed — this handler is read-only.
- `backend/internal/operatorapi/router.go`: wired
  `GET /targets/:target_id/incidents` onto the existing `auth`-gated group.
- `backend/internal/operatorapi/incidents_test.go`: four new tests against
  a real Postgres — empty history for a fresh target, `404` for an unknown
  target ID, correct `status`/ordering across one open and one resolved
  incident inserted directly (mirroring `status_test.go`'s own existing
  shortcut of inserting `incidents` rows directly rather than re-driving
  ADR-0002's guarded writes, which `internal/alerting` already tests
  exhaustively), and a same-request-response scoping check (target B never
  sees target A's incidents).
- `backend/internal/operatorapi/middleware_test.go`: added the new
  `/incidents` route (and, found as a genuine pre-existing gap while
  editing this file, the already-shipped `/slo` route, which this table's
  own doc comment claims is "exhaustive against router.go" but wasn't) to
  `TestEveryOperatorFacingEndpoint_RejectsUnauthenticatedRequests`'s route
  table.

### Verification performed (real, not just `go build`)
- Started this sandbox's own installed local Postgres 16 (`service
  postgresql start` — present but not running at session start, no Docker
  daemon available here, consistent with prior sessions' own sandbox
  findings), created the `pulsewatch`/`pulsewatch` role+database, and
  applied all nine committed `backend/migrations/*.up.sql` files directly
  via `psql` in order (no `golang-migrate` binary available in this
  sandbox; the SQL itself is this repo's real, committed migration
  content, not a hand-written substitute).
- `go build ./...` and `go vet ./...`: clean.
- `go test ./internal/operatorapi/...` (all tests, not just the new ones):
  all pass against the real database, including this session's four new
  tests and the pre-existing full suite (targets/status/slo/alert-channels/
  agents/auth/middleware).
- `go test ./...` (every package): all pass — `alerting`, `agentapi`,
  `agentauth`, `operatorauth`, `rollup`, `scheduler` all green, confirming
  no regression from the new route/handler.
- `npx --yes @redocly/cli lint docs/architecture/openapi.yaml`: still
  valid, zero warnings — this session's endpoint matches the contract
  already committed there; no spec change was needed.
- **R-005's known test-fixture-cleanup gap (`10-risk-register.md`)
  reproduced exactly as documented**, even with no live backend container
  running against this local Postgres: a full `go test ./...` run left 44
  orphaned `targets` rows, 19 orphaned `incidents`, and 9 orphaned `agents`
  behind (confirmed by `created_at`/`registered_at` timestamps all falling
  within this session's own ~50-second test run, not real or prior data —
  `operators` cleaned up correctly, matching R-005's own description of
  which cleanup paths already handle ordering correctly). Deleted directly
  after confirming timestamps, following this project's own established
  practice (`00c-evidence-preservation.md`'s sibling discipline of
  confirming before deleting, applied here to real never-should-persist
  test rows rather than evidence). This is **B-006/R-005 reproducing**,
  not a new defect introduced by this session's own new test files (which
  themselves clean up correctly via `t.Cleanup`) — left undiagnosed
  further and unfixed, exactly as B-006 already scopes it as its own
  future item, not folded into this session's bounded B-002 objective.

### What was deliberately not done
- No frontend change. B-002 as specified, and as every prior "Small,
  no new computation" precedent (Session 9's `/status`, Session 12's
  `/slo`) actually delivered, is a backend endpoint; the dashboard
  consuming it is a separate, not-yet-scoped item (nothing in
  `frontend/src/routes/dashboard/` currently calls `/status` or `/slo`'s
  sibling incidents data either, so adding one only for `/incidents`
  would be inventing scope, not matching an existing pattern).
- No new ADR and no new decision-log entry: this session made zero design
  decisions ADR-0002/`04-data-model.md`/`05-api-contracts.md` hadn't
  already made. The one non-decision worth naming explicitly: `status`
  (`open`/`resolved`) is computed from `closed_at IS NULL` at read time,
  never stored — the `Incident` schema in `openapi.yaml` already specifies
  this enum, and ADR-0002's own schema (`04-data-model.md`) already
  specifies `opened_at`/`closed_at` with no separate status column, so
  deriving it at the handler layer is the only shape consistent with both
  already-committed documents, not a new choice this session had to make.
- Did not touch `internal/alerting`, `internal/scheduler`, or any
  incident-opening/closing code — B-002 needed none of it, and this
  session confirmed that by reading the existing, tested implementation
  first rather than assuming (per this session's own task framing) that
  it needed to be built.
- Did not start B-012 (docs) or attempt B-013 (Terraform/cert-manager/
  NetworkPolicy) — out of this session's scope per its own instructions,
  and this session's own sandbox was never checked for `registry.terraform.io`/
  registry-blob egress (irrelevant to a pure Go-backend task).

## Real gaps found and named during this session
- The `middleware_test.go` route-audit table's own doc comment claims
  exhaustiveness against `router.go` but was missing the already-shipped
  `GET /targets/{target_id}/slo` route (a pre-existing gap, not introduced
  this session) — fixed as a one-line addition alongside adding this
  session's own new `/incidents` route, since leaving a known-false
  "exhaustive" claim in a file this session was already editing would
  itself be a new, avoidable inaccuracy.
- R-005 (test-fixture cleanup leaving orphaned rows in a persistent local
  Postgres) reproduced again, in a sandbox with no live backend container
  at all — confirming the leak's root cause is broader than "a live
  scheduler races test cleanup" alone (this session's Postgres had no
  scheduler running against it), and is at least partly just the
  documented non-transactional, non-`CASCADE` cleanup ordering across
  packages (B-006) by itself, independent of a live container's
  concurrent inserts. Recorded here rather than reopening R-005/B-006's
  own text, since this session's bounded scope was B-002, not test-hygiene
  remediation.

## Open questions and risks
- **R-002, R-003, R-005, R-006, R-007, R-008 (carried forward, untouched
  this session):** unchanged — see `10-risk-register.md`. This session's
  scope was the B-002 read endpoint only.
- **B-004 through B-008, B-010 through B-013 (carried forward from prior
  sessions, untouched):** see `11-backlog.md`. B-002 is now closed.

## Next recommended session
- Proposed session title: **alert dispatch delivery** (webhook/email
  actually sending, per ADR-0002 Consequences: "the delivery mechanism's
  own retry/outbox semantics are an Implementation concern, deliberately
  left there") — `internal/alerting/dispatch.go`/`channel.go` already
  exist; check their current state directly before assuming what's built
  vs. still a stub, the same discipline this session applied to B-002.
  Alternatively, B-006 (test-fixture cleanup ordering) is now a
  well-evidenced, bounded hygiene item if a session wants a small,
  self-contained fix with real payoff (two independent reproductions this
  session and Session 12/13/14 already, with the mechanism well
  understood).
  - Do **not** reopen ADR-0001–0005 without new measured/real evidence.
  - Do **not** attempt B-013 (Terraform/cert-manager/NetworkPolicy) without
    first testing this sandbox's own egress policy directly.
- Inputs required: this handoff; `internal/alerting/dispatch.go` and
  `channel.go` (read directly, not assumed); `10-risk-register.md`'s R-005/
  B-006 if picking up test hygiene instead.
- Definition of done: whichever item is picked up, verified against a real
  local Postgres with real `go test` output, per this project's own
  standard.

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Repository state:** branch `main` (this session developed on
`claude/incidents-endpoint-detection-9z6f44` per its own worker-branch
instructions), unreleased (pre-v0.1.0). B-002 is now closed; everything
Session 15's own handoff listed as complete remains complete and
untouched.

**Problem being solved:** unchanged — see `00-project-brief.md`.

**Users:** unchanged — single operator plus the machine "Agent" role (`02-requirements.md`).

**Current stack:** unchanged. No new technology was added — B-002 is a
plain Go/Gin/pgx read handler over an existing table, the same shape every
prior read-only operator endpoint already used.

**Architecture decisions that must not be reversed:** ADR-0001–0005
unchanged, not reopened. ADR-0002 (the alert-suppression state machine)
was read closely this session to confirm its already-decided shape, not
amended — `incidents.status`'s open/resolved derivation is a direct,
unavoidable consequence of ADR-0002's own schema (`opened_at`/`closed_at`
nullable, no status column), not a new interpretation.

**Implementation state:**
- Done: `GET /api/v1/targets/{target_id}/incidents`, real-database-tested;
  full backend suite green; `openapi.yaml` re-validated (unchanged, since
  the endpoint already matched the committed contract exactly).
- In progress: nothing mid-flight.
- Not started (carried forward): alert dispatch delivery (webhook/email
  send), B-004 through B-013 as listed in `11-backlog.md`, R-002/R-003/
  R-005 through R-008 as listed in `10-risk-register.md`.

**Constraints and non-goals:** unchanged — see `01-scope-and-non-goals.md`.

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning, Requirements Analysis, Verification & Testing, Retirement & Handover
**Baseline-depth, real rigor on its own merits:** this session's real
rigor was in verifying, before writing any code, that the task framing
handed to it ("no incident detection... no /incidents endpoint") did not
match this repo's actual state — reading ADR-0002 and
`internal/alerting/incident.go` directly rather than trusting the framing,
then building exactly the small remaining gap the backlog itself already
scoped correctly, with real database-backed tests and a full-suite
regression run rather than claiming success from `go build` alone.

**Task for this session (B-002) — now complete:** implement
`GET /targets/{id}/incidents`. **Done — see "What was actually built this
session" above.**

**Definition of done — met:**
- The endpoint exists, matches `openapi.yaml`'s already-committed
  `Incident` schema exactly, and is gated by the same `operatorSession`
  auth every other target-scoped read endpoint uses. ✅
- Real tests against a real Postgres cover: empty history, 404 on an
  unknown target, correct status derivation and ordering for a mixed
  open/resolved history, and per-target scoping. ✅
- Full `go test ./...` passes with no regression. ✅
- `openapi.yaml` re-validated clean. ✅
- Project memory (this handoff, `11-backlog.md`, `13-release-notes.md`)
  updated to reflect the real, current state — no new ADR, since no new
  design decision was made. ✅

**Files to attach or paste for the next session:**
- `backend/internal/alerting/dispatch.go` / `channel.go` (if picking up
  alert-dispatch delivery)
- `10-risk-register.md` (R-005) and `11-backlog.md` (B-006) (if picking up
  test-fixture hygiene instead)

**Ground rules:** Do not change the application stack (Go/Gin/SvelteKit/
Postgres/Redis/OTel) or reopen the application-layer technology freeze.
Do not reopen ADR-0001–0005 without new measured/real evidence. Do not
touch `privacy-forge`, `laravel-consent-guard`, `bookslot`, or `lexicon`.

