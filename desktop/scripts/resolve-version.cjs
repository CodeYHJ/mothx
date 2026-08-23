const { spawnSync } = require('node:child_process');
const path = require('node:path');

function normalizeVersion(raw) {
  let version = String(raw || '').trim();
  if (!version) return '';
  version = version.replace(/^v/, '');
  version = version.replace(/-dirty$/, '');
  version = version.replace(/-\d+-g[0-9a-f]+$/i, '');
  return version;
}

function gitDescribe(repoRoot) {
  const result = spawnSync('git', ['describe', '--tags', '--abbrev=0'], {
    cwd: repoRoot,
    encoding: 'utf8',
  });
  if (result.status !== 0) return '';
  return String(result.stdout || '').trim();
}

function resolveVersion(options = {}) {
  const env = options.env || process.env;
  const repoRoot = options.repoRoot || path.resolve(__dirname, '..', '..');
  const describe = options.describe || (() => gitDescribe(repoRoot));
  const githubRef = String(env.GITHUB_REF || '').trim();
  const githubTag = githubRef.startsWith('refs/tags/') ? env.GITHUB_REF_NAME : '';
  const candidates = [
    env.MOTHX_VERSION,
    env.GITEE_BRANCH,
    githubTag,
    describe(),
    'dev',
  ];
  for (const candidate of candidates) {
    const version = normalizeVersion(candidate);
    if (version) return version;
  }
  return 'dev';
}

module.exports = { normalizeVersion, resolveVersion };
