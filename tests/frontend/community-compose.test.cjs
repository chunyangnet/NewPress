const { test } = require('node:test');
const assert = require('node:assert/strict');
const { chromium } = require('playwright');
const path = require('node:path');
const {compile} = require('./helpers/typescript.cjs');
const source = compile('frontend/core/community-compose.ts');

for (const variant of ['typescript', 'built']) test(`${variant}: community composer retains drafts, protects pending recovery and preserves filter context`, async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 }, reducedMotion: 'reduce' });
    const errors = []; page.on('pageerror', error => errors.push(error.message));
    await page.route('https://example.test/**', route => route.fulfill({ contentType: 'text/html', body: '<div data-questions data-user-id="7"><form class="qa-filters"></form><form data-qa-command="create"><label>标题<input name="title"></label><label>正文<textarea name="body" maxlength="20000"></textarea></label><button>发布</button></form></div>' }));
    const script = variant === 'typescript' ? {content: source} : {path: path.resolve('internal/coreui/assets/js/newpress-community-compose.js')};
    const mount = async () => page.addScriptTag(script);
    await page.goto('https://example.test/questions/new/?tag=design&scope=mine&page=3'); await mount();
    const href = await page.getByRole('link', { name: '还没有回答' }).getAttribute('href');
    assert.match(href, /tag=design/); assert.match(href, /scope=mine/); assert.match(href, /status=unanswered/); assert.doesNotMatch(href, /page=/);
    const tabs = await browser.newPage();
    try {
      await tabs.route('https://example.test/**', route => route.fulfill({ contentType: 'text/html; charset=utf-8', body: '<div data-questions data-user-id="0"><nav class="channel-tabs" aria-label="问题状态"><a href="/questions/" aria-current="page">全部问题</a><a href="/questions/?status=open">等待解答</a><a href="/questions/?status=solved">已解决</a></nav><form class="qa-filters"></form></div>' }));
      await tabs.goto('https://example.test/questions/?tag=design&scope=mine&status=unanswered&page=3');
      await tabs.addScriptTag(script);
      assert.equal(await tabs.getByRole('navigation').count(), 1);
      assert.equal(await tabs.getByRole('link').count(), 4);
      assert.equal(await tabs.locator('[aria-current="page"]').textContent(), '还没有回答');
      const open = await tabs.getByRole('link', { name: '等待解答', exact: true }).getAttribute('href');
      assert.match(open, /tag=design/); assert.match(open, /scope=mine/); assert.match(open, /status=open/); assert.doesNotMatch(open, /page=/);
    } finally { await tabs.close(); }
    await page.locator('[name=body]').fill('保留我的问题');
    await page.reload(); await mount();
    assert.equal(await page.getByRole('button', { name: '恢复草稿' }).isVisible(), true);
    await page.locator('[name=body]').fill('新的输入不应覆盖待恢复草稿');
    await page.reload(); await mount();
    await page.getByRole('button', { name: '恢复草稿' }).click();
    assert.equal(await page.locator('[name=body]').inputValue(), '保留我的问题');
    await page.locator('summary').click();
    page.once('dialog', dialog => dialog.accept());
    await page.getByRole('button', { name: '问题模板', exact: true }).click();
    assert.match(await page.locator('[name=body]').inputValue(), /^保留我的问题\n\n## 背景/);
    await page.locator('[data-qa-command]').evaluate(form => form.dispatchEvent(new CustomEvent('community:saved', {detail: {owner: 7, values: Object.fromEntries(new FormData(form))}})));
    await page.reload(); await mount();
    assert.equal(await page.getByRole('button', { name: '恢复草稿' }).isVisible(), false);
    await page.setContent('<div data-circle-page data-user-id="7"><form class="circle-topic-filters"><select name="filter"><option value="">全部</option></select></form><form data-circle-command="topic"><label>正文<textarea name="body" maxlength="10000"></textarea></label><div data-circle-editor><select name="format"><option value="markdown">Markdown</option></select><button type="button">预览正文</button></div></form></div>');
    await mount();
    await mount();
    assert.equal(await page.locator('.community-compose-tools').count(), 1);
    assert.equal(await page.locator('.community-quick-filters').count(), 1);
    assert.equal(await page.getByRole('link', { name: '我发布的' }).count(), 1);
    assert.equal(await page.locator('[name=filter] option[value=participated]').count(), 1);
    assert.equal(await page.getByRole('button', { name: '预览正文' }).isVisible(), false);
    await page.getByText('图片、附件与正文预览', { exact: true }).click();
    assert.equal(await page.getByRole('button', { name: '预览正文' }).isVisible(), true);
    await page.evaluate(() => { Storage.prototype.setItem = () => { throw new Error('quota'); }; });
    await page.locator('[name=body]').fill('存储不可用时保留输入');
    assert.match(await page.getByRole('status').textContent(), /暂时无法保存/);
    assert.equal(await page.locator('[name=body]').inputValue(), '存储不可用时保留输入');
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

for (const variant of ['typescript', 'built']) test(`${variant}: community draft cleanup binds submitted values, owner, recovery and composition`, async () => {
  const browser = await chromium.launch({headless: true});
  try {
    const page = await browser.newPage(), errors = []; page.on('pageerror', error => errors.push(error.message));
    const html = '<div data-questions data-user-id="7"><form data-qa-command="create"><input name="title" maxlength="200"><textarea name="body" maxlength="20000"></textarea><button>发布</button></form></div>';
    await page.route('https://example.test/**', route => new URL(route.request().url()).pathname === '/api/v1/social/session' ? route.fulfill({json: {authenticated: true, configured: true, id: 8, csrf_token: 'other'}}) : route.fulfill({contentType: 'text/html; charset=utf-8', body: html}));
    const script = variant === 'typescript' ? {content: source} : {path: path.resolve('internal/coreui/assets/js/newpress-community-compose.js')};
    const mount = () => page.addScriptTag(script), key = 'newpress-compose-v1:7:/questions/new/:create::';
    const stored = () => page.evaluate(key => JSON.parse(sessionStorage.getItem(key) || 'null'), key);
    const saved = (values, owner = 7) => page.locator('form').evaluate((form, detail) => form.dispatchEvent(new CustomEvent('community:saved', {detail})), {owner, values});
    await page.goto('https://example.test/questions/new/'); await mount();
    await page.locator('[name=body]').fill('提交时正文'); const submitted = await stored();
    await page.locator('[name=body]').fill('保存期间的新正文'); await saved(submitted); assert.equal((await stored()).body, '保存期间的新正文');
    await saved(await stored(), 8); assert.equal((await stored()).body, '保存期间的新正文');
    await page.reload(); await mount(); await page.locator('[name=body]').fill('另一份当前输入');
    await saved({title: '', body: '另一份当前输入'}); assert.equal((await stored()).body, '保存期间的新正文');
    await page.getByRole('button', {name: '删除草稿'}).click();
    await page.locator('[name=body]').dispatchEvent('compositionstart'); await page.locator('[name=body]').fill('输入法组合文字'); assert.equal(await stored(), null);
    await page.locator('[name=body]').dispatchEvent('compositionend'); assert.equal((await stored()).body, '输入法组合文字');
    await saved({title: '', body: '输入法组合文字'}); assert.equal(await stored(), null);
    await page.evaluate(key => sessionStorage.setItem(key, JSON.stringify({title: '保留损坏记录', body: 42, csrf: 'never-restore'})), key);
    await page.reload(); await mount(); await page.locator('[name=body]').fill('不能覆盖损坏的旧记录');
    await saved({title: '', body: '不能覆盖损坏的旧记录'}); assert.equal((await stored()).body, 42);
    await page.getByRole('button', {name: '删除草稿'}).click(); await page.locator('[name=body]').fill('旧账号的草稿');
    await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pagehide', {persisted: true})));
    await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', {persisted: true})));
    await page.waitForFunction(() => document.querySelector('[name=body]').value === '');
    assert.equal((await stored()).body, '旧账号的草稿'); assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});
