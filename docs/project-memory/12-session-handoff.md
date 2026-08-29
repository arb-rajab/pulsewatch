# Session Handoff

## Project
- Repository: `pulsewatch`
- Public or private: public (flagship)
- Product/domain: Self-hosted uptime, SLO, and alerting service with a lightweight agent
- Current version or branch: `main` (unreleased, pre-v0.1.0)

## Session completed
- Session number and title: **Session 0 — Portfolio Governance & Technology Allocation**
- Objective: Confirm the ledger row, learning budget, and non-goals before any architecture work begins.
- Status: **complete**

## Work completed
- Confirmed framework allocation: **Go 1.25 + Gin (backend) + SvelteKit (frontend)** — verified `UNIQUE`, zero collisions against the master ledger (`privacy-forge` uses Laravel/Vue, `lexicon` uses FastAPI/Next.js).
- Confirmed learning budget: exactly 2 new technologies (Go concurrency patterns — worker pools, context cancellation, graceful shutdown; OpenTelemetry collector pipelines) — at cap, not over.
- Confirmed the two deep SDLC phases for this repo: **Release & Deployment** and **Operations & Maintenance** — chosen because pulsewatch is the portfolio's first genuinely continuous, operated system (correctness is partly about behaviour across hours/days, not just within a single request), unlike every other flagship which is request/response.
- Confirmed ship-ability estimate is within the ≤120-hour guideline; the full hour estimate is deferred to Session 1 pending MVP sizing.
- Created repository skeleton: directory structure, licence (AGPL-3.0), `SECURITY.md`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `CHANGELOG.md`, `README.md` (status: skeleton with non-goals preview).
- Scaffolded the full 15-file Project Memory Pack under `docs/project-memory/`, plus the separate `00a-ledger-confirmation.md`.
- Wrote a draft `00-project-brief.md` (marked STUB — to be validated and finalised in Session 1), explicitly framing the problem around continuous operation rather than a request/response product.
- Built a minimal, real Go + Gin backend (single `/health` endpoint, passing test, `golangci-lint` configured and clean) and a minimal, real SvelteKit frontend (placeholder page, `/health` route, passing `vitest` test, `eslint` clean).
- Wrote `docker-compose.yml` (PostgreSQL, Redis, backend, frontend) with working health checks on all four services — booted and verified locally.
- Wrote a **real** CI pipeline from the first commit (`golangci-lint`, `go vet`, `go test -race`, `govulncheck` for the backend; `eslint`, build, `vitest` for the frontend; `gitleaks`; CodeQL for Go + JS/TS) — learning from `privacy-forge`'s Session 0 mistake (placeholder CI that needed rework) and `lexicon`'s correct Session 0 (real CI immediately).
- Added GitHub issue templates (bug, feature, security) and a PR template.
- Initialised git, verified `.gitignore` excludes the local `.claude/` tooling directory (confirmed present in this environment before writing the ignore rules) plus standard Go/Node/Docker/IDE/OS artifacts, and made the first commit.

