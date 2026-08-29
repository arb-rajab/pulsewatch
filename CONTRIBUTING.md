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

```bash
docker compose up --build
```

- Backend (Go + Gin): `backend/` — `go test ./...`, `golangci-lint run`
- Frontend (SvelteKit): `frontend/` — `npm install`, `npm run build`,
  `npm test`, `npm run lint`

## Reporting security issues

See [`SECURITY.md`](SECURITY.md) — do not use public issues for
vulnerabilities.

## Code of conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md).
