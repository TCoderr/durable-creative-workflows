# VELIN architecture

VELIN runs creative commissions as durable workflows. The public page is the editorial identity; `/commission/` is the private folio; behind them sit one Go control plane (API and Temporal worker as two roles of one binary), one stateless Python capability service, PostgreSQL, a Temporal server, Core NATS and private content-addressed artifact storage.

## Truth and transactions

Temporal history owns execution: stage, retries, timers, the approval wait, cancellation and completion. PostgreSQL owns the append-only record: the immutable commission, runs, accepted steps, revisions, approval requests, decisions, artifacts, provenance rows and progress events. There is no workflow-status column; status is a Temporal query. NATS carries disposable hints that only prompt the API to query again; a forged or duplicated hint cannot change what a client sees. Object storage holds artifact JSON addressed by SHA-256.

Every durable effect has a stable identifier and is written with `ON CONFLICT DO NOTHING` inside a transaction with its provenance row and its event. A retried activity therefore observes the earlier effect instead of repeating it. The domain boundaries are:

| Entity | Identity | Owner of truth |
| --- | --- | --- |
| Commission | UUID, also the Temporal workflow ID | PostgreSQL |
| Run | derived from commission and Temporal run ID; attempt numbered per commission | PostgreSQL (recorded by the workflow's first activity) |
| Step | `commission:capability:revision` | PostgreSQL |
| Revision | derived from commission, number and the direction hash | PostgreSQL |
| Approval request | derived from commission and round; binds one revision | Temporal state while pending, PostgreSQL once recorded |
| Decision | owner-supplied id, unique per approval request; must cite the approval's revision (composite foreign key) | PostgreSQL |
| Artifact | derived from commission; cites revision and decision (composite foreign key) | PostgreSQL plus object store |
| Provenance | keyed by output reference | PostgreSQL |
| Event | stable event id; monotonic sequence | PostgreSQL |

## Execution

The workflow runs eight capability steps (brief, research, strategy, art direction, typography, motion, imagery, critique), records a revision, lets critique request bounded revisions, then records an approval request and waits. A decision is accepted only if it validates, comes from the owner, names the pending approval and its revision, and, for approval, passes the deterministic production gate. Approval leads to artifact production and `COMPLETED`; revision loops back within a budget of three revisions and three rounds; rejection, expiry, cancellation and failure are explicit terminal stages. The stage graph is a closed table; the workflow refuses any other move.

No model chooses a transition, executes a tool or owns approval. The capability service returns proposals with provenance and checks; the control plane revalidates identity and provenance before accepting a step, and the production gate re-reads accepted rows rather than trusting workflow memory.

## Recovery

Worker restarts are the normal case, not an exception. Activities heartbeat, so an abandoned attempt is retried within seconds on any live worker; accepted steps are reused by id; the pending approval survives in Temporal state and in PostgreSQL. A real dev-server test kills a worker mid-step and proves a second worker completes the same run without repeating accepted work, then replays the recorded history. Replay of a committed history fixture runs in every test run so a non-deterministic change to the workflow fails before it ships.

## Frontend

The public page's History section reads a published record through `/api/v1/public/records`; it renders a controlled error when the service is unavailable and never falls back to demo data. The folio uses the authenticated API through a same-origin proxy and always submits the pending approval and revision identifiers with a decision.

See [Temporal](TEMPORAL.md), [security](SECURITY.md), [threat model](THREAT-MODEL.md), [observability](docs/OBSERVABILITY.md), [runbook](RUNBOOK.md), [API contracts](docs/api/README.md) and [decisions](docs/decisions/).
