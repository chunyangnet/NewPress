const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(process.env.CHUNNEL_WECHAT_SOURCE || path.join(__dirname, '../../internal/coreui/assets/js/chunnel-wechat.source.js'), 'utf8');
const draftSource = fs.readFileSync(path.join(__dirname, '../../internal/coreui/assets/js/chunnel-wechat-drafts.js'), 'utf8');
const tick = () => new Promise(resolve => setImmediate(resolve));

function fixture(search = '', options = {}) {
  const nodes = new Map(), calls = [], sessionCalls = [], timers = new Map(); let timerID = 0;
  const session = {payload: {authenticated:true, read_only:false, user_id:42, csrf_token:'renewed-session-token-'.repeat(3),role_capabilities:['*']},status:200};
  class Node {
    constructor(tag = 'div') { this.tagName = tag; this.children = []; this.dataset = {}; this.events = {}; this.value = ''; this.checked = false; this.disabled = false; this.className = ''; this.classList = { toggle() {} }; this.attrs = {}; }
    append(...children) { const connect = node => { node.isConnected = true; node.children.forEach(connect); }; children.forEach(child => { child.parent = this; connect(child); this.children.push(child); }); }
    replaceChildren(...children) {
      const detach = node => { node.isConnected = false; node.children.forEach(detach); };
      if (this.contains(document.activeElement)) document.activeElement = document.body;
      this.children.forEach(detach); this.children = []; this.append(...children);
    }
    get firstChild() { return this.children[0]; }
    addEventListener(name, fn) { this.events[name] = fn; }
    setAttribute(name, value) { this.attrs[name] = value; }
    contains(node) { return this.children.some(child => child === node || child.contains(node)); }
    focus(options) { document.activeElement = this; this.focusOptions = options; docEvents.focusin?.({type:'focusin', target:this}); }
    showModal() { this.open = true; }
    close(value) { this.open = false; this.returnValue = value; this.events.close?.(); }
    closest(selector) { return selector === this.tagName || selector === '[hidden]' && this.hidden || this.tagName === 'button' && this.dataset.action ? this : this.parent?.closest(selector); }
    reset() { Object.values(this.elements || {}).forEach(node => { node.value = ''; }); }
    querySelector(selector) { if (selector === 'fieldset') return this.fieldset ||= new Node('fieldset'); return this.querySelectorAll(selector)[0] || null; }
    querySelectorAll(selector) {
      const result = [], match = node => selector.split(',').some(s => s === node.tagName || s === 'input[type=checkbox]' && node.tagName === 'input' && node.type === 'checkbox' || s === '[data-wechat-retry]' && node.dataset.wechatRetry !== undefined || s.startsWith('.') && node.className.split(' ').includes(s.slice(1)));
      const walk = node => { for (const child of node.children) { if (match(child)) result.push(child); walk(child); } }; walk(this); return result;
    }
  }
  const get = selector => { if (!nodes.has(selector)) nodes.set(selector, new Node()); return nodes.get(selector); };
  const form = (selector, fields) => { const n = get(selector); n.elements = {}; fields.forEach(field => { const input = new Node('input'); n.elements[field] = input; n.append(input); }); return n; };
  form('[data-wechat-config]', ['app_id', 'author', 'thumb_media_id', 'enabled', 'collect_enabled', 'collect_drafts', 'auto_sync', 'auto_publish', 'include_source', 'open_comment', 'fans_comment', 'app_secret', 'clear_secret']);
  form('[data-wechat-sync]', ['q', 'post_id']);
  form('[data-wechat-collect]', ['urls']);
  get('[data-wechat-collect]').append(get('[data-wechat-preview]'));
  form('[data-wechat-job-filters]', ['job_q', 'job_status']);
  get('[data-wechat-confirm]').append(new Node('input'));
  const tabs = ['sync', 'collect', 'config'].map(name => { const n = new Node('button'); n.dataset.wechatTab = name; return n; });
  const panels = ['sync', 'collect', 'config'].map(name => { const n = get(`[data-wechat-panel="${name}"]`); n.dataset.wechatPanel = name; return n; });
  const root = new Node(); root.dataset = { readonly: 'false', csrf: 'test-csrf', userId:'42' }; root.querySelector = get;
  root.querySelectorAll = selector => selector === '[data-wechat-tab]' ? tabs : selector === '[data-wechat-panel]' ? panels : [];
  const docEvents = {}, winEvents = {}, document = { hidden: false, activeElement: null, body:new Node('body'), querySelector: () => root, createElement: tag => new Node(tag), createDocumentFragment: () => new Node(), addEventListener: (name, fn) => { docEvents[name] = fn; }, removeEventListener(name, fn) { if (docEvents[name] === fn) delete docEvents[name]; } };
  const stored = options.stored || new Map(), storageState = { fail: false };
  const window = { addEventListener: (name, fn) => { winEvents[name] = fn; }, crypto: require('node:crypto').webcrypto, sessionStorage: {
    getItem: key => { if (storageState.fail) throw new Error('暂存不可用'); return stored.get(key) ?? null; },
    setItem: (key, value) => { if (storageState.fail) throw new Error('暂存不可用'); stored.set(key, value); },
    removeItem: key => { if (storageState.fail) throw new Error('暂存不可用'); stored.delete(key); },
  } };
  vm.runInNewContext(draftSource, { window, Date, TextEncoder, Uint8Array });
  vm.runInNewContext(source, { document, window, location: { search }, URL, URLSearchParams, AbortController, DOMException, Date, setTimeout(fn, ms) { timers.set(++timerID, { fn, ms }); return timerID; }, clearTimeout(id) { timers.delete(id); }, fetch(url, options) {
    if(url==='/api/v1/admin/session'){
      sessionCalls.push(options);
      if (!session.pending) return Promise.resolve({status:session.status,ok:session.status===200,json:async()=>session.payload});
      return new Promise((resolve, reject) => {
        session.respond = () => resolve({status:session.status,ok:session.status===200,json:async()=>session.payload});
        options.signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), {once:true});
      });
    }
    return new Promise((resolve,reject) => {
      options.signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), {once:true});
      calls.push({ url, options, body: options.body && JSON.parse(options.body), reject, respond(data, status = 200) { resolve({ status, ok: status >= 200 && status < 300, json: async () => data }); } });
    }); } });
  const emit = (selector, event, data = {}) => get(selector).events[event]?.({ preventDefault() {}, ...data });
  return { get, calls, timers, emit, tabs, docEvents, winEvents, session, sessionCalls, root, stored, storageState, window, document };
}
const workspace = overrides => ({ user_id:42, config: { app_id: 'wx0123456789abcdef', author:'',thumb_media_id:'',version: 2, enabled: true, collect_enabled: true,auto_sync:false,auto_publish:false,include_source:true,open_comment:false,fans_comment:false }, can_sync: true, can_collect: true, jobs: [], posts: [], job_page: 1, job_pages: 1, total_jobs: 0, filtered_jobs: 0, active_jobs: 0, attention_jobs: 0, ...overrides });
const preview = () => ({ articles: ['one', 'two'].map(key => ({ key, title: key, author: '公众号', excerpt:'', body:'<p>预览</p>', cover:'', images: [], warnings: [], url: 'https://mp.weixin.qq.com/s/' + key, preview: '<p>预览</p>' })), receipts: { one: 'receipt-one', two: 'receipt-two' }, expires: new Date(Date.now() + 600000).toISOString(), failures: [], plan: { new: 2, existing: 0, conflicts: 0, items: ['one', 'two'].map(key => ({ key, action: 'create' })) } });
async function ready(search) { const f = fixture(search); f.calls[0].respond(workspace()); await tick(); return f; }
const recoveryKey = 'newpress.wechat.collect.42';

const selectionPosts = [1, 2, 3].map(id => ({id, version: 1, title: `文章 ${id}`, status: 'draft'}));
async function selectionFixture() {
  const f = fixture();
  f.calls[0].respond(workspace({posts: selectionPosts, active_jobs: 1})); await tick();
  f.get('[data-wechat-sync]').elements.post_id.value = '1';
  return f;
}
const selectedWorkspace = (id, version = 1, overrides = {}) => workspace({posts: selectionPosts, selected_post: id ? {...selectionPosts.find(post => post.id === id), version} : null, ...overrides});

test('sync selection: a late refresh rechecks the current article before replacing options', async () => {
  const f = await selectionFixture(), select = f.get('[data-wechat-sync]').elements.post_id;
  f.emit('[data-wechat-refresh]', 'click'); const pending = f.calls.at(-1);
  select.value = '2'; pending.respond(selectedWorkspace(1)); await tick();
  assert.equal(select.value, '2'); assert.notEqual(f.calls.at(-1), pending);
  assert.equal(new URL(f.calls.at(-1).url, 'https://example.test').searchParams.get('post'), '2');
  f.calls.at(-1).respond(selectedWorkspace(2, 5, {posts: selectionPosts.filter(post => post.id !== 2)})); await tick();
  assert.equal(select.value, '2');
  f.emit('[data-wechat-sync]', 'submit'); await tick();
  assert.deepEqual(f.calls.at(-1).body, {post_id: 2, version: 5, config_version: 2});
});

test('sync selection: clearing the selection is not undone by a late refresh', async () => {
  const f = await selectionFixture(), select = f.get('[data-wechat-sync]').elements.post_id;
  f.emit('[data-wechat-refresh]', 'click'); const pending = f.calls.at(-1);
  select.value = ''; pending.respond(selectedWorkspace(1)); await tick();
  assert.equal(select.value, ''); assert.notEqual(f.calls.at(-1), pending);
  assert.equal(new URL(f.calls.at(-1).url, 'https://example.test').searchParams.has('post'), false);
  f.calls.at(-1).respond(selectedWorkspace(null)); await tick();
  assert.equal(select.value, ''); assert.equal(f.get('[data-wechat-send]').disabled, true);
});

test('sync selection: polling an invalid old article does not clear a newly selected article', async () => {
  const f = await selectionFixture(), select = f.get('[data-wechat-sync]').elements.post_id;
  [...f.timers.values()].find(timer => timer.ms === 6000).fn(); const pending = f.calls.at(-1);
  select.value = '2'; pending.respond(selectedWorkspace(null, 1, {posts: selectionPosts.slice(1)})); await tick();
  assert.equal(select.value, '2'); assert.notEqual(f.calls.at(-1), pending);
  f.calls.at(-1).respond(selectedWorkspace(2, 6)); await tick();
  assert.equal(select.value, '2');
  f.emit('[data-wechat-sync]', 'submit'); await tick();
  assert.deepEqual(f.calls.at(-1).body, {post_id: 2, version: 6, config_version: 2});
});

test('sync selection: repeated changes resolve to the final article without write requests', async () => {
  const f = await selectionFixture(), select = f.get('[data-wechat-sync]').elements.post_id;
  f.emit('[data-wechat-refresh]', 'click'); const first = f.calls.at(-1);
  select.value = '2'; first.respond(selectedWorkspace(1)); await tick(); const second = f.calls.at(-1);
  assert.notEqual(first, second); select.value = '3'; second.respond(selectedWorkspace(2)); await tick();
  assert.equal(select.value, '3'); assert.notEqual(f.calls.at(-1), second);
  assert.equal(new URL(f.calls.at(-1).url, 'https://example.test').searchParams.get('post'), '3');
  f.calls.at(-1).respond(selectedWorkspace(3)); await tick();
  assert.equal(select.value, '3'); assert.ok(f.calls.every(call => !call.body));
});

test('sync selection: the fresh read still clears an article that is actually unavailable', async () => {
  const f = await selectionFixture(), select = f.get('[data-wechat-sync]').elements.post_id;
  f.emit('[data-wechat-refresh]', 'click'); const pending = f.calls.at(-1);
  select.value = '2'; pending.respond(selectedWorkspace(1)); await tick();
  assert.notEqual(f.calls.at(-1), pending);
  f.calls.at(-1).respond(selectedWorkspace(null, 1, {posts: selectionPosts.filter(post => post.id !== 2)})); await tick();
  assert.equal(select.value, ''); assert.equal(f.get('[data-wechat-send]').disabled, true);
  assert.match(f.get('[data-wechat-post-note]').textContent, /不存在/);
});

test('sync selection: a failed follow-up preserves the chosen article and can be retried', async () => {
  const f = await selectionFixture(), select = f.get('[data-wechat-sync]').elements.post_id;
  f.emit('[data-wechat-refresh]', 'click'); const pending = f.calls.at(-1);
  select.value = '2'; pending.respond(selectedWorkspace(1)); await tick();
  assert.notEqual(f.calls.at(-1), pending);
  f.calls.at(-1).respond({error: {message: '读取失败，请重试'}}, 503); await tick();
  assert.equal(select.value, '2'); assert.match(f.get('[data-wechat-status]').textContent, /读取失败/);
  const count = f.calls.length; f.emit('[data-wechat-refresh]', 'click');
  assert.equal(f.calls.length, count + 1); f.calls.at(-1).respond(selectedWorkspace(2)); await tick();
  assert.equal(select.value, '2'); assert.ok(f.calls.every(call => !call.body));
});

test('sync selection: current-account checks take precedence over selection refreshes', async () => {
  const f = await selectionFixture(), select = f.get('[data-wechat-sync]').elements.post_id;
  f.emit('[data-wechat-refresh]', 'click'); const count = f.calls.length;
  select.value = '2'; f.calls.at(-1).respond(selectedWorkspace(1, 1, {user_id: 99})); await tick();
  assert.equal(f.calls.length, count); assert.equal(select.value, '2');
  assert.equal(f.get('[data-wechat-sync]').fieldset.disabled, true);
  assert.match(f.get('[data-wechat-status]').textContent, /账号已切换/);
});

