// Canonical trajectory projection helpers. The projection is deliberately
// independent from Svelte so the Web UI and export fixtures share the same
// identity, ordering, and redaction rules.

const SOURCE_ORDER = { transcript: 10, tool: 20, run: 30, decision: 40, capability: 50 };

function text(value) {
  if (value === undefined || value === null) return '';
  return typeof value === 'string' ? value : String(value);
}

function object(value) {
  return value && typeof value === 'object' ? value : {};
}

function firstDefined(...values) {
  return values.find((value) => value !== undefined && value !== null && value !== '') ?? '';
}

function numeric(value, fallback = 0) {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : fallback;
}

export function sourceOrder(source) {
  return SOURCE_ORDER[source] || 99;
}

export function recordTimestamp(record) {
  const value = firstDefined(record?.timestamp, record?.startedAt, record?.completedAt);
  if (!value) return null;
  const parsed = typeof value === 'number' ? value : Date.parse(value);
  return Number.isFinite(parsed) ? parsed : null;
}

export function stableRecordID(source, item, sessionId = '') {
  const value = object(item);
  const sid = text(value.sessionId || sessionId);
  const role = text(value.role).toLowerCase();
  if (value.toolCallId && (source === 'tool' || value.kind === 'tool' || role === 'toolcall' || role === 'toolresult')) {
    return `tool:${sid}:${text(value.toolCallId)}`;
  }
  if (value.id) return `${source}:${sid}:${text(value.id)}`;
  if (value.decisionId || value.questionId || value.approvalId) {
    return `decision:${sid}:${text(value.decisionId || value.questionId || value.approvalId)}`;
  }
  const seq = numeric(value.seq, 0);
  if (seq > 0) return `${source}:${sid}:${seq}`;
  return `${source}:${sid}:${text(value.eventType || value.role || value.kind || 'record')}:${text(value.runId)}:${text(value.timestamp)}`;
}

function statusFor(value, fallback = 'completed') {
  const status = text(value?.status || value?.eventType || '').toLowerCase();
  if (['running', 'started', 'pending', 'retrying', 'run_retrying'].includes(status)) return status === 'started' ? 'running' : status;
  if (['failed', 'error', 'timed_out', 'incomplete'].includes(status) || value?.isError) return 'failed';
  if (['canceled', 'cancelled'].includes(status)) return 'canceled';
  return fallback;
}

function preview(value, limit = 180) {
  const normalized = text(value).replace(/\s+/g, ' ').trim();
  if (normalized.length <= limit) return normalized;
  return `${normalized.slice(0, Math.max(0, limit - 1))}...`;
}

function contentText(item) {
  if (item?.content) return text(item.content);
  return (Array.isArray(item?.contents) ? item.contents : [])
    .filter((block) => block?.type === 'text' && block.text)
    .map((block) => text(block.text))
    .join('\n');
}

function baseRecord(source, item, sessionId, kind, summary, extra = {}) {
  const value = object(item);
  const id = stableRecordID(source, value, sessionId);
  return {
    id,
    sessionId: text(value.sessionId || sessionId),
    parentSessionId: text(value.parentSessionId || value.parentId),
    runId: text(value.runId),
    attempt: numeric(value.attempt || value.data?.attempt, 0),
    seq: numeric(value.seq, 0),
    timestamp: firstDefined(value.timestamp, value.startedAt, value.completedAt) || null,
    startedAt: value.startedAt || value.timestamp || null,
    completedAt: value.completedAt || null,
    source,
    kind,
    status: statusFor(value),
    summary: preview(summary || kind, 140),
    preview: preview(summary || value.content || value.summary || '', 220),
    input: value.arguments ?? value.args ?? null,
    output: value.result ?? value.summary ?? null,
    usage: value.usage || value.data?.usage || null,
    toolCallId: text(value.toolCallId),
    parentToolCallId: text(value.parentToolCallId || value.parentToolId),
    sourceEvent: value,
    ...extra
  };
}

