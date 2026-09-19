# Temporal execution contract

The Go `DurableCommission` workflow runs on task queue `velin-commissions`. `velin worker` registers the workflow and activities; `velin api` starts, queries and signals executions. The Python service is a capability invoked from activities, never a second workflow engine.

## Stages and bounds

`DRAFT → BRIEF_ANALYSIS → RESEARCH → STRATEGY → DIRECTION → TYPOGRAPHY → MOTION → IMAGERY → CRITIQUE → (REVISION → CRITIQUE)* → WAITING_FOR_APPROVAL → APPROVED → PRODUCTION → COMPLETED`. `REVISE` returns to `REVISION`; `REJECT` ends in `REJECTED`; the review timer ends in `EXPIRED`; cancellation ends in `CANCELLED`; exhausted retries or invalid output end in `FAILED`. Terminal stages have no outgoing transitions, and every ending records a `workflow.<stage>` event with the outcome and duration. The five product movements (Brief, Execute, Review, Approve, Continue) are a projection of these stages.

Capability activities have a 55-second start-to-close timeout, four-minute schedule-to-close timeout, 10-second heartbeat timeout and three attempts with exponential backoff capped at 15 seconds. Validation, provenance and domain conflicts are non-retryable. Revisions and approval rounds are each limited to three. The approval timer defaults to and cannot exceed seven days (`REVIEW_TIMEOUT`); the execution timeout is 22 days. Bounds keep histories finite; Continue-As-New is not needed.

Terminal evidence uses a disconnected context and retries without an attempt limit, with a 30-second attempt timeout and backoff capped at one minute. The workflow does not report `terminal: true` until that write commits. The execution deadline remains the outer limit. Operator termination or execution timeout requires reconciliation because neither can execute workflow cleanup.

## Signals, queries and binding

The `human-decision` signal carries the decision id, the approval request id, the revision id, the action, a reason code, a reason and the server-derived actor. The `state` query returns the snapshot including `pending_approval`. The workflow deduplicates decision ids, then requires: valid shape, owner as actor, exact approval and revision identifiers, and `can_approve` for approval. Anything else is counted in `stale_decisions` and ignored. The first valid decision wins; it is persisted by an activity whose database write is guarded by the same binding.

The internal `decision-receipt` query returns a content fingerprint for an
accepted signal. An exact HTTP retry can therefore be acknowledged while the
decision activity is still committing; reuse of the identifier with changed
content conflicts. Receipt fingerprints are reconstructed from Temporal history
and are never included in public records.

## Delivery and restart

The API starts the workflow with the commission UUID and reject-duplicate reuse. The immutable PostgreSQL run ledger also rejects re-execution after Temporal history retention. Steps have stable ids and are reused after a crash; the capability service may execute again, but only one accepted result per step and one artifact per commission can exist. Heartbeats let the server notice a dead worker in seconds. Worker shutdown drains for a bounded period; a hard crash leaves retry to Temporal.

New terminal events include an immutable, redacted state snapshot. When Temporal reports that a retained history no longer exists, the API can return this exact observation from PostgreSQL. A previously started execution with neither history nor a terminal snapshot produces a controlled error. Retain or archive histories for older executions without snapshots.

## Replay and versioning

Workflow code uses only Temporal time, timers, selectors and pure hashing. `WorkflowVersion` in `domain.go` names the current logic; any change that reorders commands must add a `workflow.GetVersion` branch and keep the previous branch until open executions drain. `internal/platform/testdata/durable-commission-resume.json` is a real history captured from the crash-recovery test; `TestReplayRecordedHistory`, the CI `velin replay` step and the SDK metric `temporal_workflow_task_execution_failed{failure_reason="NonDeterminismError"}` are the replay gates. Never reset or delete production histories as a deployment mechanism.

Local Compose runs the Temporal development server with a persisted SQLite volume for verification only. Production targets a provisioned Temporal Cloud namespace with mTLS material injected from Key Vault; nothing here claims live Temporal Cloud use.
