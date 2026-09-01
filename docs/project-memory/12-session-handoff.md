# Session Handoff

## Re-verification pass (2026-09-01, same day as Session 13)

Session 13's own claims for items 1 (rollup cadence root cause), 2 (the
extended-observation window), and 3 (the real, non-mock auth capture) had
been narrated accurately in this file but were **not backed by any
retrievable raw artifact** — the underlying containers had been recreated
after the fact, so there was nothing left to point to. A dedicated
re-verification pass re-ran all three and this time persisted the actual
raw output as it was captured, under `docs/project-memory/evidence/`:

- **Item 1 (rollup cadence root cause) — re-confirmed and refined, not
  merely repeated.** A fresh live gap was caught in progress (not
  historical): rollup ticks and `pulsewatch-postgres-1`'s own logging both
  went silent after ~15:44–15:52 and were still silent ~3h later at capture
  time, correlated across both containers exactly as originally reported —
  but this time with **no** `Microsoft-Windows-Kernel-Power` sleep/wake
  event covering the gap window at all, meaning the mechanism isn't tied to
  an explicit OS sleep event the way the original write-up implied. Raw
  evidence: `docs/project-memory/evidence/session13-logs.txt`.
- **Item 2 (extended-observation window) — re-run live, artifact is the
  real stream.** `TickInterval` lowered to 20s
  (`session13-tickinterval-diff-lowered.txt`), backend rebuilt/redeployed
  (`session13-build-lowered.txt`, `session13-redeploy-lowered.txt`), real
  log output redirected live to a file as the job ran
  (`docs/project-memory/evidence/session13-rollup-ticks.log`) — 11
  consecutive real ticks at ~20s cadence (16:08:32–16:11:52), zero
  cadence-gap WARNs. Reverted (`session13-tickinterval-diff-restored.txt`
  — empty diff, confirming byte-identical to committed `main.go`), rebuilt
  (`session13-build-restored.txt`), redeployed, real post-restore startup
  tick captured (`session13-redeploy-restored.txt`).
- **Item 3 (real auth capture) — re-run live with a fresh credential
  reset.** The prior session's reset password wasn't preserved anywhere
  retrievable, so a new one was generated via the same documented
  break-glass path, named and confirmed with the user first
  (`session13-pw-reset-output.txt`). Real login → real `/slo` (separate TLS
  connection) → real dashboard render, all captured live via
  `curl -v ... | Tee-Object` as the requests happened:
  `docs/project-memory/evidence/session13-auth-capture.txt` (login +
  `/slo`), `session13-dashboard-capture.txt` (dashboard HTML, `99.91%`
  uptime matching the `/slo` response verbatim in both the rendered table
  and the hydration data island). Throwaway reset tool
  (`backend/cmd/_verify_resetpw`) and the session cookie jar were deleted
  after use, consistent with Session 12/13's own precedent.

No R-005 recurrence this pass (no `go test` was run; `targets` count
confirmed still exactly 4 real rows throughout). See
`00c-evidence-preservation.md` for why this pass was needed and the
practice change it prompted.

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed
- Session number and title: **Session 13 — Rollup Job Stall, Real Auth Capture, New Flake.**
- Objective: three bounded, evidence-driven tasks surfaced by a real
  evidence-gathering pass after Session 12's own handoff overclaimed two
  things — root-cause and fix the rollup job's apparent stall; obtain a
  real, non-mock authenticated `/slo` + dashboard capture (Session 12's own
  capture used a session minted directly via `operatorauth.IssueSession`,
  bypassing the real operator's actual password, and was explicitly labeled
  mock for the parts that needed a real login); and log a newly-surfaced
  test flake, distinct from R-002.
- Status: **complete**, with an important correction to the task's own
  framing — see "Real bugs found" below. The rollup job was never actually
  broken; the flake was real-reproduced (including reproducing under
  `go test -p 1`, unlike R-002); the real auth capture is real.