function messageRecords(message, sessionId) {
  const value = object(message);
  const role = text(value.role).toLowerCase();
  const records = [];
  const body = contentText(value);
  if (role === 'toolcall' || role === 'toolCall'.toLowerCase()) {
    records.push(baseRecord('transcript', value, sessionId, 'tool', value.toolName || 'Tool', {
      status: 'running',
      summary: value.toolName || 'Tool',
      preview: value.toolName || 'Tool',
      input: value.arguments ?? null,
      toolCallId: text(value.toolCallId)
    }));
    return records;
  }
  if (role === 'toolresult' || role === 'toolResult'.toLowerCase()) {
    records.push(baseRecord('transcript', value, sessionId, 'tool', value.toolName || 'Tool result', {
      status: value.isError ? 'failed' : 'completed',
      summary: value.toolName || 'Tool result',
      preview: value.summary || 'Tool result',
      output: value.summary || null,
      toolCallId: text(value.toolCallId)
    }));
    return records;
  }
  if (role === 'plan') {
    records.push(baseRecord('transcript', value, sessionId, 'capability', value.plan?.title || 'Plan', {
      status: 'completed',
      preview: value.plan?.note || value.plan?.title || 'Plan',
      input: value.plan || null
    }));
    return records;
  }
  const kind = role === 'user' ? 'user' : value.isError ? 'error' : 'assistant';
  records.push(baseRecord('transcript', value, sessionId, kind, role === 'user' ? 'User' : 'Assistant', {
    status: value.isError ? 'failed' : 'completed',
    preview: body,
    output: body,
    contents: Array.isArray(value.contents) ? value.contents : []
  }));
  for (const [index, block] of (Array.isArray(value.contents) ? value.contents : []).entries()) {
    if (block?.type !== 'thinking' || !block.thinking) continue;
    records.push(baseRecord('transcript', { ...value, id: `${value.id || value.seq || 'message'}:thinking:${index}` }, sessionId, 'reasoning', 'Reasoning', {
      status: 'completed',
      preview: block.thinking,
      output: block.thinking,
      contents: [block]
    }));
  }
  return records;
}

function eventRecord(source, event, sessionId, kind, summary) {
  const value = object(event);
  const eventKind = text(value.eventType || value.event || kind).toLowerCase();
  return baseRecord(source, value, sessionId, kind, summary || eventKind, {
    status: statusFor(value, eventKind.includes('request') ? 'pending' : 'completed'),
    preview: value.summary || value.error || value.data?.summary || eventKind,
    output: value.data || value.summary || null,
    input: value.arguments || value.args || null
  });
}

