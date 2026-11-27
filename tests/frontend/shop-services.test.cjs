const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm'), esbuild = require('esbuild');
const compiled = esbuild.buildSync({ entryPoints: ['frontend/shared/shop-services.ts'], bundle: true, write: false, format: 'cjs', target: 'es2022' }).outputFiles[0].text;
const result = { exports: {} }; vm.runInNewContext(compiled, { module: result, exports: result.exports, URL, Date });
const { resolve, safeLink } = result.exports;
const plain = value => JSON.parse(JSON.stringify(value));

test('service cards default to automatic while explicit empty or hidden configurations stay hidden', () => {
  const product = { kind: 'virtual', currency: 'CNY' };
  assert.deepEqual(plain(resolve(product).map(item => item.title)), ['订单交付', '人民币结算', '退款与售后']);
  assert.equal(resolve({ ...product, services: { mode: 'custom', items: [] } }).length, 0);
  assert.equal(resolve({ ...product, services: { mode: 'hidden', items: [{ title: '未公开草稿' }] } }).length, 0);
  assert.equal(resolve({ ...product, services: { mode: 'auto', items: [{ title: '未公开草稿' }] } }).length, 3);
});

test('module variables use the current currency, group and installment values without mutating stored templates', () => {
  const product = { kind: 'virtual', currency: 'POINT', precision: 0, shipping_fee: '12', group: { size: 5, hours: 48 }, installment: { periods: [3, 6], interval_days: 30, interest_bps: 50 }, services: { mode: 'custom', items: [{ icon: 'wallet', title: '{结算货币}结算', description: '{成团人数} 人，{拼团时限} 小时，每期 {每期费率}，运费 {运费}' }] } };
  const result = resolve(product, { currencies: [{ key: 'POINT', name: '积分', symbol: '积分 ', precision: 0 }] });
  assert.equal(result[0].title, '积分结算');
  assert.equal(result[0].description, '5 人，48 小时，每期 0.5%，运费 积分 12');
  assert.equal(product.services.items[0].title, '{结算货币}结算');
});

test('service cards preserve configured order, optional copy and safe navigation', () => {
  const services = { mode: 'custom', items: [{ icon: 'star', title: '买家评价', description: '', link: '#shop-product-reviews' }, { icon: 'info', title: '咨询', description: '自定义说明', link: 'javascript:alert(1)' }] };
  const result = resolve({ kind: 'virtual', currency: 'CNY', services });
  assert.deepEqual(plain(result.map(item => item.title)), ['买家评价', '咨询']);
  assert.equal(result[0].link, '#shop-product-reviews');
  assert.equal(result[1].link, '');
  assert.equal(result[0].description, '');
});

test('service links reject protocols, credentials and encoded path escapes', () => {
  for (const value of ['javascript:alert(1)', 'data:text/html,a', '//example.test', '/\\example.test', '/%5cexample.test', '/%2fexample.test', '/x%0ay', 'http://example.test', 'https://user:pass@example.test', ' https://example.test']) assert.equal(safeLink(value), '', value);
  for (const value of ['/shop/orders/', '/shop/?coupons=1', '#shop-product-rules', 'https://example.test/help']) assert.equal(safeLink(value), value);
});

test('incomplete preview amounts and precision never become a fabricated zero shipping fee', () => {
  for (const patch of [{ shipping_fee: 'bad' }, { shipping_fee: '1000000000001' }, { precision: 10000000 }, { precision: -1 }]) {
    assert.equal(result.exports.context({ kind: 'physical', currency: 'wallet:main', shipping_fee: '12', precision: 1, ...patch })['运费'], '以商品说明为准');
  }
});
