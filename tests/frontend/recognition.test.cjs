const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const crypto = require('node:crypto').webcrypto;

class Element {
  constructor() { this.dataset = {}; this.children = []; this.selectors = new Map(); this.listeners = new Map(); this.attributes = {}; this.style = { setProperty() {} }; this.value = ''; this.checked = false; this.disabled = false; this.hidden = false; }
  querySelector(key) { if (!this.selectors.has(key)) this.selectors.set(key, new Element()); return this.selectors.get(key); }
  querySelectorAll(key) { return key.startsWith('button') ? this.children.filter(e => e.tag === 'button') : []; }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; }
  setAttribute(key, value) { this.attributes[key] = value; }
  addEventListener(name, fn) { this.listeners.set(name, [...(this.listeners.get(name) || []), fn]); }
  emit(name, detail) { return Promise.all((this.listeners.get(name) || []).map(fn => fn({ preventDefault() {}, target: this, detail }))); }
  showModal() { this.open = true; }
  close() { this.open = false; this.emit('close'); }
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
  document.body = new Element();
  document.createElement = tag => Object.assign(new Element(), { tag });
  document.createElementNS = (namespaceURI,tag) => Object.assign(new Element(), { tag, namespaceURI });
  let session = { id: user, authenticated: true }, send = async body => ({ completed: true, request_id: body.request_id });
  const client = {
    getSession: async () => session,
    setSession: next => { session = next; },
    async request(url, options = {}) {
      requests.push({ url, ...options, body: options.body && { ...options.body } });
      if (options.method === 'POST') return send(options.body);
      return { user_id: user, enabled: true, settings_version: 3, hide_amounts: false, allow_hide_amount: true,
        currencies: [
          { key: 'main', name: '积分', symbol: '分', precision: 0, version: 2, balance: '10', min: '1', max: '100', presets: ['1', '3'] },
          { key: 'balance', name: '余额', symbol: '元', precision: 2, version: 4, balance: '20.00', min: '0.01', max: '100.00', presets: ['1.25', '3'] },
        ] };
    },
  };
  const sessionStorage = { getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key) };
  const window = { NewPressClient: { create: () => client }, addEventListener() {} };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../../internal/coreui/assets/js/newpress-recognition.source.js'), 'utf8'), { window, document, crypto, sessionStorage });
  return { root, dialog, form, fields, submit, storage, requests, recognition: window.NewPressRecognition,
    open: () => root.querySelector('[data-tip-open]').emit('click'),
    submitTip: () => form.emit('submit'),
    send: fn => { send = fn; },
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
  const f = fixture(); await f.open(); f.fields.amount.value = '30';
  f.send(async () => { throw Object.assign(new Error('余额不足'), { status: 409, code: 'currency.insufficient' }); });
  await f.submitTip();
  assert.equal(f.fields.amount.value, '30');
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
