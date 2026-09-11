# Decision Log
> Purpose: why things are the way they are, so decisions are not silently undone
> Project: pulsewatch (public)
> Last updated: 2026-09-11 (Session 19)

Full reasoning for each ADR lives in `docs/adr/`. This log is the
short-form index — read it first, open the linked ADR for the trade-off
detail.

## ADR-0001 — Scheduler Restart-Safety and Run-Exclusivity via Postgres Row Leasing
- **Date:** 2026-08-29 · **Status:** accepted · [Full ADR](../adr/ADR-0001-scheduler-restart-safety-leasing.md)
- **Decision:** an atomic conditional `UPDATE ... RETURNING` lease claim on
  a per-target `target_schedule` row (owner + expiry), not in-memory
  tracking, a Redis lock, or `pg_advisory_lock`.
- **Must not be silently reversed because:** it's the single mechanism
  that satisfies both FR-005/NFR-007 (never two overlapping checks) and
  FR-006/NFR-003/NFR-004 (restart recovery, zero duplicate alerts) with no
  separate "restart recovery" code path — a fresh process's first tick
  runs the identical claim logic as every other tick.

## ADR-0002 — Alert-Suppression State Machine
- **Date:** 2026-08-29 · **Status:** accepted · [Full ADR](../adr/ADR-0002-alert-suppression-state-machine.md)
- **Decision:** an explicit 3-state machine (Healthy/Suspect/Alerting)
  whose edge transitions (open/close an incident) are guarded by an
  atomically-conditional write, backed by a partial unique index
  (`incidents (target_id) WHERE closed_at IS NULL`) — not an inline
  counter check.
- **Must not be silently reversed because:** it's what makes "exactly one
  opened alert, exactly one resolution notification" (US-005/FR-015/
  FR-016) a database-enforced guarantee rather than a caller-discipline
  assumption, including across a restart (US-008).

## ADR-0003 — Agent-Initiated Push Reporting and Assignment Discovery
- **Date:** 2026-08-29 · **Status:** accepted · [Full ADR](../adr/ADR-0003-agent-push-reachability-model.md)
- **Decision:** agent-initiated outbound reporting only (results via OTLP
  through the OTel Collector; assignment list via an authenticated REST
  poll from the agent) — never server-pull, never a manually-distributed
  static config file.
- **Must not be silently reversed because:** it's the concrete mechanism
  that makes FR-018's "no inbound port on the agent's network" real, and
  the direct fulfillment of the private-target-reachability justification
  `00b-build-vs-alternatives.md` names for building pulsewatch at all.

## ADR-0004 — Bounded Worker-Pool Scheduler with Context-Based Graceful Drain
- **Date:** 2026-08-29 · **Status:** accepted · [Full ADR](../adr/ADR-0004-go-concurrency-worker-pool.md)
- **Decision:** a long-lived, fixed-size worker pool (default 20) fed by a
  blocking channel, 1s scheduler tick, per-check `context.WithTimeout`, and
  a bounded hard-shutdown deadline after which an abandoned in-flight
  check's lease is safely left to expire (ADR-0001) rather than force
  anything to complete.
- **Must not be silently reversed because:** the worker-pool bound is what
  satisfies US-007's "not an unbounded synchronized burst" after a
  mass-restart, and the graceful-drain design is only safe because of
  ADR-0001's durable leasing — the two are one connected design, not two
  independent choices.

## Session 12 — Hourly Rollup Job: Recompute-Everything, Not Bounded-Lookback
- **Date:** 2026-08-31 · **Status:** accepted (not a new ADR — implements a
  pre-aggregation decision `04-data-model.md` already made in Session 3/4) ·
  [Full write-up](../project-memory/04-data-model.md#session-12-addendum-the-hourly-rollup-job-real-implemented)
- **Decision:** `internal/rollup`'s hourly job recomputes and
  `ON CONFLICT ... DO UPDATE`s every `(target, hour_bucket)` row since each
  target's `created_at` on every tick, rather than a bounded lookback plus a
  separate one-time backfill pass.
- **Must not be silently reversed because:** it's what makes the job
  self-healing for late-arriving `check_results` (an agent-reported OTLP
  result landing after its own hour already rolled up) with no separate
  dirty-bucket tracking table — replacing it with a bounded lookback removes
  that self-healing property for anything outside the lookback window unless
  a real, separate reconciliation mechanism replaces it. Revisit only
  against measured tick duration exceeding NFR-005's 5-minute bound (this
  session measured 350ms on real data), not by default habit.

