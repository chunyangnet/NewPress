import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium } from 'playwright';

const directory = resolve(process.argv[2] || 'output/playwright/feature-status-audit-20261126');
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const page = await context.newPage();
page.setDefaultTimeout(8000);
const results = [], errors = [];
page.on('pageerror', error => errors.push(error.message));
page.on('dialog', dialog => dialog.accept());
const root = () => page.locator('[data-feature-status]');
const go = path => page.goto(fixture.base + path);
const refresh = async () => {
  const response = page.waitForResponse(r => r.url().includes('/api/v1/admin/feature-status?'));
  await page.evaluate(() => document.dispatchEvent(new Event('newpress:feature-status-refresh')));
  await response;
  await page.waitForFunction(() => document.querySelector('[data-feature-status]').getAttribute('aria-busy') === 'false');
};
const modules = changes => page.evaluate(async changes => {
  const snapshot = await (await fetch('/api/v1/admin/modules')).json();
  const response = await fetch('/api/v1/admin/modules', { method: 'PATCH', headers: {
    'Content-Type': 'application/json', 'X-CSRF-Token': document.querySelector('[data-feature-status]').dataset.csrf,
  }, body: JSON.stringify({ revision: snapshot.revision, modules: changes }) });
  if (!response.ok) throw new Error('fixture setup: ' + response.status);
}, changes);
const check = async (name, fn) => {
  try { await fn(); results.push({ name, passed: true }); console.log('PASS', name); }
  catch (error) { results.push({ name, passed: false, error: error.message }); console.error('FAIL', name, error.message); }
};

