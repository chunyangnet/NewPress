const C = require('./shop-coupon-fixtures.cjs');
const P = require('./payment-fixtures.cjs');
const zero = C.zero, now = '2026-11-26T04:00:00Z', later = '2026-11-26T04:30:00Z';
const orderID = 'so_' + 'a'.repeat(28), groupID = 'sg_' + 'b'.repeat(28);
function address(patch = {}) { return { name: '', phone: '', region: '', detail: '', postal_code: '', ...patch }; }
function product(patch = {}) {
  return { key: 'digital', title: '数字商品', summary: '商品说明', image: '', category: '学习', kind: 'virtual', mode: 'direct', enabled: true,
    currency: 'CNY', precision: 2, price: '2500', compare_at: '0', stock: '-1', shipping_fee: '0', deposit: '0', max_donation: '0',
    release_at: zero, balance_due_at: zero, variants: null, bundle: null, group: null, installment: null, raffle: null, ...patch };
}
function part(patch = {}) { return { index: 0, title: '全款', principal: '5000', interest: '0', amount: '5000', due_at: now, available_at: zero, paid_at: zero, ...patch }; }
function quote(patch = {}) {
  return { coupon: '', coupon_title: '', discount: '0', precision: 2, original_subtotal: '5000', version: '2', product: 'digital', variant: '', quantity: 2,
    kind: 'virtual', mode: 'direct', currency: 'CNY', unit_price: '2500', subtotal: '5000', shipping: '0', interest: '0', total: '5000', pay_now: '5000',
    requires_address: false, schedule: [part()], interest_description: '', ...patch };
}
function draft(patch = {}) {
  return { coupon: '', product: 'digital', variant: '', quantity: 2, periods: 0, donation_amount: '0', group_id: '', mode: 'direct',
    version: '2', request_id: 'storefront-original-request', accepted: true, address: address(), ...patch };
}
function order(patch = {}) {
  return { ...quote(), id: orderID, user_id: '2', title: '数字商品', status: 'pending', paid_amount: '0', address: address(), group_id: '', deliveries: null, tickets: null,
    carrier: '', tracking: '', refund_reference: '', refund_note: '', created_at: now, updated_at: now, expires_at: later, completed_at: zero, shipped_at: zero,
    terms: '下单条款', refund_policy: '退款规则', catalog_version: '2', released: false, ...patch };
}
function summary(patch = {}) { return { count: 0, average: 0, positive: 0, distribution: [0, 0, 0, 0, 0], ...patch }; }
function catalog(patch = {}) {
  return { catalog: { version: '2', enabled: true, order_minutes: 30, terms: '下单条款', refund_policy: '退款规则', products: [product()] }, groups: [],
    channels: [{ key: 'epay', name: '支付渠道', currency: 'CNY', provider: 'epay' }], payment_enabled: true, currencies: C.currencies(), coupons: [],
    product_stats: { digital: { ...summary(), sold: 0 } }, raffle_rounds: null, raffle_quotas: null, server_time: now, ...patch };
}
function payment(patch = {}) { return P.order({ kind: 'shop', title: '数字商品', amount: '50.00', target_currency: '', credit: '0', created_at: now, expires_at: later, ...patch }); }
function review(patch = {}) { return { id: 'sr_' + 'c'.repeat(28), author: '买家', variant: '', rating: 5, content: '交付及时', anonymous: false, created_at: now, reply: '', replied_at: zero, ...patch }; }
function ownReview(patch = {}) { return { ...review(), order_id: orderID, user_id: '2', product: 'digital', title: '数字商品', hidden: false, hidden_reason: '', updated_at: now, moderator_id: '0', version: '1', ...patch }; }
module.exports = { zero, now, later, orderID, groupID, address, product, part, quote, draft, order, summary, catalog, payment, review, ownReview };
