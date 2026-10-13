import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const rendererIndex = await readFile(new URL('../renderer/index.html', import.meta.url), 'utf8');
const main = await readFile(new URL('./index.ts', import.meta.url), 'utf8');

test('renderer declares an effective content security policy', () => {
  assert.match(
    rendererIndex,
    /http-equiv="Content-Security-Policy"/,
    'Electron only recognizes the exact Content-Security-Policy directive name',
  );
});

test('existing renderer WebContents cannot navigate away from the local UI', () => {
  assert.match(main, /webContents\.on\('will-navigate',[\s\S]*?event\.preventDefault\(\)/);
  assert.match(main, /webContents\.setWindowOpenHandler\([\s\S]*?shell\.openExternal/);
});
