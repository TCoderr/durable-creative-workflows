#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p .local/security .local/trivy-cache
scanner=velin-trivy:0.74.0-security.1
docker build --file infra/docker/scanner.Dockerfile --tag "$scanner" .
if [[ $# == 0 ]]; then
  set -- "velin-control:${APP_VERSION:-local}" "velin-capabilities:${APP_VERSION:-local}" "velin-frontend:${APP_VERSION:-local}" \
    velin-postgres:17-security.1 velin-temporal:1.8.3-security.1 velin-nats:2.14.6-security.1 \
    velin-prometheus:3.14.0-security.1 otel/opentelemetry-collector:0.160.0 \
    velin-grafana:13.2.2-security.1 alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0 "$scanner"
fi
result=0
for image in "$@"; do
  name=$(printf '%s' "$image" | tr '/:@' '___')
  docker image inspect --format '{{.Id}}' "$image" > ".local/security/$name.image-id"
  image_result=0
  sbom_result=0
  docker run --rm --cap-drop ALL --security-opt no-new-privileges \
    -v /var/run/docker.sock:/var/run/docker.sock:ro \
    -v "$PWD/.local/security:/reports" \
    -v "$PWD/.local/trivy-cache:/root/.cache/trivy" \
    "$scanner" image --image-src docker --scanners vuln \
    --severity HIGH,CRITICAL --exit-code 1 --format json \
    --output "/reports/$name.vulnerabilities.json" "$image" \
    2> >(tee ".local/security/$name.scan.log" >&2) || image_result=$?
  printf '%s\n' "$image_result" > ".local/security/$name.scan-exit-code"
  if [[ "$image_result" != 0 ]]; then
    result=1
  fi
  docker run --rm --cap-drop ALL --security-opt no-new-privileges \
    -v /var/run/docker.sock:/var/run/docker.sock:ro \
    -v "$PWD/.local/security:/reports" \
    -v "$PWD/.local/trivy-cache:/root/.cache/trivy" \
    "$scanner" image --image-src docker --format cyclonedx \
    --output "/reports/$name.sbom.cdx.json" "$image" || sbom_result=$?
  printf '%s\n' "$sbom_result" > ".local/security/$name.sbom-exit-code"
  if [[ "$sbom_result" != 0 ]]; then
    result=1
  fi
done
echo "Scan gate exit: $result. Reports include image IDs and real CycloneDX SBOMs in .local/security."
exit "$result"
