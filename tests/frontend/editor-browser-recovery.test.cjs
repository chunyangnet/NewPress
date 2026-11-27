const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {webcrypto} = require('node:crypto');
const source = fs.readFileSync(path.join(__dirname, '../../internal/coreui/assets/js/chunnel-editor-recovery.js'), 'utf8');
const context = vm.createContext({window: {}, URLSearchParams, Date, Map, Set});
vm.runInContext(source, context);
const createStore = context.window.ChunnelEditorRecovery.createStore;
class Storage {
  data = new Map(); fail = false;
  get length() { return this.data.size; }
  key(index) { return [...this.data.keys()][index] ?? null; }
  getItem(key) { return this.data.get(key) ?? null; }
  setItem(key, value) { if (this.fail) throw new Error('quota'); this.data.set(key, value); }
  removeItem(key) { this.data.delete(key); }
}
const id = n => String(n).padStart(32, '0');
const now = 1794211200000;
const draft = (version = '') => [['title','标题'],['content_source','<p>later text 🎈</p>'],['type','post'],['status','draft'],['published_timezone','UTC'],['published_source','canonical'],['csrf_token','never-store-this'], ...(version ? [['version',version]] : [])];
const snapshot = (existing = false) => ({route: existing ? '/admin/content/42/edit/' : '/admin/content/new/', draft: draft(existing ? '2' : ''), pending: null});
const pending = (existing = false) => {
  const s = snapshot(existing), body = new URLSearchParams(s.draft); body.set('content_source','<p>original text</p>'); body.set('action','save');
  s.pending = {key:'editor-' + id(9), route:s.route, body:body.toString(), wasDirty:true, createdAt:now}; return s;
};

test('refresh and browser recreation recover later input and the exact original request without CSRF', () => {
  for (const existing of [false,true]) {
    const storage = new Storage(), first = createStore(storage,'id:1',72,()=>now);
    first.enable(true); first.write(id(1),pending(existing));
    assert.ok(![...storage.data.values()].join('').includes('never-store-this'));
    const reopened = createStore(storage,'id:1',72,()=>now+1000), copy = reopened.list()[0];
    assert.equal(reopened.enabled(),true); assert.equal(copy.pending.uncertain,true);
    assert.equal(copy.pending.key,'editor-' + id(9)); assert.equal(copy.pending.route,snapshot(existing).route);
    assert.equal(new URLSearchParams(copy.pending.body).get('content_source'),'<p>original text</p>');
    assert.equal(new URLSearchParams(copy.draft).get('content_source'),'<p>later text 🎈</p>');
    assert.equal(copy.pending.id,existing?42:0); assert.equal(copy.pending.version,existing?2:0);
    assert.equal(copy.pending.action,existing?'save':'create');
    assert.equal(JSON.parse(copy.pending.signature).content_source,'<p>original text</p>');
  }
});

test('copies and preferences are isolated by original identity and document', () => {
  const storage = new Storage(), a = createStore(storage,'id:1',72,()=>now), b = createStore(storage,'id:2',72,()=>now);
  a.enable(true); a.write(id(1),snapshot()); a.write(id(2),snapshot(true));
  assert.equal(b.enabled(),false); assert.equal(b.list().length,0);
  assert.equal(a.list('/admin/content/new/').length,1); assert.equal(a.list('/admin/content/42/edit/').length,1);
  b.clear(); assert.equal(a.list().length,2);
  assert.equal(createStore(storage,'login:author',72,()=>now).list().length,0);
});

