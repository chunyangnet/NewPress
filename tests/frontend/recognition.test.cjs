const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const crypto = require('node:crypto').webcrypto;
const {compile} = require('./helpers/typescript.cjs');
const {tips} = require('./fixtures/recognition.cjs');
const script = process.env.NEWPress_GENERATED === '1' ? fs.readFileSync(path.join(__dirname, '../../internal/coreui/assets/js/newpress-recognition.js'), 'utf8') : compile('frontend/core/recognition.ts');

class Element {
  constructor() { this.isConnected = true; this.dataset = {}; this.children = []; this.selectors = new Map(); this.listeners = new Map(); this.attributes = {}; this.style = { setProperty() {} }; this.value = ''; this.checked = false; this.disabled = false; this.hidden = false; }
  querySelector(key) { if (!this.selectors.has(key)) this.selectors.set(key, new Element()); return this.selectors.get(key); }
  querySelectorAll(key) { return key.startsWith('button') ? this.children.filter(e => e.tag === 'button') : []; }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; }
  setAttribute(key, value) { this.attributes[key] = value; }
  hasAttribute(key) { return key === 'hidden' ? this.hidden : Object.hasOwn(this.attributes,key); }
  addEventListener(name, fn) { this.listeners.set(name, [...(this.listeners.get(name) || []), fn]); }
  emit(name, detail) { return Promise.all((this.listeners.get(name) || []).map(fn => fn({ preventDefault() {}, target: this, detail }))); }
  showModal() { this.open = true; }
  close() { if (!this.open) return; this.open = false; this.emit('close'); }
  focus() { this.focused = true; }
}

function fixture({ storage = new Map(), user = 2 } = {}) {
  const root = new Element(), document = new Element(), requests = [];
  root.dataset = { tipPost: '7', tipApi: '/api/v1/social/tips/7' };
  const dialog = root.querySelector('[data-tip-dialog]'), form = dialog.querySelector('form'), submit = new Element();
  submit.tag = 'button'; form.append(submit); form.selectors.set('[type=submit]', submit);
  const fields = Object.fromEntries(['currency', 'amount', 'hidden'].map(key => [key, new Element()]));
  form.elements = { namedItem: key => fields[key] };
  document.querySelector = key => key === '[data-article-tips]' ? root : null;
  document.querySelectorAll = key => key === '[data-article-tips]' ? [root] : [];
  document.body = new Element();
  document.createElement = tag => Object.assign(new Element(), { tag });
  document.createElementNS = (namespaceURI,tag) => Object.assign(new Element(), { tag, namespaceURI });
  let session = { id: user, authenticated: true, csrf_token:'test-token' }, send = async body => ({ completed: true, request_id: body.request_id, message:'打赏成功' }), read = async()=>tips(user), beforeSession;
  const subscribers = [];
  const client = {
    getSession: async () => {if(beforeSession)await beforeSession();subscribers.forEach(fn=>fn(session));return session;},
    setSession: next => { session = next; subscribers.forEach(fn=>fn(session)); },
    subscribe: fn => subscribers.push(fn), snapshot: () => session, cancel() {},
    async request(url, options = {}) {
      requests.push({ url, ...options, body: options.body && { ...options.body } });
      if (options.method === 'POST') return send(options.body);
      return read();
    },
  };
  const sessionStorage = { getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key) };
  const window = { NewPressClient: { create: () => client }, addEventListener() {} };
  vm.runInNewContext(script, { window, document, crypto, sessionStorage, Element, Node:Element, HTMLInputElement:Element, HTMLSelectElement:Element, HTMLTextAreaElement:Element });
  return { root, dialog, form, fields, submit, storage, requests, recognition: window.NewPressRecognition,
    open: () => root.querySelector('[data-tip-open]').emit('click'),
    submitTip: () => form.emit('submit'),
    send: fn => { send = fn; },
    read: fn => { read = fn; }, beforeSession: fn => { beforeSession = fn; }, sessionStorage,
    writes: () => requests.filter(r => r.method === 'POST'),
    session: next => document.emit('chunnel:session', next),
  };
}

