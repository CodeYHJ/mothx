const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const { chmodSync, cpSync, mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } = require('node:fs');
const { tmpdir } = require('node:os');
const path = require('node:path');
const test = require('node:test');

const sourceWrapper = path.join(__dirname, 'npm-installer-wrapper.js');

const platformPackages = {
  'linux-x64': ['mothx-installer-linux-x64', 'mothx-installer-linux-musl-x64'],
  'linux-arm64': ['mothx-installer-linux-arm64', 'mothx-installer-linux-musl-arm64'],
  'linux-loong64': ['mothx-installer-linux-loong64'],
  'linux-ppc64': ['mothx-installer-linux-ppc64le'],
  'linux-s390x': ['mothx-installer-linux-s390x'],
  'linux-riscv64': ['mothx-installer-linux-riscv64'],
  'darwin-x64': ['mothx-installer-darwin-x64'],
  'darwin-arm64': ['mothx-installer-darwin-arm64'],
  'freebsd-x64': ['mothx-installer-freebsd-x64'],
  'freebsd-arm64': ['mothx-installer-freebsd-arm64'],
  'openbsd-x64': ['mothx-installer-openbsd-x64'],
  'openbsd-arm64': ['mothx-installer-openbsd-arm64'],
  'netbsd-x64': ['mothx-installer-netbsd-x64']
};

const fallbackNames = {
  'linux-x64': 'mothx-linux-amd64',
  'linux-arm64': 'mothx-linux-arm64',
  'linux-loong64': 'mothx-linux-loong64',
  'linux-ppc64': 'mothx-linux-ppc64le',
  'linux-s390x': 'mothx-linux-s390x',
  'linux-riscv64': 'mothx-linux-riscv64',
  'darwin-x64': 'mothx-darwin-amd64',
  'darwin-arm64': 'mothx-darwin-arm64',
  'freebsd-x64': 'mothx-freebsd-amd64',
  'freebsd-arm64': 'mothx-freebsd-arm64',
  'openbsd-x64': 'mothx-openbsd-amd64',
  'openbsd-arm64': 'mothx-openbsd-arm64',
  'netbsd-x64': 'mothx-netbsd-amd64'
};

function setupPackage(t, packageName = 'mothx-installer') {
  const root = mkdtempSync(path.join(tmpdir(), 'mothx-npm-wrapper-'));
  const binDir = path.join(root, 'bin');
  mkdirSync(binDir, { recursive: true });
  const wrapper = path.join(binDir, packageName === 'vibecoding-installer' ? 'vibecoding' : 'mothx');
  cpSync(sourceWrapper, wrapper);
  writeFileSync(path.join(root, 'package.json'), JSON.stringify({ name: packageName }));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  return { root, binDir, wrapper };
}

function writeFakeBinary(file) {
  mkdirSync(path.dirname(file), { recursive: true });
  writeFileSync(file, `#!/usr/bin/env node
const fs = require('fs');
fs.writeFileSync(process.env.MOTHX_TEST_CAPTURE, JSON.stringify(process.argv.slice(2)));
process.exit(Number(process.env.MOTHX_TEST_EXIT || 0));
`);
  chmodSync(file, 0o755);
}

function runWrapper(wrapper, capture, args, extraEnv = {}) {
  const nodeDir = path.dirname(process.execPath);
  const inheritedPath = process.env.PATH || process.env.Path || '';
  return spawnSync(process.execPath, [wrapper, ...args], {
    encoding: 'utf8',
    env: {
      ...process.env,
      PATH: [nodeDir, inheritedPath].filter(Boolean).join(path.delimiter),
      MOTHX_TEST_CAPTURE: capture,
      ...extraEnv
    }
  });
}

test('wrapper executes the installed platform package and preserves arguments', (t) => {
  if (process.platform === 'win32') t.skip('Unix executable fixture');
  const key = `${process.platform}-${process.arch}`;
  const packages = platformPackages[key];
  if (!packages) t.skip(`unsupported test platform ${key}`);

  const { root, wrapper } = setupPackage(t);
  const capture = path.join(root, 'args.json');
  for (const packageName of packages) {
    const packageDir = path.join(root, 'node_modules', packageName);
    mkdirSync(packageDir, { recursive: true });
    writeFileSync(path.join(packageDir, 'package.json'), JSON.stringify({ name: packageName }), { flag: 'w' });
    writeFakeBinary(path.join(packageDir, 'bin', 'mothx'));
  }

  const args = ['serve', '--port', '9090', '--provider', 'name with spaces'];
  const result = runWrapper(wrapper, capture, args);

  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(JSON.parse(readFileSync(capture, 'utf8')), args);
});

test('wrapper forwards the native binary exit code', (t) => {
  if (process.platform === 'win32') t.skip('Unix executable fixture');
  const key = `${process.platform}-${process.arch}`;
  const fallback = fallbackNames[key];
  if (!fallback) t.skip(`unsupported test platform ${key}`);

  const { root, binDir, wrapper } = setupPackage(t);
  const capture = path.join(root, 'args.json');
  writeFakeBinary(path.join(binDir, fallback));

  const result = runWrapper(wrapper, capture, ['doctor'], { MOTHX_TEST_EXIT: '23' });

  assert.equal(result.status, 23, result.stderr);
  assert.deepEqual(JSON.parse(readFileSync(capture, 'utf8')), ['doctor']);
});

test('wrapper gives an actionable error when the platform binary is absent', (t) => {
  const { root, wrapper } = setupPackage(t);
  const result = runWrapper(wrapper, path.join(root, 'unused.json'), []);

  assert.equal(result.status, 1);
  assert.match(result.stderr, /Could not find mothx binary for platform:/);
  assert.match(result.stderr, /npm install -g mothx-installer/);
});
