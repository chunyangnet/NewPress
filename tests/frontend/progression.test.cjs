const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const crypto = require('node:crypto').webcrypto;
const {compile}=require('./helpers/typescript.cjs');
const source = name => process.env.NEWPRESS_WALLET_TS==='1' && name !== '-admin' ? compile(`frontend/shared/progression${name}.ts`) : fs.readFileSync(`themes/chunnel/assets/js/chunnel-progression${name}.js`, 'utf8');
const orderReceipt = (payload, patch = {}) => ({ user_id:'2', status:'completed', plan_name:'标准会员', days:30, amount:'1.25', currency:'main', symbol:'分', ...payload, ...patch });
const tick = () => new Promise(resolve => setImmediate(resolve));
class Element {
  constructor(tag = 'div') { this.tagName = tag; this.dataset = {}; this.children = []; this.selectors = new Map(); this.listeners = new Map(); this.style = { setProperty() {} }; this.disabled = false; this.hidden = false; this.textContent = ''; }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; this.textContent = ''; }
  matches(selector) { if (selector === this.tagName) return true; const attribute = selector.match(/^\[data-([a-z-]+)\]$/); if (attribute) return Object.hasOwn(this.dataset, attribute[1].replace(/-([a-z])/g, (_, c) => c.toUpperCase())); return false; }
  querySelectorAll(selector) { const all = []; const visit = el => { for (const child of [...el.children, ...el.selectors.values()]) { if (child.matches?.(selector)) all.push(child); visit(child); } }; visit(this); return [...new Set(all)]; }
  querySelector(selector) { if (this.selectors.has(selector)) return this.selectors.get(selector); const found = this.querySelectorAll(selector)[0]; if (found) return found; if(selector==='[data-progression-check-in]')return null; const e = new Element(selector === 'form' ? 'form' : 'div'); this.selectors.set(selector, e); return e; }
  addEventListener(name, callback) { const listeners = this.listeners.get(name) || []; this.listeners.set(name, [...listeners, callback]); }
  async emit(name, detail = {}) { for (const callback of this.listeners.get(name) || []) await callback({ currentTarget: this, preventDefault() {}, ...detail }); }
  setAttribute(name, value) { this[name] = value; }
  showModal() { this.open = true; }
  close() { this.open = false; }
}
function harness({ pending = null } = {}) {
  const storage = new Map(), root = new Element(), documentEvents = new Element(), windowEvents = new Element(), requests = [];
  root.dataset.userId = '2'; const document = { querySelector: selector => selector === '[data-progression-panel]' ? root : null, createElement: tag => new Element(tag), addEventListener: (...args) => documentEvents.addEventListener(...args) };
  const window = { addEventListener: (...args) => windowEvents.addEventListener(...args) };
  const sessionStorage = { getItem: k => storage.get(k) ?? null, setItem: (k, v) => storage.set(k, v), removeItem: k => storage.delete(k) };
  let snapshot = { settings: { version: '2', experience: { enabled: true, name: '经验值', maximum: '1000000000', daily_limit: '1000', require_verified_email: false }, check_in: { enabled: true, amount: '10' }, membership: { enabled: true, plans: [{ key: 'member', name: '标准会员', description: '描述', color: '#b57525', days: 30, experience_percent: 125, enabled: true, purchase_enabled: true, currency: 'main', price: '1.25' }] } }, account: { user_id: '2', experience: '0', membership: { version: '0', plan: '', revoked: false, since: '', until: '' } }, level: null, next_level: null, membership_active: false, earned_today: '0', next_check_in: '', pending_order: pending, entries: [], next_before: '0', currencies: [{ key: 'main', name: '积分', symbol: '分', precision: 2, enabled: true }], verified_email: true };
  let respond = async (path, options) => {
    if (path === '/api/v1/social/session') return { authenticated: true, id: 2, csrf_token: 'csrf-one' };
    if (path === '/api/v1/social/progression') return structuredClone(snapshot);
    if (path.endsWith('/purchase')) { const payload = JSON.parse(options.body); snapshot.account.membership = { version: '1', plan: 'member', revoked: false, since: new Date().toISOString(), until: new Date(Date.now() + 2592000000).toISOString() }; snapshot.membership_active = true; return orderReceipt(payload); }
    throw new Error('Unexpected request ' + path);
  };
  const fetch = async (path, options) => { requests.push({ path, ...options }); const value = await respond(path, options); return { ok: true, status: 200, json: async () => value }; };
  const context = { window, document, sessionStorage, crypto, fetch, AbortController, setTimeout, clearTimeout, Uint8Array, URL, Date };
  vm.runInNewContext(source('-shared'), context); const P = window.ChunnelProgressionShared;
  return { context, P, root, requests, storage, documentEvents, windowEvents, window, snapshot: () => snapshot, respond(fn) { const previous = respond; respond = (path, options) => fn(path, options, previous); }, start() { vm.runInNewContext(source(''), context); return window.ChunnelProgression.load(); } };
}
test('experience integers and thresholds stay exact beyond Number precision', () => {
  const { P } = harness(); assert.equal(P.integer('9007199254740993'), 9007199254740993n); assert.equal(P.integer('9223372036854775807'), 9223372036854775807n);
  for (const raw of ['1.2', '01', '-0', '1e3', '9223372036854775808', 10, null]) assert.throws(() => P.integer(raw));
  assert.equal(P.progress('9007199254740993', { threshold: '9007199254740992' }, { threshold: '9007199254740994' }), 50);
});
test('module dirtiness ignores JSON property order while preserving changes', () => {
  const { P } = harness(); assert.equal(P.equal({ plans: [{ key: 'a', enabled: true }] }, { plans: [{ enabled: true, key: 'a' }] }), true); assert.equal(P.equal({ enabled: true }, { enabled: false }), false);
});
test('purchase request is frozen, scoped to owner, and never contains a price override', () => {
  const { P } = harness(); const payload = { version: '2', member_version: '0', plan: 'member', mode: 'grant', request_id: 'progression-test-request' };
  const pending = P.save('purchase', '2', payload, { amount: '1.25' }); payload.plan = 'another'; assert.equal(pending.payload.plan, 'member'); assert.equal(Object.isFrozen(pending.payload), true);
  assert.equal(P.read('purchase', '3'), null); assert.equal(P.read('purchase', '2').payload.request_id, pending.payload.request_id); assert.equal(P.validPayload('purchase', { ...pending.payload, price: '0.01' }), false);
});
test('pending storage does not accept another kind or invalid membership version', () => {
  const { P, storage } = harness(); const payload = { user_id: '3', version: '2', member_version: '0', action: 'grant', plan: 'member', days: 30, request_id: 'admin-member-request', reason: '开通原因' };
  P.save('membership', '2', payload); assert.equal(P.read('membership', '2').payload.user_id, '3'); assert.equal(P.read('experience', '2'), null);
  const value = JSON.parse(storage.get('chunnel.progression.v1.membership.2')); value.payload.member_version = '-1'; storage.set('chunnel.progression.v1.membership.2', JSON.stringify(value)); assert.equal(P.read('membership', '2'), null);
});
test('loading a pending purchase never initiates payment', async () => {
  const f = harness({ pending: { user_id: '2', request_id: 'pending-order-request', status: 'pending', plan: 'member', plan_name: '标准会员', days: 30, version: '2', member_version: '0', mode: 'grant', amount: '1.25', symbol: '分', currency: 'main' } });
  await f.start(); assert.equal(f.requests.some(r => r.method !== 'GET'), false); assert.equal(f.root.querySelector('[data-progression-pending]').hidden, false);
});
test('purchase requires review and retries the same request after uncertain failure', async () => {
  const f = harness(); let attempts = 0; f.respond((path, options, previous) => { if (path.endsWith('/purchase') && attempts++ === 0) throw new Error('uncertain network failure'); return previous(path, options); });
  await f.start(); const buy = f.root.querySelectorAll('[data-progression-buy]')[0]; await buy.emit('click'); assert.equal(f.root.querySelector('[data-progression-dialog]').open, true); assert.equal(f.requests.some(r => r.method === 'POST'), false);
  await f.root.querySelector('[data-progression-confirm-submit]').emit('click'); const original = f.P.read('purchase', '2'); assert.ok(original);
  await f.root.querySelector('[data-progression-confirm-submit]').emit('click'); const calls = f.requests.filter(r => r.path.endsWith('/purchase')); assert.equal(calls.length, 2); assert.equal(calls[0].body, calls[1].body); assert.equal(f.P.read('purchase', '2'), null); assert.equal(JSON.parse(calls[0].body).price, undefined);
});
test('malformed success keeps the frozen purchase instead of enabling a new charge', async () => {
  const f = harness(); f.respond((path, options, previous) => path.endsWith('/purchase') ? { user_id: '9', request_id: JSON.parse(options.body).request_id, status: 'completed' } : previous(path, options)); await f.start(); await f.root.querySelectorAll('[data-progression-buy]')[0].emit('click'); await f.root.querySelector('[data-progression-confirm-submit]').emit('click'); assert.ok(f.P.read('purchase', '2')); assert.equal(f.root.querySelectorAll('[data-progression-buy]')[0].disabled, true);
});
test('account change discards late responses and prevents cross-account retries', async () => {
  const f = harness(); let finish; f.respond((path, options, previous) => path.endsWith('/purchase') ? new Promise(resolve => { finish = () => resolve({ user_id: '2', request_id: JSON.parse(options.body).request_id, status: 'completed' }); }) : previous(path, options)); await f.start(); await f.root.querySelectorAll('[data-progression-buy]')[0].emit('click'); const work = f.root.querySelector('[data-progression-confirm-submit]').emit('click'); await tick(); await f.documentEvents.emit('chunnel:session', { detail: { id: 3, csrf_token: 'new-account' } }); finish(); await work;
  assert.equal(f.root.querySelector('[data-progression-summary]').children.length, 0); assert.equal(f.root.querySelector('[data-progression-plans]').children.length, 0); const count = f.requests.length; await f.root.querySelector('[data-progression-retry]').emit('click'); assert.equal(f.requests.length, count); assert.ok(f.P.read('purchase', '2')); assert.equal(f.P.read('purchase', '3'), null);
});
test('auth mismatch stops mutation before sending a POST', async () => {
  const f = harness(); let lost = false; const connection = f.P.transport('3', '', () => { lost = true; }); await assert.rejects(connection.request('/api/v1/social/progression/purchase', { method: 'POST', body: {} })); assert.equal(lost, true); assert.equal(f.requests.some(r => r.method === 'POST'), false);
});
const flatten = element => (element.textContent || '') + element.children.map(flatten).join('');
test('numeric helpers keep exact experience thresholds and clamp daily allowance', () => {
  const { P } = harness();
  const levels = { enabled: true, items: [
    { key: 'beginner', name: '初来乍到', threshold: '0' },
    { key: 'reader', name: '常来常往', threshold: '9007199254740993' },
    { key: 'veteran', name: '资深', threshold: '9007199254740994' },
  ] };
  assert.equal(P.levelFor('9007199254740992', levels).key, 'beginner');
  assert.equal(P.levelFor('9007199254740993', levels).key, 'reader');
  assert.equal(P.levelFor('9007199254740994', levels).key, 'veteran');
  assert.equal(P.levelFor('9007199254740994', { ...levels, enabled: false }), null);
  assert.equal(P.remainingDaily('1000', '1250'), 0n);
  assert.equal(P.remainingDaily('1000', '950'), 50n);
  assert.equal(P.remainingDaily('bad', '0'), 0n);
  assert.equal(P.membershipDays('2026-01-02T12:00:00Z', Date.parse('2026-01-01T00:00:00Z')), 2);
  assert.equal(P.membershipDays('invalid', Date.now()), 0);
});
test('account summary exposes exact daily headroom, level progress and membership days', () => {
  const f = harness(), data = f.snapshot();
  data.account.experience = '180';
  data.account.membership = { version: '1', plan: 'member', revoked: false, since: new Date().toISOString(), until: new Date(Date.now() + 2.5 * 86400000).toISOString() };
  data.settings.levels = { enabled: true, items: [{ key: 'beginner', name: '初来乍到', threshold: '0' }, { key: 'reader', name: '常来常往', threshold: '100' }, { key: 'contributor', name: '热心分享', threshold: '500' }] };
  data.level = data.settings.levels.items[1]; data.next_level = data.settings.levels.items[2];
  data.membership_active = true; data.earned_today = '950';
  const container = new Element(); f.P.summary(container, data, () => {});
  const text = flatten(container);
  assert.match(text, /距离「热心分享」还需 320 经验值 · 本级进度 20%/);
  assert.match(text, /今日还可获得 50 经验值/);
  assert.match(text, /剩余 3 天/);
  assert.match(text, /自动经验奖励 × 1\.25/);
});

