import { request } from './api.js';

const pendingPreflights = new Map();

export function sessionLogURL(sessionID, includeDescendants = true) {
  const params = new URLSearchParams({ format: 'log', include_descendants: String(includeDescendants) });
  return `/api/sessions/${encodeURIComponent(sessionID)}/export?${params.toString()}`;
}

export async function prepareSessionLog(sessionID, includeDescendants = true, signal) {
  if (!sessionID) throw new Error('Session is required');
  const url = sessionLogURL(sessionID, includeDescendants);
  const key = sessionID + ':' + includeDescendants;
  if (pendingPreflights.has(key)) return pendingPreflights.get(key);
  const promise = request(url, { method: 'HEAD', signal })
    .then(() => ({ url }))
    .finally(() => pendingPreflights.delete(key));
  pendingPreflights.set(key, promise);
  return promise;
}

export async function downloadSessionLog(sessionID, includeDescendants = true, signal) {
  const prepared = await prepareSessionLog(sessionID, includeDescendants, signal);
  if (typeof document === 'undefined') return prepared;
  const anchor = document.createElement('a');
  anchor.href = prepared.url;
  anchor.download = '';
  anchor.rel = 'noreferrer';
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  return prepared;
}
