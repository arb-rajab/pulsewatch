# Session 0 — Ledger Confirmation

> Purpose: freeze the technology allocation for this repository before any
> architecture work begins, per Portfolio Governance Rule D1 ("ledger before
> architecture").
> Last updated: 2026-08-29

## Ledger row (from master Framework Allocation Ledger)

| Field | Value |
|---|---|
| Repository | `pulsewatch` (R03) |
| Domain | Uptime / SLO / alerting — infrastructure and service monitoring |
| Platform | Web app + lightweight agent |
| Primary frontend | SvelteKit |
| Primary backend | Go 1.25 (Gin) |
| Primary mobile/desktop | — (agent is a Go binary, not a separate framework layer) |
| Language(s) | Go, TypeScript |
| Key data/infra | PostgreSQL, Redis, OpenTelemetry Collector |
| New learning objective | Go concurrency patterns (worker pools, context cancellation, graceful shutdown); OpenTelemetry collector pipelines |
| SDLC phases (deep) | 6. Release & Deployment · 7. Operations & Maintenance |
| Overlap status | `UNIQUE` |

## Overlap check

- Go (Gin) as primary **backend**: not used as primary backend by any other
  flagship repository — `privacy-forge` uses Laravel, `lexicon` uses
  FastAPI. ✅ No collision.
- SvelteKit as primary **frontend**: not used as primary frontend by any
  other flagship repository — `privacy-forge` uses Vue 3 (via Inertia),
  `lexicon` uses Next.js. ✅ No collision.
- No mobile/desktop framework claimed. The monitoring agent is a Go binary
  reusing the backend's language, not a distinct framework allocation, so it
  does not need its own ledger row. N/A.

**Result: PASS.** This repository may proceed past Session 2 (per Rule D1, the
gate is actually "before Architecture," i.e. before Session 3 — recorded here
so Session 3 does not need to re-verify).

## Learning budget check (Rule D3 — max 2 new technologies)

| New technology | Genuinely new? | Counts against budget |
|---|---|---|
| Go concurrency patterns (worker pools, context cancellation, graceful shutdown) | Yes — first from-scratch concurrent-worker-pool implementation in the portfolio; the agent and server both need to poll/collect many targets in parallel and shut down cleanly, which is the actual reason this pattern is load-bearing here rather than decorative | 1 |
| OpenTelemetry collector pipelines | Yes — first OTel collector integration in the portfolio; used to carry agent → server telemetry, not as an add-on | 2 |
| Go, Gin, SvelteKit, PostgreSQL, Redis, Docker Compose | No — the language/framework pairing is new to *this repository's ledger row*, but general REST-service, SPA, relational-database, and cache-layer engineering are established skills being applied to a new domain, not learned for the first time | 0 |

**Result: PASS.** Exactly 2 new technologies — at budget, not over.

## Deep SDLC phase check (Rule D2 — exactly two)

1. **Release & Deployment** — chosen because this system cannot simply go
   quiet during a deploy the way a request/response API can: it must keep
   monitoring (or fail predictably and visibly) through its own releases,
   handle agent/server version skew during a rolling upgrade, and run
   migrations against a live monitoring database without losing in-flight
   check history. That is real release engineering, not a one-line deploy
   script.
2. **Operations & Maintenance** — chosen because this is the portfolio's
   first genuinely continuous, operated system: correctness here is partly
   about behaviour across hours and days, not just within a single request.
   Runbooks, backup/restore, capacity planning, and "what happens when the
   thing that watches everything else goes down" are the actual subject
   matter of this repository, not an afterthought bolted onto a
   request/response product.

No third deep phase is claimed. Architecture, Requirements, Security,
Implementation, and Testing are baseline; Discovery and Retirement are
intentionally light (reasons to be recorded in `docs/SDLC-EVIDENCE.md` once
the deep-phase work lands). Requirements Analysis and Retirement/Handover are
already demonstrated deeply elsewhere in the portfolio (`privacy-forge`), and
Discovery & Planning and Verification & Testing are demonstrated deeply in
`lexicon` — this repository deliberately does not duplicate that depth.

## Ship-ability check (Rule D4)

Estimated time to credible v1: within the ≤120-hour guideline (full estimate
to be recorded against the flagship specification in Session 1, once the MVP
boundary — how many check types, how much of the alerting/SLO surface ships
in v1 — is sized).

## Governance sign-off

- [x] Ledger row confirmed against master register
- [x] Zero collisions
- [x] Learning budget ≤ 2 confirmed
- [x] Exactly two deep SDLC phases chosen
- [ ] Ship-ability check passed — full estimate deferred to Session 1
- [x] Repository added to Status Board under **Now** (see portfolio
      governance repo — action: add this row manually to
      `portfolio/STATUS.md`)

**This file is not modified again.** It is the frozen record Session 3 checks
before starting architecture work.