try {
  await go(fixture.login);
  await check('focus returns to the disclosure when a focused repair action disappears during refresh', async () => {
    await modules({ comments: false });
    await go('/admin/comments/');
    await page.locator('[data-feature-overview]').evaluate(el => el.open = true);
    await page.locator('[data-feature-brief] [data-feature-module]').focus();
    await modules({ comments: true });
    await refresh();
    assert.equal(await root().getAttribute('data-state'), 'ready');
    assert.equal(await page.locator('[data-feature-overview] > summary').evaluate(el => el === document.activeElement), true);
  });
  await check('modified and pre-cancelled configuration links retain native browser behavior', async () => {
    await go('/admin/progression/');
    await refresh();
    await page.locator('#progression-experience [name=enabled]').waitFor({ state: 'attached' });
    const outcomes = await page.evaluate(() => {
      const link = document.querySelector('[data-feature-brief] [data-feature-target]');
      if (!link) throw new Error('missing fixture configuration action');
      return [{ ctrlKey: true }, { metaKey: true }, { shiftKey: true }, { altKey: true }, { button: 1 }, { cancelled: true }].map(options => {
        const event = new MouseEvent('click', { bubbles: true, cancelable: true, ...options });
        if (options.cancelled) event.preventDefault();
        let prevented, moved;
        const original = document.activeElement;
        document.addEventListener('click', e => {
          prevented = e.defaultPrevented;
          moved = document.activeElement !== original;
          e.preventDefault(); // Suppress navigation in this event-contract test only.
        }, { once: true });
        link.dispatchEvent(event);
        return { options, prevented, moved };
      });
    });
    for (const { options, prevented, moved } of outcomes) {
      assert.equal(prevented, !!options.cancelled, JSON.stringify(options));
      assert.equal(moved, false, 'modified link moved focus: ' + JSON.stringify(options));
    }
    await page.locator('[data-feature-brief] [data-feature-target]').click();
    assert.equal(await page.locator('#progression-experience [name=enabled]').evaluate(el => el === document.activeElement), true);
  });
  await check('returning to a cached page rechecks immediately and ignores a late module result', async () => {
    await go('/admin/comments/');
    await modules({ comments: false });
    await go('/admin/comments/');
    await page.evaluate(() => {
      const original = window.fetch;
      window.auditReads = 0;
      window.fetch = (url, options) => {
        if (url === '/api/v1/admin/modules' && options?.method === 'PATCH') {
          // Model a transport whose completion was queued before pagehide.
          return new Promise(resolve => window.auditRelease = () => resolve(new Response('{}', { status: 200 })));
        }
        if (String(url).includes('/api/v1/admin/feature-status?')) window.auditReads++;
        return original(url, options);
      };
    });
    await page.locator('[data-feature-brief] [data-feature-module]').click();
    await page.waitForFunction(() => typeof window.auditRelease === 'function');
    await page.evaluate(() => {
      window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true }));
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }));
    });
    const readsOnReturn = await page.evaluate(() => window.auditReads);
    if (readsOnReturn) await page.waitForFunction(() => document.querySelector('[data-feature-status]').getAttribute('aria-busy') === 'false');
    const feedback = await page.locator('[data-feature-feedback]').textContent();
    await page.evaluate(async () => { window.auditRelease(); await new Promise(resolve => setTimeout(resolve, 50)); });
    assert.ok(readsOnReturn >= 1, 'page restoration was blocked by an obsolete write');
    assert.equal(await page.locator('[data-feature-feedback]').textContent(), feedback, 'late write announced unconfirmed success');
    assert.equal(await page.locator('[data-feature-brief] [data-feature-module]').isEnabled(), true);
    assert.equal(await root().getAttribute('data-state'), 'paused');
    await page.screenshot({ path: resolve(directory, 'audit-restored-comments.png') });
  });
  await check('changed wallet limits show a partial state and presets can be repaired in place', async () => {
    await go('/admin/recognition/');
    await page.locator('[data-recognition-settings]').waitFor({ state: 'visible' });
    const original = await page.evaluate(async () => {
      const { settings } = await (await fetch('/api/v1/admin/currencies/main')).json();
      const { settings: recognition } = await (await fetch('/api/v1/admin/recognition')).json();
      const response = await fetch('/api/v1/admin/currencies/main', { method: 'PUT', headers: {
        'Content-Type': 'application/json', 'X-CSRF-Token': document.querySelector('[data-feature-status]').dataset.csrf,
      }, body: JSON.stringify({ settings: { ...settings, min_transfer: '10.00' } }) });
      if (!response.ok) throw new Error('wallet setup: ' + response.status);
      return { wallet: settings, recognition };
    });
    try {
      await refresh();
      assert.equal(await root().getAttribute('data-state'), 'partial');
      await root().screenshot({ path: resolve(directory, 'audit-preset-status.png') });
      await page.locator('[data-feature-brief]').getByRole('link', { name: '调整打赏钱包', exact: true }).click();
      assert.equal(await page.locator('[data-recognition-currencies]').evaluate(el => el === document.activeElement), true);
      await page.locator('[data-feature-overview]').evaluate(el => el.open = true);
      await page.locator('[data-feature-id=tips] details').evaluate(el => el.open = true);
      await page.getByRole('link', { name: '调整快捷数值', exact: true }).click();
      const presets = page.locator('[data-currency=main] [data-currency-presets]');
      assert.equal(await presets.evaluate(el => el === document.activeElement), true);
      await page.screenshot({ path: resolve(directory, 'audit-preset-conflict.png') });
      await presets.fill('10, 20');
      await page.getByRole('button', { name: '保存互动设置', exact: true }).click();
      await page.waitForFunction(() => document.querySelector('[data-feature-status]').dataset.state === 'ready');
      assert.equal(await presets.inputValue(), '10, 20');
    } finally {
      await page.evaluate(async original => {
        const { settings } = await (await fetch('/api/v1/admin/currencies/main')).json();
        const response = await fetch('/api/v1/admin/currencies/main', { method: 'PUT', headers: {
          'Content-Type': 'application/json', 'X-CSRF-Token': document.querySelector('[data-feature-status]').dataset.csrf,
        }, body: JSON.stringify({ settings: { ...original.wallet, version: settings.version } }) });
        if (!response.ok) throw new Error('wallet cleanup: ' + response.status);
        const { settings: recognition } = await (await fetch('/api/v1/admin/recognition')).json();
        const restored = await fetch('/api/v1/admin/recognition', { method: 'PUT', headers: {
          'Content-Type': 'application/json', 'X-CSRF-Token': document.querySelector('[data-feature-status]').dataset.csrf,
        }, body: JSON.stringify({ ...original.recognition, version: recognition.version }) });
        if (!restored.ok) throw new Error('recognition cleanup: ' + restored.status);
      }, original);
    }
  });
  await check('topic editor uses its own object-scoped status and refreshes successfully', async () => {
    await go(`/admin/plugins/ai-pro-max/articles/${fixture.post_id}/topics/`);
    const status = JSON.parse(await root().getAttribute('data-status'));
    assert.equal(status.feature, 'ai-related');
    assert.equal(status.object, String(fixture.post_id));
    await refresh();
    assert.equal(await root().getAttribute('data-state'), 'ready');
    assert.notEqual(await root().getAttribute('data-stale'), 'true');
  });
  assert.deepEqual(errors, [], 'browser JavaScript errors');
} finally {
  const label = process.argv.includes('--before') ? 'before' : 'after';
  await writeFile(resolve(directory, `verification-audit-${label}.json`), JSON.stringify({ results, errors }, null, 2));
  await browser.close();
}
assert.ok(results.every(result => result.passed), 'audit regressions failed');