## Session 13 — Rollup Cadence Gaps Get Visibility Logging, Not a Forced-Tick Workaround
- **Date:** 2026-09-01 · **Status:** accepted (implementation-level, not a new ADR)
- **Decision:** when the real-world gap since the rollup job's previous tick
  exceeds `TickInterval` by more than 10%, `rollup.Run` now logs a `WARN`
  naming the expected interval and the actual gap. No mechanism was added to
  force ticks to happen during a gap, and no periodic-restart or watchdog
  workaround was added.
- **Must not be silently reversed because:** the originally-reported "rollup
  job silently stopped ticking" symptom (R-006, `10-risk-register.md`) was
  root-caused to this developer's own machine — Docker Desktop's WSL2 VM
  going unscheduled for extended stretches during host idle, confirmed by
  identical simultaneous silent gaps in `pulsewatch-postgres-1`'s own logs
  and `pulsewatch-backend-1`'s entire HTTP traffic, not just rollup logging.
  A `time.Ticker` cannot fire while its process isn't being scheduled at
  all — there is no code-level fix for that. Forcing a workaround (e.g.
  periodically restarting the job) would treat a real environmental
  characteristic as a code defect and add machinery that solves nothing a
  real always-on deployment would ever need. The chosen fix instead makes
  the *existing* self-healing property (Session 12's recompute-everything
  design already means no data is lost across a gap) observable, so an
  operator watching real logs knows when `/slo`'s freshness bound was
  actually violated instead of trusting a silently-stale dashboard.
  Revisit if a real always-on deployment (post R-003) ever shows this
  warning firing — that would mean the mechanism is not, in fact, limited
  to dev-machine VM idling, and would need real investigation.

## Session 14 — IaC/Kubernetes Scope Expansion: a Reasoned Amendment to Rule D3, Not a Silent Violation
- **Date:** 2026-09-06 · **Status:** accepted · [Full ADR](../adr/ADR-0005-iac-terraform-and-kubernetes-deployment.md)
- **Decision:** the project owner confirmed a supplementary technology
  budget of two — **Terraform** and **Kubernetes** — on top of Rule D3's
  original two-technology freeze (Go concurrency, OTel pipelines), scoped
  strictly to the deployment layer. Terraform manages cluster-level
  platform state (namespace, secrets, ingress controller, TLS issuer) via
  the `hashicorp/kubernetes`/`hashicorp/helm` providers, against a
  Kubernetes cluster the operator already controls (BYO-cluster — this
  project does not provision cloud compute on the operator's behalf).
  Application workload manifests are plain Kubernetes YAML composed with
  Kustomize, applied separately from Terraform's own apply cycle, service-
  for-service mirroring `docker-compose.yml` (Docker Compose remains the
  documented local/dev path, not replaced).
- **Must not be silently reversed because:** it is the only way this
  repo's own claimed deep phases (Release & Deployment, Operations &
  Maintenance — `00a-ledger-confirmation.md`) can demonstrate a real
  rolling upgrade (old and new pods briefly coexisting, exactly the case
  ADR-0001's Postgres leasing was already designed to survive) and a
  declarative, rebuildable infrastructure story. `docker compose up
  --build` replaces a service outright — it structurally cannot exercise
  the overlap window ADR-0001's own revisit trigger anticipated ("if a
  future session ever adds a second server instance for real, this
  mechanism already generalizes without modification").
- **Named, not hidden, limitation:** this session's own sandbox has no
  outbound access to the Terraform provider registry and no live
  Kubernetes cluster or Docker daemon — `terraform apply` and a real
  `kubectl apply` reaching a `Running` pod were not exercised. What was
  verified offline: `terraform fmt -check` (HCL syntax), a real `kubectl
  kustomize` build (base + overlay merge, no cluster contact required),
  and direct manifest/HCL review. Tracked as B-009 (`11-backlog.md`) —
  real-cluster verification is the next session's actual objective, not
  optional polish, before Kubernetes can be called this project's *proven*
  production target rather than its documented one.
- **Does not reopen the application-layer freeze**: TimescaleDB,
  Prometheus/Grafana, and a message queue remain excluded from the
  application itself, per `01-scope-and-non-goals.md`'s own (updated, not
  deleted) non-goal row — this amendment is scoped to the deployment layer
  only.

## Session 15 — Real-Cluster Verification of ADR-0005 (B-009): Amendment, Not Reversal

