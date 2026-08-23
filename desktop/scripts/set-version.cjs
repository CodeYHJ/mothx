const fs = require('node:fs');
const path = require('node:path');

const { resolveVersion } = require('./resolve-version.cjs');

const version = resolveVersion();
if (!version) throw new Error('Could not resolve desktop version from MOTHX_VERSION, git tag, or fallback');

for (const filename of ['package.json', 'package-lock.json']) {
  const file = path.join(__dirname, '..', filename);
  const pkg = JSON.parse(fs.readFileSync(file, 'utf8'));
  pkg.version = version;
  if (pkg.mothxRuntime) pkg.mothxRuntime.version = version;
  if (pkg.packages?.['']) pkg.packages[''].version = version;
  fs.writeFileSync(file, `${JSON.stringify(pkg, null, 2)}\n`);
}

console.log(`Desktop version set to ${version}`);
