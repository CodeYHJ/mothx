import { createRequire } from 'node:module';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const require = createRequire(import.meta.url);
const { resolveVersion } = require('./resolve-version.cjs') as { resolveVersion: () => string };

const pkg = JSON.parse(readFileSync(join(import.meta.dirname, '..', 'package.json'), 'utf8')) as {
  version: string;
  mothxRuntime?: { version?: string };
};
const expected = resolveVersion();
if (pkg.mothxRuntime?.version !== pkg.version) {
  throw new Error(`desktop version ${pkg.version} does not match mothxRuntime.version ${pkg.mothxRuntime?.version}`);
}
if (expected !== pkg.version) {
  throw new Error(`resolved git/tag version ${expected} does not match desktop version ${pkg.version}`);
}
console.log(`Desktop version OK: ${pkg.version}`);
