const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const crypto = require('node:crypto').webcrypto;
const {compile}=require('./helpers/typescript.cjs');
const source = name => process.env.NEWPRESS_WALLET_TS==='1' ? compile(`frontend/shared/${name.replace('chunnel-','')}.ts`) : fs.readFileSync(path.join(__dirname, '../../themes/chunnel/assets/js/', name + '.js'), 'utf8');
const tick = () => new Promise(resolve => setImmediate(resolve));

class Element {
  constructor() { this.dataset = {}; this.textContent = ''; this.children = []; this.hidden = false; this.disabled = false; this.value = ''; this.defaultValue = ''; this.listeners = new Map(); this.selectors = new Map(); }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.children = nodes; this.textContent = ''; }
  querySelector(selector) { if (!this.selectors.has(selector)) this.selectors.set(selector, new Element()); return this.selectors.get(selector); }
  querySelectorAll() { return []; }
  addEventListener(name, callback) { this.listeners.set(name, [...(this.listeners.get(name) || []), callback]); }
  emit(name) { return Promise.all((this.listeners.get(name) || []).map(callback => callback({ preventDefault() {}, currentTarget: this }))); }
  showModal() { this.open = true; }
  close() { this.open = false; }
  reset() { for (const field of Object.values(this.elements || {})) field.value = field.defaultValue; }
}

function shared(storage = new Map()) {
  const document = { createElement: () => new Element(), createTextNode: text => ({ textContent: text }) };
  const sessionStorage = { getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key) };
  const window = {};
  const context = { window, document, sessionStorage, crypto, Uint8Array, URLSearchParams };
  vm.runInNewContext(source('chunnel-currency-shared'), context);
  return { C: window.ChunnelCurrency, context, storage };
}

const pendingTransfer = {
  owner: '1', recipient_id: '2', recipient: 'recipient', amount: '1.25', version: '2', precision: 2, symbol: '币', request_id: 'stable-currency-transfer-0001',
};

function walletResponse(balance = '10.00', currency = 'main') {
  return {
    currency_key: currency, user_id: '1', balance, username: 'reader', display_name: '读者', status: 'active',
    transferred_today: '0.00', transfer_remaining: '100.00', rewarded_today: '0.00', reward_remaining: '10.00',
    verification_required: false, check_in_ready: true, entries: [], next_before: '',
    settings: { version: '2', enabled: true, name: '社区币', symbol: '币', precision: 2, allow_transfers: true,
      min_transfer: '0.01', max_transfer: '100.00', daily_transfer_limit: '100.00', daily_reward_limit: '10.00',
      check_in_enabled: true, check_in_reward: '0.10', check_in_interval_hours: 24,
      registration_enabled: true, registration_reward: '1.00', publication_enabled: true, publication_reward: '0.50', publication_min_length: 100, publication_daily_limit: 5,
    },
  };
}

