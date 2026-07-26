const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

function run(command, args, options) {
  const result = spawnSync(command, args, { ...options, stdio: 'inherit' });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${command} ${args.join(' ')} failed with status ${result.status}`);
}

function targetFor(packContext) {
  const platform = packContext.electronPlatformName;
  const arch = packContext.arch === 3 ? 'arm64' : packContext.arch === 1 ? 'amd64' : undefined;
  if (!arch) throw new Error(`Unsupported desktop architecture for bundled CLI: ${packContext.arch}`);
  const goos = platform === 'win32' ? 'windows' : platform;
  return { goos, goarch: arch, binaryName: goos === 'windows' ? 'mothx.exe' : 'mothx' };
}

module.exports = async function afterPack(packContext) {
  const repoRoot = path.resolve(__dirname, '..', '..');
  const { goos, goarch, binaryName } = targetFor(packContext);
  const binary = path.join(packContext.appOutDir, 'vendor', 'mothx', 'bin', binaryName);
  const ext = goos === 'windows' ? '.exe' : '';
  const tempBinary = path.join(packContext.appOutDir, `.mothx-${goos}-${goarch}${ext}`);
  const version = process.env.MOTHX_VERSION || process.env.GITHUB_REF_NAME?.replace(/^v/, '') || 'dev';

  fs.mkdirSync(path.dirname(binary), { recursive: true });
  console.log(`Building bundled MothX CLI from source for ${goos}/${goarch}...`);
  run('go', [
    'build', '-trimpath',
    '-ldflags', `-s -w -X main.version=${version} -X github.com/startvibecoding/mothx/internal/ua.Version=${version}`,
    '-o', tempBinary,
    './cmd/mothx',
  ], {
    cwd: repoRoot,
    env: { ...process.env, CGO_ENABLED: '0', GOOS: goos, GOARCH: goarch },
  });

  fs.copyFileSync(tempBinary, binary);
  fs.rmSync(tempBinary, { force: true });
  if (goos !== 'windows') fs.chmodSync(binary, 0o755);
  console.log(`Bundled source-built MothX CLI at ${binary}`);
};
