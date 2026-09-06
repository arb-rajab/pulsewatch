# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed

- Session number and title: **Session 17 — Alert Dispatch Delivery (B-015, ADR-0006).**
- Objective: get alert dispatch delivery to a real, working, tested state —
  named by Session 16's own handoff as the recommended next session, with
  an explicit instruction to read `internal/alerting/dispatch.go`/
  `channel.go` directly before assuming what was built vs. still a stub.
- Status: **done, narrower in scope than the task framing handed to this
  session assumed.** Before writing any code, this session read
  `dispatch.go`, `channel.go`, `incident.go`, `scheduler.go`, and
  `agentapi/logs.go` directly and found that **the wiring from
  incident-state transitions to dispatch was already fully built and
  already tested** — `scheduler.handleJob` and
  `agentapi.processCheckResult` both already called
  `alerting.NotifyChannels` with the `*DispatchRequest` `OpenIncident`/
  `CloseIncident` produced, exactly as ADR-0002 specifies. The one real
  gap was `LogDispatcher`: a stub `Dispatcher` implementation that logged
  "a notification would be sent" and never touched `Channel.destination`
  at all — so no channel, webhook or email, actually delivered anything
  anywhere, regardless of how correctly the state machine and wiring
  around it worked. This session's task framing described "wire dispatch
  into the incident state machine" as if that wiring needed to be built;
  it did not — that framing was wrong for this repo's actual state, the
  same kind of mismatch Session 16 found and reported honestly for B-002.
  This session did the same: read first, then scoped the real work to
  what was actually missing.

### What was actually built this session
- `backend/internal/alerting/dispatch.go`: `LogDispatcher` deleted,
  replaced by `WebhookDispatcher` — a real `Dispatcher` implementation that
  performs an actual `HTTP POST` of a small JSON payload (`kind`,
  `incident_id`, `target_id`, `sent_at`) to a `"webhook"` channel's
  decrypted destination, retried up to 3 times (default) with exponential
  backoff (200ms base, doubling, capped at 2s) on transient failures
  (network/transport errors, HTTP 5xx, HTTP 429). A malformed destination
  or any other 4xx is treated as permanent and not retried. Every field on
  `WebhookDispatcher` has a safe zero-value default so tests can override
  just what they need (a faster backoff for test speed) without
  reconstructing the whole thing. See ADR-0006 for the full retry-policy
  and idempotency reasoning.
- `Dispatcher.Dispatch`'s return type changed from a bare `error` to
  `DispatchOutcome{Confirmed, Attempts, LastError}` — the additive shape
  needed to report real attempt counts and a *sanitized* failure reason
  back to `NotifyChannels` without ever risking a channel's destination
  leaking into a log line or `alert_dispatches.last_error` (FR-023).
  `WebhookDispatcher`'s own `webhookAttemptError` type is constructed from
  a fixed vocabulary (`malformed`/`transport`/`status`+code), never from a
  raw `net/http`/`*url.Error`'s own `Error()` string, which for a bad URL
  can embed the URL itself.
- `backend/migrations/000010_add_alert_dispatches_delivery_status.up.sql`
  (+`.down.sql`): adds `alert_dispatches.attempts` (smallint, default 1)
  and `alert_dispatches.last_error` (nullable text) — additive, every
  pre-existing row's `delivery_confirmed` meaning is unchanged.
- `backend/internal/scheduler/scheduler.go`: default dispatcher is now
  `alerting.NewWebhookDispatcher(nil)`; the per-dispatch context budget
  grew from the shared 5s `dbOpTimeout` (sized for one Postgres
  round-trip) to a dedicated 20s `dispatchTimeout`, since a genuinely
  retried webhook delivery can legitimately take several real seconds.
- `backend/main.go`: the agent-facing OTLP ingestion path's dispatcher
  construction updated the same way (`alerting.NewWebhookDispatcher(nil)`).
