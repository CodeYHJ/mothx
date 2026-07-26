import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const pkg = JSON.parse(readFileSync(join(import.meta.dirname, '..', 'package.json'), 'utf8')) as {
  version: string;
  mothxRuntime?: { version?: string };
};
const expected = process.env.MOTHX_VERSION?.trim() || process.env.GITHUB_REF_NAME?.trim();
if (pkg.mothxRuntime?.version !== pkg.version) {
  throw new Error(`desktop version ${pkg.version} does not match mothxRuntime.version ${pkg.mothxRuntime?.version}`);
}
if (expected) {
  const releaseVersion = expected.replace(/^v/, '').split('-', 1)[0];
  if (releaseVersion !== pkg.version) {
    throw new Error(`release ref ${expected} does not match desktop version ${pkg.version}`);
  }
}
console.log(`Desktop version OK: ${pkg.version}`);
