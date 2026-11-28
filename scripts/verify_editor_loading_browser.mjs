// Actual Go editor pages; mutations are intercepted and verified by the fixture.
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
if (variant === 'typescript') for (const entry of ['editor-page', 'editor-insert-tools']) {
  scripts.set(`newpress-${entry}.js`, (await build({ entryPoints: [`frontend/core/${entry}.ts`], bundle: true, write: false,
    format: 'iife', platform: 'browser', target: ['chrome100', 'firefox110', 'safari16'] })).outputFiles[0].text);
}
const until = async predicate => {
  const deadline = Date.now() + 12000;
  while (!predicate()) {
    if (Date.now() > deadline) throw Error('Expected held request did not arrive');
    await new Promise(resolve => setTimeout(resolve, 20));
  }
};

for (const name of selected) {
  const browser = await engines[name].launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: 'reduce' });
  const page = await context.newPage(); page.setDefaultTimeout(12000);
  const errors = [], writes = [], intercepted = [], downloads = [], assets = [];
  let mode = '', held = null, draftBody = null, listRequests = 0;
  const check = label => { evidence.push({ browser: name, check: label }); console.log(`${name}: ${label}`); };
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  page.on('pageerror', error => errors.push(error.message));
  page.on('dialog', dialog => dialog.accept());
  page.on('request', request => {
    if (request.url().includes('/js/')) downloads.push(new URL(request.url()).pathname.split('/').pop());
    if (!['GET', 'HEAD'].includes(request.method())) writes.push({ method: request.method(), url: request.url() });
  });
  await page.addInitScript(() => {
    const original = window.setTimeout.bind(window);
    window.setTimeout = (callback, delay, ...args) => original(callback, window.__shortToolTimeout && delay === 15000 ? 100 : delay, ...args);
  });
  await context.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url()), file = url.pathname.split('/').pop();
    if (url.origin !== fixture.base) return route.abort('blockedbyclient');
    const serve = () => scripts.has(file) ? route.fulfill({ contentType: 'text/javascript', body: scripts.get(file) }) : route.continue();
    if (file === 'newpress-editor-insert-tools.js') {
      if (mode === 'fail') return route.abort('failed');
      if (mode === 'empty') return route.fulfill({ contentType: 'text/javascript', body: '/* failed initialization */' });
      if (mode === 'hold') { held = serve; return; }
    }
    if (scripts.has(file)) return serve();
    if (url.pathname === '/api/v1/admin/updates') return json(route, { error: { message: 'unavailable in this fixture' } }, 503);
    if (url.pathname === '/api/v1/admin/article-widgets') {
      listRequests++;
      return json(route, { items: [{ id: 7, title: '按需文章卡片', permalink: '/posts/loading-check', excerpt: '选区核对', author_name: '作者', category: '分类', cover_url: '' }], pagination: { page: 1, pages: 1, total: 1 } });
    }
    if (url.pathname === '/api/v1/admin/snippets') return json(route, { items: [{ id: 9, version: 2, current_version: 1, title: '按需共享片段', visibility: 'shared' }], page: 1, pages: 1 });
    if (url.pathname === '/api/v1/admin/snippets/9') return json(route, { id: 9, version: 2, current_version: 1, visibility: 'shared' });
    if (!['GET', 'HEAD'].includes(request.method())) {
      intercepted.push({ method: request.method(), url: request.url() });
      if (url.pathname.endsWith('/snippet-draft')) {
        draftBody = request.postDataJSON();
        return json(route, { id: 99, version: 1, request_key: draftBody.request_key, replayed: true, editor_url: '/admin/content/99/edit/' }, 201);
      }
      return route.fulfill({ status: 204 });
    }
    return route.fallback();
  });
  const visit = async () => {
    mode = ''; held = null;
    await page.goto(fixture.base + fixture.paths.editor); await page.waitForLoadState('networkidle');
    await page.locator('[data-composer].is-document-editor .ProseMirror').waitFor();
  };
  const openInsert = async kind => {
    const menu = page.locator('.editor-insert-menu');
    if (!await menu.evaluate(node => node.open)) await menu.locator(':scope > summary').click();
    await menu.locator(`[data-editor-${kind}-open]`).click();
  };
  const source = page.locator('[data-content-source]');
  const toolDownloads = () => downloads.filter(file => file === 'newpress-editor-insert-tools.js').length;
  const release = async () => { const fulfill = held; held = null; await fulfill(); };
  try {
    await page.goto(fixture.base + fixture.login); await visit();
    const original = await source.inputValue();
    assert.equal(toolDownloads(), 0);
    assert.equal(await page.locator('script[src*="chunnel-editor-reuse.js"], script[src*="newpress-editor-snippets.js"], script[src*="chunnel-editor-widgets.js"]').count(), 0);
    await page.locator('.ProseMirror').click(); await page.keyboard.press('ControlOrMeta+End');
    await page.keyboard.type('常规编辑'); await page.locator('[data-composer-undo]').click();
    assert.equal(await source.inputValue(), original); assert.equal(toolDownloads(), 0);
    check('The default continuous editor supports typing and undo before any optional insertion script is downloaded.');

    mode = 'fail'; await openInsert('widget');
    await page.waitForFunction(() => document.querySelector('[data-composer-feedback]').textContent.includes('加载失败'));
    assert.equal(await page.locator('[data-editor-widget-dialog]').evaluate(node => node.open), false);
    assert.equal(await source.inputValue(), original);
    check('A real script network failure leaves the document intact and offers retry.');

    mode = 'hold';
    await page.locator('.ProseMirror > p').last().click();
    await page.waitForFunction(() => window.ChunnelBlockEditor.capture().start === document.querySelector('[data-content-source]').value.length);
    const captured = await page.evaluate(() => window.ChunnelBlockEditor.capture());
    await openInsert('widget'); await until(() => held);
    await page.locator('.ProseMirror > h2').first().click();
    await page.waitForFunction(start => window.ChunnelBlockEditor.capture().start !== start, captured.start);
    await page.evaluate(() => {
      const editor = window.ChunnelBlockEditor, insert = editor.insert;
      editor.insert = (markup, selection) => { window.__insertedSelection = selection; return insert(markup, selection); };
    });
    await release(); mode = '';
    await page.locator('[data-article-choice="7"]').click(); await page.locator('[data-editor-widget-insert]').click();
    assert.deepEqual(await page.evaluate(() => window.__insertedSelection), captured);
    assert.match(await source.inputValue(), /data-np-post="7"/);
    await page.locator('[data-composer-undo]').click(); assert.equal(await source.inputValue(), original);
    check('First successful activation retains the selection from before downloading even when the caret moves meanwhile; insertion is undoable.');

    const downloaded = toolDownloads();
    await openInsert('snippets'); await page.getByRole('button', { name: '插入「按需共享片段」的固定版本引用' }).click();
    await page.waitForFunction(() => !document.querySelector('[data-editor-snippets-dialog]').open);
    assert.match(await source.inputValue(), /data-np-snippet="9" data-np-snippet-version="1"/);
    await page.locator('[data-composer-undo]').click(); assert.equal(await source.inputValue(), original);
    await openInsert('reuse'); await page.locator('[data-editor-reuse-results] button').first().click();
    await page.waitForFunction(() => !document.querySelector('[data-editor-reuse-dialog]').open);
    assert.ok((await source.inputValue()).length > original.length);
    await page.locator('[data-composer-undo]').click(); assert.equal(await source.inputValue(), original);
    assert.equal(toolDownloads(), downloaded);
    check('Shared snippets and real Go content reuse use the already loaded bundle and preserve undo.');

    for (const invalidation of ['body', 'readonly', 'fieldset']) {
      await visit(); mode = 'hold'; await openInsert('widget'); await until(() => held);
      await page.evaluate(kind => {
        const source = document.querySelector('[data-content-source]');
        if (kind === 'body') window.ChunnelBlockEditor.reset('<p>下载期间的新输入</p>');
        else if (kind === 'readonly') source.readOnly = true;
        else { const fieldset = document.createElement('fieldset'); source.before(fieldset); fieldset.append(source); fieldset.disabled = true; }
      }, invalidation);
      await release(); mode = '';
      await page.waitForFunction(() => document.querySelector('[data-composer-feedback]').textContent.includes('已变化'));
      assert.equal(await page.locator('[data-editor-widget-dialog]').evaluate(node => node.open), false);
      assert.equal(await source.inputValue(), invalidation === 'body' ? '<p>下载期间的新输入</p>' : original);
      check(`A delayed script cannot reopen an insertion after ${invalidation} changes.`);
    }

    await visit(); mode = 'empty'; await openInsert('widget');
    await page.waitForFunction(() => document.querySelector('[data-composer-feedback]').textContent.includes('初始化失败'));
    mode = ''; await openInsert('widget'); await page.locator('[data-article-choice="7"]').waitFor();
    await page.locator('[data-editor-widget-close]').click();
    check('A downloaded script that fails to initialize can be retried successfully.');

    await visit(); mode = 'hold'; await openInsert('widget'); await until(() => held);
    await page.keyboard.press('Escape'); await release(); mode = '';
    await page.waitForFunction(() => window.NewPressEditorTools?.has(document.querySelector('[data-editor-widget-open]')));
    assert.equal(await page.locator('[data-editor-widget-dialog]').evaluate(node => node.open), false);
    await openInsert('widget'); await page.locator('[data-article-choice="7"]').waitFor();
    await page.locator('[data-editor-widget-close]').click();
    check('Escape cancels a pending activation; the next click reuses the completed download.');

    await visit(); mode = 'hold';
    await page.evaluate(() => { window.__shortToolTimeout = true; });
    await openInsert('widget'); await until(() => held);
    await page.waitForFunction(() => document.querySelector('[data-composer-feedback]').textContent.includes('加载失败'));
    await page.evaluate(() => { window.__shortToolTimeout = false; });
    await release(); mode = '';
    await page.waitForFunction(() => window.NewPressEditorTools?.has(document.querySelector('[data-editor-widget-open]')));
    assert.equal(await page.locator('[data-editor-widget-dialog]').evaluate(node => node.open), false);
    const beforeList = listRequests;
    await openInsert('widget'); await page.locator('[data-article-choice="7"]').waitFor();
    assert.equal(listRequests, beforeList + 1); await page.locator('[data-editor-widget-close]').click();
    check('A timed-out script that arrives late does not open an old intent or duplicate handlers on the next click.');

    await visit(); mode = 'hold'; await openInsert('widget'); await until(() => held);
    await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })));
    await release(); mode = '';
    await page.waitForFunction(() => window.NewPressEditorTools?.has(document.querySelector('[data-editor-widget-open]')));
    assert.equal(await page.locator('[data-editor-widget-dialog]').evaluate(node => node.open), false);
    await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })));
    const beforeRestore = toolDownloads();
    await openInsert('widget'); await page.locator('[data-article-choice="7"]').waitFor();
    assert.equal(toolDownloads(), beforeRestore); await page.locator('[data-editor-widget-close]').click();
    check('Page suspension invalidates an unfinished activation; restoration requires a new click and reuses the loaded script.');

    await visit();
    const pending = { post_id: fixture.editor_post_id, version: fixture.editor_version, digest: 'a'.repeat(64), request_key: 'b'.repeat(64) };
    await page.evaluate(request => {
      const form = document.querySelector('[data-content-editor]'), user = Number(form.dataset.editorUserId || 0);
      const identity = user > 0 ? 'user:' + user : 'legacy:' + form.dataset.editorLogin;
      sessionStorage.setItem(`newpress:snippet-draft:${identity}:${request.post_id}`, JSON.stringify(request));
    }, pending);
    const beforeDraft = toolDownloads();
    await visit(); assert.equal(toolDownloads(), beforeDraft);
    assert.equal(await page.evaluate(() => !window.dispatchEvent(new Event('beforeunload', { cancelable: true }))), true);
    await page.locator('.editor-page-more > summary').click(); await page.locator('[data-snippet-draft-open]').click();
    await page.waitForFunction(() => document.querySelector('[data-snippet-draft-confirm]').textContent.includes('核对'));
    assert.equal(await page.locator('[data-snippet-draft-preview]').isDisabled(), true);
    await page.locator('[data-snippet-draft-confirm]').click();
    await page.locator('[data-snippet-draft-result]').waitFor();
    assert.deepEqual(draftBody, pending); assert.equal(await source.inputValue(), original);
    assert.equal(await page.locator('[data-snippet-draft-result]').getAttribute('href'), '/admin/content/99/edit/');
    await page.locator('[data-snippet-draft-close]').click();
    check('An uncertain independent-draft request survives refresh before loading and resumes with the same identity after explicit activation.');

    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 900 }); await openInsert('widget');
      await page.locator('[data-article-choice="7"]').waitFor();
      const geometry = await page.locator('[data-editor-widget-dialog]').evaluate(node => { const r = node.getBoundingClientRect(); return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: innerWidth, height: innerHeight }; });
      assert.ok(geometry.left >= -1 && geometry.right <= geometry.width + 1 && geometry.top >= -1 && geometry.bottom <= geometry.height + 1, JSON.stringify(geometry));
      await page.screenshot({ path: resolve(directory, `${variant}-${name}-tools-${width}.png`) });
      await page.locator('[data-editor-widget-close]').click();
    }
    check('Loaded dialogs fit desktop and narrow screens.');

    if (variant === 'generated') for (const file of ['newpress-editor-page.js', 'newpress-editor-insert-tools.js']) {
      const url = await page.locator('script[data-editor-tools-loader]').evaluate((entry, name) => name.includes('insert-tools') ? entry.dataset.editorToolsScript : entry.src, file);
      const response = await context.request.get(new URL(url, fixture.base).href), bytes = await response.body();
      assert.equal(response.status(), 200); assert.deepEqual(bytes, await readFile(resolve('internal/coreui/assets/js', file)));
      assert.ok(response.headers().etag); assert.match(response.headers()['cache-control'], /immutable/);
      assets.push({ file, bytes: bytes.length, sha256: createHash('sha256').update(bytes).digest('hex'), etag: response.headers().etag });
    }
    assert.deepEqual(errors, []); assert.deepEqual(writes, intercepted);
    check('No browser exceptions; all writes were intercepted.' + (variant === 'generated' ? ' Both generated resources match their actual HTTP cache/version contract.' : ''));
  } catch (error) {
    await page.screenshot({ path: resolve(directory, `${variant}-${name}-failure.png`) }).catch(() => {});
    throw error;
  } finally {
    await writeFile(resolve(directory, `${variant}-${name}-loading.json`), JSON.stringify({ evidence: evidence.filter(item => item.browser === name), errors, writes, intercepted, downloads, assets }, null, 2) + '\n');
    await context.close(); await browser.close();
  }
}
await writeFile(resolve(directory, `${variant}-loading-acceptance.json`), JSON.stringify({ evidence }, null, 2) + '\n');
