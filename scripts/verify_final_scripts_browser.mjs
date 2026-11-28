// Real Go templates and read APIs; native form POSTs are intercepted before Go.
import assert from 'node:assert/strict';
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';
import { compileFrontendEntry } from './frontend_entries.mjs';

const directory = resolve(process.argv[2]), variant = process.argv[3] || 'generated';
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const entries = { 'chunnel-images.js': 'images', 'chunnel-analytics.js': 'analytics-lite' };
const scripts = new Map(), evidence = [], servedOutput = [];
if (variant === 'typescript') for (const [filename, entry] of Object.entries(entries)) scripts.set(filename, (await compileFrontendEntry(entry, { minify: false })).code);
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(fn) { const end = Date.now() + 12000; while (!await fn()) { if (Date.now() > end) throw Error('Browser condition timed out'); await delay(25); } }
await mkdir(directory, { recursive: true });
const engines = { chromium, firefox, webkit };
for (const name of process.argv[4] ? [process.argv[4]] : Object.keys(engines)) {
  const browser = await engines[name].launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1280, height: 960 }, reducedMotion: 'reduce', timezoneId: 'America/Los_Angeles' });
  const page = await context.newPage(), errors = [], requests = [], writes = [], served = new Set();
  let stage = 'initialization', mutation = '', failReact = false;
  page.setDefaultTimeout(12000);
  page.on('pageerror', error => { errors.push(error.message); console.error(name, stage, error.message); });
  page.on('request', request => requests.push(request.url()));
  page.on('dialog', dialog => dialog.accept());
  const check = text => { stage = text; evidence.push({ browser: name, check: text }); console.log(`${name}: ${text}`); };
  await context.addInitScript(() => {
    const Native = window.ResizeObserver;
    window.liteObservers = new Map();
    window.ResizeObserver = class extends Native {
      observe(target, options) { if (target.matches('[data-chart]')) window.liteObservers.set(this, target); super.observe(target, options); }
      unobserve(target) { if (window.liteObservers.get(this) === target) window.liteObservers.delete(this); super.unobserve(target); }
      disconnect() { window.liteObservers.delete(this); super.disconnect(); }
    };
  });
  await context.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url()), filename = url.pathname.split('/').at(-1);
    if (url.origin !== fixture.base) return route.abort();
    if (failReact && filename === 'newpress-analytics.js') return route.abort();
    if (scripts.has(filename)) { served.add(filename); return route.fulfill({ contentType: 'text/javascript', body: scripts.get(filename) }); }
    if (request.method() !== 'GET' && request.method() !== 'HEAD') {
      assert.equal(url.pathname, `/admin/media/${fixture.media_id}/process/`);
      const form = new URLSearchParams(request.postData());
      assert.deepEqual([...form.keys()].sort(), ['csrf_token', 'focal_x', 'focal_y', 'version']);
      assert.ok(form.get('csrf_token'));
      assert.equal(form.get('version'), String(fixture.media_version));
      writes.push({ path: url.pathname, x: form.get('focal_x'), y: form.get('focal_y'), version: form.get('version') });
      return route.fulfill({ status: 204 });
    }
    if (mutation && url.pathname === '/admin/analytics/' && request.isNavigationRequest()) {
      const response = await route.fetch(); let body = await response.text();
      body = mutation === 'invalid' ? body.replace(/data-views="\d+"/, 'data-views="unknown"') : body.replace('data-chart-cursor', 'data-missing-cursor');
      return route.fulfill({ response, body });
    }
    return route.continue();
  });
  const login = async (role = 'owner') => { const response = await context.request.get(fixture.base + fixture.login + (role === 'reader' ? '?role=reader' : '')); assert.equal(response.status(), 204); };
  const visit = async key => { await page.goto(fixture.base + fixture.paths[key], { waitUntil: 'networkidle' }); };
  const observers = () => page.evaluate(() => window.liteObservers.size);
  const chart = page.locator('[data-chart]'), form = page.locator('form[data-image-process]');
  const x = form.locator('[name=focal_x]'), y = form.locator('[name=focal_y]'), submit = form.locator('[data-image-submit]');
  const show = () => page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })));
  const emitUpdate = () => page.evaluate(() => document.dispatchEvent(new CustomEvent('chunnel:admin-updated', { detail: { regions: [] } })));
  try {
    await login(); await visit('analytics');
    await page.locator('[data-chart-switch]:not([hidden])').waitFor();
    const report = await page.locator('[data-analytics]').evaluate(node => JSON.parse(node.dataset.analyticsReport));
    assert.equal(requests.some(url => /\/newpress-analytics\.(?:js|css)/.test(url)), false);
    assert.equal(requests.some(url => url.includes('/api/v1/admin/analytics')), false);
    assert.equal(await observers(), 1);
    assert.equal(await page.locator('[data-chart-total]').textContent(), new Intl.NumberFormat('zh-CN').format(report.views));
    assert.deepEqual(await page.locator('[data-analytics-daily] tbody tr').evaluateAll(rows => rows.map(row => row.dataset.date)), report.trend.map(row => row.date));
    check('default report matches the Go snapshot without loading React/ECharts or reading an API');

    await chart.focus(); await chart.press('Home');
    assert.equal(await page.locator('[data-chart-tooltip] time').getAttribute('datetime'), report.trend[0].date);
    assert.ok((await page.locator('[data-chart-tooltip] time').textContent()).includes(report.timezone));
    await chart.press('ArrowRight');
    assert.equal(await page.locator('[data-chart-tooltip] time').getAttribute('datetime'), report.trend[1].date);
    await chart.press('End'); assert.equal(await page.locator('[data-chart-tooltip] b').textContent(), String(report.trend.at(-1).views));
    await chart.press('Escape'); assert.equal(await page.locator('[data-chart-tooltip]').isVisible(), false);
    check('keyboard navigation preserves exact report dates and report timezone in a different browser timezone');
    for (const key of ['searches', 'misses', 'views']) {
      await page.locator(`button[data-series=${key}]`).click();
      assert.equal(await page.locator(`button[data-series=${key}]`).getAttribute('aria-pressed'), 'true');
      assert.equal(await page.locator('[data-chart-total]').textContent(), new Intl.NumberFormat('zh-CN').format(report[key]));
    }
    const segmentCount = await page.locator('[data-source-segments] circle').count();
    await emitUpdate(); await emitUpdate(); await emitUpdate();
    assert.equal(await observers(), 1); assert.equal(await page.locator('[data-source-segments] circle').count(), segmentCount);
    await chart.focus(); await chart.press('Home'); await chart.press('ArrowRight');
    assert.equal(await page.locator('[data-chart-tooltip] time').getAttribute('datetime'), report.trend[1].date);
    check('series switching and repeated initialization do not duplicate drawings, listeners or observers');

    await page.getByRole('link', { name: '近 7 天', exact: true }).click();
    await page.waitForFunction(() => document.querySelector('[data-analytics]')?.dataset.days === '7');
    assert.equal(await observers(), 1);
    await page.getByRole('link', { name: '近 90 天', exact: true }).click();
    await page.waitForFunction(() => document.querySelector('[data-analytics]')?.dataset.days === '90');
    assert.equal(await observers(), 1);
    await page.goBack(); await page.waitForFunction(() => document.querySelector('[data-analytics]')?.dataset.days === '7');
    assert.equal(await observers(), 1);
    check('actual period navigation and browser history dispose each previous native chart');

    await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })));
    assert.equal(await observers(), 0); await show(); assert.equal(await observers(), 1);
    await page.evaluate(() => {
      const host = document.querySelector('[data-analytics]'); host.querySelector('[data-analytics-fallback]').hidden = true;
      document.dispatchEvent(new CustomEvent('newpress:analytics-ready', { detail: { host } }));
    });
    assert.equal(await observers(), 0);
    await page.evaluate(() => {
      const host = document.querySelector('[data-analytics]'); host.querySelector('[data-analytics-fallback]').hidden = false;
      document.dispatchEvent(new CustomEvent('newpress:analytics-fallback', { detail: { host } }));
    });
    assert.equal(await observers(), 1);
    check('simulated BFCache and React handoff events pause and resume the preserved native report');
    await page.evaluate(() => {
      const host = document.querySelector('[data-analytics]'); window.oldChart = host.querySelector('[data-chart]');
      document.dispatchEvent(new CustomEvent('chunnel:admin-before-replace', { detail: { regions: [host] } }));
      const replacement = host.cloneNode(true); host.replaceWith(replacement);
      document.dispatchEvent(new CustomEvent('chunnel:admin-updated', { detail: { regions: [replacement] } }));
    });
    assert.equal(await observers(), 1);
    assert.equal(await page.evaluate(() => { const event = new KeyboardEvent('keydown', { key: 'Home', bubbles: true, cancelable: true }); window.oldChart.dispatchEvent(event); return event.defaultPrevented; }), false);
    check('detached chart listeners are removed and cloned server markup mounts once');

    for (const kind of ['invalid', 'missing']) {
      mutation = kind; await visit('analytics');
      assert.equal(await observers(), 0); assert.equal(await page.locator('[data-chart-switch]').isHidden(), true);
      assert.equal(await page.locator('[data-analytics-daily] tbody tr').count(), 30);
      assert.equal(await page.locator('[data-chart-total]').textContent(), String(report.views));
    }
    mutation = ''; await visit('analytics');
    check('invalid counts or missing enhancement nodes leave the readable server report untouched');
    await page.evaluate(() => {
      const host = document.querySelector('[data-analytics]');
      document.dispatchEvent(new CustomEvent('chunnel:admin-before-replace', { detail: { regions: [host] } }));
      for (const key of ['views', 'articleViews', 'searches', 'misses']) host.dataset[key] = '0';
      host.querySelectorAll('[data-analytics-daily] tbody tr').forEach(row => { for (const key of ['views', 'searches', 'misses']) row.dataset[key] = '0'; });
      host.querySelectorAll('[data-source-count]').forEach(row => { row.dataset.sourceCount = '0'; });
      document.dispatchEvent(new CustomEvent('chunnel:admin-updated', { detail: { regions: [host] } }));
    });
    assert.equal(await page.locator('[data-chart-empty]').isVisible(), true);
    assert.equal(await page.locator('[data-chart-series] polyline').count(), 0);
    assert.equal(await page.locator('[data-source-segments] circle').count(), 0);
    assert.equal(await page.locator('[data-article-ring]').isHidden(), true);
    assert.equal(await page.locator('[data-article-share]').textContent(), '—');
    check('an all-zero report has no fabricated chart segments or percentages');

    await visit('analytics'); failReact = true;
    await page.getByRole('button', { name: '试用交互图表' }).click();
    await page.locator('[data-analytics-load-status]').filter({ hasText: '未能加载' }).waitFor();
    assert.equal(await page.locator('[data-analytics-fallback]').isVisible(), true); assert.equal(await observers(), 1);
    failReact = false; await visit('analytics');
    await page.getByRole('button', { name: '试用交互图表' }).click();
    await page.locator('[data-react-ready=true]').waitFor(); assert.equal(await observers(), 0);
    await page.getByRole('link', { name: '简洁报告', exact: true }).click();
    await page.locator('[data-chart-switch]:not([hidden])').waitFor(); assert.equal(await observers(), 1);
    check('actual React activation, failed script loading and return to the simple report retain the default report path');

    for (const width of [1280, 390]) for (const colorScheme of ['light', 'dark']) {
      await page.setViewportSize({ width, height: 960 }); await page.emulateMedia({ colorScheme });
      await page.waitForFunction(theme => document.documentElement.dataset.theme === theme, colorScheme); await delay(400);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 2), false);
      await page.screenshot({ path: resolve(directory, `${variant}-${name}-analytics-${width}-${colorScheme}.png`), fullPage: true });
    }
    check('native charts resize without horizontal overflow on desktop and narrow light/dark pages');

    await page.setViewportSize({ width: 1280, height: 960 }); await visit('media');
    await form.locator('[data-focus-image]').evaluate(image => image.decode());
    assert.equal(requests.filter(url => /\/chunnel-images\.js/.test(url)).length > 0, true);
    await form.evaluate(node => { window.focusEvents = []; node.addEventListener('input', e => window.focusEvents.push(['input', e.target.name])); node.addEventListener('change', e => window.focusEvents.push(['change', e.target.name])); });
    const rect = await form.locator('[data-focus-image]').boundingBox();
    await form.locator('[data-focus-image]').click({ position: { x: rect.width * .75, y: rect.height * .25 } });
    assert.equal(await x.inputValue(), '75'); assert.equal(await y.inputValue(), '25');
    assert.equal(await form.locator('[data-focus-crop="1"]').evaluate(node => node.style.objectPosition), '100% 50%');
    assert.deepEqual(await page.evaluate(() => window.focusEvents), [['input', 'focal_x'], ['change', 'focal_x'], ['input', 'focal_y'], ['change', 'focal_y']]);
    check('real image clicks update integer form fields, crop previews and native change notifications');
    await emitUpdate(); await emitUpdate(); await form.locator('[data-focus-center]').click();
    assert.equal(await x.inputValue(), '50'); assert.equal(await y.inputValue(), '50'); assert.equal(await x.evaluate(node => document.activeElement === node), true);
    assert.equal(await form.locator('[data-focus-crop="1"]').evaluate(node => node.style.objectPosition), '50% 50%');
    await x.fill('25'); await y.fill('75');
    await form.evaluate(node => node.reset()); await until(async () => await x.inputValue() === '50');
    await until(async () => await form.locator('[data-focus-mark]').evaluate(node => node.style.left) === '50%');
    check('center, repeated initialization and native reset synchronize inputs, focus and previews');
    await x.fill(''); await submit.click(); assert.equal(writes.length, 0); assert.equal(await submit.isDisabled(), false);
    await x.fill('101'); await submit.click(); assert.equal(writes.length, 0);
    await x.fill('80'); await y.fill('20');
    await page.evaluate(() => document.addEventListener('submit', event => event.preventDefault(), { once: true }));
    await submit.click(); await delay(50); assert.equal(writes.length, 0); assert.equal(await submit.isDisabled(), false);
    assert.notEqual(await form.getAttribute('aria-busy'), 'true');
    check('native validation and a late canceled submit leave the image form usable without POSTs');
    await submit.click(); await until(() => writes.length === 1); await until(async () => await submit.isDisabled());
    assert.deepEqual(writes[0], { path: `/admin/media/${fixture.media_id}/process/`, x: '80', y: '20', version: String(fixture.media_version) });
    assert.equal(await form.getAttribute('aria-busy'), 'true');
    await form.evaluate(node => { node.requestSubmit(); node.requestSubmit(); }); await delay(50); assert.equal(writes.length, 1);
    check('native POST keeps CSRF, version and focal coordinates intact and blocks duplicate submission');
    await show(); assert.equal(await submit.isDisabled(), false); assert.equal(await x.inputValue(), '80');
    assert.equal(await form.locator('[data-image-status]').textContent(), '');
    await submit.evaluate(node => { node.disabled = true; }); await show(); assert.equal(await submit.isDisabled(), true);
    await submit.evaluate(node => { node.disabled = false; });
    check('simulated BFCache restoration releases only the pending lock and retains preexisting disabled state');
    await page.evaluate(() => {
      const form = document.querySelector('[data-image-process]'); window.oldImageForm = form;
      document.dispatchEvent(new CustomEvent('chunnel:admin-before-replace', { detail: { regions: [form] } }));
      const replacement = form.cloneNode(true); form.replaceWith(replacement);
      document.dispatchEvent(new CustomEvent('chunnel:admin-updated', { detail: { regions: [replacement] } }));
    });
    assert.equal(await page.evaluate(() => { const old = window.oldImageForm; const left = old.querySelector('[data-focus-mark]').style.left; old.elements.focal_x.value = '10'; old.elements.focal_x.dispatchEvent(new Event('input')); return old.querySelector('[data-focus-mark]').style.left === left; }), true);
    await form.locator('[data-focus-center]').click(); assert.equal(await x.inputValue(), '50');
    check('removed image forms release input and submit handlers while replacement forms initialize');
    await page.goto(fixture.base + '/admin/media/', { waitUntil: 'networkidle' }); await page.goBack({ waitUntil: 'networkidle' });
    assert.equal(await submit.isDisabled(), false);
    await form.locator('[data-focus-center]').click(); assert.equal(await x.inputValue(), '50');
    check('actual browser back returns to a usable media processing form');
    await page.setViewportSize({ width: 390, height: 960 });
    await page.evaluate(() => { document.activeElement?.blur(); window.scrollTo({ top: 0, behavior: 'instant' }); });
    await page.waitForFunction(() => Math.abs(scrollY) < 1); await delay(400);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 2), false);
    await page.screenshot({ path: resolve(directory, `${variant}-${name}-media-390.png`), fullPage: true });
    check('actual media processing layout remains usable at a narrow width');

    for (const [filename] of Object.entries(entries)) {
      const script = page.locator(`script[src*="/${filename}"]`);
      const path = await script.count() ? await script.getAttribute('src') : null;
      const url = path || requests.find(url => new URL(url).pathname.endsWith('/' + filename));
      const response = await context.request.get(new URL(url, fixture.base).href);
      const actual = await response.body(), expected = await readFile(resolve('internal/coreui/assets/js', filename));
      assert.ok(actual.equals(expected), filename + ' served output differs');
      assert.ok(response.headers()['etag']); assert.match(response.headers()['cache-control'], /immutable/);
      servedOutput.push({ browser: name, filename, bytes: actual.length, equal: true });
    }
    if (variant === 'typescript') assert.deepEqual([...served].sort(), Object.keys(entries).sort());
    assert.deepEqual(errors, []); check('served generated scripts match disk and all exercised pages have no uncaught errors');
    await login('reader'); const denied = await context.request.get(fixture.base + fixture.paths.analytics); assert.equal(denied.status(), 403);
    check('an actual subscriber cannot access the report');
  } catch (error) {
    await page.screenshot({ path: resolve(directory, `${variant}-${name}-failure.png`), fullPage: true }).catch(() => {});
    await writeFile(resolve(directory, `${variant}-${name}-failure.json`), JSON.stringify({ stage, message: error.stack, errors, url: page.url(), writes }, null, 2));
    throw error;
  } finally { await context.close(); await browser.close(); }
}
await writeFile(resolve(directory, `${variant}-browser-acceptance.json`), JSON.stringify({ variant, checks: evidence.length, evidence }, null, 2));
await writeFile(resolve(directory, `${variant}-served-output.json`), JSON.stringify(servedOutput, null, 2));
console.log(`Final scripts ${variant}: ${evidence.length} checks passed`);
