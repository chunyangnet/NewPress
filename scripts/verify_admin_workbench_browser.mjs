// Opt-in acceptance against TestAdminResourcesBrowserFixture's actual Go pages.
// All writes stay in browser form state; the fixture verifies stored data later.
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
  const errors = [], writes = [], scripts = [], layouts = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => {
    if (!['GET', 'HEAD'].includes(request.method())) writes.push({ method: request.method(), url: request.url() });
    if (request.resourceType() === 'script') scripts.push(new URL(request.url()).pathname);
  });
  const check = label => { evidence.push({ browser: name, check: label }); console.log(`${name}: ${label}`); };
  const visit = async path => { await page.goto(fixture.base + path); await page.waitForLoadState('networkidle'); };
  try {
    await visit(fixture.login);
    const sourceURLs = await page.locator('script[src]').evaluateAll(nodes => nodes.map(node => node.src));
    assert.equal(sourceURLs.filter(url => url.includes('/chunnel-admin-workbench.js')).length, 1);
    assert.equal(sourceURLs.some(url => /\/(chunnel-time|chunnel-admin-ui|chunnel-admin-navigation)\.js/.test(url)), false);
    assert.deepEqual(await page.evaluate(() => [typeof window.ChunnelTime.formatDateTime, typeof window.ChunnelAdminUI.savebar]), ['function', 'function']);
    const tabs = page.getByRole('tab');
    await tabs.first().focus(); await page.keyboard.press('ArrowRight');
    assert.equal(await tabs.nth(1).getAttribute('aria-selected'), 'true');
    await page.keyboard.press('Home');
    assert.equal(await tabs.first().getAttribute('aria-selected'), 'true');
    await page.getByRole('searchbox', { name: '搜索后台功能' }).fill('站点设置');
    assert.equal(await page.locator('[data-admin-command-results]').isVisible(), true);
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('[data-admin-command-results]').isVisible(), false);
    check('Merged entry initializes time, UI, tabs and keyboard command search once.');

    await visit('/admin/content/');
    await page.evaluate(() => { window.__workbenchProbe = 'same-document'; });
    const draftLink = page.locator('.admin-main a[href*="status=draft"]').first();
    await draftLink.click();
    await page.waitForURL(url => url.searchParams.get('status') === 'draft');
    await page.waitForFunction(() => !document.querySelector('.admin-main')?.hasAttribute('data-navigation-loading'));
    assert.equal(await page.evaluate(() => window.__workbenchProbe), 'same-document');
    await page.goBack();
    await page.waitForFunction(() => !document.querySelector('.admin-main')?.hasAttribute('data-navigation-loading'));
    assert.equal(new URL(page.url()).searchParams.has('status'), false);
    assert.equal(await page.evaluate(() => window.__workbenchProbe), 'same-document');
    check('Content filters and browser history replace only the declared region.');

    await page.evaluate(() => {
      const region = document.querySelector('.admin-content-page');
      const form = document.createElement('form'); form.id = 'acceptance-form';
      const input = document.createElement('input'); input.name = 'sample'; input.value = 'original'; form.append(input);
      const bar = document.createElement('footer');
      bar.innerHTML = '<button type="button" data-admin-save>保存</button><button type="button" data-admin-discard>放弃</button><p data-save-state></p>';
      region.append(form, bar);
      window.__acceptanceSavebar = window.ChunnelAdminUI.savebar(form, { bar });
      input.value = 'unsaved'; input.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await draftLink.click();
    await page.locator('[data-admin-navigation-error]').waitFor();
    assert.equal(new URL(page.url()).searchParams.has('status'), false);
    assert.equal(await page.locator('#acceptance-form input').inputValue(), 'unsaved');
    await page.evaluate(() => { window.__acceptanceSavebar.discard(); window.__acceptanceSavebar.dispose(); });
    await draftLink.click(); await page.waitForURL(url => url.searchParams.get('status') === 'draft');
    check('Dirty forms block region replacement; discarding releases navigation.');

    await visit('/admin/settings/');
    const savebar = page.locator('[data-admin-savebar][data-form-id="site-settings-form"]');
    const field = page.locator('#site-settings-form input[type="text"]:visible:not([readonly]):not([disabled])').first();
    const original = await field.inputValue();
    await field.fill(original + '-acceptance');
    assert.equal(await savebar.locator('[data-admin-save]').isEnabled(), true);
    await savebar.locator('[data-admin-discard]').click();
    assert.equal(await field.inputValue(), original);
    assert.equal(await savebar.locator('[data-admin-save]').isDisabled(), true);
    await page.locator('[data-settings-target]').nth(1).click();
    assert.equal(await page.locator('[data-settings-section]:visible').count(), 1);
    check('Settings edits activate the savebar; discard restores values and section navigation.');

    // The actual picker runs against a deterministic image fixture, including
    // the second read that checks the selected image is still usable.
    const item = { id: 1, status: 'active', kind: 'image', width: 1, height: 1, title: '验收图片', filename: 'acceptance.png', public_url: '/media/acceptance.png', mime_type: 'image/png', size_bytes: 70, alt_text: '验收图片', caption: '' };
    await page.route('**/api/v1/admin/media**', route => {
      const url = new URL(route.request().url());
      return route.fulfill({ contentType: 'application/json', body: JSON.stringify(url.pathname.endsWith('/1') ? item : {
        items: [item], pagination: { page: 1, pages: 1, total: 1 }, max_upload_bytes: 1048576, can_upload: true
      }) });
    });
    await page.route('**/media/acceptance.png', route => route.fulfill({ contentType: 'image/png', body: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==', 'base64') }));
    await visit('/admin/series/?edit=new');
    const cover = page.locator('input[name="cover_url"]');
    const wrapper = cover.locator('..');
    const choose = wrapper.getByRole('button', { name: '选择或上传选集封面', exact: true });
    assert.equal(await wrapper.locator('.admin-media-field-actions').count(), 1);
    await choose.click();
    await page.locator('[data-editor-image-id="1"]').click();
    const apply = page.locator('[data-editor-media-apply]');
    await page.waitForFunction(() => !document.querySelector('[data-editor-media-apply]').disabled);
    await apply.click();
    await page.waitForFunction(() => !document.querySelector('[data-editor-media-dialog]').open);
    assert.equal(await cover.inputValue(), item.public_url);
    // close is queued after the open property changes (notably in WebKit).
    await page.waitForFunction(() => document.querySelector('.admin-media-field-actions button').getAttribute('aria-expanded') === 'false');
    assert.equal(await choose.getAttribute('aria-expanded'), 'false');
    await wrapper.getByRole('button', { name: '清除选集封面', exact: true }).click();
    assert.equal(await cover.inputValue(), '');
    assert.equal(await cover.evaluate(node => document.activeElement === node), true);
    await cover.evaluate(node => { node.readOnly = true; });
    await page.waitForFunction(() => document.querySelector('.admin-media-field-actions button').disabled);
    assert.equal(await choose.isDisabled(), true);
    await cover.evaluate(node => { node.readOnly = false; });
    await page.waitForFunction(() => !document.querySelector('.admin-media-field-actions button').disabled);
    check('Merged media picker opens, revalidates and selects an image; clear restores focus; readonly changes disable it.');

    // Dynamic forms share the same observer, URL validation and array limit.
    await page.evaluate(() => {
      const field = document.createElement('textarea'); field.id = 'acceptance-images'; field.dataset.mediaInput = ''; field.dataset.mediaMultiple = '2';
      field.dataset.mediaTitle = '验收图片组'; field.value = '/media/acceptance.png\n/media/acceptance.png';
      document.querySelector('.admin-main').append(field);
    });
    const images = page.locator('#acceptance-images');
    await images.locator('..').locator('.admin-media-field-actions').waitFor();
    assert.match(await images.locator('..').locator('[role="status"]').textContent(), /^1 \/ 2/);
    await images.fill('javascript:alert(1)');
    assert.equal(await images.locator('..').locator('img').getAttribute('src'), null);
    await images.locator('..').evaluate(node => { node.inert = true; });
    await page.waitForFunction(() => document.querySelector('#acceptance-images').parentElement.querySelector('button').disabled);
    check('Dynamically inserted media fields deduplicate URLs, reject unsafe previews and honor inert ancestors.');
    await images.locator('..').evaluate(node => node.remove());

    for (const colorScheme of ['light', 'dark']) {
      await page.emulateMedia({ colorScheme });
      await page.evaluate(mode => window.ChunnelColorMode.set(mode), colorScheme);
      for (const width of [1280, 375]) {
        await page.setViewportSize({ width, height: 900 });
        await page.evaluate(async () => {
          scrollTo(0, 0);
          await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
          const sample = () => JSON.stringify([...document.querySelectorAll('.series-admin, .series-admin h1, .series-admin label, .admin-save-dock')].map(node => {
            const style = getComputedStyle(node), rect = node.getBoundingClientRect();
            return [style.color, style.backgroundColor, rect.left, rect.top, rect.width, rect.height];
          }));
          const deadline = performance.now() + 2000; let previous = sample(), stable = 0;
          while (stable < 3) {
            if (performance.now() > deadline) throw new Error('Inspected styles did not settle');
            await new Promise(resolve => setTimeout(resolve, 60));
            const next = sample(); stable = next === previous ? stable + 1 : 0; previous = next;
          }
        });
        const geometry = await page.evaluate(() => {
          const rect = document.querySelector('.admin-save-dock').getBoundingClientRect();
          const canvas = document.createElement('canvas'); canvas.width = canvas.height = 1;
          const paint = canvas.getContext('2d');
          const luminance = rgb => rgb.slice(0, 3).map(value => value / 255).map(value => value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4).reduce((sum, value, i) => sum + value * [.2126, .7152, .0722][i], 0);
          const contrasts = ['.series-admin h1', '.series-admin label'].map(selector => {
            const element = document.querySelector(selector), ancestors = []; let parent = element;
            while (parent) { ancestors.unshift(parent); parent = parent.parentElement; }
            paint.fillStyle = '#fff'; paint.fillRect(0, 0, 1, 1);
            for (const node of ancestors) { paint.fillStyle = getComputedStyle(node).backgroundColor; paint.fillRect(0, 0, 1, 1); }
            const background = luminance(Array.from(paint.getImageData(0, 0, 1, 1).data));
            paint.clearRect(0, 0, 1, 1); paint.fillStyle = getComputedStyle(element).color; paint.fillRect(0, 0, 1, 1);
            const foreground = luminance(Array.from(paint.getImageData(0, 0, 1, 1).data));
            return { selector, ratio: (Math.max(background, foreground) + .05) / (Math.min(background, foreground) + .05) };
          });
          return { overflow: document.documentElement.scrollWidth > innerWidth + 1, contentRight: document.documentElement.getBoundingClientRect().right,
            dock: { left: rect.left, right: rect.right, bottom: rect.bottom }, contrasts };
        });
        layouts.push({ width, colorScheme, ...geometry });
        assert.equal(geometry.overflow, false, `${name} ${colorScheme} ${width} overflow`);
        assert.ok(geometry.contrasts.every(item => item.ratio >= 4.5));
        assert.ok(Math.abs(geometry.dock.bottom - 900) <= 1);
        if (width === 375) { assert.ok(Math.abs(geometry.dock.left) <= 1); assert.ok(Math.abs(geometry.dock.right - geometry.contentRight) <= 1); }
        await page.screenshot({ path: resolve(directory, `${name}-${colorScheme}-${width}.png`), fullPage: true });
      }
    }
    check('Series form fits desktop/mobile; settled title/label contrast meets 4.5 and save dock aligns with the viewport.');
    assert.equal(scripts.some(path => /\/(newpress-analytics|react|echarts)\.(js|css)/.test(path)), false);
    assert.deepEqual(errors, []);
    assert.deepEqual(writes, []);
    check('No page errors, content writes, or React/ECharts downloads on ordinary admin pages.');
  } finally {
    await mkdir(directory, { recursive: true });
    await writeFile(resolve(directory, `browser-${name}.json`), JSON.stringify({ evidence: evidence.filter(item => item.browser === name), errors, writes, layouts }, null, 2) + '\n');
    await context.close(); await browser.close();
  }
}
await writeFile(resolve(directory, 'browser-acceptance.json'), JSON.stringify({ evidence }, null, 2) + '\n');
