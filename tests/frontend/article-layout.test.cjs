const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { resolve } = require('node:path');
const test = require('node:test');

const read = path => readFileSync(resolve(__dirname, '../../themes/chunnel', path), 'utf8');
const portal = read('assets/css/chunnel-portal.source.css');
const ui = read('assets/css/chunnel-ui.css');
const declarations = (source, selector) => {
  const start = source.indexOf(selector + ' {');
  assert.notEqual(start, -1, selector);
  return source.slice(start + selector.length, source.indexOf('}', start) + 1);
};

test('article grid gives the content column its width without a second inset', () => {
  assert.match(declarations(portal, '.portal-site .article-grid'), /grid-template-columns: var\(--article-rail-width\) minmax\(0,1fr\)/);
  assert.match(declarations(portal, '.portal-site .article-grid'), /gap: var\(--article-column-gap\)/);
  for (const selector of ['.portal-site .article-extras', '.portal-site .article-footer']) {
    assert.match(declarations(portal, selector), /margin: 20px 0 0;/);
  }
  assert.doesNotMatch(portal, /(?:padding-left|margin-left): (?:238|196)px/);
});

test('extras reset legacy centered width instead of adding a second content inset', () => {
  const extras = declarations(portal, '.portal-site .article-extras');
  assert.match(extras, /width: auto;/);
  assert.match(extras, /min-width: 0;/);
  assert.match(extras, /padding: 0;/);
  assert.match(declarations(portal, '.portal-site .article-footer'), /max-width: none;/);
  assert.equal((portal.match(/\.portal-site \.article-extras \{/g) || []).length, 1);
});

test('tablet, single-column and print layouts retain the shared content column', () => {
  const tablet = portal.slice(portal.indexOf('@media (max-width: 980px)'), portal.indexOf('@media (max-width: 760px)'));
  assert.match(tablet, /--article-rail-width: 180px; --article-column-gap: 16px/);
  const mobile = portal.slice(portal.indexOf('@media (max-width: 760px)'), portal.indexOf('@media (max-width: 540px)'));
  assert.doesNotMatch(portal, /--article-content-inset/);
  assert.match(mobile, /\.article-grid \{ grid-template-columns: minmax\(0,1fr\)/);
  assert.match(portal.slice(portal.indexOf('@media print')), /\.article-grid \{ display: block;/);
});

test('article integrations remain inside the shared extras container', () => {
  const template = read('templates/single.html');
  const integrations = template.slice(template.indexOf('<div class="article-extras">'), template.indexOf('<footer id="next"'));
  for (const component of ['series-reading', 'article-action-bar', 'article-author-card', 'public-comments']) assert.ok(integrations.includes(component), component);
});

test('long comment authors shrink and wrap without pushing fixed comment anchors away', () => {
  assert.match(declarations(ui.slice(ui.indexOf('/* Runtime comments */')), '.comment-main > header > div'), /min-width: 0; overflow-wrap: anywhere;/);
  assert.match(declarations(ui, '.comment-main > header > .comment-anchor'), /flex-shrink: 0; white-space: nowrap;/);
  assert.match(ui, /\.comment-depth-3, \.comment-depth-4, \.comment-depth-5/);
});

test('article author and category text wrap within their card boundaries', () => {
  assert.match(declarations(portal, '.portal-site .article-author-card > div'), /min-width: 0; overflow-wrap: anywhere;/);
  assert.match(declarations(ui, '.article-action-bar > div'), /min-width: 0; max-width: 100%;/);
  assert.match(declarations(ui, '.article-action-bar > div:last-child'), /flex-shrink: 0;/);
  assert.match(declarations(ui, '.article-action-bar .ui-badge'), /max-width: 100%; overflow-wrap: anywhere;/);
});

test('built route CSS retains the alignment rules and stays out of homepage CSS', () => {
  const built = read('assets/css/chunnel-portal.css');
  assert.match(built, /\.portal-site \.article-extras\{width:auto;min-width:0;margin:20px 0 0;padding:0\}/);
  assert.match(built, /max-width:none;margin:20px 0 0;/);
  for (const file of ['chunnel-home.css', 'chunnel-home-glass.css']) assert.doesNotMatch(read('assets/css/' + file), /article-content/);
});

function containersAt(template, marker) {
  const clean = template.replace(/\{\{[\s\S]*?\}\}/g, '');
  const position = clean.indexOf(marker);
  assert.notEqual(position, -1, marker);
  const containers = [];
  for (const match of clean.matchAll(/<\/?div\b[^>]*>/g)) {
    if (match.index >= position) break;
    if (match[0].startsWith('</')) containers.pop();
    else containers.push(match[0].match(/class="([^"]*)"/)?.[1] || '');
  }
  return containers;
}

for (const [file, body, footer] of [['single.html', 'id="article-body"', 'id="next"'], ['page.html', 'id="page-body"', 'id="page-end"']]) {
  test(file + ' keeps prose and following sections in a column independent of rail height', () => {
    const template = read('templates/' + file);
    for (const marker of [body, footer, ...(file === 'single.html' ? ['class="article-extras"'] : [])]) {
      const containers = containersAt(template, marker);
      assert.ok(containers.some(classes => classes.split(' ').includes('article-grid')), marker);
      assert.ok(containers.includes('article-content'), marker);
    }
    assert.ok(!containersAt(template, 'class="article-rail"').includes('article-content'));
  });
}

test('content flow stays intrinsic and trims only the trailing prose margin', () => {
  const flow = declarations(portal, '.portal-site .article-content');
  assert.match(flow, /display: flow-root; min-width: 0;/);
  assert.doesNotMatch(flow, /(?:min-)?height|position:|overflow:|transform:|padding:/);
  assert.match(declarations(portal, 'body.portal-site .article-content > .prose > :last-child'), /margin-bottom: 0;/);
  assert.doesNotMatch(portal, /article-grid > \.prose/);
  assert.doesNotMatch(read('assets/css/chunnel-community.source.css'), /article-grid > \.prose/);
});
