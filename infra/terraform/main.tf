# pulsewatch platform layer (ADR-0005). Owns cluster-level infrastructure
# only: the namespace, application secrets, the ingress controller, and
# cert-manager's Let's Encrypt issuer. The application workload itself
# (Deployments, Services, the migration Job) lives in ../../deploy/k8s and
# is applied separately by deploy/k8s/rollout.sh — see that ADR's Decision
# for why these two are deliberately not the same apply cycle.

resource "kubernetes_namespace" "pulsewatch" {
  metadata {
    name = var.namespace
    labels = {
      "app.kubernetes.io/part-of"    = "pulsewatch"
      "app.kubernetes.io/managed-by" = "terraform"
    }
  }
}

# Mirrors .env.example one-for-one (see variables.tf). DATABASE_URL is
# assembled here, once, from the same postgres_user/password/db inputs
# postgres itself is configured with below — the backend and the Postgres
# StatefulSet (deploy/k8s/base/postgres.yaml) must agree on these
# credentials, which is why both sides read from this one Secret rather
# than each hardcoding its own copy.
resource "kubernetes_secret" "pulsewatch_app" {
  metadata {
    name      = "pulsewatch-app-secrets"
    namespace = kubernetes_namespace.pulsewatch.metadata[0].name
    labels = {
      "app.kubernetes.io/part-of" = "pulsewatch"
    }
  }

  data = {
    POSTGRES_USER     = var.postgres_user
    POSTGRES_PASSWORD = var.postgres_password
    POSTGRES_DB       = var.postgres_db
    DATABASE_URL = format(
      "postgres://%s:%s@postgres:5432/%s?sslmode=disable",
      var.postgres_user,
      var.postgres_password,
      var.postgres_db,
    )
    SESSION_SIGNING_SECRET       = var.session_signing_secret
    ALERT_CHANNEL_ENCRYPTION_KEY = var.alert_channel_encryption_key
  }

  type = "Opaque"
}

# --- Ingress controller ---
# Kubernetes's equivalent of docker-compose.yml's `proxy` (Caddy) service —
# terminates the operator-facing HTTPS connection, per R-001's own
# reasoning (a real TLS story is required, self-signed-by-default is an
# accepted but named trade-off). ingress-nginx is the ingress controller
# implementation; cert-manager (below) is what gets it a real, publicly
# trusted certificate instead of Caddy's `tls internal` local CA.
resource "helm_release" "ingress_nginx" {
  name             = "ingress-nginx"
  repository       = "https://kubernetes.github.io/ingress-nginx"
  chart            = "ingress-nginx"
  version          = "4.11.3"
  namespace        = "ingress-nginx"
  create_namespace = true

  set {
    name  = "controller.publishService.enabled"
    value = "true"
  }
}

# --- cert-manager ---
# Automates real Let's Encrypt certificate issuance/renewal for
# var.ingress_hostname — the Kubernetes-target equivalent of the
# real-domain upgrade path 08-deployment-and-operations.md already
# documented (but never exercised) for Caddy under Compose. Session 10's
# own reasoning for why `tls internal`/self-signed was the accepted
# default there (no real domain, no cost paid for a public CA) is exactly
# the gap this makes real for the Kubernetes target instead, when the
# operator supplies var.ingress_hostname and var.acme_email.
resource "helm_release" "cert_manager" {
  name             = "cert-manager"
  repository       = "https://charts.jetstack.io"
  chart            = "cert-manager"
  version          = "v1.15.3"
  namespace        = "cert-manager"
  create_namespace = true

  set {
    name  = "crds.enabled"
    value = "true"
  }
}

locals {
  acme_server = var.use_staging_acme ? "https://acme-staging-v02.api.letsencrypt.org/directory" : "https://acme-v02.api.letsencrypt.org/directory"
}

# A ClusterIssuer, not a namespaced Issuer: pulsewatch has exactly one
# namespace today, but a ClusterIssuer costs nothing extra and doesn't need
# re-creating if a future session ever adds a second namespace (e.g. a
# staging copy) that also wants real certificates.
resource "kubernetes_manifest" "letsencrypt_issuer" {
  manifest = {
    apiVersion = "cert-manager.io/v1"
    kind       = "ClusterIssuer"
    metadata = {
      name = "pulsewatch-letsencrypt"
    }
    spec = {
      acme = {
        server = local.acme_server
        email  = var.acme_email
        privateKeySecretRef = {
          name = "pulsewatch-letsencrypt-account-key"
        }
        solvers = [
          {
            http01 = {
              ingress = {
                ingressClassName = "nginx"
              }
            }
          }
        ]
      }
    }
  }

  depends_on = [helm_release.cert_manager]
}
