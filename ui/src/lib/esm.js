import { request, jsonBody } from './api.js';

export async function getESM(sessionID) {
  if (!sessionID) return { status: 'none' };
  return request(`/api/sessions/${encodeURIComponent(sessionID)}/esm`);
}

export async function createESM(sessionID, payload) {
  return request(`/api/sessions/${encodeURIComponent(sessionID)}/esm`, { method: 'POST', ...jsonBody(payload) });
}

export async function updateESM(sessionID, payload) {
  return request(`/api/sessions/${encodeURIComponent(sessionID)}/esm`, { method: 'PATCH', ...jsonBody(payload) });
}

export async function pauseESM(sessionID) {
  return request(`/api/sessions/${encodeURIComponent(sessionID)}/esm/pause`, { method: 'POST' });
}

export async function resumeESM(sessionID) {
  return request(`/api/sessions/${encodeURIComponent(sessionID)}/esm/resume`, { method: 'POST' });
}

export async function clearESM(sessionID) {
  return request(`/api/sessions/${encodeURIComponent(sessionID)}/esm`, { method: 'DELETE' });
}

export async function addESMGuidance(sessionID, guidance, version = '') {
  return request(`/api/sessions/${encodeURIComponent(sessionID)}/esm/guidance`, { method: 'POST', ...jsonBody({ guidance, version }) });
}

export async function setESMBudget(sessionID, tokenBudget, version = '') {
  return request(`/api/sessions/${encodeURIComponent(sessionID)}/esm/budget`, { method: 'PATCH', ...jsonBody({ tokenBudget, version }) });
}
