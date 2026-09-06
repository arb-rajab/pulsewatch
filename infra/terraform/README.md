# pulsewatch platform infrastructure (ADR-0005)

Manages cluster-level state for the Kubernetes deployment target: the
`pulsewatch` namespace, application secrets, the `ingress-nginx`
controller, `cert-manager`, and a Let's Encrypt `ClusterIssuer`. Does
**not** provision the Kubernetes cluster itself (BYO-cluster, per
ADR-0005's Decision) and does **not** manage the application workload
(Deployments/Services/the migration Job — see `../../deploy/k8s/`).

## Usage

```
cp terraform.tfvars.example terraform.tfvars
# fill in real values — never commit terraform.tfvars (see ../../.gitignore)
terraform init
terraform plan
terraform apply
```

## Known limitation — not exercised against a live cluster

This module was authored and offline-validated (`terraform fmt -check`)
in a sandboxed session with no outbound access to
`registry.terraform.io` (confirmed: `connect_rejected` through the
session's egress proxy) and no live Kubernetes cluster or Docker daemon
available. `terraform init`/`plan`/`apply` have **not** been run against
real infrastructure. See R-008
(`../../docs/project-memory/10-risk-register.md`) and B-009
(`../../docs/project-memory/11-backlog.md`) — closing that gap with a
real cluster is a future session's actual objective, not optional
polish.

## Variables

See `variables.tf` for the full, documented list — it mirrors
`../../.env.example`'s own required/optional secrets one-for-one so the
Kubernetes target needs exactly the same real inputs Compose does.

Notably: `use_staging_acme` defaults to `true` so a first `apply` cannot
accidentally burn a real Let's Encrypt production rate-limit slot while
you're still confirming DNS/ingress wiring. Flip it to `false` only once
`kubectl get certificate` shows the staging certificate issuing
successfully end-to-end.
