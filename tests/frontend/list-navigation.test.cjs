const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const test = require('node:test');
const vm = require('node:vm');
const {loadModule} = require('./helpers/typescript.cjs');

function fixture(kind) {
  const home = kind === 'home';
  const requests = [], redirects = [], positions = [], entries = [], timers = new Map();
  let timerID = 0;
  const location = new URL(`https://example.test/${home ? '' : 'explore/'}`);
  location.assign = url => redirects.push(url);
  const node = label => ({
    label, className: label, innerHTML: label, attributes: new Map(),
    setAttribute(name, value) { this.attributes.set(name, value); },
    removeAttribute(name) { this.attributes.delete(name); },
    querySelector() { return null; },
    getBoundingClientRect() { return { top: 496 - window.scrollY }; },
    replaceWith(next) { region = next; },
  });
  let region = node('initial');
  const history = {
    state: { unrelated: 'preserved', chunnelScroll: 780 },
    replaceState(state, title, url) { this.state = state; if (url) location.href = url; },
    pushState(state, title, url) { entries.push(this.state); this.state = state; location.href = url; },
  };
  const window = { scrollY: 780, scrollTo({ top }) { positions.push(top); this.scrollY = top; } };
  const errors = [];
  window.ChunnelScroll = {
    keep(change, position = {}) { const top = position.top ?? window.scrollY; const result = change(); if (top !== window.scrollY) window.scrollTo({ top }); return result; },
    error(region, message) { errors.push(message); },
  };
  const field = { value: '' };
  const hidden = [];
  const form = {
    querySelectorAll: () => hidden.slice(),
    append(input) { hidden.push(input); input.remove = () => hidden.splice(hidden.indexOf(input), 1); },
  };
  const count = { innerHTML: '9' }, facts = { innerHTML: '9 articles, updated' };
  const discovery = {
    classList: { add() {}, remove() {} },
    querySelector: selector => selector === '[data-discovery-results]' ? region : ({ '[data-discovery-search]': form, '.explore-hero h1 .portal-count': count, '.explore-facts': facts })[selector] || null,
    querySelectorAll: () => [],
  };
  const context = {
    URL, AbortController, history, location, window, document: { title: 'initial', querySelector() { return null; } }, discovery,
    feedRequest: null, discoveryRequest: null, feedScrollTimer: null, discoveryScrollTimer: null,
    feedLocation: location.pathname + location.search, discoveryLocation: location.pathname + location.search,
    setTimeout(callback, delay) { timers.set(++timerID, { callback, delay }); return timerID; },
    clearTimeout(id) { timers.delete(id); },
    $: selector => selector === '#latest' ? region : selector === '#explore-query' ? field : null,
    discoveryRegion: () => region, discoveryStatus: () => null,
    preferences() {}, syncDiscoveryView() {}, refreshPreferences() {}, featureReady: false,
    rememberFeedScroll: remember, rememberDiscoveryScroll: remember,
    placeFeed: place, placeDiscovery: place,
    fetch(url, options) {
      return new Promise((resolve, reject) => requests.push({
        url, signal: options.signal, reject,
        resolve(label, values = {}) { resolve({ ok: true, headers: { get: () => 'text/html' }, text: async () => ({ label, ...values }) }); },
      }));
    },
    DOMParser: class {
      parseFromString({ label, category = '', sort = 'updated', query = '' }) {
        const next = node(label);
        const fields = [{ name: 'sort', value: sort }, ...(category ? [{ name: 'category', value: category }] : [])];
        return { title: label, querySelector(selector) {
          if (selector === '#latest' || selector === '[data-discovery-results]') return next;
          if (selector === '#explore-query') return { value: query };
          if (selector === '[data-discovery-search]') return { querySelectorAll: () => fields.map(input => ({ cloneNode: () => ({ ...input }) })) };
          if (selector === '.explore-hero h1 .portal-count') return { innerHTML: '3' };
          if (selector === '.explore-facts') return { innerHTML: `3 articles, ${sort}` };
          return null;
        } };
      }
    },
  };
  function remember() { history.replaceState({ ...history.state, chunnelScroll: window.scrollY }, ''); }
  function place() { window.scrollTo({ top: 400 }); }
  if (home) {
    context.document.querySelector = context.$;
    context.document.addEventListener = () => {};
    window.addEventListener = () => {};
    const controller = loadModule('frontend/themes/chunnel/home-feed-controller.ts',context).createHomeFeed();
    context.navigate = controller.loadFeed; context.switchView = controller.applyFeedView; context.ensureBadgeStyles = controller.ensureBadgeStyles;
  } else context.navigate=loadModule('frontend/shared/discovery.ts',context).createDiscovery(discovery,{$:context.$,optional:context.$,$$:()=>[]},()=>{}).loadDiscovery;
  return { ...context, requests, redirects, positions, entries, hidden, field, count, facts, errors, timers, region: () => region };
}