test('optional plan benefits default to empty and validate editable lines', () => {
  const { P } = harness();
  assert.deepEqual([...P.planBenefits({})], []);
  assert.deepEqual([...P.benefitLines('  会员身份\r\n\n自动经验加成  ')], ['会员身份', '自动经验加成']);
  assert.equal(P.benefitLines('好'.repeat(80))[0].length, 80);
  assert.equal([...P.benefitLines('😀'.repeat(80))[0]].length, 80);
  for (const benefits of [null, 'text', [''], [' repeat'], ['a\nb'], ['a\u200bb'], ['<img>'], ['重复', '重复'], ['好'.repeat(81)], Array.from({ length: 9 }, (_, i) => String(i))]) {
    assert.throws(() => P.planBenefits({ benefits }));
  }
  const values = ['会员身份'], copy = P.planBenefits({ benefits: values }); copy[0] = 'modified'; assert.equal(values[0], '会员身份');
});

test('renewal preview extends an active term and restarts expired or revoked memberships', () => {
  const { P } = harness(), now = Date.parse('2026-11-15T12:00:00Z');
  const plan = { key: 'member', days: 30 }, m = { version: '1', plan: 'member', revoked: false, until: '2026-11-20T12:00:00Z' };
  assert.equal(P.membershipEnd(m, plan, now), '2026-12-20T12:00:00.000Z');
  assert.equal(P.membershipEnd({ ...m, until: '2026-11-10T12:00:00Z' }, plan, now), '2026-12-15T12:00:00.000Z');
  assert.equal(P.membershipEnd({ ...m, revoked: true }, plan, now), '2026-12-15T12:00:00.000Z');
  assert.equal(P.membershipEnd({ ...m, plan: 'supporter' }, plan, now), '2026-12-15T12:00:00.000Z');
  assert.equal(P.membershipEnd(m, { ...plan, days: 3650 }, now), '');
});

