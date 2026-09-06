# Provider pins for pulsewatch's Kubernetes platform layer (ADR-0005).
#
# This module deliberately does not declare a cloud provider (AWS/GCP/
# Azure/Hetzner/etc.) — per ADR-0005's Decision, pulsewatch manages
# resources inside a Kubernetes cluster the operator already controls
# (BYO-cluster), matching this project's "agents run on infrastructure the
# operator controls" business assumption (00-project-brief.md). Point
# `var.kubeconfig_path`/`var.kube_context` at whatever cluster that is.

terraform {
  required_version = ">= 1.5.0"

  required_providers {
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.31"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.15"
    }
  }
}

provider "kubernetes" {
  config_path    = var.kubeconfig_path
  config_context = var.kube_context
}

provider "helm" {
  kubernetes {
    config_path    = var.kubeconfig_path
    config_context = var.kube_context
  }
}