function walletFixture({ pending, deferWallet = false, currency = 'main', unavailableStorage = false } = {}) {
  const f = shared(), root = new Element(), room = new Element(), requests = [], deferred = [];
  const listeners = new Map(); let session = { id: 1, authenticated: true };
  if(unavailableStorage)for(const method of ['getItem','setItem','removeItem'])f.context.sessionStorage[method]=()=>{throw new Error('Storage unavailable');};
  room.dataset.accountUser = '1';
  const form = root.querySelector('[data-wallet-transfer]'); form.elements = { recipient: new Element(), amount: new Element() }; form.elements.namedItem=name=>form.elements[name];
  f.context.HTMLInputElement=Element;
  f.context.document.querySelector = selector => selector === '[data-currency-wallet]' ? root : selector === '[data-account-room]' ? room : null;
  f.context.document.addEventListener = (name, callback) => listeners.set(name, [...(listeners.get(name) || []), callback]);
  f.context.window.location = { search: '?currency=' + currency };
  f.context.window.ChunnelInteractions = {
    getSession: async () => session,
    api(url, options = {}) {
      const request = { path: url, options }; requests.push(request);
      if (url === '/api/v1/social/wallets') return Promise.resolve({ currencies: [{ key: 'main', name: '积分', enabled: true }, { key: 'gold', name: '金币', enabled: true }] });
      const key = url.split('/')[5]?.split('?')[0];
      if (url.endsWith('/sync')) return Promise.resolve({ currency_key: key, awarded: false });
      if (url.includes('/recipient?')) return Promise.resolve({ currency_key: key, id: '2', username: 'recipient' });
      if (/\/wallets\/(main|gold)$/.test(url) && !deferWallet) return Promise.resolve(walletResponse(key === 'gold' ? '20.00' : '10.00', key));
      return new Promise((resolve, reject) => { Object.assign(request, { resolve, reject }); deferred.push(request); });
    },
  };
  if (pending) f.C.savePending('transfer', '1', pending, currency);
  vm.runInNewContext(source('chunnel-wallet'), f.context);
  return { ...f, root, form, requests, deferred, load: f.context.window.ChunnelWallet.load,
    get: selector => root.querySelector(selector),
    session(value) { session = value; for (const callback of listeners.get('chunnel:session') || []) callback({ detail: value }); },
  };
}

test('currency decimal strings preserve int64 limits and reject ambiguous representations', () => {
  const { C } = shared();
  assert.equal(C.units('92233720368547758.07', 2), 9223372036854775807n);
  assert.equal(C.units('900719925474099.31', 2), 90071992547409931n);
  assert.equal(C.units('-900719925474099.31', 2, true), -90071992547409931n);
  assert.equal(C.units('0', 6), 0n);
  assert.equal(C.format(90071992547409931n, 2), '900719925474099.31');
  for (const value of ['92233720368547758.08', '1e3', '+1', '01', '.1', '1.', '1.001', ' 1', 'NaN', 'Infinity', '１', 1]) {
    assert.throws(() => C.units(value, 2), undefined, String(value));
  }
});

test('pending operations are immutable and isolated by account and operation type', () => {
  const { C, storage } = shared();
  const saved = C.savePending('transfer', '1', pendingTransfer);
  assert.ok(Object.isFrozen(saved));
  assert.equal(C.readPending('transfer', '1').request_id, pendingTransfer.request_id);
  assert.equal(C.readPending('transfer', '2'), null);
  assert.equal(C.readPending('adjust', '1'), null);
  for (const patch of [{ owner: '2' }, { amount: '1e5' }, { recipient_id: 2 }, { precision: 9 }, { request_id: 'short' }]) {
    storage.set('chunnel.currency.v2.main.transfer.1', JSON.stringify({ ...pendingTransfer, currency_key: 'main', ...patch }));
    assert.equal(C.readPending('transfer', '1'), null);
  }
});

test('opening a wallet restores an uncertain transfer without automatically resending it', async () => {
  const f = walletFixture({ pending: pendingTransfer });
  await f.load();
  assert.equal(f.requests.filter(r => r.path.endsWith('/transfer')).length, 0);
  assert.equal(f.form.elements.recipient.value, 'recipient');
  assert.equal(f.form.elements.amount.value, '1.25');
  assert.equal(f.form.elements.amount.readOnly, true);
  assert.match(f.get('[data-wallet-transfer-result]').textContent, /尚未确认/);
});

