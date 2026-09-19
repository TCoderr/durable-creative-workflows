import { defineConfig } from 'vite';
import { resolve } from 'node:path';

// Two pages: the public site and the private commission folio. In development
// the API is proxied to the local control plane so the browser stays
// same-origin; in production scripts/serve.mjs performs the same proxying.
export default defineConfig({
  build: {
    rollupOptions: {
      input: {
        index: resolve(import.meta.dirname, 'index.html'),
        commission: resolve(import.meta.dirname, 'commission/index.html'),
      },
    },
  },
  server: {
    host: '127.0.0.1',
    proxy: { '/api': process.env.VITE_API_UPSTREAM || 'http://127.0.0.1:8080' },
  },
});
