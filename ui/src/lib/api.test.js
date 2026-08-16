import test from 'node:test';
import assert from 'node:assert/strict';

import { ApiError, postJSON, request } from './api.js';

function mockFetch(t, implementation) {
  const original = globalThis.fetch;
  globalThis.fetch = implementation;
  t.after(() => { globalThis.fetch = original; });
}

test('request preserves structured ErrorInfo fields from an HTTP response', async (t) => {
  mockFetch(t, async () => new Response(JSON.stringify({
    error: {
      message: 'The service is temporarily unavailable.',
      type: 'provider_error',
      code: 'provider_unavailable',
      failureClass: 'transient',
      phase: 'model',
      messageKey: 'run.error.providerUnavailable',
      retryMode: 'automatic',
      retryable: true,
      retryAfterMs: 1200,
      attempt: 2,
      maxAttempts: 3,
      sideEffectState: 'none',
      partialOutput: true,
      runId: 'run_1',
      intentId: 'intent_1',
      requestId: 'req_1'
    }
  }), {
    status: 503,
    headers: { 'Content-Type': 'application/json', 'Retry-After': '3' }
  }));

  await assert.rejects(request('/api/runs/run_1'), (error) => {
    assert.ok(error instanceof ApiError);
    assert.equal(error.status, 503);
    assert.equal(error.code, 'provider_unavailable');
    assert.equal(error.type, 'provider_error');
    assert.equal(error.failureClass, 'transient');
    assert.equal(error.phase, 'model');
    assert.equal(error.retryMode, 'automatic');
    assert.equal(error.retryAfterMs, 1200);
    assert.equal(error.runId, 'run_1');
    assert.equal(error.intentId, 'intent_1');
    assert.equal(error.requestId, 'req_1');
    assert.equal(error.detail.sideEffectState, 'none');
    return true;
  });
});

test('postJSON keeps caller supplied idempotency headers', async (t) => {
  let options;
  mockFetch(t, async (_path, nextOptions) => {
    options = nextOptions;
    return new Response(JSON.stringify({ runId: 'run_1' }), { status: 202 });
  });

  const result = await postJSON('/api/sessions/session_1/runs', { message: 'hello' }, {
    headers: { 'Idempotency-Key': 'submit_1' }
  });

  assert.equal(result.runId, 'run_1');
  assert.equal(new Headers(options.headers).get('content-type'), 'application/json');
  assert.equal(new Headers(options.headers).get('idempotency-key'), 'submit_1');
});

test('request uses Retry-After when ErrorInfo omits a retry delay', async (t) => {
  mockFetch(t, async () => new Response(JSON.stringify({
    error: { message: 'Try again later', code: 'rate_limited' }
  }), {
    status: 429,
    headers: { 'Retry-After': '2' }
  }));

  await assert.rejects(request('/api/runs/run_1/retry'), (error) => {
    assert.ok(error instanceof ApiError);
    assert.equal(error.retryAfterMs, 2000);
    return true;
  });
});

test('request wraps network failures but preserves caller aborts', async (t) => {
  mockFetch(t, async () => { throw new TypeError('offline'); });
  await assert.rejects(request('/api/health'), (error) => {
    assert.ok(error instanceof ApiError);
    assert.equal(error.code, 'network_error');
    assert.equal(error.message, 'offline');
    return true;
  });
});
