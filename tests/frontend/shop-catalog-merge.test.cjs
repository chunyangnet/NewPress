const test = require('node:test');
const assert = require('node:assert/strict');
const helpers = require('./helpers/typescript.cjs').loadModule('frontend/shared/shop-admin-helpers.ts', { structuredClone });
const mergeCatalog = (...args) => structuredClone(helpers.mergeCatalog(...args));

test('saving a title preserves concurrent reservations and newly added products', () => {
  const base = { version: '4', enabled: true, products: [{ key: 'code', title: '旧标题', stock: '3', codes: ['A', 'B', 'C'] }] };
  const next = structuredClone(base); next.products[0].title = '新标题';
  const latest = structuredClone(base); latest.version = '6'; latest.products[0].stock = '2'; latest.products[0].codes = ['B', 'C']; latest.products.push({ key: 'new', title: '其他管理员添加' });
  const result = mergeCatalog(base, next, latest);
  assert.deepEqual(result.conflicts, []);
  assert.equal(result.catalog.version, '6');
  assert.equal(result.catalog.products[0].title, '新标题');
  assert.equal(result.catalog.products[0].stock, '2');
  assert.deepEqual(result.catalog.products[0].codes, ['B', 'C']);
  assert.equal(result.catalog.products.length, 2);
  assert.equal(latest.products[0].title, '旧标题');
});

test('concurrent stock, code, configuration and key conflicts never overwrite fresh values', () => {
  const base = { version: '4', terms: '旧规则', products: [{ key: 'code', title: '卡密', stock: '3', codes: ['A', 'B', 'C'] }] };
  const next = structuredClone(base); next.terms = '本次规则'; next.products[0].stock = '4'; next.products[0].codes.push('D'); next.products.push({ key: 'new', title: '新建' });
  const latest = structuredClone(base); latest.version = '5'; latest.terms = '已更新规则'; latest.products[0].stock = '2'; latest.products[0].codes = ['B', 'C']; latest.products.push({ key: 'new', title: '已被使用' });
  const result = mergeCatalog(base, next, latest);
  assert.equal(result.conflicts.length, 4);
  assert.deepEqual(result.catalog, latest);
});
