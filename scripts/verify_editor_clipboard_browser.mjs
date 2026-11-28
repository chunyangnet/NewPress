// Opt-in Windows system clipboard acceptance on the isolated real Go editor.
// Two separate, visible browser processes exchange actual clipboard formats.
// No ClipboardEvent is synthesized and every attempted business write is blocked.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium } from 'playwright';

assert.equal(process.platform, 'win32', 'This acceptance requires the Windows system clipboard.');
const directory = resolve(process.argv[2]);
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const evidence = [], errors = [], writes = [], clipboardEvents = [];
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==', 'base64');
const image = { id: 7, status: 'active', kind: 'image', width: 1, height: 1, size_bytes: png.length,
  title: '系统剪贴板图片', filename: 'clipboard.png', public_url: '/media/7/clipboard.png', alt_text: '', caption: '' };
let imageUploads = 0;
let backupReady = false, copied = false, restored = false, externalClipboardChange = false;
const producerBrowser = await chromium.launch({ headless: false });
let editorBrowser, producerContext, editorContext, producer, editor;
const check = text => { evidence.push(text); console.log(text); };
try {
  editorBrowser = await chromium.launch({ channel: 'msedge', headless: false });
  producerContext = await producerBrowser.newContext({ viewport: { width: 1000, height: 760 } });
  editorContext = await editorBrowser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: 'reduce' });
  for (const context of [producerContext, editorContext]) {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: fixture.base });
    await context.route('**/*', route => {
      const request = route.request(), url = new URL(request.url());
      if (url.origin !== fixture.base) return route.abort('blockedbyclient');
      if (url.pathname === '/__native_clipboard_source/') return route.fulfill({ contentType: 'text/html; charset=utf-8', body:
        '<!doctype html><html lang="zh-CN"><title>NewPress 独立剪贴板验收源</title><main><h1>系统剪贴板验收</h1><p>本窗口仅提供测试文字，完成后自动关闭。</p><textarea id="plain" aria-label="复制测试文字">跨进程剪贴板：中文、emoji 🐳 与第二行\n原文完整保留。</textarea><div id="rich" tabindex="0"><p>富文本剪贴板 <strong>加粗中文</strong> <em>斜体</em>。</p><ul><li>第一项</li><li>第二项</li></ul></div></main></html>' });
      if (url.pathname === '/api/v1/admin/updates') return route.fulfill({ status: 503, contentType: 'application/json', body: '{}' });
      if (url.pathname === image.public_url) return route.fulfill({ contentType: 'image/png', body: png });
      if (url.pathname === '/api/v1/admin/media/7') return route.fulfill({ contentType: 'application/json', body: JSON.stringify(image) });
      if (url.pathname === '/api/v1/admin/media') {
        if (request.method() === 'POST') {
          writes.push({ method: request.method(), path: url.pathname }); imageUploads++;
          assert.ok(request.postDataBuffer().includes(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])));
          assert.ok(request.headers()['x-csrf-token']);
          return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ item: image, duplicate: true }) });
        }
        return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ items: [image], pagination: { page: 1, pages: 1, total: 1 }, can_upload: true, max_upload_bytes: 1048576 }) });
      }
      if (['GET', 'HEAD'].includes(request.method())) return route.fallback();
      writes.push({ method: request.method(), path: url.pathname });
      if (url.pathname === `/admin/content/${fixture.editor_post_id}/autosave/`) {
        const form = new URLSearchParams(request.postData() || '');
        return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ ...fixture.autosave_receipt, source_version: Number(form.get('version')) }) });
      }
      return route.fulfill({ status: 204 });
    });
  }
  producer = await producerContext.newPage(); editor = await editorContext.newPage();
  for (const page of [producer, editor]) page.on('pageerror', error => errors.push(error.message));
  editor.on('dialog', dialog => dialog.accept());
  await producer.goto(fixture.base + '/__native_clipboard_source/');
  await editorContext.request.get(fixture.base + fixture.login);
  await editor.goto(fixture.base + fixture.paths.editor, { waitUntil: 'load' });
  await editor.locator('.ProseMirror').waitFor();
  const original = await editor.locator('[data-content-source]').inputValue();
  await editor.evaluate(() => {
    window.__nativeClipboardEvents = [];
    document.addEventListener('paste', event => {
      window.__nativeClipboardEvents.push({ type: event.type, trusted: event.isTrusted, formats: [...event.clipboardData.types],
        files: [...event.clipboardData.files].map(file => ({ type: file.type, bytes: file.size })) });
    }, true);
  });

  await producer.bringToFront();
  await producer.locator('#plain').click();
  // Keep the original clipboard data in this browser's memory, never in logs
  // or evidence files. Materialize every Blob before the first test copy.
  await producer.evaluate(async () => {
    window.__clipboardSignature = async () => {
      const signatures = [];
      for (const item of await navigator.clipboard.read()) {
        const formats = [];
        for (const type of item.types) {
          const bytes = await (await item.getType(type)).arrayBuffer();
          formats.push({ type, hash: Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', bytes))) });
        }
        signatures.push(formats);
      }
      return JSON.stringify(signatures);
    };
    const before = await navigator.clipboard.read();
    window.__originalClipboard = await Promise.all(before.map(async item => new ClipboardItem(Object.fromEntries(
      await Promise.all(item.types.map(async type => [type, new Blob([await (await item.getType(type)).arrayBuffer()], { type })]))))));
  });
  backupReady = true;

  const copyPlain = async () => {
    await producer.bringToFront(); await producer.locator('#plain').focus();
    await producer.keyboard.press('Control+A'); await producer.keyboard.press('Control+C');
    await producer.evaluate(async () => { window.__lastTestClipboard = await window.__clipboardSignature(); });
    copied = true;
  };
  const plain = await producer.locator('#plain').inputValue();
  await copyPlain();
  await editor.bringToFront();
  assert.equal((await editor.evaluate(() => navigator.clipboard.readText())).replaceAll('\r\n', '\n'), plain);
  await editor.locator('.ProseMirror').click(); await editor.keyboard.press('Control+End'); await editor.keyboard.press('Control+V');
  await editor.waitForFunction(() => document.querySelector('[data-content-source]').value.includes('跨进程剪贴板'));
  assert.match(await editor.locator('.ProseMirror').textContent(), /中文、emoji 🐳/);
  assert.match(await editor.locator('[data-content-source]').inputValue(), /原文完整保留/);
  await editor.locator('[data-composer-undo]').click();
  assert.equal(await editor.locator('[data-content-source]').inputValue(), original);
  await editor.locator('[data-composer-redo]').click();
  assert.match(await editor.locator('[data-content-source]').inputValue(), /跨进程剪贴板/);
  await editor.locator('[data-composer-undo]').click();
  assert.equal(await editor.locator('[data-content-source]').inputValue(), original);
  check('Separate Chromium and Edge processes transfer real Windows clipboard Unicode text; paste, undo and redo preserve the original article.');

  await producer.bringToFront(); await producer.locator('#rich').focus();
  await producer.locator('#rich').evaluate(node => { const range = document.createRange(); range.selectNodeContents(node); const selection = getSelection(); selection.removeAllRanges(); selection.addRange(range); });
  await producer.keyboard.press('Control+C');
  await producer.evaluate(async () => { window.__lastTestClipboard = await window.__clipboardSignature(); });
  await editor.bringToFront();
  const formats = await editor.evaluate(async () => (await navigator.clipboard.read()).flatMap(item => item.types));
  assert.ok(formats.includes('text/plain') && formats.includes('text/html'));
  await editor.locator('.ProseMirror').click(); await editor.keyboard.press('Control+End'); await editor.keyboard.press('Control+V');
  await editor.waitForFunction(() => document.querySelector('[data-content-source]').value.includes('富文本剪贴板'));
  assert.equal(await editor.locator('.ProseMirror strong').filter({ hasText: '加粗中文' }).count(), 1);
  assert.equal(await editor.locator('.ProseMirror em').filter({ hasText: '斜体' }).count(), 1);
  assert.equal(await editor.locator('.ProseMirror li').filter({ hasText: '第二项' }).count(), 1);
  await editor.locator('[data-composer-undo]').click();
  assert.equal(await editor.locator('[data-content-source]').inputValue(), original);
  check('Cross-process clipboard HTML preserves emphasis and list items; one undo restores every original source byte.');

  await copyPlain(); await editor.bringToFront();
  await editor.locator('.document-block-menu > summary').click();
  await editor.locator('[data-composer-mode="source"]').click();
  await editor.locator('[data-content-source]').focus(); await editor.keyboard.press('Control+End'); await editor.keyboard.press('Control+V');
  await editor.waitForFunction(() => document.querySelector('[data-content-source]').value.includes('跨进程剪贴板'));
  assert.equal(await editor.locator('[data-content-source]').inputValue(), original + plain);
  await editor.locator('.document-block-menu > summary').click();
  await editor.locator('[data-composer-mode="blocks"]').click();
  await editor.locator('[data-composer-undo]').click();
  assert.equal(await editor.locator('[data-content-source]').inputValue(), original);
  check('Native textarea clipboard paste synchronizes with the continuous editor and can be undone after a mode switch.');

  await producer.bringToFront();
  await producer.evaluate(async encoded => {
    const bytes = Uint8Array.from(atob(encoded), char => char.charCodeAt(0));
    await navigator.clipboard.write([new ClipboardItem({ 'image/png': new Blob([bytes], { type: 'image/png' }) })]);
    window.__lastTestClipboard = await window.__clipboardSignature();
  }, png.toString('base64'));
  await editor.bringToFront();
  await editor.locator('.ProseMirror').click(); await editor.keyboard.press('Control+End'); await editor.keyboard.press('Control+V');
  await editor.locator('[data-editor-media-dialog][open]').waitFor();
  await editor.waitForFunction(() => !document.querySelector('[data-editor-media-apply]').disabled);
  assert.equal(imageUploads, 1);
  await editor.locator('[data-editor-media-insert] [name="alt_text"]').fill('系统剪贴板图片');
  await editor.locator('[data-editor-media-apply]').click();
  await editor.waitForFunction(url => document.querySelector('[data-content-source]').value.includes(url), image.public_url);
  assert.equal(await editor.locator('.ProseMirror img[src="' + image.public_url + '"]').count(), 1);
  await editor.locator('[data-composer-undo]').click();
  assert.equal(await editor.locator('[data-content-source]').inputValue(), original);
  check('A real Windows clipboard PNG arrives as a browser File, loads the media tool once, and inserts with an intercepted upload; undo restores the article.');

  clipboardEvents.push(...await editor.evaluate(() => window.__nativeClipboardEvents));
  assert.equal(clipboardEvents.length, 4);
  assert.ok(clipboardEvents.every(event => event.trusted));
  assert.equal(clipboardEvents.at(-1).files.length, 1);
  assert.equal(clipboardEvents.at(-1).files[0].type, 'image/png');
  assert.deepEqual(errors, []);
  await editor.screenshot({ path: resolve(directory, 'native-clipboard-edge.png'), fullPage: true });
  check('All four paste events are browser-trusted events; no synthetic ClipboardEvent or business write reaches the server.');
} finally {
  try {
    if (backupReady && copied && producer && !producer.isClosed()) {
      await producer.bringToFront();
      const outcome = await producer.evaluate(async () => {
        if (await window.__clipboardSignature() !== window.__lastTestClipboard) return 'external-change';
        if (window.__originalClipboard.length) await navigator.clipboard.write(window.__originalClipboard);
        else await navigator.clipboard.writeText('');
        delete window.__originalClipboard; delete window.__lastTestClipboard;
        return 'restored';
      });
      restored = outcome === 'restored'; externalClipboardChange = outcome === 'external-change';
    }
  } finally {
    await writeFile(resolve(directory, 'native-clipboard-acceptance.json'), JSON.stringify({ evidence, errors, writes,
      clipboard_events: clipboardEvents, image_uploads_intercepted: imageUploads, original_clipboard_restored: restored, external_clipboard_change: externalClipboardChange,
      browsers: { producer: producerBrowser.version(), editor: editorBrowser?.version() }, platform: process.platform,
      scope: 'Actual Windows browser clipboard via separate headed processes; not native IME or actual Safari acceptance.' }, null, 2) + '\n');
    await editorContext?.close(); await producerContext?.close(); await editorBrowser?.close(); await producerBrowser.close();
  }
}
