const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const postcss = require('postcss');
const roots = ['themes/chunnel/assets/css', 'internal/coreui/assets/css'];
const read = (root, name) => fs.readFileSync(path.join(__dirname, '../..', root, `chunnel-${name}.source.css`), 'utf8');
function rule(css, selector) {
  let result;
  postcss.parse(css).walkRules(r => {
    if (r.selector === selector && r.parent.type === 'root') {
      result = Object.fromEntries(r.nodes.filter(n => n.type === 'decl').map(n => [n.prop, n.value]));
    }
  });
  assert.ok(result, selector);
  return result;
}
for (const root of roots) {
  test(`${root}: content selections are flat and counters have no nested plate`, () => {
    const css = read(root, 'explore');
    for (const selector of ['.explore-page .explore-chips a[aria-current]', '.explore-page .portal-view-switch :is(a,button):is([aria-current],[aria-pressed="true"])']) {
      const selected = rule(css, selector);
      assert.equal(selected['box-shadow'], 'none');
      assert.equal(selected.background, 'var(--content-selected-bg)');
      assert.equal(selected.color, 'var(--content-selected-text)');
      assert.equal(selected['border-color'], 'transparent');
    }
    assert.equal(rule(css, '.explore-chips a small').background, 'transparent');
    assert.equal(rule(css, '.explore-page .explore-chips a')['min-height'], '36px');
    assert.match(css, /min-height: 44px/);
    assert.match(css, /:focus-visible\s*\{\s*outline: 2px/);
    assert.match(css, /@media \(forced-colors: active\)/);
    assert.match(css, /background: Highlight; color: HighlightText/);
  });
}
test('content selection tokens agree in theme and embedded UI', () => {
  const selector = ':is(.portal-site, .home-portal)';
  const tokens = roots.map(root => rule(read(root, 'foundation'), selector));
  assert.deepEqual(tokens[0], tokens[1]);
  assert.equal(tokens[0]['--content-choice-radius'], '6px');
  assert.ok(tokens[0]['--content-selected-text']);
});
test('home and portal switches use the same flat selected surface', () => {
  const home = rule(read(roots[0], 'home-portal'), '.home-view-switch a[aria-current]');
  for (const root of roots) {
    const portal = rule(read(root, 'portal'), '.portal-view-switch :is(a,button):is([aria-current],[aria-pressed="true"])');
    assert.deepEqual(portal, home);
    assert.equal(portal['box-shadow'], 'none');
  }
});
