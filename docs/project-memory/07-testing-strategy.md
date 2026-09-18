# Testing Strategy
> Purpose: what we test, at which level, and why that is sufficient
> Project: pulsewatch (public)
> Last updated: 2026-09-17

## Testing philosophy for this project

This project's own recurring standard, stated across `09-decision-log.md`
and `12-session-handoff.md`, is **real evidence over "looks right"**: a
feature isn't done until it's exercised against the real schema, the real
HTTP surface, or (for deployment work) a real cluster — not a mock standing
in for one. That standard shapes every level below:

- **Backend tests run against a real Postgres**, not an in-memory
  substitute or a mocked `pgxpool.Pool` — every `*_test.go` package has its
  own `testPool(t)` helper (e.g. `backend/internal/scheduler/testdb_test.go`)
  that connects to a live database and skips (never fails) if one isn't
  reachable, so the same test genuinely proves something in CI (where
  Postgres is a real service container) and degrades gracefully on a
  laptop with no database running.
- **HTTP handlers are tested through the real router**, via
  `net/http/httptest`, not by calling handler functions directly — a test
  goes through the same `gin.Engine`, the same middleware chain (auth,
  CSRF, rate limiting), and the same JSON marshaling a real client would.
- **Fixtures create real rows through real code paths where one exists**
  (e.g. `agentauth.CreateAgent`, not a hand-rolled `INSERT` with a fake
  token hash; `operatorauth.CreateOperator`, not a bypassed password check)
  — a test fixture that diverges from the real provisioning path can hide
  a bug in that path itself.
- **Concurrency and timing properties are tested with real goroutines and
  a real clock**, not simulated — e.g. `scheduler`'s restart-safety and
  overlap-prevention tests (ADR-0001, ADR-0004) run the actual worker pool
  against the actual leasing SQL, not a model of it.

This buys confidence at the cost of speed: the full backend suite takes
tens of seconds, not milliseconds, and needs a running Postgres to mean
anything (see "Quality gates in CI"). That trade was made deliberately —
see `00-project-brief.md`/`00a-ledger-confirmation.md` for why this
project prioritizes proof over velocity.

## Levels

| Level | Tool | Scope | Gate |
|---|---|---|---|
| Backend unit/integration | Go `testing` + `net/http/httptest` + a real Postgres | Every `internal/*` package (`agentapi`, `agentauth`, `alerting`, `emailprovider`, `operatorapi`, `operatorauth`, `pushprovider`, `rollup`, `scheduler`) plus `cmd/agent`, `cmd/provision-operator`, `cmd/seed-agent`, and the root package's own router-split tests — 52 test files across 13 packages as of this writing. | `go test ./... -race -p 1` in CI (backend job); real bugs found this way include two agent-input integer-conversion boundary bugs (`09-decision-log.md`). |
| Frontend unit | Vitest, Node environment (no DOM/browser) | SvelteKit `load`/form-action logic and pure helper functions (`src/routes/page.test.ts`, `src/routes/dashboard/devices/{deviceStatus,page.server}.test.ts`) — **not** component rendering or DOM interaction, since `vitest.config.ts` sets `environment: 'node'`. | `npm test` in CI (frontend job). |
| Static analysis (Go) | `go vet`, `golangci-lint` (pinned `v2.13.2`) | Whole-module correctness/style linting. | CI `backend` job; zero issues required. |
| Static analysis (frontend) | ESLint + Prettier (`npm run lint`), `svelte-check` | Whole-frontend lint/format/type-checking. | CI `frontend` job (`svelte-check` is available via `npm run check` but is not itself a separate CI step — see "Known gaps" below). |
| Static analysis (security) | CodeQL (`go`, `javascript-typescript`), gitleaks | Whole repo, every push/PR. | CI `codeql`/`gitleaks` jobs — see `06-security-threat-model.md`'s "Dependency and supply-chain controls." |
| Dependency vulnerability scanning | `govulncheck`, `npm audit --omit=dev` | Go module graph / frontend production dependencies. | CI `backend`/`frontend` jobs. |
| Migration reversibility | `golang-migrate` CLI | Every migration applies, reverses (`down -all`), and re-applies cleanly against a fresh Postgres. | CI `backend` job's dedicated step. |
| Infrastructure (offline) | `terraform fmt -check`, `kubectl kustomize` | HCL formatting; a real Kustomize base+overlay build (image substitution, ConfigMap generation) with no live cluster contacted. | CI `iac-and-k8s-manifests-validate` job. |
| Infrastructure (real cluster) | Manual, session-driven (not CI) | `terraform apply`/`kubectl apply` against a real control plane — done once, Session 15 (B-009), in a sandbox with the right egress access; not repeatable in ordinary CI. | Not gated in CI — see R-008 (`10-risk-register.md`) for exactly what remains cluster-unverified (Terraform provider registry access, NetworkPolicy enforcement on a real CNI, two container images). |
| OTel Collector config | `otelcol-contrib ... validate` (Docker) | `otel-collector-config.yaml` syntactic validity. | CI `otel-collector-config` job. |

