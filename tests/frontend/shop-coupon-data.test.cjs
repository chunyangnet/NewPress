const test = require('node:test'), assert = require('node:assert/strict'), vm = require('node:vm'), esbuild = require('esbuild');
const F = require('./helpers/shop-coupon-fixtures.cjs');
const compiled = esbuild.buildSync({ entryPoints: ['frontend/shared/shop-coupon-data.ts'], bundle: true, write: false, format: 'cjs', target: 'es2022' }).outputFiles[0].text;
const result = { exports: {} }; vm.runInNewContext(compiled, { module: result, exports: result.exports, Date }); const D = result.exports;

test('coupon sessions retain exact wallet balances and understand wallet-prefixed settlement currencies', () => {
  const raw = F.session(); raw.coupons[0].currency = 'wallet:main'; raw.coupons[0].value = '12';
  const value = D.couponSession(raw, '2'); assert.equal(value.currencies[1].balance, '9007199254740993');
  assert.equal(D.couponAmount('12', 'wallet:main', value.currencies), '分1.2');
  assert.equal(D.couponAmount('500', 'CNY', value.currencies), '¥5.00');
  assert.equal(value.coupons[0].kinds.length, 0);
});
test('coupon sessions reject missing fields, wrong owners, absent metadata and false booleans', () => {
  for (const change of [v => delete v.csrf_token, v => v.csrf_token = '', v => v.user_id = 2, v => v.user_id = '3', v => delete v.coupons, v => v.currencies = [], v => v.currencies[0].precision = 3, v => v.currencies[0].enabled = 'true', v => v.currencies[1].balance = 12, v => v.currencies[1].key = 'wallet:../../private', v => v.coupons.push(v.coupons[0])]) {
    const value = F.session(); change(value); assert.throws(() => D.couponSession(value, '2'));
  }
});
test('coupon eligibility is never inferred from malformed counts, dates, conditions or amounts', () => {
  for (const change of [v => v.value = '1e3', v => v.value = '1000000000001', v => v.value = 500, v => v.per_user_limit = 0, v => v.claimed = true, v => v.can_claim = 'true', v => v.user_claim_count = 3, v => v.available_count = 1, v => v.remaining = 9, v => v.ends_at = v.starts_at, v => v.ends_at = '2026-02-30T00:00:00Z', v => v.kinds = ['service'], v => { v.kind = 'percent'; v.value = '10001'; }, v => delete v.minimum]) {
    const value = F.coupon(); change(value); assert.throws(() => D.couponOption(value), String(change));
  }
  assert.equal(D.couponOption(F.coupon({ kind: 'percent', value: '1250', currency: '', minimum: '0' })).value, '1250');
  assert.equal(D.couponOption(F.coupon({ claim_limit: 0, remaining: 0 })).remaining, 0);
});
test('claim confirmation matches both account and coupon and requires an unused available receipt', () => {
  assert.equal(D.couponClaim(F.claim(), '2', 'PHOTO-5').id, F.claim().claim.id);
  for (const patch of [{ user_id: '3' }, { code: 'OTHER-5' }, { status: 'used' }, { id: 'sc_bad' }, { order_id: 'so_0123456789abcdef0123456789ab' }, { used_at: '2026-11-26T00:00:00Z' }, { claimed_at: F.zero }, { user_id: 2 }]) assert.throws(() => D.couponClaim(F.claim(patch), '2', 'PHOTO-5'));
  for (const key of ['id', 'code', 'user_id', 'status', 'claimed_at', 'used_at']) { const value = F.claim(); delete value.claim[key]; assert.throws(() => D.couponClaim(value, '2', 'PHOTO-5')); }
});
test('pending non-idempotent claims retain exact owner, coupon and original count', () => {
  const pending = { owner: '2', code: 'PHOTO-5', count: 1 };
  assert.equal(D.pendingCoupon(pending, '2', 'PHOTO-5').count, 1);
  for (const patch of [{ owner: '3' }, { code: 'OTHER-5' }, { count: '1' }, { count: -1 }, { confirmed: 'wrong' }, { rejected: 'true' }, { confirmed: F.claim().claim.id, rejected: true }]) assert.throws(() => D.pendingCoupon({ ...pending, ...patch }, '2', 'PHOTO-5'));
  assert.equal(D.pendingCoupon({ ...pending, confirmed: F.claim().claim.id }, '2', 'PHOTO-5').confirmed, F.claim().claim.id);
});