test('current benefit claims require active dates and disappear at expiry even for a stale snapshot', () => {
  const f = harness(), data = f.snapshot(), now = Date.now(), container = new Element();
  data.account.membership = { version: '1', plan: 'member', revoked: false, since: new Date(now - 86400000).toISOString(), until: new Date(now + 3600000).toISOString() };
  data.settings.membership.plans[0].benefits = ['有效会员专属说明'];
  data.membership_active = true;
  f.P.summary(container, data); assert.match(flatten(container), /有效会员专属说明/); assert.match(flatten(container), /即将到期/);
  const fresh = structuredClone(data);
  for (const mutate of [
    value => { value.account.membership.until = new Date(now).toISOString(); },
    value => { value.account.membership.revoked = true; },
    value => { value.account.membership.since = new Date(now + 1000).toISOString(); },
    value => { value.settings.membership.enabled = false; },
    value => { value.membership_active = false; },
  ]) {
    const value = structuredClone(fresh); mutate(value); f.P.summary(container, value);
    assert.doesNotMatch(flatten(container), /有效会员专属说明|自动经验奖励 ×|权益生效中/);
  }
  fresh.settings.membership.plans[0].enabled = false;
  f.P.summary(container, fresh); assert.match(flatten(container), /有效会员专属说明/); assert.match(flatten(container), /已停止续费/);
});

