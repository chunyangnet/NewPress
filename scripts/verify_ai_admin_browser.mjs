// Actual Go pages/read contracts; all business writes and model calls are intercepted.
import assert from 'node:assert/strict';
import {readFile,writeFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import {build} from 'esbuild';
import {chromium,firefox,webkit} from 'playwright';
const directory=resolve(process.argv[2]),fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
const variant=process.argv[4]==='generated'?'generated':'typescript',engines={chromium,firefox,webkit},names=process.argv[3]&&process.argv[3]!=='all'?[process.argv[3]]:Object.keys(engines),assets=new Map(),evidence=[];
if(variant==='typescript')for(const name of ['ai-settings','ai-usage','ai-workbench','ai-loader'])assets.set('chunnel-'+name+'.js',(await build({entryPoints:['frontend/core/'+name+'.ts'],bundle:true,write:false,format:'iife',platform:'browser',target:['chrome100','firefox110','safari16'],charset:'utf8'})).outputFiles[0].text);
const until=async fn=>{const end=Date.now()+12000;while(!fn()){if(Date.now()>end)throw new Error('Missing intercepted request');await new Promise(r=>setTimeout(r,20));}};
for(const name of names){
 const browser=await engines[name].launch(),context=await browser.newContext({viewport:{width:1365,height:960},reducedMotion:'reduce',acceptDownloads:true}),page=await context.newPage();page.setDefaultTimeout(12000);
 let settings,config,mode='normal',held,preview,report=structuredClone(fixture.report),serial=1,history=[report],lookupFail=false,loaderMode='',textLoads=0,imageLoads=0,styleLoads=0;
 const errors=[],writes=[],scripts=[],check=text=>{evidence.push({engine:name,text});console.log(name+': '+text);};
 const json=(route,body,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(body)});
 page.on('pageerror',e=>{errors.push(e.message);console.error('PAGE ERROR',page.url(),e.stack);});page.on('dialog',d=>d.accept());page.on('request',r=>{if(r.resourceType()==='script')scripts.push(r.url());});
 const redact=(body)=>({...body,revision:'saved-'+serial++,providers:body.providers.map(p=>{const old=settings.providers.find(q=>q.id===p.id),result={...p,has_key:!!p.api_key||!!old?.has_key,balance:{...p.balance,has_token:!p.balance.clear_token&&(!!p.balance.token||!!old?.balance.has_token)}};delete result.api_key;delete result.balance.token;delete result.balance.clear_token;return result;})});
 const done=body=>{
  const provider=config.providers.find(p=>p.id===body.provider_id);report={id:(serial++).toString(16).padStart(32,'0'),owner:fixture.owner_id,task:preview.task,title:preview.title,created_at:new Date().toISOString(),provider:provider.name,model:body.model,content:'## 验收报告\n\n| 项目 | 数量 |\n|---|---:|\n| 内容 | 2 |\n\n<img src=x onerror=alert(1)>\n\n[危险](javascript:alert(1))',source:{...preview,token:''}};
  if(mode==='wrong-done')report.model='unexpected-model';
  const saved=mode!=='unsaved';if(saved&&mode!=='wrong-done')history.unshift(report);
  return {report,saved,usage:{prompt_tokens:0,completion_tokens:0,total_tokens:0,total_source:'reported'},...(saved?{}:{warning:'报告已生成，但保存失败。请复制或导出后重试。'})};
 };
 await context.route('**/*',async route=>{
  const req=route.request(),url=new URL(req.url()),path=url.pathname,file=path.split('/').at(-1);
  if(url.origin!==fixture.url)return route.abort();
  if(loaderMode&&file==='chunnel-ai-editor.js'){textLoads++;if(loaderMode==='fail'){loaderMode='retry';return route.abort();}return route.fulfill({contentType:'text/javascript',body:"window.ChunnelAIEditorReady=true;document.addEventListener('click',e=>{if(e.target.closest('[data-ai-open]'))window.fixtureTextOpened=(window.fixtureTextOpened||0)+1});"});}
  if(loaderMode&&file==='chunnel-ai-images.js'){imageLoads++;return route.fulfill({contentType:'text/javascript',body:"window.ChunnelAIImagesReady=true;document.addEventListener('click',e=>{if(e.target.closest('[data-ai-image-open]'))window.fixtureImageOpened=(window.fixtureImageOpened||0)+1});"});}
  if(loaderMode&&['chunnel-ai-editor.css','chunnel-ai-images.css'].includes(file)){styleLoads++;return route.fulfill({contentType:'text/css',body:''});}
  if(assets.has(file))return route.fulfill({contentType:'text/javascript',body:assets.get(file)});
  if(path.startsWith('/fixture/'))return route.continue();
  if(req.method()==='GET'){
   if(mode==='readonly'&&req.resourceType()==='document'){const response=await route.fetch();return route.fulfill({response,body:(await response.text()).replaceAll('data-read-only="false"','data-read-only="true"').replace('data-ai-config-fields>','disabled data-ai-config-fields>')});}
   if(path==='/api/v1/admin/ai/settings'&&settings){if(mode==='401')return json(route,{error:{message:'会话失效'}},401);if(mode==='hold-read'){held=()=>json(route,settings);return;}return json(route,settings);}
   if(path==='/api/v1/admin/ai/usage'){
    if(mode==='usage-fail')return json(route,{error:{message:'统计暂不可用'}},503);
    if(mode==='usage-401')return json(route,{error:{message:'会话失效'}},401);
    if(mode==='usage-hold'){const response=await route.fetch();held=()=>route.fulfill({response});return;}
    if(mode==='usage-wrong'){const response=await route.fetch(),data=await response.json();data.start='2000-01-01';return json(route,data);}
   }
   if(path==='/api/v1/admin/ai/workbench/reports'){if(lookupFail)return json(route,{error:{message:'历史暂不可用'}},503);return json(route,{reports:history.map(({content,source,...item})=>item)});}
   if(path.startsWith('/api/v1/admin/ai/workbench/reports/')){const item=history.find(r=>r.id===path.split('/').at(-1));return json(route,item||{error:{message:'未找到'}},item?200:404);}
   return route.continue();
  }
  if(req.method()==='HEAD')return route.continue();
  const body=req.headers()['content-type']?.includes('json')?req.postDataJSON():{};writes.push({path,body,method:req.method()});
  const deliver=async value=>{if(mode==='hold'){held=()=>json(route,value);return;}if(mode==='drop')return route.abort();if(mode==='invalid')return json(route,{ok:true});return json(route,value);};
  if(path==='/api/v1/admin/ai/settings'){
   if(mode==='conflict')return json(route,{error:{message:'设置已被其他页面更新'}},409);
   const result=redact(body);if(mode!=='invalid')settings=result;return deliver(result);
  }
  if(path==='/api/v1/admin/ai/models')return deliver({models:[{id:'discovered-model'}],base_url:'https://fixture.example.test/v1'});
  if(path==='/api/v1/admin/ai/balance')return deliver({remaining:0,used:1,total:1,currency:'USD',checked_at:new Date().toISOString(),expires_at:new Date(Date.now()+300000).toISOString(),cached:false});
  if(path==='/api/v1/admin/ai/workbench/preview'){
   preview={...fixture.previews[body.task],focus:body.focus.trim(),token:(serial++).toString(16).padStart(32,'0'),created_at:new Date().toISOString(),expires_at:new Date(Date.now()+600000).toISOString()};
   if(body.task==='summary')preview.context=JSON.stringify(body.post_id?{article:{id:body.post_id}}:{material:body.material.trim()});
   if(mode==='wrong-preview')return json(route,{...preview,focus:'unexpected'});return deliver(preview);
  }
  if(path==='/api/v1/admin/ai/workbench/generate'){
   const delta='event: delta\ndata: '+JSON.stringify({text:'已收到的部分文字'})+'\n\n';
   const complete=()=>route.fulfill({contentType:'text/event-stream',body:delta+'event: done\ndata: '+JSON.stringify(done(body))+'\n\n'});
   if(mode==='hold-stream'){held=complete;return;}
   if(mode==='broken')return route.fulfill({contentType:'text/event-stream',body:delta});
   if(mode==='malformed')return route.fulfill({contentType:'text/event-stream',body:delta+'event: delta\ndata: {"text":2}\n\n'});
   return complete();
  }
  if(path.startsWith('/api/v1/admin/ai/workbench/reports/')&&req.method()==='DELETE'){const id=path.split('/').at(-1);if(mode!=='invalid')history=history.filter(r=>r.id!==id);return deliver({deleted:true});}
  return json(route,{error:{message:'验收中未启用此写入'}},422);
 });
 const text=async(selector,value)=>page.waitForFunction(({selector,value})=>document.querySelector(selector)?.textContent?.includes(value),{selector,value});
 const submit=selector=>page.locator(selector).evaluate(f=>f.requestSubmit());
 const open=path=>page.goto(fixture.url+path);
 const settingsPage=async()=>{await open('/admin/settings/#site-settings-ai');await text('[data-ai-config-status]','接口已保存');};
 const workbench=async()=>{await open('/admin/ai/');await text('[data-aiw-meta]','个文本接口');await page.waitForFunction(()=>!document.querySelector('[data-aiw-preview]').disabled);};
 const prepare=async()=>{await submit('[data-aiw-form]');await text('[data-aiw-status]','数据已预览');};
 const retire=()=>page.evaluate(()=>document.dispatchEvent(new CustomEvent('chunnel:session',{detail:{authenticated:true,id:999,csrf_token:'other'}})));
 try{
  await page.goto(fixture.login);settings=await (await context.request.get(fixture.url+'/api/v1/admin/ai/settings')).json();config=await (await context.request.get(fixture.url+'/api/v1/admin/ai/workbench')).json();
  await settingsPage();assert.equal(await page.locator('.ai-provider-card').count(),2);check('AI settings load actual redacted Go providers and prices.');
  const card=()=>page.locator('.ai-provider-card').first();await card().locator('[data-provider-name]').fill('本次保存');mode='hold';await submit('[data-ai-config-form]');await until(()=>held);await card().locator('[data-provider-name]').fill('后续输入');await card().locator('[data-provider-key]').fill('later-secret');await held();held=null;mode='normal';await text('[data-ai-config-status]','后续输入仍待保存');assert.equal(await card().locator('[data-provider-name]').inputValue(),'后续输入');assert.equal(await card().locator('[data-provider-key]').inputValue(),'later-secret');check('Saving retains later provider edits and replacement secrets.');
  await submit('[data-ai-config-form]');await text('[data-ai-config-status]','AI 接口与模型已保存');assert.equal(await card().locator('[data-provider-key]').inputValue(),'');check('Confirmed save clears only the submitted secret.');
  mode='hold-read';await page.locator('[data-ai-config-reload]').click();await until(()=>held);await card().locator('[data-provider-name]').fill('读取中新输入');await held();held=null;mode='normal';await text('[data-ai-config-status]','读取期间有新输入');assert.equal(await card().locator('[data-provider-name]').inputValue(),'读取中新输入');check('Pending reload preserves new inputs.');
  for(const rejected of ['invalid','conflict','drop']){mode=rejected;await submit('[data-ai-config-form]');await text('[data-ai-config-status]',rejected==='invalid'?'未确认':rejected==='conflict'?'其他页面':'网络连接失败');assert.equal(await card().locator('[data-provider-name]').inputValue(),'读取中新输入');}mode='normal';check('Malformed, conflicting and lost save receipts preserve the draft.');
  await page.locator('[data-ai-config-reload]').click();await text('[data-ai-config-status]','接口已保存');mode='hold';await card().locator('[data-provider-probe]').click();await until(()=>held);await card().locator('[data-provider-base]').fill('http://localhost:11434/v1');await held();held=null;mode='normal';await text('.ai-provider-card [data-provider-status]','重新探测');assert.equal(await card().locator('[data-provider-allow-insecure]').isChecked(),false);assert.equal(await card().locator('[data-provider-discovered]').isHidden(),true);check('Changing a target resets approval and ignores obsolete model discovery.');
  await card().locator('[data-provider-allow-insecure]').check();const beforeProbe=writes.length;await card().locator('[data-provider-probe]').click();await text('.ai-provider-card [data-provider-status]','重新填写 Key');assert.equal(writes.length,beforeProbe);check('Saved keys cannot silently move to another endpoint.');
  await page.locator('[data-ai-config-reload]').click();await text('[data-ai-config-status]','接口已保存');await card().locator('[data-provider-probe]').click();await text('.ai-provider-card [data-provider-status]','找到 1 个');assert.deepEqual(writes.at(-1).body,{provider_id:'writer'});check('Unchanged discovery uses only the saved provider ID.');
  await card().locator('details').nth(1).locator('summary').click();await card().locator('[data-provider-balance-probe]').click();await text('.ai-provider-card [data-provider-balance-status]','剩余 USD 0');check('A reported zero balance remains zero.');
  await card().locator('[data-provider-name]').fill('待失效');mode='hold';await submit('[data-ai-config-form]');await until(()=>held);await retire();await held().catch(()=>{});held=null;mode='normal';await text('[data-ai-config-status]','页面已变化');assert.equal(await page.locator('.ai-provider-card').count(),0);check('Identity change clears configuration secrets and rejects a late save.');
  mode='401';await open('/admin/settings/#site-settings-ai');await text('[data-ai-config-status]','页面已变化');assert.equal(await page.locator('[data-ai-config-save]').isDisabled(),true);mode='normal';check('401 settings read retires the page.');
  await open('/admin/analytics/ai/');await page.waitForFunction(()=>document.querySelectorAll('[data-aiu-rows] tr').length>0);const oldPeriod=await page.locator('[data-aiu-period]').textContent();check('Usage charts and records load real Go totals.');
  await page.locator('[data-aiu-next]').click();await text('[data-aiu-page-info]','第 2 页');await page.locator('[data-aiu-prev]').click();await text('[data-aiu-page-info]','第 1 页');check('Usage pagination follows the last displayed record cursor.');
  await page.locator('[data-aiu-rows] button').first().click();await page.locator('[data-aiu-detail][open]').waitFor();assert.ok((await page.locator('[data-aiu-detail-body]').textContent()).includes('请求 ID'));await page.locator('[data-aiu-close]').click();check('Request details render the selected record and optional usage.');
  mode='usage-fail';await page.locator('[data-aiu-refresh]').click();await text('[data-aiu-notice]','上次成功结果');assert.equal(await page.locator('[data-aiu-period]').textContent(),oldPeriod);mode='usage-wrong';await page.locator('[data-aiu-refresh]').click();await text('[data-aiu-notice]','未确认');mode='normal';check('Failed and malformed usage reads retain clearly marked prior data.');
  mode='usage-hold';await page.locator('[data-aiu-range]').selectOption('7');await until(()=>held);const stale=held;held=null;mode='normal';await page.locator('[data-aiu-range]').selectOption('30');await page.waitForFunction(()=>!document.querySelector('[data-ai-usage]').classList.contains('is-loading'));const current=await page.locator('[data-aiu-period]').textContent();await stale().catch(()=>{});assert.equal(await page.locator('[data-aiu-period]').textContent(),current);check('Late usage responses cannot replace a newer period.');
  mode='usage-401';await page.locator('[data-aiu-refresh]').click();await text('[data-aiu-notice]','页面已变化');assert.equal(await page.locator('[data-aiu-rows] tr').count(),0);mode='normal';check('Expired usage access removes old metrics and request details.');
  await workbench();assert.equal(await page.locator('.aiw-task').count(),6);await prepare();assert.ok((await page.locator('[data-aiw-source-json]').textContent()).includes('system'));check('Workbench tasks and previews preserve actual Go source contracts.');
  await page.locator('[data-aiw-focus]').fill('新的要求');assert.equal(await page.locator('[data-aiw-generate]').isDisabled(),true);mode='wrong-preview';await submit('[data-aiw-form]');await text('[data-aiw-notice]','不一致');mode='normal';check('Changed inputs invalidate preview; mismatched previews cannot generate.');
  await prepare();await page.locator('[data-aiw-generate]').click();await text('[data-aiw-status]','报告已保存');assert.equal(await page.locator('[data-aiw-document] table').count(),1);assert.equal(await page.locator('[data-aiw-document] img,[data-aiw-document] script,[data-aiw-document] a[href^="javascript:"]').count(),0);await text('[data-aiw-usage]','输入 0');check('Verified completion renders accessible Markdown and preserves zero usage.');
  for(const failureMode of ['broken','malformed','wrong-done']){mode='normal';await prepare();mode=failureMode;await page.locator('[data-aiw-generate]').click();await text('[data-aiw-badge]','未完成');assert.equal(await page.locator('[data-aiw-document]').textContent(),'已收到的部分文字');}mode='normal';check('Broken streams and mismatched completion receipts retain partial text.');
  await prepare();mode='unsaved';await page.locator('[data-aiw-generate]').click();await text('[data-aiw-badge]','未保存');const downloadPromise=page.waitForEvent('download');await page.locator('[data-aiw-export]').click();const download=await downloadPromise;assert.match(await readFile(await download.path(),'utf8'),/未完成或未保存/);mode='normal';check('Unsaved complete reports export with an explicit warning.');
  await prepare();lookupFail=true;await page.locator('[data-aiw-generate]').click();await text('[data-aiw-notice]','报告已保存，历史刷新失败');assert.equal(await page.locator('[data-aiw-badge]').textContent(),'已保存');lookupFail=false;check('History refresh failure cannot turn confirmed generation into failure.');
  await page.locator('[data-aiw-view=history]').click();await page.locator('[data-aiw-history-refresh]').click();await page.locator('.aiw-history-open').first().click();await text('[data-aiw-badge]','历史报告');await page.locator('[data-aiw-view=history]').click();mode='invalid';await page.locator('.aiw-history-delete').first().click();await page.locator('[data-aiw-delete-confirm]').click();await text('[data-aiw-notice]','删除结果未确认');const count=await page.locator('.aiw-history-row').count();mode='normal';await page.locator('.aiw-history-delete').first().click();await page.locator('[data-aiw-delete-confirm]').click();await text('[data-aiw-notice]','报告已删除');assert.equal(await page.locator('.aiw-history-row').count(),count-1);check('History detail and deletion require valid matching receipts.');
  await workbench();await page.locator('[data-task=summary]').click();await page.locator('[data-aiw-material]').fill('隔离验收材料');await prepare();assert.ok((await page.locator('[data-aiw-source-json]').textContent()).includes('隔离验收材料'));check('Summary preview matches explicitly submitted material.');
  mode='hold-stream';await page.locator('[data-aiw-generate]').click();await until(()=>held);await page.locator('[data-aiw-stop]').click();await text('[data-aiw-badge]','未完成');await held().catch(()=>{});held=null;mode='normal';assert.equal(await page.locator('[data-aiw-document]').textContent(),'');check('Cancelled generation cannot accept a late completion.');
  await prepare();mode='hold-stream';await page.locator('[data-aiw-generate]').click();await until(()=>held);await retire();await held().catch(()=>{});held=null;mode='normal';await text('[data-aiw-notice]','页面已变化');assert.equal(await page.locator('[data-aiw-document]').textContent(),'');assert.equal(await page.locator('[data-aiw-source-json]').textContent(),'');assert.equal(await page.locator('.aiw-history-row').count(),0);check('Retired workbench clears reports, source snapshots and late streams.');
  mode='readonly';const writesBefore=writes.length;await open('/admin/ai/');await text('[data-aiw-meta]','个文本接口');await submit('[data-aiw-form]');await open('/admin/settings/#site-settings-ai');await text('[data-ai-config-status]','接口已保存');await submit('[data-ai-config-form]');assert.equal(await page.locator('[data-ai-config-save]').isDisabled(),true);assert.equal(writes.length,writesBefore);mode='normal';check('Read-only pages prevent generation and settings mutations.');
  loaderMode='fail';await open('/admin/content/new/');assert.equal(textLoads,0);assert.equal(imageLoads,0);await page.locator('[data-ai-open]').first().click();await text('.ai-load-status','再次点击重试');await page.locator('[data-ai-open]').first().click();await page.waitForFunction(()=>window.fixtureTextOpened===1);await page.locator('[data-ai-image-open=cover]').click();await page.waitForFunction(()=>window.fixtureImageOpened===1);assert.equal(textLoads,2);assert.equal(imageLoads,1);assert.equal(styleLoads,2);await retire();await page.locator('[data-ai-open]').first().click();assert.equal(await page.evaluate(()=>window.fixtureTextOpened),1);loaderMode='';check('AI assets load only on explicit launch, retry failures and block retired click replay.');
  for(const [style,scheme,width] of [['classic','light',1365],['liquid-glass','dark',390]]){
   await context.request.post(fixture.url+'/fixture/style/'+style);await page.setViewportSize({width,height:960});await page.emulateMedia({colorScheme:scheme});
   for(const [key,path] of [['settings','/admin/settings/#site-settings-ai'],['usage','/admin/analytics/ai/'],['workbench','/admin/ai/']]){await open(path);if(key==='settings')await text('[data-ai-config-status]','接口已保存');if(key==='workbench')await text('[data-aiw-meta]','个文本接口');if(key==='usage')await page.waitForFunction(()=>document.querySelectorAll('[data-aiu-rows] tr').length>0);await page.evaluate(()=>document.fonts.ready);assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+2),true,key+' overflow');await page.screenshot({path:resolve(directory,`${name}-${variant}-${key}-${style}.png`),fullPage:true,animations:'disabled'});}
  }check('AI pages fit desktop and narrow screens in both visual styles.');
  await page.goto(fixture.url+'/fixture/login/reader');for(const path of ['/api/v1/admin/ai/settings','/api/v1/admin/ai/usage','/api/v1/admin/ai/workbench'])assert.equal((await context.request.get(fixture.url+path)).status(),403);check('Reader accounts cannot read administrator AI reports or settings.');
  assert.deepEqual(errors,[]);assert.equal(scripts.some(path=>/analytics-react|echarts/i.test(path)),false);check('Page scripts complete without console errors or React/ECharts dependencies.');
 }catch(error){console.log(await page.locator('[data-ai-config-status],[data-aiu-notice],[data-aiw-notice],[data-aiw-status]').allTextContents());await page.screenshot({path:resolve(directory,`${name}-${variant}-failure.png`),fullPage:true}).catch(()=>{});await writeFile(resolve(directory,`${name}-${variant}-failure.json`),JSON.stringify({error:error.stack,errors,writes,evidence},null,2));throw new Error(`${name} ${variant}: ${error.message}; page errors=${JSON.stringify(errors)}`,{cause:error});}
 finally{await browser.close();}
}
await writeFile(resolve(directory,`${variant}-summary.json`),JSON.stringify({variant,checks:evidence.length,evidence},null,2));
