# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed

- Session number and title: **Session 18 — Mobile Push Dispatch Channel
  (B-018, ADR-0007).**
- Objective: add a real mobile-push alert-dispatch channel and the
  device-token registration surface a mobile client needs — the backend
  half of a paired task whose other half is a new companion repository,
  `pulsewatch-mobile` (React Native). The pairing is the point: the one
  mobile-specific capability that justifies that app existing is receiving
  a push the moment an incident opens. An app that polls
  `GET /targets/{id}/incidents` on a timer is a smaller, worse copy of the
  SvelteKit dashboard. So this channel was built first, deliberately —
  building the app against a backend that cannot push to it would have
  meant stubbing the only part that matters.
- Status: **done.** Read `dispatch.go`, `channel.go`, `scheduler.go`,
  `main.go` and `middleware.go` directly before writing any code, per the
  standing instruction in this file's Session 17 version — and found
  ADR-0006's `Dispatcher`/`DispatchOutcome` extension point genuinely
  usable as designed, with one structural gap it had never needed to solve
  (see "Real gaps found" below).

### What was actually built this session

- **`backend/internal/pushprovider/` (new package)** — real clients for both
  providers, over `net/http`, **with no new module dependency** (the same
  property ADR-0006's `WebhookDispatcher` has):
  - `fcm.go`: FCM **HTTP v1** (`POST /v1/projects/{id}/messages:send`),
    with the real OAuth 2 JWT-bearer exchange that mints its access token
    from a service-account key, cached across sends. The legacy
    `/fcm/send` server-key API is deliberately not implemented — Google
    turned it down in 2024, so building against it would be building
    against something that cannot work.
  - `apns.go`: APNs' **HTTP/2** provider API (`POST /3/device/{token}`),
    with a cached ES256 provider token, the real `apns-topic`/
    `apns-push-type`/`apns-priority`/`apns-expiration`/`apns-collapse-id`
    headers, and the payload shape Apple defines (custom deep-link keys as
    *siblings* of `aps`, not nested inside it).
  - `jwt.go`: exactly the two JWT flavors those two APIs require (ES256
    over `crypto/ecdsa`, RS256 over `crypto/rsa`), ~30 lines each. A JWT
    library would have added a dependency and its CVE surface to a backend
    with four direct dependencies, to save that.
  - `provider.go`: the four-way `ErrorKind` classification
    (`transient`/`dead_token`/`credential`/`permanent`) that the whole
    retry policy turns on, and the FR-023 sanitization discipline — every
    error message is a fixed vocabulary plus a provider error code or HTTP
    status, never a raw `net/http` error or a provider's own response text.
- **`backend/internal/alerting/pushdispatch.go`** — `PushDispatcher`: fans
  one incident edge transition out over every live device token for the
  channel's provider. Transient failures retry on ADR-0006's unchanged
  backoff; a dead token gets exactly one attempt and is recorded dead; a
  rejected credential aborts the whole fan-out (every remaining device
  would fail identically) and never marks a device dead.
- **`backend/internal/alerting/router.go`** — `ChannelRouter` and
  `NewDefaultDispatcher`, the single construction both production entry
  points now call.
- **`backend/internal/alerting/devicetokens.go`** — the `device_tokens`
  read/write surface: registration upsert (which clears a previous
  dead-marking), revoke, list, plus the dispatch-side fan-out load and
  dead/delivered marking.
- **`backend/internal/operatorapi/devicetokens.go`** + `router.go` —
  `POST` / `GET` / `DELETE /api/v1/device-tokens`, behind the same
  `operatorSession` cookie as every other operator route.
- **`backend/internal/operatorapi/middleware.go`** — `RequireOperator` now
  stashes the verified operator id (`OperatorIDFrom`) instead of
  discarding it as it has since Session 6. `device_tokens.operator_id` is
  this schema's first genuinely per-account resource; discarding the
  identity and then re-deriving it by assuming the single operator row
  would have been exactly the unexamined shortcut that middleware's own
  comment argued against.
- **`backend/migrations/000011_create_device_tokens.{up,down}.sql`** —
  `device_tokens`, plus widening `alert_channels`' type CHECK to include
  `'push'`. The `down` deletes any `'push'` rows (and the
  `alert_dispatches` rows referencing them) before restoring the narrower
  constraint — a real data loss, stated in the migration file itself
  rather than hidden.
- **`backend/internal/operatorapi/alertchannels.go`** — `'push'` accepted
  as a channel type. No other change was needed: a push credential is
  encrypted, stored and structurally-unreadable through exactly the same
  code path a webhook URL already was.
- **Dependency hygiene:** `golang.org/x/net` bumped `0.54.0 → 0.57.0`
  (Dependabot PR #6's finding, resolved inside this session's own PR
  rather than left open), pulling `x/crypto`, `x/sync`, `x/sys` and
  `x/text` forward with it.
- **Docs:** `docs/adr/ADR-0007-mobile-push-dispatch-channel.md` (new),
  `09-decision-log.md`, `04-data-model.md` (ERD gains `DEVICE_TOKENS`,
  plus a Session 18 addendum on why the token column is deliberately *not*
  encrypted), `05-api-contracts.md`, `docs/architecture/openapi.yaml`
  (three new operations, two new schemas, `AlertChannel.type` gains
  `push`), `11-backlog.md`, `13-release-notes.md`, `CHANGELOG.md`.

### The one substantive design departure from ADR-0006, and why

A webhook channel has one destination and every failure is one of two
things: try again, or give up. A push channel has N destinations, and its
single most common failure — the operator uninstalled the app, or the OS
rotated the token — is a third thing. Retrying it is pure waste on every
future incident forever; treating it as "give up" throws away the one fact
worth recording. So dead tokens are marked
(`device_tokens.dead_at`/`dead_reason`) and skipped by every later
incident, and that is proven by test, not asserted: the dead-token test
opens a *second* incident and asserts it costs exactly one provider call
rather than two.

Marking tokens dead is only safe because registration resurrects them — a
mobile app re-registers on every launch, so a token wrongly killed by one
provider response recovers on the operator's next app open instead of
muting their phone forever. That pairing is the load-bearing part; either
half alone is a bug.

### Verification performed (real, not just `go build`)

- Started this sandbox's own local Postgres 16 (not running at session
  start, same as every prior session's finding), created the
  `pulsewatch`/`pulsewatch` role and database fresh, and applied all
  eleven committed `backend/migrations/*.up.sql` files via `psql`,
  including this session's `000011` — then ran `000011`'s `down` and
  `up` again to prove reversibility directly, not by assertion.
- `go build ./...`, `go vet ./...`, `golangci-lint run ./...` (v2.13.2,
  CI's pinned version) — all clean. Two real revive findings were found
  and fixed during the session, not pre-existing.
- `go test ./... -race -p 1` (CI's exact invocation) against the real
  local Postgres: **all 12 packages pass**, including every new test and
  the full pre-existing suite, with no regression from `RequireOperator`'s
  new context write or the `ChannelRouter` change.
- **A measurement, not a guess, about test timing:** the `alerting`
  package took 29s on a locally-polluted database and **4.1s** on a freshly
  truncated one. That gap is entirely B-006/R-005's known orphaned-row
  mechanism (Session 17 measured the same thing for webhooks) and not
  anything this session's tests introduce — confirmed by truncating and
  re-measuring rather than assuming. CI gets a fresh Postgres per run and
  is unaffected. B-006 remains open and out of scope; notably this
  session's *own* fixtures do clean up completely, because
  `device_tokens.operator_id` is `ON DELETE CASCADE`.

### Tests added

`internal/pushprovider`: `TestFCMClient_SendSignsRealAssertionAndPostsRealV1Message`,
`TestFCMClient_AccessTokenIsCachedAcrossSends`,
`TestFCMClient_ClassifiesRealErrorBodies` (7 real FCM error codes),
`TestFCMClient_RejectedAssertionIsCredentialNotTransient`,
`TestNewFCMClient_RejectsIncompleteCredential`,
`TestClientFromCredentialJSON_BuildsTheRightClient`,
`TestAPNsClient_SendSignsRealProviderTokenAndPostsRealRequest`,
`TestAPNsClient_SendUsesHTTP2`,
`TestAPNsClient_ProviderTokenIsReusedAcrossSends`,
`TestAPNsClient_ClassifiesRealRejectionReasons` (9 real Apple reasons),
`TestAPNsClient_UnrecognisedReasonIsBounded`,
`TestNewAPNsClient_RejectsBadSigningKeyUpFront`,
`TestAPNsClient_SandboxEnvironmentSelectsSandboxHost`.

The signed assertions and provider tokens are **cryptographically verified
against the signing keys' public keys inside the tests**, so a wrong
algorithm — or the classic ES256 ASN.1-instead-of-`R||S` mistake, which
produces a structurally valid JWT that Apple silently rejects — fails here
rather than in production. The APNs test server runs real HTTP/2
(`httptest`'s `EnableHTTP2`) and one test asserts `ProtoMajor == 2`,
because APNs speaks nothing else.

`internal/alerting` (real Postgres):
`TestNotifyChannels_PushDeliversToEveryLiveTokenAndRecordsDispatch`,
`TestPushDispatcher_DeadTokenIsMarkedNotRetriedAndSkippedNextTime`,
`TestPushDispatcher_RetriesTransientFailureThenSucceeds`,
`TestPushDispatcher_ExhaustedTransientFailureIsUnconfirmedAndTokenStaysLive`,
`TestPushDispatcher_CredentialRejectionAbortsFanOutWithoutKillingTokens`,
`TestPushDispatcher_NoLiveTokensIsHonestlyUnconfirmed`,
`TestPushDispatcher_UnusableCredentialIsRecordedNotRetried`,
`TestChannelRouter_RoutesByTypeAndKeepsEmailHonest`,
`TestRegisterDeviceToken_UpsertResurrectsADeadToken`,
`TestRevokeDeviceToken_RemovesFromFanOutAndIsScopedToItsOperator`,
`TestLoadLiveDeviceTokens_IsScopedToItsProvider`,
`TestRegisterDeviceToken_RejectsInvalidRegistrations`,
`TestListDeviceTokens_ShowsDeadReasonAndNeverTheTokenItself`.

`internal/operatorapi` (real gated HTTP):
`TestDeviceTokenRoutes_RequireAnOperatorSession`,
`TestRegisterDeviceToken_RegistersUpsertsAndNeverEchoesTheToken`,
`TestRegisterDeviceToken_ValidatesItsInputs`,
`TestListAndUnregisterDeviceTokens` (including cross-operator isolation),
`TestCreateAlertChannel_AcceptsAPushChannel`.

### What was deliberately not done

- **No notification was delivered to a real device by real FCM or real
  APNs infrastructure.** That needs a Firebase service-account key and an
  Apple Developer `.p8` with a registered bundle id — real credentials no
  public repo or CI job can hold. Named as **B-016**, the same way B-013
  names the `registry.terraform.io` block, rather than implied or papered
  over. Both protocols are implemented against the providers' real
  published APIs and exercised against servers that speak them; nothing is
  faked into success.
- **Email delivery (FR-014, B-014) is still not implemented** — untouched.
  It is now `ChannelRouter`, not `WebhookDispatcher`, that reports it as
  not implemented; the recorded `alert_dispatches` outcome is unchanged
  (unconfirmed, honest `last_error`, never a silent drop).
- **No dashboard surface for `device_tokens`** — `GET /api/v1/device-tokens`
  returns exactly the data an operator would want, but only the mobile app
  and `curl` can see it today. Filed as B-017 rather than added
  speculatively.
- **B-006/R-005 not fixed**, despite this session measuring its cost again
  (see above). Out of scope, unchanged.
- B-004, B-005, B-007, B-008, B-010 through B-014 untouched, carried
  forward exactly as Session 17 left them.

## Real gaps found and named during this session

- **ADR-0006's dispatch design had one structural gap it had never needed
  to solve.** With exactly one real `Dispatcher`,
  `WebhookDispatcher.Dispatch` doubled as the "which channel type is
  implemented at all" decision — answering "not implemented" for anything
  that wasn't a webhook. That shape cannot hold two real implementations
  without each knowing about the other. Lifting the decision into
  `ChannelRouter` is additive, not a reversal: ADR-0006's retry policy,
  sanitization discipline and `DispatchOutcome` shape are all unchanged.
  `NewDefaultDispatcher` also collapses what were two hand-assembled
  dispatcher literals (`scheduler.New` and `main.go`) that Session 17 had
  to keep in lockstep, and that this session would have made three.
- **`DispatchOutcome.LastError`'s documented invariant ("empty when
  Confirmed is true") was written for a single-destination channel and is
  not expressive enough for a fan-out.** Amended rather than worked around:
  `Confirmed` now means "at least one device was reached", and `LastError`
  is empty only when *every* live device was. A partial delivery reports
  its shortfall instead of hiding behind a confirmed flag. Per-device
  detail lives in `device_tokens`, which is a better home for a per-device
  fact than one shared text column.

## Open questions and risks

- **B-016 (real-device push delivery):** the one thing about this channel
  that a sandbox structurally cannot prove. See above.
- **`scheduler`'s `dispatchTimeout` (20s, ADR-0006) is now shared with a
  fan-out.** An operator with many registered devices on a badly degraded
  provider could exhaust it mid-fan-out; the tokens not reached are simply
  not reached and `alert_dispatches` records the real count. Raising it is
  a tuning decision for a session with a real measurement, not a
  speculative change made here.
- **R-002, R-003, R-005 through R-008 (carried forward, untouched):**
  unchanged — see `10-risk-register.md`.

## Next recommended session

- Proposed session title: **email alert delivery (B-014)** — still the
  natural next channel, and now a strictly smaller job than it was: this
  session proved by doing it that a new sender is a `Dispatcher`-shaped
  sibling plus one line in `NewDefaultDispatcher`'s routing table, with no
  change to `NotifyChannels`, `DispatchOutcome`, or `alert_dispatches`.
  Read `internal/alerting/pushdispatch.go` as well as `dispatch.go` — SMTP
  failure modes (a hard bounce, a suppressed address) are much closer to
  push's dead-token shape than to a webhook's, and that pattern already
  exists here now rather than needing to be invented.
  - Alternatively **B-017** (a device list in the dashboard) is small,
    self-contained, and turns `dead_reason` from a column into something
    an operator can actually see.
  - **B-006** remains the best-evidenced small fix in the backlog (four
    independent reproductions now, including this session's).
  - Do **not** reopen ADR-0001–0007 without new measured/real evidence.
  - Do **not** attempt B-013 or B-016 without first checking that session's
    own sandbox actually has the egress/credentials each needs.
- Definition of done: whatever is picked up, verified against a real local
  Postgres with real `go test` output, per this project's own standard.

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Companion repository:** `pulsewatch-mobile` — the React Native companion
app this session's push channel exists for. It authenticates against this
API's `POST /api/v1/auth/login`, registers its device token via
`POST /api/v1/device-tokens`, reads `GET /api/v1/targets/{id}/incidents`,
and deep-links on a tapped notification using the `incident_id`/`target_id`/
`kind` data keys `PushDispatcher` sends. **That data-key contract is the
integration surface between the two repos** — changing those three key
names is a breaking change for the app, not an internal refactor.

**Status of that companion repository, stated plainly:** the app was built
and fully verified in this same session — real React Native 0.82.1 project
with real Android/iOS native configuration, 62 tests across 9 suites, both
platform Metro production bundles building, `npm audit` clean at zero — but
**it has not been published to GitHub.** This session's GitHub credential is
scoped to `arb-rajab/pulsewatch` and cannot create a repository; both
`create_repository` and the REST equivalent were refused. Creating
`arb-rajab/pulsewatch-mobile` (empty, public) is a one-time human action,
after which the app's single prepared commit can be pushed to it unchanged.
Nothing in *this* repository depends on that happening: the push channel,
its endpoints and its tests are complete and independent.
**Repository state:** branch `main` (this session developed on
`claude/push-notifications-pulsewatch-mobile-efslai` per its own
worker-branch instructions), unreleased (pre-v0.1.0). B-018 closed;
everything Session 17's handoff listed as complete remains complete.

**Problem being solved:** unchanged — see `00-project-brief.md`.

**Users:** unchanged — single operator plus the machine "Agent" role
(`02-requirements.md`). The mobile app is a second *client* of the operator
identity, not a third principal: no new identity type was added.

**Current stack:** unchanged. No new technology and **no new Go module
dependency** — `internal/pushprovider` speaks both providers' real HTTP
APIs over stdlib `net/http`, `crypto/ecdsa` and `crypto/rsa`.

**Architecture decisions that must not be reversed:** ADR-0001–0006
unchanged, not reopened. ADR-0007 is additive: it adds a channel type, a
table, a dispatcher and a router, and amends exactly one documented detail
of ADR-0006 (`LastError`'s meaning for a fan-out channel).

**Implementation state:**
- Done: real webhook delivery (ADR-0006) and real mobile push delivery
  (ADR-0007) with per-device fan-out, dead-token recording and device
  registration; full backend suite green under `-race -p 1`;
  `golangci-lint` clean; migrations reversible.
- In progress: nothing mid-flight.
- Not started (carried forward): email delivery (B-014), B-016 (real-device
  push verification), B-017, and B-004 through B-013 as listed in
  `11-backlog.md`.

**Constraints and non-goals:** unchanged — see `01-scope-and-non-goals.md`.

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning, Requirements Analysis, Verification & Testing, Retirement & Handover
**Baseline-depth, real rigor on its own merits:** this session's real rigor
was refusing to reuse ADR-0006's retry policy just because it was there —
identifying that push has a third failure shape a webhook does not, then
building and *proving* the dead-token path (including that a later incident
costs zero attempts for a dead token, and that re-registration resurrects
it) rather than describing it; and verifying both providers' signed tokens
cryptographically inside the tests rather than checking they were
merely well-formed.

**Ground rules:** Do not change the application stack (Go/Gin/SvelteKit/
Postgres/Redis/OTel) or reopen the application-layer technology freeze. Do
not reopen ADR-0001–0007 without new measured/real evidence. Do not touch
`privacy-forge`, `laravel-consent-guard`, `bookslot`, or `lexicon`.
