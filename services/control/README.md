# VELIN control plane

One Go binary, `velin`, runs as the API (`velin api`), the Temporal worker (`velin worker`) or the migration job (`velin migrate`). `velin health <loopback readiness url>` is the container probe and `velin replay <history.json>` replays an exported Temporal history against the current workflow code.

## What owns what

Temporal history owns execution: stage, retries, timers, the approval wait, cancellation and completion. PostgreSQL owns the append-only record: the immutable commission, every run, accepted step, revision, approval request, decision, artifact, provenance row and progress event. Core NATS carries disposable hints that only prompt a fresh query. The capability service receives no database credentials.

## Domain invariants

- A **run** is one Temporal execution of a commission. A resume after a worker crash stays inside the run; a later execution is a new attempt.
- A **step** has a stable id (`commission:capability:revision`). A retried activity first looks for an accepted row and reuses it, so a crash cannot produce a second accepted result.
- A **revision** id is derived from the commission, its number and the SHA-256 of the direction. Naming a revision names exact content; rebinding a number to different content is a conflict.
- An **approval request** binds a review round to one revision. A **decision** must cite the pending approval request and its revision; the workflow refuses anything else and PostgreSQL enforces the same binding with a composite foreign key, so a stale approval cannot be recorded even by a defective activity.
- An **artifact** cites the approved revision and the approving decision; the foreign key `(decision_id, revision_id)` makes an artifact for a different revision unrepresentable.
- **Provenance** rows are keyed by the output they describe and inserted with `ON CONFLICT DO NOTHING`, so replay or retry cannot record a second lineage.
- The stage graph in `domain.go` is closed: the workflow refuses any transition that is not listed, and every ending is an explicit terminal stage (`COMPLETED`, `REJECTED`, `EXPIRED`, `CANCELLED`, `FAILED`) with a terminal event.

## Recovery

Activities carry a 10-second heartbeat timeout and `RunCapability` heartbeats while the capability service works, so an abandoned attempt is retried on another worker within seconds rather than after the full start-to-close budget. `TestDevServerWorkflowResumesAfterWorkerCrashAndHistoryReplays` runs a real Temporal dev server, kills the first worker mid-step, proves a second worker completes the same run without repeating accepted steps, then replays the recorded history. The history is committed under `internal/platform/testdata/` and replayed by `TestReplayRecordedHistory` and by the `velin replay` command in CI.

## Configuration

Required: `DATABASE_URL`, `CAPABILITIES_TOKEN` (32 or more characters), and either `VELIN_API_KEYS` (local JSON token-to-subject mapping) or `OIDC_ISSUER` plus `OIDC_AUDIENCE`. Defaults: API `:8080`, worker `:8081`, capabilities `http://127.0.0.1:8000`, Temporal `127.0.0.1:7233`, namespace `default`, task queue `velin-commissions`, NATS `nats://127.0.0.1:4222`, `REVIEW_TIMEOUT` `168h`. Production requires HTTPS OIDC, no local tokens, PostgreSQL `sslmode=verify-full`, Temporal mTLS paths and `AZURE_BLOB_ENDPOINT`.

## Checks

```sh
go test ./...                       # unit and SDK workflow tests
VELIN_TEMPORAL_DEV=1 go test -run 'TestDevServer|TestReplay' ./internal/platform
VELIN_INTEGRATION=1 VELIN_TEST_ENV_FILE=/abs/path/.local/stack.env go test -race ./...   # real PostgreSQL
go vet ./... && govulncheck ./...
```

Integration tests create a uniquely named `velin_test_*` schema, migrate it, exercise every constraint and drop only that schema.
