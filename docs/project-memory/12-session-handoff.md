# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed

- Session number and title: **Session 22 — Backlog Cleanup: B-006 (Test-
  Fixture Cleanup Ordering) and B-012 (Security/Testing Docs).**
- Objective: close two long-carried backlog items — B-006 (root-cause the
  test-fixture cleanup bug R-005 has tracked since Session 12, not just
  work around it again) and B-012 (fill `06-security-threat-model.md` and
  `07-testing-strategy.md`, both empty section-header skeletons since
  Session 14). B-013 (Terraform apply/cert-manager real-cloud
  verification, sandbox-egress-blocked) and B-016 (real FCM/APNs delivery
  with live credentials, permanently descoped) were explicitly out of
  scope and untouched.
- Status: **done, both items closed.**

### B-006: two distinct root causes, not one

The bug R-005 (`10-risk-register.md`) has described since Session 12 —
`t.Cleanup`'s best-effort `DELETE` silently failing and leaving orphaned
rows — turned out to have **two independent causes** once actually
traced, not the single one every prior session's own description assumed:

1. **Foreign-key ordering.** `check_results`/`incidents`/
   `check_rollups_hourly` (plain `REFERENCES` to `targets`) and
   `alert_dispatches` (plain `REFERENCES` to both `incidents` and
   `alert_channels`) were still present when a fixture's own
   `DELETE FROM targets`/`DELETE FROM alert_channels` ran — the delete hit
   a foreign-key violation that `_, _ = pool.Exec(...)` silently
   discarded. `internal/rollup`'s own fixture already had the correct
   child-before-parent fix (Session 12); the other four packages named in
   B-006's own title (`scheduler`, `agentapi`, `alerting`, `operatorapi`)
   did not.
2. **A second, previously-undocumented bug, found while fixing the
   first.** Several of `operatorapi`'s older fixtures
   (`targets_test.go`, `status_test.go`, `agents_test.go`,
   `alertchannels_test.go`, `incidents_test.go`) called
   `pool.Exec(t.Context(), ...)` **inside their own `t.Cleanup` closure**.
   `testing.T.Context()`'s own doc comment: "canceled just before
   Cleanup-registered functions are called" — so every one of those
   deletes returned `context.Canceled` immediately, never reaching
   Postgres at all, and the error was discarded the same way as (1). This
   is a strictly worse failure mode than (1) (guaranteed no-op vs.
   conditional FK failure), and explains why `operatorapi` was named in
   B-006's scope even for fixtures with no FK-child-row exposure at all.

**Fix:** a shared `deleteTargetCascade(pool, targetID)` helper added to
each of `scheduler/testdb_test.go`, `agentapi/testdb_test.go`,
`alerting/testdb_test.go`, and `operatorapi/testdb_test.go`, deleting
`alert_dispatches` → `incidents` → `check_results` →
`check_rollups_hourly` → the target, in that order, on a fresh
`context.WithTimeout(context.Background(), 5*time.Second)`
(`target_schedule` needs no explicit delete — it's the one child table
with real `ON DELETE CASCADE`). The two alert-channel fixtures that can
hold live `alert_dispatches` rows against them
(`scheduler/alert_lifecycle_test.go`, `alerting/dispatch_test.go`) got the
equivalent one-line-earlier delete for `alert_channels` itself. Every
`operatorapi` cleanup closure that used `t.Context()` was moved onto a
fresh context, consolidated behind new `deleteAgentCleanup`/
`deleteIncidentCleanup` helpers alongside `deleteTargetCascade`. No
sleep/retry anywhere — every fix is either an ordering fix or a
context-lifetime fix, matching this session's own instruction not to
paper over the bug.

