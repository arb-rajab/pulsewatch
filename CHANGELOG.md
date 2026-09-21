# Changelog

All notable changes to this project will be documented in this file.
Format based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
versioning follows [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Live dashboard push over Server-Sent Events (ADR-0010): `GET /api/v1/events`
  streams real `target_status` and `incident` (opened/resolved) events to
  connected operator sessions, sourced from the exact same write path
  (`alerting.RecordCheckResult`/`OpenIncident`/`CloseIncident`) that already
  drives webhook/push/email dispatch — no parallel detection mechanism. The
  scheduler's own release path and the agent-facing OTLP ingestion path both
  publish to one in-process `internal/livefeed.Hub`. The dashboard
  (`/dashboard`) proxies the stream through its own `/dashboard/events`
  route (browser `EventSource` never talks to the backend directly, same
  pattern as every other session-cookie-gated call) and re-fetches a
  target's real status on each event; a capped exponential backoff
  reconnects the client-side stream after a network blip. Existing
  SSR-load-based fetching is unchanged and remains the initial-load/
  fallback path.
- Device list dashboard (B-017): `/dashboard/devices` shows an operator's
  registered devices (platform, registered date, last delivery,
  active/dead/revoked status) with a manual revoke action against
  `DELETE /api/v1/device-tokens/{id}`. `GET /api/v1/device-tokens` and the
  `DeviceToken` schema now also return `revoked_at`, which was previously
  written but never read back.
- Mobile push alert delivery (ADR-0007): a `"push"` alert channel sends a
  real notification through FCM HTTP v1 or APNs HTTP/2 to every device
  registered via the new `/api/v1/device-tokens` endpoints. Dead device
  tokens (uninstalled app, rotated token) are recorded and never retried;
  transient provider failures still retry; re-registering a device
  resurrects it. No new Go module dependency.
- Repository governance: framework allocation ledger row confirmed
  (`UNIQUE` — Go + Gin (backend) + SvelteKit (frontend), no flagship
  collision).
- Project Memory Pack scaffolded (15-file structure under
  `docs/project-memory/`).
- Session 0 deliverables: ledger confirmation, project brief stub,
  repository skeleton, licence, contribution/security/conduct policies.
- Minimal real Go module (Gin, single health endpoint, passing test,
  golangci-lint configured) and SvelteKit skeleton (placeholder page,
  health route, passing test).
- Docker Compose skeleton (PostgreSQL, Redis, backend, frontend) with
  working health checks.
- Real CI pipeline from the first commit: golangci-lint, go vet, go test,
  govulncheck (backend); eslint, build, tests (frontend); gitleaks; CodeQL.

Nothing has shipped yet — this project is pre-v0.1.0.