test('sync selection: follow-up reads still apply disabled modules and revoked sync access', async () => {
  for (const mode of ['disabled', 'revoked']) {
    const f = await selectionFixture(), select = f.get('[data-wechat-sync]').elements.post_id;
    f.emit('[data-wechat-refresh]', 'click'); const pending = f.calls.at(-1);
    select.value = '2'; pending.respond(selectedWorkspace(1)); await tick();
    assert.notEqual(f.calls.at(-1), pending);
    f.calls.at(-1).respond(selectedWorkspace(2, 1, {config: {...workspace().config, enabled: mode !== 'disabled'}, can_sync: mode !== 'revoked'})); await tick();
    assert.equal(select.value, '2'); assert.equal(f.get('[data-wechat-send]').disabled, true);
    assert.ok(f.calls.every(call => !call.body));
  }
});

test('sync selection: departure aborts pending refresh without starting another request', async () => {
  const f = await selectionFixture(), select = f.get('[data-wechat-sync]').elements.post_id;
  f.emit('[data-wechat-refresh]', 'click'); const pending = f.calls.at(-1), count = f.calls.length;
  select.value = '2'; f.winEvents.pagehide(); pending.respond(selectedWorkspace(1)); await tick();
  assert.equal(f.calls.length, count); assert.equal(select.value, '2'); assert.equal(pending.options.signal.aborted, true);
});

test('sync selection: returning to the requested article avoids redundant reads', async () => {
  const f = await selectionFixture(), select = f.get('[data-wechat-sync]').elements.post_id;
  f.emit('[data-wechat-refresh]', 'click'); const count = f.calls.length;
  select.value = '2'; select.value = '1'; f.calls.at(-1).respond(selectedWorkspace(1)); await tick();
  assert.equal(f.calls.length, count); assert.equal(select.value, '1');
});

test('sync selection: explicit new searches supersede an automatic recheck', async () => {
  const f = await selectionFixture(), form = f.get('[data-wechat-sync]'), select = form.elements.post_id;
  f.emit('[data-wechat-refresh]', 'click'); const first = f.calls.at(-1);
  select.value = '2'; first.respond(selectedWorkspace(1)); await tick(); const second = f.calls.at(-1);
  assert.notEqual(first, second);
  select.value = '3'; form.elements.q.value = '文章 3'; f.emit('[data-wechat-search]', 'click'); const third = f.calls.at(-1);
  assert.equal(second.options.signal.aborted, true); assert.notEqual(second, third);
  second.respond(selectedWorkspace(2)); third.respond(selectedWorkspace(3)); await tick();
  assert.equal(select.value, '3'); assert.equal(f.calls.at(-1), third);
});

test('sync selection: rechecks keep dirty settings and refresh the selected article version', async () => {
  const f = await selectionFixture(), config = f.get('[data-wechat-config]'), select = f.get('[data-wechat-sync]').elements.post_id;
  config.elements.author.value = '尚未保存的作者'; f.emit('[data-wechat-config]', 'input');
  f.emit('[data-wechat-refresh]', 'click'); const pending = f.calls.at(-1);
  select.value = '2'; pending.respond(selectedWorkspace(1, 1, {config: {...workspace().config, version: 3}})); await tick();
  assert.notEqual(f.calls.at(-1), pending);
  f.calls.at(-1).respond(selectedWorkspace(2, 8, {posts: [{...selectionPosts[1], version: 8}]})); await tick();
  assert.equal(select.value, '2'); assert.equal(config.elements.author.value, '尚未保存的作者');
  f.emit('[data-wechat-config]', 'submit'); await tick();
  assert.equal(f.calls.at(-1).body.config.version, 2); assert.equal(f.calls.at(-1).body.config.author, '尚未保存的作者');
});

const failedPreview = () => ({...preview(), failures:[{url:'https://mp.weixin.qq.com/s/failed', message:'读取超时'}]});
const retriedPreview = () => {
  const data = preview();
  return {...data, articles:[{...data.articles[0], key:'three', title:'重试文章', url:'https://mp.weixin.qq.com/s/failed'}], receipts:{three:'receipt-three'}, plan:{new:1, existing:0, conflicts:0, items:[{key:'three', action:'create'}]}};
};
const importAck = keys => ({applied:true,plan:{new:keys.length,existing:0,conflicts:0,items:keys.map((key,index)=>({key,title:key,post_id:31+index,action:'created'}))}});
async function startFocusedPreview(f, trigger = f.get('[data-wechat-preview]')) {
  f.get('[data-wechat-collect]').elements.urls.value = 'https://mp.weixin.qq.com/s/firstx';
  trigger.focus(); f.emit('[data-wechat-collect]', 'submit'); f.document.activeElement = f.document.body; await tick();
}
async function startFocusedRetry(f) {
  await collectImageBatch(f, failedPreview());
  const trigger = f.get('[data-wechat-collection]').querySelectorAll('[data-wechat-retry]')[0];
  trigger.focus(); trigger.events.click(); f.document.activeElement = f.document.body; await tick(); return trigger;
}
async function startFocusedImport(f) {
  f.get('[data-wechat-apply]').focus(); f.emit('[data-wechat-apply]', 'click'); f.document.activeElement = f.document.body; await tick();
}

test('collection focus: preview completion restores its disabled trigger without scrolling', async () => {
  const f = await ready(); await startFocusedPreview(f);
  f.calls.at(-1).respond(preview()); await tick();
  assert.equal(f.document.activeElement, f.get('[data-wechat-preview]'));
  assert.equal(f.document.activeElement.focusOptions.preventScroll, true);
  assert.equal(f.docEvents.focusin, undefined); assert.equal(f.docEvents.pointerdown, undefined);
});

test('collection focus: preview network failure restores the original editable input', async () => {
  const f = await ready(), field = f.get('[data-wechat-collect]').elements.urls;
  await startFocusedPreview(f, field); f.calls.at(-1).reject(new Error('读取中断')); await tick();
  assert.equal(f.document.activeElement, field); assert.equal(field.value, 'https://mp.weixin.qq.com/s/firstx');
  assert.equal(f.docEvents.focusin, undefined);
});

test('collection focus: successful retry moves to the returned article summary', async () => {
  const f = await ready(); await startFocusedRetry(f);
  f.calls.at(-1).respond(retriedPreview()); await tick();
  assert.equal(f.document.activeElement.tagName, 'summary');
  assert.equal(f.document.activeElement.textContent, '重试文章');
  assert.equal(f.document.activeElement.parent.dataset.previewKey, 'three');
  assert.equal(f.document.activeElement.focusOptions.preventScroll, false);
});

test('collection focus: another failed read focuses the replacement retry button', async () => {
  const f = await ready(), original = await startFocusedRetry(f);
  f.calls.at(-1).respond({articles:[],failures:[{url:'https://mp.weixin.qq.com/s/failed',message:'仍未读取到'}]}); await tick();
  const replacement = f.get('[data-wechat-collection]').querySelectorAll('[data-wechat-retry]')[0];
  assert.notEqual(replacement, original); assert.equal(f.document.activeElement, replacement);
  assert.equal(original.isConnected, false); assert.equal(replacement.disabled, false);
});

test('collection focus: retry request errors preserve the original retry button', async () => {
  const f = await ready(), original = await startFocusedRetry(f);
  f.calls.at(-1).reject(new Error('网络中断')); await tick();
  assert.equal(f.document.activeElement, original); assert.equal(original.disabled, false);
});

test('collection focus: partial import continues at the first remaining unchecked article', async () => {
  const f = await ready(); await collect(f);
  const checks = f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]'); checks[1].checked=false; checks[1].events.change();
  await startFocusedImport(f); f.calls.at(-1).respond(importAck(['one'])); await tick();
  assert.equal(f.document.activeElement, f.document.body, 'wait until workspace refresh unlocks the remaining choices');
  f.calls.at(-1).respond(workspace()); await tick();
  assert.equal(f.document.activeElement.dataset.key, 'two'); assert.equal(f.document.activeElement.checked, false); assert.equal(f.document.activeElement.disabled, false);
});

test('collection focus: a completed import moves to its new edit link', async () => {
  const f = await ready(); await collect(f); await startFocusedImport(f);
  f.calls.at(-1).respond(importAck(['one','two'])); await tick(); f.calls.at(-1).respond(workspace()); await tick();
  assert.equal(f.document.activeElement, f.get('[data-wechat-import-results]').querySelectorAll('a')[0]);
  assert.equal(f.document.activeElement.href, '/admin/content/31/edit/');
});

test('collection focus: malformed import receipt keeps selection and returns to confirmation', async () => {
  const f = await ready(); await collect(f); await startFocusedImport(f);
  f.calls.at(-1).respond({applied:true,plan:{items:[]}}); await tick();
  assert.equal(f.document.activeElement, f.get('[data-wechat-apply]'));
  assert.deepEqual(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').map(node=>node.checked), [true,true]);
});

test('collection focus: successful response does not take focus back from another control', async () => {
  const f = await ready(); await startFocusedPreview(f);
  const elsewhere = f.get('[data-wechat-tab=sync]'); elsewhere.focus();
  f.calls.at(-1).respond(preview()); await tick();
  assert.equal(f.document.activeElement, elsewhere);
});

test('collection focus: a deliberate click on nonfocusable content cancels return focus', async () => {
  const f = await ready(); await startFocusedPreview(f);
  f.docEvents.pointerdown?.({type:'pointerdown',target:f.document.body});
  f.calls.at(-1).respond(preview()); await tick();
  assert.equal(f.document.activeElement, f.document.body);
});

test('collection focus: moving elsewhere then blurring does not restore the old trigger', async () => {
  const f = await ready(); await startFocusedPreview(f);
  f.get('[data-wechat-status]').focus(); f.document.activeElement=f.document.body;
  f.calls.at(-1).respond(preview()); await tick();
  assert.equal(f.document.activeElement, f.document.body);
});

test('collection focus: canceled retry still returns from the stop button', async () => {
  const f = await ready(), retry = await startFocusedRetry(f);
  f.get('[data-wechat-stop-read]').focus(); f.emit('[data-wechat-stop-read]','click'); await tick();
  assert.equal(f.document.activeElement, retry); assert.equal(retry.disabled, false);
  assert.equal(f.docEvents.focusin, undefined); assert.equal(f.docEvents.pointerdown, undefined);
});

test('collection focus: page departure clears temporary listeners and does not focus late results', async () => {
  const f = await ready(); await startFocusedPreview(f);
  f.winEvents.pagehide(); await tick();
  assert.equal(f.document.activeElement, f.document.body);
  assert.equal(f.docEvents.focusin, undefined); assert.equal(f.docEvents.pointerdown, undefined);
});

test('collection focus: programmatic preview does not move an unrelated focus', async () => {
  const f = await ready(); f.get('[data-wechat-status]').focus(); const original = f.document.activeElement;
  await collect(f);
  assert.equal(f.document.activeElement, original);
});

test('collection focus: retry refresh keeps the existing article the reader focused while waiting', async () => {
  const f = await ready(); await startFocusedRetry(f);
  const old = f.get('[data-wechat-collection]').querySelectorAll('summary')[0]; old.focus();
  f.calls.at(-1).respond(retriedPreview()); await tick();
  assert.notEqual(f.document.activeElement, old); assert.equal(old.isConnected, false);
  assert.equal(f.document.activeElement.tagName, 'summary');
  assert.equal(f.document.activeElement.parent.dataset.previewKey, 'one');
});

test('collection focus: retry refresh keeps a focused original-article link', async () => {
  const f = await ready(); await startFocusedRetry(f);
  const first = f.get('[data-wechat-collection]').querySelectorAll('details')[0]; first.open = true;
  const old = first.querySelector('a'); old.focus();
  f.calls.at(-1).respond(retriedPreview()); await tick();
  assert.notEqual(f.document.activeElement, old); assert.equal(f.document.activeElement.href, old.href);
  assert.equal(f.document.activeElement.parent.dataset.previewKey, 'one');
});

test('collection focus: switching the active tab prevents return into a hidden collection', async () => {
  const f = await ready(); f.tabs[1].events.click(); await startFocusedPreview(f);
  f.tabs[2].events.click(); f.document.activeElement = f.document.body;
  f.calls.at(-1).respond(preview()); await tick();
  assert.equal(f.document.activeElement, f.document.body);
});

const pastedLinkCases = [
  ['share text and same-line links', '阅读笔记：https://mp.weixin.qq.com/s/firstx 第二篇 https://mp.weixin.qq.com/s/second', ['https://mp.weixin.qq.com/s/firstx', 'https://mp.weixin.qq.com/s/second']],
  ['markdown and Chinese punctuation', '【收藏】[第一篇](https://mp.weixin.qq.com/s/firstx)。\n“https://mp.weixin.qq.com/s/second”，请查收', ['https://mp.weixin.qq.com/s/firstx', 'https://mp.weixin.qq.com/s/second']],
  ['short-link sharing variants', 'http://mp.weixin.qq.com/s/firstx?scene=1#wechat_redirect https://mp.weixin.qq.com:443/s/firstx?from=groupmessage', ['https://mp.weixin.qq.com/s/firstx']],
  ['long-link HTML separators', '原文 https://mp.weixin.qq.com/s?__biz=MzA%3D%3D&amp;mid=123&#38;idx=1&#x26;sn=abcdef&scene=1', ['https://mp.weixin.qq.com/s?__biz=MzA%3D%3D&idx=1&mid=123&sn=abcdef']],
  ['long-link identity variants', 'https://mp.weixin.qq.com/s/?sn=abc&idx=1&mid=123&__biz=MzA%3D%3D#rd\nhttps://mp.weixin.qq.com/s?__biz=MzA%3D%3D&mid=123&idx=1&sn=abc&scene=2', ['https://mp.weixin.qq.com/s?__biz=MzA%3D%3D&idx=1&mid=123&sn=abc']],
  ['distinct long-link article IDs', 'https://mp.weixin.qq.com/s?__biz=abc&mid=123&idx=1&sn=abc https://mp.weixin.qq.com/s?__biz=abc&mid=123&idx=2&sn=abc', ['https://mp.weixin.qq.com/s?__biz=abc&idx=1&mid=123&sn=abc', 'https://mp.weixin.qq.com/s?__biz=abc&idx=2&mid=123&sn=abc']],
  ['links separated by full-width punctuation', 'https://mp.weixin.qq.com/s/firstx；https://mp.weixin.qq.com/s/second，https://mp.weixin.qq.com/s/thirdx。', ['https://mp.weixin.qq.com/s/firstx', 'https://mp.weixin.qq.com/s/second', 'https://mp.weixin.qq.com/s/thirdx']],
];
for (const [name, input, urls] of pastedLinkCases) test('collection pasted input: ' + name, async () => {
  const f = await ready(), field = f.get('[data-wechat-collect]').elements.urls;
  field.value = input; field.events.input();
  f.emit('[data-wechat-collect]', 'submit'); await tick();
  assert.equal(f.calls.at(-1).url, '/api/v1/admin/wechat/collect/preview');
  assert.deepEqual(f.calls.at(-1).body.urls, urls);
  assert.equal(f.get('[data-wechat-input-count]').textContent, `${urls.length} / 10`);
  assert.equal(field.value, input, 'original pasted text stays editable');
});

test('collection pasted input: sharing variants count only once toward the batch limit', async () => {
  const f = await ready(), field = f.get('[data-wechat-collect]').elements.urls;
  field.value = Array.from({length:12}, (_, i) => `https://mp.weixin.qq.com/s/firstx?scene=${i}`).join('\n');
  field.events.input();
  assert.equal(f.get('[data-wechat-input-count]').textContent, '1 / 10');
  assert.match(f.get('[data-wechat-input-note]').textContent, /合并 11/);
  f.emit('[data-wechat-collect]', 'submit'); await tick();
  assert.deepEqual(f.calls.at(-1).body.urls, ['https://mp.weixin.qq.com/s/firstx']);
});

test('collection pasted input: over-limit input is explained and retained without collection', async () => {
  const f = await ready(), field = f.get('[data-wechat-collect]').elements.urls;
  const input = Array.from({length:11}, (_, i) => `https://mp.weixin.qq.com/s/article${i}`).join(' ');
  field.value = input; field.events.input();
  assert.equal(f.get('[data-wechat-preview]').disabled, true);
  assert.equal(field.attrs['aria-invalid'], 'true');
  assert.match(f.get('[data-wechat-input-note]').textContent, /减少 1/);
  f.emit('[data-wechat-collect]', 'submit'); await tick();
  assert.equal(f.calls.length, 1);
  assert.equal(field.value, input);
  field.value = input.slice(0, input.lastIndexOf(' ')); field.events.input();
  assert.equal(f.get('[data-wechat-preview]').disabled, false);
  assert.equal(field.attrs['aria-invalid'], 'false');
});

for (const invalid of [
  'https://example.test/?next=https://mp.weixin.qq.com/s/firstx',
  'https://mp.weixin.qq.com.evil.test/s/firstx',
  'https://user@mp.weixin.qq.com/s/firstx',
  'https://mp.weixin.qq.com:8443/s/firstx',
  'https://mp.weixin.qq.com/s?__biz=abc&mid=1&mid=2&idx=1&sn=abc',
  'https://mp.weixin.qq.com/s?__biz=abc&mid=1&idx=1',
]) test('collection pasted input: keeps unsupported address for per-link failure ' + invalid, async () => {
  const f = await ready(), field = f.get('[data-wechat-collect]').elements.urls;
  field.value = `待检查：${invalid}\nhttps://mp.weixin.qq.com/s/second`; field.events.input();
  assert.match(f.get('[data-wechat-input-note]').textContent, /1 项地址需核对/);
  f.emit('[data-wechat-collect]', 'submit'); await tick();
  assert.deepEqual(f.calls.at(-1).body.urls, [invalid, 'https://mp.weixin.qq.com/s/second']);
});

test('collection pasted input: sharing-only edits preserve selection and new links invalidate it', async () => {
  const f = await ready(); await collect(f);
  const field = f.get('[data-wechat-collect]').elements.urls, checks = f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]');
  checks[1].checked = false; checks[1].events.change();
  const expiry = f.get('[data-wechat-apply-note]').textContent;
  field.value = '分享 https://mp.weixin.qq.com/s/firstx?scene=2#rd\nhttps://mp.weixin.qq.com/s/firstx'; field.events.input();
  assert.equal(f.get('[data-wechat-apply]').disabled, false);
  assert.deepEqual(checks.map(input => input.checked), [true, false]);
  assert.equal(f.get('[data-wechat-apply-note]').textContent, expiry);
  field.value += ' https://example.test/new'; field.events.input();
  assert.equal(f.get('[data-wechat-apply]').disabled, true);
  assert.match(f.get('[data-wechat-preview-state]').textContent, /链接已修改/);
});

