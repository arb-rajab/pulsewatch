# Release Notes
> Purpose: what changed, for humans
> Project: pulsewatch (public)
> Last updated: 2026-08-31

## Unreleased
### Added
- FR-008's hourly rollup job (`internal/rollup`) computes real per-target,
  per-hour uptime/latency aggregates into `check_rollups_hourly`; a new
  `GET /api/v1/targets/{id}/slo` endpoint reads them into a rolling-window
  uptime%/error-budget figure; the dashboard now shows each target's uptime
  over the default 30-day window alongside its live status (Session 12).
- Real, persistent TLS termination: a Caddy reverse proxy (`proxy` service
  in `docker-compose.yml`) now serves the dashboard/login flow and the API
  over `https://localhost:8443`, using a self-signed certificate from
  Caddy's own local CA (Session 10).
### Changed
- `frontend` no longer publishes a plain-HTTP host port — reachable only
  through the new HTTPS proxy (Session 10).
### Fixed
- `docker-compose.yml`'s `SESSION_SIGNING_SECRET` line had a YAML parse bug
  that made `docker compose up` fail immediately (Session 10).
- `frontend`'s `PUBLIC_API_URL` default pointed at `http://localhost:8020`,
  which does not resolve to `backend` from inside the `frontend` container —
  fixed to `http://backend:8080` (Session 10).
### Security
- The `operatorSession` cookie's `Secure` attribute is now actually
  honored by a real browser (R-001, Session 10) — self-signed cert, so a
  first-visit browser warning is expected and accepted (see
  `08-deployment-and-operations.md`).
### Migration notes
- Existing deployments: re-run `docker compose up -d --build` after
  pulling; `frontend`'s host port (previously `3003`) is gone, use
  `https://localhost:8443` instead.
### Rollback instructions
- Revert the `proxy` service and `frontend` port change in
  `docker-compose.yml`; `frontend` will publish `3003:3000` again over
  plain HTTP (not recommended — this reopens R-001).
