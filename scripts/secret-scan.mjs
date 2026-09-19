import { readdir, readFile, stat } from 'node:fs/promises';
import { resolve, relative, extname } from 'node:path';
import { configuredSecrets } from './secret-values.mjs';

const root = resolve(import.meta.dirname, '..');
const ignored = new Set([
  '.git',
  '.local',
  '.tools',
  '.venv',
  'node_modules',
  'dist',
  '__pycache__',
  '.pytest_cache',
  '.ruff_cache',
  '.mypy_cache',
  '.terraform',
  'test-results',
  'playwright-report',
  'coverage',
  'runtime',
]);
const extensions = new Set([
  '.js',
  '.mjs',
  '.ts',
  '.py',
  '.go',
  '.json',
  '.yaml',
  '.yml',
  '.tf',
  '.md',
  '.sql',
  '.sh',
  '.html',
  '.css',
  '.toml',
  '.txt',
  '.lock',
  '.sum',
  '.mod',
]);
const secrets = [];
try {
  const environment = await readFile(resolve(root, '.local/stack.env'), 'utf8');
  secrets.push(...configuredSecrets(environment));
} catch (error) {
  if (error.code !== 'ENOENT') throw error;
}
let scanned = 0;
const findings = [];
async function walk(directory) {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (ignored.has(entry.name) || entry.name.startsWith('pytest-cache-files-'))
      continue;
    const file = resolve(directory, entry.name);
    if (entry.isSymbolicLink()) continue;
    if (entry.isDirectory()) {
      await walk(file);
      continue;
    }
    if (entry.name.startsWith('.env') && entry.name !== '.env.example') {
      findings.push(
        relative(root, file) +
          ': environment file outside ignored local directory',
      );
      continue;
    }
    if (!extensions.has(extname(file)) && !entry.name.includes('Dockerfile'))
      continue;
    if ((await stat(file)).size > 5_000_000) continue;
    const source = await readFile(file, 'utf8');
    scanned++;
    if (secrets.some((secret) => source.includes(secret)))
      findings.push(relative(root, file) + ': configured local secret');
    if (/-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----/.test(source))
      findings.push(relative(root, file) + ': private key');
    if (
      /\b(?:ghp_[A-Za-z0-9]{36}|AKIA[A-Z0-9]{16}|sk-proj-[A-Za-z0-9_-]{60,})\b/.test(
        source,
      )
    )
      findings.push(relative(root, file) + ': credential pattern');
  }
}
await walk(root);
console.log(JSON.stringify({ scanned_text_files: scanned, findings }, null, 2));
if (findings.length) process.exitCode = 1;
