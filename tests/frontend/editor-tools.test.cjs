const test = require('node:test');
const assert = require('node:assert/strict');
const { loadModule } = require('./helpers/typescript.cjs');
const { environment, dialogFixture, Element } = require('./helpers/editor-tools.cjs');
const data = loadModule('frontend/core/editor-tool-data.ts', { URL });
const item = { id: 42, version: 3, type: 'post', status: 'draft', title: 'Existing article', excerpt: 'summary', permalink: '/posts/existing-article', content_html: '<p>Reusable body</p>' };
const list = (items = [item]) => ({ items, pagination: { page: 1, pages: 1 } });

test('editor response boundaries reject malformed list, source and link report data', () => {
  for (const bad of [null, {}, { items: [], pagination: { page: -1, pages: 1 } }, { items: [], pagination: { page: 1, pages: '2' } }]) assert.throws(() => data.contentList(bad));
  assert.equal(data.contentList(list([null, { ...item, version: '3' }, item])).items.length, 1);
  for (const bad of [null, { post_id: 1 }, { post_id: 1, version: 1, truncated: false, counts: { ok: '1' }, items: [] }]) assert.throws(() => data.linkReport(bad));
  for (const bad of [null, { total: 0, page: 0, pages: 0, items: [] }, { total: 1, page: 1, pages: 1, items: [{ ...item, id: '42' }] }]) assert.throws(() => data.seoSources(bad));
});

for (const variant of ['TypeScript', 'generated']) {
  for (const name of ['editor-reuse', 'editor-widgets']) {
    test(`${variant} ${name}: opening, selection and insertion preserve the captured range`, async () => {
      const f = dialogFixture(name, variant); f.open(); await f.reply(0, list());
      if (name === 'editor-widgets') { f.select(); f.layouts[0].checked = false; f.layouts[1].checked = true; f.layouts[1].emit('change'); }
      f.insert(); if (name === 'editor-reuse') { assert.equal(f.requests[1].url, '/api/v1/admin/contents/42'); await f.reply(1, item); }
      assert.equal(f.insertions.length, 1); assert.equal(f.insertions[0].selection.value, 'draft'); assert.equal(f.insertions[0].selection.start, 5);
      assert.equal(f.dialog.open, false); if (name === 'editor-widgets') assert.equal(f.renderings[0].layout, 'compact');
    });

    test(`${variant} ${name}: close, reopen and pagehide invalidate old responses and release controls`, async () => {
      const f = dialogFixture(name, variant); f.open(); f.close(); f.open();
      assert.equal(f.requests[0].init.signal.aborted, true); await f.reply(0, list()); assert.equal(f.nodes.results.children.length, 0);
      await f.reply(1, list()); assert.equal(f.nodes.results.children.length, 1);
      f.nodes.search.emit('submit'); f.hide(); assert.equal(f.requests[2].init.signal.aborted, true); assert.equal(f.nodes.submit.disabled, false);
      assert.match(f.nodes.status.textContent, /中断/); await f.reply(2, list()); assert.match(f.nodes.status.textContent, /中断/);
    });

    test(`${variant} ${name}: failed responses unlock controls and cannot overwrite changed text`, async () => {
      const f = dialogFixture(name, variant); f.open(); await f.reply(0, { error: { message: 'retry' } }, 503);
      assert.equal(f.nodes.submit.disabled, false); f.nodes.search.emit('submit'); await f.reply(1, list());
      if (name === 'editor-widgets') f.select();
      f.source.value = 'changed'; f.insert(); if (name === 'editor-reuse') await f.reply(2, item);
      assert.equal(f.insertions.length, 0); assert.equal(f.dialog.open, true); assert.match(f.nodes.status.textContent, /变化/);
    });

    test(`${variant} ${name}: timeout restores controls and reports retained content`, async () => {
      const f = dialogFixture(name, variant); f.open(); f.flush(); f.requests[0].reject(new DOMException('aborted', 'AbortError')); await f.settle();
      assert.match(f.nodes.status.textContent, /超时/); assert.equal(f.nodes.submit.disabled, false); assert.equal(f.source.value, 'draft');
    });
  }

  test(`${variant}: reuse rejects version changes, empty body and newly locked editor`, async () => {
    for (const payload of [{ ...item, version: 4 }, { ...item, status: 'trash' }, { ...item, content_html: ' ' }, null]) {
      const f = dialogFixture('editor-reuse', variant); f.open(); await f.reply(0, list()); f.insert(); await f.reply(1, payload);
      assert.equal(f.insertions.length, 0); assert.equal(f.dialog.open, true); assert.equal(f.nodes.submit.disabled, false);
    }
    const f = dialogFixture('editor-reuse', variant); f.open(); await f.reply(0, list()); f.insert(); f.source.readOnly = true; await f.reply(1, item);
    assert.equal(f.insertions.length, 0); assert.match(f.nodes.status.textContent, /权限/);
  });

  test(`${variant}: resource loader is lazy, deduplicates assets and retries failed script or CSS`, () => {
    const f = environment(), panel = new Element('details'), status = new Element();
    panel.dataset = { resourceCss: '/style.css', resourceScript: '/script.js' }; panel.selectors.set('[data-resource-editor-status]', status);
    f.document.selectors.set('[data-resource-panel]', panel); f.load('resource-loader', variant); assert.equal(f.document.head.children.length, 0);
    panel.open = true; panel.emit('toggle'); const [css, script] = f.document.head.children;
    panel.emit('toggle'); assert.equal(f.document.head.children.length, 2);
    script.emit('error'); assert.equal(script.isConnected, false); assert.match(status.textContent, /加载失败/);
    panel.emit('toggle'); assert.equal(f.document.head.children.length, 3); const replacement = f.document.head.children[2]; replacement.emit('load');
    css.emit('error'); panel.emit('toggle'); assert.equal(f.document.head.children.length, 4); assert.equal(f.document.head.children[3].tagName, 'LINK');
    panel.emit('toggle'); assert.equal(f.document.head.children.length, 4);
  });

  test(`${variant}: SEO link check validates identity, blocks duplicates and recovers from pagehide`, async () => {
    const f = environment(), panel = new Element(), nodes = {};
    panel.dataset.postId = '42'; for (const key of ['run', 'status', 'summary', 'results']) { nodes[key] = new Element(key === 'run' ? 'button' : 'div'); panel.selectors.set(`[data-seo-check-${key}]`, nodes[key]); }
    f.document.groups.set('[data-seo-link-check]', [panel]); f.load('seo', variant);
    nodes.run.click(); nodes.run.emit('click'); assert.equal(f.requests.length, 1);
    await f.reply(0, { post_id: 43, version: 3, truncated: false, items: [], counts: {} }); assert.match(nodes.status.textContent, /编号不一致/);
    nodes.run.click(); f.window.emit('pagehide'); assert.equal(nodes.run.disabled, false); await f.reply(1, { post_id: 42, version: 3, truncated: false, items: [], counts: {} }); assert.match(nodes.status.textContent, /中断/);
    nodes.run.click(); await f.reply(2, { post_id: 42, version: 3, truncated: false, counts: { ok: 1 }, items: [{ url: '/posts/a', message: 'ok', status: 'ok' }] });
    assert.equal(nodes.results.children.length, 1); assert.match(nodes.status.textContent, /正式版本 v3/); assert.equal(nodes.run.disabled, false);
  });
}
