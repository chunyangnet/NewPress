// Opt-in acceptance against TestFeatureControlsBrowserFixture's real Go pages.
// Search responses are controlled; all other pages/assets come from the fixture.
import assert from 'node:assert/strict';
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';

const directory = resolve(process.argv[2]);
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const engines = { chromium, firefox, webkit };
const evidence = [];
const selected = process.argv[3] ? [process.argv[3]] : Object.keys(engines);
for (const name of selected) {
  const browser = await engines[name].launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: 'reduce' });
  const page = await context.newPage();
  page.setDefaultTimeout(12000);
  const errors = [], scripts = [], writes = [], queries = [], layouts = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => {
    if (request.resourceType() === 'script') scripts.push(new URL(request.url()).pathname);
    if (!['GET', 'HEAD'].includes(request.method())) writes.push(request.method() + ' ' + request.url());
  });
  const check = label => { evidence.push({ browser: name, check: label }); console.log(`${name}: ${label}`); };
  const visit = async path => { await page.goto(fixture.url + path); await page.waitForLoadState('networkidle'); };
  const settled = () => page.evaluate(async () => {
    await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    // A theme change may start color transitions after the first layout frame.
    // Wait for the inspected colors and geometry to stabilize; unrelated caret
    // or decorative animations do not determine screenshot readiness.
    const sample = () => JSON.stringify([...document.querySelectorAll('.series-admin, .series-admin h1, .series-admin label, .admin-save-dock, [data-header]')]
      .map(node => { const style = getComputedStyle(node), rect = node.getBoundingClientRect();
        return [style.color, style.backgroundColor, rect.left, rect.top, rect.width, rect.height]; }));
    const deadline = performance.now() + 2000;
    let previous = sample(), stable = 0;
    while (stable < 3) {
      if (performance.now() > deadline) throw new Error('Inspected page styles did not settle');
      await new Promise(resolve => setTimeout(resolve, 60));
      const next = sample(); stable = next === previous ? stable + 1 : 0; previous = next;
    }
  });
  let late;
  await page.route('**/api/v1/posts?**', async route => {
    const term = new URL(route.request().url()).searchParams.get('q');
    if (!term) return route.continue();
    queries.push(term);
    const fulfill = () => route.fulfill({ contentType: 'application/json', body: JSON.stringify({
      items: [{ slug: 'browser-story', title: term + '：前端交互验收', author_name: '验收' }], total: 1,
    }) });
    if (term === '迟到') { late = fulfill; return; }
    if (term === '失败') return route.fulfill({ status: 503, contentType: 'application/json', body: '{}' });
    return fulfill();
  });
  try {
    assert.equal((await context.request.post(fixture.url + '/fixture/style/classic')).status(), 204);
    await visit('/');
    assert.deepEqual(await page.evaluate(() => [typeof window.ChunnelScroll.keep, typeof window.ChunnelColorMode.set]), ['function', 'function']);
    assert.equal(scripts.some(path => /chunnel-home-(search|features)\.js/.test(path)), false);
    assert.equal(await page.locator('[data-cookie-consent]').isVisible(), true);
    assert.equal(await page.evaluate(() => window.NewPressCookieConsent.allowsAnalytics()), false);
    check('Initial classic homepage initializes shared controls without downloading interactive features or quick search.');

    await page.locator('[data-cookie-consent] [data-cookie-reject]').click();
    await page.reload(); await page.waitForLoadState('networkidle');
    assert.equal(await page.locator('[data-cookie-consent]').isVisible(), false);
    await page.locator('footer [data-cookie-open]').click();
    await page.locator('[data-cookie-analytics]').check();
    await page.locator('[data-cookie-save]').click();
    assert.equal(await page.evaluate(() => window.NewPressCookieConsent.allowsAnalytics()), true);
    const other = await context.newPage(); await other.goto(fixture.url + '/');
    await other.waitForFunction(() => window.NewPressCookieConsent?.allowsAnalytics());
    await page.locator('footer [data-cookie-open]').click();
    await page.locator('[data-cookie-dialog] [data-cookie-reject]').click();
    await other.waitForFunction(() => !window.NewPressCookieConsent.allowsAnalytics());
    await other.close();
    assert.equal(await page.locator('footer [data-cookie-open]').evaluate(node => node === document.activeElement), true);
    check('Cookie rejection persists, customization restores focus, and another tab receives revocation.');

    await page.evaluate(() => scrollTo(0, 0)); await settled();
    await page.keyboard.press('/');
    const input = page.locator('[data-search-input]');
    await page.waitForFunction(() => window.ChunnelSearchReady === true);
    const requestsBefore = queries.length;
    await input.evaluate(node => {
      node.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }));
      node.value = 'zhongwen';
      node.dispatchEvent(new InputEvent('input', { bubbles: true, isComposing: true }));
    });
    await page.waitForTimeout(300);
    assert.equal(queries.length, requestsBefore);
    await input.evaluate(node => {
      node.value = '中文'; node.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true, data: '中文' }));
      node.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertFromComposition' }));
    });
    await page.locator('.search-preview-result').waitFor();
    const scrollBefore = await page.evaluate(() => scrollY);
    await page.keyboard.press('ArrowDown');
    assert.equal(await input.getAttribute('aria-activedescendant'), 'quick-search-option-0');
    assert.equal(await page.evaluate(() => scrollY), scrollBefore);
    await page.keyboard.press('Escape');
    assert.equal(await input.inputValue(), '中文');
    assert.equal(await input.getAttribute('aria-expanded'), 'false');
    check('Quick search waits for composition commit, supports keyboard selection, and preserves scroll and text on Escape.');

    await page.keyboard.press('/'); await input.fill('迟到');
    await page.waitForRequest(request => new URL(request.url()).searchParams.get('q') === '迟到');
    await page.keyboard.press('Escape');
    if (late) await late().catch(() => {}); await settled();
    assert.equal(await input.getAttribute('aria-expanded'), 'false');
    await page.keyboard.press('/'); await input.fill('失败');
    await page.locator('.search-preview-retry').waitFor();
    assert.match(await page.locator('.search-preview-status').textContent(), /暂时无法加载/);
    assert.match(await page.locator('.search-preview-footer a').getAttribute('href'), /q=/);
    await page.keyboard.press('Escape');
    check('Dismissed late responses stay hidden; server failures retain a retry and full-search fallback.');

    await page.setViewportSize({ width: 390, height: 844 }); await page.evaluate(() => scrollTo(0, 0));
    await page.locator('[data-nav-toggle]').click();
    assert.equal(await page.locator('[data-nav-toggle]').getAttribute('aria-expanded'), 'true');
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('[data-nav-toggle]').getAttribute('aria-expanded'), 'false');
    assert.equal((await context.request.post(fixture.url + '/fixture/style/liquid-glass')).status(), 204);
    await visit('/'); await page.locator('.mobile-bottom-nav .nav-lens').waitFor();
    await page.evaluate(() => {
      window.__navActivations = [];
      document.addEventListener('click', event => {
        if (event.target instanceof Element && event.target.closest('.mobile-bottom-nav a')) {
          window.__navActivations.push(event.target.closest('a').getAttribute('href')); event.preventDefault();
        }
      }, true);
    });
    const links = page.locator('.mobile-bottom-nav a:not(.mobile-create)');
    const first = await links.nth(0).boundingBox(), second = await links.nth(1).boundingBox();
    assert.ok(first && second);
    await page.mouse.move(first.x + first.width / 2, first.y + first.height / 2); await page.mouse.down();
    await page.mouse.move(second.x + second.width / 2, second.y + second.height / 2, { steps: 6 }); await page.mouse.up();
    assert.equal(await page.evaluate(() => window.__navActivations.length), 1);
    assert.equal(await page.locator('.mobile-bottom-nav').evaluate(node => node.classList.contains('is-dragging')), false);
    check('Classic mobile menu closes by keyboard; glass drag activates once and releases its capture.');

    await page.evaluate(() => scrollTo(0, 400));
    const kept = await page.evaluate(() => {
      const before = scrollY;
      window.ChunnelScroll.keep(() => scrollTo(0, 0));
      return [before, scrollY, document.documentElement.style.overflowAnchor];
    });
    assert.ok(kept[0] > 0); assert.equal(kept[0], kept[1]); assert.equal(kept[2], '');
    for (const mode of ['light', 'dark']) {
      await page.evaluate(mode => { window.ChunnelColorMode.set(mode); scrollTo(0, 0); }, mode); await settled();
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false);
      await page.screenshot({ path: resolve(directory, `${name}-home-glass-${mode}.png`), fullPage: false });
    }
    check('Real scroll transactions restore synchronously; glass mobile layout fits both color modes.');

    // Use the real generated motion entry on a real DOM element, without opening
    // unrelated account dialogs or submitting a form.
    await page.addScriptTag({ url: await page.evaluate(() => new URL('chunnel-motion.js', document.querySelector('[data-home-entry]').src).href) });
    const motion = await page.evaluate(async () => {
      const node = document.createElement('div'); node.style.transform = 'rotate(1deg)'; document.body.append(node);
      const animation = window.ChunnelMotion.surface(node, { x: 100, opacity: 0 }, {});
      await animation.finished; const final = node.style.transform;
      animation.cancel(); const restored = node.style.transform; node.remove(); return { final, restored };
    });
    assert.match(motion.final, /translate3d\(0px/); assert.equal(motion.restored, 'rotate(1deg)');
    check('Generated motion settles with reduced motion and cancellation restores caller styles.');

    await page.goto(fixture.login); await visit('/admin/series/?edit=new');
    for (const mode of ['light', 'dark']) for (const width of [1280, 375]) {
      await page.setViewportSize({ width, height: 900 });
      await page.evaluate(mode => { window.ChunnelColorMode.set(mode); scrollTo(0, 0); }, mode); await settled();
      const geometry = await page.evaluate(() => {
        const dock = document.querySelector('.admin-save-dock'), rect = dock.getBoundingClientRect();
        const canvas = document.createElement('canvas'); canvas.width = canvas.height = 1;
        const paint = canvas.getContext('2d');
        const color = value => { paint.clearRect(0, 0, 1, 1); paint.fillStyle = value; paint.fillRect(0, 0, 1, 1); return Array.from(paint.getImageData(0, 0, 1, 1).data); };
        const luminance = rgb => rgb.slice(0, 3).map(value => value / 255).map(value => value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4).reduce((sum, value, i) => sum + value * [.2126, .7152, .0722][i], 0);
        const contrasts = ['.series-admin h1', '.series-admin label'].map(selector => {
          const element = document.querySelector(selector), ancestors = []; let parent = element;
          while (parent) { ancestors.unshift(parent); parent = parent.parentElement; }
          paint.fillStyle = '#fff'; paint.fillRect(0, 0, 1, 1);
          for (const node of ancestors) { paint.fillStyle = getComputedStyle(node).backgroundColor; paint.fillRect(0, 0, 1, 1); }
          const background = luminance(Array.from(paint.getImageData(0, 0, 1, 1).data));
          const foreground = luminance(color(getComputedStyle(element).color));
          return { selector, ratio: (Math.max(background, foreground) + .05) / (Math.min(background, foreground) + .05) };
        });
        return { width: innerWidth, clientWidth: document.documentElement.clientWidth, contentRight: document.documentElement.getBoundingClientRect().right, overflow: document.documentElement.scrollWidth > innerWidth + 1,
          dock: { left: rect.left, right: rect.right, bottom: rect.bottom }, contrasts };
      });
      layouts.push({ mode, width, ...geometry });
      assert.equal(geometry.overflow, false);
      assert.ok(geometry.contrasts.every(item => item.ratio >= 4.5));
      assert.ok(Math.abs(geometry.dock.bottom - 900) <= 1);
      // The stable scrollbar gutter is outside the content rectangle even on
      // headless Chromium where clientWidth may still report the outer viewport.
      if (width === 375) { assert.ok(Math.abs(geometry.dock.left) <= 1); assert.ok(Math.abs(geometry.dock.right - geometry.contentRight) <= 1); }
      await page.screenshot({ path: resolve(directory, `${name}-series-${mode}-${width}.png`), fullPage: false });
    }
    check('Series titles/labels meet 4.5 contrast and the save dock stays in the viewport at desktop and narrow widths.');
    assert.deepEqual(errors, []); assert.deepEqual(writes, []);
    assert.equal(scripts.some(path => /\/(?:newpress-analytics|react|echarts)\./.test(path)), false);
    check('No page exceptions, content writes, or React/ECharts downloads in ordinary page flows.');
  } finally {
    await mkdir(directory, { recursive: true });
    await writeFile(resolve(directory, `browser-${name}.json`), JSON.stringify({ evidence: evidence.filter(item => item.browser === name), errors, writes, queries, layouts }, null, 2) + '\n');
    await context.close(); await browser.close();
  }
}
await writeFile(resolve(directory, 'browser-acceptance.json'), JSON.stringify({ evidence }, null, 2) + '\n');
