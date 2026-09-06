# Security and Threat Model
> Purpose: what can go wrong, and what stops it
> Project: pulsewatch (public)
> Last updated: 2026-08-29

## Assets and data classification
## Trust boundaries
## Threats (STRIDE)
| ID | Boundary | Threat | Category | L/I | Mitigation | Verified by |
|---|---|---|---|---|---|---|
## Abuse cases
## Authentication and authorisation design
## Secrets management

**Note on this section and this file generally:** every section above this
one was, as of Session 14, an empty header with no content — a real,
pre-existing gap independent of the Kubernetes/Terraform work below, not
something this session introduced. Tracked as B-012 (`11-backlog.md`): a
future session should fill this file for the whole system, not just the
deployment layer. What follows is scoped strictly to the secrets this
session's own IaC work introduced.

### Kubernetes/Terraform secrets (ADR-0005, Session 14)

- `infra/terraform/variables.tf` marks every credential-shaped variable
  (`postgres_password`, `session_signing_secret`,
  `alert_channel_encryption_key`) `sensitive = true` — Terraform redacts
  these from `plan`/`apply` console output and from `terraform.tfstate`'s
  human-readable diffs (the underlying state file itself still contains
  the real values in plaintext, Terraform's own standard behavior; this
  module does not configure a remote encrypted backend, since none is
  chosen yet — a future session should pick one, e.g. an encrypted S3/GCS
  backend with versioning, before this is used against a real production
  secret rather than staging/testing values).
- `terraform.tfvars` (the file that actually holds real secret values) is
  gitignored (`.gitignore`'s new Terraform section) and never committed —
  `terraform.tfvars.example` in the repo holds only empty placeholders.
- Kubernetes-side, secrets land in a single `Secret`
  (`pulsewatch-app-secrets`, `infra/terraform/main.tf`), consumed by
  `backend`/`postgres`/the migration `Job` via `envFrom`/`secretKeyRef` —
  never baked into an image or a ConfigMap. This is the Kubernetes-native
  minimum, not a hardened secret store: `Secret` objects are
  base64-encoded, not encrypted, at rest by default on most clusters
  (encryption-at-rest is a cluster-level configuration this module does
  not control) and are readable by anyone with `get secret` RBAC in the
  `pulsewatch` namespace. A future session should consider a real
  secrets-manager integration (e.g. `external-secrets` syncing from a
  cloud secrets manager, or Sealed Secrets for GitOps-safe encrypted
  commits) if this deployment target ever needs to satisfy a stricter
  threat model than "the operator's own cluster, the operator's own
  RBAC" — not built now, since no such stricter requirement exists yet
  for a single-operator, self-hosted deployment (`01-scope-and-non-goals.md`).
- `.env`/`.env.example` (Compose) and `terraform.tfvars.example`
  (Kubernetes) deliberately hold the identical set of required secrets —
  no deployment-target-specific secret was invented, so an operator
  moving from Compose to Kubernetes reuses the same values rather than
  generating a second set.

## Dependency and supply-chain controls
## Accepted risks (reason + revisit trigger)
