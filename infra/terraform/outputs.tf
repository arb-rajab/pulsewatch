output "namespace" {
  description = "Namespace the pulsewatch workload should be applied into (deploy/k8s/rollout.sh reads this)."
  value       = kubernetes_namespace.pulsewatch.metadata[0].name
}

output "app_secret_name" {
  description = "Name of the Kubernetes Secret deploy/k8s/base's Deployments reference via envFrom."
  value       = kubernetes_secret.pulsewatch_app.metadata[0].name
}

output "cluster_issuer_name" {
  description = "cert-manager ClusterIssuer name referenced by deploy/k8s/base/ingress.yaml's cert-manager.io/cluster-issuer annotation."
  value       = "pulsewatch-letsencrypt"
}

output "acme_server_in_use" {
  description = "Which ACME server is configured — a loud reminder this defaults to Let's Encrypt staging (untrusted certs) until use_staging_acme=false is set deliberately."
  value       = local.acme_server
}
