// Thin HTTP + SSE helpers shared across views.
// Keeps fetch/JSON error handling in one place so views stay declarative.

const jsonHeaders = { 'Content-Type': 'application/json' };
const DEFAULT_TIMEOUT_MS = 15000;

// ApiError preserves the structured error contract exposed by Serve. Views can
// render a friendly message while still using status/code metadata to decide
// whether a user action can be retried.
export class ApiError extends Error {
  constructor(message, options = {}) {
    super(message || 'Request failed');
    this.name = 'ApiError';
    this.status = Number(options.status || 0);
    this.code = String(options.code || '');
    this.type = String(options.type || '');
    this.failureClass = String(options.failureClass || '');
    this.phase = String(options.phase || '');
    this.messageKey = String(options.messageKey || '');
    this.retryMode = String(options.retryMode || '');
    this.retryable = Boolean(options.retryable);
    this.retryAfterMs = Number(options.retryAfterMs || 0);
    this.attempt = Number(options.attempt || 0);
    this.maxAttempts = Number(options.maxAttempts || 0);
    this.sideEffectState = String(options.sideEffectState || '');
    this.partialOutput = Boolean(options.partialOutput);
    this.runId = String(options.runId || '');
    this.intentId = String(options.intentId || '');
    this.requestId = String(options.requestId || '');
    this.detail = options.detail ?? null;
    if (options.cause !== undefined) this.cause = options.cause;
  }
}

export async function request(path, options = {}) {
  const { timeoutMs = DEFAULT_TIMEOUT_MS, signal: callerSignal, ...fetchOptions } = options;
  const controller = new AbortController();
  const timeout = Number.isFinite(timeoutMs) && timeoutMs > 0
    ? setTimeout(() => {
        const reason = new Error(`Request timed out after ${timeoutMs}ms`);
        reason.name = 'TimeoutError';
        controller.abort(reason);
      }, timeoutMs)
    : null;
  const forwardAbort = () => controller.abort(callerSignal.reason);
  if (callerSignal) {
    if (callerSignal.aborted) forwardAbort();
    else callerSignal.addEventListener('abort', forwardAbort, { once: true });
  }

  let res;
  try {
    res = await fetch(path, { ...fetchOptions, signal: controller.signal });
  } catch (err) {
    if (err?.name === 'TimeoutError' || controller.signal.reason?.name === 'TimeoutError') {
      const timeoutError = new ApiError(`Request timed out after ${timeoutMs}ms`, {
        code: 'request_timeout',
        type: 'timeout_error',
        detail: err,
        cause: err
      });
      timeoutError.name = 'TimeoutError';
      throw timeoutError;
    }
    if (err?.name === 'AbortError') throw err;
    if (err instanceof ApiError) throw err;
    throw new ApiError(err?.message || 'Network request failed', {
      code: 'network_error',
      type: 'network_error',
      detail: err,
      cause: err
    });
  } finally {
    if (timeout) clearTimeout(timeout);
    callerSignal?.removeEventListener('abort', forwardAbort);
  }
  const text = await res.text();
  const data = text ? safeJSON(text) : null;
  if (!res.ok) {
    throw apiErrorFromResponse(res, data);
  }
  return data;
}

export function jsonBody(value, headers = {}) {
  return { headers: { ...jsonHeaders, ...(headers || {}) }, body: JSON.stringify(value) };
}

export function putJSON(path, value, options = {}) {
  const { headers, ...requestOptions } = options;
  return request(path, { method: 'PUT', ...requestOptions, ...jsonBody(value, headers) });
}

export function postJSON(path, value, options = {}) {
  const { headers, ...requestOptions } = options;
  return request(path, { method: 'POST', ...requestOptions, ...jsonBody(value, headers) });
}

export function patchJSON(path, value, options = {}) {
  const { headers, ...requestOptions } = options;
  return request(path, { method: 'PATCH', ...requestOptions, ...jsonBody(value, headers) });
}

