# Runbook

## Local stack

Prerequisites: Node 24 to 26, Docker Engine with the Compose plugin, Go 1.26 and Python 3.12 to 3.14 with uv for host-side checks. On Windows, run Docker commands inside a WSL2 Ubuntu distribution with the repository mounted under `/mnt/<drive>/…`.

```sh
npm ci
npm run local:env                                  # ignored credentials in .local/
COMPOSE_PARALLEL_LIMIT=1 docker compose --env-file .local/stack.env --profile observability build
docker compose --env-file .local/stack.env up -d
docker compose --env-file .local/stack.env ps
```

| Surface | Local address |
| --- | --- |
| Frontend (built, proxied) | `http://127.0.0.1:4173` |
| Frontend (Vite, proxied) | `http://127.0.0.1:5173` after `npm run dev` |
| Control-plane API | `http://127.0.0.1:8080` (`/health/ready`) |
| Worker readiness | `http://127.0.0.1:8081/health/ready` |
| Capability service | `http://127.0.0.1:18000/health/ready` (container port 8000) |
| Temporal UI / gRPC | `http://127.0.0.1:8233` / port 7233 |
| NATS monitoring / client | `http://127.0.0.1:8222/healthz` / port 4222 |
| PostgreSQL | port 5432 |
| Prometheus | `http://127.0.0.1:9090` |
| Grafana (profile `observability`) | `http://127.0.0.1:3001/d/velin-runtime` |
| OTLP collector | port 4318 |

All published ports bind loopback. `.local/test-access.json` holds two local test identities; use the first in the folio. Never commit, paste or bundle `.local/`. Stopping the stack retains volumes; no documented command deletes them.

## Verification

```sh
npm run lint && npm run format:check && npm run test:unit && npm run build
npx playwright test tests/site.spec.mjs                    # frontend alone, mocked record API
E2E_STACK=1 npx playwright test --project=desktop           # frontend record and folio flow against the stack
python scripts/verify-topology.py --phase normal            # then durability, crash, capabilities, security, outages, events, observability, runtime_smoke
cd services/control && VELIN_INTEGRATION=1 VELIN_TEST_ENV_FILE=/abs/.local/stack.env go test -race ./...
cd services/control && VELIN_TEMPORAL_DEV=1 go test -run 'TestDevServer|TestReplay' ./internal/platform
cd services/capabilities && uv run pytest -q && uv run python -m velin_capabilities.eval_cli
bash scripts/scan-images.sh                                 # Trivy, HIGH and CRITICAL gate, SBOMs under .local/security
bash scripts/validate-infra.sh                              # Terraform fmt/validate, kustomize, kubeconform
```

Topology phases stop and restart only VELIN containers and write results to `.local/verification/`. Run them serially with no other active local commissions. On Windows set `VELIN_WSL_DISTRO` to the distribution that runs Docker; the script then issues Compose commands through WSL.

The first image build compiles Temporal, Prometheus and Grafana from source to
patch their embedded dependencies. Build services serially on machines with
limited memory. The Go compiler uses bounded parallelism; Grafana remains a
large build. The Grafana image includes only the official signed Prometheus
datasource used by the provisioned dashboard.

## Operating notes

- **Worker restart or crash.** Nothing to do. Open executions resume from history; `velin_worker_open_workflows_at_start` reports how many were found on start. The interrupted step is retried after the 10-second heartbeat timeout and accepted steps are reused.
- **Approval waiting too long.** The workflow waits up to `REVIEW_TIMEOUT` (default seven days) and then ends in `EXPIRED` with an `approval.expired` event. A decision arriving for an expired or superseded approval is refused as `STALE_APPROVAL`.
- **Capability service unavailable.** Steps fail with `CAPABILITY_TRANSPORT_UNAVAILABLE`, are recorded as `step.failed` events and retried within the activity budget; the worker reports not-ready until the service returns.
- **PostgreSQL unavailable.** API readiness fails; liveness stays up; the workflow resumes when the database returns.
- **NATS unavailable.** Readiness is unaffected; progress hints are lost and the API falls back to its polling interval.
- **Changing workflow code.** Bump `WorkflowVersion`, add a `workflow.GetVersion` branch, run the replay tests, and keep the previous worker until open executions drain.
- **Migrations.** Embedded, transactional, checksum-protected and advisory-locked. Never edit an applied migration; add another file.
- **WSL sessions.** On Windows, WSL stops the distribution shortly after the last Windows-initiated session closes, which also stops the containers and the loopback port relay. Keep one session open (for example `wsl -d <distro> -- sleep 3600`) while host-side browser tests run against the stack.
- **Port collisions.** Set the corresponding `VELIN_PORT_*` value in `.local/stack.env`; container ports stay fixed. Set `E2E_API_URL` and `E2E_BASE_URL` to those host addresses for browser tests.

## Production boundaries

Production requires HTTPS OIDC, no local tokens, PostgreSQL `sslmode=verify-full` with Entra authentication, Temporal Cloud mTLS, Azure Blob for artifacts and an authenticated OTLP exporter. The Terraform, Kubernetes and Argo CD definitions under `infra/` describe that target and validate offline; nothing in this repository has deployed it.
