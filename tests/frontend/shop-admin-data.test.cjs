const test = require('node:test');
const assert = require('node:assert/strict');
const { loadModules } = require('./helpers/typescript.cjs');
const F = require('./helpers/shop-storefront-fixtures.cjs');
const C = require('./helpers/shop-coupon-fixtures.cjs');
const [D, A, H] = loadModules(['frontend/shared/shop-admin-data.ts', 'frontend/shared/shop-admin-actions.ts', 'frontend/shared/shop-admin-helpers.ts'], { structuredClone, Date, TextEncoder, URL, DOMException });
const plain = value => structuredClone(value);
const catalog = () => D.adminCatalog(F.catalog().catalog);
const shipping = () => D.adminOrder(F.order({ kind: 'physical', status: 'awaiting_shipment', paid_amount: '5000', schedule: [F.part({ paid_at: F.now, payment_ref: 'payment-1' })] }));
const action = { kind: 'ship', carrier: '顺丰', tracking: 'SF123456' };
const shipped = before => ({ ...plain(before), status: 'shipped', carrier: action.carrier, tracking: action.tracking, shipped_at: F.now });

test('admin catalog retains private delivery, codes, rich source and future fields', () => {
  const raw = F.catalog().catalog;
  Object.assign(raw.products[0], { delivery: '仅付款后展示', codes: ['PRIVATE-A'], description_source: '<p>详情</p>', description_author_id: '9223372036854775807', future: { keep: true } });
  raw.future_catalog = { keep: 'v2' };
  const result = D.adminCatalog(raw);
  assert.equal(result.products[0].delivery, '仅付款后展示');
  assert.deepEqual(plain(result.products[0].codes), ['PRIVATE-A']);
  assert.equal(result.products[0].description_author_id, '9223372036854775807');
  assert.equal(result.products[0].future.keep, true);
  assert.equal(result.future_catalog.keep, 'v2');
  for (const patch of [{ codes: ['same', 'same'] }, { description_author_id: '-1' }, { description_source: '字'.repeat(90000) }, { delivery: 12 }, { stock: 'NaN' }]) assert.throws(() => D.adminProduct({ ...raw.products[0], ...patch }));
});

test('catalog saves require exact version and preserve all submitted fields', () => {
  const before = catalog(), saved = plain(before); saved.version = String(BigInt(before.version) + 1n);
  assert.equal(D.savedAdminCatalog({ catalog: saved }, before).version, saved.version);
  for (const mutate of [v => v.version = before.version, v => v.products[0].price = '1', v => v.products[0].delivery = '丢失私有字段', v => v.enabled = !v.enabled]) {
    const broken = plain(saved); mutate(broken); assert.throws(() => D.savedAdminCatalog({ catalog: broken }, before));
  }
  before.products[0].description_source = '<p>新详情</p>'; saved.products[0].description_source = before.products[0].description_source; saved.products[0].description_author_id = '1';
  assert.equal(D.savedAdminCatalog({ catalog: saved }, before).products[0].description_author_id, '1');
});

test('catalog merge recovers already-created products and preserves concurrent unknown fields', () => {
  const base = catalog(), desired = plain(base), latest = plain(base);
  desired.products.push({ ...plain(base.products[0]), key: 'created', description_author_id: '0' });
  latest.products.push({ ...plain(desired.products[1]), description_author_id: '1' }); latest.version = '3'; latest.future = 'keep';
  const result = H.mergeCatalog(base, desired, latest);
  assert.equal(result.conflicts.length, 0); assert.equal(result.catalog.products.length, 2); assert.equal(result.catalog.future, 'keep');
  latest.products[1].price = '99'; assert.equal(H.mergeCatalog(base, desired, latest).conflicts.length, 1);
  assert.equal(H.equal({ a: 1, b: [2] }, { b: [2], a: 1 }), true);
});

test('coupon versions stay numeric and a changed saved rule never counts as success', () => {
  const coupons = D.adminCoupons({ version: 3, coupons: [C.coupon()] }).coupons;
  assert.equal(D.savedAdminCoupons({ version: 4, coupons }, 3, coupons).version, 4);
  assert.throws(() => D.adminCoupons({ version: '4', coupons }));
  assert.throws(() => D.savedAdminCoupons({ version: 4, coupons: [{ ...coupons[0], value: '1' }] }, 3, coupons));
  assert.throws(() => D.savedAdminCoupons({ version: 6, coupons }, 3, coupons));
});

test('moderation receipts verify immutable review, version, reason and moderator', () => {
  const before = D.adminReview(F.ownReview()), input = { version: '1', reply: '谢谢', hidden: true, reason: '含个人资料' };
  const saved = { ...plain(before), version: '2', reply: input.reply, hidden: true, hidden_reason: input.reason, moderator_id: '1', replied_at: F.now };
  assert.equal(D.savedAdminReview({ review: saved }, before, input, '1').hidden, true);
  for (const patch of [{ content: '被替换' }, { version: '3' }, { hidden_reason: '其他原因' }, { moderator_id: '2' }]) assert.throws(() => D.savedAdminReview({ review: { ...saved, ...patch } }, before, input, '1'));
  assert.throws(() => D.adminReviews({ reviews: [saved], total: 1, page: 2, page_size: 20 }, 1));
});

