const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { resolve } = require('node:path');
const test = require('node:test');
const { transformSync } = require('esbuild');

const theme = resolve(__dirname, '../../internal/coreui');
const css = readFileSync(resolve(theme, 'assets/css/chunnel-claude-admin.css'), 'utf8');
const template = readFileSync(resolve(theme, 'templates/admin-base.html'), 'utf8');

test('administration skin compiles and stays confined to administrative pages', () => {
  assert.deepEqual(transformSync(css, { loader: 'css' }).warnings, []);
  for (const match of css.matchAll(/([^{}]+)\{/g)) {
    const selector = match[1].trim();
    if (selector.startsWith('@')) continue;
    assert.match(selector, /^html(?:\[data-style\])? body\.(?:admin-workbench|is-admin-login)(?:$|\s)/, selector);
  }
  assert.match(template, /chunnel-admin-skin\.css/);
  assert.match(template, /chunnel-admin-login-skin\.css/);
  assert.doesNotMatch(readFileSync(resolve(__dirname, '../../themes/chunnel/templates/base.html'), 'utf8'), /chunnel-(?:claude-admin|admin-(?:login-)?skin)\.css/);
});

test('administration surfaces preserve configurable public and brand tokens', () => {
  assert.doesNotMatch(css, /--(?:paper|white|ink|cyan|cyan-dark|primary|primary-dark|focus)\s*:/);
  assert.match(css, /--admin-canvas: color-mix\(in srgb, var\(--paper\) 75%, #eff1f4\)/);
  assert.match(css, /--workspace-accent: color-mix\(in srgb, var\(--admin-blue\) 7%, #ffffff\)/);
});

test('administration skin keeps mobile and accessibility adaptations', () => {
  assert.match(css, /@media \(max-width: 1050px\)/);
  assert.match(css, /@media \(max-width: 600px\)/);
  assert.match(css, /@media \(pointer: coarse\)/);
  assert.match(css, /@media \(prefers-reduced-motion: reduce\)/);
  assert.match(css, /@media \(forced-colors: active\)/);
  assert.doesNotMatch(css, /outline:\s*(?:none|0)[;!]/);
});