- **Not a new ADR, and not a reopening of ADR-0005's Decision.** B-009
  asked whether Session 14's Kubernetes deployment target actually works,
  not whether it should exist or be built differently — this session
  found real evidence, it did not revisit the design. Recorded as an
  update appended to ADR-0005 itself (its own convention: see that file's
  Session 15 section), not a separate ADR-0006.
- **The real blocker Session 14 named (no live cluster) turned out to be
  two distinct, fixable sandbox quirks, not a fundamental sandbox
  limitation** — this session's own sandbox got a working Docker daemon
  and installable `kubectl`/`kustomize`/`terraform` (unlike Session 14's),
  but real pods still would not start until two root causes were found by
  replaying the exact failing container spec directly through `runc
  --debug` rather than trusting containerd's own terse error message: (1)
  this sandbox kills container creation when a cgroup path matches a real
  kubelet pod's shape (`kubepods/besteffort/...`) combined with a new
  network namespace — almost certainly because the sandbox VM is itself a
  pod on a real Kubernetes host and that path is reserved; (2)
  containerd's default sandbox `oom_score_adj` (-998) is rejected by this
  sandbox's own resource controls. Both are named, reproducible, and
  fixed via documented flags (`cgroupsPerQOS: false`,
  `restrict_oom_score_adj = true`) rather than routed around blindly.
- **Container-registry blob downloads are blocked everywhere tested**
  (Docker Hub, GHCR, `registry.k8s.io`, `quay.io`, public ECR) even though
  each registry's own API is reachable — a different, more precise
  finding than Session 14's "no Docker daemon." Every image this session
  needed was built from source (Go binaries compiled locally) or from
  real Ubuntu packages (`debootstrap` + `apt`, both reachable) instead of
  pulled. This is a real, working substitute for verifying this repo's
  own manifests against a real cluster; it is not a substitute for
  verifying the specific upstream images (`postgres:16-alpine`,
  `redis:7-alpine`, `otel/opentelemetry-collector-contrib`) this repo's
  manifests actually name in production.
- **`terraform apply` remains blocked, precisely re-confirmed rather than
  re-assumed:** `registry.terraform.io` (the provider registry) is an
  explicit `403` organization-policy denial this session too, even though
  the Terraform CLI itself installs fine here (unlike Session 14). Two
  different, independently-confirmed facts, not one repeated claim.
- **One real design refinement came out of measurement, not
  speculation:** continuous health-check polling during three real
  rolling updates found a small, real, repeatable gap in `backend.yaml`'s
  `maxUnavailable: 0` guarantee (kube-proxy's Endpoint removal racing the
  terminating pod). Fixed with a `preStop: sleep 5` — the standard,
  minimal mitigation — rather than either ignoring the measurement or
  over-engineering a load-balancer-level fix this sandbox can't verify
  anyway (no ingress-nginx image, see above).
- **Evidence saved as files, not just narrated** — this project's own
  established practice (`00c-evidence-preservation.md`), followed here
  for every claim above: `docs/project-memory/evidence/session15-*`.

## ADR-0006 — Webhook Dispatch: Real Delivery, Retry Policy, and Idempotency
- **Date:** 2026-09-06 · **Status:** accepted · [Full ADR](../adr/ADR-0006-webhook-dispatch-delivery-semantics.md)
- **Decision:** `WebhookDispatcher` (real `HTTP POST`, retried in-process up
  to 3 times with exponential backoff, 200ms→2s cap, 5s per-attempt
  timeout) replaces `LogDispatcher` as the default `Dispatcher` for
  `"webhook"` channels. No async outbox/retry-queue table — retries happen
  synchronously inside the same `Dispatch` call `NotifyChannels` already
  makes, so `alert_dispatches` keeps its existing one-row-per-attempted-
  notification shape (two new columns, `attempts`/`last_error`, migration
  `000010`). `"email"` channels are reported back as not implemented this
  session, never silently dropped or falsely confirmed.
- **Must not be silently reversed because:** it's what makes ADR-0002's
  already-guaranteed "exactly one dispatch attempt per edge transition"
  land somewhere real for a webhook channel — before this, every channel
  type, `LogDispatcher`, delivered nothing regardless of the guarantee
  gating it. Reverting to a log-only stub without a real replacement would
  silently regress FR-013 (webhook notification) to a no-op again.
- **Before writing any code, this session read `dispatch.go`/`channel.go`
  directly** (per `12-session-handoff.md`'s own instruction) and confirmed
  the incident-to-dispatch wiring (`scheduler.handleJob`,
  `agentapi.processCheckResult` → `alerting.NotifyChannels`) was already
  built and already tested — the only real gap was `LogDispatcher` itself
  never touching a channel's decrypted destination. This session's actual
  scope was narrower than "wire dispatch into the incident state machine,"
  because that wiring already existed.

## ADR-0007 — Mobile Push as a Dispatch Channel: Fan-Out, Dead Tokens, and Device Registration
- **Date:** 2026-09-07 · **Status:** accepted · [Full ADR](../adr/ADR-0007-mobile-push-dispatch-channel.md)
- **Decision:** a third `alert_channels.type`, `'push'`, whose
  `destination_encrypted` holds a *provider credential* (Firebase
  service-account JSON, or an APNs `.p8` token-auth object) rather than a
  single address; a new `device_tokens` table holding the actual delivery
  addresses (migration `000011`); `alerting.PushDispatcher` fanning one
  incident transition out over every live token for that channel's
  provider, via `internal/pushprovider`'s real FCM HTTP v1 and APNs HTTP/2
  clients (stdlib only, no new module dependency); and
  `alerting.ChannelRouter` taking over the channel-type-to-implementation
  decision that `WebhookDispatcher` used to answer for by itself.
- **The one substantive departure from ADR-0006's retry policy, and why:**
  a webhook failure is either "retry" or "give up". A push failure has a
  third shape — an uninstalled app or a rotated token — that must **never**
  be retried and must be *recorded*, or every future incident pays for it
  again. `device_tokens.dead_at`/`dead_reason` is that record; transient
  provider failures still retry on ADR-0006's unchanged backoff; a rejected
  *credential* aborts the whole fan-out instead of repeating one
  configuration failure once per registered phone, and never marks a device
  dead. Dead-marking is only safe because re-registration resurrects a
  token, so a wrongly-killed device recovers on the operator's next app
  launch instead of being muted forever.
- **Must not be silently reversed because:** it is what makes
  `pulsewatch-mobile` a companion app rather than a smaller copy of the
  SvelteKit dashboard — a mobile client that polls
  `GET /targets/{id}/incidents` on a timer has no reason to exist. Removing
  the dead-token path specifically (the tempting "simplify: retry
  everything like the webhook does") silently reintroduces unbounded waste
  on every incident for every uninstalled app, which is the exact failure
  mode this ADR was written around.
- **Named, not hidden, limitation:** no notification was delivered to a real
  device by real FCM or real APNs infrastructure — that needs a Firebase
  service-account key and an Apple Developer `.p8` with a registered bundle
  id, real credentials no public repo or CI job can hold. Both protocols
  are implemented against the providers' real published APIs and exercised
  against mock servers that speak them (real OAuth 2 JWT-bearer exchange,
  real paths/headers, real error bodies, cryptographically verified
  signatures, real HTTP/2 for APNs), never faked into success. Tracked as
  B-016, the same way B-013 tracks the `registry.terraform.io` block.
- **Does not reopen ADR-0002 or ADR-0006.** ADR-0002's exactly-once
  edge-transition guarantee is what gates dispatch, unchanged;
  `DispatchOutcome` and `alert_dispatches`' one-row-per-attempted-
  notification shape are unchanged (a fan-out aggregates into that one row
  and needs no migration). The only amendment is to `LastError`'s meaning:
  for a fan-out channel it is empty only when *every* live device was
  reached, so a partial success reports its shortfall rather than hiding
  behind `delivery_confirmed=true`.

**Session 19 addendum (B-017, device list dashboard) — `revoked_at` added
to the read surface.** `device_tokens.revoked_at` (this ADR's own column)
was written by `RegisterDeviceToken`/`RevokeDeviceToken` from the start,
correctly excluded from `loadLiveDeviceTokens`'s fan-out `WHERE` clause,
but never selected by `ListDeviceTokens` or present on the `DeviceToken`
OpenAPI schema — nothing had consumed it, since the mobile app has no
reason to display its own revocation state back to itself. This was found
by hand, not by a pre-existing test: `DELETE /api/v1/device-tokens/{id}`
returned `204` and the fan-out set genuinely shrank, but the same operator's
next `GET` still showed that device as indistinguishable from "Active." Not
a design decision (nothing about the write path, the fan-out query, or
ADR-0007's schema changed) — a read-path oversight, closed by adding
`revoked_at` to `DeviceTokenRecord`, `ListDeviceTokens`'s `SELECT`, and the
`DeviceToken` schema. Real operator/backend regression test:
`TestListAndUnregisterDeviceTokens` now asserts `revoked_at` is non-nil on
the list response after a revoke, not just that the row is still present.

## ADR-0008 — Email as a Dispatch Channel: SMTP Delivery and Retry Semantics
- **Date:** 2026-09-11 · **Status:** accepted · [Full ADR](../adr/ADR-0008-email-dispatch-channel.md)
- **Decision:** `alerting.EmailDispatcher` — a real SMTP send
  (`internal/emailprovider`, RFC 5321/3207/4954 spoken directly over
  `net/smtp`, no new module dependency) with STARTTLS and implicit-TLS
  support — is the real `Dispatcher` for `"email"` channels, registered
  alongside `WebhookDispatcher`/`PushDispatcher` in
  `alerting.NewDefaultDispatcher`. It is `WebhookDispatcher`'s sibling, not
  `PushDispatcher`'s: an email channel has one destination (the recipient
  address, already a valid `alert_channels.type` since migration `000008`,
  Session 6 — **no schema or endpoint change was needed for B-014's
  registration side**, only delivery was ever stubbed), so it reuses
  ADR-0006's exact retry/backoff policy rather than push's fan-out/
  dead-token shape.
- **Retryable vs. not, reusing ADR-0006's own convention for SMTP:** a `4xx`
  reply (any phase) or a network/transport failure retries with ADR-0006's
  unchanged backoff. A `5xx` on `RCPT TO`/`MAIL FROM`/`DATA` — the classic
  case being `550` "mailbox unavailable," SMTP's own hard-bounce signal —
  is `KindPermanent`: one attempt, never retried, the SMTP-protocol sibling
  of ADR-0007's dead-token case. A `5xx` (or a non-protocol refusal) on
  `AUTH` is `KindCredential`: also never retried, but never blamed on the
  recipient — it's the operator's own relay account, not the address, that
  needs fixing.
- **Deliberately narrower than ADR-0007's dead-token design, and why:**
  push needed `device_tokens.dead_at` because one credential fans out to
  *N* tokens and a wrongly-live dead one gets retried forever otherwise.
  An email channel has exactly one destination per row — the same shape a
  permanently broken webhook URL already has, and that has never needed a
  persistent-suppression column either. A `550` is recorded honestly in
  that row's own `alert_dispatches.last_error`, visible on the very next
  incident; a second, email-only suppression mechanism for a problem
  webhook already lives with unremarked would have been new, unrequested
  machinery, not a gap this session found evidence for.
- **Must not be silently reversed because:** it closes B-014, the last
  planned dispatch channel from ADR-0006's original three-channel scope
  (webhook/push/email) — reverting to "not implemented" would regress a
  real, working delivery path back to the honest-but-inert stub ADR-0006
  shipped for it.
- **Named, not hidden, limitation:** no email was delivered by a real SMTP
  relay to a real inbox — needs a real relay account (Gmail app password,
  SES/SendGrid SMTP credentials, or similar) this sandbox cannot hold or
  reach. The client is implemented against the real, published protocol and
  exercised against a real server (`net.Listener` + `net/textproto`, not a
  mocked `Send`) that speaks it, the same honest-gap pattern B-013/B-016
  already established rather than faked into success.
- **Confirmed, while starting this session, that the registration side was
  already real:** `operatorapi.CreateAlertChannel` has never special-cased
  `"email"` — `POST /api/v1/alert-channels` with `{"type":"email",...}`
  already worked, encrypted at rest, since Session 6, well before ADR-0006
  existed to build a sender for it. This session's actual scope was
  narrower than "build email channel support" for the same reason
  ADR-0006 found webhook's own incident-to-dispatch wiring already built:
  only the one real gap (delivery) needed closing.
- **B-006 test-fixture-cleanup gap, re-confirmed from a new angle:** unlike
  webhook/push (whose destination directly names a per-test server/
  credential), an email channel's destination is only the recipient
  address — the relay dialed is the dispatcher's own shared `Config`. A
  leftover channel row from an earlier test in the same package therefore
  genuinely redials whatever fake SMTP server the *current* test is
  running, not a harmlessly-closed old one. Worked around in this session's
  own tests (unique recipient addresses, assertions scoped by recipient);
  B-006 itself stays open, out of this session's scope.
