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
| 2. Requirements Analysis | light | 10 user stories with Given/When/Then acceptance criteria covering every core continuous-operation flow (register HTTP/TCP check, scheduled check execution, rolling-window SLO evaluation with the pulsewatch-downtime "unknown" carve-out, threshold-breach alerting, no-alert-on-blip, both restart-safety properties, dashboard viewing, private-target monitoring via the agent); 23 functional requirements and 12 non-functional requirements with numeric targets specific to continuous operation (alert latency ≤5s, restart recovery ≤10s, 0 duplicate alerts across restart, rollup job timing, agent staleness); single-operator + machine-agent roles matrix sized to actual v1 scope (no ABAC); data classification covering target topology, alert-channel credentials, check results, and agent/operator credentials; the private-target-reachability integration requirement made concrete (agent-initiated outbound-only reporting, agent-staleness-vs-target-down distinction) rather than left as Discovery-phase justification. Deliberately baseline-depth (per Rule D2 — this repo's two deep phases are 6 and 7; Requirements is demonstrated deeply in `privacy-forge`, not duplicated here). | `docs/project-memory/02-requirements.md` |
| 3. Architecture & Design | baseline | | |
| 4. Implementation | baseline | | |
| 5. Verification & Testing | light | | |
| 6. Release & Deployment | **deep** | | |
| 7. Operations & Maintenance | **deep** | | |
| 8. Retirement & Handover | light | | |

## Why some phases are light

To be filled in as each phase's session lands, per
`docs/project-memory/00a-ledger-confirmation.md`.
