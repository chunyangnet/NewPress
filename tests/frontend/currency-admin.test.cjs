'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const crypto = require('node:crypto').webcrypto;
const {compile}=require('./helpers/typescript.cjs');
const source = name => process.env.NEWPRESS_WALLET_TS==='1' ? compile(name==='chunnel-currency-shared'?'frontend/shared/currency-shared.ts':'frontend/core/currency-admin.ts') : fs.readFileSync(path.join(__dirname, '../../internal/coreui/assets/js/', name + '.js'), 'utf8');
const tick = () => new Promise(resolve => setImmediate(resolve));

class Element {
  constructor() { this.isConnected = true; this.readOnly = false; this.dataset = {}; this.value = ''; this.defaultValue = ''; this.checked = false; this.defaultChecked = false; this.hidden = false; this.disabled = false; this.children = []; this.listeners = new Map(); this.selectors = new Map(); this.groups = new Map(); this.textContent = ''; }
  toggleAttribute(name,value) { this[name] = value; }
  querySelector(selector) { if (!this.selectors.has(selector)) this.selectors.set(selector, new Element()); return this.selectors.get(selector); }
  closest(selector) { return this.querySelector('closest:' + selector); }
  querySelectorAll(selector) { return this.groups.get(selector) || []; }
  addEventListener(name, fn) { this.listeners.set(name, [...(this.listeners.get(name) || []), fn]); }
  emit(name, extras = {}) { return Promise.all((this.listeners.get(name) || []).map(fn => fn({ preventDefault() {}, currentTarget: this, ...extras }))); }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.children = nodes; }
}
const definitions = {
  identity: ['enabled','name','symbol','precision','max_balance'],
  transfers: ['allow_transfers','min_transfer','max_transfer','daily_transfer_limit'],
  'check-in': ['check_in_enabled','check_in_reward','check_in_interval_hours','check_in_mode','check_in_timezone'],
  registration: ['registration_enabled','registration_reward'],
  publication: ['publication_enabled','publication_reward','publication_min_length','publication_daily_limit'],
  'reward-policy': ['require_verified_email','daily_reward_limit','daily_issuance_limit'],
};
const initial = { check_in_mode:'interval', check_in_timezone:'Asia/Shanghai', version:'2', enabled:true, name:'积分', symbol:'分', precision:2, precision_locked:false, max_balance:'1000000.00', allow_transfers:true, min_transfer:'0.01', max_transfer:'100.00', daily_transfer_limit:'200.00', check_in_enabled:true, check_in_reward:'0.10', check_in_interval_hours:24, registration_enabled:true, registration_reward:'1.00', publication_enabled:true, publication_reward:'0.50', publication_min_length:100, publication_daily_limit:5, require_verified_email:true, daily_reward_limit:'10.00', daily_issuance_limit:'1000.00' };
Object.assign(initial,{registration_since:'',publication_since:''});