test('collection pasted input: a different stored baseline cannot restore import choices', async () => {
  const f = await recoveryReady(), data = preview();
  const input = 'https://mp.weixin.qq.com/s/firstx?scene=7';
  f.window.ChunnelWeChatDrafts.create(42).save(input, input, [{key:'one',digest:(await f.window.ChunnelWeChatDrafts.fingerprints(data.articles)).get('one'),selected:true}], false);
  const next = await recoveryReady(f.stored); await restoreCollection(next); await recoveryPreview(next, data);
  assert.deepEqual(next.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').map(input => input.checked), [false, false]);
  assert.equal(next.get('[data-wechat-input-count]').textContent, '1 / 10');
  assert.equal(next.get('[data-wechat-collect]').elements.urls.value, input);
});

test('collection pasted input: draft recovery retains sharing text and deduplicated choice baseline', async () => {
  const f = await recoveryReady(), field = f.get('[data-wechat-collect]').elements.urls;
  const input = '笔记 https://mp.weixin.qq.com/s/firstx?scene=1 https://mp.weixin.qq.com/s/firstx#rd';
  field.value = input; field.events.input(); await enableRecovery(f); await recoveryPreview(f);
  const saved = JSON.parse(f.stored.get(recoveryKey));
  assert.equal(saved.input, input);
  assert.equal(saved.baseline, 'https://mp.weixin.qq.com/s/firstx');
  assert.match(f.get('[data-wechat-draft-summary]').textContent, /已暂存 1 个链接/);
  const next = await recoveryReady(f.stored); await restoreCollection(next);
  assert.equal(next.get('[data-wechat-collect]').elements.urls.value, input);
  assert.equal(next.calls.some(call => call.url.endsWith('/collect/preview')), false);
});

test('collection pasted input: sharing-only edits during preview do not reject the same articles', async () => {
  const f = await ready(), field = f.get('[data-wechat-collect]').elements.urls;
  field.value = 'https://mp.weixin.qq.com/s/firstx';
  f.emit('[data-wechat-collect]', 'submit'); await tick();
  field.value = '阅读 https://mp.weixin.qq.com/s/firstx?scene=3'; field.events.input();
  f.calls.at(-1).respond(preview()); await tick();
  assert.match(f.get('[data-wechat-status]').textContent, /预览已生成/);
  assert.equal(f.get('[data-wechat-apply]').disabled, false);
  assert.equal(field.value, '阅读 https://mp.weixin.qq.com/s/firstx?scene=3');
});

test('collection pasted input: empty input resets feedback and does not send a collection request', async () => {
  const f = await ready(), field = f.get('[data-wechat-collect]').elements.urls;
  field.value = 'https://mp.weixin.qq.com/s/firstx'; field.events.input();
  field.value = '\n  '; field.events.input();
  assert.equal(f.get('[data-wechat-input-count]').textContent, '0 / 10');
  assert.equal(f.get('[data-wechat-input-state]').dataset.state, 'empty');
  f.emit('[data-wechat-collect]', 'submit'); await tick();
  assert.equal(f.calls.length, 1);
});

test('collection pasted input: editing share text does not repeat unchanged live announcements', async () => {
  const f = await ready(), field = f.get('[data-wechat-collect]').elements.urls;
  field.value = 'https://mp.weixin.qq.com/s/firstx\n'; field.events.input();
  const announcements = [];
  for (const selector of ['[data-wechat-input-count]', '[data-wechat-input-note]']) {
    const node = f.get(selector); let content = node.textContent;
    Object.defineProperty(node, 'textContent', {get:() => content, set:value => {content = value; announcements.push(value);}});
  }
  field.value += '阅读笔记'; field.events.input();
  assert.deepEqual(announcements, []);
  field.value += '\nhttps://mp.weixin.qq.com/s/second'; field.events.input();
  assert.deepEqual(announcements, ['2 / 10']);
});
const recoveryWorkspace = () => workspace({ config: {...workspace().config, collect_drafts:true} });
async function recoveryReady(stored = new Map()) { const f=fixture('',{stored});f.calls[0].respond(recoveryWorkspace());await tick();return f; }
async function settleRecovery(f) { const deadline=Date.now()+2000;while(f.root.attrs['aria-busy']==='true'&&Date.now()<deadline)await new Promise(resolve=>setTimeout(resolve,1));assert.equal(f.root.attrs['aria-busy'],'false','collection operation did not settle'); }
function flushRecovery(f) { for(const [id,timer] of [...f.timers])if(timer.ms===500){f.timers.delete(id);timer.fn();} }
async function enableRecovery(f) { const input=f.get('[data-wechat-draft-enable]');input.checked=true;await f.emit('[data-wechat-draft-enable]','change'); }
async function recoveryPreview(f,data=preview()) { f.emit('[data-wechat-collect]','submit');await tick();f.calls.at(-1).respond(data);await settleRecovery(f);flushRecovery(f); }
async function savedCollection() {
  const f=await recoveryReady();await enableRecovery(f);
  f.get('[data-wechat-collect]').elements.urls.value='https://mp.weixin.qq.com/s/firstx';
  await recoveryPreview(f);const checks=f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]');checks[1].checked=false;checks[1].events.change();flushRecovery(f);
  return f;
}
async function restoreCollection(f, latest=recoveryWorkspace()) {
  f.get('[data-wechat-draft-restore]').focus();f.emit('[data-wechat-draft-restore]','click');await tick();f.calls.at(-1).respond(latest);await settleRecovery(f);
}