test('tip retry preserves a charged request across network failure, CSRF failure and page reload', async () => {
  const f = fixture(); await f.open();
  f.fields.currency.value = 'balance'; await f.fields.currency.emit('change');
  f.fields.amount.value = '3.50';
  f.send(async () => { throw new Error('network lost after charge'); });
  await f.submitTip();
  const original = f.writes()[0].body;
  assert.equal(f.form.querySelector('fieldset').disabled, true);
  f.send(async () => { throw Object.assign(new Error('csrf rejected'), { status: 403, code: 'auth.csrf_invalid' }); });
  await f.submitTip();
  assert.equal(f.writes()[1].body.request_id, original.request_id);
  assert.equal(f.storage.size, 1);
  const next = fixture({ storage: f.storage }); await next.open();
  assert.equal(next.writes().length, 0, 'opening must never charge');
  assert.equal(next.fields.amount.value, '3.50');
  await next.submitTip();
  assert.equal(next.writes()[0].body.request_id, original.request_id);
  assert.equal(next.storage.size, 0);
  assert.equal(next.dialog.open, false);
});

test('medals expose an accessible icon button while labels and admin previews stay noninteractive', () => {
  const { recognition } = fixture();
  const design = { kind:'medal',name:'原创之星',description:'认真记录生活',icon:'award',color:'#b57525',style:'soft',granted_at:'2026-11-20T12:00:00Z',expires_at:'2026-12-20T12:00:00Z' };
  const visible = recognition.badge(design);
  assert.equal(visible.tag,'button'); assert.equal(visible.type,'button');
  assert.equal(visible.attributes['aria-haspopup'],'dialog');
  assert.match(visible.attributes['aria-label'],/原创之星/);
  assert.equal(visible.children.length,1);
  assert.equal(visible.children[0].children[0].tag,'svg');
  assert.equal(visible.dataset.identityExpires,design.expires_at);
  const preview=recognition.badge(design,{interactive:false});
  assert.equal(preview.tag,'span'); assert.equal(preview.dataset.medalDetails,undefined);
  const unsafe=recognition.badge({...design,image:'https://tracker.example/medal.png'});
  assert.equal(unsafe.children[0].children[0].tag,'svg');
});

test('only numbered levels receive LV artwork; membership names remain readable', () => {
  const { recognition } = fixture();
  const level=recognition.badge({kind:'level',name:'自定义资深称号',level:12,color:'#b57525'});
  assert.match(level.className,/identity-level/);
  assert.equal(level.children[0].children[1].textContent,'12');
  assert.match(level.attributes['aria-label'],/自定义资深称号/);
  const member=recognition.badge({kind:'membership',name:'年度会员',color:'#2563eb',icon:'star'});
  assert.doesNotMatch(member.className,/identity-level/);
  assert.equal(member.children[1].textContent,'年度会员');
});

test('insufficient balance retains editable input, while an unrelated receipt cannot clear a pending tip', async () => {
  const f = fixture(); await f.open(); f.fields.amount.value = '3';
  f.send(async () => { throw Object.assign(new Error('余额不足'), { status: 409, code: 'currency.insufficient' }); });
  await f.submitTip();
  assert.equal(f.fields.amount.value, '3');
  assert.equal(f.form.querySelector('fieldset').disabled, false);
  assert.equal(f.submit.disabled, false);
  assert.equal(f.storage.size, 0);
  f.fields.amount.value = '3';
  f.send(async () => ({ completed: true, request_id: 'a-different-payment' }));
  await f.submitTip();
  assert.notEqual(f.writes()[0].body.request_id, f.writes()[1].body.request_id);
  assert.equal(f.storage.size, 1);
  assert.equal(f.dialog.open, true);
});

test('tip confirmation snapshots inputs before the asynchronous session refresh',async()=>{
  const f=fixture();await f.open();f.fields.amount.value='3';let resume;
  f.beforeSession(()=>new Promise(resolve=>{resume=resolve;}));const sent=f.submitTip();
  f.fields.amount.value='8';resume();await sent;
  assert.equal(f.writes()[0].body.amount,'3');
});

test('closed dialogs discard delayed reads and keep an uncertain payment for later retry',async()=>{
  const f=fixture();let resume;f.read(()=>new Promise(resolve=>{resume=resolve;}));const opening=f.open();
  await new Promise(resolve=>setImmediate(resolve));f.dialog.close();resume(tips());await opening;
  assert.equal(f.form.hidden,true);
  f.read(async()=>tips());await f.open();let pay;f.send(body=>new Promise(resolve=>{pay=()=>resolve({completed:true,request_id:body.request_id,message:'打赏成功'});}));
  const sending=f.submitTip();await new Promise(resolve=>setImmediate(resolve));f.dialog.close();pay();await sending;
  assert.equal(f.storage.size,1);assert.equal(f.root.querySelector('[data-tip-result]').textContent,undefined);
});

