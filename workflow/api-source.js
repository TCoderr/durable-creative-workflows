/**
 * workflow/api-source.js — the WorkflowSource over the control-plane API.
 *
 * Reads published workflow records from `/api/v1/public/records`. Every
 * field the page shows comes from the authoritative response: Temporal state
 * for the workflow, PostgreSQL for the append-only record. Nothing is
 * synthesised on the client, and an unreachable service surfaces as an
 * ApiError that the renderer shows as a controlled error.
 *
 * @typedef {import('./model.js').WorkflowSource} WorkflowSource
 * @typedef {import('./model.js').WorkflowRecord} WorkflowRecord
 */

export class ApiError extends Error {
  /**
   * @param {number} status
   * @param {string} code
   * @param {string} message
   */
  constructor(status, code, message) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

const TERMINAL = new Set([
  'COMPLETED',
  'REJECTED',
  'EXPIRED',
  'CANCELLED',
  'FAILED',
]);

/**
 * @param {{ baseUrl?: string, fetchImpl?: typeof fetch, pollMs?: number }} [options]
 * @returns {WorkflowSource & { terminal: (record: WorkflowRecord) => boolean }}
 */
export function createApiSource({
  baseUrl = '/api/v1',
  fetchImpl = (...args) => fetch(...args),
  pollMs = 5000,
} = {}) {
  async function get(path) {
    let response;
    try {
      response = await fetchImpl(baseUrl + path, {
        headers: { Accept: 'application/json' },
        cache: 'no-store',
        credentials: 'omit',
        signal: AbortSignal.timeout(15000),
      });
    } catch {
      throw new ApiError(
        0,
        'SERVICE_UNREACHABLE',
        'The workflow service could not be reached.',
      );
    }
    const body = await response.json().catch(() => null);
    if (!response.ok) {
      const code =
        body && typeof body.code === 'string'
          ? body.code
          : 'HTTP_' + response.status;
      const message =
        body && typeof body.message === 'string'
          ? body.message
          : 'The workflow service returned an error.';
      throw new ApiError(response.status, code, message);
    }
    if (!body || typeof body !== 'object') {
      throw new ApiError(
        response.status,
        'INVALID_RESPONSE',
        'The workflow service returned an unexpected response.',
      );
    }
    return body;
  }

  return {
    async listCommissions() {
      const body = await get('/public/records');
      if (!Array.isArray(body.items)) throw invalidRecord();
      const items = body.items;
      return items.map((item) => ({
        id: String(item.publication_id ?? ''),
        commissionId: String(item.commission_id ?? ''),
        title: String(item.title ?? 'Untitled commission'),
        kind: '',
        brief: '',
        approvers: [],
        receivedAt: String(item.published_at ?? ''),
      }));
    },
    async getRecord(publicationId) {
      const body = await get(
        '/public/records/' + encodeURIComponent(publicationId),
      );
      return mapRecord(body);
    },
    subscribe(publicationId, onEvent) {
      let active = true;
      const tick = async () => {
        if (!active) return;
        try {
          const record = await this.getRecord(publicationId);
          if (!active) return;
          onEvent({
            id: 'poll:' + Date.now(),
            workflowId: record.workflow.id,
            at: new Date().toISOString(),
            type: 'record.refreshed',
            kind: 'run',
            subject: 'Record',
            detail: '',
            record,
          });
          if (record.workflow.terminal) active = false;
        } catch (error) {
          onEvent({
            id: 'poll:error:' + Date.now(),
            workflowId: publicationId,
            at: new Date().toISOString(),
            type: 'record.unavailable',
            kind: 'failure',
            subject: 'Record',
            detail: '',
            error,
          });
        }
        if (active) timer = setTimeout(tick, pollMs);
      };
      let timer = setTimeout(tick, pollMs);
      return () => {
        active = false;
        clearTimeout(timer);
      };
    },
    terminal(record) {
      return record.workflow.terminal;
    },
  };
}

/** Maps the control-plane WorkflowRecord onto the page's WorkflowRecord. */
export function mapRecord(body) {
  if (
    !body?.commission?.id ||
    body.workflow?.commission_id !== body.commission.id ||
    typeof body.workflow?.stage !== 'string' ||
    typeof body.workflow?.terminal !== 'boolean' ||
    ![
      'runs',
      'steps',
      'revisions',
      'approvals',
      'decisions',
      'artifacts',
      'provenance',
      'events',
    ].every((key) => Array.isArray(body[key]))
  )
    throw invalidRecord();
  const workflow = body.workflow ?? {};
  const commission = body.commission ?? {};
  const stage = String(workflow.stage ?? 'DRAFT');
  const attempts = (stepId) =>
    body.events.filter(
      (event) =>
        event.type === 'step.attempted' && event.detail?.step_id === stepId,
    );
  return {
    commission: {
      id: String(commission.id ?? ''),
      title: String(commission.title ?? ''),
      kind: String(commission.objective ?? ''),
      brief: String(commission.objective ?? ''),
      approvers: ['Commission owner'],
      receivedAt: String(commission.created_at ?? ''),
    },
    workflow: {
      id: String(commission.id ?? ''),
      commissionId: String(commission.id ?? ''),
      status: statusOf(stage),
      stage,
      movement: String(workflow.movement ?? ''),
      terminal: Boolean(workflow.terminal),
      currentStepId: null,
      reviewRound: Number(workflow.review_round ?? 0),
      revisionNumber: Number(workflow.revision_number ?? 0),
      canApprove: Boolean(workflow.can_approve),
      lastError: String(workflow.last_error ?? ''),
      startedAt: String(body.runs?.[0]?.started_at ?? ''),
      updatedAt: String(body.events?.at(-1)?.created_at ?? ''),
    },
    runs: (body.runs ?? []).map((run) => ({
      id: String(run.id),
      workflowId: String(commission.id ?? ''),
      attempt: Number(run.attempt ?? 1),
      status: 'recorded',
      startedAt: String(run.started_at ?? ''),
      endedAt: null,
      reason:
        run.attempt > 1 ? 'later attempt of the same commission' : 'initial',
    })),
    steps: (body.steps ?? []).map((step, index) => ({
      id: String(step.step_id),
      runId: String(step.run_id ?? ''),
      index: index + 1,
      name: String(step.capability ?? ''),
      status: 'completed',
      attempts: Math.max(
        1,
        ...attempts(step.step_id).map((event) => Number(event.detail.attempt)),
      ),
      startedAt: attempts(step.step_id)[0]?.created_at ?? null,
      endedAt: String(step.created_at ?? ''),
      revisionId:
        body.revisions.find((revision) => revision.produced_by === step.step_id)
          ?.id ?? null,
      provider: String(step.provider ?? ''),
      model: String(step.model ?? ''),
      live: Boolean(step.live),
    })),
    revisions: (body.revisions ?? []).map((revision) => ({
      id: String(revision.id),
      workflowId: String(commission.id ?? ''),
      number: Number(revision.number ?? 0),
      parentId: revision.parent_id ? String(revision.parent_id) : null,
      summary: String(revision.title ?? ''),
      createdAt: String(revision.created_at ?? ''),
      artifactIds: body.artifacts
        .filter((artifact) => artifact.revision_id === revision.id)
        .map((artifact) => String(artifact.id)),
      hash: String(revision.direction_hash ?? ''),
    })),
    approvals: (body.approvals ?? []).map((approval) => ({
      id: String(approval.id),
      revisionId: String(approval.revision_id ?? ''),
      approver: 'Commission owner',
      status: String(approval.status ?? 'pending'),
      round: Number(approval.round ?? 0),
      requestedAt: String(approval.requested_at ?? ''),
      decidedAt:
        body.decisions.find((decision) => decision.approval_id === approval.id)
          ?.decided_at ?? null,
      action: approval.action ? String(approval.action) : '',
    })),
    decisions: (body.decisions ?? []).map((decision) => ({
      id: String(decision.decision_id ?? ''),
      approvalId: String(decision.approval_id ?? ''),
      revisionId: String(decision.revision_id ?? ''),
      actor: String(decision.actor_id ?? 'commission owner'),
      outcome: String(decision.action ?? '').toLowerCase(),
      note: String(decision.reason ?? ''),
      at: String(decision.decided_at ?? ''),
    })),
    artifacts: (body.artifacts ?? []).map((artifact) => ({
      id: String(artifact.id),
      revisionId: String(artifact.revision_id ?? ''),
      kind: 'artifact',
      title: 'Delivered artifact',
      contentHash: artifact.sha256 ? 'sha256:' + String(artifact.sha256) : null,
      createdAt: String(artifact.created_at ?? ''),
    })),
    provenance: (body.provenance ?? []).map((row) => ({
      artifactId:
        row.capability === 'artifact'
          ? (body.artifacts.find(
              (artifact) =>
                artifact.sha256 === row.sha256 &&
                artifact.revision_id === row.revision_id,
            )?.id ?? null)
          : null,
      outputRef: String(row.output_ref ?? ''),
      runId: String(row.run_id ?? ''),
      stepId: row.step_id ?? null,
      commissionId: String(commission.id ?? ''),
      revisionId: String(row.revision_id ?? ''),
      decisionId: String(row.decision_id ?? ''),
      stepIds: Array.isArray(row.inputs?.step_ids)
        ? row.inputs.step_ids.map(String)
        : [],
      sourceIds: Array.isArray(row.inputs?.source_ids)
        ? row.inputs.source_ids.map(String)
        : [],
      capability: String(row.capability ?? ''),
      sha256: String(row.sha256 ?? ''),
    })),
    events: (body.events ?? []).map((event) => ({
      id: String(event.id),
      workflowId: String(commission.id ?? ''),
      at: String(event.created_at ?? ''),
      type: String(event.type ?? ''),
      kind: String(event.kind ?? 'run'),
      subject: String(event.subject ?? ''),
      detail: describe(event.type, event.detail),
      data: event.detail ?? {},
    })),
    source: String(body.source ?? 'temporal+postgresql'),
    public: Boolean(body.public),
  };
}

function invalidRecord() {
  return new ApiError(
    200,
    'INVALID_RESPONSE',
    'The workflow service returned an incomplete record.',
  );
}

function statusOf(stage) {
  if (stage === 'WAITING_FOR_APPROVAL') return 'waiting';
  if (stage === 'DRAFT') return 'received';
  if (TERMINAL.has(stage)) return stage.toLowerCase();
  return 'running';
}

/** One readable sentence from an event's detail object. */
function describe(type, detail) {
  const d = detail && typeof detail === 'object' ? detail : {};
  switch (type) {
    case 'commission.received':
      return 'Brief received.';
    case 'commission.published':
      return 'Record published.';
    case 'run.started':
      return `Run ${d.attempt ?? 1} started.`;
    case 'run.resumed':
      return `Run ${d.attempt ?? ''} recorded as a later attempt of this commission.`;
    case 'workflow.stage':
      return `Entered ${String(d.stage ?? '')
        .toLowerCase()
        .replaceAll('_', ' ')} (${String(d.movement ?? '').toLowerCase()}).`;
    case 'step.attempted':
      return `Attempt ${d.attempt ?? 1} of ${d.capability ?? 'step'}.`;
    case 'step.completed':
      return `${d.capability ?? 'step'} accepted from ${d.provider ?? 'provider'} (${d.live ? 'live' : 'fixture'}), prompt ${d.prompt_id ?? ''} v${d.prompt_version ?? ''}.`;
    case 'step.failed':
      return `Attempt ${d.attempt ?? ''} failed with ${d.code ?? 'an error'}; recorded, not accepted.`;
    case 'revision.created':
      return `Revision ${d.number ?? ''} recorded (${String(d.direction_hash ?? '').slice(0, 12)}).${d.requires_revision ? ' Critique asks for another pass.' : ''}`;
    case 'approval.requested':
      return `Round ${d.round ?? ''} waits on a named person for revision ${String(d.revision_id ?? '').slice(0, 8)}.`;
    case 'approval.expired':
      return `Round ${d.round ?? ''} expired without a decision.`;
    case 'decision.recorded':
      return `${d.action ?? 'Decision'} on revision ${String(d.revision_id ?? '').slice(0, 8)} (${d.reason_code ?? ''}) after ${Math.round(Number(d.waited_ms ?? 0) / 1000)}s.`;
    case 'artifact.recorded':
      return `Artifact ${String(d.artifact_id ?? '').slice(0, 8)} bound to revision ${String(d.revision_id ?? '').slice(0, 8)} and decision ${d.decision_id ?? ''}.`;
    case 'workflow.cancellation_requested':
      return 'Cancellation requested by the owner.';
    default:
      if (type.startsWith('workflow.')) {
        return `Ended ${String(d.outcome ?? '')}${d.code ? ` (${d.code})` : ''} after ${Math.round(Number(d.duration_ms ?? 0) / 1000)}s.`;
      }
      return '';
  }
}