test('collection recovery requires both the server option and explicit per-tab opt-in',async()=>{
  const f=await recoveryReady();f.get('[data-wechat-collect]').elements.urls.value='https://mp.weixin.qq.com/s/firstx';
  f.get('[data-wechat-collect]').elements.urls.events.input();flushRecovery(f);assert.equal(f.stored.size,0);
  await enableRecovery(f);assert.equal(JSON.parse(f.stored.get(recoveryKey)).input,'https://mp.weixin.qq.com/s/firstx');
  f.get('[data-wechat-config]').elements.app_secret.value='never-save-this-secret';f.emit('[data-wechat-config]','input');f.winEvents.pagehide();
  assert.doesNotMatch(f.stored.get(recoveryKey),/never-save|app_secret|csrf|receipt|<p>/);
});
test('reloading offers manual collection restoration and revalidates unchanged selection with fresh receipts',async()=>{
  const first=await savedCollection(),stored=JSON.parse(first.stored.get(recoveryKey));assert.equal(stored.choices.length,2);
  const f=await recoveryReady(first.stored);assert.equal(f.get('[data-wechat-collect]').elements.urls.value,'');assert.equal(f.calls.length,1);
  assert.equal(f.get('[data-wechat-draft-restore]').hidden,false);await restoreCollection(f);
  assert.equal(f.get('[data-wechat-collect]').elements.urls.value,stored.input);assert.equal(f.calls.length,2);assert.equal(f.get('[data-wechat-apply]').disabled,true);
  assert.equal(f.document.activeElement,f.get('[data-wechat-collect]').elements.urls);
  const data=preview();data.receipts={one:'fresh-one',two:'fresh-two'};await recoveryPreview(f,data);
  assert.deepEqual(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').map(c=>c.checked),[true,false]);
  f.emit('[data-wechat-apply]','click');await tick();assert.deepEqual(f.calls.at(-1).body.receipts,{one:'fresh-one'});
});
test('restored changed and newly discovered articles remain unchecked for review',async()=>{
  const original=await savedCollection(),f=await recoveryReady(original.stored);await restoreCollection(f);
  const data=preview();data.articles[0].excerpt='摘要改变';data.articles.push({...data.articles[1],key:'new',url:'https://mp.weixin.qq.com/s/newarticle',title:'新增'});data.receipts.new='fresh-new';data.plan.items.push({key:'new',action:'create'});data.plan.new++;
  await recoveryPreview(f,data);assert.deepEqual(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').map(c=>c.checked),[false,false,false]);
  assert.match(f.get('[data-wechat-preview-state]').textContent,/2 篇/);assert.equal(f.get('[data-wechat-apply]').disabled,true);
});
test('restoring then refreshing before a new preview retains the original selection fingerprints',async()=>{
  const original=await savedCollection(),f=await recoveryReady(original.stored);await restoreCollection(f);f.winEvents.pagehide();
  const record=JSON.parse(f.stored.get(recoveryKey));assert.equal(record.choices.length,2);assert.equal(record.choices[0].selected,true);assert.equal(record.baseline,'https://mp.weixin.qq.com/s/firstx');
  const second=await recoveryReady(f.stored);await restoreCollection(second);await recoveryPreview(second);
  assert.deepEqual(second.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').map(c=>c.checked),[true,false]);
});
test('partially failed restoration keeps an unchecked article unchecked when its link later succeeds',async()=>{
  const original=await savedCollection(),f=await recoveryReady(original.stored);await restoreCollection(f);
  const data=preview();data.articles=data.articles.slice(0,1);data.plan.items=data.plan.items.slice(0,1);data.plan.new=1;delete data.receipts.two;data.failures=[{url:'https://mp.weixin.qq.com/s/retrytwo',message:'暂时失败'}];
  await recoveryPreview(f,data);
  assert.equal(JSON.parse(f.stored.get(recoveryKey)).choices.length,2);
  const retry=f.get('[data-wechat-collection]').querySelectorAll('[data-wechat-retry]')[0];retry.events.click();await tick();
  const next=preview();next.articles=next.articles.slice(1);next.plan.items=next.plan.items.slice(1);next.plan.new=1;delete next.receipts.one;
  f.calls.at(-1).respond(next);await settleRecovery(f);
  assert.deepEqual(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').map(c=>c.checked),[true,false]);
});
test('uncertain imports are persisted before sending and recovery uses the current import plan',async()=>{
  const original=await savedCollection();original.emit('[data-wechat-apply]','click');await tick();
  assert.equal(JSON.parse(original.stored.get(recoveryKey)).uncertain,true);assert.ok(original.calls.at(-1).url.endsWith('/collect/apply'));
  original.winEvents.pagehide();const f=await recoveryReady(original.stored);await restoreCollection(f);assert.match(f.get('[data-wechat-status]').textContent,/上次导入结果/);
  const data=preview();data.plan.items[0]={key:'one',action:'skip',post_id:90};delete data.receipts.one;data.plan.new=1;data.plan.existing=1;
  await recoveryPreview(f,data);assert.equal(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').length,1);assert.equal(f.get('[data-wechat-apply]').disabled,true);
  assert.equal(f.calls.some(call=>call.url.endsWith('/collect/apply')),false);
});
test('stale local drafts cannot cross accounts or restore after server disablement',async()=>{
  const original=await savedCollection(),f=await recoveryReady(new Map(original.stored));f.session.payload.user_id=43;f.emit('[data-wechat-draft-restore]','click');await tick();
  assert.equal(f.calls.length,1);assert.equal(f.get('[data-wechat-collect]').elements.urls.value,'');assert.equal(f.stored.has(recoveryKey),true);
  const g=await recoveryReady(new Map(original.stored));await restoreCollection(g,workspace());assert.equal(g.get('[data-wechat-collect]').elements.urls.value,'');assert.equal(g.stored.has(recoveryKey),false);
  assert.equal(g.get('[data-wechat-collection-draft]').hidden,true);
});
test('a lost collection permission hides recovery without erasing a saved snapshot',async()=>{
  const original=await savedCollection(),f=fixture('',{stored:original.stored});f.calls[0].respond({...recoveryWorkspace(),can_collect:false});await tick();
  assert.equal(f.get('[data-wechat-collection-draft]').hidden,true);assert.equal(f.stored.has(recoveryKey),true);
  f.emit('[data-wechat-draft-restore]','click');await tick();assert.equal(f.get('[data-wechat-collect]').elements.urls.value,'');
});
test('a storage quota error preserves the previous snapshot while new input stays editable',async()=>{
  const f=await savedCollection(),previous=f.stored.get(recoveryKey);f.storageState.fail=true;
  f.get('[data-wechat-collect]').elements.urls.value='https://mp.weixin.qq.com/s/newdraft';f.get('[data-wechat-collect]').elements.urls.events.input();flushRecovery(f);
  assert.equal(f.stored.get(recoveryKey),previous);assert.equal(f.get('[data-wechat-collect]').elements.urls.value,'https://mp.weixin.qq.com/s/newdraft');assert.match(f.get('[data-wechat-draft-status]').textContent,/暂存不可用/);
});
test('an unhandled saved recovery is not silently overwritten by new page input',async()=>{
  const original=await savedCollection(),f=await recoveryReady(original.stored),previous=f.stored.get(recoveryKey);
  f.get('[data-wechat-collect]').elements.urls.value='https://mp.weixin.qq.com/s/newinput';f.get('[data-wechat-collect]').elements.urls.events.input();flushRecovery(f);f.winEvents.pagehide();
  assert.equal(f.stored.get(recoveryKey),previous);assert.match(f.get('[data-wechat-draft-status]').textContent,/尚待处理/);
});
test('confirmed partial imports preserve the remaining recovery and drop consumed article selections',async()=>{
  const f=await savedCollection();f.emit('[data-wechat-apply]','click');await tick();f.calls.at(-1).respond({applied:true,plan:{new:1,existing:0,conflicts:0,items:[{key:'one',action:'created',post_id:9,title:'one'}]}});await tick();
  const record=JSON.parse(f.stored.get(recoveryKey));assert.equal(record.uncertain,false);assert.deepEqual(record.choices.map(c=>c.key),['two']);assert.equal(record.choices[0].selected,false);
});
test('turning off local collection retention confirms deletion and stops pagehide from writing again',async()=>{
  const f=await savedCollection(),input=f.get('[data-wechat-draft-enable]');input.checked=false;f.emit('[data-wechat-draft-enable]','change');await tick();
  f.get('[data-wechat-confirm]').close('cancel');await tick();assert.equal(f.stored.has(recoveryKey),true);assert.equal(input.checked,true);
  input.checked=false;f.emit('[data-wechat-draft-enable]','change');await tick();f.get('[data-wechat-confirm]').close('confirm');await tick();
  f.winEvents.pagehide();assert.equal(f.stored.has(recoveryKey),false);assert.equal(f.get('[data-wechat-collect]').elements.urls.value,'https://mp.weixin.qq.com/s/firstx');
});

test('a newly reviewed link batch replaces the restored baseline for later refreshes',async()=>{
  const original=await savedCollection(),f=await recoveryReady(original.stored);await restoreCollection(f);
  const urls=f.get('[data-wechat-collect]').elements.urls;urls.value='https://mp.weixin.qq.com/s/changedbatch';urls.events.input();
  await recoveryPreview(f);const checks=f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]');checks[0].checked=true;checks[0].events.change();flushRecovery(f);
  assert.equal(JSON.parse(f.stored.get(recoveryKey)).baseline,urls.value);
  const next=await recoveryReady(f.stored);await restoreCollection(next);await recoveryPreview(next);
  assert.deepEqual(next.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').map(c=>c.checked),[true,false]);
});

test('missing hashing drops old choices for freshly previewed articles instead of reviving them later',async()=>{
  const original=await savedCollection(),f=await recoveryReady(original.stored);await restoreCollection(f);f.window.crypto={};
  await recoveryPreview(f);assert.deepEqual(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').map(c=>c.checked),[false,false]);
  assert.equal(JSON.parse(f.stored.get(recoveryKey)).choices.length,0);
  const next=await recoveryReady(f.stored);await restoreCollection(next);await recoveryPreview(next);
  assert.deepEqual(next.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').map(c=>c.checked),[false,false]);
});

test('restoration rechecks identity after an overwrite confirmation',async()=>{
  const original=await savedCollection(),f=await recoveryReady(original.stored),urls=f.get('[data-wechat-collect]').elements.urls;
  urls.value='current unsaved links';f.emit('[data-wechat-draft-restore]','click');await tick();f.calls.at(-1).respond(recoveryWorkspace());await tick();assert.equal(f.get('[data-wechat-confirm]').open,true);
  f.session.payload.user_id=43;f.get('[data-wechat-confirm]').close('confirm');await tick();
  assert.equal(urls.value,'current unsaved links');assert.equal(f.get('[data-wechat-recovery]').hidden,false);assert.equal(f.stored.has(recoveryKey),true);
});

test('restoration reloads server policy after confirmation and keeps current input when disabled',async()=>{
  const original=await savedCollection(),f=await recoveryReady(original.stored),urls=f.get('[data-wechat-collect]').elements.urls;
  urls.value='current unsaved links';f.emit('[data-wechat-draft-restore]','click');await tick();f.calls.at(-1).respond(recoveryWorkspace());await tick();
  const count=f.calls.length;f.get('[data-wechat-confirm]').close('confirm');await tick();assert.equal(f.calls.length,count+1);
  f.calls.at(-1).respond(workspace());await settleRecovery(f);
  assert.equal(urls.value,'current unsaved links');assert.equal(f.stored.has(recoveryKey),false);
});

test('leaving during restore confirmation preserves both saved and current inputs',async()=>{
  const original=await savedCollection(),f=await recoveryReady(original.stored),urls=f.get('[data-wechat-collect]').elements.urls,record=f.stored.get(recoveryKey);
  urls.value='current unsaved links';f.emit('[data-wechat-draft-restore]','click');await tick();f.calls.at(-1).respond(recoveryWorkspace());await tick();
  f.winEvents.pagehide();await tick();assert.equal(urls.value,'current unsaved links');assert.equal(f.stored.get(recoveryKey),record);assert.equal(f.get('[data-wechat-confirm]').open,false);
});

test('stopping a preview while fingerprinting ignores the late hash result',async()=>{
  const f=await savedCollection(),before=f.stored.get(recoveryKey);let finishHash;
  f.window.ChunnelWeChatDrafts.fingerprints=()=>new Promise(resolve=>{finishHash=resolve;});
  f.emit('[data-wechat-collect]','submit');await tick();const data=preview();data.articles[0].title='changed after cancellation';f.calls.at(-1).respond(data);await tick();assert.equal(typeof finishHash,'function');
  f.emit('[data-wechat-stop-read]','click');finishHash(new Map());await settleRecovery(f);flushRecovery(f);
  assert.equal(f.stored.get(recoveryKey),before);assert.deepEqual(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').map(c=>c.checked),[true,false]);
});
async function collect(f) { f.get('[data-wechat-collect]').elements.urls.value = 'https://mp.weixin.qq.com/s/firstx'; f.emit('[data-wechat-collect]', 'submit'); await tick(); f.calls.at(-1).respond(preview()); await tick(); }

const collectionImages = (start,count) => Array.from({length:count},(_,i)=>'https://mmbiz.qpic.cn/budget-'+(start+i));
async function collectImageBatch(f,data){f.get('[data-wechat-collect]').elements.urls.value='https://mp.weixin.qq.com/s/firstx';f.emit('[data-wechat-collect]','submit');await tick();f.calls.at(-1).respond(data);await tick();}

test('canceling an initial preview unlocks the workspace and ignores its late response', async () => {
  const f = await ready(), input = f.get('[data-wechat-collect]').elements.urls;
  input.value = 'https://mp.weixin.qq.com/s/firstx'; input.focus();
  f.emit('[data-wechat-collect]', 'submit'); await tick();
  const pending = f.calls.at(-1), stop = f.get('[data-wechat-stop-read]');
  assert.equal(stop.hidden, false); assert.equal(stop.disabled, false);
  assert.equal(stop.textContent, '停止预览');
  stop.focus(); f.emit('[data-wechat-stop-read]', 'click'); await tick();
  assert.equal(pending.options.signal.aborted, true);
  assert.equal(f.root.attrs['aria-busy'], 'false'); assert.equal(stop.hidden, true);
  assert.equal(f.get('[data-wechat-collect]').querySelector('fieldset').disabled, false);
  assert.equal(input.value, 'https://mp.weixin.qq.com/s/firstx');
  assert.match(f.get('[data-wechat-status]').textContent, /已停止预览/);
  assert.equal(f.get('[data-wechat-apply]').disabled, true);
  pending.respond(preview()); await tick();
  assert.equal(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').length, 0);
  assert.match(f.get('[data-wechat-status]').textContent, /已停止预览/);
});

test('canceling preview refresh retains choices expiry expansion and allows a fresh request', async () => {
  const f = await ready(); await collect(f);
  const box = f.get('[data-wechat-collection]'), checks = box.querySelectorAll('input[type=checkbox]');
  checks[1].checked = false; checks[1].events.change();
  box.querySelectorAll('details')[0].open = true;
  const note = f.get('[data-wechat-apply-note]').textContent;
  f.emit('[data-wechat-collect]', 'submit'); await tick();
  const old = f.calls.at(-1);
  f.emit('[data-wechat-stop-read]', 'click'); await tick();
  assert.deepEqual(checks.map(c => c.checked), [true, false]);
  assert.equal(box.querySelectorAll('details')[0].open, true);
  assert.equal(f.get('[data-wechat-apply-note]').textContent, note);
  assert.equal(f.get('[data-wechat-apply]').disabled, false);
  assert.equal(f.get('[data-wechat-preview-state]').hidden, true);
  assert.ok(![...f.timers.values()].some(timer => [10000, 15000, 60000].includes(timer.ms)));
  f.emit('[data-wechat-collect]', 'submit'); await tick();
  const next = f.calls.at(-1);
  const stale = preview(); stale.articles[0].title = '迟到内容';
  old.respond(stale); await tick();
  assert.equal(f.root.attrs['aria-busy'], 'true');
  assert.equal(next.options.signal.aborted, false);
  assert.equal(f.get('[data-wechat-stop-read]').hidden, false);
  next.respond(preview()); await tick();
  assert.deepEqual(box.querySelectorAll('input[type=checkbox]').map(c => c.checked), [true, false]);
  assert.equal(box.querySelectorAll('details')[0].open, true);
  assert.equal(box.querySelectorAll('summary')[0].textContent, 'one');
});

test('canceling an article check preserves the prior report and supports checking again', async () => {
  const f = await checkedWorkspace(); await inspect(f);
  f.get('[data-wechat-check]').focus();
  f.emit('[data-wechat-check]', 'click'); await tick();
  const pending = f.calls.at(-1), stop = f.get('[data-wechat-stop-read]');
  assert.equal(stop.textContent, '停止检查');
  stop.focus(); f.emit('[data-wechat-stop-read]', 'click'); await tick();
  assert.equal(pending.options.signal.aborted, true);
  assert.equal(f.get('[data-wechat-check-result]').dataset.state, 'ready');
  assert.equal(f.get('[data-wechat-check-images]').textContent, '2 / 2 项');
  assert.equal(f.get('[data-wechat-check]').disabled, false);
  assert.match(f.get('[data-wechat-status]').textContent, /已停止检查/);
  pending.respond({check:checkedReport({title:'迟到的检查'})}); await tick();
  assert.notEqual(f.get('[data-wechat-check-title]').textContent, '迟到的检查');
  await inspect(f);
  assert.equal(f.get('[data-wechat-check-result]').dataset.state, 'ready');
});

test('canceling a failed-link retry preserves its error the existing selection and preview expiry', async () => {
  const f = await ready(), data = preview();
  data.failures = [{url:'https://mp.weixin.qq.com/s/failed', message:'原链接读取超时'}];
  await collectImageBatch(f, data);
  const box = f.get('[data-wechat-collection]');
  const checks = box.querySelectorAll('input[type=checkbox]');
  checks[1].checked = false; checks[1].events.change();
  const note = f.get('[data-wechat-apply-note]').textContent;
  const retry = box.querySelectorAll('[data-wechat-retry]')[0];
  retry.focus(); retry.events.click(); await tick();
  const pending = f.calls.at(-1);
  assert.deepEqual(pending.body.urls, ['https://mp.weixin.qq.com/s/failed']);
  f.emit('[data-wechat-stop-read]', 'click'); await tick();
  assert.equal(pending.options.signal.aborted, true);
  assert.equal(retry.disabled, false);
  assert.equal(box.querySelectorAll('[data-wechat-retry]').length, 1);
  assert.ok(box.querySelectorAll('p').some(item => item.textContent === '原链接读取超时'));
  assert.deepEqual(checks.map(item => item.checked), [true, false]);
  assert.equal(f.get('[data-wechat-apply-note]').textContent, note);
  assert.equal(f.get('[data-wechat-apply]').disabled, false);
});

test('canceling during session validation prevents the subsequent preview request', async () => {
  const f = await ready();
  f.session.pending = true;
  f.get('[data-wechat-collect]').elements.urls.value = 'https://mp.weixin.qq.com/s/firstx';
  f.emit('[data-wechat-collect]', 'submit'); await tick();
  assert.equal(f.get('[data-wechat-stop-read]').hidden, false);
  f.emit('[data-wechat-stop-read]', 'click'); await tick();
  assert.equal(f.sessionCalls.at(-1).signal.aborted, true);
  assert.equal(f.root.attrs['aria-busy'], 'false');
  f.session.respond(); await tick();
  assert.equal(f.calls.length, 1);
  assert.equal(f.get('[data-wechat-collect]').querySelector('fieldset').disabled, false);
  assert.match(f.get('[data-wechat-status]').textContent, /已停止预览/);
});

test('canceling a connection test preserves unsaved configuration and ignores a late success', async () => {
  const f = await ready(), form = f.get('[data-wechat-config]');
  form.elements.author.value = '未保存的作者'; f.emit('[data-wechat-config]', 'input');
  f.emit('[data-wechat-test]', 'click'); await tick();
  const pending = f.calls.at(-1);
  assert.equal(f.get('[data-wechat-stop-read]').textContent, '停止连接测试');
  f.emit('[data-wechat-stop-read]', 'click'); await tick();
  assert.equal(pending.options.signal.aborted, true);
  pending.respond({message:'迟到的成功'}); await tick();
  assert.equal(form.elements.author.value, '未保存的作者');
  assert.equal(f.get('[data-wechat-dirty]').hidden, false);
  assert.match(f.get('[data-wechat-status]').textContent, /已停止连接测试/);
  f.emit('[data-wechat-test]', 'click'); await tick();
  f.calls.at(-1).respond({message:'连接检查成功'}); await tick();
  assert.match(f.get('[data-wechat-status]').textContent, /连接检查成功/);
});

test('write operations never expose or respond to the read cancellation control', async () => {
  for (const kind of ['sync', 'config', 'import']) {
    const f = kind === 'sync' ? await checkedWorkspace() : await ready();
    if (kind === 'import') { await collect(f); f.emit('[data-wechat-apply]', 'click'); }
    else f.emit(kind === 'sync' ? '[data-wechat-sync]' : '[data-wechat-config]', 'submit');
    await tick();
    const pending = f.calls.at(-1);
    assert.notEqual(f.get('[data-wechat-stop-read]').hidden, false, kind);
    f.emit('[data-wechat-stop-read]', 'click'); await tick();
    assert.equal(pending.options.signal.aborted, false, kind);
    assert.equal(f.root.attrs['aria-busy'], 'true', kind);
    f.winEvents.pagehide(); f.winEvents.pageshow({persisted:true});
    assert.match(f.get('[data-wechat-recovery-copy]').textContent, /上次操作结果尚待核对/, kind);
  }
});

test('page interruption of preview check and connection reads does not claim an uncertain write', async () => {
  for (const kind of ['preview', 'check', 'test']) {
    const f = kind === 'check' ? await checkedWorkspace() : await ready();
    if (kind === 'preview') {
      f.get('[data-wechat-collect]').elements.urls.value = 'https://mp.weixin.qq.com/s/firstx';
      f.emit('[data-wechat-collect]', 'submit');
    } else f.emit(kind === 'check' ? '[data-wechat-check]' : '[data-wechat-test]', 'click');
    await tick();
    const pending = f.calls.at(-1);
    f.winEvents.pagehide(); f.winEvents.pageshow({persisted:true}); await tick();
    assert.equal(pending.options.signal.aborted, true, kind);
    assert.doesNotMatch(f.get('[data-wechat-recovery-copy]').textContent, /上次操作结果尚待核对/, kind);
    assert.equal(f.get('[data-wechat-stop-read]').hidden, true, kind);
    assert.equal(f.get('[data-wechat-apply]').disabled, true, kind);
  }
});

test('oversized image selections remain reviewable and can be reduced before any import request',async()=>{
  const f=await ready(),data=preview();data.articles[0].images=collectionImages(0,26);data.articles[1].images=collectionImages(26,26);
  await collectImageBatch(f,data);
  assert.equal(f.get('[data-wechat-image-count]').textContent,'52 / 50 张');assert.equal(f.get('[data-wechat-image-budget]').dataset.state,'over');
  assert.match(f.get('[data-wechat-image-budget-note]').textContent,/超出 2 张.*取消部分文章/);assert.equal(f.get('[data-wechat-apply]').disabled,true);
  const count=f.calls.length;f.emit('[data-wechat-apply]','click');await tick();assert.equal(f.calls.length,count);
  const checks=f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]');assert.ok(checks.every(c=>!c.disabled));
  checks[1].checked=false;checks[1].events.change();
  assert.equal(f.get('[data-wechat-image-count]').textContent,'26 / 50 张');assert.equal(f.get('[data-wechat-apply]').disabled,false);
  f.emit('[data-wechat-apply]','click');await tick();assert.deepEqual(f.calls.at(-1).body.articles.map(a=>a.key),['one']);
});

test('collection image budget counts shared links once including independent covers and excludes existing posts',async()=>{
  const f=await ready(),data=preview();data.articles[0].images=collectionImages(0,26);data.articles[1].images=collectionImages(24,26);
  data.articles.push({...data.articles[0],key:'existing',images:collectionImages(100,30)});data.plan.items.push({key:'existing',action:'changed',post_id:55});
  await collectImageBatch(f,data);
  assert.equal(f.get('[data-wechat-image-count]').textContent,'50 / 50 张');assert.equal(f.get('[data-wechat-apply]').disabled,false);
  const next=structuredClone(data);next.articles[0].cover='https://mmbiz.qpic.cn/cover';next.articles[0].images.push(next.articles[0].cover);
  await collectImageBatch(f,next);
  assert.equal(f.get('[data-wechat-image-count]').textContent,'26 / 50 张');
  const check=f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]')[0];check.checked=true;check.events.change();
  assert.equal(f.get('[data-wechat-image-count]').textContent,'51 / 50 张');assert.equal(f.get('[data-wechat-apply]').disabled,true);
});

test('partial collection imports leave remaining images available for the next explicit batch',async()=>{
  const f=await ready(),data=preview();data.articles[0].images=collectionImages(0,26);data.articles[1].images=collectionImages(26,26);await collectImageBatch(f,data);
  const check=f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]')[1];check.checked=false;check.events.change();
  f.emit('[data-wechat-apply]','click');await tick();f.calls.at(-1).respond({applied:true,plan:{new:1,existing:0,conflicts:0,items:[{key:'one',title:'one',post_id:31,action:'created'}]}});await tick();
  f.calls.at(-1).respond(workspace());await tick();
  assert.equal(f.get('[data-wechat-image-count]').textContent,'0 / 50 张');assert.equal(f.get('[data-wechat-apply]').disabled,true);
  f.get('[data-wechat-select-all]').checked=true;f.emit('[data-wechat-select-all]','change');
  assert.equal(f.get('[data-wechat-image-count]').textContent,'26 / 50 张');assert.equal(f.get('[data-wechat-apply]').disabled,false);
  assert.deepEqual(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').map(c=>c.dataset.key),['two']);
});

test('failed preview and disabled collection retain the image budget without unlocking import',async()=>{
  const f=await ready(),data=preview();data.articles[0].images=collectionImages(0,26);data.articles[1].images=collectionImages(26,26);await collectImageBatch(f,data);
  f.emit('[data-wechat-collect]','submit');await tick();f.calls.at(-1).reject(new TypeError('network failure'));await tick();
  assert.equal(f.get('[data-wechat-image-count]').textContent,'52 / 50 张');assert.equal(f.get('[data-wechat-apply]').disabled,true);
  f.emit('[data-wechat-refresh]','click');f.calls.at(-1).respond(workspace({config:{...workspace().config,collect_enabled:false}}));await tick();
  const check=f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]')[1];check.checked=false;check.events.change();
  assert.equal(f.get('[data-wechat-image-count]').textContent,'26 / 50 张');assert.equal(f.get('[data-wechat-apply]').disabled,true);
});

test('a successful per-link retry recalculates the budget while preserving prior article choices',async()=>{
  const f=await ready(),data=preview();data.articles=[data.articles[0]];data.articles[0].images=collectionImages(0,26);data.plan.items=[{key:'one',action:'create'}];data.receipts={one:'receipt-one'};data.failures=[{url:'https://mp.weixin.qq.com/s/second',message:'读取失败'}];
  await collectImageBatch(f,data);f.get('[data-wechat-collection]').querySelectorAll('[data-wechat-retry]')[0].events.click();await tick();
  const next=preview();next.articles=[next.articles[1]];next.articles[0].images=collectionImages(26,26);next.plan.items=[{key:'two',action:'create'}];next.receipts={two:'receipt-two'};
  f.calls.at(-1).respond(next);await tick();
  assert.equal(f.get('[data-wechat-image-count]').textContent,'52 / 50 张');assert.equal(f.get('[data-wechat-apply]').disabled,true);
  assert.deepEqual(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').map(c=>c.checked),[true,true]);
});

test('per-article validation failures preserve valid selections through retry and explicit import', async () => {
  const f = await ready(), data = preview();
  const failedURL = 'https://mp.weixin.qq.com/s/thirdx';
  const reason = '文章未通过导入校验：标题不能包含控制字符';
  data.failures = [{url:failedURL,message:reason}];
  f.get('[data-wechat-collect]').elements.urls.value = 'https://mp.weixin.qq.com/s/firstx\nhttps://mp.weixin.qq.com/s/second\n' + failedURL;
  f.emit('[data-wechat-collect]', 'submit'); await tick();
  f.calls.at(-1).respond(data); await tick();
  const collection = f.get('[data-wechat-collection]');
  assert.ok(collection.querySelectorAll('p').some(n => n.textContent?.includes(reason)));
  let checks = collection.querySelectorAll('input[type=checkbox]');
  assert.deepEqual(checks.map(n => n.dataset.key), ['one','two']);
  checks[1].checked = false; checks[1].events.change();
  const expiry = f.get('[data-wechat-apply-note]').textContent.split('预览有效至 ')[1];
  collection.querySelectorAll('[data-wechat-retry]')[0].events.click(); await tick();
  assert.deepEqual(f.calls.at(-1).body.urls, [failedURL]);
  f.calls.at(-1).respond({articles:[],failures:[{url:failedURL,message:reason}]}); await tick();
  checks = collection.querySelectorAll('input[type=checkbox]');
  assert.deepEqual(checks.map(n => n.checked), [true,false]);
  assert.equal(f.get('[data-wechat-apply]').disabled, false);
  assert.equal(f.get('[data-wechat-apply-note]').textContent.split('预览有效至 ')[1], expiry);
  collection.querySelectorAll('[data-wechat-retry]')[0].events.click(); await tick();
  const repaired = preview();
  repaired.articles = [{...repaired.articles[0],key:'three',title:'修正后的文章',url:failedURL}];
  repaired.receipts = {three:'receipt-three'};
  repaired.plan = {new:1,existing:0,conflicts:0,items:[{key:'three',action:'create'}]};
  f.calls.at(-1).respond(repaired); await tick();
  assert.deepEqual(collection.querySelectorAll('input[type=checkbox]').map(n => n.checked), [true,false,true]);
  assert.equal(collection.querySelectorAll('[data-wechat-retry]').length, 0);
  assert.equal(f.get('[data-wechat-apply-note]').textContent.split('预览有效至 ')[1], expiry);
  f.emit('[data-wechat-apply]', 'click'); await tick();
  assert.deepEqual(f.calls.at(-1).body.articles.map(a => a.key), ['one','three']);
  assert.deepEqual(f.calls.at(-1).body.receipts, {one:'receipt-one',three:'receipt-three'});
  f.calls.at(-1).respond({applied:true,plan:{new:2,existing:0,conflicts:0,items:['one','three'].map((key,i) => ({key,title:key,post_id:31+i,action:'created'}))}}); await tick();
  f.calls.at(-1).respond(workspace()); await tick();
  assert.deepEqual(collection.querySelectorAll('input[type=checkbox]').map(n => n.dataset.key), ['two']);
  assert.equal(f.get('[data-wechat-import-results]').querySelectorAll('a').length, 2);
});

const checkedPost = { id:91,version:3,title:'检查文章',status:'draft' };
const checkedReport = overrides => ({ post_id:91,version:3,config_version:2,title:'检查文章',auto_publish:false,ready:true,title_chars:4,content_chars:100,content_bytes:200,images:2,checked_images:2,upload_bytes:2048,issues:[],warnings:['图片地址会在同步时再次核对。'],checked_at:new Date().toISOString(),...overrides });
async function checkedWorkspace(){const f=fixture('?post=91');f.calls[0].respond(workspace({posts:[checkedPost],selected_post:checkedPost}));await tick();return f;}
async function inspect(f, report=checkedReport()){f.emit('[data-wechat-check]','click');await tick();f.calls.at(-1).respond({check:report});await tick();}

test('local article checks show metrics and corrections without starting synchronization',async()=>{
  const f=await checkedWorkspace();await inspect(f);
  assert.equal(f.calls.at(-1).url,'/api/v1/admin/wechat/sync/check');
  assert.deepEqual(f.calls.at(-1).body,{post_id:91,version:3,config_version:2});
  assert.equal(f.get('[data-wechat-check-result]').dataset.state,'ready');
  assert.equal(f.get('[data-wechat-check-title-count]').textContent,'4 / 32');
  assert.equal(f.get('[data-wechat-check-images]').textContent,'2 / 2 项');
  assert.equal(f.get('[data-wechat-check-edit]').href,'/admin/content/91/edit/');
  await inspect(f,checkedReport({ready:false,title_chars:33,checked_images:0,upload_bytes:0,issues:[{field:'title',message:'标题超过 32 字'}]}));
  assert.equal(f.get('[data-wechat-check-result]').dataset.state,'issues');
  assert.equal(f.get('[data-wechat-check-issues]').querySelectorAll('li')[0].textContent,'标题超过 32 字');
  assert.equal(f.calls.filter(c=>c.url.endsWith('/sync')).length,0);
});

test('article and saved configuration changes invalidate a previous check while preserving its report',async()=>{
  const f=await checkedWorkspace();await inspect(f);
  f.get('[data-wechat-config]').elements.author.value='未保存';f.emit('[data-wechat-config]','input');
  assert.equal(f.get('[data-wechat-check-result]').dataset.state,'ready');assert.match(f.get('[data-wechat-check-state]').textContent,/已保存的设置/);
  f.get('[data-wechat-sync]').elements.post_id.value='92';f.get('[data-wechat-sync]').elements.post_id.events.change();
  assert.equal(f.get('[data-wechat-check-result]').dataset.state,'stale');
  f.get('[data-wechat-sync]').elements.post_id.value='91';f.get('[data-wechat-sync]').elements.post_id.events.change();
  assert.equal(f.get('[data-wechat-check-result]').dataset.state,'ready');
  f.emit('[data-wechat-refresh]','click');f.calls.at(-1).respond(workspace({posts:[{...checkedPost,version:4}],selected_post:{...checkedPost,version:4}}));await tick();
  assert.equal(f.get('[data-wechat-check-result]').dataset.state,'stale');assert.equal(f.get('[data-wechat-check-title]').textContent,'检查文章');
  assert.equal(f.get('[data-wechat-config]').elements.author.value,'未保存');
});

test('incomplete or mismatched checks preserve the previous result and require another check',async()=>{
  const f=await checkedWorkspace();await inspect(f);
  for(const report of [checkedReport({post_id:92}),checkedReport({ready:true,checked_images:0}),checkedReport({issues:null})]){
    await inspect(f,report);assert.match(f.get('[data-wechat-status]').textContent,/检查结果不完整/);
    assert.equal(f.get('[data-wechat-check-result]').dataset.state,'stale');assert.equal(f.get('[data-wechat-check-images]').textContent,'2 / 2 项');
  }
  await inspect(f);assert.equal(f.get('[data-wechat-check-result]').dataset.state,'ready');
  f.emit('[data-wechat-check]','click');await tick();f.calls.at(-1).respond({error:{message:'图片准备暂不可用'}},503);await tick();
  assert.match(f.get('[data-wechat-status]').textContent,/图片准备暂不可用/);assert.equal(f.get('[data-wechat-check-result]').dataset.state,'stale');
});

test('record-only refresh updates the selected version before rechecking and removes unavailable articles',async()=>{
  const f=await checkedWorkspace();await inspect(f);
  f.emit('[data-wechat-job-filters]','submit');
  f.calls.at(-1).respond(workspace({posts:[checkedPost],selected_post:{...checkedPost,version:4,title:'更新标题'}}));await tick();
  assert.equal(f.get('[data-wechat-check-result]').dataset.state,'stale');
  f.emit('[data-wechat-check]','click');await tick();
  assert.equal(f.calls.at(-1).body.version,4);
  f.calls.at(-1).respond({check:checkedReport({version:4,title:'更新标题'})});await tick();
  assert.equal(f.get('[data-wechat-check-result]').dataset.state,'ready');
  f.emit('[data-wechat-job-filters]','submit');f.calls.at(-1).respond(workspace({posts:[],selected_post:null}));await tick();
  assert.equal(f.get('[data-wechat-check-result]').dataset.state,'stale');
  assert.equal(f.get('[data-wechat-check]').disabled,true);
});

test('check controls follow permissions and configuration and an interrupted check is read-only',async()=>{
  const f=await checkedWorkspace();f.session.payload={...f.session.payload,role_capabilities:['admin:access','settings:manage']};
  const before=f.calls.length;f.emit('[data-wechat-check]','click');await tick();assert.equal(f.calls.length,before);assert.equal(f.get('[data-wechat-check]').disabled,true);
  const g=await checkedWorkspace();g.emit('[data-wechat-check]','click');await tick();const pending=g.calls.at(-1);
  g.winEvents.pagehide();g.winEvents.pageshow({persisted:true});pending.respond({check:checkedReport()});await tick();
  assert.doesNotMatch(g.get('[data-wechat-recovery-copy]').textContent,/上次操作结果尚待核对/);
  assert.equal(g.get('[data-wechat-check-title]').textContent,undefined);
  const h=fixture('?post=91');h.calls[0].respond(workspace({posts:[checkedPost],selected_post:checkedPost,config:{...workspace().config,enabled:false}}));await tick();assert.equal(h.get('[data-wechat-check]').disabled,true);
});

test('a late check cannot replace the result for a newly selected article',async()=>{
  const f=await checkedWorkspace();f.emit('[data-wechat-check]','click');await tick();const request=f.calls.at(-1);
  f.get('[data-wechat-sync]').elements.post_id.value='92';request.respond({check:checkedReport()});await tick();
  assert.equal(f.get('[data-wechat-check-title]').textContent,undefined);assert.match(f.get('[data-wechat-status]').textContent,/所选文章已变化/);
});

test('article deep link resolves outside search results and sends the reviewed configuration version', async () => {
  const f = fixture('?post=91');
  assert.match(f.calls[0].url, /post=91/);
  f.calls[0].respond(workspace({ selected_post: { id: 91, version: 3, title: '旧文章', status: 'draft' } })); await tick();
  assert.equal(f.get('[data-wechat-sync]').elements.post_id.value, '91');
  f.emit('[data-wechat-sync]', 'submit'); await tick();
  assert.deepEqual(f.calls.at(-1).body, { post_id: 91, version: 3, config_version: 2 });
});

test('selective import submits only checked articles and keeps remaining previews usable', async () => {
  const f = await ready(); await collect(f);
  const checks = f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]');
  assert.equal(checks.length, 2); checks[1].checked = false; checks[1].events.change();
  f.emit('[data-wechat-apply]', 'click'); await tick();
  const call = f.calls.at(-1);
  assert.deepEqual(call.body.articles.map(a => a.key), ['one']); assert.deepEqual(call.body.receipts, { one: 'receipt-one' });
  assert.ok(checks.every(c => c.disabled));
  call.respond({ applied: true, plan: { new: 1, existing: 0, conflicts:0, items: [{ key: 'one', title: 'one', post_id: 31, action:'created' }] } }); await tick();
  f.calls.at(-1).respond(workspace()); await tick();
  const remaining = f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]');
  assert.equal(remaining.length, 1); assert.equal(remaining[0].dataset.key, 'two'); assert.equal(remaining[0].checked, false);
  assert.equal(f.get('[data-wechat-apply]').disabled, true);
  remaining[0].checked = true; remaining[0].events.change();
  assert.equal(f.get('[data-wechat-apply]').disabled, false);
  assert.equal(f.get('[data-wechat-import-results]').querySelectorAll('a')[0].href, '/admin/content/31/edit/');
  f.emit('[data-wechat-apply]', 'click'); await tick(); assert.deepEqual(f.calls.at(-1).body.receipts, { two: 'receipt-two' });
});

test('failed or incomplete import receipts keep the selected preview available for safe retry', async () => {
  const f = await ready(); await collect(f);
  f.emit('[data-wechat-apply]', 'click'); await tick();
  f.calls.at(-1).respond({ applied: true, plan: { items: [] } }); await tick();
  assert.match(f.get('[data-wechat-status]').textContent, /回执不完整/);
  assert.equal(f.get('[data-wechat-apply]').disabled, false);
  assert.equal(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').length, 2);
  f.emit('[data-wechat-apply]', 'click'); await tick();
  f.calls.at(-1).respond({ error: { message: '图片暂不可访问' } }, 422); await tick();
  assert.match(f.get('[data-wechat-status]').textContent, /图片暂不可访问/);
  assert.equal(f.get('[data-wechat-apply]').disabled, false);
});

test('history polling continues when active jobs are on another page and retains filters', async () => {
  const f = fixture();
  f.calls[0].respond(workspace({ active_jobs: 2, job_pages: 7, total_jobs: 125 })); await tick();
  assert.ok([...f.timers.values()].some(timer => timer.ms === 6000));
  f.get('[data-wechat-job-filters]').elements.job_q.value = '历史';
  f.get('[data-wechat-job-filters]').elements.job_status.value = 'draft';
  f.emit('[data-wechat-job-filters]', 'submit'); await tick();
  assert.match(f.calls.at(-1).url, /job_status=draft/);
  f.calls.at(-1).respond(workspace({ active_jobs: 2, job_pages: 3 })); await tick();
  f.emit('[data-wechat-next]', 'click'); await tick();
  assert.match(f.calls.at(-1).url, /job_page=2/); assert.match(f.calls.at(-1).url, /job_status=draft/);
});

test('a superseded read cannot overwrite the newer history page', async () => {
  const f = await ready();
  f.emit('[data-wechat-refresh]', 'click'); const stale = f.calls.at(-1);
  f.emit('[data-wechat-refresh]', 'click'); const current = f.calls.at(-1);
  current.respond(workspace({ job_page: 2, job_pages: 4 })); await tick();
  stale.respond(workspace({ job_page: 1, job_pages: 4 })); await tick();
  assert.equal(f.get('[data-wechat-page]').textContent, '第 2 / 4 页');
});

test('expired previews disable selection and never start an import', async () => {
  const f = await ready();
  f.get('[data-wechat-collect]').elements.urls.value = 'https://mp.weixin.qq.com/s/firstx';
  f.emit('[data-wechat-collect]', 'submit'); await tick();
  const data = preview(); data.expires = new Date(Date.now() - 1000).toISOString();
  f.calls.at(-1).respond(data); await tick();
  assert.equal(f.get('[data-wechat-apply]').disabled, true);
  assert.match(f.get('[data-wechat-apply-note]').textContent, /已过期/);
  const before = f.calls.length; f.emit('[data-wechat-apply]', 'click'); await tick();
  assert.equal(f.calls.length, before);
});

test('editing collection links retains the old preview but blocks import until the links are restored', async () => {
  const f = await ready(); await collect(f);
  const checks = f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]');checks[1].checked=false;checks[1].events.change();
  f.get('[data-wechat-collect]').elements.urls.value='https://mp.weixin.qq.com/s/changed';
  f.get('[data-wechat-collect]').elements.urls.events.input();
  assert.equal(f.get('[data-wechat-apply]').disabled, true);
  assert.equal(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').length, 2);
  assert.match(f.get('[data-wechat-preview-state]').textContent,/链接已修改/);
  const before = f.calls.length; f.emit('[data-wechat-apply]', 'click'); await tick();
  assert.equal(f.calls.length, before);
  f.get('[data-wechat-collect]').elements.urls.value='  https://mp.weixin.qq.com/s/firstx\n';f.get('[data-wechat-collect]').elements.urls.events.input();
  assert.equal(f.get('[data-wechat-apply]').disabled,false);assert.deepEqual(checks.map(c=>c.checked),[true,false]);
});

test('retrying one failed link preserves successful previews and unchecked choices', async () => {
  const f = await ready();
  f.get('[data-wechat-collect]').elements.urls.value = 'https://mp.weixin.qq.com/s/firstx';
  f.emit('[data-wechat-collect]', 'submit'); await tick();
  const original = preview(); original.failures = [{ url: 'https://mp.weixin.qq.com/s/failed', message: '读取超时' }];
  f.calls.at(-1).respond(original); await tick();
  const checks = f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]'); checks[1].checked = false; checks[1].events.change();
  const retry = f.get('[data-wechat-collection]').querySelectorAll('[data-wechat-retry]')[0];
  retry.events.click(); await tick();
  assert.deepEqual(f.calls.at(-1).body, { urls: ['https://mp.weixin.qq.com/s/failed'] });
  assert.equal(retry.disabled, true);
  const later = preview(); later.articles = [{ ...later.articles[0], key: 'three', title: 'three' }]; later.receipts = { three: 'receipt-three' }; later.plan.items = [{ key: 'three', action: 'create' }];
  f.calls.at(-1).respond(later); await tick();
  const remaining = f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]');
  assert.deepEqual(remaining.map(node => [node.dataset.key, node.checked]), [['one', true], ['two', false], ['three', true]]);
  assert.equal(f.get('[data-wechat-collection]').querySelectorAll('[data-wechat-retry]').length, 0);
  f.emit('[data-wechat-apply]', 'click'); await tick();
  assert.deepEqual(f.calls.at(-1).body.articles.map(article => article.key), ['one', 'three']);
  assert.deepEqual(f.calls.at(-1).body.receipts, { one: 'receipt-one', three: 'receipt-three' });
});

test('resume confirms the stored publication mode even when the new default is draft-only', async () => {
  const f = fixture();
  const job = { id: 'job-1', title: '旧任务', app_id: 'wx0123456789abcdef', auto_publish: true, status: 'skipped', updated_at: new Date().toISOString() };
  f.calls[0].respond(workspace({ jobs: [job] })); await tick();
  const button = f.get('[data-wechat-jobs]').querySelectorAll('button').find(node => node.dataset.action === 'resume');
  assert.ok(button); f.emit('[data-wechat-jobs]', 'click', { target: button }); await tick();
  assert.equal(f.calls.length, 1); assert.equal(f.get('[data-wechat-confirm]').open, true);
  assert.match(f.get('[data-wechat-confirm-copy]').textContent, /同步后发表/);
  f.get('[data-wechat-confirm]').close('confirm'); await tick();
  assert.equal(f.calls.at(-1).body.action, 'resume'); assert.equal(f.calls.at(-1).body.updated_at, job.updated_at);
});

test('draft-only remains available while sync is disabled; uncertain or submitted jobs cannot downgrade', async () => {
  const f = fixture(), base = { app_id: 'wx0123456789abcdef', auto_publish: true, updated_at: new Date().toISOString() };
  f.calls[0].respond(workspace({ config: { ...workspace().config, enabled: false }, jobs: [{ ...base, id: 'safe', status: 'queued' }, { ...base, id: 'unknown', status: 'uncertain' }, { ...base, id: 'remote', status: 'failed', publish_id: 'remote-id' }] })); await tick();
  const buttons = f.get('[data-wechat-jobs]').querySelectorAll('button').filter(node => node.dataset.action === 'draft_only');
  assert.equal(buttons.length, 1); assert.equal(buttons[0].dataset.id, 'safe'); assert.equal(buttons[0].disabled, false);
  f.emit('[data-wechat-jobs]', 'click', { target: buttons[0] }); await tick();
  assert.equal(f.calls.at(-1).body.action, 'draft_only');
});

test('unchanged tasks refresh their explanatory text when module settings change', async () => {
  const f = fixture(), jobs = [{ id: 'old-job', title: '原有任务', app_id: 'wx0123456789abcdef', auto_publish: true, status: 'queued' }];
  f.calls[0].respond(workspace({ config: { ...workspace().config, enabled: false, auto_publish: true }, jobs })); await tick();
  const copy = () => f.get('[data-wechat-jobs]').querySelectorAll('p').map(node => node.textContent).join('\n');
  assert.match(copy(), /同步模块已关闭/);
  assert.doesNotMatch(copy(), /当前默认设置仅用于新任务/);
  f.emit('[data-wechat-refresh]', 'click');
  f.calls.at(-1).respond(workspace({ config: { ...workspace().config, enabled: true, auto_publish: false }, jobs })); await tick();
  assert.doesNotMatch(copy(), /同步模块已关闭/);
  assert.match(copy(), /当前默认设置仅用于新任务/);
});

test('a retried alternate URL for an existing article preserves its unchecked choice and preview expiry', async () => {
  const f = await ready();
  f.get('[data-wechat-collect]').elements.urls.value = 'https://mp.weixin.qq.com/s/firstx';
  f.emit('[data-wechat-collect]', 'submit'); await tick();
  const original = preview(); original.failures = [{ url: 'https://mp.weixin.qq.com/s/alternate', message: '读取超时' }];
  f.calls.at(-1).respond(original); await tick();
  const beforeNote = f.get('[data-wechat-apply-note]').textContent;
  const checks = f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]'); checks[1].checked = false; checks[1].events.change();
  f.get('[data-wechat-collection]').querySelectorAll('[data-wechat-retry]')[0].events.click(); await tick();
  const later = preview(); later.articles = [later.articles[1]]; later.receipts = { two: 'renewed-receipt-two' }; later.plan.items = [{ key: 'two', action: 'create' }]; later.expires = new Date(Date.parse(original.expires) + 60000).toISOString();
  f.calls.at(-1).respond(later); await tick();
  const remaining = f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]');
  assert.deepEqual(remaining.map(node => [node.dataset.key, node.checked]), [['one', true], ['two', false]]);
  assert.equal(f.get('[data-wechat-apply-note]').textContent.split('预览有效至 ')[1], beforeNote.split('预览有效至 ')[1]);
});

test('unsaved configuration protects leaving and survives refresh with its original version', async () => {
  const f=await ready(), form=f.get('[data-wechat-config]');
  form.elements.app_secret.value='private credential only in memory';f.emit('[data-wechat-config]','input');
  assert.match(f.get('[data-wechat-config-state]').textContent,/未保存/);
  let prevented=false;f.winEvents.beforeunload?.({preventDefault(){prevented=true;}});assert.equal(prevented,true);
  assert.equal(f.get('[data-wechat-discard]').hidden,false);
  f.emit('[data-wechat-refresh]','click');f.calls.at(-1).respond(workspace({config:{...workspace().config,version:3,author:'other window'}}));await tick();
  assert.equal(form.elements.app_secret.value,'private credential only in memory');
  assert.equal(form.elements.author.value,'');assert.match(f.get('[data-wechat-config-state]').textContent,/服务器配置版本已变化/);
});

test('reverting all configuration changes removes the dirty flag and permits fresh values',async()=>{
  const f=await ready(),form=f.get('[data-wechat-config]');
  form.elements.author.value='changed';f.emit('[data-wechat-config]','input');
  form.elements.author.value='';f.emit('[data-wechat-config]','input');
  f.emit('[data-wechat-refresh]','click');f.calls.at(-1).respond(workspace({config:{...workspace().config,version:3,author:'new saved author'}}));await tick();
  assert.equal(form.elements.author.value,'new saved author');assert.equal(f.get('[data-wechat-discard]').hidden,true);
});

test('expired login retains collection choices and same-account recovery sends no automatic write',async()=>{
  const f=await ready();await collect(f);
  const checkboxes=f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]');checkboxes[1].checked=false;checkboxes[1].events.change();
  f.session.status=401;f.session.payload={authenticated:false};const count=f.calls.length;
  f.emit('[data-wechat-apply]','click');await tick();
  assert.equal(f.calls.length,count);assert.equal(f.get('[data-wechat-recovery]').hidden,false);assert.equal(f.get('[data-wechat-apply]').disabled,true);
  assert.deepEqual(checkboxes.map(c=>c.checked),[true,false]);
  f.session.status=200;f.session.payload={authenticated:true,read_only:false,user_id:42,csrf_token:'renewed-session-token-'.repeat(3),role_capabilities:['*']};
  f.emit('[data-wechat-recover]','click');await tick();
  assert.equal(f.calls.at(-1).options.method,'GET');f.calls.at(-1).respond(workspace());await tick();
  assert.equal(f.root.dataset.csrf,f.session.payload.csrf_token);assert.equal(f.get('[data-wechat-apply]').disabled,false);
  f.emit('[data-wechat-apply]','click');await tick();
  assert.deepEqual(f.calls.at(-1).body.articles.map(a=>a.key),['one']);
});

test('account switches block writes and cannot adopt another account workspace data',async()=>{
  const f=await ready(),form=f.get('[data-wechat-config]');form.elements.author.value='private draft';f.emit('[data-wechat-config]','input');
  f.session.payload={...f.session.payload,user_id:99};const before=f.calls.length;
  f.emit('[data-wechat-config]','submit');await tick();assert.equal(f.calls.length,before);assert.match(f.get('[data-wechat-status]').textContent,/账号已切换/);
  f.emit('[data-wechat-recover]','click');await tick();assert.equal(f.calls.length,before);assert.equal(form.elements.author.value,'private draft');
  const fresh=await ready();fresh.emit('[data-wechat-refresh]','click');fresh.calls.at(-1).respond(workspace({user_id:99,config:{...workspace().config,author:'foreign'}}));await tick();
  assert.equal(fresh.get('[data-wechat-config]').elements.author.value,'');assert.equal(fresh.get('[data-wechat-config]').fieldset.disabled,true);
});

test('configuration discard requires confirmation and a successful fresh read',async()=>{
  const f=await ready(),form=f.get('[data-wechat-config]');form.elements.author.value='draft';f.emit('[data-wechat-config]','input');
  f.emit('[data-wechat-discard]','click');await tick();assert.equal(f.get('[data-wechat-confirm]').open,true);
  f.get('[data-wechat-confirm]').close('cancel');await tick();assert.equal(form.elements.author.value,'draft');
  f.emit('[data-wechat-discard]','click');await tick();f.get('[data-wechat-confirm]').close('confirm');await tick();
  f.calls.at(-1).respond({error:{message:'unavailable'}},503);await tick();assert.equal(form.elements.author.value,'draft');
  f.emit('[data-wechat-discard]','click');await tick();f.get('[data-wechat-confirm]').close('confirm');await tick();
  f.calls.at(-1).respond(workspace({config:{...workspace().config,version:4,author:'latest'}}));await tick();
  assert.equal(form.elements.author.value,'latest');assert.equal(f.get('[data-wechat-discard]').hidden,true);
});

test('late configuration acknowledgement after pagehide cannot erase retained input',async()=>{
  const f=await ready(),form=f.get('[data-wechat-config]');form.elements.author.value='submitted';f.emit('[data-wechat-config]','input');
  f.emit('[data-wechat-config]','submit');await tick();const pending=f.calls.at(-1);
  f.winEvents.pagehide();assert.equal(pending.options.signal.aborted,true);
  f.winEvents.pageshow({persisted:true});await tick();
  form.elements.author.value='still retained';f.emit('[data-wechat-config]','input');
  pending.respond({config:{...workspace().config,version:3,author:'submitted'}});await tick();
  assert.equal(form.elements.author.value,'still retained');
});

test('malformed configuration acknowledgements keep the entered secret and original version',async()=>{
  const f=await ready(),form=f.get('[data-wechat-config]');form.elements.app_secret.value='retained-private-secret';f.emit('[data-wechat-config]','input');
  f.emit('[data-wechat-config]','submit');await tick();
  f.calls.at(-1).respond({config:{...workspace().config,version:99}});await tick();
  assert.equal(form.elements.app_secret.value,'retained-private-secret');assert.match(f.get('[data-wechat-status]').textContent,/回执不完整/);
  f.emit('[data-wechat-config]','submit');await tick();assert.equal(f.calls.at(-1).body.config.version,2);
});

test('revoked permissions prevent a retained configuration from being sent',async()=>{
  const f=await ready();f.session.payload={...f.session.payload,role_capabilities:['admin:access','content:write']};const count=f.calls.length;
  f.emit('[data-wechat-config]','submit');await tick();assert.equal(f.calls.length,count);assert.equal(f.get('[data-wechat-recovery]').hidden,false);
  assert.match(f.get('[data-wechat-status]').textContent,/没有公众号设置权限/);
});

test('expiry during reauthentication keeps collection disabled after login recovery',async()=>{
  const f=await ready();await collect(f);const expiry=[...f.timers.values()].find(timer=>timer.ms>60000);
  f.session.status=401;f.emit('[data-wechat-apply]','click');await tick();
  // The preserved report is now expired, so a login refresh must not re-enable import.
  const actualNow=Date.now;Date.now=()=>actualNow()+700000;
  try {
    expiry.fn();f.session.status=200;f.emit('[data-wechat-recover]','click');await tick();f.calls.at(-1).respond(workspace());await tick();
    assert.equal(f.get('[data-wechat-apply]').disabled,true);assert.match(f.get('[data-wechat-apply-note]').textContent,/预览已过期/);
  }finally{Date.now=actualNow;}
});

test('a failed record refresh after confirmed config save cannot claim the workspace is ready',async()=>{
  const f=await ready(),form=f.get('[data-wechat-config]');form.elements.author.value='saved author';f.emit('[data-wechat-config]','input');
  f.emit('[data-wechat-config]','submit');await tick();f.calls.at(-1).respond({config:{...workspace().config,version:3,author:'saved author'}});await tick();
  f.calls.at(-1).respond({error:{message:'read unavailable'}},503);await tick();
  assert.match(f.get('[data-wechat-status]').textContent,/保存已确认.*尚未读取成功/);assert.equal(form.elements.author.value,'saved author');
});

test('pagehide during publication confirmation cancels the action and never sends later',async()=>{
  const f=fixture();f.calls[0].respond(workspace({config:{...workspace().config,auto_publish:true},posts:[{id:1,version:1,title:'publishable',status:'publish'}]}));await tick();
  f.get('[data-wechat-sync]').elements.post_id.value='1';f.emit('[data-wechat-sync]','submit');await tick();
  assert.equal(f.get('[data-wechat-confirm]').open,true);const count=f.calls.length;f.winEvents.pagehide();await tick();
  assert.equal(f.get('[data-wechat-confirm]').open,false);assert.equal(f.calls.length,count);
});

function departAtConfirmation(f, timing) {
  const dialog = f.get('[data-wechat-confirm]');
  if (timing === 'resolved-before-departure') dialog.close('confirm');
  else { dialog.open = false; dialog.returnValue = 'confirm'; }
  f.winEvents.pagehide();
  if (timing === 'queued-after-return') f.winEvents.pageshow({persisted: true});
  if (timing !== 'resolved-before-departure') dialog.events.close();
}

for (const timing of ['resolved-before-departure', 'queued-after-departure', 'queued-after-return']) {
  for (const action of ['sync', 'publish']) test(`confirmation lifetime: ${action} is not sent when ${timing}`, async () => {
    const f = fixture();
    const job = {id: 'lifecycle-job', title: '待发表草稿', app_id: 'wx0123456789abcdef', auto_publish: false, status: 'draft', updated_at: new Date().toISOString()};
    f.calls[0].respond(workspace({config: {...workspace().config, auto_publish: true}, posts: [{id: 1, version: 1, title: '等待同步', status: 'publish'}], jobs: [job]})); await tick();
    if (action === 'sync') { f.get('[data-wechat-sync]').elements.post_id.value = '1'; f.emit('[data-wechat-sync]', 'submit'); }
    else f.emit('[data-wechat-jobs]', 'click', {target: f.get('[data-wechat-jobs]').querySelectorAll('button').find(node => node.dataset.action === 'publish')});
    await tick(); assert.equal(f.get('[data-wechat-confirm]').open, true);
    const count = f.calls.length; departAtConfirmation(f, timing); await tick();
    assert.equal(f.calls.length, count, 'a confirmed action from the departed page must never create a new request');
    assert.equal(f.get('[data-wechat-sync]').elements.post_id.value, action === 'sync' ? '1' : '');
  });
  for (const action of ['clear', 'disable']) test(`confirmation lifetime: collection draft survives ${action} when ${timing}`, async () => {
    const f = await savedCollection(), before = f.stored.get(recoveryKey), input = f.get('[data-wechat-collect]').elements.urls.value;
    if (action === 'clear') f.emit('[data-wechat-draft-clear]', 'click');
    else { f.get('[data-wechat-draft-enable]').checked = false; f.emit('[data-wechat-draft-enable]', 'change'); }
    assert.equal(f.get('[data-wechat-confirm]').open, true);
    departAtConfirmation(f, timing); await tick();
    assert.equal(f.stored.get(recoveryKey), before, 'a stale dialog result must not delete the retained collection copy');
    assert.equal(f.get('[data-wechat-collect]').elements.urls.value, input);
  });
}

test('confirmation lifetime: an ordinary confirmed automatic sync sends once', async () => {
  const f = fixture(); f.calls[0].respond(workspace({config: {...workspace().config, auto_publish: true}, posts: [{id: 1, version: 2, title: '正式文章', status: 'publish'}]})); await tick();
  f.get('[data-wechat-sync]').elements.post_id.value = '1'; f.emit('[data-wechat-sync]', 'submit'); await tick();
  const count = f.calls.length; f.get('[data-wechat-confirm]').close('confirm'); await tick();
  assert.equal(f.calls.length, count + 1); assert.deepEqual(f.calls.at(-1).body, {post_id: 1, version: 2, config_version: 2});
});

test('confirmation lifetime: ordinary cancellation retains collection recovery', async () => {
  const f = await savedCollection(), before = f.stored.get(recoveryKey);
  f.emit('[data-wechat-draft-clear]', 'click'); f.get('[data-wechat-confirm]').close('cancel'); await tick();
  assert.equal(f.stored.get(recoveryKey), before);
});

test('failed preview refresh preserves reviewed articles, choices and import results',async()=>{
  const f=await ready();await collect(f);
  const checks=f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]');checks[1].checked=false;checks[1].events.change();
  const results=f.get('[data-wechat-import-results]');results.append({tagName:'a',children:[],textContent:'previous import'});
  f.emit('[data-wechat-collect]','submit');await tick();
  assert.equal(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').length,2);
  f.calls.at(-1).reject(new TypeError('network failure'));await tick();
  assert.deepEqual(checks.map(c=>c.checked),[true,false]);assert.equal(results.children.length,1);
  assert.equal(f.get('[data-wechat-apply]').disabled,false);
});

test('successful refresh retains unchecked choices but requires changed articles to be selected again',async()=>{
  const f=await ready();await collect(f);
  const checks=f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]');checks[1].checked=false;checks[1].events.change();
  f.emit('[data-wechat-collect]','submit');await tick();
  const newer=preview();newer.articles[0].preview='<p>正文已变化</p>';newer.receipts.one='new-receipt';
  f.calls.at(-1).respond(newer);await tick();
  const current=f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]');
  assert.deepEqual(current.map(c=>c.checked),[false,false]);
  assert.match(f.get('[data-wechat-preview-state]').textContent,/1 篇.*更新/);
  current[0].checked=true;current[0].events.change();f.emit('[data-wechat-apply]','click');await tick();
  assert.deepEqual(f.calls.at(-1).body.articles.map(a=>a.key),['one']);assert.equal(f.calls.at(-1).body.receipts.one,'new-receipt');
});

