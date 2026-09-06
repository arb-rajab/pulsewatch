# ADR-0006 — Webhook Dispatch: Real Delivery, Retry Policy, and Idempotency

- **Date:** 2026-09-06
- **Status:** accepted

## Context

ADR-0002 fixed the alert-suppression state machine and deliberately left
"the delivery mechanism's own retry/outbox semantics" as an Implementation
concern (its own Consequences section). Session 4 built that Implementation
as `internal/alerting/dispatch.go`'s `LogDispatcher` — explicitly a stub
that logs "a notification would be sent" and nothing more, gated behind
`Dispatcher`, an interface a real provider could later implement without
changing `NotifyChannels`, `OpenIncident`/`CloseIncident`, or the
`alert_dispatches` write path at all.

This session picked up that named follow-up (`12-session-handoff.md`'s
"Next recommended session: alert dispatch delivery") and, before writing
any code, read `dispatch.go`/`channel.go` directly per that handoff's own
instruction — confirming `LogDispatcher` was real code, not a placeholder
comment, and that the wiring from `OpenIncident`/`CloseIncident` through
`scheduler.handleJob` and `agentapi.processCheckResult` into
`NotifyChannels` was **already complete and already tested**
(`alert_lifecycle_test.go`'s real end-to-end scheduler tests, `logs_test.go`
for the agent-reported path). The one genuine gap was `LogDispatcher`
itself: it never touched `Channel.destination` at all, so no channel type —
webhook or email — actually delivered anything anywhere. This ADR is about
replacing that one stub with a real sender for webhook channels
specifically, and the delivery-semantics decisions that required.

## Decision

**`WebhookDispatcher` replaces `LogDispatcher` as the default `Dispatcher`**
(`scheduler.New`, `main.go`), performing a real `HTTP POST` of a small JSON
body (`kind`, `incident_id`, `target_id`, `sent_at`) to a `"webhook"`
channel's decrypted destination. Three sub-decisions this required:

### 1. Retry policy: bounded, synchronous, in-process — not an outbox table

A failed delivery is retried up to `MaxAttempts` (default 3) times,
in-process, inside the single `Dispatch` call `NotifyChannels` already
makes — not written as "pending" and picked up later by a separate worker
or queue table.

**Considered and rejected: an async outbox** (a `status` column driving a
background retry sweep, mirroring `check_results`' own eventual-durability
shape). Rejected as over-scoped for what this repo actually needs: a
single-operator install sending its own webhook to its own Slack/PagerDuty
endpoint has no multi-tenant fan-out volume and no requirement that
dispatch survive a process restart mid-retry (an incident that's still
open will simply be re-notified, harmlessly duplicate at worst, by the next
edge transition — there is no "this specific retry must eventually land"
guarantee any FR/NFR asks for). A synchronous retry keeps `alert_dispatches`
at exactly the same one-row-per-attempted-notification shape
`04-data-model.md` already committed to (a retried delivery still produces
one row, since every retry happens before that row is ever inserted) —
adding an outbox would have meant a second migration widening that shape
for a durability guarantee nothing here asks for.

### 2. What's retryable

