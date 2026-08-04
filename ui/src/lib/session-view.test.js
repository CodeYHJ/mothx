import test from 'node:test';
import assert from 'node:assert/strict';

import {
  emptySessionView,
  viewFromSessionState,
  sessionStateWithView,
  normalizeSessionMessage,
  upsertTranscriptMessageInView,
  appendAssistantDeltaToView,
  reduceTranscriptEvent,
  reduceToolStatusEvent,
  reduceRunEvent,
  reduceCapabilityEvent,
  reduceRuntimeSnapshot,
  reduceStreamDone,
  reduceStreamError,
  reduceApprovalRequest,
  reduceApprovalResolved
} from './session-view.js';

const tr = (key) => key;

test('assistant_delta concatenates into the last assistant message', () => {
  let view = emptySessionView();
  view = appendAssistantDeltaToView(view, 'Hello');
  view = appendAssistantDeltaToView(view, ' world');
  assert.equal(view.messages.length, 1);
  assert.equal(view.messages[0].role, 'assistant');
  assert.equal(view.messages[0].content, 'Hello world');
});

test('attachments transcript event merges provider-neutral attachments into assistant message', () => {
  let view = { ...emptySessionView(), messages: [{ role: 'assistant', content: 'answer' }] };
  ({ view } = reduceTranscriptEvent(view, {
    type: 'attachments',
    message: {
      role: 'assistant',
      attachments: [
        { kind: 'citation', name: 'Source', url: 'https://example.test/source' },
        { kind: 'file', providerRef: 'file_123' }
      ]
    }
  }, tr));
  assert.equal(view.messages.length, 1);
  assert.equal(view.messages[0].attachments.length, 2);
  assert.equal(view.messages[0].attachments[0].url, 'https://example.test/source');
  assert.equal(view.messages[0].attachments[1].providerRef, 'file_123');
});

test('delta after a tool call starts a new assistant message', () => {
  let view = emptySessionView();
  view = { ...view, messages: [{ role: 'toolCall', toolName: 'read', toolCallId: 'c1' }] };
  view = appendAssistantDeltaToView(view, 'done');
  assert.equal(view.messages.length, 2);
  assert.equal(view.messages[1].content, 'done');
});

test('transcript message upserts by id and replay dedupe is a no-op', () => {
  let view = emptySessionView();
  const frame = { type: 'message', message: { id: 'm1', role: 'assistant', content: 'full text', seq: 7 } };
  const first = reduceTranscriptEvent(view, frame, tr).view;
  assert.equal(first.messages.length, 1);
  assert.equal(first.cursor.entrySeq, 7);

  // Same frame replayed (e.g. WebSocket history replay after a refresh):
  // must return the identical view so the UI can skip render + scroll.
  const second = reduceTranscriptEvent(first, frame, tr).view;
  assert.equal(second, first);
});

test('tool status running/completed pairs toolResult after its toolCall', () => {
  let view = emptySessionView();
  view = reduceToolStatusEvent(view, { tool: 'read', toolCallId: 'c1', status: 'running', args: { path: '/tmp/a' } }, tr).view;
  assert.equal(view.messages.length, 1);
  assert.equal(view.messages[0].role, 'toolCall');

  view = reduceToolStatusEvent(view, { tool: 'read', toolCallId: 'c1', status: 'completed', summary: 'ok' }, tr).view;
  assert.equal(view.messages.length, 2);
  assert.equal(view.messages[1].role, 'toolResult');
  assert.equal(view.messages[1].toolCallId, 'c1');
  assert.equal(view.toolEvents.length, 2);
});

test('empty assistant placeholder is replaced by the first tool call', () => {
  let view = emptySessionView();
  view = { ...view, messages: [{ role: 'assistant', content: '' }] };
  view = reduceToolStatusEvent(view, { tool: 'bash', toolCallId: 'c1', status: 'running', args: { command: 'ls' } }, tr).view;
  assert.equal(view.messages.length, 1);
  assert.equal(view.messages[0].role, 'toolCall');
});