test('unchanged preview refresh preserves expanded article and selected choices',async()=>{
  const f=await ready();await collect(f);
  const details=f.get('[data-wechat-collection]').querySelectorAll('details');details[0].open=true;
  const checks=f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]');checks[1].checked=false;checks[1].events.change();
  f.emit('[data-wechat-collect]','submit');await tick();f.calls.at(-1).respond(preview());await tick();
  assert.equal(f.get('[data-wechat-collection]').querySelectorAll('details')[0].open,true);
  assert.deepEqual(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').map(c=>c.checked),[true,false]);
});

test('malformed preview responses never replace the usable reviewed batch',async()=>{
  for(const corrupt of [data=>{data.articles[0].images=null;},data=>{data.articles[1].key='one';},data=>{data.plan.items[1].key='one';},data=>{data.articles[0].url='javascript:alert(1)';},data=>{data.plan.items[0].action='review';}]){
    const f=await ready();await collect(f);const original=f.get('[data-wechat-collection]').children.slice();
    f.emit('[data-wechat-collect]','submit');await tick();const malformed=preview();corrupt(malformed);f.calls.at(-1).respond(malformed);await tick();
    assert.deepEqual(f.get('[data-wechat-collection]').children,original);
    assert.match(f.get('[data-wechat-status]').textContent,/预览响应不完整/);assert.equal(f.get('[data-wechat-apply]').disabled,false);
  }
});

