import test from 'node:test';
import assert from 'node:assert/strict';
import { markdownToHTML, highlightedCodeToHTML } from './markdown.js';

test('markdown escapes raw HTML and does not create executable markup', () => {
  const html = markdownToHTML('<img src=x onerror=alert(1)> <script>alert(1)</script>');
  assert.equal(html.includes('<img'), false);
  assert.equal(html.includes('<script>'), false);
  assert.match(html, /&lt;img/);
});

test('markdown rejects unsafe link protocols and preserves malformed links as text', () => {
  const html = markdownToHTML('[x](javascript:alert(1)) [y](data:text/html,evil) [z](not a url)');
  assert.equal(html.includes('href="javascript:'), false);
  assert.equal(html.includes('href="data:'), false);
  assert.equal(html.includes('<a '), false);
});

test('markdown handles null, numbers, and unclosed fences safely', () => {
  assert.equal(markdownToHTML(null), '');
  assert.match(markdownToHTML(42), /42/);
  const html = markdownToHTML('```js\nconst x = "<safe>"');
  assert.match(html, /code-block/);
  assert.match(html, /&lt;safe&gt;/);
  assert.equal(html.includes('<safe>'), false);
});

test('code highlighting escapes source before adding token spans', () => {
  const html = highlightedCodeToHTML('const x = "</code><script>"', 'file.js');
  assert.equal(html.includes('<script>'), false);
  assert.equal(html.includes('</code>'), false);
  assert.match(html, /tok-keyword/);
});
