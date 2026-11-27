const nodeTest = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');
const { compile } = require('./helpers/typescript.cjs');
for (const entry of ['TypeScript', 'generated']) {
const test = (name, fn) => nodeTest(entry + ': ' + name, fn);
const source = entry === 'TypeScript' ? compile('frontend/core/admin-navigation.ts') : readFileSync('internal/coreui/assets/js/chunnel-admin-navigation.js', 'utf8');
class Element {}
const tick = () => new Promise(resolve => setImmediate(resolve));

function fixture(path = '/admin/content/') {
  const requests = [], entries = [], events = [], timers = new Map();
  const location = new URL(path, 'https://example.test');
  const listeners = {}, documentListeners = {}, windowListeners = {};
  const regions = new Map();
  const selectors = ['.admin-content-page', '.admin-comments-page', '.admin-media-status-tabs', '.admin-media-library', '.admin-users-index', '.admin-taxonomy-index', '.admin-taxonomy-page', '[data-analytics]', '.admin-sessions-page', '.series-admin-tools', '.series-admin-list', '#audit-events', '#redirects', '.extension-main', '.admin-revision-page'];
  function region(label, selector) {
    return { label, field: { name: 'q', value: '', focus() { this.focused = true; } }, busy: false,
      querySelector: query => query === 'form[aria-busy="true"]' && regions.get(selector)?.busy ? {} : null,
      querySelectorAll(query) { return query.includes('input') ? [this.field] : []; },
      replaceWith(next) { regions.set(selector, next); }, contains: () => false };
  }
  selectors.forEach(selector => regions.set(selector, region('initial', selector)));
  const main = { style: {}, attributes: {}, error: null,
    querySelector(selector) { return selector === '[data-admin-navigation-error]' ? this.error : regions.get(selector); },
    setAttribute(key, value) { this.attributes[key] = value; }, removeAttribute(key) { delete this.attributes[key]; },
    addEventListener(name, fn) { listeners[name] = fn; }, getBoundingClientRect: () => ({top: 64 - window.scrollY}),
    prepend(node) { this.error = node; node.remove = () => { this.error = null; }; } };
  const style = {getAttribute: () => '/theme.css?v=1'};
  const sidebar = {dataset: {account: '7'}};
  const document = { title: 'initial', activeElement: null,
    querySelector(selector) { return selector === '.admin-main' ? main : selector === '[data-admin-sidebar]' ? sidebar : style; },
    addEventListener(name, fn) { (documentListeners[name] ||= []).push(fn); },
    dispatchEvent(event) { events.push(event); for (const fn of documentListeners[event.type] || []) fn(event); return !event.defaultPrevented; },
    createElement() { return { dataset: {}, setAttribute() {} }; } };
  const history = { state: { chunnelScroll: 500, other: 'retained' }, scrollRestoration: 'auto',
    replaceState(state, title, url) { this.state = state; if (url) location.href = new URL(url, location).href; },
    pushState(state, title, url) { entries.push({url: location.href, state: this.state}); this.state = state; location.href = new URL(url, location).href; } };
  const window = { scrollY: 500, innerHeight: 720, addEventListener(name, fn) { windowListeners[name] = fn; },
    ChunnelScroll: { remember() { history.replaceState({...history.state, chunnelScroll: window.scrollY}, ''); },
      keep(fn, position) { fn(); window.scrollY = position.top; } } };
  class Form { constructor(values) { this.method = 'get'; this.action = location.href; this.values = values; } contains() { return true; } }
  const context = {document, window, history, location, URL, AbortController, Element,
    HTMLFormElement: Form, FormData: class { constructor(form) { return form.values[Symbol.iterator](); } },
    CustomEvent: class { constructor(type, options) { this.type = type; Object.assign(this, options); } preventDefault() { this.defaultPrevented = true; } },
    clearTimeout(id) { timers.delete(id); }, setTimeout(fn) { const id = timers.size + 1; timers.set(id, fn); return id; },
    fetch(url, options) { return new Promise((resolve, reject) => requests.push({url, options, reject, resolve(label, overrides = {}) {
      resolve({ok: true, url, headers: {get: () => 'text/html'}, text: async () => ({label, ...overrides}), ...overrides});
    }})); },
    DOMParser: class { parseFromString(data) { return {title: data.label, querySelector(selector) {
      if (selector === '[data-admin-sidebar]') return {dataset: {account: data.owner || '7'}};
      if (selector === 'link[rel="stylesheet"]') return {getAttribute: () => data.style || '/theme.css?v=1'};
      if (data.missing) return null;
      return region(data.label, selector);
    }}; } } };
  vm.runInNewContext(source, context);
  const click = (url, overrides = {}) => {
    const link = {href: new URL(url, location).href, hasAttribute: () => false};
    const event = {button: 0, detail: 1, target: Object.assign(new Element(), {closest: () => link}), preventDefault() {this.defaultPrevented = true;}, ...overrides};
    listeners.click?.(event); return event;
  };
  return {main, regions, document, history, window, location, requests, entries, events, windowListeners, click, timers,
    submit(values) { const event = {target: new Form(values), preventDefault() {this.defaultPrevented = true;}}; listeners.submit?.(event); return event; } };
}

test('admin filters keep the document, position and history state while replacing their region', async () => {
  const f = fixture();
  const enhanced = []; f.window.ChunnelEmoji = { enhance(region) { enhanced.push(region); } };
  assert.equal(f.click('?status=draft').defaultPrevented, true);
  assert.equal(f.regions.get('.admin-content-page').label, 'initial');
  f.requests[0].resolve('drafts'); await tick();
  assert.equal(f.regions.get('.admin-content-page').label, 'drafts');
  assert.equal(f.window.scrollY, 500); assert.equal(f.history.state.other, 'retained');
  assert.equal(f.location.search, '?status=draft'); assert.equal(f.entries.length, 1);
  assert.equal(f.entries[0].state.chunnelScroll, 500);
  assert.equal(f.events.at(-1).type, 'chunnel:admin-updated');
  assert.deepEqual(enhanced, [f.regions.get('.admin-content-page')]);
});

test('newer filters abort older work and stale responses cannot replace the current view', async () => {
  const f = fixture(); f.click('?status=draft'); f.click('?status=publish');
  assert.equal(f.requests[0].options.signal.aborted, true);
  f.requests[1].resolve('published'); await tick(); f.requests[0].resolve('stale'); await tick();
  assert.equal(f.regions.get('.admin-content-page').label, 'published'); assert.equal(f.entries.length, 1);
});

for (const invalid of [{ok: false}, {owner: 'other'}, {style: '/theme.css?v=2'}, {missing: true}, {url: 'https://example.test/admin/login/'}]) {
  test(`invalid response retains the current page: ${JSON.stringify(invalid)}`, async () => {
    const f = fixture(); f.click('?status=draft'); f.requests[0].resolve('invalid', invalid); await tick();
    assert.equal(f.regions.get('.admin-content-page').label, 'initial'); assert.equal(f.entries.length, 0);
    assert.ok(f.main.error?.textContent); assert.equal(f.location.search, '');
  });
}

test('network failure leaves current content and unlocks a retry', async () => {
  const f = fixture(); f.click('?status=draft'); f.requests[0].reject(new Error('offline')); await tick();
  assert.ok(f.main.error); assert.equal(f.main.attributes['data-navigation-loading'], undefined);
  f.click('?status=draft'); f.requests[1].resolve('retry'); await tick();
  assert.equal(f.regions.get('.admin-content-page').label, 'retry'); assert.equal(f.main.error, null);
});

test('history restores its own position and yields to user scrolling during fetch', async () => {
  const f = fixture(); f.click('?status=draft'); f.requests[0].resolve('draft'); await tick();
  f.location.search = ''; f.history.state = {chunnelScroll: 190}; f.windowListeners.popstate();
  f.requests[1].resolve('all'); await tick(); assert.equal(f.window.scrollY, 190);
  f.location.search = '?status=draft'; f.history.state = {chunnelScroll: 500}; f.windowListeners.popstate();
  f.window.scrollY = 75; f.requests[2].resolve('draft'); await tick(); assert.equal(f.window.scrollY, 75);
  assert.equal(f.history.scrollRestoration, 'auto');
});

test('failed history traversal rolls back the URL without replacing content', async () => {
  const f = fixture(); f.click('?status=draft'); f.requests[0].resolve('draft'); await tick();
  f.location.search = ''; f.windowListeners.popstate(); f.requests[1].reject(new Error('offline')); await tick();
  assert.equal(f.location.search, '?status=draft'); assert.equal(f.regions.get('.admin-content-page').label, 'draft');
});

test('search submits every filter and preserves typing made while waiting', async () => {
  const f = fixture(); assert.equal(f.submit([['q','first'], ['type','post']]).defaultPrevented, true);
  f.regions.get('.admin-content-page').field.value = 'second';
  f.requests[0].resolve('result'); await tick();
  assert.equal(new URL(f.requests[0].url).searchParams.get('q'), 'first');
  assert.equal(f.regions.get('.admin-content-page').field.value, 'second');
});

test('dirty editors and pending writes block replacement, including changes during fetch', async () => {
  const f = fixture(); f.document.addEventListener('chunnel:admin-before-update', event => event.preventDefault());
  f.click('?status=draft'); assert.equal(f.requests.length, 0); assert.ok(f.main.error);
  const g = fixture(); g.click('?status=draft'); g.regions.get('.admin-content-page').busy = true;
  g.requests[0].resolve('result'); await tick(); assert.equal(g.regions.get('.admin-content-page').label, 'initial');
});

test('new windows, downloads, editors, hashes and outside routes retain native navigation', () => {
  const f = fixture();
  for (const [url, event] of [['?status=draft',{ctrlKey:true}], ['/admin/content/new/',{}], ['?edit=3',{}], ['#details',{}], ['/admin/media/',{}]]) assert.equal(f.click(url, event).defaultPrevented, undefined);
  assert.equal(f.requests.length, 0);
  const editor = fixture('/admin/users/?edit=3'); editor.click('?status=active'); assert.equal(editor.requests.length, 0);
});

test('media filters preserve upload and inspector regions', async () => {
  const f = fixture('/admin/media/'); const untouched = f.regions.get('.admin-users-index');
  f.click('?kind=image'); f.requests[0].resolve('images'); await tick();
  assert.equal(f.regions.get('.admin-media-library').label, 'images');
  assert.equal(f.regions.get('.admin-media-status-tabs').label, 'images');
  assert.equal(f.regions.get('.admin-users-index'), untouched);
});

test('taxonomy searches keep the form, taxonomy changes replace its matching editor', async () => {
  const f = fixture('/admin/taxonomy/'); const form = f.regions.get('.admin-taxonomy-page');
  f.click('?q=hello'); f.requests[0].resolve('search'); await tick(); assert.equal(f.regions.get('.admin-taxonomy-page'), form);
  f.click('?taxonomy=post_tag'); f.requests[1].resolve('tags'); await tick(); assert.equal(f.regions.get('.admin-taxonomy-page').label, 'tags');
});

test('SEO filtering preserves link inspection and native section anchors', async () => {
  const f = fixture('/admin/seo/');
  assert.equal(f.click('#redirects').defaultPrevented, undefined);
  const outside = f.regions.get('#audit-events');
  f.click('?q=old&page=2#redirects'); f.requests[0].resolve('rules'); await tick();
  assert.equal(f.regions.get('#redirects').label, 'rules');
  assert.equal(f.regions.get('#audit-events'), outside);
  assert.equal(f.click('?edit=new#redirects').defaultPrevented, undefined);
});

}