test('home layout switches reuse article nodes, update filter URLs and cancel stale requests', async () => {
  const f = fixture('home');
  const panel = f.region(), classes = new Set(['home-feed-list']);
  const feed = { classList: { toggle(name, active) { if (active) classes.add(name); else classes.delete(name); } } };
  const link = href => ({ href: new URL(href, f.location).href, attributes: {}, setAttribute(name, value) { this.attributes[name] = value; }, removeAttribute(name) { delete this.attributes[name]; } });
  const views = [link('?view=list'), link('?view=grid')], filters = [link('?category=design&view=list')];
  panel.querySelector = selector => selector === '.home-feed' ? feed : null;
  panel.querySelectorAll = selector => selector === '.home-view-switch a' ? views : filters;
  const pending = f.navigate(new URL('?category=stale', f.location).href);
  assert.equal(f.switchView(new URL('?view=grid', f.location).href), true);
  assert.equal(f.requests.length, 1, 'layout changes do not request a new document or list');
  assert.equal(f.requests[0].signal.aborted, true);
  assert.equal(f.region(), panel);
  assert.equal(panel.querySelector('.home-feed'), feed);
  assert.equal(classes.has('home-feed-grid'), true);
  assert.equal(classes.has('home-feed-list'), false);
  assert.equal(views[1].attributes['aria-current'], 'true');
  assert.equal(new URL(filters[0].href).searchParams.get('view'), 'grid');
  assert.equal(f.window.scrollY, 780);
  f.requests[0].resolve('stale');
  await pending;
  assert.equal(panel.innerHTML, 'initial');
  assert.equal(f.location.search, '?view=grid');
});

test('home loads badge styles before committing newly badged feed content and shares concurrent loads', async () => {
  const f = fixture('home'), links = [];
  f.document.createElement = () => ({ setAttribute() {}, remove() { links.splice(links.indexOf(this), 1); } });
  f.document.head = { append(link) { links.push(link); } };
  const doc = { querySelector: () => ({ getAttribute: () => '/theme-assets/chunnel/version/css/chunnel-article-badges.css' }) };
  const next = { querySelector: () => ({}) };
  let ready = false;
  const first = f.ensureBadgeStyles(doc, next);
  first.then(() => { ready = true; });
  const second = f.ensureBadgeStyles(doc, next);
  assert.equal(first, second);
  assert.equal(links.length, 1);
  assert.equal(ready, false);
  assert.equal(links[0].href, 'https://example.test/theme-assets/chunnel/version/css/chunnel-article-badges.css');
  links[0].onload();
  await first;
  assert.equal(ready, true);
});

