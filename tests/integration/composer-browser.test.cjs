const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { chromium, firefox, webkit } = require('playwright');
const { compile } = require('../frontend/helpers/typescript.cjs');
const variants = {
  TypeScript: compile('frontend/core/composer.ts'),
  generated: fs.readFileSync('internal/coreui/assets/js/chunnel-composer.js', 'utf8'),
};
const componentLibrary = fs.readFileSync('internal/coreui/assets/js/chunnel-component-library.js', 'utf8');
const fixture = `<form data-content-editor>
  <div data-composer-toolbar hidden><button type="button" data-composer-mode="blocks">块编辑</button><button type="button" data-composer-mode="source">源码</button><button type="button" data-composer-undo>撤销</button><button type="button" data-composer-redo>重做</button></div>
  <div data-composer-source-pane><textarea data-content-source data-max-bytes="262144"></textarea></div>
  <div data-composer><b data-composer-count>0</b><div data-composer-list></div><button type="button" data-block-add="paragraph">添加段落</button><button type="button" data-block-add="table">添加表格</button><button type="button" data-block-add="html">添加 HTML</button></div>
  <p data-composer-feedback hidden></p></form>`;

for (const [engine, launcher] of Object.entries({ chromium, firefox, webkit })) {
  test(`${engine}: shop block editor shared TS preserves source and interactions`, { timeout: 120000 }, async t => {
    const browser = await launcher.launch({ headless: true });
    t.after(() => browser.close());
    for (const [variant, code] of Object.entries(variants)) await t.test(variant, async t => {
      const page = await browser.newPage(); t.after(() => page.close());
      const errors = []; page.on('pageerror', error => errors.push(error.message));
      page.setDefaultTimeout(7000);
      await page.route('https://newpress.test/composer-fixture', route => route.fulfill({ contentType: 'text/html; charset=utf-8', body: fixture }));
      await page.goto('https://newpress.test/composer-fixture');
      await page.addScriptTag({ content: componentLibrary }); await page.addScriptTag({ content: code });
      const source = () => page.locator('[data-content-source]').inputValue();
      const reset = value => page.evaluate(value => window.ChunnelBlockEditor.reset(value), value);
      await t.test('untouched, rich and active HTML roundtrip without mounting raw nodes', async () => {
        const originals = ['\n<p>原文 &amp; 空格</p>\n<!--备注--><div class="legacy">保留</div>\n', '<p class="rich"><b>格式</b></p>', '<p>broken<div>nest</div>', '<script>window.executed=true</script><img src="x" onerror="window.executed=true">', '<p>' + '中'.repeat(300) + '</p>'];
        for (const original of originals) {
          await reset(original);
          const result = await page.evaluate(value => { const api = window.ChunnelBlockEditor; return api.serialize(api.parse(value)); }, original);
          assert.equal(result, original); assert.equal(await source(), original);
        }
        assert.equal(await page.evaluate(() => !!window.executed), false);
        assert.equal(await page.locator('[data-composer-list] .legacy').count(), 0);
      });
      await t.test('typing, moving, copying, removal and undo preserve neighboring source', async () => {
        const original = '<p>第一段</p>\n<p>第二段</p>\n<!--备注-->';
        await reset(original);
        await page.locator('.admin-block-paragraph textarea').first().fill('修改 <内容>');
        assert.equal(await source(), '<p>修改 &lt;内容&gt;</p>\n<p>第二段</p>\n<!--备注-->');
        await page.locator('.admin-block-paragraph').first().getByRole('button', { name: '下移', exact: true }).click();
        assert.equal(await source(), '\n<p>第二段</p><p>修改 &lt;内容&gt;</p>\n<!--备注-->');
        await page.getByRole('button', { name: '撤销', exact: true }).click();
        assert.match(await source(), /^<p>修改/);
        await page.locator('.admin-block-paragraph').first().getByRole('button', { name: '复制', exact: true }).click();
        assert.equal(await page.locator('.admin-block-paragraph').count(), 3);
        await page.locator('.admin-block-paragraph').first().getByRole('button', { name: '删除', exact: true }).click();
        assert.equal(await page.locator('.admin-block-paragraph').count(), 2);
      });
      await t.test('successive table cells survive another edit and row/column operations', async () => {
        await reset(''); await page.getByRole('button', { name: '添加表格', exact: true }).click();
        const cells = page.locator('.admin-block-table textarea');
        await cells.nth(0).fill('首列'); await cells.nth(1).fill('次列'); await cells.nth(2).fill('首值');
        assert.match(await source(), /首列/); assert.match(await source(), /次列/); assert.match(await source(), /首值/);
        await page.getByRole('button', { name: '加一行', exact: true }).click();
        await page.getByRole('button', { name: '加一列', exact: true }).click();
        assert.equal(await page.locator('.admin-block-table tr').count(), 4);
        assert.equal(await page.locator('.admin-block-table tr').first().locator('th,td').count(), 3);
        assert.match(await source(), /首值/);
        await page.getByRole('button', { name: '撤销', exact: true }).click();
        assert.equal(await page.locator('.admin-block-table tr').first().locator('th,td').count(), 2);
      });
      await t.test('HTML insertion and mode switching keep raw source while delayed writes are rejected', async () => {
        await reset(''); await page.getByRole('button', { name: '添加 HTML', exact: true }).click();
        await page.locator('.admin-block-html textarea').fill('<div class="custom">原样 HTML</div>');
        await page.getByRole('button', { name: '源码', exact: true }).click();
        const before = await source();
        await page.evaluate(() => { window.captured = window.ChunnelBlockEditor.capture(); });
        await page.locator('[data-content-source]').fill(before + '<p>新增</p>');
        const refused = await page.evaluate(() => { try { window.ChunnelBlockEditor.insert('<p>过期</p>', window.captured); return false; } catch { return true; } });
        assert.equal(refused, true); assert.equal(await source(), before + '<p>新增</p>');
        await page.getByRole('button', { name: '块编辑', exact: true }).click();
        assert.equal(await source(), before + '<p>新增</p>');
      });
      await t.test('component duplication changes identity, source limits and locks reject insertions', async () => {
        const markup = await page.evaluate(() => window.NewPressComponentLibrary.markup(window.NewPressComponentLibrary.defaults('callout')));
        await reset(markup);
        await page.locator('.admin-block-component').getByRole('button', { name: '复制', exact: true }).click();
        const ids = await page.evaluate(() => window.ChunnelBlockEditor.parse(document.querySelector('[data-content-source]').value).blocks.map(block => block.config.id));
        assert.equal(ids.length, 2); assert.notEqual(ids[0], ids[1]);
        const before = await source();
        const limit = await page.evaluate(() => { const api = window.ChunnelBlockEditor; try { api.insert('中'.repeat(100000), api.capture()); return ''; } catch (error) { return error.message; } });
        assert.match(limit, /256 KiB/); assert.equal(await source(), before);
        await page.evaluate(() => { document.querySelector('[data-content-source]').readOnly = true; });
        const lock = await page.evaluate(() => { const api = window.ChunnelBlockEditor; try { api.insert('<p>锁定</p>', api.capture()); return false; } catch { return true; } });
        assert.equal(lock, true); assert.equal(await source(), before);
      });
      assert.deepEqual(errors, []);
    });
  });
}
