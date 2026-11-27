const test = require('node:test'), assert = require('node:assert/strict'), vm = require('node:vm'), esbuild = require('esbuild'), fs = require('node:fs');
const compiled = esbuild.buildSync({ entryPoints: ['frontend/shared/shop-service-data.ts'], bundle: true, write: false, format: 'cjs', target: 'es2022' }).outputFiles[0].text;
const result = { exports: {} }; vm.runInNewContext(compiled, { module: result, exports: result.exports, URL }); const D = result.exports;
const library = () => ({ ...JSON.parse(fs.readFileSync('internal/shop/service_templates.json', 'utf8')), limit: 60 });
const item = () => ({ template: 'currency', icon: 'wallet', title: ' {结算货币}结算 ', description: ' 下单前核对金额 ', link: '' });
test('actual service library retains all templates, categories, icons and the server selection limit', () => {
  const value = D.serviceLibrary(library()); assert.equal(value.templates.length, 96); assert.equal(value.limit, 60);
  assert.ok(value.templates.some(v => v.template === 'currency')); assert.ok(value.icons.some(v => v.key === 'wallet'));
});
test('nil service configurations remain automatic while custom empty and hidden selections preserve intent', () => {
  assert.equal(D.serviceConfig(null).mode, 'auto'); assert.equal(D.serviceConfig({ mode: 'custom', items: null }).items.length, 0);
  assert.equal(D.collectServices(D.serviceConfig({ mode: 'custom' }), D.serviceLibrary(library())).mode, 'custom');
  assert.equal(D.collectServices(D.serviceConfig({ mode: 'auto' }), D.serviceLibrary(library())), null);
  assert.equal(D.collectServices({ mode: 'hidden', items: [item()] }, D.serviceLibrary(library())).items.length, 1);
});
test('service collection trims copies without changing drafts and rejects unsafe links, duplicate templates and unknown icons', () => {
  const original = { mode: 'custom', items: [item()] }, source = D.serviceLibrary(library());
  assert.equal(D.collectServices(original, source).items[0].title, '{结算货币}结算'); assert.equal(original.items[0].title, ' {结算货币}结算 ');
  for (const patch of [{ title: '' }, { title: '字'.repeat(25) }, { description: '<b>说明</b>' }, { link: 'https://user:pass@example.test/' }, { icon: 'unknown-icon' }, { template: 'missing-template' }]) assert.throws(() => D.collectServices({ mode: 'custom', items: [{ ...item(), ...patch }] }, source));
  assert.throws(() => D.collectServices({ mode: 'custom', items: [item(), item()] }, source));
  assert.throws(() => D.collectServices({ mode: 'custom', items: Array(61).fill(item()) }, source));
});
test('service library rejects duplicate identifiers, unknown template icons and malformed structures', () => {
  for (const change of [v => v.templates.push(v.templates[0]), v => v.icons.push(v.icons[0]), v => v.limit = 61, v => v.templates[0].icon = 'missing', v => v.templates[0].link = '//example.test', v => v.icons[0].key = '<svg>', v => delete v.templates]) { const value = library(); change(value); assert.throws(() => D.serviceLibrary(value)); }
  for (const value of [{ mode: 'other' }, { mode: 'custom', items: {} }, { mode: 'custom', items: [{ icon: 'wallet', title: 1, description: '' }] }]) assert.throws(() => D.serviceConfig(value));
});
