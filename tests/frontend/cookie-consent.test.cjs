const nodeTest = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { join } = require('node:path');
const vm = require('node:vm');
const { compile } = require('./helpers/typescript.cjs');
for (const entry of ['TypeScript', 'generated']) {
const test = (name, fn) => nodeTest(entry + ': ' + name, fn);
const source = entry === 'TypeScript' ? compile('frontend/shared/cookie-consent.ts') : readFileSync(join(__dirname, '../../themes/chunnel/assets/js/cookie-consent.js'), 'utf8');
const loader = readFileSync(join(__dirname, '../../internal/httpserver/assets/site-integrations.js'), 'utf8');
const key = 'newpress_cookie_consent_v1';
const saved = (analytics, overrides = {}) => JSON.stringify({ version: 1, necessary: true, analytics, savedAt: new Date().toISOString(), ...overrides });

function fixture({ stored = null, cookies = {}, blockStorage = false, blockCookies = false } = {}) {
  class Element {}
  const nodes = new Map();
  const node = name => {
    if (!nodes.has(name)) nodes.set(name, Object.assign(new Element(), {
      hidden: true, checked: false, open: false, listeners: {},
      addEventListener(type, callback) { (this.listeners[type] ??= []).push(callback); },
      emit(type, event = {}) { for (const callback of this.listeners[type] ?? []) callback({ currentTarget: this, ...event }); },
      querySelector: node, querySelectorAll: name => [node(name)],
      getBoundingClientRect: () => ({ height: 80, top: 600 }), getClientRects: () => [{}],
      showModal() { this.open = true; }, close() { this.open = false; this.emit('close'); },
      focus() { this.focused = true; },
    }));
    return nodes.get(name);
  };
  const values = new Map(stored === null ? [] : [[key, stored]]);
  const jar = new Map(Object.entries(cookies));
  const deletions = [];
  const scripts = [];
  const classes = new Set();
  const document = Object.assign(node('document'), {
    hidden: false,
    currentScript: { dataset: { consentRequired: 'true', siteIntegrations: JSON.stringify({ enabled: true, clarity_id: 'test' }) } },
    documentElement: { classList: { add: value => classes.add(value), remove: value => classes.delete(value) }, style: { setProperty() {} } },
    dispatchEvent(event) { this.emit(event.type, event); },
    createElement: () => ({ setAttribute() {} }),
    head: { appendChild: script => scripts.push(script) },
  });
  Object.defineProperty(document, 'cookie', {
    get() { return [...jar].map(([name, value]) => `${name}=${value}`).join('; '); },
    set(value) {
      if (blockCookies) return;
      const [pair] = value.split(';');
      const [name, ...data] = pair.split('=');
      if (value.includes('Max-Age=0')) { jar.delete(name); deletions.push(value); }
      else jar.set(name, data.join('='));
    },
  });
  let reloads = 0;
  const window = Object.assign(node('window'), { innerHeight: 800, location: {
    hostname: 'www.example.test', pathname: '/articles/demo/', protocol: 'https:', reload() { reloads++; },
  } });
  const localStorage = {
    getItem(name) { if (blockStorage) throw Error('blocked'); return values.get(name) ?? null; },
    setItem(name, value) { if (blockStorage) throw Error('blocked'); values.set(name, value); },
    removeItem(name) { if (blockStorage) throw Error('blocked'); values.delete(name); },
  };
  const context = vm.createContext({ document, window, localStorage, HTMLElement: Element, CustomEvent: class { constructor(type, options) { this.type = type; Object.assign(this, options); } } });
  vm.runInContext(source, context);
  vm.runInContext(loader, context);
  return { node, window, document, jar, values, scripts, deletions, classes, reloads: () => reloads, click: name => node(`[data-cookie-${name}]`).emit('click') };
}

test('first visit shows a compact opt-in prompt and loads no analytics', () => {
  const f = fixture();
  assert.equal(f.node('[data-cookie-consent]').hidden, false);
  assert.equal(f.node('[data-cookie-analytics]').checked, false);
  assert.equal(f.scripts.length, 0);
});

test('reject, accept and customize survive new page loads without asking again', () => {
  for (const [action, expected] of [['accept', true], ['reject', false], ['save', false]]) {
    const f = fixture();
    f.click(action);
    assert.equal(JSON.parse(f.values.get(key)).analytics, expected);
    assert.equal(f.node('[data-cookie-consent]').hidden, true);
    assert.equal(f.scripts.length, Number(expected));
    const next = fixture({ stored: f.values.get(key), cookies: Object.fromEntries(f.jar) });
    assert.equal(next.node('[data-cookie-consent]').hidden, true);
    assert.equal(next.scripts.length, Number(expected));
  }
});

test('missing fields, invalid JSON, future timestamps, and expired versions never grant consent', () => {
  for (const stored of ['oops', 'null', '{"analytics":true}', saved(true, { version: 2 }), saved(true, { necessary: false }), saved(true, { savedAt: 'invalid' }), saved(true, { savedAt: '2099-01-01' }), saved(true, { savedAt: '2000-01-01' })]) {
    const f = fixture({ stored });
    assert.equal(f.scripts.length, 0, stored);
    assert.equal(f.node('[data-cookie-consent]').hidden, false, stored);
  }
});

test('dialog reflects saved choice, cancellation does not save, and closing restores focus', () => {
  const f = fixture({ stored: saved(true) });
  f.click('open');
  assert.equal(f.node('[data-cookie-dialog]').open, true);
  assert.equal(f.node('[data-cookie-analytics]').checked, true);
  assert.ok(f.classes.has('cookie-dialog-open'));
  f.node('[data-cookie-analytics]').checked = false;
  f.click('close');
  assert.equal(JSON.parse(f.values.get(key)).analytics, true);
  assert.equal(f.node('[data-cookie-open]').focused, true);
  assert.equal(f.classes.has('cookie-dialog-open'), false);
  f.click('open');
  f.node('[data-cookie-dialog]').emit('close');
  assert.equal(f.classes.has('cookie-dialog-open'), true, 'a queued close event must not unlock a reopened dialog');
});

test('revocation clears known analytics cookies across paths and domains, preserves session and reloads', () => {
  const f = fixture({ stored: saved(true), cookies: { _ga: 'id', _clck: 'id', Hm_lvt_abc: 'id', '_pk_id.1.abcd': 'id', session: 'keep', csrf: 'keep', appearance: 'dark' } });
  f.click('reject');
  assert.equal(f.reloads(), 1);
  assert.equal(f.jar.has('_ga'), false);
  assert.equal(f.jar.has('_clck'), false);
  assert.equal(f.jar.get('session'), 'keep');
  assert.equal(f.jar.get('csrf'), 'keep');
  assert.equal(f.jar.get('appearance'), 'dark');
  assert.ok(f.deletions.some(value => value.includes('Domain=example.test') && value.includes('Path=/articles/;')));
});

test('cookie fallback persists rejection when localStorage is blocked', () => {
  const f = fixture({ blockStorage: true, cookies: { [key]: encodeURIComponent(saved(true)) } });
  assert.equal(f.scripts.length, 1);
  f.click('reject');
  assert.equal(f.reloads(), 1);
  const next = fixture({ blockStorage: true, cookies: Object.fromEntries(f.jar) });
  assert.equal(next.scripts.length, 0);
  assert.equal(next.node('[data-cookie-consent]').hidden, true);
});

test('fully blocked storage keeps the choice in memory and defaults to no tracking on a fresh page', () => {
  const f = fixture({ blockStorage: true, blockCookies: true });
  f.click('accept');
  assert.equal(f.scripts.length, 1);
  f.document.emit('visibilitychange');
  assert.equal(f.node('[data-cookie-consent]').hidden, true);
  f.click('reject');
  assert.equal(f.reloads(), 1);
  assert.equal(fixture({ blockStorage: true, blockCookies: true }).scripts.length, 0);
});

test('the newer denial wins over stale consent and another tab revoking consent stops tracking', () => {
  const older = saved(true, { savedAt: new Date(Date.now() - 10000).toISOString() });
  const f = fixture({ stored: older, cookies: { [key]: encodeURIComponent(saved(false)) } });
  assert.equal(f.scripts.length, 0);
  const active = fixture({ stored: older });
  active.values.set(key, saved(false));
  active.window.emit('storage', { key });
  assert.equal(active.reloads(), 1);
  assert.equal(active.node('[data-cookie-analytics]').checked, false);
});

test('restoring a page from history rechecks consent before keeping analytics active', () => {
  const f = fixture({ stored: saved(true) });
  f.values.set(key, saved(false));
  f.window.emit('pageshow', { persisted: true });
  assert.equal(f.reloads(), 1);
});

}
