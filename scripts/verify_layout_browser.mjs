// Isolated Go pages: only the non-persisting preview endpoint may receive POST.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';
import { build } from 'esbuild';
const directory = resolve(process.argv[2]), fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const engines = { chromium, firefox, webkit }, selected = process.argv[3] && process.argv[3] !== 'all' ? [process.argv[3]] : Object.keys(engines), evidence = [];
const tsAssets = new Map();
if (process.argv[4] === 'typescript') for (const name of ['home-editor', 'design', 'appearance-preview']) {
  const compiled = await build({ entryPoints: [`frontend/core/${name}.ts`], bundle: true, format: 'iife', write: false });
  tsAssets.set(`chunnel-${name}.js`, compiled.outputFiles[0].text);
}
for (const name of selected) {
  const browser = await engines[name].launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce' });
  const page = await context.newPage(); page.setDefaultTimeout(12000);
  const errors = [], writes = [], previews = [], intercepted = [];
  let failPreview = false, hold = false, held = null;
  page.on('pageerror', error => errors.push(error.message)); page.on('dialog', dialog => dialog.accept());
  page.on('request', request => { if (!['GET', 'HEAD'].includes(request.method())) writes.push(request.url()); });
  await context.route('**/*', route => {
    const request = route.request(), url = new URL(request.url());
    const ts = tsAssets.get(url.pathname.split('/').at(-1));
    if (ts) return route.fulfill({ contentType: 'application/javascript', body: ts });
    if (url.pathname === '/admin/appearance/preview/') {
      previews.push(request.url());
      if (hold) { held = route; return; }
      if (failPreview) return route.fulfill({ status: 503, contentType: 'application/json', body: '{"error":"unavailable"}' });
      return route.continue();
    }
    if (['GET', 'HEAD'].includes(request.method())) return route.continue();
    intercepted.push(request.url()); return route.fulfill({ status: 204 });
  });
  const check = text => { console.log(`${name}: ${text}`); evidence.push({ browser: name, check: text }); };
  const visit = async path => { await page.goto(fixture.base + path); await page.waitForLoadState('networkidle'); };
  const payload = () => page.locator('[name="home_custom_blocks"]').inputValue();
  const ready = async () => { await page.waitForFunction(() => document.querySelector('[data-preview-status]')?.textContent.includes('预览已同步')); };
  const undo = () => page.locator('[data-layout-undo]').click();
  const redo = () => page.locator('[data-layout-redo]').click();
  try {
    await visit(fixture.login); await visit('/admin/appearance/layout/'); await ready();
    const initialCustom = await payload(), initialTop = await page.locator('[name="home_top_order"]').inputValue();
    const first = page.locator('[data-home-editor="top"] .home-editor-row').first(), key = await first.getAttribute('data-home-key');
    await first.locator('[data-home-move="down"]').click();
    assert.notEqual(await page.locator('[name="home_top_order"]').inputValue(), initialTop);
    await undo(); assert.equal(await page.locator('[name="home_top_order"]').inputValue(), initialTop);
    await redo(); await undo();
    const firstRow = page.locator(`[data-home-key="${key}"]`);
    await firstRow.locator('[data-home-region]').selectOption('aside');
    assert.equal(await firstRow.evaluate(row => row.closest('[data-home-editor]').dataset.homeEditor), 'aside');
    await undo(); assert.equal(await firstRow.evaluate(row => row.closest('[data-home-editor]').dataset.homeEditor), 'top');
    await firstRow.locator('[data-home-handle]').focus(); await page.keyboard.press('ArrowDown');
    assert.notEqual(await page.locator('[name="home_top_order"]').inputValue(), initialTop); await undo();
    check('Layout button/keyboard ordering and cross-region movement update native fields and undo/redo correctly.');

    const destinationKey = await page.locator('[data-home-editor="top"] .home-editor-row').nth(1).getAttribute('data-home-key');
    const destination = page.locator(`[data-home-key="${destinationKey}"]`);
    await firstRow.evaluate(row => row.scrollIntoView({ block: 'center' }));
    const box = await destination.boundingBox();
    await firstRow.locator('[data-home-handle]').dragTo(destination, { targetPosition: { x: 20, y: box.height - 8 } });
    assert.notEqual(await page.locator('[name="home_top_order"]').inputValue(), initialTop); await undo();
    await firstRow.evaluate(row => row.scrollIntoView({ block: 'center' }));
    const start = await firstRow.locator('[data-home-handle]').boundingBox(), end = await destination.boundingBox();
    await page.mouse.move(start.x + start.width / 2, start.y + start.height / 2); await page.mouse.down();
    await page.mouse.move(end.x + 20, end.y + end.height - 8, { steps: 10 });
    await page.keyboard.press('Escape'); await page.mouse.up();
    assert.equal(await page.locator('[name="home_top_order"]').inputValue(), initialTop);
    assert.equal(await firstRow.evaluate(row => row.parentElement.firstElementChild === row), true);
    check('Real mouse dragging commits one order change; Escape restores the pre-drag DOM and native form values.');

    await firstRow.evaluate(row => row.scrollIntoView({ block: 'center' }));
    const handleRect = await firstRow.locator('[data-home-handle]').boundingBox(), targetRect = await destination.boundingBox();
    const from = { x: handleRect.x + handleRect.width / 2, y: handleRect.y + handleRect.height / 2 };
    const to = { x: targetRect.x + 40, y: targetRect.y + targetRect.height - 8 };
    await page.mouse.move(from.x, from.y); await page.mouse.down(); await page.mouse.move(to.x, to.y, { steps: 8 });
    await page.mouse.move(10, 100, { steps: 3 }); await page.mouse.up();
    assert.equal(await page.locator('[name="home_top_order"]').inputValue(), initialTop);
    if (name === 'chromium') {
      const session = await context.newCDPSession(page);
      await session.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ ...from, id: 1 }] });
      await session.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ ...to, id: 1 }] });
      await session.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
      assert.notEqual(await page.locator('[name="home_top_order"]').inputValue(), initialTop); await undo();
      await firstRow.evaluate(row => row.scrollIntoView({ block: 'center' }));
      const start = await firstRow.locator('[data-home-handle]').boundingBox(), end = await destination.boundingBox();
      await session.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: start.x + 10, y: start.y + 15, id: 2 }] });
      await session.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: end.x + 30, y: end.y + end.height - 8, id: 2 }] });
      await session.send('Input.dispatchTouchEvent', { type: 'touchCancel', touchPoints: [] });
      assert.equal(await page.locator('[name="home_top_order"]').inputValue(), initialTop); await session.detach();
      check('Browser-protocol touch movement commits on release and restores on pointer cancellation.');
    }
    check('Releasing outside every layout region restores the previous order without changing saved fields.');
    await firstRow.evaluate(row => row.scrollIntoView({ block: 'center' }));
    const edgeStart = await firstRow.locator('[data-home-handle]').boundingBox(), scrollBefore = await page.evaluate(() => scrollY);
    await page.mouse.move(edgeStart.x + 12, edgeStart.y + 15); await page.mouse.down();
    await page.mouse.move(edgeStart.x + 12, 990, { steps: 5 });
    await page.waitForFunction(before => scrollY > before, scrollBefore);
    await page.keyboard.press('Escape'); await page.mouse.up();
    assert.equal(await page.locator('[name="home_top_order"]').inputValue(), initialTop);
    assert.equal(await firstRow.locator('[data-home-handle]').evaluate(node => document.activeElement === node), true);
    check('Dragging at the viewport edge scrolls toward offscreen regions; cancellation restores order and keyboard focus.');

    await page.locator('[data-home-custom-add]').first().click();
    const custom = page.locator('[data-home-custom]').last(), title = custom.locator('[data-home-custom-title]'), html = custom.locator('[data-home-custom-html]');
    await title.fill('迁移验收 HTML'); await html.fill('<p>自定义 <strong>正文</strong></p>');
    await html.blur();
    const one = await payload(); assert.equal(JSON.parse(one).at(-1).title, '迁移验收 HTML');
    await html.focus(); await html.evaluate(node => { node.setSelectionRange(4, 4); window.layoutActiveHTML = node; });
    await page.evaluate(() => document.dispatchEvent(new Event('appearance-preview-change')));
    assert.equal(await html.evaluate(node => node === window.layoutActiveHTML && document.activeElement === node && node.selectionStart === 4), true);
    await custom.locator('[data-home-custom-copy]').click();
    assert.equal(await page.locator('[data-home-custom]').count(), 2);
    const copied = JSON.parse(await payload()); assert.notEqual(copied[0].key, copied[1].key);
    await page.locator('[data-home-custom]').last().locator('[data-home-custom-remove]').click();
    assert.equal(await page.locator('[data-home-custom]').count(), 1);
    await undo(); assert.equal(await page.locator('[data-home-custom]').count(), 2);
    await undo(); assert.equal(await payload(), one);
    check('Custom HTML add/copy/remove uses distinct IDs; undo restores source and preview events preserve focused text selection.');

    await page.evaluate(() => { const input = document.querySelector('[name="home_custom_blocks"]'); input.value = '{broken'; input.form.dispatchEvent(new Event('input', { bubbles: true })); });
    assert.equal(await payload(), '{broken'); assert.equal(await page.locator('[data-home-custom]').count(), 1);
    const prevented = await page.evaluate(() => !document.querySelector('#theme-settings-form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })));
    assert.equal(prevented, true); assert.equal(await payload(), '{broken');
    await page.evaluate(value => { const input = document.querySelector('[name="home_custom_blocks"]'); input.value = value; input.form.dispatchEvent(new Event('input', { bubbles: true })); }, one);
    await page.locator('[data-layout-defaults]').click();
    assert.equal(JSON.parse(await payload()).every(item => item.visibility === 'hidden'), true);
    await undo(); assert.equal(await payload(), one);
    check('Malformed restored HTML is preserved and blocks submit; valid restoration and layout defaults remain undoable.');

    await page.evaluate(() => { const form = document.querySelector('#theme-settings-form'); const fieldset = form.querySelector('fieldset'); fieldset.disabled = true; form.dispatchEvent(new Event('change', { bubbles: true })); });
    assert.equal(await page.locator('[data-home-custom-add]').first().isDisabled(), true);
    await page.evaluate(() => { const form = document.querySelector('#theme-settings-form'); form.querySelector('fieldset').disabled = false; form.dispatchEvent(new Event('change', { bubbles: true })); });
    await ready();
    const iframe = page.locator('iframe.appearance-preview-frame.is-ready');
    assert.equal(await iframe.getAttribute('sandbox'), 'allow-same-origin');
    assert.equal(await iframe.evaluate(node => node.contentDocument.body.inert), true);
    assert.equal(await iframe.evaluate(node => node.contentDocument.querySelectorAll('script,iframe,object,embed,[autofocus]').length), 0);
    await page.locator('[data-preview-device="mobile"]').click(); assert.equal(await page.locator('[data-preview-viewport]').innerText(), '390 px');
    await page.locator('[data-preview-device="desktop"]').click();
    failPreview = true; await page.locator('[data-preview-refresh]').click();
    await page.waitForFunction(() => document.querySelector('[data-preview-status]').dataset.tone === 'error');
    assert.equal(await iframe.count(), 1);
    failPreview = false; await page.locator('[data-preview-refresh]').click(); await ready();
    await page.locator('[data-color-mode="dark"]').first().click();
    await page.waitForFunction(() => document.querySelector('iframe.is-ready').contentDocument.documentElement.dataset.theme === 'dark');
    check('Preview uses an inert sandbox, keeps the previous frame on failure, retries and follows the current theme and viewport.');
    for (const width of [1440, 375]) {
      await page.setViewportSize({ width, height: 1000 });
      await page.evaluate(async () => {
        scrollTo(0, 0);
        await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
        const sample = () => JSON.stringify([...document.querySelectorAll('.design-workspace,.design-workspace h1,.admin-save-dock')].map(node => {
          const style = getComputedStyle(node), r = node.getBoundingClientRect(); return [style.color, style.backgroundColor, r.x, r.y, r.width, r.height];
        }));
        let previous = sample(), stable = 0; const deadline = performance.now() + 2500;
        while (stable < 3) { if (performance.now() > deadline) throw new Error('Layout did not settle'); await new Promise(resolve => setTimeout(resolve, 60)); const next = sample(); stable = next === previous ? stable + 1 : 0; previous = next; }
      });
      const layout = await page.evaluate(() => {
        const dock = document.querySelector('.admin-save-dock').getBoundingClientRect();
        return { left: dock.left, right: dock.right, contentRight: document.documentElement.getBoundingClientRect().right, overflow: document.documentElement.scrollWidth > innerWidth + 1 };
      });
      assert.equal(layout.overflow, false); assert.ok(Math.abs(layout.right - layout.contentRight) <= 1, JSON.stringify(layout));
      if (width === 375) assert.ok(Math.abs(layout.left) <= 1, JSON.stringify(layout));
      await page.screenshot({ path: resolve(directory, `layout-${name}-${width}.png`) });
    }
    check('After viewport and theme transitions settle, desktop/mobile layouts fit and the save dock spans the content viewport.');

    await page.setViewportSize({ width: 1440, height: 1000 }); await visit('/admin/appearance/');
    const count = previews.length; await page.waitForTimeout(450); assert.equal(previews.length, count);
    hold = true; await page.locator('[data-settings-preview-open]').click();
    await page.waitForTimeout(450); assert.ok(held, 'dialog should request preview after opening');
    await page.keyboard.press('Escape'); hold = false;
    await held.continue().catch(() => {}); held = null;
    await page.waitForTimeout(200);
    assert.equal(await page.locator('[data-appearance-preview] iframe:not(.is-ready)').count(), 0);
    await page.locator('[data-settings-preview-open]').click(); await ready();
    await page.locator('[data-preview-page="article"]').click();
    await page.waitForFunction(() => document.querySelector('[data-preview-status]').textContent.includes('还没有公开文章'));
    assert.equal(await page.locator('iframe.is-ready').count(), 1);
    await page.locator('[data-preview-page="home"]').click(); await ready(); await page.keyboard.press('Escape');
    check('Appearance dialog loads on open, closing cancels delayed work, reopening recovers and missing articles retain the last frame.');
    await visit('/admin/modules/');
    const cards = page.locator('[data-module-card]'), total = await cards.count(); assert.ok(total > 0);
    await page.locator('[data-module-search]').fill('不存在的模块验收'); assert.equal(await cards.filter({ visible: true }).count(), 0);
    await page.locator('[data-module-empty]').waitFor(); await page.locator('[data-module-search]').fill('');
    assert.equal(await cards.filter({ visible: true }).count(), total);
    check('Module center search retains its empty state and restores all cards after clearing.');
    assert.deepEqual(errors, []); assert.equal(intercepted.length, 0);
    assert.ok(writes.every(url => new URL(url).pathname === '/admin/appearance/preview/'), JSON.stringify(writes));
    check('No browser exception or persisted form write; only the read-only preview operation reached the server.');
  } finally {
    if (held) await held.abort().catch(() => {});
    await context.close(); await browser.close();
  }
}
await writeFile(resolve(directory, 'layout-checks.json'), JSON.stringify(evidence, null, 2) + '\n');