test('home retries failed badge styles and rejects cross-origin styles', async () => {
  const f = fixture('home'), links = [];
  f.document.createElement = () => ({ setAttribute() {}, remove() { links.splice(links.indexOf(this), 1); } });
  f.document.head = { append(link) { links.push(link); } };
  const doc = { querySelector: () => ({ getAttribute: () => '/theme-assets/chunnel/version/css/chunnel-article-badges.css' }) };
  const next = { querySelector: () => ({}) };
  const failed = f.ensureBadgeStyles(doc, next);
  links[0].onerror();
  await assert.rejects(failed, /unavailable/);
  assert.equal(links.length, 0);
  await assert.rejects(f.ensureBadgeStyles({ querySelector: () => ({ getAttribute: () => 'https://other.test/styles.css' }) }, next), /origin/);
  const retry = f.ensureBadgeStyles(doc, next);
  links[0].onload();
  await retry;
});

test('a stalled badge stylesheet times out, clears handlers and permits a fresh load', async () => {
  const f = fixture('home'), links = [];
  f.document.createElement = () => ({ setAttribute() {}, remove() { links.splice(links.indexOf(this), 1); } });
  f.document.head = { append(link) { links.push(link); } };
  const doc = { querySelector: () => ({ getAttribute: () => '/theme-assets/chunnel/version/css/chunnel-article-badges.css' }) };
  const next = { querySelector: () => ({}) };
  const first = f.ensureBadgeStyles(doc, next), old = links[0];
  [...f.timers.values()].find(timer => timer.delay === 15000).callback();
  await assert.rejects(first, /timeout/);
  assert.equal(old.onload, null); assert.equal(old.onerror, null);
  assert.equal(links.length, 0); assert.equal(f.timers.size, 0);
  const retry = f.ensureBadgeStyles(doc, next); links[0].onload(); await retry;
  assert.equal(f.timers.size, 0);
});

for (const kind of ['home', 'discovery']) {
  test(`${kind} navigation preserves the outgoing history position and unrelated state`, async () => {
    const f = fixture(kind);
    const task = f.navigate(new URL('?category=design', f.location).href);
    f.window.scrollY = 920;
    f.requests[0].resolve('design');
    await task;
    assert.equal(f.entries[0].chunnelScroll, 920);
    assert.equal(f.history.state.chunnelScroll, 920);
    assert.deepEqual(f.positions, [], 'filters do not force a jump to the result heading');
    assert.equal(f.history.state.unrelated, 'preserved');
    assert.equal(f.document.title, 'design');
  });

  test(`${kind} paging explicitly positions the result heading`, async () => {
    const f = fixture(kind);
    const task = f.navigate(new URL('?page=2', f.location).href, { scroll: true });
    f.requests[0].resolve('page 2');
    await task;
    assert.equal(f.window.scrollY, 400);
  });

  test(`${kind} failed requests keep the document and previous results available for retry`, async () => {
    const f = fixture(kind);
    const task = f.navigate(new URL('?category=unavailable', f.location).href);
    f.requests[0].reject(new Error('offline'));
    await task;
    assert.deepEqual(f.redirects, []);
    assert.equal(f.region().innerHTML, 'initial');
    assert.equal(f.entries.length, 0);
    assert.equal(f.history.scrollRestoration, 'auto');
    assert.equal(f.errors.length, 1);
  });

  test(`${kind} history traversal restores its saved position including the top of the page`, async () => {
    for (const top of [0, 1150]) {
      const f = fixture(kind);
      f.history.state = { chunnelScroll: top };
      const task = f.navigate(f.location.href, { history: 'none', scroll: false });
      assert.equal(f.positions.length, 0, 'wait for the destination content before restoring');
      f.requests[0].resolve('restored');
      await task;
      assert.equal(f.window.scrollY, top);
      assert.equal(f.entries.length, 0);
    }
  });

  test(`${kind} ignores a superseded response even when the transport resolves after abort`, async () => {
    const f = fixture(kind);
    const first = f.navigate(new URL('?category=old', f.location).href);
    const latest = f.navigate(new URL('?category=new', f.location).href);
    assert.equal(f.requests[0].signal.aborted, true);
    f.requests[1].resolve('new');
    await latest;
    f.requests[0].resolve('old');
    await first;
    assert.equal(f.region().innerHTML, 'new');
    assert.equal(f.location.search, '?category=new');
    assert.equal(f.entries.length, 1);
  });

  test(`${kind} history restoration yields to scrolling during a slow traversal`, async () => {
    const f = fixture(kind);
    f.history.state = { chunnelScroll: 1200 };
    const task = f.navigate(f.location.href, { history: 'none' });
    f.window.scrollY = 320;
    f.requests[0].resolve('restored');
    await task;
    assert.equal(f.window.scrollY, 320);
    assert.equal(f.history.scrollRestoration, 'auto');
  });

  test(`${kind} ignores late failures from superseded requests`, async () => {
    const f = fixture(kind);
    const first = f.navigate(new URL('?category=old', f.location).href);
    const latest = f.navigate(new URL('?category=new', f.location).href);
    f.requests[0].reject(new Error('connection closed'));
    await first;
    assert.deepEqual(f.redirects, []);
    f.requests[1].resolve('new');
    await latest;
  });
}

