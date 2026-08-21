import test from 'node:test';
import assert from 'node:assert/strict';

import {
  addESMGuidance,
  clearESM,
  createESM,
  getESM,
  pauseESM,
  resumeESM,
  setESMBudget,
  updateESM
} from './esm.js';

function mockFetch(t, calls) {
  const original = globalThis.fetch;
  globalThis.fetch = async (path, options = {}) => {
    calls.push({ path, method: options.method || 'GET', body: options.body });
    return new Response(JSON.stringify({ status: 'ok' }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' }
    });
  };
  t.after(() => { globalThis.fetch = original; });
}

test('getESM avoids a request until a persisted session exists', async (t) => {
  const calls = [];
  mockFetch(t, calls);

  assert.deepEqual(await getESM(''), { status: 'none' });
  assert.deepEqual(calls, []);
});

test('ESM controls use encoded session routes and the expected HTTP methods', async (t) => {
  const calls = [];
  mockFetch(t, calls);
  const sessionID = 'session /?#';

  await getESM(sessionID);
  await createESM(sessionID, { objective: 'ship it', tokenBudget: 1200 });
  await updateESM(sessionID, { objective: 'ship safely' });
  await pauseESM(sessionID);
  await resumeESM(sessionID);
  await clearESM(sessionID);

  const base = '/api/sessions/session%20%2F%3F%23/esm';
  assert.deepEqual(calls.map(({ path, method }) => ({ path, method })), [
    { path: base, method: 'GET' },
    { path: base, method: 'POST' },
    { path: base, method: 'PATCH' },
    { path: `${base}/pause`, method: 'POST' },
    { path: `${base}/resume`, method: 'POST' },
    { path: base, method: 'DELETE' }
  ]);
  assert.deepEqual(JSON.parse(calls[1].body), { objective: 'ship it', tokenBudget: 1200 });
  assert.deepEqual(JSON.parse(calls[2].body), { objective: 'ship safely' });
});

test('ESM guidance and budget updates retain optimistic concurrency versions', async (t) => {
  const calls = [];
  mockFetch(t, calls);

  await addESMGuidance('session-1', 'focus on tests', 'version-7');
  await setESMBudget('session-1', 4096, 'version-8');

  assert.deepEqual(calls.map(({ path, method, body }) => ({
    path,
    method,
    body: JSON.parse(body)
  })), [
    {
      path: '/api/sessions/session-1/esm/guidance',
      method: 'POST',
      body: { guidance: 'focus on tests', version: 'version-7' }
    },
    {
      path: '/api/sessions/session-1/esm/budget',
      method: 'PATCH',
      body: { tokenBudget: 4096, version: 'version-8' }
    }
  ]);
});
