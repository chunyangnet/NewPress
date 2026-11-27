const test = require('node:test');
const assert = require('node:assert/strict');
const shop = require('./helpers/typescript.cjs').loadModule('frontend/shared/shop-helpers.ts', { Date, URL });
const {money, units, isDate} = shop;
const optionsFor = product => Array.from(shop.optionsFor(product));
test('storefront preserves integer cents and rejects ambiguous prices', () => {
  assert.equal(units('19.90'), '1990'); assert.equal(units('0.01'), '1'); assert.equal(money('100000000000'),'¥1000000000.00');
  for (const value of ['-1','1e3','1.001','NaN','1000000000.01']) assert.throws(() => units(value));
});
test('external payment decimals convert exactly before storefront formatting', () => {
  const amount = units('34.00', 2);
  assert.equal(amount, '3400');
  assert.equal(money(amount, 'CNY', 2), '¥34.00');
  assert.equal(money(units('12.5', 2), 'CNY', 2), '¥12.50');
});
test('only optional group and installment modes permit direct purchase', () => {
  assert.deepEqual(optionsFor({mode:'deposit'}),['deposit']); assert.deepEqual(optionsFor({mode:'presale'}),['presale']);
  assert.deepEqual(optionsFor({mode:'group'}),['group','direct']); assert.equal(isDate('0001-01-01T00:00:00Z'),false);
});
