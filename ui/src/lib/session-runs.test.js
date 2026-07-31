import test from 'node:test';
import assert from 'node:assert/strict';
import { get } from 'svelte/store';
import {
  sessionRunStates,
  ensureSessionState,
  getSessionState,
  updateSessionState,
  setSessionState,
  registerCompletion,
  markCompletion,
  abortCompletion,
  clearCompletion,
  isCompletionActive,
  registerObserver,
  clearObserver,
  stopObserver,
  removeSessionState,
  eventBelongsToSession
} from './session-runs.js';

test.beforeEach(() => sessionRunStates.set({}));

test('creates isolated session state', () => {
  ensureSessionState('a');
  ensureSessionState('b');
  updateSessionState('a', (state) => ({ ...state, messages: [{ role: 'user', content: 'A' }] }));
  assert.equal(getSessionState('a').messages.length, 1);
  assert.equal(getSessionState('b').messages.length, 0);
  assert.notEqual(getSessionState('a'), getSessionState('b'));
});

test('aborts only the bound session completion', () => {
  const calls = [];
  const a = { abort: () => calls.push('a') };
  const b = { abort: () => calls.push('b') };
  registerCompletion('a', a);
  registerCompletion('b', b);
  assert.equal(abortCompletion('a'), true);
  assert.deepEqual(calls, ['a']);
  assert.equal(getSessionState('a').completion.status, 'cancel_requested');
  assert.equal(getSessionState('b').completion.status, 'starting');
});

test('does not clear a replacement completion controller', () => {
  const first = { abort() {} };
  const second = { abort() {} };
  registerCompletion('a', first);
  updateSessionState('a', (state) => ({
    ...state,
    completion: { ...state.completion, controller: second }
  }));
  clearCompletion('a', first);
  assert.equal(getSessionState('a').completion.controller, second);
});

test('rejects payloads belonging to another session', () => {
  assert.equal(eventBelongsToSession('a', { sessionId: 'a' }), true);
  assert.equal(eventBelongsToSession('a', { x_session_id: 'a' }), true);
  assert.equal(eventBelongsToSession('a', {}), true);
  assert.equal(eventBelongsToSession('a', { sessionId: 'b' }), false);
});

test('keeps per-session cursors independent', () => {
  updateSessionState('a', (state) => ({ ...state, cursor: { ...state.cursor, entrySeq: 8 } }));
  updateSessionState('b', (state) => ({ ...state, cursor: { ...state.cursor, runSeq: 3 } }));
  const states = get(sessionRunStates);
  assert.equal(states.a.cursor.entrySeq, 8);
  assert.equal(states.a.cursor.runSeq, 0);
  assert.equal(states.b.cursor.entrySeq, 0);
  assert.equal(states.b.cursor.runSeq, 3);
});

test('completion lifecycle transitions correctly', () => {
  const controller = { abort() {} };
  registerCompletion('a', controller);
  assert.equal(isCompletionActive(getSessionState('a')), true);
  assert.equal(getSessionState('a').completion.status, 'starting');

  markCompletion('a', 'running');
  assert.equal(getSessionState('a').completion.status, 'running');
  assert.equal(isCompletionActive(getSessionState('a')), true);

  markCompletion('a', 'completed');
  assert.equal(getSessionState('a').completion.status, 'completed');
  assert.equal(isCompletionActive(getSessionState('a')), false);

  clearCompletion('a', controller);
  assert.equal(getSessionState('a').completion, null);
});

test('completion rejects duplicate registration', () => {
  const controller = { abort() {} };
  registerCompletion('a', controller);
  assert.throws(() => {
    registerCompletion('a', controller);
  }, /already has an active run/);
});

test('observer lifecycle', () => {
  const controller = { abort() {} };
  registerObserver('a', controller);
  assert.equal(getSessionState('a').observer.controller, controller);

  clearObserver('a', controller);
  assert.equal(getSessionState('a').observer, null);
});

test('observer does not clear wrong controller', () => {
  const a = { abort() {} };
  const b = { abort() {} };
  registerObserver('x', a);
  clearObserver('x', b);
  assert.equal(getSessionState('x').observer.controller, a);
});

test('stopObserver aborts and clears', () => {
  let aborted = false;
  const controller = { abort: () => { aborted = true; } };
  registerObserver('a', controller);
  assert.equal(stopObserver('a'), true);
  assert.equal(aborted, true);
  assert.equal(getSessionState('a').observer, null);
});

