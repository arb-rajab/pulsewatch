# ADR-0007 — Mobile Push as a Dispatch Channel: Fan-Out, Dead Tokens, and Device Registration

- **Date:** 2026-09-07
- **Status:** accepted

## Context

ADR-0006 (Session 17) replaced the log-only dispatch stub with
`WebhookDispatcher`, a real `HTTP POST` with retry/backoff, and left
`Dispatcher`/`DispatchOutcome` deliberately shaped so a second real sender
could be added without touching `NotifyChannels`, `OpenIncident`/
`CloseIncident`, or the `alert_dispatches` write path. Its own "Next
recommended session" named email (B-014) as that second sender.

This session added a different one, for a reason outside this repo: a
companion React Native app (`pulsewatch-mobile`) is being built, and the
single mobile-specific capability that justifies its existence is receiving
a push notification the moment an incident opens. A mobile app polling
`GET /targets/{id}/incidents` on a timer is a smaller, worse version of the
SvelteKit dashboard. So the app and this channel are one piece of work, and
this channel came first: building the app against a backend that cannot
push to it would have meant stubbing exactly the part that matters.

Before writing any code this session read `dispatch.go`, `channel.go`,
`router.go`'s absence, `scheduler.go` and `main.go` directly, per
`12-session-handoff.md`'s standing instruction — and found ADR-0006's
extension point genuinely usable as designed, with one structural gap it
had not needed to solve: with only one real implementation,
`WebhookDispatcher.Dispatch` doubled as the "which channel type is
implemented" decision. Two real implementations cannot both do that.

## Decision

### 1. Push is a third `alert_channels.type`, whose destination is a credential

`alert_channels.type` gains `'push'` (migration `000011`). A push channel's
`destination_encrypted` holds the **provider credential** — a Firebase
service-account JSON, or an APNs token-auth object (`.p8` key, key id, team
id, topic, environment) — not a single delivery address.

**Considered and rejected: a separate `push_channels` table.** It would
have needed its own encryption, its own operator API, and its own
`alert_dispatches` foreign key, duplicating three things `alert_channels`
already does correctly, in exchange for a schema that names its columns
more literally. The existing column already holds "a webhook URL *including
any embedded bearer token*" — it was never an address column, it was always
the channel's secret.

The delivery addresses are device tokens, in a new `device_tokens` table.
One push channel fans out to every live token registered for **its
provider**: an APNs credential is never handed an FCM token (it would
reject it as `BadDeviceToken`, and this session's dead-token rule would
then permanently kill a perfectly good registration).

### 2. Retry policy: transient failures retry, dead tokens never do

This is the substantive difference from ADR-0006, and the reason push is a
sibling implementation rather than a parameterisation of the webhook one.

A webhook channel has one destination, and every failure is one of two
things: try again, or give up. A push channel has N destinations, and its
single most common failure — the operator uninstalled the app, or the OS
rotated the token — is a third thing. Retrying it is pure waste on every
future incident forever, and treating it as "give up" throws away the one
fact worth recording.

So `internal/pushprovider` classifies every provider response into four
kinds, and `PushDispatcher` acts on them differently:

| Kind | Providers' own signal | Action |
|---|---|---|
| `transient` | FCM `UNAVAILABLE`/`INTERNAL`/`QUOTA_EXCEEDED`; APNs `ServiceUnavailable`/`TooManyRequests`; any 5xx, 429, network error | Retry, up to 3 attempts, ADR-0006's exponential backoff unchanged |
| `dead_token` | FCM `UNREGISTERED`/`INVALID_ARGUMENT`/`SENDER_ID_MISMATCH`; APNs `Unregistered`/`BadDeviceToken`/`DeviceTokenNotForTopic`, any 410 | **One attempt. Mark `device_tokens.dead_at`/`dead_reason` and never send to it again** |
| `credential` | FCM `THIRD_PARTY_AUTH_ERROR`, a rejected service-account assertion; APNs `ExpiredProviderToken`/`InvalidProviderToken`, 401/403 | **One attempt, then abort the whole fan-out.** Every remaining device would fail identically; repeating one configuration failure once per registered phone is noise, and for APNs walks into `TooManyProviderTokenUpdates` on top of it. Device tokens are never marked dead for this |
| `permanent` | Any other 4xx | One attempt. Token stays live |