function fixture({ currency = 'main', pending, creation } = {}) {
  const root = new Element(), requests = [], storage = new Map(), navigations = [], confirmations = [];
  root.dataset = { currencyKey:currency, userId:'1', csrf:'csrf-test', readOnly:'false' };
  const get = selector => root.querySelector(selector), settings = get('[data-currency-settings]');
  settings.elements = {namedItem(name){return this[name]||null;}}; const fields = [], modules = {};
  for (const [key,names] of Object.entries(definitions)) {
    const element = new Element(); element.dataset.currencyModule = key;
    const own = names.map(name => {
      const field = new Element(); field.name = name;
      field.dataset.currencyField = typeof initial[name] === 'boolean' ? 'toggle' : typeof initial[name] === 'number' ? 'integer' : ['name','symbol','check_in_mode','check_in_timezone'].includes(name) ? 'text' : 'amount';
      field.value = field.defaultValue = String(initial[name]); field.checked = field.defaultChecked = initial[name] === true;
      settings.elements[name] = field; fields.push(field); return field;
    });
    element.groups.set('[data-currency-field]',own); modules[key] = element;
  }
  settings.groups.set('[data-currency-field]',fields); settings.groups.set('[data-currency-module]',Object.values(modules));
  const setupForm = (selector, names) => {
    const form = get(selector); form.elements = Object.fromEntries(names.map(name => { const e = new Element(); e.name = name; return [name,e]; })); form.elements.namedItem=function(name){return this[name]||null;}; return form;
  };
  settings.elements.version = new Element(); settings.elements.version.value = initial.version;
  const adjust = setupForm('[data-currency-adjust]',['user_id','delta','reason','version','request_id']);
  adjust.elements.version.value = '2'; adjust.elements.request_id.value = 'admin-adjust-stable-request';
  const create = setupForm('[data-currency-create]',['key','name','symbol','precision','request_id']);
  create.elements.precision.value = '0'; create.elements.request_id.value = 'admin-create-stable-request';
  setupForm('[data-currency-lookup]',['username']); setupForm('[data-currency-more-form]',['username','before']);
  const select = get('[data-currency-select]'); select.value = currency; select.options = ['main','gold'].map(value => ({ value, textContent:value }));
  let confirmation = true;
  const window = { location: { assign: url => navigations.push(url) }, addEventListener() {}, confirm: value => { confirmations.push(value); return confirmation; } };
  const document = { querySelector: () => root, createElement: () => new Element(), createTextNode: text => ({ textContent:text }), addEventListener() {} };
  const context = { window, document, crypto, Uint8Array, URLSearchParams, AbortController, setTimeout, clearTimeout, HTMLInputElement:Element, HTMLButtonElement:Element, HTMLTextAreaElement:Element, HTMLSelectElement:class {},
    sessionStorage: { getItem: key => storage.get(key) ?? null, setItem: (key,value) => storage.set(key,value), removeItem: key => storage.delete(key) },
    fetch(url, options) {
      return new Promise((resolve,reject) => {
        requests.push({ url, options, body:options.body ? JSON.parse(options.body) : null, reject,
          resolve: (data,status=200) => resolve({ ok:status>=200 && status<300, status, json:async () => data }) });
      });
    },
  };
  vm.runInNewContext(source('chunnel-currency-shared'),context);
  if (pending) window.ChunnelCurrency.savePending('adjust','1',pending,'main');
  if (creation) window.ChunnelCurrency.savePending('create','1',creation);
  vm.runInNewContext(source('chunnel-currency-admin'),context);
  return { root,get,settings,adjust,create,select,modules,requests,storage,navigations,confirmations,C:window.ChunnelCurrency,
    confirm(value) { confirmation=value; },
    submitModule(key) { return settings.emit('submit',{ submitter:Object.assign(new Element(),{name:'module',value:key}) }); },
    change(name,value) { const field=settings.elements[name]; if (typeof value==='boolean') field.checked=value; else field.value=String(value); return settings.emit('input'); },
  };
}

test('saving one module preserves other drafts and advances the version for the next save', async () => {
  const f=fixture({currency:'gold'});
  await f.change('check_in_reward','0.75'); await f.change('registration_reward','9.00');
  const saving=f.submitModule('check-in'); await tick();
  const request=f.requests.at(-1);
  assert.equal(request.url,'/api/v1/admin/currencies/gold/modules/check-in');
  assert.equal(request.options.method,'PATCH');
  assert.deepEqual(Object.keys(request.body.values).sort(),['check_in_enabled','check_in_interval_hours','check_in_mode','check_in_reward','check_in_timezone']);
  assert.equal(request.body.version,'2');
  request.resolve({currency_key:'gold',module:'check-in',settings:{...initial,version:'3',check_in_reward:'0.75'}}); await saving;
  assert.equal(f.settings.elements.registration_reward.value,'9.00');
  assert.equal(f.settings.elements.registration_reward.defaultValue,'1.00');
  assert.equal(f.modules.registration.querySelector('[data-currency-module-state]').textContent,'未保存');
  assert.equal(f.modules['check-in'].querySelector('[data-currency-module-state]').textContent,'已保存');
  assert.equal(f.modules['check-in'].querySelector('[data-currency-module-state]').dataset.dirty,'false');
  const second=f.submitModule('registration'); await tick();
  assert.equal(f.requests.at(-1).body.version,'3');
  f.requests.at(-1).resolve({currency_key:'gold',module:'registration',settings:{...initial,version:'4',check_in_reward:'0.75',registration_reward:'9.00'}}); await second;
  assert.equal(f.settings.elements.version.value,'4');
});

test('module conflicts preserve both the draft and its original version', async () => {
  const f=fixture(); await f.change('registration_reward','2.00');
  const saving=f.submitModule('registration'); await tick();
  f.requests.at(-1).resolve({error:{code:'currency.settings_conflict',message:'规则已更新'}},409); await saving;
  assert.equal(f.settings.elements.registration_reward.value,'2.00');
  assert.equal(f.settings.elements.version.value,'2');
  assert.match(f.modules.registration.querySelector('[data-currency-module-message]').textContent,/当前输入已保留/);
});

test('uncertain creation freezes and retries the original currency and request', async () => {
  const f=fixture();
  Object.assign(f.create.elements.key,{value:'gold'}); f.create.elements.name.value='金币'; f.create.elements.symbol.value='枚'; f.create.elements.precision.value='2';
  const first=f.create.emit('submit'); await tick();
  const original=f.requests.at(-1).body; f.requests.at(-1).reject(new Error('连接中断')); await first;
  assert.equal(f.create.elements.key.readOnly,true);
  assert.equal(f.C.readPending('create','1').key,'gold');
  f.create.elements.key.value='silver';
  const retry=f.create.emit('submit'); await tick();
  assert.deepEqual(f.requests.at(-1).body,original);
  f.requests.at(-1).resolve({currency_key:'gold',settings:{...initial,version:'1'},replayed:true}); await retry;
  assert.equal(f.C.readPending('create','1'),null);
  assert.deepEqual(f.navigations,['/admin/currency/?currency=gold']);
});

