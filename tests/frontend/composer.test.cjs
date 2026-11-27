const test = require('node:test');
const assert = require('node:assert/strict');
const {loadModule} = require('./helpers/typescript.cjs');
const variants = {TypeScript: loadModule('frontend/core/composer-core.ts', {URL, TextEncoder}), generated: require('../../internal/coreui/assets/js/chunnel-composer.js')};
for (const [variant, api] of Object.entries(variants)) {
const {renderBlock, serialize, replacement, localImageURL, History, MAX_BYTES} = api;

test(variant + ': ' + 'structured fields render hostile text as text in every supported block', () => {
  const text = '<script>alert(1)</script><img src=x onerror=evil>&';
  for (const block of [
    {kind: 'paragraph', text}, {kind: 'h2', text}, {kind: 'h3', text}, {kind: 'quote', text},
    {kind: 'code', text, language: 'go'}, {kind: 'callout', text, tone: 'warning'},
    {kind: 'list', text, ordered: true}, {kind: 'table', rows: [[text], [text]]},
    {kind: 'image', url: '/media/1/photo.png', alt: '" onerror="evil', caption: text, width: 20, height: 10},
  ]) {
    const result = renderBlock(block);
    assert.ok(!result.includes('<script>'));
    assert.ok(!result.includes('<img src=x'));
    assert.ok(!result.includes('alt="" onerror='));
    if (block.kind !== 'image') assert.ok(result.includes('&lt;script&gt;'));
  }
});

test(variant + ': ' + 'block markup rejects arbitrary code classes, callout attributes and unbounded tables', () => {
  assert.throws(() => renderBlock({kind: 'code', language: 'go" onmouseover="bad', text: ''}));
  assert.throws(() => renderBlock({kind: 'callout', tone: 'note" style="color:red', text: ''}));
  for (const rows of [[], [['one']], [['one'], ['two', 'three']], Array.from({length: 31}, () => ['cell']), [Array(11).fill('head'), Array(11).fill('body')]]) assert.throws(() => renderBlock({kind: 'table', rows}));
  assert.match(renderBlock({kind: 'table', rows: [['A', 'B'], ['1', '2']]}), /<th scope="col">A<\/th>/);
});

test(variant + ': ' + 'image blocks accept only local stable media or theme paths', () => {
  for (const value of ['https://remote.test/a.png', '//remote.test/a.png', 'javascript:evil', '/media/1/x.png?secret=x', '/media/1/x.png#x', '/media/../admin/x', '/media/1/a\\b.png', '/media/1/a"x.png']) {
    assert.equal(localImageURL(value), false, value);
    assert.throws(() => renderBlock({kind: 'image', url: value, width: 10, height: 10}));
  }
  assert.equal(localImageURL('/media/1/photo.png'), true);
  assert.throws(() => renderBlock({kind: 'image', url: '/media/1/photo.png', width: Infinity, height: 10}));
});

test(variant + ': ' + 'untouched blocks retain exact source including attributes and whitespace', () => {
  const state = {blocks: [
    {prefix: '<!--intro-->\r\n', raw: '<p class="original">A &amp; B</p>', kind: 'html', text: 'changed but not committed'},
    {prefix: '\n  ', kind: 'paragraph', raw: '<p>old</p>', dirty: true, text: 'new <text>'},
  ], tail: '\r\n'};
  assert.equal(serialize(state), '<!--intro-->\r\n<p class="original">A &amp; B</p>\n  <p>new &lt;text&gt;</p>\r\n');
});

test(variant + ': ' + 'async insertion rejects changed drafts, invalid positions and UTF-8 overflow', () => {
  const original = '<p>first</p><p>last</p>', selection = {value: original, start: 12, end: 12};
  assert.equal(replacement(original, '<p>inserted</p>', selection), '<p>first</p><p>inserted</p><p>last</p>');
  assert.throws(() => replacement(original + 'new typing', 'x', selection), /正文已变化/);
  assert.throws(() => replacement(original, 'x', {...selection, end: 999}), /插入位置/);
  const large = '中'.repeat(Math.floor(MAX_BYTES / 3));
  assert.throws(() => replacement(large, '中文', {value: large, start: large.length, end: large.length}), /2 MiB/);
});

test(variant + ': ' + 'undo retains typed groups, supports redo, and discards redo after a new edit', () => {
  const history = new History();
  history.record('A', 'block-1:text', 1000);
  history.record('AB', 'block-1:text', 1200);
  history.record('ABC', 'block-1:text', 2500);
  assert.equal(history.undo('ABCD'), 'ABC');
  assert.equal(history.undo('ABC'), 'A');
  assert.equal(history.redo('A'), 'ABC');
  history.record('ABC', 'block-2:text', 3000);
  assert.equal(history.redo('new'), null);
  for (let i = 0; i < 40; i++) history.record(String(i));
  assert.ok(history.past.length <= 20);
});

test(variant + ': ' + 'undo and redo share a single memory budget for large drafts', () => {
  const history = new History(), large = '中'.repeat(512 * 1024);
  for (let i = 0; i < 14; i++) history.record(large + i);
  for (let i = 0; i < 4; i++) history.undo(large + 'current');
  assert.ok([...history.past, ...history.future].reduce((bytes, value) => bytes + value.length * 2, 0) <= 8 * 1024 * 1024);
  assert.ok(history.past.length + history.future.length <= 20);
  assert.notEqual(history.redo(large), null);
});

test(variant + ': unknown block fields reject coercion and never serialize partial state', () => {
  for (const value of [null, [], 'paragraph', {kind:'paragraph', text:{}}, {kind:'table', rows:[['A'],[{}]]}, {kind:'list', ordered:'yes'}, {kind:'image', url:'/media/p.png', width:'10', height:10}]) assert.throws(() => renderBlock(value));
  for (const value of [null, {blocks:{}}, {blocks:[{kind:'paragraph',dirty:'false',raw:'x'}]}, {blocks:[{kind:'html'}]}]) assert.throws(() => serialize(value));
});
}
