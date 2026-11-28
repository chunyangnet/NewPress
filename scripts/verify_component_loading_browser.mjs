// Verify lazy component assets and homepage-only CSS on the isolated Go fixture.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';
const directory = resolve(process.argv[2]), fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const engines = { chromium, firefox, webkit }, selected = process.argv[3] ? [process.argv[3]] : Object.keys(engines), evidence = [];
for (const name of selected) {
  const browser = await engines[name].launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: 'reduce' });
  const page = await context.newPage(); page.setDefaultTimeout(12000);
  const errors = [], writes = [], intercepted = [], dependencies = [];
  let attempts = 0, held = null, hold = false;
  const check = text => { console.log(`${name}: ${text}`); evidence.push({ browser: name, check: text }); };
  page.on('pageerror', error => errors.push(error.message)); page.on('dialog', dialog => dialog.accept());
  page.on('request', request => {
    if (!['GET', 'HEAD'].includes(request.method())) writes.push({ method: request.method(), url: request.url() });
    if (['script', 'stylesheet'].includes(request.resourceType())) dependencies.push(new URL(request.url()).pathname);
  });
  await context.route('**/*', route => {
    const request = route.request();
    if (new URL(request.url()).origin !== new URL(fixture.base).origin) return route.abort('blockedbyclient');
    if (['GET', 'HEAD'].includes(request.method())) return route.fallback();
    intercepted.push({ method: request.method(), url: request.url() });
    if (request.url().includes('/article-components/validate')) return route.fulfill({ contentType: 'application/json', body: request.postData() });
    return route.fulfill({ status: 204 });
  });
  await page.route('**/js/chunnel-component-library.js*', route => {
    attempts++;
    if (hold) { held = route; return; }
    return attempts === 1 ? route.abort('failed') : route.continue();
  });
  const visit = async path => { await page.goto(fixture.base + path); await page.waitForLoadState('domcontentloaded'); };
  const launch = async () => {
    const menu = page.locator('.editor-insert-menu');
    if (!(await menu.evaluate(node => node.open))) await menu.locator(':scope > summary').click();
    await page.locator('[data-component-library-open]').click();
  };
  try {
    await visit(fixture.login); await visit(fixture.paths.editor);
    await page.locator('.ProseMirror').waitFor();
    const source = page.locator('[data-content-source]'), original = await source.inputValue();
    assert.equal(dependencies.some(url => /\/(chunnel-component-library\.js|chunnel-editor-components\.css)$/.test(url)), false);
    assert.equal(await page.locator('link[href*="chunnel-admin-layout-workspace.css"]').count(), 0);
    check('The default continuous editor starts without component scripts/styles or homepage-editor styles.');
    await launch();
    await page.waitForFunction(() => document.querySelector('[data-composer-feedback]').textContent.includes('加载失败'));
    assert.equal(await page.locator('[data-component-library]').evaluate(node => node.open), false);
    await launch(); await page.locator('[data-component-library][open]').waitFor();
    assert.equal(attempts, 2);
    assert.equal(await page.locator('link[href*="chunnel-editor-components.css"]').count(), 1);
    const cascade = await page.evaluate(() => {
      const sheets = [...document.querySelectorAll('link[rel=stylesheet]')].map(node => node.href);
      return sheets.findIndex(url => url.includes('chunnel-editor-content.css')) < sheets.findIndex(url => url.includes('chunnel-editor-components.css')) && sheets.findIndex(url => url.includes('chunnel-editor-components.css')) < sheets.findIndex(url => url.includes('chunnel-admin-settings.css'));
    }); assert.equal(cascade, true);
    await page.locator('[data-component-list] button').filter({ hasText: '提示块' }).click();
    await page.locator('[data-component-insert]').click();
    await page.waitForFunction(() => !document.querySelector('[data-component-library]').open);
    assert.match(await source.inputValue(), /data-np-widget="callout"/);
    await page.locator('[data-composer-undo]').click(); assert.equal(await source.inputValue(), original);
    check('Failed component loading retries; insertion preserves stylesheet order and editor undo.');

    const existing = await page.evaluate(() => window.NewPressComponentLibrary.markup(window.NewPressComponentLibrary.defaults('callout')));
    await visit(fixture.paths.editor);
    await page.evaluate(value => window.ChunnelBlockEditor.reset(value), existing);
    assert.equal(await page.locator('script[src*="/chunnel-component-library.js"]').count(), 0);
    await page.locator('.document-preserved').getByRole('button', { name: '编辑组件', exact: true }).click();
    await page.locator('[data-component-library][open]').waitFor();
    assert.equal(await page.locator('[data-component-insert]').innerText(), '保存组件设置');
    assert.match(await page.locator('[data-component-preview]').innerText(), /提示/);
    for (const width of [1280, 375]) {
      await page.setViewportSize({ width, height: 900 });
      const geometry = await page.locator('[data-component-library]').evaluate(node => {
        const r = node.getBoundingClientRect(); return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: innerWidth, height: innerHeight };
      });
      assert.ok(geometry.left >= -1 && geometry.right <= geometry.width + 1 && geometry.top >= -1 && geometry.bottom <= geometry.height + 1, JSON.stringify(geometry));
      await page.screenshot({ path: resolve(directory, `component-${name}-${width}.png`) });
    }
    await page.locator('[data-component-close]').click();
    await page.setViewportSize({ width: 1280, height: 900 });
    assert.equal(await source.inputValue(), existing);
    check('Existing component blocks keep an edit action before assets load; cancelling preserves markup at both viewport sizes.');
    await launch();
    const kinds = await page.locator('[data-component-list] [data-component-kind]').evaluateAll(nodes => nodes.map(node => node.dataset.componentKind));
    for (const kind of kinds) {
      await page.locator(`[data-component-kind="${kind}"]`).click();
      await page.locator(`[data-component-preview] [data-np-widget="${kind}"]`).waitFor();
    }
    assert.equal(kinds.length, 26);
    await page.locator('[data-component-close]').click();
    assert.equal(await source.inputValue(), existing);
    check('All 26 typed component defaults render without changing the document.');

    await visit(fixture.paths.editor); hold = true; await launch();
    const deadline = Date.now() + 12000;
    while (!held && Date.now() < deadline) await new Promise(resolve => setTimeout(resolve, 20));
    assert.ok(held); await page.evaluate(() => window.ChunnelBlockEditor.reset('<p>正文已更新</p>'));
    await held.continue(); held = null; hold = false;
    await page.waitForFunction(() => document.querySelector('[data-composer-feedback]').textContent.includes('正文或编辑权限已变化'));
    assert.equal(await source.inputValue(), '<p>正文已更新</p>');
    assert.equal(await page.locator('[data-component-library]').evaluate(node => node.open), false);
    await launch(); await page.locator('[data-component-library][open]').waitFor(); await page.locator('[data-component-close]').click();
    check('A delayed component download cannot act on a changed document; another explicit open uses the loaded assets.');

    for (const path of ['/admin/users/', '/admin/plugins/', '/admin/appearance/', '/admin/appearance/layout/']) {
      await visit(path); const layout = path.includes('/appearance/');
      assert.equal(await page.locator('link[href*="chunnel-admin-layout-workspace.css"]').count(), layout ? 1 : 0);
      assert.equal(await page.locator('link[href*="chunnel-admin-workspace.css"]').count(), layout ? 0 : 1);
      if (layout) {
        assert.ok(await page.locator('[data-home-editor]').count());
        assert.equal(await page.locator('.home-editor-row').first().evaluate(node => getComputedStyle(node).display), 'grid');
      }
    }
    const order = page.locator('[data-home-editor="top"] [data-home-order]'), before = await order.inputValue();
    await page.locator('[data-home-editor="top"] [data-home-move="down"]').first().click();
    assert.notEqual(await order.inputValue(), before);
    await page.screenshot({ path: resolve(directory, `layout-${name}.png`) });
    check('Only appearance/layout pages load homepage editor styles; real layout controls still reorder the form.');
    assert.deepEqual(errors, []); assert.deepEqual(writes, intercepted);
    check('No page errors or server writes; normal pages never load React/ECharts.');
    assert.equal(dependencies.some(url => /newpress-analytics\.js/.test(url)), false);
  } catch (error) {
    await page.screenshot({ path: resolve(directory, `component-${name}-failure.png`) }).catch(() => {}); throw error;
  } finally {
    if (held) await held.abort().catch(() => {});
    await writeFile(resolve(directory, `component-${name}.json`), JSON.stringify({ evidence: evidence.filter(item => item.browser === name), errors, writes, intercepted, dependencies }, null, 2) + '\n');
    await context.close(); await browser.close();
  }
}
await writeFile(resolve(directory, 'component-loading-acceptance.json'), JSON.stringify({ evidence }, null, 2) + '\n');
