// Editor tools acceptance on isolated Go pages. All browser writes are intercepted.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';

const directory = resolve(process.argv[2]);
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const engines = { chromium, firefox, webkit };
const selected = process.argv[3] ? [process.argv[3]] : Object.keys(engines);
const evidence = [];
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==', 'base64');
for (const name of selected) {
  const browser = await engines[name].launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: 'reduce' });
  const page = await context.newPage(); page.setDefaultTimeout(12000);
  const errors = [], writes = [], intercepted = [];
  let loaderAttempts = 0, reuseMismatch = true, widgetsMalformed = false;
  const check = label => { evidence.push({ browser: name, check: label }); console.log(`${name}: ${label}`); };
  const json = (route, body) => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => { if (!['GET', 'HEAD'].includes(request.method())) writes.push({ method: request.method(), url: request.url() }); });
  page.on('dialog', dialog => dialog.accept());
  await context.route('**/*', route => {
    if (['GET', 'HEAD'].includes(route.request().method())) return route.fallback();
    intercepted.push({ method: route.request().method(), url: route.request().url() });
    return route.fulfill({ status: 204 });
  });
  await page.route('**/js/newpress-resource-editor.js*', route => ++loaderAttempts === 1 ? route.abort('failed') : route.continue());
  await page.route('**/api/v1/admin/contents/1', async route => {
    if (route.request().method() !== 'GET') return route.fallback();
    if (!reuseMismatch) return route.continue();
    const response = await route.fetch(), body = await response.json();
    return json(route, { ...body, version: body.version + 1 });
  });
  await page.route('**/api/v1/admin/article-widgets*', route => json(route, widgetsMalformed ? { items: null } : {
    items: [{ id: 7, title: '文章引用验收', permalink: '/posts/editor-tools-acceptance', excerpt: '公开文章简介', author_name: '验收作者', category: '阅读', cover_url: '' }],
    pagination: { page: 1, pages: 1, total: 1 }
  }));
  await page.route('**/media/editor-tools.png', route => route.fulfill({ contentType: 'image/png', body: png }));
  const visit = async path => { await page.goto(fixture.base + path); await page.waitForLoadState('networkidle'); };
  const openWide = async kind => {
    const toggle = page.locator('[data-editor-settings-toggle]');
    if (await toggle.getAttribute('aria-expanded') !== 'true') await toggle.click();
    const group = page.locator(`[data-editor-wide="${kind}"]`);
    const tab = await group.evaluate(node => node.closest('[data-editor-settings-panel]')?.dataset.editorSettingsPanel);
    if (tab) await page.locator(`[data-editor-settings-tab="${tab}"]`).click();
    await group.locator(':scope > summary').click();
    await page.locator('.editor-wide-dialog[open]').waitFor();
  };
  const closeWide = async () => { await page.locator('[data-wide-done]').click(); await page.waitForFunction(() => !document.querySelector('.editor-wide-dialog').open); };
  const openInsert = async kind => {
    const menu = page.locator('.editor-insert-menu');
    if (!(await menu.evaluate(node => node.open))) await menu.locator(':scope > summary').click();
    await menu.locator(`[data-editor-${kind}-open]`).click();
  };
  const change = async (selector, value) => page.locator(selector).evaluate((node, value) => { node.value = value; node.dispatchEvent(new Event('change', { bubbles: true })); }, value);
  const config = () => page.locator('[data-resource-config]').inputValue().then(JSON.parse);
  try {
    await visit(fixture.login); await visit(fixture.paths.editor);
    await page.locator('[data-composer].is-document-editor .ProseMirror').waitFor();
    const source = page.locator('[data-content-source]'), original = await source.inputValue();
    assert.equal(await page.locator('script[src*="/newpress-resource-editor.js"]').count(), 0);
    await openWide('resources');
    await page.waitForFunction(() => document.querySelector('[data-resource-editor-status]').textContent.includes('加载失败'));
    await closeWide(); await openWide('resources');
    await page.locator('[data-action="preset-paid"]').waitFor();
    await closeWide(); await openWide('resources');
    assert.equal(loaderAttempts, 2);
    assert.equal(await page.locator('script[src*="/newpress-resource-editor.js"]').count(), 1);
    assert.equal(await page.locator('link[href*="/newpress-resource-editor.css"]').count(), 1);
    check('Resource tools load on demand; a failed script retries and subsequent opens reuse one script and stylesheet.');

    await page.locator('[data-action="preset-paid"]').click();
    const moneyPath = 'items.0.variants.0.groups.0.conditions.0';
    const amount = page.locator(`[data-path="${moneyPath}.amount"]`);
    await amount.fill('9.999');
    assert.match(await amount.evaluate(node => node.validationMessage), /2 位/);
    assert.equal((await config()).items[0].variants[0].groups[0].conditions[0].amount, 990);
    const prevented = await page.locator('[data-content-editor]').evaluate(form => !form.dispatchEvent(new SubmitEvent('submit', { bubbles: true, cancelable: true })));
    assert.equal(prevented, true);
    await page.locator('[data-action="add-group"]').click();
    assert.equal(await amount.inputValue(), '9.999');
    await amount.fill('19.90');
    await change(`[data-path="${moneyPath}.currency"]`, 'JPY');
    assert.match(await page.locator('[data-resource-editor-status]').innerText(), /无法切换货币/);
    assert.equal((await config()).items[0].variants[0].groups[0].conditions[0].currency, 'CNY');
    await amount.fill('20'); await change(`[data-path="${moneyPath}.currency"]`, 'JPY');
    assert.equal((await config()).items[0].variants[0].groups[0].conditions[0].amount, 20);
    check('Invalid money stays visible and blocks submit; currency changes preserve exact values and reject precision loss.');

    const restored = await config(); restored.items[0].title = '恢复的资源'; restored.items[0].variants[0].downloads = null;
    await page.locator('[data-resource-config]').evaluate((input, value) => {
      input.value = JSON.stringify(value);
      document.querySelector('[data-content-source]').dispatchEvent(new Event('input', { bubbles: true }));
    }, restored);
    assert.equal(await page.locator('[data-path="items.0.title"]').inputValue(), '恢复的资源');
    await page.locator('[data-resource-workspace] summary').filter({ hasText: '外观与访问设置' }).click();
    await page.locator('[data-path="items.0.title"]').fill('恢复后继续编辑');
    assert.deepEqual((await config()).items[0].variants[0].downloads, []);
    await page.locator('[data-resource-workspace] summary').filter({ hasText: '提交记录' }).click();
    await page.locator('[data-action="submissions"]').click();
    await page.waitForFunction(() => document.querySelector('[data-submissions]')?.textContent.includes('暂无提交记录'));
    await change('[data-path="items.0.variants.0.kind"]', 'content');
    check('Recovery events refresh an already open resource editor, normalize nullable collections and read real submission data.');

    for (const width of [1280, 375]) {
      await page.setViewportSize({ width, height: 900 });
      const geometry = await page.locator('.editor-wide-dialog').evaluate(node => {
        const r = node.getBoundingClientRect(); return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: innerWidth, height: innerHeight };
      });
      assert.ok(geometry.left >= -1 && geometry.right <= geometry.width + 1 && geometry.top >= -1 && geometry.bottom <= geometry.height + 1, JSON.stringify(geometry));
      await page.screenshot({ path: resolve(directory, `editor-tools-${name}-${width}.png`) });
    }
    await page.setViewportSize({ width: 1280, height: 900 }); await closeWide();
    await page.locator('.ProseMirror').click(); await page.keyboard.press('ControlOrMeta+A');
    await page.waitForFunction(() => Boolean(window.ChunnelBlockEditor.captureText()));
    const selection = await page.evaluate(() => window.ChunnelBlockEditor.captureText());
    assert.ok(selection && selection.to > selection.from);
    await openWide('resources'); await page.locator('[data-action="hide-selection"]').click();
    const resource = (await config()).items[0];
    assert.match(await source.inputValue(), new RegExp(`\\[resource:${resource.id}\\]`));
    assert.equal(resource.variants[0].body, original);
    await closeWide(); await page.locator('[data-composer-undo]').click(); assert.equal(await source.inputValue(), original);
    check('Resource dialogs fit desktop/narrow screens; hiding a real editor selection stores its body and marker insertion is undoable.');

    await visit(fixture.paths.editor);
    await page.locator('.ProseMirror').click(); await page.keyboard.press('ControlOrMeta+End'); await openInsert('reuse');
    await change('[data-editor-reuse-search] [name="status"]', 'draft');
    await page.locator('[data-editor-reuse-search] button[type="submit"]').click();
    const reuseChoice = page.locator('[data-editor-reuse-results] button').first(); await reuseChoice.click();
    await page.waitForFunction(() => document.querySelector('[data-editor-reuse-status]').textContent.includes('原内容已变化'));
    assert.equal(await source.inputValue(), original); reuseMismatch = false; await reuseChoice.click();
    await page.waitForFunction(() => !document.querySelector('[data-editor-reuse-dialog]').open);
    assert.ok((await source.inputValue()).length > original.length);
    await page.locator('[data-composer-undo]').click(); assert.equal(await source.inputValue(), original);
    check('Reuse checks a real Go content version, preserves text on mismatch and inserts into the continuous editor with undo.');

    widgetsMalformed = true; await openInsert('widget');
    await page.waitForFunction(() => document.querySelector('[data-editor-widget-status]').textContent.includes('响应无效'));
    assert.equal(await page.locator('[data-editor-widget-insert]').isEnabled(), false);
    widgetsMalformed = false; await page.locator('[data-editor-widget-search] button').click();
    await page.locator('[data-article-choice="7"]').click();
    await page.locator('[name="article_widget_layout"][value="compact"]').check();
    assert.equal(await page.locator('[data-editor-widget-preview] [data-np-layout="compact"]').count(), 1);
    await page.locator('[data-editor-widget-insert]').click();
    await page.waitForFunction(() => !document.querySelector('[data-editor-widget-dialog]').open);
    assert.match(await source.inputValue(), /data-np-layout="compact"/);
    await page.locator('[data-composer-undo]').click(); assert.equal(await source.inputValue(), original);
    check('Article cards recover from malformed lists, preview compact layout and preserve editor undo.');

    await openInsert('widget'); await page.locator('[data-article-choice="7"]').click();
    await page.evaluate(() => window.ChunnelBlockEditor.reset('<p>更新后的正文</p>'));
    await page.locator('[data-editor-widget-insert]').click();
    assert.match(await page.locator('[data-editor-widget-status]').innerText(), /正文已变化/);
    assert.equal(await source.inputValue(), '<p>更新后的正文</p>');
    await page.locator('[data-editor-widget-close]').click();
    await page.evaluate(value => window.ChunnelBlockEditor.reset(value), original);
    check('An article picker cannot overwrite a document changed while its dialog is open.');

    await openWide('badge');
    await page.locator('[data-article-badge-preset="status"]').click();
    assert.match(await page.locator('[data-article-badge-preview]').innerText(), /草稿/);
    await change('[data-content-editor] [name="status"]', 'private');
    assert.match(await page.locator('[data-article-badge-preview]').innerText(), /私密/);
    await change('[data-content-editor] [name="pinned"]', 'true');
    assert.match(await page.locator('[data-article-badge-preview]').innerText(), /置顶/);
    await change('[data-content-editor] [name="cover_url"]', '/media/editor-tools.png');
    await page.waitForFunction(() => document.querySelector('[data-article-badge-image]').naturalWidth === 1);
    await page.locator('[data-article-badge-field="kind"]').evaluate(node => { node.disabled = true; });
    await page.locator('[data-article-badge-preset="pinned"]').click();
    assert.equal(await page.locator('[data-article-badge-field="kind"]').inputValue(), 'status');
    await page.locator('[data-article-badge-field="kind"]').evaluate(node => { node.disabled = false; }); await closeWide();
    check('Badge previews follow publication/pinned state, decode a local cover and respect disabled fields.');

    await openWide('seo');
    await page.evaluate(() => {
      const form = document.querySelector('[data-content-editor]');
      form.querySelector('[name="title"]').value = 'SEO <img src=x> 标题';
      form.querySelector('[name="excerpt"]').value = '';
      form.querySelector('[name="content_source"]').value = '<p>第一段</p><p>第二段</p><script>window.__seoExecuted=true</script><img src="/seo-inert-probe" onerror="window.__seoExecuted=true">';
      form.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await page.waitForFunction(() => document.querySelector('[data-seo-preview-description]').textContent === '第一段 第二段');
    assert.match(await page.locator('[data-seo-preview-title]').first().innerText(), /SEO <img src=x> 标题/);
    assert.equal(await page.evaluate(() => window.__seoExecuted), undefined);
    await page.locator('[data-seo-check-run]').click();
    await page.waitForFunction(() => document.querySelector('[data-seo-check-status]').textContent.includes('正式版本'));
    assert.match(await page.locator('[data-seo-check-status]').innerText(), /内容 #1/);
    check('SEO preview treats markup as inert text and link checking validates the real Go report.');

    await visit('/admin/seo/?edit=new#redirects');
    const search = page.locator('[data-seo-source-search]');
    await search.locator('button').click();
    const target = page.locator('[data-seo-source-results] button').first(); await target.click();
    assert.equal(await page.locator('#seo-target-id').inputValue(), '1');
    await page.locator('#seo-target-id').evaluate(node => { node.value = ''; node.readOnly = true; });
    await target.click(); assert.equal(await page.locator('#seo-target-id').inputValue(), '');
    check('SEO redirect target search accepts the real Go response and cannot change a locked target.');
    assert.deepEqual(errors, []); assert.deepEqual(writes, intercepted);
    check('No browser exceptions; every attempted write was intercepted before reaching the isolated server.');
  } catch (error) {
    await page.screenshot({ path: resolve(directory, `editor-tools-${name}-failure.png`) }).catch(() => {});
    throw error;
  } finally {
    await writeFile(resolve(directory, `editor-tools-${name}.json`), JSON.stringify({ evidence: evidence.filter(item => item.browser === name), errors, writes, intercepted }, null, 2) + '\n');
    await context.close(); await browser.close();
  }
}
await writeFile(resolve(directory, 'editor-tools-acceptance.json'), JSON.stringify({ evidence }, null, 2) + '\n');