test('article badge configuration survives browser recovery and original-write retries', () => {
  const storage = new Storage(), store = createStore(storage, 'id:1', 72, () => now);
  const value = pending(true);
  const badge = [['pinned', 'true'], ['article_badge_kind', 'custom'], ['article_badge_text', '<精选>😀'], ['article_badge_style', 'ribbon'], ['article_badge_tone', 'violet'], ['article_badge_icon', 'star-fill'], ['article_badge_placement', 'top-right'], ['article_badge_hide_mobile', 'true']];
  value.draft.push(...badge);
  const body = new URLSearchParams(value.pending.body);
  badge.forEach(([key, value]) => body.set(key, value));
  value.pending.body = body.toString();
  store.write(id(7), value);
  const copy = createStore(storage, 'id:1', 72, () => now + 1000).read(id(7));
  for (const [key, value] of badge) {
    assert.equal(new URLSearchParams(copy.draft).get(key), value);
    assert.equal(new URLSearchParams(copy.pending.body).get(key), value);
  }
});

test('two tabs keep independent copies and compare-before-delete preserves newer input', () => {
  const storage = new Storage(), store = createStore(storage,'id:1',72,()=>now);
  const original = store.write(id(1),snapshot()); store.write(id(2),snapshot());
  const newer = snapshot(); newer.draft[0][1]='newer in another tab'; store.write(id(3),newer,id(1));
  store.remove(id(1),original.raw); assert.equal(store.list().length,2);
  assert.equal(new URLSearchParams(store.read(id(3)).draft).get('title'),'newer in another tab');
  store.remove(id(2)); assert.equal(store.list().length,1);
});

test('snapshot keys are immutable even when a second writer has the same identity', () => {
  const storage=new Storage(),a=createStore(storage,'id:1',72,()=>now),b=createStore(storage,'id:1',72,()=>now);
  const original=a.write(id(1),snapshot());
  assert.throws(()=>b.write(id(1),pending()),/collision/);
  assert.equal(a.read(id(1)).raw,original.raw);
});

test('a pending request cannot renew its lifetime while later input changes', () => {
  const storage = new Storage(); let time=now;
  const store = createStore(storage,'id:1',1,()=>time), request=pending(); store.write(id(1),request);
  time+=3500000; request.draft[0][1]='near expiry'; store.write(id(2),request,id(1));
  assert.equal(store.read(id(2)).expires,now+3600000);
  time+=100001; assert.equal(store.list().length,0); assert.throws(()=>store.write(id(1),request));
});

test('normal drafts renew on edits, but the retention cap is six days', () => {
  const storage = new Storage(); let time=now;
  const store=createStore(storage,'id:1',720,()=>time); store.write(id(1),snapshot());
  time+=3600000; store.write(id(2),snapshot(),id(1));
  assert.equal(store.read(id(2)).expires,time+144*3600000);
  time+=144*3600000; assert.equal(store.list().length,0);
});

test('malformed or cross-scope records cannot become a retry', () => {
  for (const mutate of [
    x=>x.owner='id:2', x=>x.route='//evil.test/admin/content/new/', x=>x.route='/admin/content/99/edit/',
    x=>x.pending.route='/admin/content/99/edit/', x=>x.pending.key='another-key',
    x=>x.pending.body+='&csrf_token=secret', x=>x.pending.body+='&title=duplicate',
    x=>x.pending.body+='&version=9', x=>x.pending.body=x.pending.body.replace('action=save','action=trash'),
    x=>x.draft.push(['token','secret']), x=>x.pending.createdAt=now+100, x=>x.expires=now-1,
    x=>x.draft.push(['version','3']), x=>x.updated=now+100000,
    x=>x.draft=x.draft.filter(([name])=>name!=='published_timezone'),
    x=>x.draft=x.draft.filter(([name])=>name!=='published_source'),
    x=>x.pending.body=x.pending.body.replace('published_timezone=UTC','published_timezone='),
    x=>x.pending.body=x.pending.body.replace('published_source=canonical','published_source=unknown')
  ]) {
    const storage=new Storage(),store=createStore(storage,'id:1',72,()=>now); store.write(id(1),pending());
    const key=[...storage.data.keys()][0],value=JSON.parse(storage.getItem(key));mutate(value);storage.setItem(key,JSON.stringify(value));
    assert.equal(store.list().length,0); assert.equal(storage.length,0);
  }
});