test('discovery refresh synchronizes search constraints and totals without replacing the search field', async () => {
  const f = fixture('discovery');
  f.hidden.push({ name: 'category', value: 'old', remove() { f.hidden.splice(f.hidden.indexOf(this), 1); } });
  const task = f.navigate(new URL('?category=design&sort=title', f.location).href);
  f.field.value = 'unfinished next query';
  f.requests[0].resolve('design', { category: 'design', sort: 'title' });
  await task;
  assert.deepEqual(f.hidden.map(({ name, value }) => [name, value]), [['sort', 'title'], ['category', 'design']]);
  assert.equal(f.field.value, 'unfinished next query');
  assert.equal(f.count.innerHTML, '3');
  assert.equal(f.facts.innerHTML, '3 articles, title');
});

test('home refresh restores the focused filter without scrolling', async () => {
  const f = fixture('home'), panel = f.region(), calls = [];
  const href = new URL('?sort=updated&view=list#latest', f.location).href;
  const active = { href, closest(selector) { return selector === 'a[href]' ? this : { getAttribute: () => '首页内容排序' }; } };
  panel.contains = element => element === active;
  panel.querySelectorAll = () => [
    { href, closest: () => ({ getAttribute: () => '文章排列方式' }), focus: () => calls.push('wrong group') },
    { href, closest: () => ({ getAttribute: () => '首页内容排序' }), focus: options => calls.push(options.preventScroll) },
  ];
  f.document.activeElement = active;
  const task = f.navigate(href);
  f.requests[0].resolve('updated');
  await task;
  assert.deepEqual(calls, [true]);
  assert.deepEqual(f.positions, []);
});

test('discovery refresh restores a replaced control without scrolling', async () => {
  const f = fixture('discovery'), calls = [];
  const active = { id: 'explore-sort', isConnected: true };
  const replacement = { id: active.id, focus(options) { calls.push(options.preventScroll); f.document.activeElement = this; } };
  const region = f.region(), replace = region.replaceWith;
  region.replaceWith = next => { active.isConnected = false; replace(next); };
  f.discovery.querySelectorAll = () => [replacement];
  f.document.activeElement = active;
  const task = f.navigate(new URL('?sort=title', f.location).href);
  f.requests[0].resolve('sorted', { sort: 'title' });
  await task;
  assert.equal(f.document.activeElement, replacement);
  assert.deepEqual(calls, [true]);
  assert.deepEqual(f.positions, []);
  assert.deepEqual(f.errors, []);
});

