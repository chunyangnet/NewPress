// Read-only interaction acceptance against the actual Go admin fixture.
// Firefox may perform native submission for a synthetic SubmitEvent. All POSTs
// are intercepted before reaching the fixture and acknowledged with HTTP 204.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';

const directory = resolve(process.argv[2]);
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
assert.ok(fixture.comment_editor, 'enable NEWPRESS_ADMIN_BUSINESS_BROWSER for a seeded comment');
const engines = { chromium, firefox, webkit };
const evidence = [];
for (const name of process.argv[3] ? [process.argv[3]] : Object.keys(engines)) {
  const browser = await engines[name].launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: 'reduce' });
  const page = await context.newPage();
  const errors = [], writes = [], intercepted = [];
  const interceptWrites = async route => {
    const request = route.request();
    if (['GET', 'HEAD'].includes(request.method())) return route.continue();
    intercepted.push({ method: request.method(), url: request.url() });
    return route.fulfill({ status: 204 });
  };
  await context.route('**/*', interceptWrites);
  page.setDefaultTimeout(12000);
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => { if (!['GET', 'HEAD'].includes(request.method())) writes.push({ method: request.method(), url: request.url() }); });
  const check = label => { evidence.push({ browser: name, check: label }); console.log(`${name}: ${label}`); };
  const visit = async path => { await page.goto(fixture.base + path); await page.waitForLoadState('networkidle'); };
  const reveal = async field => {
    if (await field.isVisible()) return;
    const section = await field.evaluate(node => node.closest('[data-settings-section]')?.id);
    assert.ok(section, 'hidden field must have a navigable settings section');
    await page.locator(`[data-settings-target="${section}"]`).click();
  };
  const submitEvent = (selector, cancel) => page.evaluate(({ selector, cancel }) => {
    const form = document.querySelector(selector);
    if (cancel) form.addEventListener('submit', event => event.preventDefault(), { once: true });
    return form.dispatchEvent(new SubmitEvent('submit', { bubbles: true, cancelable: true }));
  }, { selector, cancel });
  const returnToPage = () => page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })));
  try {
    await visit(fixture.login);
    const scripts = await page.locator('script[src]').evaluateAll(nodes => nodes.map(node => node.src));
    assert.equal(scripts.some(url => /chunnel-admin-(lists|comments|settings|media-upload)\.js/.test(url)), false);
    assert.equal(scripts.some(url => /newpress-analytics\.js|echarts|react/.test(url)), false);
    await page.setViewportSize({ width: 375, height: 812 });
    await page.locator('[data-admin-nav-open]').click();
    assert.equal(await page.locator('[data-admin-sidebar]').getAttribute('aria-modal'), 'true');
    assert.equal(await page.locator('.admin-workspace').evaluate(node => node.inert), true);
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('[data-admin-nav-open]').getAttribute('aria-expanded'), 'false');
    await page.setViewportSize({ width: 1280, height: 900 });
    check('Dashboard loads only common helpers; mobile navigation restores the workspace.');

    await visit('/admin/content/');
    await page.locator('[data-content-select]').first().check();
    await page.locator('[data-content-bulk-action]').selectOption('draft', { force: true });
    assert.equal(await page.locator('[data-content-selected-count]').textContent(), '1');
    assert.equal(await submitEvent('[data-content-bulk]', true), false);
    assert.equal(await page.locator('[data-content-bulk-submit]').isEnabled(), true);
    assert.equal(await submitEvent('[data-content-bulk]', false), true);
    await page.waitForFunction(() => document.querySelector('[data-content-bulk-submit]').disabled);
    assert.equal(await submitEvent('[data-content-bulk]', false), false);
    await returnToPage();
    assert.equal(await page.locator('[data-content-bulk-submit]').isEnabled(), true);
    await page.evaluate(() => { window.__businessDocument = true; });
    await page.locator('.admin-main a[href*="status=draft"]').first().click();
    await page.waitForURL(url => url.searchParams.get('status') === 'draft');
    await page.waitForFunction(() => !document.querySelector('.admin-main').hasAttribute('data-navigation-loading'));
    assert.equal(await page.evaluate(() => window.__businessDocument), true);
    await page.locator('[data-content-select-all]').check();
    assert.ok(Number(await page.locator('[data-content-selected-count]').textContent()) > 0);
    check('Content bulk selection, cancellation, duplicate locking, history recovery and region rebinding work.');

    await visit('/admin/taxonomy/');
    await page.locator('[data-term-name]').fill('Ａ 中文 分类');
    assert.equal(await page.locator('[data-term-slug]').inputValue(), 'a-中文-分类');
    await page.locator('[data-term-slug]').fill('custom-slug');
    await page.locator('[data-term-name]').fill('改名');
    assert.equal(await page.locator('[data-term-slug]').inputValue(), 'custom-slug');
    await page.locator('[data-admin-discard]').click();
    await page.locator('.admin-taxonomy-heading a[href*="post_tag"]').click();
    await page.waitForURL(url => url.searchParams.get('taxonomy') === 'post_tag');
    await page.waitForFunction(() => !document.querySelector('.admin-main').hasAttribute('data-navigation-loading'));
    await page.locator('[data-term-name]').fill('新 标签');
    assert.equal(await page.locator('[data-term-slug]').inputValue(), '新-标签');
    await page.locator('[data-admin-discard]').click();
    check('Taxonomy slug generation preserves manual input and reinitializes after changing taxonomy.');

    await visit('/admin/settings/');
    const settingsTitle = page.locator('input[name="site_title"]');
    await reveal(settingsTitle);
    const initialTitle = await settingsTitle.inputValue();
    await settingsTitle.fill('迁移验收站点');
    assert.equal(await page.locator('[data-admin-savebar][data-form-id="site-settings-form"] [data-admin-save]').isEnabled(), true);
    await page.locator('[data-admin-discard]').click();
    assert.equal(await settingsTitle.inputValue(), initialTitle);
    await page.locator('details.admin-more > summary').click();
    await page.locator('[data-import-open]').click();
    const dialog = page.locator('[data-import-dialog]');
    await dialog.locator('[data-settings-file]').setInputFiles({ name: 'large.json', mimeType: 'application/json', buffer: Buffer.alloc(256 * 1024 + 1, 32) });
    assert.equal(await dialog.locator('[data-settings-file]').evaluate(node => node.validity.valid), false);
    await dialog.locator('[data-settings-file]').setInputFiles({ name: 'settings.json', mimeType: 'application/json', buffer: Buffer.from('{}') });
    assert.equal(await dialog.locator('[data-settings-file]').evaluate(node => node.validity.valid), true);
    await dialog.locator('.admin-dialog-close').focus(); await page.keyboard.press('Shift+Tab');
    assert.equal(await dialog.locator('button[type="submit"]').evaluate(node => node === document.activeElement), true);
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => !document.body.classList.contains('admin-dialog-open'));
    check('Settings save/discard and import size validation, focus loop and native Escape remain usable.');

    await visit('/admin/tools/#navigation-import');
    await reveal(page.locator('[data-tools-navigation-file]'));
    await page.locator('[data-tools-navigation-file]').setInputFiles({ name: 'navigation.json', mimeType: 'application/json', buffer: Buffer.from('{}') });
    assert.match(await page.locator('[data-tools-navigation-file-name]').textContent(), /navigation\.json/);
    const importSubmit = page.locator('[data-navigation-import-preview] button[type="submit"]');
    const importLabel = await importSubmit.textContent();
    await submitEvent('[data-navigation-import-preview]', true); assert.equal(await importSubmit.isEnabled(), true);
    await submitEvent('[data-navigation-import-preview]', false);
    await page.waitForFunction(() => document.querySelector('[data-navigation-import-preview] button[type="submit"]').disabled);
    await returnToPage(); assert.equal(await importSubmit.isEnabled(), true); assert.equal(await importSubmit.textContent(), importLabel);
    check('Navigation import filename and pending label restore after cancelled or returned submissions.');

    await visit('/admin/media/');
    const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScLbtAAAAABJRU5ErkJggg==', 'base64');
    await page.locator('[data-media-file]').setInputFiles({ name: 'preview.png', mimeType: 'image/png', buffer: png });
    await page.locator('.admin-media-upload-preview').waitFor();
    assert.equal(await page.locator('[data-media-file-title]').textContent(), 'preview.png');
    await page.locator('[data-media-file]').setInputFiles({ name: 'blocked.svg', mimeType: 'image/svg+xml', buffer: Buffer.from('<svg/>') });
    assert.equal(await page.locator('.admin-media-upload-preview').count(), 0);
    await submitEvent('[data-media-upload]', true);
    assert.equal(await page.locator('[data-media-upload] button[type="submit"]').isEnabled(), true);
    await submitEvent('[data-media-upload]', false);
    await page.waitForFunction(() => document.querySelector('[data-media-upload] button[type="submit"]').disabled);
    await returnToPage(); assert.equal(await page.locator('[data-media-upload] button[type="submit"]').isEnabled(), true);
    check('Media previews raster files only; cancelled upload and bfcache restore keep the form available.');

    await visit('/admin/comments/?status=approved');
    await page.locator('[data-comment-select]').first().check();
    assert.equal(await page.locator('[data-comment-selected-count]').textContent(), '1');
    await visit(fixture.comment_editor);
    const source = page.locator('[data-comment-edit-source]');
    const initialComment = await source.inputValue();
    const text = '<img src=x onerror=alert(1)>\n\n中文👍';
    await page.locator('[data-comment-edit-form] .emoji-editor').fill(text);
    assert.equal(await source.inputValue(), text);
    assert.match(await page.locator('[data-comment-safe-preview]').textContent(), /<img src=x onerror=alert\(1\)>/);
    assert.equal(await page.locator('[data-comment-safe-preview] img').count(), 0);
    assert.equal(await page.locator('[data-comment-edit-count]').textContent(), String([...text].length));
    await submitEvent('[data-comment-edit-form]', true);
    assert.equal(await page.locator('[data-admin-save]').isEnabled(), true);
    await submitEvent('[data-comment-edit-form]', false);
    await page.waitForFunction(() => document.querySelector('[data-admin-save]').disabled);
    await returnToPage(); await page.locator('[data-admin-discard]').click();
    assert.equal(await source.inputValue(), initialComment);
    await page.locator('[data-comment-reply-form] .emoji-editor').fill('中文👍');
    assert.equal(await page.locator('[data-comment-reply-count]').textContent(), '3');
    await page.locator('[data-comment-reply-form] .emoji-editor').fill('');
    check('Comment list and editor retain safe preview, Unicode counts, save recovery and reply controls.');

    await page.screenshot({ path: resolve(directory, `business-${name}-comments.png`), fullPage: true });
    const loginContext = await browser.newContext();
    await loginContext.route('**/*', interceptWrites);
    const login = await loginContext.newPage();
    login.on('pageerror', error => errors.push(error.message));
    login.on('request', request => { if (!['GET', 'HEAD'].includes(request.method())) writes.push({ method: request.method(), url: request.url() }); });
    try {
      await login.goto(fixture.base + '/admin/login/');
      await login.locator('[data-password-toggle]').click();
      assert.equal(await login.locator('input[name="password"]').getAttribute('type'), 'text');
      await login.locator('[data-password-toggle]').click();
      assert.equal(await login.locator('input[name="password"]').getAttribute('type'), 'password');
      const state = await login.evaluate(() => {
        const form = document.querySelector('.admin-login-form');
        form.addEventListener('submit', event => event.preventDefault(), { once: true });
        form.dispatchEvent(new SubmitEvent('submit', { bubbles: true, cancelable: true }));
        const cancelled = !form.dataset.submitOnce;
        form.dispatchEvent(new SubmitEvent('submit', { bubbles: true, cancelable: true }));
        const locked = form.dataset.submitOnce === 'true';
        const duplicate = !form.dispatchEvent(new SubmitEvent('submit', { bubbles: true, cancelable: true }));
        window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }));
        return { cancelled, locked, duplicate, reset: !form.dataset.submitOnce };
      });
      assert.deepEqual(state, { cancelled: true, locked: true, duplicate: true, reset: true });
    } finally { await loginContext.close(); }
    check('Admin login preserves password toggle, verification cancellation, duplicate guard and return recovery.');
    assert.deepEqual(errors, []); assert.deepEqual(writes, intercepted);
    assert.ok(intercepted.every(item => item.method === 'POST' && new URL(item.url).pathname.startsWith('/admin/')));
    check('No page errors; all synthetic native submissions intercepted before reaching the fixture.');
  } finally {
    await writeFile(resolve(directory, `business-${name}.json`), JSON.stringify({ evidence: evidence.filter(item => item.browser === name), errors, writes, intercepted }, null, 2) + '\n');
    await context.close(); await browser.close();
  }
}
await writeFile(resolve(directory, 'business-acceptance.json'), JSON.stringify({ evidence }, null, 2) + '\n');
