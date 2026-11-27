const test = require('node:test');
const assert = require('node:assert/strict');
const { loadModule } = require('./helpers/typescript.cjs');
const { amountUnits, amountText } = loadModule('frontend/core/resource-data.ts');

test('resource editor represents tenths with exact integer units', () => {
  for (const [text, units] of [['0.1',1],['0.3',3],['100',1000],['100.0',1000],['99999999999.9',999999999999]]) {
    assert.equal(amountUnits(text,1),units);
    assert.equal(amountUnits(amountText(units,1),1),units);
  }
  for (const text of ['0.01','1.11','-0.1','1e-1','NaN','Infinity','0','','100000000000.1']) assert.throws(() => amountUnits(text,1));
  assert.equal(amountText(1,1),'0.1');
  assert.equal(amountText(1000,1),'100.0');
});

test('resource money uses the selected currency precision without rounding', () => {
  assert.equal(amountUnits('9.90',2),990);
  assert.equal(amountText(990,2),'9.90');
  assert.equal(amountUnits('30',0),30);
  assert.throws(() => amountUnits('30.1',0));
  assert.equal(amountUnits(amountText(1,1),2),10);
  assert.equal(amountUnits(amountText(100,0),1),1000);
  assert.throws(() => amountUnits(amountText(1,2),1));
  assert.throws(() => amountUnits('1',null));
  assert.throws(() => amountText(Number.MAX_SAFE_INTEGER+1,1));
});
