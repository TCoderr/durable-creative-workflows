# VELIN — Durable Creative Workflows

VELIN runs creative commissions as durable workflows: a brief becomes a run of typed steps, a reviewed revision, a human decision bound to exactly that revision, and a delivered artifact with its complete record. Execution survives worker crashes and supports review waits of up to seven days per round.

The public page is an editorial single page with one WebGL moment; its History section renders a published commission's authoritative record. `/commission/` is the private folio. Behind them: a Go control plane (API and Temporal worker), a stateless Python capability service, PostgreSQL, Temporal, Core NATS, OpenTelemetry and Prometheus, with Grafana as an optional local dashboard.

## Key capabilities

- **Temporal orchestration** of long-running commissions: eight typed capability steps, bounded critique revisions, a durable approval wait, and a closed stage graph with explicit terminal states.
- **Worker restart recovery**: activities heartbeat, an abandoned attempt is retried on any live worker inside the same run, accepted steps are reused by stable id, and a committed history is replayed in every test run.
- **Revision-bound approvals**: a decision must cite the pending approval request and its exact revision; the API, the workflow and PostgreSQL composite foreign keys each refuse anything else, so a **stale approval is rejected** before and after a revision.
- **Artifact provenance**: every step, revision, approval, decision and artifact carries a provenance row keyed by its output; the artifact cites the approved revision and approving decision.
- **PostgreSQL authoritative state**: the append-only record of commissions, runs, steps, revisions, approvals, decisions, artifacts, provenance and events, written once with stable identifiers.
- **NATS transient notifications** that only prompt a fresh authoritative query, and **SSE live progress** with duplicate-hint suppression and a monotonic timeline cursor.
- **Go control plane** (`velin api`, `velin worker`, `velin migrate`, `velin replay`) and a **Python/FastAPI capability service** with versioned, hash-pinned runtime prompt templates, strict output schemas and deterministic evaluation.
- **OpenTelemetry, Prometheus and Grafana**: one trace per commission across API, worker and capability service; application and Temporal SDK metrics; a provisioned dashboard.
- **Docker Compose, Kubernetes, Terraform and Argo CD** definitions for a local topology and an Azure target.

## Status

- Validated locally: unit, integration, real Temporal dev-server crash recovery and replay, nine topology phases against the Compose stack (normal, durability, crash, capabilities, security, outages, events, observability, runtime smoke), browser flows for the public page and the folio, Terraform and Kubernetes validation, dependency and image scans. See [docs/VALIDATION.md](docs/VALIDATION.md).
- **Cloud deployment was not performed.** The Azure, Temporal Cloud, Kubernetes and Argo CD definitions validate offline only; nothing has been provisioned or deployed.
- **Live external provider execution was not exercised.** The capability service ran with deterministic fixtures labelled `live: false`; the HTTPS provider adapter requires operator-supplied credentials.
- **Grafana is optional** (Compose profile `observability`, never a Kubernetes workload) and currently carries **one known, unsuppressed HIGH finding**: `CVE-2026-84445` in `google.golang.org/grpc` v1.83.1, fixed in 1.83.2, embedded in the official signed Prometheus datasource plugin v13.1.9. No newer fixed signed plugin release exists upstream yet, no suppression or threshold change is used, and the image gate therefore reports FAIL for that one image. 10 of 11 images (all runtime and required supporting images, the init image and the scanner) scan at 0 HIGH / 0 CRITICAL. This project is not fully security-clean until that plugin is updated. See [SECURITY.md](SECURITY.md).

## Run locally

```sh
npm ci
npm run local:env
COMPOSE_PARALLEL_LIMIT=1 docker compose --env-file .local/stack.env up --build -d
npm run dev        # http://127.0.0.1:5173, API proxied to the stack
```

On Windows run the Docker commands inside a WSL2 distribution. See the [runbook](RUNBOOK.md) for ports, verification phases and operating notes.

## Repository

| Path | Role |
| --- | --- |
| `index.html`, `styles.css`, `script.js`, `shaders.js`, `navigation.js` | The public page and its fluid-trail reveal |
| `workflow/` | Frontend data model, API source and record renderer |
| `commission/` | Private folio: brief, review, decisions, record, publication |
| `services/control/` | Go control plane: domain, workflow, activities, repository, API |
| `services/capabilities/` | Python capability service with typed contracts and deterministic fixtures |
| `infra/` | Container recipes, observability, Kubernetes, Terraform, GitOps |
| `scripts/` | Local credentials, static server, verification, scans, validation |
| `tests/` | Playwright and Node unit tests |
| `docs/` | API contracts, observability, validation, decisions |

## Guarantees the code enforces

- Every step, revision, approval, decision, artifact, provenance row and event has a stable identifier and is written once; retries and replays reuse it.
- A decision applies only to the exact revision the workflow is waiting on, checked by the API, by the workflow and by PostgreSQL foreign keys.
- Workflow stages form a closed graph with explicit terminal states; every ending is recorded.
- A worker crash mid-step is recovered by heartbeat timeout and retry on any live worker, inside the same run; a real dev-server test proves it and replays the recorded history.
- The page never shows invented data: when the service is unavailable it shows a controlled error.

Read [ARCHITECTURE.md](ARCHITECTURE.md), [TEMPORAL.md](TEMPORAL.md), [SECURITY.md](SECURITY.md), [THREAT-MODEL.md](THREAT-MODEL.md), [SEAMS.md](SEAMS.md), [ASSETS.md](ASSETS.md), [docs/OBSERVABILITY.md](docs/OBSERVABILITY.md), [docs/api/README.md](docs/api/README.md) and [infra/README.md](infra/README.md).

## Checks

```sh
npm run lint && npm run format:check && npm run test:unit && npm run build
npx playwright test tests/site.spec.mjs
cd services/control && go test ./...
cd services/capabilities && uv run pytest -q
```

Stack-level verification (topology phases, PostgreSQL and Temporal integration, browser flows, image scans, infrastructure validation) is described in the runbook.
