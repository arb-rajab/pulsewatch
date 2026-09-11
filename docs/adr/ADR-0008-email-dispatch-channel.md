# ADR-0008 — Email as a Dispatch Channel: SMTP Delivery and Retry Semantics

- **Date:** 2026-09-11
- **Status:** accepted

## Context

ADR-0006 (Session 17) built `WebhookDispatcher` and explicitly time-boxed
`"email"` channels out, reporting them back as "not implemented this
session" rather than silently dropping or falsely confirming a
notification. ADR-0007 (Session 18) added `PushDispatcher` as a second real
sibling implementation and lifted the channel-type-to-implementation
decision into `alerting.ChannelRouter`, so a third real sender needs no
change to `Dispatcher`, `DispatchOutcome`, `NotifyChannels`, or
`alert_dispatches` — only registration in `NewDefaultDispatcher`'s routing
table. Both prior sessions named email (B-014) as the natural next channel.

Before writing any code this session read `dispatch.go`, `channel.go`,
`router.go`, `pushdispatch.go`, and `internal/pushprovider` directly, per
`12-session-handoff.md`'s standing instruction. Two things were found that
shaped this session's actual scope:

1. **The registration side of B-014 was already done.** `alert_channels.type`
   has allowed `'email'` since migration `000008` (Session 6, long before
   ADR-0006 existed), and `operatorapi.CreateAlertChannel` has never
   special-cased it — `POST /api/v1/alert-channels` with
   `{"type":"email","destination":"<address>"}` already worked, was already
   encrypted at rest with the same `EncryptDestination` call every other
   channel type uses, and was already exercised (if only incidentally) by
   `dispatch_test.go`'s own "not implemented" test fixture. This session's
   real scope is delivery only — no schema migration, no new endpoint.
2. **Email's destination shape is a webhook's, not push's.** A push
   channel's `destination_encrypted` holds a *provider credential* because
   FCM/APNs are fixed endpoints an operator brings a credential to; a push
   channel fans out to N device tokens. An email channel has exactly one
   destination — the recipient address — the same single-destination shape
   ADR-0006 already built for webhook URLs. So `EmailDispatcher` is
   `WebhookDispatcher`'s sibling, not `PushDispatcher`'s: no fan-out, no
   per-destination dead-marking table, same retry-loop shape, same
   `defaultMaxAttempts`/backoff constants reused verbatim.

## Decision

### 1. A real SMTP client, spoken directly, with no new dependency

`internal/emailprovider` implements RFC 5321 SMTP (plus STARTTLS/RFC 3207
and AUTH PLAIN/RFC 4616) directly over `net/smtp` and `net/textproto`
(stdlib only) — the same "no new dependency" property `WebhookDispatcher`
and `internal/pushprovider` both already have. `alerting.EmailDispatcher`
wraps it with ADR-0006's exact retry/backoff policy (3 attempts, 200ms→2s
exponential backoff, per-attempt timeout), reusing `dispatch.go`'s own
`backoffDelay`/`defaultMaxAttempts`/etc. rather than re-deriving a second
policy that happens to look the same.

**Considered and rejected: a transactional email provider's HTTP API**
(SendGrid/SES/Postmark/Mailgun). Rejected for the same reason ADR-0007
rejected `firebase.google.com/go` and an APNs SDK: it would add a module
dependency (and, for most such providers, an account/API-key this sandbox
cannot hold) to save maybe a hundred lines of protocol code, for a repo
whose entire backend is four direct dependencies. SMTP is also the more
honest choice for a *self-hosted* product: `08-deployment-and-operations.md`
already assumes no managed cloud account exists, and every such product
(a self-hosted PagerDuty/Uptime-Kuma alternative) needs to work against
"whatever SMTP relay the operator already has" — a Gmail app password, a
company relay, a local Postfix — not force a specific vendor's API key into
the deployment story.

### 2. Retry policy: SMTP reply codes classify the same way HTTP status does

`internal/emailprovider`'s `ErrorKind` (`transient`/`permanent`/`credential`)
reuses ADR-0006's own 4xx/5xx convention, RFC 5321's own convention for the
identical reason: a `4xx` reply means "try again," a `5xx` reply means
"don't." Which `Kind` a `5xx` becomes still depends on *where* in the
protocol it happened:

| Kind | SMTP shape | Action |
|---|---|---|
| `transient` | Any `4xx` reply (any phase); dial/network/timeout failure | Retry, up to 3 attempts, ADR-0006's unchanged backoff |
| `permanent` | `5xx` on `RCPT TO`/`MAIL FROM`/`DATA` — a hard bounce (`550` "mailbox unavailable" is the canonical case) or a rejected sender/message | **One attempt. Never retried.** |
| `credential` | `5xx` (or a non-protocol refusal, e.g. `net/smtp`'s own "won't send credentials over an unencrypted connection") on `AUTH` | **One attempt. Never retried.** Our own relay account, not the recipient's fault. |

`permanent` is this channel's SMTP-protocol sibling of ADR-0007's
dead-token case: "a hard bounce/invalid address... no blind retry," per
this session's own brief. It stops *short* of ADR-0007's full design,
deliberately: push's dead-token marking exists because one credential fans
out to *N* device tokens and a wrongly-live-but-actually-dead token would
otherwise be retried on *every future incident forever* — that is what
`device_tokens.dead_at` is for. An email channel has exactly one
destination per row, the same shape a permanently-misconfigured webhook URL
already has under `WebhookDispatcher`, and that channel has never grown a
persistent-suppression column either: a `550` is recorded honestly in that
one row's `alert_dispatches.last_error` (`"smtp rcpt rejected (code 550)"`),
visible to the operator on the very next incident, exactly as a `400` from
a broken webhook URL already is. Adding a second, email-only suppression
mechanism for the same shape of problem webhook has lived with unremarked
since Session 17 would be new, unrequested machinery, not a gap this
session found evidence for.

`credential` exists as its own `Kind` (rather than folding into
`permanent`) for the same reason ADR-0007 keeps `KindCredential` distinct
from `KindDeadToken`: the *fix* is different (an operator misreading a
`"recipient rejected"` error as their own typo in an address that is
actually fine, when the real problem is an expired relay password, is a
worse debugging experience than a message that says so directly), even
though the retry *behavior* — one attempt, never retried — is identical for
a single-destination channel with no fan-out to abort.

### 3. Both STARTTLS and implicit TLS, because neither is universal

`emailprovider.Config.ImplicitTLS` selects TLS-from-the-first-byte (port
465, e.g. some SES/Gmail configurations) instead of the default plaintext-
then-`STARTTLS`-if-advertised upgrade (port 587, e.g. most other relays).
Skipping TLS support entirely to save code would have shipped a client that
cannot speak to any relay that requires encryption — effectively every
provider a self-hosted operator would actually point this at — which is a
worse trade than the ~40 extra lines TLS support costs here.

### 3a. Message construction: reject-or-encode every field, never raw-concatenate

`buildMessage` (`internal/emailprovider/smtp.go`) treats every `Message`
field as caller-supplied and untrusted, regardless of what this session's
own one caller (`alerting.emailMessage`) actually puts in it — the package
boundary, not the current call site, is what a library's own safety has to
hold at. `to`/`msg.Subject` are rejected outright (a permanent
`SendError`, before any network I/O) if they contain a CR or LF — the same
reject-don't-mangle shape `net/smtp.Client`'s own `validateLine` already
uses internally for these two values. `Subject` is additionally rendered
via RFC 2047 `mime.QEncoding.Encode`, and `Body` via RFC 2045
`Content-Transfer-Encoding: base64` — both structural guarantees rather
than heuristic ones: their output alphabets cannot contain a raw CR, LF, or
a bare `.` line, so neither can inject a header, forge a `Bcc`, or
prematurely terminate the SMTP `DATA` block, regardless of content. `Body`
specifically cannot be CR/LF-rejected like `Subject`/`to` — a multi-line
body legitimately needs line breaks — so encoding its wire representation,
rather than restricting its content, is what keeps it both safe and
unrestricted. (`net/textproto`'s own `DotWriter`, still in effect
underneath this, already made the bare-`.`-line case safe at the protocol
layer; the base64 encoding is a second, application-visible guarantee of
the same property, not a replacement for it.)

### 4. What is and isn't a secret here (FR-023, unchanged discipline)

The recipient address is `alert_channels.destination_encrypted`, AES-256-GCM
encrypted at rest exactly like a webhook URL or push credential — no new
encryption code, `alerting.EncryptDestination`/`decryptDestination` are
unchanged and untouched by this session. The relay account
(`SMTP_HOST`/`SMTP_USERNAME`/`SMTP_PASSWORD`/`SMTP_FROM_ADDRESS`, all new
env vars — `.env.example`/`docker-compose.yml`) is deliberately **not** a
per-channel secret: unlike an FCM/APNs credential, one self-hosted install
has exactly one outgoing relay account, the same "one repo-wide piece of
configuration" shape `ALERT_CHANNEL_ENCRYPTION_KEY`/`SESSION_SIGNING_SECRET`
already have, not a second per-channel encrypted column. `internal/emailprovider`
follows `internal/pushprovider`'s exact sanitization discipline: every
`SendError.Reason` is built from a fixed vocabulary plus an SMTP reply code,
never from the server's own free-text message (which for some relays can
echo the rejected address back) and never from a raw `net`/`net/smtp` error
string (which for a dial failure can embed the relay's own address).
`emaildispatch_test.go`/`smtp_test.go` assert this the same way
`dispatch_test.go`/`pushdispatch_test.go` do: the recipient address must
never appear in a log line or in `alert_dispatches.last_error`.

### 5. `ChannelRouter`/`NewDefaultDispatcher` gains a real third entry

`NewDefaultDispatcher(pool, client, emailCfg)` now wires all three real
channel types (`"webhook"`, `"push"`, `"email"`) — a signature change to
the one shared constructor both production entry points
(`scheduler.New`, `main.go`'s agent-facing OTLP path) call, exactly the
"one construction, not two hand-assembled dispatcher literals" property
ADR-0007 established. `emailprovider.ConfigFromEnv()` is read independently
at each entry point, the same pattern `EncryptionKeyFromEnv()` already
uses: an unset `SMTP_HOST` degrades gracefully (a logged warning, `Config{}`
passed through) rather than refusing to start a process with no email
channel configured yet. `ChannelRouter`'s own "not implemented" fallback
is, as of this session, a defensive guard against a mis-wired router
(`TestChannelRouter_UnknownTypeIsReportedNotImplemented`) rather than
production's actual answer for any type a real `alert_channels` row can
hold — every type the table's own `CHECK` constraint allows now has a real
dispatcher.

## What is and isn't verified

**Verified, for real, against a real Postgres and a real SMTP server:**

- The full SMTP exchange — greeting, `EHLO`, `STARTTLS` (verified against
  a real, cryptographically-checked self-signed TLS certificate, both the
  opportunistic-upgrade and implicit-TLS paths), `AUTH PLAIN`, `MAIL FROM`,
  `RCPT TO`, `DATA` — against a real `net.Listener` speaking the real
  wire protocol via `net/textproto` (`internal/emailprovider/smtp_test.go`),
  not a mocked `Send`.
- Reply-code classification for every phase: a `550` hard bounce on
  `RCPT TO` (`permanent`), a `450` transient rejection (`transient`), a
  `553` `MAIL FROM` rejection (`permanent`), a `554` `DATA` rejection
  (`permanent`), a rejected `AUTH` (`credential`), and a dial failure
  against a closed port (`transient`).
- The dispatch behavior end to end through `NotifyChannels` against a real
  Postgres (`internal/alerting/emaildispatch_test.go`): immediate success,
  a hard bounce recorded unconfirmed after exactly one attempt (never
  retried), a transient failure retried then succeeding on the third
  attempt, exhausted retries recorded honestly, an unconfigured relay
  reported as an honest unconfirmed outcome rather than a panic, and all
  three real channel types routed correctly in one `NotifyChannels` call
  (`TestChannelRouter_RoutesEachTypeToItsRealDispatcher`,
  `internal/alerting/pushdispatch_test.go`).
- The registration surface through the real gated HTTP API
  (`TestCreateAlertChannel_AcceptsAnEmailChannel`,
  `internal/operatorapi/alertchannels_test.go`): an email channel is
  creatable, its recipient address is never echoed back by the create or
  read response, and it is encrypted at rest — the same proof
  `TestCreateAlertChannel_AcceptsAPushChannel` already gave push, now given
  to email even though (per Context above) nothing about this registration
  path is new code.

**Not verified, and named rather than implied:** no email was delivered by
a real SMTP relay to a real inbox. Doing so needs a real relay account
(Gmail app password, SES/SendGrid SMTP credentials, or similar) — a real
credential no public repo or CI job can hold, and a live delivery this
sandbox's network policy could not reach even if one existed. This is the
same class of named limitation as B-013's `registry.terraform.io` block and
B-016's push-to-a-real-device gap, named the same way rather than papered
over: the client is implemented against the real, published protocol
(RFC 5321/3207/4954) and exercised against a server that speaks it for
real, never faked into success. Tracked as a new backlog follow-up
alongside B-016, for a session whose sandbox has real relay credentials and
egress.

## Consequences

- No migration. `alert_channels.type` already allowed `'email'`
  (migration `000008`); no new table, no new column.
- Two new files register in `alerting.NewDefaultDispatcher`'s map:
  `internal/emailprovider` (the SMTP client) and
  `internal/alerting/emaildispatch.go` (`EmailDispatcher`). `router.go`'s
  and `scheduler.go`'s doc comments are updated to stop describing email as
  "not implemented" — a description this session makes false for any real
  `alert_channels` row.
- `NewDefaultDispatcher`'s signature changes (`pool, client` →
  `pool, client, emailCfg`), touching its two production call sites
  (`main.go`, `scheduler.New`) and the shared test helpers in
  `pushdispatch_test.go`/`emaildispatch_test.go` that build an equivalent
  router for tests.
- New environment variables (`SMTP_HOST`/`SMTP_PORT`/`SMTP_USERNAME`/
  `SMTP_PASSWORD`/`SMTP_FROM_ADDRESS`/`SMTP_IMPLICIT_TLS`), documented in
  `.env.example` and wired through `docker-compose.yml`'s `backend` service
  with the same all-empty-by-default, graceful-degradation defaults
  `ALERT_CHANNEL_ENCRYPTION_KEY` already has.
- This session's own tests re-confirmed (see `11-backlog.md`'s B-014
  addendum) that B-006's test-fixture-cleanup gap affects email dispatch
  tests in a way webhook/push tests don't: because an email channel's
  relay target is this dispatcher's shared `Config` rather than something
  named in the channel's own destination, a leftover channel row from an
  earlier test in the same package genuinely redials whatever fake SMTP
  server the *current* test is running. Worked around locally (unique
  recipient addresses per test, assertions scoped by recipient); B-006
  itself remains open and out of this session's scope, per this session's
  own brief.
