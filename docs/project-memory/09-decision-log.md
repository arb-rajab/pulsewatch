# Decision Log
> Purpose: why things are the way they are, so decisions are not silently undone
> Project: pulsewatch (public)
> Last updated: 2026-09-06 (Session 17)

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
