import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const main = await readFile(new URL('./index.ts', import.meta.url), 'utf8');
const ipc = await readFile(new URL('./ipc.ts', import.meta.url), 'utf8');
const store = await readFile(new URL('./store.ts', import.meta.url), 'utf8');

test('ACP startup cwd is the launch scratch directory, never the stored picker location', () => {
  const runtimeCwd = main.match(/function resolveRuntimeCwd\(\): string \{[\s\S]*?\n\}/)?.[0] || '';
  assert.match(runtimeCwd, /return initialNewSessionDirectory\(\);/);
  assert.doesNotMatch(runtimeCwd, /lastWorkspace/);
});

test('restarting ACP cannot overwrite the directory-picker history', () => {
  const restart = ipc.match(/ipcMain\.handle\('acp:restart',[\s\S]*?\n  \}\);/)?.[0] || '';
  assert.doesNotMatch(restart, /lastWorkspace/);
  assert.doesNotMatch(restart, /getState\(\)\.workspace/);
  assert.match(store, /legacy on-disk field name for the picker location/);
});
