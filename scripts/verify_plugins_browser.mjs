// Disposable Go pages and read APIs; intercept every business write and external request.
import assert from 'node:assert/strict';
import {readFile,writeFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import {build} from 'esbuild';
import {chromium,firefox,webkit} from 'playwright';
const directory=resolve(process.argv[2]),fixture=JSON.parse(await readFile(resolve(directory,'fixture.json'),'utf8'));
const engines={chromium,firefox,webkit},names=process.argv[3]&&process.argv[3]!=='all'?[process.argv[3]]:Object.keys(engines),variant=process.argv[4]==='generated'?'generated':'typescript',assets=new Map(),evidence=[];
const outputs={'plugins':'chunnel-plugins','plugin-runtime':'chunnel-plugin-runtime','plugin-records':'newpress-plugin-records','plugin-webhooks':'newpress-plugin-webhooks'};
if(variant==='typescript')for(const [entry,out] of Object.entries(outputs))assets.set(out+'.js',(await build({entryPoints:['frontend/core/'+entry+'.ts'],bundle:true,write:false,format:'iife',platform:'browser',target:['chrome100','firefox110','safari16'],charset:'utf8'})).outputFiles[0].text);
for(const name of names){
 const browser=await engines[name].launch(),context=await browser.newContext({viewport:{width:1440,height:1000},reducedMotion:'reduce'}),page=await context.newPage();page.setDefaultTimeout(12000);
 let mode='',held=null,badTasks=false,badMarket=false,badHooks=false;const writes=[],errors=[],scripts=[],reads=[];
 const check=text=>{evidence.push({engine:name,text});console.log(name+': '+text);};
 const headers={'X-NewPress-Review-User':String(fixture.owner_id)},json=(route,data,status=200)=>route.fulfill({status,headers,contentType:'application/json',body:JSON.stringify(data)});
 const has=(selector,text)=>page.waitForFunction(({selector,text})=>document.querySelector(selector)?.textContent?.includes(text),{selector,text});
 const submit=selector=>page.locator(selector).evaluate(form=>form.requestSubmit());
 const wait=async predicate=>{const end=Date.now()+12000;while(!predicate()){if(Date.now()>end)throw new Error('Missing intercepted request');await new Promise(resolve=>setTimeout(resolve,20));}};
 const release=async()=>{await wait(()=>held);const deliver=held;held=null;await deliver().catch(()=>{});};
 const activeTasks=()=>page.waitForFunction(()=>document.querySelector('[data-plugin-tasks]')?.getAttribute('aria-busy')!=='true');
 const section=async id=>{const target=page.locator(`[data-settings-target="${id}"]`);if(await target.count())await target.click();};
 const changeCheck=(selector,value)=>page.locator(selector).evaluate((el,value)=>{el.checked=value;el.dispatchEvent(new Event('input',{bubbles:true}));el.dispatchEvent(new Event('change',{bubbles:true}));},value);
 const replace=selector=>page.evaluate(selector=>document.dispatchEvent(new CustomEvent('chunnel:admin-before-replace',{detail:{regions:[document.querySelector(selector)]}})),selector);
 const session=()=>page.evaluate(()=>document.dispatchEvent(new CustomEvent('chunnel:session')));
 const snapshot=async label=>{
  await writeFile(resolve(directory,variant+'-'+name+'-'+label+'.aria.txt'),await page.locator('body').ariaSnapshot());
  const expected={market:'chunnel-plugins',origin:'chunnel-plugins',runtime:'chunnel-plugin-runtime',records:'newpress-plugin-records',webhooks:'newpress-plugin-webhooks'}[label];
  for(const output of Object.values(outputs))assert.equal(await page.locator(`script[src*="/${output}.js"]`).count(),output===expected?1:0);
  check(label+' loads only its own plugin entry.');
 };
 const visual=async label=>{await page.setViewportSize({width:390,height:844});for(const theme of ['light','dark']){await page.evaluate(theme=>document.documentElement.dataset.theme=theme,theme);await page.waitForTimeout(400);assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'narrow page overflow: '+label);await page.screenshot({path:resolve(directory,variant+'-'+name+'-'+label+'-'+theme+'.png'),fullPage:true});}await page.setViewportSize({width:1440,height:1000});check(label+' narrow light/dark layouts fit the viewport.');};
 page.on('pageerror',error=>{errors.push(error.message);console.error('PAGE ERROR',page.url(),error.stack);});page.on('dialog',dialog=>dialog.accept());page.on('request',r=>{if(r.resourceType()==='script')scripts.push(r.url());});
 await context.route('**/*',async route=>{
  const request=route.request(),url=new URL(request.url()),path=url.pathname,file=path.split('/').at(-1),method=request.method();
  if(url.origin!==fixture.url)return route.abort();
  if(assets.has(file))return route.fulfill({contentType:'text/javascript',body:assets.get(file)});
  if(method==='GET'||method==='HEAD'){
   if(path==='/admin/plugins/'&&badMarket&&request.resourceType()!=='document')return route.fulfill({status:503,body:'fixture read unavailable'});
   if(path==='/api/v1/admin/plugins/task-fixture/tasks'){
    if(badTasks)return json(route,{error:{message:'任务读取失败'}},503);
    const response=await route.fetch();reads.push({path,status:response.status()});const data=await response.json();
    if(mode==='bad-tasks'&&data.items?.length)data.items.push(data.items[0]);
    if(mode==='hold-tasks'){held=()=>json(route,data);return;}return json(route,data,response.status());
   }
   if(path.startsWith('/api/v1/admin/plugins/comment-origin/records/'))return json(route,{id:mode==='wrong-record'?999:Number(path.split('/').at(-1)),status:'resolved',source_label:'验证夹具',ip:'203.0.113.8',ua:'private fixture UA',location:{country:'中国',region:'浙江',city:'杭州',precision:'city',provider:'fixture'}});
   if(path==='/api/v1/admin/plugin-webhooks'&&badHooks)return json(route,{error:{message:'fixture read unavailable'}},503);
   if(path.includes('/plugin-shared-')||path==='/api/v1/admin/plugin-webhooks'||path==='/api/v1/admin/plugin-webhook-session'){
    const response=await route.fetch();reads.push({path,status:response.status()});
    if(mode==='foreign-header')return route.fulfill({response,headers:{...response.headers(),'X-NewPress-Review-User':'999'}});
    if(mode==='hold-records'&&path==='/api/v1/admin/plugin-shared-records'){const body=await response.body();held=()=>route.fulfill({response,body});return;}
    if(mode==='hold-hooks'&&path==='/api/v1/admin/plugin-webhooks'){const body=await response.body();held=()=>route.fulfill({response,body});return;}
    return route.fulfill({response});
   }
   return route.continue();
  }
  const body=request.postDataJSON();writes.push({path,body});assert.equal(String(body.expected_user_id),String(fixture.owner_id));assert(request.headers()['x-csrf-token']);
  if(path==='/api/v1/admin/plugins'){
   if(mode==='bad-action')return json(route,{ok:'true'});
   if(mode==='market-read-fail')badMarket=true;
   if(mode==='hold-action'){held=()=>json(route,{ok:true});return;}return json(route,{ok:true});
  }
  if(path==='/api/v1/admin/plugins/comment-origin/settings'){
   const settings={...body.settings,version:body.settings.version+1};delete settings.key;
   if(mode==='wrong-origin')settings.rules=settings.rules.map((r,i)=>i===0?{...r,location:!r.location}:r);
   const result={...fixture.origin_receipt,settings,key_saved:!body.clear_key&&!!body.settings.key};
   if(mode==='hold-origin'){held=()=>json(route,result);return;}return json(route,result);
  }
  if(path==='/api/v1/admin/plugins/comment-origin/test')return json(route,{country:'中国',region:'浙江',city:'杭州',precision:'city',provider:'fixture'});
  if(path==='/api/v1/admin/plugins/task-fixture/configuration'){
   const result={...fixture.configuration_receipt,version:body.configuration.version+(mode==='bad-config'?0:1)};
   if(mode==='hold-config'){held=()=>json(route,result);return;}return json(route,result);
  }
  if(path.endsWith('/reissues')){if(mode==='lost-reissue')return route.abort();return json(route,fixture.reissue,202);}
  if(path==='/api/v1/admin/plugins/task-fixture/tasks'){if(mode==='lost-enqueue')return route.abort();return json(route,fixture.accepted,202);}
  if(path.endsWith('/archive'))return json(route,{tasks:path.includes('/fanouts/')?0:1,fanouts:path.includes('/fanouts/')?1:0});
  if(path.startsWith('/api/v1/admin/plugins/task-fixture/tasks/')){if(mode==='task-read-fail')badTasks=true;return json(route,{ok:true});}
  if(path.startsWith('/api/v1/admin/plugin-webhooks/')&&path.endsWith('/review')){
   if(mode==='bad-review')return json(route,{recorded:'true'});if(mode==='review-read-fail')badHooks=true;return json(route,{recorded:true});
  }
  throw new Error('Unexpected mutation '+path);
 });
 const fresh=async path=>{mode='';badTasks=badMarket=badHooks=false;await page.goto(fixture.url+path);};
 const runtime=async()=>{await fresh('/admin/plugins/task-fixture/');await section('plugin-settings-tasks');};
 const records=async()=>{await fresh('/admin/plugin-records/');await has('[data-review-status]','已读取');};
 const hooks=async()=>{await fresh('/admin/plugin-webhooks/');await has('[data-webhook-status]','已读取');};
 const reviewHook=async()=>{const row=page.locator('[data-webhook-list]>li').first();await row.locator('details').last().locator('summary').click();await row.locator('[name=decision]').selectOption('received');await row.locator('[name=reason]').fill('已核对接收方记录');await row.locator('input[type=checkbox]').check();await row.locator('form').evaluate(f=>f.requestSubmit());};
 try{
  await page.goto(fixture.login);await page.waitForFunction(()=>document.querySelector('[data-plugin-workspace]')?.dataset.marketLoading!=='true');await snapshot('market');
  assert(await page.locator('[data-plugin-card]').count()>=2);check('Actual Go marketplace renders installed and available plugin cards.');
  await page.locator('[data-plugin-search]').fill('不存在的插件');await page.locator('[data-plugin-empty]').waitFor({state:'visible'});await page.locator('[data-plugin-clear]').click();check('Market search and clear restore the visible result count.');
  await page.locator('[data-plugin-status-filter]').selectOption('available');assert.equal(await page.locator('[data-plugin-card][data-installed=true]:visible').count(),0);await page.locator('[data-plugin-status-filter]').selectOption('all');check('Installed-state filtering hides incompatible cards.');
  let card=page.locator('[data-plugin-card="comment-origin"]');await card.locator('[data-plugin-details-open]').first().click();await card.locator('dialog').waitFor({state:'visible'});await page.keyboard.press('Escape');assert(await card.locator('[data-plugin-details-open]').first().evaluate(el=>el===document.activeElement));check('Plugin drawer supports Escape and restores opener focus.');
  await card.locator('[data-plugin-details-open]').first().click();await card.locator('[data-plugin-remove]').click();const beforeCancel=writes.length;await page.locator('[data-plugin-uninstall] button[value=cancel]').last().click();assert.equal(writes.length,beforeCancel);await page.keyboard.press('Escape');check('Cancelling plugin uninstall sends no mutation.');
  card=page.locator('[data-plugin-card][data-installed=false]').first();mode='bad-action';await card.locator('[data-plugin-action=install]').first().click();await has('[data-plugin-status]','未收到完成确认');check('Malformed action acknowledgment stays unconfirmed.');
  mode='market-read-fail';await card.locator('[data-plugin-action=install]').first().click();await has('[data-plugin-status]','插件操作已完成');check('Confirmed installation remains confirmed when market refresh fails.');await visual('market');
  await fresh('/admin/plugins/');await page.waitForFunction(()=>document.querySelector('[data-plugin-workspace]')?.dataset.marketLoading!=='true');mode='hold-action';await page.locator('[data-plugin-card][data-installed=false] [data-plugin-action=install]').first().click();await wait(()=>held);await replace('[data-plugin-workspace]');await release();assert.equal(await page.locator('[data-plugin-status]').innerText(),'');check('Retired market cannot apply a late action response.');
  await fresh('/admin/plugins/comment-origin/');await snapshot('origin');await section('origin-provider');await page.locator('[name=provider]').selectOption('amap');await page.locator('[name=key]').fill('submitted-test-key');mode='hold-origin';await submit('[data-origin-settings]');await wait(()=>held);await page.locator('[name=key]').fill('newer-test-key');await release();await has('[data-save-state]','未保存');assert.equal(await page.locator('[name=key]').inputValue(),'newer-test-key');check('Origin save keeps a secret entered during the pending acknowledgment dirty.');
  mode='wrong-origin';await submit('[data-origin-settings]');await has('[data-save-state]','角色设置不一致');assert.equal(await page.locator('[name=key]').inputValue(),'newer-test-key');check('Origin role mismatch cannot accept or erase the pending secret.');
  await section('origin-diagnostics');mode='';await page.locator('[data-origin-test] [name=ip]').fill('8.8.8.8');await submit('[data-origin-test]');await has('[data-test-result]','杭州');check('Origin diagnostics render the bounded location projection.');await visual('origin');
  await page.locator('[name=comment_id]').fill('7');mode='wrong-record';await submit('[data-origin-record]');await has('[data-record-result]','编号不匹配');assert(!await page.locator('[data-record-result]').innerText().then(t=>t.includes('private fixture UA')));mode='';await submit('[data-origin-record]');await has('[data-record-result]','private fixture UA');await session();assert.equal(await page.locator('[data-record-result]').innerText(),'');check('Private comment records require matching IDs and clear on session change.');
  await runtime();await snapshot('runtime');await page.locator('[data-task-refresh]').click();await has('[data-task-status]','共 3 条');await activeTasks();check('Runtime lists the real Go task queue and provenance.');
  await page.locator('[data-task-filter]').selectOption('cancelled');await has('[data-task-status]','共 1 条');await activeTasks();await page.locator('[data-task-filter]').selectOption('');await activeTasks();await page.locator('[data-task-scope]').selectOption('archived');await has('[data-task-status]','共 0 条');await activeTasks();await page.locator('[data-task-scope]').selectOption('live');await activeTasks();check('Runtime status and archived-history filters match the Go projections.');
  await section('plugin-settings-permissions');await changeCheck('[data-runtime-grant]',false);mode='hold-config';await submit('[data-runtime-form]');await wait(()=>held);await changeCheck('[data-runtime-grant]',true);await release();await has('[data-save-state]','未保存');check('Runtime save marks only the submitted permissions snapshot as saved.');
  mode='bad-config';await submit('[data-runtime-form]');await has('[data-save-state]','未收到有效保存结果');check('Runtime requires the exact next configuration version.');
  await runtime();await page.locator('[data-task-payload]').fill('{"original":1}');mode='lost-enqueue';await page.locator('[data-task-create]').click();await activeTasks();const original=writes.at(-1);assert.equal(original.path,'/api/v1/admin/plugins/task-fixture/tasks');await page.locator('[data-task-payload]').fill('{"newer":2}');mode='';await page.locator('[data-task-create]').click();await has('[data-task-status]','任务已接受');assert.deepEqual(writes.at(-1),original);check('An ambiguous enqueue replays the original key and payload after local edits.');
  await activeTasks();await page.locator(`[data-task-review-open][data-task-id="${fixture.source_id}"]`).click();await has('[data-task-status]','已读取当前任务');await activeTasks();await page.locator('[data-task-reissue-payload]').fill('{"reviewed":true}');await page.locator('[data-task-reissue-reason]').fill('已核对原任务');await page.locator('[data-task-reissue-confirm]').check();mode='lost-reissue';await page.locator('[data-task-reissue-submit]').click();await activeTasks();const originalReissue=writes.at(-1);await page.locator('[data-task-reissue-payload]').fill('{"newer":true}');mode='';await page.locator('[data-task-reissue-submit]').click();await has('[data-task-status]','重新投递已确认');assert.deepEqual(writes.at(-1),originalReissue);check('Reviewed reissue preserves the original source, owner, key and payload across lost responses.');
  await activeTasks();mode='bad-tasks';await page.locator('[data-task-refresh]').click();await activeTasks();assert(!(await page.locator('[data-task-status]').innerText()).includes('共 '));check('Malformed or duplicated task pages are rejected.');
  mode='task-read-fail';await page.locator('[data-task-action=cancel]').first().click();await has('[data-task-status]','任务操作已确认');check('Confirmed cancellation remains confirmed after a list failure.');
  await runtime();await page.locator('.plugin-task-retention summary').click();await page.locator('[data-task-archive]').click();await has('[data-task-status]','已归档本插件终态任务 1 条');await activeTasks();check('Task archive acknowledgment is validated and followed by a read.');await visual('runtime');
  mode='hold-tasks';await page.locator('[data-task-refresh]').click();await wait(()=>held);await replace('[data-runtime-admin]');await release();assert.equal(await page.locator('[data-task-list]').innerText(),'');check('Runtime replacement clears results and retires an in-flight task read.');
  await records();await snapshot('records');assert((await page.locator('[data-review-list]').innerText()).includes('9007199254740993'));assert.equal(await page.locator('[data-review-list]>li').count(),20);check('Real Go numeric int64 tokens retain exact IDs and versions in the browser.');
  await page.locator('[data-review-next]').click();await has('[data-review-page]','21–23 / 23');assert.equal(await page.locator('[data-review-list]>li').count(),3);check('Shared-record pagination preserves the projected cursor and row count.');
  await page.locator('[data-review-type]').selectOption('authority');assert.equal(await page.locator('[data-review-list]>li').count(),0);await submit('[data-review-form]');await has('[data-review-status]','已读取');assert.equal(await page.locator('[data-review-list]>li').count(),3);check('Changing record type clears old data and reads the selected authority records.');
  await page.locator('[data-review-type]').selectOption('content_effect');await page.locator('[data-review-scope][value=quarantines]').check();await has('[data-review-status]','已读取');assert((await page.locator('[data-review-list]').innerText()).includes('<img src=x onerror=alert(1)>'));assert.equal(await page.locator('[data-review-list] img').count(),0);check('Quarantine audit text is rendered as text, including angle-bracket content.');await visual('records');
  mode='foreign-header';await submit('[data-review-form]');await has('[data-review-status]','账号');assert.equal(await page.locator('[data-review-list]>li').count(),0);check('Mismatched response ownership clears shared records and blocks reuse.');
  await records();mode='hold-records';await submit('[data-review-form]');await wait(()=>held);await replace('[data-shared-review]');await release();assert.equal(await page.locator('[data-review-list]>li').count(),0);check('Shared-record region replacement rejects late data.');
  await hooks();await snapshot('webhooks');assert.equal(await page.locator('[data-webhook-list]>li').count(),20);await page.locator('[data-webhook-next]').click();await has('[data-webhook-page]','2 / 2');assert.equal(await page.locator('[data-webhook-list]>li').count(),3);check('Webhook reads and pagination use the actual Go summary projection.');
  await page.locator('[data-webhook-filters] [name=state]').selectOption('uncertain');assert.equal(await page.locator('[data-webhook-list]>li').count(),0);await submit('[data-webhook-filters]');await has('[data-webhook-status]','已读取');check('Webhook filter edits clear the old scope before manual query.');
  mode='bad-review';await reviewHook();await has('[data-webhook-status]','保存结果待确认');check('Webhook review requires a boolean recorded acknowledgment.');
  mode='';await submit('[data-webhook-filters]');await has('[data-webhook-status]','已读取');mode='review-read-fail';await reviewHook();await has('[data-webhook-status]','核对结论已保存，列表暂未刷新');check('Confirmed Webhook review remains saved when the following read fails.');
  await hooks();await visual('webhooks');mode='hold-hooks';await submit('[data-webhook-filters]');await wait(()=>held);await session();await release();assert.equal(await page.locator('[data-webhook-list]>li').count(),0);check('Session invalidation prevents late Webhook data from being displayed.');
  assert(writes.filter(w=>w.path.startsWith('/api/v1/admin/plugin-webhooks/')).every(w=>w.path.endsWith('/review')));assert(!scripts.some(s=>/analytics-react|echarts|react-dom/.test(s)));assert.deepEqual(errors,[]);check('No resend, unhandled page errors or React/ECharts dependency appeared.');
  await page.goto(fixture.url+'/fixture/login/reader');const denied=await page.goto(fixture.url+'/admin/plugin-records/');assert([401,403].includes(denied.status()));check('A real subscriber session cannot open administrative review pages.');
  await writeFile(resolve(directory,variant+'-'+name+'-requests.json'),JSON.stringify({variant,reads,writes,errors,scripts},null,2));
 }catch(error){await writeFile(resolve(directory,variant+'-'+name+'-failure.txt'),String(error.stack)+'\n'+await page.locator('body').innerText());await page.screenshot({path:resolve(directory,variant+'-'+name+'-failure.png'),fullPage:true});throw error;}
 finally{await browser.close();}
}
await writeFile(resolve(directory,'browser-'+variant+'.json'),JSON.stringify({variant,count:evidence.length,fixtureStore:fixture.store,checks:evidence},null,2)+'\n');
console.log(`Plugin ${variant}: ${evidence.length} browser checks passed.`);
