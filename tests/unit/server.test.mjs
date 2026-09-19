import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createServer, request } from 'node:http';
import { mkdtemp, mkdir, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createFrontendServer } from '../../scripts/serve.mjs';

async function listen(server) {
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  return `http://127.0.0.1:${server.address().port}`;
}
async function stop(server) {
  server.closeAllConnections();
  await new Promise((resolve) => server.close(resolve));
}

test('production server isolates static files, enforces headers and proxies only the configured API', async () => {
  const root = await mkdtemp(join(tmpdir(), 'velin-static-test-'));
  await mkdir(join(root, 'commission'));
  await writeFile(join(root, 'index.html'), '<h1>VELIN</h1>');
  await writeFile(
    join(root, 'commission', 'index.html'),
    '<h1>Commission</h1>',
  );
  await writeFile(join(root, '.env'), 'PRIVATE');
  let apiRequests = 0;
  const upstream = createServer((req, res) => {
    apiRequests++;
    assert.equal(req.headers.authorization, 'Bearer test-only');
    assert.equal(req.headers.cookie, undefined);
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify({ id: 'test' }));
  });
  const upstreamURL = await listen(upstream);
  const server = createFrontendServer({ root, upstream: upstreamURL });
  const base = await listen(server);
  try {
    const page = await fetch(base + '/commission/');
    assert.equal(page.status, 200);
    assert.match(
      page.headers.get('content-security-policy'),
      /script-src 'self';/,
    );
    assert.equal(page.headers.get('x-content-type-options'), 'nosniff');
    assert.equal((await fetch(base + '/health/ready')).status, 200);
    assert.equal((await fetch(base + '/missing')).status, 404);
    assert.equal((await fetch(base + '/.env')).status, 404);
    assert.equal((await fetch(base + '/', { method: 'POST' })).status, 405);
    const proxied = await fetch(base + '/api/v1/session', {
      headers: { Authorization: 'Bearer test-only', Cookie: 'secret=session' },
    });
    assert.equal(proxied.status, 200);
    assert.deepEqual(await proxied.json(), { id: 'test' });
    assert.equal(apiRequests, 1);
    const traversal = await new Promise((resolve, reject) => {
      request(base, { path: '/%2e%2e/private' }, (res) => {
        res.resume();
        resolve(res.statusCode);
      })
        .on('error', reject)
        .end();
    });
    assert.equal(traversal, 400);
    const oversized = await fetch(base + '/api/v1/commissions', {
      method: 'POST',
      body: 'x'.repeat(70000),
    });
    assert.equal(oversized.status, 413);
    assert.equal(apiRequests, 1);
  } finally {
    await stop(server);
    await stop(upstream);
  }
});

test('missing build fails readiness while live remains available', async () => {
  const root = await mkdtemp(join(tmpdir(), 'velin-unbuilt-test-'));
  const server = createFrontendServer({ root });
  const base = await listen(server);
  try {
    assert.equal((await fetch(base + '/health/live')).status, 200);
    assert.equal((await fetch(base + '/health/ready')).status, 503);
  } finally {
    await stop(server);
  }
});

test('proxy failure is bounded and does not reveal internals', async () => {
  const server = createFrontendServer({ upstream: 'http://127.0.0.1:1' });
  const base = await listen(server);
  try {
    const response = await fetch(base + '/api/v1/session');
    assert.equal(response.status, 502);
    const body = await response.json();
    assert.equal(body.code, 'API_UNAVAILABLE');
    assert.ok(body.traceId);
    assert.doesNotMatch(JSON.stringify(body), /ECONN|stack|127\.0\.0/);
  } finally {
    await stop(server);
  }
});