## Work completed

### `backend/internal/rollup/rollup.go` (edited)
Added `cadenceGapExceeded` and wired it into `Run`'s tick loop: when the
real wall-clock gap since the previous tick exceeds `TickInterval` by more
than 10%, logs `WARN "rollup job cadence gap exceeded expected interval —
check_rollups_hourly was stale until this run"` with the expected interval
and the actual gap. This is **not** a fix for the originally-reported
symptom in the sense of making the job tick more reliably — see "Real bugs
found" below for why no such fix exists or is needed. It makes a
previously-silent cadence violation observable.

### `backend/internal/rollup/cadence_test.go` (new)
`TestCadenceGapExceeded`: five cases (on-time, small jitter under load,
just under/over the 10% tolerance, and the real 13h33m gap observed on this
machine) against the new threshold function, deterministic, no DB needed.

### `docs/project-memory/09-decision-log.md`, `10-risk-register.md`, `11-backlog.md`, `13-release-notes.md` (edited)
New Session 13 decision-log entry (why visibility logging, not a forced-tick
workaround); new R-006 (rollup cadence gaps — root cause, evidence, fix) and
R-007 (the new scheduler flake) in the risk register; R-005 updated with
this session's own recurrence evidence; new B-007 backlog item (operator
password-reset tooling gap, found while doing Part 2); a real "Added" entry
in release notes.

### `backend/cmd/_verify_resetpw` (created, then deleted before this diff was finalized)
A throwaway tool, matching Session 12's own `_verify_mintcookie` precedent:
reset the one real operator's password via the project's own documented
break-glass path (`operatorauth.HashPassword` + a direct `UPDATE operators`)
so a real `POST /auth/login` could be captured. Named and confirmed with the
user before running (see "Decisions made"). Not present in this session's
final diff.

### No other backend/frontend code changed
`04-data-model.md`, `openapi.yaml`, the schema, the scheduler, alerting,
agent, and operator-auth packages are all untouched — this session's actual
code change is the single `cadenceGapExceeded` addition described above.

## Decisions made

- **The rollup job was not broken, and no "fix" that changes its ticking
  behavior was made.** The task's own framing assumed a job-specific bug
  ("silently stopped ticking... no error, no crash, no restart"). Real
  investigation (see "Real bugs found") showed the entire container/VM goes
  unscheduled for extended stretches on this developer's own machine, not
  that `rollup.Run`'s goroutine died — confirmed by identical simultaneous
  silent gaps in `pulsewatch-postgres-1`'s own logs and in
  `pulsewatch-backend-1`'s *entire* HTTP traffic, not just rollup logging.
  A `time.Ticker` physically cannot fire while its process isn't being
  scheduled; there is no code fix for that. Getting user confirmation
  before scoping a fix (rather than inventing a workaround like periodic
  restarts) surfaced the right-sized fix: visibility logging only. Full
  reasoning in `09-decision-log.md`'s new Session 13 entry.
- **Reset the one real operator's password via the documented break-glass
  path, after naming it and getting explicit confirmation first** (per
  Session 11's own closeout standing instruction on state-changing
  actions). `provision-operator` refuses to run once an operator exists,
  and this project has no reset/rotate tool — its own doc comment already
  named direct DB access as the accepted gap. A throwaway
  `backend/cmd/_verify_resetpw` (matching Session 12's own
  `_verify_mintcookie` precedent) computed a bcrypt hash for a freshly
  generated password and wrote it directly, then was deleted before this
  session's diff was finalized.
- **R-005 (test-fixture DB pollution) recurred multiple times during Part
  3's investigation and was named, confirmed, and cleaned up transactionally
  each time**, per the standing ground rule — see "Real bugs found" below
  for the actual counts. Confirmed this is not a one-off — it is exactly
  the mechanism R-005 already documents, still fully open, tracked as
  B-006.
