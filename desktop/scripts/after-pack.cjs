const fs = require('node:fs');
const path = require('node:path');

function targetFor(packContext) {
  const platform = packContext.electronPlatformName;
  const arch = packContext.arch === 3 ? 'arm64' : packContext.arch === 1 ? 'amd64' : undefined;
  if (!arch) throw new Error(`Unsupported desktop architecture for bundled CLI: ${packContext.arch}`);
  const goos = platform === 'win32' ? 'windows' : platform;
  return { goos, goarch: arch, binaryName: goos === 'windows' ? 'mothx.exe' : 'mothx' };
}

module.exports = async function afterPack(packContext) {
  const { goos, binaryName } = targetFor(packContext);
  const source = path.join(__dirname, '..', 'vendor', 'mothx', 'bin', binaryName);
  const binary = path.join(packContext.appOutDir, 'vendor', 'mothx', 'bin', binaryName);
  if (!fs.existsSync(source)) {
    throw new Error(`Source-built MothX CLI not found at ${source}. Run npm run build:runtime before packaging.`);
  }
  fs.mkdirSync(path.dirname(binary), { recursive: true });
  fs.copyFileSync(source, binary);
  if (goos !== 'windows') fs.chmodSync(binary, 0o755);
  console.log(`Bundled source-built MothX CLI at ${binary}`);
};
