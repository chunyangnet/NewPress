import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { gzipSync } from 'node:zlib';
import { chromium } from 'playwright';

const directory = resolve(process.argv[2]);
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const interactive = process.argv.includes('--interactive');
const prefix = interactive ? 'interactive-' : '';
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, colorScheme: 'light', reducedMotion: 'reduce' });
const page = await context.newPage();
const errors = [];
page.on('pageerror', error => errors.push(error.message));
try {
  await page.goto(fixture.base + fixture.login);
  if (interactive) {
    await page.getByRole('button', { name: '试用交互图表', exact: true }).click();
    await page.locator('[data-react-ready="true"]').waitFor();
    await page.locator('[data-analytics-root] svg').first().waitFor();
  }
  await page.waitForLoadState('networkidle');
  const timing = () => page.evaluate(() => performance.getEntriesByType('resource').filter(item => ['script', 'link'].includes(item.initiatorType)).map(item => ({ name: item.name, encoded: item.encodedBodySize, decoded: item.decodedBodySize, transfer: item.transferSize, start: item.startTime, duration: item.duration })));
  const cold = await timing();
  const resources = await page.locator('link[rel="stylesheet"], script[src]').evaluateAll(elements => elements.map(el => el.href || el.src));
  const measured = [];
  for (const url of resources) {
    const response = await context.request.get(url), data = await response.body();
    measured.push({ url: new URL(url).pathname, status: response.status(), bytes: data.length,
      gzip5: data.length < 512 ? data.length : gzipSync(data, { level: 5 }).length,
      kind: response.headers()['content-type']?.includes('text/css') ? 'CSS' : 'JS',
      cacheControl: response.headers()['cache-control'], etag: response.headers().etag });
  }
  await page.screenshot({ path: resolve(directory, prefix + 'desktop-light.png'), fullPage: true });
  await page.reload();
  if (interactive) await page.locator('[data-react-ready="true"]').waitFor();
  await page.waitForLoadState('networkidle');
  const warm = await timing();
  await page.evaluate(() => window.ChunnelColorMode?.set('dark'));
  await page.screenshot({ path: resolve(directory, prefix + 'desktop-dark.png'), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: resolve(directory, prefix + 'mobile-dark.png'), fullPage: true });
  await page.evaluate(() => window.ChunnelColorMode?.set('light'));
  await page.screenshot({ path: resolve(directory, prefix + 'mobile-light.png'), fullPage: true });
  for (const days of [7, 30, 90]) {
    const response = await context.request.get(fixture.base + `/api/v1/admin/analytics?days=${days}`);
    await writeFile(resolve(directory, `report-${days}.json`), JSON.stringify(await response.json(), null, 2) + '\n');
  }
  const result = { mode: interactive ? 'interactive (complete dependency graph)' : 'default lightweight', chromium: browser.version(), compression: 'Node zlib gzip level 5 projection of actual HTTP bodies; browser encoded sizes below are separate',
    totals: Object.fromEntries(['CSS', 'JS'].map(kind => [kind, measured.filter(item => item.kind === kind).reduce((sum, item) => sum + item.gzip5, 0)])),
    requests: measured.length, measured, cold, warm, errors };
  await writeFile(resolve(directory, prefix + 'measurements.json'), JSON.stringify(result, null, 2) + '\n');
  console.log(JSON.stringify({ totals: result.totals, requests: result.requests, errors }));
  if (process.argv.includes('--stop')) await context.request.post(fixture.base + fixture.stop);
  if (errors.length) process.exitCode = 1;
} finally { await browser.close(); }
