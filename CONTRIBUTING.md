# Contributing

This is primarily a solo portfolio project built through a documented,
session-based workflow (see `docs/project-memory/`). External contributions
are welcome once the project reaches a stable v1.0.0, but please read this
first.

## Before contributing

1. Check `docs/project-memory/01-scope-and-non-goals.md` — pull requests for
   explicit non-goals will be declined regardless of quality (see the
   non-goals preview in `README.md` for what this already anticipates).
2. Check `docs/project-memory/11-backlog.md` for planned work to avoid
   duplicate effort.
3. Open an issue before a large PR — small fixes (typos, obvious bugs) can go
   straight to a PR.

## Workflow

- **Branching:** trunk-based. Branch from `main` as `feat/<issue>-<slug>`,
  `fix/…`, `chore/…`, or `sec/…`.
- **Commits:** [Conventional Commits](https://www.conventionalcommits.org/)
  (`feat:`, `fix:`, `docs:`, `test:`, `ci:`, `refactor:`, `chore:`, `perf:`,
  `sec:`).
- **Pull requests:** must link an issue (`Closes #N`), pass CI (lint,
  vet, tests, security scans), and use the PR template.
- **Code review:** all PRs require passing CI at minimum; the maintainer
  reviews for architecture and security fit.

## Development setup

Copy `.env.example` to `.env` first (all Session-0 skeleton defaults, no
real secrets), then:

```bash
docker compose up --build
```

This brings up, in dependency order: Postgres → a one-shot `migrate`
service that applies every migration in `backend/migrations/` and exits →
Redis and the OTel Collector → the backend (waits for `migrate` to exit
`0`) → the frontend. A fresh `docker compose up` always starts from the
real, current schema — there is no separate manual migration step for
local dev.

Verify the stack:

- Backend health: `curl http://localhost:8020/health` → `{"status":"ok"}`
- Frontend: `curl http://localhost:3003/health` → `{"status":"ok"}`
- OTel Collector health: `curl http://localhost:13143/` → `{"status":"Server available"}`
  (the collector's images ship no shell/`wget`/`curl`, so this is checked
  from the host, not via a Docker-level `healthcheck:`)
- Schema landed: `docker compose exec postgres psql -U pulsewatch -d pulsewatch -c '\dt'`
  should list `targets`, `target_schedule`, `check_results` (plus its day
  partitions and a `check_results_default` catch-all), `check_rollups_hourly`,
  `incidents`, `alert_channels`, `alert_dispatches`, `agents`, `operators`,
  and golang-migrate's own `schema_migrations`.

### Backend (Go + Gin) — `backend/`

- `go test ./...` — unit tests
- `golangci-lint run` — see `backend/.golangci.yml`; includes
  context-propagation linters (`contextcheck`, `containedctx`,
  `fatcontext`, `noctx`) and `bodyclose`, load-bearing given how much of
  this system's correctness rests on context cancellation
  (`docs/adr/ADR-0004-go-concurrency-worker-pool.md`) and long-running
  outbound HTTP checks.
- `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` — vulnerability scan

### Migrations — `backend/migrations/`

Managed with [`golang-migrate`](https://github.com/golang-migrate/migrate)
(sequential `NNNNNN_description.up.sql` / `.down.sql` pairs, plain SQL —
the tooling choice `04-data-model.md`'s "Migration approach" section left
open for this session). `docker compose up` applies them automatically;
to run them by hand against a running Postgres:

```bash
docker run --rm --network pulsewatch_default \
  -v "$(pwd)/backend/migrations:/migrations" \
  migrate/migrate:v4.19.1 \
  -path=/migrations \
  -database "postgres://pulsewatch:pulsewatch@postgres:5432/pulsewatch?sslmode=disable" \
  up
```

Substitute `down -all` (or `down N`) to roll back, or `-database` with your
own connection string outside Docker. CI (`.github/workflows/ci.yml`)
verifies this same up → down -all → up round-trip against a fresh Postgres
service container on every push/PR, so a migration that doesn't reverse
cleanly fails CI, not just local dev.

### Frontend (SvelteKit) — `frontend/`

- `npm install`
- `npm run build`
- `npm test` — Vitest
- `npm run lint` — ESLint + `prettier --check .`
- `npm run format` — `prettier --write .`
- `npm run check` — `svelte-check` (TypeScript)

### OTel Collector config — `otel-collector-config.yaml`

Two pipelines through one collector (`docs/project-memory/03-architecture.md`'s
"Where OpenTelemetry actually fits" section): a `logs` pipeline forwarding
agent-reported check results/heartbeats to the backend's `/v1/logs`
ingestion endpoint (preserving the agent's `Authorization` header via the
`headers_setter` extension, per ADR-0003), and `traces`/`metrics` pipelines
for the server's own self-observability, exported to the `debug` exporter
only — no Grafana/Prometheus backend, by design. Validate config syntax
after editing it:

```bash
docker run --rm -v "$(pwd)/otel-collector-config.yaml:/etc/otelcol-contrib/config.yaml" \
  otel/opentelemetry-collector-contrib:0.159.0 \
  validate --config=/etc/otelcol-contrib/config.yaml
```

## Reporting security issues

See [`SECURITY.md`](SECURITY.md) — do not use public issues for
vulnerabilities.

## Code of conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md).
