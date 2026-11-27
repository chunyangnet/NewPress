const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { resolve } = require('node:path');
const nodeTest = require('node:test');
const vm = require('node:vm');
const { compile } = require('./helpers/typescript.cjs');

for (const entry of ['TypeScript', 'generated']) {
const test = (name, fn) => nodeTest(entry + ': ' + name, fn);
const source = entry === 'TypeScript' ? compile('frontend/shared/shell.ts') : readFileSync('themes/chunnel/assets/js/chunnel-shell.js', 'utf8');
const read = path => readFileSync(resolve(__dirname, '../../themes/chunnel', path), 'utf8');
const scrollKey = 'chunnel.navigation.scroll';

function fixture(options = {}) {
  class Element {
    constructor() { this.attributes = {}; this.events = {}; this.classes = new Set(); }
    classList = {
      contains: name => this.classes.has(name),
      add: (...names) => names.forEach(name => this.classes.add(name)),
      remove: (...names) => names.forEach(name => this.classes.delete(name)),
      toggle: (name, value) => value ? this.classes.add(name) : this.classes.delete(name),
    };
    addEventListener(name, handler) { this.events[name] = handler; }
    getAttribute(name) { return this.attributes[name]; }
    setAttribute(name, value) { this.attributes[name] = value; }
    hasAttribute(name) { return name in this.attributes; }
    toggleAttribute(name, value) { if (value) this.setAttribute(name, ''); else delete this.attributes[name]; }
    click() { this.events.click?.({ target: this, button: 0 }); }
    contains(target) { return this === target; }
    closest() { return null; }
    append() {}
    focus() {}
  }
  const row = new Element(), nav = new Element(), toggle = new Element(), header = new Element(), search = new Element(), toast = new Element();
  const label = {};
  toggle.querySelector = () => label;
  nav.closest = () => row;
  const nodes = { '[data-header]': header, '[data-nav-toggle]': toggle, '[data-nav]': nav, '[data-search-box]': search, '[data-toast-stack]': toast };
  const document = new Element(), window = new Element(), media = new Element();
  document.documentElement = new Element();
  document.body = new Element();
  document.fonts = { ready: Promise.resolve() };
  document.createElement = () => new Element();
  document.querySelector = selector => nodes[selector] || null;
  const pathname = options.pathname || '/explore/';
  const searchString = options.search || '?sort=new';
  const location = { href: `https://example.test${pathname}${searchString}${options.hash || ''}`, pathname, search: searchString, hash: options.hash || '' };
  const storage = new Map();
  if (options.stored) storage.set(scrollKey, JSON.stringify(options.stored));
  const sessionStorage = {
    getItem: key => storage.has(key) ? storage.get(key) : null,
    setItem: (key, value) => storage.set(key, String(value)),
    removeItem: key => storage.delete(key),
  };
  const scrollCalls = [];
  window.history = { scrollRestoration: 'auto', state: options.historyState || null, replaceState(value) { this.state = value; } };
  window.scrollY = options.scrollY || 0;
  window.scrollTo = value => { scrollCalls.push(value); if (value && typeof value === 'object') window.scrollY = value.top; };
  window.requestAnimationFrame = callback => { callback(); return 1; };
  window.cancelAnimationFrame = () => {};
  window.setTimeout = callback => { callback(); return 1; };
  window.clearTimeout = () => {};
  vm.runInNewContext(source, {
    document, window, Element, Node: Element, location, sessionStorage, URL,
    matchMedia: () => media,
    requestAnimationFrame: window.requestAnimationFrame,
    cancelAnimationFrame: window.cancelAnimationFrame,
  });
  toggle.click();
  return {
    row, nav, toggle, header, search, document, window, media, label, sessionStorage, storage, scrollCalls,
    click(href, clickOptions = {}) {
      const link = new Element();
      link.href = new URL(href, location.href).href;
      link.target = clickOptions.target || '';
      if (clickOptions.download) link.setAttribute('download', '');
      link.closest = selector => selector === 'a[href]' ? link : null;
      const event = { button: 0, ...clickOptions, target: link };
      nav.events.click(event);
      return event;
    },
  };
}

for (const href of ['/posts/welcome/', '/explore/?sort=hot', '/explore/?sort=new', 'https://other.test/']) {
  test('navigation keeps the outgoing menu geometry: ' + href, () => {
    const state = fixture();
    state.click(href);
    assert.equal(state.row.classList.contains('is-open'), true);
    assert.equal(state.toggle.getAttribute('aria-expanded'), 'true');
    assert.equal(state.header.classList.contains('has-open-menu'), true);
  });
}

for (const href of ['#latest', '#', '/explore/?sort=new#latest']) {
  test('same-document anchors still close the mobile menu: ' + href, () => {
    const state = fixture();
    state.click(href);
    assert.equal(state.row.classList.contains('is-open'), false);
    assert.equal(state.toggle.getAttribute('aria-expanded'), 'false');
  });
}

for (const options of [{ ctrlKey: true }, { metaKey: true }, { shiftKey: true }, { altKey: true }, { button: 1 }, { defaultPrevented: true }, { target: '_blank' }, { download: true }]) {
  test('modified and non-navigation clicks preserve the current view: ' + JSON.stringify(options), () => {
    const state = fixture();
    state.click('#latest', options);
    assert.equal(state.row.classList.contains('is-open'), true);
  });
}

test('shell leaves full-document restoration to the browser', () => {
  const state = fixture({ stored: ['/explore/?sort=new', 900], historyState: { chunnelScroll: 640 } });
  assert.equal(state.window.history.scrollRestoration, 'auto');
  assert.equal(state.scrollCalls.length, 0);
});

test('bfcache restore resets transient shell state, not the outgoing page', () => {
  const state = fixture();
  state.search.classList.add('is-visible', 'is-focused');
  state.document.documentElement.classList.add('is-search-editing');
  state.window.events.pageshow({ persisted: false });
  assert.equal(state.row.classList.contains('is-open'), true);
  state.window.events.pageshow({ persisted: true });
  assert.equal(state.row.classList.contains('is-open'), false);
  assert.equal(state.search.classes.size, 0);
  assert.equal(state.document.documentElement.classes.size, 0);
  state.toggle.click();
  assert.equal(state.label.textContent, '收起菜单');
});

test('passive content paints opaque without entry or tab fade animations', () => {
  for (const file of ['chunnel-reader.css', 'chunnel-ui.css', 'chunnel-home.css', 'chunnel-home-glass.css']) {
    assert.doesNotMatch(read('assets/css/' + file), /reader-arrive|chunnel-image-in|tab-reveal/, file);
  }
  assert.match(read('assets/css/chunnel-foundation.source.css'), /html\s*\{[^}]*background:\s*var\(--paper\)/);
});

test('native page navigation adds no snapshot crossfade or whole-page fade-out', () => {
  for (const file of ['chunnel-community.css', 'chunnel-home.css', 'chunnel-home-glass.css']) {
    const css = read('assets/css/' + file);
    assert.doesNotMatch(css, /@view-transition|::view-transition/, file);
  }
  assert.doesNotMatch(readFileSync('frontend/shared/shell.ts', 'utf8'), /startViewTransition|location\.(assign|replace)|document\.body\.style\.opacity/);
});
}
