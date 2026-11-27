const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { chromium } = require('playwright');

test('bulk action selected text stays readable for publish and danger states', async () => {
  const browser = await chromium.launch({headless: true});
  try {
    const page = await browser.newPage();
    const css = ['chunnel-admin-components.source', 'chunnel-selects'].map(name => fs.readFileSync(`internal/coreui/assets/css/${name}.css`, 'utf8')).join('\n');
    await page.setContent(`<html data-choice-controls><head><style>:root{--ink:#27364b;--fluent-text:#27364b;--white:#fff;--admin-blue:#06f} ${css}</style></head><body class="admin-workbench"><div class="admin-content-bulkbar"><div class="admin-content-bulk-controls"><label><select><option value="draft">恢复 / 设为草稿</option><option value="publish">立即发布</option><option value="private">设为私密</option><option value="delete">永久删除</option></select></label><button>应用</button></div></div></body></html>`);
    await page.addScriptTag({path: 'internal/coreui/assets/js/chunnel-selects.js'});
    for (const value of ['draft', 'publish', 'private', 'delete']) {
      await page.selectOption('select', value);
      await page.evaluate(value => { document.querySelector('.admin-content-bulkbar').className = 'admin-content-bulkbar ' + (value === 'publish' ? 'is-publish' : value === 'draft' ? '' : 'is-danger'); }, value);
      const result = await page.locator('selectedcontent').evaluate(el => ({text: el.textContent, color: getComputedStyle(el).color, width: el.getBoundingClientRect().width}));
      assert.ok(result.text.trim(), value);
      assert.equal(result.color, 'rgb(39, 54, 75)', value);
      assert.ok(result.width > 0, value);
    }
  } finally { await browser.close(); }
});
