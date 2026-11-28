// Real Go CDN page; all writes and every non-fixture origin are intercepted.
import assert from 'node:assert/strict';
import {readFile,writeFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import {build} from 'esbuild';
import {chromium,firefox,webkit} from 'playwright';
const directory=resolve(process.argv[2]),fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
const variant=process.argv[4]==='generated'?'generated':'typescript',engines={chromium,firefox,webkit};
const names=process.argv[3]&&process.argv[3]!=='all'?[process.argv[3]]:Object.keys(engines),evidence=[],assets=new Map();
if(variant==='typescript')for(const entry of ['cdn','cdn-routing'])assets.set('chunnel-'+entry+'.js',(await build({entryPoints:['frontend/core/'+entry+'.ts'],bundle:true,write:false,format:'iife',platform:'browser',target:['chrome100','firefox110','safari16'],charset:'utf8'})).outputFiles[0].text);
const copy=value=>structuredClone(value),delay=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const base='/api/v1/admin/plugins/cdn';
for(const engine of names){
 const browser=await engines[engine].launch(),context=await browser.newContext({viewport:{width:1440,height:1000},reducedMotion:'reduce'}),page=await context.newPage();page.setDefaultTimeout(10000);
 let mode='',holdRead='',held,heldRead,status,routing;const writes=[],errors=[],scripts=[],network=[];
 const check=text=>{evidence.push({engine,text});console.log(engine+': '+text);};
 const reset=()=>{mode='';held=undefined;heldRead=undefined;holdRead='';status=copy(fixture.snapshot);status.jobs=[copy(fixture.job)];routing=copy(fixture.routing);routing.reports={site:copy(fixture.report)};routing.plans=[{...copy(fixture.plan),expires:new Date(Date.now()+600000).toISOString()}];routing.tasks=[copy(fixture.task)];};reset();
 const json=(route,value,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(value)});
 const wait=async predicate=>{const end=Date.now()+10000;while(!predicate()){if(Date.now()>end)throw Error('Missing request');await delay(20);}};
 const release=async()=>{await wait(()=>held);const fn=held;held=undefined;await fn().catch(()=>{});};
 const has=(selector,text)=>page.waitForFunction(({selector,text})=>document.querySelector(selector)?.textContent?.includes(text),{selector,text});
 const value=(selector,text)=>page.waitForFunction(({selector,text})=>document.querySelector(selector)?.value===text,{selector,text});
 const set=(selector,text)=>page.locator(selector).evaluate((n,text)=>{n.value=text;n.dispatchEvent(new Event('input',{bubbles:true}));},text);
 const submit=selector=>page.locator(selector).evaluate(form=>form.requestSubmit());
 const ready=()=>page.waitForFunction(()=>document.querySelector('[data-dns="name"]')?.value==='分区 DNS');
 const fresh=async()=>{reset();await page.goto(fixture.url+'/admin/plugins/cdn/');await ready();};
 const show=async selector=>{const id=await page.locator(selector).evaluate(n=>n.closest('[data-settings-section]')?.id);if(id)await page.locator('[data-settings-target="'+id+'"]').click();};
 const click=async selector=>{await show(selector);await page.locator(selector).click();};
 const refresh=async()=>{await click('[data-route-refresh]');await delay(80);};
 const apply=async()=>{await show('[data-routing-plans]');await page.locator('[data-routing-plans] button').filter({hasText:'执行此计划'}).click();};
 const close=(selector,accepted)=>page.locator(selector).evaluate((dialog,accepted)=>{dialog.returnValue=accepted?'confirm':'cancel';dialog.close();},accepted);
 const cdnSettings=body=>{
  const next=copy(body.settings);next.version++;
  next.asset_base=next.asset_base.trim().replace(/\/+$/,'');if(next.asset_base)next.asset_base='https://'+new URL(next.asset_base).host.toLowerCase();
  const flags=[];
  for(const p of next.profiles){const old=status.settings.profile_views.find(x=>x.id===p.id&&x.provider===p.provider&&x.access_key===p.access_key.trim()&&x.resource_id===p.resource_id.trim());const tokenSaved=body.clear_tokens.includes(p.id)?false:!!(p.token||old?.token_saved);p.name=p.name.trim();p.access_key=p.access_key.trim();p.resource_id=p.resource_id.trim();p.domains=[...new Set(p.domains.map(d=>d.trim().toLowerCase()))].sort();delete p.secret;delete p.token;flags.push({...p,secret_saved:true,token_saved:tokenSaved});}
  next.profile_views=flags;return next;
 };
 const routeConfig=body=>{
  const next=copy(body.config);next.version++;
  for(const p of next.dns){const old=routing.config.dns.find(x=>x.id===p.id&&x.provider===p.provider&&x.zone===p.zone.trim().toLowerCase().replace(/\.$/,'')&&x.access_key===p.access_key);const tokenSaved=body.clear_tokens.includes(p.id)?false:!!(p.token||old?.token_saved);p.name=p.name.trim();p.zone=p.zone.trim().toLowerCase().replace(/\.$/,'');p.access_key=p.access_key.trim();delete p.secret;delete p.token;p.secret_saved=true;if(tokenSaved)p.token_saved=true;else delete p.token_saved;}
  for(const r of next.routes){r.name=r.name.trim();for(const k of ['hostname','fallback_origin','target','preferred_target'])r[k]=r[k].trim().toLowerCase().replace(/\.$/,'');r.preferred_lines=[...new Set(r.preferred_lines)].sort();}return next;
 };
 page.on('pageerror',error=>{errors.push(error.message);console.error('PAGE ERROR',error.stack);});
 page.on('dialog',dialog=>dialog.type()==='beforeunload'?dialog.accept():dialog.dismiss());
 page.on('request',request=>{if(request.resourceType()==='script')scripts.push(request.url());});
 await context.route('**/*',async route=>{
  const request=route.request(),url=new URL(request.url()),path=url.pathname,file=path.split('/').at(-1),method=request.method();network.push({method,path});
  if(url.origin!==fixture.url)return route.abort();
  if(assets.has(file))return route.fulfill({contentType:'text/javascript',body:assets.get(file)});
  if(method==='GET'||method==='HEAD'){
   if(path===base+'/status'||path===base+'/routing'){
    if(mode==='auth'&&path===base+'/routing')return json(route,{error:{code:'auth.expired',message:'fixture expired'}},403);
    let next=copy(path.endsWith('/status')?status:routing);
    if(mode==='malformed'&&path.endsWith('/routing'))next.plans[0].changes[0].after.ttl='invalid';
    if(holdRead===path){holdRead='';heldRead=()=>json(route,next);return;}
    return json(route,next);
   }
   return route.continue();
  }
  const body=request.postDataJSON();writes.push({path,body});assert.equal(body.expected_user_id,fixture.owner_id);assert(request.headers()['x-csrf-token']);assert.equal(request.headers()['origin'],fixture.url);
  if(mode==='conflict')return json(route,{error:{code:'cdn.conflict',message:'配置已变化，请刷新后重试'}},409);
  if(mode==='identity')return json(route,{error:{code:'plugins.identity_changed',message:'账号已变化'}},409);
  if(mode==='lost')return route.abort();
  let deliver;
  if(path===base+'/settings'){
   const saved=cdnSettings(body);deliver=()=>{status.settings=saved;return json(route,{ok:true,settings:saved});};
  }else if(path===base+'/routing/settings'){
   assert.equal(body.cdn_version,status.settings.version);const saved=routeConfig(body);deliver=()=>{routing.config=saved;return json(route,routing);};
  }else if(path===base+'/action'){
   assert.equal(body.expected_version,status.settings.version);
   if(body.action==='enqueue'){
    const job={...copy(fixture.job),operation:body.operation,profile_id:body.profile_id,targets:body.operation==='purge_host'?[...new Set(body.targets.map(v=>v.toLowerCase()))]:[...new Set(body.targets)],status:'queued',attempts:0};delete job.benchmark;delete job.benchmark_request;if(body.benchmark)job.benchmark_request=copy(body.benchmark);if(mode==='wrong-receipt')job.profile_id='wrong';
    deliver=()=>json(route,{ok:true,job},202);
   }else{assert(['cancel','retry'].includes(body.action));assert(body.job_id);deliver=()=>json(route,{ok:true});}
  }else if(path===base+'/routing/action'){
   assert.equal(body.expected_version,routing.config.version);assert.equal(body.cdn_version,status.settings.version);
   const task={...copy(fixture.task),action:body.action,route_id:body.route_id,status:'queued'};if(body.plan_id)task.plan_id=body.plan_id;if(mode==='wrong-receipt')task.route_id='wrong';
   if(body.action==='apply'){assert.equal(body.confirm,true);assert.equal(body.plan_id,routing.plans[0].id);}
   deliver=()=>json(route,{task},202);
  }else throw Error('Unexpected CDN write '+path);
  if(mode==='held'){held=deliver;return;}return deliver();
 });
 try{
  await page.goto(fixture.login);await ready();await click('[data-refresh]');await has('[data-benchmark-summary]','1 个地址');
  assert.equal(await page.locator('[data-profile="name"]').inputValue(),'主站 CF');assert.equal(await page.locator('[data-profile="secret"]').inputValue(),'');await has('[data-route-report]','隔离检查结果');check('Real Go page renders redacted profiles, benchmark results and routing reports.');
  assert.equal(await page.locator('[data-route="preferred_lines"] option[value="default"]').count(),0);assert.equal(await page.locator('[data-route="preferred_lines"] option[value="telecom"]').count(),1);check('Provider DNS lines exclude the default line reserved for normal traffic.');
  await show('[data-profile="name"]');await writeFile(resolve(directory,variant+'-'+engine+'-cdn.aria.txt'),await page.locator('body').ariaSnapshot());await page.screenshot({path:resolve(directory,variant+'-'+engine+'-cdn-desktop.png'),fullPage:true});
  await page.setViewportSize({width:390,height:844});for(const theme of ['light','dark']){await page.evaluate(theme=>document.documentElement.dataset.theme=theme,theme);await delay(300);assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'narrow CDN overflow');await page.screenshot({path:resolve(directory,variant+'-'+engine+'-cdn-'+theme+'.png'),fullPage:true});}await page.setViewportSize({width:1440,height:1000});check('Desktop and narrow light/dark layouts fit.');
  for(const selector of ['[data-routing-plans]','[data-benchmark-summary]']){await show(selector);const label=selector.includes('routing')?'routing':'benchmark';await page.screenshot({path:resolve(directory,variant+'-'+engine+'-'+label+'-desktop.png'),fullPage:true});await page.setViewportSize({width:390,height:844});await delay(150);assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'narrow '+label+' overflow');await page.screenshot({path:resolve(directory,variant+'-'+engine+'-'+label+'-narrow.png'),fullPage:true});await page.setViewportSize({width:1440,height:1000});}await show('[data-profile="name"]');check('Routing plans and benchmark tables remain usable on narrow screens.');

  await set('[data-profile="name"]','  已提交名称  ');await set('[data-profile="secret"]','submitted-secret');mode='held';await submit('#cdn-settings');await wait(()=>held);await set('[data-profile="name"]','等待期间的新名称');await set('[data-profile="secret"]','newer-secret');await release();await has('[data-message]','新输入仍未保存');assert.equal(await page.locator('[data-profile="name"]').inputValue(),'等待期间的新名称');assert.equal(await page.locator('[data-profile="secret"]').inputValue(),'newer-secret');assert.equal(writes.at(-1).body.settings.profiles[0].name,'  已提交名称  ');check('CDN save retains newer name and secret while binding the receipt to the submitted draft.');
  mode='';await submit('#cdn-settings');await has('[data-message]','配置已保存。');await value('[data-profile="secret"]','');assert.equal(await page.locator('[data-save]').isDisabled(),true);check('Confirmed credential replacement clears only the submitted secret.');
  assert.equal(await page.locator('[data-profiles] button').first().isDisabled(),false);check('Profile selection unlocks after reconciliation.');

  await fresh();await set('[data-dns="name"]','  新 DNS  ');await set('[data-dns="secret"]','dns-submitted');mode='held';await submit('[data-routing-form]');await wait(()=>held);await set('[data-route="ttl"]','1200');await set('[data-dns="secret"]','dns-newer');await release();await has('[data-route-message]','新输入仍未保存');assert.equal(await page.locator('[data-dns="name"]').inputValue(),'新 DNS');assert.equal(await page.locator('[data-route="ttl"]').inputValue(),'1200');assert.equal(await page.locator('[data-dns="secret"]').inputValue(),'dns-newer');check('Routing save adopts Go normalization and preserves newer DNS credentials and route fields.');
  mode='';await submit('[data-routing-form]');await has('[data-route-message]','接入配置已保存。');await value('[data-dns="secret"]','');assert.equal(await page.locator('[data-routing-save]').isDisabled(),true);check('Routing save reconciles the latest draft and clears submitted DNS credentials.');

  await fresh();mode='conflict';await set('[data-profile="name"]','冲突后的草稿');await submit('#cdn-settings');await has('[data-message]','配置已变化');assert.equal(await page.locator('[data-profile="name"]').inputValue(),'冲突后的草稿');assert.equal(await page.locator('[data-save]').isDisabled(),false);check('CDN version conflict keeps draft and allows an explicit retry.');
  await fresh();mode='conflict';await set('[data-route="name"]','接入草稿');await submit('[data-routing-form]');await has('[data-route-message]','配置已变化');assert.equal(await page.locator('[data-route="name"]').inputValue(),'接入草稿');check('Routing version conflict keeps the full route draft.');

  await fresh();await apply();let count=writes.length;await close('[data-routing-confirm]',false);await delay(70);assert.equal(writes.length,count);check('Cancelling the exact DNS plan confirmation submits nothing.');
  await apply();await close('[data-routing-confirm]',true);await has('[data-route-message]','接入任务已提交');assert.equal(writes.at(-1).body.action,'apply');assert.equal(writes.at(-1).body.plan_id,routing.plans[0].id);check('Applying a reviewed plan sends its exact ID, route and both configuration versions.');
  await fresh();await apply();count=writes.length;routing.plans[0].changes[0].after.value='changed.example.test';await page.locator('[data-route-refresh]').evaluate(n=>n.click());await delay(100);await close('[data-routing-confirm]',true);await has('[data-route-message]','重新预览并确认');assert.equal(writes.length,count);check('Changed plan contents invalidate an open confirmation.');
  await fresh();await apply();count=writes.length;routing.config.version++;await page.locator('[data-route-refresh]').evaluate(n=>n.click());await delay(100);await close('[data-routing-confirm]',true);await has('[data-route-message]','重新预览并确认');assert.equal(writes.length,count);check('A routing version change invalidates an open confirmation.');
  await fresh();routing.plans[0].expires=new Date(Date.now()-1000).toISOString();await refresh();assert.equal(await page.locator('[data-routing-plans] button').first().isDisabled(),true);check('Expired plans cannot be executed.');
  await fresh();await apply();count=writes.length;status.settings.version++;await page.locator('[data-refresh]').evaluate(n=>n.click());await delay(100);await close('[data-routing-confirm]',true);await has('[data-route-message]','重新预览并确认');assert.equal(writes.length,count);check('A CDN connection version change also invalidates routing confirmation.');
  await fresh();await show('[data-preferred-service]');await page.locator('[data-preferred-service]').selectOption({index:1});await click('[data-preferred-fill]');assert.equal(await page.locator('[data-route="preferred_target"]').inputValue(),await page.locator('[data-preferred-service]').inputValue());count=writes.length;assert.equal(await page.locator('[data-route-action="inspect"]').isDisabled(),true);await submit('[data-routing-form]');await has('[data-route-message]','接入配置已保存。');assert.equal(writes.length,count+1);check('Preferred-service fill edits only the target and requires saving before inspection.');

  await fresh();await show('[data-operation]');await page.locator('[data-operation]').selectOption('purge_all');count=writes.length;await submit('[data-operation-form]');await has('[data-confirm-text]','zone123');await close('[data-confirm]',false);await delay(60);assert.equal(writes.length,count);await submit('[data-operation-form]');await close('[data-confirm]',true);await has('[data-message]','任务已入队');assert.equal(writes.at(-1).body.confirm_broad,true);assert.deepEqual(writes.at(-1).body.targets,[]);check('Broad cache purge requires a scope confirmation and binds the enqueue receipt.');
  await fresh();await show('[data-operation]');await page.locator('[data-operation]').selectOption('purge_all');await submit('[data-operation-form]');count=writes.length;status.settings.version++;await page.locator('[data-refresh]').evaluate(n=>n.click());await delay(100);await close('[data-confirm]',true);await has('[data-message]','重新核对操作范围');assert.equal(writes.length,count);check('Changed CDN configuration invalidates a pending broad-purge confirmation.');

  await fresh();mode='wrong-receipt';await click('[data-test]');await has('[data-message]','未能确认操作结果');assert.equal(await page.locator('[data-test]').isDisabled(),true);check('Mismatched CDN enqueue receipts remain unresolved and cannot be replayed automatically.');
  await fresh();mode='wrong-receipt';await click('[data-route-action="inspect"]');await has('[data-route-message]','未能确认操作结果');assert.equal(await page.locator('[data-route-action="inspect"]').isDisabled(),true);check('Mismatched routing task receipts keep mutations locked.');
  await fresh();mode='lost';count=writes.length;await click('[data-route-action="inspect"]');await has('[data-route-message]','未能确认操作结果');mode='';await refresh();assert.equal(writes.length,count+1);assert.equal(await page.locator('[data-route-action="inspect"]').isDisabled(),true);check('A lost routing receipt is not cleared by an old idle task list or replayed.');
  await fresh();mode='lost';count=writes.length;await click('[data-test]');await has('[data-message]','未能确认操作结果');mode='';await click('[data-refresh]');await delay(100);assert.equal(writes.length,count+1);assert.equal(await page.locator('[data-test]').isDisabled(),true);check('Lost CDN enqueue results remain locked after a successful status refresh.');
  await fresh();await set('[data-benchmark-form] [name="url"]','https://example.test/cdn-cgi/trace');await submit('[data-benchmark-form]');await has('[data-message]','任务已入队');assert.equal(writes.at(-1).body.operation,'benchmark');assert.equal(writes.at(-1).body.benchmark.url,'https://example.test/cdn-cgi/trace');check('Benchmark submission keeps its complete request and validates its returned parameters.');
  await fresh();status.jobs[0].status='running';await click('[data-refresh]');await page.locator('[data-jobs] button').filter({hasText:'取消'}).click();await has('[data-message]','已取消本地任务');assert.equal(writes.at(-1).body.job_id,status.jobs[0].id);status.jobs[0].status='failed';await click('[data-refresh]');await page.locator('[data-jobs] button').filter({hasText:'重试'}).click();await has('[data-confirm-text]','重新执行');count=writes.length;await close('[data-confirm]',false);await delay(60);assert.equal(writes.length,count);await page.locator('[data-jobs] button').filter({hasText:'重试'}).click();await close('[data-confirm]',true);await has('[data-message]','任务已重新排队');assert.equal(writes.at(-1).body.action,'retry');assert.equal(writes.at(-1).body.job_id,status.jobs[0].id);check('Cancellation binds the exact job and retry requires a fresh explicit confirmation.');

  await fresh();holdRead=base+'/routing';await click('[data-route-refresh]');await wait(()=>heldRead);await set('[data-route="name"]','新版本名称');await submit('[data-routing-form]');await has('[data-route-message]','接入配置已保存。');await heldRead().catch(()=>{});await delay(100);assert.equal(await page.locator('[data-route="name"]').inputValue(),'新版本名称');check('Pre-save routing reads are retired and cannot overwrite a successful save.');
  await fresh();holdRead=base+'/status';await click('[data-refresh]');await wait(()=>heldRead);await set('[data-profile="name"]','新 CDN 名称');await submit('#cdn-settings');await has('[data-message]','配置已保存。');await heldRead().catch(()=>{});await delay(100);assert.equal(await page.locator('[data-profile="name"]').inputValue(),'新 CDN 名称');check('Pre-save CDN reads cannot restore an older configuration.');

  await fresh();mode='held';await set('[data-dns="secret"]','temporary-private');await submit('[data-routing-form]');await wait(()=>held);await page.evaluate(()=>document.dispatchEvent(new CustomEvent('chunnel:session')));await has('[data-route-message]','登录或权限已变化');await release();await delay(100);assert.equal(await page.locator('[data-dns="secret"]').inputValue(),'');assert.equal(await page.locator('[data-profile="name"]').inputValue(),'');assert.equal(await page.locator('[data-route-report]').textContent(),'保存方案并检查接入，查看证书、NS 委派和可用解析线路。');assert.equal(await page.locator('[data-routing-save]').isDisabled(),true);check('Account change clears both workspaces and ignores a late credential save response.');
  await fresh();mode='auth';await refresh();await has('[data-message]','登录或权限已变化');assert.equal(await page.locator('[data-dns="zone"]').inputValue(),'');assert.equal(await page.locator('[data-routing-export][href]').count(),0);check('Read permission loss clears private fields and removes all routing export links.');
  await fresh();mode='identity';await set('[data-profile="name"]','different-account');await submit('#cdn-settings');await has('[data-message]','登录或权限已变化');assert.equal(await page.locator('[data-profile="name"]').inputValue(),'');check('Server identity conflicts retire both authenticated workspaces.');

  await fresh();mode='held';await set('[data-profile="name"]','late-save');await submit('#cdn-settings');await wait(()=>held);count=writes.length;await page.evaluate(()=>{dispatchEvent(new PageTransitionEvent('pagehide',{persisted:true}));dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}));});await release();await delay(150);assert.equal(await page.locator('[data-save]').isDisabled(),true);assert.equal(writes.length,count);await has('[data-save-status]','待核对');check('BFCache restore keeps an interrupted mutation unresolved without consuming its late response.');
  await fresh();holdRead=base+'/routing';await click('[data-route-refresh]');await wait(()=>heldRead);const previous=await page.locator('[data-route-report]').textContent();await page.evaluate(()=>document.dispatchEvent(new CustomEvent('chunnel:admin-before-replace',{detail:{regions:[document.querySelector('[data-cdn-admin]')]}})));const requestsBefore=network.length;await heldRead().catch(()=>{});await page.evaluate(()=>dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true})));await delay(150);assert.equal(await page.locator('[data-route-report]').textContent(),previous);assert.equal(network.length,requestsBefore);check('Region replacement permanently disposes reads, listeners and BFCache restart.');

  await fresh();mode='malformed';await refresh();await has('[data-route-message]','响应不完整');await has('[data-route-report]','隔离检查结果');check('Malformed routing data keeps the last validated report and reports the failure.');
  await fresh();const invalid={...copy(fixture.snapshot.settings),workers:'not-number'};count=writes.length;await page.locator('[data-import-file]').setInputFiles({name:'invalid.json',mimeType:'application/json',buffer:Buffer.from(JSON.stringify(invalid))});await has('[data-message]','响应不完整');assert.equal(writes.length,count);check('Config import validates every policy field before replacing the draft.');
  const valid=copy(fixture.snapshot.settings);valid.profiles[0].name='导入连接';await page.locator('[data-import-file]').setInputFiles({name:'cdn.json',mimeType:'application/json',buffer:Buffer.from(JSON.stringify(valid))});await close('[data-confirm]',true);await value('[data-profile="name"]','导入连接');assert.equal(writes.length,count);check('Valid import requires confirmation and stays a local draft until saved.');
  assert.equal(errors.length,0);assert(scripts.some(url=>url.includes('/chunnel-cdn.js')));assert(scripts.some(url=>url.includes('/chunnel-cdn-routing.js')));assert(!scripts.some(url=>/analytics-app|echarts/.test(url)));check('No page exceptions or interactive analytics dependencies.');
 }catch(error){await page.screenshot({path:resolve(directory,variant+'-'+engine+'-failure.png'),fullPage:true}).catch(()=>{});throw error;}
 finally{await writeFile(resolve(directory,variant+'-'+engine+'-network.json'),JSON.stringify({writes,scripts,errors,network},null,2));await context.close();await browser.close();}
}
await writeFile(resolve(directory,variant+'-results.json'),JSON.stringify({variant,checks:evidence.length,evidence},null,2));
console.log(variant+' CDN verification: '+evidence.length+' checks passed.');
