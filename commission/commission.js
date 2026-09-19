import { ApiError, CommissionApi, object, text } from './api.js';

/**
 * commission/commission.js — the private folio.
 *
 * Every view is rebuilt from the authoritative API: workflow state from
 * Temporal, the record from PostgreSQL. A decision always names the approval
 * request and the exact revision the workflow is waiting on, so a stale
 * approval is refused by the service and shown here as a controlled notice.
 */

/** @template {HTMLElement} T @param {string} id @returns {T} */
function element(id) {
  const result = document.getElementById(id);
  if (!result) throw new Error(`Missing commission element: ${id}`);
  return /** @type {T} */ (result);
}

const movements = [
  ['BRIEF', 'Brief', ['DRAFT', 'BRIEF_ANALYSIS']],
  [
    'EXECUTE',
    'Execute',
    [
      'RESEARCH',
      'STRATEGY',
      'DIRECTION',
      'TYPOGRAPHY',
      'MOTION',
      'IMAGERY',
      'REVISION',
    ],
  ],
  ['REVIEW', 'Review', ['CRITIQUE', 'WAITING_FOR_APPROVAL']],
  ['APPROVE', 'Approve', ['APPROVED']],
  ['CONTINUE', 'Continue', ['PRODUCTION']],
  [
    'DONE',
    'Delivered',
    ['COMPLETED', 'REJECTED', 'EXPIRED', 'CANCELLED', 'FAILED'],
  ],
];
const terminal = new Set([
  'COMPLETED',
  'REJECTED',
  'EXPIRED',
  'CANCELLED',
  'FAILED',
]);

/** @type {CommissionApi | undefined} */
let api;
let commissionId = '';
/** @type {Record<string, unknown>} */
let current = {};
/** @type {AbortController | undefined} */
let stream;
/** @type {ReturnType<typeof setInterval> | undefined} */
let poll;
let refreshing = false;
/** @type {{ fingerprint: string, key: string, id?: string } | undefined} */
let pendingSubmission;
/** @type {{ fingerprint: string, id: string } | undefined} */
let pendingDecision;

function notice(message = '') {
  element('notice').textContent = message;
}
function show(id, visible) {
  element(id).hidden = !visible;
}
function errorMessage(error) {
  if (error instanceof ApiError && error.status === 401) {
    return 'Access has expired or is not recognized. Close the folio and enter a valid token.';
  }
  if (error instanceof ApiError && error.code === 'STALE_APPROVAL') {
    return 'The workflow is now waiting on a newer revision. The view has been refreshed; review it before deciding again.';
  }
  return error instanceof Error
    ? error.message
    : 'The request could not be completed. Please retry.';
}
async function action(form, callback) {
  const buttons = [...form.querySelectorAll('button')];
  buttons.forEach((button) => (button.disabled = true));
  notice();
  try {
    await callback();
  } catch (error) {
    notice(errorMessage(error));
    if (error instanceof ApiError && error.code === 'STALE_APPROVAL') {
      pendingDecision = undefined;
      await refresh();
    }
  } finally {
    buttons.forEach((button) => (button.disabled = false));
    const approve = element('review-form').querySelector(
      'button[value="APPROVE"]',
    );
    if (approve) approve.disabled = current.can_approve === false;
  }
}
function client() {
  if (!api) throw new Error('Open your folio first.');
  return api;
}
function lines(id) {
  const values = element(id)
    .value.split('\n')
    .map((line) => line.trim())
    .filter(Boolean);
  if (values.length > 20 || values.some((line) => line.length > 500)) {
    throw new Error('Use at most 20 lines, each under 500 characters.');
  }
  return values;
}
function stopProgress() {
  stream?.abort();
  stream = undefined;
  clearInterval(poll);
  poll = undefined;
}

