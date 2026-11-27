const test = require('node:test'), assert = require('node:assert/strict'), vm = require('node:vm'), esbuild = require('esbuild');
const F = require('./helpers/shop-storefront-fixtures.cjs');
const C = require('./helpers/shop-coupon-fixtures.cjs');
function load(entry) {
  const compiled = esbuild.buildSync({ entryPoints: [entry], bundle: true, write: false, format: 'cjs', target: 'es2022' }).outputFiles[0].text;
  const value = { exports: {} }; vm.runInNewContext(compiled, { module: value, exports: value.exports, URL, Date }); return value.exports;
}
const D = load('frontend/shared/shop-data.ts'), H = load('frontend/shared/shop-helpers.ts');
const R = load('frontend/shared/shop-recovery.ts');
const plain = value => JSON.parse(JSON.stringify(value));

test('storefront catalog understands Go nil slices and wallet-prefixed currencies', () => {
  const result = D.shopCatalogResponse(F.catalog());
  assert.equal(result.catalog.products[0].price, '2500');
  assert.deepEqual(plain(result.catalog.products[0].variants), []);
  assert.deepEqual(plain(result.raffle_quotas), {});
  assert.equal(result.currencies[1].balance, '9007199254740993');
  for (const change of [v => delete v.catalog, v => delete v.catalog.products, v => v.catalog.version = 2, v => v.catalog.products.push(v.catalog.products[0]), v => v.catalog.products[0].price = 2500, v => v.catalog.products[0].currency = 'wallet:missing', v => v.server_time = F.zero, v => v.channels.push(v.channels[0])]) {
    const value = F.catalog(); change(value); assert.throws(() => D.shopCatalogResponse(value), String(change));
  }
});
test('product Unicode limits and raffle prize length follow Go rune limits', () => {
  assert.equal(D.shopProduct(F.product({ summary: '🎟'.repeat(4000) })).summary.length, 8000);
  const raffle = { mode: 'instant', draw_at: F.zero, winners: 1, win_bps: 100, prize: '奖'.repeat(1000) };
  assert.equal(D.shopRafflePlan(raffle).prize.length, 1000);
  assert.throws(() => D.shopRafflePlan({ ...raffle, winners: 0 }));
  assert.throws(() => D.shopRafflePlan({ ...raffle, mode: 'scheduled' }));
  assert.throws(() => D.shopRafflePlan({ ...raffle, prize: '奖'.repeat(1001) }));
});
test('good review totals above 100 remain counts, with consistent star distribution', () => {
  const stats = { count: 125, average: 5, positive: 125, distribution: [0, 0, 0, 0, 125], sold: 130 };
  assert.equal(D.shopCatalogResponse(F.catalog({ product_stats: { digital: stats } })).product_stats.digital.positive, 125);
  for (const patch of [{ positive: 126 }, { count: 124 }, { positive: 100 }, { distribution: [0, 0, 0, 125] }, { average: 6 }]) {
    assert.throws(() => D.shopCatalogResponse(F.catalog({ product_stats: { digital: { ...stats, ...patch } } })));
  }
});
test('quote amounts and schedules are checked with integer arithmetic', () => {
  assert.equal(D.shopQuote(F.quote()).total, '5000');
  for (const change of [v => v.unit_price = '2501', v => v.quantity = 3, v => v.total = '5001', v => v.discount = '1', v => v.pay_now = '4999', v => v.schedule[0].principal = '4999', v => v.schedule[0].index = 1, v => v.schedule.push(v.schedule[0]), v => v.schedule = null, v => v.precision = 7, v => v.total = '1000000000001', v => v.total = '5e3']) {
    const value = F.quote(); change(value); assert.throws(() => D.shopQuote(value), String(change));
  }
});
test('quotes must match the selected product, variant, quantity, mode and coupon', () => {
  assert.doesNotThrow(() => D.matchQuote(D.shopQuote(F.quote()), F.draft(), F.product()));
  for (const patch of [{ product: 'other' }, { variant: 'large' }, { quantity: 1 }, { mode: 'presale' }, { coupon: 'PHOTO-5' }]) {
    assert.throws(() => D.matchQuote(D.shopQuote(F.quote()), F.draft(patch)));
  }
  assert.throws(() => D.matchQuote(D.shopQuote(F.quote()), F.draft(), F.product({ currency: 'wallet:main' })));
});
test('created receipts match account, version and selected terms without requiring a private request ID echo', () => {
  const value = D.createdShopOrder({ order: F.order({ request_id: '', fingerprint: '', snapshot: {} }) }, '2', F.draft(), D.shopQuote(F.quote()));
  assert.equal(value.id, F.orderID); assert.equal(value.request_id, undefined);
  for (const patch of [{ user_id: '3' }, { product: 'other' }, { version: '3', catalog_version: '3' }, { kind: 'cdk' }, { id: 'so_bad' }]) {
    assert.throws(() => D.createdShopOrder({ order: F.order(patch) }, '2', F.draft(), D.shopQuote(F.quote())));
  }
  assert.throws(() => D.createdShopOrder({ order: F.order() }, '2', F.draft(), D.shopQuote(F.quote({ version: '3' }))));
});
test('physical purchase receipts require the original address and chosen group', () => {
  const address = F.address({ name: '买家', phone: '12345', detail: '测试地址' });
  const q = F.quote({ kind: 'physical', mode: 'group', requires_address: true });
  const draft = F.draft({ mode: 'group', group_id: F.groupID, address });
  const order = F.order({ ...q, address, group_id: F.groupID });
  assert.equal(D.createdShopOrder({ order }, '2', draft, D.shopQuote(q)).address.name, '买家');
  assert.throws(() => D.createdShopOrder({ order: { ...order, address: { ...address, detail: '其他地址' } } }, '2', draft));
  assert.throws(() => D.createdShopOrder({ order: { ...order, group_id: 'sg_' + 'c'.repeat(28) } }, '2', draft));
});
test('creation can recompute relative due dates but cannot change the fixed deposit balance deadline', () => {
  const immediate = F.order(); immediate.schedule[0].due_at = F.later;
  assert.doesNotThrow(() => D.createdShopOrder({ order: immediate }, '2', F.draft(), D.shopQuote(F.quote())));
  const schedule = [F.part({ title: '定金', principal: '2000', amount: '2000' }), F.part({ index: 1, title: '尾款', principal: '3000', amount: '3000', due_at: '2026-12-05T00:00:00Z', available_at: '2026-12-01T00:00:00Z' })];
  const quote = F.quote({ mode: 'deposit', schedule, pay_now: '2000' }), draft = F.draft({ mode: 'deposit' });
  assert.doesNotThrow(() => D.createdShopOrder({ order: F.order(quote) }, '2', draft, D.shopQuote(quote)));
  const changed = structuredClone(schedule); changed[1].due_at = '2026-12-06T00:00:00Z';
  assert.throws(() => D.createdShopOrder({ order: F.order({ ...quote, schedule: changed }) }, '2', draft, D.shopQuote(quote)));
});
test('historical payment amounts remain valid after a refund and do not become zero', () => {
  const value = F.order({ status: 'refunded', paid_amount: '5000', schedule: [F.part({ paid_at: F.now, payment_ref: 'payment-receipt' })] });
  assert.equal(D.shopOrder(value, '2', F.orderID).paid_amount, '5000');
  assert.throws(() => D.shopOrder({ ...value, paid_amount: '0' }, '2'));
  assert.throws(() => D.shopOrder({ ...value, catalog_version: '3' }, '2'));
  assert.throws(() => D.shopOrder(value, '2', 'so_' + 'b'.repeat(28)));
});
test('multiple checkout attempts may belong to one installment, but payment identities remain unique', () => {
  const first = F.payment({ status: 'cancelled', index: 0 });
  const second = F.payment({ id: 'np_' + 'd'.repeat(28), index: 0 });
  assert.equal(D.shopOrderResponse({ order: F.order(), payments: [first, second] }, '2', F.orderID).payments.length, 2);
  assert.throws(() => D.shopOrderResponse({ order: F.order(), payments: [first, first] }, '2', F.orderID));
  for (const patch of [{ index: 1 }, { index: -1 }, { user_id: '3' }, { amount: '50.01' }, { currency: 'USD' }, { kind: 'resource' }]) {
    assert.throws(() => D.shopOrderResponse({ order: F.order(), payments: [{ ...second, ...patch }] }, '2', F.orderID));
  }
});
test('payment parsing accepts Go maximum amounts independently of the donation input limit', () => {
  const quote = F.quote({ quantity: 1, unit_price: '1000000000000', original_subtotal: '1000000000000', subtotal: '1000000000000', total: '1000000000000', pay_now: '1000000000000', schedule: [F.part({ principal: '1000000000000', amount: '1000000000000' })] });
  const order = D.shopOrder(F.order(quote), '2');
  assert.equal(D.shopPayment(F.payment({ amount: '10000000000.00' }), order, 0).amount, '10000000000.00');
  assert.throws(() => H.units('10000000000.00'));
});
test('pending recovery preserves legacy requests and validates any attached quote', () => {
  assert.equal(D.shopPending({ owner: '2', draft: F.draft() }, '2').draft.request_id, 'storefront-original-request');
  assert.equal(D.shopPending({ owner: '2', draft: F.draft(), quote: F.quote() }, '2').quote.total, '5000');
  for (const patch of [{ owner: '3' }, { draft: F.draft({ request_id: '' }) }, { draft: F.draft({ accepted: false }) }, { quote: F.quote({ version: '3' }) }, { quote: F.quote({ product: 'other' }) }]) {
    assert.throws(() => D.shopPending({ owner: '2', draft: F.draft(), ...patch }, '2'));
  }
  assert.throws(() => D.shopPending('corrupt recovery', '2'));
});
test('recovery storage cannot replace an unresolved request or erase another request after a race', () => {
  const data = new Map(), storage = { getItem: key => data.get(key) ?? null, setItem: (key, value) => data.set(key, value), removeItem: key => data.delete(key) };
  const recovery = R.createShopRecovery(storage), first = { owner: '2', draft: F.draft(), quote: F.quote() };
  recovery.remember(first);
  assert.equal(recovery.read('2').draft.request_id, first.draft.request_id);
  assert.equal(recovery.read('3'), null);
  assert.throws(() => recovery.remember({ ...first, draft: F.draft({ request_id: 'new-request' }) }));
  assert.throws(() => recovery.remember({ ...first, draft: F.draft({ quantity: 1 }) }));
  assert.equal(recovery.forget({ ...first, draft: F.draft({ request_id: 'other-request' }) }), false);
  assert.ok(recovery.read('2'));
  assert.equal(recovery.forget(first), true); assert.equal(recovery.read('2'), null);
});
test('corrupt and unavailable storage never look like an empty recovery slot', () => {
  for (const storage of [
    { getItem: () => '{broken', setItem: () => assert.fail('Must not overwrite broken data'), removeItem: () => assert.fail('Must not erase broken data') },
    { getItem: () => { throw Error('blocked'); }, setItem: () => assert.fail('Must not allocate on read failure'), removeItem: () => {} },
  ]) {
    const recovery = R.createShopRecovery(storage);
    assert.throws(() => recovery.read('2'));
    assert.throws(() => recovery.remember({ owner: '2', draft: F.draft() }));
  }
  const noWrite = R.createShopRecovery({ getItem: () => null, setItem: () => { throw Error('quota'); }, removeItem: () => {} });
  assert.throws(() => noWrite.remember({ owner: '2', draft: F.draft() }));
});
test('order updates can advance status while preserving every immutable payment and delivery term', () => {
  const previous = D.shopOrder(F.order(), '2');
  assert.equal(D.shopOrderUpdate(F.order({ status: 'cancelled' }), previous).status, 'cancelled');
  for (const patch of [{ product: 'other' }, { terms: 'different terms' }, { address: F.address({ detail: 'changed' }) }, { expires_at: '2026-12-01T00:00:00Z' }, { schedule: [F.part({ due_at: F.later })] }]) {
    assert.throws(() => D.shopOrderUpdate(F.order(patch), previous));
  }
});
test('personal quotas require the exact current account and do not fabricate unavailable values', () => {
  const response = C.session({ raffle_quotas: { raffle: { product_left: -1, campaign_left: 10, user_product_left: 0, user_campaign_left: 2 } } });
  assert.equal(D.shopSession(response, '2').raffle_quotas.raffle.product_left, -1);
  for (const patch of [{ user_id: '3' }, { csrf_token: '' }, { raffle_quotas: {} }]) {
    if (patch.raffle_quotas) assert.deepEqual(plain(D.shopSession({ ...response, ...patch }, '2').raffle_quotas), {});
    else assert.throws(() => D.shopSession({ ...response, ...patch }, '2'));
  }
  assert.throws(() => D.shopSession({ ...response, raffle_quotas: { raffle: { product_left: -2 } } }, '2'));
});
test('review parsing distinguishes public reviews from order-bound private receipts', () => {
  const page = { reviews: [F.review()], summary: F.summary({ count: 1, average: 5, positive: 1, distribution: [0, 0, 0, 0, 1] }), total: 1, page: 1, page_size: 10 };
  assert.equal(D.shopReviews(page, 1).reviews[0].content, '交付及时');
  assert.throws(() => D.shopReviews(page, 2));
  const order = D.shopOrder(F.order({ status: 'completed' }), '2');
  assert.equal(D.shopOrderReview({ review: F.ownReview(), eligible: false, note: '' }, order).review.order_id, F.orderID);
  assert.throws(() => D.ownShopReview(F.ownReview({ user_id: '3' }), order));
  assert.throws(() => D.ownShopReview(F.ownReview({ product: 'other' }), order));
  assert.equal(D.shopOrderReview({ review: null, eligible: true, note: '' }, order).review, null);
});
test('storefront helpers retain exact cents, supported purchase modes and strict calendar dates', () => {
  assert.equal(H.units('19.90'), '1990'); assert.equal(H.money('100000000000'), '¥1000000000.00');
  assert.equal(H.money('12', 'wallet:main', 1, { name: '积分', symbol: '分' }), '分1.2');
  for (const value of ['-1', '1e3', '1.001', 'NaN', '1000000000.01']) assert.throws(() => H.units(value));
  assert.deepEqual(plain(H.optionsFor({ mode: 'group' })), ['group', 'direct']);
  assert.deepEqual(plain(H.optionsFor({ mode: 'deposit' })), ['deposit']);
  assert.equal(H.isDate(F.zero), false); assert.equal(H.isDate('2026-02-30T00:00:00Z'), false); assert.equal(H.isDate(F.now), true);
});
test('storefront URLs reject credentials, controls and foreign insecure checkout destinations', () => {
  assert.equal(H.safeURL('/image.png', 'http://localhost:8000/shop/'), 'http://localhost:8000/image.png');
  assert.equal(H.safeURL('https://pay.example.test/order'), 'https://pay.example.test/order');
  for (const url of ['javascript:alert(1)', 'data:text/html,hello', 'https://user:secret@example.test/', 'https://example.test/\ncheckout', 'http://foreign.test/', 'https:\\example.test']) assert.equal(H.safeURL(url), '', url);
});
test('raffle helpers keep cutoff distinct from result publication and enforce the tightest quota', () => {
  const deadline = Date.parse(F.later);
  assert.equal(H.raffleDeadline({ kind: 'raffle', raffle: { mode: 'scheduled', draw_at: F.later } }), deadline);
  assert.equal(H.rafflePhase(deadline, deadline, { entries: 0 }, deadline - 1), '已截止，等待开奖');
  assert.equal(H.rafflePhase(deadline, deadline, { entries: 0 }, deadline), '本轮无人参与');
  assert.equal(H.rafflePhase(deadline, deadline, { drawn: true }, deadline), '已开奖');
  assert.deepEqual(plain(H.raffleCountdown(deadline, deadline + 1)), { days: 0, hours: 0, minutes: 0, seconds: 0 });
  const product = { stock: '-1', raffle: { ticket_limit: 100, user_limit: 2, order_limit: 1 } };
  assert.equal(H.rafflePurchaseCapacity(product, { product_left: 100 }, { user_product_left: 0 }).personalLimit, true);
  assert.equal(H.rafflePurchaseCapacity(product, { product_left: 0 }, { user_product_left: 0 }).personalLimit, false);
  assert.equal(H.rafflePurchaseCapacity(product).max, 1);
  assert.equal(H.rafflePurchaseCapacity({ stock: -1, raffle: {} }).max, 99);
});