test('duplicate import acknowledgement keys cannot consume an unacknowledged selection',async()=>{
  const f=await ready();await collect(f);f.emit('[data-wechat-apply]','click');await tick();
  f.calls.at(-1).respond({applied:true,plan:{new:2,existing:0,conflicts:0,items:[{key:'one',title:'one',post_id:31,action:'created'},{key:'one',title:'one',post_id:31,action:'created'}]}});await tick();
  assert.match(f.get('[data-wechat-status]').textContent,/回执不完整/);
  assert.deepEqual(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').map(c=>c.checked),[true,true]);
  assert.equal(f.get('[data-wechat-import-results]').children.length,0);
});

test('collection permission revoked after rendering prevents preview and import requests',async()=>{
  const f=await ready();await collect(f);
  f.session.payload={...f.session.payload,role_capabilities:['admin:access','settings:manage','content:write','taxonomy:manage']};
  const count=f.calls.length;f.emit('[data-wechat-apply]','click');await tick();assert.equal(f.calls.length,count);
  f.emit('[data-wechat-collect]','submit');await tick();assert.equal(f.calls.length,count);
  assert.match(f.get('[data-wechat-status]').textContent,/采集.*权限/);assert.equal(f.get('[data-wechat-config]').fieldset.disabled,false);
});

test('revoked content permission blocks a visible publication action before confirmation',async()=>{
  const f=fixture();f.calls[0].respond(workspace({jobs:[{id:'draft-job',title:'draft',app_id:'wx0123456789abcdef',status:'draft'}]}));await tick();
  f.session.payload={...f.session.payload,role_capabilities:['admin:access','settings:manage']};
  const button=f.get('[data-wechat-jobs]').querySelectorAll('button').find(b=>b.dataset.action==='publish');const count=f.calls.length;
  f.emit('[data-wechat-jobs]','click',{target:button});await tick();
  assert.equal(f.calls.length,count);assert.notEqual(f.get('[data-wechat-confirm]').open,true);
  assert.match(f.get('[data-wechat-status]').textContent,/内容管理权限/);
});

test('a full refresh that cannot read any article retains the previous preview and its expiry',async()=>{
  const f=await ready();await collect(f);const expiry=f.get('[data-wechat-apply-note]').textContent.split('预览有效至 ')[1];
  const checks=f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]');checks[1].checked=false;checks[1].events.change();
  f.emit('[data-wechat-collect]','submit');await tick();
  f.calls.at(-1).respond({articles:[],failures:[{url:'https://mp.weixin.qq.com/s/firstx',message:'微信未提供文章内容'}]});await tick();
  assert.deepEqual(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').map(c=>c.checked),[true,false]);
  assert.equal(f.get('[data-wechat-apply-note]').textContent.split('预览有效至 ')[1],expiry);
  assert.match(f.get('[data-wechat-status]').textContent,/上次预览.*保留/);
});

test('changed links with a failed refresh keep the old batch blocked until restored',async()=>{
  const f=await ready();await collect(f);f.get('[data-wechat-collect]').elements.urls.value='https://mp.weixin.qq.com/s/different';f.get('[data-wechat-collect]').elements.urls.events.input();
  f.emit('[data-wechat-collect]','submit');await tick();f.calls.at(-1).respond({articles:[],failures:[{url:'https://mp.weixin.qq.com/s/different',message:'读取失败'}]});await tick();
  assert.equal(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').length,2);assert.equal(f.get('[data-wechat-apply]').disabled,true);
  f.get('[data-wechat-collect]').elements.urls.value='https://mp.weixin.qq.com/s/firstx';f.get('[data-wechat-collect]').elements.urls.events.input();
  assert.equal(f.get('[data-wechat-apply]').disabled,false);
});

test('invalid import counts or target identifiers preserve the full pending selection',async()=>{
  for(const corrupt of [data=>{data.plan.new=0;},data=>{data.plan.items[0].post_id=31.5;},data=>{data.plan.items[0].action='conflict';},data=>{data.applied='true';}]){
    const f=await ready();await collect(f);f.emit('[data-wechat-apply]','click');await tick();
    const result={applied:true,plan:{new:2,existing:0,conflicts:0,items:['one','two'].map((key,i)=>({key,title:key,post_id:31+i,action:'created'}))}};corrupt(result);f.calls.at(-1).respond(result);await tick();
    assert.match(f.get('[data-wechat-status]').textContent,/回执不完整/);assert.equal(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').length,2);
  }
});

test('replayed imports with preserved existing posts are accepted without duplicate create reports',async()=>{
  const f=await ready();await collect(f);f.emit('[data-wechat-apply]','click');await tick();
  f.calls.at(-1).respond({applied:true,plan:{new:0,existing:2,conflicts:0,items:['one','two'].map((key,i)=>({key,title:key,post_id:31+i,action:i?'changed':'skip'}))}});await tick();
  f.calls.at(-1).respond(workspace());await tick();
  assert.equal(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').length,0);
  assert.equal(f.get('[data-wechat-import-results]').querySelectorAll('a').length,2);
});

test('changed source and metadata keep edit links and never become import selections', async () => {
  const f = await ready();
  f.get('[data-wechat-collect]').elements.urls.value = 'https://mp.weixin.qq.com/s/firstx';
  f.emit('[data-wechat-collect]', 'submit'); await tick();
  const data = preview();
  data.articles.push({...data.articles[0], key:'three', title:'new article'});
  data.plan = {new:1, existing:2, conflicts:0, items:[
    {key:'one', action:'changed', post_id:31, message:'来源正文已变化，保留当前内容与修改'},
    {key:'two', action:'changed', post_id:32, message:'来源摘要或封面已变化，保留现有目标'},
    {key:'three', action:'create'}
  ]};
  data.receipts.three = 'receipt-three';
  f.calls.at(-1).respond(data); await tick();
  const collection = f.get('[data-wechat-collection]');
  assert.deepEqual(collection.querySelectorAll('input[type=checkbox]').map(n=>n.dataset.key), ['three']);
  assert.equal(collection.querySelectorAll('.wechat-imported-link').length, 2);
  assert.ok(collection.querySelectorAll('p').some(n=>n.textContent?.includes('来源正文已变化')));
  f.get('[data-wechat-select-all]').checked = true; f.emit('[data-wechat-select-all]', 'change');
  f.emit('[data-wechat-apply]', 'click'); await tick();
  assert.deepEqual(f.calls.at(-1).body.articles.map(a=>a.key), ['three']);
  assert.deepEqual(Object.keys(f.calls.at(-1).body.receipts), ['three']);
});

test('replay acknowledgements retain source change reasons beside the existing article', async () => {
  const f = await ready(); await collect(f);
  f.emit('[data-wechat-apply]', 'click'); await tick();
  const reasons = ['来源正文已变化，保留当前内容与修改', '来源摘要或封面已变化，保留现有目标'];
  f.calls.at(-1).respond({applied:true, plan:{new:0,existing:2,conflicts:0,items:['one','two'].map((key,i)=>({key,title:key,post_id:31+i,action:'changed',message:reasons[i]}))}}); await tick();
  f.calls.at(-1).respond(workspace()); await tick();
  const result = f.get('[data-wechat-import-results]');
  assert.deepEqual(result.querySelectorAll('.wechat-collect-state').map(n=>n.textContent), reasons);
  assert.equal(result.querySelectorAll('a').length, 2);
  assert.match(f.get('[data-wechat-status]').textContent, /本次未新建草稿/);
  assert.equal(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').length, 0);
});

test('a retried changed article merges without changing pending new article choices', async () => {
  const f = await ready();
  f.get('[data-wechat-collect]').elements.urls.value = 'https://mp.weixin.qq.com/s/firstx';
  f.emit('[data-wechat-collect]', 'submit'); await tick();
  const original = preview(); original.failures = [{url:'https://mp.weixin.qq.com/s/changed',message:'读取失败'}];
  f.calls.at(-1).respond(original); await tick();
  const checks = f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]');
  checks[1].checked=false; checks[1].events.change();
  f.get('[data-wechat-collection]').querySelectorAll('[data-wechat-retry]')[0].events.click(); await tick();
  const retried = {...original, articles:[{...original.articles[0],key:'changed',title:'changed article'}], failures:[], receipts:{}, plan:{new:0,existing:1,conflicts:0,items:[{key:'changed',action:'changed',post_id:40,message:'来源摘要或封面已变化，保留现有目标'}]}};
  f.calls.at(-1).respond(retried); await tick();
  assert.deepEqual(f.get('[data-wechat-collection]').querySelectorAll('input[type=checkbox]').map(n=>[n.dataset.key,n.checked]), [['one',true],['two',false]]);
  assert.equal(f.get('[data-wechat-collection]').querySelectorAll('details').length, 3);
  assert.equal(f.get('[data-wechat-collection]').querySelectorAll('.wechat-imported-link').length, 1);
});
