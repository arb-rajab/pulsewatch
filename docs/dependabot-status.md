# Dependabot status

_Last updated: 2026-10-08. Maintained during the Dependabot clean-up pass; update when the state changes._

## Configuration

- Ecosystems covered: gomod (`/backend`, `/serverless/otlp-normalizer`), npm (`/frontend`), docker (`/backend`, `/frontend`), terraform (`/infra/terraform`), github-actions (`/`), docker-compose (`/`).
- Grouping: `minor-and-patch` for every ecosystem (open-PR limit 5 each).
- Schedule: weekly.
- Ignore rules: `@sveltejs/kit` and `@sveltejs/adapter-node` majors (SvelteKit 3 is a migration, peers TypeScript 6); `typescript` majors; Terraform `hashicorp/kubernetes` and `hashicorp/helm` majors (CI cannot show schema changes are safe; upgrade with a real `terraform plan`); docker-compose image majors (stateful services need a deliberate migration).

## State at last update

- Open Dependabot PRs: 0 (each merged or closed only after reading its checks).
- Default-branch CI: green at last check.

## Time-limited exemptions

- `osv-scanner.toml` (proposed, awaiting owner approval): `golang.org/x/crypto` v0.57.0 (GO-2026-5932, the unmaintained `openpgp` package; not imported here, only `bcrypt` is), `ignoreUntil` 2026-11-15.

## Notes

- The Go toolchain in CI is pinned via `setup-go` with go.mod requiring Go 1.26; govulncheck runs with `GOTOOLCHAIN: auto`.
- A `dependency-scan` (osv-scanner) job in `ci.yml` gates both Go modules and the frontend lockfile, including dev dependencies that `npm audit --omit=dev` skips and that `govulncheck` does not report.

## Deferred (not re-raised each pass)

- Ignored major versions are listed in `.github/dependabot.yml` with the reason for each.
- Re-check exemptions before their `effectiveUntil` date (2026-11-15) and drop them once upstream fixes ship.
