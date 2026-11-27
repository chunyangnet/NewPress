import { chromium } from 'playwright';
import { mkdir, writeFile } from 'node:fs/promises';

const base = process.env.NEXORA_VISUAL_URL || 'http://127.0.0.1:19433';
const output = process.env.NEXORA_VISUAL_OUTPUT || 'output/nexora-visual-audit';
const browser = await chromium.connectOverCDP('http://127.0.0.1:19434');
const context = await browser.newContext();
const page = await context.newPage();
const states = ['home', 'explore', 'single', 'page', 'series', 'account', 'account-access', 'billing', 'shop', 'feedback-directory', 'feedback-list', 'feedback', 'questions-list', 'questions', 'circles', 'links', 'flash', 'flash-single', 'flash-submit', 'notification-unsubscribe', 'announcements-list', 'announcements', '404', 'maintenance', 'community'];
const results = [];
await mkdir(output, { recursive: true });
for (const width of [320, 390, 768, 1024, 1440]) {
  await page.setViewportSize({ width, height: 1000 });
  for (const state of states) {
    const failedResources = [];
    const onResponse = response => { if (response.status() >= 400) failedResources.push({ url: response.url(), status: response.status() }); };
    page.on('response', onResponse);
    const response = await page.goto(`${base}/__nexora_visual/${state}?static=1`, { waitUntil: 'networkidle' });
    await page.evaluate(async () => { document.querySelectorAll('img[loading="lazy"]').forEach(img => { img.loading = 'eager'; }); await document.fonts.ready; await Promise.all([...document.images].map(img => img.decode().catch(() => {}))); });
    const metrics = await page.evaluate(() => {
      const visible = el => el.getClientRects().length && getComputedStyle(el).visibility !== 'hidden';
      const rect = el => { const r = el.getBoundingClientRect(); return { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) }; };
      return {
        overflow: document.documentElement.scrollWidth > innerWidth + 1,
        offenders: [...document.querySelectorAll('body *')].filter(el => visible(el) && !el.closest('dialog,svg') && (el.getBoundingClientRect().right > innerWidth + 1 || el.getBoundingClientRect().left < -1)).slice(0, 15).map(el => ({ tag: el.tagName, class: el.className, ...rect(el) })),
        brokenImages: [...document.images].filter(el => visible(el) && el.complete && !el.naturalWidth).map(el => el.src),
        headings: [...document.querySelectorAll('main h1')].map(el => ({ text: el.textContent, size: getComputedStyle(el).fontSize, ...rect(el) })),
        header: rect(document.querySelector('.site-header')),
        main: rect(document.querySelector('main')),
      };
    });
    page.off('response', onResponse);
    results.push({ width, state, status: response.status(), failedResources, ...metrics });
    if ([390, 1440].includes(width)) await page.screenshot({ path: `${output}/${state}-${width}.png`, fullPage: true });
  }
}
await writeFile(`${output}/metrics.json`, JSON.stringify(results, null, 2));
console.log(JSON.stringify({ total: results.length, failures: results.filter(x => x.status !== 200 || x.overflow || x.brokenImages.length || x.failedResources.length) }, null, 2));
await context.close();
await browser.close();
