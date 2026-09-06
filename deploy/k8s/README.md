# pulsewatch on Kubernetes (ADR-0005)

Production deployment target, additive to (not a replacement for) the
Docker Compose local/dev path documented in
`../../docs/project-memory/08-deployment-and-operations.md`.

## Prerequisites

1. A Kubernetes cluster you already control (BYO-cluster — see
   ADR-0005's Decision; this project does not provision cloud compute on
   your behalf).
2. `infra/terraform/` applied against that cluster — creates the
   namespace, `pulsewatch-app-secrets`, `ingress-nginx`, `cert-manager`,
   and a Let's Encrypt `ClusterIssuer`. See `../../infra/terraform/README.md`.
3. `kubectl`, the standalone `kustomize` CLI, and a DNS record for your
   chosen hostname pointing at the ingress controller's external
   IP/LoadBalancer.
4. Backend and frontend images published somewhere this cluster can pull
   from — `.github/workflows/ci.yml`'s `publish-images` job builds and
   pushes both to GHCR on every push to `main`.

## First-time setup

1. Edit `overlays/production/kustomization.yaml`'s Ingress patch: replace
   both `pulsewatch.example.com` placeholders with your real hostname —
   it must match `infra/terraform/terraform.tfvars`'s `ingress_hostname`
   exactly, or cert-manager's HTTP-01 challenge will request a
   certificate for a name this Ingress doesn't answer for.
2. Confirm `infra/terraform` has been applied and its namespace output
   matches what you pass to `rollout.sh` below.

## Deploying

```
./rollout.sh <namespace> <backend-image> <frontend-image>
```

This is the *only* supported way to apply `deploy/k8s/` — it enforces
migrations-before-workload ordering (Kubernetes Jobs have no built-in
equivalent of Compose's `depends_on: condition:
service_completed_successfully`) and waits for each Deployment's rollout
to finish before returning. See the script's own header comment and
`../../docs/project-memory/08-deployment-and-operations.md`'s Kubernetes
section for the full rollout/migration/agent-compatibility contract this
enforces.

## Validating changes offline (no cluster required)

```
kubectl kustomize --load-restrictor LoadRestrictionsNone overlays/production
```

`--load-restrictor LoadRestrictionsNone` is required because this
kustomization deliberately references `../../../backend/migrations/*.sql`
and `../../../otel-collector-config.yaml` from outside `deploy/k8s/` —
so the Kubernetes and Compose targets read the exact same migration files
and collector config, and can never silently drift apart. This is the
validation this repository's own Session 14 work relied on: no live
cluster was available in that session's sandbox (no outbound access to
`registry.terraform.io`, no Docker daemon, no `kubectl`/`helm`/`terraform`
installed beforehand) — see R-008 (`../../docs/project-memory/10-risk-register.md`)
and B-009 (`../../docs/project-memory/11-backlog.md`) for what a real
cluster still needs to verify.

## What's here

| File | Kubernetes equivalent of |
|---|---|
| `base/postgres.yaml` | `docker-compose.yml`'s `postgres` service |
| `base/redis.yaml` | `docker-compose.yml`'s `redis` service |
| `base/otel-collector.yaml` | `docker-compose.yml`'s `otel-collector` service |
| `base/migration-job.yaml` | `docker-compose.yml`'s `migrate` service |
| `base/backend.yaml` | `docker-compose.yml`'s `backend` service — **two** Services (`backend-operator`, `backend-agent`), one per R-004's router split |
| `base/frontend.yaml` | `docker-compose.yml`'s `frontend` service |
| `base/ingress.yaml` | `Caddyfile` — plus routing agent traffic through the same real-TLS Ingress, which Compose's `proxy` deliberately does not do (see `backend.yaml`'s comment and R-003) |
| `base/networkpolicy.yaml` | no Compose equivalent — network-layer defense-in-depth for R-004's router split |