export function del(path) {
  return request(path, { method: 'DELETE' });
}

function safeJSON(text) {
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

function apiErrorFromResponse(res, data) {
  const error = data?.error;
  const detail = error && typeof error === 'object' ? error : (data?.detail ?? data ?? null);
  const message =
    (error && typeof error === 'object' ? error.message : error) ||
    data?.message ||
    `${res.status} ${res.statusText}`;
  const retryAfterMs = numberFrom(
    detail?.retryAfterMs
      ?? detail?.retry_after_ms
      ?? data?.retryAfterMs
      ?? retryAfterMilliseconds(res.headers?.get?.('Retry-After'))
  );
  const requestId =
    detail?.requestId ||
    detail?.request_id ||
    data?.requestId ||
    data?.request_id ||
    res.headers?.get?.('X-Request-ID') ||
    res.headers?.get?.('Request-Id') ||
    '';
  return new ApiError(String(message), {
    status: res.status,
    code: detail?.code || data?.code || '',
    type: detail?.type || data?.type || '',
    failureClass: detail?.failureClass || detail?.failure_class || data?.failureClass || '',
    phase: detail?.phase || data?.phase || '',
    messageKey: detail?.messageKey || detail?.message_key || data?.messageKey || '',
    retryMode: detail?.retryMode || detail?.retry_mode || data?.retryMode || '',
    retryable: detail?.retryable ?? data?.retryable,
    retryAfterMs,
    attempt: detail?.attempt ?? data?.attempt,
    maxAttempts: detail?.maxAttempts ?? detail?.max_attempts ?? data?.maxAttempts,
    sideEffectState: detail?.sideEffectState ?? detail?.side_effect_state ?? data?.sideEffectState,
    partialOutput: detail?.partialOutput ?? detail?.partial_output ?? data?.partialOutput,
    runId: detail?.runId || detail?.run_id || data?.runId || '',
    intentId: detail?.intentId || detail?.intent_id || data?.intentId || '',
    requestId,
    detail
  });
}

function numberFrom(value) {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
}

function retryAfterMilliseconds(value) {
  if (!value) return 0;
  const seconds = Number(value);
  if (Number.isFinite(seconds)) return Math.max(0, Math.round(seconds * 1000));
  const date = Date.parse(value);
  return Number.isFinite(date) ? Math.max(0, date - Date.now()) : 0;
}

// Consume an SSE body and emit parsed events. Callers own the abort controller.
export async function readSSE(body, onEvent) {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  const flush = (final = false) => {
    // A streamed chunk may end between CR and LF. Preserve that trailing CR
    // until the next chunk arrives so one logical newline cannot become two.
    const trailingCR = !final && buffer.endsWith('\r');
    const ready = trailingCR ? buffer.slice(0, -1) : buffer;
    buffer = ready.replace(/\r\n/g, '\n').replace(/\r/g, '\n') + (trailingCR ? '\r' : '');
    let idx = buffer.indexOf('\n\n');
    while (idx !== -1) {
      dispatch(buffer.slice(0, idx));
      buffer = buffer.slice(idx + 2);
      idx = buffer.indexOf('\n\n');
    }
    if (final && buffer.trim()) {
      dispatch(buffer);
      buffer = '';
    }
  };

  const dispatch = (raw) => {
    const event = parseSSEBlock(raw);
    if (!event || event.data === '') return;
    onEvent(event);
  };

  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      flush();
    }
    buffer += decoder.decode();
    flush(true);
  } finally {
    reader.releaseLock();
  }
}

function parseSSEBlock(raw) {
  const lines = raw.split('\n');
  const data = [];
  let event = 'message';
  for (const line of lines) {
    if (!line || line.startsWith(':')) continue;
    if (line.startsWith('event:')) {
      event = line.slice(6).trim();
    } else if (line.startsWith('data:')) {
      data.push(line.slice(5).trimStart());
    }
  }
  return { event, data: data.join('\n') };
}
