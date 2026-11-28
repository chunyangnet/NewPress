const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const postcss = require('postcss');
const read = name => fs.readFileSync(path.join(__dirname, '../..', name), 'utf8');
const theme = 'themes/chunnel/assets/css/';
const core = 'internal/coreui/assets/css/';
function declarations(source, selector, mobile = false) {
  const values = {};
  const normalize = value => value.replaceAll('"', '').replaceAll("'", '').replace(/\s*([>+~,])\s*/g, '$1');
  postcss.parse(source).walkRules(rule => {
    const media = rule.parent.type === 'atrule' && rule.parent.name === 'media';
    if (normalize(rule.selector) !== normalize(selector) || !!media !== mobile) return;
    rule.walkDecls(d => { values[d.prop] = d.value; });
  });
  return values;
}
test('shared alignment sources stay identical across theme and embedded UI', () => {
  assert.equal(read(theme + 'chunnel-selects.css'), read(core + 'chunnel-selects.css'));
  for (const [selector, mobile] of [['.portal-site .discovery-sort-form', false], [':root .portal-site .discovery-sort-form', true], ['.portal-site .ui-button-small', true]]) {
    assert.deepEqual(declarations(read(theme + 'chunnel-portal.source.css'), selector, mobile), declarations(read(core + 'chunnel-portal.source.css'), selector, mobile));
  }
});
test('enhanced select centers the label while preserving its clipping box', () => {
  const css = read(theme + 'chunnel-selects.css');
  const prefix = ':root[data-choice-controls] :is(body, body[class]) select[data-native-select]';
  const control = declarations(css, prefix);
  assert.equal(control.display, 'inline-flex');
  assert.equal(control['align-items'], 'center');
  const label = declarations(css, prefix + ' > button > selectedcontent');
  assert.equal(label['min-width'], '0');
  assert.equal(label['text-overflow'], 'ellipsis');
  assert.equal(declarations(css, prefix + '::picker-icon')['inset-block-start'], '50%');
});
test('sorting controls and compact buttons share desktop and touch heights', () => {
  const css = read(theme + 'chunnel-portal.source.css');
  assert.equal(declarations(css, '.portal-site .discovery-sort-form')['--fluent-control-height'], '36px');
  assert.equal(declarations(css, ':root .portal-site .discovery-sort-form', true)['--fluent-control-height'], '44px');
  assert.equal(declarations(css, '.portal-site .ui-button-small', true)['min-height'], '44px');
});
test('narrow admin header gives search its own row without squeezing the title', () => {
  const css = postcss.parse(read(core + 'chunnel-admin-ui.source.css'));
  const media = css.nodes.find(node => node.type === 'atrule' && node.params === '(max-width:440px)');
  assert.ok(media);
  const values = selector => {
    const rule = media.nodes.find(node => node.type === 'rule' && node.selector === 'html[data-style] body.admin-workbench ' + selector);
    assert.ok(rule);
    return Object.fromEntries(rule.nodes.filter(node => node.type === 'decl').map(node => [node.prop, node.value]));
  };
  assert.equal(values('.admin-topbar')['flex-wrap'], 'wrap');
  assert.equal(values('.admin-breadcrumb').flex, '1 1 0');
  assert.equal(values('.admin-breadcrumb')['max-width'], 'none');
  assert.equal(values('.admin-command-wrap').flex, '0 0 100%');
  assert.equal(values('.admin-command-wrap').order, '1');
  assert.equal(values('.admin-command-wrap')['max-width'], '100%');
});

test('admin search inner control fits 44px frame and grows for touch', () => {
  const css = read(core + 'chunnel-admin-components.source.css');
  const selector = ':root[data-input-style="fluent"] body.admin-workbench .admin-main form.fluent-input-group[role="search"] > input:not([type="hidden"])';
  assert.equal(declarations(css, selector)['min-height'], '36px');
  assert.equal(declarations(css, selector, true)['min-height'], '44px');
});
