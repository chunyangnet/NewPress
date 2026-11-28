// Actual application pages from TestThemeSiteBrowserFixture. Writes and external
// calls are blocked; the Go fixture checks disposable state again on explicit stop.
import assert from 'node:assert/strict';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';

const directory = resolve(process.argv[2]);
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const engines = { chromium, firefox, webkit };
const selected = process.argv[3] && process.argv[3] !== 'all' ? [process.argv[3]] : Object.keys(engines);
const only = process.argv[4];
const evidence = [];
const base = new URL(fixture.base).origin;
const primary = new Set(['home', 'article', 'account', 'product', 'question']);
const appearance = [['light', 1280], ['dark', 390], ['dark', 1280], ['light', 390]];
const styles = fixture.theme === 'chunnel' ? ['classic', 'liquid-glass'] : ['default'];
const scenarios = styles.flatMap(style => appearance.map(([color, width]) => ({ style, color, width })));
const selectStyle = async style => {
  if (style === 'default') return;
  const response = await fetch(base + '/__theme_site/style/' + style, { method: 'POST' });
  assert.equal(response.status, 204, 'isolated fixture style must change successfully');
};
await mkdir(directory, { recursive: true });

for (const engine of selected) {
  const browser = await engines[engine].launch();
  const errors = [], failedAssets = [], writes = [], foreign = [], metrics = [];
  let context, page, stage = 'initialization';
  try {
    for (const { style, color, width } of scenarios) {
      if (only === 'smoke' && (color !== 'light' || width !== 1280)) continue;
      await selectStyle(style);
      context = await browser.newContext({ viewport: { width, height: 960 }, reducedMotion: 'reduce', colorScheme: color });
      await context.addInitScript(color => {
        if (window !== window.top) return;
        localStorage.setItem('newpress.colorMode', color);
        localStorage.setItem('docshub:mode', color);
      }, color);
      page = await context.newPage();
      page.setDefaultTimeout(15000);
      page.on('pageerror', error => errors.push({ stage, message: error.message }));
      page.on('response', response => {
        if (response.status() >= 400 && ['script', 'stylesheet', 'font', 'image'].includes(response.request().resourceType())) {
          failedAssets.push({ stage, status: response.status(), url: response.url() });
        }
      });
      await context.route('**/*', async route => {
        const request = route.request(), url = new URL(request.url());
        if (!['GET', 'HEAD'].includes(request.method())) {
          writes.push({ stage, method: request.method(), path: url.pathname });
          return route.fulfill({ status: 409, contentType: 'application/json', body: '{"error":"isolated read-only acceptance"}' });
        }
        if (url.origin !== base) {
          foreign.push({ stage, url: request.url(), type: request.resourceType() });
          return route.abort('blockedbyclient');
        }
        return route.continue();
      });
      let currentRole;
      const login = async role => {
        if (role === currentRole) return;
        await context.clearCookies();
        if (role) assert.equal((await context.request.get(base + '/__theme_site/login/' + role)).status(), 204, 'Fixture login must still be valid');
        currentRole = role;
      };
      const visit = async entry => {
        stage = `${fixture.theme} ${style} ${engine} ${width} ${color} ${entry.key}`;
        await login(entry.role || '');
        // The admin shell keeps a status stream open. Its loaded DOM and editor
        // readiness are the acceptance conditions, not an idle connection pool.
        const response = await page.goto(base + entry.path, { waitUntil: entry.path.startsWith('/admin/') ? 'load' : 'networkidle' });
        if (entry.key === 'editor') await page.locator('.ProseMirror[contenteditable="true"]').waitFor();
        assert.equal(response.status(), entry.status, stage + ' HTTP status');
        assert.equal(await page.locator('main').count(), 1, stage + ' main landmark');
        assert.equal(await page.locator('main').isVisible(), true, stage + ' main visibility');
        if (entry.marker) assert.ok((await page.locator('main').innerText()).includes(entry.marker), stage + ' actual content');
        if (entry.role !== 'owner') assert.equal((await page.locator('body').innerText()).includes('PRIVATE-THEME-DRAFT'), false, stage + ' draft privacy');
        const geometry = await page.evaluate(() => {
          const overflow = document.documentElement.scrollWidth > innerWidth + 2;
          const offenders = overflow ? [...document.querySelectorAll('body *')].filter(node => {
            const r = node.getBoundingClientRect();
            return r.width > 0 && r.height > 0 && (r.right > innerWidth + 2 || r.left < -2) && getComputedStyle(node).position !== 'fixed';
          }).slice(0, 16).map(node => ({ tag: node.tagName, class: node.className?.baseVal ?? node.className, rect: node.getBoundingClientRect().toJSON() })) : [];
          return { width: innerWidth, scrollWidth: document.documentElement.scrollWidth, overflow, offenders,
            theme: document.documentElement.dataset.theme, mode: document.documentElement.dataset.mode,
            style: document.documentElement.dataset.style };
        });
        metrics.push({ stage, ...geometry });
        assert.equal(geometry.overflow, false, stage + ' page overflow: ' + JSON.stringify(geometry.offenders));
        const scripts = await page.locator('script[src]').evaluateAll(nodes => nodes.map(node => node.src));
        assert.equal(scripts.some(url => /(?:react|echarts|newpress-analytics)/i.test(url)), false, stage + ' optional analytics leakage');
        assert.equal(new Set(scripts).size, scripts.length, stage + ' duplicate scripts');
        if (fixture.theme === 'docshub' && !entry.path.startsWith('/admin/')) assert.equal(geometry.mode, color, stage + ' actual color preference');
        if (fixture.theme === 'chunnel' && !entry.path.startsWith('/admin/')) {
          assert.equal(geometry.style, style, stage + ' actual visual style');
          assert.equal(geometry.theme, color, stage + ' actual color preference');
        }
        if (primary.has(entry.key)) {
          await page.evaluate(() => document.fonts.ready);
          await page.screenshot({ path: resolve(directory, `${engine}-${style === 'default' ? '' : style + '-'}${entry.key}-${width}-${color}.png`) });
        }
        evidence.push({ theme: fixture.theme, style, engine, width, color, page: entry.key, status: response.status() });
      };
      const pages = fixture.pages;
      for (const entry of pages) await visit(entry);
      console.log(`${fixture.theme} ${style} ${engine} ${width} ${color}: ${pages.length} actual page routes passed.`);
      if (only !== 'smoke') {
        await visit(fixture.pages.find(entry => entry.key === 'article'));
        const article = page.locator('#article-body, [data-article-body]').first();
        assert.equal(await article.isVisible(), true);
        const toc = page.locator('.reader-directory a[href^="#"], [data-toc-list] a, [data-reading-toc-list] a');
        assert.ok(await toc.count() >= 3, stage + ' generated reading TOC');
        const tabs = page.locator('[data-np-widget="component"] [role="tab"], .npw-tablist [role="tab"]');
        if (await tabs.count()) {
          const second = tabs.nth(1);
          await second.click();
          assert.equal(await second.getAttribute('aria-selected'), 'true');
          await page.getByText('第二组实际正文', { exact: true }).waitFor({ state: 'visible' });
          await second.press('ArrowLeft');
          assert.equal(await tabs.first().getAttribute('aria-selected'), 'true');
        } else throw Error(stage + ': rendered article tabs missing');
        const reply = page.locator('[data-comment-reply], [data-reply-id]').first();
        await reply.click();
        assert.equal(await page.locator('[data-comment-form] [name="parent_id"]').inputValue(), String(fixture.comment_id));
        await page.locator('[data-comment-reply-cancel], [data-reply-cancel]').filter({ visible: true }).first().click();
        assert.equal(await page.locator('[data-comment-form] [name="parent_id"]').inputValue(), '0');
        evidence.push({ theme: fixture.theme, style, engine, width, color, interaction: 'actual article TOC, tabs, keyboard and comment reply/cancel' });
        if (width < 760) {
          const menu = page.locator('[data-menu-toggle], [data-nav-toggle]').filter({ visible: true }).first();
          await menu.click();
          assert.equal(await menu.getAttribute('aria-expanded'), 'true');
          await page.keyboard.press('Escape');
          assert.equal(await menu.getAttribute('aria-expanded'), 'false');
          assert.equal(await menu.evaluate(node => node === document.activeElement), true);
          evidence.push({ theme: fixture.theme, style, engine, width, color, interaction: 'mobile navigation opens, Escape closes and restores focus' });
        }
        await page.evaluate(() => scrollTo(0, 0));
        if (fixture.theme === 'nexora') {
          await page.locator('[data-search-open]').filter({ visible: true }).first().click();
          await page.locator('#site-search[open]').waitFor();
        }
        if (fixture.theme === 'chunnel' && !await page.locator('#header-search input[name="q"]').isVisible()) {
          await page.locator('[data-focus-search]').filter({ visible: true }).first().click();
        }
        const query = page.locator('form[role="search"] input[name="q"], #site-search input[name="q"]').filter({ visible: true }).first();
        await query.fill('整站验收文章 00');
        await Promise.all([page.waitForURL(url => url.pathname === '/explore/' && url.searchParams.get('q') === '整站验收文章 00'), query.press('Enter')]);
        await page.waitForLoadState('networkidle');
        // Chunnel keeps recommended articles beside the filtered collection.
        const results = page.locator(fixture.theme === 'chunnel' ? '[data-server-results]' : 'main');
        assert.ok((await results.innerText()).includes('整站验收文章 00'));
        assert.equal(await results.locator('a[href="/posts/theme-site-1"]').count(), 0, 'exact title search excludes other result titles');
        evidence.push({ theme: fixture.theme, style, engine, width, color, interaction: 'actual search form submits Chinese query and displays matching article' });
      }
      await context.close(); context = undefined;
    }
    if (only !== 'smoke') {
      for (const style of styles) {
        await selectStyle(style);
        context = await browser.newContext({ javaScriptEnabled: false, viewport: { width: 390, height: 960 } });
        await context.route('**/*', route => new URL(route.request().url()).origin === base ? route.continue() : route.abort());
        page = await context.newPage();
        for (const key of ['home', 'article', 'search']) {
          stage = `${fixture.theme} ${style} ${engine} no-JS ${key}`;
          const entry = fixture.pages.find(entry => entry.key === key);
          const response = await page.goto(base + entry.path);
          assert.equal(response.status(), 200);
          assert.equal(await page.locator('main').isVisible(), true);
          if (entry.marker) assert.ok((await page.locator('main').innerText()).includes(entry.marker));
          if (fixture.theme === 'chunnel') assert.equal(await page.locator('html').getAttribute('data-style'), style);
          evidence.push({ theme: fixture.theme, style, engine, page: key, javascript: false });
        }
        await context.close(); context = undefined;
      }
    }
    assert.deepEqual(errors, [], 'browser exceptions');
    assert.deepEqual(failedAssets, [], 'local resources must load');
    assert.deepEqual(writes, [], 'read and navigation flows must not issue business writes');
  } catch (error) {
    console.error(stage, error.message);
    if (page && !page.isClosed()) await page.screenshot({ path: resolve(directory, `${engine}-failure.png`) }).catch(() => {});
    throw error;
  } finally {
    await writeFile(resolve(directory, `site-${engine}${only ? '-' + only : ''}.json`), JSON.stringify({ theme: fixture.theme, stage, evidence: evidence.filter(row => row.engine === engine), errors, failedAssets, writes, foreign, metrics }, null, 2) + '\n');
    if (context) await context.close();
    await browser.close();
  }
}
await writeFile(resolve(directory, `site-acceptance${only ? '-' + only : ''}.json`), JSON.stringify({ evidence }, null, 2) + '\n');
