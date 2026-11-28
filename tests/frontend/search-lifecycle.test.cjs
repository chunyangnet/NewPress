'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const nodeTest = require('node:test');
const { compile } = require('./helpers/typescript.cjs');

for (const entry of ['TypeScript', 'generated']) {
const test = (name, fn) => nodeTest(entry + ': ' + name, fn);
const source = entry === 'TypeScript' ? compile('frontend/shared/search.ts') : fs.readFileSync(process.env.CHUNNEL_SEARCH_SOURCE || path.join(__dirname, '../../themes/chunnel/assets/js/chunnel-search.js'), 'utf8');
const {loadModules} = require('./helpers/typescript.cjs');
const flush = () => new Promise(resolve => setImmediate(resolve));
const posts = term => ({ items: Array.from({ length: 5 }, (_, i) => ({ slug: `${term}-${i}`, title: `${term}文章 ${i}`, author_name: '作者' })), total: 9 });

// The real module runs against controlled focus, event ordering, timers and
// delayed response bodies. Browser coverage separately exercises native layout.
function fixture({ composing = false, dismissed = false, initialFocus = false, bootstrap = false } = {}) {
  let document;
  class Element {
    constructor(tag = 'div') {
      this.tag = tag; this.children = []; this.listeners = new Map(); this.attributes = new Map();
      this.dataset = {}; this.hidden = false; this.value = ''; this.textContent = ''; this.scrollTop = 0; this.isConnected = true;
      this.bounds = { top: 0, bottom: 100 }; this.clicks = 0;
    }
    addEventListener(type, fn) { this.listeners.set(type, [...(this.listeners.get(type) || []), fn]); }
    emit(type, extra = {}) {
      const event = { type, target: this, preventDefault() { this.defaultPrevented = true; }, stopPropagation() { this.stopped = true; }, ...extra };
      let target = this;
      do {
        for (const fn of target.listeners.get(type) || []) fn(event);
        target = !event.stopped && ['keydown', 'input', 'compositionstart', 'compositionend', 'focusout', 'submit'].includes(type) ? target.parent : null;
      } while (target);
      return event;
    }
    append(...children) { for (const child of children) { child.parent = this; this.children.push(child); } }
    get firstChild() { return this.children[0]; }
    get lastChild() { return this.children.at(-1); }
    remove() { this.isConnected = false; if (this.parent) this.parent.children = this.parent.children.filter(child => child !== this); }
    replaceChildren(...children) { this.children = []; this.append(...children); }
    contains(target) { return target === this || this.children.some(child => child.contains(target)); }
    setAttribute(name, value) { this.attributes.set(name, String(value)); }
    getAttribute(name) { return this.attributes.get(name) ?? null; }
    removeAttribute(name) { this.attributes.delete(name); }
    focus(options) {
      if (document.activeElement === this) return;
      const previous = document.activeElement; document.activeElement = this; this.lastFocus = options;
      previous?.emit('focusout', { relatedTarget: this }); this.emit('focus');
    }
    click() { this.clicks++; this.emit('click'); }
    getBoundingClientRect() { return this.bounds; }
  }
  document = new Element('document');
  document.body = new Element('body'); document.head = new Element('head');
  document.currentScript = { src: 'https://example.test/theme-assets/js/chunnel-home.js?v=test' };
  const window = new Element('window'), form = new Element('form'), input = new Element('input');
  const launcher = new Element('button'), outside = new Element('button');
  document.append(form, launcher, outside); form.append(input); document.activeElement = initialFocus ? input : outside;
  if (composing) input.dataset.searchComposing = 'true';
  if (dismissed) input.dataset.searchDismissed = 'true';
  input.value = initialFocus ? '初始' : '';
  form.querySelector = selector => selector === '[data-search-input]' ? input : null;
  const bootstrapStatus = new Element('div'); bootstrapStatus.className = 'search-preview'; bootstrapStatus.hidden = true;
  const bootstrapCopy = new Element('p'), bootstrapRetry = new Element('button');
  bootstrapCopy.className = 'search-preview-status'; bootstrapCopy.setAttribute('role', 'status');
  bootstrapRetry.className = 'search-preview-retry'; bootstrapRetry.type = 'button';
  bootstrapStatus.append(bootstrapCopy, bootstrapRetry);
  if (bootstrap) form.append(bootstrapStatus);
  document.querySelector = selector => selector === '[data-search-box]' ? form : selector === '[data-search-input]' ? input : selector === '[data-home-search-status]' && bootstrap ? bootstrapStatus : null;
  document.querySelectorAll = () => [launcher];
  document.createElement = tag => new Element(tag);
  document.createDocumentFragment = () => new Element('fragment');
  document.createTextNode = text => Object.assign(new Element('text'), { textContent: text });
  const timers = new Map(), requests = []; let nextTimer = 0;
  const navigator = { onLine: true };
  const context = vm.createContext({
    document, window, navigator, Element, Node: Element, URL, URLSearchParams, AbortController,
    setTimeout: (fn, delay) => { timers.set(++nextTimer, { fn, delay }); return nextTimer; },
    clearTimeout: id => timers.delete(id),
    fetch: (url, options) => new Promise((resolve, reject) => {
      requests.push({ url, signal: options.signal, reject,
        resolve: (data, status = 200) => resolve({ ok: status === 200, status, json: async () => data }),
        // A response body may finish after a newer query or dismissal.
        body: promise => resolve({ ok: true, json: () => promise }),
      });
      options.signal.addEventListener('abort', () => reject(new Error('aborted')), { once: true });
    }),
  });
  if (bootstrap) {
    const [loader, search] = loadModules(['frontend/themes/chunnel/home-loader.ts','frontend/themes/chunnel/home-search-loader.ts'],context);
    search.initHomeSearch(loader.createHomeLoader(new URL(document.currentScript.src)));
  } else vm.runInContext(source, context, { filename: 'chunnel-search.js' });
  const advance = delay => {
    for (const [id, timer] of [...timers]) if (timer.delay === delay) { timers.delete(id); timer.fn(); }
  };
  const type = (value, extra = {}) => { input.value = value; return input.emit('input', extra); };
  const start = (value = '搜索') => { input.focus(); type(value); advance(240); return requests.at(-1); };
  return { document, window, form, input, launcher, outside, timers, requests, navigator, advance, type, start,
    get panel() { return form.children[1]; },
    get status() { return this.panel.children[1]; },
    get list() { return this.panel.children[2]; },
    get retry() { return this.panel.children[3]; },
    get all() { return this.panel.children[4].children[0]; },
    finishScript() { vm.runInContext(source, context); document.head.lastChild.onload(); },
  };
}

test('search stays idle until focus and debounces rapid edits into the final query', async () => {
  const f = fixture(); assert.equal(f.requests.length, 0); assert.equal(f.panel.hidden, true);
  f.input.focus(); f.type('雨'); f.type('雨后'); f.type('雨后 散步'); f.advance(240);
  assert.equal(f.requests.length, 1);
  assert.equal(new URL(f.requests[0].url, 'https://example.test').searchParams.get('q'), '雨后 散步');
  assert.equal(new URL(f.requests[0].url, 'https://example.test').searchParams.get('sort'), 'relevance');
  f.requests[0].resolve(posts('雨后')); await flush();
  assert.equal(f.list.children.length, 5); assert.match(f.status.textContent, /9 篇/);
  assert.equal(f.list.getAttribute('aria-busy'), null);
});

test('composition waits for its final text and does not submit or navigate candidates', async () => {
  const f = fixture(); f.input.focus(); f.input.emit('compositionstart'); f.type('zhong', { isComposing: true });
  f.advance(240); assert.equal(f.requests.length, 0);
  assert.equal(f.form.emit('submit').defaultPrevented, true);
  assert.equal(f.input.emit('keydown', { key: 'ArrowDown', keyCode: 229 }).defaultPrevented, undefined);
  f.input.value = '中文'; f.input.emit('compositionend'); f.type('中文', { inputType: 'insertFromComposition' }); f.advance(240);
  assert.equal(f.requests.length, 1); f.requests[0].resolve(posts('中文')); await flush();
  assert.match(f.list.children[0].href, /%E4%B8%AD%E6%96%87/);
});

test('invalid response roots show failure and malformed records cannot become search links', async () => {
  for (const data of [null, [], { items: null }]) {
    const f = fixture(); f.start().resolve(data); await flush();
    assert.equal(f.list.children.length, 0); assert.equal(f.retry.hidden, false);
    assert.match(f.status.textContent, /暂时无法加载/);
  }
  const f = fixture();
  f.start().resolve({ items: [null, { slug: '../private', title: 'invalid' }, { slug: 'ok', title: 7 },
    { slug: '真实文章', title: '<img src=x onerror=alert(1)>', category: {}, author_name: '作者' }], total: '900' });
  await flush();
  assert.equal(f.list.children.length, 1);
  assert.equal(f.list.children[0].href, '/posts/' + encodeURIComponent('真实文章'));
  assert.match(f.status.textContent, /找到 1 篇/);
  assert.equal(f.list.children[0].children[1].children[0].children[0].children[0].textContent, '<img src=x onerror=alert(1)>');
});

for (const dismissal of ['focusout', 'outside pointer', 'overlay', 'pagehide']) {
  test(`a ${dismissal} during composition suppresses its end and trailing input`, async () => {
    const f = fixture(); f.input.focus(); f.input.emit('compositionstart'); f.type('zhong', { isComposing: true });
    if (dismissal === 'focusout') f.outside.focus();
    if (dismissal === 'outside pointer') f.document.emit('pointerdown', { target: f.outside });
    if (dismissal === 'overlay') f.document.emit('chunnel:overlay', { detail: { open: true } });
    if (dismissal === 'pagehide') f.window.emit('pagehide');
    assert.equal(f.panel.hidden, true);
    f.input.value = '中文'; f.input.emit('compositionend'); f.type('中文', { inputType: 'insertText' }); f.advance(240);
    assert.equal(f.panel.hidden, true); assert.equal(f.requests.length, 0);
    assert.equal(f.input.value, '中文'); assert.equal(f.input.getAttribute('aria-expanded'), 'false');
    f.outside.focus(); f.input.focus(); f.advance(240);
    assert.equal(f.requests.length, 1); f.requests[0].resolve(posts('中文')); await flush();
    assert.equal(f.list.children.length, 5);
  });
}

test('new edits and a new composition can reopen after a canceled composition without blur', () => {
  const f = fixture(); f.input.focus(); f.input.emit('compositionstart'); f.type('zhi');
  f.document.emit('pointerdown', { target: f.outside });
  f.input.value = '止'; f.input.emit('compositionend'); f.type('止'); f.advance(240);
  assert.equal(f.requests.length, 0);
  f.type('止步', { inputType: 'insertFromPaste' }); f.advance(240);
  assert.equal(f.requests.length, 1);
  f.input.emit('compositionstart'); f.document.emit('chunnel:overlay', { detail: { open: true } });
  f.input.emit('compositionend');
  f.input.emit('compositionstart'); f.type('再', { isComposing: true }); f.input.emit('compositionend'); f.advance(240);
  assert.equal(f.requests.length, 2); assert.equal(f.panel.hidden, false);
});

test('input delivered while focus is elsewhere cannot open or request a search', () => {
  const f = fixture(); f.type('浏览器补全'); f.advance(240);
  assert.equal(f.panel.hidden, true); assert.equal(f.requests.length, 0);
});

test('Escape during composition leaves the active preview and query intact', () => {
  const f = fixture(); f.input.focus(); f.input.emit('compositionstart'); f.type('拼');
  f.input.emit('keydown', { key: 'Escape', isComposing: false, keyCode: 229 });
  assert.equal(f.panel.hidden, false); assert.equal(f.input.value, '拼');
  f.input.emit('compositionend'); f.advance(240); assert.equal(f.requests.length, 1);
});

test('Escape cancels pending search and explicit typing reopens it', async () => {
  const f = fixture(), pending = f.start();
  f.input.emit('keydown', { key: 'Escape' }); await flush();
  assert.equal(pending.signal.aborted, true); assert.equal(f.panel.hidden, true);
  assert.equal(f.input.getAttribute('aria-activedescendant'), null);
  f.type('再搜索'); f.advance(240); assert.equal(f.requests.length, 2);
});

test('late response bodies cannot overwrite a newer query or revive a closed preview', async () => {
  const f = fixture(), old = f.start('旧'); let release;
  old.body(new Promise(resolve => { release = resolve; })); await flush();
  f.type('新'); f.advance(240); f.requests[1].resolve(posts('新')); await flush();
  release(posts('旧')); await flush(); assert.match(f.list.children[0].href, /%E6%96%B0/);
  f.type('关闭前'); f.advance(240); let finish;
  f.requests[2].body(new Promise(resolve => { finish = resolve; })); await flush();
  f.outside.focus(); finish(posts('迟到')); await flush();
  assert.equal(f.panel.hidden, true); assert.equal(f.list.children.length, 0);
});

test('timeout offers an explicit retry with the same text and prevents focus scrolling', async () => {
  const f = fixture(), pending = f.start('慢查询'); f.advance(8000); await flush();
  assert.equal(pending.signal.aborted, true); assert.equal(f.retry.hidden, false);
  f.retry.focus(); f.retry.click(); assert.equal(f.requests.length, 2);
  assert.equal(f.input.value, '慢查询'); assert.equal(f.input.lastFocus.preventScroll, true);
  f.requests[1].resolve(posts('慢查询')); await flush();
  assert.equal(f.retry.hidden, true); assert.equal(f.timers.size, 0);
});

test('unavailable quick search retains full search while offline failure permits retry', async () => {
  const f = fixture(); f.start('完整').resolve({}, 404); await flush();
  assert.equal(f.retry.hidden, true); assert.match(f.status.textContent, /完整搜索/);
  assert.equal(new URL(f.all.href, 'https://example.test').searchParams.get('q'), '完整');
  f.navigator.onLine = false; f.type('离线'); f.advance(240); f.requests[1].reject(new Error('offline')); await flush();
  assert.equal(f.retry.hidden, false); assert.match(f.status.textContent, /离线/);
});

test('keyboard choices scroll only the panel and Enter activates the selected result', async () => {
  const f = fixture(); f.start().resolve(posts('搜索')); await flush();
  f.panel.bounds = { top: 20, bottom: 120 };
  f.list.children[0].bounds = { top: 150, bottom: 200 };
  f.input.emit('keydown', { key: 'ArrowDown' });
  assert.equal(f.panel.scrollTop, 80); assert.equal(f.document.activeElement, f.input);
  assert.equal(f.input.getAttribute('aria-activedescendant'), f.list.children[0].id);
  assert.equal(f.list.children[0].getAttribute('aria-selected'), 'true');
  f.input.emit('keydown', { key: 'Enter' }); assert.equal(f.list.children[0].clicks, 1);
  f.input.emit('keydown', { key: 'ArrowUp' }); assert.equal(f.list.children[4].getAttribute('aria-selected'), 'true');
});

test('late initialization respects dismissal and inherited composition before an intentional focus', () => {
  const dismissed = fixture({ initialFocus: true, dismissed: true }); dismissed.advance(240);
  assert.equal(dismissed.panel.hidden, true); assert.equal(dismissed.requests.length, 0);
  const f = fixture({ initialFocus: true, composing: true }); f.advance(240);
  assert.equal(f.requests.length, 0); f.input.value = '输入完成'; f.input.emit('compositionend'); f.advance(240);
  assert.equal(f.requests.length, 1);
});

test('Escape while the lazy search script loads preserves text and suppresses late initialization', async () => {
  const f = fixture({ bootstrap: true }); f.input.focus(); f.type('准备收起');
  const event = f.input.emit('keydown', { key: 'Escape' });
  // A native search input clears itself and emits input when Escape is not canceled.
  if (!event.defaultPrevented) f.type('');
  assert.equal(f.input.value, '准备收起'); assert.equal(f.panel.hidden, true);
  f.finishScript(); await flush(); f.advance(240);
  assert.equal(f.panel.hidden, true); assert.equal(f.requests.length, 0);
  assert.equal(f.form.getAttribute('aria-busy'), null);
  assert.equal(f.input.emit('keydown', { key: 'Escape' }).defaultPrevented, undefined);
});

test('IME Escape during lazy loading stays inside search without canceling the input method', async () => {
  const f = fixture({ bootstrap: true }); let globalEscape = 0;
  f.document.addEventListener('keydown', () => globalEscape++);
  f.input.focus(); f.input.emit('compositionstart'); f.type('zhong', { isComposing: true });
  const event = f.input.emit('keydown', { key: 'Escape', keyCode: 229 });
  assert.equal(event.defaultPrevented, undefined); assert.equal(globalEscape, 0); assert.equal(f.panel.hidden, false);
  f.input.value = '中文'; f.input.emit('compositionend'); f.finishScript(); await flush(); f.advance(240);
  assert.equal(f.requests.length, 1);
  assert.equal(new URL(f.requests[0].url, 'https://example.test').searchParams.get('q'), '中文');
});

test('failed lazy search can retry without scrolling or losing the query', async () => {
  const f = fixture({ bootstrap: true }); f.input.focus(); f.type('保留关键词');
  f.document.head.lastChild.onerror(); await flush();
  const retry = f.panel.lastChild; assert.equal(retry.hidden, false);
  retry.focus(); retry.click();
  assert.equal(f.input.lastFocus.preventScroll, true); assert.equal(f.input.value, '保留关键词');
  f.finishScript(); await flush(); f.advance(240);
  assert.equal(f.requests.length, 1); assert.equal(f.form.getAttribute('aria-busy'), null);
});

for (const bootstrap of [false, true]) test(`pointer retry survives a browser that blurs buttons without focusing them (bootstrap=${bootstrap})`, async () => {
  const f = fixture({ bootstrap });
  if (bootstrap) {
    f.input.focus(); f.type('保留关键词'); f.document.head.lastChild.onerror();
  } else f.start('保留关键词').reject(new Error('offline'));
  await flush();
  const panel = f.panel, retry = bootstrap ? panel.lastChild : f.retry;
  assert.equal(retry.hidden, false);
  const down = retry.emit('pointerdown', { button: 0 });
  // WebKit's pointer default blurs the input with no relatedTarget, hiding
  // the panel before mouseup/click. Canceling that default preserves the hit.
  if (!down.defaultPrevented) {
    f.document.activeElement = f.document.body;
    f.input.emit('focusout', { relatedTarget: null });
  }
  assert.equal(panel.hidden, false, 'the retry button disappeared before click');
  retry.click();
  if (bootstrap) { f.finishScript(); await flush(); f.advance(240); }
  assert.equal(f.input.value, '保留关键词');
  assert.equal(f.requests.length, bootstrap ? 1 : 2);
});

}
