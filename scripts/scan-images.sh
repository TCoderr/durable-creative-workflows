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
# The scanner runs as the invoking user with every capability dropped, so the
# report and cache directories it writes are its own; the Docker socket's group
# is added for the image source. No capability is granted back.
run_scanner() {
  docker run --rm --cap-drop ALL --security-opt no-new-privileges \
    --user "$(id -u):$(id -g)" --group-add "$(stat -c %g /var/run/docker.sock)" \
    -e HOME=/tmp -e TRIVY_CACHE_DIR=/cache \
    -v /var/run/docker.sock:/var/run/docker.sock:ro \
    -v "$PWD/.local/security:/reports" \
    -v "$PWD/.local/trivy-cache:/cache" \
    "$scanner" image --image-src docker "$@"
}
result=0
summary=()
for image in "$@"; do
  name=$(printf '%s' "$image" | tr '/:@' '___')
  docker image inspect --format '{{.Id}}' "$image" > ".local/security/$name.image-id"
  image_result=0
  sbom_result=0
  run_scanner --scanners vuln --severity HIGH,CRITICAL --exit-code 1 --format json \
    --output "/reports/$name.vulnerabilities.json" "$image" \
    2> >(tee ".local/security/$name.scan.log" >&2) || image_result=$?
  printf '%s\n' "$image_result" > ".local/security/$name.scan-exit-code"
  if [[ "$image_result" != 0 ]]; then
    result=1
  fi
  run_scanner --format cyclonedx --output "/reports/$name.sbom.cdx.json" "$image" || sbom_result=$?
  printf '%s\n' "$sbom_result" > ".local/security/$name.sbom-exit-code"
  if [[ "$sbom_result" != 0 ]]; then
    result=1
  fi
  summary+=("$(printf '%-60s scan-exit=%s sbom-exit=%s' "$image" "$image_result" "$sbom_result")")
done
printf '%s\n' "Scan gate summary (exit 1 = HIGH/CRITICAL finding or scanner error; see the JSON report):" "${summary[@]}"
echo "Scan gate exit: $result. Reports include image IDs and real CycloneDX SBOMs in .local/security."
exit "$result"
