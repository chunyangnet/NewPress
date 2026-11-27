// Actual Go navigation pages in an isolated fixture; all writes are intercepted.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium, firefox, webkit } from 'playwright';
import { build } from 'esbuild';

const directory = resolve(process.argv[2]);
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const engines = { chromium, firefox, webkit };
const selected = process.argv[3] && process.argv[3] !== 'all' ? [process.argv[3]] : Object.keys(engines);
const evidence = [], tsAssets = new Map();
if (process.argv[4] === 'typescript') for (const [name, file] of [['admin-menu', 'chunnel-admin-menu.js'], ['navigation-badges', 'newpress-navigation-badges.js']]) {
  const compiled = await build({ entryPoints: [`frontend/core/${name}.ts`], bundle: true, format: 'iife', write: false });
  tsAssets.set(file, compiled.outputFiles[0].text);
}

for (const name of selected) {
  const browser = await engines[name].launch();
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce' });
  const page = await context.newPage(), errors = [], intercepted = [], requests = [];
  let confirmRemoval = true;
  page.on('pageerror', error => errors.push(String(error)));
  page.on('dialog', dialog => dialog.type() === 'confirm' && !confirmRemoval ? dialog.dismiss() : dialog.accept());
  page.on('request', request => requests.push(request.url()));
  await page.route('**/*', route => {
    const request = route.request(), url = new URL(request.url());
    const ts = tsAssets.get(url.pathname.split('/').pop());
    if (ts) return route.fulfill({ contentType: 'application/javascript', body: ts });
    if (['GET', 'HEAD'].includes(request.method())) return route.continue();
    intercepted.push({ url: request.url(), data: request.postData() });
    return route.fulfill({ status: 204 });
  });
  const check = text => { console.log(`${name}: ${text}`); evidence.push({ browser: name, check: text }); };
  const visit = async path => { const response = await page.goto(fixture.base + path); assert.equal(response.status(), 200, path); await page.waitForLoadState('networkidle'); };
  const raw = () => page.locator('[data-navigation-payload]').inputValue();
  const items = async () => JSON.parse(await raw());
  const card = id => page.locator(`[data-navigation-item][data-nav-id="${id}"]`);
  const field = (id, key) => card(id).locator(`[data-nav-field="${key}"]`);
  const discard = () => page.locator('[data-navigation-savebar] [data-admin-discard]').click();
  try {
    await visit(fixture.login); await visit(fixture.paths.navigation);
    const initial = await raw(), original = JSON.parse(initial), ids = original.map(item => item.id);
    assert.equal(original.length, 6);
    assert.equal(await page.locator('#navigation-icon-picker').count(), 0);
    assert.equal(await page.locator('[data-icon-picker-trigger]').count(), 6);
    assert.equal(await page.locator('[data-navigation-savebar]').evaluate(bar => bar.classList.contains('is-dirty')), false);
    const sources = page.locator('[data-navigation-source]'), sourceCount = await sources.count(); assert.ok(sourceCount > 0);
    await page.locator('[data-navigation-source-search]').fill('不存在的导航验收');
    assert.equal(await sources.filter({ visible: true }).count(), 0);
    await page.locator('[data-navigation-source-search]').fill('');
    assert.equal(await sources.count(), sourceCount);
    check('Saved tree initializes cleanly; icon grid is lazy and source filtering preserves the catalog.');

    await field(ids[0], 'label').fill('<b>中文导航</b>');
    assert.equal((await items())[0].label, '<b>中文导航</b>');
    assert.equal(await page.locator('[data-navigation-preview] b').count(), 0);
    await field(ids[0], 'label').evaluate(input => { input.focus(); input.setSelectionRange(3, 5); input.dispatchEvent(new Event('input', { bubbles: true })); });
    assert.deepEqual(await field(ids[0], 'label').evaluate(input => [input.selectionStart, input.selectionEnd, document.activeElement === input]), [3, 5, true]);
    await field(ids[0], 'icon').evaluate(select => { window.__navigationIconChanges = 0; select.addEventListener('change', () => window.__navigationIconChanges++); });
    await card(ids[0]).locator('[data-icon-picker-trigger]').click();
    await page.getByRole('searchbox', { name: '搜索图标', exact: true }).fill('compass');
    await page.getByRole('searchbox', { name: '搜索图标', exact: true }).press('ArrowDown');
    await page.keyboard.press('Enter');
    assert.equal((await items())[0].icon, 'compass');
    assert.equal(await page.evaluate(() => window.__navigationIconChanges), 1);
    assert.equal(await card(ids[0]).locator('[data-icon-picker-trigger]').evaluate(button => document.activeElement === button), true);
    await card(ids[1]).locator('[data-icon-picker-trigger]').click();
    assert.equal(await page.locator('#navigation-icon-picker').count(), 1); await page.keyboard.press('Escape');
    check('Labels remain literal text with selection intact; one shared searchable icon dialog updates the native field once and restores focus.');

    await field(ids[0], 'badge_style').selectOption('outline', { force: true });
    assert.equal((await items())[0].badge.color, (await items())[0].badge.background);
    await field(ids[0], 'badge_style').selectOption('gradient', { force: true });
    await field(ids[0], 'badge_placement').selectOption('inline', { force: true });
    await field(ids[0], 'badge_color').evaluate(input => { input.value = '#123456'; input.dispatchEvent(new Event('input', { bubbles: true })); });
    await field(ids[0], 'badge_show_on_mobile').evaluate(input => input.click());
    let badge = (await items())[0].badge;
    assert.equal(badge.hide_on_mobile, true); assert.equal(badge.style, 'gradient'); assert.equal(badge.placement, 'inline');
    assert.equal(await card(ids[0]).locator('[data-badge-preview]').getAttribute('data-style'), 'gradient');
    assert.equal(await card(ids[0]).locator('[data-badge-preview]').evaluate(node => node.style.getPropertyValue('--nav-badge-color')), '#123456');
    await field(ids[0], 'badge_style').selectOption('soft', { force: true });
    assert.equal((await items())[0].badge.color, '#123456');
    await field(ids[0], 'badge_text').fill(''); assert.equal((await items())[0].badge, undefined);
    await discard(); assert.equal(await raw(), initial);
    assert.equal(await page.locator('[data-icon-picker-trigger]').count(), 6);
    await card(ids[0]).locator('[data-icon-picker-trigger]').click();
    await page.locator('[data-icon-option="compass"]').click(); assert.equal((await items())[0].icon, 'compass');
    await discard(); assert.equal(await raw(), initial);
    check('Badge presets, custom colors and mobile visibility match the payload; discard restores exact fields and working cloned icon triggers.');

    await card(ids[0]).locator('[data-navigation-move="down"]').click();
    assert.deepEqual((await items()).map(item => item.id), [ids[3], ids[4], ids[0], ids[1], ids[2], ids[5]]);
    for (const item of await items()) assert.equal(item.parent_id, original.find(before => before.id === item.id).parent_id);
    await discard();
    await card(ids[0]).locator('[data-navigation-drag]').focus(); await page.keyboard.press('ArrowDown');
    assert.deepEqual((await items()).map(item => item.id), [ids[3], ids[4], ids[0], ids[1], ids[2], ids[5]]);
    await page.keyboard.press('ArrowUp'); assert.equal(await raw(), initial);
    await card(ids[3]).locator('[data-navigation-drag]').focus(); await page.keyboard.press('ArrowRight');
    assert.equal((await items())[3].parent_id, ids[0]); await page.keyboard.press('ArrowLeft'); assert.equal(await raw(), initial);
    await card(ids[1]).locator('[data-navigation-outdent]').click();
    assert.equal((await items())[1].parent_id, 0); assert.equal((await items())[2].parent_id, ids[1]);
    await discard(); const beforeDepthLimit = await raw();
    await card(ids[2]).locator('[data-navigation-indent]').click(); assert.equal(await raw(), beforeDepthLimit);
    await card(ids[3]).locator('[data-navigation-indent]').click();
    assert.equal((await items())[3].parent_id, ids[0]); assert.equal((await items())[4].parent_id, ids[3]);
    assert.equal(await field(ids[3], 'presentation').isDisabled(), true);
    await discard();
    confirmRemoval = false; await card(ids[0]).locator('[data-navigation-remove]').click(); assert.equal(await raw(), initial);
    confirmRemoval = true; await card(ids[0]).locator('[data-navigation-remove]').click();
    assert.deepEqual((await items()).map(item => item.id), ids.slice(3)); await discard();
    check('Move, indent, outdent and confirmed removal preserve whole subtrees and enforce the three-level limit.');

    await card(ids[0]).locator('[data-navigation-drag]').scrollIntoViewIfNeeded();
    const edgeStart = await card(ids[0]).locator('[data-navigation-drag]').boundingBox(), scrollBefore = await page.evaluate(() => scrollY);
    await page.mouse.move(edgeStart.x + 12, edgeStart.y + 12); await page.mouse.down();
    await page.mouse.move(edgeStart.x + 12, 980, { steps: 8 }); await page.waitForTimeout(220);
    assert.ok(await page.evaluate(() => scrollY) > scrollBefore + 15, 'dragging near the viewport edge scrolls to offscreen cards');
    await page.keyboard.press('Escape'); await page.mouse.up(); assert.equal(await raw(), initial);
    assert.equal(await card(ids[0]).locator('[data-navigation-drag]').evaluate(handle => document.activeElement === handle), true);
    check('Captured dragging scrolls at the viewport edge; cancellation preserves the complete tree and keyboard focus.');

    // Two compact sibling cards leave both drag handles reachable in the viewport.
    await card(ids[0]).locator('[data-navigation-remove]').click();
    await card(ids[3]).locator('[data-navigation-remove]').click();
    await page.locator('[data-navigation-add-custom]').click();
    const dragItems = await items(), dragged = card(dragItems[0].id), destination = card(dragItems[1].id);
    await dragged.locator('[data-navigation-drag]').scrollIntoViewIfNeeded();
    const beforeDrag = await raw(), targetBox = await destination.boundingBox();
    await dragged.locator('[data-navigation-drag]').dragTo(destination, { targetPosition: { x: 25, y: targetBox.height - 8 } });
    assert.notEqual(await raw(), beforeDrag, 'real mouse drag must commit the new order');
    assert.deepEqual((await items()).map(item => item.id), dragItems.map(item => item.id).reverse());
    const afterDrag = await raw();
    const cancelSource = card(dragItems[0].id), cancelTarget = card(dragItems[1].id);
    const points = async () => {
      await cancelTarget.evaluate(row => row.scrollIntoView({ block: 'start' }));
      const from = await cancelSource.locator('[data-navigation-drag]').boundingBox(), to = await cancelTarget.boundingBox();
      return { from: { x: from.x + from.width / 2, y: from.y + from.height / 2 }, to: { x: to.x + 35, y: to.y + to.height / 3 } };
    };
    for (const action of ['escape', 'outside']) {
      const { from, to } = await points();
      await page.mouse.move(from.x, from.y); await page.mouse.down(); await page.mouse.move(to.x, to.y, { steps: 8 });
      assert.equal(await raw(), afterDrag, 'draft only changes on release');
      if (action === 'escape') await page.keyboard.press('Escape'); else await page.mouse.move(5, 100, { steps: 4 });
      await page.mouse.up(); assert.equal(await raw(), afterDrag);
      assert.equal(await page.locator('.is-drag-source,.is-drop-target').count(), 0);
    }
    if (name === 'chromium') {
      const session = await context.newCDPSession(page), { from, to } = await points();
      await session.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ ...from, id: 1 }] });
      await session.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ ...to, id: 1 }] });
      await session.send('Input.dispatchTouchEvent', { type: 'touchCancel', touchPoints: [] });
      assert.equal(await raw(), afterDrag);
      await session.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ ...from, id: 2 }] });
      await session.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ ...to, id: 2 }] });
      await session.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
      assert.notEqual(await raw(), afterDrag); await session.detach();
      check('Real touch movement commits on release and pointer cancellation leaves the saved draft order intact.');
    }
    await discard(); assert.equal(await raw(), initial);
    check('Real mouse drag commits on release; Escape and outside release cancel without changing order; discard restores the saved tree.');

    await sources.first().click(); const added = (await items()).at(-1);
    assert.ok(added.id < 0); assert.equal(added.label, await sources.first().getAttribute('data-source-label'));
    await page.locator('[data-navigation-add-custom]').click();
    assert.notEqual((await items()).at(-1).id, added.id); await discard();
    await page.locator('[data-navigation-add-custom]').evaluate(button => { for (let index = 0; index < 75; index++) button.click(); });
    const maximum = await items(); assert.equal(maximum.length, 80); assert.equal(new Set(maximum.map(item => item.id)).size, 80);
    await discard(); assert.equal(await raw(), initial);
    check('Catalog/custom additions allocate distinct temporary IDs and the 80-item limit does not lose existing items.');

    await field(ids[0], 'label').fill('保存验收草稿');
    await page.locator('[data-navigation-items-form]').evaluate(form => {
      form.requestSubmit(); form.requestSubmit();
    });
    await page.waitForTimeout(250);
    assert.equal(intercepted.length, 1);
    assert.equal(JSON.parse(new URLSearchParams(intercepted[0].data).get('items_json'))[0].label, '保存验收草稿');
    await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })));
    await discard(); assert.equal(await raw(), initial);
    await field(ids[0], 'label').fill('取消提交');
    await page.locator('[data-navigation-items-form]').evaluate(form => { form.addEventListener('submit', event => event.preventDefault(), { once: true }); form.requestSubmit(); });
    await discard(); assert.equal(await raw(), initial); assert.equal(intercepted.length, 1);
    check('Native submission serializes the current draft once; canceled submission and page restoration release the busy state.');

    await visit(fixture.paths.navigation_explore);
    const explore = await items(), exploreInitial = await raw();
    assert.equal(await card(explore[0].id).locator('[data-navigation-depth-label]').textContent(), '分组（标题可点击）');
    assert.equal(await card(explore[1].id).locator('[data-navigation-depth-label]').textContent(), '分组内入口');
    assert.equal(await card(explore[5].id).locator('[data-navigation-depth-label]').textContent(), '独立入口');
    await card(explore[1].id).locator('[data-navigation-outdent]').click();
    assert.equal(await card(explore[0].id).locator('[data-navigation-depth-label]').textContent(), '独立入口');
    assert.equal(await card(explore[1].id).locator('[data-navigation-depth-label]').textContent(), '分组（标题可点击）');
    await discard(); assert.equal(await raw(), exploreInitial);
    check('Explore group labels follow the draft hierarchy and discard restores them.');

    for (const width of [1440, 375]) {
      await page.setViewportSize({ width, height: 1000 });
      const theme = width === 375 ? 'dark' : 'light';
      await page.evaluate(theme => window.ChunnelColorMode.set(theme), theme);
      await page.waitForFunction(theme => document.documentElement.dataset.theme === theme, theme);
      await card(explore[0].id).evaluate(row => { row.scrollIntoView({ block: 'start' }); window.scrollBy(0, -90); });
      await page.waitForTimeout(350);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false);
      await page.screenshot({ path: resolve(directory, `navigation-${name}-${width}.png`) });
      await card(explore[0].id).locator('[data-icon-picker-trigger]').click();
      const bounds = await page.locator('#navigation-icon-picker').boundingBox();
      assert.ok(bounds.x >= -1 && bounds.x + bounds.width <= width + 1 && bounds.y >= -1 && bounds.y + bounds.height <= 1001, JSON.stringify(bounds));
      await page.screenshot({ path: resolve(directory, `navigation-icons-${name}-${width}.png`) });
      await page.keyboard.press('Escape');
    }
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.route('**/admin/navigation/**', async route => {
      if (!['GET', 'HEAD'].includes(route.request().method())) return route.fallback();
      const response = await route.fetch(), body = (await response.text()).replace('data-can-write="true"', 'data-can-write="false"').replace(/<fieldset\b(?![^>]*\bdisabled)([^>]*)>/g, '<fieldset disabled$1>');
      await route.fulfill({ response, body });
    });
    await visit(fixture.paths.navigation); const readOnly = await raw();
    await page.evaluate(() => {
      document.querySelector('[data-navigation-add-custom]').dispatchEvent(new Event('click'));
      document.querySelector('[data-navigation-move="down"]').dispatchEvent(new Event('click'));
      document.querySelector('[data-navigation-remove]').dispatchEvent(new Event('click'));
      document.querySelector('[data-icon-picker-trigger]').dispatchEvent(new Event('click'));
      document.querySelector('[data-navigation-items-form]').dispatchEvent(new Event('submit', { cancelable: true }));
    });
    assert.equal(await raw(), readOnly); assert.equal(await page.locator('#navigation-icon-picker').count(), 0);
    assert.equal(intercepted.length, 1);
    check('Read-only DOM controls reject synthetic edits, icon opening and form submission.');
    await page.unroute('**/admin/navigation/**');
    await page.addInitScript(() => { HTMLDialogElement.prototype.showModal = undefined; });
    await visit(fixture.paths.navigation);
    assert.equal(await page.locator('[data-icon-picker-trigger]').count(), 0);
    await field(ids[0], 'icon').selectOption('compass', { force: true });
    assert.equal((await items())[0].icon, 'compass');
    await discard(); assert.equal(await raw(), initial);
    check('Without modal dialog support native icon selects remain usable and retain discard behavior.');
    assert.deepEqual(errors, []); assert.ok(requests.every(url => !/newpress-analytics\.js/.test(url)));
    check('Desktop/mobile pages fit without browser exceptions or loading the optional statistics bundle.');
  } catch (error) {
    await page.screenshot({ path: resolve(directory, `failure-${name}.png`) }).catch(() => {});
    throw error;
  } finally {
    await context.close(); await browser.close();
  }
}
await writeFile(resolve(directory, 'navigation-checks.json'), JSON.stringify(evidence, null, 2) + '\n');