- `backend/internal/alerting/dispatch_test.go`: rewritten around real
  `httptest.Server` instances (not a stub) — `TestNotifyChannels_
  WebhookDeliversRecordsAttemptAndNeverLogsDestination` (real 200,
  attempts=1, payload shape verified, destination never logged),
  `TestWebhookDispatcher_RetriesTransientFailureThenSucceeds` (a real
  server failing twice then succeeding — proves the retry genuinely
  happens end-to-end through `NotifyChannels`, attempts=3), `TestWebhookDispatcher_
  PermanentFailureRecordsUnconfirmedWithError` (every attempt 500 —
  exhausts `MaxAttempts`, records `delivery_confirmed=false` and a
  destination-free `last_error`), `TestWebhookDispatcher_
  NonRetryableStatusStopsImmediately` (a 400 — exactly 1 HTTP attempt, no
  retry), `TestNotifyChannels_EmailChannelReportedNotImplemented` (an
  `"email"` channel comes back unconfirmed with an explicit "not
  implemented" `last_error`, never silently dropped or falsely confirmed).
- `backend/internal/scheduler/alert_lifecycle_test.go` and
  `backend/internal/agentapi/testdb_test.go`: `spyDispatcher.Dispatch`
  updated to the new `DispatchOutcome` return shape (one-line change each,
  behavior unchanged — these tests use a spy, not the real dispatcher, so
  they still make no real network calls).
- `backend/internal/operatorapi/agents_test.go`: its one
  `alerting.NewLogDispatcher` call site updated to
  `alerting.NewWebhookDispatcher(nil)`.
- Docs: `docs/adr/ADR-0006-webhook-dispatch-delivery-semantics.md` (new),
  `09-decision-log.md` (new ADR-0006 entry), `04-data-model.md`
  (`ALERT_DISPATCHES` ERD gains `attempts`/`last_error`), `11-backlog.md`
  (B-015 closed; B-014 opened for the deliberately-deferred email sender;
  B-006's entry updated with this session's severity finding — see below),
  `13-release-notes.md`.

### Verification performed (real, not just `go build`)
- Started this sandbox's own local Postgres 16 (not running at session
  start, same as every prior session's own sandbox finding), created the
  `pulsewatch`/`pulsewatch` role+database fresh, and applied all ten
  committed `backend/migrations/*.up.sql` files directly via `psql` in
  order, including this session's new `000010`.
- `go build ./...`, `go vet ./...`, `golangci-lint run ./...`: all clean
  (two real lint findings — an unchecked `resp.Body.Close()` and a
  `max`-shadowing parameter name — found and fixed during this session,
  not pre-existing).
- `go test ./... -race -p 1` (the exact invocation `.github/workflows/
  ci.yml`'s backend job runs): all packages pass, against the real local
  Postgres, including every new test above and the full pre-existing
  suite (no regression from the `Dispatcher` interface's shape change).
- **A real, previously-abstract consequence of R-005/B-006 (test-fixture
  cleanup) reproduced and was diagnosed, not just noticed:** this
  session's first attempt at running the new dispatch tests measured
  2.5–3s per test — traced (via a throwaway timing instrumentation test,
  not guessed) to `NotifyChannels` looping over *every* row `LoadChannels`
  returns from the global `alert_channels` table, including leftover rows
  from earlier test runs this session that `t.Cleanup` could never
  actually delete (blocked by `alert_dispatches`' plain `REFERENCES`, no
  `CASCADE` — exactly B-006's own already-documented mechanism). Under the
  old `LogDispatcher` stub this cost nothing measurable; under a real
  `WebhookDispatcher`, each such row triggers a real (retried) HTTP attempt
  against an unreachable fake destination, costing real seconds. Confirmed
  by direct measurement (a `WebhookDispatcher.Dispatch` call against a real
  local `httptest.Server`, in isolation, took ~1ms; the same call through
  `NotifyChannels` against the polluted shared table took ~2.7s). Fixed for
  this session's own verification runs by truncating the affected tables
  in this sandbox's local Postgres (the same "confirm timestamps, then
  delete" practice Session 16 used for its own orphaned-row finding) —
  **not** a code fix, and B-006 itself remains open and out of scope. See
  `11-backlog.md`'s updated B-006 entry and ADR-0006's Consequences section
  for the full writeup.

### What was deliberately not done
- **Email delivery (FR-014) is explicitly out of scope this session,
  time-boxed out on purpose** — see B-014. An `"email"` channel today
  produces an honest, real `alert_dispatches` row
  (`delivery_confirmed=false`, `last_error` saying plainly it isn't
  implemented), never a silent no-op and never a false success. A real
  SMTP/provider-API sender is a straightforward follow-up: the
  `Dispatcher` interface needs no change, since `DispatchOutcome` was
  already designed as the shape any real sender reports through.
- Did not touch `OpenIncident`/`CloseIncident`, the alert-suppression state
  machine (`state.go`), or `04-data-model.md`'s `incidents` schema — none
  of that needed any change; ADR-0002's guarantees are exactly what this
  session built real delivery on top of, not something this session had
  to re-derive.
- Did not build a channel-registration API. None exists yet (unchanged
  from every prior session's own finding) — this session's tests insert
  `alert_channels` rows directly via SQL, the same shortcut every prior
  session in this area used.
- Did not fix B-006/R-005 (test-fixture cleanup ordering) despite finding
  a real, measured escalation of its cost — out of this session's own
  bounded scope per its own instructions. Recorded honestly rather than
  quietly worked around with a larger, unscoped fix.
- Did not start B-004, B-005, B-007, B-008, B-010 through B-013 — untouched,
  carried forward exactly as Session 16 left them.

## Real gaps found and named during this session
- **The task framing handed to this session ("wire dispatch into the
  incident state machine so opening an incident actually triggers a
  dispatch attempt") did not match this repo's real state** — that wiring
  was already built and tested (Session 4/5-era `scheduler`/`agentapi`
  code, unmodified by this session). This session verified that by reading
  the code first, exactly as `12-session-handoff.md` (Session 16's own
  version) explicitly instructed the next session to do, and scoped its
  real work — `Dispatcher` was the only actually-stubbed layer — instead
  of building a second, redundant wiring path to match the (wrong) framing.
- **B-006/R-005's cost profile changed, not just its likelihood** — see
  "Verification performed" above and ADR-0006's Consequences section. This
  is a genuine new finding (a stub-vs.-real dispatcher cost asymmetry that
  didn't exist before real delivery shipped), not a re-statement of the
  already-known orphaned-rows fact itself.

## Open questions and risks
- **R-002, R-003, R-006, R-007, R-008 (carried forward, untouched this
  session):** unchanged — see `10-risk-register.md`.
- **R-005 (test-fixture cleanup / B-006):** unchanged in root cause, but
  this session added a concrete, measured cost dimension to it — see
  above. Still open, still B-006's own scope to fix, not reopened here.
- **B-004, B-005, B-007, B-008, B-010 through B-013 (carried forward,
  untouched):** see `11-backlog.md`. B-015 (webhook dispatch) is now
  closed; B-014 (email dispatch) is newly opened as its explicitly-scoped
  remainder.

## Next recommended session
- Proposed session title: **email alert delivery (B-014)** — a real
  SMTP or provider-API (SES/SendGrid/etc.) `Dispatcher` sibling to
  `WebhookDispatcher`, following the identical `DispatchOutcome`-returning
  shape ADR-0006 established. Read `internal/alerting/dispatch.go` first
  (not assumed) — `WebhookDispatcher`'s retry/backoff/timeout policy and
  its `webhookAttemptError` sanitization pattern (never let a raw
  transport error's `Error()` string, which can embed a secret, reach a
  log line or `alert_dispatches.last_error`) are the template to match,
  not necessarily to reuse verbatim (SMTP/provider-API failure modes
  differ from HTTP's).
  - Alternatively, B-006 (test-fixture cleanup ordering) is now better
    evidenced than ever (three independent reproductions — Session 12/13/
    14's original finding, and this session's own cost-escalation
    measurement) and still a small, bounded, self-contained fix if a
    session wants real payoff without a design decision attached.
  - Do **not** reopen ADR-0001–0006 without new measured/real evidence.
  - Do **not** attempt B-013 (Terraform/cert-manager/NetworkPolicy)
    without first testing this sandbox's own egress policy directly.
- Inputs required: this handoff; `internal/alerting/dispatch.go` (read
  directly, not assumed) if picking up B-014; `10-risk-register.md`'s
  R-005/`11-backlog.md`'s B-006 if picking up test hygiene instead.
- Definition of done: whichever item is picked up, verified against a real
  local Postgres with real `go test` output (and, for B-014, a real
  receiving endpoint/mailbox if one is reachable from that session's own
  sandbox — not just `go build`), per this project's own standard.

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Repository state:** branch `main` (this session developed on
`claude/alert-dispatch-delivery-1tjcpa` per its own worker-branch
instructions), unreleased (pre-v0.1.0). B-015 is now closed; everything
Session 16's own handoff listed as complete remains complete and
untouched.

**Problem being solved:** unchanged — see `00-project-brief.md`.

**Users:** unchanged — single operator plus the machine "Agent" role (`02-requirements.md`).

**Current stack:** unchanged. No new technology was added —
`WebhookDispatcher` is `net/http` against an interface (`Dispatcher`) this
repo already had; no new dependency was added to `go.mod`.

**Architecture decisions that must not be reversed:** ADR-0001–0005
unchanged, not reopened. ADR-0002 (the alert-suppression state machine) was
read closely this session to confirm its already-decided dispatch-gating
shape ("dispatch only after the conditional write returns a row"), not
amended — this session's new ADR-0006 is additive, covering only the
delivery mechanism ADR-0002 always said was a separate Implementation
concern.

**Implementation state:**
- Done: real webhook alert delivery (`WebhookDispatcher`, retry/backoff,
  `alert_dispatches.attempts`/`last_error`), wired through the
  already-existing incident-to-dispatch path, real-database-tested; full
  backend suite green under `-race -p 1` (CI's own invocation);
  `golangci-lint` clean.
- In progress: nothing mid-flight.
- Not started (carried forward): email alert delivery (B-014), B-004
  through B-013 as listed in `11-backlog.md`, R-002/R-003/R-005 through
  R-008 as listed in `10-risk-register.md`.

**Constraints and non-goals:** unchanged — see `01-scope-and-non-goals.md`.

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning, Requirements Analysis, Verification & Testing, Retirement & Handover
**Baseline-depth, real rigor on its own merits:** this session's real rigor
was, again, verifying before writing code that the task framing handed to
it didn't match this repo's real state (the incident-to-dispatch wiring
was already built), then scoping real work to the actual gap
(`LogDispatcher` never delivering anything) — plus diagnosing, by direct
measurement rather than guessing, why the new tests were unexpectedly slow
before writing a single assertion about retry behavior.

**Task for this session (alert dispatch delivery, B-015) — now complete:**
implement real webhook delivery with retry/backoff and delivery-status
tracking, wired into the (already-existing) incident state machine. **Done
— see "What was actually built this session" above.**

**Definition of done — met:**
- `WebhookDispatcher` performs a real `HTTP POST`, retried with
  exponential backoff on transient failures, to every configured
  `"webhook"` channel on every incident open/resolve edge transition. ✅
- `alert_dispatches` records exactly what was attempted
  (`delivery_confirmed`/`attempts`/`last_error`), never a channel's
  destination (FR-023 verified by test). ✅
- Real tests against a real Postgres cover: successful delivery,
  retry-then-succeed, permanent failure after exhausting retries,
  non-retryable-status short-circuit, and the explicit email-not-
  implemented path. ✅
- Full `go test ./... -race -p 1` passes with no regression;
  `golangci-lint run ./...` clean. ✅
- Project memory (this handoff, `09-decision-log.md`, `11-backlog.md`,
  `13-release-notes.md`, `04-data-model.md`, new ADR-0006) updated to
  reflect what's now real vs. still stubbed (email). ✅

**Files to attach or paste for the next session:**
- `backend/internal/alerting/dispatch.go` (if picking up B-014 — read
  `WebhookDispatcher` as the template for the sanitization/retry pattern,
  not necessarily to copy its exact policy)
- `10-risk-register.md` (R-005) and `11-backlog.md` (B-006) (if picking up
  test-fixture hygiene instead)

**Ground rules:** Do not change the application stack (Go/Gin/SvelteKit/
Postgres/Redis/OTel) or reopen the application-layer technology freeze.
Do not reopen ADR-0001–0006 without new measured/real evidence. Do not
touch `privacy-forge`, `laravel-consent-guard`, `bookslot`, or `lexicon`.