Dead-marking is only safe because registration resurrects: re-registering a
token clears `dead_at`/`dead_reason`. A mobile app re-registers on every
launch, so a token wrongly marked dead recovers on the operator's next app
open rather than muting their phone forever.

### 3. `DispatchOutcome` is unchanged; its `LastError` gains a partial-success meaning

`alert_dispatches` keeps its one-row-per-attempted-notification shape and
needs no migration of its own. A fan-out aggregates into that one row:

- `delivery_confirmed` is true when **at least one live device accepted**.
  An operator with three phones, one of which was wiped last week, genuinely
  was notified.
- `attempts` is the total provider attempts across every token, so a
  retried fan-out reports the real work done.
- `last_error` is empty only when **every** live token was delivered to. A
  partial success reports its shortfall ("delivered to 1 of 2 device
  tokens; 1 marked dead (fcm rejected device token: UNREGISTERED)") rather
  than hiding it behind `delivery_confirmed=true`.

Per-device detail lives in `device_tokens.dead_at`/`dead_reason`/
`last_delivered_at`, which is a better home for a per-device fact than one
shared text column, and is what an operator asking "why didn't my phone
buzz" actually needs.

**Zero live tokens is reported unconfirmed with zero attempts.** Nothing was
notified; saying otherwise would be exactly the silent-success failure mode
ADR-0006 went out of its way to avoid for email.

### 4. `ChannelRouter` replaces "the dispatcher answers for types it isn't"

With one real implementation, `WebhookDispatcher.Dispatch` answered "not
implemented" for every non-webhook type. With two, that shape requires each
implementation to know about the other. The type-to-implementation decision
is lifted into `alerting.ChannelRouter`, and `alerting.NewDefaultDispatcher`
is the single construction both production entry points (`scheduler.New`,
`main.go`'s agent-facing OTLP path) now call — Session 17 had to update
those two in lockstep to add one dispatcher, and this session would have
made that three. Each real dispatcher keeps its own defensive type guard: a
mis-wired router should produce an honest unconfirmed outcome, never a
webhook POST to a push credential. `"email"` is still reported as not
implemented, unchanged in behaviour (B-014).

### 5. Device registration is a per-operator resource, and the first one

`POST /api/v1/device-tokens` (upsert), `GET /api/v1/device-tokens`,
`DELETE /api/v1/device-tokens/{id}` (revoke, not delete — `alert_dispatches`
history depends on the row surviving), all behind the same
`operatorSession` cookie the dashboard uses. **No second identity type was
added**: 05-api-contracts.md's "there are only ever two disjoint identity
types" holds; the mobile app is a second *client* of the operator identity,
not a third principal.

`device_tokens.operator_id` is the first genuinely per-account resource in
this schema, so `RequireOperator` now stashes the verified identity in the
request context (`OperatorIDFrom`) instead of discarding it as it has since
Session 6. Discarding it and then re-deriving it — by assuming the single
operator row — would have been exactly the unexamined shortcut that
middleware's own comment argued against.

### 6. What is and isn't a secret here

`alert_channels.destination_encrypted` (the provider credential) is
AES-256-GCM encrypted at rest, unchanged from FR-023. `device_tokens.token`
is **not**, deliberately:

- It is not a credential. Holding one grants nothing without the provider
  credential that *is* encrypted.
- It is rotated by the device itself, continuously, and is meaningless once
  the app is uninstalled.
- Registration must match it by exact value on every re-registration — an
  upsert on a deterministic column. Randomised-nonce AES-GCM ciphertext
  structurally cannot support that; the alternative would be a deterministic
  encryption scheme, which is materially weaker than no encryption is
  honest.

It is still never returned by any API (`DeviceTokenRecord` has no field for
it) and never reaches a log line or `last_error` — every message
`internal/pushprovider` can produce is built from a fixed vocabulary plus a
provider error code or HTTP status, never from a raw `net/http` error or a
provider's own response text. An unrecognised APNs `reason` is stripped to
alphanumerics and bounded to 64 characters before it is quoted anywhere.

### 7. Both providers, spoken directly, with no new dependency

`internal/pushprovider` implements FCM's **HTTP v1** API
(`POST /v1/projects/{id}/messages:send` with an OAuth 2 access token minted
from a service-account RS256 JWT assertion) and APNs' **HTTP/2** provider
API (`POST /3/device/{token}` with an ES256 provider token), over `net/http`
with no vendor SDK and no new module dependency — the same property
ADR-0006's `WebhookDispatcher` has.

**Considered and rejected: `firebase.google.com/go` and an APNs library.**
Together they would have added dozens of transitive modules and their CVE
surface to a repo whose entire backend is four direct dependencies, to save
roughly sixty lines of stdlib JWT signing. FCM's legacy `/fcm/send`
server-key API was not implemented at all: Google turned it down in 2024, so
building against it would be building against something that cannot work.

## What is and isn't verified

**Verified, for real, against a real Postgres and real servers:**

- Both providers' wire protocols, against mock servers that speak the real
  FCM and APNs protocols — real OAuth 2 JWT-bearer exchange, real request
  paths and headers, real published error bodies. The signed assertions and
  provider tokens are **verified cryptographically against the signing
  keys' public keys** inside the tests, so a wrong algorithm or the classic
  ES256 ASN.1-instead-of-`R||S` mistake fails here rather than at Apple.
  The APNs test server runs HTTP/2 (`httptest`'s `EnableHTTP2`) and a test
  asserts `ProtoMajor == 2` — APNs speaks nothing else.
- The dispatch behaviour, end to end through `NotifyChannels` against a real
  Postgres: multi-device delivery, retry-then-succeed, exhausted-retry,
  dead-token marking (including that the *next* incident costs exactly zero
  attempts for it), credential-rejection fan-out abort, zero-live-tokens,
  unusable credential, and channel routing across all three types at once.
- The registration API through the real gated HTTP surface, including
  cross-operator isolation and that no response can carry a token value.

**Not verified, and named rather than implied:** no notification was
delivered to a real device by real FCM or real APNs infrastructure. Doing so
requires a Firebase project's service-account key and an Apple Developer
`.p8` key with a registered bundle id — real credentials that cannot be
committed to a public repo or held by CI. This is the same class of named
limitation as B-013's `registry.terraform.io` block, and is tracked the same
way (B-016) rather than papered over: the protocol is implemented against
the providers' real, published APIs and exercised against servers that
speak them, not faked into success.

## Consequences

- Migration `000011` adds `device_tokens` and widens
  `alert_channels`' type CHECK. Its `down` deletes any `'push'` rows (and
  the `alert_dispatches` rows referencing them) before restoring the
  narrower constraint — a real data loss, stated in the migration itself
  rather than hidden.
- `device_tokens.operator_id` is `ON DELETE CASCADE`, so this session's own
  test fixtures clean up completely — unlike the pre-existing fixtures
  B-006 tracks. B-006 itself is untouched and still open.
- `alert_dispatches` needs no migration: a fan-out still produces exactly
  one row, because every retry and every device happens inside the single
  `Dispatch` call `NotifyChannels` already makes, before that row is
  written.
- `scheduler`'s `dispatchTimeout` (20s, ADR-0006) is unchanged and is now
  shared with a fan-out. An operator with many registered devices on a
  badly degraded provider could exhaust it mid-fan-out; the tokens not
  reached are simply not reached, and `alert_dispatches` records the real
  count. Raising it is a tuning decision for a session with a real
  measurement, not a speculative change here.
- Push notifications carry ids, not target URLs or hostnames — the same
  choice ADR-0006 made for the webhook payload, and it matters more here,
  because a push notification renders on a lock screen in front of whoever
  is holding the phone. The mobile app resolves those ids against
  `GET /targets/{id}/incidents` once unlocked and authenticated.
