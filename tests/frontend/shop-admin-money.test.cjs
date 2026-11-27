const test = require('node:test');
const assert = require('node:assert/strict');
const { moneyUnits, moneyText } = require('./helpers/typescript.cjs').loadModule('frontend/shared/shop-admin-helpers.ts');

test('shop editor preserves exact minor units and never rounds entered prices', () => {
  for (const [text, expected] of [['0', '0'], ['0.01', '1'], ['19.90', '1990'], ['1000000000.00', '100000000000'], ['0.1000', '10']]) {
    assert.equal(moneyUnits(text), expected);
    assert.equal(moneyUnits(moneyText(expected)), expected);
  }
  for (const text of ['-1', '1e2', 'NaN', 'Infinity', '1.001', '0x10', '', '10000000000.01']) assert.throws(() => moneyUnits(text));
  assert.equal(moneyUnits('0.1', 1), '1');
  assert.equal(moneyText('1000', 1), '100.0');
  assert.equal(moneyUnits('100000000000.0', 1), '1000000000000');
  assert.throws(() => moneyUnits('0.01', 1));
  assert.equal(moneyUnits('30', 0), '30');
  assert.equal(moneyText('30', 0), '30');
  assert.throws(() => moneyUnits('30.01', 0));
});
