/**
 * workflow/model.js — shapes of the workflow record.
 *
 * These typedefs describe the authoritative record projected by api-source.js.
 * A WorkflowSource resolves these shapes through the same three methods.
 */

/** @typedef {'received'|'running'|'waiting'|'completed'|'rejected'|'expired'|'cancelled'|'failed'} WorkflowStatus */
/** @typedef {'pending'|'running'|'waiting'|'completed'|'failed'|'skipped'} StepStatus */
/** @typedef {'pending'|'decided'|'expired'|'cancelled'|'failed'|'rejected'} ApprovalStatus */

/**
 * @typedef {Object} Commission
 * @property {string} id            Stable public identifier, e.g. "C-0007".
 * @property {string} title
 * @property {string} kind          e.g. "Identity system".
 * @property {string} brief         One-paragraph summary of intent.
 * @property {string[]} approvers   Named roles, never accounts.
 * @property {string} receivedAt    ISO 8601.
 */

/**
 * @typedef {Object} Workflow
 * @property {string} id
 * @property {string} commissionId
 * @property {WorkflowStatus} status
 * @property {string|null} currentStepId
 * @property {string} startedAt
 * @property {string} updatedAt
 */

/**
 * @typedef {Object} Run
 * @property {string} id
 * @property {string} workflowId
 * @property {number} attempt       Execution ordinal; worker recovery stays in the same run.
 * @property {'recorded'} status
 * @property {string} startedAt
 * @property {string|null} endedAt
 * @property {string} reason        Initial or a later execution of the commission.
 */

/**
 * @typedef {Object} Step
 * @property {string} id
 * @property {string} runId
 * @property {number} index         1-based position within the workflow.
 * @property {string} name          Producing capability.
 * @property {StepStatus} status
 * @property {number} attempts
 * @property {string|null} startedAt
 * @property {string|null} endedAt
 * @property {string|null} revisionId  Revision produced by this step, if any.
 */

/**
 * @typedef {Object} Revision
 * @property {string} id
 * @property {string} workflowId
 * @property {number} number
 * @property {string|null} parentId
 * @property {string} summary
 * @property {string} createdAt
 * @property {string[]} artifactIds
 */

/**
 * @typedef {Object} Approval
 * @property {string} id
 * @property {string} revisionId    The exact revision under review.
 * @property {string} approver      Named role.
 * @property {ApprovalStatus} status
 * @property {string} requestedAt
 * @property {string|null} decidedAt
 */

/**
 * @typedef {Object} Decision
 * @property {string} id
 * @property {string} approvalId
 * @property {string} revisionId
 * @property {string} actor
 * @property {'approve'|'reject'|'revise'} outcome
 * @property {string} note
 * @property {string} at
 */

/**
 * @typedef {Object} Artifact
 * @property {string} id
 * @property {string} revisionId
 * @property {string} kind          e.g. "identity-system", "motion-spec".
 * @property {string} title
 * @property {string|null} contentHash  Recorded by the backend at delivery.
 * @property {string} createdAt
 */

/**
 * @typedef {Object} Provenance
 * @property {string|null} artifactId
 * @property {string} outputRef
 * @property {string} runId
 * @property {string|null} stepId
 * @property {string} commissionId
 * @property {string} revisionId
 * @property {string} decisionId
 * @property {string[]} stepIds     Accepted inputs; failures remain progress events.
 * @property {string[]} sourceIds   Curated references, if any.
 */

/**
 * @typedef {Object} ProgressEvent
 * @property {string} id
 * @property {string} workflowId
 * @property {string} at            ISO 8601.
 * @property {string} type          Dotted noun.verb, e.g. "step.failed".
 * @property {'commission'|'run'|'step'|'failure'|'recovery'|'approval'|'decision'|'revision'|'artifact'} kind
 * @property {string} subject       Human-readable target, e.g. "Revision r.04".
 * @property {string} detail
 */

/**
 * @typedef {Object} WorkflowRecord
 * @property {Commission} commission
 * @property {Workflow} workflow
 * @property {Run[]} runs
 * @property {Step[]} steps
 * @property {Revision[]} revisions
 * @property {Approval[]} approvals
 * @property {Decision[]} decisions
 * @property {Artifact[]} artifacts
 * @property {Provenance[]} provenance
 * @property {ProgressEvent[]} events   Chronological.
 */

/**
 * @typedef {Object} WorkflowSource
 * @property {() => Promise<Commission[]>} listCommissions
 * @property {(commissionId: string) => Promise<WorkflowRecord|null>} getRecord
 * @property {(workflowId: string, onEvent: (event: ProgressEvent) => void) => () => void} subscribe
 *   Returns an unsubscribe function.
 */

const REQUIRED = ['listCommissions', 'getRecord', 'subscribe'];

/**
 * Wrap a source so the page depends on this surface and nothing else.
 * @param {WorkflowSource} source
 * @returns {WorkflowSource}
 */
export function createWorkflowClient(source) {
  for (const name of REQUIRED) {
    if (typeof source?.[name] !== 'function') {
      throw new TypeError(`WorkflowSource is missing ${name}()`);
    }
  }
  return {
    listCommissions: () => source.listCommissions(),
    getRecord: (commissionId) => source.getRecord(commissionId),
    subscribe: (workflowId, onEvent) => source.subscribe(workflowId, onEvent),
  };
}

/**
 * Counts the page shows beside a record. Derived, never stored.
 * @param {WorkflowRecord} record
 */
export function summarize(record) {
  const failures = new Set();
  for (const event of record.events) {
    const data = event.data ?? {};
    if (event.type === 'step.failed')
      failures.add(`${data.step_id}:${data.attempt}`);
    if (event.type === 'step.attempted') {
      for (let attempt = 1; attempt < Number(data.attempt); attempt++) {
        failures.add(`${data.step_id}:${attempt}`);
      }
    }
  }
  const approvers = new Set(record.approvals.map((a) => a.approver));
  return {
    revisions: record.revisions.length,
    approvers: approvers.size,
    decisions: record.decisions.length,
    failures: failures.size,
    resumes: Math.max(0, record.runs.length - 1),
    artifacts: record.artifacts.length,
    status: record.workflow.status,
  };
}
