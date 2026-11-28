// Compare rendered shell and dialog styles against an explicit saved CSS baseline.
// The fixture serves real Go templates; every browser mutation is intercepted.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';

const directory = resolve(process.argv[2]);
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const before = resolve(process.argv[3]);
const engines = { chromium, firefox, webkit }, selected = process.argv[4] ? [process.argv[4]] : Object.keys(engines);
const originals = new Map();
for (const file of ['chunnel-home.css', 'chunnel-home-glass.css', 'chunnel-home-overlays.css', 'chunnel-home-overlays-glass.css']) {
  originals.set(file, await readFile(resolve(before, 'themes/chunnel/assets/css', file)));
}
const evidence = [];
for (const name of selected) {
  const browser = await engines[name].launch({ headless: true });
  const snapshots = [], errors = [], writes = [], differences = [];
  const context = await browser.newContext({ reducedMotion: 'reduce' });
  await context.addInitScript(() => { localStorage.setItem('chunnel-cookie-consent', 'rejected'); });
  const page = await context.newPage(); page.setDefaultTimeout(12000);
  page.on('pageerror', error => errors.push(error.message));
  let baseline = true, overlayRequests = 0;
  await context.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url()), file = url.pathname.split('/').pop();
    if (url.origin !== fixture.url) return route.abort('blockedbyclient');
    if (!['GET', 'HEAD'].includes(request.method())) {
      writes.push({ method: request.method(), path: url.pathname });
      return route.fulfill({ status: 409, contentType: 'application/json', body: '{"error":"intercepted"}' });
    }
    if (/^chunnel-home-overlays/.test(file)) overlayRequests++;
    if (baseline && originals.has(file)) return route.fulfill({ contentType: 'text/css', body: originals.get(file) });
    return route.fallback();
  });
  const collect = selector => page.locator(selector).evaluate(root => {
    const properties = ['display', 'position', 'color', 'backgroundColor', 'backgroundImage', 'fontFamily', 'fontSize', 'fontWeight', 'lineHeight', 'letterSpacing',
      'borderTopColor', 'borderTopWidth', 'borderTopStyle', 'borderTopLeftRadius', 'borderBottomRightRadius', 'boxShadow', 'backdropFilter',
      // Spacing is compared through rendered rectangles below. Chromium can
      // serialize the same centered auto margin as either 0px or its used value.
      'paddingTop', 'paddingRight', 'paddingBottom', 'paddingLeft',
      'gap', 'gridTemplateColumns', 'gridTemplateRows', 'flexDirection', 'flexWrap', 'alignItems', 'justifyContent', 'overflowX', 'overflowY', 'opacity', 'zIndex'];
    return [root, ...root.querySelectorAll('*')].filter(node => node.getClientRects().length && getComputedStyle(node).visibility !== 'hidden').map(node => {
      // Force layout before requesting used values such as auto margins.
      const rect = node.getBoundingClientRect(), css = getComputedStyle(node);
      return { tag: node.tagName, class: node.getAttribute('class') || '', role: node.getAttribute('role') || '',
        rect: { x: Math.round(rect.x * 10) / 10, y: Math.round(rect.y * 10) / 10, width: Math.round(rect.width * 10) / 10, height: Math.round(rect.height * 10) / 10 },
        properties: Object.fromEntries(properties.map(key => [key, css[key]])) };
    });
  });
  const launchHeader = async selector => {
    const control = page.locator(`header ${selector}`).filter({ visible: true }).first();
    if (!await control.count()) await page.locator('header [data-nav-toggle]').click();
    await control.click();
  };
  try {
    assert.equal((await context.request.post(fixture.url + '/fixture/reload')).status(), 204);
    await page.goto(fixture.url + '/');
    for (const style of ['classic', 'liquid-glass']) {
      assert.equal((await context.request.post(fixture.url + '/fixture/style/' + style)).status(), 204);
      for (const width of [1280, 390]) for (const color of ['light', 'dark']) {
        const pair = [];
        for (baseline of [true, false]) {
          await page.setViewportSize({ width, height: 900 });
          // Compare each palette from its first paint. Interactive switching is
          // covered separately; inherited-color transitions are not a CSS diff.
          await page.evaluate(color => localStorage.setItem('newpress.colorMode', color), color);
          const beforeRequests = overlayRequests;
          assert.equal((await page.goto(fixture.url + '/')).status(), 200);
          await page.waitForLoadState('networkidle');
          assert.equal(await page.evaluate(() => window.ChunnelColorMode.theme), color);
          await page.waitForFunction(() => {
            const brand = document.querySelector('.brand-type b'), icon = document.querySelector('[data-search-box] button svg');
            return brand && icon && getComputedStyle(brand).color === getComputedStyle(document.body).color && getComputedStyle(icon).color === getComputedStyle(icon.parentElement).color;
          });
          const consent = page.locator('[data-cookie-reject]').first();
          if (await consent.isVisible()) await consent.click();
          await page.evaluate(() => document.fonts.ready);
          await page.evaluate(() => scrollTo(0, 0));
          await page.waitForTimeout(180);
          assert.equal(overlayRequests, beforeRequests, 'No dialog stylesheet before explicit activation');
          const record = { shell: await collect('.site-header'), dialogs: {} };
          await launchHeader('[data-account-login]');
          await page.locator('#account-login-modal[open]').waitFor();
          await page.waitForLoadState('networkidle'); await page.evaluate(() => document.fonts.ready);
          await page.waitForTimeout(180);
          record.dialogs.login = await collect('#account-login-modal');
          await page.locator('#account-login-modal [data-modal-close]').filter({ visible: true }).last().click();
          await page.locator('#account-login-modal[open]').waitFor({ state: 'hidden' });
          await launchHeader('[data-saved-open]');
          await page.locator('#saved-drawer[open]').waitFor();
          await page.waitForTimeout(180);
          record.dialogs.saved = await collect('#saved-drawer');
          if (!baseline && color === 'dark') await page.screenshot({ path: resolve(directory, `overlay-${name}-${style}-${width}.png`) });
          await page.keyboard.press('Escape'); await page.locator('#saved-drawer[open]').waitFor({ state: 'hidden' });
          // Use the same delegated launch contract as the mobile shortcut.
          await page.evaluate(() => {
            const button = document.createElement('button'); button.type = 'button'; button.dataset.modalOpen = 'quick-actions';
            button.id = 'overlay-quick-probe'; button.textContent = '快捷操作'; document.body.append(button);
          });
          await page.locator('#overlay-quick-probe').click(); await page.locator('#quick-actions[open]').waitFor();
          await page.waitForTimeout(180); record.dialogs.quick = await collect('#quick-actions');
          await page.keyboard.press('Escape'); await page.locator('#quick-actions[open]').waitFor({ state: 'hidden' });
          assert.equal(overlayRequests, beforeRequests + 1, 'Dialog flows reuse one stylesheet request');
          pair.push(record);
        }
        const scenario = { browser: name, style, width, color };
        try { assert.deepEqual(pair[1], pair[0]); }
        catch (error) { differences.push({ ...scenario, before: pair[0], after: pair[1] }); throw error; }
        snapshots.push({ ...scenario, result: pair[1] }); evidence.push(scenario);
        console.log(`${name}: ${style} ${width} ${color}: header, login, saved and quick dialog styles match the original CSS.`);
      }
    }
    assert.deepEqual(errors, []); assert.deepEqual(writes, []);
  } catch (error) {
    await page.screenshot({ path: resolve(directory, `overlay-${name}-failure.png`) }).catch(() => {});
    throw error;
  } finally {
    await writeFile(resolve(directory, `overlay-${name}-styles.json`), JSON.stringify({ evidence: evidence.filter(item => item.browser === name), snapshots, differences, errors, writes }, null, 2) + '\n');
    await context.close(); await browser.close();
  }
}
await writeFile(resolve(directory, 'overlay-styles-acceptance.json'), JSON.stringify({ evidence }, null, 2) + '\n');