test('creation recovery is available after opening another currency without automatic submission', () => {
  const f=fixture({currency:'gold',creation:{key:'silver',name:'银币',symbol:'枚',precision:0,request_id:'recover-create-silver'}});
  assert.equal(f.requests.length,0);
  assert.equal(f.create.elements.key.value,'silver');
  assert.equal(f.create.elements.key.readOnly,true);
  assert.equal(f.get('[data-currency-create-panel]').open,true);
});

test('adjustment recovery is isolated and rejects a different currency receipt', async () => {
  const pending={user_id:'2',delta:'1.25',reason:'补发',precision:2,version:'2',request_id:'recover-adjust-main'};
  const gold=fixture({currency:'gold',pending});
  assert.equal(gold.adjust.elements.delta.value,'');
  assert.equal(gold.adjust.elements.delta.readOnly,false);
  const main=fixture({pending});
  assert.equal(main.adjust.elements.delta.value,'1.25');
  const sending=main.adjust.emit('submit'); await tick();
  main.requests.at(-1).resolve({currency_key:'gold',id:'20',delta:'1.25',balance:'1.25'}); await sending;
  assert.ok(main.C.readPending('adjust','1','main'));
  assert.equal(main.C.readPending('adjust','1','gold'),null);
});

test('precision changes require an explicit combined save when another module is dirty', async () => {
  const f=fixture(); await f.change('precision',3); await f.change('registration_reward','2.000');
  await f.submitModule('registration'); await f.submitModule('identity');
  assert.equal(f.requests.length,0);
  assert.match(f.modules.identity.querySelector('[data-currency-module-message]').textContent,/保存全部规则/);
  const saving=f.settings.emit('submit'); await tick();
  assert.equal(f.requests.at(-1).options.method,'PUT');
  assert.equal(f.requests.at(-1).body.settings.precision,3);
  f.requests.at(-1).resolve({currency_key:'main',settings:{...initial,version:'3',precision:3,registration_reward:'2.000'}}); await saving;
});

test('switching with unsaved modules requires a deliberate discard and never changes pending ownership', async () => {
  const f=fixture(); await f.change('name','新的名称');
  f.confirm(false); f.select.value='gold'; await f.select.emit('change');
  assert.equal(f.select.value,'main'); assert.equal(f.navigations.length,0);
  f.confirm(true); f.select.value='gold'; await f.select.emit('change');
  assert.deepEqual(f.navigations,['/admin/currency/?currency=gold']);
});

test('calendar schedule toggles fields and persists with its timezone', async () => {
  const f=fixture();
  await f.change('check_in_mode','calendar_day');
  assert.equal(f.settings.elements.check_in_interval_hours.closest('label').hidden,true);
  assert.equal(f.settings.elements.check_in_timezone.closest('label').hidden,false);
  const saving=f.submitModule('check-in'); await tick();
  const request=f.requests.at(-1);
  assert.equal(request.body.values.check_in_mode,'calendar_day');
  assert.equal(request.body.values.check_in_timezone,'Asia/Shanghai');
  request.resolve({currency_key:'main',module:'check-in',settings:{...initial,version:'3',check_in_mode:'calendar_day'}}); await saving;
  assert.equal(f.settings.elements.check_in_mode.value,'calendar_day');
  await f.change('check_in_mode','interval');
  assert.equal(f.settings.elements.check_in_interval_hours.closest('label').hidden,false);
  assert.equal(f.settings.elements.check_in_timezone.closest('label').hidden,true);
});

test('the currency key HTML pattern compiles with browser Unicode sets and enforces key boundaries', () => {
  const template=fs.readFileSync(path.join(__dirname,'../../internal/coreui/templates/admin-currency.html'),'utf8');
  const match=/<input\b[^>]*\bname="key"[^>]*\bpattern="([^"]+)"/u.exec(template);
  assert.ok(match, 'currency creation must declare its HTML key pattern');
  const pattern=new RegExp('^(?:'+match[1]+')$', 'v');
  for (const key of ['a','main','gold','reward-2026','a'+'1'.repeat(31)]) assert.ok(pattern.test(key),key);
  for (const key of ['','-gold','Gold','1gold','gold-','gold_1','../gold','göld','a'+'1'.repeat(32)]) assert.equal(pattern.test(key),false,key);
});