async function listCommissions() {
  const response = await client().request('/commissions');
  const list = element('commission-list');
  list.replaceChildren();
  if (!Array.isArray(response.items)) return;
  for (const item of response.items.slice(0, 20)) {
    const commission = object(item);
    const button = document.createElement('button');
    button.type = 'button';
    button.textContent =
      (commission.brief ? text(object(commission.brief).title) : '') ||
      'Untitled commission';
    button.addEventListener(
      'click',
      () => void action(list, () => openCommission(text(commission.id))),
    );
    list.append(button);
  }
}

element('access-form').addEventListener('submit', (event) => {
  event.preventDefault();
  void action(element('access-form'), async () => {
    const tokenField = element('access-token');
    api = new CommissionApi(tokenField.value.trim());
    tokenField.value = '';
    const session = await client().request('/session');
    element('provider-notice').textContent =
      text(session.provider_mode) === 'deterministic'
        ? 'Deterministic capability fixtures — no live model execution'
        : 'Provider and model are recorded with each step';
    show('access-section', false);
    show('workspace', true);
    await listCommissions();
    const id = location.hash.slice(1);
    if (/^[a-f0-9-]{36}$/i.test(id)) await openCommission(id);
  });
});

element('sign-out').addEventListener('click', () => {
  stopProgress();
  api?.clear();
  api = undefined;
  current = {};
  commissionId = '';
  pendingSubmission = undefined;
  pendingDecision = undefined;
  element('brief-form').reset();
  element('review-form').reset();
  for (const id of [
    'direction-content',
    'provenance',
    'timeline',
    'commission-list',
    'publication',
  ]) {
    element(id).replaceChildren();
  }
  for (const id of [
    'workspace',
    'process-section',
    'direction-section',
    'review-section',
    'evidence-section',
  ]) {
    show(id, false);
  }
  show('access-section', true);
  history.replaceState(null, '', location.pathname);
  notice('Folio closed.');
});

element('brief-form').addEventListener('submit', (event) => {
  event.preventDefault();
  void action(element('brief-form'), async () => {
    const brief = {
      title: element('title').value.trim(),
      objective: element('objective').value.trim(),
      audience: element('audience').value.trim(),
      constraints: lines('constraints'),
      prohibited: lines('prohibited'),
      brand_memory: [],
    };
    const fingerprint = JSON.stringify({ brief });
    if (pendingSubmission?.fingerprint !== fingerprint) {
      pendingSubmission = { fingerprint, key: crypto.randomUUID() };
    }
    const pending = pendingSubmission;
    if (!pending.id) {
      const result = await client().request('/commissions', {
        method: 'POST',
        headers: { 'Idempotency-Key': pending.key },
        body: fingerprint,
      });
      pending.id = text(result.id);
    }
    if (!pending.id)
      throw new Error('The service did not return a commission identifier.');
    await client().request(`/commissions/${pending.id}/start`, {
      method: 'POST',
    });
    await openCommission(pending.id);
    pendingSubmission = undefined;
    await listCommissions();
  });
});

async function openCommission(id) {
  stopProgress();
  current = {};
  pendingDecision = undefined;
  const commission = await client().request(
    `/commissions/${encodeURIComponent(id)}`,
  );
  commissionId = id;
  history.replaceState(null, '', `${location.pathname}#${id}`);
  element('commission-title').textContent = text(
    object(commission.brief).title,
  );
  element('provenance').replaceChildren();
  element('publication').textContent = '';
  show('provenance', false);
  show('process-section', true);
  show('evidence-section', true);
  await refresh();
  if (terminal.has(text(current.stage))) return;
  stream = new AbortController();
  const selected = commissionId;
  element('connection-status').textContent = 'Live progress connected';
  void client()
    .events(id, stream.signal, () => {
      if (commissionId === selected) void refresh();
    })
    .catch(() => undefined)
    .finally(() => {
      if (commissionId === selected) {
        element('connection-status').textContent = terminal.has(
          text(current.stage),
        )
          ? 'Workflow finished — its record is preserved'
          : 'Live connection paused — refreshing every 10 seconds';
      }
    });
  poll = setInterval(() => void refresh(), 10000);
}

