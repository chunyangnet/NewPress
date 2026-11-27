const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { resolve } = require('node:path');
const test = require('node:test');

const root = resolve(__dirname, '../..');
const read = path => readFileSync(resolve(root, path), 'utf8');
const source = read('themes/chunnel/assets/css/chunnel-community.source.css');

test('community skin reads configurable brand, spacing and accessibility tokens', () => {
  for (const token of ['radius', 'primary', 'paper', 'ink', 'font-scale', 'body-leading']) {
    assert.match(source, new RegExp('var\\(--' + token + '[,)\\s]'), token);
    assert.doesNotMatch(source, new RegExp('--' + token + '\\s*:'), token);
  }
  assert.match(source, /body\.theme-shadow-none/);
  assert.match(read('themes/chunnel/assets/css/chunnel-foundation.source.css'), /var\(--focus,/);
  assert.match(source, /body\.theme-footer-light/);
  assert.match(source, /prefers-reduced-motion: no-preference/);
  assert.match(source, /forced-colors: active/);
  assert.match(source, /min-height: 44px/);
  assert.match(source, /font-size: max\(16px, var\(--type-ui/);
});

test('public skin is built once and excludes the legacy administration skin', () => {
  const build = read('scripts/build_frontend.mjs');
  const base = read('themes/chunnel/templates/base.html');
  assert.match(build, /const community = await read\('assets\/css\/chunnel-community.source.css'\)/);
  assert.match(base, /chunnel-community\.css/);
  assert.doesNotMatch(base, /chunnel-claude(?:-admin)?\.css/);
  assert.ok(base.indexOf('chunnel-community.css') < base.indexOf('{{.ThemeCSSURL}}'));
  for (const file of ['chunnel-home.css', 'chunnel-home-glass.css']) {
    const compiled = read('themes/chunnel/assets/css/' + file);
    assert.match(compiled, /--community-font:/);
    assert.doesNotMatch(compiled, /--claude-serif:/);
    assert.match(compiled, /home-masthead-copy/);
    assert.match(compiled, /body\.is-home :is\(\.home-masthead h1,\.home-hero-copy h2\)/);
  }
});

test('community components share a skin without flattening membership and check-in cards', () => {
  for (const component of ['home-panel', 'portal-panel', 'circle-directory-card', 'feedback-card', 'links-card', 'access-card', 'account-library', 'ui-modal', 'interaction-drawer', 'article-header', 'toast']) {
    assert.ok(source.includes('.' + component), component);
  }
  assert.match(source, /\.home-panel:not\(\.home-member-card\):not\(\.home-checkin-card\)/);
  assert.match(source, /html\[data-style="classic"\]/);
});