## Security testing

Covered in full in `06-security-threat-model.md` (Session 21's own pass at
B-012, alongside this file) — not duplicated here beyond a pointer:

- Automated: `govulncheck`, `npm audit`, gitleaks, CodeQL — see that file's
  "Dependency and supply-chain controls" section for exactly what each
  catches and doesn't.
- Behavioral: the login rate limiter, CSRF mitigation, session-forgery
  resistance, and the operator/agent router split (closing R-004) are all
  covered by real tests exercising the real HTTP surface — see that file's
  STRIDE table for which test file backs which threat.
- **Not done:** no dedicated penetration test or fuzzing harness exists for
  either the operator- or agent-facing HTTP surface. `go test` boundary
  tests (e.g. the integer-conversion fixes referenced above) have caught
  real bugs of the kind fuzzing typically finds, but that's incidental
  coverage from correctness tests, not a fuzz corpus (`go-fuzz`/`testing/
  quick`/`testing.F` are not used anywhere in this repo).

## Accessibility testing

**No accessibility testing exists** — no automated tooling (no `axe-core`,
no `eslint-plugin-jsx-a11y`-equivalent for Svelte, no Lighthouse CI) and no
manual accessibility review has been recorded in any session's own
decision log or handoff. This is a real, honest gap, not a deliberate
scoping decision documented anywhere in `01-scope-and-non-goals.md` — it
was simply never picked up. Given the dashboard's actual audience (the one
self-hosting operator, per `01-scope-and-non-goals.md`'s single-operator
model) this has not blocked any session, but it should not be read as
"accessibility doesn't matter here" — it means "nobody has verified it
either way."

## Performance testing and budgets

`02-requirements.md` states twelve numeric NFRs (`NFR-001` through
`NFR-012`) — scheduling jitter, alert latency, restart recovery time,
rollup-job duration, dashboard read latency, and several fixed defaults.
Their actual test coverage is uneven, and it matters to be precise about
which is which rather than imply blanket "NFRs are verified":

- **Functionally verified, not numerically measured in CI:** NFR-001
  (scheduling jitter), NFR-003 (restart recovery), NFR-004 (restart
  correctness), NFR-007 (concurrency safety) — `internal/scheduler`'s test
  suite (`alert_lifecycle_test.go`, `shutdown_test.go`, and others) proves
  the *behavior* these NFRs describe (no overlapping checks, no duplicate
  alert across a restart, a due check does eventually run) using real
  goroutines and a real Postgres, but does not assert against the specific
  numeric bounds (±2s jitter, ≤10s restart, etc.) as a pass/fail gate — a
  regression that made recovery take 30s instead of ≤10s would not fail
  `go test`.
- **Measured, at least once, outside the automated suite:** NFR-005
  (rollup cadence) — R-006 (`10-risk-register.md`) documents a real,
  live-container measurement (11 consecutive ticks at ~20s cadence,
  timestamped) proving the cadence-gap warning logic works, done as a
  manual session investigation, not as a repeatable CI benchmark.
- **Not measured at all:** NFR-002 (alert latency ≤5s), NFR-006 (retention
  correctness timing), NFR-009 (dashboard read ≤500ms p95). No
  `testing.B` benchmark exists anywhere in this repo (`grep -r func
  Benchmark` across `backend/` returns nothing), and no load-testing tool
  (`k6`, `vegeta`, `hey`) is wired into CI or documented as run manually.
  `internal/operatorapi`'s SLO/status handler tests (`slo_test.go`,
  `status_test.go`) assert correctness of the returned numbers, never
  their latency.