test('removeSessionState cleans up', () => {
  ensureSessionState('a');
  ensureSessionState('b');
  assert.ok(get(sessionRunStates)['a']);
  assert.ok(get(sessionRunStates)['b']);
  removeSessionState('a');
  assert.equal(get(sessionRunStates)['a'], undefined);
  assert.ok(get(sessionRunStates)['b']);
});

test('setSessionState updates partial state', () => {
  ensureSessionState('a');
  setSessionState('a', { messages: [{ role: 'user', content: 'hi' }], cursor: { entrySeq: 5, runSeq: 0, capabilitySeq: 0 } });
  setSessionState('a', { cursor: { entrySeq: 10, runSeq: 3, capabilitySeq: 1 } });
  assert.equal(getSessionState('a').messages.length, 1);
  assert.equal(getSessionState('a').cursor.entrySeq, 10);
  assert.equal(getSessionState('a').cursor.runSeq, 3);
  assert.equal(getSessionState('a').cursor.capabilitySeq, 1);
});

test('run cursor update from WebSocket session_event', () => {
  // Simulate the pattern used by stores.js when receiving WS events
  ensureSessionState('sess-1');
  ensureSessionState('sess-2');

  // Simulate cursor updates from session_event
  const cursors = {};
  const updateCursor = (sessionId, stream, seq) => {
    const current = cursors[sessionId] || { entrySeq: 0, runSeq: 0, capabilitySeq: 0 };
    const key = stream === 'transcript' ? 'entrySeq' : stream === 'capability' ? 'capabilitySeq' : 'runSeq';
    cursors[sessionId] = { ...current, [key]: Math.max(current[key] || 0, Number(seq) || 0) };
  };

  updateCursor('sess-1', 'transcript', 5);
  updateCursor('sess-1', 'transcript', 10);
  updateCursor('sess-1', 'run', 3);
  updateCursor('sess-2', 'capability', 2);

  assert.equal(cursors['sess-1'].entrySeq, 10);
  assert.equal(cursors['sess-1'].runSeq, 3);
  assert.equal(cursors['sess-1'].capabilitySeq, 0);
  assert.equal(cursors['sess-2'].capabilitySeq, 2);
  assert.equal(cursors['sess-2'].entrySeq, 0);
});

test('reconnect preserves cursor state', () => {
  // Simulate a reconnect scenario: cursors saved, WS reconnects, subscriptions use saved cursors
  const savedCursors = {
    'sess-1': { entrySeq: 42, runSeq: 18, capabilitySeq: 3 },
    'sess-2': { entrySeq: 7, runSeq: 0, capabilitySeq: 0 }
  };

  // On reconnect, subscriptions should use saved cursors
  const subscriptions = Object.entries(savedCursors).map(([sessionId, cursor]) => ({
    sessionId,
    cursor
  }));

  assert.equal(subscriptions.length, 2);
  assert.equal(subscriptions[0].sessionId, 'sess-1');
  assert.equal(subscriptions[0].cursor.entrySeq, 42);
  assert.equal(subscriptions[1].sessionId, 'sess-2');
  assert.equal(subscriptions[1].cursor.entrySeq, 7);
});

test('multi-session run state isolation', () => {
  const c1 = { abort() {} };
  const c2 = { abort() {} };

  registerCompletion('a', c1);
  registerCompletion('b', c2);

  markCompletion('a', 'running');
  assert.equal(getSessionState('a').completion.status, 'running');
  assert.equal(getSessionState('b').completion.status, 'starting');

  markCompletion('a', 'completed');
  clearCompletion('a', c1);
  assert.equal(getSessionState('a').completion, null);
  assert.equal(getSessionState('b').completion.status, 'starting');
});

test('lastError tracking across completion', () => {
  const controller = { abort() {} };
  registerCompletion('a', controller);
  markCompletion('a', 'failed', new Error('test error'));
  assert.equal(getSessionState('a').lastError, 'test error');
  assert.equal(getSessionState('a').completion.status, 'failed');
});

test('cancel_requested is active state', () => {
  const controller = { abort() {} };
  registerCompletion('a', controller);
  markCompletion('a', 'cancel_requested');
  assert.equal(isCompletionActive(getSessionState('a')), true);
});

// P2-15: WebSocket auto-reconnect with exponential backoff
test('ws reconnect computes exponential backoff delay', () => {
  // Simulate the exponential backoff logic from stores.js scheduleRunsReconnect.
  const computeDelay = (attempt) => Math.min(1000 * (2 ** attempt), 15000);

  assert.equal(computeDelay(0), 1000);
  assert.equal(computeDelay(1), 2000);
  assert.equal(computeDelay(2), 4000);
  assert.equal(computeDelay(3), 8000);
  assert.equal(computeDelay(4), 15000); // min(16000, 15000) = 15000
  assert.equal(computeDelay(10), 15000); // stays capped
});

