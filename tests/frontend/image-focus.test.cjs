const test = require('node:test');
const assert = require('node:assert/strict');
const { loadNativeModule } = require('./helpers/typescript.cjs');
const { cropPosition, focusPercent } = loadNativeModule('frontend/shared/image-focus.ts');
test('focus input and crop guards keep unavailable geometry out of previews', () => {
  assert.equal(focusPercent(''),null); assert.equal(focusPercent('Infinity'),null);
  assert.equal(focusPercent('-10'),0); assert.equal(focusPercent('110'),100);
  for (const args of [[0,100,1,50,50],[100,0,1,50,50],[100,100,0,50,50],[100,100,NaN,50,50],[100,100,1,NaN,50]]) assert.equal(cropPosition(...args),null);
});
test('wide and tall images crop toward the selected edge without stretching', () => {
  assert.deepEqual(cropPosition(800,400,1,0,50),[0,50]);
  assert.deepEqual(cropPosition(800,400,1,50,50),[50,50]);
  assert.deepEqual(cropPosition(800,400,1,100,50),[100,50]);
  assert.deepEqual(cropPosition(400,800,1,50,100),[50,100]);
  assert.deepEqual(cropPosition(400,400,1,100,0),[50,50]);
  for(const ratio of [1,16/9])for(const x of [0,25,50,75,100])for(const y of [0,50,100]) {
    assert.ok(cropPosition(800,600,ratio,x,y).every(value=>Number.isFinite(value)&&value>=0&&value<=100));
  }
});
