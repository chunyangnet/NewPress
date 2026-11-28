// Isolated browser DOM fixtures: no running site, credentials or persistent writes.
import {chromium, firefox, webkit} from 'playwright';
import {readFile, mkdir, writeFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import {frontendEntries, compileFrontendEntry} from './frontend_entries.mjs';

const output = resolve(process.argv[2] || 'output/reading-themes-browser');
await mkdir(output, {recursive: true});
const results = [];
for (const mode of ['typescript', 'generated']) {
  const sources = new Map();
  for (const name of ['emoji-editor', 'emoji', 'questions', 'chunnel-reader', 'docshub', 'nexora', 'docshub-verification', 'nexora-verification']) {
    const entry = frontendEntries.find(entry => entry.name === name);
    sources.set(name, mode === 'typescript' ? (await compileFrontendEntry(name, {minify: false})).code : await readFile(entry.outputs[0].path, 'utf8'));
  }
  for (const [engine, launcher] of Object.entries({chromium, firefox, webkit})) {
    const browser = await launcher.launch({headless: true});
    const page = await browser.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const check = (condition, message) => { if (!condition) throw new Error(`${engine}/${mode}: ${message}`); };
    let html = '', requests = [];
    await page.route('http://localhost/**', async route => {
      const url = new URL(route.request().url());
      if (url.pathname.startsWith('/scripts/')) return route.fulfill({contentType: 'text/javascript', body: sources.get(url.pathname.slice(9, -3))});
      if (url.pathname.startsWith('/api/v1/verification/config')) return route.fulfill({json: {enabled: false, scene: 'comment', revision: 'fixture'}});
      if (url.pathname.startsWith('/api/v1/questions/')) {
        requests.push(route.request().postDataJSON());
        return route.fulfill({status: 503, json: {error: {message: '保留草稿重试'}}});
      }
      if (url.pathname.includes('/images/')) return route.fulfill({status: 204});
      return route.fulfill({contentType: 'text/html', body: `<!doctype html><meta charset="utf-8">${html}`});
    });
    const load = async (body, scripts) => {
      html = body + scripts.map(name => `<script src="/scripts/${name}.js"></script>`).join('');
      await page.goto('http://localhost/', {waitUntil: 'load'});
    };
    await load('<label>评论<textarea id="draft" data-comment-content rows="3"></textarea></label>', ['emoji-editor', 'emoji']);
    const editor = page.locator('.emoji-editor');
    const picker = page.locator('[data-emoji-popover]'), emojiTrigger = page.locator('[data-emoji-trigger]');
    await editor.fill('中文草稿');
    await emojiTrigger.click();
    await page.locator('[data-emoji-token="[得意]"]').first().click();
    check(await page.locator('#draft').inputValue() === '中文草稿[得意]', 'emoji must insert canonical text at the caret');
    await picker.waitFor({state: 'hidden'});
    check(await emojiTrigger.getAttribute('aria-expanded') === 'false', 'successful insertion must collapse the trigger');
    check(await editor.evaluate(node => node === document.activeElement), 'successful insertion must restore comment focus');
    await editor.press('ControlOrMeta+z');
    check(await page.locator('#draft').inputValue() === '中文草稿', 'emoji insertion must undo');
    await editor.press('ControlOrMeta+Shift+z');
    check((await page.locator('#draft').inputValue()).includes('[得意]'), 'emoji insertion must redo');
    await emojiTrigger.focus();
    await page.keyboard.press('Enter');
    await picker.waitFor({state: 'visible'});
    const recent = picker.locator('.emoji-recent [data-emoji-token="[得意]"]');
    await recent.focus();
    await page.keyboard.press('Enter');
    await picker.waitFor({state: 'hidden'});
    check(await page.locator('#draft').inputValue() === '中文草稿[得意][得意]', 'recent emoji must insert once at the restored caret');
    check(await editor.evaluate(node => node === document.activeElement), 'keyboard selection must restore comment focus');
    await page.evaluate(() => { document.querySelector('#draft').dataset.emojiMax = '1'; });
    await emojiTrigger.click();
    await picker.locator('[data-emoji-grid] [data-emoji-token="[得意]"]').click();
    check(await picker.isVisible(), 'rejected insertion must keep its explanation visible');
    check((await picker.locator('[role="status"]').textContent()).includes('最多输入'), 'rejected insertion must explain the length limit');
    check(await page.locator('#draft').inputValue() === '中文草稿[得意][得意]', 'rejected insertion must preserve the comment');
    await page.evaluate(() => { document.querySelector('#draft').disabled = true; });
    await page.waitForFunction(() => document.querySelector('[data-emoji-popover]').hidden);
    results.push({engine, mode, scenario: 'emoji auto-close/focus/recent keyboard selection/history/rejected insertion/owner cleanup'});

    await load('<main data-questions data-user-id="1" data-id="5" data-version="2" data-csrf="fixture"><p data-qa-status hidden></p><form data-qa-command="answer"><textarea name="content">中文回答</textarea><button>发送</button></form></main>', ['questions']);
    await page.locator('button').click();
    await page.waitForFunction(() => !document.querySelector('[data-questions]').hasAttribute('aria-busy'));
    await page.locator('button').click();
    await page.waitForFunction(() => !document.querySelector('[data-questions]').hasAttribute('aria-busy'));
    check(requests.length === 2 && requests[0].key === requests[1].key, 'question retry must preserve request key');
    check(await page.locator('textarea').inputValue() === '中文回答', 'question failure must preserve draft');
    results.push({engine, mode, scenario: 'question failed request and idempotent retry'});

    await load('<article id="article-body"><h2>第一章</h2><p>阅读正文</p><h3>小节</h3></article><aside class="article-rail"><details class="reader-directory"><nav data-reader-toc><a href="#article-body">正文</a></nav></details><span data-reader-chapters></span><span data-reader-time></span><div data-reader-summary></div><div data-reader-settings></div><span data-reader-size-label></span><button data-reader-size="-1">缩小</button><button data-reader-size="1">放大</button><button data-reader-focus><span>专注阅读</span></button><span data-reader-percent></span><div data-reader-meter></div></aside>', ['chunnel-reader']);
    check(await page.locator('[data-reader-toc] a').count() === 2, 'reader must build both headings');
    await page.locator('[data-reader-size="1"]').click();
    check(await page.locator('#article-body').getAttribute('data-reader-size') === '1', 'reader size');
    await page.locator('[data-reader-focus]').click();
    await page.keyboard.press('Escape');
    check(!await page.locator('body').evaluate(body => body.classList.contains('reader-focused')), 'escape must leave focused reading');
    results.push({engine, mode, scenario: 'Chunnel TOC, size and focus'});

    for (const theme of ['docshub', 'nexora']) {
      await load('<main><article data-article-body><h2>第一章</h2><p>中文正文</p><h2>第二章</h2></article><span data-reading-time></span><details data-reading-toc><ul data-reading-toc-list></ul></details><ul data-toc-list></ul><form data-comment-form><input name="parent_id" value="0"><textarea name="content"></textarea><div data-reply-context><span data-reply-label></span><button type="button" data-reply-cancel>取消</button></div></form><button data-comment-reply="7" data-reply-id="7" data-reply-author="读者">回复</button></main>', [theme]);
      await page.locator('[data-comment-reply]').click();
      check(await page.locator('[name=parent_id]').inputValue() === '7', `${theme} reply`);
      await page.locator('[data-reply-cancel]').click();
      check(await page.locator('[name=parent_id]').inputValue() === '0', `${theme} cancel reply`);
      check(await page.locator(theme === 'docshub' ? '[data-toc-list] a' : '[data-reading-toc-list] a').count() === 2, `${theme} TOC`);
      results.push({engine, mode, scenario: `${theme} reading and reply`});
      await load('<form id="owner"><button>提交</button></form>', [`${theme}-verification`]);
      const token = await page.evaluate(async theme => Promise.race([
        window[theme === 'docshub' ? 'DocsHubVerification' : 'NexoraVerification'].getToken(document.querySelector('#owner'), 'comment'),
        new Promise((_, reject) => setTimeout(() => reject(new Error('Verification fixture timeout')), 5000)),
      ]), theme);
      check(token === '', `${theme} disabled verification`);
      check(await page.locator('.verification-box').count() === 0, `${theme} verification cleanup`);
      results.push({engine, mode, scenario: `${theme} verification disabled and cleanup`});
    }
    check(errors.length === 0, errors.join('\n'));
    await browser.close();
  }
}
await writeFile(resolve(output, 'summary.json'), JSON.stringify(results, null, 2));
console.log(`${results.length} browser scenarios passed`);