## Files created or changed
- `docs/project-memory/00a-ledger-confirmation.md` — frozen governance record; Session 3 checks this before starting architecture work.
- `docs/project-memory/00-project-brief.md` — draft brief; **will be rewritten, not appended to, in Session 1**.
- `docs/project-memory/01-scope-and-non-goals.md` through `14-maintenance-and-retirement.md` — empty templates from the standard scaffold, ready for their respective sessions.
- `docs/SDLC-EVIDENCE.md` — scaffolded with Phases 6 and 7 declared deep; populated with real evidence links as those sessions land.
- `README.md` — skeleton with status banner and non-goals preview.
- `LICENSE` — AGPL-3.0 (rationale: hostable application, not a library — recorded so this isn't silently changed later).
- `SECURITY.md`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `CHANGELOG.md` — standard governance docs.
- `.github/workflows/ci.yml` — real pipeline (not a placeholder): backend job (golangci-lint, go vet, go test -race, govulncheck), frontend job (eslint, build, vitest, npm audit), gitleaks, CodeQL (go + javascript-typescript).
- `.github/PULL_REQUEST_TEMPLATE.md`, `.github/ISSUE_TEMPLATE/*.yml` — contribution scaffolding.
- `backend/` — `go.mod`, `go.sum`, `main.go` (Gin, `/health`), `main_test.go`, `.golangci.yml`, `Dockerfile`, `.dockerignore`.
- `frontend/` — SvelteKit skeleton (`package.json`, `svelte.config.js`, `vite.config.ts`, `src/app.html`, `src/routes/+page.svelte`, `src/routes/health/+server.ts`, `src/routes/page.test.ts`, `eslint.config.js`), `Dockerfile`, `.dockerignore`.
- `docker-compose.yml` — PostgreSQL, Redis, backend, frontend, with health checks.
- `.env.example` — Session-0 skeleton defaults only, no real secrets.
- `.gitignore` — comprehensive: `.env`, Go build artifacts, Node/SvelteKit build artifacts (`node_modules/`, `.svelte-kit/`, `build/`), `.claude/` (confirmed present locally), `.vscode/`, `.idea/`, OS files.

## Decisions made
- **Licence: AGPL-3.0**, not MIT — because this is a hostable application (per the portfolio rule: MIT for libraries/tools, AGPL for hostable apps). Should not be silently changed without a recorded reason.
- **Framework allocation is frozen** at Go 1.25 + Gin (backend) + SvelteKit (frontend). Must not be silently reversed — doing so would require reopening the entire ledger and re-checking all flagship rows for new collisions.
- **Exactly two deep SDLC phases** (Release & Deployment, Operations & Maintenance) are committed. A third should not quietly creep in during later sessions (Rule D2). Requirements Analysis and Retirement/Handover are already deep elsewhere (`privacy-forge`); Discovery and Verification & Testing are already deep in `lexicon` — this repo deliberately does not duplicate that depth.
- **CI is real from the first commit**, not a placeholder — an explicit lesson carried over from `privacy-forge`'s Session 0 (which shipped a placeholder and had to redo it) and confirmed working by `lexicon`'s Session 0.
- Gin's latest release (v1.12.0) requires Go ≥1.25, so the module targets **Go 1.25** rather than 1.23 — discovered and corrected during Session 0 verification, not assumed.
- Docker Compose host ports (5435/6382/8020/3003) are deliberately offset from `privacy-forge` (5432/6379/8000/5173) and `lexicon` (5433/6380/8010/3001, plus a `-prod` variant on 5434/6381/8011/3002) so all flagships can run concurrently on this machine without collision — 3002 was tried first but was already taken by `lexicon-prod-frontend-1`, discovered and corrected during verification.
- No formal ADR yet — ADRs begin at Session 3. This session's decisions are governance decisions, not architecture decisions, and are recorded here and in `00a-ledger-confirmation.md` instead.

## Validation performed
- `go vet ./...` and `go test ./... -race -v` — passed (inside a `golang:1.25-alpine` container, since Go is not installed on the host).
- `golangci-lint run` — initially found 2 real issues (unchecked `r.Run` error in `errcheck`; missing package comment in `revive`); both fixed, then re-verified clean.
- `npm run build`, `npm test` (vitest), `npm run lint` (eslint) — all passed on the host (Node is installed locally).
- `npm audit --omit=dev` — 0 vulnerabilities (the CI gate's exact command). Full `npm audit` (including dev deps) has 3 low-severity transitive findings from `@sveltejs/kit`'s own `cookie` dependency with no safe fix available yet; accepted for a Session-0 skeleton since the CI gate is clean.
- `docker compose up --build` — all four services (postgres, redis, backend, frontend) reached a healthy state; backend `/health` and frontend `/health` both returned 200.
- `git status` immediately after the first commit — clean (verified, not assumed); `git status --ignored` confirmed `.claude/`, `node_modules/`, `.svelte-kit/`, and `build/` are correctly excluded.
- Pushed to `origin/main` and confirmed the GitHub Actions CI run for the first commit is green.

## Open questions and risks
- **Open question:** how much of the alerting/SLO surface ships in v1 vs. backlog — needs a decision in Session 1 alongside the failure-cost analysis that will size the MVP boundary.
- **Risk:** Go concurrency patterns and OTel collector pipelines are both genuinely new; timebox a learning spike before Session 3 architecture work if either proves a bigger lift than expected.
- **Risk:** treating this like another request/response API and under-investing in the "runs continuously, must self-heal and be operable" concerns that are this repo's actual point. Session 1 must frame Discovery around continuous operation and real infrastructure monitoring, not a CRUD feature list.
- **Risk (portfolio-level, not repo-level):** this repo occupies a "Now" slot per the WIP-limit governance rule — confirm with the Status Board owner that no other public-track repo is concurrently active.
- **No blockers.** Session 1 can start immediately.

## Next recommended session
- Proposed session title: **Session 1 — Project Discovery & Business Framing**
- Single objective: Validate (or revise) every assumption in the draft brief with real reasoning, and produce the finalised `00-project-brief.md` plus `01-scope-and-non-goals.md` — **specifically framing the problem around continuous operation and real infrastructure monitoring, not a request/response product like this portfolio's other flagships.**
- Inputs required: this handoff; `00a-ledger-confirmation.md`; the draft `00-project-brief.md`.
- Expected deliverables: finalised project brief (no "draft" markers remaining); scope and non-goals document; 5 concrete success metrics; explicit MVP boundary.
- Definition of done: Gate 1→2 checklist satisfied (problem statement, target users, stakeholders, assumptions, risks, feasibility note, success metrics, MVP boundary, non-goals — all written and no longer marked draft, and explicitly reasoned in terms of a continuously-operated system rather than a single request/response cycle).

## Paste-into-new-session context

**Project:** pulsewatch — self-hosted uptime, SLO, and alerting service with a lightweight agent
**Track:** public flagship
**Repository state:** branch `main`, unreleased (pre-v0.1.0), Session 0 complete

**Problem being solved:** Small engineering teams either bolt together uptime checks from disconnected tools or over-adopt a heavyweight observability platform, with no honest, continuously maintained answer to "is this service meeting its availability target, and who gets told the moment it isn't." Unlike this portfolio's other flagships, correctness here is about the system continuing to tell the truth over hours and days, not a single request's output.

**Users:** Primary — an engineer/small SRE-adjacent team monitoring a bounded set of services. Secondary — the on-call engineer who receives and acts on an alert.

**Current stack:**
- Frontend: SvelteKit
- Backend: Go 1.25 (Gin)
- Data: PostgreSQL, Redis
- Infra: Docker Compose, GitHub Actions, OpenTelemetry Collector (planned)
- Testing: Go's `testing` package, Vitest

**Architecture decisions that must not be reversed:**
- Licence is AGPL-3.0 (hostable app, not a library).
- Primary frontend/backend framework pair is fixed (Go + Gin, SvelteKit) — frozen against the portfolio-wide framework allocation ledger; changing it requires reopening ledger governance, not just a local decision.
- Exactly two deep SDLC phases for this repo: Release & Deployment, Operations & Maintenance. Do not let a third phase creep in — Requirements Analysis and Retirement/Handover are deliberately baseline here since they're deep elsewhere in the portfolio (`privacy-forge`), and Discovery/Verification & Testing are deep in `lexicon`.

**Implementation state:**
- Done: repository skeleton, licence, governance docs, empty Project Memory Pack, draft (unvalidated) project brief, minimal real Go/Gin backend and SvelteKit frontend with passing tests and clean lint, real CI, working `docker compose up` with health checks.
- In progress: nothing mid-flight.
- Not started: everything product-related — no monitoring logic, agent code, or alerting exists yet.

**Constraints and non-goals:**
- Max 2 new technologies for this repo (Go concurrency patterns, OpenTelemetry collector pipelines) — already at that cap; do not introduce a third new technology.
- Explicit non-goals to be finalised in Session 1 but already anticipated (see README): APM/distributed tracing of user applications, log aggregation as a product, on-call scheduling/paging escalation trees, synthetic browser monitoring, multi-tenant SaaS billing.

**Deep SDLC phases for this repo:** Release & Deployment, Operations & Maintenance
**Intentionally light phases:** Discovery & Planning and Verification & Testing (deep in `lexicon`), Requirements Analysis and Retirement & Handover (deep in `privacy-forge`)

**Task for this session (single objective):**
Conduct project discovery: validate the draft problem statement, users, and assumptions; identify real risks and success metrics; define the MVP boundary and non-goals — explicitly reasoning about this as a continuously-operated system, not a request/response product.

**Definition of done:**
- `00-project-brief.md` rewritten with no "draft" markers, every section validated with actual reasoning (not assumed).
- `01-scope-and-non-goals.md` produced with an explicit non-goals table (reason for exclusion + condition that would reconsider it).
- 5 concrete, checkable success metrics defined.
- MVP boundary stated as a bullet list a reviewer could tick off.

**Files to attach or paste:**
- `docs/project-memory/00-project-brief.md` (current draft)
- `docs/project-memory/00a-ledger-confirmation.md`
- `docs/project-memory/12-session-handoff.md` (this file)

**Ground rules:** Do not change the stack. Do not introduce a third new technology. Do not expand the deep-SDLC-phase count beyond two. Ask before introducing any new dependency or scope item not already anticipated above.
