import { existsSync, mkdirSync, rmSync, cpSync, chmodSync, readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const desktopRoot = resolve(fileURLToPath(new URL('..', import.meta.url)));
const repoRoot = resolve(desktopRoot, '..');
const vendorRoot = join(desktopRoot, 'vendor', 'mothx');
const binName = process.platform === 'win32' ? 'mothx.exe' : 'mothx';

function run(command: string, args: string[], cwd: string): void {
  const result = spawnSync(command, args, { cwd, stdio: 'inherit', shell: process.platform === 'win32' });
  if (result.status !== 0) throw new Error(`${command} ${args.join(' ')} failed`);
}

function packagedBinaryCandidates(root: string): string[] {
  const platformPackage = `mothx-installer-${process.platform === 'darwin' ? 'darwin' : process.platform}-${process.arch}`;
  return [
    // Prefer the real platform package over the main package's JS wrapper.
    join(root, 'node_modules', platformPackage, 'bin', binName),
    join(root, 'bin', binName),
    join(root, 'node_modules', platformPackage, 'bin', binName),
  ];
}

function findPackagedBinary(root: string): string | undefined {
  return packagedBinaryCandidates(root).find(existsSync);
}

function verify(root: string): void {
  const binary = findPackagedBinary(root) || [join(root, 'bin', binName), join(root, binName)].find(existsSync);
  if (!binary) throw new Error(`MothX binary not found in ${root}`);
  if (process.platform !== 'win32') chmodSync(binary, 0o755);
  const result = spawnSync(binary, ['--version'], { cwd: repoRoot, encoding: 'utf8' });
  if (result.status !== 0) throw new Error(`MothX verification failed: ${result.stderr || result.stdout}`);
  console.log(`Vendored ${binary}: ${(result.stdout || '').trim()}`);
}

function localOverride(): string | undefined {
  if (!process.env.MOTHX_LOCAL?.trim()) return undefined;
  const source = join(repoRoot, 'bin', binName);
  if (!existsSync(source)) throw new Error(`MOTHX_LOCAL=1 requires ${source}; run make build first`);
  rmSync(vendorRoot, { recursive: true, force: true });
  mkdirSync(join(vendorRoot, 'bin'), { recursive: true });
  cpSync(source, join(vendorRoot, 'bin', binName));
  return vendorRoot;
}

function tarballOverride(): string | undefined {
  const source = process.env.MOTHX_TARBALL?.trim();
  if (!source) return undefined;
  if (!existsSync(resolve(source))) throw new Error(`MOTHX_TARBALL not found: ${source}`);
  rmSync(vendorRoot, { recursive: true, force: true });
  mkdirSync(vendorRoot, { recursive: true });
  const tar = process.platform === 'win32' ? 'tar.exe' : 'tar';
  run(tar, ['-xzf', resolve(source), '-C', vendorRoot, '--strip-components=1'], desktopRoot);
  return vendorRoot;
}

function npmInstall(): string {
  const packageJson = JSON.parse(readFileSync(join(desktopRoot, 'package.json'), 'utf8')) as { mothxRuntime: { version: string } };
  const version = process.env.MOTHX_VERSION?.trim() || packageJson.mothxRuntime.version;
  rmSync(vendorRoot, { recursive: true, force: true });
  mkdirSync(vendorRoot, { recursive: true });
  writeFileSync(join(vendorRoot, 'package.json'), JSON.stringify({
    name: 'mothx-desktop-runtime', version: '0.0.0', private: true,
    dependencies: { 'mothx-installer': version },
  }, null, 2));
  const npm = process.platform === 'win32' ? 'npm.cmd' : 'npm';
  run(npm, ['install', '--include=optional', '--no-package-lock', '--no-audit', '--no-fund', '--omit=dev', '--install-strategy=nested'], vendorRoot);
  return join(vendorRoot, 'node_modules', 'mothx-installer');
}

const root = localOverride() || tarballOverride() || npmInstall();
const binary = join(vendorRoot, 'bin', binName);
if (!existsSync(binary)) {
  const packagedBinary = findPackagedBinary(root) || [join(root, 'bin', binName), join(root, binName)].find(existsSync);
  if (!packagedBinary) throw new Error(`MothX binary not found in ${root}. Checked: ${packagedBinaryCandidates(root).join(', ')}`);
  mkdirSync(join(vendorRoot, 'bin'), { recursive: true });
  cpSync(packagedBinary, binary);
}
// The npm dependency tree is only a staging area. The final desktop package
// contains the normalized executable at vendor/mothx/bin/mothx[.exe].
rmSync(join(vendorRoot, 'node_modules'), { recursive: true, force: true });
verify(vendorRoot);
