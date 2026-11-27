import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium } from 'playwright';

// Uses the same disposable server as verify_feature_status_browser.mjs.
const directory = resolve(process.argv[2] || 'output/playwright/feature-status-compact-20261126');
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const page = await context.newPage();
page.setDefaultTimeout(12000);
const passed = [], errors = [], measurements = {};
page.on('pageerror', error => errors.push(error.message));
page.on('dialog', dialog => dialog.accept());
const root = () => page.locator('[data-feature-status]');
const details = () => page.locator('[data-feature-overview]');
const toggle = () => page.locator('.feature-status-toggle');
const check = async (name, fn) => { await fn(); passed.push(name); console.log('PASS', name); };
const go = path => page.goto(fixture.base + path);
const refresh = async () => {
  const response = page.waitForResponse(r => r.url().includes('/api/v1/admin/feature-status?'));
  await page.evaluate(() => document.dispatchEvent(new Event('newpress:feature-status-refresh')));
  await response;
  await page.waitForFunction(() => document.querySelector('[data-feature-status]')?.getAttribute('aria-busy') === 'false');
  assert.notEqual(await root().getAttribute('data-stale'), 'true');
};
const measure = async name => {
  const height = await root().evaluate(el => Math.round(el.getBoundingClientRect().height));
  measurements[name] = height;
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), true, 'no horizontal overflow');
  return height;
};
const modules = async changes => page.evaluate(async changes => {
  const snapshot = await (await fetch('/api/v1/admin/modules')).json();
  const response = await fetch('/api/v1/admin/modules', { method: 'PATCH', headers: {
    'Content-Type': 'application/json', 'X-CSRF-Token': document.querySelector('[data-feature-status]').dataset.csrf,
  }, body: JSON.stringify({ revision: snapshot.revision, modules: changes }) });
  if (!response.ok) throw new Error('fixture setup: ' + response.status);
}, changes);