- **Did not attempt to root-cause R-007 (the new flake) beyond what real
  evidence this session could gather**, exactly as the task's own scope
  allowed ("fixing it outright is optional this session — logging it
  accurately is not"). Did not touch R-002's own existing flakes.

## Real bugs found and fixed during this session's own verification

1. **Not a bug — the task's own premise about the rollup job was wrong,
   and this session corrected it with real evidence rather than either
   accepting the premise or silently ignoring it.** `docker inspect
   pulsewatch-backend-1 --format '{{.State.StartedAt}}'` showed the
   container had been running continuously (no restart, `RestartCount=0`)
   since 2026-08-31T15:36:31Z, yet only 3 `rollup job run complete` lines
   existed in its logs by 2026-09-01T11:26 (~20h later): 15:36:32
   (startup), 21:04:18 (+5h28m), 10:38:10 next day (+13h34m) — visibly
   irregular, not the ~hourly cadence NFR-005 requires. Cross-checking
   `pulsewatch-backend-1`'s own `[GIN]` HTTP-access-log line counts by hour
   showed the *entire* container's traffic went to zero for the same
   windows (0 lines 17:00–19:59 and 22:00–09:59, full ~4,000+/hour
   otherwise) — not just rollup logging. `pulsewatch-postgres-1`'s own log
   line counts by hour showed the identical gap pattern. This rules out a
   goroutine-specific failure (a panic would crash the whole process — no
   panic-recovery wrapper exists around any of `main.go`'s goroutines, and
   the process never restarted) and points at the container/VM itself going
   unscheduled. Confirmed via the WSL2 VM's own `/proc/uptime` (22.28h of
   real accumulated runtime measured at 2026-09-01T11:27:35Z, less than the
   VM's wall-clock age implied by its own boot-time-minus-uptime
   arithmetic) and Windows' `Microsoft-Windows-Kernel-Power` event log
   (real sleep(42)/wake(107) event pairs on this host — each only 1-5
   seconds apart, ruled out as the direct cause of multi-hour gaps; the
   actual mechanism is Docker Desktop's own WSL2 VM going idle/unscheduled
   during host inactivity, a distinct thing from full OS suspend). See
   R-006 for the full write-up.
2. **R-005 (known risk, not a new one) recurred four separate times this
   session**, each found, named, confirmed, and cleaned up before
   proceeding:
   - 5 orphaned rows after 5 isolated `TestEndToEnd_ThresholdCrossing...`
     reruns (targets count 4→9).
   - 74 orphaned rows found at the start of this session's second work
     block (leftover from a prior batch of runs interrupted by an
     environment restart mid-session — see item 3), cleaned back to 4.
   - 43 orphaned rows after the `-p 1` three-run verification batch,
     cleaned back to 4.
   - 23 orphaned rows after the final full `go test ./...` confirmation
     pass, cleaned back to 4.
   Each cleanup used a single transaction (child rows — `alert_dispatches`,
   `incidents`, `check_results`, `target_schedule`, `check_rollups_hourly`
   — deleted before the `targets` row itself), scoped explicitly to
   non-real target IDs, re-verified back to exactly the real 4 afterward
   each time.