test('quota and size failures preserve the last usable copy', () => {
  const storage=new Storage(),store=createStore(storage,'id:1',72,()=>now),original=store.write(id(1),snapshot());
  storage.fail=true; assert.throws(()=>store.write(id(2),pending(),id(1))); storage.fail=false;
  assert.equal(store.read(id(1)).raw,original.raw);
  const huge=snapshot();huge.draft[1][1]='中'.repeat(256*1024);assert.throws(()=>store.write(id(2),huge,id(1)),/size/);
  assert.equal(store.read(id(1)).raw,original.raw);
});

test('capacity rejects new copies without evicting another tab or identity', () => {
  const storage=new Storage(),a=createStore(storage,'id:1',72,()=>now),b=createStore(storage,'id:2',72,()=>now);
  for(let n=1;n<=8;n++)a.write(id(n),snapshot());
  assert.throws(()=>b.write(id(9),snapshot()),/capacity/);assert.equal(a.list().length,8);
  a.write(id(10),pending(),id(1));assert.equal(a.list().length,8);
  a.clear();assert.equal(a.enabled(),false);b.write(id(9),snapshot());assert.equal(b.list().length,1);
});

test('status actions retain the original action and source version', () => {
  const storage=new Storage(),store=createStore(storage,'id:1',72,()=>now),s=pending(true),body=new URLSearchParams(s.pending.body);
  body.set('action','restore_revision-3');s.pending.body=body.toString();store.write(id(1),s);
  const copy=store.read(id(1));assert.equal(copy.pending.action,'restore_revision');assert.equal(copy.pending.version,2);
  assert.equal(new URLSearchParams(copy.pending.body).get('action'),'restore_revision-3');
});

test('visibilitychange after acknowledged navigation cannot recreate a deleted copy', () => {
  const storage=new Storage(),store=createStore(storage,'id:1',72);store.enable(true);
  const listeners={},nodes={};
  const element=()=>({dataset:{},checked:false,addEventListener(){},replaceChildren(){},append(){}});
  for(const name of ['toggle','list','status','count','clear'])nodes['[data-browser-recovery-'+name+']']=element();
  const panel={hidden:true,querySelector:selector=>nodes[selector]};
  const browser={localStorage:storage,crypto:webcrypto,setTimeout:()=>1,clearTimeout(){},addEventListener(){}};
  const document={visibilityState:'visible',querySelector:()=>panel,addEventListener:(type,fn)=>{listeners[type]=fn;}};
  const scope=vm.createContext({window:browser,document,URLSearchParams,Date,Map,Set,Uint8Array});
  vm.runInContext(source,scope);
  let leaving=false;
  const mounted=browser.ChunnelEditorRecovery.mount({allowed:true,owner:'id:1',hours:72,route:()=>'/admin/content/new/',pending:()=>null,
    dirty:()=>true,persistable:()=>!leaving,snapshot:()=>snapshot(),changed(){}});
  mounted.flush();assert.equal(store.list().length,1);
  leaving=true;mounted.confirmed();assert.equal(store.list().length,0);
  document.visibilityState='hidden';listeners.visibilitychange();assert.equal(store.list().length,0);
});

test('disabled server policy clears only the current identity without enabling persistence', () => {
  const storage=new Storage(),a=createStore(storage,'id:1',72),b=createStore(storage,'id:2',72);
  a.enable(true);a.write(id(1),snapshot());b.enable(true);b.write(id(2),snapshot());
  const browser={localStorage:storage},scope=vm.createContext({window:browser,document:{querySelector:()=>null},URLSearchParams,Date,Map,Set});
  vm.runInContext(source,scope);
  assert.equal(browser.ChunnelEditorRecovery.mount({allowed:false,owner:'id:1',hours:72}),null);
  assert.equal(a.enabled(),false);assert.equal(a.list().length,0);
  assert.equal(b.enabled(),true);assert.equal(b.list().length,1);
});
