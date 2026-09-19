/**
 * workflow/render.js — paints one workflow record into the History section.
 *
 * The record comes from the control-plane API (a published commission's
 * authoritative state and append-only record). When unavailable it shows a
 * controlled error instead of stale or invented data.
 */

import { createWorkflowClient, summarize } from './model.js';
import { createApiSource, ApiError } from './api-source.js';

const mount = document.querySelector('[data-seam="workflow-record"]');

if (mount) {
  boot(mount).catch((error) => {
    console.error('[VELIN] The workflow record could not be rendered.', error);
    showError(mount, error);
  });
}

async function boot(root) {
  const client = createWorkflowClient(createApiSource());
  const requested =
    root.dataset.record ||
    new URLSearchParams(location.search).get('record') ||
    '';
  let recordId = requested;
  if (!recordId) {
    const [first] = await client.listCommissions();
    if (!first) {
      root.replaceChildren(
        el('p', 'meta record-note', 'No published workflow record yet.'),
      );
      return;
    }
    recordId = first.id;
  }
  const record = await client.getRecord(recordId);
  if (!record) {
    root.replaceChildren(el('p', 'meta record-note', 'No record to show.'));
    return;
  }
  paint(root, record);
  if (!record.workflow.terminal) {
    // The service is polled while the workflow is open; each refresh repaints
    // from authoritative state and a failure shows as a controlled notice.
    client.subscribe(recordId, (event) => {
      if (event.record) paint(root, event.record);
      else if (event.error) showError(root, event.error, true);
    });
  }
}

function paint(root, record) {
  const counts = summarize(record);
  const head = el('div', 'record-head');
  const title = el('h3', 'record-title');
  title.append(
    document.createTextNode(`${record.commission.id.slice(0, 8)} — `),
    el('em', null, record.commission.title),
  );
  const summary = el('p', 'record-summary meta');
  for (const text of [
    `${counts.revisions} revision${counts.revisions === 1 ? '' : 's'}`,
    `${counts.decisions} decision${counts.decisions === 1 ? '' : 's'}`,
    `${counts.failures} failed attempt${counts.failures === 1 ? '' : 's'}`,
    `${counts.resumes} later run${counts.resumes === 1 ? '' : 's'}`,
    `${counts.artifacts} artifact${counts.artifacts === 1 ? '' : 's'}`,
    `stage · ${String(record.workflow.stage || counts.status)
      .toLowerCase()
      .replaceAll('_', ' ')}`,
  ]) {
    summary.append(el('span', null, text));
  }
  head.append(title, summary);

  const ledger = el('ol', 'ledger');
  for (const event of record.events) {
    const row = el('li');
    row.dataset.kind = event.kind;
    row.append(
      el('span', 'ledger-at', formatStamp(event.at)),
      el('span', 'ledger-type', event.type),
      el(
        'span',
        'ledger-detail',
        event.detail ? `${event.subject} — ${event.detail}` : event.subject,
      ),
    );
    ledger.append(row);
  }

  const note = el(
    'p',
    'meta record-note',
    `Authoritative record from ${record.source || 'the workflow service'}.${record.workflow.terminal ? '' : ' Refreshing while the workflow is open.'}`,
  );
  root.replaceChildren(head, ledger, note);
}

function showError(root, error, keepContent = false) {
  const code = error instanceof ApiError ? error.code : 'RENDER_FAILED';
  const message =
    error instanceof ApiError
      ? error.message
      : 'The record could not be rendered in this browser.';
  const notice = el('p', 'meta record-note record-error');
  notice.setAttribute('role', 'status');
  notice.textContent = `Record unavailable — ${code}. ${message}`;
  if (keepContent) {
    const previous = root.querySelector('.record-error');
    if (previous) previous.replaceWith(notice);
    else root.append(notice);
    return;
  }
  root.replaceChildren(notice);
}

/** "2026-06-01T09:12:00Z" -> "2026-06-01 · 09:12" without locale drift. */
function formatStamp(iso) {
  if (!iso || iso.length < 16) return iso || '';
  return `${iso.slice(0, 10)} · ${iso.slice(11, 16)}`;
}

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text != null) node.textContent = text;
  return node;
}
