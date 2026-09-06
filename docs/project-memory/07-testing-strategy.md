# Testing Strategy
> Purpose: what we test, at which level, and why that is sufficient
> Project: pulsewatch (public)
> Last updated: 2026-08-29

## Testing philosophy for this project
## Levels
| Level | Tool | Scope | Gate |
|---|---|---|---|
## Security testing
## Accessibility testing
## Performance testing and budgets
## Test data strategy (synthetic only)
## Quality gates in CI

**Note on this section and this file generally:** every section above this
one was, as of Session 14, an empty header with no content — a real,
pre-existing gap independent of the Kubernetes/Terraform work below, not
something this session introduced. Tracked as B-012 (`11-backlog.md`): a
future session should fill this file for the whole system. What follows
is scoped to the deployment-layer gate this session actually added.

`.github/workflows/ci.yml`'s `iac-and-k8s-manifests-validate` job
(Session 14) gates every push/PR on two offline checks: `terraform fmt
-check -diff -recursive` (HCL syntax/formatting for `infra/terraform/`)
and `kubectl kustomize --load-restrictor LoadRestrictionsNone
deploy/k8s/overlays/production` (a real Kustomize base+overlay build —
image substitution, patches, ConfigMap generation from
`backend/migrations/*.sql` and `otel-collector-config.yaml` — with no
live cluster contacted). This catches syntax and structural regressions
(a broken patch, a missing resource reference, invalid HCL) on every
change; it does **not** catch a real provider-schema mismatch, a real
cloud API rejection, or a real cluster admission-policy failure — those
require `terraform apply`/`kubectl apply` against live infrastructure,
which neither this CI job nor the Session 14 sandbox that authored it had
access to (R-008, `10-risk-register.md`; B-009, `11-backlog.md`).

## Known gaps and why they are acceptable

- **`06-security-threat-model.md` and this file are largely unfilled
  beyond section headers** (B-012) — a real, pre-existing gap, not new to
  Session 14.
- **The Kubernetes/Terraform deployment layer (ADR-0005) has never been
  applied against a live cluster** — see the "What's not yet real"
  section of `08-deployment-and-operations.md`, R-008, and B-009. Offline
  structural validation (above) is real and passing; end-to-end
  production-readiness is not yet proven the way this project's own
  standard (real evidence, not "looks right") requires for everything
  else it calls done.
