const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const editor = require('../../internal/coreui/assets/js/chunnel-composer.js');
const { loadModule } = require('./helpers/typescript.cjs');
const {articleSummary, localCoverURL} = loadModule('frontend/core/editor-tool-data.ts', { URL });
const item = {id: 42, title: '把文章串联起来', permalink: '/posts/reading-together', excerpt: '文章摘要', cover_url: '/media/1/photo.webp', author_name: '编辑', category: '写作'};

test('article references store stable identity and layout without embedding a stale card or body', () => {
  for (const layout of ['card', 'compact']) {
    const markup = editor.renderBlock({kind: 'post', postID: item.id, title: item.title, url: item.permalink, layout});
    assert.equal(markup, `<aside data-np-widget="post" data-np-post="42" data-np-layout="${layout}"><p><a href="/posts/reading-together">把文章串联起来</a></p></aside>`);
    assert.doesNotMatch(markup, /style=|class=|<img|文章摘要|<script/);
    const value = '<p>开头</p>\n<p>结尾</p>', selection = {value, start: 10, end: 10};
    const inserted = editor.replacement(value, markup, selection);
    assert.ok(inserted.includes(markup));
    const history = new editor.History(); history.record(value);
    assert.equal(history.undo(inserted), value);
    assert.equal(history.redo(value), inserted);
  }
});

test('article reference labels are escaped and reject unsafe identity, links and layouts', () => {
  const block = {kind: 'post', postID: 42, title: '<img src=x onerror=evil> & "标题"', url: '/posts/reading', layout: 'card'};
  assert.match(editor.renderBlock(block), /&lt;img src=x onerror=evil&gt; &amp;/);
  assert.doesNotMatch(editor.renderBlock(block), /<img|onerror="/);
  for (const url of ['https://evil.test/posts/a', '//evil.test/posts/a', '/posts/../admin', '/posts/a?secret=x', '/posts/a#x', '/posts/a/b', '/posts/a\\b', '/posts/" onclick="x', 'javascript:alert(1)', '/pages/a']) {
    assert.equal(editor.localPostURL(url), false, url);
    assert.throws(() => editor.renderBlock({...block, url}), /文章引用无效/);
  }
  for (const postID of [0, -1, '42', 1.5, Infinity, Number.MAX_SAFE_INTEGER + 1]) assert.throws(() => editor.renderBlock({...block, postID}));
  for (const layout of ['', 'large', 'card" style="color:red']) assert.throws(() => editor.renderBlock({...block, layout}));
  assert.throws(() => editor.renderBlock({...block, title: '文'.repeat(201)}));
});

test('summary validation rejects active links and limits remote or malformed covers', () => {
  assert.deepEqual(JSON.parse(JSON.stringify(articleSummary(item, editor))), item);
  for (const value of [null, {}, {...item, id: '42'}, {...item, id: 0}, {...item, title: ''}, {...item, permalink: 'https://evil.test/'}]) assert.equal(articleSummary(value, editor), null);
  for (const cover_url of ['https://tracker.test/a.webp', '//tracker.test/a.webp', '/media/../admin/a', '/media/1/a" onerror="evil']) assert.equal(articleSummary({...item, cover_url}, editor).cover_url, '');
  assert.equal(localCoverURL('/custom/covers/photo.webp?v=2'), true);
  assert.equal(articleSummary({...item, cover_url: '/custom/covers/photo.webp?v=2'}, editor).cover_url, '/custom/covers/photo.webp?v=2');
  const summary = articleSummary({...item, excerpt: '中'.repeat(200), author_name: {}, category: null}, editor);
  assert.equal([...summary.excerpt].length, 160);
  assert.equal(summary.author_name, ''); assert.equal(summary.category, '');
});