function renderDirection(direction) {
  element('direction-title').textContent = text(direction.title);
  const content = element('direction-content');
  content.replaceChildren();
  const labels = {
    objective: 'Strategic objective',
    audience: 'Audience',
    visual_principles: 'Visual principles',
    colors: 'Color direction',
    typography: 'Typography',
    motion: 'Motion',
    imagery: 'Imagery',
    composition: 'Composition',
    prohibitions: 'Prohibited patterns',
    references: 'References',
    uncertainties: 'Open questions',
  };
  for (const [key, label] of Object.entries(labels)) {
    const value = direction[key];
    if (value == null || (Array.isArray(value) && value.length === 0)) continue;
    const heading = document.createElement('h3');
    heading.textContent = label;
    content.append(heading);
    if (Array.isArray(value)) {
      const list = document.createElement('ul');
      for (const item of value) {
        const li = document.createElement('li');
        li.textContent = typeof item === 'string' ? item : JSON.stringify(item);
        list.append(li);
      }
      content.append(list);
    } else {
      const paragraph = document.createElement('p');
      paragraph.textContent = text(value) || JSON.stringify(value);
      content.append(paragraph);
    }
  }
}

function renderRecord(record) {
  const rows = element('timeline');
  rows.replaceChildren();
  const events = Array.isArray(record.events) ? record.events : [];
  for (const entry of events) {
    const item = object(entry);
    const row = document.createElement('li');
    const detail =
      item.detail && typeof item.detail === 'object' ? object(item.detail) : {};
    const facts = [text(item.created_at), text(item.type), text(item.subject)];
    if (text(detail.code)) facts.push(text(detail.code));
    if (text(detail.stage)) facts.push(text(detail.stage));
    row.textContent = facts.filter(Boolean).join(' · ');
    rows.append(row);
  }
  const runs = Array.isArray(record.runs) ? record.runs.length : 0;
  const revisions = Array.isArray(record.revisions)
    ? record.revisions.length
    : 0;
  const decisions = Array.isArray(record.decisions)
    ? record.decisions.length
    : 0;
  const artifacts = Array.isArray(record.artifacts)
    ? record.artifacts.length
    : 0;
  element('run-label').textContent =
    `${runs} run${runs === 1 ? '' : 's'} · ${revisions} revision${revisions === 1 ? '' : 's'} · ${decisions} decision${decisions === 1 ? '' : 's'} · ${artifacts} artifact${artifacts === 1 ? '' : 's'}`;
}

async function refresh() {
  if (!api || !commissionId || refreshing) return;
  refreshing = true;
  const selected = commissionId;
  try {
    const record = await client().request(`/workflows/${selected}/record`);
    if (commissionId !== selected) return;
    current = object(record.workflow);
    const stage = text(current.stage);
    show('start-existing', stage === 'DRAFT');
    element('stage-description').textContent =
      `${text(current.movement).toLowerCase()} · ${stage.replaceAll('_', ' ').toLowerCase()}`;
    const list = element('stage-list');
    list.replaceChildren();
    for (const [code, label, stages] of movements) {
      const row = document.createElement('li');
      row.textContent = label;
      if (stages.includes(stage) || text(current.movement) === code) {
        row.setAttribute('aria-current', 'step');
      }
      list.append(row);
    }
    const direction = current.direction;
    show('direction-section', !!direction && typeof direction === 'object');
    if (direction && typeof direction === 'object')
      renderDirection(object(direction));
    const revision =
      current.revision && typeof current.revision === 'object'
        ? object(current.revision)
        : {};
    element('revision-label').textContent =
      `Revision ${Number(revision.number ?? 0)} · ${text(revision.direction_hash).slice(0, 12)} · review round ${Number(current.review_round ?? 0)}`;
    element('execution-mode').textContent =
      'Consult the record for the exact provider, prompt versions and evaluation evidence of every step.';
    const pending =
      current.pending_approval && typeof current.pending_approval === 'object'
        ? object(current.pending_approval)
        : null;
    const reviewing = stage === 'WAITING_FOR_APPROVAL' && pending !== null;
    show('review-section', reviewing);
    if (pending) {
      element('approval-binding').textContent =
        `Approval request ${text(pending.id).slice(0, 8)} for revision ${text(pending.revision_id).slice(0, 8)}, round ${Number(pending.round ?? 0)}.`;
    }
    const approveButton = element('review-form').querySelector(
      'button[value="APPROVE"]',
    );
    if (approveButton) approveButton.disabled = current.can_approve === false;
    const critique = current.critique;
    if (critique && typeof critique === 'object') {
      element('critique').textContent = text(object(critique).summary);
    }
    show('download-artifact', stage === 'COMPLETED' && !!current.artifact_id);
    show('cancel', !terminal.has(stage));
    if (current.last_error) notice(text(current.last_error));
    renderRecord(record);
    if (terminal.has(stage)) stopProgress();
  } catch (error) {
    notice(errorMessage(error));
  } finally {
    refreshing = false;
  }
}

