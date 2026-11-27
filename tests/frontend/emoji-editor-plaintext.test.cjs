'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const source = fs.readFileSync('themes/chunnel/assets/js/chunnel-emoji-editor.js', 'utf8');
const start = source.indexOf('    function plain(root) {');
const end = source.indexOf('    function selection()', start);
assert.ok(start >= 0 && end > start);

// Browser-generated block structure captured from real native text/IME input.
// This small DOM model tests only text serialization; browser checks separately
// exercise selection, composition, deletion and history against the full script.
class Element {
  constructor(tag, children = [], dataset = {}) {
    this.tagName = this.nodeName = tag.toUpperCase();
    this.nodeType = 1;
    this.childNodes = children;
    this.dataset = dataset;
  }
  get firstChild() { return this.childNodes[0] || null; }
  matches(selector) {
    return selector.split(',').some(part => {
      part = part.trim();
      if (part === '[data-emoji-tail]') return Object.hasOwn(this.dataset, 'emojiTail');
      if (part === 'img[data-emoji-value]') return this.tagName === 'IMG' && Object.hasOwn(this.dataset, 'emojiValue');
      return part.toUpperCase() === this.tagName;
    });
  }
}
const text = data => ({ nodeType: 3, nodeName: '#text', data });
const el = (tag, ...children) => new Element(tag, children);
const tail = () => new Element('br', [], { emojiTail: '' });
const emoji = () => new Element('img', [], { emojiValue: '[微笑]' });
const root = (...children) => el('fragment', ...children);
const plain = vm.runInNewContext(`(${source.slice(start, end).trim()})`, { Node: { TEXT_NODE: 3 }, Element });

test('native multiline input retains consecutive and edge blank lines', () => {
  const samples = [
    [root(text('a'), el('div', el('br')), el('div', text('b'))), 'a\n\nb'],
    [root(tail(), el('div', el('br')), el('div', text('a'))), '\n\na'],
    [root(text('a'), el('div', el('br')), el('div', el('br'))), 'a\n\n'],
    [root(tail(), el('div', el('br'))), '\n'],
    [root(tail(), el('div', el('br')), el('div', el('br'))), '\n\n'],
    [root(text(''), tail(), el('div', el('br')), el('div', text('a'))), '\n\na'],
  ];
  for (const [dom, expected] of samples) assert.equal(plain(dom), expected);
});

test('block boundaries preserve inline content, emoji atoms and nested empty lines', () => {
  const samples = [
    [root(el('div', text('a')), text('b')), 'a\nb'],
    [root(text('a\n'), el('div', text('b'))), 'a\n\nb'],
    [root(el('div', text('a')), emoji(), text('b')), 'a\n[微笑]b'],
    [root(text('甲'), el('div', el('br')), el('div', emoji()), el('div', el('br')), el('div', text('乙'))), '甲\n\n[微笑]\n\n乙'],
    [root(el('div', el('div', el('br')), el('div', text('a'))), el('div', text('b'))), '\na\nb'],
  ];
  for (const [dom, expected] of samples) assert.equal(plain(dom), expected);
});

test('prefix ranges retain the blank-line offset without creating placeholder text', () => {
  assert.equal(plain(root(text('a'), el('div', el('br')), el('div'))), 'a\n\n');
  assert.equal(plain(root(tail(), el('div'))), '\n');
  assert.equal(plain(root(tail(), el('div', el('br')), el('div'))), '\n\n');
  assert.equal(plain(root(el('div'))), '');
});

test('canonical text, placeholders and non-content nodes retain their semantics', () => {
  assert.equal(plain(root(tail())), '');
  assert.equal(plain(root(el('br'))), '');
  assert.equal(plain(root(text('a\n\n'), tail())), 'a\n\n');
  assert.equal(plain(root(text('甲'), emoji(), text('\n👨‍👩‍👧‍👦e\u0301'), tail())), '甲[微笑]\n👨‍👩‍👧‍👦e\u0301');
  assert.equal(plain(root(el('script', text('ignored')), el('style', text('ignored')), text('visible'))), 'visible');
  assert.equal(plain(root(text('a'), el('span', text('b')), text('c'))), 'abc');
});

const beforeStart = source.indexOf("editor.addEventListener('beforeinput', (event) => {");
const beforeEnd = source.indexOf("    editor.addEventListener('keydown'", beforeStart);
assert.ok(beforeStart >= 0 && beforeEnd > beforeStart);
function editEvent(inputType, data, options = {}) {
  let handler;
  const replacements = [];
  const context = { editor: { addEventListener: (_type, fn) => { handler = fn; } }, composing: !!options.composing,
    replace: value => replacements.push(value), erase() {}, undo() {}, snapshot: () => ({ value: 'before' }), pendingEdit: null };
  vm.runInNewContext(source.slice(beforeStart, beforeEnd), context);
  const event = { inputType, data, isComposing: !!options.isComposing, cancelable: options.cancelable !== false,
    defaultPrevented: false, preventDefault() { this.defaultPrevented = true; } };
  handler(event);
  return { replacements, event, pendingEdit: context.pendingEdit };
}

test('one multiline native insertion uses the validated replacement and normalizes Windows newlines', () => {
  for (const [data, expected] of [['\n\n甲', '\n\n甲'], ['甲\r\n\r\n乙\r', '甲\n\n乙\n'], ['甲\n[微笑]\n\n', '甲\n[微笑]\n\n']]) {
    const result = editEvent('insertText', data);
    assert.equal(result.event.defaultPrevented, true);
    assert.deepEqual(result.replacements, [expected]);
  }
});

test('composition and noncancelable edits retain native editing; plain typing keeps the existing history path', () => {
  for (const options of [{ composing: true }, { isComposing: true }, { cancelable: false }]) {
    const result = editEvent('insertText', '甲\n\n乙', options);
    assert.equal(result.event.defaultPrevented, false);
    assert.deepEqual(result.replacements, []);
  }
  for (const [type, data] of [['insertText', '甲'], ['insertText', null], ['insertCompositionText', '甲\n乙'], ['insertReplacementText', '甲\n乙']]) {
    const result = editEvent(type, data);
    assert.equal(result.event.defaultPrevented, false);
    assert.deepEqual(result.replacements, []);
    assert.equal(result.pendingEdit.value, 'before');
  }
});
