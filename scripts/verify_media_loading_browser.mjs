// Media loading against isolated Go pages. Synthetic FileLists exercise browser
// event handling; they do not stand in for real OS clipboard acceptance.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { createHash } from 'node:crypto';
import { chromium, firefox, webkit } from 'playwright';
import { build } from 'esbuild';

const directory = resolve(process.argv[2]);
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const variant = process.argv[3] || 'generated';
const engines = { chromium, firefox, webkit };
const selected = process.argv[4] ? [process.argv[4]] : Object.keys(engines);
const scripts = new Map(), evidence = [];
const encodedPNG = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==';
const png = Buffer.from(encodedPNG, 'base64');
const image = { id: 7, status: 'active', kind: 'image', width: 1, height: 1, size_bytes: png.length,
  title: '图片按需加载验收', filename: 'migration.png', public_url: '/media/7/migration.png', alt_text: '验收图片', caption: '' };
if (variant === 'typescript') for (const entry of ['editor-page', 'editor-media-dialog']) {
  scripts.set(`newpress-${entry}.js`, (await build({ entryPoints: [`frontend/core/${entry}.ts`], bundle: true, write: false,
    format: 'iife', platform: 'browser', target: ['chrome100', 'firefox110', 'safari16'] })).outputFiles[0].text);
}
const until = async predicate => {
  const deadline = Date.now() + 12000;
  while (!predicate()) {
    if (Date.now() > deadline) throw Error('Held media script did not arrive');
    await new Promise(resolve => setTimeout(resolve, 20));
  }
};

