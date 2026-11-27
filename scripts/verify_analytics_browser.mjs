// Opt-in browser acceptance against TestAnalyticsBrowserFixture's real Go server.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium } from 'playwright';

const directory = resolve(process.argv[2]);
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const browser = await chromium.launch({ headless: true });
const evidence = [], errors = [];
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce' });
const page = await context.newPage();
page.on('pageerror', error => errors.push(error.message));
const calls = [];
page.on('request', request => calls.push(request.url()));
const ready = () => page.locator('[data-react-ready="true"]').waitFor();
const days = async expected => {
  await page.waitForFunction(value => document.querySelector('[data-report-days]')?.getAttribute('data-report-days') === String(value), expected);
  assert.equal(new URL(page.url()).searchParams.get('days'), String(expected));
};
try {
  await page.goto(fixture.base + fixture.login);
  await page.waitForLoadState('networkidle');
  assert.equal(calls.some(url => /\/newpress-analytics\.(js|css)/.test(url)), false);
  assert.equal(await page.locator('[data-analytics-fallback]').isVisible(), true);
  await page.setViewportSize({ width: 320, height: 844 });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'native 320px overflow');
  await page.screenshot({ path: resolve(directory, 'default-mobile-320.png'), fullPage: true });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByRole('button', { name: '试用交互图表' }).click();
  await ready();
  assert.equal(await page.locator('[data-analytics-fallback]').isVisible(), false);
  assert.equal(calls.filter(url => url.includes('/api/v1/admin/analytics')).length, 0);
  await page.locator('[data-analytics-root] svg').first().waitFor();
  const initial = await page.locator('[data-analytics]').evaluate(el => JSON.parse(el.dataset.analyticsReport));
  const shown = await page.locator('[data-metric]').allTextContents();
  assert.deepEqual(shown.map(text => Number(text.replaceAll(',', ''))), [initial.views, initial.article_views, initial.searches, initial.misses]);
  await page.getByText('查看每日明细 · 30 天', { exact: true }).click();
  assert.deepEqual(await page.locator('[data-report-daily] tbody tr').evaluateAll(rows => rows.map(row => [...row.cells].map(cell => cell.textContent))),
    initial.trend.map(row => [row.date, String(row.views), String(row.searches), String(row.misses)]));
  evidence.push('Default does not load React/ECharts; activation reuses the Go report with no API read; all initial metrics and daily rows match.');

  // Delayed 7-day response must not overwrite the subsequently chosen 90 days.
  await page.route('**/api/v1/admin/analytics?days=7', async route => {
    const response = await route.fetch();
    await new Promise(resolve => setTimeout(resolve, 200));
    await route.fulfill({ response }).catch(() => {});
  });
  await page.getByRole('link', { name: '近 7 天', exact: true }).click();
  await page.getByRole('link', { name: '近 90 天', exact: true }).click();
  await days(90);
  await page.waitForLoadState('networkidle');
  await days(90);
  await page.unroute('**/api/v1/admin/analytics?days=7');
  await page.goBack(); await days(7);
  await page.goForward(); await days(90);
  evidence.push('Rapid 30→7→90 switching and browser back/forward preserve URL, report period, and latest-response ownership.');

  for (const status of [503, 500]) {
    await page.route('**/api/v1/admin/analytics?*', route => route.fulfill({ status, contentType: 'application/json', body: '{"error":{}}' }));
    await page.getByRole('button', { name: '刷新报告', exact: true }).click();
    await page.getByRole('button', { name: '重试', exact: true }).waitFor();
    await days(90);
    assert.match(await page.locator('.analytics-react-feedback').textContent(), /近 90 天/);
    await page.unroute('**/api/v1/admin/analytics?*');
  }
  await page.route('**/api/v1/admin/analytics?*', route => route.abort());
  await page.getByRole('button', { name: '刷新报告', exact: true }).click();
  await page.getByRole('button', { name: '重试', exact: true }).waitFor();
  assert.match(await page.locator('.analytics-react-feedback').textContent(), /网络连接失败/);
  await page.unroute('**/api/v1/admin/analytics?*');
  await page.getByRole('button', { name: '重试', exact: true }).click(); await days(90);
  evidence.push('503/500 and network failures retain the correctly labeled previous report and recover through retry.');

  const api = await context.request.get(fixture.base + '/api/v1/admin/analytics?days=90');
  const report = await api.json();
  for (const mutation of ['empty', 'paused', 'malformed', 'unsafe-text']) {
    const data = structuredClone(report);
    if (mutation === 'empty') {
      data.views = data.article_views = data.searches = data.misses = 0;
      data.sources = []; data.terms = []; data.articles = [];
      data.trend.forEach(row => { row.views = row.searches = row.misses = 0; });
    } else if (mutation === 'paused') data.enabled = false;
    else if (mutation === 'malformed') delete data.views;
    else data.sources[0].label = '<img src=x onerror="window.analyticsInjected=true">';
    await page.route('**/api/v1/admin/analytics?*', route => route.fulfill({ json: data }));
    await page.getByRole('button', { name: '刷新报告', exact: true }).click();
    if (mutation === 'empty') await page.getByText('本期还没有请求记录。采集后的真实计数会显示在这里。').waitFor();
    if (mutation === 'paused') { await page.getByText('采集已暂停。以下为保留的历史报告，暂停期间不会产生新记录。').waitFor(); assert.notEqual(await page.locator('[data-metric="01"]').textContent(), '0'); }
    if (mutation === 'malformed') await page.getByRole('button', { name: '重试', exact: true }).waitFor();
    if (mutation === 'unsafe-text') {
      await page.waitForFunction(() => document.querySelector('.analytics-react-ranks')?.textContent.includes('<img'));
      assert.equal(await page.locator('[data-analytics-root] img').count(), 0);
      assert.equal(await page.evaluate(() => window.analyticsInjected), undefined);
    }
    await page.unroute('**/api/v1/admin/analytics?*');
  }
  evidence.push('Zero data, paused collection with history, malformed payloads, and hostile source labels are handled without fabricated counts or HTML execution.');

  for (const status of [401, 403]) {
    await page.goto(fixture.base + '/admin/analytics/?days=30#interactive');
    await page.reload(); await ready();
    await page.route('**/api/v1/admin/analytics?*', route => route.fulfill({ status, contentType: 'application/json', body: '{"error":{}}' }));
    await page.getByRole('button', { name: '刷新报告', exact: true }).click();
    await page.getByRole('alert').waitFor();
    assert.equal(await page.locator('[data-metric]').count(), 0);
    assert.equal(await page.locator('[data-analytics-report]').count(), 0);
    assert.equal(await page.locator('[data-analytics-fallback] table').count(), 0);
    await page.unroute('**/api/v1/admin/analytics?*');
  }
  evidence.push('401/403 clear all report components, the bootstrap payload, and the stale fallback report.');

  // Replacing a managed region must disconnect observers and remove React effects.
  await page.reload(); await ready();
  const nativeHTML = await (await context.request.get(fixture.base + '/admin/analytics/?days=30')).text();
  const lifecycle = await page.evaluate(html => {
    const host = document.querySelector('[data-analytics]');
    document.dispatchEvent(new CustomEvent('chunnel:admin-before-replace', { detail: { regions: [host] } }));
    const state = { children: host.querySelector('[data-analytics-root]').childElementCount, chart: host.querySelector('[data-analytics-root] [_echarts_instance_]') !== null, ready: host.querySelector('[data-react-ready]') !== null };
    const incoming = new DOMParser().parseFromString(html, 'text/html').querySelector('[data-analytics]');
    host.replaceWith(incoming);
    document.dispatchEvent(new CustomEvent('chunnel:admin-updated', { detail: { regions: [incoming] } }));
    return state;
  }, nativeHTML);
  assert.deepEqual(lifecycle, { children: 0, chart: false, ready: false });
  await ready();
  assert.equal(await page.locator('[data-analytics-root] h1').count(), 1);
  evidence.push('Native region teardown unmounts the report; reinitialization produces one root without duplicate charts.');

  await page.getByRole('link', { name: '近 7 天', exact: true }).focus();
  await page.keyboard.press('Enter'); await days(7);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.screenshot({ path: resolve(directory, 'interactive-desktop-light.png'), fullPage: true });
  await page.evaluate(() => window.ChunnelColorMode.set('dark'));
  await page.screenshot({ path: resolve(directory, 'interactive-desktop-dark.png'), fullPage: true });
  for (const width of [390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `${width}px horizontal overflow`);
    await page.screenshot({ path: resolve(directory, `interactive-mobile-${width}-dark.png`), fullPage: true });
  }
  await page.evaluate(() => window.ChunnelColorMode.set('light'));
  await page.screenshot({ path: resolve(directory, 'interactive-mobile-light.png'), fullPage: true });
  await page.getByRole('link', { name: '简洁报告', exact: true }).click();
  await page.locator('[data-analytics-fallback]').waitFor({ state: 'visible' });
  assert.equal(page.url().includes('#interactive'), false);
  evidence.push('Keyboard period changes, light/dark modes, 320/390px layouts, and return to the native report work.');

  const blocked = await browser.newContext({ storageState: await context.storageState() });
  const blockedPage = await blocked.newPage();
  await blockedPage.route('**/newpress-analytics.js?*', route => route.abort());
  await blockedPage.goto(fixture.base + '/admin/analytics/?days=30#interactive');
  await blockedPage.getByText('交互图表未能加载，可继续查看当前报告或重试。').waitFor();
  assert.equal(await blockedPage.locator('[data-analytics-fallback]').isVisible(), true);
  await blockedPage.unroute('**/newpress-analytics.js?*');
  await blockedPage.getByRole('button', { name: '试用交互图表', exact: true }).click();
  await blockedPage.locator('[data-react-ready="true"]').waitFor();
  assert.equal(await blockedPage.locator('link[href*="/newpress-analytics.css"]').count(), 1);
  assert.equal(await blockedPage.locator('script[src*="/newpress-analytics.js"]').count(), 1);
  await blockedPage.getByRole('link', { name: '简洁报告', exact: true }).click();
  await blockedPage.getByRole('link', { name: '近 7 天', exact: true }).click();
  await blockedPage.waitForURL('**/?days=7');
  await blocked.close();
  const blockedCSS = await browser.newContext({ storageState: await context.storageState() });
  const blockedCSSPage = await blockedCSS.newPage();
  await blockedCSSPage.route('**/newpress-analytics.css?*', route => route.abort());
  await blockedCSSPage.goto(fixture.base + '/admin/analytics/?days=30#interactive');
  await blockedCSSPage.getByText('交互图表未能加载，可继续查看当前报告或重试。').waitFor();
  assert.equal(await blockedCSSPage.locator('[data-analytics-fallback]').isVisible(), true);
  await blockedCSSPage.unroute('**/newpress-analytics.css?*');
  await blockedCSSPage.getByRole('button', { name: '试用交互图表', exact: true }).click();
  await blockedCSSPage.locator('[data-react-ready="true"]').waitFor();
  assert.equal(await blockedCSSPage.locator('link[href*="/newpress-analytics.css"]').count(), 1);
  assert.equal(await blockedCSSPage.locator('script[src*="/newpress-analytics.js"]').count(), 1);
  await blockedCSS.close();
  const brokenChart = await browser.newContext({ storageState: await context.storageState() });
  const brokenPage = await brokenChart.newPage();
  await brokenPage.goto(fixture.base + '/admin/analytics/?days=30');
  const beforeFallback = await brokenPage.locator('[data-sparkline] polyline').count();
  await brokenPage.evaluate(() => {
    const NativeObserver = window.ResizeObserver;
    window.ResizeObserver = class {
      constructor(callback) { this.observer = new NativeObserver(callback); }
      observe(element) {
        if (element.classList.contains('analytics-react-chart')) throw new Error('fixture chart initialization failure');
        this.observer.observe(element);
      }
      disconnect() { this.observer.disconnect(); }
      unobserve(element) { this.observer.unobserve(element); }
    };
    window.restoreAnalyticsObserver = () => { window.ResizeObserver = NativeObserver; };
  });
  await brokenPage.getByRole('button', { name: '试用交互图表', exact: true }).click();
  await brokenPage.getByText('交互图表初始化失败，已恢复简洁报告。').waitFor();
  assert.equal(await brokenPage.locator('[data-analytics-fallback]').isVisible(), true);
  assert.equal(await brokenPage.locator('[data-react-ready]').count(), 0);
  assert.equal(await brokenPage.locator('[data-sparkline] polyline').count(), beforeFallback);
  assert.equal(brokenPage.url().includes('#interactive'), false);
  await brokenPage.getByRole('link', { name: '近 7 天', exact: true }).click();
  await brokenPage.waitForURL('**/?days=7');
  await brokenPage.evaluate(() => window.restoreAnalyticsObserver());
  await brokenPage.getByRole('button', { name: '试用交互图表', exact: true }).click();
  await brokenPage.locator('[data-react-ready="true"]').waitFor();
  await brokenChart.close();
  evidence.push('A chart initialization exception restores the native URL and report without duplicate native charts, and permits navigation and a later retry.');
  const nojs = await browser.newContext({ javaScriptEnabled: false, storageState: await context.storageState() });
  const nojsPage = await nojs.newPage();
  await nojsPage.goto(fixture.base + '/admin/analytics/?days=30');
  assert.equal(await nojsPage.locator('[data-analytics-fallback]').isVisible(), true);
  await nojsPage.getByRole('link', { name: '近 7 天', exact: true }).click();
  await nojsPage.waitForURL('**/?days=7');
  assert.equal(await nojsPage.locator('[data-analytics-daily] tbody tr').count(), 7);
  await nojs.close();
  evidence.push('Blocked optional JS/CSS preserves the native report; retry reuses successful resources without duplication. Disabled JavaScript preserves period navigation.');

  await page.goto(fixture.base + fixture.login + '?role=editor');
  assert.equal((await page.request.get(fixture.base + '/api/v1/admin/analytics')).status(), 200);
  await page.goto(fixture.base + fixture.login + '?role=reader');
  assert.equal((await page.request.get(fixture.base + '/api/v1/admin/analytics')).status(), 403);
  const guest = await browser.newContext();
  assert.equal((await guest.request.get(fixture.base + '/api/v1/admin/analytics')).status(), 401);
  await guest.close();
  await page.goto(fixture.base + fixture.login);
  for (const path of ['/', '/admin/']) {
    calls.length = 0;
    // The authenticated public shell has a long-lived event connection.
    // Wait for page readiness rather than requiring that connection to close.
    await page.goto(fixture.base + path, { waitUntil: 'load' });
    await page.waitForTimeout(1000);
    assert.equal(calls.some(url => url.includes('newpress-analytics')), false);
  }
  evidence.push('Real editor/reader/anonymous API authorization is 200/403/401; public and ordinary admin pages load no analytics packages.');
  assert.deepEqual(errors, []);
  await writeFile(resolve(directory, 'browser-acceptance.json'), JSON.stringify({ passed: true, chromium: browser.version(), evidence, errors }, null, 2) + '\n');
  console.log(JSON.stringify({ passed: evidence.length, errors }));
} catch (error) {
  await page.screenshot({ path: resolve(directory, 'failure.png'), fullPage: true }).catch(() => {});
  await writeFile(resolve(directory, 'browser-acceptance-failure.json'), JSON.stringify({ message: error.message, stack: error.stack, evidence, errors }, null, 2));
  throw error;
} finally { await browser.close(); }
