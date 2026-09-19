import { createServer, request as httpRequest } from 'node:http';
import { request as httpsRequest } from 'node:https';
import { stat, readFile } from 'node:fs/promises';
import { createReadStream } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { resolve, sep, extname } from 'node:path';
import { randomUUID } from 'node:crypto';

// Production static server for the built site plus a fixed same-origin proxy
// for /api/. It forwards only the headers the API needs and never cookies.
export function createFrontendServer({
  root = fileURLToPath(new URL('../dist', import.meta.url)),
  upstream = process.env.API_UPSTREAM || 'http://127.0.0.1:8080',
} = {}) {
  const api = new URL(upstream);
  if (
    !['http:', 'https:'].includes(api.protocol) ||
    api.username ||
    api.password ||
    api.pathname !== '/'
  ) {
    throw new Error(
      'API_UPSTREAM must be an HTTP(S) origin without credentials or path.',
    );
  }
  const types = {
    '.html': 'text/html; charset=utf-8',
    '.js': 'text/javascript; charset=utf-8',
    '.css': 'text/css; charset=utf-8',
    '.png': 'image/png',
    '.svg': 'image/svg+xml',
    '.json': 'application/json',
    '.woff2': 'font/woff2',
  };
  const security = {
    'Content-Security-Policy':
      "default-src 'self'; script-src 'self'; style-src 'self' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'",
    'X-Content-Type-Options': 'nosniff',
    'Referrer-Policy': 'no-referrer',
    'Permissions-Policy': 'camera=(), microphone=(), geolocation=()',
    'X-Frame-Options': 'DENY',
  };
  const server = createServer(
    { maxHeaderSize: 16384 },
    async (request, response) => {
      const requestId = randomUUID();
      Object.entries(security).forEach(([key, value]) =>
        response.setHeader(key, value),
      );
      response.setHeader('X-Request-Id', requestId);
      if (process.env.APP_ENV === 'production')
        response.setHeader('Strict-Transport-Security', 'max-age=31536000');
      const fail = (status, code) => {
        if (response.headersSent) {
          response.destroy();
          return;
        }
        response.writeHead(status, {
          'Content-Type': 'application/json',
          'Cache-Control': 'no-store',
        });
        response.end(
          JSON.stringify({
            code,
            message: 'The request could not be served.',
            traceId: requestId,
          }),
        );
      };
      let path;
      try {
        path = decodeURIComponent((request.url || '/').split('?')[0]);
        if (
          path.includes('\\') ||
          path.includes('\0') ||
          path.split('/').some((part) => part === '..' || part === '.')
        )
          return fail(400, 'INVALID_PATH');
      } catch {
        return fail(400, 'INVALID_PATH');
      }
      if (path.startsWith('/api/')) {
        if (Number(request.headers['content-length'] || 0) > 65536)
          return fail(413, 'INPUT_TOO_LARGE');
        const transport =
          api.protocol === 'https:' ? httpsRequest : httpRequest;
        const headers = { host: api.host, 'x-request-id': requestId };
        for (const key of [
          'authorization',
          'content-type',
          'accept',
          'idempotency-key',
          'last-event-id',
          'traceparent',
          'tracestate',
        ]) {
          if (request.headers[key]) headers[key] = request.headers[key];
        }
        const proxy = transport(
          new URL(request.url, api),
          { method: request.method, headers, timeout: 25000 },
          (result) => {
            response.statusCode = result.statusCode || 502;
            for (const key of [
              'content-type',
              'cache-control',
              'x-request-id',
              'retry-after',
              'etag',
            ])
              if (result.headers[key])
                response.setHeader(key, result.headers[key]);
            response.setHeader('Cache-Control', 'no-store');
            result.on('error', () => response.destroy());
            result.pipe(response);
          },
        );
        let size = 0;
        request.on('data', (chunk) => {
          size += chunk.length;
          if (size > 65536) {
            proxy.destroy();
            fail(413, 'INPUT_TOO_LARGE');
          }
        });
        proxy.on('timeout', () => {
          proxy.destroy();
          fail(504, 'API_TIMEOUT');
        });
        proxy.on('error', () => fail(502, 'API_UNAVAILABLE'));
        response.on('close', () => proxy.destroy());
        request.pipe(proxy);
        return;
      }
      if (!['GET', 'HEAD'].includes(request.method))
        return fail(405, 'METHOD_NOT_ALLOWED');
      if (path === '/health/live' || path === '/health/ready') {
        try {
          if (path.endsWith('ready'))
            await readFile(resolve(root, 'index.html'));
        } catch {
          return fail(503, 'FRONTEND_NOT_READY');
        }
        response.writeHead(200, {
          'Content-Type': 'application/json',
          'Cache-Control': 'no-store',
        });
        response.end(
          JSON.stringify({ status: 'ok', service: 'velin-frontend' }),
        );
        return;
      }
      const filename = resolve(
        root,
        `.${path.endsWith('/') ? path + 'index.html' : path}`,
      );
      if (!filename.startsWith(resolve(root) + sep))
        return fail(400, 'INVALID_PATH');
      try {
        const info = await stat(filename);
        if (!info.isFile()) return fail(404, 'NOT_FOUND');
        if (!Object.hasOwn(types, extname(filename)))
          return fail(404, 'NOT_FOUND');
        response.writeHead(200, {
          'Content-Type': types[extname(filename)],
          'Content-Length': info.size,
          'Cache-Control': path.startsWith('/assets/')
            ? 'public, max-age=31536000, immutable'
            : 'no-cache',
        });
        if (request.method === 'HEAD') response.end();
        else
          createReadStream(filename)
            .on('error', () => response.destroy())
            .pipe(response);
      } catch {
        fail(404, 'NOT_FOUND');
      }
    },
  );
  server.requestTimeout = 30000;
  server.headersTimeout = 10000;
  server.keepAliveTimeout = 5000;
  return server;
}

if (
  process.argv[1] &&
  resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  const port = Number(process.env.PORT || 4173);
  if (!Number.isInteger(port) || port < 1 || port > 65535)
    throw new Error('Invalid PORT');
  const server = createFrontendServer();
  server.listen(port, process.env.HOST || '127.0.0.1', () =>
    console.log(
      JSON.stringify({
        timestamp: new Date().toISOString(),
        level: 'info',
        service: 'velin-frontend',
        event: 'listening',
        port,
      }),
    ),
  );
  for (const signal of ['SIGTERM', 'SIGINT'])
    process.on(signal, () => {
      console.log(
        JSON.stringify({
          timestamp: new Date().toISOString(),
          level: 'info',
          service: 'velin-frontend',
          event: 'shutdown',
          signal,
        }),
      );
      server.close(() => process.exit(0));
      setTimeout(() => {
        server.closeAllConnections();
        process.exit(1);
      }, 10000).unref();
    });
}
