# Backlog
> Purpose: everything deliberately not being done right now
> Project: pulsewatch (public)
> Last updated: 2026-08-29

## Next up
| ID | Item | Type | Size | Why now |
|---|---|---|---|---|
| B-001 | `GET /targets/{id}/slo` + FR-008's hourly rollup job | Feature | Medium | The read path (`operatorapi`, the dashboard screen) now exists for status; SLO is the next piece `05-api-contracts.md` already specifies but no session has built the rollup job for (see R-001/R-002 in `10-risk-register.md` for other open items first) |
| B-002 | `GET /targets/{id}/incidents` | Feature | Small | Simple direct `incidents` read, no new computation — same pattern as this session's `/status` endpoint |
| B-003 | Real TLS/reverse-proxy story for `docker-compose.yml` | Deployment | Medium | R-001: the `Secure` operator-session cookie cannot work in a real browser against the current plain-HTTP deployment shape |
## Later
## Explicitly rejected (with reasons)
