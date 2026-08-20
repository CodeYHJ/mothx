import test from 'node:test';
import assert from 'node:assert/strict';

import { ApiError, postJSON, readSSE, request } from './api.js';

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
      requestId: 'req_1',
      detail: 'API error 503: upstream overloaded'
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
    assert.equal(error.detail.detail, 'API error 503: upstream overloaded');
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

test('request wraps network failures in a structured API error', async (t) => {
  mockFetch(t, async () => { throw new TypeError('offline'); });
  await assert.rejects(request('/api/health'), (error) => {
    assert.ok(error instanceof ApiError);
    assert.equal(error.code, 'network_error');
    assert.equal(error.message, 'offline');
    return true;
  });
});

test('request aborts a stalled fetch at the configured timeout', async (t) => {
  mockFetch(t, async (_path, options) => new Promise((_resolve, reject) => {
    options.signal.addEventListener('abort', () => reject(options.signal.reason), { once: true });
  }));

  await assert.rejects(request('/api/health', { timeoutMs: 10 }), (error) => {
    assert.ok(error instanceof ApiError);
    assert.equal(error.name, 'TimeoutError');
    assert.equal(error.code, 'request_timeout');
    return true;
  });
});

test('request preserves an abort initiated by its caller', async (t) => {
  const controller = new AbortController();
  const reason = new DOMException('navigation changed', 'AbortError');
  controller.abort(reason);
  mockFetch(t, async (_path, options) => {
    options.signal.throwIfAborted();
  });

  await assert.rejects(request('/api/health', { signal: controller.signal }), (error) => error === reason);
});

test('readSSE handles chunk boundaries, CRLF, comments, and a final unterminated event', async () => {
  const encoder = new TextEncoder();
  const payload = encoder.encode(': heartbeat\r\nevent: delta\r\ndata: first \ud83d\ude80\r\ndata: second\r\n\r\ndata: tail');
  const expected = [
    { event: 'delta', data: 'first \ud83d\ude80\nsecond' },
    { event: 'message', data: 'tail' }
  ];

  // TCP and fetch streams may split at any byte, including between CR/LF or
  // in the middle of a multi-byte UTF-8 character.
  for (let split = 1; split < payload.length; split += 1) {
    const chunks = [payload.slice(0, split), payload.slice(split)];
    const body = new ReadableStream({
      pull(controller) {
        if (chunks.length === 0) controller.close();
        else controller.enqueue(chunks.shift());
      }
    });
    const events = [];

    await readSSE(body, (event) => events.push(event));

    assert.deepEqual(events, expected, `incorrect events when split at byte ${split}`);
    assert.equal(body.locked, false, 'the stream reader should always release its lock');
  }
});