try {
  await go(fixture.login);
  await check('multiple states start collapsed with one cause and a count; refresh retains the compact height', async () => {
    await go('/admin/progression/');
    assert.equal(await details().evaluate(el => el.open), false);
    assert.equal(await page.locator('[data-feature-items]').isVisible(), false);
    const data = JSON.parse(await root().getAttribute('data-status'));
    assert.equal(await page.locator('[data-feature-more]').textContent(), data.overview.additional ? `还有 ${data.overview.additional} 项待处理` : '查看详情');
    const initial = await page.locator('[data-feature-brief]').textContent();
    assert.ok(await measure('blockedDesktop') <= 100);
    await page.screenshot({ path: resolve(directory, 'compact-progression-desktop.png') });
    await refresh();
    assert.equal(await details().evaluate(el => el.open), false);
    assert.equal((await page.locator('[data-feature-brief]').textContent()).replace(/\s/g,''), initial.replace(/\s/g,''));
    assert.equal(await measure('blockedDesktopAfterRefresh'), measurements.blockedDesktop);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.waitForTimeout(500); // Let the desktop sidebar finish leaving the mobile viewport.
    assert.ok(await measure('blockedMobile') <= 150);
    await page.screenshot({ path: resolve(directory, 'compact-progression-mobile.png') });
    await page.getByRole('button', { name: '深色模式', exact: true }).click();
    await page.waitForFunction(() => document.documentElement.dataset.theme === 'dark');
    await page.waitForTimeout(600); // The color reveal briefly overlays the previous palette.
    await page.screenshot({ path: resolve(directory, 'compact-progression-mobile-dark.png') });
    await page.getByRole('button', { name: '浅色模式', exact: true }).click();
    await page.waitForFunction(() => document.documentElement.dataset.theme === 'light');
    await page.waitForTimeout(600);
  });
  await check('keyboard disclosure, nested conditions and focus survive a refresh', async () => {
    await toggle().focus(); await page.keyboard.press('Enter');
    assert.equal(await details().evaluate(el => el.open), true);
    const nested = page.locator('[data-feature-id="progression-checkin"] details');
    await nested.locator('summary').focus(); await page.keyboard.press('Enter');
    await refresh();
    assert.equal(await details().evaluate(el => el.open), true);
    assert.equal(await nested.evaluate(el => el.open), true);
    assert.equal(await nested.locator('summary').evaluate(el => el === document.activeElement), true);
    await toggle().click();
    assert.equal(await page.locator('[data-feature-items]').isVisible(), false);
  });
  await check('unsaved status is integrated into the save bar without increasing the top area', async () => {
    const checkbox = page.locator('#progression-experience [name=enabled]');
    await page.locator('[data-settings-target="progression-experience"]').click();
    await checkbox.setChecked(!await checkbox.isChecked(), { force: true });
    const hint = page.locator('[data-feature-draft]');
    await hint.waitFor({ state: 'visible' });
    assert.equal(await hint.evaluate(el => !!el.closest('[data-admin-savebar]')), true);
    await refresh();
    assert.equal(await measure('blockedMobileWithDraft'), measurements.blockedMobile);
    await page.locator('[data-admin-discard]').click();
    await hint.waitFor({ state: 'hidden' });
  });
  await check('ready state stays a single line on mobile and desktop', async () => {
    await go('/admin/navigation/');
    assert.equal(await root().getAttribute('data-state'), 'ready');
    assert.ok(await measure('readyMobile') <= 64, 'one row with a touch-sized disclosure');
    await refresh();
    assert.equal(await measure('readyMobileAfterRefresh'), measurements.readyMobile);
    await page.screenshot({ path: resolve(directory, 'compact-ready-mobile.png') });
    await page.setViewportSize({ width: 1440, height: 1000 });
    assert.ok(await measure('readyDesktop') <= 64);
    await page.screenshot({ path: resolve(directory, 'compact-ready-desktop.png') });
  });
  await check('failed detection remains visible while collapsed and a successful read clears the warning', async () => {
    await page.route('**/api/v1/admin/feature-status?*', route => route.abort());
    await page.evaluate(() => document.dispatchEvent(new Event('newpress:feature-status-refresh')));
    await page.waitForFunction(() => document.querySelector('[data-feature-status]').dataset.stale === 'true');
    assert.equal(await page.locator('[data-feature-stale]').isVisible(), true);
    assert.equal(await page.locator('[data-feature-feedback]').getAttribute('data-visible'), 'true');
    assert.equal(await details().evaluate(el => el.open), false);
    await page.unrouteAll(); await refresh();
    assert.equal(await page.locator('[data-feature-stale]').isVisible(), false);
    assert.equal(await measure('readyAfterNetworkRecovery'), measurements.readyDesktop);
  });
  await check('article states stay in publication settings; only a site-wide pause has a top reminder', async () => {
    await modules({ comments: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await go(`/admin/content/${fixture.post_id}/edit/`);
    assert.equal(await root().evaluate(el => !!el.closest('#editor-publication-panel')), true);
    assert.equal(await page.locator('[data-feature-editor-alert]').isVisible(), false);
    assert.equal(await root().isVisible(), false);
    await page.screenshot({ path: resolve(directory, 'compact-editor-mobile.png') });
    await page.locator('[data-editor-settings-toggle]').click();
    await page.locator('[data-editor-settings-tab="publication"]').click();
    await page.locator('[name=comment_status]').selectOption('closed', { force: true });
    await page.locator('[data-editor-settings-close]').click();
    await page.locator('[data-content-editor] button[name=action][value=save]').click();
    await page.waitForFunction(() => document.querySelector('[data-feature-status]').dataset.state === 'paused');
    assert.equal(await page.locator('[data-feature-editor-alert]').isVisible(), false);
    await modules({ comments: false });
    await refresh();
    assert.equal(await root().isVisible(), false, 'background reads must not open the drawer');
    assert.equal(await page.locator('[data-feature-editor-alert]').isVisible(), true);
    await page.locator('[data-feature-open-settings]').click();
    assert.equal(await page.locator('[data-editor-settings-tab="publication"]').getAttribute('aria-pressed'), 'true');
    assert.equal(await root().isVisible(), true);
    await page.locator('[data-feature-brief] [data-feature-module="comments"]').click();
    await page.locator('[data-feature-editor-alert]').waitFor({ state: 'hidden' });
    assert.equal(await root().getAttribute('data-state'), 'paused', 'local comment pause remains');
    assert.equal(await page.locator('[name=comment_status]').inputValue(), 'closed');
    await page.screenshot({ path: resolve(directory, 'compact-editor-publication-mobile.png') });
  });
  await check('native disclosure also works without JavaScript', async () => {
    const nativeContext = await browser.newContext({ javaScriptEnabled: false, viewport: { width: 390, height: 844 } });
    const native = await nativeContext.newPage();
    await native.goto(fixture.base + fixture.login); await native.goto(fixture.base + '/admin/progression/');
    assert.equal(await native.locator('[data-feature-items]').isVisible(), false);
    await native.locator('.feature-status-toggle').click();
    assert.equal(await native.locator('[data-feature-items]').isVisible(), true);
    await nativeContext.close();
  });
  assert.deepEqual(errors, []);
  await writeFile(resolve(directory, 'verification-compact.json'), JSON.stringify({ passed, measurements, errors }, null, 2));
  console.log(JSON.stringify(measurements));
} catch (error) {
  await page.screenshot({ path: resolve(directory, 'compact-failure.png') }).catch(() => {});
  await writeFile(resolve(directory, 'verification-compact-failure.json'), JSON.stringify({ passed, measurements, errors, error: String(error) }, null, 2));
  throw error;
} finally { await browser.close(); }