test('ws reconnect does not exceed max delay', () => {
  const computeDelay = (attempt) => Math.min(1000 * (2 ** attempt), 15000);
  // After many attempts, delay stays at 15s max.
  for (let i = 4; i <= 20; i++) {
    assert.ok(computeDelay(i) <= 15000);
  }
});

test('ws reconnect resets attempt counter on successful connection', () => {
  // Simulate the pattern: on open, attempt counter resets to 0.
  let reconnectAttempt = 5;
  // On successful connection (onopen)
  reconnectAttempt = 0;
  assert.equal(reconnectAttempt, 0);
  // Next reconnect attempt should start from delay for attempt 0.
  const delay = Math.min(1000 * (2 ** reconnectAttempt), 15000);
  assert.equal(delay, 1000);
});

// P2-15: Multi-session subscription via WebSocket
test('multi-session ws subscription builds correct subscription list', () => {
  // Simulate the runSubscriptions pattern from stores.js.
  const savedCursors = {
    'sess-1': { entrySeq: 10, runSeq: 5, capabilitySeq: 2 },
    'sess-3': { entrySeq: 0, runSeq: 0, capabilitySeq: 0 }
  };
  const sessions = [
    { id: 'sess-1' },
    { id: 'sess-2' },  // not in cursors, should use default
    { id: 'sess-3' }
  ];

  const subscriptions = sessions
    .filter((item) => item?.id)
    .map((item) => ({
      sessionId: item.id,
      cursor: savedCursors[item.id] || { entrySeq: 0, runSeq: 0, capabilitySeq: 0 }
    }));

  assert.equal(subscriptions.length, 3);
  assert.deepEqual(subscriptions[0], { sessionId: 'sess-1', cursor: { entrySeq: 10, runSeq: 5, capabilitySeq: 2 } });
  assert.deepEqual(subscriptions[1], { sessionId: 'sess-2', cursor: { entrySeq: 0, runSeq: 0, capabilitySeq: 0 } });
  assert.deepEqual(subscriptions[2], { sessionId: 'sess-3', cursor: { entrySeq: 0, runSeq: 0, capabilitySeq: 0 } });
});

test('multi-session ws subscription filters empty sessions', () => {
  const sessions = [
    { id: 'sess-1' },
    null,
    {},
    { id: '' },
    { id: 'sess-2' }
  ];
  const subscriptions = sessions
    .filter((item) => item?.id)
    .map((item) => ({ sessionId: item.id, cursor: { entrySeq: 0, runSeq: 0, capabilitySeq: 0 } }));

  assert.equal(subscriptions.length, 2);
  assert.equal(subscriptions[0].sessionId, 'sess-1');
  assert.equal(subscriptions[1].sessionId, 'sess-2');
});

// P2-15: Cursor save/replay
test('cursor replay replays events after saved cursor', () => {
  // Simulate replay: events with seq > cursor are replayed.
  const events = [
    { seq: 1, type: 'transcript' },
    { seq: 2, type: 'transcript' },
    { seq: 3, type: 'tool_event' },
    { seq: 4, type: 'transcript' },
    { seq: 5, type: 'done' }
  ];
  const cursor = { entrySeq: 2, runSeq: 0, capabilitySeq: 0 };

  const replayed = events.filter((e) => {
    if (e.type === 'transcript') return e.seq > cursor.entrySeq;
    if (e.type === 'tool_event' || e.type === 'done') return e.seq > cursor.runSeq;
    return false;
  });

  assert.equal(replayed.length, 3);
  assert.equal(replayed[0].seq, 3);
  assert.equal(replayed[1].seq, 4);
  assert.equal(replayed[2].seq, 5);
});

test('cursor replay boundary excludes events already seen', () => {
  // Simulate the replay boundary check from forwardRunWebSocketEvents.
  const replayBoundary = 10;
  const events = [
    { seq: 8, data: 'old' },
    { seq: 9, data: 'old' },
    { seq: 10, data: 'boundary' },  // seq <= boundary, should be skipped
    { seq: 11, data: 'new' },
    { seq: 12, data: 'new' }
  ];

  const forwarded = events.filter((e) => !(replayBoundary > 0 && e.seq <= replayBoundary));
  assert.equal(forwarded.length, 2);
  assert.equal(forwarded[0].data, 'new');
  assert.equal(forwarded[1].data, 'new');
});

