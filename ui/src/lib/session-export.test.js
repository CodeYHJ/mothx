import test from 'node:test';
import assert from 'node:assert/strict';
import { downloadSessionLog, prepareSessionLog, sessionLogURL } from './session-export.js';

test('session log URL encodes the session and descendant policy', () => {
  assert.equal(
    sessionLogURL('session / 1', false),
    '/api/sessions/session%20%2F%201/export?format=log&include_descendants=false'
  );
});

test('session log preflight is shared and browser download avoids blob buffering', async (t) => {
  const originalFetch = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = async (_path, options) => {
    calls += 1;
    assert.equal(options.method, 'HEAD');
    return new Response('', { status: 200 });
  };
  t.after(() => { globalThis.fetch = originalFetch; });

  const [first, second] = await Promise.all([
    prepareSessionLog('session-1'),
    prepareSessionLog('session-1')
  ]);
  assert.equal(calls, 1);
  assert.deepEqual(first, second);
  assert.deepEqual(await downloadSessionLog('session-1'), first);
  assert.equal(calls, 2);
});