Retried: network/transport failures (DNS, connection refused, timeout),
HTTP 5xx, and HTTP 429. Not retried: a malformed destination (a
configuration error, not a transient one — retrying an unparseable URL
can't succeed), and any other 4xx (the receiver rejected this specific
request; byte-for-byte retrying it won't change that). Backoff is
exponential starting at 200ms, doubling, capped at 2s; each individual HTTP
attempt is bounded to 5s. Three total attempts (one original plus two
retries) is a deliberate, conservative default for a single operator's own
webhook, not a tuned value derived from a load target — a future session
with a real operator-facing channel-configuration API (today there is
none) could make this configurable per channel.

### 3. Idempotency: unchanged from ADR-0002, not re-derived here

Exactly-once dispatch per incident-state edge transition is still
`OpenIncident`/`CloseIncident`'s own conditional-write guarantee
(ADR-0002) — this ADR adds nothing new to that. What this ADR adds is
narrower: a single dispatch attempt for one channel is retried up to
`MaxAttempts` times, and because those retries happen before the
`alert_dispatches` row is written at all, a retried-then-succeeded delivery
still produces exactly one row (`delivery_confirmed=true`, `attempts>1`),
never two. A receiver that only partially processes a webhook it actually
received before this session's own client-side retry fires again (e.g. a
5xx returned after the receiver's own side effect already happened) could
in principle see the same notification body twice — this ADR does not
give receivers an idempotency key to de-duplicate on, since no FR/NFR asks
for at-most-once delivery, only that this repo not multiply alerts on its
own read side (which the incidents-table guard already guarantees).

## Options considered for "what does `Dispatch` report back"

**A — keep `Dispatch(ctx, channel, req) error`, chosen (rejected).** The
existing shape has no way to report attempt count or a sanitized failure
reason back to `NotifyChannels` for `alert_dispatches` — either that
information is dropped entirely, or `WebhookDispatcher` would have to log
it itself, duplicating `NotifyChannels`' own logging and, worse, risking
exactly the FR-023 leak this package has otherwise been careful about (a
raw `net/http`/`*url.Error` string can embed the request URL — the
channel's secret destination — directly in its `Error()` text).

**B — `Dispatch(ctx, channel, req) DispatchOutcome{Confirmed, Attempts,
LastError}` (chosen).** A small, additive return type. `LogDispatcher`
would have mapped trivially (`{Confirmed: true, Attempts: 1}`); the
existing `spyDispatcher` test doubles (scheduler, agentapi) were updated
to the same shape as a one-line change each. `WebhookDispatcher` itself is
the only place that ever inspects a raw transport/HTTP error — its own
`webhookAttemptError` type is constructed from a fixed vocabulary
(`"malformed"`/`"transport"`/`"status"+code`), never from the underlying
`error`'s own `Error()` string, so the destination genuinely cannot reach
`LastError`, `alert_dispatches.last_error`, or any log line — proven by
this session's own `TestNotifyChannels_WebhookDeliversRecordsAttemptAndNeverLogsDestination`
and `TestWebhookDispatcher_PermanentFailureRecordsUnconfirmedWithError`,
both of which assert the test webhook server's own URL never appears in
logs or `last_error`.

## Consequences

- `alert_dispatches` gains two columns (migration `000010`): `attempts`
  (smallint, default 1) and `last_error` (nullable text) — additive,
  backward-compatible with every row Session 4 through this session's own
  prior tests already wrote (`delivery_confirmed`'s existing meaning is
  unchanged).
- `LogDispatcher` is deleted, not deprecated-in-place: it had exactly one
  caller each in `scheduler.New` and `main.go`, both now constructing
  `alerting.NewWebhookDispatcher(nil)` instead, and no other code
  referenced the stub type itself (only the `Dispatcher` interface it
  implemented, which is unchanged in shape apart from `Dispatch`'s new
  return type).
- **Email channels (FR-014) are explicitly out of this session's scope.**
  `WebhookDispatcher.Dispatch` reports any non-`"webhook"` channel type
  back as `{Confirmed: false, Attempts: 1, LastError: "<type> channel
  delivery is not implemented this session (ADR-0006)"}` — recorded
  faithfully into `alert_dispatches`, not silently dropped and not
  falsely confirmed. An operator who configures an `email` channel today
  gets an honest, visible failure record for every incident, not silence.
  A real SMTP/provider-API sender is a follow-up session's own
  `Dispatcher`-shaped addition; nothing in this ADR's interface needs to
  change for that.
- `scheduler`'s per-dispatch context budget grew from `dbOpTimeout` (5s,
  sized for a single Postgres round-trip) to a dedicated `dispatchTimeout`
  (20s) — a real retried webhook delivery can legitimately take several
  seconds by design, and reusing the tighter DB-operation budget would
  have silently truncated genuine retries before they got a chance to
  succeed.
- This session's own real-Postgres tests reproduced a real, previously
  known-but-abstract consequence of the existing R-005/B-006 test-fixture
  cleanup gap: because `alert_dispatches` has a plain `REFERENCES
  alert_channels(id)` with no `ON DELETE CASCADE`, and `NotifyChannels` now
  performs a real (occasionally slow, retried) HTTP attempt against every
  row `LoadChannels` returns — not just the one row a given test just
  inserted — a test suite's own leftover `alert_channels` rows (never
  cleaned up, exactly as B-006 already describes) now cost real
  wall-clock seconds per stale row, not the near-zero cost they had under
  `LogDispatcher`. This is the same underlying gap B-006 already names,
  made more visible (and more expensive) by this session's own change —
  not a new defect this session introduced, and not fixed here (out of
  this session's scope; see `12-session-handoff.md`). `11-backlog.md`'s
  B-006 entry is updated to note this escalation.
