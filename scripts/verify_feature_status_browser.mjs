import assert from 'node:assert/strict';
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { resolve } from 'node:path';
import { chromium } from 'playwright';

const directory = resolve(process.argv[2] || 'output/playwright/feature-status-20261125');
const fixture = JSON.parse(await readFile(resolve(directory, 'fixture.json'), 'utf8'));
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const page = await context.newPage();
page.setDefaultTimeout(12000);
const errors = [], results = [];
page.on('pageerror', error => errors.push({ url: page.url(), message: error.message }));
page.on('dialog', dialog => dialog.accept());
const root = () => page.locator('[data-feature-status]');
const state = id => page.locator(`[data-feature-id="${id}"]`);
const check = async (name, fn) => { await fn(); results.push(name); console.log('PASS', name); };
const waitState = async (id, expected) => page.waitForFunction(({ id, expected }) => document.querySelector(`[data-feature-id="${id}"]`)?.dataset.state === expected, { id, expected });
const openStatus = async () => {
  if (!await root().isVisible() && await page.locator('[data-content-editor]').count()) {
    if (!await page.locator('[data-editor-settings-tab="publication"]').isVisible()) await page.locator('[data-editor-settings-toggle]').click();
    await page.locator('[data-editor-settings-tab="publication"]').click();
  }
  if (!await page.locator('[data-feature-overview]').evaluate(el => el.open)) await page.locator('.feature-status-toggle').click();
};
const act = async (id, name) => {
  await openStatus(); await state(id).getByRole('link', { name, exact: true }).first().click();
};
const refresh = async () => {
  await openStatus();
  const response = page.waitForResponse(r => r.url().includes('/api/v1/admin/feature-status?') && r.request().method() === 'GET');
  await page.getByRole('button', { name: '重新检测', exact: true }).click(); await response;
  await page.waitForFunction(() => document.querySelector('[data-feature-status]')?.getAttribute('aria-busy') !== 'true');
};
const mutateModules = changes => page.evaluate(async changes => {
  const read = await fetch('/api/v1/admin/modules', { cache: 'no-store' }); const data = await read.json();
  const response = await fetch('/api/v1/admin/modules', { method: 'PATCH', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': document.querySelector('[data-feature-status]').dataset.csrf }, body: JSON.stringify({ revision: data.revision, modules: changes }) });
  if (!response.ok) throw new Error('fixture setup failed: '+response.status);
}, changes);

try {
  await mkdir(directory, { recursive: true });
  await page.goto(fixture.base + fixture.login);
  if (process.argv.includes('--additional')) {
    await check('first article save adopts its real identity and refreshes saved comment rules', async () => {
      await page.goto(fixture.base+'/admin/content/new/');
      await waitState('comments','unknown');
      await page.locator('[name=title]').fill('状态联动验收草稿 '+Date.now());
      await page.locator('.ProseMirror[contenteditable=true]').fill('验证首次保存后的文章评论状态。');
      await page.locator('[name=comment_status]').selectOption('closed',{force:true});
      const saved = page.waitForResponse(r => r.request().method() === 'POST' && r.url().endsWith('/admin/content/new/'));
      await page.locator('[data-content-editor] button[name=action][value=save]').click();
      const result = await (await saved).json(); assert.equal(result.saved,true);
      await waitState('comments','paused');
      assert.match(page.url(),new RegExp(`/admin/content/${result.id}/edit/`));
      await act('comments', '调整本篇评论');
      assert.equal(await page.locator('[name=comment_status]').evaluate(el => el.closest('details')?.open),true);
      assert.equal(await page.evaluate(() => document.activeElement?.matches('[name=comment_status],.np-choice-trigger')),true);
      await page.locator('[name=title]').fill('重新检测期间的草稿'); await refresh();
      assert.equal(await page.locator('[name=title]').inputValue(),'重新检测期间的草稿');
      await page.locator('[name=comment_status]').selectOption('open',{force:true});
      await page.locator('[data-content-editor] button[name=action][value=save]').click();
      await waitState('comments','ready');
      assert.equal(await page.locator('[name=title]').inputValue(),'重新检测期间的草稿');
    });
    await check('wallet actions retain the default currency page and recompute saved rules', async () => {
      await page.goto(fixture.base+'/admin/currency/');
      const checkbox=page.locator('[data-currency-settings] [name=check_in_enabled]');
      const section=await checkbox.evaluate(el=>el.closest('[data-settings-section]').id);
      await page.locator(`[data-settings-target="${section}"]`).click();
      if (!await checkbox.isChecked()) {
        await checkbox.check({force:true}); await page.locator('[data-admin-save]').click(); await waitState('wallet-checkin','ready');
      }
      await checkbox.uncheck({force:true}); await page.locator('[data-admin-save]').click();
      await waitState('wallet-checkin','paused');
      await act('wallet-checkin', '配置签到奖励');
      assert.equal(new URL(page.url()).pathname+new URL(page.url()).search,'/admin/currency/');
      assert.equal(await checkbox.evaluate(el=>el===document.activeElement),true);
      await checkbox.check({force:true}); await page.locator('[data-admin-save]').click();
      await waitState('wallet-checkin','ready');
    });
    await page.goto(fixture.base+'/admin/wechat/');
    await openStatus();
    await check('slow older status cannot replace a newer response', async () => {
      let release, arrived; const held = new Promise(resolve=>release=resolve), started = new Promise(resolve=>arrived=resolve); let first = true;
      await page.route('**/api/v1/admin/feature-status?*', async route => {
        if (!first) return route.continue(); first=false;
        const response=await route.fetch(), json=await response.json(); arrived(); await held;
        json.items[0].summary='过期响应不应显示'; await route.fulfill({response,json}).catch(()=>{});
      });
      await page.locator('[data-feature-refresh]').click(); await started;
      const second = page.waitForResponse(r=>r.url().includes('/api/v1/admin/feature-status?'));
      await page.evaluate(()=>document.dispatchEvent(new CustomEvent('newpress:feature-status-refresh'))); await second;
      await page.waitForFunction(()=>document.querySelector('[data-feature-status]')?.getAttribute('aria-busy')==='false');
      release(); await page.unrouteAll({behavior:'wait'});
      assert.doesNotMatch(await root().innerText(),/过期响应不应显示/);
      assert.equal(await root().getAttribute('data-stale'),'false');
    });
    await check('a timed-out detection keeps the old state and makes retry available', async () => {
      let release; const held=new Promise(resolve=>release=resolve);
      await page.route('**/api/v1/admin/feature-status?*',async route=>{await held;await route.abort().catch(()=>{});});
      await page.locator('[data-feature-refresh]').click();
      await page.waitForFunction(()=>document.querySelector('[data-feature-feedback]')?.textContent.includes('检测超时'));
      assert.equal(await root().getAttribute('data-stale'),'true');
      assert.equal(await page.locator('[data-feature-refresh]').isEnabled(),true);
      release(); await page.unrouteAll({behavior:'wait'}); await refresh();
      assert.equal(await root().getAttribute('data-stale'),'false');
    });
  } else {
  await check('paused module has an authenticated recovery page and enables in place', async () => {
    assert.equal(await state('comments').getAttribute('data-state'), 'paused');
    await page.getByRole('button', { name: '启用评论模块', exact: true }).click();
    await waitState('comments', 'ready');
    assert.equal(await page.locator('.feature-status-toggle').evaluate(el => el === document.activeElement), true);
    assert.match(await root().innerText(), /影响全站/);
    await page.getByRole('link', { name: '进入业务页', exact: true }).click();
    await page.locator('.admin-comments-page').waitFor();
    await page.getByRole('link', { name: /已批准/ }).first().click();
    await page.waitForURL('**/admin/comments/?status=approved');
    await refresh();
  });
  await check('stale action is rejected and presents fresh reasons before retry', async () => {
    await mutateModules({ comments: false }); await page.reload();
    await mutateModules({ series: false });
    const response = page.waitForResponse(r => r.url().endsWith('/api/v1/admin/modules') && r.request().method() === 'PATCH');
    await page.getByRole('button', { name: '启用评论模块', exact: true }).click();
    assert.equal((await response).status(), 409);
    await page.waitForFunction(() => document.querySelector('[data-feature-feedback]')?.textContent.includes('没有覆盖'));
    assert.equal(await state('comments').getAttribute('data-state'), 'paused');
    await page.getByRole('button', { name: '启用评论模块', exact: true }).click(); await waitState('comments', 'ready');
    await mutateModules({ series: true });
  });
  await page.goto(fixture.base + '/admin/wechat/');
  await check('capabilities are independent and primary action reveals the correct tab', async () => {
    await waitState('wechat-sync', 'paused'); await waitState('wechat-collect', 'ready');
    await act('wechat-sync', '配置同步');
    assert.equal(await page.getByRole('tab', { name: '账号与设置', exact: true }).getAttribute('aria-selected'), 'true');
    assert.equal(await page.locator('[data-wechat-config] [name=enabled]').evaluate(el => el === document.activeElement), true);
  });
  await check('refresh preserves unsaved text and labels the saved-state projection', async () => {
    await page.locator('[data-wechat-config] [name=author]').fill('状态验收作者');
    await refresh();
    assert.equal(await page.locator('[data-wechat-config] [name=author]').inputValue(), '状态验收作者');
    assert.equal(await page.locator('[data-feature-draft]').isVisible(), true);
    await waitState('wechat-sync', 'paused');
  });
  await check('successful save recomputes capability readiness without navigation', async () => {
    const config = page.locator('[data-wechat-config]');
    await config.locator('[name=app_id]').fill('wx0123456789abcdef');
    await config.locator('[name=app_secret]').fill('fixture-status-secret-0123456789');
    await config.getByRole('switch', { name: '开启公众号同步', exact: true }).check({ force: true });
    await config.getByRole('button', { name: '保存更改', exact: true }).click();
    await waitState('wechat-sync', 'ready');
    assert.match(await state('wechat-sync').innerText(), /仅手动同步/);
    assert.equal(await page.locator('[data-feature-draft]').isVisible(), false);
  });
  await check('failed detection retains state, marks it stale, and keeps drafts', async () => {
    await page.locator('[data-wechat-config] [name=author]').fill('断网期间草稿');
    await page.route('**/api/v1/admin/feature-status?*', route => route.abort('failed'));
    await page.getByRole('button', { name: '重新检测', exact: true }).click();
    await page.waitForFunction(() => document.querySelector('[data-feature-status]')?.dataset.stale === 'true');
    assert.equal(await state('wechat-sync').getAttribute('data-state'), 'ready');
    assert.equal(await page.locator('[data-wechat-config] [name=author]').inputValue(), '断网期间草稿');
    await page.unroute('**/api/v1/admin/feature-status?*'); await refresh();
    assert.equal(await root().getAttribute('data-stale'), 'false');
  });
  await check('a changed account response cannot replace the original state', async () => {
    await page.route('**/api/v1/admin/feature-status?*', async route => {
      const response = await route.fetch(); const json = await response.json(); json.user_id += 100;
      await route.fulfill({ response, json });
    });
    await refresh();
    assert.equal(await root().getAttribute('data-stale'), 'true');
    assert.match(await page.locator('[data-feature-feedback]').innerText(), /账号/);
    await page.unroute('**/api/v1/admin/feature-status?*'); await refresh();
  });
  await check('all planned business pages render status and refresh from their own domain', async () => {
    for (const path of ['/admin/plugins/ai-pro-max/', '/admin/ai/', '/admin/progression/', '/admin/currency/', '/admin/recognition/', '/admin/features/', '/admin/features/?edit=status', '/admin/features/links/?edit=status', '/admin/circles/', '/admin/analytics/', '/admin/analytics/ai/', '/admin/analytics/ai/requests/', '/admin/questions/', '/admin/navigation/', '/admin/appearance/layout/', '/admin/appearance/', '/admin/series/', '/admin/import/', '/admin/flash/', `/admin/content/${fixture.post_id}/edit/`]) {
      const response = await page.goto(fixture.base+path);
      assert.equal(response.status(), 200, path);
      await openStatus(); await root().waitFor(); await refresh();
      assert.notEqual(await root().getAttribute('data-stale'), 'true', path);
      assert.ok(await root().locator('[data-feature-id]').count(), path);
      assert.equal(await root().count(), 1, path);
    }
  });
  await page.goto(fixture.base+'/admin/features/?edit=status');
  await check('a page can be opened locally and reports the saved result', async () => {
    await act('feedback', '开放此页面');
    const form = page.locator('[data-feedback-form=configure]');
    await form.getByRole('switch', { name: /开放板块/ }).check({ force: true });
    await page.locator('[data-admin-save]').click();
    await waitState('feedback','ready');
  });
  await page.goto(fixture.base+'/admin/recognition/');
  await check('saving recognition settings retains newer edits made while the request is pending', async () => {
    const form = page.locator('[data-recognition-settings]'); await form.waitFor({state:'visible'});
    await form.locator('[name=hide_amounts]').check();
    let release; const gate = new Promise(resolve => release = resolve);
    let arrived; const started = new Promise(resolve => arrived = resolve);
    await page.route('**/api/v1/admin/recognition', async route => {
      if(route.request().method() !== 'PUT') return route.continue();
      arrived(); await gate; await route.continue();
    });
    await form.getByRole('button', {name:'保存互动设置',exact:true}).click(); await started;
    await form.locator('[name=verification_help]').fill('保存期间新增的说明'); release();
    await page.waitForFunction(() => document.querySelector('[data-recognition-status]')?.textContent.includes('新修改仍保留'));
    assert.equal(await form.locator('[name=verification_help]').inputValue(),'保存期间新增的说明');
    assert.equal(await page.locator('[data-feature-draft]').isVisible(),true);
    await page.unroute('**/api/v1/admin/recognition');
    await form.getByRole('button', {name:'保存互动设置',exact:true}).click();
    await page.waitForFunction(() => document.querySelector('[data-recognition-status]')?.textContent === '互动与身份设置已保存。');
    assert.equal(await page.locator('[data-feature-draft]').isVisible(),false);
  });
  await page.goto(fixture.base+'/admin/progression/');
  await check('growth save updates its effective state and the homepage source together', async () => {
    await act('progression-checkin', '配置经验规则');
    await page.locator('#progression-experience [name=enabled]').check({force:true});
    await page.locator('[data-settings-target="progression-check-in"]').click();
    await page.locator('#progression-check-in [name=enabled]').check({force:true});
    await page.locator('[data-admin-save]').click();
    await waitState('progression-checkin','ready');
    assert.match(await state('home-checkin').innerText(),/当前采用成长签到/);
  });
  await check('mobile, dark mode, and keyboard interactions remain usable', async () => {
    await page.screenshot({path:resolve(directory,'progression-desktop.png'),fullPage:false});
    await page.setViewportSize({width:390,height:844});
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth+1),true);
    const details = state('progression-checkin').locator('summary');
    await details.focus(); await page.keyboard.press('Enter');
    assert.equal(await state('progression-checkin').locator('details').getAttribute('open'),'');
    await page.screenshot({path:resolve(directory,'progression-mobile.png'),fullPage:false});
    await page.getByRole('button',{name:'深色模式',exact:true}).click();
    await page.waitForFunction(() => document.documentElement.dataset.theme === 'dark');
    await page.waitForTimeout(500);
    await page.screenshot({path:resolve(directory,'progression-mobile-dark.png'),fullPage:false});
    await page.setViewportSize({width:1440,height:1000});
    await page.getByRole('button',{name:'浅色模式',exact:true}).click();
    await page.waitForFunction(() => document.documentElement.dataset.theme === 'light');
  });
  }
  assert.deepEqual(errors, [], 'browser JavaScript errors');
  await writeFile(resolve(directory,process.argv.includes('--additional') ? 'verification-additional.json' : 'verification.json'), JSON.stringify({passed:results, errors},null,2));
  console.log(`Verified ${results.length} browser scenarios.`);
} catch (error) {
  await page.screenshot({path:resolve(directory,'failure.png'),fullPage:false}).catch(()=>{});
  await writeFile(resolve(directory,'verification-failure.json'), JSON.stringify({passed:results, errors, error:String(error)},null,2));
  throw error;
} finally { await browser.close(); }