test('discovery slow refresh preserves focus and typing in the next search', async () => {
  const f = fixture('discovery');
  const active = { id: 'explore-sort', isConnected: true };
  const region = f.region(), replace = region.replaceWith;
  region.replaceWith = next => { active.isConnected = false; replace(next); };
  f.discovery.querySelectorAll = selector => {
    if (selector === 'a[href], input[name], select[name], button[type="submit"]') throw new Error('must not move connected focus');
    return [];
  };
  f.document.activeElement = active;
  const task = f.navigate(new URL('?sort=title', f.location).href, 'push', '[data-content-summary]');
  const search = { id: 'explore-query', isConnected: true };
  f.document.activeElement = search;
  f.field.value = 'unfinished next query';
  f.requests[0].resolve('sorted', { sort: 'title' });
  await task;
  assert.equal(f.document.activeElement, search);
  assert.equal(f.field.value, 'unfinished next query');
  assert.equal(f.region().label, 'sorted');
  assert.deepEqual(f.errors, []);
});

test('home paging focuses the heading when the old page link becomes current', async () => {
  const f = fixture('home'), panel = f.region(), calls = [];
  const active = { href: new URL('?page=2', f.location).href, closest(selector) { return selector === 'a[href]' ? this : null; } };
  panel.contains = element => element === active;
  panel.querySelectorAll = () => [];
  panel.querySelector = selector => selector === 'h2' ? {
    setAttribute: (name, value) => calls.push([name, value]),
    focus: options => calls.push(options.preventScroll),
  } : null;
  f.document.activeElement = active;
  const task = f.navigate(active.href);
  f.requests[0].resolve('page 2');
  await task;
  assert.deepEqual(calls, [['tabindex', '-1'], true]);
});

test('home slow refresh respects focus moved outside the list', async () => {
  const f = fixture('home'), panel = f.region();
  const active = { href: new URL('?sort=updated', f.location).href, closest() { return this; } };
  panel.contains = element => element === active;
  panel.querySelectorAll = () => { throw new Error('must not restore focus'); };
  f.document.activeElement = active;
  const task = f.navigate(active.href);
  const search = { name: 'q' };
  f.document.activeElement = search;
  f.requests[0].resolve('updated');
  await task;
  assert.equal(f.document.activeElement, search);
  assert.equal(f.region().innerHTML, 'updated');
  assert.deepEqual(f.errors, []);
});

test('refresh reveals an offscreen category without moving a visible selection or the document', () => {
  const strip = {
    scrollLeft: 20, scrollWidth: 480, clientWidth: 320,
    getBoundingClientRect: () => ({ left: 10, right: 330 }),
    querySelector: () => ({ getBoundingClientRect: () => bounds }),
  };
  let bounds = { left: 296, right: 392 };
  const context = { URL, location: new URL('https://example.test/explore/'), $: () => null, $$: () => [],
    discovery: { querySelector: () => strip, querySelectorAll: () => [] } };
  context.syncDiscoveryView=loadModule('frontend/shared/discovery.ts',context).createDiscovery(context.discovery,{$:context.$,optional:context.$,$$:context.$$},()=>{}).syncDiscoveryView;
  context.syncDiscoveryView(true);
  assert.equal(strip.scrollLeft, 82);
  bounds = { left: 80, right: 176 };
  context.syncDiscoveryView(true);
  assert.equal(strip.scrollLeft, 82);
  bounds = { left: -30, right: 66 };
  context.syncDiscoveryView();
  assert.equal(strip.scrollLeft, 82, 'view-only changes preserve horizontal position');
  context.syncDiscoveryView(true);
  assert.equal(strip.scrollLeft, 42);
});

test('discovery layout updates only links to its own route', () => {
  const location = new URL('https://example.test/explore/?view=list');
  const links = ['https://example.test/explore/?category=design', 'https://example.test/posts/example#comments', 'https://outside.test/explore/?q=external'].map(href => ({ href, hash: new URL(href).hash }));
  loadModule('frontend/shared/discovery.ts',{URL,location}).createDiscovery({querySelectorAll:()=>links},{$:()=>null,optional:()=>null,$$:()=>[]},()=>{}).syncDiscoveryView();
  assert.equal(links[0].href, '/explore/?category=design&view=list');
  assert.equal(links[1].href, 'https://example.test/posts/example#comments');
  assert.equal(links[2].href, 'https://outside.test/explore/?q=external');
});
