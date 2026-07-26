const fs = require('node:fs');
const path = require('node:path');

const raw = process.env.MOTHX_VERSION?.trim();
if (!raw) throw new Error('MOTHX_VERSION is required');

const version = raw.replace(/^v/, '');
if (!version) throw new Error(`Invalid MOTHX_VERSION: ${raw}`);

for (const filename of ['package.json', 'package-lock.json']) {
  const file = path.join(__dirname, '..', filename);
  const pkg = JSON.parse(fs.readFileSync(file, 'utf8'));
  pkg.version = version;
  if (pkg.mothxRuntime) pkg.mothxRuntime.version = version;
  if (pkg.packages?.['']) pkg.packages[''].version = version;
  fs.writeFileSync(file, `${JSON.stringify(pkg, null, 2)}\n`);
}

console.log(`Desktop version set to ${version}`);
