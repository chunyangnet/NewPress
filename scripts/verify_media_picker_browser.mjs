// Media picker acceptance against the isolated real Go admin pages.
// Media API requests are mocked; all other writes are intercepted before the server.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';

const directory = resolve(process.argv[2]);
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const engines = { chromium, firefox, webkit };
const selected = process.argv[3] ? [process.argv[3]] : Object.keys(engines);
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==', 'base64');
const image = { id: 7, status: 'active', kind: 'image', width: 1, height: 1, size_bytes: png.length,
  title: '媒体迁移验收', filename: 'migration.png', public_url: '/media/7/migration.png', alt_text: '媒体迁移验收', caption: '' };
const evidence = [];

for (const name of selected) {
  const browser = await engines[name].launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: 'reduce' });
  const page = await context.newPage(); page.setDefaultTimeout(12000);
  const errors = [], writes = [], intercepted = [], uploads = [];
  let latest = image, uploadFailure = true, listFailure = false, holdItem = false, heldItem = null;
  const check = label => { evidence.push({ browser: name, check: label }); console.log(`${name}: ${label}`); };
  const json = (route, value, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(value) });
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => { if (!['GET', 'HEAD'].includes(request.method())) writes.push({ method: request.method(), url: request.url() }); });
  page.on('dialog', dialog => dialog.accept());
  await context.route('**/*', route => {
    if (['GET', 'HEAD'].includes(route.request().method())) return route.fallback();
    intercepted.push({ method: route.request().method(), url: route.request().url() });
    return route.fulfill({ status: 204 });
  });
  await page.route('**/api/v1/admin/media**', async route => {
    const request = route.request(), url = new URL(request.url());
    if (request.method() === 'POST') {
      intercepted.push({ method: request.method(), url: request.url() });
      uploads.push({ headers: request.headers(), body: request.postData() || '' });
      return uploadFailure ? json(route, { error: { code: 'media.duplicate_trashed', message: '相同文件已在回收站', details: { media_id: 7 } } }, 409)
        : json(route, { item: image, duplicate: true });
    }
    if (url.pathname === '/api/v1/admin/media/7') {
      if (holdItem) { heldItem = route; return; }
      return json(route, latest);
    }
    return json(route, listFailure ? { pagination: { page: 1 } } : { items: [image], pagination: { page: Number(url.searchParams.get('page') || 1), pages: 1, total: 1 }, can_upload: true, max_upload_bytes: 1048576 });
  });
  await page.route('**/media/7/migration.png', route => route.fulfill({ contentType: 'image/png', body: png }));
  const visit = async path => { await page.goto(fixture.base + path); await page.waitForLoadState('networkidle'); };
  const picker = page.locator('[data-editor-media-dialog]');
  const chooseImage = async () => {
    await page.locator('[data-editor-image-id="7"]').click();
    await page.waitForFunction(() => !document.querySelector('[data-editor-media-apply]').disabled);
    const pixel = await page.locator('[data-editor-media-preview] img').evaluate(image => {
      const canvas = document.createElement('canvas'); canvas.width = canvas.height = 1;
      const context = canvas.getContext('2d'); context.drawImage(image, 0, 0, 1, 1);
      return Array.from(context.getImageData(0, 0, 1, 1).data);
    });
    assert.deepEqual(pixel, [255, 0, 0, 255], 'preview decoded the complete fixture PNG');
  };
  const close = async () => {
    await page.locator('[data-editor-media-close]').click();
    await page.waitForFunction(() => !document.querySelector('[data-editor-media-dialog]').open);
  };
  const apply = async () => {
    await page.locator('[data-editor-media-apply]').click();
    await page.waitForFunction(() => !document.querySelector('[data-editor-media-dialog]').open);
  };
  try {
    await visit(fixture.login); await visit('/admin/series/?edit=new');
    assert.equal(await page.locator('script[src*="chunnel-admin-media.js"]').count(), 1);
    assert.equal(await page.locator('script[src*="chunnel-editor-media.js"]').count(), 0);
    const field = page.locator('input[name="cover_url"]'), wrapper = field.locator('..');
    const launcher = wrapper.getByRole('button', { name: '选择或上传选集封面', exact: true });
    await launcher.click(); await chooseImage();
    latest = { ...image, id: 8 };
    await page.locator('[data-editor-media-apply]').click();
    await page.waitForFunction(() => !document.querySelector('[data-editor-media-error]').hidden);
    assert.match(await page.locator('[data-editor-media-error]').innerText(), /不可用/); assert.equal(await field.inputValue(), '');
    latest = image; await apply(); assert.equal(await field.inputValue(), image.public_url);
    await page.waitForFunction(() => document.activeElement?.getAttribute('aria-label') === '选择或上传选集封面');
    check('Series field uses the merged TS picker, rejects a changed media ID and restores launcher focus.');

    await wrapper.getByRole('button', { name: '清除选集封面', exact: true }).click();
    await launcher.click(); await chooseImage(); holdItem = true;
    await page.locator('[data-editor-media-apply]').click();
    const detailDeadline = Date.now() + 12000;
    while (!heldItem && Date.now() < detailDeadline) await new Promise(resolve => setTimeout(resolve, 20));
    assert.ok(heldItem, 'selected media revalidation was requested');
    await field.evaluate(node => { node.readOnly = true; });
    await json(heldItem, image); heldItem = null; holdItem = false;
    await page.waitForFunction(() => !document.querySelector('[data-editor-media-error]').hidden);
    assert.match(await page.locator('[data-editor-media-error]').innerText(), /不可编辑/); assert.equal(await field.inputValue(), '');
    await close(); await field.evaluate(node => { node.readOnly = false; });
    check('A field locked while media revalidation is in flight remains unchanged.');

    listFailure = true; await launcher.click();
    await page.waitForFunction(() => !document.querySelector('[data-editor-media-error]').hidden);
    assert.equal(await page.locator('[data-editor-media-search] button').isEnabled(), true);
    listFailure = false; await page.locator('[data-editor-media-retry]').click(); await chooseImage(); await close();
    check('Malformed list responses leave controls usable and an explicit retry recovers.');

    await launcher.click(); await page.locator('[data-editor-image-id="7"]').waitFor();
    await page.waitForFunction(() => !document.querySelector('[data-editor-media-upload]').disabled);
    await page.locator('[data-editor-media-file]').setInputFiles({ name: 'retry.png', mimeType: 'image/png', buffer: png });
    await page.waitForFunction(() => !document.querySelector('[data-editor-media-error]').hidden);
    assert.match(await page.locator('[data-editor-media-recover]').getAttribute('href'), /\/admin\/media\/7\/edit\//);
    uploadFailure = false; await page.locator('[data-editor-media-retry]').click();
    await page.waitForFunction(() => document.querySelector('[data-editor-media-status]').textContent.includes('相同图片'));
    assert.equal(uploads.length, 2); assert.match(uploads[0].body, /retry\.png/); assert.match(uploads[1].body, /retry\.png/);
    assert.match(uploads[1].body, /series-cover/); assert.ok(uploads[1].headers['x-csrf-token']);
    await page.waitForFunction(() => !document.querySelector('[data-editor-media-apply]').disabled); await apply();
    assert.equal(await field.inputValue(), image.public_url);
    check('Upload retry retains the file, CSRF token and source; duplicate recovery is available without a server write.');

    await visit(fixture.paths.editor);
    await page.locator('[data-composer].is-document-editor .ProseMirror').waitFor();
    const source = page.locator('[data-content-source]'), original = await source.inputValue();
    await page.locator('.ProseMirror').click(); await page.keyboard.press('ControlOrMeta+End');
    await page.locator('.editor-insert-menu > summary').click();
    await page.locator('.editor-insert-menu [data-editor-media-open="body"]').click(); await chooseImage();
    await page.locator('[data-editor-media-insert] [name="alt_text"]').fill('迁移验收图片');
    await apply(); await page.waitForFunction(url => document.querySelector('[data-content-source]').value.includes(url), image.public_url);
    assert.equal(await page.locator('.ProseMirror img[src="' + image.public_url + '"]').count(), 1);
    assert.match(await source.inputValue(), /迁移验收图片/);
    await page.locator('[data-composer-undo]').click();
    assert.equal(await source.inputValue(), original);
    await page.locator('[data-composer-redo]').click(); assert.match(await source.inputValue(), /migration\.png/);
    await page.locator('[data-composer-undo]').click();
    check('Default continuous editor inserts the chosen image and retains undo/redo without saving the article.');

    await page.locator('[data-composer-mode="source"]').evaluate(node => { node.closest('details').open = true; });
    await page.locator('[data-composer-mode="source"]').click();
    await page.locator('[data-editor-media-open="body"]:visible').first().click(); await chooseImage();
    latest = { ...image, status: 'trash' };
    await page.locator('[data-editor-media-apply]').click();
    await page.waitForFunction(() => !document.querySelector('[data-editor-media-error]').hidden);
    assert.equal(await source.inputValue(), original); latest = image;
    await page.locator('[data-editor-media-insert] [name="alt_text"]').fill('源码模式图片'); await apply();
    assert.match(await source.inputValue(), /源码模式图片/);
    check('Source mode leaves text intact for trashed media, then inserts after successful revalidation.');

    await page.evaluate(value => window.ChunnelBlockEditor.reset(value), original);
    await page.locator('[data-composer-mode="blocks"]').evaluate(node => { node.closest('details').open = true; });
    await page.locator('[data-composer-mode="blocks"]').click();
    await page.evaluate(() => {
      const bytes = Uint8Array.from(atob('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg=='), c => c.charCodeAt(0));
      const transfer = new DataTransfer(); transfer.items.add(new File([bytes], 'clipboard.png', { type: 'image/png' }));
      const event = new ClipboardEvent('paste', { bubbles: true, cancelable: true, clipboardData: transfer });
      // Firefox strips files supplied to a synthetic ClipboardEvent constructor.
      // Supply the browser FileList explicitly; this validates the event path, not OS clipboard access.
      if (event.clipboardData.files.length !== 1) Object.defineProperty(event, 'clipboardData', { value: transfer });
      document.querySelector('.ProseMirror').dispatchEvent(event);
    });
    await picker.waitFor({ state: 'visible' });
    await page.waitForFunction(() => document.querySelector('[data-editor-media-status]').textContent.includes('相同图片'));
    assert.match(uploads.at(-1).body, /clipboard\.png/); assert.match(uploads.at(-1).body, /editor-body/);
    await page.keyboard.press('Escape'); await page.waitForFunction(() => !document.querySelector('[data-editor-media-dialog]').open);
    assert.equal(await source.inputValue(), original);
    check('Pasting a file in the continuous editor opens the same picker; Escape cancels without changing text.');

    const settingsToggle = page.locator('[data-editor-settings-toggle]');
    if (await settingsToggle.getAttribute('aria-expanded') !== 'true') await settingsToggle.click();
    await page.locator('[data-editor-media-open="cover"]').click(); await chooseImage(); await apply();
    assert.equal(await page.locator('input[name="cover_url"]').inputValue(), image.public_url);
    assert.equal(await source.inputValue(), original);
    check('Editor cover selection updates only its field and keeps the document intact.');

    await page.evaluate(() => {
      const launcher = document.createElement('button'); launcher.id = 'media-component-probe'; launcher.textContent = '选择组件图片';
      document.body.append(launcher); window.NewPressComponentMedia(launcher, (url, item) => { window.__mediaComponent = { url, id: item.id, alt: item.alt_text }; });
    });
    await chooseImage(); await apply();
    assert.deepEqual(await page.evaluate(() => window.__mediaComponent), { url: image.public_url, id: 7, alt: image.alt_text });
    check('Component callback receives the revalidated image metadata.');

    for (const width of [1280, 375]) {
      await page.setViewportSize({ width, height: 900 });
      await page.evaluate(() => window.NewPressMediaPicker.open(document.querySelector('#media-component-probe'), { title: '媒体选择器验收', onSelect() {} }));
      await chooseImage();
      const geometry = await picker.evaluate(node => { const r = node.getBoundingClientRect(); return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: innerWidth, height: innerHeight }; });
      assert.ok(geometry.left >= -1 && geometry.right <= geometry.width + 1 && geometry.top >= -1 && geometry.bottom <= geometry.height + 1, JSON.stringify(geometry));
      await page.screenshot({ path: resolve(directory, `picker-${name}-${width}.png`) }); await close();
    }
    check('Picker stays within desktop and narrow viewports.');
    assert.deepEqual(errors, []); assert.deepEqual(writes, intercepted);
    check('No browser exceptions; every attempted write was intercepted.');
  } catch (error) {
    await page.screenshot({ path: resolve(directory, `picker-${name}-failure.png`) }).catch(() => {});
    throw error;
  } finally {
    if (heldItem) await heldItem.abort().catch(() => {});
    await writeFile(resolve(directory, `picker-${name}.json`), JSON.stringify({ evidence: evidence.filter(item => item.browser === name), errors,
      writes, intercepted, uploads: uploads.map(item => ({ bytes: item.body.length, csrfPresent: Boolean(item.headers['x-csrf-token']) })) }, null, 2) + '\n');
    await context.close(); await browser.close();
  }
}
await writeFile(resolve(directory, 'picker-acceptance.json'), JSON.stringify({ evidence }, null, 2) + '\n');
