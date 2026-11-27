const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { resolve } = require('node:path');
const test = require('node:test');

const read = path => readFileSync(resolve(__dirname, /^assets\/(css|js)\/chunnel-(admin|wechat)/.test(path) ? '../../internal/coreui' : '../../themes/chunnel', path), 'utf8');

test('root scrollers no longer animate browser or script restoration', () => {
  assert.doesNotMatch(read('assets/css/chunnel.css'), /html\s*\{[^}]*scroll-behavior:\s*smooth/);
  assert.doesNotMatch(read('assets/css/chunnel-admin-base.css'), /html\s*\{[^}]*scroll-behavior:\s*smooth/);
});

test('explore filters stay on their route and the view switch stays in place', () => {
  const template = read('templates/explore.html');
  assert.doesNotMatch(template, /#content-index/);
  assert.match(template, /<button type="button" data-view-toggle="list"/);
  assert.match(template, /<button type="button" data-view-toggle="grid"/);
  assert.match(template, /data-view-toggle="list" aria-pressed="\{\{if eq \.Discovery\.View "list"\}\}true/);
  assert.match(read('assets/css/chunnel-portal.source.css'), /\.portal-view-switch :is\(a,button\)/);
  assert.match(read('assets/js/chunnel.js'), /if \(results\) keepScroll/);
});

test('public and admin use native document restoration and share a synchronous local helper', () => {
  for (const file of ['frontend/shared/shell.ts', 'frontend/core/admin.ts']) {
    const source = file.endsWith('.ts') ? readFileSync(file, 'utf8') : read(file);
    assert.doesNotMatch(source, /scrollRestoration = 'manual'|chunnel\.navigation\.scroll|ResizeObserver\(restore\)/, file);
  }
  assert.match(readFileSync('frontend/shared/scroll.ts', 'utf8'), /scrollRestoration = 'auto'/);
  assert.doesNotMatch(readFileSync('frontend/shared/scroll.ts', 'utf8'), /setTimeout|requestAnimationFrame|ResizeObserver/);
  assert.match(read('templates/base.html'), /chunnel-scroll\.js/);
});

test('in-place panel switches preserve the reader position through one helper', () => {
  const publicScript = read('assets/js/chunnel.js');
  assert.match(publicScript, /window\.ChunnelScroll\.keep\(change\)/);
  assert.match(publicScript, /if \(preserve\) keepScroll\(change\)/);
  // Account tab/scroll behavior is exercised through both entries in account-loading.test.cjs.
  assert.match(read('assets/js/chunnel-wechat.source.js'), /selectTab\(name, false, true\)/);

  const adminUI = readFileSync(resolve(__dirname, '../../frontend/core/admin-ui.ts'), 'utf8');
  assert.match(adminUI, /preserve && window\.ChunnelScroll/);
  assert.match(adminUI, /window\.ChunnelScroll\.keep\(change, \{ top \}\)/);
  assert.match(adminUI, /activate\(link\.dataset\.settingsTarget \|\| '', true, true\)/);
});

test('discovery filters swap only the results panel and keep the scroll anchor', () => {
  const template = read('templates/explore.html');
  assert.match(template, /data-discovery-results/);
  assert.match(template, /data-discovery-search/);
  assert.match(template, /data-discovery-sort/);
  assert.match(template, /data-discovery-status/);
  // Results, history, focus and scroll preservation run against the compiled
  // discovery module in list-navigation.test.cjs.
});

test('home feed retains a declared panel for its behavioral navigation tests', () => {
  assert.match(read('templates/partials/home-modules.html'), /<div id="latest" class="home-panel home-feed-panel">/);
});
