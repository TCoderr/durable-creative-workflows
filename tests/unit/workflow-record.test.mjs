import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createApiSource, mapRecord } from '../../workflow/api-source.js';
import { summarize } from '../../workflow/model.js';

const fixture = JSON.parse(
  readFileSync(new URL('../fixtures/public-record.json', import.meta.url)),
);

test('an incomplete successful HTTP response cannot become an invented draft or empty history', async () => {
  assert.throws(() => mapRecord({}), { code: 'INVALID_RESPONSE' });
  const source = createApiSource({ fetchImpl: async () => new Response('{}') });
  await assert.rejects(source.listCommissions(), { code: 'INVALID_RESPONSE' });
});

test('failed and interrupted attempts are counted once, including unaccepted steps', () => {
  const body = structuredClone(fixture);
  const step = body.steps[0];
  body.events = [
    {
      id: 'failed',
      type: 'step.failed',
      detail: { step_id: step.step_id, attempt: 1 },
    },
    {
      id: 'retried',
      type: 'step.attempted',
      detail: { step_id: step.step_id, attempt: 2 },
    },
    {
      id: 'unaccepted',
      type: 'step.failed',
      detail: { step_id: 'never-accepted', attempt: 1 },
    },
    {
      id: 'recovered',
      type: 'step.attempted',
      detail: { step_id: 'interrupted', attempt: 2 },
    },
  ];
  const record = mapRecord(body);
  assert.equal(record.steps[0].attempts, 2);
  assert.equal(summarize(record).failures, 3);
  assert.equal(record.approvals[0].decidedAt, body.decisions[0].decided_at);
});

test('a terminal stage continues refreshing until terminal evidence is committed', async () => {
  let requests = 0;
  const source = createApiSource({
    pollMs: 5,
    fetchImpl: async () => {
      const body = structuredClone(fixture);
      body.workflow.terminal = ++requests > 1;
      return Response.json(body);
    },
  });
  await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      unsubscribe();
      reject(new Error('committed terminal state never observed'));
    }, 1000);
    const unsubscribe = source.subscribe('record', ({ record, error }) => {
      if (error) {
        clearTimeout(timeout);
        unsubscribe();
        reject(error);
      } else if (record.workflow.terminal) {
        clearTimeout(timeout);
        unsubscribe();
        resolve();
      }
    });
  });
  assert.equal(requests, 2);
});
