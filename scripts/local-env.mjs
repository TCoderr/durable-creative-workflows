import { randomBytes } from 'node:crypto';
import { mkdir, writeFile, access, readFile, chmod } from 'node:fs/promises';

const directory = new URL('../.local/', import.meta.url);
const target = new URL('stack.env', directory);
await mkdir(directory, { recursive: true });
try {
  await access(target);
  console.log('Local environment already exists; credentials were preserved.');
} catch {
  const secret = () => randomBytes(32).toString('hex');
  const token = secret();
  const otherToken = secret();
  const values = {
    POSTGRES_PASSWORD: secret(),
    CAPABILITIES_TOKEN: secret(),
    NATS_TOKEN: secret(),
    VELIN_API_KEYS: JSON.stringify({
      [token]: 'local-commissioner',
      [otherToken]: 'local-other-owner',
    }),
  };
  await writeFile(
    target,
    Object.entries(values)
      .map(([key, value]) => `${key}=${value}`)
      .join('\n') + '\n',
    { mode: 0o600, flag: 'wx' },
  );
  await writeFile(
    new URL('test-access.json', directory),
    JSON.stringify({ token, otherToken }, null, 2),
    { mode: 0o600, flag: 'wx' },
  );
  console.log(
    'Generated ignored local credentials in .local/stack.env and .local/test-access.json. Values were not printed.',
  );
}

const environment = await readFile(target, 'utf8');
const aiToken = environment
  .split('\n')
  .find((line) => line.startsWith('CAPABILITIES_TOKEN='))
  ?.slice('CAPABILITIES_TOKEN='.length);
if (!aiToken)
  throw new Error('Existing local environment has no AI service token.');
// Prometheus reads this token inside its container as an unprivileged user, so
// the file must be world-readable. It lives in the ignored .local directory and
// only grants access to the loopback-bound local capability service.
const tokenFile = new URL('capabilities-token', directory);
await writeFile(tokenFile, aiToken, { mode: 0o644 });
await chmod(tokenFile, 0o644);
