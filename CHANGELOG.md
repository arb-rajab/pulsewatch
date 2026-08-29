# Changelog

All notable changes to this project will be documented in this file.
Format based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
versioning follows [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
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
