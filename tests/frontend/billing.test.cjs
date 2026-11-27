const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const crypto = require('node:crypto').webcrypto;
const billingCode = process.env.NEW_PRESS_BILLING_VARIANT === 'generated' ? fs.readFileSync('themes/chunnel/assets/js/chunnel-billing.js','utf8') : require('esbuild').buildSync({ entryPoints: ['frontend/shared/billing.ts'], bundle: true, write: false, format: 'iife', target: 'es2022' }).outputFiles[0].text;
const tick = () => new Promise(resolve => setImmediate(resolve));

class Element {
  constructor(tag = 'div') { this.tagName = tag; this.isConnected = true; this.dataset = {}; this.children = []; this.selectors = new Map(); this.listeners = new Map(); this.textContent = ''; this.hidden = false; this.disabled = false; this.checked = false; this.open = false; this._value = ''; }
  get value() { return this._value || (this.tagName === 'select' ? this.children[0]?.value || '' : ''); }
  set value(v) { this._value = String(v); }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; this.textContent = ''; if (this.tagName === 'select') this._value = ''; }
  matches(selector) { const match = selector.match(/^\[data-([a-z-]+)\]$/); return match ? Object.hasOwn(this.dataset, match[1].replace(/-([a-z])/g, (_, c) => c.toUpperCase())) : this.tagName === selector; }
  querySelectorAll(selector) { const matches = selector.split(',').map(s => s.trim()), found = []; const visit = e => { for (const child of [...e.children, ...e.selectors.values()]) { if (matches.some(s => child.matches?.(s))) found.push(child); visit(child); } }; visit(this); return [...new Set(found)]; }
  querySelector(selector) { if (this.selectors.has(selector)) return this.selectors.get(selector); const found = this.querySelectorAll(selector)[0]; if (found) return found; const el = new Element(); const match = selector.match(/^\[data-([a-z-]+)\]$/); if (match) el.dataset[match[1].replace(/-([a-z])/g, (_, c) => c.toUpperCase())] = ''; this.selectors.set(selector, el); return el; }
  closest(selector) { return this.matches(selector) ? this : null; }
  addEventListener(name, fn) { const list = this.listeners.get(name) || []; list.push(fn); this.listeners.set(name, list); }
  async emit(name, data = {}) { for (const fn of this.listeners.get(name) || []) await fn({ target: this, currentTarget: this, preventDefault() {}, ...data }); }
  setAttribute(name, value) { this[name] = value; }
  showModal() { this.open = true; }
  close() { this.open = false; }
  reportValidity() { return !this.elements?.accepted || this.elements.accepted.checked; }
  reset() { for (const e of Object.values(this.elements || {})) { e.checked = false; e._value = ''; } }
}
const flatten = element => (element.textContent || '') + element.children.map(flatten).join('');
function fixture() {
  return { version: '4', user_id: '2', csrf_token: 'fixture-csrf', enabled: true, require_verified_email: true, disclaimer: '付款前请核对商品、金额、渠道和退款规则。', refund_policy: '请联系管理员核对退款。', currencies: [{ key: 'main', name: '积分', symbol: '分', precision: 2, enabled: true }, { key: 'stars', name: '星星', symbol: '星', precision: 0, enabled: true }], channels: [{ key: 'alipay', name: '支付宝', currency: 'CNY', provider: 'alipay' }, { key: 'card', name: '银行卡', currency: 'USD', provider: 'stripe' }], products: [{ key: 'credit', title: '一份支持', enabled: true, kind: 'recharge', currency: 'CNY', price: '1299', target_currency: 'main', credit: '9007199254740993', plan: '' }, { key: 'annual', title: '年度会员', enabled: true, kind: 'membership', currency: 'USD', price: '9900', target_currency: '', credit: '0', plan: 'annual' }], exchanges: [{ key: 'stars', title: '积分兑换星星', enabled: true, from: 'main', to: 'stars', from_units: '100', to_units: '3', fee_bps: 250, minimum: '1', maximum: '1000000000000' }], orders: [] };
}
function makeOrder(options = {}) {
  return { id: 'np_0123456789abcdef0123456789ab', user_id: '2', kind: 'recharge', title: '一份支持', status: 'pending', amount: '12.99', currency: 'CNY', checkout: { provider_order_id: 'provider-1', url: 'https://pay.example.test/checkout' }, qr_url: '', error_code: '', paid_at: '0001-01-01T00:00:00Z', completed_at: '0001-01-01T00:00:00Z', resource_url: '', resource_ends_at: '0001-01-01T00:00:00Z', target_currency: 'main', credit: '90071992547409.93', plan: '', days: 0, created_at: new Date().toISOString(), expires_at: new Date(Date.now() + 1800000).toISOString(), disclaimer: '订单支付说明', refund_policy: '订单退款规则', ...options, checkout: { provider_order_id: '', ...(options.checkout || { url: 'https://pay.example.test/checkout' }) } };
}
function harness({ search = '', saved = null, storageFailure = false } = {}) {
  const root = new Element(), documentEvents = new Element(), windowEvents = new Element(), calls = [], storage = new Map();
  let cfg = fixture(), order = makeOrder(), session = { authenticated: true, id: '2' };
  if (saved) storage.set('chunnel.billing.v1.pending.2', JSON.stringify(saved));
  const purchase = root.querySelector('[data-billing-purchase]'); purchase.elements = { accepted: new Element('input') };
  const exchange = root.querySelector('[data-billing-exchange]'); exchange.elements = { rule: new Element('select'), amount: new Element('input'), accepted: new Element('input') };
  root.selectors.set('[data-billing-exchange-rule]', exchange.elements.rule);
  for (const form of [purchase, exchange]) Object.defineProperty(form.elements, 'namedItem', { value(name) { return this[name]; } });
  for (const kind of ['recharge', 'membership', 'exchange']) { const el = new Element('button'); el.dataset.billingKind = kind; root.append(el); }
  for (const key of ['confirm-close', 'confirm-cancel']) root.querySelector(`[data-billing-${key}]`);
  let respond = async path => path === '/api/v1/social/billing' ? structuredClone(cfg) : structuredClone(order);
  const window = { ChunnelInteractions: { getSession: async () => session, api: async (path, options = {}) => { calls.push({ path, ...structuredClone(options) }); return respond(path, options); } }, addEventListener: (...args) => windowEvents.addEventListener(...args) };
  const location = { search, origin: 'https://site.example.test', href: 'https://site.example.test/billing/' + search };
  const history = { replaceState(_a, _b, path) { location.href = new URL(path, location.origin).href; } };
  const document = { hidden: false, querySelector: selector => selector === '[data-billing-room]' ? root : null, createElement: tag => new Element(tag), addEventListener: (...args) => documentEvents.addEventListener(...args) };
  const context = { AbortController, DOMException, Element, HTMLElement: Element, HTMLInputElement: Element, window, document, location, history, URL, URLSearchParams, crypto, Uint8Array, Date, setTimeout: () => 1, clearTimeout() {}, sessionStorage: { getItem: key => storage.get(key) ?? null, setItem: (key, value) => { if (storageFailure) throw new Error('denied'); storage.set(key, value); }, removeItem: key => storage.delete(key) } };
  vm.runInNewContext(fs.readFileSync('themes/chunnel/assets/js/chunnel-currency-shared.js', 'utf8'), context);
  return { root, purchase, exchange, calls, storage, documentEvents, windowEvents, config: () => cfg, order: value => { order = value; }, respond(fn) { const previous = respond; respond = (path, options) => fn(path, options, previous); }, async start() { vm.runInNewContext(billingCode, context); await tick(); await tick(); }, async identity(value) { session = value; cfg.user_id = String(value.id || ''); await documentEvents.emit('chunnel:session', { detail: value }); await tick(); }, async reviewPurchase() { purchase.elements.accepted.checked = true; await purchase.emit('submit'); }, pending() { const value = storage.get('chunnel.billing.v1.pending.2'); return value ? JSON.parse(value) : null; } };
}

