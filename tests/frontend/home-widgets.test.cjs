const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const crypto = require('node:crypto').webcrypto;
const tick = () => new Promise(resolve => setImmediate(resolve));
const {compile}=require('./helpers/typescript.cjs');
const source = name => process.env.NEWPRESS_WALLET_TS==='1' ? compile(`frontend/${name==='chunnel-home-widgets'?'themes/chunnel':'shared'}/${name.replace('chunnel-','')}.ts`) : fs.readFileSync(path.join(__dirname, '../../themes/chunnel/assets/js', name + '.js'), 'utf8');
const receipt = (id='7') => ({ currency_key:'main', replayed:false, entry:{id,delta:'10',balance:'10',kind:'reward_check_in',label:'签到奖励',incoming:true,created_at:'2026-11-26T00:00:00Z'} });
const progressionReceipt = (owner='1') => ({replayed:false,entry:{id:'7',user_id:owner,actor_id:'0',delta:'10',balance:'10',kind:'reward_check_in',reason:'签到奖励',created_at:'2026-11-26T00:00:00Z'}});

class Element {
  constructor() { this.dataset = {}; this.textContent = ''; this.hidden = false; this.disabled = false; this.attributes = new Map(); this.listeners = new Map(); }
  setAttribute(key, value) { this.attributes.set(key, value); }
  removeAttribute(key) { this.attributes.delete(key); }
  addEventListener(name, callback) { this.listeners.set(name, callback); }
  emit(name) { return this.listeners.get(name)?.(); }
}

function fixture({ guest = false, unavailableStorage = false, refreshFails = false, sourceKind = 'wallet' } = {}) {
  const button = new Element(), status = new Element(), label = new Element(), note = new Element(), meta = new Element();
  const card = new Element(), storage = new Map(), listeners = new Map(), requests = [], mutations = [], logins = [];
  card.dataset.checkinSource = sourceKind;
  card.querySelector = selector => ({
    '[data-home-checkin]': button,
    '[data-home-checkin-status]': status,
    '[data-home-checkin-label]': label,
    '[data-home-checkin-note]': note,
    '[data-home-checkin-meta]': meta,
  })[selector] || null;
  let session = { authenticated: !guest, id: guest ? 0 : 1 }, ready = true, verification = false;
  const emitSession = value => { session = value; listeners.get('chunnel:session')?.({ detail: value }); };
  const document = {
    querySelector: selector => selector === '[data-home-checkin-card]' ? card : null,
    addEventListener: (name, callback) => listeners.set(name, callback),
    dispatchEvent: event => listeners.get(event.type)?.(event),
  };
  const window = { ChunnelTime: { formatDateTime: value => value }, ChunnelInteractions: {
    getSession: async () => { emitSession(session); return session; },
    signIn: async callback => { logins.push(callback); },
    api: (url, options = {}) => {
      requests.push({ url, options });
      if (refreshFails && !url.endsWith('/check-in') && mutations.length) return Promise.reject(new Error('Refresh failed'));
      if (url === '/api/v1/social/progression') return Promise.resolve({account:{user_id:String(session.id)},settings:{version:'2',experience:{enabled:true,name:'经验',require_verified_email:verification},check_in:{enabled:true}},verified_email:!verification,next_check_in:ready?'':new Date(Date.now()+86400000).toISOString()});
      if (url === '/api/v1/social/wallets/main') return Promise.resolve({
        user_id: String(session.id), currency_key:'main', check_in_ready: ready, verification_required: verification,
        next_check_in_at: '2026-11-13T00:00:00Z', settings: { version: '2', precision:0, enabled: true, check_in_enabled: true, name: '叶子' },
      });
      return new Promise((resolve, reject) => mutations.push({ options, resolve, reject }));
    },
  } };
  const sessionStorage = {
    getItem: key => { if (unavailableStorage) throw new Error('Storage disabled'); return storage.get(key) || null; },
    setItem: (key, value) => { if (unavailableStorage) throw new Error('Storage disabled'); storage.set(key, value); },
    removeItem: key => storage.delete(key),
  };
  const context = { window, document, sessionStorage, crypto, Uint8Array, Event };
  vm.runInNewContext(source('chunnel-currency-shared'), context);
  vm.runInNewContext(source('chunnel-home-widgets'), context);
  return { button, status, meta, storage, requests, mutations, logins, C: window.ChunnelCurrency,
    click: () => button.emit('click'), session: emitSession,
    ready: value => { ready = value; }, verification: value => { verification = value; },
  };
}

test('homepage check-in loads only on use and suppresses duplicate clicks', async () => {
  const f = fixture();
  assert.equal(f.requests.length, 0);
  const first = f.click(); await tick();
  await f.click();
  assert.equal(f.mutations.length, 1);
  assert.equal(f.button.attributes.get('aria-busy'), 'true');
  f.mutations[0].resolve(receipt()); await first;
  assert.equal(f.button.disabled, true);
  assert.equal(f.button.attributes.has('aria-busy'), false);
  assert.match(f.status.textContent, /签到成功.*10 叶子/);
  assert.equal(f.C.readPending('check-in', '1'), null);
  await f.click();
  assert.equal(f.mutations.length, 1);
});

for (const refreshFails of [false, true]) {
  test(`confirmed check-in uses server eligibility and survives refresh failure: ${refreshFails}`, async () => {
    const f = fixture({ refreshFails });
    const claim = f.click(); await tick();
    f.mutations[0].resolve(receipt()); await claim;
    assert.equal(f.button.disabled, true);
    assert.match(f.status.textContent, /签到成功/);
    assert.equal(f.C.readPending('check-in', '1'), null);
    assert.equal(f.meta.textContent, refreshFails ? '下次可签到时间暂未更新，请刷新查看。' : '下次可签：2026-11-13T00:00:00Z');
  });
}