test('an old-account completion cannot clear its recovery record or show success to the next account',async()=>{
  const f=fixture();await f.open();let complete;
  f.send(body=>new Promise(resolve=>{complete=()=>resolve({completed:true,request_id:body.request_id,message:'打赏成功'});}));
  const sending=f.submitTip();await new Promise(resolve=>setImmediate(resolve));await f.session({authenticated:true,id:3,csrf_token:'other'});complete();await sending;
  assert.equal(f.storage.size,1);assert.equal(f.root.querySelector('[data-tip-result]').textContent,'');assert.equal(f.dialog.open,false);
});

test('invalid stored tips block replay and extra stored fields never enter the payload',async()=>{
  const bad=fixture({storage:new Map([['newpress:tip:2:7',JSON.stringify({post_id:7,request_id:'old-payment-request',amount:'3'})]])});await bad.open();await bad.submitTip();assert.equal(bad.writes().length,0);
  const {pending}=require('./fixtures/recognition.cjs');const f=fixture({storage:new Map([['newpress:tip:2:7',JSON.stringify({...pending(),actor_id:99,csrf_token:'bad'})]])});await f.open();await f.submitTip();assert.equal(f.writes()[0].body.actor_id,undefined);assert.equal(f.writes()[0].body.csrf_token,undefined);
});

test('unavailable storage retains the original payment across close and reopen',async()=>{
  const f=fixture();f.sessionStorage.setItem=()=>{throw Error('disabled');};await f.open();f.send(async()=>{throw Error('lost');});await f.submitTip();const original=f.writes()[0].body.request_id;
  f.dialog.close();await f.open();await f.submitTip();assert.equal(f.writes()[1].body.request_id,original);
});

test('out-of-balance and wrong-owner tips never produce a write',async()=>{
  const f=fixture();await f.open();f.fields.amount.value='11';await f.submitTip();assert.equal(f.writes().length,0);
  f.dialog.close();f.read(async()=>tips(3));await f.open();await f.submitTip();assert.equal(f.writes().length,0);assert.equal(f.form.hidden,true);
});

test('the native form hidden property can be masked by the privacy field',async()=>{
  const f=fixture();await f.open();
  Object.defineProperty(f.form,'hidden',{get:()=>f.fields.hidden,set:value=>{if(value)f.form.attributes.hidden='';else delete f.form.attributes.hidden;}});
  f.form.hasAttribute=name=>Object.hasOwn(f.form.attributes,name);
  await f.submitTip();assert.equal(f.writes().length,1);assert.equal(f.dialog.open,false);
});

test('a business rejection on an uncertain retry cannot erase a possibly charged request',async()=>{
  const f=fixture();await f.open();f.send(async()=>{throw Error('lost');});await f.submitTip();const original=f.writes()[0].body;
  f.send(async()=>{throw Object.assign(Error('changed'),{code:'currency.settings_conflict',status:409});});await f.submitTip();
  assert.deepEqual(f.writes()[1].body,original);assert.equal(f.storage.size,1);assert.equal(f.form.querySelector('fieldset').disabled,true);
});

test('same-account session broadcasts preserve the original retry',async()=>{
  const f=fixture();await f.open();f.send(async()=>{throw Error('lost');});await f.submitTip();const original=f.writes()[0].body;
  await f.session({id:2,authenticated:true,csrf_token:'test-token'});assert.equal(f.dialog.open,true);await f.submitTip();assert.deepEqual(f.writes()[1].body,original);
});

test('wallet switching uses the selected currency precision and preserves decimal strings', async () => {
  const f = fixture(); await f.open(); f.fields.amount.value = '1.25';
  await f.submitTip(); assert.equal(f.writes().length, 0);
  f.fields.currency.value = 'balance'; await f.fields.currency.emit('change');
  assert.equal(f.fields.amount.value, '1.25');
  await f.submitTip();
  assert.equal(f.writes()[0].body.currency, 'balance');
  assert.equal(f.writes()[0].body.currency_version, 4);
  assert.equal(f.writes()[0].body.amount, '1.25');
});

test('account changes close the dialog and another account cannot take over a pending tip', async () => {
  const f = fixture(); await f.open();
  f.send(async () => { throw new Error('offline'); }); await f.submitTip();
  const request = f.writes()[0].body.request_id;
  await f.session({ id: 3, authenticated: true });
  assert.equal(f.dialog.open, false);
  const next = fixture({ storage: f.storage, user: 3 }); await next.open();
  assert.equal(next.form.querySelector('fieldset').disabled, false);
  await next.submitTip();
  assert.notEqual(next.writes()[0].body.request_id, request);
  assert.equal(JSON.parse(f.storage.get('newpress:tip:2:7')).request_id, request);
});
