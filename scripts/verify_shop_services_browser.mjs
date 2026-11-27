// Real Go pages and reads; all business writes stay in this browser's interception layer.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';
import { compileFrontendEntry } from './frontend_entries.mjs';

const directory = resolve(process.argv[2]), fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const engines = { chromium, firefox, webkit }, names = process.argv[3] && process.argv[3] !== 'all' ? [process.argv[3]] : Object.keys(engines);
const variant = process.argv[4] === 'typescript' ? 'typescript' : 'generated', assets = new Map(), evidence = [];
if (variant === 'typescript') for (const name of ['shop-services', 'shop-service-editor', 'shop-coupons']) assets.set(`newpress-${name}.js`, (await compileFrontendEntry(name, { minify: false })).code);
const until = async predicate => { const end = Date.now() + 15000; while (!predicate()) { if (Date.now() > end) throw Error('Expected intercepted request'); await new Promise(resolve => setTimeout(resolve, 20)); } };
for (const name of names) {
  const browser = await engines[name].launch(), context = await browser.newContext({ viewport: { width: 1365, height: 960 }, reducedMotion: 'reduce', timezoneId: 'America/Los_Angeles' }), page = await context.newPage();
  page.setDefaultTimeout(15000);
  const errors = [], writes = [], external = [], scripts = [], counts = new Map();
  let mode = 'valid', held = null, heldRead = null, sessionFailure = false, adminOverride = null;
  const json = (route, value, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(value) });
  const pendingKey = (owner, code = 'PHOTO-5') => `newpress.shop.coupon.pending.${owner}.${code}`;
  const check = message => { evidence.push({ browser: name, variant, message }); console.log(`${name}: ${message}`); };
  page.on('pageerror', error => errors.push(error.message)); page.on('dialog', dialog => dialog.accept());
  page.on('request', request => { if (request.resourceType() === 'script') scripts.push(request.url()); });
  await context.request.get(fixture.login);
  const actualSession = await (await context.request.get(fixture.url + '/api/v1/shop/session')).json();
  const actualAdmin = await (await context.request.get(fixture.url + '/api/v1/admin/shop')).json();
  assert.equal(actualSession.user_id, String(fixture.owner_id));
  await context.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method(), file = path.split('/').at(-1);
    if (url.origin !== fixture.url) { external.push({ url: url.href, method }); return route.abort(); }
    if (assets.has(file)) return route.fulfill({ contentType: 'text/javascript', body: assets.get(file) });
    if (path.startsWith('/fixture/')) return route.continue();
    if (method === 'GET' || method === 'HEAD') {
      if (path === '/api/v1/admin/shop' && adminOverride) return json(route, adminOverride);
      if (path === '/api/v1/shop/session') {
        if (sessionFailure) return json(route, { error: { message: '验收：账户读取暂不可用' } }, 503);
        if (mode === 'unauthorized') return json(route, { error: { message: '验收：登录已失效' } }, 401);
        const response = await route.fetch();
        if (!response.ok()) return route.fulfill({ response });
        const value = await response.json();
        for (const option of value.coupons) {
          const count = counts.get(`${value.user_id}:${option.code}`) || 0;
          option.user_claim_count += count; option.available_count += count; option.claim_count += count;
          option.claimed = option.available_count > 0;
          option.remaining = option.claim_limit ? Math.max(0, option.claim_limit - option.claim_count) : 0;
          option.can_claim = option.user_claim_count < option.per_user_limit && (!option.claim_limit || option.remaining > 0);
        }
        if (mode === 'invalid-session') value.csrf_token = '';
        if (mode === 'hold-session') { mode = 'valid'; heldRead = () => json(route, value); return; }
        return json(route, value);
      }
      return route.continue();
    }
    const body = request.postDataJSON(), headers = request.headers(); writes.push({ path, method, body, csrf: headers['x-csrf-token'] });
    if (path === '/api/v1/shop/coupons/claim') {
      const owner = headers['x-csrf-token'] === actualSession.csrf_token ? String(fixture.owner_id) : String(fixture.reader_id);
      const receipt = { claim: { id: 'sc_0123456789abcdef0123456789ab', code: body.code, user_id: owner, status: 'available', claimed_at: new Date().toISOString(), used_at: '0001-01-01T00:00:00Z' } };
      if (mode === 'invalid') { receipt.claim.user_id = '999'; return json(route, receipt); }
      if (mode === 'rate-limit') return json(route, { error: { message: '验收：请稍后领取' } }, 429);
      const reply = async () => {
        const key = `${owner}:${body.code}`; counts.set(key, (counts.get(key) || 0) + 1);
        if (mode === 'refresh-fail') sessionFailure = true;
        await json(route, receipt);
      };
      if (mode === 'hold') { held = reply; return; }
      return reply();
    }
    if (path === '/api/v1/admin/shop' && method === 'PUT') {
      adminOverride = { ...actualAdmin, catalog: { ...body, version: String(BigInt(body.version) + 1n) } };
      return json(route, adminOverride);
    }
    throw Error(`Unexpected business write: ${method} ${path}`);
  });
  const text = (selector, expected) => page.waitForFunction(({ selector, expected }) => document.querySelector(selector)?.textContent.includes(expected), { selector, expected });
  const open = path => page.goto(fixture.url + path, { waitUntil: 'domcontentloaded' });
  const coupon = '[data-coupon-code="PHOTO-5"]', button = () => page.locator(coupon + ' button[data-code]');
  const ready = () => page.locator(coupon).waitFor({ state: 'visible' });
  const reset = async () => {
    mode = 'valid'; sessionFailure = false; counts.clear();
    await page.evaluate(() => sessionStorage.clear()); await open('/'); await ready();
    await page.waitForFunction(() => document.querySelector('[data-coupon-code="PHOTO-5"] button[data-code]')?.textContent === '领取');
  };
  const submitProduct = () => page.locator('[data-shop-product-form]').evaluate(form => form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })));
  const openEditor = async (index = 0) => {
    await open('/admin/shop/'); await page.locator(`[data-shop-edit="${index}"]`).click();
    await page.locator('[data-shop-editor-tab="services"]').click();
    await page.locator('[data-shop-services-fields]').waitFor({ state: 'visible' });
  };
  try {
    await open('/'); await ready();
    assert.equal(await page.locator('[data-coupon-code]').count(), 2);
    await text(coupon, '¥5.00'); await text('[data-coupon-code="POINTS-12"]', '0.12');
    assert.equal(await page.locator('[data-coupon-code="MARKET-ONLY"]').count(), 0);
    const expectedDate = new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit' }).format(new Date(actualSession.coupons[0].ends_at));
    await text(coupon, expectedDate); check('Actual coupon metadata retains wallet precision, site dates and homepage visibility rules.');

    mode = 'hold'; await button().click(); await until(() => held);
    const single = writes.length; await button().evaluate(element => { element.click(); element.click(); }); assert.equal(writes.length, single);
    await held(); held = null; await text('[data-home-coupon-status]', '已领取'); await page.waitForFunction(() => document.querySelector('[data-coupon-code="PHOTO-5"] button')?.textContent === '再领一张');
    assert.equal(await page.evaluate(key => sessionStorage.getItem(key), pendingKey(fixture.owner_id)), null);
    check('Double clicks produce one claim and recovery clears only after a matched receipt and refreshed count.');

    await reset(); mode = 'invalid'; await button().click(); await text('[data-home-coupon-status]', '回执未确认');
    const invalidWrites = writes.length; mode = 'valid'; await button().click(); await text('[data-home-coupon-status]', '暂未确认'); assert.equal(writes.length, invalidWrites);
    await open('/'); await ready(); assert.equal(await button().textContent(), '核对领取结果');
    counts.set(`${fixture.owner_id}:PHOTO-5`, 1); await button().click(); await text('[data-home-coupon-status]', '领取记录已更新');
    assert.equal(writes.length, invalidWrites); check('Mismatched receipts survive reload; reconciliation performs reads and never repeats a non-idempotent claim.');

    await reset(); mode = 'refresh-fail'; await button().click(); await text('[data-home-coupon-status]', '已领取，列表刷新失败');
    const confirmedWrites = writes.length; sessionFailure = false; mode = 'valid'; await button().click(); await text('[data-home-coupon-status]', '领取记录已更新');
    assert.equal(writes.length, confirmedWrites); check('A confirmed claim remains successful when its follow-up read fails.');

    await reset(); mode = 'rate-limit'; await button().click(); await text('[data-home-coupon-status]', '先核对'); const limitedWrites = writes.length;
    mode = 'valid'; await button().click(); await text('[data-home-coupon-status]', '可重新选择领取'); assert.equal(writes.length, limitedWrites);
    check('A rejected claim refreshes eligibility before an explicit new attempt.');

    await reset(); mode = 'invalid-session'; const beforeInvalidSession = writes.length; await button().click(); await text('[data-home-coupon-status]', '领券账号');
    assert.equal(writes.length, beforeInvalidSession); mode = 'valid'; check('Missing CSRF or invalid account data blocks the claim before sending a write.');

    await reset(); await page.evaluate(() => { Storage.prototype.setItem = () => { throw new Error('验收：存储不可用'); }; });
    const storageWrites = writes.length; await button().click(); await text('[data-home-coupon-status]', '存储不可用'); assert.equal(writes.length, storageWrites);
    await open('/'); await ready(); await page.evaluate(key => sessionStorage.setItem(key, '{invalid'), pendingKey(fixture.owner_id)); await open('/'); await ready();
    await text(coupon, '记录待核对'); assert.equal(await button().count(), 0); check('Unavailable storage and corrupt recovery records cannot silently start another claim.');

    await reset(); mode = 'hold'; await button().click(); await until(() => held);
    await page.evaluate(() => window.dispatchEvent(new Event('pagehide'))); await held().catch(() => {}); held = null;
    assert.equal(await page.locator('[data-home-coupons]').isHidden(), true); const hiddenWrites = writes.length;
    mode = 'valid'; await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))); await ready();
    assert.equal(writes.length, hiddenWrites); check('Page retirement hides account state and BFcache restoration revalidates without a repeated write.');

    await reset(); mode = 'hold-session'; await open('/'); await until(() => heldRead);
    await context.request.get(fixture.url + '/fixture/login/reader');
    const reader = await (await context.request.get(fixture.url + '/api/v1/shop/session')).json();
    await page.evaluate(identity => document.dispatchEvent(new CustomEvent('chunnel:session', { detail: identity })), { authenticated: true, id: Number(reader.user_id), csrf_token: reader.csrf_token });
    await ready(); await heldRead().catch(() => {}); heldRead = null;
    await button().click(); await text('[data-home-coupon-status]', '已领取'); assert.equal(writes.at(-1).csrf, reader.csrf_token);
    check('A late previous-account read cannot restore its token or claim state after an account switch.');
    await context.request.get(fixture.login); await reset(); mode = 'unauthorized'; await button().click(); await text('[data-home-coupon-status]', '登录已失效');
    assert.equal(await button().count(), 0); mode = 'valid'; check('Session expiry removes claim controls instead of retaining an old account token.');

    await context.request.get(fixture.url + '/fixture/login/guest'); await open('/'); await ready();
    assert.equal(await page.locator(coupon + ' a').getAttribute('href'), '/shop/?claim=PHOTO-5'); assert.equal(await button().count(), 0);
    const denied = await context.request.get(fixture.url + '/api/v1/admin/shop'); assert.equal(denied.status(), 401);
    await context.request.get(fixture.login); check('Guests receive the existing explicit login/claim link and cannot access administration.');

    await open('/shop/product/group/'); await text('[data-shop-detail-service]', '2');
    assert.equal(await page.locator('[data-shop-detail-service] a[href="#shop-product-reviews"]').count(), 1); check('Actual storefront cards interpolate the current group terms and retain accessible section links.');

    await openEditor(); await text('[data-service-preview-count]', '3 项');
    await page.locator('[data-service-mode="custom"]').click(); await page.locator('[data-service-edit="0"]').click();
    const title = page.locator('[data-service-row="0"] [data-service-field="title"]');
    await title.fill('温暖🙂说明'); await title.evaluate(input => { input.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true })); input.value = '中文输入说明'; input.dispatchEvent(new InputEvent('input', { bubbles: true, isComposing: true })); input.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true })); });
    assert.equal(await title.inputValue(), '中文输入说明'); assert.equal(await title.evaluate(input => input === document.activeElement), true);
    await page.locator('[data-service-row="0"] .sa-service-variables summary').click(); await title.focus(); await title.evaluate(input => input.setSelectionRange(0, 2));
    await page.locator('[data-service-row="0"] [data-service-token="结算货币"]').click(); assert.equal(await title.inputValue(), '{结算货币}输入说明');
    await text('[data-service-preview]', '人民币输入说明'); check('Service editing preserves IME input, focus and replacement selections while updating the resolved preview.');

    await page.locator('[data-service-row="0"] [data-service-move="1"]').click(); await text('[data-service-row="1"] [data-service-row-title]', '{结算货币}输入说明');
    await page.locator('[data-service-row="1"] .sa-service-icon-picker summary').click(); await page.locator('[data-service-row="1"] [data-service-icon="wallet"]').click();
    await page.locator('[data-service-row="1"] [data-service-link="#shop-product-reviews"]').click();
    await page.locator('[data-service-search]').fill('拼团'); await page.locator('[data-service-template="group-size"]').check();
    await text('[data-service-selected-count]', '4 / 60'); await page.locator('[data-service-template="group-size"]').uncheck();
    await text('[data-service-selected-count]', '3 / 60');
    await page.locator('[data-service-mode="hidden"]').click(); await text('[data-service-preview-count]', '已关闭');
    await page.locator('[data-service-mode="custom"]').click(); await text('[data-service-selected-count]', '3 / 60');
    check('Reordering, icons, link presets, template search and hidden/custom toggles preserve the configured selection.');

    await page.locator('[data-service-edit="1"]').click();
    // The row can already be open after reordering and mode changes.
    if (!await page.locator('[data-service-row="1"] [data-service-field="link"]').isVisible()) await page.locator('[data-service-edit="1"]').click();
    await page.locator('[data-service-row="1"] [data-service-field="link"]').fill('javascript:alert(1)');
    const beforeInvalidSave = writes.length; await submitProduct(); await text('[data-shop-editor-status]', '链接请使用'); assert.equal(writes.length, beforeInvalidSave);
    await page.locator('[data-service-row="1"] [data-service-field="link"]').fill('/shop/orders/');
    await page.locator('[data-service-add]').click(); await submitProduct(); await text('[data-shop-editor-status]', '标题需填写');
    await page.locator('[data-service-row="3"] [data-service-field="title"]').fill('创作使用指南');
    await page.locator('[data-service-row="3"] [data-service-field="description"]').fill('开始前请阅读商品说明');
    await submitProduct(); await page.locator('[data-shop-editor]').waitFor({ state: 'hidden' });
    const saved = writes.at(-1).body.products[0].services; assert.equal(saved.mode, 'custom'); assert.equal(saved.items.length, 4); assert.equal(saved.items[1].title, '{结算货币}输入说明'); assert.equal(saved.items[1].link, '/shop/orders/');
    check('Invalid text or links block saving; a valid product save retains exact module order and source tokens.');

    await openEditor();
    await page.evaluate(({ library, product }) => {
      window.__serviceChanged = 0;
      const options = { root: document.querySelector('[data-shop-services-fields]'), getLibrary: () => library, getProduct: () => product, getContext: () => ({ products: [], currencies: [] }), isLocked: () => false, onChange: () => { window.__serviceChanged++; } };
      const stale = window.NewPressShopServiceEditor.create(options);
      window.NewPressShopServiceEditor.create(options); stale.destroy();
      window.__serviceEditor = window.NewPressShopServiceEditor.create(options); window.__serviceEditor.reset(product);
    }, { library: actualAdmin.service_library, product: { ...actualAdmin.catalog.products[0], services: { mode: 'custom', items: [] } } });
    await page.locator('[data-service-add]').click(); assert.equal(await page.evaluate(() => window.__serviceChanged), 1);
    await page.evaluate(() => window.dispatchEvent(new Event('pagehide')));
    assert.equal(await page.locator('[data-service-add]').isDisabled(), true);
    assert.match(await page.evaluate(() => { try { window.__serviceEditor.collect(); return ''; } catch (error) { return error.message; } }), /刷新页面/);
    check('Replacing an editor removes old listeners; retired editors reject collection and further edits.');

    adminOverride = null; sessionFailure = false; mode = 'valid'; counts.clear();
    for (const [style, scheme, width] of [['classic', 'light', 1365], ['liquid-glass', 'dark', 390]]) {
      await context.request.post(fixture.url + '/fixture/style/' + style); await page.setViewportSize({ width, height: 960 }); await page.emulateMedia({ colorScheme: scheme });
      for (const route of ['home', 'admin', 'shop']) {
        if (route === 'home') { await open('/'); await page.evaluate(() => sessionStorage.clear()); await open('/'); await ready(); }
        else if (route === 'admin') await openEditor(1);
        else { await open('/shop/product/group/'); await text('[data-shop-detail-service]', '2'); }
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false, `${style} ${width} ${route}`);
        await page.screenshot({ path: resolve(directory, `${name}-${variant}-${style}-${width}-${route}.png`), fullPage: true });
      }
      check(`${style} ${scheme} at ${width}px has no horizontal overflow on home, shop and service editing.`);
    }
    await context.request.get(fixture.url + '/fixture/login/reader');
    assert.equal((await context.request.get(fixture.url + '/api/v1/admin/shop')).status(), 403);
    assert.deepEqual(errors, []); assert.deepEqual(external, []); assert.equal(scripts.some(url => /analytics-react|echarts|react-dom/.test(url)), false);
    check('No page errors, external requests or analytics dependencies; readers cannot access shop administration.');
    await writeFile(resolve(directory, `${name}-${variant}.json`), JSON.stringify({ browser: name, variant, checks: evidence.filter(item => item.browser === name), mutationsIntercepted: writes.length, pageErrors: errors, externalRequests: external, scripts: [...new Set(scripts)] }, null, 2) + '\n');
  } finally { await browser.close(); }
}
await writeFile(resolve(directory, `summary-${variant}.json`), JSON.stringify(evidence, null, 2) + '\n');
console.log(`${evidence.length} checks passed.`);