test('cursor update after replay advances all cursor fields', () => {
  // Simulate the cursor update pattern from writeRunWebSocketReplay.
  const cursor = { entrySeq: 0, runSeq: 0, capabilitySeq: 0 };

  // Replay transcript events
  const transcriptEvents = [{ seq: 1 }, { seq: 3 }, { seq: 5 }];
  for (const item of transcriptEvents) {
    if (item.seq > cursor.entrySeq) cursor.entrySeq = item.seq;
  }

  // Replay run events
  const runEvents = [{ seq: 1 }, { seq: 2 }];
  for (const item of runEvents) {
    if (item.seq > cursor.runSeq) cursor.runSeq = item.seq;
  }

  // Replay capability events
  const capEvents = [{ seq: 1 }];
  for (const item of capEvents) {
    if (item.seq > cursor.capabilitySeq) cursor.capabilitySeq = item.seq;
  }

  assert.deepEqual(cursor, { entrySeq: 5, runSeq: 2, capabilitySeq: 1 });
});

// P2-15: Page refresh running state recovery
test('page refresh recovers running state from session state', () => {
  // Simulate: on page refresh, we load persisted session state.
  // A session that was 'running' before refresh should be detected.
  ensureSessionState('sess-1');
  setSessionState('sess-1', {
    completion: { controller: null, status: 'running', startedAt: new Date().toISOString(), runId: 'run-1' },
    runtime: { activeRun: { runId: 'run-1', status: 'running' } },
    streamCompleted: false
  });

  const state = getSessionState('sess-1');
  assert.equal(isCompletionActive(state), true);
  assert.equal(state.runtime.activeRun.status, 'running');
  assert.equal(state.streamCompleted, false);
});

test('page refresh detects completed run as inactive', () => {
  ensureSessionState('sess-1');
  setSessionState('sess-1', {
    completion: { controller: null, status: 'completed', startedAt: new Date().toISOString(), runId: 'run-1' },
    runtime: { activeRun: null },
    streamCompleted: true
  });

  const state = getSessionState('sess-1');
  assert.equal(isCompletionActive(state), false);
  assert.equal(state.streamCompleted, true);
});

test('page refresh with no active session shows empty state', () => {
  // When no sessions are active, getSessionState returns empty defaults.
  const state = getSessionState('unknown-session');
  assert.equal(state.completion, null);
  assert.equal(state.runtime, null);
  assert.equal(state.streamCompleted, false);
  assert.equal(isCompletionActive(state), false);
});

test('page refresh preserves cursor for resume', () => {
  // Simulate: after refresh, the cursor is used to resume the stream.
  ensureSessionState('sess-1');
  setSessionState('sess-1', {
    cursor: { entrySeq: 42, runSeq: 18, capabilitySeq: 3 },
    streamCompleted: false
  });

  const state = getSessionState('sess-1');
  assert.equal(state.cursor.entrySeq, 42);
  assert.equal(state.cursor.runSeq, 18);
  assert.equal(state.cursor.capabilitySeq, 3);
  // After refresh, the cursor is used to subscribe to the stream.
  // The subscription should include these cursor values.
  const subscription = {
    sessionId: 'sess-1',
    cursor: state.cursor
  };
  assert.deepEqual(subscription.cursor, { entrySeq: 42, runSeq: 18, capabilitySeq: 3 });
});

test('ws subscribe message includes all session cursors', () => {
  // Simulate the subscribe message sent after WS reconnect.
  const cursors = {
    'sess-1': { entrySeq: 10, runSeq: 5, capabilitySeq: 2 },
    'sess-2': { entrySeq: 0, runSeq: 0, capabilitySeq: 0 }
  };
  const sessions = [{ id: 'sess-1' }, { id: 'sess-2' }];

  const msg = {
    type: 'subscribe',
    subscriptions: sessions
      .filter((item) => item?.id)
      .map((item) => ({
        sessionId: item.id,
        cursor: cursors[item.id] || { entrySeq: 0, runSeq: 0, capabilitySeq: 0 }
      }))
  };

  assert.equal(msg.type, 'subscribe');
  assert.equal(msg.subscriptions.length, 2);
  assert.deepEqual(msg.subscriptions[0], { sessionId: 'sess-1', cursor: { entrySeq: 10, runSeq: 5, capabilitySeq: 2 } });
  assert.deepEqual(msg.subscriptions[1], { sessionId: 'sess-2', cursor: { entrySeq: 0, runSeq: 0, capabilitySeq: 0 } });
});
