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
- Last full rescan: 2026-10-08. Checked open PRs, default-branch and scheduled CI, Dependabot update jobs, ecosystem coverage against the manifests in the repo, Actions pins, exemption expiry dates, stray branches, and (new this pass) a local full-history gitleaks 8.28.0 scan. No new gaps.

## Time-limited exemptions

- `osv-scanner.toml` (approved by the repo owner 2026-10-08, merged in #50): `golang.org/x/crypto` v0.57.0 (GO-2026-5932, the unmaintained `openpgp` package; not imported here, only `bcrypt` is), `ignoreUntil` 2026-11-15.

## Notes

- The Go toolchain in CI is pinned via `setup-go` with go.mod requiring Go 1.26; govulncheck runs with `GOTOOLCHAIN: auto`.
- A `dependency-scan` (osv-scanner) job in `ci.yml` gates both Go modules and the frontend lockfile, including dev dependencies that `npm audit --omit=dev` skips and that `govulncheck` does not report.
- `golang.org/x/net` 0.58.0 -> 0.60.0 in `backend/go.mod` (2026-10-08, indirect): five advisories published that day (GO-2026-6603, GO-2026-6610, GO-2026-6611, GO-2026-6612, GO-2026-6617) failed `dependency-scan` and the backend job on PR #54. Fixed by the module bump; `go build`, `go vet` and `go test ./...` pass locally.
- Every workflow declares a top-level `permissions: contents: read` (added 2026-10-08, rescan cycle 3). Jobs that need more, such as CodeQL's `security-events: write`, declare it at job level.
- Merge policy (deliberate choice by the repo owner, 2026-10-08): every PR, major-version dependency bumps included, is merged as soon as all of its required checks are green, confirmed per PR. This repo is a code showcase with no business or sensitive dependency, so green checks are the only gate. Red, pending or conflicted PRs are fixed or closed instead.

## Deferred (not re-raised each pass)

- Ignored major versions are listed in `.github/dependabot.yml` with the reason for each.
- Re-check exemptions before their `effectiveUntil` date (2026-11-15) and drop them once upstream fixes ship.
- Dependabot/code-scanning alert API (2026-10-08): not readable. The proxy-injected `GH_ALERTS_TOKEN` is sent, but `GET /repos/arb-rajab/*/dependabot/alerts` and `/code-scanning/alerts` return 403 "Resource not accessible by integration" on all 12 repos; the token lacks the `vulnerability_alerts` / `security_events` read permissions. Alert state remains unverified.
