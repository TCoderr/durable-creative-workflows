// Inspect credential-bearing configuration, excluding public identifiers such
// as a Temporal volume name. Pattern checks in secret-scan remain independent.
export function configuredSecrets(environment) {
  const values = [];
  for (const line of environment.split('\n')) {
    const separator = line.indexOf('=');
    if (separator < 0) continue;
    const key = line.slice(0, separator).trim();
    let value = line.slice(separator + 1).trim();
    if (
      value.length >= 2 &&
      ['"', "'"].includes(value[0]) &&
      value.at(-1) === value[0]
    )
      value = value.slice(1, -1);
    if (key === 'VELIN_API_KEYS') {
      values.push(...Object.keys(JSON.parse(value)));
    } else if (
      /PASSWORD|TOKEN|API_KEY|SECRET|DATABASE_URL/.test(key) &&
      value
    ) {
      values.push(value);
    }
  }
  return values;
}
