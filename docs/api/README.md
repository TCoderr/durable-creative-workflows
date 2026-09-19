# Implemented API contracts

[`openapi.json`](openapi.json) documents the Go control-plane routes exactly as implemented; `validate.py` fails on any drift from `api.go`. [`capabilities-openapi.json`](capabilities-openapi.json) documents the private capability service with schema-derived typed outputs. Both use OpenAPI 3.1.

The browser talks to the same-origin `/api/v1/` proxy (Vite in development, `scripts/serve.mjs` in the container). The Go API listens on `http://127.0.0.1:8080`; the capability service is `http://127.0.0.1:18000` on the host and `http://capabilities:8000` inside Compose. The capability bearer never reaches a browser.

## Flow

1. `POST /api/v1/commissions` with an `Idempotency-Key`, then `POST /api/v1/commissions/{id}/start`. The commission UUID is the Temporal workflow ID.
2. Read `GET /api/v1/workflows/{id}` (Temporal snapshot) or `GET /api/v1/workflows/{id}/record` (snapshot plus runs, steps, revisions, approvals, decisions, artifacts, provenance and events). `GET .../events` streams state snapshots; hints only trigger fresh queries.
3. At `WAITING_FOR_APPROVAL` the snapshot carries `pending_approval` with `id` and `revision_id`. `POST /api/v1/workflows/{id}/decisions` must cite both. A decision naming any other approval or revision is `409 STALE_APPROVAL` and the response carries the pending request. `APPROVE` also requires `can_approve`. A `202` is signal delivery; the workflow validates the decision again before recording it.
4. `GET /api/v1/artifacts/{id}` downloads the artifact bound to the approved revision; `GET /api/v1/provenance/{id}` returns the full lineage.
5. `POST /api/v1/commissions/{id}/publications` makes a redacted record readable without authentication at `GET /api/v1/public/records/{publication_id}`; the public page renders that record.

Ownership comes from the authenticated subject on every private route; other subjects receive 404. Rate limiting is per subject, and per client address for public routes.

`GET /api/v1/health/live` and `GET /api/v1/health/ready` are unauthenticated versioned probes. The original `/health/` aliases remain for container and Kubernetes probes. After Temporal history retention, completed workflows can use the immutable terminal observation stored by the workflow in PostgreSQL. Missing history for an older started execution is an explicit error, never a DRAFT fallback.

## Regenerate

```sh
cd services/capabilities
uv run python ../../docs/api/build.py
uv run --with openapi-spec-validator==0.9.0 python ../../docs/api/validate.py --standards
```
