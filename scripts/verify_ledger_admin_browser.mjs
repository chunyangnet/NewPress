// Go-rendered admin pages and real read APIs; all ledger writes are intercepted.
import assert from 'node:assert/strict';
import {readFile,writeFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import {chromium,firefox,webkit} from 'playwright';
import {compileFrontendEntry} from './frontend_entries.mjs';
import fixtures from '../tests/frontend/helpers/ledger-admin-fixtures.cjs';
const directory=resolve(process.argv[2]),fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
const variant=process.argv[4]==='typescript'?'typescript':'generated',engines={chromium,firefox,webkit},names=process.argv[3]&&process.argv[3]!=='all'?[process.argv[3]]:Object.keys(engines);
const assets=new Map(),evidence=[];
if(variant==='typescript')for(const name of ['currency-admin','progression-admin'])assets.set(`chunnel-${name}.js`,(await compileFrontendEntry(name,{minify:false})).code);
const until=async predicate=>{const end=Date.now()+12000;while(!predicate()){if(Date.now()>end)throw Error('Missing intercepted request');await new Promise(resolve=>setTimeout(resolve,20));}};
for(const name of names){
  const browser=await engines[name].launch(),context=await browser.newContext({viewport:{width:1365,height:960},reducedMotion:'reduce'}),page=await context.newPage();
  page.setDefaultTimeout(12000);const errors=[],writes=[],scripts=[];let mode='valid',held,refreshFailure=false,currencySettings,progressionSettings;
  const json=(route,data,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(data)});
  page.on('pageerror',error=>errors.push({message:error.message,url:page.url()}));page.on('dialog',dialog=>dialog.accept());page.on('request',request=>{if(request.resourceType()==='script')scripts.push(request.url());});
  await context.route('**/*',async route=>{
    const request=route.request(),url=new URL(request.url()),path=url.pathname,filename=path.split('/').at(-1);
    if(url.origin!==fixture.url)return route.abort();
    if(assets.has(filename))return route.fulfill({contentType:'text/javascript',body:assets.get(filename)});
    if(refreshFailure&&request.method()==='GET'&&(path.includes('/wallet')||path.endsWith('/account')))return json(route,{error:{message:'验收：账号读取暂不可用'}},503);
    if(request.method()==='GET'||request.method()==='HEAD'||path.startsWith('/fixture/')||path.endsWith('/progression/preview'))return route.continue();
    const body=request.headers()['content-type']?.includes('application/json')?request.postDataJSON():null;writes.push({path,body});
    if(path==='/api/v1/admin/currencies/main'||path.startsWith('/api/v1/admin/currencies/main/modules/')){
      if(mode==='conflict')return json(route,{error:{code:'currency.settings_conflict',message:'规则已更新'}},409);
      const settings={...currencySettings,...(body.values||body.settings),version:String(BigInt(body.version||body.settings.version)+1n)},result={currency_key:'main',module:path.split('/').at(-1),settings};
      if(mode==='hold'){held=()=>json(route,result);return;}currencySettings=settings;return json(route,result);
    }
    if(path==='/api/v1/admin/currencies/main/adjust'){
      const receipt={...fixtures.currencyReceipt(body),id:'9001'};if(mode==='invalid')receipt.reason='other';
      if(mode==='hold'){held=()=>json(route,receipt);return;}if(mode==='refresh-fail')refreshFailure=true;return json(route,receipt,201);
    }
    if(path.startsWith('/api/v1/admin/progression/modules/')||path==='/api/v1/admin/progression'){
      const key=path.split('/').at(-1).replace('check-in','check_in'),settings={...progressionSettings,...(key==='progression'?body.settings:{[key]:body.settings}),version:String(BigInt(body.version||body.settings.version)+1n)},result={settings,currencies:fixtures.currencies};
      if(mode==='conflict')return json(route,{error:{code:'progression.settings_changed',message:'规则已更新'}},409);
      if(mode==='hold'){held=()=>json(route,result);return;}progressionSettings=settings;return json(route,result);
    }
    if(path.endsWith('/experience')||path.endsWith('/membership')){
      const receipt=fixtures.progressionReceipt({kind:path.split('/').at(-1),payload:body});receipt.entry.actor_id=String(fixture.owner_id);
      if(mode==='invalid')receipt.entry.reason='other';if(mode==='hold'){held=()=>json(route,receipt);return;}if(mode==='refresh-fail')refreshFailure=true;return json(route,receipt,201);
    }
    return json(route,{error:{message:'验收未提交此操作'}},409);
  });
  const text=(selector,value)=>page.waitForFunction(({selector,value})=>document.querySelector(selector)?.textContent.includes(value),{selector,value});
  const save=(selector,button)=>page.locator(selector).evaluate((form,button)=>form.requestSubmit(button?form.querySelector(button):undefined),button);
  const change=(selector,value)=>page.locator(selector).evaluate((field,value)=>{field.value=value;field.dispatchEvent(new Event('input',{bubbles:true}));},value);
  const open=async path=>{mode='valid';refreshFailure=false;await page.goto(fixture.url+path);await page.waitForLoadState('networkidle');};
  const check=label=>{evidence.push({browser:name,variant,check:label});console.log(`${name} ${variant}: ${label}`);};
  const tab=id=>page.locator(`[data-settings-target="${id}"]`).click();
  const lookup=async kind=>{await tab(kind==='currency'?'currency-ledger':'progression-accounts');await page.locator(`[data-${kind}-lookup] [name=username]`).fill(fixture.username);await save(`[data-${kind}-lookup]`);await page.locator(`[data-${kind}-account]`).waitFor({state:'visible'});};
  try{
    await page.goto(fixture.login);currencySettings=(await (await context.request.get(fixture.url+'/api/v1/admin/currencies/main')).json()).settings;progressionSettings=(await (await context.request.get(fixture.url+'/api/v1/admin/progression')).json()).settings;
    await open('/admin/currency/');await page.locator('[data-currency-settings]').waitFor();assert.equal(await page.locator('[data-currency-module]').count(),6);check('Currency Go form initializes six modules and the existing savebar.');
    await tab('currency-check-in');await page.locator('[data-currency-settings] [name=check_in_reward]').fill('0.75');await tab('currency-registration');await page.locator('[data-currency-settings] [name=registration_reward]').fill('9.00');await tab('currency-check-in');mode='hold';await save('[data-currency-settings]','button[value="check-in"]');await until(()=>held);
    await change('[data-currency-settings] [name=check_in_reward]','0.95');await held();held=null;await page.waitForFunction(()=>document.querySelector('[data-currency-settings] [name=version]').value==='3');assert.equal(await page.locator('[name=registration_reward]').inputValue(),'9.00');assert.equal(await page.locator('[name=check_in_reward]').inputValue(),'0.95');check('Module save keeps sibling drafts and edits made after sending.');
    await tab('currency-registration');mode='conflict';await save('[data-currency-settings]','button[value="registration"]');await text('[data-save-state]','规则已更新');assert.equal(await page.locator('[name=registration_reward]').inputValue(),'9.00');check('409 retains the original settings and input.');
    await open('/admin/currency/');await lookup('currency');await text('[data-currency-account-balance]','35.00');assert.equal(await page.locator('[data-currency-admin-history] tbody tr').count(),30);await save('[data-currency-more-form]');await page.waitForFunction(()=>document.querySelectorAll('[data-currency-admin-history] tbody tr').length===5);check('Actual wallet API, exact amounts and cursor pagination render correctly.');
    await page.locator('[data-currency-adjust] [name=delta]').fill('1.25');await page.locator('[data-currency-adjust] [name=reason]').fill('验收调整');mode='invalid';await save('[data-currency-adjust]');await text('[data-currency-adjust-status]','原调整已保留');const original=writes.at(-1).body;mode='refresh-fail';await save('[data-currency-adjust]');await text('[data-currency-adjust-status]','已确认用户');await text('[data-currency-account-status]','读取暂不可用');assert.deepEqual(writes.at(-1).body,original);assert.equal(await page.evaluate(()=>window.ChunnelCurrency.readPending('adjust','1','main')),null);check('Uncertain currency adjustment retries the same request; confirmed success survives refresh failure.');
    await open('/admin/currency/');await lookup('currency');await page.locator('[data-currency-adjust] [name=delta]').fill('0.25');await page.locator('[data-currency-adjust] [name=reason]').fill('迟到回执');mode='hold';await save('[data-currency-adjust]');await until(()=>held);await page.evaluate(()=>window.dispatchEvent(new Event('pagehide')));await held().catch(()=>{});held=null;assert.equal(await page.locator('[data-currency-account]').isHidden(),true);assert.ok(await page.evaluate(()=>window.ChunnelCurrency.readPending('adjust','1','main')));await page.evaluate(()=>window.ChunnelCurrency.clearPending('adjust','1','main'));check('Leaving the page retires pending requests and clears private wallet rows.');

    await open('/admin/progression/');await page.locator('#progression-membership').waitFor({state:'attached'});assert.equal(await page.locator('[data-progression-modules] form').count(),6);assert.equal(await page.locator('[data-admin-save]').isDisabled(),true);check('Growth Go page loads six modules without a false dirty state.');
    await tab('progression-preview');await page.locator('[data-progression-check]').click();await text('[data-progression-preview-status]','检查通过');assert.equal(await page.locator('[data-progression-preview-result]').isHidden(),false);check('Read-only Go preview displays validated metrics, plans and level thresholds.');
    await tab('progression-registration');await page.locator('#progression-registration [name=amount]').fill('200');await tab('progression-publication');await page.locator('#progression-publication [name=amount]').fill('300');await tab('progression-registration');mode='hold';await save('#progression-registration');await until(()=>held);await change('#progression-registration [name=amount]','400');await held();held=null;await text('[data-progression-version]','3');assert.equal(await page.locator('#progression-registration [name=amount]').inputValue(),'400');assert.equal(await page.locator('#progression-publication [name=amount]').inputValue(),'300');check('Growth module save preserves both newer input and another module draft.');
    await open('/admin/progression/');await lookup('progression');assert.equal(await page.locator('[data-progression-admin-history] tbody tr').count(),30);await page.locator('[data-progression-admin-more]').click();await page.waitForFunction(()=>document.querySelectorAll('[data-progression-admin-history] tbody tr').length===35);check('Actual growth account and additional history page load without duplicates.');
    await page.locator('[data-progression-adjust] [name=delta]').fill('5');await page.locator('[data-progression-adjust] [name=reason]').fill('验收经验');await save('[data-progression-adjust]');mode='invalid';await page.locator('[data-progression-admin-confirm-submit]').click();await text('[data-progression-operation-status]','原操作已保留');const originalGrowth=writes.at(-1).body;mode='refresh-fail';await page.locator('[data-progression-admin-confirm-submit]').click();await text('[data-progression-operation-status]','已记入成长记录');await text('[data-progression-operation-status]','账号刷新未完成');assert.deepEqual(writes.at(-1).body,originalGrowth);assert.equal(await page.locator('[data-progression-account]').isHidden(),true);check('Growth receipt validation, exact retry and confirmed-success feedback work on real forms.');
    await open('/admin/progression/');await lookup('progression');await page.locator('[data-progression-member] [name=reason]').fill('验收会员');await save('[data-progression-member]');await page.locator('[data-progression-admin-confirm-submit]').click();await text('[data-progression-operation-status]','已记入成长记录');assert.equal(writes.at(-1).body.action,'grant');assert.equal(await page.evaluate(()=>window.ChunnelProgressionShared.read('membership','1')),null);check('Membership requires confirmation and validates the complete receipt.');
    await page.evaluate(()=>document.dispatchEvent(new CustomEvent('chunnel:session',{detail:{authenticated:true,id:99,csrf_token:'other'}})));await text('[data-progression-admin-status]','登录状态已变化');assert.equal(await page.locator('[data-progression-account-summary]').textContent(),'');check('Account change closes dialogs and clears private growth details.');
    for(const [style,scheme,width] of [['classic','light',1365],['liquid-glass','dark',390]]){
      await context.request.post(fixture.url+'/fixture/style/'+style);await page.setViewportSize({width,height:960});await page.emulateMedia({colorScheme:scheme});
      for(const kind of ['currency','progression']){await open('/admin/'+kind+'/');await lookup(kind);await page.evaluate(()=>document.fonts.ready);assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+2),true,`${kind} overflow`);await page.screenshot({path:resolve(directory,`${name}-${variant}-${kind}-${style}.png`),fullPage:true,animations:'disabled'});}
    }
    check('Both real pages fit desktop and narrow screens in light/classic and dark/glass.');
    await page.goto(fixture.url+'/fixture/login/reader');for(const path of ['/api/v1/admin/currencies/main','/api/v1/admin/progression'])assert.equal((await context.request.get(fixture.url+path)).status(),403);check('Reader accounts cannot access either administrative API.');
    const guest=await browser.newContext({javaScriptEnabled:false}),guestPage=await guest.newPage();await guestPage.goto(fixture.url+'/admin/currency/');assert.equal(await guestPage.locator('[data-currency-adjust]').count(),0);await guest.close();assert.deepEqual(errors,[]);assert.equal(scripts.some(path=>/analytics-react|echarts/i.test(path)),false);check('Guest access, absence of React/ECharts dependencies and browser console checks pass.');
  }catch(error){await page.screenshot({path:resolve(directory,`${name}-${variant}-failure.png`),fullPage:true}).catch(()=>{});throw new Error(`${name} ${variant}: ${error.message}; page errors=${JSON.stringify(errors)}`,{cause:error});}
  finally{if(held)await held().catch(()=>{});await context.request.post(fixture.url+'/fixture/style/classic').catch(()=>{});await browser.close();}
}
await writeFile(resolve(directory,`results-${variant}-${names.join('-')}.json`),JSON.stringify({variant,checks:evidence.length,evidence},null,2)+'\n');
console.log(`${evidence.length} ledger administration checks passed.`);
