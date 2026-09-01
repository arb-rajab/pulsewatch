# Backlog
> Purpose: everything deliberately not being done right now
> Project: pulsewatch (public)
> Last updated: 2026-09-01 (Session 13)

## Next up
| ID | Item | Type | Size | Why now |
|---|---|---|---|---|
| B-002 | `GET /targets/{id}/incidents` | Feature | Small | Simple direct `incidents` read, no new computation — same pattern as Session 9's `/status` endpoint and Session 12's `/slo` endpoint |
| B-007 | Operator password reset/rotation path (a real endpoint or CLI, not raw `psql`) | Feature / Ops | Small-Medium | `provision-operator`'s own doc comment already named this gap; Session 13 had to exercise the only documented break-glass path (a throwaway Go program computing a bcrypt hash and writing it directly via `psql`) to get a real login for `05-api-contracts.md`'s own verification standard. A single-operator project with no recovery path if that operator forgets their password is a real operational gap, not just a nice-to-have. |
| B-004 | SLO-threshold alerting (e.g. "alert if uptime drops below X%") | Feature | Medium | Explicitly named out of scope for Session 12 — a new alert-machine capability, belongs in its own session with its own ADR-adjacent design, not folded into the read-only `/slo` endpoint |
| B-005 | Configurable/custom rolling windows per target | Feature | Small-Medium | Explicitly named out of scope for Session 12, which kept `/slo`'s existing request-time `window_days`/`slo_target_pct` parameters (already in `openapi.yaml`) but added no *stored per-target* window configuration — a real future feature if an operator ever wants a target's dashboard uptime figure to default to something other than the shared 30-day/99.9% default |
| B-006 | Fix test-fixture cleanup ordering across `scheduler`/`agentapi`/`alerting`/`operatorapi` (pre-Session-12 tests) so `DELETE FROM targets` in `t.Cleanup` doesn't silently fail when `check_results`/`incidents` rows still reference the fixture | Testing / hygiene | Small | R-005 (`10-risk-register.md`, opened Session 12): confirmed to leave real orphaned rows in a developer's persistent local Postgres on every full `go test ./...` run; Session 12 fixed its own two new call sites but left the broader, pre-existing gap alone as out of scope |
| B-008 | Server-side session revocation on `POST /auth/logout` (today it only clears the browser's cookie via `Set-Cookie: Max-Age=0`; the token itself, if replayed directly, stays valid until its natural expiry) | Security / Ops | Small-Medium | Found incidentally during the 2026-09-01 evidence re-verification pass while trying to invalidate a captured session cookie before committing evidence to this public repo: `POST /auth/logout` returned `204`, but the same token replayed against `GET /api/v1/targets` still returned `200`. No `Revoke`/`Invalidate` function exists in `internal/operatorauth`. Low severity today (no real remote deployment exists yet — R-003's gate; a captured token only matters to someone who already has access to this dev machine), but worth closing before R-003 is closed. |

## Later

## Explicitly rejected (with reasons)
| ID | Item | Reason |
|---|---|---|
| — | Retention job's daily rollup tier | `04-data-model.md`'s own reasoning: 400 days × 24 buckets/target is trivially small for Postgres to scan directly; a second tier would be premature machinery. Revisit only against measured evidence, not by default. |

## Done (kept for traceability, not re-litigated)
| ID | Item | Closed |
|---|---|---|
| B-001 | `GET /targets/{id}/slo` + FR-008's hourly rollup job | Session 12 — see `04-data-model.md`'s Session 12 addendum and `12-session-handoff.md` |
| B-003 | Real TLS/reverse-proxy story for `docker-compose.yml` | Session 10 (R-001) |
