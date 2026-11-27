const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');
const { compile } = require('./helpers/typescript.cjs');

const root = path.resolve(__dirname, '../..');
const origin = 'http://appearance.test';
const asset = name => path.join(root, 'internal/coreui/assets', name);
const control = fs.readFileSync(path.join(root, 'internal/coreui/templates/partials/color-mode.html'), 'utf8')
  .replace('{{define "color-mode"}}', '').replace('{{end}}', '')
  .replace('{{asset "js/chunnel-color-reveal.js"}}', '/assets/js/chunnel-color-reveal.js')
  .replace(/{{template "icon" "(sun|moon)"}}/g, '<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="7"/></svg>');

for (const entry of ['TypeScript', 'generated']) {
  test(`${entry}: color-mode controls in Chromium`, async t => {
    let browser;
    try { browser = await chromium.launch(); }
    catch (error) {
      if (/Executable doesn't exist/.test(error.message)) return t.skip('Install the Playwright browser to run the interaction tests');
      throw error;
    }
    t.after(() => browser.close());
    const compiled = entry === 'TypeScript' ? {
      'chunnel-color-mode': compile('frontend/shared/color-mode.ts'),
      'chunnel-color-reveal': compile('frontend/shared/color-reveal.ts'),
    } : {};

    async function fixture(scenario, { saved, width = 1000, colorScheme = 'light', reducedMotion = 'no-preference', blocked = false, unsupported = false } = {}) {
      const context = await browser.newContext({ viewport: { width, height: 700 }, colorScheme, reducedMotion });
      scenario.after(() => context.close());
      const requests = [], errors = [];
      context.on('request', request => requests.push(new URL(request.url()).pathname));
      context.on('page', page => page.on('pageerror', error => errors.push(error.message)));
      scenario.after(() => assert.deepEqual(errors, []));
      if (saved !== undefined || blocked || unsupported) await context.addInitScript(({ saved, blocked, unsupported }) => {
        if (location.origin !== 'http://appearance.test') return;
        if (saved !== undefined) localStorage.setItem('newpress.colorMode', saved);
        if (blocked) {
          Storage.prototype.getItem = Storage.prototype.setItem = () => { throw new DOMException('Storage blocked', 'SecurityError'); };
        }
        if (unsupported) document.startViewTransition = undefined;
      }, { saved, blocked, unsupported });
      await context.route(origin + '/**', route => {
        const url = new URL(route.request().url());
        if (url.pathname === '/' || url.pathname.startsWith('/home-')) {
          const script = url.pathname === '/home-classic' ? 'chunnel-home' : url.pathname === '/home-glass' ? 'chunnel-home-glass' : 'chunnel-color-mode';
          return route.fulfill({ contentType: 'text/html; charset=utf-8', body: `<!doctype html><html lang="zh-CN" data-style="classic"><head>
            <meta name="theme-color" content="#f7f9fc"><title>外观设置</title><script src="/assets/js/${script}.js" ${script.startsWith('chunnel-home') ? 'data-home-entry' : ''}></script>
            <script>window.earlyAppearance={theme:document.documentElement.dataset.theme,controls:document.querySelectorAll('[data-color-mode-control]').length};</script>
            <link rel="stylesheet" href="/assets/css/chunnel-color-mode.css">
            <style>:root{--white:#fff;--paper:#f7f9fc;--ink:#17253d;--muted:#63718a;--line:#dbe0e8;--primary:#2563eb;--primary-readable:#2563eb}body{margin:32px;font:16px system-ui;background:var(--paper);color:var(--ink)}main{max-width:640px;margin:auto}header{display:flex;align-items:center;justify-content:space-between;gap:24px}article{margin-top:24px;padding:24px;border:1px solid var(--line);background:var(--white);border-radius:12px}h1{font-size:24px}svg{fill:none;stroke:currentColor;stroke-width:2}</style>
            </head><body><main><header><h1>外观设置</h1>${control}</header><article><h2>跟随你的阅读习惯</h2><p>浅色与深色模式共用页面内容和交互。</p><button id="after-control">继续阅读</button></article></main></body></html>` });
        }
        const match = /^\/assets\/(js|css)\/(chunnel-color-mode|chunnel-color-reveal|chunnel-home|chunnel-home-glass)\.(js|css)$/.exec(url.pathname);
        if (!match) return route.abort();
        if (match[1] === 'js' && compiled[match[2]]) return route.fulfill({ body: compiled[match[2]], contentType: 'text/javascript; charset=utf-8' });
        const file = match[2].startsWith('chunnel-home') ? path.join(root, 'themes/chunnel/assets/js', match[2] + '.js') : asset(`${match[1]}/${match[2]}.${match[3]}`);
        return route.fulfill({ path: file, contentType: match[1] === 'js' ? 'text/javascript; charset=utf-8' : 'text/css; charset=utf-8' });
      });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const waitTheme = (target, theme) => target.waitForFunction(value => document.documentElement.dataset.theme === value, theme);
      const go = async (target = page, pathname = '/') => { await target.goto(origin + pathname); await target.locator('[data-color-mode-control]').waitFor(); };
      const screenshot = async name => {
        const directory = process.env.NEWPRESS_COLOR_AUDIT_DIR;
        if (!directory || entry !== 'generated') return;
        fs.mkdirSync(directory, { recursive: true });
        await page.screenshot({ path: path.join(directory, name + '.png'), fullPage: true });
      };
      return { page, context, requests, go, waitTheme, screenshot };
    }

    await t.test('saved appearance is synchronous before styles and does not preload the reveal', async scenario => {
      const f = await fixture(scenario, { saved: 'dark' }); await f.go();
      assert.deepEqual(await f.page.evaluate(() => window.earlyAppearance), { theme: 'dark', controls: 0 });
      assert.equal(await f.page.locator('meta[name="theme-color"]').getAttribute('content'), '#111721');
      assert.equal(await f.page.getByRole('button', { name: '深色模式' }).getAttribute('aria-pressed'), 'true');
      assert.equal(f.requests.some(url => url.includes('reveal')), false);
      await f.screenshot('dark-desktop');
    });

    await t.test('real storage events synchronize two tabs and deletion restores the system preference', async scenario => {
      const f = await fixture(scenario); await f.go();
      const second = await f.context.newPage(); await f.go(second);
      await f.page.emulateMedia({ colorScheme: 'dark' }); await f.waitTheme(f.page, 'dark');
      await f.page.evaluate(() => window.ChunnelColorMode.set('dark')); await f.waitTheme(second, 'dark');
      await second.emulateMedia({ colorScheme: 'dark' });
      await second.evaluate(() => window.ChunnelColorMode.set('light')); await f.waitTheme(f.page, 'light');
      await f.page.evaluate(() => localStorage.removeItem('newpress.colorMode')); await f.waitTheme(second, 'dark');
      assert.equal(await second.evaluate(() => window.ChunnelColorMode.mode), 'system');
    });

    await t.test('keyboard focus prepares one reveal and actual transitions settle on the last choice', async scenario => {
      const f = await fixture(scenario); await f.go();
      await f.page.keyboard.press('Tab');
      await f.page.waitForFunction(() => typeof window.ChunnelColorReveal === 'function');
      assert.equal(await f.page.evaluate(() => document.activeElement.dataset.colorMode), 'light');
      await f.page.keyboard.press('Tab'); await f.page.keyboard.press('Enter'); await f.waitTheme(f.page, 'dark');
      await f.page.evaluate(() => {
        document.querySelector('button[data-color-mode="light"]').click();
        document.querySelector('button[data-color-mode="dark"]').click();
      });
      await f.waitTheme(f.page, 'dark');
      await f.page.waitForFunction(() => document.getAnimations().length === 0);
      assert.equal(await f.page.evaluate(() => localStorage.getItem('newpress.colorMode')), 'dark');
      assert.equal(f.requests.filter(url => url.includes('reveal')).length, 1);
    });

    await t.test('a narrow keyboard toggle returns focus to the visible choice with reduced motion', async scenario => {
      const f = await fixture(scenario, { width: 390, reducedMotion: 'reduce' }); await f.go();
      await f.page.keyboard.press('Tab');
      assert.equal(await f.page.evaluate(() => document.activeElement.dataset.colorMode), 'dark');
      await f.page.keyboard.press('Enter'); await f.waitTheme(f.page, 'dark');
      assert.equal(await f.page.evaluate(() => document.activeElement.dataset.colorMode), 'light');
      assert.equal(f.requests.some(url => url.includes('reveal')), false);
      await f.screenshot('dark-mobile-reduced-motion');
      await f.page.keyboard.press('Enter'); await f.waitTheme(f.page, 'light');
    });

    await t.test('a failed reveal request applies the choice and the next interaction retries', async scenario => {
      const f = await fixture(scenario); await f.page.route('**/chunnel-color-reveal.js', route => route.abort());
      await f.go(); await f.page.getByRole('button', { name: '深色模式' }).click(); await f.waitTheme(f.page, 'dark');
      assert.ok(f.requests.filter(url => url.includes('reveal')).length >= 1);
      await f.page.unroute('**/chunnel-color-reveal.js');
      await f.page.getByRole('button', { name: '浅色模式' }).click(); await f.waitTheme(f.page, 'light');
      await f.page.waitForFunction(() => typeof window.ChunnelColorReveal === 'function');
      await f.page.waitForFunction(() => document.getAnimations().length === 0);
    });

    await t.test('blocked storage and unsupported view transitions retain native buttons', async scenario => {
      const f = await fixture(scenario, { blocked: true, unsupported: true }); await f.go();
      await f.page.getByRole('button', { name: '深色模式' }).click(); await f.waitTheme(f.page, 'dark');
      await f.page.getByRole('button', { name: '浅色模式' }).click(); await f.waitTheme(f.page, 'light');
      assert.equal(f.requests.some(url => url.includes('reveal')), false);
    });

    if (entry === 'generated') await t.test('both homepage bundles apply saved appearance before styles', async scenario => {
      const f = await fixture(scenario, { saved: 'dark', reducedMotion: 'reduce' });
      for (const route of ['/home-classic', '/home-glass']) {
        await f.go(f.page, route);
        assert.deepEqual(await f.page.evaluate(() => window.earlyAppearance), { theme: 'dark', controls: 0 });
        await f.page.getByRole('button', { name: '浅色模式' }).click(); await f.waitTheme(f.page, 'light');
      }
      assert.equal(f.requests.some(url => url.includes('reveal')), false);
    });
  });
}
