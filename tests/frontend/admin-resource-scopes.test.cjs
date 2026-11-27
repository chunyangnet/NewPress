const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const postcss = require('postcss');
const selectorParser = require('postcss-selector-parser');

const root = path.join(__dirname, '../../internal/coreui');
const css = name => fs.readFileSync(path.join(root, 'assets/css', name), 'utf8');
const imports = source => [...source.matchAll(/@import\s+"([^"]+)"/g)].map(match => match[1]);

test('the split styles retain the complete authored cascade when all scopes are present', () => {
  const legacy = imports(css('chunnel-admin.source.css'));
  const split = ['shared', 'directory', 'sessions', 'workflows'].flatMap(name => imports(css(`chunnel-admin-${name}.source.css`)));
  assert.deepEqual(split, legacy, 'shared or scoped styles cannot move across the legacy cascade');
});

for (const [name, prefixes] of [
  ['directory', /^(admin-taxonomy|admin-term|admin-users?|admin-role|admin-password-rotation)/],
  ['sessions', /^(admin-sessions?|radar-node)/],
]) {
  test(`${name}: every selector is owned by its declared page scope`, () => {
    for (const file of imports(css(`chunnel-admin-${name}.source.css`))) {
      const parsed = postcss.parse(css(file));
      parsed.walkRules(rule => {
        if (rule.parent.type === 'atrule' && /keyframes$/.test(rule.parent.name)) return;
        selectorParser(selectors => selectors.each(selector => {
          let scoped = false;
          selector.walkClasses(node => { if (prefixes.test(node.value)) scoped = true; });
          assert.equal(scoped, true, `shared selector ${selector} must remain in the shared cascade`);
        })).processSync(rule.selector);
      });
    }
  });
}

test('homepage layout stylesheet contains only module editor selectors', () => {
  postcss.parse(css('chunnel-home-editor.source.css')).walkRules(rule => {
    selectorParser(selectors => selectors.each(selector => {
      let scoped = false;
      selector.walkClasses(node => { if (/^home-(editor|module|order|custom)-/.test(node.value)) scoped = true; });
      selector.walkAttributes(node => { if (node.attribute === 'data-home-editor') scoped = true; });
      assert.equal(scoped, true, `homepage-only CSS contains shared selector: ${selector}`);
    })).processSync(rule.selector);
  });
  const common = css('chunnel-admin-workspace.css'), layout = css('chunnel-admin-layout-workspace.css');
  assert.doesNotMatch(common, /\.home-editor-row/);
  assert.match(layout, /\.home-editor-row/);
});
