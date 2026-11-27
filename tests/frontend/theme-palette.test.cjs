const assert = require('node:assert/strict');
const { readFileSync, readdirSync } = require('node:fs');
const { resolve } = require('node:path');
const test = require('node:test');

const styles = resolve(__dirname, '../../themes/chunnel/assets/css');
const read = name => readFileSync(resolve(/^(chunnel-admin|chunnel-claude)/.test(name) ? resolve(__dirname, '../../internal/coreui/assets/css') : styles, name), 'utf8');

test('shared foundation owns the blue-white palette', () => {
  const foundation = read('chunnel-foundation.source.css');
  const schema = JSON.parse(readFileSync(resolve(styles, '../../settings.schema.json'), 'utf8'));
  for (const [setting, token] of Object.entries({ accent: 'cyan', accent_strong: 'cyan-dark', paper_color: 'paper', ink_color: 'ink', highlight_color: 'orange' })) {
    assert.ok(foundation.includes('--' + token + ': ' + schema.properties[setting].default + ';'), setting);
  }
  assert.match(foundation, /--white: #ffffff;/);
  assert.match(foundation, /--primary-soft: color-mix\(in srgb, var\(--primary\)/);
  for (const name of ['chunnel.css', 'chunnel-ui.css', 'chunnel-glass.css']) {
    assert.doesNotMatch(read(name), /(?:^|[;{])\s*--(?:paper|ink|white|cyan|cyan-dark):/m, name);
  }
});

test('inputs and comments derive accent details from the saved brand color', () => {
  const inputs = read('chunnel-inputs.css');
  assert.match(inputs, /--fluent-accent: var\(--primary/);
  assert.match(inputs, /\.explore-search\.fluent-input-group:focus-within/);
  assert.match(inputs, /var\(--primary-ring\)/);
  const interactions = read('chunnel-interactions.css');
  assert.match(interactions, /\.theme-comment-refined \.comment-form-panel \{[^}]*var\(--primary-line\)/);
  assert.match(interactions, /\.comment-login-symbol \{[^}]*var\(--primary\)/);
  const foundation = read('chunnel-foundation.source.css');
  for (const token of ['--primary-readable', '--primary-tint', '--primary-line', '--primary-ring']) {
    assert.ok(foundation.includes(token + ':'), token);
  }
});

test('component aliases never reference themselves, including fallbacks', () => {
  for (const name of readdirSync(styles).filter(name => name.endsWith('.css'))) {
    assert.doesNotMatch(read(name), /--([\w-]+)\s*:[^;{}]*var\(--\1\s*[,)]/, name);
  }
});

test('glass and administration use shared surfaces and brand highlights', () => {
  const glass = read('chunnel-glass.css');
  assert.match(glass, /background: linear-gradient\(170deg, var\(--primary\), var\(--primary-dark\)\)/);
  assert.match(glass, /radial-gradient\(ellipse at 2% 8%, var\(--primary-soft\)/);
  const workspace = read('chunnel-admin-workspace.source.css');
  for (const token of ['--admin-canvas: var(--paper)', '--admin-text: var(--ink-soft)', '--workspace-accent: var(--primary-soft)']) {
    assert.ok(workspace.includes(token), token);
  }
  assert.match(read('chunnel-claude-admin.css'), /background: var\(--workspace-surface\)/);
  assert.match(read('chunnel-community.source.css'), /\.home-panel:not\(\.home-member-card\):not\(\.home-checkin-card\)/);
  assert.match(read('chunnel-foundation.source.css'), /--control-danger: #c64545;/);
});

test('light mode resolves legacy surface fallbacks without a fixed green tint', () => {
  const modes = read('chunnel-color-mode.source.css');
  const light = modes.match(/:root\s*\{([^}]+)\}/)[1];
  for (const [role, token] of Object.entries({ surface: 'white', soft: 'paper-deep', line: 'line' })) {
    assert.ok(light.includes(`--tone-${role}: var(--${token});`), role);
  }
  const links = read('chunnel-links.css');
  assert.match(links, /--links-accent:var\(--primary\);--links-tint:var\(--primary-soft\)/);
  assert.doesNotMatch(links, /#287565|#eaf2ee/);
  const home = read('chunnel-home-portal.source.css');
  assert.match(home, /\.home-channels > a \{ --channel-accent: var\(--primary-readable\); --channel-tint: var\(--primary-tint\);/);
  assert.doesNotMatch(home, /--channel-(?:accent|tint):\s*#[0-9a-f]/i);
});

test('conversation, notification and emoji states follow the saved accent', () => {
  const interactions = read('chunnel-interactions.css');
  assert.match(interactions, /\.conversation-item\.is-active \{[^}]*var\(--selection-text\)[^}]*var\(--selection-line\)[^}]*var\(--selection-bg\)/);
  assert.match(interactions, /\.notification-item\.is-unread \{[^}]*var\(--primary-line\)[^}]*var\(--primary-tint\)/);
  assert.match(interactions, /\.message-row\.is-own \.message-bubble \{[^}]*var\(--primary-line\)[^}]*var\(--primary-tint\)/);
  assert.match(interactions, /\.emoji-trigger\[aria-expanded="true"\] \{[^}]*var\(--primary-line\)[^}]*var\(--primary-soft\)[^}]*var\(--primary-readable\)/);
});

test('neutral surface aliases preserve explicit feedback colors in both UI copies', () => {
  const names = ['chunnel-color-mode.source.css', 'chunnel-interactions.css', 'chunnel-links.css', 'chunnel-pages.css', 'chunnel-ui.css', 'chunnel-currency.css', 'chunnel-progression.css'];
  for (const name of names) {
    const core = readFileSync(resolve(__dirname, '../../internal/coreui/assets/css', name), 'utf8');
    assert.equal(read(name), core, name);
  }
  const ui = read('chunnel-ui.css');
  assert.match(ui, /\.comment-notice-success \{[^}]*var\(--control-success-line\)[^}]*var\(--control-success\)[^}]*var\(--control-success-soft\)/);
  assert.match(ui, /\.comment-notice-warning \{[^}]*var\(--control-warning-line\)[^}]*var\(--control-warning-soft\)/);
  assert.match(ui, /\.comment-notice-error \{[^}]*var\(--control-danger\)[^}]*var\(--control-danger-soft\)/);
  assert.match(ui, /\.toast \{\s*--toast-accent: var\(--primary-soft\);\s*--toast-accent-ink: var\(--primary-readable\);/);
});
