const assert = require('node:assert/strict');
const test = require('node:test');

const { normalizeVersion, resolveVersion } = require('./resolve-version.cjs');

test('normalizeVersion strips v prefix, dirty, and git describe suffixes', () => {
  assert.equal(normalizeVersion('v1.2.92'), '1.2.92');
  assert.equal(normalizeVersion('1.2.92-dirty'), '1.2.92');
  assert.equal(normalizeVersion('v1.2.92-3-gabcd123'), '1.2.92');
  assert.equal(normalizeVersion('v1.2.92-pre'), '1.2.92-pre');
});

test('resolveVersion prefers MOTHX_VERSION over git', () => {
  assert.equal(resolveVersion({
    env: { MOTHX_VERSION: 'v1.2.93' },
    describe: () => 'v9.9.9',
  }), '1.2.93');
});

test('resolveVersion uses GitHub tag refs but ignores branch names', () => {
  assert.equal(resolveVersion({
    env: { GITHUB_REF: 'refs/tags/v1.2.90', GITHUB_REF_NAME: 'v1.2.90' },
    describe: () => 'v9.9.9',
  }), '1.2.90');
  assert.equal(resolveVersion({
    env: { GITHUB_REF: 'refs/heads/main', GITHUB_REF_NAME: 'main' },
    describe: () => 'v1.2.91',
  }), '1.2.91');
});

test('resolveVersion falls back to git describe then dev', () => {
  assert.equal(resolveVersion({
    env: {},
    describe: () => 'v1.2.88',
  }), '1.2.88');
  assert.equal(resolveVersion({
    env: {},
    describe: () => '',
  }), 'dev');
});
