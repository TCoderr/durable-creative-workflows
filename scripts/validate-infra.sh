#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p .local/evidence .local/manifests
terraform fmt -check -recursive infra/terraform
for environment in dev staging prod; do
  terraform -chdir="infra/terraform/environments/$environment" init -backend=false -input=false -no-color
  terraform -chdir="infra/terraform/environments/$environment" validate -no-color
  kubectl kustomize "infra/kubernetes/overlays/$environment" > ".local/manifests/$environment.yaml"
  "${KUBECONFORM:-.local/bin/kubeconform}" -strict -summary -kubernetes-version 1.34.0 ".local/manifests/$environment.yaml"
done
echo 'Infrastructure validation passed. No cloud plan/apply or Kubernetes deployment executed.'
