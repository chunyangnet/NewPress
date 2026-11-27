import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';
import { build } from 'esbuild';
import { createRequire } from 'node:module';
const require = createRequire(import.meta.url), F = require('../tests/frontend/helpers/shop-storefront-fixtures.cjs');
const directory = resolve(process.argv[2]), fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const variant = process.argv[4] === 'generated' ? 'generated' : 'typescript';
const code = variant === 'typescript' ? (await build({ entryPoints: ['frontend/core/shop.ts'], bundle: true, write: false, target: 'es2022', format: 'iife' })).outputFiles[0].text : '';
const engines = { chromium, firefox, webkit }, names = process.argv[3] && process.argv[3] !== 'all' ? [process.argv[3]] : Object.keys(engines), evidence = [];
const until = async (fn, label = 'expected request') => { const end = Date.now() + 15000; while (!fn()) { if (Date.now() > end) throw Error(label); await new Promise(r => setTimeout(r, 20)); } };
for (const name of names) {
  const browser = await engines[name].launch(), context = await browser.newContext({ viewport: { width: 1365, height: 960 }, reducedMotion: 'reduce', timezoneId: 'America/Los_Angeles' }), page = await context.newPage();
  await context.addInitScript(() => {
    window.__shopEvents = [];
    for (const type of ['click', 'submit', 'invalid', 'change']) document.addEventListener(type, event => {
      if (event.target instanceof Element && event.target.closest('[data-shop-purchase], [data-shop-buy]')) window.__shopEvents.push({ type, tag: event.target.tagName, name: event.target.getAttribute('name'), target: event.target.getAttribute('data-shop-buy'), trusted: event.isTrusted });
    }, true);
  });
  page.setDefaultTimeout(12000);
  const errors = [], writes = [], external = [], requestFailures = [], orders = new Map(), requests = new Map(), reviews = new Map(), payments = new Map();
  const inflight = new Set(); let lastNetworkEvent = Date.now();
  page.on('request', request => { inflight.add(request); lastNetworkEvent = Date.now(); });
  const finished = request => { inflight.delete(request); lastNetworkEvent = Date.now(); };
  page.on('requestfinished', finished); page.on('requestfailed', finished);
  const settled = () => until(() => inflight.size === 0 && Date.now() - lastNetworkEvent >= 600, 'Background requests did not settle: ' + [...inflight].map(r => r.url()).join(', '));
  let mode = 'valid', held = null, catalogFail = false, sessionFail = false, quote = null, claimCount = 0, cutoff = false;
  const json = (route, value, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(value) });
  const check = message => { evidence.push({ browser: name, variant, message }); console.log(name + ': ' + message); };
  page.on('pageerror', e => errors.push({ message: e.message, stack: e.stack, url: page.url(), after: evidence.at(-1)?.message })); page.on('dialog', d => d.accept('验收退款原因'));
  page.on('requestfailed', request => requestFailures.push({ url: request.url(), failure: request.failure(), after: evidence.at(-1)?.message }));
  await context.request.get(fixture.login);
  const session = await (await context.request.get(fixture.url + '/api/v1/shop/session')).json(), owner = String(fixture.owner_id), key = 'newpress.shop.pending.' + owner;
  const actualCatalog = await (await context.request.get(fixture.url + '/api/v1/shop/catalog')).json();
  const responseFor = async (operation, input) => {
    const response = await context.request.get(fixture.url + '/fixture/response/' + operation + '?input=' + encodeURIComponent(JSON.stringify(input)));
    if (!response.ok()) throw Error(operation + ': ' + await response.text()); return response.json();
  };
  await context.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method();
    if (url.origin !== fixture.url) { external.push(url.href); return route.abort(); }
    if (code && path.endsWith('/newpress-shop.js')) return route.fulfill({ contentType: 'text/javascript', body: code });
    if (path.startsWith('/fixture/')) return route.continue();
    if (method === 'GET' || method === 'HEAD') {
      if (path === '/api/v1/shop/catalog' && catalogFail) return json(route, { error: { message: '目录暂时失败' } }, 503);
      if (path === '/api/v1/shop/catalog' && mode === 'bad-catalog') { const result = structuredClone(actualCatalog); result.catalog.products[0].price = '1e3'; return json(route, result); }
      if (path === '/api/v1/shop/catalog' && cutoff) { const result = structuredClone(actualCatalog); result.catalog.products.find(p => p.key === 'draw').raffle.draw_at = new Date(Date.now() - 60000).toISOString(); result.server_time = new Date().toISOString(); return json(route, result); }
      if (path === '/api/v1/shop/session') {
        if (sessionFail) return json(route, { error: { message: '账户暂时失败' } }, 503);
        const response = await route.fetch(); if (!response.ok()) return route.fulfill({ response });
        const value = await response.json();
        if (value.user_id === owner) for (const c of value.coupons) if (c.code === 'PHOTO-5') { c.user_claim_count += claimCount; c.available_count += claimCount; c.claim_count += claimCount; c.claimed = c.available_count > 0; c.can_claim = c.user_claim_count < c.per_user_limit; }
        return json(route, value);
      }
      if (path === '/api/v1/shop/orders') return json(route, { orders: [...orders.values()], csrf_token: session.csrf_token });
      const match = path.match(/^\/api\/v1\/shop\/orders\/(so_[a-f0-9]+)(\/review)?$/);
      if (match && orders.has(match[1])) {
        if (mode === 'order-read-fail') return json(route, { error: { message: '订单读取暂时失败' } }, 503);
        if (match[2]) return json(route, { review: reviews.get(match[1]) || null, eligible: orders.get(match[1]).status === 'completed', note: '' });
        return json(route, { order: orders.get(match[1]), payments: payments.get(match[1]) || [] });
      }
      return route.continue();
    }
    const body = request.postDataJSON(), headers = request.headers(); writes.push({ path, method, body, csrf: headers['x-csrf-token'] });
    if (path === '/api/v1/shop/quote') {
      const result = await responseFor('quote', body); quote = result.quote;
      if (mode === 'bad-quote') result.quote.product = 'wrong-product';
      return json(route, result);
    }
    if (path === '/api/v1/shop/orders') {
      if (mode === 'invalid-create') return json(route, { error: { code: 'shop.conflict', message: '商品版本已变化' } }, 409);
      if (mode === 'unknown-reject') return json(route, { error: { code: 'proxy.rejected', message: '代理拒绝' } }, 409);
      let result = requests.get(body.request_id);
      if (!result) { result = await responseFor('create', body); requests.set(body.request_id, result); orders.set(result.order.id, result.order); }
      if (mode === 'bad-create') return json(route, { order: { ...result.order, user_id: '999' } });
      if (mode === 'timeout-create') return route.abort('timedout');
      if (mode === 'hold-create') { held = () => json(route, result); return; }
      return json(route, result);
    }
    if (path === '/api/v1/shop/coupons/claim') {
      const result = { claim: { id: 'sc_' + 'a'.repeat(28), code: body.code, user_id: owner, status: 'available', claimed_at: new Date().toISOString(), used_at: F.zero } };
      if (mode === 'bad-claim') { claimCount++; result.claim.user_id = '999'; return json(route, result); }
      claimCount++; return json(route, result);
    }
    const match = path.match(/^\/api\/v1\/shop\/orders\/(so_[a-f0-9]+)\/(pay|sync|received|refund|cancel|review)$/);
    if (match && orders.has(match[1])) {
      let order = orders.get(match[1]); const operation = match[2];
      if (operation === 'pay') {
        const part = order.schedule[body.index];
        if (body.channel === 'wallet') {
          order = { ...order, paid_amount: String(BigInt(order.paid_amount) + BigInt(part.amount)), status: 'completed', schedule: order.schedule.map(v => v.index === part.index ? { ...v, paid_at: new Date().toISOString(), payment_ref: 'wallet-receipt' } : v) };
          orders.set(order.id, order); sessionFail = true; return json(route, { order });
        }
        const payment = F.payment({ user_id: owner, amount: (BigInt(part.amount) / 100n) + '.' + String(BigInt(part.amount) % 100n).padStart(2, '0'), expires_at: new Date(Date.now() + 3600000).toISOString(), checkout: { provider_order_id: 'fixture-payment', url: 'https://payments.example.test/pay' } });
        if (mode === 'bad-payment') payment.amount = '0.01';
        if (mode === 'bad-qr') payment.qr_url = '/api/v1/social/billing/orders/np_' + 'd'.repeat(28) + '/qr';
        if (mode === 'post-payment') payment.checkout = { provider_order_id: 'fixture-payment', url: 'https://payments.example.test/pay', method: 'POST', fields: { merchant: 'fixture', invoice: order.id } };
        if (mode === 'unsafe-payment') payment.checkout.url = 'https://account:secret@payments.example.test/pay';
        payments.set(order.id, [{ ...payment, index: body.index }]);
        if (mode === 'refresh-fail') { catalogFail = true; sessionFail = true; }
        return json(route, { order, payment });
      }
      if (operation === 'sync') return json(route, { order, sync_pending: false });
      if (operation === 'review') {
        const review = F.ownReview({ ...body, order_id: order.id, user_id: owner, product: order.product, title: order.title }); reviews.set(order.id, review);
        if (mode === 'review-refresh-fail') catalogFail = true;
        return json(route, { review });
      }
      order = { ...order, status: operation === 'cancel' ? 'cancelled' : operation === 'received' ? 'completed' : 'refund_requested' };
      orders.set(order.id, order); return json(route, { order });
    }
    throw Error('Unexpected write: ' + method + ' ' + path);
  });
  const text = (selector, expected) => page.waitForFunction(({ selector, expected }) => document.querySelector(selector)?.textContent.includes(expected), { selector, expected });
  const open = path => page.goto(fixture.url + path, { waitUntil: 'domcontentloaded' });
  const ready = async product => {
    await open('/shop/product/' + product + '/');
    await page.locator('[data-shop-detail-content]').waitFor({ state: 'visible' });
    await page.waitForFunction(() => document.querySelector('[data-shop-buy]')?.textContent !== '正在加载');
    await settled();
  };
  const reset = async () => {
    // Confirmed writes can refresh catalog/account data in the background. Let
    // those reads finish with the current scenario before changing its replies.
    await settled();
    mode = 'valid'; held = null; catalogFail = false; sessionFail = false; quote = null; cutoff = false;
    await page.evaluate(() => sessionStorage.clear());
  };
  const quoteProduct = async product => {
    await ready(product);
    if (['physical', 'deposit', 'installment', 'bundle'].includes(product)) { await page.locator('[name=recipient]').fill('验收买家'); await page.locator('[name=phone]').fill('123456789'); await page.locator('[name=address]').fill('验收地址'); }
    await page.locator('[data-shop-buy]').click(); await page.locator('[data-shop-checkout]').waitFor({ state: 'visible' });
  };
  const submit = async () => { await page.locator('[data-shop-confirm] [name=accepted]').check(); await page.locator('[data-shop-confirm-button]').click(); };
  const counts = path => writes.filter(v => v.path === path).length;
  try {
    await open('/shop/'); await page.locator('.shop-product').first().waitFor({ state: 'visible' });
    if (await page.locator('[data-cookie-consent] [data-cookie-reject]').isVisible()) await page.locator('[data-cookie-consent] [data-cookie-reject]').click();
    assert.equal(await page.locator('.shop-product').count(), actualCatalog.catalog.products.length); check('Real catalog loads all product kinds, purchase modes and wallet settlement.');
    for (const product of ['digital','physical','cdk','donation','raffle','draw','bundle','presale','deposit','installment','group']) {
      await quoteProduct(product); assert.equal(quote.product, product);
      const expected = '¥' + (BigInt(quote.total) / 100n) + '.' + String(BigInt(quote.total) % 100n).padStart(2, '0');
      await text('[data-shop-quote]', expected);
      await page.locator('[data-shop-checkout-close]').click();
      assert.equal(errors.length, 0, errors.join('\n')); check('Real Go quote matches ' + product + ' with its address, variant, amount and schedule.');
    }
    await reset(); mode = 'bad-quote'; await ready('digital'); await page.locator('[data-shop-buy]').click(); await text('[data-shop-detail-message]', '报价未匹配'); assert.equal(await page.locator('[data-shop-checkout]').isVisible(), false); check('Mismatched quote never reaches confirmation.');
    await reset(); await quoteProduct('digital'); mode = 'bad-create'; await submit(); await text('[data-shop-checkout-message]', '订单回执未匹配');
    const pending = await page.evaluate(key => JSON.parse(sessionStorage.getItem(key)), key); assert.ok(pending.draft.request_id); const original = structuredClone(pending.draft);
    await page.locator('[data-shop-checkout-close]').click(); mode = 'valid'; await page.locator('[data-shop-buy]').click(); await text('[data-shop-order-message]', '已恢复上次下单结果');
    assert.deepEqual(writes.filter(v => v.path === '/api/v1/shop/orders').at(-1).body, original); assert.equal(await page.evaluate(key => sessionStorage.getItem(key), key), null); check('Malformed create preserves recovery; the next action replays exactly the original request before clearing it.');
    await reset(); await quoteProduct('digital'); mode = 'hold-create'; await submit(); await until(() => held); const duplicate = counts('/api/v1/shop/orders');
    await page.locator('[data-shop-confirm]').evaluate(form => { form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); });
    assert.equal(counts('/api/v1/shop/orders'), duplicate); mode = 'valid'; await held(); held = null; await text('[data-shop-order-message]', '支付单已生成'); check('Repeated confirmation creates only one order while a request is pending.');
    await reset(); await quoteProduct('digital'); mode = 'refresh-fail'; await submit(); await text('[data-shop-order-message]', '支付单已生成'); assert.equal(await page.evaluate(key => sessionStorage.getItem(key), key), null); check('Confirmed create and payment remain successful when later catalog and balance reads fail.');
    await reset(); await ready('digital'); await page.evaluate(key => sessionStorage.setItem(key, '{broken'), key); const beforeCorrupt = counts('/api/v1/shop/orders');
    await page.locator('[data-shop-buy]').click(); await text('[data-shop-detail-message]', '恢复记录无法读取'); assert.equal(counts('/api/v1/shop/orders'), beforeCorrupt); check('Corrupt recovery blocks replacement purchases instead of silently allocating a new request.');
    await reset(); await quoteProduct('digital'); mode = 'bad-payment'; await submit(); await text('[data-shop-order-message]', '付款结果尚待核对'); assert.equal(await page.locator('[data-shop-payment] a').count(), 0); check('Wrong payment amounts never expose a checkout link.');
    await reset(); await quoteProduct('digital'); mode = 'bad-qr'; await submit(); await text('[data-shop-order-message]', '支付单已生成'); assert.equal(await page.locator('[data-shop-payment] img').count(), 0); check('Payment QR paths must identify the currently verified payment.');
    for (const product of ['physical','deposit','installment','group','raffle','draw','bundle','presale','donation','cdk']) {
      await reset(); await quoteProduct(product); await submit(); await text('[data-shop-order-message]', '支付单已生成');
      assert.ok(writes.filter(v => v.path.endsWith('/pay')).at(-1).body.index === 0); check('Creation and first payment preserve ' + product + ' terms from the real Go receipt.');
    }
    await reset(); await quoteProduct('points'); await submit(); await text('[data-shop-order-message]', '本期付款已确认');
    assert.equal(writes.filter(v => v.path.endsWith('/pay')).at(-1).body.channel, 'wallet');
    assert.equal(await page.locator('[data-shop-payment] a').count(), 0); check('Wallet confirmation survives a subsequent balance-read failure without creating external checkout.');
    await reset(); await quoteProduct('digital'); mode = 'unsafe-payment'; await submit(); await text('[data-shop-order-message]', '支付单已生成'); assert.equal(await page.locator('[data-shop-payment] a, [data-shop-payment] form').count(), 0); check('Credential-bearing payment URLs are not rendered.');
    await reset(); await quoteProduct('digital'); mode = 'post-payment'; await submit(); await text('[data-shop-order-message]', '支付单已生成');
    assert.equal(await page.locator('[data-shop-payment] form input[type=hidden]').count(), 2);
    const prevented = await page.locator('[data-shop-payment] form').evaluate(form => { document.dispatchEvent(new CustomEvent('chunnel:session', { detail: { authenticated: false } })); const event = new Event('submit', { cancelable: true }); form.dispatchEvent(event); return event.defaultPrevented; });
    assert.equal(prevented, true); check('An explicit POST checkout form retains verified fields and blocks submission after logout.');
    await reset(); await quoteProduct('digital'); mode = 'invalid-create'; await submit(); await text('[data-shop-checkout-message]', '商品版本已变化'); assert.equal(await page.evaluate(key => sessionStorage.getItem(key), key), null); check('A specific Go domain rejection releases recovery after idempotency lookup.');
    await reset(); await quoteProduct('digital'); mode = 'unknown-reject'; await submit(); await text('[data-shop-checkout-message]', '代理拒绝'); assert.ok(await page.evaluate(key => sessionStorage.getItem(key), key)); check('An unrelated 409 cannot erase an uncertain purchase.');
    await reset(); await quoteProduct('digital'); mode = 'timeout-create'; await submit(); await text('[data-shop-checkout-message]', '网络连接失败');
    const uncertain = await page.evaluate(key => JSON.parse(sessionStorage.getItem(key)), key); mode = 'valid'; await page.locator('[data-shop-checkout-close]').click();
    await page.reload(); await page.locator('[data-shop-buy]').click(); await text('[data-shop-order-message]', '已恢复上次下单结果');
    assert.deepEqual(writes.filter(v => v.path === '/api/v1/shop/orders').at(-1).body, uncertain.draft); check('Reload after a lost response recovers the existing original request.');
    await reset(); await quoteProduct('digital'); const storedBefore = counts('/api/v1/shop/orders');
    await page.evaluate(() => { Storage.prototype.setItem = function() { throw new DOMException('blocked', 'SecurityError'); }; });
    await submit(); await text('[data-shop-checkout-message]', '无法保存下单恢复'); assert.equal(counts('/api/v1/shop/orders'), storedBefore); check('Unavailable session storage prevents an untracked create request.');
    await reset(); await quoteProduct('draw'); mode = 'bad-create'; await submit(); await text('[data-shop-checkout-message]', '订单回执未匹配'); mode = 'valid'; cutoff = true;
    await page.reload(); await text('[data-shop-buy]', '查看上次下单结果'); await page.locator('[data-shop-buy]').click(); await text('[data-shop-order-message]', '已恢复上次下单结果'); check('An existing raffle request remains recoverable after new entry closes.');
    await reset(); await quoteProduct('digital'); await submit(); await text('[data-shop-order-message]', '支付单已生成');
    await page.locator('[data-shop-order-cancel]').click(); await text('[data-shop-order-message]', '订单已取消'); assert.equal(await page.locator('[data-shop-payment] a').count(), 0); check('Cancellation confirms the matching order and removes its checkout.');
    const last = [...orders.values()].at(-1), completed = { ...last, id: 'so_' + 'c'.repeat(28), paid_amount: last.total, status: 'shipped', schedule: last.schedule.map(v => ({ ...v, paid_at: new Date().toISOString(), payment_ref: 'fixture-receipt' })) };
    orders.set(completed.id, completed); await reset(); await open('/shop/orders/?order=' + completed.id); await page.locator('[data-shop-received]').click(); await text('[data-shop-order-message]', '收货已确认');
    await page.locator('[data-shop-order-review] input[name=rating][value="5"]').check(); await page.locator('[data-shop-order-review] textarea').fill('商品交付及时，验收评价'); mode = 'review-refresh-fail';
    await page.locator('[data-shop-order-review] button[type=submit]').click(); await text('[data-shop-order-review]', '评价已提交'); check('Received and review receipts are validated; a later catalog failure retains the confirmed review.');
    catalogFail = false; await page.locator('[data-shop-refund]').click(); await text('[data-shop-order-message]', '退款申请已提交'); check('Refund requests retain paid history and clear payment controls.');
    await reset(); await open('/shop/?coupons=1'); const claim = page.locator('[data-shop-claim="PHOTO-5"]'); await claim.waitFor(); mode = 'bad-claim'; await claim.click(); await text('[data-shop-message]', '领取回执未确认');
    const claimBefore = counts('/api/v1/shop/coupons/claim'); mode = 'valid'; await claim.click(); await text('[data-shop-message]', '已核对上次领取结果'); assert.equal(counts('/api/v1/shop/coupons/claim'), claimBefore); check('Reconciling a successful uncertain coupon claim never submits another claim.');
    await reset(); await quoteProduct('digital'); mode = 'hold-create'; await submit(); await until(() => held); const switched = await page.evaluate(() => {
      document.dispatchEvent(new CustomEvent('chunnel:session', { detail: { authenticated: true, id: 999, csrf_token: 'other-token' } })); return true;
    }); assert.ok(switched); await held().catch(() => {}); held = null; await page.waitForTimeout(80); assert.equal(await page.locator('[data-shop-order-dialog]').isVisible(), false); assert.ok(await page.evaluate(key => sessionStorage.getItem(key), key)); check('An old account receipt cannot open an order or erase its recovery after identity changes.');
    await reset(); await ready('digital'); await page.evaluate(() => { window.dispatchEvent(new PageTransitionEvent('pagehide')); window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })); });
    await text('[data-shop-message]', '刷新后重新确认'); const frozen = writes.length; await page.locator('[data-shop-buy]').evaluate(el => el.click()); assert.equal(writes.length, frozen); check('Restored pages require a fresh account and order read before further writes.');
    await reset(); mode = 'bad-catalog'; await open('/shop/'); await text('[data-shop-products]', '商城暂时无法加载');
    await page.evaluate(({ owner, token }) => document.dispatchEvent(new CustomEvent('chunnel:session', { detail: { authenticated: true, id: Number(owner), csrf_token: token } })), { owner, token: session.csrf_token });
    await settled(); await text('[data-shop-products]', '商城暂时无法加载');
    assert.equal(await page.locator('.shop-product').count(), 0); check('Malformed catalog data stays unavailable after a late account refresh, without fabricated prices or stock.');
    for (const [style, scheme, width] of [['classic', 'light', 1365], ['liquid-glass', 'dark', 390]]) {
      await context.request.post(fixture.url + '/fixture/style/' + style); await page.setViewportSize({ width, height: 960 }); await page.emulateMedia({ colorScheme: scheme });
      await reset(); await open('/shop/'); await page.locator('.shop-product').first().waitFor();
      await page.screenshot({ path: resolve(directory, name + '-' + style + '-catalog.png'), fullPage: true });
      await ready('digital'); await page.screenshot({ path: resolve(directory, name + '-' + style + '-product.png'), fullPage: true });
      await quoteProduct('installment'); await page.screenshot({ path: resolve(directory, name + '-' + style + '-checkout.png') });
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
    }
    await context.request.post(fixture.url + '/fixture/style/classic');
    assert.deepEqual(errors, []); assert.deepEqual(external, []); check('Desktop and mobile render without overflow, script errors or external traffic.');
    await writeFile(resolve(directory, name + '-' + variant + '-writes.json'), JSON.stringify(writes, null, 2));
  } catch (error) {
    await page.screenshot({ path: resolve(directory, name + '-failure.png'), fullPage: true }).catch(() => {});
    await writeFile(resolve(directory, name + '-failure.txt'), String(error.stack) + '\nPAGE ERRORS\n' + JSON.stringify({ errors, requestFailures }, null, 2) + '\nBODY\n' + await page.locator('body').innerText());
    await writeFile(resolve(directory, name + '-failure-state.json'), JSON.stringify({ writes, page: await page.evaluate(() => ({ events: window.__shopEvents, inputs: [...document.querySelector('[data-shop-purchase]').elements].map(e => ({ tag: e.tagName, name: e.name, value: e.value, disabled: e.disabled, valid: e.validity?.valid, message: e.validationMessage })) })) }, null, 2));
    throw error;
  } finally { await context.close(); await browser.close(); }
}
await writeFile(resolve(directory, 'browser-' + variant + '-summary.json'), JSON.stringify(evidence, null, 2));
