import assert from 'node:assert/strict';
import test from 'node:test';

import { localServeRequestURLPatterns } from './network.ts';

test('local serve request patterns include HTTP and WebSocket traffic', () => {
  assert.deepEqual(localServeRequestURLPatterns(50932), [
    'http://127.0.0.1:50932/*',
    'ws://127.0.0.1:50932/*',
  ]);
});