3. **A real environment interruption mid-session, not a code or data
   bug**: this session's own shell tooling was restarted partway through
   (visible as a task-notification reporting two background shell tasks
   "stopped... may have been running when the previous Claude Code process
   exited"), and the Bash tool's PATH came back missing `docker`, `git`,
   and basic coreutils after the restart (PowerShell was unaffected and
   used for the remainder of the session). No data was lost — in-progress
   background test/build runs were simply re-run from scratch after
   confirming and cleaning up the state they'd left behind (item 2 above).

## Verification performed (all real, not description)

### Part 1 — rollup cadence
- **Root-cause evidence**: see "Real bugs found" #1 above — real
  `docker logs`/`docker inspect` output, real per-hour log-line counts
  across three containers, real WSL2 `/proc/uptime` and Windows event-log
  correlation.
- **Build/vet/gofmt/lint**: `go build ./...` clean; `go vet ./...` clean;
  `gofmt -l .` — the two pre-existing drift files (`internal/operatorapi/
  router.go`, `main.go`) are unrelated to this session's changes (confirmed
  via `git status`, neither is in this session's diff) and were left
  untouched, out of scope; `golangci-lint run ./internal/rollup/...` — `0
  issues`. Final full-repo `go build`/`go vet`/`gofmt -l`/`golangci-lint
  run ./...` reported below.
- **`TestCadenceGapExceeded`**: real `go test` output, 5/5 subtests pass.
- **Real extended-observation window**: `TickInterval` temporarily set to
  20s in `main.go` (reverted after — see below), backend rebuilt
  (`docker compose build backend`) and redeployed (`docker compose up -d
  backend`, real `Recreated`/`Started` in the compose output). Real logs
  showed 9 consecutive ticks at the expected ~20s cadence — 14:30:29,
  14:30:50, 14:31:10, 14:31:30, 14:31:50, 14:32:10, 14:32:30, 14:32:50,
  14:33:09 — comfortably past the "3-4 consecutive ticks" bar, with **zero**
  `cadence gap` WARN lines (confirming no false positives under normal
  continuous operation). `TickInterval` was then reverted to
  `rollup.DefaultConfig()`'s real 1h default, confirmed via `git diff
  backend/main.go` showing no changes, backend rebuilt and redeployed
  again — real startup tick confirmed in the post-revert logs
  (`2026/09/01 14:44:22 INFO rollup job run complete rows_written=246
  duration=942.023922ms`), `docker inspect`'s `StartedAt` showing the fresh
  `2026-09-01T14:44:20Z` restart. **This approach (temporarily lowering
  TickInterval for the observation, then restoring it) was used, exactly as
  the task's own accepted alternative described**, since the real root
  cause (host/VM idling) isn't something that can be triggered on demand to
  prove a multi-hour real-world observation directly.

### Part 2 — real authenticated capture
- **Confirmed the only existing operator account** (`operator@example.com`,
  `id=c26960b2-...`) via a real `psql SELECT` against
  `pulsewatch-postgres-1` before touching anything.
- **Reset its password via the documented break-glass path**, named and
  confirmed with the user first: a throwaway `_verify_resetpw` program
  (deleted before this diff was finalized) computed a bcrypt hash via
  `operatorauth.HashPassword` for a freshly generated 24-byte password and
  wrote it with a real `UPDATE operators SET password_hash = ... WHERE
  email = ...` (confirmed `1` row affected).
- **Real login**: `curl -v -k -c cookies.txt -H "Content-Type:
  application/json" -d '{"email":"operator@example.com","password":"..."}'
  https://localhost:8443/api/v1/auth/login` → real `200`, real
  `Set-Cookie: pulsewatch_session=...; HttpOnly; Secure; SameSite=Strict`,
  body `{"operator_id":"c26960b2-d54e-4aa8-bc32-5a44090ae1eb","email":
  "operator@example.com"}`.
- **Real `/slo` capture, replayed in a separate connection** (not the same
  TLS session as the login, proving persistence): `curl -v -k -b
  cookies.txt https://localhost:8443/api/v1/targets/2556de3a-.../slo` →
  real `200`,
  `{"target_id":"2556de3a-...","window_days":30,"window_start":
  "2026-08-02T12:00:00Z","window_end":"2026-09-01T12:00:00Z",
  "expected_checks":7943,"success_count":797,"failure_count":0,
  "unknown_count":7146,"uptime_pct":100,"slo_target_pct":99.9,
  "error_budget_consumed_pct":0}`.
- **Real dashboard render**: `curl -v -k -b cookies.txt
  https://localhost:8443/dashboard` → real `200`, real SvelteKit-rendered
  HTML (65,432 bytes) containing the real "Uptime (30d)" column with
  `100.00%` for the two healthy targets (`redis`, the real `backend`
  health check) and `0.00%` for the two deliberately-broken ones
  (`/this-path-does-not-exist`, `/also-does-not-exist`) — matching the
  `/slo` curl response's `uptime_pct` verbatim, both via the real hydration
  data island (`uptime_pct:100,slo_target_pct:99.9,...`) and the rendered
  table cells.
- **All of the above is real** — a real session, a real login, a real
  computed percentage from this instance's real historical `check_results`
  history, not mock/fabricated data (contrast Session 12's own capture,
  which minted a session directly and never exercised the real password
  path).

### Part 3 — new flake (R-007)
- **Isolated reruns**: `go test ./internal/scheduler/... -run
  TestEndToEnd_ThresholdCrossing_DispatchesOnce_ThenResolvesOnRecovery
  -count=5 -v` → real `5/5 PASS` (1.34s–3.35s each).
- **Reproduced under real concurrent load**: a plain `go test ./...` run
  against the real, live shared `docker-compose` Postgres (with
  `pulsewatch-backend-1`'s own live scheduler running against it
  concurrently) → real `--- FAIL:
  TestEndToEnd_ThresholdCrossing_DispatchesOnce_ThenResolvesOnRecovery
  (17.69s)` / `alert_lifecycle_test.go:279: condition not met within 15s`
  (waiting for the "opened" dispatch to be recorded).
- **Reproduced again under `go test -p 1 ./...`** (3 serialized runs): run
  1 failed the identical test at a *different* line —
  `alert_lifecycle_test.go:297: condition not met within 15s` (waiting for
  `state == "healthy"` after recovery), 17.60s; runs 2 and 3 passed. **This
  is the key new finding**: unlike R-002 (which `-p 1` has reliably avoided
  across two prior sessions), `-p 1` did **not** reliably avoid this flake —
  reported honestly per the task's own instruction to report the `-p 1`
  outcome either way. See R-007 in `10-risk-register.md` for the full
  mechanism discussion (real evidence points at contention with the live
  `pulsewatch-backend-1` container's own concurrent scheduler activity —
  something `-p 1` has no power over, since it only serializes `go test`'s
  own package binaries — rather than R-002's own cross-test-package
  contention mechanism).
- Logged as R-007, distinct from R-002, with an honest "not fully
  root-caused this session" — diagnosing the exact contention point is left
  to Session 14.

### Full-repo final verification
- `go build ./...`: clean (`BUILD_OK`).
- `go vet ./...`: clean (`VET_OK`).
- `gofmt -l .`: flags exactly `internal/operatorapi/router.go` and
  `main.go` — confirmed **pre-existing and unrelated to this session's
  diff** (`git diff` on both is empty/absent; `main.go`'s only line this
  session touched was reverted back to byte-identical). Root cause
  confirmed directly: both files are CRLF on disk (this developer's
  Windows git checkout, `core.autocrlf`), which `gofmt` running inside the
  Linux verification container flags purely on line endings, not real Go
  formatting — the committed blob content itself is unaffected. This
  session's own two files (`rollup.go`, `cadence_test.go`) are not
  flagged. Not fixed — reformatting unrelated files is out of this
  session's scope, noted here rather than silently claimed as "0 issues"
  the way Session 12's own note read.
- `golangci-lint run ./internal/rollup/...` (this session's actual
  changed package): `0 issues`.
- `go test ./...` (final full run against the real, live shared Postgres):
  every package passed except `internal/alerting`'s
  `TestRecordCheckResult_ThresholdCrossingOpensIncidentDispatch` — this is
  R-002's own already-tracked flake (confirmed by name and by the exact
  assertion it failed), not a regression from this session's changes; this
  session's own two changed/new packages (`internal/rollup`,
  `internal/scheduler`) both passed clean in this same run. Left alone, per
  the explicit ground rule not to fix R-002 this session. A fourth R-005
  recurrence (23 orphaned rows) resulted from this run and was cleaned up
  the same way as the other three (see "Real bugs found" #2).
- Confirmed `privacy-forge`, `laravel-consent-guard`, `bookslot`, and
  `lexicon` were not touched. `04-data-model.md`, `openapi.yaml`, the
  schema, ADR-0001–0004, the router split (Session 11), and TLS setup
  (Session 10) were not touched beyond what this session's actual root
  cause required (nothing — the fix is entirely inside `internal/rollup`).

## Open questions and risks

- **R-006 (opened this session):** rollup cadence gaps on this developer's
  own dev machine, root-caused to Docker Desktop's WSL2 VM idling during
  host inactivity, mitigated with visibility logging (not eliminable in
  code). Not applicable to a real always-on deployment, which doesn't exist
  yet (R-003's gate). See `10-risk-register.md`.
- **R-007 (opened this session):** the new scheduler flake, reproduced
  twice under real load including once under `-p 1` — notably *not* fully
  explained by R-002's own mechanism. Not root-caused to a specific query
  or lock this session. See `10-risk-register.md`.
- **R-005 (unchanged in substance, recurred three more times this
  session):** still fully open, still exactly the documented mechanism,
  still tracked as B-006.
- **R-002, R-003 (unchanged, carried forward, untouched this session per
  explicit scope):** see `10-risk-register.md`.
- **B-007 (opened this session):** no operator password-reset/rotation
  tool exists; today's only path is direct DB access, which this session
  had to use for real.
- **`/incidents` is still unbuilt** (B-002) — the last piece of
  `05-api-contracts.md`'s originally-specified read surface. With this
  session's work done, the SLO/rollup feature area (Session 12 + this
  session's correction) can genuinely be considered done.

## Next recommended session

- Proposed session title: **Session 14 — `GET /targets/{id}/incidents`,
  and/or R-005's cleanup-ordering fix (B-006), and/or R-007's deeper root
  cause.** All three are real, well-scoped candidates; `/incidents` is the
  most clearly-scoped feature work, R-005/R-007 are both testing-hygiene
  investigations that would benefit from being done together (a future
  session investigating R-007 should specifically try reproducing it with
  the live `pulsewatch-backend-1` container's scheduler stopped, which
  would also be a natural test of R-005's own already-confirmed
  mechanism).
  - Do **not** reopen ADR-0001–0004, and do not fold SLO-threshold alerting
    or configurable windows into `/incidents` work — those are B-004/B-005.
  - R-002's own flakes remain explicitly out of scope until a session picks
    them up directly.
- Inputs required: this handoff; `10-risk-register.md` (R-002, R-003,
  R-005, R-006, R-007); `11-backlog.md` (B-002, B-004, B-005, B-006, B-007).
- Expected deliverables: depends which item(s) a future session picks —
  see `11-backlog.md`/`10-risk-register.md`'s own per-item mitigation
  notes for what "done" looks like for each.
- Definition of done: whichever item(s) are picked up, verified with real
  evidence per this project's own established standard — not "looks
  right."

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Repository state:** branch `main`, unreleased (pre-v0.1.0). Everything
Session 12's own handoff listed as complete remains complete; this
session's real correction is that the SLO/rollup feature area is *now*
genuinely, fully verified end-to-end with a real (not mock) authenticated
capture, and the rollup job's apparent stall was investigated to a real
root cause (a dev-machine environment characteristic, not a code defect)
rather than left as an open question.

**Problem being solved:** unchanged — see `00-project-brief.md`. This
session did not add a feature; it closed out verification gaps in the
existing SLO/rollup feature area and logged a new testing-hygiene finding.

**Users:** Single operator (this developer) plus the machine "Agent" role — v1 has no other identity (`02-requirements.md`).

**Current stack:** unchanged from Session 12's own description, except:
- Backend: `internal/rollup/rollup.go` gained `cadenceGapExceeded` and its
  WARN-logging call site (no new dependency, no schema change).
- Testing: one new test file (`internal/rollup/cadence_test.go`, no DB
  needed).
- Infra: `docker-compose.yml` unchanged; `backend` was rebuilt/redeployed
  twice this session (once for the temporary observation build, once to
  restore the real config) — both via the project's existing `docker
  compose build`/`up -d` flow, no compose file changes.

**Architecture decisions that must not be reversed:** unchanged from
Session 12's own list — nothing in this session touched ADR-0001–0004, the
router split, TLS setup, or the schema.

**Implementation state:**
- Done: everything Session 12's handoff listed, **now with the real
  (non-mock) authenticated verification Session 12's own capture was
  missing**, and with the rollup job's apparent stall root-caused and
  addressed (visibility logging, not a behavior change) rather than left
  open.
- In progress: nothing mid-flight.
- Not started: `/incidents` (B-002), SLO-threshold alerting (B-004),
  configurable per-target windows (B-005), TLS for `backend`'s agent-facing
  listener (R-003), operator password-reset tooling (B-007, newly
  identified), R-005's broader test-cleanup fix (B-006), R-007's deeper
  root cause (newly identified).

**Constraints and non-goals:** unchanged — see `01-scope-and-non-goals.md`.

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning, Requirements Analysis, Verification & Testing, Retirement & Handover
**Baseline-depth, real rigor on its own merits:** this session's own real
rigor was almost entirely Verification & Testing depth — a real
root-cause investigation (cross-container log correlation, WSL2/Windows
event-log evidence) that overturned the task's own initial premise, a
real credential-reset-and-login verification cycle, and real,
honestly-reported flake reproduction (including a result — `-p 1` not
preventing R-007 — that complicated rather than confirmed the expected
narrative). See `00a-ledger-confirmation.md`.

**Task for this session (three-part, bounded) — now complete:**
Root-cause and address the rollup job's apparent stall; obtain a real,
non-mock authenticated `/slo` + dashboard capture; log the new scheduler
flake. **Done — see Work completed above, with the Part 1 correction
clearly stated.**

**Definition of done — met:**
- Rollup job's stall root-caused (a dev-machine environment
  characteristic, not a code defect) and given the right-sized fix
  (visibility logging), proven via a real extended-observation window
  (temporarily lowered `TickInterval`, 9 real consecutive ticks observed,
  restored afterward) — see Verification performed above.
- A real, non-mock authenticated `/slo` capture and dashboard render exist
  and are documented above, replacing Session 12's own mock evidence for
  this specific gap.
- The new scheduler flake is logged in the risk register (R-007) with an
  accurate mechanism discussion, including the honest, non-obvious result
  that `-p 1` did not prevent it (unlike R-002).
- `docs/project-memory/` updated: this file, `09-decision-log.md`,
  `10-risk-register.md`, `11-backlog.md`, `13-release-notes.md`.
- Local HEAD confirmed to match `origin/main` after commit/push — see the
  actual `git log`/`git status` proof pasted into this session's own
  closing message.

**Files to attach or paste for Session 14:**
- `10-risk-register.md` (R-002, R-003, R-005, R-006, R-007) and
  `11-backlog.md` (B-002, B-004, B-005, B-006, B-007) — next-step candidates
- `docs/architecture/openapi.yaml` (`Incident` schema,
  `/targets/{id}/incidents` path — already specified, unbuilt)
- `docs/project-memory/12-session-handoff.md` (this file)

**Ground rules:** Do not change the stack. Do not introduce a third new
technology. Do not expand the deep-SDLC-phase count beyond two. Do not
reopen ADR-0001–0004 without new measured evidence per their own Revisit
triggers. Do not touch `privacy-forge`, `laravel-consent-guard`, `bookslot`,
or `lexicon`. Do not fix R-002's own existing flakes without a session
scoped specifically for that.
