// Exercise both editor entries on isolated real Go pages, intercepting all writes.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';
const directory = resolve(process.argv[2]);
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const engines = { chromium, firefox, webkit }, selected = process.argv[3] ? [process.argv[3]] : Object.keys(engines), evidence = [];
for (const name of selected) {
  const browser = await engines[name].launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: 'reduce' });
  const page = await context.newPage(); page.setDefaultTimeout(12000);
  const errors = [], writes = [], intercepted = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => { if (!['GET', 'HEAD'].includes(request.method())) writes.push(request.url()); });
  page.on('dialog', dialog => dialog.accept());
  await context.route('**/*', route => {
    const request = route.request();
    if (new URL(request.url()).pathname === '/api/v1/admin/updates') return route.fulfill({ status: 503, contentType: 'application/json', body: '{"error":{"message":"unavailable in this fixture"}}' });
    if (new URL(request.url()).pathname === '/api/v1/admin/shop/article-widgets') return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ items: [{ id: 7, title: '商城文章引用', permalink: '/posts/shop-widget', excerpt: '', author_name: '作者', category: '分类', cover_url: '' }], pagination: { page: 1, pages: 1, total: 1 } }) });
    if (['GET', 'HEAD'].includes(request.method())) return route.fallback();
    intercepted.push(request.url());
    if (request.url().endsWith('/shop/components/validate')) return route.fulfill({ contentType: 'application/json', body: request.postData() });
    return route.fulfill({ status: 204 });
  });
  const check = text => { console.log(`${name}: ${text}`); evidence.push({ browser: name, check: text }); };
  const visit = async path => { await page.goto(fixture.base + path); await page.waitForLoadState('networkidle'); };
  try {
    await visit(fixture.login); await visit(fixture.paths.editor); await page.locator('.ProseMirror').waitFor();
    assert.equal(await page.locator('script[src*="/chunnel-composer.js"]').count(), 0);
    const initial = await page.locator('[data-content-source]').inputValue();
    assert.equal(await page.evaluate(() => window.NewPressDocumentEditor(document.querySelector('[data-document-editor]'))), true);
    assert.equal(await page.locator('.ProseMirror').count(), 1);
    assert.equal(await page.locator('[data-content-source]').inputValue(), initial);
    check('Article entry starts one continuous editor without the shop renderer; repeat initialization preserves the document.');

    await visit('/admin/shop/');
    assert.equal(await page.locator('script[src*="/chunnel-composer.js"]').count(), 1);
    assert.equal(await page.locator('script[src*="/newpress-document-editor.js"]').count(), 0);
    await page.locator('[data-shop-workspace]').waitFor();
    await page.locator('[data-shop-add]').click();
    await page.locator('[data-shop-editor][open]').waitFor();
    await page.locator('[data-shop-editor-tab="description"]').click();
    await page.locator('[data-block-add="paragraph"]').click();
    await page.locator('.admin-block-paragraph textarea').fill('商品详情迁移验收');
    const source = page.locator('[data-content-source]'), before = await source.inputValue();
    assert.match(before, /<p>商品详情迁移验收<\/p>/);
    await page.locator('[data-component-library-open]').first().click();
    await page.locator('[data-component-library][open]').waitFor();
    await page.locator('[data-component-list] button').filter({ hasText: '提示块' }).click();
    await page.locator('[data-component-insert]').click();
    await page.waitForFunction(() => !document.querySelector('[data-component-library]').open);
    assert.match(await source.inputValue(), /data-np-widget="callout"/);
    await page.locator('[data-composer-undo]').click();
    assert.equal(await source.inputValue(), before);
    check('Shop keeps its block editor; typing and component insertion share the source and remain undoable.');
    assert.equal(await page.locator('script[data-editor-tools-loader]').count(), 0);
    await page.locator('[data-editor-widget-open]').first().click();
    await page.locator('[data-article-choice="7"]').click();
    await page.locator('[data-editor-widget-insert]').click();
    await page.waitForFunction(() => !document.querySelector('[data-editor-widget-dialog]').open);
    assert.match(await source.inputValue(), /data-np-post="7"/);
    await page.locator('[data-composer-undo]').click(); assert.equal(await source.inputValue(), before);
    assert.equal(await page.locator('script[src*="newpress-editor-insert-tools.js"]').count(), 0);
    check('The shop retains its eager article-card entry; inserting and undoing a card needs no article-only loader.');
    for (const width of [1280, 375]) {
      await page.setViewportSize({ width, height: 900 });
      await page.screenshot({ path: resolve(directory, `composer-shop-${name}-${width}.png`) });
    }
    await page.locator('[data-shop-editor] [data-shop-close]').last().click();
    assert.equal(await page.locator('[data-shop-editor]').evaluate(node => node.open), false);
    assert.deepEqual(errors, []);
    assert.equal(writes.length, intercepted.length);
    assert.ok(writes.every(url => url.endsWith('/shop/components/validate')), JSON.stringify(writes));
    check('Cancelling shop editing produces no save; every validation write is intercepted and no browser exception occurs.');
  } finally { await context.close(); await browser.close(); }
}
await writeFile(resolve(directory, 'composer-checks.json'), JSON.stringify(evidence, null, 2) + '\n');
