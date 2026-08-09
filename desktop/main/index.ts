import { app, BrowserWindow, dialog, ipcMain, session, shell } from 'electron';
import { spawn, ChildProcess } from 'node:child_process';
import { createServer } from 'node:http';
import { existsSync, mkdirSync, writeFileSync, createWriteStream, appendFileSync } from 'node:fs';
import { join } from 'node:path';
import { randomBytes } from 'node:crypto';
import { get } from 'node:http';

import { localServeRequestURLPatterns } from './network';

let serve: ChildProcess | undefined;
let servePort = 0;
let serveToken = '';
let windowRef: BrowserWindow | undefined;
let stopping = false;
let desktopLogPath = '';

function logDesktopEvent(message: string): void {
  const line = `${new Date().toISOString()} [desktop] ${message}\n`;
  console.log(line.trimEnd());
  if (!desktopLogPath) return;
  try {
    appendFileSync(desktopLogPath, line);
  } catch {
    // Logging must never interfere with starting or stopping the bundled server.
  }
}

ipcMain.on('desktop:log-diagnostic', (_event, message: unknown) => {
  const text = String(message || '').replace(/[\r\n]+/g, ' ').slice(0, 2000);
  if (text) logDesktopEvent(`renderer: ${text}`);
});

ipcMain.handle('desktop:choose-directory', async (event, defaultPath?: string) => {
  if (!event.sender || event.sender.isDestroyed()) return null;
  const result = await dialog.showOpenDialog({
    defaultPath: defaultPath || undefined,
    properties: ['openDirectory', 'createDirectory'],
    title: 'Select working directory',
  });
  return result.canceled ? null : result.filePaths[0] || null;
});

// AppImage mounts are commonly `nosuid`, so Electron's SUID sandbox helper
// cannot be used even though the application itself is otherwise valid. The
// desktop app runs its own Go server on localhost and does not load arbitrary
// local code, so use Chromium's fallback only for packaged Linux builds.
if (process.platform === 'linux' && app.isPackaged) {
  app.commandLine.appendSwitch('no-sandbox');
  // Some Linux hosts and CI-produced AppImage environments do not provide a
  // writable /dev/shm, which crashes Chromium's renderer before the UI can
  // paint. Keep Chromium's shared-memory data under Electron's userData dir.
  app.commandLine.appendSwitch('disable-dev-shm-usage');
  // Avoid GPU-process startup failures on headless/remote Linux desktops.
  app.commandLine.appendSwitch('disable-gpu');
}

function binaryPath(): string {
  const name = process.platform === 'win32' ? 'mothx.exe' : 'mothx';
  const roots = [
    // electron-builder places the explicit `vendor` file pattern beside
    // `resources/app` when asar is disabled.
    join(process.resourcesPath, '..', 'vendor', 'mothx', 'bin', name),
    join(process.resourcesPath, '..', 'vendor', 'mothx', name),
    join(process.resourcesPath, 'app', 'vendor', 'mothx', 'bin', name),
    join(process.resourcesPath, 'app', 'vendor', 'mothx', name),
    join(__dirname, '..', '..', 'vendor', 'mothx', 'bin', name),
    join(__dirname, '..', '..', 'vendor', 'mothx', name),
    join(__dirname, '..', 'vendor', 'mothx', 'bin', name),
    join(__dirname, '..', 'vendor', 'mothx', name),
  ];
  const found = roots.find(existsSync);
  if (!found) throw new Error(`MothX runtime not found. Checked:\n${roots.join('\n')}`);
  return found;
}

function availablePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      const port = typeof address === 'object' && address ? address.port : 0;
      server.close(() => resolve(port));
    });
  });
}

function health(port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const req = get(`http://127.0.0.1:${port}/health`, { timeout: 500 }, (res: any) => {
      res.resume();
      resolve(res.statusCode === 200);
    });
    req.on('error', () => resolve(false));
    req.on('timeout', () => { req.destroy(); resolve(false); });
  });
}

async function waitForHealth(port: number, timeoutMs = 30000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await health(port)) return;
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  throw new Error('MothX serve did not become ready');
}