for (const unavailableStorage of [false, true]) {
  test(`uncertain home check-in reuses its request even when storage ${unavailableStorage ? 'fails' : 'works'}`, async () => {
    const f = fixture({ unavailableStorage });
    const first = f.click(); await tick();
    const requestID = f.mutations[0].options.body.request_id;
    f.mutations[0].reject(new Error('连接中断')); await first;
    assert.equal(f.button.disabled, false);
    assert.equal(f.status.dataset.tone, 'error');
    // A lost receipt may already have consumed today's eligibility.
    f.ready(false);
    const retry = f.click(); await tick();
    assert.equal(f.mutations.length, 2);
    assert.equal(f.mutations[1].options.body.request_id, requestID);
    f.mutations[1].resolve(receipt()); await retry;
    assert.equal(f.button.disabled, true);
    assert.match(f.status.textContent, /签到成功/);
  });
}

test('guest check-in continues after login without sending an anonymous mutation', async () => {
  const f = fixture({ guest: true });
  await f.click();
  assert.equal(f.requests.length, 0);
  assert.equal(f.logins.length, 1);
  f.session({ authenticated: true, id: 2 });
  const resumed = f.logins[0](); await tick();
  assert.equal(f.mutations.length, 1);
  assert.ok(f.C.readPending('check-in', '2'));
  assert.equal(f.C.readPending('check-in', '1'), null);
  f.mutations[0].resolve(receipt('8')); await resumed;
  assert.equal(f.button.disabled, true);
});

test('a late check-in receipt cannot update another account', async () => {
  const f = fixture();
  const first = f.click(); await tick();
  f.session({ authenticated: true, id: 2 });
  f.mutations[0].resolve(receipt()); await first;
  assert.equal(f.button.disabled, false);
  assert.equal(f.status.textContent, '');
  assert.equal(f.C.readPending('check-in', '2'), null);
  assert.ok(f.C.readPending('check-in', '1'));
});

test('ineligible and unverified readers get a useful status without a mutation', async () => {
  const f = fixture();
  f.ready(false); await f.click();
  assert.equal(f.mutations.length, 0);
  assert.match(f.status.textContent, /已经领取/);
  const unverified = fixture();
  unverified.verification(true); await unverified.click();
  assert.equal(unverified.mutations.length, 0);
  assert.match(unverified.status.textContent, /验证邮箱/);
});

test('only a definite rejection clears an uncertain home check-in', async () => {
  const f = fixture();
  const first = f.click(); await tick();
  const requestID = f.mutations[0].options.body.request_id;
  f.mutations[0].resolve({ entry: {} }); await first;
  assert.equal(f.C.readPending('check-in', '1').request_id, requestID);
  const retry = f.click(); await tick();
  const error = new Error('设置已变化'); error.code = 'currency.settings_conflict';
  f.mutations[1].reject(error); await retry;
  assert.equal(f.C.readPending('check-in', '1'), null);
  const fresh = f.click(); await tick();
  assert.notEqual(f.mutations[2].options.body.request_id, requestID);
  f.mutations[2].resolve(receipt('9')); await fresh;
});

for (const sourceKind of ['wallet','progression']) {
  test(`${sourceKind} home retries keep each owner's nonce across account switches without storage`, async () => {
    const f=fixture({sourceKind,unavailableStorage:true});
    const first=f.click();await tick();const original=f.mutations[0].options.body.request_id;
    f.session({authenticated:true,id:2});
    const second=f.click();await tick();const other=f.mutations[1].options.body.request_id;
    assert.notEqual(original,other);
    f.session({authenticated:true,id:1});
    const retry=f.click();await tick();assert.equal(f.mutations[2].options.body.request_id,original);
    f.mutations[0].resolve(sourceKind==='wallet'?receipt():progressionReceipt());await first;
    assert.equal(f.button.disabled,false);assert.equal(f.button.attributes.get('aria-busy'),'true');
    f.mutations[1].reject(new Error('旧账号的迟到错误'));await second;
    assert.doesNotMatch(f.status.textContent,/旧账号/);
    f.mutations[2].resolve(sourceKind==='wallet'?receipt():progressionReceipt());await retry;
    assert.equal(f.button.disabled,true);assert.match(f.status.textContent,/签到成功/);
  });
  test(`${sourceKind} home ignores incomplete receipt and retries the original request`, async () => {
    const f=fixture({sourceKind});const first=f.click();await tick();const original=f.mutations[0].options.body.request_id;
    const invalid=sourceKind==='wallet'?receipt():progressionReceipt();delete invalid.replayed;
    f.mutations[0].resolve(invalid);await first;assert.equal(f.button.disabled,false);
    const retry=f.click();await tick();assert.equal(f.mutations[1].options.body.request_id,original);
    f.mutations[1].resolve(sourceKind==='wallet'?receipt():progressionReceipt());await retry;
    assert.equal(f.button.disabled,true);
  });
}

test('confirmed home progression reward survives failed metadata refresh',async()=>{
  const f=fixture({sourceKind:'progression',refreshFails:true});const claiming=f.click();await tick();
  f.mutations[0].resolve(progressionReceipt());await claiming;
  assert.equal(f.button.disabled,true);assert.match(f.status.textContent,/签到成功.*10 经验/);
  assert.match(f.meta.textContent,/暂未更新/);assert.equal(f.storage.has('chunnel.progression.v1.check-in.1'),false);
});
