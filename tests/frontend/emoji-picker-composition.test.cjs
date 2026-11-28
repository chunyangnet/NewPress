const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = require('./helpers/typescript.cjs').compile('frontend/shared/emoji.ts');

function fixture() {
  const listeners = new Map();
  const node = name => ({ name, addEventListener(type, fn) { listeners.set(`${name}:${type}`, fn); } });
  const document = node('document'), search = node('search'), panel = node('panel');
  panel.hidden = false;
  const calls = { filters: 0, positions: 0, focus: 0, closes: 0 };
  const choice = { focus() { calls.focus++; }, parentElement: { querySelectorAll: () => [choice] } };
  const grid = { querySelector: () => choice };
  class Element { static [Symbol.hasInstance](value) { return !!value?.closest; } }
  const context = vm.createContext({ document, search, panel, grid, WeakSet, Element,
    populate() { calls.filters++; }, position() { calls.positions++; }, close() { panel.hidden = true; calls.closes++; },
    getComputedStyle: () => ({ gridTemplateColumns: '24px' }),
  });
  const setup = source.indexOf('const composing = /* @__PURE__ */ new WeakSet();');
  if (setup >= 0) vm.runInContext(source.slice(setup, source.indexOf('  if (!(document.currentScript', setup)), context);
  const keyboard = source.indexOf('  panel.addEventListener("keydown"');
  vm.runInContext(source.slice(keyboard, source.indexOf('  closeButton.addEventListener', keyboard)), context);
  const escape = source.indexOf('  document.addEventListener("keydown"');
  vm.runInContext(source.slice(escape, source.indexOf('  document.addEventListener("chunnel:overlay"', escape)), context);
  const emit = (where, type, extra = {}) => {
    const event = { target: where, key: '', keyCode: 0, isComposing: false, prevented: false, stopped: false,
      preventDefault() { this.prevented = true; }, stopImmediatePropagation() { this.stopped = true; }, ...extra };
    if (where !== document) listeners.get(`document:${type}`)?.(event);
    if (!event.stopped) listeners.get(`${where.name}:${type}`)?.(event);
    return event;
  };
  return { search, panel, choice, calls, emit, other: node('editor') };
}

for (const mode of ['isComposing', 'keyCode', 'tracked']) {
  test(`emoji search keeps candidate keys inside the IME (${mode})`, () => {
    const f = fixture();
    if (mode === 'tracked') f.emit(f.search, 'compositionstart');
    const flags = mode === 'isComposing' ? { isComposing: true } : mode === 'keyCode' ? { keyCode: 229 } : {};
    const down = f.emit(f.search, 'keydown', { key: 'ArrowDown', ...flags });
    assert.equal(down.prevented, false); assert.equal(f.calls.focus, 0);
    const escape = f.emit(f.search, 'keydown', { key: 'Escape', ...flags });
    assert.equal(escape.prevented, false); assert.equal(escape.stopped, false); assert.equal(f.panel.hidden, false);
  });
}

test('emoji filtering waits for a committed composition and then reflects later input', () => {
  const f = fixture(); f.emit(f.search, 'compositionstart');
  f.emit(f.search, 'input'); f.emit(f.search, 'input', { isComposing: true });
  assert.equal(f.calls.filters, 0); assert.equal(f.calls.positions, 0);
  f.emit(f.search, 'compositionend'); assert.equal(f.calls.filters, 1); assert.equal(f.calls.positions, 1);
  f.emit(f.search, 'input'); assert.equal(f.calls.filters, 2);
});

test('an untracked composing input does not rebuild or reposition emoji results', () => {
  const f = fixture(); f.emit(f.search, 'input', { isComposing: true });
  assert.equal(f.calls.filters, 0); assert.equal(f.calls.positions, 0);
});

test('ordinary picker navigation and closing resume after composition commits', () => {
  const f = fixture(); f.emit(f.search, 'compositionstart'); f.emit(f.search, 'compositionend');
  const down = f.emit(f.search, 'keydown', { key: 'ArrowDown' }); assert.equal(down.prevented, true); assert.equal(f.calls.focus, 1);
  const escape = f.emit(f.search, 'keydown', { key: 'Escape' }); assert.equal(escape.prevented, true); assert.equal(escape.stopped, true); assert.equal(f.calls.closes, 1);
});

test('composition in the comment editor is independent of picker focus', () => {
  const f = fixture(); f.emit(f.other, 'compositionstart');
  assert.equal(f.emit(f.other, 'keydown', { key: 'Escape' }).prevented, false);
  assert.equal(f.panel.hidden, false);
  assert.equal(f.emit(f.search, 'keydown', { key: 'ArrowDown' }).prevented, true);
  f.emit(f.other, 'compositionend');
  assert.equal(f.emit(f.other, 'keydown', { key: 'Escape' }).prevented, true);
});

test('losing focus clears a missing compositionend without leaving the picker stuck', () => {
  const f = fixture(); f.emit(f.search, 'compositionstart'); f.emit(f.search, 'focusout');
  const escape = f.emit(f.search, 'keydown', { key: 'Escape' }); assert.equal(escape.prevented, true); assert.equal(f.calls.closes, 1);
});

test('composing arrow events do not move focus within emoji choices', () => {
  const f = fixture();
  const event = f.emit(f.panel, 'keydown', { target: { closest: () => f.choice }, key: 'ArrowRight', isComposing: true });
  assert.equal(event.prevented, false); assert.equal(f.calls.focus, 0);
});

test('closed emoji picker leaves Escape available to the surrounding interface', () => {
  const f = fixture(); f.panel.hidden = true;
  const event = f.emit(f.other, 'keydown', { key: 'Escape' });
  assert.equal(event.prevented, false); assert.equal(event.stopped, false); assert.equal(f.calls.closes, 0);
});