test('billing formats smallest-unit strings without Number precision loss', async () => {
  const h = harness(); await h.start(); assert.equal(h.root.querySelector('[data-billing-total]').textContent, '12.99'); assert.match(h.root.querySelector('[data-billing-benefit]').textContent, /90071992547409\.93/); assert.equal(h.calls.some(c => c.method === 'POST'), false);
});
test('purchase requires explicit consent and a second review action', async () => {
  const h = harness(); await h.start(); await h.purchase.emit('submit'); assert.equal(h.root.querySelector('[data-billing-confirm]').open, false); await h.reviewPurchase(); assert.equal(h.root.querySelector('[data-billing-confirm]').open, true); assert.equal(h.calls.some(c => c.method === 'POST'), false);
});
test('uncertain purchase retries the original payload and releases it only on a matching receipt', async () => {
  const h = harness(); let attempts = 0; h.respond((path, options, previous) => { if (options.method === 'POST' && attempts++ === 0) throw new Error('network timeout'); return previous(path, options); }); await h.start(); await h.reviewPurchase(); const send = h.root.querySelector('[data-billing-confirm-send]'); await send.emit('click'); const original = h.pending(); assert.ok(original); assert.equal(h.root.querySelector('[data-billing-purchase-fields]').disabled, true); await send.emit('click'); const writes = h.calls.filter(c => c.method === 'POST'); assert.deepEqual(writes[0].body, writes[1].body); assert.equal(writes[0].body.price, undefined); assert.equal(writes[0].body.amount, undefined); assert.equal(writes[0].body.accepted, true); assert.equal(h.pending(), null); assert.equal(h.root.querySelector('[data-billing-order-dialog]').open, true);
});
test('wrong order amount keeps recovery information and blocks a second purchase', async () => {
  const h = harness(); h.order(makeOrder({ amount: '0.01' })); await h.start(); await h.reviewPurchase(); await h.root.querySelector('[data-billing-confirm-send]').emit('click'); assert.ok(h.pending()); assert.equal(h.root.querySelector('[data-billing-order-dialog]').open, false); assert.match(h.root.querySelector('[data-billing-confirm-message]').textContent, /不一致/);
});
test('storage failure prevents sending a payment mutation', async () => {
  const h = harness({ storageFailure: true }); await h.start(); await h.reviewPurchase(); await h.root.querySelector('[data-billing-confirm-send]').emit('click'); assert.equal(h.calls.some(c => c.method === 'POST'), false); assert.match(h.root.querySelector('[data-billing-confirm-message]').textContent, /会话存储/);
});
test('exchange preview floors after fees and submits canonical decimal strings', async () => {
  const h = harness(); await h.start(); h.exchange.elements.amount.value = '1.23'; await h.exchange.elements.amount.emit('input'); assert.equal(h.root.querySelector('[data-billing-exchange-credit]').textContent, '3'); h.exchange.elements.accepted.checked = true; await h.exchange.emit('submit'); assert.match(flatten(h.root.querySelector('[data-billing-confirm-review]')), /3 星星/); h.order(makeOrder({ kind: 'exchange', amount: '1.23', currency: 'main', target_currency: 'stars', credit: '3', status: 'completed', checkout: {} })); await h.root.querySelector('[data-billing-confirm-send]').emit('click'); const body = h.calls.find(c => c.path.endsWith('/exchange')).body; assert.equal(body.amount, '1.23'); assert.equal(body.credit, undefined);
});
test('exchange rejects scientific notation and precision overflow before review', async () => {
  for (const raw of ['1e3', '0.001', '-1', '1000000000000.01']) { const h = harness(); await h.start(); h.exchange.elements.amount.value = raw; h.exchange.elements.accepted.checked = true; await h.exchange.emit('submit'); assert.equal(h.root.querySelector('[data-billing-confirm]').open, false, raw); assert.equal(h.calls.some(c => c.method === 'POST'), false); }
});
test('configuration conflict clears the unaccepted request and requires new consent', async () => {
  const h = harness(); h.respond((path, options, previous) => { if (options.method === 'POST') { const e = new Error('changed'); e.code = 'payment.conflict'; throw e; } return previous(path, options); }); await h.start(); await h.reviewPurchase(); await h.root.querySelector('[data-billing-confirm-send]').emit('click'); assert.equal(h.pending(), null); assert.equal(h.purchase.elements.accepted.checked, false); assert.equal(h.root.querySelector('[data-billing-confirm]').open, false); assert.match(h.root.querySelector('[data-billing-message]').textContent, /重新选择/);
});
test('restoring a pending request never sends payment automatically', async () => {
  const first = harness(); first.respond((path, options, previous) => { if (options.method === 'POST') throw new Error('lost'); return previous(path, options); }); await first.start(); await first.reviewPurchase(); await first.root.querySelector('[data-billing-confirm-send]').emit('click'); const h = harness({ saved: first.pending() }); await h.start(); assert.equal(h.calls.some(c => c.method === 'POST'), false); assert.equal(h.root.querySelector('[data-billing-pending]').hidden, false); await h.root.querySelector('[data-billing-resume]').emit('click'); assert.equal(h.root.querySelector('[data-billing-confirm]').open, true); assert.equal(h.calls.some(c => c.method === 'POST'), false);
});
test('account change rejects a late result and isolates stored requests', async () => {
  const h = harness(); let finish; h.respond((path, options, previous) => options.method === 'POST' ? new Promise(resolve => { finish = () => resolve(makeOrder()); }) : previous(path, options)); await h.start(); await h.reviewPurchase(); const work = h.root.querySelector('[data-billing-confirm-send]').emit('click'); await tick(); await h.identity({ authenticated: true, id: '3' }); finish(); await work; assert.ok(h.pending()); assert.equal(h.storage.has('chunnel.billing.v1.pending.3'), false); assert.equal(h.root.querySelector('[data-billing-order-dialog]').open, false); assert.equal(h.root.querySelector('[data-billing-orders-count]').textContent, '0');
});
test('plan query preselects membership but redirect flags never imply payment success', async () => {
  const h = harness({ search: '?plan=annual&paid=true' }); await h.start(); assert.equal(h.root.querySelector('[data-billing-total]').textContent, '99.00'); assert.equal(h.root.querySelector('[data-billing-total-currency]').textContent, 'USD'); assert.equal(h.calls.some(c => c.method === 'POST'), false); assert.equal(h.root.querySelector('[data-billing-order-dialog]').open, false);
});
test('only valid HTTPS hosted checkout and same-origin order QR are rendered', async () => {
  const id = makeOrder().id; const h = harness({ search: '?order=' + id }); h.order(makeOrder({ checkout: { url: 'javascript:alert(1)' }, qr_url: 'https://outside.example/qr' })); await h.start(); const pay = h.root.querySelector('[data-billing-order-pay]'); assert.equal(pay.children.some(c => c.tagName === 'a' || c.tagName === 'img' || c.tagName === 'form'), false); assert.match(flatten(pay), /暂未就绪/);
});
test('completed and expired orders do not offer a second payment', async () => {
  for (const patch of [{ status: 'completed' }, { expires_at: new Date(Date.now() - 1000).toISOString() }]) { const h = harness({ search: '?order=' + makeOrder().id }); h.order(makeOrder(patch)); await h.start(); assert.equal(h.root.querySelector('[data-billing-order-pay]').children.length, 0); assert.equal(h.calls.some(c => c.method === 'POST'), false); }
});
test('current 31-character order links open the matching order', async () => {
  const id = 'np_0123456789abcdef0123456789ab'; const h = harness({ search: '?order=' + id }); h.order(makeOrder({ id })); await h.start(); assert.equal(id.length, 31); assert.equal(h.root.querySelector('[data-billing-order-dialog]').open, true); assert.match(flatten(h.root.querySelector('[data-billing-order-review]')), new RegExp(id));
});

