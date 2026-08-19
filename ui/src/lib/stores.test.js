import test from 'node:test';
import assert from 'node:assert/strict';
import { get } from 'svelte/store';

// Fake browser globals before importing stores.js (module side effects read them).
class FakeWebSocket {
  static OPEN = 1;
  static CLOSED = 3;
  static instances = [];
  constructor(url) {
    this.url = url;
    this.readyState = FakeWebSocket.OPEN;
    this.sent = [];
    FakeWebSocket.instances.push(this);
  }
  send(data) {
    this.sent.push(JSON.parse(data));
  }
  close() {
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.();
  }
  subscribeMessages() {
    return this.sent.filter((msg) => msg?.type === 'subscribe');
  }
}

globalThis.window = {
  location: { protocol: 'http:', host: 'unit.test' },
  setTimeout: (fn) => {
    fn();
    return 0;
  },
  clearTimeout: () => {}
};
globalThis.WebSocket = FakeWebSocket;

const {
  connectRuns,
  runCursors,
  runEvents,
  sessions,
  syncRunSubscriptions
} = await import('./stores.js');

function subscribedIDs(socket) {
  return socket.subscribeMessages().flatMap((msg) => (msg.subscriptions || []).map((sub) => sub.sessionId));
}

test('runs socket subscribes to sessions that load after the socket opens', () => {
  FakeWebSocket.instances = [];
  sessions.set([]);

  connectRuns();
  const socket = FakeWebSocket.instances[0];
  assert.ok(socket, 'socket should be created');
  socket.onopen();
  assert.deepEqual(subscribedIDs(socket), [], 'no sessions loaded yet, nothing to subscribe');

  // Sessions arriving after the socket opens must still be subscribed —
  // otherwise run events for existing sessions never reach the UI.
  sessions.set([{ id: 's1' }, { id: 's2' }]);
  assert.deepEqual(subscribedIDs(socket), ['s1', 's2']);
});

test('runs socket does not re-subscribe already subscribed sessions', () => {
  const socket = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
  socket.sent = socket.sent.filter((msg) => msg?.type !== 'subscribe');

  sessions.set([{ id: 's1' }, { id: 's2' }, { id: 's3' }]);
  assert.deepEqual(subscribedIDs(socket), ['s3'], 'only the new session should be subscribed');
});

test('runs socket re-subscribes all sessions after reconnect', () => {
  const first = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
  first.onclose();

  // window.setTimeout fires the reconnect immediately in this test harness.
  const second = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
  assert.notEqual(second, first, 'reconnect should create a new socket');
  second.onopen();
  assert.deepEqual(subscribedIDs(second).sort(), ['s1', 's2', 's3']);
});

test('runs socket advances only the persisted cursor for each event stream', () => {
  const socket = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
  runCursors.set({});
  runEvents.set([]);

  socket.onmessage({ data: JSON.stringify({
    type: 'session_event', sessionId: 's1', stream: 'transcript', seq: 900, data: { seq: 4 }
  }) });
  socket.onmessage({ data: JSON.stringify({
    type: 'session_event', sessionId: 's1', stream: 'capability', seq: 901, data: { seq: 2 }
  }) });
  socket.onmessage({ data: JSON.stringify({
    type: 'run_state', sessionId: 's1', stream: 'run', seq: 902, data: { seq: 7 }
  }) });
  socket.onmessage({ data: JSON.stringify({
    type: 'session_event', sessionId: 's1', stream: 'transcript', seq: 903, data: { seq: 3 }
  }) });
  socket.onmessage({ data: JSON.stringify({
    type: 'session_event', sessionId: 's2', stream: 'transcript', seq: 904, data: {}
  }) });

  assert.deepEqual(get(runCursors), {
    s1: { entrySeq: 4, runSeq: 7, capabilitySeq: 2 }
  });
  assert.equal(get(runEvents).length, 5, 'live broker events should still reach consumers');
  assert.deepEqual(get(runEvents).map((event) => event.wsSeq), [1, 2, 3, 4, 5]);
});

test('a rejected run subscription can be retried without reconnecting', () => {
  const socket = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
  socket.sent = [];

  socket.onmessage({ data: JSON.stringify({
    type: 'error', sessionId: 's2', data: { message: 'temporary rejection' }
  }) });
  syncRunSubscriptions();

  assert.deepEqual(subscribedIDs(socket), ['s2']);
});
