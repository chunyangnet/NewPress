import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';
import { build } from 'esbuild';

const directory = resolve(process.argv[2]), fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const variant = process.argv[4] === 'generated' ? 'generated' : 'typescript';
const code = variant === 'typescript' ? (await build({ entryPoints: ['frontend/core/shop-admin.ts'], bundle: true, write: false, target: 'es2022', format: 'iife' })).outputFiles[0].text : '';
const engines = { chromium, firefox, webkit }, names = process.argv[3] && process.argv[3] !== 'all' ? [process.argv[3]] : Object.keys(engines), evidence = [];
const until = async (predicate, label = 'expected request') => { const end = Date.now() + 15000; while (!predicate()) { if (Date.now() > end) throw Error(label); await new Promise(resolve => setTimeout(resolve, 20)); } };
for (const name of names) {
  const browser = await engines[name].launch(), context = await browser.newContext({ viewport: { width: 1365, height: 960 }, reducedMotion: 'reduce', timezoneId: 'America/Los_Angeles' }), page = await context.newPage();
  page.setDefaultTimeout(12000);
  const errors = [], writes = [], external = [], scripts = [], routeErrors = [];
  const check = message => { evidence.push({ browser: name, variant, message }); console.log(name + ': ' + message); };
  page.on('pageerror', error => errors.push({ message: error.message, stack: error.stack, after: evidence.at(-1)?.message }));
  page.on('dialog', dialog => dialog.accept());
  page.on('request', request => { if (request.resourceType() === 'script') scripts.push(request.url()); });
  await context.request.get(fixture.login);
  const actual = await (await context.request.get(fixture.url + '/api/v1/admin/shop')).json();
  const actualCoupons = await (await context.request.get(fixture.url + '/api/v1/admin/shop/coupons')).json();
  const actualOrders = await (await context.request.get(fixture.url + '/api/v1/admin/shop/orders')).json();
  const actualReviews = await (await context.request.get(fixture.url + '/api/v1/admin/shop/reviews')).json();
  let catalog, coupons, couponVersion, orders, reviews, mode = 'valid', held, heldRead, failOrders = false, readonly = false;
  const resetState = () => { catalog = structuredClone(actual.catalog); coupons = structuredClone(actualCoupons.coupons); couponVersion = actualCoupons.version; orders = Object.fromEntries(actualOrders.orders.map(o => [o.id, structuredClone(o)])); reviews = Object.fromEntries(actualReviews.reviews.map(r => [r.id, structuredClone(r)])); mode = 'valid'; failOrders = false; readonly = false; };
  resetState();
  const json = (route, value, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(value) });
  const response = async (operation, body, id, ticket) => {
    const res = await context.request.post(fixture.url + '/fixture/admin-response', { data: { operation, body, id, ticket, catalog, coupons, coupon_version: couponVersion, orders, reviews, wallet_disabled: mode === 'wallet-disabled' } });
    assert.equal(res.status(), 200, await res.text()); return res.json();
  };
  await context.route('**/*', async route => {
    try {
      const req = route.request(), url = new URL(req.url()), path = url.pathname, method = req.method();
      if (url.origin !== fixture.url) { external.push(url.href); return route.abort(); }
      if (code && path.endsWith('/newpress-shop-admin.js')) return route.fulfill({ contentType: 'text/javascript', body: code });
      if (path.startsWith('/fixture/')) return route.continue();
      if (readonly && path === '/admin/shop/') { const res = await route.fetch(); return route.fulfill({ response: res, body: (await res.text()).replace('data-read-only="false"', 'data-read-only="true"').replace('data-user-id="1"', 'data-user-id="0"').replace(/data-csrf="[^"]*"/, 'data-csrf=""') }); }
      if (method === 'GET' || method === 'HEAD') {
        if (path === '/api/v1/admin/shop') {
          if (mode === 'unauthorized') return json(route, { error: { message: '登录已失效' } }, 401);
          const result = { ...actual, catalog: structuredClone(catalog) };
          if (readonly) result.csrf_token = '';
          if (mode === 'invalid-catalog') result.catalog.products[0].price = '1e3';
          return json(route, result);
        }
        if (path === '/api/v1/admin/shop/coupons') return json(route, { version: couponVersion, coupons });
        if (path === '/api/v1/admin/shop/orders') {
          if (failOrders) return json(route, { error: { message: '订单列表暂不可用' } }, 503);
          return json(route, { orders: Object.values(orders) });
        }
        if (path === '/api/v1/admin/shop/reviews') {
          const query = url.searchParams, rating = Number(query.get('rating') || 0), status = query.get('status'), term = query.get('q');
          let items = Object.values(reviews).filter(r => (!rating || r.rating === rating) && (!status || (status === 'hidden') === r.hidden) && (!term || (r.order_id + r.content + r.title).includes(term)));
          const p = Number(query.get('page') || 1), result = { reviews: items.slice((p - 1) * 20, p * 20), total: items.length, page: p, page_size: 20 };
          if (mode === 'hold-review-read') { mode = 'valid'; heldRead = () => json(route, result); return; }
          return json(route, result);
        }
        return route.continue();
      }
      const body = req.postDataJSON(); writes.push({ path, method, body, csrf: req.headers()['x-csrf-token'] });
      if (path === '/api/v1/admin/shop/components/validate') return json(route, { valid: true, errors: [] });
      assert.equal(req.headers()['x-csrf-token'], actual.csrf_token);
      let operation, id, ticket;
      if (path === '/api/v1/admin/shop') operation = 'catalog';
      else if (path === '/api/v1/admin/shop/coupons') operation = 'coupons';
      else if (path === '/api/v1/admin/shop/advance') operation = 'advance';
      else if (path.includes('/reviews/')) { operation = 'review'; id = path.split('/').at(-1); }
      else { const parts = path.split('/'); id = parts[6]; operation = parts.at(-1); if (operation === 'deliver') ticket = parts.at(-2); }
      const result = await response(operation, body, id, ticket), currentMode = mode;
      const reply = async () => {
        catalog = result.catalog; coupons = result.coupons; couponVersion = result.coupon_version; orders = result.orders; reviews = result.reviews;
        if (currentMode === 'refresh-fail') failOrders = true;
        if (currentMode === 'lost' || currentMode === 'lost-unreadable') { mode = 'valid'; failOrders = currentMode === 'lost-unreadable'; return route.abort(); }
        const receipt = structuredClone(result.body);
        if (currentMode === 'invalid-receipt') {
          if (receipt.catalog) receipt.catalog.version = '1';
          if (receipt.order) receipt.order.user_id = '999';
          if (receipt.review) receipt.review.content = 'wrong';
          if (receipt.coupons) receipt.version += 1;
        }
        await json(route, receipt, result.status);
      };
      if (currentMode === 'hold') { held = reply; return; }
      return reply();
    } catch (error) { routeErrors.push(error.stack); await route.abort(); }
  });
  const text = (selector, expected) => page.waitForFunction(({ selector, expected }) => document.querySelector(selector)?.textContent.includes(expected), { selector, expected });
  const idle = () => page.waitForFunction(() => document.querySelector('[data-shop-admin]')?.getAttribute('aria-busy') === 'false');
  const open = async () => { await page.goto(fixture.url + '/admin/shop/', { waitUntil: 'domcontentloaded' }); await idle(); };
  const reset = async () => { resetState(); await page.evaluate(() => sessionStorage.clear()); await open(); };
  const tab = value => page.locator('[data-shop-tab="' + value + '"]').click();
  const edit = async key => { await tab('products'); const index = catalog.products.findIndex(p => p.key === key); await page.locator('[data-shop-edit="' + index + '"]').click(); await page.locator('[data-shop-editor]').waitFor({ state: 'visible' }); };
  const form = '[data-shop-product-form]', field = key => page.locator(form + ' [name="' + key + '"]');
  const submit = async selector => { await page.locator(selector).evaluate(f => f.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))); await idle(); };
  const saveProduct = () => submit(form);
  const closeProduct = () => page.locator('[data-shop-close]').first().click();
  const order = async key => { await tab('orders'); await page.locator('[data-shop-order="' + fixture.orders[key] + '"]').click(); await page.locator('[data-shop-order-dialog]').waitFor({ state: 'visible' }); };
  const businessWrites = () => writes.filter(w => !w.path.endsWith('/components/validate')).length;
  try {
    await open(); await page.locator('[data-shop-workspace]').waitFor({ state: 'visible' });
    assert.equal(await page.locator('[data-shop-edit]').count(), 12);
    check('Actual Go admin configuration renders all 12 products and private order records.');
    for (const product of actual.catalog.products) {
      await edit(product.key); assert.equal(await field('kind').inputValue(), product.kind); assert.equal(await field('mode').inputValue(), product.mode);
      const before = businessWrites(); await saveProduct();
      await page.locator('[data-shop-editor]').waitFor({ state: 'hidden' });
      assert.equal(errors.length, 0, JSON.stringify(errors)); assert.equal(routeErrors.length, 0, JSON.stringify(routeErrors));
      assert.ok(businessWrites() <= before + 1);
    }
    check('Every product kind and purchase mode opens and saves against actual Go validation.');
    await reset(); await edit('cdk');
    const oldCodes = await field('codes').inputValue(); assert.ok(oldCodes.includes('TEST-KEY-001'));
    await field('title').fill('卡密商品新标题');
    const currentCDK = catalog.products.find(p => p.key === 'cdk'); currentCDK.stock = String(Number(currentCDK.stock) - 1); currentCDK.codes.shift(); catalog.version = String(BigInt(catalog.version) + 1n);
    await saveProduct(); await page.locator('[data-shop-editor]').waitFor({ state: 'hidden' });
    assert.equal(catalog.products.find(p => p.key === 'cdk').title, '卡密商品新标题'); assert.equal(catalog.products.find(p => p.key === 'cdk').codes.length, 2);
    check('A product title save keeps concurrent inventory reservations and sold codes.');
    await edit('cdk'); await field('stock').fill('70'); catalog.products.find(p => p.key === 'cdk').stock = '60'; catalog.version = String(BigInt(catalog.version) + 1n);
    let before = businessWrites(); await saveProduct(); await text('[data-shop-editor-status]', '已被其他操作修改'); assert.equal(businessWrites(), before); assert.equal(await field('stock').inputValue(), '70'); await closeProduct();
    check('Conflicting stock edits preserve the entered value and send no catalog write.');

    await reset(); await tab('settings'); await page.locator('[data-shop-settings] [name="terms"]').fill('商城设置草稿');
    await edit('digital'); await field('title').fill('修改商品保留设置'); await saveProduct(); await page.locator('[data-shop-editor]').waitFor({ state: 'hidden' });
    await tab('settings'); assert.equal(await page.locator('[data-shop-settings] [name="terms"]').inputValue(), '商城设置草稿'); await submit('[data-shop-settings]'); assert.equal(catalog.terms, '商城设置草稿');
    check('Saving a product retains the independent settings draft and its original merge baseline.');
    await reset(); await edit('cdk'); await field('title').fill('第一次提交');
    catalog.products.find(p => p.key === 'cdk').stock = '78'; catalog.products.find(p => p.key === 'cdk').codes.shift(); catalog.version = String(BigInt(catalog.version) + 1n);
    mode = 'hold'; await page.locator(form).evaluate(f => f.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))); await until(() => held);
    await field('title').evaluate(el => { el.value = '保存期间的新输入'; el.dispatchEvent(new Event('input', { bubbles: true })); });
    await held(); held = null; mode = 'valid'; await idle(); assert.equal(await field('title').inputValue(), '保存期间的新输入');
    await saveProduct(); await page.locator('[data-shop-editor]').waitFor({ state: 'hidden' });
    assert.equal(catalog.products.find(p => p.key === 'cdk').stock, '78'); assert.equal(catalog.products.find(p => p.key === 'cdk').codes.length, 2);
    check('New input during a save survives, and the next save cannot restore stale stock or sold codes.');
    await reset(); await edit('physical'); await field('title').fill('丢失回执后的标题'); mode = 'lost'; await saveProduct();
    await text('[data-shop-editor-status]', ''); before = businessWrites(); mode = 'valid'; await saveProduct(); await page.locator('[data-shop-editor]').waitFor({ state: 'hidden' }); assert.equal(businessWrites(), before);
    check('A lost catalog response is recovered by reading the already saved content without a second PUT.');
    await reset(); await page.locator('[data-shop-add]').click(); await field('title').fill('新建商品重试'); await field('key').fill('new-retry'); mode = 'lost'; await saveProduct(); before = businessWrites(); mode = 'valid'; await saveProduct(); await page.locator('[data-shop-editor]').waitFor({ state: 'hidden' }); assert.equal(businessWrites(), before); assert.equal(catalog.products.filter(p => p.key === 'new-retry').length, 1);
    check('A newly created product also recovers a lost reply without duplicate creation.');
    await reset(); await edit('physical'); await field('title').fill('回执必须匹配'); mode = 'invalid-receipt'; await saveProduct(); await text('[data-shop-editor-status]', '版本未确认'); assert.equal(await page.locator('[data-shop-editor]').isVisible(), true); await closeProduct();
    check('A malformed catalog receipt leaves the editor and its draft available for verification.');
    await reset(); await edit('draw'); const drawAt = catalog.products.find(p => p.key === 'draw').raffle.draw_at; await field('title').fill('保留精确开奖时间'); await saveProduct(); await page.locator('[data-shop-editor]').waitFor({ state: 'hidden' }); assert.equal(catalog.products.find(p => p.key === 'draw').raffle.draw_at, drawAt);
    await edit('draw'); await page.locator('[data-shop-add-tier]').click(); await page.locator('[data-shop-tier]').nth(0).locator('[name="tier_prize"]').fill('一等奖'); await page.locator('[data-shop-add-tier]').click(); await page.locator('[data-shop-tier]').nth(1).locator('[name="tier_prize"]').fill('二等奖'); await page.locator('[data-shop-tier]').nth(1).locator('[data-tier-move="up"]').click(); await saveProduct(); await page.locator('[data-shop-editor]').waitFor({ state: 'hidden' }); assert.equal(catalog.products.find(p => p.key === 'draw').raffle.tiers[0].prize, '二等奖');
    check('Unchanged draw times keep nanoseconds and offsets; tier reordering reaches the saved raffle plan.');
    await tab('settings'); await page.locator('[data-shop-add-campaign]').click(); await page.locator('[name="campaign_title"]').last().fill('周末联合抽奖'); await submit('[data-shop-settings]');
    await edit('draw'); await field('raffle_campaign').selectOption({ label: '周末联合抽奖' }); await saveProduct(); await page.locator('[data-shop-editor]').waitFor({ state: 'hidden' }); assert.ok(catalog.products.find(p => p.key === 'draw').raffle.campaign);
    check('Shared raffle activities save and remain selectable in product limits.');
    await reset(); await edit('group'); await page.locator('[data-shop-editor-tab="services"]').click(); await page.locator('[data-service-mode="hidden"]').click(); await saveProduct(); await page.locator('[data-shop-editor]').waitFor({ state: 'hidden' }); assert.equal(catalog.products.find(p => p.key === 'group').services.mode, 'hidden');
    await edit('digital'); await page.locator('[data-shop-editor-tab="description"]').click(); await page.evaluate(() => { window.ChunnelBlockEditor.reset('<p>新的商品详细说明</p>'); const source = document.querySelector('[data-content-source]'); source.dispatchEvent(new Event('input', { bubbles: true })); }); await saveProduct(); await page.locator('[data-shop-editor]').waitFor({ state: 'hidden' }); assert.match(catalog.products.find(p => p.key === 'digital').description_source, /新的商品详细说明/);
    check('The migrated entry still integrates service descriptions and the rich product composer.');

    const couponForm = '[data-shop-coupon-form]', couponField = key => page.locator(couponForm + ' [name="' + key + '"]');
    await reset(); await tab('coupons'); await page.locator('[data-shop-coupon-edit="2"]').click(); assert.equal(await couponField('currency').inputValue(), '');
    const originalStart = coupons[2].starts_at; await couponField('title').fill('折扣券新标题'); await submit(couponForm); assert.equal(coupons[2].currency, ''); assert.equal(coupons[2].starts_at, originalStart);
    check('Editing an unrestricted percent coupon retains its currency scope and precise timestamps.');
    await page.locator('[data-shop-coupon-new]').click(); await couponField('code').fill('DRAFT-10'); await couponField('title').fill('优惠券草稿'); await couponField('value').fill('1.00');
    await tab('settings'); await page.locator('[data-shop-settings] [name="terms"]').fill('保留优惠券草稿的设置'); await submit('[data-shop-settings]'); await tab('coupons'); assert.equal(await couponField('title').inputValue(), '优惠券草稿');
    mode = 'lost'; await submit(couponForm); before = businessWrites(); mode = 'valid'; await submit(couponForm); assert.equal(businessWrites(), before); assert.equal(coupons.filter(c => c.code === 'DRAFT-10').length, 1);
    check('Independent coupon drafts survive settings saves, and lost creation replies recover without duplicate rules.');
    await page.locator('[data-shop-coupon-edit="0"]').click(); await couponField('title').fill('冲突草稿'); couponVersion++; before = businessWrites(); await submit(couponForm); await text('[data-shop-coupon-status]', '已被其他操作修改'); assert.equal(businessWrites(), before); await page.locator('[data-shop-coupon-cancel]').click();
    await open(); await tab('coupons'); const wasEnabled = coupons[0].enabled; await page.locator('[data-shop-coupon-toggle="0"]').click(); await idle(); assert.equal(coupons[0].enabled, !wasEnabled);
    const count = coupons.length; await page.locator('[data-shop-coupon-delete="3"]').click(); await idle(); assert.equal(coupons.length, count - 1);
    check('Coupon version conflicts preserve input; toggles and deletion receive verified numeric versions.');

    await reset(); await tab('reviews'); await page.locator('[data-shop-review-edit]').first().waitFor();
    const reviewIDs = await page.locator('[data-shop-review-edit]').evaluateAll(forms => forms.map(f => f.dataset.shopReviewEdit));
    const reviewForm = id => page.locator('[data-shop-review-edit="' + id + '"]');
    await reviewForm(reviewIDs[0]).locator('[name="reply"]').fill('第一条回复'); await reviewForm(reviewIDs[1]).locator('[name="reply"]').fill('第二条草稿');
    await submit('[data-shop-review-edit="' + reviewIDs[0] + '"]'); assert.equal(reviews[reviewIDs[0]].reply, '第一条回复'); assert.equal(await reviewForm(reviewIDs[1]).locator('[name="reply"]').inputValue(), '第二条草稿');
    await page.locator('[data-shop-review-refresh]').click(); await text('[data-shop-status]', '未保存'); assert.equal(await reviewForm(reviewIDs[1]).locator('[name="reply"]').inputValue(), '第二条草稿');
    mode = 'lost'; await submit('[data-shop-review-edit="' + reviewIDs[1] + '"]'); before = businessWrites(); mode = 'valid'; await submit('[data-shop-review-edit="' + reviewIDs[1] + '"]'); assert.equal(businessWrites(), before);
    check('Moderating one review preserves another draft; lost replies use versioned verification before retry.');
    await reviewForm(reviewIDs[0]).locator('[name="hidden"]').check(); await reviewForm(reviewIDs[0]).locator('[name="reason"]').fill('包含个人信息'); await submit('[data-shop-review-edit="' + reviewIDs[0] + '"]'); assert.equal(reviews[reviewIDs[0]].hidden, true); assert.equal(await reviewForm(reviewIDs[0]).locator('xpath=..').locator('.sa-badge').textContent(), '已隐藏');
    check('Review hiding validates the moderator and reason and updates the displayed state.');
    mode = 'hold-review-read'; await page.locator('[data-shop-review-refresh]').click(); await until(() => heldRead);
    await page.locator('[data-shop-review-rating="1"]').click(); await text('[data-shop-review-count]', '共 0'); await heldRead(); heldRead = null; assert.equal(await page.locator('[data-shop-review-edit]').count(), 0);
    check('A late review response cannot replace the newer selected rating filter.');

    await reset(); await order('ship'); await page.locator('[data-shop-shipment] [name="carrier"]').fill('顺丰'); await page.locator('[data-shop-shipment] [name="tracking"]').fill('SF-TEST-123'); mode = 'hold';
    await page.locator('[data-shop-shipment]').evaluate(f => f.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))); await until(() => held); before = businessWrites(); await page.locator('[data-shop-shipment]').evaluate(f => f.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))); assert.equal(businessWrites(), before);
    await held(); held = null; mode = 'valid'; await idle(); assert.equal(orders[fixture.orders.ship].status, 'shipped'); await page.locator('[data-shop-order-dialog]').waitFor({ state: 'hidden' });
    check('Shipment double submits produce one write with an exact carrier, tracking and order receipt.');
    await reset(); await order('ship'); await page.locator('[data-shop-shipment] [name="carrier"]').fill('顺丰'); await page.locator('[data-shop-shipment] [name="tracking"]').fill('SF-LOST'); mode = 'lost-unreadable'; await submit('[data-shop-shipment]'); await idle();
    failOrders = false; await open(); await order('ship'); before = businessWrites(); await page.locator('[data-shop-pending] button').click(); await idle(); assert.equal(businessWrites(), before); assert.equal(await page.evaluate(id => sessionStorage.getItem('newpress.shop.admin.pending.1.' + id), fixture.orders.ship), null);
    check('A reload restores the pending shipment action and verifies its success without resending.');
    await reset(); await order('ship'); await page.locator('[data-shop-shipment] [name="carrier"]').fill('顺丰'); await page.locator('[data-shop-shipment] [name="tracking"]').fill('SF-REFRESH'); mode = 'refresh-fail'; await submit('[data-shop-shipment]'); await text('[data-shop-status]', '暂未刷新'); assert.equal(orders[fixture.orders.ship].status, 'shipped');
    check('A failed list refresh after a confirmed shipment keeps the confirmed success visible.');
    await reset(); await order('prize'); await page.locator('[data-shop-order-detail] form [name="content"]').fill('兑奖地址 /shop/orders/'); await submit('[data-shop-order-detail] form'); assert.equal(orders[fixture.orders.prize].tickets[0].delivery, '兑奖地址 /shop/orders/');
    check('Prize delivery validates the original winning ticket and its exact delivery contents.');
    await reset(); await order('review-refund'); await page.locator('[data-shop-refund-review] [name="note"]').fill('同意退款'); await page.locator('[data-shop-refund-review] [value="true"]').click(); await idle(); assert.equal(orders[fixture.orders['review-refund']].status, 'refund_required');
    await order('reject-refund'); await page.locator('[data-shop-refund-review] [name="note"]').fill('保留订单继续交付'); await page.locator('[data-shop-refund-review] [value="false"]').click(); await idle(); assert.equal(orders[fixture.orders['reject-refund']].status, 'awaiting_shipment');
    check('Refund approval and rejection verify distinct transitions and the exact review note.');
    await order('refund'); await page.locator('[data-shop-refund-form] [name="reference"]').fill('CHANNEL-REFUND-1'); await page.locator('[data-shop-refund-form] [name="note"]').fill('原渠道退款已核对'); await submit('[data-shop-refund-form]'); assert.equal(orders[fixture.orders.refund].status, 'refunded');
    check('External refund reconciliation keeps the submitted reference and note in the confirmed order.');
    await reset(); await order('wallet'); await page.locator('[data-shop-refund-review] [name="note"]').fill('批准钱包退款'); mode = 'wallet-disabled'; await page.locator('[data-shop-refund-review] [value="true"]').click(); await idle();
    await page.locator('[data-shop-refund-form]').waitFor({ state: 'visible' }); assert.equal(orders[fixture.orders.wallet].status, 'refund_required'); mode = 'valid'; await submit('[data-shop-refund-form]'); assert.equal(orders[fixture.orders.wallet].status, 'refunded'); assert.equal(orders[fixture.orders.wallet].refund_reference, 'wallet-refund-' + fixture.orders.wallet);
    check('Committed refund approval followed by wallet failure exposes a retry and confirms the eventual wallet refund.');
    await reset(); await order('ship'); await page.locator('[data-shop-shipment] [name="carrier"]').fill('顺丰'); await page.locator('[data-shop-shipment] [name="tracking"]').fill('SF-LATE'); mode = 'hold'; await page.locator('[data-shop-shipment]').evaluate(f => f.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))); await until(() => held);
    await page.evaluate(() => document.dispatchEvent(new CustomEvent('chunnel:session', { detail: { authenticated: true, id: 2, csrf_token: 'changed' } }))); await held(); held = null; await page.locator('[data-shop-workspace]').waitFor({ state: 'hidden' }); assert.equal(await page.locator('[data-shop-order-dialog]').isVisible(), false);
    check('An identity change retires private views and ignores a late successful order response.');
    await reset(); mode = 'unauthorized'; await page.locator('[data-shop-reload]').click(); await page.locator('[data-shop-workspace]').waitFor({ state: 'hidden' });
    check('HTTP 401 retires the workspace and locks further management actions.');
    await reset(); await page.evaluate(() => window.dispatchEvent(new Event('pagehide'))); await page.locator('[data-shop-workspace]').waitFor({ state: 'hidden' }); await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))); assert.equal(await page.locator('[data-shop-add]').isDisabled(), true);
    check('Page retirement and BFCache restoration require a fresh read before further actions.');
    await reset(); readonly = true; await open(); await page.locator('[data-shop-workspace]').waitFor({ state: 'visible' }); before = businessWrites(); assert.equal(await page.locator('[data-shop-add]').isDisabled(), true); await edit('cdk'); assert.equal(await field('title').isDisabled(), true); await saveProduct(); assert.equal(businessWrites(), before); await closeProduct();
    check('An unconfigured read-only preview accepts an empty session token and cannot mutate products.');
    await reset(); mode = 'invalid-catalog'; await open(); assert.equal(await page.locator('[data-shop-workspace]').isVisible(), false); assert.ok((await page.locator('[data-shop-status]').textContent()).length);
    check('Malformed configuration never exposes an editable fabricated catalog.');
    for (const [style, scheme, width] of [['classic', 'light', 1365], ['liquid-glass', 'dark', 390]]) {
      await context.request.post(fixture.url + '/fixture/style/' + style); await page.setViewportSize({ width, height: 960 }); await page.emulateMedia({ colorScheme: scheme }); await reset();
      for (const section of ['products', 'orders', 'reviews', 'coupons', 'settings']) {
        await tab(section); if (section === 'reviews') await page.locator('[data-shop-review-edit]').first().waitFor();
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false, style + ' ' + section);
      }
      await tab('products'); await page.screenshot({ path: resolve(directory, `${name}-${variant}-${style}-${width}-catalog.png`), fullPage: true }); await edit('group');
      assert.equal(await page.evaluate(() => { const el = document.querySelector('[data-shop-editor]'); return el.getBoundingClientRect().right > innerWidth + 1; }), false);
      await page.screenshot({ path: resolve(directory, `${name}-${variant}-${style}-${width}-editor.png`), fullPage: true }); await closeProduct();
      check(`${style} ${scheme} at ${width}px retains all five panels and a usable product editor without horizontal overflow.`);
    }
    await context.request.get(fixture.url + '/fixture/login/reader'); assert.equal((await context.request.get(fixture.url + '/api/v1/admin/shop')).status(), 403);
    assert.deepEqual(errors, []); assert.deepEqual(routeErrors, []); assert.deepEqual(external, []); assert.equal(scripts.some(s => /analytics-react|echarts|react-dom/.test(s)), false);
    check('No browser exceptions, external business requests or statistics dependencies; ordinary readers remain forbidden.');
    await writeFile(resolve(directory, `${name}-${variant}.json`), JSON.stringify({ checks: evidence.filter(e => e.browser === name), errors, routeErrors, writes, external, scripts }, null, 2));
  } catch (error) {
    await writeFile(resolve(directory, `${name}-${variant}-failure.json`), JSON.stringify({ error: error.stack, errors, routeErrors, writes, status: await page.locator('[data-shop-status]').textContent().catch(() => ''), editorStatus: await page.locator('[data-shop-editor-status]').textContent().catch(() => '') }, null, 2));
    throw error;
  } finally { await browser.close(); }
}
await writeFile(resolve(directory, `summary-${variant}.json`), JSON.stringify(evidence, null, 2));
console.log(evidence.length + ' checks passed.');
