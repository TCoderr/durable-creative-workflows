# Frontend seams and their backend

The public page's History section and the private folio read the control plane through these seams. The record shown on the page is authoritative: workflow state from Temporal, the append-only record from PostgreSQL.

## Data contract

`workflow/model.js` declares the entities the page understands (Commission, Workflow, Run, Step, Revision, Approval, Decision, Artifact, Provenance, ProgressEvent) and the `WorkflowSource` interface: `listCommissions()`, `getRecord(id)`, `subscribe(id, onEvent)`. `createWorkflowClient(source)` validates a source; `summarize(record)` derives the counts shown beside a record.

## Sources

| Module | Role |
| --- | --- |
| `workflow/api-source.js` | The production source. Lists `/api/v1/public/records`, reads `/api/v1/public/records/{publication}`, maps the control-plane `WorkflowRecord` onto the page's shape, polls while the workflow is open, and raises `ApiError` with the service's code on failure. |
| `tests/fixtures/public-record.json` | Contract fixture used only by browser tests; never included in the production bundle. |

## Markers in the page

| Selector | Behaviour |
| --- | --- |
| `[data-seam="workflow-record"]` | Mount for the API record. `data-record` or `?record=<publication id>` selects a record, otherwise the newest publication. Service failures render a controlled notice with the error code. |
| `[data-seam="commissions"]` | The Commissions section; static editorial cases. |
| `[data-seam="revisions"]` | The History section around the record. |
| `[data-seam="commissions.intake"]` | The Intake section; its actions link to `/commission/`, the authenticated folio. |

## Folio

`commission/` is the private folio: token access, brief submission, live progress through the SSE hint stream, the revision under review with its approval binding, decisions that always cite `pending_approval.id` and `pending_approval.revision_id`, the record, provenance, artifact download and publication of the record to the public page. All strings are rendered as text.

## Rules the integration keeps

No live execution is faked; a fixture provider is labelled `live: false`. Decisions are recorded against exact revisions. Failures appear in the record. Public records omit the owner and the direction body. The page never invents data when the service is down.
