// A retry must finish while the original download is still held open. Ordinary
// load/error simulations miss browser request coalescing and late execution.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';
import { build } from 'esbuild';

const directory = resolve(process.argv[2]);
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const variant = process.argv[3] || 'generated';
const engines = { chromium, firefox, webkit };
const selected = process.argv[4] ? [process.argv[4]] : Object.keys(engines);
const scripts = new Map(), evidence = [], failures = [];
if (variant === 'typescript') for (const entry of ['editor-page', 'editor-media-dialog', 'editor-insert-tools']) {
  scripts.set(`newpress-${entry}.js`, (await build({ entryPoints: [`frontend/core/${entry}.ts`], bundle: true, write: false,
    format: 'iife', platform: 'browser', target: ['chrome100', 'firefox110', 'safari16'] })).outputFiles[0].text);
}
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==', 'base64');
const media = { id: 7, status: 'active', kind: 'image', width: 1, height: 1, size_bytes: png.length,
  title: '重试图片', filename: 'retry.png', public_url: '/media/7/retry.png', alt_text: '重试图片', caption: '' };

for (const name of selected) {
  const browser = await engines[name].launch({ headless: true });
  try { for (const kind of ['media', 'insert']) {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: 'reduce' });
    const page = await context.newPage(); page.setDefaultTimeout(7000);
    const target = kind === 'media' ? 'newpress-editor-media-dialog.js' : 'newpress-editor-insert-tools.js';
    const downloads = [], uploads = [], errors = [], writes = [], intercepted = [];
    let held = null, listRequests = 0;
    const check = text => { evidence.push({ browser: name, kind, check: text }); console.log(`${name}/${kind}: ${text}`); };
    page.on('pageerror', error => errors.push(error.message));
    page.on('dialog', dialog => dialog.accept());
    page.on('request', request => { if (!['GET', 'HEAD'].includes(request.method())) writes.push(request.url()); });
    await page.addInitScript(() => {
      const timeout = window.setTimeout.bind(window);
      window.setTimeout = (callback, delay, ...args) => timeout(callback, window.__shortRetryTimeout && delay === 15000 ? 200 : delay, ...args);
      const fetch = window.fetch.bind(window);
      window.__retryUploadedFiles = [];
      window.fetch = async (input, init) => {
        const file = init?.body instanceof FormData ? init.body.get('file') : null;
        if (file instanceof File) window.__retryUploadedFiles.push({ original: file === window.__retryOriginalFile,
          name: file.name, type: file.type, bytes: Array.from(new Uint8Array(await file.arrayBuffer())) });
        return fetch(input, init);
      };
    });
    await context.route('**/*', async route => {
      const request = route.request(), url = new URL(request.url()), file = url.pathname.split('/').pop();
      if (url.origin !== fixture.base) return route.abort('blockedbyclient');
      const json = (value, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(value) });
      const serve = () => scripts.has(file) ? route.fulfill({ contentType: 'text/javascript', body: scripts.get(file) }) : route.continue();
      if (file === target) {
        downloads.push(url.href);
        if (downloads.length === 1) { held = { serve, abort: () => route.abort() }; return; }
        return serve();
      }
      if (scripts.has(file)) return serve();
      if (!['GET', 'HEAD'].includes(request.method())) {
        intercepted.push(request.url());
        if (url.pathname === '/api/v1/admin/media') {
          uploads.push({ png: request.postDataBuffer().includes(png), file: request.postDataBuffer().includes(Buffer.from('retry-original.png')) });
          return json({ item: media, duplicate: true });
        }
        return route.fulfill({ status: 204 });
      }
      if (url.pathname === '/api/v1/admin/updates') return json({ error: { message: 'unavailable in fixture' } }, 503);
      if (url.pathname === '/api/v1/admin/media') return json({ items: [media], pagination: { page: 1, pages: 1, total: 1 }, can_upload: true, max_upload_bytes: 1048576 });
      if (url.pathname === '/api/v1/admin/media/7') return json(media);
      if (url.pathname === media.public_url) return route.fulfill({ contentType: 'image/png', body: png });
      if (url.pathname === '/api/v1/admin/article-widgets') {
        listRequests++;
        return json({ items: [{ id: 7, title: '重试卡片', permalink: '/posts/retry', excerpt: '选区核对', author_name: '作者', category: '分类', cover_url: '' }], pagination: { page: 1, pages: 1, total: 1 } });
      }
      return route.fallback();
    });
    const launch = async () => {
      const menu = page.locator('.editor-insert-menu');
      if (!await menu.evaluate(node => node.open)) await menu.locator(':scope > summary').click();
      await menu.locator(kind === 'media' ? '[data-editor-media-open="body"]' : '[data-editor-widget-open]').click();
    };
    try {
      await page.goto(fixture.base + fixture.login);
      await page.goto(fixture.base + fixture.paths.editor); await page.locator('.ProseMirror').waitFor();
      const source = page.locator('[data-content-source]'), original = await source.inputValue();
      assert.equal(downloads.length, 0);
      await page.locator('.ProseMirror > p').last().click();
      await page.waitForFunction(() => window.ChunnelBlockEditor.capture().start === document.querySelector('[data-content-source]').value.length);
      await page.evaluate(() => { window.__shortRetryTimeout = true; });
      if (kind === 'media') await page.evaluate(encoded => {
        const transfer = new DataTransfer();
        transfer.items.add(new File([Uint8Array.from(atob(encoded), c => c.charCodeAt(0))], 'retry-original.png', { type: 'image/png' }));
        window.__retryOriginalFile = transfer.files[0];
        const event = new ClipboardEvent('paste', { bubbles: true, cancelable: true, clipboardData: transfer });
        if (!event.clipboardData?.files.length) Object.defineProperty(event, 'clipboardData', { value: transfer });
        document.querySelector('.ProseMirror').dispatchEvent(event);
      }, png.toString('base64'));
      else await launch();
      await page.waitForFunction(kind => document.querySelector(kind === 'media' ? '.admin-media-field-status' : '[data-composer-feedback]')?.textContent.includes('加载失败'), kind);
      assert.ok(held); assert.equal(downloads.length, 1); assert.equal(await source.inputValue(), original);
      await page.evaluate(() => { window.__shortRetryTimeout = false; });
      if (kind === 'media') await page.getByRole('button', { name: '重试加载图片工具', exact: true }).click();
      else await launch();
      const dialog = page.locator(kind === 'media' ? '[data-editor-media-dialog]' : '[data-editor-widget-dialog]');
      await page.waitForFunction(selector => document.querySelector(selector)?.open, kind === 'media' ? '[data-editor-media-dialog]' : '[data-editor-widget-dialog]');
      assert.equal(downloads.length, 2); assert.notEqual(downloads[0], downloads[1]);
      check('Retry opens before the first request is released, using a distinct URL.');
      if (kind === 'media') {
        await page.waitForFunction(() => !document.querySelector('[data-editor-media-apply]').disabled);
        assert.equal(uploads.length, 1); assert.equal(uploads[0].file, true);
        // The WebKit protocol returns multipart postData as text. Check the
        // actual File sent to fetch on every engine, plus intact wire bytes
        // where Playwright exposes them without a text conversion.
        assert.deepEqual(await page.evaluate(() => window.__retryUploadedFiles), [{ original: true,
          name: 'retry-original.png', type: 'image/png', bytes: Array.from(png) }]);
        if (name !== 'webkit') assert.equal(uploads[0].png, true);
      } else await page.locator('[data-article-choice="7"]').click();
      const openedLists = listRequests;
      const loaded = page.waitForEvent('response', response => response.url() === downloads[0]);
      await held.serve(); held = null; await (await loaded).finished();
      // Let the late script's load listeners and any accidentally duplicated
      // handlers reach the event loop before checking the original interaction.
      await page.waitForTimeout(150);
      assert.equal(await dialog.evaluate(node => node.open), true);
      assert.equal(listRequests, openedLists); assert.equal(uploads.length, kind === 'media' ? 1 : 0);
      const apply = page.locator(kind === 'media' ? '[data-editor-media-apply]' : '[data-editor-widget-insert]');
      await apply.click();
      await page.waitForFunction(selector => !document.querySelector(selector).open, kind === 'media' ? '[data-editor-media-dialog]' : '[data-editor-widget-dialog]');
      const inserted = await source.inputValue();
      assert.equal((inserted.match(kind === 'media' ? /<img\b/g : /data-np-post=/g) || []).length, 1);
      await page.locator('[data-composer-undo]').click(); assert.equal(await source.inputValue(), original);
      check('The late script leaves the active dialog intact; one insertion has one undo and at most one upload.');
      await launch(); await page.waitForFunction(selector => document.querySelector(selector).open, kind === 'media' ? '[data-editor-media-dialog]' : '[data-editor-widget-dialog]');
      if (kind === 'insert') await page.locator('[data-article-choice="7"]').waitFor();
      assert.equal(downloads.length, 2); assert.equal(uploads.length, kind === 'media' ? 1 : 0);
      if (kind === 'insert') assert.equal(listRequests, openedLists + 1);
      assert.deepEqual(errors, []); assert.deepEqual(writes, intercepted);
      check('Reopening reuses the runtime without repeating the old intent; all writes were intercepted.');
    } catch (error) {
      failures.push({ browser: name, kind, message: error.message });
      await page.screenshot({ path: resolve(directory, `${variant}-${name}-${kind}-retry-failure.png`) }).catch(() => {});
    } finally {
      if (held) await held.abort().catch(() => {});
      await writeFile(resolve(directory, `${variant}-${name}-${kind}-retry.json`), JSON.stringify({ downloads, uploads, errors, writes, intercepted }, null, 2) + '\n');
      await context.close();
    }
  } } finally { await browser.close(); }
}
await writeFile(resolve(directory, `${variant}-script-retry-acceptance.json`), JSON.stringify({ evidence, failures }, null, 2) + '\n');
assert.deepEqual(failures, []);