export function normalizeTrajectoryRecords(input = {}) {
  const sessionId = text(input.sessionId);
  const records = [];
  for (const item of input.records || []) {
    const value = object(item);
    const record = {
      id: text(value.id) || stableRecordID(text(value.source || 'trajectory'), value, sessionId),
      sessionId: text(value.sessionId || sessionId),
      parentSessionId: text(value.parentSessionId || value.parentId),
      runId: text(value.runId),
      attempt: numeric(value.attempt, 0),
      seq: numeric(value.seq, 0),
      timestamp: firstDefined(value.timestamp, value.startedAt, value.completedAt) || null,
      startedAt: value.startedAt || value.timestamp || null,
      completedAt: value.completedAt || null,
      source: text(value.source || 'trajectory'),
      kind: text(value.kind || 'event'),
      status: statusFor(value),
      summary: preview(value.summary || value.kind || 'Event', 140),
      preview: preview(value.preview || value.summary || value.content || '', 220),
      input: value.input ?? value.arguments ?? value.args ?? null,
      output: value.output ?? value.result ?? null,
      usage: value.usage || null,
      toolCallId: text(value.toolCallId),
      parentToolCallId: text(value.parentToolCallId || value.parentToolId),
      sourceEvent: value.sourceEvent || value,
      contents: Array.isArray(value.contents) ? value.contents : []
    };
    records.push(record);
    for (const [index, block] of record.contents.entries()) {
      if (block?.type !== 'thinking' || !block.thinking) continue;
      records.push({
        ...record,
        id: `${record.id}:thinking:${index}`,
        kind: 'reasoning',
        summary: 'Reasoning',
        preview: preview(block.thinking, 220),
        output: block.thinking,
        contents: [block],
        sourceEvent: { ...object(record.sourceEvent), contents: [block] }
      });
    }
  }
  for (const message of input.messages || []) records.push(...messageRecords(message, sessionId));
  for (const event of input.toolEvents || []) records.push(eventRecord('tool', event, sessionId, 'tool', event.tool || 'Tool'));
  for (const event of input.runEvents || []) {
    const eventType = text(event?.eventType || event?.event || event?.kind).toLowerCase();
    const decision = eventType.includes('approval') || eventType.includes('question') || event?.kind === 'decision';
    records.push(eventRecord(decision ? 'decision' : 'run', event, sessionId, decision ? 'decision' : 'run', event.eventType || event.status || (decision ? 'Decision' : 'Run')));
  }
  for (const event of input.capabilityEvents || []) records.push(eventRecord('capability', event, sessionId, 'capability', event.capability || event.eventType || 'Capability'));
  for (const event of input.decisionEvents || []) records.push(eventRecord('decision', event, sessionId, event.kind || 'decision', event.summary || event.kind || 'Decision'));
  return records;
}

export function mergeTrajectoryRecords(records = []) {
  const byID = new Map();
  for (const record of records) {
    if (!record?.id) continue;
    const previous = byID.get(record.id);
    if (!previous) {
      byID.set(record.id, record);
      continue;
    }
    const next = { ...previous, ...record };
    if (previous.input && !record.input) next.input = previous.input;
    if (previous.output && !record.output) next.output = previous.output;
    if (previous.sourceEvent && record.sourceEvent) next.sourceEvent = { ...previous.sourceEvent, ...record.sourceEvent };
    if (previous.status === 'running' && record.status === 'completed') next.status = 'completed';
    byID.set(record.id, next);
  }
  return [...byID.values()].sort(compareTrajectoryRecords);
}

export function compareTrajectoryRecords(left, right) {
  const leftTime = recordTimestamp(left);
  const rightTime = recordTimestamp(right);
  if (leftTime !== null && rightTime === null) return -1;
  if (leftTime === null && rightTime !== null) return 1;
  if (leftTime !== null && rightTime !== null && leftTime !== rightTime) return leftTime - rightTime;
  const sourceDelta = sourceOrder(left?.source) - sourceOrder(right?.source);
  if (sourceDelta !== 0) return sourceDelta;
  const seqDelta = numeric(left?.seq) - numeric(right?.seq);
  if (seqDelta !== 0) return seqDelta;
  return text(left?.id).localeCompare(text(right?.id));
}

export function redactTrajectoryRecord(record) {
  const value = object(record);
  const sourceEvent = object(value.sourceEvent);
  return {
    id: text(value.id),
    sessionId: text(value.sessionId),
    parentSessionId: text(value.parentSessionId),
    source: text(value.source),
    seq: numeric(value.seq),
    timestamp: value.timestamp || null,
    runId: text(value.runId),
    attempt: numeric(value.attempt),
    kind: text(value.kind),
    status: text(value.status),
    summary: text(value.summary),
    preview: text(value.preview),
    input: value.input ?? null,
    output: value.output ?? null,
    usage: value.usage ?? null,
    toolCallId: text(value.toolCallId),
    metadata: {
      eventType: text(sourceEvent.eventType || sourceEvent.event),
      model: text(sourceEvent.model),
      mode: text(sourceEvent.mode),
      actor: text(sourceEvent.actor)
    }
  };
}
