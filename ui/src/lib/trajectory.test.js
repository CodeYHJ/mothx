import test from 'node:test';
import assert from 'node:assert/strict';
import {
  mergeTrajectoryRecords,
  normalizeTrajectoryRecords,
  stableRecordID
} from './trajectory/records.js';
import { emptyTrajectoryState, reduceTrajectoryState } from './trajectory/reducer.js';
import { visibleWindow } from './trajectory/layout.js';
import { createTrajectorySearch } from './trajectory/search.js';

test('trajectory records keep stable identities and merge live completion', () => {
  const id = stableRecordID('run', { id: 'run-1', sessionId: 's-1' });
  assert.equal(id, 'run:s-1:run-1');
  const merged = mergeTrajectoryRecords([
    { id: 'tool:s-1:t-1', source: 'tool', status: 'running', summary: 'read' },
    { id: 'tool:s-1:t-1', source: 'tool', status: 'completed', output: 'ok', summary: 'read' }
  ]);
  assert.equal(merged.length, 1);
  assert.equal(merged[0].status, 'completed');
  assert.equal(merged[0].output, 'ok');
});

test('tool transcript and live status share the tool call identity', () => {
  const records = normalizeTrajectoryRecords({
    sessionId: 's-1',
    messages: [{ id: 'entry:tool:1', role: 'toolCall', toolCallId: 'call-1', toolName: 'read' }],
    toolEvents: [{ id: 'event-1', toolCallId: 'call-1', tool: 'read', status: 'completed', summary: 'ok' }]
  });
  assert.equal(records[0].id, 'tool:s-1:call-1');
  assert.equal(records.some((record) => record.id === 'tool:s-1:call-1'), true);
});

test('trajectory reducer supports server records and high-water cursors', () => {
  const state = reduceTrajectoryState(emptyTrajectoryState(), {
    sessionId: 's-1',
    records: [{ id: 'run:s-1:r-1', source: 'run', kind: 'run', status: 'completed', seq: 4, summary: 'done' }],
    highWater: { entrySeq: 3, runSeq: 4, capabilitySeq: 2, decisionSeq: 1 }
  });
  assert.deepEqual(state.orderedIDs, ['run:s-1:r-1']);
  assert.equal(state.highWater.runSeq, 4);
});

test('server content blocks project persisted thinking into a reasoning record', () => {
  const records = normalizeTrajectoryRecords({
    sessionId: 's-1',
    records: [{ id: 'transcript:s-1:a-1', source: 'transcript', kind: 'assistant', status: 'completed', contents: [{ type: 'thinking', thinking: 'check the package manifest' }] }]
  });
  assert.equal(records.some((record) => record.kind === 'reasoning' && record.output === 'check the package manifest'), true);
});

test('live approval and question events share the decision projection', () => {
  const records = normalizeTrajectoryRecords({
    sessionId: 's-1',
    runEvents: [{ id: 'approval-1', eventType: 'approval_requested', status: 'pending', seq: 7 }]
  });
  assert.equal(records[0].source, 'decision');
  assert.equal(records[0].kind, 'decision');
  assert.equal(records[0].status, 'pending');
});

test('trajectory search and virtualization remain deterministic', () => {
  const records = normalizeTrajectoryRecords({
    sessionId: 's-1',
    messages: [{ id: 'm-1', role: 'user', content: 'inspect package' }]
  });
  assert.equal(createTrajectorySearch(records).query('package').length, 1);
  const window = visibleWindow(Array.from({ length: 100 }, (_, index) => ({ id: String(index) })), 580, 200);
  assert.equal(window.items[0].id, '2');
  assert.equal(window.total, 100);
});
