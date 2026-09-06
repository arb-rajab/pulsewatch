#!/usr/bin/env bash
# Ordered rollout for the Kubernetes deployment target (ADR-0005).
#
# Enforces the same guarantee docker-compose.yml gets for free from
# `depends_on: condition: service_completed_successfully` on `migrate`:
# migrations must run to completion BEFORE the new backend/frontend
# images are rolled out, not concurrently with them. A Kubernetes Job has
# no built-in equivalent of that compose dependency condition, so this
# script enforces the ordering explicitly instead of leaving it as an
# unstated assumption.
#
# Usage:
#   ./rollout.sh <namespace> <backend-image> <frontend-image>
# Example:
#   ./rollout.sh pulsewatch ghcr.io/OWNER/pulsewatch-backend:abc1234 ghcr.io/OWNER/pulsewatch-frontend:abc1234
#
# Prerequisites:
#   - infra/terraform has already been applied (namespace +
#     pulsewatch-app-secrets + ingress-nginx + cert-manager exist)
#   - overlays/production/kustomization.yaml's Ingress host patch has been
#     edited to the real hostname (see deploy/k8s/README.md)
#   - the standalone `kustomize` CLI is installed (distinct from `kubectl
#     kustomize`, which can render but has no `edit` subcommand) — used
#     below only to set the two image tags for this specific rollout

set -euo pipefail

NAMESPACE="${1:?namespace required}"
BACKEND_IMAGE="${2:?backend image required, e.g. ghcr.io/OWNER/pulsewatch-backend:TAG}"
FRONTEND_IMAGE="${3:?frontend image required, e.g. ghcr.io/OWNER/pulsewatch-frontend:TAG}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OVERLAY_DIR="${SCRIPT_DIR}/overlays/production"

echo "==> Setting image tags for this rollout"
(
  cd "${OVERLAY_DIR}"
  kustomize edit set image "pulsewatch-backend=${BACKEND_IMAGE}" "pulsewatch-frontend=${FRONTEND_IMAGE}"
)

echo "==> Rendering manifests (kubectl kustomize, --load-restrictor LoadRestrictionsNone required: this repo's kustomization deliberately references backend/migrations/*.sql and otel-collector-config.yaml from outside deploy/k8s/, so the two deployment targets can never silently drift)"
RENDERED="$(mktemp)"
trap 'rm -f "${RENDERED}"' EXIT
kubectl kustomize --load-restrictor LoadRestrictionsNone "${OVERLAY_DIR}" > "${RENDERED}"

echo "==> Phase 1: migration Job"
# Delete any prior completed Job first — a Job's pod template is
# immutable, so re-applying this release's (possibly-changed) migration
# image/args in place would be rejected by the API server.
kubectl -n "${NAMESPACE}" delete job pulsewatch-migrate --ignore-not-found
kubectl -n "${NAMESPACE}" apply -f - <<EOF
$(python3 -c "
import sys, yaml
docs = list(yaml.safe_load_all(open('${RENDERED}')))
for d in docs:
    if d and d.get('kind') in ('Job', 'ConfigMap') and d['metadata']['name'].startswith(('pulsewatch-migrate', 'pulsewatch-migrations')):
        print('---')
        print(yaml.safe_dump(d))
")
EOF

echo "==> Waiting for migration Job to complete (bounded: 120s)"
kubectl -n "${NAMESPACE}" wait --for=condition=complete job/pulsewatch-migrate --timeout=120s

echo "==> Phase 2: applying platform/data-layer objects and workload Deployments"
kubectl -n "${NAMESPACE}" apply -f "${RENDERED}"

echo "==> Waiting for backend rollout"
kubectl -n "${NAMESPACE}" rollout status deployment/backend --timeout=180s

echo "==> Waiting for frontend rollout"
kubectl -n "${NAMESPACE}" rollout status deployment/frontend --timeout=180s

echo "==> Done. backend=${BACKEND_IMAGE} frontend=${FRONTEND_IMAGE}"
