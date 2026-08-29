# SDLC Evidence Map

**Deep phases:** 6. Release & Deployment · 7. Operations & Maintenance
**Baseline phases:** 3. Architecture & Design · 4. Implementation
**Intentionally light:** 1. Discovery & Planning, 2. Requirements Analysis,
5. Verification & Testing, 8. Retirement & Handover — because Discovery &
Planning and Verification & Testing are already demonstrated deeply in
`lexicon`, and Requirements Analysis and Retirement/Handover & Disposal are
already demonstrated deeply in `privacy-forge`. Per Rule D2 (exactly two
deep phases), this repository does not duplicate that depth — see
`docs/project-memory/00a-ledger-confirmation.md` for the full rationale.

| Phase | Depth | Evidence | Location |
|---|---|---|---|
| 1. Discovery & Planning | light | Problem framing grounded in real dogfooding (this developer's own portfolio deployments, not a hypothetical persona); build-vs-alternatives comparison (hosted SaaS / self-hosted Grafana+Prometheus / purpose-built); continuous-operation requirements (restart safety, overlapping-run prevention, retention/rollup, rolling-window SLO correctness) and the resulting plain-Postgres-vs-TimescaleDB decision; MVP boundary and non-goals table; 5 success metrics specific to continuous operation. Deliberately baseline-depth documentation (per Rule D2 — this repo's two deep phases are 6 and 7, not this one) — see `00a-ledger-confirmation.md`. | `docs/project-memory/00-project-brief.md`, `docs/project-memory/00b-build-vs-alternatives.md`, `docs/project-memory/01-scope-and-non-goals.md` |
| 2. Requirements Analysis | light | | |
| 3. Architecture & Design | baseline | | |
| 4. Implementation | baseline | | |
| 5. Verification & Testing | light | | |
| 6. Release & Deployment | **deep** | | |
| 7. Operations & Maintenance | **deep** | | |
| 8. Retirement & Handover | light | | |

## Why some phases are light

To be filled in as each phase's session lands, per
`docs/project-memory/00a-ledger-confirmation.md`.
