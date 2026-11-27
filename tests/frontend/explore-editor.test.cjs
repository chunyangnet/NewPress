const test = require('node:test');
const assert = require('node:assert/strict');
const {loadModule} = require('./helpers/typescript.cjs');
const {exploreItemRoles,navigationTypeLabel} = loadModule('frontend/core/navigation-model.ts');

test('explore draft roles follow parent changes without mutating payload', () => {
  const items = [{id:1,parent_id:0},{id:2,parent_id:1},{id:3,parent_id:2},{id:4,parent_id:0}];
  const before = JSON.stringify(items);
  assert.deepEqual(Array.from(exploreItemRoles(items)),['分组（标题可点击）','分组内入口','分组内入口','独立入口']);
  assert.equal(JSON.stringify(items),before);
  items[1].parent_id=0;
  assert.deepEqual(Array.from(exploreItemRoles(items)),['独立入口','分组（标题可点击）','分组内入口','独立入口']);
  assert.deepEqual(Array.from(exploreItemRoles([])),[]);
});

test('navigation types use known labels and unknown types remain custom', () => {
  assert.equal(navigationTypeLabel('post'),'文章');
  for(const value of ['future-type','__proto__','constructor','']) assert.equal(navigationTypeLabel(value),'自定义');
});
