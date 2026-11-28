// Real Go templates and resource delivery; all mutations are intercepted.
import assert from 'node:assert/strict';
import {readFile,writeFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import {build} from 'esbuild';
import {chromium,firefox,webkit} from 'playwright';
const directory=resolve(process.argv[2]),fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
const variant=process.argv[4]==='generated'?'generated':'typescript',engines={chromium,firefox,webkit},names=process.argv[3]&&process.argv[3]!=='all'?[process.argv[3]]:Object.keys(engines),evidence=[],assets=new Map();
const outputs={updates:'chunnel-updates','extension-updates':'chunnel-extension-updates',system:'chunnel-system',themes:'newpress-themes',announcements:'chunnel-announcements'};
if(variant==='typescript')for(const [name,out]of Object.entries(outputs))assets.set(out+'.js',(await build({entryPoints:['frontend/core/'+name+'.ts'],bundle:true,write:false,format:'iife',platform:'browser',target:['chrome100','firefox110','safari16'],charset:'utf8'})).outputFiles[0].text);
const delay=ms=>new Promise(resolve=>setTimeout(resolve,ms));
for(const name of names){
 const browser=await engines[name].launch(),context=await browser.newContext({viewport:{width:1440,height:1000},reducedMotion:'reduce'}),page=await context.newPage();page.setDefaultTimeout(10000);
 let mode='',held=null,core,extension,progress,systemCalls=0,privateCalls=0,progressCalls=0;const writes=[],errors=[],scripts=[];
 const check=text=>{evidence.push({engine:name,text});console.log(name+': '+text);};
 const reset=()=>{mode='';held=null;core=structuredClone(fixture.snapshot);Object.assign(core,{trusted_publisher:true,managed:true,revision:'a'.repeat(64),available:{version:'0.2.0',notes:'隔离更新说明'}});extension=structuredClone(fixture.job);progress=structuredClone(fixture.progress);};reset();
 const json=(route,value,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(value)});
 const wait=async predicate=>{const deadline=Date.now()+10000;while(!predicate()){if(Date.now()>deadline)throw Error('Missing intercepted request');await delay(20);}};
 const release=async()=>{await wait(()=>held);const deliver=held;held=null;await deliver().catch(()=>{});};
 const has=(selector,text)=>page.waitForFunction(({selector,text})=>document.querySelector(selector)?.textContent?.includes(text),{selector,text});
 const setValue=(selector,value)=>page.locator(selector).evaluate((el,value)=>{el.value=value;el.dispatchEvent(new Event('input',{bubbles:true}));},value);
 const submit=selector=>page.locator(selector).evaluate(form=>form.requestSubmit());
 const replace=selector=>page.evaluate(selector=>document.dispatchEvent(new CustomEvent('chunnel:admin-before-replace',{detail:{regions:[document.querySelector(selector)]}})),selector);
 const session=()=>page.evaluate(()=>document.dispatchEvent(new CustomEvent('chunnel:session')));
 const fresh=async path=>{reset();await page.goto(fixture.url+path);};
 const corePage=async()=>{await fresh('/admin/updates/');await has('[data-update-connection]','实时同步');};
 const market=async()=>{await fresh('/admin/themes/');await page.waitForFunction(()=>document.querySelector('[data-extension-workspace]')?.getAttribute('aria-busy')==='false');};
 const visual=async label=>{await writeFile(resolve(directory,variant+'-'+name+'-'+label+'.aria.txt'),await page.locator('body').ariaSnapshot());await page.screenshot({path:resolve(directory,variant+'-'+name+'-'+label+'-desktop.png'),fullPage:true});await page.setViewportSize({width:390,height:844});for(const theme of ['light','dark']){await page.evaluate(theme=>document.documentElement.dataset.theme=theme,theme);await page.waitForTimeout(350);assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'viewport overflow '+label);await page.screenshot({path:resolve(directory,variant+'-'+name+'-'+label+'-'+theme+'.png'),fullPage:true});}await page.setViewportSize({width:1440,height:1000});check(label+' desktop and narrow light/dark layouts fit.');};
 page.on('pageerror',error=>{errors.push(error.message);console.error('PAGE ERROR',error.stack);});page.on('dialog',dialog=>dialog.type()==='beforeunload'?dialog.accept():dialog.dismiss());page.on('request',request=>{if(request.resourceType()==='script')scripts.push(request.url());});
 await context.route('**/*',async route=>{
  const request=route.request(),url=new URL(request.url()),path=url.pathname,file=path.split('/').at(-1),method=request.method();
  if(url.origin!==fixture.url)return route.abort();
  if(assets.has(file))return route.fulfill({contentType:'text/javascript',body:assets.get(file)});
  if(method==='GET'||method==='HEAD'){
   if(path==='/api/v1/admin/updates'){privateCalls++;if(mode==='core-auth')return json(route,{error:{message:'登录已过期'}},401);return json(route,core);}
   if(path==='/api/v1/admin/updates/progress'){progressCalls++;assert.equal(request.headers()['x-update-ticket'],'b'.repeat(64));return json(route,progress);}
   if(path.startsWith('/api/v1/admin/extensions/')&&path.endsWith('/job')){const kind=path.split('/').at(-2);return json(route,{...extension,kind:extension.phase==='idle'?'':kind});}
   if(path==='/api/v1/admin/system-status'){
    systemCalls++;if(mode==='system-auth')return json(route,{error:{message:'没有权限'}},403);
    if(mode==='system-offline')return route.abort();
    const report={...structuredClone(fixture.system),collected_at:new Date().toISOString(),collected_label:'浏览器验证',hostname:'fixture-private-host'};
    if(mode==='system-malformed')report.metrics[0].percent=101;
    if(mode==='system-held'){held=()=>json(route,report);return;}return json(route,report);
   }
   return route.continue();
  }
  const body=request.headers()['content-type']?.includes('json')?request.postDataJSON():Object.fromEntries(new URLSearchParams(request.postData()||''));writes.push({path,body});
  if(path.startsWith('/api/'))assert(request.headers()['x-csrf-token']);
  if(path==='/api/v1/admin/updates/policy'){
   const saved={...core.policy,...body,revision:'c'.repeat(64),customized:true};delete saved.use_defaults;
   if(mode==='policy-conflict')return json(route,{error:{code:'updates.policy_changed',message:'策略已变更'}},409);
   const deliver=()=>{core.policy=saved;return json(route,saved);};if(mode==='policy-held'){held=deliver;return;}return deliver();
  }
  if(path==='/api/v1/admin/updates/source'){
   if(body.official)assert.deepEqual(body,{official:true});
   if(mode==='source-held'){held=()=>json(route,{saved:true});return;}return json(route,{saved:true});
  }
  if(path==='/api/v1/admin/updates/start'){assert.equal(body.revision,core.revision);assert.equal(body.backup_confirmed,true);if(mode==='start-lost')return route.abort();return json(route,{ticket:'b'.repeat(64)},202);}
  if(path==='/api/v1/admin/updates/check'||path==='/api/v1/admin/updates/cancel'){assert.equal(request.postData(),null);return json(route,{accepted:true},202);}
  if(path.startsWith('/api/v1/admin/extensions/')){
   assert.equal(body.expected_user_id,fixture.owner_id);const parts=path.split('/'),action=parts.at(-1),kind=parts[5],slug=action==='check'?'':parts.at(-2);
   extension={...fixture.extension_job,kind,action,slug,target:body.version||'',phase:'queued'};
   if(mode==='extension-lost')return route.abort();if(mode==='extension-held'){held=()=>json(route,extension,202);return;}return json(route,extension,202);
  }
  if(path==='/admin/announcements/')return json(route,{recorded:true});
  throw Error('Unexpected write '+path);
 });
 try{
  await page.goto(fixture.login);await has('[data-update-connection]','实时同步');assert.equal(await page.locator('[data-update-start]').isDisabled(),true);check('Backup confirmation is required before installation.');
  await page.locator('[data-update-backup]').check();core.revision='d'.repeat(64);await page.waitForFunction(()=>document.querySelector('[data-update-backup]').checked===false);check('Release revision changes invalidate backup confirmation.');
  await page.locator('[name=observation_seconds]').fill('33');mode='policy-held';await submit('[data-update-policy-form]');await wait(()=>held);await setValue('[name=observation_seconds]','47');await release();await has('[data-update-policy-form] [data-save-state]','策略已保存');assert.equal(await page.locator('[name=observation_seconds]').inputValue(),'47');assert(await page.locator('[data-update-policy-form] [data-admin-savebar]').evaluate(el=>el.classList.contains('is-dirty')));check('Saved policy advances its revision while retaining newer input.');
  mode='policy-conflict';await submit('[data-update-policy-form]');await page.locator('[data-update-policy-conflict]').waitFor({state:'visible'});assert.equal(await page.locator('[name=observation_seconds]').inputValue(),'47');check('Conflicting policy saves preserve the draft and require reload.');
  await corePage();await page.locator('[name=official]').uncheck();await page.locator('[name=token]').fill('submitted-test-token');mode='source-held';await submit('[data-update-source]');await wait(()=>held);await setValue('[name=token]','newer-test-token');await release();await has('[data-update-source] [data-save-state]','未保存');assert.equal(await page.locator('[name=token]').inputValue(),'newer-test-token');check('Source save preserves a later credential and leaves it unsaved.');
  await corePage();mode='start-lost';await page.locator('[data-update-backup]').check();const starts=writes.filter(w=>w.path.endsWith('/start')).length;await page.locator('[data-update-start]').click();await page.locator('[data-update-feedback]').waitFor({state:'visible'});await delay(1200);assert.equal(await page.locator('[data-update-start]').isDisabled(),true);assert.equal(writes.filter(w=>w.path.endsWith('/start')).length,starts+1);check('Lost installation receipt stays locked after an idle poll without replaying POST.');
  await corePage();await page.locator('[data-update-backup]').check();await page.locator('[data-update-start]').click();await has('[data-update-phase]','观察');const adminReads=privateCalls,receiptReads=progressCalls;await session();progress={...progress,phase:'rolled_back',percent:100,updated_at:new Date().toISOString()};await has('[data-update-current]','0.1.0');await wait(()=>progressCalls>receiptReads);assert.equal(privateCalls,adminReads);assert.equal(await page.locator('[data-update-check]').isDisabled(),true);check('Session loss locks writes while the job receipt continues to show rollback.');await visual('updates');
  await context.clearCookies();await page.goto(fixture.login);await page.evaluate(()=>sessionStorage.removeItem('newpress-update-ticket'));await corePage();await page.locator('[name=observation_seconds]').fill('44');mode='policy-held';await submit('[data-update-policy-form]');await wait(()=>held);await replace('[data-update-center]');await release();assert.equal(await page.locator('[name=observation_seconds]').inputValue(),'44');check('Detached update page ignores late policy receipt.');
  await market();assert(await page.locator('[data-theme-card]').count()>=3);await page.locator('[data-theme-search]').fill('没有这个主题');await page.locator('[data-theme-empty]').waitFor({state:'visible'});await page.locator('[data-theme-clear]').click();await page.locator('[data-theme-filter=installed]').click();assert.equal(await page.locator('[data-theme-card][data-installed=false]:visible').count(),0);await page.locator('[data-theme-filter=all]').click();check('Theme search, clear and installed filtering preserve the catalog.');
  const preview=page.locator('#theme-gallery [data-theme-preview]');await preview.click();await page.locator('[data-theme-preview-dialog]').waitFor({state:'visible'});await page.keyboard.press('Escape');assert(await preview.evaluate(el=>el===document.activeElement));check('Theme preview supports Escape and restores focus.');
  await page.locator('#theme-gallery [data-extension-action=install]').click();const count=writes.length;await page.locator('[data-extension-dialog] button[value=cancel]').last().click();assert.equal(writes.length,count);check('Cancelling a theme confirmation sends no request.');await visual('themes');
  mode='extension-lost';await page.locator('#theme-gallery [data-extension-action=install]').click();await page.locator('[data-extension-confirm]').click();await page.locator('[data-extension-dialog]').waitFor({state:'hidden'});await page.waitForFunction(()=>document.querySelector('[data-extension-job]').dataset.phase==='queued');assert.equal(await page.locator('[data-extension-check]').isDisabled(),true);check('Unknown extension submission reconciles the existing job without retrying.');
  extension={...extension,phase:'succeeded',updated_at:new Date().toISOString()};await page.waitForEvent('load');await page.waitForFunction(()=>document.querySelector('[data-extension-workspace]').getAttribute('aria-busy')==='false');check('Terminal extension job reloads server-rendered state once.');
  await market();mode='extension-held';await page.locator('[data-extension-check]').click();await wait(()=>held);await session();await release();assert.equal(await page.locator('[data-extension-confirm]').isDisabled(),true);assert.equal(await page.locator('[data-job-detail]').innerText(),'');check('Session changes retire extension writes and clear private task details.');
  await fresh('/admin/appearance/updates/');await page.waitForFunction(()=>document.querySelector('[data-extension-workspace]').getAttribute('aria-busy')==='false');await page.locator('.extension-channels a[href*="preview"]').click();await page.waitForFunction(()=>document.querySelector('[data-extension-workspace]').dataset.channel==='preview');check('Partial channel navigation rebinds extension controls.');
  await fresh('/admin/plugins/updates/');await page.waitForFunction(()=>document.querySelector('[data-extension-workspace]').getAttribute('aria-busy')==='false');await page.locator('[data-extension-check]').click();await page.waitForFunction(()=>document.querySelector('[data-extension-job]').dataset.phase==='queued');assert.equal(writes.at(-1).path,'/api/v1/admin/extensions/plugin/check');check('Plugin updates use the plugin job scope.');
  await fresh('/admin/system/');await has('[data-system-hostname]','fixture-private-host');await page.locator('[data-system-auto]').focus();await page.locator('[data-system-auto]').press('Space');mode='system-malformed';await page.locator('[data-system-refresh]').click();await has('[data-system-feedback]','保留上次快照');assert.equal(await page.locator('[data-system-hostname]').innerText(),'fixture-private-host');check('Malformed status retains the last valid snapshot, never zero-fills.');
  mode='';await page.locator('[data-system-refresh]').click();await has('[data-system-feedback]','状态已更新');await visual('system');mode='system-auth';await page.locator('[data-system-refresh]').click();await has('[data-system-feedback]','无权');assert.equal(await page.locator('[data-system-hostname]').innerText(),'');assert.equal(await page.locator('[data-system-auto]').isDisabled(),true);check('Revoked status permission clears private snapshot and stops polling.');
  await fresh('/admin/system/');await has('[data-system-hostname]','fixture-private-host');await page.locator('[data-system-auto]').focus();await page.locator('[data-system-auto]').press('Space');mode='system-held';await page.locator('[data-system-refresh]').click();await wait(()=>held);await replace('[data-system-status]');await release();const stopped=systemCalls;await delay(250);assert.equal(systemCalls,stopped);check('Removed system page retires its request and timers.');
  await fresh('/admin/announcements/');const notices=writes.length;await page.locator('[data-announcement-delete] button').first().click();assert.equal(writes.length,notices);check('Dismissing announcement deletion preserves the native form without posting.');
  assert.equal(errors.length,0,errors.join('\n'));assert(!scripts.some(url=>/analytics-app|echarts|react-dom/.test(url)));check('No page errors or React/ECharts downloads on these native pages.');
 }finally{await writeFile(resolve(directory,variant+'-'+name+'-network.json'),JSON.stringify({writes,scripts,errors,systemCalls,privateCalls,progressCalls},null,2));await context.close();await browser.close();}
}
await writeFile(resolve(directory,variant+'-'+names.join('-')+'-results.json'),JSON.stringify({variant,checks:evidence.length,evidence},null,2));
