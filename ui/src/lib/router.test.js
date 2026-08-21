import test from 'node:test';
import assert from 'node:assert/strict';
import { get } from 'svelte/store';

const listeners = new Map();
globalThis.window = {
  location: { hash: '#/settings/serve?tab=auth&label=hello%20world' },
  addEventListener(type, listener) {
    listeners.set(type, listener);
  }
};

const { navigate, route } = await import('./router.js');

test('router parses hash paths and decoded query parameters', () => {
  assert.deepEqual(get(route), {
    path: '/settings/serve',
    segments: ['settings', 'serve'],
    section: 'settings',
    sub: 'serve',
    query: { tab: 'auth', label: 'hello world' }
  });
});

test('navigate normalizes relative paths and reacts to hash changes', () => {
  navigate('sessions');
  assert.equal(window.location.hash, '#/sessions');

  listeners.get('hashchange')();
  assert.deepEqual(get(route), {
    path: '/sessions',
    segments: ['sessions'],
    section: 'sessions',
    sub: '',
    query: {}
  });

  navigate('');
  assert.equal(window.location.hash, '#/chat');
});
