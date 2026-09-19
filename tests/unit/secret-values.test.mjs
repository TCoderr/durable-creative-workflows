import assert from 'node:assert/strict';
import test from 'node:test';
import { configuredSecrets } from '../../scripts/secret-values.mjs';

test('secret hygiene distinguishes persistent resource identities from credentials', () => {
  const token = 'synthetic-test-token';
  const values = configuredSecrets(
    [
      'TEMPORAL_DATA_VOLUME=velin_temporal-data',
      'APP_VERSION=long-public-release-identifier',
      `VELIN_API_KEYS=${JSON.stringify({ [token]: 'test-subject' })}`,
      'POSTGRES_PASSWORD=synthetic-password',
      'AI_SERVICE_TOKEN="synthetic=service-token"',
      'NATS_TOKEN=synthetic-nats-token',
      'LLM_API_KEY=synthetic-provider-key',
      'CLIENT_SECRET=synthetic-client-secret',
    ].join('\n'),
  );
  assert.deepEqual(values, [
    token,
    'synthetic-password',
    'synthetic=service-token',
    'synthetic-nats-token',
    'synthetic-provider-key',
    'synthetic-client-secret',
  ]);
});

test('invalid credential configuration fails closed during secret inspection', () => {
  assert.throws(() => configuredSecrets('VELIN_API_KEYS={broken'), SyntaxError);
});