element('review-form').addEventListener('submit', (event) => {
  event.preventDefault();
  const button = /** @type {HTMLButtonElement | null} */ (event.submitter);
  if (!button || !['APPROVE', 'REVISE', 'REJECT'].includes(button.value))
    return;
  void action(element('review-form'), async () => {
    const pending =
      current.pending_approval && typeof current.pending_approval === 'object'
        ? object(current.pending_approval)
        : null;
    if (!pending)
      throw new Error('The workflow is not waiting for a decision.');
    const decision = {
      approval_id: text(pending.id),
      revision_id: text(pending.revision_id),
      action: button.value,
      reason_code: element('reason-code').value,
      reason: element('reason').value.trim(),
    };
    const fingerprint = JSON.stringify(decision);
    if (pendingDecision?.fingerprint !== fingerprint) {
      pendingDecision = { fingerprint, id: crypto.randomUUID() };
    }
    await client().request(`/workflows/${commissionId}/decisions`, {
      method: 'POST',
      body: JSON.stringify({ ...decision, decision_id: pendingDecision.id }),
    });
    notice(
      'Decision delivered. The workflow is validating it against the exact revision under review.',
    );
    await refresh();
  });
});

element('refresh').addEventListener('click', () => void refresh());
element('start-existing').addEventListener(
  'click',
  () =>
    void action(element('process-section'), async () => {
      await client().request(`/commissions/${commissionId}/start`, {
        method: 'POST',
      });
      await refresh();
    }),
);
element('cancel').addEventListener(
  'click',
  () =>
    void action(element('process-section'), async () => {
      await client().request(`/workflows/${commissionId}/cancel`, {
        method: 'POST',
      });
      await refresh();
    }),
);
element('load-provenance').addEventListener(
  'click',
  () =>
    void action(element('evidence-section'), async () => {
      const provenance = await client().request(`/provenance/${commissionId}`);
      element('provenance').textContent = JSON.stringify(provenance, null, 2);
      show('provenance', true);
    }),
);
element('publish').addEventListener(
  'click',
  () =>
    void action(element('evidence-section'), async () => {
      const result = await client().request(
        `/commissions/${commissionId}/publications`,
        { method: 'POST' },
      );
      const id = text(result.publication_id);
      element('publication').textContent = id
        ? `Published. The index shows this record at /?record=${id}`
        : 'Published.';
    }),
);
element('download-artifact').addEventListener(
  'click',
  () =>
    void action(element('evidence-section'), async () => {
      const artifact = await client().request(
        `/artifacts/${encodeURIComponent(text(current.artifact_id))}`,
      );
      const url = URL.createObjectURL(
        new Blob([JSON.stringify(artifact, null, 2)], {
          type: 'application/json',
        }),
      );
      const link = document.createElement('a');
      link.href = url;
      link.download = `velin-${commissionId}.json`;
      link.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    }),
);
window.addEventListener('pagehide', stopProgress);