for (const name of selected) {
  const browser = await engines[name].launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: 'reduce' });
  const page = await context.newPage(); page.setDefaultTimeout(12000);
  const errors = [], writes = [], intercepted = [], downloads = [], uploads = [], assets = [];
  let mode = '', held = null;
  const check = label => { evidence.push({ browser: name, check: label }); console.log(`${name}: ${label}`); };
  const json = (route, value, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(value) });
  page.on('pageerror', error => errors.push(error.message));
  page.on('dialog', dialog => dialog.accept());
  page.on('request', request => {
    if (request.url().includes('/js/')) downloads.push(new URL(request.url()).pathname.split('/').pop());
    if (!['GET', 'HEAD'].includes(request.method())) writes.push({ method: request.method(), url: request.url() });
  });
  await page.addInitScript(() => {
    const original = window.setTimeout.bind(window);
    window.setTimeout = (callback, delay, ...args) => original(callback, window.__shortMediaTimeout && delay === 15000 ? 200 : delay, ...args);
    const fetch = window.fetch.bind(window);
    window.__mediaUploadFiles = [];
    window.fetch = async (input, init) => {
      if (init?.body instanceof FormData) {
        const file = init.body.get('file');
        if (file instanceof File) window.__mediaUploadFiles.push({ name: file.name, type: file.type, size: file.size,
          original: file === window.__mediaEventFile, bytes: Array.from(new Uint8Array(await file.arrayBuffer())) });
      }
      return fetch(input, init);
    };
  });
  await context.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url()), file = url.pathname.split('/').pop();
    if (url.origin !== fixture.base) return route.abort('blockedbyclient');
    const serve = () => scripts.has(file) ? route.fulfill({ contentType: 'text/javascript', body: scripts.get(file) }) : route.continue();
    if (file === 'newpress-editor-media-dialog.js') {
      if (mode === 'fail') return route.abort('failed');
      if (mode === 'empty') return route.fulfill({ contentType: 'text/javascript', body: '/* no runtime */' });
      if (mode === 'hold') { held = { serve, abort: () => route.abort() }; return; }
    }
    if (scripts.has(file)) return serve();
    if (url.pathname === '/api/v1/admin/updates') return json(route, { error: { message: 'unavailable in this fixture' } }, 503);
    if (!['GET', 'HEAD'].includes(request.method())) {
      intercepted.push({ method: request.method(), url: request.url() });
      if (url.pathname === '/api/v1/admin/media' && request.method() === 'POST') {
        const body = request.postDataBuffer();
        uploads.push({ body: body.toString('latin1'), containsPNG: body.includes(png), csrfPresent: !!request.headers()['x-csrf-token'] });
        return json(route, { item: image, duplicate: true });
      }
      return route.fulfill({ status: 204 });
    }
    if (url.pathname === '/api/v1/admin/media/7') return json(route, image);
    if (url.pathname === '/api/v1/admin/media') return json(route, { items: [image], pagination: { page: 1, pages: 1, total: 1 }, can_upload: true, max_upload_bytes: 1048576 });
    if (url.pathname === image.public_url) return route.fulfill({ contentType: 'image/png', body: png });
    return route.fallback();
  });
  const source = page.locator('[data-content-source]'), picker = page.locator('[data-editor-media-dialog]');
  const retry = page.getByRole('button', { name: '重试加载图片工具', exact: true });
  const notice = page.locator('.admin-media-field-status[role="status"]');
  const runtimeDownloads = () => downloads.filter(file => file === 'newpress-editor-media-dialog.js').length;
  const visit = async () => {
    mode = ''; held = null;
    await page.goto(fixture.base + fixture.paths.editor); await page.waitForLoadState('networkidle');
    await page.locator('[data-composer].is-document-editor .ProseMirror').waitFor();
  };
  const openBody = async () => {
    const menu = page.locator('.editor-insert-menu');
    if (!await menu.evaluate(node => node.open)) await menu.locator(':scope > summary').click();
    await menu.locator('[data-editor-media-open="body"]').click();
  };
  const openCover = async () => {
    const toggle = page.locator('[data-editor-settings-toggle]');
    if (await toggle.getAttribute('aria-expanded') !== 'true') await toggle.click();
    await page.locator('[data-editor-media-open="cover"]').click();
  };
  const release = async () => { const pending = held; held = null; await pending.serve(); };
  const ready = () => page.waitForFunction(() => !!window.NewPressMediaRuntime);
  const closed = async () => assert.equal(await picker.evaluate(node => node.open), false);
  const close = async () => { await page.locator('[data-editor-media-close]').click(); await page.waitForFunction(() => !document.querySelector('[data-editor-media-dialog]').open); };
  const choose = async () => {
    await page.locator('[data-editor-image-id="7"]').click();
    await page.waitForFunction(() => !document.querySelector('[data-editor-media-apply]').disabled);
  };
  const apply = async () => { await page.locator('[data-editor-media-apply]').click(); await page.waitForFunction(() => !document.querySelector('[data-editor-media-dialog]').open); };
  const fileEvent = (kind, count = 1) => page.evaluate(({ kind, count, encodedPNG }) => {
    const bytes = Uint8Array.from(atob(encodedPNG), char => char.charCodeAt(0));
    const transfer = new DataTransfer();
    for (let i = 0; i < count; i++) transfer.items.add(new File([bytes], `first-${kind}-${i}.png`, { type: 'image/png' }));
    window.__mediaEventFile = transfer.files[0];
    const event = kind === 'paste' ? new ClipboardEvent('paste', { bubbles: true, cancelable: true, clipboardData: transfer })
      : new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: transfer });
    if (kind === 'paste' && event.clipboardData?.files.length !== count) Object.defineProperty(event, 'clipboardData', { value: transfer });
    document.querySelector('.ProseMirror').dispatchEvent(event);
  }, { kind, count, encodedPNG });
  try {
    await page.goto(fixture.base + fixture.login); await visit();
    const original = await source.inputValue();
    assert.equal(runtimeDownloads(), 0);
    assert.equal(await page.locator('script[src*="chunnel-admin-media.js"]').count(), 0);
    assert.equal(await page.evaluate(() => !!window.NewPressMediaPicker && !!window.NewPressComponentMedia && !window.NewPressMediaRuntime), true);
    await page.locator('.ProseMirror > p').last().click(); await page.keyboard.type('继续编辑');
    await page.locator('[data-composer-undo]').click(); assert.equal(await source.inputValue(), original);
    assert.equal(runtimeDownloads(), 0);
    check('The continuous editor, undo and public media entrypoints are ready without downloading the media dialog.');

    for (const kind of ['paste', 'drop']) {
      await visit(); mode = 'fail';
      await page.locator('.ProseMirror > p').last().click();
      await page.waitForFunction(() => window.ChunnelBlockEditor.capture().start === document.querySelector('[data-content-source]').value.length);
      const captured = await page.evaluate(() => window.ChunnelBlockEditor.capture()), beforeUpload = uploads.length;
      await fileEvent(kind); await retry.waitFor();
      await closed(); assert.equal(uploads.length, beforeUpload); assert.equal(await source.inputValue(), original);
      await page.locator('.ProseMirror > h2').first().click();
      await page.waitForFunction(start => window.ChunnelBlockEditor.capture().start !== start, captured.start);
      await page.evaluate(() => {
        const editor = window.ChunnelBlockEditor, insert = editor.insert;
        editor.insert = (markup, selection) => { window.__mediaInsertSelection = selection; return insert(markup, selection); };
      });
      mode = 'hold'; await retry.click(); await until(() => held);
      assert.equal(uploads.length, beforeUpload); await release(); mode = '';
      await page.waitForFunction(() => document.querySelector('[data-editor-media-status]').textContent.includes('相同图片'));
      assert.equal(uploads.length, beforeUpload + 1);
      // The WebKit protocol exposes multipart postData as text and does not
      // preserve binary octets. Verify the browser's actual submitted File too.
      const submitted = await page.evaluate(() => window.__mediaUploadFiles);
      assert.equal(submitted.length, 1); assert.equal(submitted[0].original, true);
      assert.deepEqual(submitted[0].bytes, Array.from(png)); assert.equal(submitted[0].size, png.length);
      assert.equal(submitted[0].name, `first-${kind}-0.png`); assert.equal(submitted[0].type, 'image/png');
      assert.ok(uploads.at(-1).csrfPresent);
      if (name !== 'webkit') assert.ok(uploads.at(-1).containsPNG);
      assert.ok(uploads.at(-1).body.includes(`first-${kind}-0.png`));
      await page.locator('[data-editor-media-insert] [name="alt_text"]').fill('首次文件'); await apply();
      assert.deepEqual(await page.evaluate(() => window.__mediaInsertSelection), captured);
      assert.match(await source.inputValue(), /migration\.png/);
      await page.locator('[data-composer-undo]').click(); assert.equal(await source.inputValue(), original);
      check(`First ${kind} survives a failed script; explicit retry uploads the original bytes once and inserts at the original selection with undo.`);
    }

    for (const invalidation of ['body', 'readonly', 'fieldset', 'editor']) {
      await visit(); mode = 'hold'; const beforeUpload = uploads.length;
      await fileEvent('paste'); await until(() => held);
      await page.evaluate(kind => {
        const source = document.querySelector('[data-content-source]');
        if (kind === 'body') window.ChunnelBlockEditor.reset('<p>媒体下载期间的新输入</p>');
        else if (kind === 'readonly') source.readOnly = true;
        else if (kind === 'editor') window.ChunnelBlockEditor = { ...window.ChunnelBlockEditor };
        else { const fieldset = document.createElement('fieldset'); source.before(fieldset); fieldset.append(source); fieldset.disabled = true; }
      }, invalidation);
      await release(); mode = '';
      await page.waitForFunction(() => document.querySelector('[data-composer-feedback]').textContent.includes('已变化'));
      await closed(); assert.equal(uploads.length, beforeUpload); assert.equal(await retry.count(), 0);
      assert.equal(await source.inputValue(), invalidation === 'body' ? '<p>媒体下载期间的新输入</p>' : original);
      check(`A delayed file intent is rejected after ${invalidation} changes, preserving input without uploading.`);
    }

    await visit(); mode = 'hold'; await openCover(); await until(() => held);
    await page.locator('input[name="cover_url"]').fill('/media/new-cover.png');
    await release(); mode = ''; await ready(); await notice.filter({ hasText: '已变化' }).waitFor(); await closed();
    assert.equal(await page.locator('input[name="cover_url"]').inputValue(), '/media/new-cover.png');
    check('A changed cover invalidates the delayed cover request.');

    await visit(); mode = 'empty'; await openBody(); await retry.waitFor();
    assert.match(await notice.innerText(), /初始化失败/); mode = ''; await retry.click(); await choose(); await close();
    check('A script that fails to initialize can be explicitly retried.');

    for (const cancellation of ['escape', 'cancel', 'pagehide', 'timeout']) {
      await visit(); mode = 'hold'; const beforeUpload = uploads.length;
      if (cancellation === 'timeout') await page.evaluate(() => { window.__shortMediaTimeout = true; });
      await fileEvent('drop'); await until(() => held);
      if (cancellation === 'escape') await page.keyboard.press('Escape');
      else if (cancellation === 'cancel') await notice.getByRole('button', { name: '取消', exact: true }).click();
      else if (cancellation === 'pagehide') await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })));
      else { await retry.waitFor(); await page.evaluate(() => { window.__shortMediaTimeout = false; }); }
      await release(); mode = ''; await ready(); await closed(); assert.equal(uploads.length, beforeUpload);
      if (cancellation === 'pagehide') await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })));
      const beforeDownload = runtimeDownloads(); await openBody(); await choose(); await close();
      assert.equal(runtimeDownloads(), beforeDownload); assert.equal(uploads.length, beforeUpload);
      check(`${cancellation} prevents a late script from opening or uploading; a new click reuses the runtime without the old file.`);
    }

    await visit(); mode = 'hold'; const beforeLatestDownload = runtimeDownloads(), beforeLatestUpload = uploads.length;
    await fileEvent('paste'); await until(() => held); await openCover();
    assert.equal(runtimeDownloads(), beforeLatestDownload + 1); await release(); mode = ''; await choose(); await apply();
    assert.equal(await page.locator('input[name="cover_url"]').inputValue(), image.public_url);
    assert.equal(uploads.length, beforeLatestUpload); assert.equal(await source.inputValue(), original);
    check('Concurrent body and cover intents share one download, and only the latest cover intent is activated.');

    await visit(); mode = 'hold';
    await page.evaluate(() => {
      const owner = document.createElement('dialog'); owner.id = 'media-owner-probe';
      const button = document.createElement('button'); button.type = 'button'; button.textContent = '组件图片';
      owner.append(button); document.body.append(owner); owner.showModal();
      window.NewPressComponentMedia(button, () => { window.__unexpectedMediaCallback = true; });
    });
    await until(() => held);
    await page.evaluate(() => document.querySelector('#media-owner-probe').close());
    await page.waitForFunction(() => !document.querySelector('#media-owner-probe button').hasAttribute('aria-busy'));
    await page.evaluate(() => document.querySelector('#media-owner-probe').showModal());
    await release(); mode = ''; await ready(); await closed();
    assert.equal(await page.evaluate(() => window.__unexpectedMediaCallback), undefined);
    await page.evaluate(() => document.querySelector('#media-owner-probe').close());
    check('Closing and reopening the owner dialog invalidates its earlier pending component activation.');

    await visit(); mode = 'hold'; await openBody(); await until(() => held);
    await page.evaluate(() => { const dialog = document.createElement('dialog'); dialog.id = 'other-dialog-probe'; document.body.append(dialog); dialog.showModal(); });
    await release(); mode = ''; await ready(); await closed();
    await page.evaluate(() => document.querySelector('#other-dialog-probe').close());
    check('A different modal opened during download prevents the old media intent from stealing focus.');

    await visit(); const beforeMultiple = uploads.length; await fileEvent('paste', 2);
    await page.waitForFunction(() => document.querySelector('[data-editor-media-error]').textContent.includes('一张图片'));
    assert.equal(uploads.length, beforeMultiple); await close();
    check('Multiple pasted files display the existing warning without uploading.');

    await visit();
    await page.evaluate(() => {
      const button = document.createElement('button'); button.type = 'button'; button.id = 'media-api-probe'; button.textContent = '图片接口'; document.body.append(button);
      window.NewPressComponentMedia(button, (url, item) => { window.__componentMedia = { url, id: item.id, alt: item.alt_text }; });
    });
    await choose(); await apply();
    assert.deepEqual(await page.evaluate(() => window.__componentMedia), { url: image.public_url, id: 7, alt: image.alt_text });
    const beforeField = runtimeDownloads();
    await page.evaluate(() => { void window.NewPressMediaPicker.open(document.querySelector('#media-api-probe'), { title: '字段图片', onSelect(url, item) { window.__fieldMedia = { url, id: item.id }; } }); });
    await choose(); await apply();
    assert.deepEqual(await page.evaluate(() => window.__fieldMedia), { url: image.public_url, id: 7 });
    assert.equal(runtimeDownloads(), beforeField);
    check('Component and field APIs deliver validated metadata and reuse the same runtime.');

    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 900 });
      await page.evaluate(() => { void window.NewPressMediaPicker.open(document.querySelector('#media-api-probe'), { title: '图片按需加载', onSelect() {} }); });
      await choose();
      const geometry = await picker.evaluate(node => { const r = node.getBoundingClientRect(); return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: innerWidth, height: innerHeight }; });
      assert.ok(geometry.left >= -1 && geometry.right <= geometry.width + 1 && geometry.top >= -1 && geometry.bottom <= geometry.height + 1, JSON.stringify(geometry));
      await page.screenshot({ path: resolve(directory, `${variant}-${name}-media-${width}.png`) }); await close();
    }
    check('The media dialog fits desktop and narrow viewports.');

    if (variant === 'generated') for (const file of ['newpress-editor-page.js', 'newpress-editor-media-dialog.js']) {
      const url = await page.locator('script[data-editor-media-loader]').evaluate((entry, name) => name.includes('media-dialog') ? entry.dataset.mediaScript : entry.src, file);
      const response = await context.request.get(new URL(url, fixture.base).href), bytes = await response.body();
      assert.equal(response.status(), 200); assert.deepEqual(bytes, await readFile(resolve('internal/coreui/assets/js', file)));
      assert.ok(response.headers().etag); assert.match(response.headers()['cache-control'], /immutable/);
      assets.push({ file, bytes: bytes.length, sha256: createHash('sha256').update(bytes).digest('hex'), etag: response.headers().etag });
    }
    assert.deepEqual(errors, []); assert.deepEqual(writes, intercepted);
    check('No browser exceptions; all writes were intercepted.' + (variant === 'generated' ? ' Actual generated responses match their cache/version contract.' : ''));
  } catch (error) {
    await page.screenshot({ path: resolve(directory, `${variant}-${name}-media-failure.png`) }).catch(() => {});
    throw error;
  } finally {
    if (held) await held.abort().catch(() => {});
    await writeFile(resolve(directory, `${variant}-${name}-media-loading.json`), JSON.stringify({ evidence: evidence.filter(item => item.browser === name), errors,
      writes, intercepted, downloads, uploads: uploads.map(({ body, ...item }) => ({ bytes: body.length, ...item })), assets }, null, 2) + '\n');
    await context.close(); await browser.close();
  }
}
await writeFile(resolve(directory, `${variant}-media-loading-acceptance.json`), JSON.stringify({ evidence }, null, 2) + '\n');