test('run events upsert and track streamCompleted', () => {
  let view = { ...emptySessionView(), streamCompleted: true };
  view = reduceRunEvent(view, { id: 'r1', eventType: 'started', status: 'running', seq: 3 });
  assert.equal(view.streamCompleted, false);
  assert.equal(view.cursor.runSeq, 3);
  view = reduceRunEvent(view, { id: 'r1', eventType: 'finished', status: 'completed', seq: 4 });
  assert.equal(view.runEvents.length, 1);
  assert.equal(view.runEvents[0].status, 'completed');
  view = reduceStreamDone(view);
  assert.equal(view.streamCompleted, true);
});

test('capability events upsert and bump capability cursor', () => {
  let view = emptySessionView();
  view = reduceCapabilityEvent(view, { id: 'cap1', seq: 9 });
  assert.equal(view.capabilityEvents.length, 1);
  assert.equal(view.cursor.capabilitySeq, 9);
});

test('runtime snapshot replaces view runtime', () => {
  const view = reduceRuntimeSnapshot(emptySessionView(), { mode: 'yolo', pendingApprovals: [] });
  assert.equal(view.runtime.mode, 'yolo');
});

test('approval request folds into runtime and resolution removes it', () => {
  let view = emptySessionView();
  const item = { approvalId: 'a1', sessionId: 's1', tool: { name: 'bash' } };
  const requested = reduceApprovalRequest(view, item, 's1');
  assert.equal(requested.applies, true);
  view = requested.view;
  assert.equal(view.runtime.pendingApprovals.length, 1);

  view = reduceApprovalResolved(view, { approvalId: 'a1' });
  assert.equal(view.runtime.pendingApprovals.length, 0);
});

test('approval request for another session does not apply', () => {
  const view = emptySessionView();
  const item = { approvalId: 'a1', sessionId: 'other' };
  const { applies, view: next } = reduceApprovalRequest(view, item, 's1');
  assert.equal(applies, false);
  assert.equal(next, view);
});

test('sub-agent deltas go to subAgentTranscripts, not main messages', () => {
  let view = emptySessionView();
  const frame = { type: 'assistant_delta', message: { agentId: 'agent-1', role: 'assistant', content: 'hi' } };
  const { view: next, effects } = reduceTranscriptEvent(view, frame, tr);
  assert.equal(next.messages.length, 0);
  assert.equal(next.subAgentTranscripts['agent-1'].length, 1);
  assert.equal(next.subAgentTranscripts['agent-1'][0].content, 'hi');
  assert.equal(next.subAgents.length, 1);
  assert.equal(effects.subAgentTranscriptAgent, 'agent-1');
});

test('stream error reuses the empty assistant placeholder', () => {
  let view = { ...emptySessionView(), messages: [{ role: 'assistant', content: '' }] };
  const { view: next, effects } = reduceStreamError(view, 'boom', tr);
  assert.equal(next.messages.length, 1);
  assert.equal(next.messages[0].isError, true);
  assert.ok(next.messages[0].content.includes('boom'));
  assert.equal(effects.forceScroll, true);
});

test('a new run clears the previous transient stream error', () => {
  let view = emptySessionView();
  view = reduceStreamError(view, 'upstream failed', tr).view;
  assert.equal(view.messages.length, 1);
  assert.equal(view.messages[0].transientError, true);

  view = reduceRunEvent(view, { id: 'run-2', eventType: 'started', status: 'running', seq: 2 });
  assert.equal(view.messages.length, 0);
});

test('a new persisted user message clears a previous transient stream error', () => {
  let view = reduceStreamError(emptySessionView(), 'upstream failed', tr).view;
  ({ view } = reduceTranscriptEvent(view, {
    type: 'message',
    message: { id: 'user-2', seq: 3, role: 'user', content: 'retry' }
  }, tr));
  assert.equal(view.messages.length, 1);
  assert.equal(view.messages[0].role, 'user');
});

