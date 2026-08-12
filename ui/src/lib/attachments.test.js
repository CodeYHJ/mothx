import test from 'node:test';
import assert from 'node:assert/strict';
import { safeAttachmentURL, validProviderRef } from './attachments.js';

test('attachment URLs allow external HTTPS and reject unsafe schemes', () => {
  assert.equal(safeAttachmentURL('https://cdn.example.com/file.png'), 'https://cdn.example.com/file.png');
  assert.equal(safeAttachmentURL('http://cdn.example.com/file.png'), '');
  assert.equal(safeAttachmentURL('javascript:alert(1)'), '');
  assert.equal(safeAttachmentURL('data:text/html,evil'), '');
});

test('attachment URLs reject localhost and private network targets', () => {
  for (const value of [
    'https://localhost/file',
    'https://127.0.0.1/file',
    'https://10.0.0.2/file',
    'https://172.16.0.1/file',
    'https://192.168.1.1/file',
    'https://[::1]/file',
    'https://[fd00::1]/file'
  ]) assert.equal(safeAttachmentURL(value), '', value);
});

test('provider references reject malformed control characters and excessive input', () => {
  assert.equal(validProviderRef('provider-file-1'), true);
  assert.equal(validProviderRef(''), false);
  assert.equal(validProviderRef('a\nheader'), false);
  assert.equal(validProviderRef('x'.repeat(2049)), false);
  assert.equal(validProviderRef(123), false);
});