test('retired 35-character order links are rejected before fetching an order', async () => {
  const id = 'np_0123456789abcdef0123456789abcdef', h = harness({ search: '?order=' + id });
  await h.start();
  assert.equal(h.root.querySelector('[data-billing-order-dialog]').open, false);
  assert.match(flatten(h.root.querySelector('[data-billing-message]')), /订单链接格式无效/);
  assert.equal(h.calls.some(call => call.path.includes('/orders/' + id)), false);
});

test('resource billing shows delivery deadline and article access instead of wallet credits', async () => {
  const id=makeOrder().id,h=harness({search:'?order='+id});
  h.order(makeOrder({kind:'resource',status:'completed',resource_url:'/posts/resource#resource-bundle',resource_ends_at:new Date(Date.now()+60000).toISOString(),checkout:{}}));
  await h.start();assert.doesNotMatch(flatten(h.root.querySelector('[data-billing-order-review]')),/到账数量/);assert.match(flatten(h.root.querySelector('[data-billing-order-review]')),/资源访问截止/);assert.equal(h.root.querySelector('[data-billing-order-account]').href,'/posts/resource#resource-bundle');assert.match(h.root.querySelector('[data-billing-order-account]').textContent,/核验解锁条件/);
});

test('resource delivery review keeps the captured payment distinct from fulfilled access', async () => {
  const id=makeOrder().id,h=harness({search:'?order='+id});h.order(makeOrder({kind:'resource',status:'review',error_code:'resource_delivery_requires_reconciliation'}));await h.start();assert.match(h.root.querySelector('[data-billing-order-state-copy]').textContent,/收款已记录/);assert.match(h.root.querySelector('[data-billing-order-state-copy]').textContent,/核对交付或退款/);assert.equal(h.root.querySelector('[data-billing-order-pay]').children.length,0);
});
test('completed order navigation uses the account view contract', async () => {
  for (const kind of ['membership', 'recharge']) { const h = harness({ search: '?order=' + makeOrder().id }); h.order(makeOrder({ kind, status: 'completed' })); await h.start(); const href = h.root.querySelector('[data-billing-order-account]').href; assert.match(href, kind === 'membership' ? /view=progression/ : /view=wallet&currency=main/); }
});
test('same-origin QR images match the exact current order and unknown checkout stays recoverable', async () => {
  const id = makeOrder().id, h = harness({ search: '?order=' + id }); h.order(makeOrder({ checkout: {}, qr_url: '/api/v1/social/billing/orders/' + id + '/qr' })); await h.start(); const images = h.root.querySelector('[data-billing-order-pay]').children.filter(e => e.tagName === 'img'); assert.equal(images.length, 1); assert.match(images[0].src, new RegExp('/orders/' + id + '/qr$'));
  const unknown = harness({ search: '?order=' + id }); unknown.order(makeOrder({ checkout: {}, error_code: 'checkout_unknown' })); await unknown.start(); assert.match(unknown.root.querySelector('[data-billing-order-state-copy]').textContent, /暂勿新建/); assert.equal(unknown.calls.some(c => c.method === 'POST'), false);
});

