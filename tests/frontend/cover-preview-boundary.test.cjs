const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { compile } = require('./helpers/typescript.cjs');

const root = process.env.NEWPRESS_COVER_TEST_ROOT || path.resolve(__dirname, '../..');
const source = name => name === 'badge-ts' ? compile('frontend/core/article-badge-editor.ts') : fs.readFileSync(path.join(root, 'internal/coreui/assets/js', name), 'utf8');

function image(requests) {
  const attrs = new Map();
  return {
    tagName: 'IMG', hidden: true,
    set src(value) { attrs.set('src', value); requests.push(value); },
    getAttribute(name) { return attrs.get(name) ?? null; },
    removeAttribute(name) { attrs.delete(name); },
    addEventListener() {},
  };
}

function badgeFixture(asset) {
  const requests = [], cover = {value: ''}, listeners = {};
  const picture = image(requests);
  const fields = Object.fromEntries(['kind', 'text', 'tone', 'icon', 'style', 'placement', 'hide_mobile'].map(name => [name, {
    value: '', dataset: {articleBadgeField: name}, options: [], selectedOptions: [], addEventListener() {},
  }]));
  const form = {
    querySelector: selector => selector === '[name="cover_url"]' ? cover : selector.startsWith('[name="') ? {value: ''} : null,
    elements: {namedItem: name => name === 'cover_url' ? cover : {value: ''}},
    addEventListener: (name, callback) => { listeners[name] = callback; },
  };
  const nodes = new Map([
    ['[data-article-badge-preview]', {dataset: {}, replaceChildren() {}}],
    ['[data-article-badge-note]', {}], ['[data-article-badge-count]', {}],
    ['[data-article-badge-title]', {}], ['[data-article-badge-image]', picture],
  ]);
  const section = {
    closest: () => form,
    querySelector: selector => { const field = /^\[data-article-badge-field="([^"\]]+)"\]$/.exec(selector); if (field) return fields[field[1]]; assert.ok(nodes.has(selector), selector); return nodes.get(selector); },
    querySelectorAll: selector => selector === '[data-article-badge-field]' ? Object.values(fields) : [],
  };
  vm.runInNewContext(source(asset), {document: {querySelector: () => section}, queueMicrotask});
  return {
    requests,
    change(value) { cover.value = value; listeners.input(); },
    current: () => picture.hidden ? null : picture.getAttribute('src'),
  };
}

function editorFixture() {
  const code = source('chunnel-admin-editor.source.js');
  const start = code.indexOf('    const updateCoverPreview =');
  const end = code.indexOf('    editorForm.querySelectorAll', start);
  assert.ok(start > 0 && end > start, 'editor preview handler must be present');
  const requests = [], coverInput = {value: ''};
  const coverPreview = {children: [], replaceChildren() { this.children = []; }, append(...nodes) { this.children.push(...nodes); }};
  const sandbox = {
    coverInput, coverPreview, coverChoices: [], icon: () => ({}),
    document: {createElement: tag => tag === 'img' ? image(requests) : {append() {}}},
  };
  vm.runInNewContext(code.slice(start, end) + '\nglobalThis.update = updateCoverPreview;', sandbox);
  return {
    requests,
    change(value) { coverInput.value = value; sandbox.update(); },
    current: () => coverPreview.children.find(node => node.tagName === 'IMG')?.getAttribute('src') ?? null,
  };
}

for (const [name, fixture] of [
  ['badge source', () => badgeFixture('badge-ts')],
  ['badge generated', () => badgeFixture('chunnel-article-badge-editor.js')],
  ['main editor', editorFixture],
]) {
  for (const value of [
    'https://cdn.example.test/cover.jpg', 'http://cdn.example.test/cover.jpg', 'https://example.test/media/cover.jpg',
    '//cdn.example.test/cover.jpg', '/\\cdn.example.test/cover.jpg', '/\n/cdn.example.test/cover.jpg',
    '/\t/cdn.example.test/cover.jpg', '/\r/cdn.example.test/cover.jpg', '/media/\u0000bad.jpg',
    '/media/\u0085bad.jpg', 'data:image/svg+xml,<svg/>', 'javascript:alert(1)', 'images/cover.jpg', '',
  ]) {
    test(`${name} makes no image request for ${JSON.stringify(value)}`, () => {
      const view = fixture();
      view.change(value);
      assert.deepEqual(view.requests, []);
      assert.equal(view.current(), null);
    });
  }
  for (const value of ['/media/cover.jpg', '/theme-assets/images/cover.webp', '/media/中文.webp', '/media/a%20b.png?size=2#hero', '  /media/spaced.jpg  ']) {
    test(`${name} previews site path ${JSON.stringify(value)}`, () => {
      const view = fixture();
      view.change(value);
      assert.deepEqual(view.requests, [value.trim()]);
      assert.equal(view.current(), value.trim());
    });
  }
  test(`${name} removes a previous image when the edited address becomes invalid`, () => {
    const view = fixture();
    view.change('/media/previous.jpg');
    view.change('https://cdn.example.test/rejected.jpg');
    assert.deepEqual(view.requests, ['/media/previous.jpg']);
    assert.equal(view.current(), null);
    view.change('/media/next.jpg');
    assert.equal(view.current(), '/media/next.jpg');
  });
}
