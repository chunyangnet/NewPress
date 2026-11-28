const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const nodeTest = require('node:test');
const vm = require('node:vm');
const { compile } = require('./helpers/typescript.cjs');
const tick = () => new Promise(resolve => setImmediate(resolve));

// Execute the complete entry and dispatch controller events without slicing source.
for (const variant of ['TypeScript', 'generated']) {
const source = variant === 'TypeScript' ? compile('frontend/shared/account.ts') : readFileSync('themes/chunnel/assets/js/chunnel-account.js', 'utf8');
const test = (name, fn) => nodeTest(variant + ': account ' + name, fn);
function fixture(view = 'like') {
  class Element {
    constructor(tag = 'div') { Object.assign(this, { tag, dataset: {}, attributes: {}, children: [], listeners: {}, selectors: new Map(), value: '', _text: '', hidden: false, disabled: false, replacements: 0 }); }
    get textContent() { return this._text + this.children.map(child => child.textContent || '').join(''); }
    set textContent(value) { this._text = value; this.children = []; }
    append(...children) { children.forEach(child => { child.parentElement = this; this.children.push(child); }); }
    replaceChildren(...children) { this._text = ''; this.children = children; this.replacements++; }
    insertBefore(child, reference) { const index = this.children.indexOf(reference); child.parentElement = this; this.children.splice(index < 0 ? this.children.length : index, 0, child); }
    before(child) { this.parentElement?.insertBefore(child, this); }
    setAttribute(name, value) { this.attributes[name] = value; }
    getAttribute(name) { return this.attributes[name] || null; }
    removeAttribute(name) { delete this.attributes[name]; }
    addEventListener(name, listener) { this.listeners[name] = listener; }
    querySelector(selector) { if (!this.selectors.has(selector)) this.selectors.set(selector, new Element()); return this.selectors.get(selector); }
    querySelectorAll() { return []; }
    contains() { return false; }
    focus() {}
    reset() { for (const field of Object.values(this.elements)) field.value = ''; }
    emit(type) { return this.listeners[type]?.({ target: this, currentTarget: this, preventDefault() {}, stopPropagation() {} }); }
  }
  const elements = new Map(), query = selector => { if (!elements.has(selector)) elements.set(selector, new Element()); return elements.get(selector); };
  const tabs = ['bookmark','feed','like','follow','profile','posts','comments','import'].map(kind => { const tab = new Element(); tab.dataset.libraryTab = kind; return tab; });
  const navigation = query('.account-navigation'); navigation.append(...tabs);
  navigation.selectors.set('[data-library-tab="profile"]', tabs.find(tab => tab.dataset.libraryTab === 'profile'));
  const previous = query('[data-library-page="-1"]'), next = query('[data-library-page="1"]'); previous.dataset.libraryPage = '-1'; next.dataset.libraryPage = '1';
  const root = new Element(); root.dataset.accountUser = '2'; root.querySelector = query;
  root.querySelectorAll = selector => selector.includes('[data-library-tab]') ? navigation.children : selector === '[data-library-page]' ? [previous, next] : [];
  for (const [selector, keys] of [['library-search',['q','collection']],['profile-form',['username','display_name','bio','locale','version','remove_avatar']],['avatar-form',['file']],['collection-form',['collection']]]) {
    const form = query(`[data-${selector}]`);
    form.elements = Object.fromEntries(keys.map(key => [key,new Element('input')]));
    Object.defineProperty(form.elements,'namedItem',{ value(name) { return this[name] || null; } });
  }
  const calls = [], errors = [], events = {}, historyCalls = [], scrollEvents = [];
  let reloads = 0, session = { id: 2, authenticated: true };
  const window = { innerWidth: 1280, addEventListener() {},
    ChunnelInteractions: { api(url, options) { return new Promise((resolve,reject) => calls.push({url,options,resolve,reject})); }, getSession: async () => session, run: action => Promise.resolve().then(action).catch(error => errors.push(error)) },
    ChunnelEngagement: { set: async () => true },
    ChunnelScroll: { keep(change) { const result = change(); scrollEvents.push(root.dataset.accountView); return result; } },
  };
  vm.runInNewContext(source, { window, document: { querySelector: () => root, createElement: tag => new Element(tag), createElementNS: (_,tag) => new Element(tag), addEventListener(name,listener) { events[name] = listener; }, activeElement:null },
    HTMLInputElement:Element, HTMLSelectElement:Element, HTMLTextAreaElement:Element, Element, Error, URL, URLSearchParams, setTimeout, clearTimeout, setTimeout, clearTimeout,
    location: { href:'https://example.test/account/?view='+view, reload() { reloads++; } }, history: { state:null, replaceState(...args) { historyCalls.push(args); } } });
  const form = query('[data-library-search]'), list = query('[data-library-list]'), status = query('[data-library-status]');
  const response = (titles=['新内容']) => ({ pages:2, total:titles.length, collections:{}, items:titles.map((title,i) => ({ id:i+1,title,url:'/posts/'+i,cover_url:'',excerpt:'摘要',collection:'',author_name:'作者',author_url:'/authors/1/',published_at:'2026-11-26T00:00:00Z' })) });
  return { root, form, list, status, query, calls, errors, historyCalls, scrollEvents, response,
    async ready(titles) { await tick(); calls[0].resolve(response(titles)); await tick(); },
    async respond(index,titles) { calls[index].resolve(response(titles)); await tick(); },
    async load() { form.emit('submit'); await tick(); },
    async tab(name) { tabs.find(tab=>tab.dataset.libraryTab===name).emit('click'); await tick(); },
    async next() { next.emit('click'); await tick(); },
    session(next) { session=next; events['chunnel:session']({detail:next}); }, reloads:()=>reloads,
  };
}

test('refresh preserves rows and pagination until atomic replacement', async () => {
  const f=fixture(); await f.ready(['旧内容']); const entry=f.list.children[0], replacements=f.list.replacements;
  await f.load(); assert.equal(f.list.children[0],entry); assert.equal(f.list.replacements,replacements);
  assert.equal(f.list.attributes['aria-busy'],'true'); assert.equal(f.status.dataset.state,'refreshing'); assert.equal(f.query('[data-library-pagination]').hidden,false);
  await f.respond(1,['更新一','更新二']); assert.equal(f.list.replacements,replacements+1); assert.equal(f.list.children.length,2);
  assert.match(f.list.textContent,/更新一/); assert.equal(f.list.attributes['aria-busy'],undefined);
});
for (const [name,change] of [['view', f=>f.tab('feed')],['search', async f=>{ f.form.elements.q.value='新的关键词'; await f.load(); }],['page', f=>f.next()]]) {
  test(`changing ${name} keeps stale rows inert while loading`, async () => {
    const f=fixture(); await f.ready(); const old=f.list.children[0]; await change(f);
    assert.equal(f.list.children[0],old); assert.equal(f.list.inert,true); assert.equal(f.status.dataset.state,'refreshing'); assert.match(f.status.textContent,/上次结果/);
    await f.respond(1); assert.equal(f.list.inert,false); assert.notEqual(f.list.children[0],old);
  });
}
test('old responses cannot replace the active view or clear its busy state', async () => {
  const f=fixture(); await tick(); await f.tab('feed'); await f.respond(0,['过期数据']);
  assert.equal(f.list.children.length,0); assert.equal(f.list.attributes['aria-busy'],'true'); await f.respond(1,['当前数据']); assert.match(f.list.textContent,/当前数据/);
});
test('failed and malformed refreshes retain content; confirmed empty response clears it', async () => {
  const f=fixture(); await f.ready(['旧内容']); await f.load(); f.calls[1].reject(new Error('网络不可用')); await tick();
  assert.match(f.list.textContent,/旧内容/); assert.equal(f.status.dataset.state,'error'); assert.equal(f.query('[data-library-total]').textContent,'更新失败，保留上次内容');
  await f.load(); f.calls[2].resolve({pages:1,total:0,items:null}); await tick(); assert.match(f.list.textContent,/旧内容/); assert.equal(f.status.dataset.state,'error');
  await f.load(); await f.respond(3,[]); assert.equal(f.list.children.length,0); assert.equal(f.status.dataset.state,'empty');
});
test('utility tabs clear the library and ignore outstanding responses', async () => {
  const f=fixture(); await f.ready(); await f.load(); await f.tab('import'); assert.equal(f.list.children.length,0);
  await f.respond(1); assert.equal(f.list.children.length,0); assert.equal(f.list.attributes['aria-busy'],undefined);
});
test('switching identity hides private panels, clears rows and suppresses old responses', async () => {
  const f=fixture(); await f.ready(); await f.load(); f.session({authenticated:true,id:3}); await f.respond(1);
  assert.equal(f.root.hidden,true); assert.equal(f.list.children.length,0); assert.equal(f.reloads(),1);
});
test('duplicate tabs skip requests and changed tabs use the scroll helper', async () => {
  const f=fixture(); await f.ready(); await f.tab('like'); assert.equal(f.calls.length,1);
  f.scrollEvents.length=0; await f.tab('feed'); assert.ok(f.scrollEvents.includes('feed')); assert.equal(f.calls.length,2); assert.match(f.historyCalls.at(-1)[2],/view=feed/);
});
test('profile validates identity/version and keeps local edits on malformed saves', async () => {
  const f=fixture('profile'); await tick(); const dto={id:2,version:1,username:'reader',display_name:'读者',bio:'简介',avatar_url:'',locale:'zh-CN'};
  f.calls[0].resolve(dto); await tick(); const profile=f.query('[data-profile-form]'); assert.equal(profile.elements.version.value,'1');
  profile.elements.display_name.value='新昵称'; profile.emit('submit'); await tick(); f.calls[1].resolve({...dto,id:3,version:2}); await tick();
  assert.equal(profile.elements.version.value,'1'); assert.equal(profile.elements.display_name.value,'新昵称'); assert.match(f.query('[data-profile-result]').textContent,/账号已变化/);
  profile.emit('submit'); await tick(); f.calls[2].resolve({...dto,display_name:'新昵称',version:2}); await tick(); assert.equal(profile.elements.version.value,'2'); assert.equal(f.query('[data-profile-heading]').textContent,'新昵称');
});
}
