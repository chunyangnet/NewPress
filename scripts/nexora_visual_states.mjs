import { chromium } from 'playwright';
import { mkdir, writeFile } from 'node:fs/promises';

const base = process.env.NEXORA_VISUAL_URL || 'http://127.0.0.1:19435';
const output = process.env.NEXORA_VISUAL_OUTPUT || 'output/nexora-visual-audit/states';
await mkdir(output, { recursive: true });
const browser = await chromium.connectOverCDP('http://127.0.0.1:19434');
const context = await browser.newContext();
const page = await context.newPage();
const errors = [];
page.on('pageerror', error => errors.push({ url: page.url(), stack: error.stack }));
const results = [];
async function capture(name, width) {
  await page.evaluate(async () => { await document.fonts.ready; await Promise.all(document.getAnimations().filter(a => a.effect?.getTiming().iterations !== Infinity).map(a => a.finished.catch(() => {}))); });
  await page.screenshot({ path: `${output}/${name}-${width}.png`, fullPage: false });
  results.push({ name, width, ...await page.evaluate(() => ({ overflow: document.documentElement.scrollWidth > innerWidth + 1, dialogs: [...document.querySelectorAll('dialog[open]')].map(el => { const r = el.getBoundingClientRect(); return { id: el.id, x: r.x, right: r.right, y: r.y, bottom: r.bottom, scroll: el.scrollHeight, height: el.clientHeight }; }) })) });
}
for (const width of [320, 390, 768, 1024, 1440]) {
  await page.setViewportSize({ width, height: 900 });
  await page.goto(`${base}/account/`, { waitUntil: 'networkidle' });
  await page.locator('main [data-account-login]').click();
  await page.locator('#account-login-modal').waitFor({ state: 'visible' });
  await capture('login', width);
  await page.keyboard.press('Escape');
  await page.goto(`${base}/account/access/`, { waitUntil: 'networkidle' });
  await capture('registration', width);
  await page.goto(`${base}/__component_fixture_reader`, { waitUntil: 'networkidle' });
  await page.goto(`${base}/account/`, { waitUntil: 'networkidle' });
  await capture('account-member', width);
  await page.locator('[data-account-menu] summary').click();
  await capture('account-menu', width);
  await page.locator('[data-account-menu] [data-drawer-open="notification-drawer"]').click();
  await page.locator('#notification-drawer').waitFor({ state: 'visible' });
  await capture('notifications', width);
  await page.keyboard.press('Escape');
  await page.goto(`${base}/account/?view=profile`, { waitUntil: 'networkidle' });
  await capture('profile', width);
  await page.goto(`${base}/__nexora_visual/home?long=1&static=1`, { waitUntil: 'networkidle' });
  if (width <= 760) { await page.locator('[data-menu-toggle]').click(); await capture('mobile-nav-long-brand', width); }
  else await capture('header-long-brand', width);
  await context.clearCookies();
}
await writeFile(`${output}/metrics.json`, JSON.stringify({ results, errors }, null, 2));
console.log(JSON.stringify({ count: results.length, failures: results.filter(x => x.overflow || x.dialogs.some(d => d.x < -1 || d.right > x.width + 1 || d.y < -1 || d.bottom > 901)), errors }, null, 2));
await context.close();
await browser.close();