test('uncertain transfer retries retain recipient, amount, and request ID until a valid receipt', async () => {
  const f = walletFixture({ pending: pendingTransfer }); await f.load();
  f.form.elements.amount.value = '8.00';
  await f.form.emit('submit');
  assert.equal(f.get('[data-wallet-confirm]').open, true);
  const first = f.get('[data-wallet-confirm-send]').emit('click'); await tick();
  let request = f.deferred.at(-1);
  assert.equal(request.options.body.amount, '1.25');
  assert.equal(request.options.body.recipient_id, '2');
  request.reject(new Error('网络中断')); await first;
  assert.equal(f.C.readPending('transfer', '1').request_id, pendingTransfer.request_id);
  const retry = f.get('[data-wallet-confirm-send]').emit('click'); await tick();
  request = f.deferred.at(-1);
  assert.equal(request.options.body.request_id, pendingTransfer.request_id);
  request.resolve({ currency_key: 'main', entry: { id: '123', delta: '-1.25', kind: 'transfer_out', label:'转出', balance:'8.75', incoming:false, created_at:'2026-11-26T00:00:00Z' }, recipient_id: '2', replayed: true });
  await retry;
  assert.equal(f.C.readPending('transfer', '1'), null);
  assert.equal(f.get('[data-wallet-confirm]').open, false);
  assert.match(f.get('[data-wallet-transfer-result]').textContent, /#123/);
});

test('an unconfirmed receipt preserves the pending transfer, while a definite rejection releases it', async () => {
  const f = walletFixture({ pending: pendingTransfer }); await f.load(); await f.form.emit('submit');
  let send = f.get('[data-wallet-confirm-send]').emit('click'); await tick();
  f.deferred.at(-1).resolve({ currency_key: 'main', entry: { id: '124', delta: '-9.99', kind: 'transfer_out' }, recipient_id: '2' }); await send;
  assert.ok(f.C.readPending('transfer', '1'));
  send = f.get('[data-wallet-confirm-send]').emit('click'); await tick();
  f.deferred.at(-1).reject(Object.assign(new Error('规则已变化'), { code: 'currency.settings_conflict' })); await send;
  assert.equal(f.C.readPending('transfer', '1'), null);
  assert.equal(f.form.elements.amount.readOnly, false);
});

test('logout clears private wallet state and ignores a late wallet response', async () => {
  const f = walletFixture({ deferWallet: true });
  const loading = f.load(); await tick();
  const read = f.deferred.at(-1); assert.equal(read.path, '/api/v1/social/wallets/main');
  f.session({ authenticated: false });
  read.resolve(walletResponse('12345.67')); await loading;
  assert.equal(f.get('[data-wallet-balance]').textContent, '—');
  assert.equal(f.get('[data-wallet-history]').children.length, 0);
  assert.equal(f.form.querySelector('fieldset').disabled, true);
});

test('switching account ignores late transfer receipts without clearing the original account request', async () => {
  const f = walletFixture({ pending: pendingTransfer }); await f.load(); await f.form.emit('submit');
  const sending = f.get('[data-wallet-confirm-send]').emit('click'); await tick();
  const request = f.deferred.at(-1);
  f.session({ authenticated: true, id: 2 });
  request.resolve({ currency_key: 'main', entry: { id: '125', delta: '-1.25', kind: 'transfer_out' }, recipient_id: '2' }); await sending;
  assert.ok(f.C.readPending('transfer', '1'));
  assert.equal(f.C.readPending('transfer', '2'), null);
  assert.equal(f.get('[data-wallet-transfer-result]').textContent, '');
  assert.equal(f.get('[data-wallet-confirm-recipient]').textContent, '');
});

test('an uncertain check-in keeps its request ID and does not issue a background retry', async () => {
  const f = walletFixture(); await f.load();
  let claiming = f.get('[data-wallet-check-in]').emit('click'); await tick();
  const first = f.deferred.at(-1); const id = first.options.body.request_id;
  first.reject(new Error('断网')); await claiming;
  await f.load();
  assert.equal(f.requests.filter(r => r.path.endsWith('/check-in')).length, 1);
  claiming = f.get('[data-wallet-check-in]').emit('click'); await tick();
  const retry = f.deferred.at(-1); assert.equal(retry.options.body.request_id, id);
  retry.resolve({ currency_key: 'main', entry: { id: '126', delta: '0.10', kind: 'reward_check_in', label:'签到奖励', balance:'10.10', incoming:true, created_at:'2026-11-26T00:00:00Z' }, replayed: true }); await claiming;
  assert.equal(f.C.readPending('check-in', '1'), null);
});

test('ledger descriptions remain text even when peer names or reasons contain markup', () => {
  const { C } = shared(), container = new Element();
  C.history(container, [{ id: '1', incoming: true, delta: '1.00', balance: '1.00', label: '转入', peer: '<img src=x onerror=alert(1)>', reason: '<script>alert(1)</script>', created_at: '2026-11-12T00:00:00Z' }]);
  const all = []; const walk = element => { all.push(element); for (const child of element.children || []) walk(child); }; walk(container);
  assert.ok(all.some(element => element.textContent === '<script>alert(1)</script>'));
  assert.ok(all.some(element => element.textContent.includes?.('<img src=x onerror=alert(1)>')));
  assert.ok(all.every(element => !Object.hasOwn(element, 'innerHTML')));
});

test('currency namespaces isolate pending transfers, check-ins, and creation receipts', () => {
  const { C, storage } = shared();
  storage.set('chunnel.currency.v1.transfer.1', JSON.stringify(pendingTransfer));
  assert.equal(C.readPending('transfer', '1', 'main'), null);
  C.savePending('transfer', '1', pendingTransfer, 'main');
  assert.equal(C.readPending('transfer', '1', 'main').request_id, pendingTransfer.request_id);
  const gold = C.savePending('transfer', '1', { ...pendingTransfer, amount: '8.00' }, 'gold');
  assert.equal(gold.currency_key, 'gold');
  assert.equal(C.readPending('transfer', '1', 'gold').amount, '8.00');
  assert.equal(C.readPending('transfer', '1', 'main').amount, '1.25');
  C.clearPending('transfer', '1', 'gold');
  assert.ok(C.readPending('transfer', '1', 'main'));
  assert.equal(C.readPending('transfer', '1', 'gold'), null);
  assert.throws(() => C.savePending('transfer', '1', gold, 'main'));
  storage.set('chunnel.currency.v2.gold.transfer.1', JSON.stringify(pendingTransfer));
  assert.equal(C.readPending('transfer', '1', 'gold'), null);
  C.savePending('create', '1', { key: 'gold', name: '金币', symbol: '枚', precision: 2, request_id: 'stable-creation-request' });
  assert.equal(C.readPending('create', '1').key, 'gold');
  assert.equal(C.readPending('create', '2'), null);
  assert.equal(C.readPending('adjust', '1'), null);
});

test('switching currency clears new amounts and keeps uncertain requests in their original wallet', async () => {
  const f = walletFixture({ pending: pendingTransfer }); await f.load();
  const select = f.get('[data-wallet-currency]');
  select.value = 'gold'; await select.emit('change'); await tick();
  assert.equal(f.get('[data-wallet-balance]').textContent, '20.00');
  assert.equal(f.form.elements.amount.value, '');
  assert.equal(f.form.elements.amount.readOnly, false);
  assert.ok(f.C.readPending('transfer', '1', 'main'));
  assert.equal(f.C.readPending('transfer', '1', 'gold'), null);
  f.form.elements.amount.value = '9.00';
  select.value = 'main'; await select.emit('change'); await tick();
  assert.equal(f.form.elements.amount.value, '1.25');
  assert.equal(f.form.elements.amount.readOnly, true);
  assert.equal(f.requests.filter(r => r.path.endsWith('/transfer')).length, 0);
});

test('late wallet data cannot replace the newly selected currency', async () => {
  const f = walletFixture({ deferWallet: true });
  const mainLoading = f.load(); await tick(); const main = f.deferred.at(-1);
  const select = f.get('[data-wallet-currency]');
  select.value = 'gold'; await select.emit('change'); await tick(); const gold = f.deferred.at(-1);
  assert.equal(main.path, '/api/v1/social/wallets/main');
  assert.equal(gold.path, '/api/v1/social/wallets/gold');
  gold.resolve(walletResponse('25.00', 'gold')); await tick();
  main.resolve(walletResponse('888.00', 'main')); await mainLoading;
  assert.equal(f.get('[data-wallet-balance]').textContent, '25.00');
  assert.equal(select.value, 'gold');
});

test('unknown currency does not fall back to the main wallet', async () => {
  const f = walletFixture({ currency: 'missing' }); await f.load();
  assert.match(f.get('[data-wallet-message]').textContent, /不存在/);
  assert.equal(f.requests.filter(r => r.path.includes('/wallets/')).length, 0);
  const select = f.get('[data-wallet-currency]'); select.value = 'main';
  await select.emit('change'); await tick();
  assert.equal(f.get('[data-wallet-balance]').textContent, '10.00');
});

test('a receipt for another currency cannot clear an outstanding transfer', async () => {
  const f = walletFixture({ pending: pendingTransfer }); await f.load(); await f.form.emit('submit');
  const send = f.get('[data-wallet-confirm-send]').emit('click'); await tick();
  assert.equal(f.get('[data-wallet-currency]').disabled, true);
  const select = f.get('[data-wallet-currency]'); select.value = 'gold'; await select.emit('change');
  assert.equal(select.value, 'main');
  f.deferred.at(-1).resolve({ currency_key: 'gold', entry: { id: '127', delta: '-1.25', kind: 'transfer_out' }, recipient_id: '2' });
  await send;
  assert.ok(f.C.readPending('transfer', '1', 'main'));
  assert.match(f.get('[data-wallet-transfer-result]').textContent, /未确认/);
});

const ledgerEntry = (id='300') => ({id,delta:'0.10',balance:'10.10',kind:'reward_check_in',label:'签到奖励',incoming:true,created_at:'2026-11-26T00:00:00Z'});
const flatten = node => (node.textContent || '') + (node.children || []).map(flatten).join('');
for(const field of ['balance','entries','settings','verification_required']) {
  test(`wallet rejects malformed ${field} before displaying a usable balance`,async()=>{
    const f=walletFixture({deferWallet:true}),loading=f.load();await tick();const data=walletResponse();
    data[field]=field==='balance'?'1e5':field==='entries'?[ledgerEntry(),{...ledgerEntry('299'),incoming:false}]:null;
    f.deferred.at(-1).resolve(data);await loading;
    assert.notEqual(f.get('[data-wallet-balance]').textContent,'10.00');assert.equal(f.form.querySelector('fieldset').disabled,true);
    assert.equal(f.get('[data-wallet-message]').dataset.tone,'error');
  });
}
test('wallet pagination coalesces clicks, validates the whole page and preserves its cursor on failure',async()=>{
  const f=walletFixture({deferWallet:true}),loading=f.load();await tick();
  f.deferred.at(-1).resolve({...walletResponse(),entries:[ledgerEntry()],next_before:'300'});await loading;
  const more=f.get('[data-wallet-more]'),before=flatten(f.get('[data-wallet-history]'));
  const first=more.emit('click');await tick();const request=f.deferred.at(-1);await more.emit('click');
  assert.equal(f.requests.filter(r=>r.path.includes('?before=')).length,1);
  request.resolve({...walletResponse(),entries:[ledgerEntry('299'),{...ledgerEntry('298'),incoming:false}],next_before:'298'});await first;
  assert.equal(flatten(f.get('[data-wallet-history]')),before);
  const retry=more.emit('click');await tick();assert.equal(f.deferred.at(-1).path,'/api/v1/social/wallets/main?before=300');
  f.deferred.at(-1).resolve({...walletResponse(),entries:[ledgerEntry(),ledgerEntry('299')],next_before:''});await retry;
  assert.equal(flatten(f.get('[data-wallet-history]')).split('#300').length,2);assert.match(flatten(f.get('[data-wallet-history]')),/#299/);
  assert.equal(more.hidden,true);
});
test('late pagination failure cannot overwrite a refreshed wallet or its controls',async()=>{
  const f=walletFixture({deferWallet:true}),loading=f.load();await tick();
  f.deferred.at(-1).resolve({...walletResponse(),entries:[ledgerEntry()],next_before:'300'});await loading;
  const more=f.get('[data-wallet-more]'),paging=more.emit('click');await tick();const old=f.deferred.at(-1);
  const refreshing=f.load();await tick();f.deferred.at(-1).resolve(walletResponse('20.00'));await refreshing;
  old.reject(new Error('旧分页请求失败'));await paging;
  assert.equal(f.get('[data-wallet-balance]').textContent,'20.00');assert.doesNotMatch(f.get('[data-wallet-message]').textContent,/旧分页/);
  assert.equal(more.disabled,true);assert.equal(more.hidden,true);
});
for(const patch of [{incoming:true},{balance:'-1'},{label:null},{created_at:'invalid'},{delta:'-2.00'}]) {
  test(`invalid transfer receipt retains its pending request ${JSON.stringify(patch)}`,async()=>{
    const f=walletFixture({pending:pendingTransfer});await f.load();await f.form.emit('submit');
    const sending=f.get('[data-wallet-confirm-send]').emit('click');await tick();
    f.deferred.at(-1).resolve({currency_key:'main',replayed:false,recipient_id:'2',entry:{...ledgerEntry(),kind:'transfer_out',delta:'-1.25',incoming:false,...patch}});await sending;
    assert.equal(f.C.readPending('transfer','1').request_id,pendingTransfer.request_id);assert.equal(f.get('[data-wallet-confirm]').open,true);
  });
}
test('confirmed transfer remains successful and locks stale controls when the wallet refresh fails',async()=>{
  const f=walletFixture({pending:pendingTransfer,deferWallet:true}),loading=f.load();await tick();f.deferred.at(-1).resolve(walletResponse());await loading;await f.form.emit('submit');
  const sending=f.get('[data-wallet-confirm-send]').emit('click');await tick();
  f.deferred.at(-1).resolve({currency_key:'main',replayed:false,recipient_id:'2',entry:{...ledgerEntry(),kind:'transfer_out',delta:'-1.25',incoming:false}});await tick();
  assert.equal(f.deferred.at(-1).path,'/api/v1/social/wallets/main');f.deferred.at(-1).reject(new Error('刷新失败'));await sending;
  assert.equal(f.C.readPending('transfer','1'),null);assert.match(f.get('[data-wallet-transfer-result]').textContent,/已完成/);
  assert.equal(f.form.querySelector('fieldset').disabled,true);
});

for(const kind of ['transfer','check-in'])test(`uncertain ${kind} survives currency switches when storage is unavailable`,async()=>{
  const f=walletFixture({unavailableStorage:true});await f.load();
  const send=async()=>{
    if(kind==='transfer'){
      if(!f.form.elements.amount.readOnly){f.form.elements.recipient.value='recipient';f.form.elements.amount.value='1.25';}
      await f.form.emit('submit');return f.get('[data-wallet-confirm-send]').emit('click');
    }
    return f.get('[data-wallet-check-in]').emit('click');
  };
  const first=send();await tick();const original=f.deferred.at(-1).options.body;f.deferred.at(-1).reject(new Error('Disconnected'));await first;
  const select=f.get('[data-wallet-currency]');select.value='gold';await select.emit('change');await tick();
  const other=send();await tick();assert.notEqual(f.deferred.at(-1).options.body.request_id,original.request_id);f.deferred.at(-1).reject(new Error('Disconnected'));await other;
  select.value='main';await select.emit('change');await tick();const retry=send();await tick();
  assert.deepEqual(f.deferred.at(-1).options.body,original);f.deferred.at(-1).reject(new Error('Disconnected'));await retry;
});
