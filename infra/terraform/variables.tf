variable "kubeconfig_path" {
  description = "Path to the kubeconfig for the cluster the operator already controls. BYO-cluster, per ADR-0005 — this module never provisions the cluster itself."
  type        = string
  default     = "~/.kube/config"
}

variable "kube_context" {
  description = "kubeconfig context to use. Empty string uses the current context."
  type        = string
  default     = ""
}

variable "namespace" {
  description = "Kubernetes namespace pulsewatch's platform resources and workload live in."
  type        = string
  default     = "pulsewatch"
}

variable "acme_email" {
  description = "Email address cert-manager's ClusterIssuer registers with Let's Encrypt for expiry/revocation notices."
  type        = string
}

variable "ingress_hostname" {
  description = "Public DNS hostname the operator's dashboard/API will be reachable at (e.g. pulsewatch.example.com). Must already point at the ingress controller's external IP/LB — this module does not manage DNS."
  type        = string
}

variable "use_staging_acme" {
  description = "Use Let's Encrypt's staging ACME server (untrusted certs, no rate limits) instead of production. Default true so a first apply cannot accidentally burn a real Let's Encrypt production rate-limit slot while testing this module — flip to false once the ingress hostname and DNS are confirmed working end-to-end."
  type        = bool
  default     = true
}

# --- Application secrets ---
# Mirrors .env.example's own required/optional secrets one-for-one so the
# Kubernetes deployment target needs exactly the same real inputs Compose
# does, no more and no less.

variable "postgres_user" {
  description = "Postgres role pulsewatch connects as."
  type        = string
  default     = "pulsewatch"
}

variable "postgres_password" {
  description = "Postgres password. No default — unlike docker-compose.yml's dev-only 'pulsewatch' default, a production secret must be supplied explicitly."
  type        = string
  sensitive   = true
}

variable "postgres_db" {
  description = "Postgres database name."
  type        = string
  default     = "pulsewatch"
}

variable "session_signing_secret" {
  description = "Base64-encoded HMAC signing key (decodes to >=32 bytes) for the pulsewatch_session cookie. Required — backend refuses to start without it. Generate with: openssl rand -base64 32"
  type        = string
  sensitive   = true

  validation {
    condition     = length(var.session_signing_secret) > 0
    error_message = "session_signing_secret is required — generate one with: openssl rand -base64 32 (main.go: operatorauth.SigningSecretFromEnv has no fallback, by design)."
  }
}

variable "alert_channel_encryption_key" {
  description = "Base64-encoded AES-256 key (decodes to exactly 32 bytes) used to decrypt alert_channels.destination_encrypted. Optional — the scheduler degrades gracefully (logs a warning, skips dispatch) if unset, matching .env.example's own documented behavior."
  type        = string
  sensitive   = true
  default     = ""
}

# Note: container image references (backend/frontend tags) are deliberately
# not Terraform inputs — per ADR-0005's Decision, per-release image
# versioning belongs to deploy/k8s/overlays/production/kustomization.yaml's
# `images:` field (set by deploy/k8s/rollout.sh), not to this module's
# plan/apply cycle. Terraform owns platform state that changes rarely;
# the image tag changes on every deploy.
