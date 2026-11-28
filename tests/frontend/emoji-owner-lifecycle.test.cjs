'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = require('./helpers/typescript.cjs').compile('frontend/shared/emoji.ts');

function fixture(popover = true) {
  const observers = [], focus = [], listeners = new Map();
  class Control {
    constructor(parent = null) { this.parentElement = parent; this.connected = true; this.hidden = false; this.disabled = false; this.readOnly = false; this.inert = false; this.visibility = 'visible'; this.attrs = {}; }
    get isConnected() { return this.connected && (!this.parentElement || this.parentElement.isConnected); }
    matches(selector) { return selector === ':disabled' && (this.disabled || !!this.parentElement?.disabled); }
    closest(selector) { for (let node = this; node; node = node.parentElement) if (selector === '[inert]' && node.inert) return node; return null; }
    getClientRects() { for (let node = this; node; node = node.parentElement) if (node.hidden) return []; return this.isConnected ? [{}] : []; }
    getBoundingClientRect() { return { left: 20, top: 100, bottom: 144 }; }
    setAttribute(key, value) { this.attrs[key] = value; }
    focus(options) { focus.push({ node: this, options }); }
    append() {}
  }
  const root = new Control(), owner = new Control(root), input = new Control(owner), trigger = new Control(owner);
  input.value = '保留草稿[微笑]'; input.selectionStart = 2; input.selectionEnd = 4;
  const panel = { hidden: true, style: {}, scrollHeight: 230, visible: false, matches() { return this.visible; } };
  if (popover) { panel.showPopover = () => { panel.visible = true; }; panel.hidePopover = () => { panel.visible = false; }; }
  const context = vm.createContext({ target: null, trigger: null, panel, window: {}, innerWidth: 390, innerHeight: 844,
    document: { body: root, addEventListener(type, callback) { listeners.set(type, callback); }, removeEventListener(type) { listeners.delete(type); } }, editors: new Map(), ranges: new Map(), search: { value: '', focus() { focus.push({ node: 'search' }); } }, status: {},
    populate() {}, remember() {}, getComputedStyle: node => { for (let item = node; item; item = item.parentElement) if (item.visibility !== 'visible') return { visibility: item.visibility }; return { visibility: 'visible' }; },
    MutationObserver: class {
      constructor(callback) { this.callback = callback; this.nodes = new Map(); this.disconnected = false; observers.push(this); }
      observe(node, options) { this.nodes.set(node, options); }
      disconnect() { this.disconnected = true; this.nodes.clear(); }
    },
  });
  const start = source.indexOf('let ownerObserver = null;');
  vm.runInContext(source.slice(start, source.indexOf('  function remember(', start)), context);
  const openStart = source.indexOf('  function open(');
  vm.runInContext(source.slice(openStart, source.indexOf('  function insert(', openStart)), context);
  return { owner, root, input, trigger, panel, observers, focus, listeners, open: (keyboard = false) => context.open(trigger, input, keyboard), close: restore => context.close(restore), position: () => context.position(),
    notify(node) { for (const observer of observers) if (observer.nodes.has(node)) observer.callback([{ target: node }]); },
    transition(type, target, propertyName) { listeners.get(type)?.({ target, propertyName }); },
    snapshot: () => [input.value, input.selectionStart, input.selectionEnd],
  };
}

for (const [name, change, target] of [
  ['hidden composer', f => { f.owner.hidden = true; }, 'owner'],
  ['removed composer', f => { f.owner.connected = false; }, 'root'],
  ['removed input', f => { f.input.connected = false; }, 'owner'],
  ['removed trigger', f => { f.trigger.connected = false; }, 'owner'],
  ['disabled input', f => { f.input.disabled = true; }, 'input'],
  ['readonly input', f => { f.input.readOnly = true; }, 'input'],
  ['disabled fieldset', f => { f.owner.disabled = true; }, 'owner'],
  ['disabled trigger', f => { f.trigger.disabled = true; }, 'trigger'],
  ['inert owner', f => { f.owner.inert = true; }, 'owner'],
  ['visibility hidden owner', f => { f.owner.visibility = 'hidden'; }, 'owner'],
]) {
  test(`emoji picker closes with its ${name} without changing the draft or focus`, () => {
    const f = fixture(); f.open(); const before = f.snapshot(); change(f); f.notify(f[target]);
    assert.equal(f.panel.hidden, true); assert.equal(f.panel.visible, false);
    assert.equal(f.trigger.attrs['aria-expanded'], 'false'); assert.deepEqual(f.snapshot(), before); assert.deepEqual(f.focus, []);
  });
}

test('viewport changes close a picker whose responsive composer became hidden', () => {
  const f = fixture(); f.open(); f.owner.hidden = true; f.position();
  assert.equal(f.panel.hidden, true); assert.equal(f.trigger.attrs['aria-expanded'], 'false');
});

for (const [name, change] of [
  ['inherited disabled state', f => { f.owner.disabled = true; }],
  ['hidden owner', f => { f.owner.hidden = true; }],
  ['disconnected input', f => { f.input.connected = false; }],
]) test(`a picker does not open for ${name}`, () => {
  const f = fixture(); change(f); f.open(true); assert.equal(f.panel.hidden, true); assert.deepEqual(f.focus, []);
});

test('unrelated owner changes retain an active picker and canonical selection', () => {
  const f = fixture(); f.open(); const before = f.snapshot(); f.notify(f.root); f.notify(f.owner); f.position();
  assert.equal(f.panel.hidden, false); assert.equal(f.trigger.attrs['aria-expanded'], 'true'); assert.deepEqual(f.snapshot(), before);
});

test('closing releases scoped watchers and reopening starts fresh', () => {
  const f = fixture(); f.open(); assert.equal(f.observers.length, 1);
  assert.equal(f.observers[0].nodes.size, 4, 'shared ancestors are watched once');
  assert.ok([...f.observers[0].nodes.values()].every(options => !options.subtree), 'no document-wide subtree observation');
  f.close(true); assert.equal(f.observers[0].disconnected, true); assert.equal(f.observers[0].nodes.size, 0);
  assert.equal(f.listeners.size, 0, 'transition listeners are released with the picker');
  assert.equal(f.focus[0].node, f.input); assert.equal(f.focus[0].options.preventScroll, true);
  f.close(); f.open(); assert.equal(f.observers.length, 2); f.owner.hidden = true; f.notify(f.owner);
  assert.equal(f.panel.hidden, true); assert.equal(f.observers[1].disconnected, true);
});

test('picker cleanup also works without native popover support', () => {
  const f = fixture(false); f.open(); f.input.readOnly = true; f.notify(f.input);
  assert.equal(f.panel.hidden, true); assert.equal(f.trigger.attrs['aria-expanded'], 'false');
});

for (const type of ['transitionend', 'transitioncancel']) test(`deferred hiding is rechecked after ${type}`, () => {
  const f = fixture(); f.open(); f.notify(f.owner);
  assert.equal(f.panel.hidden, false, 'discrete transition initially leaves owner visible');
  f.owner.visibility = 'hidden'; f.transition(type, f.owner, 'visibility');
  assert.equal(f.panel.hidden, true); assert.equal(f.listeners.size, 0);
});

test('unrelated transitions do not disturb an active picker', () => {
  const f = fixture(); f.open(); f.owner.hidden = true;
  f.transition('transitionend', {}, 'display'); f.transition('transitionend', f.owner, 'transform');
  assert.equal(f.panel.hidden, false);
  f.transition('transitionend', f.owner, 'display'); assert.equal(f.panel.hidden, true);
});
