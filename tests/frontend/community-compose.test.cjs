const { test } = require('node:test');
const assert = require('node:assert/strict');
const { chromium } = require('playwright');
const path = require('node:path');

test('community composer retains drafts, protects pending recovery and preserves filter context', async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 }, reducedMotion: 'reduce' });
    const errors = []; page.on('pageerror', error => errors.push(error.message));
    await page.route('https://example.test/**', route => route.fulfill({ contentType: 'text/html', body: '<div data-questions data-user-id="7"><form class="qa-filters"></form><form data-qa-command="create"><label>标题<input name="title"></label><label>正文<textarea name="body" maxlength="20000"></textarea></label><button>发布</button></form></div>' }));
    const mount = async () => page.addScriptTag({ path: path.resolve('internal/coreui/assets/js/newpress-community-compose.source.js') });
    await page.goto('https://example.test/questions/new/?tag=design&scope=mine&page=3'); await mount();
    const href = await page.getByRole('link', { name: '还没有回答' }).getAttribute('href');
    assert.match(href, /tag=design/); assert.match(href, /scope=mine/); assert.match(href, /status=unanswered/); assert.doesNotMatch(href, /page=/);
    const tabs = await browser.newPage();
    try {
      await tabs.route('https://example.test/**', route => route.fulfill({ contentType: 'text/html; charset=utf-8', body: '<div data-questions data-user-id="0"><nav class="channel-tabs" aria-label="问题状态"><a href="/questions/" aria-current="page">全部问题</a><a href="/questions/?status=open">等待解答</a><a href="/questions/?status=solved">已解决</a></nav><form class="qa-filters"></form></div>' }));
      await tabs.goto('https://example.test/questions/?tag=design&scope=mine&status=unanswered&page=3');
      await tabs.addScriptTag({ path: path.resolve('internal/coreui/assets/js/newpress-community-compose.source.js') });
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
    await page.locator('[data-qa-command]').evaluate(form => form.dispatchEvent(new Event('community:saved')));
    await page.reload(); await mount();
    assert.equal(await page.getByRole('button', { name: '恢复草稿' }).isVisible(), false);
    await page.setContent('<div data-circle-page data-user-id="7"><form class="circle-topic-filters"><select name="filter"><option value="">全部</option></select></form><form data-circle-command="topic"><label>正文<textarea name="body" maxlength="10000"></textarea></label><div data-circle-editor><select name="format"><option value="markdown">Markdown</option></select><button type="button">预览正文</button></div></form></div>');
    await mount();
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