async function startServe(): Promise<void> {
  servePort = await availablePort();
  serveToken = randomBytes(32).toString('hex');
  const logDir = join(app.getPath('userData'), 'logs');
  mkdirSync(logDir, { recursive: true });
  const logPath = join(logDir, 'serve.log');
  desktopLogPath = join(logDir, 'desktop.log');
  const log = createWriteStream(logPath, { flags: 'a' });
  const configPath = join(app.getPath('userData'), 'desktop-serve.json');
  writeFileSync(configPath, JSON.stringify({
    api: { listen: `127.0.0.1:${servePort}`, auth: { enabled: true, tokens: [serveToken] } },
    features: { webUI: true, openaiAPI: true },
    webUI: { enabled: true },
  }, null, 2));
  const args = ['serve', '--config', configPath];
  serve = spawn(binaryPath(), args, {
    cwd: app.getPath('home'),
    env: { ...process.env },
    stdio: ['ignore', 'pipe', 'pipe'],
    windowsHide: true,
  });
  serve.stdout?.pipe(log);
  serve.stderr?.pipe(log);
  const startupError = new Promise<never>((_, reject) => {
    serve?.once('error', reject);
  });
  serve.once('exit', (code, signal) => {
    logDesktopEvent(`serve exited: code=${code ?? 'none'} signal=${signal ?? 'none'}`);
    if (!stopping && windowRef && !windowRef.isDestroyed()) {
      void dialog.showErrorBox('MothX stopped', `The MothX server stopped unexpectedly.\nLog: ${logPath}`);
      void startServe().then(() => windowRef?.loadURL(url())).catch(showStartupError);
    }
  });
  try {
    await Promise.race([waitForHealth(servePort), startupError]);
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    throw new Error(`Unable to start bundled MothX runtime: ${message}.\nLog: ${logPath}`);
  }
}

function url(): string { return `http://127.0.0.1:${servePort}/`; }

function showStartupError(error: unknown): void {
  const message = error instanceof Error ? error.message : String(error);
  void dialog.showErrorBox('Unable to start MothX', `${message}\n\nCheck the desktop log under your MothX user data directory.`);
}

function createWindow(): void {
  windowRef = new BrowserWindow({
    title: 'MothX Desktop',
    width: 1440,
    height: 900,
    minWidth: 900,
    minHeight: 640,
    autoHideMenuBar: app.isPackaged,
    menuBarVisible: !app.isPackaged,
    webPreferences: {
      preload: join(__dirname, 'preload.cjs'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: !(process.platform === 'linux' && app.isPackaged),
      devTools: !app.isPackaged,
    },
  });
  const filter = { urls: localServeRequestURLPatterns(servePort) };
  session.defaultSession.webRequest.onBeforeSendHeaders(filter, (details, callback) => {
    details.requestHeaders.Authorization = `Bearer ${serveToken}`;
    callback({ requestHeaders: details.requestHeaders });
  });
  windowRef.webContents.setWindowOpenHandler(({ url: target }) => {
    if (!target.startsWith(`http://127.0.0.1:${servePort}`)) void shell.openExternal(target);
    return { action: 'deny' };
  });
  windowRef.webContents.on('render-process-gone', (_event, details) => {
    console.error(`[desktop] renderer process gone: ${details.reason} ${details.exitCode}`);
  });
  windowRef.webContents.on('did-fail-load', (_event, errorCode, errorDescription, validatedURL, isMainFrame) => {
    if (isMainFrame) {
      console.error(`[desktop] failed to load ${validatedURL}: ${errorCode} ${errorDescription}`);
    }
  });
  windowRef.webContents.on('did-finish-load', () => {
    console.log(`[desktop] loaded ${url()}`);
  });
  void windowRef.loadURL(url(), {
    extraHeaders: `Authorization: Bearer ${serveToken}\n`,
  }).catch(showStartupError);
}

function waitForServeExit(child: ChildProcess, timeoutMs: number): Promise<boolean> {
  if (child.exitCode !== null || child.signalCode !== null) return Promise.resolve(true);
  return new Promise((resolve) => {
    let finished = false;
    const finish = (exited: boolean) => {
      if (finished) return;
      finished = true;
      clearTimeout(timer);
      child.removeListener('exit', onExit);
      resolve(exited);
    };
    const onExit = () => finish(true);
    const timer = setTimeout(() => finish(false), timeoutMs);
    child.once('exit', onExit);
  });
}

async function stopServe(): Promise<void> {
  stopping = true;
  const child = serve;
  if (!child || child.exitCode !== null || child.signalCode !== null) return;

  try {
    child.kill('SIGTERM');
  } catch (error) {
    logDesktopEvent(`failed to stop serve gracefully: ${error instanceof Error ? error.message : String(error)}`);
  }
  if (await waitForServeExit(child, 1500)) return;

  logDesktopEvent('serve did not exit after 1500ms; forcing termination');
  try {
    child.kill('SIGKILL');
  } catch (error) {
    logDesktopEvent(`failed to force-stop serve: ${error instanceof Error ? error.message : String(error)}`);
    return;
  }
  if (!(await waitForServeExit(child, 1500))) {
    logDesktopEvent('serve did not exit after forced termination');
  }
}

const gotLock = app.requestSingleInstanceLock();
if (!gotLock) app.quit();
else {
  app.on('second-instance', () => windowRef?.show());
  app.whenReady().then(async () => {
    try { await startServe(); createWindow(); }
    catch (error) { showStartupError(error); app.quit(); }
  });
  app.on('before-quit', () => { void stopServe(); });
  app.on('window-all-closed', () => { if (process.platform !== 'darwin') app.quit(); });
  app.on('activate', () => { if (!windowRef || windowRef.isDestroyed()) createWindow(); });
}