- **No performance budget is enforced in CI** for either backend or
  frontend (no bundle-size check, no response-time assertion). This is
  consistent with the project's own technology-freeze discipline (no new
  benchmarking/load-testing dependency has been added, `01-scope-and-non-
  goals.md`) but is a real gap relative to the numeric NFR table's own
  stated bounds — a future session should not claim any of the three
  "not measured at all" NFRs as verified without adding real measurement
  first.

## Test data strategy (synthetic only)

- **No production or real third-party data is used anywhere in tests.**
  Every fixture inserts synthetic rows through real schema/real code paths
  (see "Testing philosophy" above) with deliberately fake-but-valid-shaped
  values.
- **Destination hostnames use the reserved `.invalid` TLD**
  (`http://example.invalid/...`, `oncall+...@example.invalid`) per RFC
  2606 — guaranteed to never resolve, so a test bug that accidentally let
  a real HTTP/SMTP request escape a mock would fail loudly (DNS failure)
  rather than silently reach a real third party.
- **External services are always faked in-process, never called for
  real:** `httptest.Server` stands in for webhook receivers and mock
  FCM/APNs/SMTP endpoints (`alerting/pushdispatch_test.go`'s `mockFCM`,
  `emailprovider`'s own tests) — B-016 (real FCM/APNs delivery with live
  credentials) and the equivalent real-email-delivery follow-up are
  explicitly named, permanently- or currently-descoped exceptions
  (`11-backlog.md`), not something any existing automated test attempts.
- **Encryption/signing keys used in tests are fixed, non-secret, and
  committed in plain sight** (`testEncryptionKey`, `testSessionSecret` —
  e.g. `backend/internal/alerting/testdb_test.go`,
  `backend/internal/operatorapi/testdb_test.go`) — deliberately identical
  across every package's own test suite, since `alert_channels` is a
  global table every package's tests share against the same test
  Postgres, and two different random keys would fail to decrypt each
  other's rows.
- **Every test cleans up its own fixture rows** via `t.Cleanup`, in
  dependency order (child rows before the parent row they reference) —
  see B-006 (`11-backlog.md`, closed this session) for the specific
  cleanup-ordering and context-lifetime bugs found and fixed, and why this
  matters even though the test database itself is disposable in CI: a
  developer's own persistent local Postgres is not.
- **Test emails/identifiers are namespaced by test name**
  (`test-agents-crud@example.invalid`, `test-status-stale@example.invalid`,
  etc.) rather than reused across tests, so two tests' fixtures never
  collide even though `alert_channels`/`agents`/`operators` are global
  tables with no per-test schema isolation.

## Quality gates in CI

`.github/workflows/ci.yml` runs seven independent jobs on every push/PR to
`main`, none of which is currently marked as a required-status-check in
this repo's own branch-protection configuration (not itself part of this
repo's files, so not verifiable from the codebase alone — see
`08-deployment-and-operations.md` for anything more specific already
recorded there):

1. **`backend`** — `go vet`, `golangci-lint` (pinned `v2.13.2`), migration
   apply, `go test ./... -race -v -p 1` against a real `postgres:16-alpine`
   service container, `govulncheck`, and a migration up/down/up
   reversibility check.
2. **`frontend`** — `npm ci`, ESLint + Prettier, `npm run build`, `npm
   test`, `npm audit --omit=dev`.
3. **`otel-collector-config`** — offline config validation via the real
   `otelcol-contrib` binary.
4. **`gitleaks`** — full-history secret scanning.
5. **`codeql`** — static analysis, `go` and `javascript-typescript`.
6. **`iac-and-k8s-manifests-validate`** — `terraform fmt -check`, a real
   offline `kubectl kustomize` build of the production overlay.
7. **`publish-images`** — gated on jobs 1 and 2 passing, and on `push` to
   `main` only (never a PR branch) — builds and pushes real images to
   `ghcr.io`.

`-p 1` on the backend `go test` step (serializing package test binaries)
is itself a quality-gate decision with its own paper trail, not an
arbitrary flag: R-002 (`10-risk-register.md`) documents cross-package
Postgres contention causing real, reproduced CI flakes without it, across
three separate sessions, before it became CI's permanent default.