test('a different order receipt cannot open or replace the requested order', async () => {
  const id=makeOrder().id,h=harness({search:'?order='+id});h.order(makeOrder({id:'np_ffffffffffffffffffffffffffff'}));await h.start();assert.equal(h.root.querySelector('[data-billing-order-dialog]').open,false);assert.match(flatten(h.root.querySelector('[data-billing-message]')),/回执未确认/);
  const active=harness({search:'?order='+id});await active.start();active.order(makeOrder({id:'np_ffffffffffffffffffffffffffff',status:'completed'}));await active.root.querySelector('[data-billing-order-refresh]').emit('click');assert.equal(active.root.querySelector('[data-billing-order-status]').textContent,'待支付');assert.match(flatten(active.root.querySelector('[data-billing-order-message]')),/回执未确认/);
});
test('missing order fields and wrong promised credits retain the original request', async () => {
  for(const patch of [{days:undefined},{checkout:{fields:{token:4}}},{credit:'90071992547409.92'},{target_currency:'stars'}]) {const h=harness();h.order(makeOrder(patch));await h.start();await h.reviewPurchase();await h.root.querySelector('[data-billing-confirm-send]').emit('click');assert.ok(h.pending());assert.equal(h.root.querySelector('[data-billing-order-dialog]').open,false);}
});
test('pagehide retires late payment receipts while preserving recovery, and BFcache restores reads only', async () => {
  const h=harness();let finish;h.respond((path,options,previous)=>options.method==='POST'?new Promise(resolve=>{finish=()=>resolve(makeOrder());}):previous(path,options));await h.start();await h.reviewPurchase();const sending=h.root.querySelector('[data-billing-confirm-send]').emit('click');await tick();const original=h.pending();await h.windowEvents.emit('pagehide');finish();await sending;assert.deepEqual(h.pending(),original);assert.equal(h.root.querySelector('[data-billing-order-dialog]').open,false);assert.equal(h.root.querySelector('[data-billing-content]').hidden,true);await h.windowEvents.emit('pageshow',{persisted:true});await tick();await tick();assert.equal(h.calls.filter(c=>c.method==='POST').length,1);assert.equal(h.root.querySelector('[data-billing-pending]').hidden,false);
});
test('same account with a new session token retires the previous pending response', async () => {
  const h=harness();let finish;h.respond((path,options,previous)=>options.method==='POST'?new Promise(resolve=>{finish=()=>resolve(makeOrder());}):previous(path,options));await h.start();await h.reviewPurchase();const sending=h.root.querySelector('[data-billing-confirm-send]').emit('click');await tick();h.config().csrf_token='new-token';await h.identity({authenticated:true,id:'2',csrf_token:'new-token'});finish();await sending;assert.ok(h.pending());assert.equal(h.root.querySelector('[data-billing-order-dialog]').open,false);assert.equal(h.root.querySelector('[data-billing-pending]').hidden,false);
});
test('a late catalog cannot roll back the order confirmed after it started loading', async () => {
  const id=makeOrder().id,h=harness({search:'?order='+id});h.config().orders=[makeOrder()];await h.start();let finish;h.respond((path,options,previous)=>path==='/api/v1/social/billing'?new Promise(resolve=>{finish=()=>resolve(structuredClone(h.config()));}):previous(path,options));const loading=h.root.querySelector('[data-billing-refresh]').emit('click');await tick();h.order(makeOrder({status:'completed'}));await h.root.querySelector('[data-billing-order-refresh]').emit('click');finish();await loading;assert.match(flatten(h.root.querySelector('[data-billing-orders]')),/已完成/);assert.doesNotMatch(flatten(h.root.querySelector('[data-billing-orders]')),/待支付/);
});
test('closing an order dialog ignores a late refresh and allows the next order to load', async () => {
  const id=makeOrder().id,h=harness({search:'?order='+id});await h.start();let finish;h.respond((path,options,previous)=>path.endsWith('/'+id)?new Promise(resolve=>{finish=()=>resolve(makeOrder({status:'completed'}));}):previous(path,options));const reading=h.root.querySelector('[data-billing-order-refresh]').emit('click');await tick();const dialog=h.root.querySelector('[data-billing-order-dialog]');dialog.close();await dialog.emit('close');finish();await reading;assert.equal(dialog.open,false);assert.equal(h.root.querySelector('[data-billing-order-status]').textContent,'待支付');
});
test('corrupt recovery data blocks replacement purchases and never sends an automatic retry', async () => {
  const h=harness({saved:{owner:'2',kind:'purchase',body:{request_id:'broken'}}});await h.start();await h.reviewPurchase();assert.equal(h.root.querySelector('[data-billing-confirm]').open,false);assert.equal(h.root.querySelector('[data-billing-purchase-fields]').disabled,true);assert.match(flatten(h.root.querySelector('[data-billing-message]')),/恢复信息无法读取/);assert.equal(h.calls.some(c=>c.method==='POST'),false);
});
test('external POST checkout preserves safe fields but is blocked when its order expires', async () => {
  const id=makeOrder().id,h=harness({search:'?order='+id});h.order(makeOrder({checkout:{method:'POST',url:'https://pay.example.test/checkout',fields:{merchant:'one',signature:'signed-value'}}}));await h.start();const form=h.root.querySelector('[data-billing-order-pay]').children.find(c=>c.tagName==='form');assert.ok(form);assert.equal(form.rel,'noopener noreferrer');assert.deepEqual(form.children.filter(c=>c.tagName==='input').map(c=>[c.name,c.value]),[['merchant','one'],['signature','signed-value']]);await h.windowEvents.emit('pagehide');let prevented=false;await form.emit('submit',{preventDefault(){prevented=true;}});assert.equal(prevented,true);
});

