const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const {loadModules} = require('./helpers/typescript.cjs');
const flush = () => new Promise(resolve => setImmediate(resolve));

function fixture() {
  let document;
  class Element {
    constructor(tag = 'button', control = false) {
      this.tag = tag; this.control = control; this.listeners = new Map();
      this.attributes = new Map(); this.children = []; this.hidden = false; this.isConnected = true;
      this.acceptedClicks = 0;
    }
    addEventListener(type, fn) { this.listeners.set(type, [...(this.listeners.get(type) || []), fn]); }
    emit(type, extra = {}) {
      const event = { target: this, preventDefault() { this.defaultPrevented = true; }, stopImmediatePropagation() { this.stopped = true; }, ...extra };
      for (const handler of document.listeners.get(type) || []) { handler(event); if (event.stopped) return event; }
      for (const handler of this.listeners.get(type) || []) handler(event);
      return event;
    }
    click(extra) { if (!this.emit('click', extra).stopped) this.acceptedClicks++; }
    closest() { return this.control ? this : null; }
    matches(tag) { return this.tag === tag; }
    append(...children) { this.children.push(...children); }
    insertBefore(child) { this.children.push(child); }
    contains(target) { return target === this || this.children.some(child => child.contains(target)); }
    setAttribute(name, value) { this.attributes.set(name, value); }
    removeAttribute(name) { this.attributes.delete(name); }
    hasAttribute(name) { return this.attributes.has(name); }
    focus() { document.activeElement = this; this.emit('focusin'); }
    remove() { this.isConnected = false; }
  }
  document = new Element('document');
  document.body = new Element('body'); document.head = new Element('head');
  document.currentScript = { src: 'https://example.test/theme-assets/js/chunnel-home.js?v=0.22.5' };
  document.createElement = tag => new Element(tag);
  document.querySelector = () => null;
  const window = new Element('window');
  const timers = new Map(); let clock = 0;
  const [loader, features] = loadModules(['frontend/themes/chunnel/home-loader.ts','frontend/themes/chunnel/home-features-loader.ts'], { document, window, Element, Node: Element, URL, console,
    setTimeout: (fn, delay) => { timers.set(++clock, { fn, delay }); return clock; },
    clearTimeout: id => timers.delete(id),
  });
  const entry = new URL(document.currentScript.src); features.initHomeFeatures(entry, loader.createHomeLoader(entry));
  const status = document.body.children[0];
  return { document, window, timers, status, retry: status.children[1], dismiss: status.children[2],
    button: (tag) => new Element(tag || 'button', true),
    get scripts() { return document.head.children.filter(node => node.tag === 'script'); },
    get styles() { return document.head.children.filter(node => node.tag === 'link'); },
    finish: () => { window.ChunnelHomeFeaturesReady = true; for (const node of document.head.children) if (node.isConnected) node.onload?.(); },
  };
}

test('homepage does not fetch enhancement scripts before explicit use', () => {
  const f = fixture();
  assert.equal(f.scripts.length, 0);
  assert.equal(f.status.hidden, true);
  assert.equal(f.timers.size, 0);
});

test('rapid first clicks deduplicate the download and apply the action once', async () => {
  const f = fixture(), button = f.button();
  button.click(); button.click(); button.click();
  assert.equal(f.scripts.length, 1);
  assert.equal(f.scripts[0].src, 'https://example.test/theme-assets/js/chunnel-home-features.js?v=0.22.5');
  assert.equal(f.status.hidden, false);
  assert.equal(button.hasAttribute('aria-busy'), true);
  f.finish(); await flush();
  assert.equal(button.acceptedClicks, 1);
  assert.equal(button.hasAttribute('aria-busy'), false);
  assert.equal(f.status.hidden, true);
  assert.equal(f.timers.size, 0);
  button.click(); assert.equal(button.acceptedClicks, 2);
});

test('latest explicit action wins while a shared script is loading', async () => {
  const f = fixture(), first = f.button(), last = f.button();
  first.click(); last.click();
  assert.equal(f.scripts.length, 1);
  f.finish(); await flush();
  assert.equal(first.acceptedClicks, 0); assert.equal(last.acceptedClicks, 1);
});

test('cancelling and retrying the same control cannot replay an earlier intent', async () => {
  const f = fixture(), button = f.button();
  button.click(); f.dismiss.click(); button.click();
  f.finish(); await flush();
  assert.equal(button.acceptedClicks, 1);
});

test('Escape cancels the pending action and restores launcher focus', async () => {
  const f = fixture(), button = f.button();
  button.click(); button.emit('keydown', { key: 'Escape' });
  assert.equal(f.document.activeElement, button);
  f.finish(); await flush();
  assert.equal(button.acceptedClicks, 0);
  assert.equal(f.status.hidden, true);
});

test('leaving the page suppresses a late activation', async () => {
  const f = fixture(), button = f.button();
  button.click(); for (const fn of f.window.listeners.get('pagehide')) fn();
  f.finish(); await flush(); assert.equal(button.acceptedClicks, 0);
});

test('moving keyboard focus to another control cancels a pending activation', async () => {
  const f = fixture(), first = f.button(), next = f.button();
  first.focus(); first.click(); next.focus();
  f.finish(); await flush();
  assert.equal(first.acceptedClicks, 0);
  assert.equal(f.document.activeElement, next);
});

for (const failure of ['network', 'timeout', 'initialization']) {
  test(`${failure} failure exposes retry and releases the failed script`, async () => {
    const f = fixture(), button = f.button(); button.click();
    if (failure === 'network') f.scripts[0].onerror();
    if (failure === 'timeout') [...f.timers.values()].filter(timer => timer.delay === 15000).at(-1).fn();
    if (failure === 'initialization') f.scripts[0].onload();
    await flush();
    assert.equal(f.scripts[0].isConnected, false);
    assert.equal(f.retry.hidden, false);
    assert.equal(button.hasAttribute('aria-busy'), false);
    f.retry.click(); assert.equal(f.scripts.length, 2);
    f.finish(); await flush(); assert.equal(button.acceptedClicks, 1);
  });
}

test('modified native links keep browser navigation and do not load scripts', () => {
  const f = fixture(), link = f.button('a');
  link.click({ ctrlKey: true });
  assert.equal(link.acceptedClicks, 1); assert.equal(f.scripts.length, 0);
});

test('an enhancement waits for its stylesheet before replaying the first click', async () => {
  const f = fixture(), button = f.button(); button.click();
  assert.equal(f.styles.length, 1);
  assert.equal(f.styles[0].href, 'https://example.test/theme-assets/css/chunnel-home-overlays.css?v=0.22.5');
  f.window.ChunnelHomeFeaturesReady = true; f.scripts[0].onload(); await flush();
  assert.equal(button.acceptedClicks, 0);
  assert.equal(button.hasAttribute('aria-busy'), true);
  f.styles[0].onload(); await flush();
  assert.equal(button.acceptedClicks, 1);
  assert.equal(f.styles[0].isConnected, true);
});

test('failed overlay CSS retries without downloading initialized feature scripts again', async () => {
  const f = fixture(), button = f.button(); button.click();
  f.window.ChunnelHomeFeaturesReady = true; f.scripts[0].onload();
  f.styles[0].onerror(); await flush();
  assert.equal(button.acceptedClicks, 0);
  assert.equal(f.retry.hidden, false);
  f.retry.click();
  assert.equal(f.styles.length, 2);
  assert.equal(f.scripts.length, 1);
  f.styles[1].onload(); await flush();
  assert.equal(button.acceptedClicks, 1);
});