**Verified, not just fixed:** two consecutive full
`go test ./... -race -p 1 -count=1` runs against a real local Postgres
both passed (all 13 packages, 52 test files), and a direct
`SELECT count(*)` afterward against every table a test fixture writes
(`targets`, `check_results`, `incidents`, `alert_dispatches`,
`alert_channels`, `agents`, `target_schedule`, `device_tokens`,
`check_rollups_hourly`, `operators`) read back exactly zero rows both
times. The first verification attempt showed 23 leftover `alert_channels`
rows and looked like a fix gap — traced to stale data left by an earlier
pre-alert-channel-fix run that was never truncated in between, not a
live per-run leak; a clean `TRUNCATE` plus a fresh run confirmed zero, and
a second consecutive run without truncating in between confirmed it holds
across repeated runs, not just once. `go vet ./...` and
`golangci-lint v2.13.2` (CI's exact pinned version) both clean, 0 issues.

Closes B-006 (`11-backlog.md`) and R-005 (`10-risk-register.md`).

### B-012: real audit, not invented content

Both files' remaining empty section headers are now filled from a real
code audit — every claim in the diff cites the file/function/line it
comes from, not a generic "how a system like this would typically work"
description:

- `06-security-threat-model.md`: Assets and data classification, Trust
  boundaries, a ten-row STRIDE table, Abuse cases, Authentication and
  authorisation design, Dependency and supply-chain controls, and
  Accepted risks. Kept the Session 14 secrets-management content, which
  was already accurate, rather than re-deriving it.
- `07-testing-strategy.md`: Testing philosophy, a Levels table (13
  backend packages / 52 test files, frontend Vitest scope, every CI
  static-analysis/scanning job), Security testing (points at the file
  above rather than duplicating it), Accessibility testing, Performance
  testing and budgets, Test data strategy, and Quality gates in CI.

**Two real, previously-undocumented gaps were found and named honestly**
while auditing, rather than glossed over:

- No SSRF guard exists on operator-configured webhook destinations —
  `CreateAlertChannel` accepts any syntactically valid URL, and nothing
  resolves/blocklists private or loopback address ranges before
  `WebhookDispatcher` makes the real request. Accepted for now under the
  single-trusted-operator model; named as a real revisit trigger if
  multi-operator access is ever added.
- `device_tokens.token` (push registration) is stored in **plaintext** —
  unlike `alert_channels.destination_encrypted`, which is AES-256-GCM
  encrypted at rest. No `EncryptDestination`-equivalent call exists
  anywhere in `devicetokens.go`.

Also named honestly rather than overclaimed: three numeric NFRs
(`NFR-002` alert latency, `NFR-006` retention timing, `NFR-009` dashboard
read p95) have no automated measurement anywhere in this repo (`grep -r
"func Benchmark"` returns nothing) — functional correctness is tested,
the numeric budgets themselves are not gated in CI. Accessibility testing
is named as a real, unaddressed gap, not a documented non-goal.

Closes B-012 (`11-backlog.md`).

### Verification performed (real, not just reading code)

- Started this sandbox's stopped local `postgresql@16` cluster, created a
  `pulsewatch` role/database, applied all 11 migrations with the real
  `migrate` CLI (v4.19.1, `go install -tags 'postgres'`).
- `go build ./...`, `go vet ./...` — clean.
- `go test ./... -race -p 1 -count=1` — twice consecutively, all 13
  packages passing both times.
- Real row-count verification against every fixture-written table after
  each run (see above) — the actual proof this session's own instruction
  ("re-run the full test suite... since fixture-ordering bugs can mask or
  reveal other flaky tests") asked for, not just a green `go test`.
- `golangci-lint v2.13.2` (CI's exact pinned version, `go run .../
  golangci-lint@v2.13.2`) — 0 issues on the full module.
- Every factual claim in the two filled-in docs was checked against the
  real source (`grep -n`/`sed -n` against `operatorauth`, `agentauth`,
  `alerting`, `operatorapi` — bcrypt cost, rate-limiter window/threshold,
  session HMAC construction, AES-256-GCM key size, CSRF middleware
  mechanism) rather than written from memory of how such a system
  "usually" works.

### PR / merge status

- Branch: `claude/backlog-cleanup-b006-b012-ta2l41`.
- No PR created this session (not requested).
- CI: not yet run against this branch — a future push should confirm the
  `backend` job (which now includes this session's two-root-cause fix)
  stays green, matching this session's own local `-race -p 1` result.

## Real gaps found and named during this session

- No SSRF guard on webhook-channel destinations (new — see B-012 section
  above and `06-security-threat-model.md`'s T-08/Accepted risks).
- `device_tokens.token` stored in plaintext (new — see T-09/Accepted
  risks in the same file).
- No `.github/dependabot.yml` (already known since Session 20/21, restated
  in the new docs rather than re-discovered).
- Three NFRs with no automated measurement (`NFR-002`, `NFR-006`,
  `NFR-009`) and zero `testing.B` benchmarks anywhere in the repo (new —
  see `07-testing-strategy.md`'s "Performance testing and budgets").
- No accessibility testing tooling or review exists anywhere (new — see
  the same file's "Accessibility testing").
- B-013 (Terraform apply/cert-manager real-cloud verification) and B-016
  (real FCM/APNs delivery with live credentials) are unchanged, untouched
  this session, per this session's own explicit instructions — B-013
  remains sandbox-egress-blocked, B-016 remains permanently descoped by
  design.

## Open questions and risks

- **R-002, R-003, R-006 through R-008 (carried forward, untouched):**
  unchanged — see `10-risk-register.md`.
- **R-005 is now closed** (this session) — see its Closed-risks entry for
  the full resolution.
- **R-007** (`alert_lifecycle_test.go` timing flake under real concurrent
  load) is a distinct, still-open flake from the R-005 mechanism this
  session fixed — not touched, since this session's own fixture-ordering
  fix does not change query/dispatch latency under contention, only
  cleanup correctness. A future session re-running the full suite under
  load should not conflate the two if it recurs.

## Next recommended session

- **B-007** (operator password reset/rotation path) and **B-008**
  (server-side session revocation on logout) are both real, small-medium,
  previously-scoped security/ops gaps still open — either is a reasonable
  next pick, and both are now cross-referenced from the freshly-filled
  `06-security-threat-model.md`.
- **B-011** (Postgres backup/restore for the Kubernetes target) remains
  open and load-bearing now that Kubernetes is a real, cluster-verified
  target (Session 15/B-009).
- The two new gaps this session named (SSRF guard on webhook
  destinations; plaintext `device_tokens.token`) are real, small, and not
  yet backlog rows of their own — a future session should decide whether
  to open dedicated backlog IDs for them or fold them into an existing
  security-hardening pass.
- **B-013** and **B-016** remain blocked/descoped exactly as before — do
  not reopen without new sandbox capability (B-013) or without the
  explicit go-ahead this project has never given (B-016).
- Do **not** reopen ADR-0001–0008 without new measured/real evidence.
- Definition of done: whatever is picked up, verified against a real
  local Postgres with real `go test` output, per this project's own
  standard.

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Companion repository:** `pulsewatch-mobile` — untouched this session.

**Status of `pulsewatch` itself:** Sessions 18–21 (mobile push dispatch,
device-list dashboard, email dispatch, CodeQL integer-conversion triage)
all closed, unchanged. Session 22 (this session) is a backlog-cleanup
session, not a feature session: it touches test fixture files across
`scheduler`/`agentapi`/`alerting`/`operatorapi` (no production code, no
schema migration, no new endpoint, no new dependency) and two
documentation files (`06-security-threat-model.md`,
`07-testing-strategy.md`).

**Problem being solved:** unchanged — see `00-project-brief.md`.

**Current stack:** unchanged.

**Architecture decisions that must not be reversed:** ADR-0001–0008
unchanged, not reopened. This session created no new ADR — fixing a
test-cleanup bug and filling in documentation are not architectural
decisions.

**Implementation state:**
- Done: everything Session 21 left done, plus this session's B-006 fix
  (test fixtures only) and B-012 docs fill.
- In progress: nothing mid-flight.
- Not started (carried forward): B-004, B-005, B-007, B-008, B-009 (mostly
  closed, narrow remainder is B-013), B-010, B-011, B-013, B-016 — see
  `11-backlog.md`.

**Constraints and non-goals:** unchanged — see `01-scope-and-non-goals.md`.

**Ground rules:** Do not change the application stack (Go/Gin/SvelteKit/
Postgres/Redis/OTel) or reopen the application-layer technology freeze. Do
not reopen ADR-0001–0008 without new measured/real evidence. Do not touch
`privacy-forge`, `laravel-consent-guard`, `bookslot`, or `lexicon`.
