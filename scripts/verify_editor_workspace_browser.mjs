// Real Go editor acceptance. All browser mutations are intercepted and the
// fixture verifies that the persisted post and revisions remain unchanged.
import assert from 'node:assert/strict';
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';
import { build } from 'esbuild';

const directory = resolve(process.argv[2]);
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const engines = { chromium, firefox, webkit };
const variant = process.argv[3] || 'generated';
const selected = process.argv[4] ? [process.argv[4]] : Object.keys(engines);
const evidence = [];
const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
const scripts = new Map();
if (variant === 'typescript') for (const entry of ['editor-page']) {
  scripts.set(`newpress-${entry}.js`, (await build({entryPoints: [`frontend/core/${entry}.ts`], bundle: true, write: false,
    platform: 'browser', format: 'iife', target: ['chrome100', 'firefox110', 'safari16']})).outputFiles[0].text);
}
const until = async predicate => {
  const end = Date.now() + 10000;
  while (!predicate()) { if (Date.now() > end) throw new Error('Expected request did not arrive'); await wait(20); }
};

for (const name of selected) {
  const browser = await engines[name].launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: 'reduce' });
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  const errors = [], writes = [], requests = [], scriptRequests = [];
  let initialScripts = [];
  let mode = '', heldSession, holdSession = false;
  const basePath = `/admin/content/${fixture.editor_post_id}`;
  const editRequests = () => requests.filter(request => request.path === basePath + '/edit/');
  const autosaves = () => requests.filter(request => request.path === basePath + '/autosave/');
  page.on('dialog', dialog => dialog.accept());
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => {
    if (!['GET', 'HEAD'].includes(request.method())) writes.push({ method: request.method(), url: request.url() });
    if (request.resourceType() === 'script') scriptRequests.push(new URL(request.url()).pathname.split('/').pop());
  });
  await context.route('**/*', async route => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.origin !== fixture.base) return route.abort('blockedbyclient');
    if (url.pathname === '/api/v1/admin/updates') return route.fulfill({ status: 503, contentType: 'application/json', body: '{"error":{"message":"unavailable in this fixture"}}' });
    if (scripts.has(url.pathname.split('/').pop())) return route.fulfill({contentType: 'text/javascript', body: scripts.get(url.pathname.split('/').pop())});
    if (url.pathname === '/api/v1/admin/session') {
      const response = await route.fetch(), data = await response.json();
      if (mode === 'other-account') data.user_id += 100;
      if (holdSession) { heldSession = () => route.fulfill({contentType: 'application/json', body: JSON.stringify(data)}); return; }
      return route.fulfill({response, json: data});
    }
    if (['GET', 'HEAD'].includes(request.method())) return route.fallback();
    const body = new URLSearchParams(request.postData() || ''), key = request.headers()['idempotency-key'] || '';
    const safeBody = new URLSearchParams(body); safeBody.delete('csrf_token');
    requests.push({method: request.method(), path: url.pathname, body: safeBody.toString(), key});
    if (url.pathname === basePath + '/edit/') {
      if (mode === 'lost') return route.abort('failed');
      const receipt = {...fixture.editor_receipt, submission_key: key, version: Number(body.get('version')) + 1, replayed: mode === 'replay'};
      if (mode === 'wrong-receipt') receipt.id++;
      return route.fulfill({ contentType: 'application/json', body: JSON.stringify(receipt) });
    }
    if (url.pathname === basePath + '/autosave/') return route.fulfill({contentType: 'application/json', body: JSON.stringify({...fixture.autosave_receipt, source_version: Number(body.get('version'))})});
    if (url.pathname.endsWith('/slug-suggestion')) return route.fulfill({contentType: 'application/json', body: JSON.stringify({slug: 'editor-acceptance', source: 'fallback', notice: '验收别名'})});
    return route.fulfill({ status: 204 });
  });
  const check = label => { evidence.push({ browser: name, check: label }); console.log(`${name}: ${label}`); };
  const visit = async path => {
    await page.goto(fixture.base + path, { waitUntil: 'domcontentloaded' });
    if (path === fixture.paths.editor) await page.locator('.ProseMirror').waitFor();
  };
  const idle = () => page.waitForFunction(() => !document.querySelector('[data-content-editor]')?.hasAttribute('aria-busy'));
  const setTitle = value => page.locator('[name="title"]').evaluate((node, value) => {node.value = value; node.dispatchEvent(new Event('input', {bubbles: true}));}, value);
  const save = () => page.locator('button[name="action"][value="save"]').click();
  const fresh = async () => { mode = ''; holdSession = false; heldSession = undefined; await visit(fixture.paths.editor); };
  try {
    await visit(fixture.login);
    scriptRequests.length = 0;
    await visit(fixture.paths.editor);
    await page.locator('[data-content-editor]').waitFor();
    await page.locator('[data-content-source]').waitFor({ state: 'attached' });
    await page.locator('.ProseMirror').waitFor();
    const source = page.locator('[data-content-source]');
    const original = await source.inputValue();
    assert.equal(await page.locator('.ProseMirror').count(), 1, 'continuous editor should mount once');
    assert.equal(await page.locator('script[src*="newpress-editor-page.js"]').count(), 1);
    assert.equal(await page.locator('script[src*="chunnel-admin-editor.js"], script[src*="chunnel-editor-workspace.js"], script[src*="chunnel-emoji-editor.js"]').count(), 0);
    assert.equal(await page.locator('script[src*="chunnel-composer.js"]').count(), 0);
    check('real editor page starts the continuous editor and the combined page enhancements once.');

    await page.waitForLoadState('load');
    await page.waitForFunction(() => window.NewPressSelects);
    assert.equal(await page.evaluate(() => typeof window.ChunnelEmoji), 'undefined');
    assert.deepEqual(await page.evaluate(() => [typeof window.ChunnelAdmin?.icon, typeof window.ChunnelAdmin?.showToast, typeof window.ChunnelAdminUI]), ['function', 'function', 'undefined']);
    assert.equal(await page.locator('.admin-sidebar').isVisible(), false);
    assert.equal(await page.locator('.admin-topbar').isVisible(), false);
    assert.equal(await page.locator('.editor-back').getAttribute('href'), '/admin/content/');
    check('the writing workspace keeps shared notices and its own navigation without ordinary admin savebar controllers.');
    initialScripts = [...scriptRequests];
    for (const entry of ['newpress-editor-page.js', 'newpress-document-editor.js']) {
      assert.equal(initialScripts.filter(script => script === entry).length, 1, `${entry} should download once`);
    }
    const merged = ['chunnel-admin-workbench.js', 'chunnel-admin.js', 'chunnel-admin-editor.js',
      'chunnel-editor-workspace.js', 'chunnel-editor-recovery.js', 'chunnel-seo.js', 'chunnel-emoji-editor.js',
      'chunnel-emoji.js', 'chunnel-article-badge-editor.js', 'chunnel-selects.js',
      'newpress-resource-loader.js', 'newpress-editor-media-loader.js', 'newpress-component-loader.js', 'newpress-editor-tools-loader.js'];
    assert.deepEqual(initialScripts.filter(script => merged.includes(script)), []);
    assert.deepEqual([...initialScripts].sort(), ['chunnel-color-mode.js', 'chunnel-ai-loader.js',
      'newpress-editor-page.js', 'newpress-document-editor.js'].sort());
    assert.equal(await page.locator('script[data-editor-media-loader][data-component-loader][data-editor-tools-loader]').count(), 1);
    assert.equal(await page.locator('[data-content-status]').getAttribute('data-choice'), '');
    await page.locator('[data-editor-settings-tab="publication"]').click();
    const commentStatus = page.locator('select[name="comment_status"]');
    const originalCommentStatus = await commentStatus.inputValue();
    await commentStatus.selectOption(originalCommentStatus === 'open' ? 'closed' : 'open', { force: true });
    assert.notEqual(await commentStatus.inputValue(), originalCommentStatus);
    await commentStatus.selectOption(originalCommentStatus, { force: true });
    await page.locator('[data-editor-settings-tab="article"]').click();
    check('initial requests contain each merged entry once and included select controls preserve form values.');

    const titleBefore = await page.locator('[name="title"]').inputValue();
    await setTitle('合并入口预览核对');
    await page.waitForFunction(() => [...document.querySelectorAll('[data-seo-preview-title]')].every(node => node.textContent.includes('合并入口预览核对')));
    assert.equal(await page.locator('[data-article-badge-title]').textContent(), '合并入口预览核对');
    const badgeKind = page.locator('[data-article-badge-field="kind"]');
    const badgeBefore = await badgeKind.inputValue();
    await badgeKind.evaluate(node => { node.value = 'status'; node.dispatchEvent(new Event('change', { bubbles: true })); });
    assert.equal(await page.locator('[data-article-badge-preview] .np-article-badge').count(), 1);
    assert.equal(await page.locator('[data-article-badge-preview] .np-article-badge-text').textContent(), '草稿');
    await badgeKind.evaluate((node, value) => { node.value = value; node.dispatchEvent(new Event('change', { bubbles: true })); }, badgeBefore);
    await setTitle(titleBefore);
    check('SEO and article badge previews follow changed form fields without duplicate preview nodes.');

    await page.locator('.document-block-menu > summary').click();
    await page.locator('[data-composer-mode="source"]').click();
    await source.focus();
    await source.evaluate(node => { node.setSelectionRange(node.value.length, node.value.length); node.dispatchEvent(new Event('select')); });
    await page.locator('[data-emoji-trigger][data-emoji-target="content-source"]').click();
    await page.locator('[data-emoji-grid] [data-emoji-token="[得意]"]').click();
    assert.equal((await source.inputValue()).split('[得意]').length, original.split('[得意]').length + 1);
    await page.locator('[data-emoji-popover]').waitFor({state: 'hidden'});
    assert.equal(await source.evaluate(node => node === document.activeElement), true);
    await page.locator('.document-block-menu > summary').click();
    await page.locator('[data-composer-mode="blocks"]').click();
    assert.equal(await source.inputValue(), original + '[得意]');
    await page.locator('[data-composer-undo]').click();
    assert.equal(await source.inputValue(), original);
    assert.equal(await page.locator('[data-emoji-popover]').count(), 1);
    check('source emoji insertion survives editor mode changes and undo restores the complete original document.');

    const settings = page.locator('[data-editor-settings-toggle]');
    await settings.click();
    assert.equal(await settings.getAttribute('aria-expanded'), 'false');
    await page.locator('[data-editor-focus]').click();
    assert.equal(await page.locator('.admin-editor-page').evaluate(node => node.classList.contains('is-focus')), true);
    await page.locator('[data-editor-focus]').click();
    check('settings drawer and focus mode preserve accessible state transitions.');

    await page.locator('.ProseMirror').click(); await page.keyboard.press('Control+End');
    await page.keyboard.insertText(' 撤销和选区核对');
    assert.match(await source.inputValue(), /撤销和选区核对/);
    await page.locator('[data-composer-undo]').click();
    assert.equal(await source.inputValue(), original);
    await page.locator('[data-composer-redo]').click(); assert.match(await source.inputValue(), /撤销和选区核对/);
    await page.locator('[data-composer-undo]').click();
    check('continuous editor selection, insertion, undo and redo preserve the original serialized document.');

    await page.evaluate(() => {
      const form = document.querySelector('[data-content-editor]'), title = form.querySelector('[name="title"]');
      title.dispatchEvent(new CompositionEvent('compositionstart', {bubbles:true}));
      title.value = '编辑器输入法暂存'; title.dispatchEvent(new Event('input', {bubbles:true}));
      form.requestSubmit(form.querySelector('button[value="save"]'));
    });
    await idle();
    assert.equal(editRequests().length, 0, 'composition must defer saves');
    assert.equal(await page.locator('[data-content-editor]').getAttribute('data-editor-composing'), 'true');
    await page.evaluate(() => document.querySelector('[name="title"]').dispatchEvent(new CompositionEvent('compositionend', { bubbles: true })));
    check('IME composition defers a save and resumes after compositionend.');

    const status = page.locator('[data-content-status]');
    await status.evaluate(node => { node.value = 'publish'; node.dispatchEvent(new Event('change', { bubbles: true })); });
    await page.locator('button[name="action"][value="save"]').click();
    await page.locator('dialog[data-publish-confirm], .editor-publish-dialog').waitFor();
    assert.equal(editRequests().length, 0);
    const before = await page.locator('[data-publish-intro]').textContent();
    await page.locator('[name="title"]').evaluate(node => { node.value = '发布确认后又有修改'; node.dispatchEvent(new Event('input', { bubbles: true })); });
    await page.locator('[data-publish-confirm]').click();
    assert.notEqual(await page.locator('[data-publish-intro]').textContent(), before);
    assert.equal(editRequests().length, 0);
    await page.locator('[data-publish-cancel]').click();
    check('publication review rechecks changed fields before allowing a write.');

    await page.locator('[name="title"]').fill('实际保存回执校验');
    await status.evaluate(node => { node.value = 'draft'; node.dispatchEvent(new Event('change', { bubbles: true })); });
    await page.locator('button[name="action"][value="save"]').click();
    await page.waitForFunction(() => document.querySelector('[data-editor-save-state]')?.textContent?.includes('正式版本') || document.querySelector('[data-editor-save-state]')?.textContent?.includes('已保存'));
    assert.equal(editRequests().length, 1);
    assert.equal(await page.locator('[name="title"]').inputValue(), '实际保存回执校验');
    check('native save accepts a typed receipt and keeps the current document in place.');

    await page.setViewportSize({ width: 390, height: 844 });
    await page.waitForFunction(() => document.querySelector('.admin-editor-page').classList.contains('is-narrow'));
    await page.locator('[data-editor-settings-toggle]').click();
    assert.equal(await page.locator('dialog.editor-settings-drawer').isVisible(), true);
    await page.locator('[data-editor-settings-close]').click();
    assert.equal(await page.locator('dialog.editor-settings-drawer').isVisible(), false);
    assert.equal(await source.inputValue(), original);
    check('mobile settings dialog opens and closes without losing the source field.');
    for (const color of ['light', 'dark']) {
      await page.evaluate(color => window.ChunnelColorMode.set(color), color);
      for (const width of [390, 1280]) {
        await page.setViewportSize({width, height:900});
        await page.waitForFunction(width => document.querySelector('.admin-editor-page').classList.contains('is-narrow') === (width < 1280), width);
        // ResizeObserver applies the workspace layout in the following frame.
        // The class change alone does not mean those layout writes have painted.
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
        const beforeScreenshot = await page.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, viewport: innerWidth, scrollX }));
        assert.ok(beforeScreenshot.scrollWidth <= beforeScreenshot.viewport + 1, `${name} ${variant} ${color} ${width} before screenshot: ${JSON.stringify(beforeScreenshot)}`);
        await page.screenshot({path:resolve(directory, `${variant}-${name}-${color}-${width}.png`), fullPage:true});
        const layout = await page.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, viewport: innerWidth, scrollX }));
        assert.ok(layout.scrollWidth <= layout.viewport + 1, `${name} ${variant} ${color} ${width}: ${JSON.stringify(layout)}`);
      }
    }
    check('light and dark editor layouts fit mobile and desktop viewports.');

    await fresh(); await setTitle('点击保存时的标题'); holdSession = true;
    await save(); await until(() => heldSession);
    await setTitle('等待登录期间输入的新标题');
    holdSession = false; await heldSession(); await idle();
    assert.equal(new URLSearchParams(editRequests().at(-1).body).get('title'), '点击保存时的标题');
    assert.equal(await page.locator('[name="title"]').inputValue(), '等待登录期间输入的新标题');
    assert.equal(await page.locator('[name="version"]').inputValue(), String(fixture.editor_version+1));
    check('save freezes the reviewed input before session refresh and retains later typing after acknowledgement.');

    await fresh(); mode = 'lost'; await setTitle('丢失回执的原保存'); await save();
    await page.waitForFunction(() => document.querySelector('[data-content-editor]').dataset.editorPendingSubmission === 'true' && !document.querySelector('[data-content-editor]').hasAttribute('aria-busy'));
    const sent = editRequests().at(-1); await setTitle('丢失回执后的新输入'); mode = 'replay'; await save(); await idle();
    assert.equal(editRequests().at(-1).key, sent.key); assert.equal(editRequests().at(-1).body, sent.body);
    assert.equal(await page.locator('[name="title"]').inputValue(), '丢失回执后的新输入');
    assert.equal(await page.locator('[data-content-editor]').getAttribute('data-editor-pending-submission'), 'false');
    check('response loss retries the same idempotency key and body without discarding newer input.');

    await fresh(); mode = 'wrong-receipt'; await setTitle('错误归属回执'); await save(); await idle();
    assert.equal(await page.locator('[name="version"]').inputValue(), String(fixture.editor_version));
    assert.equal(await page.locator('[data-content-editor]').getAttribute('data-editor-pending-submission'), 'true');
    mode = 'replay'; await save(); await idle();
    check('a receipt for another article cannot acknowledge or discard the pending write.');

    await fresh(); mode = 'other-account'; await setTitle('原账号私有输入');
    const count = editRequests().length; await save(); await idle();
    assert.equal(editRequests().length, count); assert.equal(await page.locator('[name="title"]').inputValue(), '原账号私有输入');
    assert.match(await page.locator('[data-editor-autosave-detail]').textContent(), /账号已切换/);
    check('another account cannot submit the retained document even with a valid session.');

    await fresh(); await setTitle('自动恢复点验收');
    await page.locator('[data-editor-retry]').evaluate(node => node.click());
    await page.waitForFunction(() => document.querySelector('[data-editor-autosave-detail]').textContent.includes('恢复点已更新'));
    assert.equal(autosaves().length > 0, true);
    assert.equal(await page.locator('[name="version"]').inputValue(), String(fixture.editor_version));
    check('recovery save validates Go article/version metadata and leaves the canonical revision unchanged.');

    await page.evaluate(() => {
      window.dispatchEvent(new PageTransitionEvent('pagehide', {persisted:true}));
      window.dispatchEvent(new PageTransitionEvent('pageshow', {persisted:true}));
    });
    await setTitle('返回后保存'); await save(); await idle();
    assert.equal(await page.locator('[name="version"]').inputValue(), String(fixture.editor_version+1));
    check('pagehide cancellation and persisted pageshow resume the same editor safely.');

    await fresh();
    await page.locator('[data-browser-recovery-toggle]').evaluate(node => {node.checked = true; node.dispatchEvent(new Event('change', {bubbles:true}));});
    await setTitle('跨标签保留的正文');
    await page.waitForFunction(() => document.querySelector('[data-browser-recovery-status]').textContent.includes('本机副本已更新'));
    // Visibility flushing takes an immediate snapshot without waiting for the
    // storage debounce, then another real tab observes the stored copy.
    await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pagehide', {persisted:true})));
    await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', {persisted:true})));
    const second = await context.newPage();
    second.on('pageerror', error => errors.push(error.message)); second.on('dialog', dialog => dialog.accept());
    await second.goto(fixture.base + fixture.paths.editor);
    await second.locator('.ProseMirror').waitFor();
    await second.waitForFunction(() => document.querySelector('[data-browser-recovery-list]').textContent.includes('跨标签保留的正文'));
    assert.notEqual(await second.locator('[name="title"]').inputValue(), '跨标签保留的正文');
    await second.getByText('恢复这份编辑', {exact:true}).evaluate(node => node.click());
    await second.waitForFunction(() => document.querySelector('[name="title"]').value === '跨标签保留的正文');
    assert.equal(await second.locator('[name="version"]').inputValue(), String(fixture.editor_version));
    assert.match(await second.locator('[data-editor-save-state]').textContent(), /等待正式保存/);
    check('a real second tab offers explicit draft recovery without silently replacing its current content.');
    await second.locator('[data-browser-recovery-clear]').evaluate(node => node.click());
    await page.waitForFunction(() => !document.querySelector('[data-browser-recovery-toggle]').checked);
    assert.equal(await page.locator('[name="title"]').inputValue(), '跨标签保留的正文');
    assert.equal(await second.locator('[name="title"]').inputValue(), '跨标签保留的正文');
    check('clearing browser recovery propagates across tabs while keeping each open editor input.');
    await second.close();
    assert.deepEqual(errors, []);
    assert.equal(writes.filter(item => item.method !== 'POST').length, 0);
  } finally {
    await mkdir(directory, { recursive: true });
    const state = await page.evaluate(() => ({dataset: {...document.querySelector('[data-content-editor]')?.dataset},
      invalid: [...document.querySelectorAll('[data-content-editor] :invalid')].map(node => ({name:node.name, message:node.validationMessage})),
      detail:document.querySelector('[data-editor-autosave-detail]')?.textContent,
      status:document.querySelector('[data-editor-save-state]')?.textContent,
      dialogs:[...document.querySelectorAll('dialog[open]')].map(node => node.className),
      disabled:document.querySelector('button[value="save"]')?.disabled,
    })).catch(() => null);
    await writeFile(resolve(directory, `${variant}-editor-browser-${name}.json`), JSON.stringify({ evidence: evidence.filter(item => item.browser === name), errors, writes, requests, initialScripts, scriptRequests, state }, null, 2) + '\n');
    await context.close();
    await browser.close();
  }
}
await writeFile(resolve(directory, `${variant}-editor-browser-acceptance.json`), JSON.stringify({ evidence }, null, 2) + '\n');
