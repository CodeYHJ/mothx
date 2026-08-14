import assert from 'node:assert/strict';
import test from 'node:test';

import { localServeRequestURLPatterns } from './network.ts';

test('local serve request patterns include HTTP and WebSocket traffic', () => {
  assert.deepEqual(localServeRequestURLPatterns(50932), [
    'http://127.0.0.1:50932/',
    'http://127.0.0.1:50932/assets/*',
    'http://127.0.0.1:50932/mothx-small.ico',
    'http://127.0.0.1:50932/api/*',
    'http://127.0.0.1:50932/v1/*',
    'http://127.0.0.1:50932/health',
    'ws://127.0.0.1:50932/ws/*',
  ]);
});