test('membership cards show benefits and link the matching plan to online checkout without charging', async () => {
  const f = harness(); f.snapshot().settings.membership.plans[0].benefits = ['会员专属说明', '权益有效期随套餐'];
  await f.start();
  const card = f.root.querySelector('[data-progression-plans]').children[0], link = card.querySelector('a');
  assert.match(flatten(card), /会员专属说明/); assert.equal(link.href, '/billing/?plan=member');
  await link.emit('click'); assert.equal(f.requests.some(request => request.method !== 'GET'), false);
  await card.querySelectorAll('[data-progression-buy]')[0].emit('click');
  assert.match(f.root.querySelector('[data-progression-confirm]').textContent, /预计到期/);
  assert.equal(f.requests.some(request => request.method !== 'GET'), false);
});

test('malformed benefits stop rendering before offering a purchase', async () => {
  const f = harness(); f.snapshot().settings.membership.plans[0].benefits = ['<script>'];
  await f.start();
  assert.equal(f.root.querySelectorAll('[data-progression-buy]').length, 0);
  assert.match(f.root.querySelector('[data-progression-status]').textContent, /权益说明/);
});

const rewardReceipt = () => ({replayed:false,entry:{id:'9',user_id:'2',actor_id:'0',kind:'reward_check_in',delta:'10',balance:'10',reason:'签到奖励',created_at:'2026-11-26T00:00:00Z'}});
const pendingOrder = () => orderReceipt({version:'2',member_version:'0',plan:'member',mode:'grant',request_id:'progression-pending-test'},{status:'pending'});
for(const action of ['purchase','cancel','check-in']) {
  test(`confirmed ${action} cannot reuse stale controls after refresh fails`,async()=>{
    const pending=action==='cancel'?pendingOrder():null,f=harness({pending});await f.start();
    const oldBuy=f.root.querySelectorAll('[data-progression-buy]')[0];
    f.respond((path,options,previous)=>{
      if(path==='/api/v1/social/progression')throw new Error('refresh failed');
      if(path.endsWith('/check-in'))return rewardReceipt();
      if(path.endsWith('/purchase/cancel'))return {...pending,status:'cancelled'};
      return previous(path,options);
    });
    if(action==='purchase'){await oldBuy.emit('click');await f.root.querySelector('[data-progression-confirm-submit]').emit('click');}
    else await f.root.querySelector(action==='cancel'?'[data-progression-cancel]':'[data-progression-check-in]').emit('click');
    assert.equal(f.P.read(action==='check-in'?'check-in':'purchase','2'),null);
    assert.equal(f.root.querySelector('[data-progression-pending]').hidden,true);
    assert.match(f.root.querySelector('[data-progression-status]').textContent,/最新状态暂未载入/);
    assert.doesNotMatch(f.root.querySelector('[data-progression-status]').textContent,/refresh failed/);
    assert.equal(f.root.querySelector('[data-progression-plans]').children.length,0);
    const count=f.requests.length;await oldBuy.emit('click');await f.root.querySelector('[data-progression-confirm-submit]').emit('click');
    assert.equal(f.requests.length,count);
  });
}
for(const patch of [{plan:'other'},{version:'3'},{member_version:'1'},{mode:'renew'},{days:0},{status:'pending'}]) {
  test(`purchase retains original request on mismatched receipt ${JSON.stringify(patch)}`,async()=>{
    const f=harness();f.respond((path,options,previous)=>path.endsWith('/purchase')?orderReceipt(JSON.parse(options.body),patch):previous(path,options));
    await f.start();await f.root.querySelectorAll('[data-progression-buy]')[0].emit('click');await f.root.querySelector('[data-progression-confirm-submit]').emit('click');
    assert.ok(f.P.read('purchase','2'));assert.equal(f.root.querySelectorAll('[data-progression-buy]')[0].disabled,true);
  });
}
test('invalid cancellation retains the original order for explicit retry',async()=>{
  const pending=pendingOrder(),f=harness({pending});f.respond((path,options,previous)=>path.endsWith('/purchase/cancel')?{...pending,status:'cancelled',request_id:'another-request-key'}:previous(path,options));
  await f.start();await f.root.querySelector('[data-progression-cancel]').emit('click');
  assert.equal(f.root.querySelector('[data-progression-pending]').hidden,false);
  await f.root.querySelector('[data-progression-retry]').emit('click');assert.equal(f.root.querySelector('[data-progression-dialog]').open,true);
});
for(const patch of [{delta:'-10'},{user_id:'3'},{created_at:'invalid'},{actor_id:0},{balance:'1e3'}]) {
  test(`invalid reward preserves original check-in ${JSON.stringify(patch)}`,async()=>{
    const f=harness();f.respond((path,options,previous)=>{if(!path.endsWith('/check-in'))return previous(path,options);const value=rewardReceipt();Object.assign(value.entry,patch);return value;});
    await f.start();await f.root.querySelector('[data-progression-check-in]').emit('click');
    assert.ok(f.P.read('check-in','2'));assert.match(f.root.querySelector('[data-progression-check-in]').textContent,/重试原签到/);
  });
}
test('body decoding after switching away and back cannot consume a purchase receipt',async()=>{
  const f=harness();await f.start();await f.root.querySelectorAll('[data-progression-buy]')[0].emit('click');
  const originalFetch=f.context.fetch;let finish;
  f.context.fetch=async(path,options)=>path.endsWith('/purchase')?{ok:true,status:200,json:()=>new Promise(resolve=>{finish=()=>resolve(orderReceipt(JSON.parse(options.body)));})}:originalFetch(path,options);
  const sending=f.root.querySelector('[data-progression-confirm-submit]').emit('click');await tick();assert.equal(typeof finish,'function');
  await f.documentEvents.emit('chunnel:session',{detail:{authenticated:true,id:3,csrf_token:'csrf-other'}});
  await f.documentEvents.emit('chunnel:session',{detail:{authenticated:true,id:2,csrf_token:'csrf-one'}});finish();await sending;
  assert.ok(f.P.read('purchase','2'));assert.equal(f.root.querySelector('[data-progression-plans]').children.length,0);
  assert.match(f.root.querySelector('[data-progression-status]').textContent,/登录状态已变化/);
});
test('timed out response body cannot turn an uncertain write into success',async()=>{
  const f=harness();let expire,finish;
  f.context.setTimeout=callback=>{expire=callback;return 1;};f.context.clearTimeout=()=>{};
  const connection=f.P.transport('2','',()=>{});await connection.check();
  f.context.fetch=async()=>({ok:true,status:200,json:()=>new Promise(resolve=>{finish=resolve;})});
  const request=connection.request('/api/v1/social/progression/purchase',{method:'POST',verify:false,body:{}});await tick();expire();finish({ok:true});
  await assert.rejects(request,/超时/);
});
