// Real Go pages/read APIs; all business writes and external traffic are intercepted.
import assert from 'node:assert/strict';
import {readFile,writeFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import {build} from 'esbuild';
import {chromium,firefox,webkit} from 'playwright';
const directory=resolve(process.argv[2]),fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
const engines={chromium,firefox,webkit},names=process.argv[3]&&process.argv[3]!=='all'?[process.argv[3]]:Object.keys(engines),variant=process.argv[4]==='generated'?'generated':'typescript',assets=new Map(),evidence=[];
if(variant==='typescript')for(const name of ['wechat','wechat-drafts'])assets.set('chunnel-'+name+'.js',(await build({entryPoints:['frontend/core/'+name+'.ts'],bundle:true,write:false,format:'iife',platform:'browser',target:['chrome100','firefox110','safari16'],charset:'utf8'})).outputFiles[0].text);
const clone=value=>structuredClone(value);
for(const name of names){
 const browser=await engines[name].launch(),context=await browser.newContext({viewport:{width:1440,height:1000},reducedMotion:'reduce'}),page=await context.newPage();page.setDefaultTimeout(12000);
 let mode='',held=null,config=clone(fixture.workspace.config),jobs=clone(fixture.workspace.jobs),badRead=false;
 const writes=[],errors=[],scripts=[],check=text=>{evidence.push({engine:name,text});console.log(name+': '+text);};
 const json=(route,value,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(value)});
 const text=(selector,value)=>page.waitForFunction(({selector,value})=>document.querySelector(selector)?.textContent?.includes(value),{selector,value});
 const status=async value=>{await text('[data-wechat-status]',value);await page.waitForFunction(()=>document.querySelector('[data-wechat]')?.getAttribute('aria-busy')!=='true');},tab=name=>page.locator(`[data-wechat-tab=${name}]`).click(),field=(form,name)=>page.locator(`[data-wechat-${form}] [name=${name}]`);
 const submit=selector=>page.locator(selector).evaluate(form=>form.requestSubmit());
 const wait=async predicate=>{const end=Date.now()+12000;while(!predicate()){if(Date.now()>end)throw new Error('Missing intercepted request');await new Promise(resolve=>setTimeout(resolve,20));}};
 const release=async()=>{await wait(()=>held);const deliver=held;held=null;await deliver().catch(()=>{});};
 const preview=()=>{const result=clone(fixture.preview);result.expires=new Date(Date.now()+15*60000).toISOString();return result;};
 page.on('pageerror',error=>{errors.push(error.message);console.error('PAGE ERROR',page.url(),error.stack);});page.on('request',request=>{if(request.resourceType()==='script')scripts.push(request.url());});page.on('dialog',dialog=>dialog.accept());
 await context.route('**/*',async route=>{
  const req=route.request(),url=new URL(req.url()),path=url.pathname,file=path.split('/').at(-1);
  if(url.origin!==fixture.url)return route.abort();
  if(assets.has(file))return route.fulfill({contentType:'text/javascript',body:assets.get(file)});
  if(path.startsWith('/fixture/'))return route.continue();
  if(req.method()==='GET'||req.method()==='HEAD'){
   if(path==='/api/v1/admin/session'&&mode==='foreign-session'){const response=await route.fetch(),data=await response.json();return json(route,{...data,user_id:999});}
   if(path==='/api/v1/admin/wechat'){
    if(badRead)return json(route,{error:{message:'工作区暂不可用'}},503);
    const response=await route.fetch();if(response.status()!==200)return route.fulfill({response});const data=await response.json();
    data.config=clone(config);data.jobs=clone(jobs);data.total_jobs=jobs.length;data.filtered_jobs=jobs.length;data.job_page=1;data.job_pages=1;data.active_jobs=0;data.attention_jobs=1;
    if(mode==='bad-workspace')data.jobs=[{id:'incomplete'}];
    if(mode==='wrong-selection')data.selected_post={...fixture.workspace.selected_post,id:fixture.other_post_id};
    if(mode==='hold-read'){held=()=>json(route,data);return;}return json(route,data);
   }
   return route.continue();
  }
  const body=req.headers()['content-type']?.includes('json')?req.postDataJSON():{};writes.push({path,body});
  if(path==='/api/v1/admin/wechat/config'){
   const next={...body.config,version:body.config.version+1,since:config.since,has_secret:!body.clear_secret&&(!!body.config.app_secret||body.config.app_id===config.app_id&&!!config.has_secret)};delete next.app_secret;
   if(mode==='wrong-config')return json(route,{config:{...next,has_secret:!next.has_secret}});
   config=clone(next);const deliver=()=>json(route,{config:next});if(mode==='hold-config'){held=deliver;return;}return deliver();
  }
  if(path==='/api/v1/admin/wechat/test'){const deliver=()=>json(route,{message:'已成功获取接口凭证。'});if(mode==='hold-connection'){held=deliver;return;}return deliver();}
  if(path==='/api/v1/admin/wechat/sync/check'){
   const result={...clone(fixture.check.check),...body};if(mode==='bad-check')result.checked_images=result.images+1;
   const deliver=()=>json(route,{check:result});if(mode==='hold-check'){held=deliver;return;}return deliver();
  }
  if(path==='/api/v1/admin/wechat/sync'){
   const job={...clone(fixture.queued_job),post_id:mode==='wrong-sync'?999:body.post_id,version:body.version};if(mode==='sync-read-fail')badRead=true;return json(route,{job},202);
  }
  if(path.startsWith('/api/v1/admin/wechat/jobs/')){
   if(mode==='bad-job')return json(route,{ok:'true'});
   if(mode==='job-read-fail')badRead=true;return json(route,{ok:true});
  }
  if(path==='/api/v1/admin/wechat/collect/preview'){
   let result=preview();
   if(mode==='retry-link'){result.articles=[{...result.articles[0],key:'retry-article',title:'重试得到的文章',url:body.urls[0]}];result.failures=[];result.receipts={'retry-article':'fixture-retry'};result.plan={items:[{key:'retry-article',action:'create'}],new:1,existing:0,conflicts:0};}
   if(mode==='changed-preview')result.articles[0].excerpt='来源摘要已更新';
   if(mode==='bad-preview')result.plan.new++;
   if(mode==='expired-preview')result.expires=new Date(Date.now()-1000).toISOString();
   if(mode==='oversized-preview'){for(let i=0;i<2;i++)result.articles[i].images=Array.from({length:26},(_,n)=>`https://mmbiz.qpic.cn/image-${i*26+n}`);}
   const deliver=()=>json(route,result);if(mode==='hold-preview'){held=deliver;return;}return deliver();
  }
  if(path==='/api/v1/admin/wechat/collect/apply'){
   if(mode==='lost-import')return route.abort();
   const items=body.articles.map(article=>({...fixture.applied.plan.items.find(row=>row.key===article.key),key:article.key,title:article.title,post_id:fixture.applied.plan.items.find(row=>row.key===article.key)?.post_id||100,action:'created'}));
   const result={applied:true,plan:{items,new:items.length,existing:0,conflicts:0}};
   if(mode==='bad-import')result.plan.items[0].key='foreign-result';if(mode==='import-read-fail')badRead=true;return json(route,result);
  }
  return json(route,{error:{message:'隔离验证未允许此业务写入'}},409);
 });
 const fresh=async()=>{mode='';badRead=false;await page.goto(fixture.login);await status('工作区已就绪');};
 const collect=async()=>{await tab('collect');await field('collect','urls').fill('分享第一篇 https://mp.weixin.qq.com/s/firstx?from=1\nhttps://mp.weixin.qq.com/s/second\nhttps://mp.weixin.qq.com/s/failed');await submit('[data-wechat-collect]');await status('预览已生成');};
 const confirm=async()=>{await page.locator('[data-wechat-confirm]').waitFor({state:'visible'});await page.locator('[data-wechat-confirm] button[value=confirm]').click();};
 try{
  await fresh();assert.equal(await field('sync','post_id').inputValue(),String(fixture.post_id));check('Actual Go workspace opens with the requested saved article and typed redacted config.');
  await tab('config');await field('config','author').fill('尚未保存');await tab('collect');await tab('config');assert.equal(await field('config','author').inputValue(),'尚未保存');check('Switching workspace tabs retains pending settings.');
  mode='bad-workspace';await tab('sync');await page.locator('[data-wechat-refresh]').click();await status('工作区响应不完整');await tab('config');assert.equal(await field('config','author').inputValue(),'尚未保存');mode='';check('Malformed jobs cannot replace the prior workspace or pending draft.');
  await tab('sync');mode='wrong-selection';await page.locator('[data-wechat-refresh]').click();await status('返回文章与当前选择不一致');mode='';check('A read response cannot substitute a different selected article.');
  mode='hold-read';await page.locator('[data-wechat-refresh]').click();await wait(()=>held);await field('sync','post_id').selectOption(String(fixture.other_post_id));mode='';await release();await status('工作区已就绪');assert.equal(await field('sync','post_id').inputValue(),String(fixture.other_post_id));check('Changing article during a refresh triggers a fresh read for the final selection.');
  await fresh();await page.locator('[data-wechat-check]').click();await status('本地检查通过');assert.equal(await page.locator('[data-wechat-check-result]').getAttribute('data-state'),'ready');check('Local check displays actual Go metrics for the saved article and config version.');
  mode='bad-check';await page.locator('[data-wechat-check]').click();await status('检查结果不完整');assert.equal(await page.locator('[data-wechat-check-result]').getAttribute('data-state'),'stale');mode='';check('Invalid check metrics preserve the prior result and mark it stale.');
  mode='hold-check';await page.locator('[data-wechat-check]').click();await wait(()=>held);await page.locator('[data-wechat-stop-read]').click();mode='';await release();await status('已停止检查');check('Stopping a check ignores the late response and unlocks the page.');
  mode='wrong-sync';await submit('[data-wechat-sync]');await status('同步回执与当前文章');mode='';check('Synchronization rejects a receipt for another article.');
  mode='sync-read-fail';await submit('[data-wechat-sync]');await status('同步回执已确认');badRead=false;mode='';check('A confirmed synchronization remains successful when the following record refresh fails.');
  await fresh();await page.locator('[data-wechat-jobs] button[data-action=publish]').click();await page.locator('[data-wechat-confirm]').waitFor();const beforeCancel=writes.length;await page.locator('[data-wechat-confirm] button[value=cancel]').click();assert.equal(writes.length,beforeCancel);check('Publication requires explicit confirmation and cancel sends no mutation.');
  mode='bad-job';await page.locator('[data-wechat-jobs] button[data-action=publish]').click();await confirm();await status('任务回执未确认');mode='';check('Job actions require an explicit boolean success acknowledgement.');
  await page.locator('[data-wechat-jobs] button[data-action=resolve]').click();await page.locator('[data-wechat-confirm] input').fill('verified-publish-id');await confirm();await status('任务状态已更新');assert.equal(writes.at(-1).body.publish_id,'verified-publish-id');check('Uncertain publication uses the verified remote identifier and original task version.');
  await tab('config');await field('config','author').fill('本次保存');mode='hold-config';await submit('[data-wechat-config]');await wait(()=>held);
  await field('config','author').evaluate(node=>{node.value='保存中的新输入';node.dispatchEvent(new Event('input',{bubbles:true}));});await field('config','app_secret').evaluate(node=>{node.value='later-private-secret';node.dispatchEvent(new Event('input',{bubbles:true}));});mode='';await release();await status('后续修改仍待保存');assert.equal(await field('config','author').inputValue(),'保存中的新输入');assert.equal(await field('config','app_secret').inputValue(),'later-private-secret');check('Confirmed settings merge preserves later author edits and a newly entered secret.');
  mode='wrong-config';await submit('[data-wechat-config]');await status('保存回执不完整');assert.equal(await field('config','app_secret').inputValue(),'later-private-secret');mode='';check('A mismatched secret acknowledgement cannot clear the pending key.');
  mode='hold-connection';await page.locator('[data-wechat-test]').click();await wait(()=>held);await page.locator('[data-wechat-stop-read]').click();mode='';await release();await status('已停止连接测试');check('Connection test cancellation retains the settings draft.');
  await fresh();await collect();assert.equal(await page.locator('[data-wechat-collection] input[type=checkbox]:checked').count(),2);assert.equal(await page.locator('[data-wechat-collection] [data-wechat-retry]').count(),1);check('A partial batch shows valid article choices together with per-link failures.');
  await page.locator('[data-wechat-collection] summary').first().click();assert.match(await page.locator('.wechat-prose').first().innerText(),/可核对/);assert.equal(await page.locator('.wechat-prose img').count(),0);check('Article prose mounts on expansion with placeholders instead of remote image requests.');
  await field('collect','urls').fill('https://mp.weixin.qq.com/s/changed');assert.equal(await page.locator('[data-wechat-apply]').isDisabled(),true);await text('[data-wechat-preview-state]','链接已修改');check('Editing links invalidates import while retaining the previous preview.');
  await collect();mode='bad-preview';await submit('[data-wechat-collect]');await status('预览响应不完整');assert.equal(await page.locator('[data-wechat-collection] input[type=checkbox]:checked').count(),2);mode='';check('Inconsistent preview counts leave the existing selection intact.');
  mode='changed-preview';await submit('[data-wechat-collect]');await status('预览已生成');assert.equal(await page.locator('[data-wechat-collection] input[type=checkbox]:checked').count(),1);mode='';check('Changed source content invalidates only its prior selection.');
  await fresh();await collect();await page.locator('[data-wechat-collection] input[type=checkbox]').nth(1).uncheck();mode='retry-link';await page.locator('[data-wechat-retry]').click();await status('重试结果已加入预览');assert.equal(await page.locator('[data-wechat-collection] input[type=checkbox]').count(),3);assert.equal(await page.locator('[data-wechat-collection] input[type=checkbox]:checked').count(),2);mode='';check('Retrying one failed link preserves earlier unchecked choices.');
  await fresh();mode='oversized-preview';await collect();await text('[data-wechat-image-count]','52 / 50');assert.equal(await page.locator('[data-wechat-apply]').isDisabled(),true);await page.locator('[data-wechat-collection] input[type=checkbox]').nth(1).uncheck();await text('[data-wechat-image-count]','26 / 50');assert.equal(await page.locator('[data-wechat-apply]').isDisabled(),false);mode='';check('The distinct-image budget blocks oversized imports and allows explicit smaller batches.');
  await fresh();mode='expired-preview';await collect();assert.equal(await page.locator('[data-wechat-apply]').isDisabled(),true);await text('[data-wechat-apply-note]','预览已过期');mode='';check('Expired receipts cannot authorize an import.');
  await fresh();await collect();mode='bad-import';await page.locator('[data-wechat-apply]').click();await status('导入回执不完整');assert.equal(await page.locator('[data-wechat-collection] input[type=checkbox]:checked').count(),2);mode='';check('Foreign import receipts preserve every pending article choice.');
  mode='lost-import';await page.locator('[data-wechat-apply]').click();await status('请求未能完成');assert.equal(await page.locator('[data-wechat-collection] input[type=checkbox]:checked').count(),2);mode='';check('A lost import response retains the preview for explicit reconciliation.');
  await page.locator('[data-wechat-collection] input[type=checkbox]').nth(1).uncheck();mode='import-read-fail';await page.locator('[data-wechat-apply]').click();await status('所选文章处理完成');await status('最新工作区暂未刷新');assert.equal(await page.locator('[data-wechat-collection] input[type=checkbox]').count(),1);assert.equal(await page.locator('[data-wechat-import-results] a').count(),1);badRead=false;mode='';check('Confirmed selective import retains unselected articles and success after readback failure.');
  await fresh();await collect();mode='hold-preview';await submit('[data-wechat-collect]');await wait(()=>held);await page.locator('[data-wechat-stop-read]').click();mode='';await release();await status('已停止预览');assert.equal(await page.locator('[data-wechat-collection] input[type=checkbox]:checked').count(),2);check('Cancelling preview keeps its original selection and discards late completion.');
  await page.locator('label').filter({has:page.locator('[data-wechat-draft-enable]')}).click();assert.equal(await page.locator('[data-wechat-draft-enable]').isChecked(),true);await text('[data-wechat-draft-status]','采集进度已暂存');await page.waitForFunction(id=>JSON.parse(sessionStorage.getItem('newpress.wechat.collect.'+id)||'{}').choices?.length===2,fixture.owner_id);
  const stored=await page.evaluate(id=>sessionStorage.getItem('newpress.wechat.collect.'+id),fixture.owner_id);assert.doesNotMatch(stored,/app_secret|receipts|可核对的采集正文/);check('Opt-in tab storage contains links and fingerprints without article body or credentials.');
  await page.reload();await status('工作区已就绪');await tab('collect');const beforeRestore=writes.length;await page.locator('[data-wechat-draft-restore]').click();await status('已恢复链接');assert.equal(writes.length,beforeRestore);await submit('[data-wechat-collect]');await status('预览已生成');assert.equal(await page.locator('[data-wechat-collection] input[type=checkbox]:checked').count(),2);check('Restoring a tab draft performs no import and revalidates fingerprints on explicit preview.');
  mode='foreign-session';await page.locator('[data-wechat-apply]').click();await status('当前账号已切换');assert.equal(await page.locator('[data-wechat-apply]').isDisabled(),true);mode='';await page.locator('[data-wechat-recover]').click();await status('登录已确认');assert.equal(await page.locator('[data-wechat-collection] input[type=checkbox]:checked').count(),2);check('A different account cannot submit retained work; returning to the original account requires manual recovery.');
  mode='hold-preview';await submit('[data-wechat-collect]');await wait(()=>held);await page.evaluate(()=>document.dispatchEvent(new CustomEvent('chunnel:session',{detail:{authenticated:true,id:999,csrf_token:'foreign'}})));mode='';await release();await status('登录状态已变化');assert.equal(await page.locator('[data-wechat-apply]').isDisabled(),true);check('Session events cancel active work and prevent a late preview from re-enabling import.');
  await page.locator('[data-wechat-recover]').click();await status('登录已确认');await page.locator('[data-wechat-draft-clear]').click();await confirm();await text('[data-wechat-draft-status]','已停止暂存');assert.equal(await page.evaluate(id=>sessionStorage.getItem('newpress.wechat.collect.'+id),fixture.owner_id),null);check('Explicitly clearing the tab draft removes recovery data while keeping current inputs.');
  await page.setViewportSize({width:390,height:844});await tab('config');for(const color of ['light','dark']){await page.evaluate(color=>{window.ChunnelColorMode?.set(color);document.documentElement.dataset.colorMode=color;},color);await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));await page.waitForFunction(()=>!document.getAnimations().some(animation=>animation.constructor.name==='CSSTransition'&&animation.playState==='running'));await page.screenshot({path:resolve(directory,`${variant}-${name}-wechat-${color}.png`),fullPage:true});assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));}check('Narrow settings layout fits the viewport in light and dark appearance.');
  await page.goto(fixture.url+'/fixture/login/reader');const denied=await context.request.get(fixture.url+'/api/v1/admin/wechat');assert.equal(denied.status(),403);check('Reader account cannot read private WeChat workspace configuration.');
  assert.deepEqual(errors,[]);assert(!scripts.some(url=>/analytics-react|echarts|react-dom/.test(url)));check('All flows finish without page errors or React/ECharts dependencies.');
  await writeFile(resolve(directory,`${variant}-${name}-writes.json`),JSON.stringify(writes,null,2)+'\n');
 }catch(error){await page.screenshot({path:resolve(directory,`${variant}-${name}-failure.png`),fullPage:true}).catch(()=>{});await writeFile(resolve(directory,`${variant}-${name}-failure.txt`),await page.locator('body').innerText().catch(()=>''));throw error;}finally{await browser.close();}
}
await writeFile(resolve(directory,variant+'-summary.json'),JSON.stringify({variant,checks:evidence.length,evidence},null,2)+'\n');console.log(`${variant}: ${evidence.length} WeChat browser checks passed.`);
