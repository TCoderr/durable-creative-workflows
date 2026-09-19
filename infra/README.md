# Infrastructure and deployment boundary

This tree defines an Azure production target and a local Compose topology. Nothing here has provisioned Azure, Temporal Cloud or a Kubernetes cluster. Local Compose runs a real Temporal development server with a persisted SQLite volume, PostgreSQL, authenticated Core NATS, the capability service, the Go API and worker, the frontend, an OpenTelemetry collector, Prometheus and optional Grafana.

## Local

```sh
node scripts/local-env.mjs
COMPOSE_PARALLEL_LIMIT=1 docker compose --env-file .local/stack.env up --build -d
docker compose --env-file .local/stack.env ps
docker compose --env-file .local/stack.env logs --tail 100 api worker capabilities
docker compose --env-file .local/stack.env stop
```

`infra/docker/` holds the recipes: `control` (distroless static Go), `capabilities` (Python on Alpine with uv), `frontend` (distroless Node serving the built site through `scripts/serve.mjs`), and support images for PostgreSQL, NATS, Temporal, Prometheus and Grafana with patched dependencies. Grafana uses a rebuilt upstream server and the official signed Prometheus datasource; unused datasource plugins are excluded and automatic plugin downloads are disabled. Volume initializers use a digest-pinned Alpine image with a package inventory. Every Dockerfile base image is pinned by digest. `scripts/scan-images.sh` scans the exact images with Trivy for HIGH and CRITICAL findings and writes CycloneDX SBOMs; the gate has no suppressions.

`scanner.Dockerfile` rebuilds the pinned Trivy release with patched dependencies.
The scan script builds and scans that tooling image as well. It has no Compose
service or Kubernetes workload and is never part of application deployment.

## Terraform

`infra/terraform/modules/platform` describes a private AKS cluster with workload identity, Premium ACR, Azure Database for PostgreSQL Flexible Server with Entra-only authentication, a private Blob container for artifacts, Key Vault with RBAC, private endpoints and DNS, Log Analytics and Application Insights. Environments `dev`, `staging` and `prod` instantiate it with independent state and identities. Validate offline with `bash scripts/validate-infra.sh`; plan only with real backend and identity configuration supplied out of band, and apply only after review of the saved plan.

## Kubernetes and GitOps

`kubectl kustomize infra/kubernetes/overlays/<env>` renders the workloads. All deployments intentionally have zero replicas and `REQUIRES_` markers until real image digests, identity client ids, Temporal Cloud address, Blob endpoint and secrets are supplied through the SecretProviderClass template, which lists the secret keys each workload requires (the capability service runs `VELIN_PROVIDER=http` in the cluster and needs its HTTPS endpoint, key and model). Namespaces enforce restricted pod security; a network policy limits ingress to the frontend from the ingress namespace and metrics from the observability namespace, and egress to DNS, the collector, private services, Temporal Cloud and HTTPS. Argo CD's `AppProject` authorizes no repository by default; the `Application` template is rendered by an operator with a reviewed commit. Argo CD reconciles application resources, never workflow state.

Promote identical image digests through environments; `scripts/prepare-promotion.py` validates a release manifest without building, pushing or applying anything.

## Telemetry and recovery

The local collector logs spans; production must configure an authenticated exporter and retention. PostgreSQL production backups retain 14 days and Blob versions 30 days; restore to a new server, verify migrations and invariants, then switch endpoints through an approved change. Reconcile the PostgreSQL record with Temporal histories after any point-in-time restore before resuming workers.