test('viewFromSessionState/sessionStateWithView round trip', () => {
  const state = {
    sessionId: 's1',
    messages: [{ role: 'assistant', content: 'x' }],
    toolEvents: [{ type: 'tool' }],
    runEvents: [{ id: 'r1' }],
    capabilityEvents: [],
    runtime: { mode: 'agent' },
    cursor: { entrySeq: 1, runSeq: 2, capabilitySeq: 3 },
    streamCompleted: true,
    subAgents: [{ id: 'a' }],
    subAgentTranscripts: { a: [] },
    completion: null,
    historyLoaded: true
  };
  const view = viewFromSessionState(state);
  assert.equal(view.messages.length, 1);
  const next = sessionStateWithView(state, { ...view, streamCompleted: false });
  assert.equal(next.streamCompleted, false);
  assert.equal(next.historyLoaded, true, 'unrelated state fields are preserved');
  assert.equal(next.cursor.runSeq, 2);
});

test('normalizeSessionMessage maps plan tool calls to plan role', () => {
  const normalized = normalizeSessionMessage({
    role: 'toolCall',
    toolName: 'plan',
    arguments: { title: 't', steps: [{ title: 's1', status: 'done' }] }
  }, tr);
  assert.equal(normalized.role, 'plan');
  assert.equal(normalized.plan.steps[0].status, 'done');
});

test('replayed assistant message merges into its live streaming copy instead of the last one', () => {
  // Live: turn-2 text streamed, tool ran, turn-3 text is streaming.
  let view = {
    ...emptySessionView(),
    messages: [
      { id: 'u1', seq: 1, role: 'user', content: 'hi' },
      { id: 'a1', seq: 2, role: 'assistant', content: '第一部分。' },
      { id: 'tc1', seq: 3, role: 'toolCall', toolName: 'read', toolCallId: 'c1' },
      { id: 'tr1', seq: 4, role: 'toolResult', toolName: 'read', toolCallId: 'c1', summary: 'ok' }
    ]
  };
  view = reduceTranscriptEvent(view, { type: 'assistant_delta', message: { role: 'assistant', content: '第二部分。' } }, tr).view;
  view = reduceToolStatusEvent(view, { tool: 'bash', toolCallId: 'c2', status: 'running', args: { command: 'go test' } }, tr).view;
  view = reduceToolStatusEvent(view, { tool: 'bash', toolCallId: 'c2', status: 'completed', summary: 'ok' }, tr).view;
  view = reduceTranscriptEvent(view, { type: 'assistant_delta', message: { role: 'assistant', content: '总结。' } }, tr).view;

  // SSE/replay delivers the persisted turn-2 message mid-stream: it must
  // merge into its live copy (position 4), never into the streaming tail.
  view = upsertTranscriptMessageInView(view, { id: 'a2', seq: 5, role: 'assistant', content: '第二部分。' });
  assert.equal(view.messages.length, 8);
  assert.equal(view.messages[4].id, 'a2');
  assert.equal(view.messages[4].content, '第二部分。');
  assert.equal(view.messages[7].content, '总结。');

  // Streaming keeps appending to the tail message afterwards.
  view = reduceTranscriptEvent(view, { type: 'assistant_delta', message: { role: 'assistant', content: '完毕。' } }, tr).view;
  assert.equal(view.messages.length, 8);
  assert.equal(view.messages[7].content, '总结。完毕。');
});

test('replayed user message does not duplicate the live optimistic one', () => {
  let view = {
    ...emptySessionView(),
    messages: [{ role: 'user', content: 'hello' }, { role: 'assistant', content: '' }]
  };
  view = upsertTranscriptMessageInView(view, { id: 'u1', seq: 1, role: 'user', content: 'hello' });
  assert.equal(view.messages.length, 2);
  assert.equal(view.messages[0].id, 'u1');
});

test('replayed tool messages without a live match insert in seq order before the live tail', () => {
  let view = {
    ...emptySessionView(),
    messages: [
      { id: 'u1', seq: 1, role: 'user', content: 'hi' },
      { role: 'assistant', content: 'streaming…' }
    ]
  };
  view = upsertTranscriptMessageInView(view, { id: 'tc9', seq: 9, role: 'toolCall', toolName: 'bash', toolCallId: 'c9' });
  assert.equal(view.messages.length, 3);
  assert.equal(view.messages[1].id, 'tc9');
  assert.equal(view.messages[2].content, 'streaming…');
});