test('shipment, refund and review actions validate server text limits', () => {
  for (const bad of [{ ...action, carrier: 'a'.repeat(81) }, { ...action, tracking: 'line\nbreak' }, { kind: 'refund', reference: 'x', note: 'line\nbreak' }, { kind: 'review-refund', approved: 'false', note: '' }]) assert.throws(() => A.orderAction(bad));
  assert.equal(A.orderAction({ kind: 'review-refund', approved: false, note: '多行\n说明' }).note, '多行\n说明');
});

test('order receipt rejects changed identity or payment and accepts subsequent buyer confirmation', () => {
  const before = shipping(), after = shipped(before);
  assert.equal(D.adminOrderReceipt({ order: after }, before, action).status, 'shipped');
  assert.equal(D.adminOrderReceipt({ order: { ...after, status: 'completed', completed_at: F.now } }, before, action, true).status, 'completed');
  for (const patch of [{ user_id: '3' }, { tracking: 'different' }, { total: '99' }]) assert.throws(() => D.adminOrderReceipt({ order: { ...after, ...patch } }, before, action));
});

test('lost refund rejection cannot be inferred from an ambiguous refund state', () => {
  const before = D.adminOrder(F.order({ status: 'refund_requested', mode: 'group', group_id: F.groupID, refund_previous_status: 'awaiting_group', paid_amount: '5000', schedule: [F.part({ paid_at: F.now })] }));
  const reject = { kind: 'review-refund', approved: false, note: '已核对' };
  const after = { ...plain(before), status: 'refund_required', refund_previous_status: '', refund_review: reject.note };
  assert.equal(D.adminOrderReceipt({ order: after }, before, reject).status, 'refund_required');
  assert.throws(() => D.adminOrderReceipt({ order: after }, before, reject, true));
  assert.equal(D.adminOrderReceipt({ order: { ...after, status: 'awaiting_group' } }, before, reject, true).status, 'awaiting_group');
  assert.throws(() => D.adminOrderReceipt({ order: { ...after, status: 'shipped' } }, before, reject, true));
});

function setup(options = {}) {
  const store = new Map(), calls = []; let current = shipping(), alive = true;
  const storage = { getItem: key => store.get(key) ?? null, setItem: (key, value) => store.set(key, value), removeItem: key => store.delete(key), ...options.storage };
  const actions = A.createAdminActions({ owner: '1', storage, alive: () => alive,
    api: async (path, method, body) => { calls.push({ path, method, body }); if (!method) return { orders: [plain(current)] }; return options.write ? options.write(current) : { order: shipped(current) }; } });
  return { actions, store, calls, before: current, setCurrent: value => current = value, retire: () => alive = false };
}

test('lost shipment reply persists original action and retry verifies without another POST', async () => {
  const env = setup({ write: async current => { env.setCurrent(shipped(current)); throw Error('response lost'); } });
  await assert.rejects(env.actions.perform(env.before, action));
  assert.equal(env.store.size, 1);
  await assert.rejects(env.actions.perform(env.before, { ...action, tracking: 'another' }), /原内容/);
  const result = await env.actions.perform(env.before, action);
  assert.equal(result.order.status, 'shipped'); assert.equal(env.store.size, 0); assert.equal(env.calls.filter(c => c.method === 'POST').length, 1);
});

test('unmatched receipts retain recovery and block a different order operation', async () => {
  const env = setup({ write: async current => ({ order: { ...shipped(current), tracking: 'wrong' } }) });
  await assert.rejects(env.actions.perform(env.before, action), /未匹配/); assert.equal(env.store.size, 1);
  await assert.rejects(env.actions.perform(env.before, { kind: 'refund', reference: 'ref', note: '核对' }), /原内容/);
});

test('corrupt or unavailable storage prevents an untracked business write', async () => {
  for (const storage of [{ getItem: () => '{corrupt' }, { setItem: () => { throw Error('storage denied'); } }]) {
    const env = setup({ storage }); await assert.rejects(env.actions.perform(env.before, action)); assert.equal(env.calls.some(c => c.method === 'POST'), false);
  }
});

test('concurrent action submissions share one write and late identity response stays unconfirmed', async () => {
  let release; const env = setup({ write: current => new Promise(resolve => { release = () => resolve({ order: shipped(current) }); }) });
  const first = env.actions.perform(env.before, action); while (!release) await new Promise(resolve => setImmediate(resolve));
  await assert.rejects(env.actions.perform(env.before, action), /正在处理/);
  env.retire(); release(); await assert.rejects(first, /会话/); assert.equal(env.store.size, 1);
  assert.equal(env.calls.filter(c => c.method === 'POST').length, 1);
});

test('storage cleanup failure reports confirmed success and a remaining recovery record', async () => {
  const env = setup({ storage: { removeItem: () => { throw Error('cleanup denied'); } } });
  const result = await env.actions.perform(env.before, action);
  assert.equal(result.order.status, 'shipped'); assert.equal(result.recoveryPending, true); assert.equal(env.store.size, 1);
});
