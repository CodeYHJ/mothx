const fs = require('node:fs');
const path = require('node:path');
const cp = require('node:child_process');

const electronDir = path.dirname(require.resolve('electron/package.json'));
const installScript = path.join(electronDir, 'install.js');
const executablePath = process.platform === 'darwin'
  ? path.join(electronDir, 'dist', 'Electron.app', 'Contents', 'MacOS', 'Electron')
  : path.join(electronDir, 'dist', process.platform === 'win32' ? 'electron.exe' : 'electron');

if (fs.existsSync(executablePath)) {
  console.log(`Electron runtime is ready: ${executablePath}`);
  process.exit(0);
}

if (!fs.existsSync(installScript)) {
  console.error(`Electron runtime is missing and install script was not found: ${installScript}`);
  process.exit(1);
}

const attempts = Number(process.env.ELECTRON_INSTALL_ATTEMPTS || 3);
const mirrors = [
  process.env.ELECTRON_MIRROR?.trim(),
  'https://npmmirror.com/mirrors/electron/',
  undefined,
].filter((mirror, index, list) => mirror || list.indexOf(mirror) === index);

for (const mirror of mirrors) {
  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    console.log(`Installing Electron runtime (attempt ${attempt}/${attempts}${mirror ? `, mirror ${mirror}` : ''})...`);
    const env = { ...process.env };
    if (mirror) env.ELECTRON_MIRROR = mirror;
    else delete env.ELECTRON_MIRROR;
    const result = cp.spawnSync(process.execPath, [installScript], {
      cwd: electronDir,
      stdio: 'inherit',
      env,
    });
    if (result.status === 0 && fs.existsSync(executablePath)) {
      console.log(`Electron runtime is ready: ${executablePath}`);
      process.exit(0);
    }
  }
}

console.error(`Electron runtime installation failed for ${process.platform}-${process.arch}. Check network access to the Electron download mirror.`);
process.exit(1);
