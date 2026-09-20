# Local validation and operational boundaries

This document separates implementation from executed checks. No cloud service
or deployment has been created; every result below was produced on the local
Compose topology or by offline validation, and the GitHub Actions workflow
repeats the same gates on an ephemeral runner.

## Executed verification

| Area | Check | Result |
| --- | --- | --- |
| Frontend | `npm run lint`, `npm run format:check`, `npm run test:unit`, `npm run build`, `npm audit --audit-level=high`, `node scripts/secret-scan.mjs` | Pass; 8 unit tests, 0 audit findings, 0 secret findings |
| Frontend browsers | `tests/site.spec.mjs` on desktop, tablet and mobile against the built frontend container: reveal, texture swap, idle trail, menu, sections, resize, reduced motion, controlled error without demo data | 12 passed |
| Frontend integration | `E2E_STACK=1` run of `tests/record.spec.mjs` and `tests/commission.spec.mjs` on desktop, tablet and mobile against the live stack: folio brief to revision-bound approval and delivered artifact; History section renders the published record | 6 passed (18 with the site suite) |
| Control plane | `gofmt`, `go vet`, `go test -race ./...` with `VELIN_INTEGRATION=1` against the stack's PostgreSQL; `velin replay` of the committed history | Pass; 7 integration tests, all unit tests, replay passed |
| Temporal | `VELIN_TEMPORAL_DEV=1`: worker killed mid-step and resumed on a second worker inside the same run, cancellation as an explicit terminal state, replay of the recorded history | 3 passed |
| Capability service | `uv run ruff check`, `ruff format --check`, `mypy`, `pytest -q`, evaluation corpus gate, regressed corpus must fail, OpenAPI validation | Pass; 34 tests; regressed gate exits 1 as required |
| Topology `normal` | Idempotent create and start, duplicate NATS hint, approval bound to the exact revision, artifact lineage, record event sequence | PASS |
| Topology `durability` | Pending approval survives stopping and restarting API and worker; approved afterwards | PASS |
| Topology `crash` | Worker `SIGKILL` during the strategy step; recovery on heartbeat timeout inside the same run; accepted steps reused; retry visible in real Temporal history | PASS |
| Topology `capabilities` | Provider failure and fallback evidence retained; malformed output never accepted; explicit `FAILED` terminal state | PASS |
| Topology `security` | Cross-owner 404s, malformed and oversized bodies, actor spoofing, stale approval before and after a human revision, rejection, cancellation, public-record redaction, no secrets in logs | PASS |
| Topology `outages` | NATS down: workflow advances, readiness unaffected; PostgreSQL down: live 200, ready 503; terminal evidence retried beyond three attempts and committed after recovery | PASS |
| Topology `events` | Forged and duplicated hints change nothing; SSE snapshots deduplicated; timeline cursor monotonic | PASS |
| Topology `observability` | One commission, one trace across API, worker and capability service with 13 named spans and unbroken parent linkage; Prometheus targets up; metric families present | PASS |
| Topology `runtime_smoke` | Graceful stop of API, worker, capabilities and frontend with exit code 0; healthy restart of the whole stack | PASS |
| Infrastructure | `actionlint`, `terraform fmt` and `validate` for dev, staging and prod, `kubectl kustomize` plus `kubeconform -strict` for each overlay | Pass; 21 resources valid per environment |
| Go dependencies | `govulncheck ./...` | 0 reachable; module-level `GO-2026-5932` documented in SECURITY.md |
| Image gate | `scripts/scan-images.sh` over the exact runtime, support and scanner images with `scripts/summarize-image-security.py` binding reports and SBOMs to image ids | 10 of 11 images at 0 HIGH / 0 CRITICAL (control, capabilities, frontend, PostgreSQL, Temporal, NATS, Prometheus, collector, init image, scanner); optional Grafana image fails on one HIGH, `CVE-2026-84445` in gRPC v1.83.1 inside the signed Prometheus datasource plugin v13.1.9, no upstream fix (see SECURITY.md) |

The `observability` phase failed before this round because `EventInput`
excluded its trace parent from JSON, so Temporal's data converter dropped it and
every stage and terminal span started a new trace. The field now serializes and
`TestActivityInputsCarryTraceParentThroughTemporalSerialization` round-trips
every activity input through the default converter and asserts that recorded
events, approvals and decisions keep the parent.

## Evidence

Detailed local output, real Temporal histories, image reports, SBOMs and browser
screenshots are kept outside the publishable source in ignored `.local/` and
`test-results/`. Results here are updated only after the corresponding command
succeeds.

## Operational limits

- Local Temporal is a real development server with SQLite persistence. It is
  suitable for integration and recovery testing, not a production deployment.
- Workflow execution has a 22-day deadline, at most three revisions and three
  review rounds. Each review lasts at most seven days. Terminal persistence
  retries until committed or the execution deadline. Operator termination or
  expiry of the entire execution cannot run workflow cleanup; inspect Temporal
  status and reconcile evidence before any manual recovery.
- Versioned terminal snapshots keep completed workflow observations queryable
  after Temporal history retention. Older executions without a snapshot require
  retained/archived Temporal histories; missing history is an explicit error,
  never a newly invented DRAFT state. The run ledger prevents restarting an
  already executed commission after history retention.
- NATS notifications are disposable. Only Temporal executes work; PostgreSQL
  stores accepted evidence. SSE reloads authoritative state and suppresses
  duplicate snapshots. Polling recovers notification loss.
- Capabilities use deterministic fixtures locally (`live: false`). Runtime
  templates are functional service assets. Optional external adapters require
  operator-supplied credentials; no live provider execution is claimed.
- Azure AKS, Blob, Key Vault, managed PostgreSQL, Temporal Cloud mTLS and Argo CD
  are configuration only. Real tenant/subscription, namespace, identities,
  certificates, domains and reviewed image digests are unavailable. Workloads
  intentionally have zero replicas and explicit configuration markers; the
  SecretProviderClass template lists the secret keys each workload needs.
- Telemetry records local observations only. Process counters may count repeated
  activity deliveries. The active-workflow gauge comes from Temporal visibility;
  use `max` across workers. Visibility can lag briefly behind execution.

## Security policy

The runtime and supporting-image gate remains zero HIGH and zero CRITICAL
findings, without suppression. Initial failures do not qualify as a pass.
Module-level findings in unimported Go packages are reported separately from
reachable runtime vulnerabilities. No private credentials or raw local scan
reports belong in published source.

Support images are rebuilt from pinned upstream sources with patched embedded
dependencies (`velin-temporal`, `velin-prometheus`, `velin-grafana`, the
`velin-trivy` scanner) and are scanned as the exact tags Compose runs. The
scanner image is local/CI tooling with Docker socket access and is never a
deployed workload.
